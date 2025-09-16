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
	"time"

	"golang.org/x/tools/cover"
)

const (
	// controlSocketPath is used by Keploy to send commands (START/END) to the app.
	controlSocketPath = "/tmp/coverage_control.sock"
	// dataSocketPath is used by the app to send coverage data back to Keploy.
	dataSocketPath = "/tmp/coverage_data.sock"
)

var (
	// controlMu protects access to the currentTestID, ensuring command handling is atomic.
	controlMu sync.Mutex
	// currentTestID stores the ID of the test case currently being recorded.
	currentTestID string
)

// init starts the background control server that listens for commands from the Keploy test runner.
func init() {
	log.Printf("[Agent] Init: starting control server goroutine. PID=%d, PPID=%d, CWD=%s", os.Getpid(), os.Getppid(), mustGetwd())
	go startControlServer()
}

// startControlServer sets up and runs the Unix socket server that listens for commands from Keploy.
func startControlServer() {
	log.Printf("[Agent] Control: preparing socket at %s", controlSocketPath)

	if err := os.RemoveAll(controlSocketPath); err != nil {
		log.Printf("[Agent] Control: failed to remove old control socket: %v", err)
		// not returning; try to continue
	}

	ln, err := net.Listen("unix", controlSocketPath)
	if err != nil {
		log.Printf("[Agent] 🚨 FATAL: Could not start control server: %v", err)
		return
	}
	defer func() {
		log.Printf("[Agent] Control: shutting down listener")
		if err := ln.Close(); err != nil {
			log.Printf("[Agent] Control: error closing control server: %v", err)
		}
	}()

	log.Printf("[Agent] Control: listening on %s", controlSocketPath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				log.Printf("[Agent] Control: listener closed, stopping accept loop")
				break
			}
			log.Printf("[Agent] Control: error accepting connection: %v", err)
			continue
		}
		log.Printf("[Agent] Control: accepted connection from %T", conn.RemoteAddr())
		go handleControlRequest(conn)
	}
}

// handleControlRequest parses commands from Keploy ("START testID", "END testID")
func handleControlRequest(conn net.Conn) {
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("[Agent] Control: error closing connection: %v", err)
		}
	}()

	reader := bufio.NewReader(conn)
	command, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("[Agent] Control: error reading command: %v", err)
		return
	}
	command = strings.TrimSpace(command)
	log.Printf("[Agent] Control: raw command received: %q", command)

	// Split the command into action and testID
	parts := strings.SplitN(command, " ", 2)
	if len(parts) != 2 {
		log.Printf("[Agent] Control: invalid command format: %q", command)
		return
	}
	action, id := parts[0], parts[1]
	log.Printf("[Agent] Control: action=%q testID=%q", action, id)

	controlMu.Lock()
	defer controlMu.Unlock()

	switch action {
	case "START":
		log.Printf("[Agent] Control: START for testID=%q (prev=%q). Clearing coverage counters...", id, currentTestID)
		currentTestID = id
		if err := coverage.ClearCounters(); err != nil {
			log.Printf("[Agent] Control: error clearing coverage counters: %v", err)
		} else {
			log.Printf("[Agent] Control: coverage counters cleared successfully")
		}
	case "END":
		log.Printf("[Agent] Control: END for testID=%q (current=%q).", id, currentTestID)
		if currentTestID != id {
			log.Printf("[Agent] Control: WARNING mismatch END. Expected=%q, Got=%q. Skipping coverage report.", currentTestID, id)
			return
		}
		if err := reportCoverage(id); err != nil {
			log.Printf("[Agent] 🚨 Error reporting coverage for test %s: %v", id, err)
		} else {
			log.Printf("[Agent] Control: reportCoverage done for %q", id)
		}
		currentTestID = ""

		if _, err := conn.Write([]byte("ACK\n")); err != nil {
			log.Printf("[Agent] Control: error sending ACK to controller: %v", err)
		} else {
			log.Printf("[Agent] Control: ACK sent to controller")
		}
	default:
		log.Printf("[Agent] Control: unrecognized command: %s", action)
	}
}

