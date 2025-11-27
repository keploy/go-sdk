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
	"runtime"
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

var (
	// controlMu protects access to the currentTestID, ensuring command handling is atomic.
	controlMu sync.Mutex
	// currentTestID stores the ID of the test case currently being recorded.
	currentTestID string
)

// init starts the background control server that listens for commands from the Keploy test runner.
func init() {
	log.Printf("[Agent-Init] Starting control server. GOOS=%s GOARCH=%s PID=%d", runtime.GOOS, runtime.GOARCH, os.Getpid())
	go startControlServer()
}

// startControlServer sets up and runs the Unix socket server that listens for commands from Keploy.
func startControlServer() {
	err := os.RemoveAll(controlSocketPath)
	if err != nil {
		log.Printf("[Agent] Failed to remove old control socket: %v", err)
		return
	}
	log.Printf("[Agent-Init] Control socket cleared. Path=%s", controlSocketPath)
	ln, err := net.Listen("unix", controlSocketPath)
	if err != nil {
		log.Printf("[Agent] 🚨 FATAL: Could not start control server: %v", err)
		return
	}
	log.Printf("[Agent] Control server listening on %s (cwd=%s)", controlSocketPath, mustGetwd())

	defer func() {
		if err := ln.Close(); err != nil {
			log.Printf("[Agent] Error closing control server: %v", err)
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				log.Printf("[Agent] Listener closed, exiting control loop")
				break
			}
			log.Printf("[Agent] Error accepting connection: %v", err)
			continue
		}
		log.Printf("[Agent-Debug] New controller connection from local process")
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
		log.Printf("[Agent-Debug] Error reading command: %v", err)
		return
	}
	log.Printf("[Agent-Debug] Received raw command: '%s'", strings.TrimSpace(command))

	// Split the command into action and testID
	parts := strings.SplitN(strings.TrimSpace(command), " ", 2)
	log.Printf("[Agent-Debug] Command parts len=%d; parts=%q", len(parts), parts)
	if len(parts) != 2 {
		log.Printf("[Agent-Debug] Invalid command format: '%s' (expected: '<ACTION> <ID>')", command)
		return
	}
	action, id := parts[0], parts[1]
	log.Printf("[Agent-Debug] Parsed command. Action='%s', ID='%s'", action, id)

	controlMu.Lock()
	defer controlMu.Unlock()

	switch action {
	case "START":
		log.Printf("[Agent-Debug] Handling START for test ID='%s' (prev currentTestID='%s')", id, currentTestID)
		currentTestID = id
		err := coverage.ClearCounters()
		if err != nil {
			log.Printf("[Agent-Debug] Error clearing coverage counters: %v", err)
		} else {
			log.Printf("[Agent-Debug] Cleared coverage counters for test ID='%s'", id)
		}
	case "END":
		log.Printf("[Agent-Debug] Handling END for test ID='%s' (currentTestID='%s')", id, currentTestID)
		if currentTestID != id {
			log.Printf("[Agent-Debug] Warning: Mismatched END. Expected '%s', got '%s'. Skipping coverage report.", currentTestID, id)
			return
		}
		err := reportCoverage(id)
		if err != nil {
			log.Printf("[Agent] 🚨 Error reporting coverage for test %s: %v", id, err)
		} else {
			log.Printf("[Agent-Debug] Coverage report initiated for test ID='%s'", id)
		}
		log.Printf("[Agent-Debug] Resetting currentTestID from '%s' to ''", currentTestID)
		currentTestID = ""

		_, err = conn.Write([]byte("ACK\n"))
		if err != nil {
			log.Printf("[Agent-Debug] Error sending ACK to controller: %v", err)
		}
	default:
		log.Printf("[Agent-Debug] Unrecognized command: %s", action)
	}
}

