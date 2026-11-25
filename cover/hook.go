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
	"sort"
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
	// A START taken is answered with whose BACKGROUND ask this process
	// holds: "bg=-" until Keploy's agent asks, then "bg=<the ask's token>",
	// so an agent asks when the token is not its own (a new run, a restarted
	// agent, a restarted app). An SDK not to be asked at a START says no bg:
	// an older Go SDK, which sends no BACKGROUND record, and the Java SDK,
	// which is asked after its INDEX.
	bgUnasked = "-"
)

var (
	// controlMu makes each command atomic: one START or END at a time.
	controlMu sync.Mutex
	// currentTestID is the test whose START was taken and whose END has not
	// come yet, or "".
	currentTestID string

	// prevTestID is the test whose END was taken last and whose counters
	// were cleared after it: the window from then to the next START is a gap
	// between tests, and prevTestID opened it. "" when there is no such gap
	// to trust (no END yet, a command not taken, a reset that failed).
	prevTestID string
	// bgAsked says Keploy's agent asked for BACKGROUND records (the
	// BACKGROUND command), bgToken is the token its ask named (its receiver;
	// "" for an ask that named none), and bgLost counts the gap records
	// since that ask that could not be read or sent.
	bgAsked bool
	bgToken string
	bgLost  int
	// bgShown is the lost count the last record sent to that receiver
	// carried, and lastTest the last test whose START was taken: once asked,
	// a START after a test with no gap to send (its END not taken, its reset
	// failed) counts a lost gap.
	bgShown  int
	lastTest string
	// everGap is every line any gap window ran, of any test set, since the
	// app started: a test whose lines meet it may hold the app's
	// between-tests work (overlapBy).
	everGap = map[string]map[int]struct{}{}

	// prepareMeta reads the app's coverage meta-data at a START, once;
	// clearCounters and publishCoverage reset the counters at a START and
	// send a test's coverage at an END. Variables for tests.
	prepareMeta     = func() error { _, err := loadMeta(); return err }
	clearCounters   = coverage.ClearCounters
	publishCoverage = reportCoverage
	// snapshotLines reads the lines run since the counters were last
	// cleared, sendRecord sends one record to Keploy, and buildID names the
	// app's build. Variables for tests.
	snapshotLines = executedLines
	sendRecord    = sendToSocket
	buildID       = goBuildID

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
	reply := runCommand(strings.TrimSpace(command))
	if _, err := conn.Write([]byte(reply)); err != nil {
		log.Printf("[keploy coverage] error answering %q: %v", strings.TrimSpace(reply), err)
	}
}

// backgroundAsk is the control command with which Keploy's agent asks for
// BACKGROUND records: the lines the app ran between two tests.
const backgroundAsk = "BACKGROUND"

// startReply is a taken START's answer: whose BACKGROUND ask this process
// holds. Called with controlMu held, in the START's own hold, so the answer
// is the state the START left.
func startReply() string {
	token := bgUnasked
	if bgAsked {
		token = bgToken
	}
	return "ACK proto=2 bg=" + token + "\n"
}

// runCommand runs one command and returns its answer.
func runCommand(command string) string {
	if command == backgroundAsk || strings.HasPrefix(command, backgroundAsk+" ") {
		token := strings.TrimSpace(strings.TrimPrefix(command, backgroundAsk))
		controlMu.Lock()
		defer controlMu.Unlock()
		// An app that cannot be measured has no gap to tell of either.
		if err := prepareMeta(); err != nil {
			sayWhyNotMeasured(err)
			return replyIgnored
		}
		// The records go to the receiver that asked: a new one (a new keploy
		// run, an agent restarted: another token) counts their losses
		// afresh. A gap open now is still read and sent at the next START;
		// one whose "after" is an earlier run's test is no window of the
		// new run's (keploy's ranNext), but its lines still mark the tests
		// that meet them.
		// lastTest stays: the ask comes after this receiver's first START,
		// so lastTest is already its test.
		if !bgAsked || token != bgToken {
			bgLost, bgShown = 0, 0
		}
		bgAsked, bgToken = true, token
		return replyTaken
	}
	return runTestCommand(command)
}