// reportCoverage dumps, processes, and sends the coverage data.
func reportCoverage(testID string) error {
	start := time.Now()
	log.Printf("[Agent] Report: starting for testID=%q", testID)

	// Derive a stable baseID from the last path segment to avoid index errors and to work with/without slashes.
	// Examples:
	//  - "test-set-0/test-2" -> "test-2"
	//  - "test-2" -> "test-2"
	//  - "suite/a/b/test-13" -> "test-13"
	segments := strings.Split(testID, "/")
	baseID := segments[len(segments)-1]
	log.Printf("[Agent] Report: derived baseID=%q from testID=%q (segments=%v)", baseID, testID, segments)

	// Create a temporary directory to store the coverage data.
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("keploy-coverage-%s-", baseID))
	if err != nil {
		return fmt.Errorf("report: failed to create temp dir: %w", err)
	}
	log.Printf("[Agent] Report: tempDir=%s", tempDir)

	// Try to write binary counters and meta.
	if err := coverage.WriteCountersDir(tempDir); err != nil {
		return fmt.Errorf("report: failed to write coverage counters. Ensure the app was built with '-cover -covermode=atomic'. original: %w", err)
	}
	log.Printf("[Agent] Report: WriteCountersDir OK")

	if err := coverage.WriteMetaDir(tempDir); err != nil {
		return fmt.Errorf("report: failed to write meta dir: %w", err)
	}
	log.Printf("[Agent] Report: WriteMetaDir OK")

	// Quick diagnostics: list what got written
	listed := listDir(tempDir)
	log.Printf("[Agent] Report: tempDir listing -> %d entries: %v", len(listed), listed)

	// Process with covdata and then ParseProfiles
	executed, err := processCoverageProfilesUsingCovdata(tempDir)
	if err != nil {
		return fmt.Errorf("report: failed to process coverage profiles: %w", err)
	}

	// Summarize results
	totalFiles := 0
	totalLines := 0
	for f, lines := range executed {
		totalFiles++
		totalLines += len(lines)
		if len(lines) == 0 {
			log.Printf("[Agent] Report: file=%s has 0 covered lines (unexpected since it’s present)", f)
		} else {
			// For very verbose mode, dump first few lines only to keep logs manageable
			preview := lines
			if len(lines) > 20 {
				preview = lines[:20]
			}
			log.Printf("[Agent] Report: file=%s coveredLines=%d preview(first<=20)=%v", f, len(lines), preview)
		}
	}

	if totalLines == 0 {
		log.Printf("[Agent-Warning] No covered lines were found for test %s. The report will be empty. (files=%d)", testID, totalFiles)
	}

	payload := map[string]interface{}{
		"id":                  testID,
		"executedLinesByFile": executed,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("report: failed to marshal coverage data to JSON: %w", err)
	}
	log.Printf("[Agent] Report: payload bytes=%d, files=%d, totalCoveredLines=%d", len(jsonData), totalFiles, totalLines)

	// Send to data socket
	if err := sendToSocket(jsonData); err != nil {
		return fmt.Errorf("report: failed sending to data socket: %w", err)
	}
	log.Printf("[Agent] Report: sendToSocket OK in %s", time.Since(start))
	return nil
}

// sendToSocket connects to the Keploy data socket and writes the JSON payload.
func sendToSocket(data []byte) error {
	log.Printf("[Agent] DataSock: dialing %s", dataSocketPath)
	conn, err := net.Dial("unix", dataSocketPath)
	if err != nil {
		return fmt.Errorf("could not connect to keploy data socket at %s: %w", dataSocketPath, err)
	}
	defer func() {
		log.Printf("[Agent] DataSock: closing connection")
		if err := conn.Close(); err != nil {
			log.Printf("[Agent] DataSock: error closing connection: %v", err)
		}
	}()

	n, err := conn.Write(data)
	log.Printf("[Agent] DataSock: wrote %d bytes (err=%v)", n, err)
	return err
}