// reportCoverage dumps, processes, and sends the coverage data.
func reportCoverage(testID string) error {
	log.Printf("[Agent-Debug] reportCoverage start. testID='%s'", testID)

	// Only take the part before the first slash,
	// e.g. "test-2" from "test-set-0/test-2"
	parts := strings.SplitN(testID, "/", 2)
	log.Printf("[Agent-Debug] testID split parts len=%d; parts=%q", len(parts), parts)
	baseID := parts[1] // NOTE: current behavior; will panic if len(parts)<2
	log.Printf("[Agent-Debug] baseID derived='%s' (NOTE: uses parts[1])", baseID)

	// Create a temporary directory to store the coverage data.
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("keploy-coverage-%s-", baseID))
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	log.Printf("[Agent-Debug] Temp coverage dir: %s", tempDir)
	// defer func() {
	// 	if err := os.RemoveAll(tempDir); err != nil {
	// 		log.Printf("[Agent] Error removing temp dir: %v", err)
	// 	}
	// }()

	err = coverage.WriteCountersDir(tempDir)
	if err != nil {
		return fmt.Errorf("failed to write coverage counters. Ensure the application was built with '-cover -covermode=atomic'. Original error: %w", err)
	}
	log.Printf("[Agent-Debug] Wrote coverage counters -> %s", tempDir)

	err = coverage.WriteMetaDir(tempDir)
	if err != nil {
		return fmt.Errorf("failed to write meta dir: %w", err)
	}
	log.Printf("[Agent-Debug] Wrote meta data -> %s", tempDir)

	log.Printf("[Agent-Debug] Processing coverage profiles from dir: %s", tempDir)
	processedData, err := processCoverageProfilesUsingCovdata(tempDir)
	if err != nil {
		return fmt.Errorf("failed to process coverage profiles: %w", err)
	}
	log.Printf("[Agent-Debug] Finished processing profiles. Files with coverage=%d", len(processedData))

	if len(processedData) == 0 {
		log.Printf("[Agent-Warning] No covered lines found for test %s. The report will be empty.", testID)
	}

	payload := map[string]interface{}{
		"id":                  testID,
		"executedLinesByFile": processedData,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal coverage data to JSON: %w", err)
	}
	log.Printf("[Agent-Debug] JSON payload size=%d bytes for testID='%s'", len(jsonData), testID)

	log.Printf("[Agent-Debug] Sending coverage JSON to data socket: %s", dataSocketPath)
	err = sendToSocket(jsonData)
	if err != nil {
		log.Printf("[Agent-Debug] Error sending data to socket: %v", err)
		return err
	}
	log.Printf("[Agent-Debug] Successfully sent coverage JSON for testID='%s'", testID)
	return nil
}

// sendToSocket connects to the Keploy data socket and writes the JSON payload.
func sendToSocket(data []byte) error {
	conn, err := net.Dial("unix", dataSocketPath)
	if err != nil {
		return fmt.Errorf("could not connect to keploy data socket at %s: %w", dataSocketPath, err)
	}
	log.Printf("[Agent-Debug] Connected to data socket: %s", dataSocketPath)
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("[Agent] Error closing data socket connection: %v", err)
		}
	}()

	n, err := conn.Write(data)
	log.Printf("[Agent-Debug] Wrote %d bytes to data socket (err=%v)", n, err)
	return err
}

