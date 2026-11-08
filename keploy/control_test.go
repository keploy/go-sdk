package keploy

import (
	"bufio"
	"errors"
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
	os.Exit(m.Run())
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
	// The children exit with their socket file still there.
	t.Cleanup(func() { _ = os.Remove(controlSocketPath) })
	for i := 0; i < 5; i++ {
		// This process's init bound the same path: remove it, so the child can
		// reach no socket but one it bound itself.
		if err := os.RemoveAll(controlSocketPath); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^$")
		cmd.Env = append(os.Environ(), dialAtMainEnv+"=1", "GOMAXPROCS=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run %d: the control socket was not there when main ran (%v):\n%s", i, err, out)
		}
	}
}

// startTestServer binds a control socket in a temp dir, as init does, and
// returns its path.
func startTestServer(t *testing.T) string {
	t.Helper()
	// A short dir: a Unix socket path is limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "kc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	// One P: a bind left on a goroutine would not have run by the dial below.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	if err := startControlServer(path); err != nil {
		t.Fatal(err)
	}
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
// the STARTs' resets return in turn, the last one again for any after it, and
// publishErr each END's publish; it returns the ids published.
func stubCoverage(t *testing.T, clearErrs []error, publishErr error) *[]string {
	t.Helper()
	oldClear, oldPublish := clearCounters, publishCoverage
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
		clearCounters, publishCoverage = oldClear, oldPublish
		currentTestID = ""
	})
	currentTestID = ""
	return &published
}

// TestEveryCommandIsAnswered pins the SDK's side of the control socket: every
// command it reads gets one line before the close, ACK for a command taken and
// IGNORED for one not taken, each with proto=2, so Keploy can read a close
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
			{"START set-0/test-1", replyTaken},
			{"END set-0/test-1", replyTaken},
		}, published: []string{"set-0/test-1"}},
		{name: "an END with no START is ignored", steps: []step{
			{"END set-0/test-1", replyIgnored},
		}},
		{name: "an END for another test is ignored and publishes nothing", steps: []step{
			{"START set-0/test-1", replyTaken},
			{"END set-0/test-2", replyIgnored},
		}},
		{name: "a second END for a test is ignored", steps: []step{
			{"START set-0/test-1", replyTaken},
			{"END set-0/test-1", replyTaken},
			{"END set-0/test-1", replyIgnored},
		}, published: []string{"set-0/test-1"}},
		{name: "a START whose reset failed is ignored, and so is its END", clearErrs: []error{failed}, steps: []step{
			{"START set-0/test-1", replyIgnored},
			{"END set-0/test-1", replyIgnored},
		}},
		{name: "a START whose reset failed ends the test before it", clearErrs: []error{nil, failed}, steps: []step{
			{"START set-0/test-1", replyTaken},
			{"START set-0/test-1", replyIgnored},
			{"END set-0/test-1", replyIgnored},
		}},
		{name: "an END whose publish failed is ignored", publishErr: failed, steps: []step{
			{"START set-0/test-1", replyTaken},
			{"END set-0/test-1", replyIgnored},
		}, published: []string{"set-0/test-1"}},
		{name: "an END whose publish failed still ends the test", publishErr: failed, steps: []step{
			{"START set-0/test-1", replyTaken},
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

// TestTheAnswersCarryProto2 pins the field Keploy tells an SDK that answers
// every command by: an older SDK closed some connections with no answer.
func TestTheAnswersCarryProto2(t *testing.T) {
	for _, reply := range []string{replyTaken, replyIgnored} {
		fields := strings.Fields(reply)
		if len(fields) != 2 || fields[1] != "proto=2" || !strings.HasSuffix(reply, "\n") {
			t.Fatalf("answer %q: want one line, a word then proto=2", reply)
		}
	}
	if !strings.HasPrefix(replyTaken, "ACK ") || !strings.HasPrefix(replyIgnored, "IGNORED ") {
		t.Fatalf("answers %q, %q: want ACK and IGNORED first, as Keploy reads them", replyTaken, replyIgnored)
	}
}