// processCoverageProfilesUsingCovdata uses the covdata tool to convert binary coverage data to text format
// and then processes it using the standard cover package.
func processCoverageProfilesUsingCovdata(dir string) (map[string][]int, error) {
	log.Printf("[Agent] Covdata: begin. dir=%s", dir)

	// Log go version for diagnostics
	if out, err := exec.Command("go", "version").CombinedOutput(); err != nil {
		log.Printf("[Agent] Covdata: 'go version' failed: %v (out=%q)", err, string(out))
	} else {
		log.Printf("[Agent] Covdata: %s", strings.TrimSpace(string(out)))
	}

	// Create a temporary file for the text format output
	textFile, err := os.CreateTemp("", "coverage-*.txt")
	if err != nil {
		return nil, fmt.Errorf("covdata: failed to create temp file for text coverage: %w", err)
	}
	log.Printf("[Agent] Covdata: text output file=%s", textFile.Name())

	defer func() {
		log.Printf("[Agent] Covdata: closing text file")
		if err := textFile.Close(); err != nil {
			log.Printf("[Agent] Covdata: error closing temp file: %v", err)
		}
		log.Printf("[Agent] Covdata: removing text file %s", textFile.Name())
		if err := os.Remove(textFile.Name()); err != nil {
			log.Printf("[Agent] Covdata: error removing temp file: %v", err)
		}
	}()

	// Use covdata to convert binary format to text format
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+dir, "-o="+textFile.Name())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	log.Printf("[Agent] Covdata: running %q", strings.Join(cmd.Args, " "))
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("covdata: failed to convert coverage data to text format: %w; stderr: %s", err, stderr.String())
	}
	if s := strings.TrimSpace(stderr.String()); s != "" {
		log.Printf("[Agent] Covdata: warnings/stderr: %s", s)
	} else {
		log.Printf("[Agent] Covdata: textfmt OK; stderr empty")
	}

	// Get the module path (e.g., "your/module/path") to resolve file paths correctly.
	modulePathCmd := exec.Command("go", "list", "-m")
	var stderrModPath bytes.Buffer
	modulePathCmd.Stderr = &stderrModPath
	modulePathBytes, err := modulePathCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("covdata: failed to get module path with 'go list -m': %w; stderr: %s", err, stderrModPath.String())
	}
	modulePath := strings.TrimSpace(string(modulePathBytes))
	log.Printf("[Agent] Covdata: modulePath=%q", modulePath)

	// Get the module's root directory on the filesystem.
	moduleDirCmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	var stderrModDir bytes.Buffer
	moduleDirCmd.Stderr = &stderrModDir
	moduleDirBytes, err := moduleDirCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("covdata: failed to get module directory with 'go list -m -f {{.Dir}}': %w; stderr: %s", err, stderrModDir.String())
	}
	moduleDir := strings.TrimSpace(string(moduleDirBytes))
	log.Printf("[Agent] Covdata: moduleDir=%q", moduleDir)

	// Parse the text format using the standard cover package.
	log.Printf("[Agent] Covdata: parsing profiles from %s", textFile.Name())
	profiles, err := cover.ParseProfiles(textFile.Name())
	if err != nil {
		return nil, fmt.Errorf("covdata: failed to parse text coverage profile: %w", err)
	}
	log.Printf("[Agent] Covdata: parsed %d profiles", len(profiles))

	executedLinesByFile := make(map[string][]int)

	for i, profile := range profiles {
		log.Printf("[Agent] Covdata: profile[%d] fileName=%q blocks=%d", i, profile.FileName, len(profile.Blocks))

		var absolutePath string
		switch {
		case strings.HasPrefix(profile.FileName, modulePath):
			relativePath := strings.TrimPrefix(profile.FileName, modulePath)
			absolutePath = filepath.Join(moduleDir, relativePath)
			log.Printf("[Agent] Covdata: resolved via modulePath => %s", absolutePath)
		case !filepath.IsAbs(profile.FileName):
			log.Printf("[Agent] Covdata: skipping non-absolute non-module path file=%q", profile.FileName)
			continue
		default:
			absolutePath = profile.FileName
			log.Printf("[Agent] Covdata: absolute path taken => %s", absolutePath)
		}

		lineSet := make(map[int]bool)
		totalPositive := 0

		// For each block in the profile, if the count is greater than 0, add the lines to the map.
		for j, block := range profile.Blocks {
			if block.Count <= 0 {
				// This block wasn't executed
				continue
			}
			totalPositive++
			for line := block.StartLine; line <= block.EndLine; line++ {
				lineSet[line] = true
			}
			if j < 5 { // limit per-profile spam
				log.Printf("[Agent] Covdata:   block[%d] start=%d end=%d count=%d", j, block.StartLine, block.EndLine, block.Count)
			}
		}

		if len(lineSet) == 0 {
			log.Printf("[Agent] Covdata: file=%s has 0 executed lines; positiveBlocks=%d (might indicate counters not flushed or build flags missing)", absolutePath, totalPositive)
			continue
		}

		lines := make([]int, 0, len(lineSet))
		for line := range lineSet {
			lines = append(lines, line)
		}
		sort.Ints(lines)
		executedLinesByFile[absolutePath] = lines
		log.Printf("[Agent] Covdata: file=%s executedLines=%d", absolutePath, len(lines))
	}

	log.Printf("[Agent] Covdata: executed files=%d", len(executedLinesByFile))
	return executedLinesByFile, nil
}

// helpers

func listDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{fmt.Sprintf("ERR: %v", err)}
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			out = append(out, fmt.Sprintf("%s (err: %v)", name, err))
			continue
		}
		out = append(out, fmt.Sprintf("%s size=%d mode=%s", full, info.Size(), info.Mode()))
	}
	return out
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Sprintf("ERR:%v", err)
	}
	return wd
}