// runTestCommand runs a START or END and returns its answer: replyIgnored for
// one not taken, startReply (made in the same hold of controlMu) for a START
// taken, replyTaken for an END taken.
func runTestCommand(command string) string {
	action, id, ok := strings.Cut(command, " ")
	if !ok || id == "" {
		log.Printf("[keploy coverage] command %q names no test", command)
		return replyIgnored
	}
	controlMu.Lock()
	defer controlMu.Unlock()
	switch action {
	case "START":
		// The meta-data is read before the counters are reset, so reading it
		// is no test's work, and an app whose coverage cannot be read says
		// so at its first START, not at each END.
		if err := prepareMeta(); err != nil {
			currentTestID, prevTestID = "", ""
			sayWhyNotMeasured(err)
			return replyIgnored
		}
		// The counters hold the gap since the last test's END: what the app
		// ran between the two tests, never this test's or that one's.
		switch {
		case prevTestID != "" && currentTestID == "":
			noteGap(prevTestID, id)
		case bgAsked && lastTest != "":
			// The window since the last test holds more than a gap (a
			// test whose END was not taken, a failed reset): keploy is
			// told it lost one.
			bgLost++
		}
		prevTestID = ""
		// A START whose counters were not reset is not taken: the test's
		// coverage would hold the lines of the tests before it.
		if err := clearCounters(); err != nil {
			currentTestID = ""
			sayWhyNotMeasured(err)
			return replyIgnored
		}
		currentTestID, lastTest = id, id
		return startReply()
	case "END":
		if currentTestID != id {
			// An app that cannot be measured already said why at its START.
			if currentTestID != "" || len(saidWhy) == 0 {
				log.Printf("[keploy coverage] END for %q while the test started is %q: its coverage is not sent", id, currentTestID)
			}
			prevTestID = ""
			return replyIgnored
		}
		currentTestID = ""
		if bgAsked && bgLost > bgShown {
			sendLost(id)
		}
		err := publishCoverage(id)
		// The counters are reset after every END, so the window to the next
		// START is a gap between tests alone. One whose reset failed holds
		// this test's lines too, and is no gap to trust.
		cerr := clearCounters()
		if cerr == nil {
			prevTestID = id
		} else {
			prevTestID = ""
			log.Printf("[keploy coverage] could not reset the counters after test %s, so the window to the next test is not taken as a gap: %v", id, cerr)
		}
		if err != nil {
			log.Printf("[keploy coverage] could not send the coverage of test %s: %v", id, err)
			return replyIgnored
		}
		return replyTaken
	default:
		log.Printf("[keploy coverage] unknown command %q", action)
		return replyIgnored
	}
}

// noteGap reads the window from after's END to before's START, a gap between
// two tests, keeps its lines (everGap) and, once the agent asked, sends it as
// a BACKGROUND record. Called with controlMu held.
func noteGap(after, before string) {
	lines, err := snapshotLines()
	if err != nil {
		if bgAsked {
			bgLost++
		}
		log.Printf("[keploy coverage] could not read what the app ran between tests %s and %s: %v", after, before, err)
		return
	}
	for file, ls := range lines {
		ever := everGap[file]
		if ever == nil {
			ever = map[int]struct{}{}
			everGap[file] = ever
		}
		for _, l := range ls {
			ever[l] = struct{}{}
		}
	}
	if !bgAsked || len(lines) == 0 {
		return
	}
	if err := sendBackground(after, before, lines); err != nil {
		log.Printf("[keploy coverage] could not send what the app ran between tests %s and %s: %v", after, before, err)
	}
}

// sendLost tells keploy, ahead of test's own record, of the gaps lost since
// the last record sent: a BACKGROUND record of test's set with no lines and
// no "after" (so it is no window), only the lost count, so the set the loss
// is of is never taken as complete. Called with controlMu held.
func sendLost(test string) {
	if err := sendBackground("", test, map[string][]int{}); err != nil {
		log.Printf("[keploy coverage] could not tell keploy of the gaps between tests it lost before test %s: %v", test, err)
	}
}

// sendBackground sends one BACKGROUND record, carrying bgLost; one not sent
// counts as lost too. Called with controlMu held.
func sendBackground(after, before string, lines map[string][]int) error {
	data, err := encodeBackground(after, before, lines)
	if err == nil {
		err = sendRecord(data)
	}
	if err != nil {
		bgLost++
		return err
	}
	bgShown = bgLost
	return nil
}

// backgroundRecordV is the version of the BACKGROUND records this package
// sends.
const backgroundRecordV = 1

