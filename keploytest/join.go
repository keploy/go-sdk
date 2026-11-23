package keploytest

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// The variables keploy sets for the test command it runs.
const (
	// envAgent is the URL of the keploy agent the test reports to, or "off".
	envAgent = "KEPLOY_MOCK_AGENT"
	// envToken is the bearer token of that agent's control plane.
	envToken = "KEPLOY_MOCK_AGENT_TOKEN"
	// envProject is the keploy project's directory (the keploy.yml that sets
	// mock.command): a test's ID carries its package's path relative to it.
	envProject = "KEPLOY_MOCK_PROJECT"
	// envTestPID is the pid of the test binary that joined keploy's run. The
	// package sets it as it joins, so a process that inherits it and runs
	// inside that binary (a helper process the test starts, a binary built
	// and run from a test) is a member: it stays inert.
	envTestPID = "KEPLOY_TEST_PID"
	// envTimeFile is the run's clock file, as keploy's clock shim
	// (LD_PRELOAD) reads it. keploy's claim answers with it when the run is
	// frozen, and the claimer exports it, so the processes the test process
	// starts that load the shim run on the run's clock.
	envTimeFile = "KEPLOY_TIME_FILE"
)

type mode int

const (
	// inert: keploy is not running this binary; nothing changes.
	inert mode = iota
	// active: report every top-level test to the agent.
	active
	// refused: every test that starts with T or TP fails with the refusal.
	refused
)

// joining is what this process decided about keploy, once.
type joining struct {
	mode    mode
	url     string
	token   string
	refusal string
	// inherited: keploy's run was claimed for this process before it
	// started, so the package does not claim it again, and only begins and
	// ends the tests.
	inherited bool
}

var (
	joinOnce sync.Once
	joined   joining
)

// join decides, once per process, whether this test binary reports to keploy.
// It is decided in the package's init, before the test package's own init
// and TestMain run (claim.go).
func join() joining {
	joinOnce.Do(func() {
		joined = decide(os.Getenv, os.Getpid())
	})
	return joined
}

// decide reads the variables keploy sets (getenv) and this process's pid.
// The package joins only when KEPLOY_MOCK_AGENT is a URL: keploy mock record
// and keploy mock replay export it into the test command. Unset or "off",
// the package is inert.
func decide(getenv func(string) string, pid int) joining {
	v := strings.TrimSpace(getenv(envAgent))
	if v == "" || strings.EqualFold(v, "off") {
		return joining{mode: inert}
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return refuse("keploy: KEPLOY_MOCK_AGENT=%q is neither off nor the http URL of a keploy agent; set it to off to run without keploy", v)
	}
	j := joining{mode: active, url: strings.TrimRight(v, "/"), token: getenv(envToken)}
	switch tp := strings.TrimSpace(getenv(envTestPID)); {
	case tp == strconv.Itoa(pid):
		// The marker names this process: the run was claimed for it before
		// it started. A launcher (keploy-go-exec) claims the run for the
		// test binary, exports the marker with its own pid, and execs the
		// binary, which keeps that pid (and the claim, which the agent
		// holds by process). Only an exec gives a process a marker naming
		// itself: keploy removes the marker from the environment of the
		// command it runs, and a claimer exports its own pid, which a
		// process it starts does not have. A begin while no live process
		// holds the run's claim is refused by the agent.
		j.inherited = true
	case tp != "":
		// Another process joined keploy's run. This one is a member only
		// when it runs inside that process: a marker naming any other
		// process was inherited from elsewhere (a stale environment), and
		// left to decide, it would let this binary's tests pass unjudged.
		// Such a process claims like any test process; the agent still
		// refuses a second test process of the run.
		if n, err := strconv.Atoi(tp); err == nil && runsInside(n) {
			return joining{mode: inert}
		}
	}
	return j
}

func refuse(format string, args ...any) joining {
	return joining{mode: refused, refusal: fmt.Sprintf(format, args...)}
}

// runsInside reports whether this process runs inside process pid: pid is
// its parent, or an ancestor of it. It reads the parent chain from /proc;
// without /proc (a system other than Linux) it cannot tell, and takes the
// marker at its word.
var runsInside = func(pid int) bool {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return true
	}
	p := os.Getppid()
	for i := 0; i < 128 && p > 0; i++ {
		if p == pid {
			return true
		}
		next, ok := procParent(p)
		if !ok || next == p {
			return false
		}
		p = next
	}
	return false
}

// procParent is process pid's parent, from /proc/<pid>/stat.
func procParent(pid int) (int, bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	// The command name (field 2) is parenthesised and may hold spaces and
	// parentheses: the fields that follow come after its last ')'.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	return ppid, err == nil
}