// processCoverageProfilesUsingCovdata uses the covdata tool to convert binary coverage data to text format
// and then processes it using the standard cover package.
func processCoverageProfilesUsingCovdata(dir string) (map[string][]int, error) {
	log.Printf("[Agent-Debug] processCoverageProfilesUsingCovdata(dir=%s) cwd=%s", dir, mustGetwd())
	log.Printf("[Agent-Debug] Env: GOMOD=%q GOPATH=%q GOROOT=%q PATH(head)=%q",
		os.Getenv("GOMOD"), os.Getenv("GOPATH"), os.Getenv("GOROOT"), headPath(os.Getenv("PATH")))

	// Create a temporary file for the text format output
	textFile, err := os.CreateTemp("", "coverage-*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file for text coverage: %w", err)
	}
	log.Printf("[Agent-Debug] Created temporary text file: '%s'", textFile.Name())
	defer func() {
		if err := os.Remove(textFile.Name()); err != nil {
			log.Printf("[Agent] Error removing temp file: %v", err)
		}
		log.Printf("[Agent-Debug] Removed temporary text file: '%s'", textFile.Name())
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
	log.Printf("[Agent-Debug] Executing: %s (cwd=%s)", cmd.String(), mustGetwd())

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to convert coverage data to text format: %w\nStderr: %s", err, stderr.String())
	}
	log.Printf("[Agent-Debug] 'go tool covdata' ok. stderr=%q", stderr.String())

	// Check the size of the output file
	fileInfo, err := textFile.Stat()
	if err != nil {
		log.Printf("[Agent-Debug] Stat failed for '%s': %v", textFile.Name(), err)
	} else {
		log.Printf("[Agent-Debug] Text coverage file size=%d bytes", fileInfo.Size())
		if fileInfo.Size() == 0 {
			log.Printf("[Agent-Warning] 'go tool covdata' produced empty output; likely no counters set.")
		}
	}

	// Get the module path (e.g., "your/module/path") to resolve file paths correctly.
	modulePathCmd := exec.Command("go", "list", "-m")
	var stderrModPath bytes.Buffer
	modulePathCmd.Stderr = &stderrModPath
	log.Printf("[Agent-Debug] Executing: %s (cwd=%s)", modulePathCmd.String(), mustGetwd())
	modulePathBytes, err := modulePathCmd.Output()
	if err != nil {
		log.Printf("[Agent-Error] 'go list -m' failed. stderr=%s", stderrModPath.String())
		return nil, fmt.Errorf("failed to get module path with 'go list -m': %w\nStderr: %s", err, stderrModPath.String())
	}
	modulePath := strings.TrimSpace(string(modulePathBytes))
	log.Printf("[Agent-Debug] Detected module path: %q (len=%d)", modulePath, len(modulePath))

	// Get the module's root directory on the filesystem.
	moduleDirCmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	var stderrModDir bytes.Buffer
	moduleDirCmd.Stderr = &stderrModDir
	log.Printf("[Agent-Debug] Executing: %s (cwd=%s)", moduleDirCmd.String(), mustGetwd())
	moduleDirBytes, err := moduleDirCmd.Output()
	if err != nil {
		log.Printf("[Agent-Error] 'go list -m -f {{.Dir}}' failed. stderr=%s", stderrModDir.String())
		return nil, fmt.Errorf("failed to get module directory with 'go list -m -f {{.Dir}}': %w\nStderr: %s", err, stderrModDir.String())
	}
	moduleDir := strings.TrimSpace(string(moduleDirBytes))
	if moduleDir == "" {
		log.Printf("[Agent-Warning] Module directory is empty. Did you copy go.mod into the runtime image and set GOMOD?")
	}
	log.Printf("[Agent-Debug] Detected module directory: %q", moduleDir)

	// Parse the text format using the standard cover package.
	log.Printf("[Agent-Debug] Parsing text coverage profile from: '%s'", textFile.Name())
	profiles, err := cover.ParseProfiles(textFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to parse text coverage profile: %w", err)
	}
	log.Printf("[Agent-Debug] Parsed %d profiles from the coverage file.", len(profiles))

	executedLinesByFile := make(map[string][]int)
	skipped := 0

	for i, profile := range profiles {
		log.Printf("[Agent-Debug] Processing profile #%d file='%s'", i+1, profile.FileName)
		var absolutePath string
		if strings.HasPrefix(profile.FileName, modulePath) {
			relativePath := strings.TrimPrefix(profile.FileName, modulePath)
			absolutePath = filepath.Join(moduleDir, relativePath)
			log.Printf("[Agent-Debug] Profile #%d matched modulePath. rel='%s' abs='%s'", i+1, relativePath, absolutePath)
		} else if !filepath.IsAbs(profile.FileName) {
			skipped++
			log.Printf("[Agent-Warning] Profile #%d SKIP file='%s' reason='not absolute & no module prefix match' modulePath=%q moduleDir=%q",
				i+1, profile.FileName, modulePath, moduleDir)
			continue
		} else {
			absolutePath = profile.FileName
			log.Printf("[Agent-Debug] Profile #%d using absolute path: '%s'", i+1, absolutePath)
		}

		lineSet := make(map[int]bool)

		log.Printf("[Agent-Debug] Profile #%d blocks=%d", i+1, len(profile.Blocks))
		for j, block := range profile.Blocks {
			if block.Count <= 0 {
				continue
			}
			log.Printf("[Agent-Trace] Profile #%d Block #%d count=%d lines=%d..%d", i+1, j+1, block.Count, block.StartLine, block.EndLine)
			for line := block.StartLine; line <= block.EndLine; line++ {
				lineSet[line] = true
			}
		}

		if len(lineSet) > 0 {
			lines := make([]int, 0, len(lineSet))
			for line := range lineSet {
				lines = append(lines, line)
			}
			sort.Ints(lines)
			executedLinesByFile[absolutePath] = lines
			log.Printf("[Agent-Debug] Profile #%d: collected %d executed lines for '%s'", i+1, len(lines), absolutePath)
		} else {
			log.Printf("[Agent-Debug] Profile #%d: 0 executed lines after processing blocks for '%s'", i+1, absolutePath)
		}
	}

	log.Printf("[Agent-Debug] Final processed data: filesWithCoverage=%d, profilesParsed=%d, profilesSkipped=%d",
		len(executedLinesByFile), len(profiles), skipped)
	return executedLinesByFile, nil
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "<unknown>"
	}
	return wd
}

func headPath(p string) string {
	// show first entry of PATH to avoid overly long logs
	if p == "" {
		return ""
	}
	if i := strings.IndexByte(p, ':'); i >= 0 {
		return p[:i]
	}
	return p
}
