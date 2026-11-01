// To activate, simply import this package for its side effects:
//
//	import _ "github.com/keploy/go-sdk/v3/keploy"
//
// Then, build your application with atomic coverage instrumentation:
//
//	go build -cover -covermode=atomic -o your-app . (-cover and -covermode=atomic is required as per https://pkg.go.dev/runtime/coverage@go1.25rc2#ClearCounters)
package keploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/coverage"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/tools/cover"
)

const (
	// coverageDirEnv names the folder Keploy shares with the app for the two
	// sockets and the coverage temp files. In k8s the app sees the shared folder
	// at /keploy-sockets, so Keploy sets it there. Unset keeps the old behaviour:
	// sockets in /tmp, temp files in the OS default temp folder.
	coverageDirEnv = "KEPLOY_COVERAGE_DIR"
	// defaultSocketDir holds the sockets when coverageDirEnv is unset.
	defaultSocketDir = "/tmp"
	// covdataTimeout bounds one covdata run. Keploy waits on END with no
	// deadline, so a hung covdata would stall the whole replay.
	covdataTimeout = 30 * time.Second
)

// errCovdataMissing means neither Keploy's covdata nor the go tool is there.
var errCovdataMissing = errors.New("no covdata tool: Keploy's keploy-covdata was not found and 'go' is not on PATH")

var (
	// coverageDir is the value of coverageDirEnv, or "" when it is unset.
	coverageDir = strings.TrimSpace(os.Getenv(coverageDirEnv))
	// controlSocketPath is used by Keploy to send commands (START/END) to the app.
	controlSocketPath = filepath.Join(socketDir(coverageDir), "coverage_control.sock")
	// dataSocketPath is used by the app to send coverage data back to Keploy.
	dataSocketPath = filepath.Join(socketDir(coverageDir), "coverage_data.sock")
)

// socketDir returns the folder that holds the sockets: dir, or /tmp when dir is
// empty.
func socketDir(dir string) string {
	if dir == "" {
		return defaultSocketDir
	}
	return dir
}

// coverageRecord is what the app sends on the data socket for one test.
type coverageRecord struct {
	ID                  string           `json:"id"`
	ExecutedLinesByFile map[string][]int `json:"executedLinesByFile"`
	// ExecutedBlocksByFile is file -> the blocks that ran, with columns. It is
	// sent next to ExecutedLinesByFile, which older agents still need.
	ExecutedBlocksByFile map[string][]codeBlock `json:"executedBlocksByFile,omitempty"`
	// Totals are Go's own statement counts for the main module.
	Totals *statementTotals `json:"totals,omitempty"`
	// MetaHash names the build's coverage meta-data (the covmeta.<hash> file).
	MetaHash string `json:"metaHash,omitempty"`
	// BuildBlocks is file -> every block of the build, run or not. It goes with
	// the first record of each test set only, since it is the same for every
	// test of one build.
	BuildBlocks map[string][]codeBlock `json:"buildBlocks,omitempty"`
	// Reason says why the test has no coverage; nil when coverage was taken.
	Reason *noCoverageReason `json:"reason,omitempty"`
}

// codeBlock is one Go coverage block as
// [startLine, startCol, endLine, endCol, numStmt].
type codeBlock [5]int

// statementTotals are statement counts over the main module's blocks.
type statementTotals struct {
	Statements        int `json:"statements"`
	CoveredStatements int `json:"coveredStatements"`
}

// noCoverageReason explains a test with no coverage, so Keploy can show
// "not measured" with the cause instead of a silent gap.
type noCoverageReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Reason codes for noCoverageReason.Code.
const (
	reasonNoStart        = "no_start"        // END came with no START before it
	reasonEndMismatch    = "end_mismatch"    // END named another test than the last START
	reasonDumpFailed     = "dump_failed"     // writing the counters or meta-data failed
	reasonCovdataFailed  = "covdata_failed"  // converting or reading the coverage data failed
	reasonCovdataMissing = "covdata_missing" // no covdata tool to convert the data with
)

// reportError carries the reason code of an error from reportCoverage.
type reportError struct {
	code string
	err  error
}

func (e *reportError) Error() string { return e.err.Error() }
func (e *reportError) Unwrap() error { return e.err }

var (
	// controlMu protects access to the currentTestID, ensuring command handling is atomic.
	controlMu sync.Mutex
	// currentTestID stores the ID of the test case currently being recorded.
	currentTestID string
	// buildTableSent and buildTableSentForSet record which test set's first
	// record already carried the build's block table.
	buildTableSent       bool
	buildTableSentForSet string
	// useGoCovdata is set once Keploy's covdata could not start, so later
	// tests go straight to 'go tool covdata'.
	useGoCovdata bool
)

