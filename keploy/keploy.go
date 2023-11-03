package keploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const (
	GraphQLEndpoint = "/query"
	Host            = "http://localhost:"
)

var (
	// serverPort is the port on which the keploy GraphQL will be running.
	serverPort = 6789
	// process which is running the keploy GraphQL server.
	kProcess   *exec.Cmd
	// Create an buffered channel for stopping the user app.
	shutdownChan = make(chan os.Signal, 1)
)

// Define a custom signal to trigger shutdown event
const shutdownSignal = syscall.SIGUSR1

func init() {
	// Notify the channel when the shutdown signal is received for user app
	signal.Notify(shutdownChan, shutdownSignal)

	logger, _ = zap.NewDevelopment()
	defer func() {
		_ = logger.Sync()
	}()
}

type GraphQLResponse struct {
	Data ResponseData
}

type ResponseData struct {
	TestSets      []string
	TestSetStatus TestSetStatus
	RunTestSet    RunTestSetResponse
}

type TestSetStatus struct {
	Status string
}

type RunTestSetResponse struct {
	Success   bool
	TestRunId string
	Message   string
}

type TestRunStatus string

const (
	Running TestRunStatus = "RUNNING"
	Passed  TestRunStatus = "PASSED"
	Failed  TestRunStatus = "FAILED"
)

// LaunchShutdown sends a custom signal to request the application to 
// shut down gracefully.
func LaunchShutdown() {
	pid := os.Getpid()
	logger.Info(fmt.Sprintf("Sending custom signal %s to PID %d...", shutdownSignal, pid))
	err := syscall.Kill(pid, shutdownSignal)
	if err != nil {
		logger.Info("Failed to send custom signal:", zap.Error(err))
	}
}

// AddShutdownListener listens for the custom signal and initiate shutdown by 
// executing stopper function from the parameter.
func AddShutdownListener(stopper func()) {
	go func() {
		sig := <-shutdownChan
		fmt.Println("Received custom signal:", sig)
		stopper()
	}()
}

// RunKeployServer starts the Keploy server with specified parameters.
func RunKeployServer(pid int64, delay int, testPath string, port int) error {
	defer func() {
		if r := recover(); r != nil {
			logger.Info("Recovered in RunKeployServer", zap.Any("message", r))
		}
	}()

	if port != 0 {
		serverPort = port
	}

	cmd := exec.Command(
		"sudo",
		"/usr/local/bin/keploy",
		"serve",
		fmt.Sprintf("--pid=%d", pid),
		fmt.Sprintf("-p=%s", testPath),
		fmt.Sprintf("-d=%d", delay),
		fmt.Sprintf("--port=%d", port),
		"--language=go",
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		logger.Error("failed to start the keploy serve cmd", zap.Error(err))
		return err
	}
	kProcess = cmd
	// delay to start the proxy and graphql server
	time.Sleep(10 * time.Second)
	return nil
}

// setHttpClient returns a HTTP client and request.
func setHttpClient() (*http.Client, *http.Request, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequest("POST", Host+fmt.Sprintf("%d", serverPort)+GraphQLEndpoint, nil)
	if err != nil {
		return nil, nil, err
	}

	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Accept", "application/json")

	// Set a context with a timeout for reading the response
	ctx, _ := context.WithTimeout(req.Context(), 15*time.Second)

	req = req.WithContext(ctx)

	return client, req, nil
}

// FetchTestSets fetches the recorded test sets from the keploy GraphQL server.
func FetchTestSets() ([]string, error) {
	client, req, err := setHttpClient()
	if err != nil {
		return nil, err
	}

	payload := []byte(`{ "query": "{ testSets }" }`)
	req.Body = io.NopCloser(bytes.NewBuffer(payload))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		var response GraphQLResponse
		if err := json.Unmarshal(bodyBytes, &response); err != nil {
			return nil, err
		}

		return response.Data.TestSets, nil
	}

	return nil, fmt.Errorf("Error fetching test sets")
}

