package cover

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// dialAtMainEnv makes TestMain, which runs after every init as an app's main
// does, dial the control socket before anything else and exit with the result
// (TestControlSocketIsBoundBeforeMain).
const dialAtMainEnv = "KEPLOY_GO_SDK_TEST_DIAL_AT_MAIN"

// socketPathEnv is the control socket path a child of
// TestControlSocketIsBoundBeforeMain binds: one its parent chose.
const socketPathEnv = "KEPLOY_GO_SDK_TEST_CONTROL_SOCKET"

// testSocketDir holds the control socket this test binary's init binds.
// Package variables are set before any init runs, so init binds a path of
// these tests' own, never /tmp/coverage_control.sock, which a
// Keploy-instrumented app on the same machine may be serving. A child given
// socketPathEnv binds that path, and this is "".
var testSocketDir = func() string {
	if p := os.Getenv(socketPathEnv); p != "" {
		controlSocketPath = p
		dataSocketPath = p + ".data"
		return ""
	}
	dir, err := os.MkdirTemp("", "kc")
	if err != nil {
		panic(err)
	}
	controlSocketPath = filepath.Join(dir, "c.sock")
	dataSocketPath = filepath.Join(dir, "d.sock")
	return dir
}()

func TestMain(m *testing.M) {
	if os.Getenv(dialAtMainEnv) == "1" {
		c, err := net.Dial("unix", controlSocketPath)
		if err != nil {
			_, _ = os.Stdout.WriteString("dial at main: " + err.Error() + "\n")
			os.Exit(3)
		}
		_ = c.Close()
		os.Exit(0)
	}
	code := m.Run()
	if testSocketDir != "" {
		_ = os.RemoveAll(testSocketDir)
	}
	os.Exit(code)
}

// The tests' init bound the control socket at a path of their own, not the
// path an app's init binds.
func TestTheTestsBindAControlSocketOfTheirOwn(t *testing.T) {
	if controlSocketPath == "/tmp/coverage_control.sock" || !strings.HasPrefix(controlSocketPath, testSocketDir+string(filepath.Separator)) {
		t.Fatalf("init bound %s, want a path in %s", controlSocketPath, testSocketDir)
	}
	c, err := net.Dial("unix", controlSocketPath)
	if err != nil {
		t.Fatalf("init did not bind %s: %v", controlSocketPath, err)
	}
	_ = c.Close()
}

// TestControlSocketIsBoundBeforeMain runs this test binary again with one P, so
// a goroutine started in init runs only once main blocks, and its TestMain
// dials the control socket first thing, as the first START does once the app
// is up. The socket must be there: bound on a goroutine, it was not, and the
// test's coverage was lost.
func TestControlSocketIsBoundBeforeMain(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Each child binds a path of its own, which no one else serves, so it can
	// reach no socket but one it bound itself. The children exit with their
	// socket files still there.
	dir := shortTempDir(t)
	for i := 0; i < 5; i++ {
		cmd := exec.Command(exe, "-test.run=^$")
		cmd.Env = append(os.Environ(), dialAtMainEnv+"=1", "GOMAXPROCS=1",
			socketPathEnv+"="+filepath.Join(dir, fmt.Sprintf("c%d.sock", i)))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run %d: the control socket was not there when main ran (%v):\n%s", i, err, out)
		}
	}
}

// shortTempDir is a temp dir removed when t ends, with a short path: a Unix
// socket path is limited to about 100 bytes, which t.TempDir can pass.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startTestServer binds a control socket in a temp dir, as init does, and
// returns its path.
func startTestServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "c.sock")
	// One P: a bind left on a goroutine would not have run by the dial below.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	ln, err := startControlServer(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("startControlServer returned before the socket was bound: %v", err)
	}
	_ = c.Close()
	return path
}

