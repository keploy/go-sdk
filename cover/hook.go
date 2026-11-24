package cover

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"runtime/coverage"
	"strings"
	"sync"
	"time"
)

// controlSocketPath is where Keploy sends this app its commands (START, END).
// Keploy's agent reaches it in the app's own /tmp: on the same host, through
// a shared /tmp volume (docker, compose), or, in a Kubernetes replay pod,
// through /proc/<app pid>/root/tmp. A variable, not a constant, only so this
// package's tests (and a test build's -ldflags -X) bind a path of their own.
var controlSocketPath = "/tmp/coverage_control.sock"

// dataSocketPath is where this app sends Keploy each test's coverage. A
// variable for the same reason as controlSocketPath.
var dataSocketPath = "/tmp/coverage_data.sock"

// The answers on the control socket: one line for every command read, written
// before the connection is closed. ACK says the command was taken: the
// counters were reset for a START, or the test's coverage was sent for an
// END. IGNORED says it was read and not taken. proto=2 says this SDK answers
// every command it reads, so Keploy reads a close with no answer as a
// command not taken.
const (
	replyTaken   = "ACK proto=2\n"
	replyIgnored = "IGNORED proto=2\n"
)

var (
	// controlMu makes each command atomic: one START or END at a time.
	controlMu sync.Mutex
	// currentTestID is the test whose START was taken and whose END has not
	// come yet, or "".
	currentTestID string

	// prepareMeta reads the app's coverage meta-data at a START, once;
	// clearCounters and publishCoverage reset the counters at a START and
	// send a test's coverage at an END. Variables for tests.
	prepareMeta     = func() error { _, err := loadMeta(); return err }
	clearCounters   = coverage.ClearCounters
	publishCoverage = reportCoverage

	// saidWhy holds the reasons a START was not taken that were already
	// logged, so an app that cannot be measured says why once, not per test.
	saidWhy = map[string]bool{}
)

// init binds the control socket before the app's main runs, so it is there
// before the app can say it is ready: bound on a goroutine, the bind could
// come after the app's port, and the first test's START then found no one
// listening and its coverage was lost. It then serves Keploy's commands on a
// goroutine.
func init() {
	if _, err := startControlServer(controlSocketPath); err != nil {
		log.Printf("[keploy coverage] could not listen for Keploy's coverage commands on %s, so no test of this run is measured: %v", controlSocketPath, err)
	}
}

// startControlServer binds the Unix socket at path, replacing a stale one,
// and returns its listener once it is bound, serving Keploy's commands on a
// goroutine until the listener is closed.
func startControlServer(path string) (net.Listener, error) {
	if err := os.RemoveAll(path); err != nil {
		return nil, fmt.Errorf("remove the old control socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go serveControl(ln)
	return ln, nil
}

// commandReadTimeout bounds how long a connection may take to send its
// command: one that sends none holds no goroutine for ever.
const commandReadTimeout = 30 * time.Second

// serveControl handles each connection on ln until ln is closed. An accept
// that keeps failing (no file descriptor left) is retried after a pause that
// grows to a second, never in a tight loop.
func serveControl(ln net.Listener) {
	defer func() { _ = ln.Close() }()
	pause := 5 * time.Millisecond
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("[keploy coverage] error accepting a command: %v", err)
			time.Sleep(pause)
			if pause *= 2; pause > time.Second {
				pause = time.Second
			}
			continue
		}
		pause = 5 * time.Millisecond
		go handleControlRequest(conn)
	}
}

// handleControlRequest reads one command ("START <set>/<test>", "END
// <set>/<test>"), runs it, and answers it before it closes the connection.
func handleControlRequest(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(commandReadTimeout))
	command, err := bufio.NewReader(conn).ReadString('\n')
	switch {
	case errors.Is(err, io.EOF) && strings.TrimSpace(command) == "":
		// A connection closed with no command (a probe of the socket).
		return
	case err != nil && !errors.Is(err, io.EOF):
		log.Printf("[keploy coverage] error reading a command: %v", err)
		return
	}
	// A command closed by EOF with no newline is run and answered too.
	reply := replyIgnored
	if runCommand(strings.TrimSpace(command)) {
		reply = replyTaken
	}
	if _, err := conn.Write([]byte(reply)); err != nil {
		log.Printf("[keploy coverage] error answering %q: %v", strings.TrimSpace(reply), err)
	}
}