// FetchTestSetStatus fetches test set status based on the running testRunId.
func FetchTestSetStatus(testRunId string) (TestRunStatus, error) {
	client, req, err := setHttpClient()
	if err != nil {
		return "", err
	}

	payloadStr := fmt.Sprintf(`{ "query": "{ testSetStatus(testRunId: \"%s\") { status } }" }`, testRunId)
	req.Body = io.NopCloser(bytes.NewBufferString(payloadStr))

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		var response GraphQLResponse
		if err := json.Unmarshal(bodyBytes, &response); err != nil {
			return "", err
		}

		switch response.Data.TestSetStatus.Status {
		case "RUNNING":
			return Running, nil
		case "PASSED":
			return Passed, nil
		case "FAILED":
			return Failed, nil
		default:
			return "", fmt.Errorf("Unknown status: %s", response.Data.TestSetStatus.Status)
		}
	}

	return "", fmt.Errorf("Error fetching test set status")
}

// RunTestSet runs a test set.
func RunTestSet(testSetName string) (string, error) {
	client, req, err := setHttpClient()
	if err != nil {
		return "", err
	}

	payloadStr := fmt.Sprintf(`{ "query": "mutation { runTestSet(testSet: \"%s\") { success testRunId message } }" }`, testSetName)
	req.Body = io.NopCloser(bytes.NewBufferString(payloadStr))

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		var response GraphQLResponse
		if err := json.Unmarshal(bodyBytes, &response); err != nil {
			return "", err
		}

		return response.Data.RunTestSet.TestRunId, nil
	}

	return "", fmt.Errorf("Error running test set")
}

// isSuccessfulResponse checks if an HTTP response is successful.
func isSuccessfulResponse(resp *http.Response) bool {
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// getResponseBody fetches the response body from an HTTP response.
func getResponseBody(conn *http.Response) (string, error) {
	defer conn.Body.Close()
	bodyBytes, err := io.ReadAll(conn.Body)
	if err != nil {
		return "", err
	}
	return string(bodyBytes), nil
}

// StopKeployServer stops the Keploy GraphQL server.
func StopKeployServer() {
	killProcessOnPort(serverPort)
}

// killProcessOnPort kills the processes and its children listening on the specified port.
func killProcessOnPort(port int) {
	cmdStr := fmt.Sprintf("lsof -t -i:%d", port)
	processIDs, err := exec.Command("sh", "-c", cmdStr).Output()
	if err != nil {
		logger.Error("failed to fetch the proces ID of user application", zap.Error(err), zap.Any("on port", port))
		return
	}

	pids := strings.Split(string(processIDs), "\n")
	for _, pidStr := range pids {
		if pidStr != "" {
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				logger.Error("failed to convert pid from string to integer")
			}
			killProcessesAndTheirChildren(pid)
		}
	}
}

// killProcessesAndTheirChildren recursively kills child processes and their descendants of the parentPID.
func killProcessesAndTheirChildren(parentPID int) {

	pids := []int{}

	findAndCollectChildProcesses(fmt.Sprintf("%d", parentPID), &pids)

	for _, childPID := range pids {
		if os.Getpid() != childPID {
			// Use the `sudo` command to execute the `kill` command with elevated privileges.
			cmd := exec.Command("sudo", "kill", "-9", fmt.Sprint(childPID))

			// Run the `sudo kill` command.
			err := cmd.Run()
			if err != nil {
				fmt.Printf("Failed to kill child process %d: %s\n", childPID, err)
			} else {
				fmt.Printf("Killed child process %d\n", childPID)
			}
		}

	}
}

// findAndCollectChildProcesses find and collect child processes of a parent process.
func findAndCollectChildProcesses(parentPID string, pids *[]int) {
	cmd := exec.Command("pgrep", "-P", parentPID)
	parentIDint, err := strconv.Atoi(parentPID)
	if err != nil {
		logger.Error("failed to convert parent PID to int", zap.Any("error converting parent PID to int", err.Error()))
	}

	*pids = append(*pids, parentIDint)

	output, err := cmd.Output()
	if err != nil {
		return
	}

	outputStr := string(output)
	childPIDs := strings.Split(outputStr, "\n")
	childPIDs = childPIDs[:len(childPIDs)-1]

	for _, childPID := range childPIDs {
		if childPID != "" {
			findAndCollectChildProcesses(childPID, pids)
		}
	}
}
