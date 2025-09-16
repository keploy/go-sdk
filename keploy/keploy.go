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
		log.Printf("[Agent-Debug] Error reading command: %v", err)
		return
	}
	log.Printf("[Agent-Debug] Received raw command: '%s'", strings.TrimSpace(command))

	// Split the command into action and testID
	parts := strings.SplitN(strings.TrimSpace(command), " ", 2)
	if len(parts) != 2 {
		log.Printf("[Agent-Debug] Invalid command format: '%s'", command)
		return
	}
	action, id := parts[0], parts[1]
	log.Printf("[Agent-Debug] Parsed command. Action: '%s', ID: '%s'", action, id)

	controlMu.Lock()
	defer controlMu.Unlock()

	switch action {
	case "START":
		log.Printf("[Agent-Debug] Handling START command for test ID: '%s'", id)
		currentTestID = id
		err := coverage.ClearCounters()
		if err != nil {
			log.Printf("[Agent-Debug] Error clearing coverage counters: %v", err)
		} else {
			log.Printf("[Agent-Debug] Successfully cleared coverage counters for test ID: '%s'", id)
		}
	case "END":
		log.Printf("[Agent-Debug] Handling END command for test ID: '%s'", id)
		if currentTestID != id {
			log.Printf("[Agent-Debug] Warning: Mismatched END command. Expected '%s', got '%s'. Skipping coverage report to avoid inconsistent state.", currentTestID, id)
			return
		}
		err := reportCoverage(id)
		if err != nil {
			log.Printf("[Agent] 🚨 Error reporting coverage for test %s: %v", id, err)
		} else {
			log.Printf("[Agent-Debug] Successfully initiated coverage report for test ID: '%s'", id)
		}
		// Reset the currentTestID to an empty string to indicate that no test is currently being recorded.
		log.Printf("[Agent-Debug] Resetting currentTestID from '%s' to empty.", currentTestID)
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
	log.Printf("[Agent-Debug] Starting reportCoverage for testID: '%s'", testID)
	// Only take the part before the first slash,
	// e.g. "test-2" from "test-set-0/test-2"
	parts := strings.SplitN(testID, "/", 2)
	baseID := parts[1]
	log.Printf("[Agent-Debug] Extracted baseID: '%s'", baseID)

	// Create a temporary directory to store the coverage data.
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("keploy-coverage-%s-", baseID))
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	log.Printf("[Agent-Debug] Created temporary directory for coverage data: %s", tempDir)
	// defer func() {
	// 	if err := os.RemoveAll(tempDir); err != nil {
	// 		log.Printf("[Agent] Error removing temp dir: %v", err)
	// 	}
	// }()

	err = coverage.WriteCountersDir(tempDir)
	if err != nil {
		return fmt.Errorf("failed to write coverage counters. Ensure the application was built with '-cover -covermode=atomic'. Original error: %w", err)
	}
	log.Printf("[Agent-Debug] Successfully wrote coverage counters to %s", tempDir)

	err = coverage.WriteMetaDir(tempDir)
	if err != nil {
		return fmt.Errorf("failed to write meta dir: %w", err)
	}
	log.Printf("[Agent-Debug] Successfully wrote meta data to %s", tempDir)

	log.Printf("[Agent-Debug] Processing coverage profiles from directory: %s", tempDir)
	processedData, err := processCoverageProfilesUsingCovdata(tempDir)
	if err != nil {
		return fmt.Errorf("failed to process coverage profiles: %w", err)
	}
	log.Printf("[Agent-Debug] Finished processing coverage profiles. Found coverage for %d files.", len(processedData))

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
	log.Printf("[Agent-Debug] Marshalled JSON payload of size %d bytes for testID: '%s'", len(jsonData), testID)

	log.Printf("[Agent-Debug] Sending coverage data to socket for testID: '%s'", testID)
	err = sendToSocket(jsonData)
	if err != nil {
		log.Printf("[Agent-Debug] Error sending data to socket: %v", err)
		return err
	}
	log.Printf("[Agent-Debug] Successfully sent coverage data to socket for testID: '%s'", testID)
	return nil
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
	log.Printf("[Agent-Debug] processCoverageProfilesUsingCovdata called for directory: '%s'", dir)
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
	log.Printf("[Agent-Debug] Executing command: %s", cmd.String())

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to convert coverage data to text format: %w\nStderr: %s", err, stderr.String())
	}
	log.Printf("[Agent-Debug] Successfully executed 'go tool covdata'.")

	// Check the size of the output file
	fileInfo, err := textFile.Stat()
	if err != nil {
		log.Printf("[Agent-Debug] Could not stat temporary text file '%s': %v", textFile.Name(), err)
	} else {
		log.Printf("[Agent-Debug] Temporary text file '%s' has size: %d bytes.", textFile.Name(), fileInfo.Size())
		if fileInfo.Size() == 0 {
			log.Printf("[Agent-Warning] The 'go tool covdata' command produced an empty output file. This likely means no coverage counters were set during the test run.")
		}
	}

	// Get the module path (e.g., "your/module/path") to resolve file paths correctly.
	modulePathCmd := exec.Command("go", "list", "-m")
	var stderrModPath bytes.Buffer
	modulePathCmd.Stderr = &stderrModPath
	log.Printf("[Agent-Debug] Executing command: %s", modulePathCmd.String())
	modulePathBytes, err := modulePathCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get module path with 'go list -m': %w\nStderr: %s", err, stderrModPath.String())
	}
	modulePath := strings.TrimSpace(string(modulePathBytes))
	log.Printf("[Agent-Debug] Detected module path: '%s'", modulePath)

	// Get the module's root directory on the filesystem.
	moduleDirCmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	var stderrModDir bytes.Buffer
	moduleDirCmd.Stderr = &stderrModDir
	log.Printf("[Agent-Debug] Executing command: %s", moduleDirCmd.String())
	moduleDirBytes, err := moduleDirCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get module directory with 'go list -m -f {{.Dir}}': %w\nStderr: %s", err, stderrModDir.String())
	}
	moduleDir := strings.TrimSpace(string(moduleDirBytes))
	log.Printf("[Agent-Debug] Detected module directory: '%s'", moduleDir)

	// Parse the text format using the standard cover package.
	log.Printf("[Agent-Debug] Parsing text coverage profile from: '%s'", textFile.Name())
	profiles, err := cover.ParseProfiles(textFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to parse text coverage profile: %w", err)
	}
	log.Printf("[Agent-Debug] Parsed %d profiles from the coverage file.", len(profiles))

	executedLinesByFile := make(map[string][]int)

	for i, profile := range profiles {
		log.Printf("[Agent-Debug] Processing profile #%d for file: '%s'", i+1, profile.FileName)
		var absolutePath string
		if strings.HasPrefix(profile.FileName, modulePath) {
			relativePath := strings.TrimPrefix(profile.FileName, modulePath)
			absolutePath = filepath.Join(moduleDir, relativePath)
			log.Printf("[Agent-Debug] Profile #%d: Matched module path. Relative path: '%s', Absolute path: '%s'", i+1, relativePath, absolutePath)
		} else if !filepath.IsAbs(profile.FileName) {
			log.Printf("[Agent-Debug] Profile #%d: Skipping file '%s' because it is not an absolute path and does not match the module path.", i+1, profile.FileName)
			continue
		} else {
			absolutePath = profile.FileName
			log.Printf("[Agent-Debug] Profile #%d: Using existing absolute path: '%s'", i+1, absolutePath)
		}

		lineSet := make(map[int]bool)

		log.Printf("[Agent-Debug] Profile #%d has %d blocks to process.", i+1, len(profile.Blocks))
		// For each block in the profile, if the count is greater than 0, add the lines to the map.
		for j, block := range profile.Blocks {
			if block.Count <= 0 {
				continue
			}
			log.Printf("[Agent-Debug] Profile #%d, Block #%d: Count is %d. Processing lines %d to %d.", i+1, j+1, block.Count, block.StartLine, block.EndLine)
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
			log.Printf("[Agent-Debug] Profile #%d: Found %d executed lines for file '%s'.", i+1, len(lines), absolutePath)
		} else {
			log.Printf("[Agent-Debug] Profile #%d: No executed lines found for file '%s' after processing all blocks.", i+1, absolutePath)
		}
	}

	log.Printf("[Agent-Debug] Final processed data contains coverage for %d files.", len(executedLinesByFile))
	return executedLinesByFile, nil
}
