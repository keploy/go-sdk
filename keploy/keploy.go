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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/coverage"
	"sort"
	"strings"
	"sync"

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
)

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
	// Reason says why the test has no coverage; nil when coverage was taken.
	Reason *noCoverageReason `json:"reason,omitempty"`
}

// noCoverageReason explains a test with no coverage, so Keploy can show
// "not measured" with the cause instead of a silent gap.
type noCoverageReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Reason codes for noCoverageReason.Code.
const (
	reasonNoStart       = "no_start"       // END came with no START before it
	reasonEndMismatch   = "end_mismatch"   // END named another test than the last START
	reasonDumpFailed    = "dump_failed"    // writing the counters or meta-data failed
	reasonCovdataFailed = "covdata_failed" // converting or reading the coverage data failed
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

	processedData, err := processCoverageProfilesUsingCovdata(tempDir)
	if err != nil {
		return &reportError{reasonCovdataFailed, fmt.Errorf("failed to process coverage profiles: %w", err)}
	}

	if len(processedData) == 0 {
		log.Printf("[Agent-Warning] No covered lines were found for test %s. The report will be empty.", testID)
	}

	jsonData, err := json.Marshal(coverageRecord{ID: testID, ExecutedLinesByFile: processedData})
	if err != nil {
		return fmt.Errorf("failed to marshal coverage data to JSON: %w", err)
	}

	return sendToSocket(jsonData)
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

// processCoverageProfilesUsingCovdata uses the covdata tool to convert binary coverage data to text format
// and then processes it using the standard cover package.
func processCoverageProfilesUsingCovdata(dir string) (map[string][]int, error) {
	// Create a temporary file for the text format output
	textFile, err := os.CreateTemp(coverageDir, "coverage-*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file for text coverage: %w", err)
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
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+dir, "-o="+textFile.Name())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to convert coverage data to text format: %w\nStderr: %s", err, stderr.String())
	}

	// Get the module path (e.g., "your/module/path") to resolve file paths correctly.
	modulePathCmd := exec.Command("go", "list", "-m")
	var stderrModPath bytes.Buffer
	modulePathCmd.Stderr = &stderrModPath
	modulePathBytes, err := modulePathCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get module path with 'go list -m': %w\nStderr: %s", err, stderrModPath.String())
	}
	modulePath := strings.TrimSpace(string(modulePathBytes))

	// Get the module's root directory on the filesystem.
	moduleDirCmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	var stderrModDir bytes.Buffer
	moduleDirCmd.Stderr = &stderrModDir
	moduleDirBytes, err := moduleDirCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get module directory with 'go list -m -f {{.Dir}}': %w\nStderr: %s", err, stderrModDir.String())
	}
	moduleDir := strings.TrimSpace(string(moduleDirBytes))

	// Parse the text format using the standard cover package.
	profiles, err := cover.ParseProfiles(textFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to parse text coverage profile: %w", err)
	}

	executedLinesByFile := make(map[string][]int)

	for _, profile := range profiles {
		var absolutePath string
		if strings.HasPrefix(profile.FileName, modulePath) {
			relativePath := strings.TrimPrefix(profile.FileName, modulePath)
			absolutePath = filepath.Join(moduleDir, relativePath)
		} else if !filepath.IsAbs(profile.FileName) {
			continue
		} else {
			absolutePath = profile.FileName
		}

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
			executedLinesByFile[absolutePath] = lines
		}
	}

	return executedLinesByFile, nil
}
