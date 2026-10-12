package keploytest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func vars(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDecideOffIsInert(t *testing.T) {
	for _, v := range []string{"off", "OFF", " off "} {
		// Off wins over keploy's other variables.
		got := decide(vars(map[string]string{envAgent: v, envTestPID: "7"}), 7)
		if got.mode != inert {
			t.Errorf("KEPLOY_MOCK_AGENT=%q: mode %d, want inert", v, got.mode)
		}
	}
}

// Without the variable the package is inert: only keploy mock record and
// keploy mock replay export it, and nothing else joins a test binary to
// keploy.
func TestDecideNoVariableIsInert(t *testing.T) {
	got := decide(vars(map[string]string{envTestPID: "7"}), 7)
	if got.mode != inert {
		t.Fatalf("mode %d (%s), want inert", got.mode, got.refusal)
	}
}

// A URL joins: keploy mock record|replay exported it into the test command.
func TestDecideAURLIsActive(t *testing.T) {
	got := decide(vars(map[string]string{envAgent: "http://127.0.0.1:16789", envToken: "tok"}), 7)
	if got != (joining{mode: active, url: "http://127.0.0.1:16789", token: "tok"}) {
		t.Fatalf("got %+v, want active", got)
	}
}

// stubRunsInside makes this process run inside exactly the pids given.
func stubRunsInside(t *testing.T, pids ...int) {
	t.Helper()
	was := runsInside
	runsInside = func(pid int) bool {
		for _, p := range pids {
			if p == pid {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { runsInside = was })
}

func TestDecideAnotherTestProcessMakesAMember(t *testing.T) {
	stubRunsInside(t, 8)
	got := decide(vars(map[string]string{envAgent: "http://127.0.0.1:16789", envTestPID: "8"}), 7)
	if got.mode != inert {
		t.Fatalf("mode %d (%s), want inert: pid 8 is the test process, this is 7", got.mode, got.refusal)
	}
}

// A marker naming a process this one does not run inside was inherited from
// elsewhere: this process is a test process of its own, and joins.
func TestDecideAStaleMarkerJoins(t *testing.T) {
	stubRunsInside(t)
	for _, tp := range []string{"8", "not-a-pid"} {
		got := decide(vars(map[string]string{envAgent: "http://127.0.0.1:16789", envTestPID: tp}), 7)
		if got.mode != active {
			t.Fatalf("KEPLOY_TEST_PID=%s: mode %d (%s), want active", tp, got.mode, got.refusal)
		}
	}
}

// The parent chain read from /proc: this process runs inside its parent and
// the parent's parent, and not inside a process it started.
func TestRunsInsideReadsTheParentChain(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc on this system: the marker is taken at its word")
	}
	if !runsInside(os.Getppid()) {
		t.Fatal("not inside its parent")
	}
	if gp, ok := procParent(os.Getppid()); ok && gp > 0 && !runsInside(gp) {
		t.Fatal("not inside its parent's parent")
	}
	c := exec.Command("sleep", "60")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	if runsInside(c.Process.Pid) {
		t.Fatal("inside a process it started")
	}
}

func TestDecideTheTestProcessIsActive(t *testing.T) {
	got := decide(vars(map[string]string{
		envAgent:   "http://127.0.0.1:16789/",
		envTestPID: "7",
		envToken:   "tok",
	}), 7)
	want := joining{mode: active, url: "http://127.0.0.1:16789", token: "tok"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDecideRefusesWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, agent, pid, want string
	}{
		{"not a URL", "localhost:16789", "7", "is neither off nor the http URL"},
		{"no host", "http://", "7", "is neither off nor the http URL"},
		{"another scheme", "unix:///tmp/agent.sock", "7", "is neither off nor the http URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(vars(map[string]string{envAgent: tc.agent, envTestPID: tc.pid}), 7)
			if got.mode != refused || !strings.Contains(got.refusal, tc.want) {
				t.Fatalf("got %+v, want a refusal saying %q", got, tc.want)
			}
		})
	}
}