// init starts the background control server that listens for commands from the Keploy test runner.
func init() {
	go startControlServer()
}

// startControlServer sets up and runs the Unix socket server that listens for commands from Keploy.
func startControlServer() {
	err := os.RemoveAll(controlSocketPath)
	if err != nil {
		log.Printf("[Agent] Failed to remove old control socket: %v", err)
		return
	}

	ln, err := net.Listen("unix", controlSocketPath)
	if err != nil {
		log.Printf("[Agent] 🚨 FATAL: Could not start control server: %v", err)
		return
	}
	defer func() {
		if err := ln.Close(); err != nil {
			log.Printf("[Agent] Error closing control server: %v", err)
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
			log.Printf("[Agent] Error accepting connection: %v", err)
			continue
		}
		go handleControlRequest(conn)
	}
}

// handleControlRequest parses commands from Keploy ("START testID", "END testID")
func handleControlRequest(conn net.Conn) {
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("[Agent] Error closing connection: %v", err)
		}
	}()

	command, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		log.Printf("[Agent] Error reading command: %v", err)
		return
	}

	// Split the command into action and testID
	parts := strings.SplitN(strings.TrimSpace(command), " ", 2)
	if len(parts) != 2 {
		log.Printf("[Agent] Invalid command format: '%s'", command)
		return
	}
	action, id := parts[0], parts[1]

	controlMu.Lock()
	defer controlMu.Unlock()

	switch action {
	case "START":
		currentTestID = id
		err := coverage.ClearCounters()
		if err != nil {
			log.Printf("[Agent] Error clearing coverage counters: %v", err)
		}
	case "END":
		if currentTestID != id {
			log.Printf("[Agent] Warning: Mismatched END command. Expected '%s', got '%s'. Skipping coverage report to avoid inconsistent state.", currentTestID, id)
			code := reasonEndMismatch
			if currentTestID == "" {
				code = reasonNoStart
			}
			sendReason(id, code, fmt.Sprintf("END for %q, but the last START was for %q", id, currentTestID))
			ack(conn)
			return
		}
		err := reportCoverage(id)
		if err != nil {
			log.Printf("[Agent] 🚨 Error reporting coverage for test %s: %v", id, err)
			var re *reportError
			if errors.As(err, &re) {
				sendReason(id, re.code, re.Error())
			}
		}
		// Reset the currentTestID to an empty string to indicate that no test is currently being recorded.
		currentTestID = ""

		ack(conn)
	default:
		log.Printf("[Agent] Unrecognized command: %s", action)
	}
}

// reportCoverage dumps, processes, and sends the coverage data.
func reportCoverage(testID string) error {
	// Create a temporary directory to store the coverage data.
	tempDir, err := os.MkdirTemp(coverageDir, tempDirPattern(testID))
	if err != nil {
		return &reportError{reasonDumpFailed, fmt.Errorf("failed to create temp dir: %w", err)}
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			log.Printf("[Agent] Error removing temp dir: %v", err)
		}
	}()

	err = coverage.WriteCountersDir(tempDir)
	if err != nil {
		return &reportError{reasonDumpFailed, fmt.Errorf("failed to write coverage counters. Ensure the application was built with '-cover -covermode=atomic'. Original error: %w", err)}
	}

	err = coverage.WriteMetaDir(tempDir)
	if err != nil {
		return &reportError{reasonDumpFailed, fmt.Errorf("failed to write meta dir: %w", err)}
	}

	processed, err := processCoverageProfilesUsingCovdata(tempDir)
	if err != nil {
		code := reasonCovdataFailed
		if errors.Is(err, errCovdataMissing) {
			code = reasonCovdataMissing
		}
		return &reportError{code, fmt.Errorf("failed to process coverage profiles: %w", err)}
	}

	if len(processed.executedLines) == 0 {
		log.Printf("[Agent-Warning] No covered lines were found for test %s. The report will be empty.", testID)
	}

	set := testSetOf(testID)
	withTable := needsBuildTable(set)
	record := buildRecord(testID, processed, metaHash(tempDir), withTable)

	jsonData, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal coverage data to JSON: %w", err)
	}

	if err := sendToSocket(jsonData); err != nil {
		return err
	}
	if withTable {
		buildTableSent, buildTableSentForSet = true, set
	}
	return nil
}

// buildRecord assembles the record for one test: the blocks that ran, and
// every block of the build when withTable is set.
func buildRecord(testID string, processed processedCoverage, hash string, withTable bool) coverageRecord {
	totals := processed.totals()
	record := coverageRecord{
		ID:                   testID,
		ExecutedLinesByFile:  processed.executedLines,
		ExecutedBlocksByFile: processed.codeBlocks(true),
		Totals:               &totals,
		MetaHash:             hash,
	}
	if withTable {
		record.BuildBlocks = processed.codeBlocks(false)
	}
	return record
}