// encodeBackground is a BACKGROUND record: the lines the app ran between
// tests after and before. "kind" and "before" come first, so an agent that
// reads only a record's head still knows its test set.
func encodeBackground(after, before string, lines map[string][]int) ([]byte, error) {
	build, err := buildID()
	if err != nil {
		return nil, err
	}
	head, err := json.Marshal(struct {
		Kind   string `json:"kind"`
		Before string `json:"before"`
		After  string `json:"after"`
		V      int    `json:"v"`
		Build  string `json:"build"`
		Lost   int    `json:"lost"`
	}{"background", before, after, backgroundRecordV, build, bgLost})
	if err != nil {
		return nil, fmt.Errorf("encode what the app ran between tests: %w", err)
	}
	body, err := json.Marshal(lines)
	if err != nil {
		return nil, fmt.Errorf("encode what the app ran between tests: %w", err)
	}
	// head without its closing brace, then the lines.
	out := append(head[:len(head)-1:len(head)-1], `,"executedLinesByFile":`...)
	out = append(out, body...)
	return append(out, '}'), nil
}

// maxPartialNames bounds how many files a coverage-partial record names.
const maxPartialNames = 8

// overlapBy returns the coverage-partial marks ("between-tests:<file>", at
// most maxPartialNames then "+N") and the exact overlapping lines per file
// for lines (a test's) that some gap between tests ran too, before this test's
// END, in this process.
//
// A record that names any is coverage-partial ("contaminated") so an older
// agent or an incomplete background still fails closed and neither drops the
// test nor drops another as its duplicate. Sending the complete overlap lines
// alongside lets a Keploy that holds the set's complete between-tests
// background confirm that every overlapping line is subtracted from the set's
// dedup keys and trust the test. Called with controlMu held.
func overlapBy(lines map[string][]int) ([]string, map[string][]int) {
	var files []string
	var overlap map[string][]int
	for file, ls := range lines {
		ever := everGap[file]
		if ever == nil {
			continue
		}
		var hit []int
		for _, l := range ls {
			if _, seen := ever[l]; seen {
				hit = append(hit, l)
			}
		}
		if len(hit) == 0 {
			continue
		}
		// linesOf already returns sorted unique lines, but defensively sort
		// and deduplicate so synthetic/test snapshotLines cannot emit dupes.
		sort.Ints(hit)
		hit = compactSortedInts(hit)
		if overlap == nil {
			overlap = make(map[string][]int)
		}
		overlap[file] = hit
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil, nil
	}
	sort.Strings(files)
	names := make([]string, 0, maxPartialNames+1)
	for i, f := range files {
		if i == maxPartialNames {
			names = append(names, fmt.Sprintf("+%d", len(files)-maxPartialNames))
			break
		}
		names = append(names, "between-tests:"+f)
	}
	return names, overlap
}

// compactSortedInts deduplicates an already-sorted slice in place.
func compactSortedInts(s []int) []int {
	if len(s) < 2 {
		return s
	}
	w := 1
	for i := 1; i < len(s); i++ {
		if s[i] != s[w-1] {
			s[w] = s[i]
			w++
		}
	}
	return s[:w]
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
// source file, and, where some of them ran between tests too, which files and
// exact lines overlapped (overlapBy). Called with controlMu held.
func reportCoverage(testID string) error {
	lines, err := snapshotLines()
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		log.Printf("[keploy coverage] test %s ran no measured line", testID)
	}
	partial, overlap := overlapBy(lines)
	data, err := encodeRecord(testID, lines, partial, overlap)
	if err != nil {
		return err
	}
	return sendRecord(data)
}

// encodeRecord is a test's coverage record: a map, as the SDK always sent,
// so its keys are written sorted ("contaminated", when partial names a file,
// "executedLinesByFile", "id", then "overlapLinesByFile" when overlap holds
// lines). contaminated is the agent's field for a record whose lines may hold
// work that is not the test's; overlapLinesByFile lists the exact lines shared
// with between-tests gaps so Keploy can confirm them against a complete
// background.
func encodeRecord(testID string, lines map[string][]int, partial []string, overlap map[string][]int) ([]byte, error) {
	rec := map[string]interface{}{
		"id":                  testID,
		"executedLinesByFile": lines,
	}
	if len(partial) > 0 {
		rec["contaminated"] = partial
	}
	if len(overlap) > 0 {
		rec["overlapLinesByFile"] = overlap
	}
	data, err := json.Marshal(rec)
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