// send sends one command and returns the SDK's answer: the line it wrote
// before it closed the connection, or "" for a close with no answer.
func send(t *testing.T, path, command string) string {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte(command + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return line
}

// stubCoverage replaces the coverage calls for one test: clearErrs are what
// the resets return in turn (each START's, and each taken END's after its
// publish), the last one again for any after it, and publishErr each END's
// publish; it returns the ids published.
func stubCoverage(t *testing.T, clearErrs []error, publishErr error) *[]string {
	t.Helper()
	oldPrepare, oldClear, oldPublish, oldSaid, oldMeta := prepareMeta, clearCounters, publishCoverage, saidWhy, meta
	saidWhy = map[string]bool{}
	prepareMeta = func() error { return nil }
	var published []string
	clearCounters = func() error {
		if len(clearErrs) == 0 {
			return nil
		}
		err := clearErrs[0]
		if len(clearErrs) > 1 {
			clearErrs = clearErrs[1:]
		}
		return err
	}
	publishCoverage = func(id string) error {
		published = append(published, id)
		return publishErr
	}
	t.Cleanup(func() {
		prepareMeta, clearCounters, publishCoverage, saidWhy, meta = oldPrepare, oldClear, oldPublish, oldSaid, oldMeta
		currentTestID = ""
	})
	currentTestID = ""
	resetGaps(t)
	return &published
}

// A taken command's v3 answers with default (enabled) goroutine-tail tracking:
// non-START commands (END, BACKGROUND, TABLE), START before any BACKGROUND ask,
// and START after an ask that named token.
const (
	defaultCaps       = "windowCut=true,contaminated=true,classMismatch=false,background=true,asyncExecutors=true,listenerContamination=false"
	replyTaken        = "ACK proto=3 caps=" + defaultCaps + "\n"
	replyStartUnasked = "ACK proto=3 bg=- caps=" + defaultCaps + "\n"
)

func replyStartAskedBy(token string) string {
	return "ACK proto=3 bg=" + token + " caps=" + defaultCaps + "\n"
}

// encodeRecord is a test helper for records without executedBlocksByFile.
func encodeRecord(testID string, lines map[string][]int, partial []string, overlap map[string][]int) ([]byte, error) {
	return encodeRecordWithBlocks(testID, lines, nil, partial, overlap)
}

// resetGaps clears the between-tests and goroutine-tail state and stubs the
// reads and sends a gap makes, for one test: snapshotLines answers {} unless
// the test sets it.
func resetGaps(t *testing.T) {
	t.Helper()
	oldSnap, oldCov, oldSend, oldBuild, oldGor := snapshotLines, snapshotCoverage, sendRecord, buildID, snapshotGoroutines
	snapshotLines = func() (map[string][]int, error) { return map[string][]int{}, nil }
	snapshotCoverage = func() (map[string][]int, map[string][]int, error) {
		l, err := snapshotLines()
		return l, nil, err
	}
	sendRecord = func([]byte) error { return nil }
	buildID = func() (string, error) { return "go1-00112233445566778899aabbccddeeff", nil }
	clear := func() {
		prevTestID, bgAsked, bgToken, bgLost, bgShown, lastTest = "", false, "", 0, 0, ""
		everGap = map[string]map[int]struct{}{}
		indexSent, indexSockInfo, indexBgToken = false, nil, ""
		resetGoroutineState()
	}
	clear()
	t.Cleanup(func() {
		snapshotLines, snapshotCoverage, sendRecord, buildID, snapshotGoroutines = oldSnap, oldCov, oldSend, oldBuild, oldGor
		clear()
	})
}

// TestEveryCommandIsAnswered pins the SDK's side of the control socket: every
// command it reads gets one line before the close, ACK for a command taken and
// IGNORED for one not taken, each with proto=3, so Keploy can read a close
// with no answer as a command not taken.
func TestEveryCommandIsAnswered(t *testing.T) {
	failed := errors.New("failed")
	type step struct{ command, want string }
	rows := []struct {
		name       string
		clearErrs  []error
		publishErr error
		steps      []step
		published  []string
	}{
		{name: "a START and its END are taken", steps: []step{
			{"START set-0/test-1", replyStartUnasked},
			{"END set-0/test-1", replyTaken},
		}, published: []string{"set-0/test-1"}},
		{name: "an END with no START is ignored", steps: []step{
			{"END set-0/test-1", replyIgnored},
		}},
		{name: "an END for another test is ignored and publishes nothing", steps: []step{
			{"START set-0/test-1", replyStartUnasked},
			{"END set-0/test-2", replyIgnored},
		}},
		{name: "a second END for a test is ignored", steps: []step{
			{"START set-0/test-1", replyStartUnasked},
			{"END set-0/test-1", replyTaken},
			{"END set-0/test-1", replyIgnored},
		}, published: []string{"set-0/test-1"}},
		{name: "a START whose reset failed is ignored, and so is its END", clearErrs: []error{failed}, steps: []step{
			{"START set-0/test-1", replyIgnored},
			{"END set-0/test-1", replyIgnored},
		}},
		{name: "a START whose reset failed ends the test before it", clearErrs: []error{nil, failed}, steps: []step{
			{"START set-0/test-1", replyStartUnasked},
			{"START set-0/test-1", replyIgnored},
			{"END set-0/test-1", replyIgnored},
		}},
		{name: "an END whose publish failed is ignored", publishErr: failed, steps: []step{
			{"START set-0/test-1", replyStartUnasked},
			{"END set-0/test-1", replyIgnored},
		}, published: []string{"set-0/test-1"}},
		{name: "an END whose publish failed still ends the test", publishErr: failed, steps: []step{
			{"START set-0/test-1", replyStartUnasked},
			{"END set-0/test-1", replyIgnored},
			{"END set-0/test-1", replyIgnored},
		}, published: []string{"set-0/test-1"}},
		{name: "a command with no test id is ignored", steps: []step{
			{"START", replyIgnored},
		}},
		{name: "an unknown command is ignored", steps: []step{
			{"PAUSE set-0/test-1", replyIgnored},
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			published := stubCoverage(t, row.clearErrs, row.publishErr)
			path := startTestServer(t)
			for _, s := range row.steps {
				if got := send(t, path, s.command); got != s.want {
					t.Fatalf("%q: answer %q, want %q", s.command, got, s.want)
				}
			}
			if strings.Join(*published, ",") != strings.Join(row.published, ",") {
				t.Fatalf("published %v, want %v", *published, row.published)
			}
		})
	}
}

// TestTheAnswersCarryProto3AndCaps pins the v3 control-socket reply format:
// every command gets one line before close with ACK or IGNORED first, proto=3
// second (compatible with proto>=2 parsers), optional bg=<token>, and caps=...
// with no spaces inside caps.
func TestTheAnswersCarryProto3AndCaps(t *testing.T) {
	for _, row := range []struct {
		reply string
		want  []string
	}{
		{replyTaken, []string{"ACK", "proto=3", "caps=" + defaultCaps}},
		{replyIgnored, []string{"IGNORED", "proto=3"}},
		{replyStartUnasked, []string{"ACK", "proto=3", "bg=-", "caps=" + defaultCaps}},
		{replyStartAskedBy("r7"), []string{"ACK", "proto=3", "bg=r7", "caps=" + defaultCaps}},
	} {
		if !strings.HasSuffix(row.reply, "\n") {
			t.Fatalf("answer %q missing trailing newline", row.reply)
		}
		fields := strings.Fields(row.reply)
		if len(fields) != len(row.want) {
			t.Fatalf("answer %q fields = %v, want %v", row.reply, fields, row.want)
		}
		for i := range row.want {
			if fields[i] != row.want[i] {
				t.Fatalf("answer %q field[%d] = %q, want %q", row.reply, i, fields[i], row.want[i])
			}
		}
	}
	stubCoverage(t, nil, nil)
	if got := startReply(); got != replyStartUnasked {
		t.Fatalf("START before any ask answers %q, want %q", got, replyStartUnasked)
	}
	bgAsked, bgToken = true, "r7"
	if got := startReply(); got != replyStartAskedBy("r7") {
		t.Fatalf("START after an ask answers %q, want %q", got, replyStartAskedBy("r7"))
	}
	if !strings.HasPrefix(replyTaken, "ACK ") || !strings.HasPrefix(replyIgnored, "IGNORED ") {
		t.Fatalf("answers %q, %q: want ACK and IGNORED first, as Keploy reads them", replyTaken, replyIgnored)
	}
}

// TestAStartWhoseMetaDataCannotBeReadIsIgnored pins that START reads the
// app's coverage meta-data first: one it cannot read (not a -cover build, a
// format this SDK does not read) is not taken, and resets no counter.
func TestAStartWhoseMetaDataCannotBeReadIsIgnored(t *testing.T) {
	stubCoverage(t, nil, nil)
	cleared := 0
	clearCounters = func() error { cleared++; return nil }
	prepareMeta = func() error { return errors.New("coverage meta-data is format version 2") }
	path := startTestServer(t)
	if got := send(t, path, "START set-0/test-1"); got != replyIgnored {
		t.Fatalf("START: answer %q, want %q", got, replyIgnored)
	}
	if got := send(t, path, "END set-0/test-1"); got != replyIgnored {
		t.Fatalf("END: answer %q, want %q", got, replyIgnored)
	}
	if cleared != 0 {
		t.Fatalf("the counters were reset %d times", cleared)
	}
}

// TestACommandClosedWithNoNewlineIsAnswered pins the reads the agent's
// commands may take: one closed by EOF with no newline is run and answered,
// and a connection closed with no command runs nothing.
func TestACommandClosedWithNoNewlineIsAnswered(t *testing.T) {
	published := stubCoverage(t, nil, nil)
	path := startTestServer(t)
	exchange := func(command string) string {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		if command != "" {
			if _, err := c.Write([]byte(command)); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.(*net.UnixConn).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, _ := bufio.NewReader(c).ReadString('\n')
		return line
	}
	if got := exchange(""); got != "" || currentTestID != "" {
		t.Fatalf("a connection with no command: answer %q, test %q", got, currentTestID)
	}
	if got := exchange("START set-0/test-1"); got != replyStartUnasked {
		t.Fatalf("START with no newline: answer %q, want %q", got, replyStartUnasked)
	}
	if got := exchange("END set-0/test-1"); got != replyTaken {
		t.Fatalf("END with no newline: answer %q, want %q", got, replyTaken)
	}
	if strings.Join(*published, ",") != "set-0/test-1" {
		t.Fatalf("published %v", *published)
	}
}