// needsBuildTable reports whether the record for a test of set must carry the
// build's block table: no record carried it yet, or the last one that did was
// for another set. The caller holds controlMu.
func needsBuildTable(set string) bool {
	return !buildTableSent || set != buildTableSentForSet
}

// testSetOf returns the test set part of a test ID ("test-set-0" from
// "test-set-0/test-2"), or "" when there is no slash.
func testSetOf(testID string) string {
	if set, _, ok := strings.Cut(testID, "/"); ok {
		return set
	}
	return ""
}

// metaHash returns the hash from the covmeta.<hash> file WriteMetaDir wrote to
// dir, or "" when there is none.
func metaHash(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if hash, ok := strings.CutPrefix(e.Name(), "covmeta."); ok {
			return hash
		}
	}
	return ""
}

// tempDirPattern builds the os.MkdirTemp pattern for a test ID. It takes the
// part after the first slash, e.g. "test-2" from "test-set-0/test-2", or the
// whole ID when there is no slash. Path separators left in the name are
// replaced, because MkdirTemp rejects a pattern that contains one.
func tempDirPattern(testID string) string {
	_, name, found := strings.Cut(testID, "/")
	if !found {
		name = testID
	}
	name = strings.NewReplacer("/", "_", `\`, "_").Replace(name)
	return fmt.Sprintf("keploy-coverage-%s-", name)
}

// sendReason tells Keploy why a test has no coverage. A failure is only
// logged; the test is then missing, as it was before reasons existed.
func sendReason(testID, code, message string) {
	data, err := json.Marshal(coverageRecord{
		ID:                  testID,
		ExecutedLinesByFile: map[string][]int{},
		Reason:              &noCoverageReason{Code: code, Message: message},
	})
	if err != nil {
		log.Printf("[Agent] Error encoding the no-coverage reason for test %s: %v", testID, err)
		return
	}
	if err := sendToSocket(data); err != nil {
		log.Printf("[Agent] Error sending the no-coverage reason for test %s: %v", testID, err)
	}
}

// ack tells Keploy that the END command is done.
func ack(conn net.Conn) {
	if _, err := conn.Write([]byte("ACK\n")); err != nil {
		log.Printf("[Agent] Error sending ACK to controller: %v", err)
	}
}

// sendToSocket connects to the Keploy data socket and writes the JSON payload.
func sendToSocket(data []byte) error {
	conn, err := net.Dial("unix", dataSocketPath)
	if err != nil {
		return fmt.Errorf("could not connect to keploy data socket at %s: %w", dataSocketPath, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("[Agent] Error closing connection: %v", err)
		}
	}()

	_, err = conn.Write(data)
	return err
}

// covdataPath is where Keploy's agent drops a covdata binary for this arch.
func covdataPath() string {
	return filepath.Join(socketDir(coverageDir), "keploy-covdata-"+runtime.GOARCH)
}

// covdataCommand returns the command that converts the binary coverage data in
// dir to text in out. It prefers the covdata Keploy delivers, so the app image
// needs no Go toolchain, and falls back to 'go tool covdata' when it is missing
// or could not start before.
func covdataCommand(ctx context.Context, dir, out string) (*exec.Cmd, error) {
	args := []string{"textfmt", "-i=" + dir, "-o=" + out}
	if info, err := os.Stat(covdataPath()); err == nil && info.Mode().IsRegular() && !useGoCovdata {
		return exec.CommandContext(ctx, covdataPath(), args...), nil
	}
	if _, err := exec.LookPath("go"); err != nil {
		return nil, errCovdataMissing
	}
	return exec.CommandContext(ctx, "go", append([]string{"tool", "covdata"}, args...)...), nil
}

// processedCoverage is one test's coverage, read from a covdata text profile.
// Only files of the main module are kept (see fileKey).
type processedCoverage struct {
	// executedLines is file -> sorted lines of the blocks that ran.
	executedLines map[string][]int
	// blocks is file -> every block of the file, run or not, with its count.
	blocks map[string][]cover.ProfileBlock
}

// codeBlocks returns file -> the blocks that ran (onlyRun) or every block.
func (p processedCoverage) codeBlocks(onlyRun bool) map[string][]codeBlock {
	out := make(map[string][]codeBlock)
	for file, blocks := range p.blocks {
		for _, b := range blocks {
			if onlyRun && b.Count <= 0 {
				continue
			}
			out[file] = append(out[file], codeBlock{b.StartLine, b.StartCol, b.EndLine, b.EndCol, b.NumStmt})
		}
	}
	return out
}

// totals sums the statements of every block, and of the blocks that ran.
func (p processedCoverage) totals() statementTotals {
	var t statementTotals
	for _, blocks := range p.blocks {
		for _, b := range blocks {
			t.Statements += b.NumStmt
			if b.Count > 0 {
				t.CoveredStatements += b.NumStmt
			}
		}
	}
	return t
}

// processCoverageProfilesUsingCovdata uses the covdata tool to convert binary coverage data to text format
// and then processes it using the standard cover package.
func processCoverageProfilesUsingCovdata(dir string) (processedCoverage, error) {
	// Create a temporary file for the text format output
	textFile, err := os.CreateTemp(coverageDir, "coverage-*.txt")
	if err != nil {
		return processedCoverage{}, fmt.Errorf("failed to create temp file for text coverage: %w", err)
	}
	defer func() {
		if err := os.Remove(textFile.Name()); err != nil {
			log.Printf("[Agent] Error removing temp file: %v", err)
		}
	}()

	defer func() {
		if err := textFile.Close(); err != nil {
			log.Printf("[Agent] Error closing temp file: %v", err)
		}
	}()

	// Use covdata to convert binary format to text format
	ctx, cancel := context.WithTimeout(context.Background(), covdataTimeout)
	defer cancel()
	cmd, err := covdataCommand(ctx, dir, textFile.Name())
	if err != nil {
		return processedCoverage{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err = cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && cmd.Path == covdataPath() && !errors.As(err, &exitErr) {
		// Keploy's covdata is there but could not start, e.g. its folder is
		// mounted noexec. Use 'go tool covdata' instead, now and from then on.
		// A covdata that ran and exited non-zero is not retried.
		if _, lookErr := exec.LookPath("go"); lookErr == nil {
			log.Printf("[Agent-Warning] Keploy's covdata could not start (%v). Using 'go tool covdata' from now on.", err)
			useGoCovdata = true
			cmd, err = covdataCommand(ctx, dir, textFile.Name())
			if err != nil {
				return processedCoverage{}, err
			}
			cmd.Stderr = &stderr
			err = cmd.Run()
		}
	}
	if err != nil {
		return processedCoverage{}, fmt.Errorf("failed to convert coverage data to text format: %w\nStderr: %s", err, stderr.String())
	}

	// The main module path (e.g., "your/module/path") from the binary itself, so
	// no Go toolchain or source tree is needed in the app's image.
	return parseProfiles(textFile.Name(), mainModulePath())
}

// parseProfiles reads a covdata text profile into a processedCoverage.
func parseProfiles(path, modulePath string) (processedCoverage, error) {
	// Parse the text format using the standard cover package.
	profiles, err := cover.ParseProfiles(path)
	if err != nil {
		return processedCoverage{}, fmt.Errorf("failed to parse text coverage profile: %w", err)
	}

	executedLinesByFile := make(map[string][]int)
	blocksByFile := make(map[string][]cover.ProfileBlock)

	for _, profile := range profiles {
		key, ok := fileKey(profile.FileName, modulePath)
		if !ok {
			continue
		}
		blocksByFile[key] = profile.Blocks

		lineSet := make(map[int]bool)

		// For each block in the profile, if the count is greater than 0, add the lines to the map.
		for _, block := range profile.Blocks {
			if block.Count <= 0 {
				continue
			}
			for line := block.StartLine; line <= block.EndLine; line++ {
				lineSet[line] = true
			}
		}

		// If there are any lines executed, add them to the map.
		if len(lineSet) > 0 {
			lines := make([]int, 0, len(lineSet))
			for line := range lineSet {
				lines = append(lines, line)
			}
			sort.Ints(lines)
			executedLinesByFile[key] = lines
		}
	}

	return processedCoverage{executedLines: executedLinesByFile, blocks: blocksByFile}, nil
}

// mainModulePath returns the app's main module path from its build info, e.g.
// "example.com/app", or "" when the binary carries no build info.
func mainModulePath() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Path
}

// fileKey turns a file name from the coverage profile into the key Keploy
// stores. A main-module file becomes module-relative ("handlers/user.go"), the
// same on every machine. An absolute name, which Go uses for files built
// outside a module (go build -cover main.go), is kept as it is. A file of any
// other module is skipped (ok is false).
func fileKey(fileName, modulePath string) (key string, ok bool) {
	if modulePath != "" && strings.HasPrefix(fileName, modulePath+"/") {
		return strings.TrimPrefix(fileName, modulePath+"/"), true
	}
	if filepath.IsAbs(fileName) {
		return fileName, true
	}
	return "", false
}