// runCommand runs one command and reports whether it took it.
func runCommand(command string) bool {
	action, id, ok := strings.Cut(command, " ")
	if !ok || id == "" {
		log.Printf("[keploy coverage] command %q names no test", command)
		return false
	}
	controlMu.Lock()
	defer controlMu.Unlock()
	switch action {
	case "START":
		// The meta-data is read before the counters are reset, so reading it
		// is no test's work, and an app whose coverage cannot be read says
		// so at its first START, not at each END.
		if err := prepareMeta(); err != nil {
			currentTestID = ""
			sayWhyNotMeasured(err)
			return false
		}
		// A START whose counters were not reset is not taken: the test's
		// coverage would hold the lines of the tests before it.
		if err := clearCounters(); err != nil {
			currentTestID = ""
			sayWhyNotMeasured(err)
			return false
		}
		currentTestID = id
		return true
	case "END":
		if currentTestID != id {
			// An app that cannot be measured already said why at its START.
			if currentTestID != "" || len(saidWhy) == 0 {
				log.Printf("[keploy coverage] END for %q while the test started is %q: its coverage is not sent", id, currentTestID)
			}
			return false
		}
		currentTestID = ""
		if err := publishCoverage(id); err != nil {
			log.Printf("[keploy coverage] could not send the coverage of test %s: %v", id, err)
			return false
		}
		return true
	default:
		log.Printf("[keploy coverage] unknown command %q", action)
		return false
	}
}

// sayWhyNotMeasured logs, once per reason, why a START was not taken. Called
// with controlMu held.
func sayWhyNotMeasured(err error) {
	why := err.Error()
	switch {
	case strings.Contains(why, "not built with -cover"):
		why = "the app was built without -cover, so Keploy cannot measure its tests' coverage: build it with go build -cover -covermode=atomic (keploy-go-cover-build.sh does)"
	case strings.Contains(why, "covermode"):
		why = "the app needs -covermode=atomic for Keploy to measure each test: " + why
	}
	if saidWhy[why] {
		return
	}
	saidWhy[why] = true
	log.Printf("[keploy coverage] not measuring tests: %s", why)
}

// reportCoverage sends the test's coverage to Keploy: the lines it ran, per
// source file. Called with controlMu held.
func reportCoverage(testID string) error {
	lines, err := executedLines()
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		log.Printf("[keploy coverage] test %s ran no measured line", testID)
	}
	data, err := encodeRecord(testID, lines)
	if err != nil {
		return err
	}
	return sendToSocket(data)
}

// encodeRecord is a test's coverage record: a map, as the SDK always sent,
// so its keys are written sorted ("executedLinesByFile", then "id").
func encodeRecord(testID string, lines map[string][]int) ([]byte, error) {
	data, err := json.Marshal(map[string]interface{}{
		"id":                  testID,
		"executedLinesByFile": lines,
	})
	if err != nil {
		return nil, fmt.Errorf("encode the coverage: %w", err)
	}
	return data, nil
}

// The bounds of a record's delivery: an agent that stops reading must not
// hold every later command, which waits on controlMu behind it.
const (
	dataDialTimeout  = 5 * time.Second
	dataWriteTimeout = 30 * time.Second
)

// sendToSocket writes one record on a connection of its own to Keploy's data
// socket.
func sendToSocket(data []byte) error {
	conn, err := net.DialTimeout("unix", dataSocketPath, dataDialTimeout)
	if err != nil {
		return fmt.Errorf("could not reach Keploy's coverage data socket at %s: %w", dataSocketPath, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetWriteDeadline(time.Now().Add(dataWriteTimeout)); err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}
