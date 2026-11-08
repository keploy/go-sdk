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

	"golang.org/x/tools/cover"
)

const (
	// controlSocketPath is used by Keploy to send commands (START/END) to the app.
	controlSocketPath = "/tmp/coverage_control.sock"
	// dataSocketPath is used by the app to send coverage data back to Keploy.
	dataSocketPath = "/tmp/coverage_data.sock"
)

// The SDK's answers on the control socket: one line for every command it
// reads, written before it closes the connection. ACK says it took the command:
// it reset the counters for a START, or published the test's coverage for an
// END. IGNORED says it read the command and did not take it. proto=2 says this
// SDK answers every command it reads, so Keploy can read a close with no answer
// as a command not taken: on macOS a connection closed with the command unread
// gives Keploy no error, only the close.
const (
	replyTaken   = "ACK proto=2\n"
	replyIgnored = "IGNORED proto=2\n"
)

var (
	// controlMu protects access to the currentTestID, ensuring command handling is atomic.
	controlMu sync.Mutex
	// currentTestID stores the ID of the test case currently being recorded.
	currentTestID string

	// clearCounters and publishCoverage reset the counters at a START and
	// publish a test's coverage at an END. Variables for tests.
	clearCounters   = coverage.ClearCounters
	publishCoverage = reportCoverage
)

// init binds the control socket before the app's main runs, so the socket is
// there before the app can say it is ready: bound on a goroutine, the bind
// could come after the app's port, and the first test's START then found no
// one listening and its coverage was lost. It then serves Keploy's commands on
// a goroutine.
func init() {
	if err := startControlServer(controlSocketPath); err != nil {
		log.Printf("[Agent] 🚨 FATAL: Could not start control server: %v", err)
	}
}

// startControlServer binds the Unix socket at path, replacing a stale one, and
// returns once it is bound, serving Keploy's commands on it on a goroutine.
func startControlServer(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("failed to remove old control socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	go serveControl(ln)
	return nil
}

// serveControl handles each connection on ln until ln is closed.
func serveControl(ln net.Listener) {
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

// handleControlRequest reads one command from Keploy ("START testID", "END
// testID"), runs it, and answers it (replyTaken, replyIgnored) before it
// closes the connection.
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

	reply := replyIgnored
	if runCommand(strings.TrimSpace(command)) {
		reply = replyTaken
	}
	if _, err := conn.Write([]byte(reply)); err != nil {
		log.Printf("[Agent] Error answering %q to controller: %v", strings.TrimSpace(reply), err)
	}
}

// runCommand runs one command and reports whether it took it.
func runCommand(command string) bool {
	// Split the command into action and testID
	parts := strings.SplitN(command, " ", 2)
	if len(parts) != 2 {
		log.Printf("[Agent] Invalid command format: '%s'", command)
		return false
	}
	action, id := parts[0], parts[1]

	controlMu.Lock()
	defer controlMu.Unlock()

	switch action {
	case "START":
		// A START whose counters were not reset is not taken: the test's
		// coverage would hold the lines of the tests before it.
		if err := clearCounters(); err != nil {
			currentTestID = ""
			log.Printf("[Agent] Error clearing coverage counters: %v", err)
			return false
		}
		currentTestID = id
		return true
	case "END":
		if currentTestID != id {
			log.Printf("[Agent] Warning: Mismatched END command. Expected '%s', got '%s'. Skipping coverage report to avoid inconsistent state.", currentTestID, id)
			return false
		}
		// Reset the currentTestID to an empty string to indicate that no test is currently being recorded.
		currentTestID = ""
		if err := publishCoverage(id); err != nil {
			log.Printf("[Agent] 🚨 Error reporting coverage for test %s: %v", id, err)
			return false
		}
		return true
	default:
		log.Printf("[Agent] Unrecognized command: %s", action)
		return false
	}
}

// reportCoverage dumps, processes, and sends the coverage data.
func reportCoverage(testID string) error {
	// Only take the part before the first slash,
	// e.g. "test-2" from "test-set-0/test-2"
	parts := strings.SplitN(testID, "/", 2)
	baseID := parts[1]

	// Create a temporary directory to store the coverage data.
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("keploy-coverage-%s-", baseID))
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			log.Printf("[Agent] Error removing temp dir: %v", err)
		}
	}()

	err = coverage.WriteCountersDir(tempDir)
	if err != nil {
		return fmt.Errorf("failed to write coverage counters. Ensure the application was built with '-cover -covermode=atomic'. Original error: %w", err)
	}

	err = coverage.WriteMetaDir(tempDir)
	if err != nil {
		return fmt.Errorf("failed to write meta dir: %w", err)
	}

	processedData, err := processCoverageProfilesUsingCovdata(tempDir)
	if err != nil {
		return fmt.Errorf("failed to process coverage profiles: %w", err)
	}

	if len(processedData) == 0 {
		log.Printf("[Agent-Warning] No covered lines were found for test %s. The report will be empty.", testID)
	}

	payload := map[string]interface{}{
		"id":                  testID,
		"executedLinesByFile": processedData,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal coverage data to JSON: %w", err)
	}

	return sendToSocket(jsonData)
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
	textFile, err := os.CreateTemp("", "coverage-*.txt")
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
