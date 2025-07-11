// Package keploy provides a universal, protocol-agnostic code coverage solution for Keploy integration tests.
// It is designed to be concurrency-safe and robust by leveraging standard Go tooling.
//
// To activate, simply import this package for its side effects:
//
//	import _ "your/module/path/coverage"
//
// Then, build your application with atomic coverage instrumentation:
//
//	go build -cover -covermode=atomic -o your-app-instrumented .
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
	"strings"
	"sync"

	"golang.org/x/tools/cover"
)

const (
	// controlSocketPath is used by Keploy to send commands (START/END) to the app.
	controlSocketPath = "/tmp/coverage_control.sock"
	// dataSocketPath is used by the app to send coverage data back to Keploy.
	dataSocketPath = "/tmp/keploy-coverage.sock"
)

var (
	// controlMu protects access to the currentTestID, ensuring command handling is atomic.
	controlMu sync.Mutex
	// currentTestID stores the ID of the test case currently being recorded.
	currentTestID string
)

// init starts the background control server that listens for commands from the Keploy test runner.
func init() {
	log.Println("✅ [Agent] Keploy universal coverage agent initialized. Ready to receive commands.")
	go startControlServer()
}

// startControlServer sets up and runs the Unix socket server that listens for commands from Keploy.
func startControlServer() {
	if err := os.RemoveAll(controlSocketPath); err != nil {
		log.Printf("[Agent] Failed to remove old control socket: %v", err)
		return
	}

	ln, err := net.Listen("unix", controlSocketPath)
	if err != nil {
		log.Printf("[Agent] 🚨 FATAL: Could not start control server: %v", err)
		return
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				break // Graceful shutdown
			}
			log.Printf("[Agent] Error accepting connection: %v", err)
			continue
		}
		go handleControlRequest(conn)
	}
}

// handleControlRequest parses commands from Keploy ("START testID", "END testID")
func handleControlRequest(conn net.Conn) {
	defer conn.Close()

	command, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		log.Printf("[Agent] Error reading command: %v", err)
		return
	}

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
		// coverage.ClearCounters() // Uncomment if you want to clear counters between tests
		log.Printf("[Agent] Started coverage capture for test: %s", currentTestID)
	case "END":
		if currentTestID != id {
			log.Printf("[Agent] Warning: Mismatched END command. Expected '%s', got '%s'. Reporting anyway.", currentTestID, id)
		}
		log.Printf("[Agent] Ended coverage capture for test: %s. Reporting...", id)
		if err := reportCoverage(id); err != nil {
			log.Printf("[Agent] 🚨 Error reporting coverage for test %s: %v", id, err)
		}
		currentTestID = "" // Reset for the next test.
	default:
		log.Printf("[Agent] Unrecognized command: %s", action)
	}
}

// reportCoverage dumps, processes, and sends the coverage data.
func reportCoverage(testID string) error {
	tempDir, err := os.MkdirTemp("", "keploy-coverage-")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	// defer os.RemoveAll(tempDir) // Clean up temp directory
	log.Printf("[Agent-Debug] Created temp directory for coverage data: %s", tempDir)

	if err := coverage.WriteCountersDir(tempDir); err != nil {
		return fmt.Errorf("failed to write coverage counters. Ensure the application was built with '-cover -covermode=atomic'. Original error: %w", err)
	}
	log.Printf("[Agent-Debug] Successfully wrote coverage counters to temp dir.")

	if err := coverage.WriteMetaDir(tempDir); err != nil {
		return fmt.Errorf("failed to write meta dir: %w", err)
	}
	log.Printf("[Agent-Debug] Successfully wrote coverage metadata to temp dir.")

	processedData, err := processCoverageProfilesUsingCovdata(tempDir)
	if err != nil {
		return fmt.Errorf("failed to process coverage profiles: %w", err)
	}
	log.Printf("[Agent-Debug] Processed coverage data. Found %d files with covered lines.", len(processedData))

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
	defer conn.Close()

	_, err = conn.Write(data)
	if err == nil {
		log.Printf("[Agent-Debug] Successfully sent %d bytes of coverage data to Keploy.", len(data))
	}
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
	defer os.Remove(textFile.Name())
	defer textFile.Close()

	// Use covdata to convert binary format to text format
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+dir, "-o="+textFile.Name())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to convert coverage data to text format: %w\nStderr: %s", err, stderr.String())
	}

	log.Printf("[Agent-Debug] Successfully converted binary coverage data to text format: %s", textFile.Name())

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
	log.Printf("[Agent-Debug] Detected module path: %s at %s", modulePath, moduleDir)

	// Now parse the text format using the standard cover package
	profiles, err := cover.ParseProfiles(textFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to parse text coverage profile: %w", err)
	}

	log.Printf("[Agent-Debug] Parsed %d profiles from text coverage file.", len(profiles))

	executedLinesByFile := make(map[string][]int)
	totalCoveredBlocks := 0

	for _, profile := range profiles {
		var absolutePath string
		if strings.HasPrefix(profile.FileName, modulePath) {
			// This path is relative to the module root. Construct the absolute path.
			relativePath := strings.TrimPrefix(profile.FileName, modulePath)
			absolutePath = filepath.Join(moduleDir, relativePath)
		} else if !filepath.IsAbs(profile.FileName) {
			// This is a file from outside the main module (e.g., stdlib, dependency)
			// and its path is not absolute. We cannot reliably resolve it.
			log.Printf("[Agent-Debug] Skipping file '%s' as it is outside the main module and its path is not absolute.", profile.FileName)
			continue
		} else {
			// The path is already absolute.
			absolutePath = profile.FileName
		}

		lineSet := make(map[int]bool)
		for _, block := range profile.Blocks {
			if block.Count > 0 {
				totalCoveredBlocks++
				for line := block.StartLine; line <= block.EndLine; line++ {
					lineSet[line] = true
				}
			}
		}

		if len(lineSet) > 0 {
			lines := make([]int, 0, len(lineSet))
			for line := range lineSet {
				lines = append(lines, line)
			}
			executedLinesByFile[absolutePath] = lines
		}
	}

	log.Printf("[Agent-Debug] Found a total of %d covered code blocks across all profiles.", totalCoveredBlocks)
	return executedLinesByFile, nil
}
