package keploytest

// The behaviour of T, TP, Run and Parallel is checked by running this
// package's own test binary again as a child, with the variables keploy sets,
// against a fake agent. The child-only tests are named TestChild...; the
// parent skips them (TestMain), and each parent test runs the ones it needs.

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// envChild marks a child run of the test binary.
	envChild = "KEPLOYTEST_CHILD"
	// envBody says what TestChildBody does.
	envBody = "KEPLOYTEST_BODY"
	// bodyRan is printed by a child test whose body ran.
	bodyRan = "KEPLOYTEST-BODY-RAN"
)

func TestMain(m *testing.M) {
	flag.Parse()
	child := os.Getenv(envChild) == "1"
	if !child {
		// Keeps the parent's output free of the child tests; childOnly skips
		// them when -skip is set too.
		if f := flag.Lookup("test.skip"); f != nil && f.Value.String() == "" {
			_ = f.Value.Set("^TestChild")
		}
	}
	code := m.Run()
	if child {
		fmt.Printf("\nKTLOG %s\n", strings.Join(windows.log(), ","))
	}
	os.Exit(code)
}

// childOnly skips a child test outside a child run, where keploy is off and
// it would test nothing.
func childOnly(t *testing.T) {
	t.Helper()
	if os.Getenv(envChild) != "1" {
		t.Skip("runs only as a child of this package's tests")
	}
}

// ---------------------------------------------------------------------------
// The child's record of user code: act opens a window; two windows open at
// once log an overlap.

type windowLog struct {
	mu      sync.Mutex
	open    map[string]bool
	entries []string
}

var windows = &windowLog{open: map[string]bool{}}

func act(name string) func() {
	windows.mu.Lock()
	for o := range windows.open {
		windows.entries = append(windows.entries, "OVL("+o+"|"+name+")")
	}
	windows.open[name] = true
	windows.entries = append(windows.entries, name)
	windows.mu.Unlock()
	return func() {
		windows.mu.Lock()
		delete(windows.open, name)
		windows.mu.Unlock()
	}
}

func (w *windowLog) log() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.entries...)
}

// work yields often, so tests that are not kept apart interleave.
func work() {
	for i := 0; i < 2000; i++ {
		runtime.Gosched()
	}
}

// ---------------------------------------------------------------------------
// The fake agent.

type call struct {
	path    string
	name    string
	outcome string
	canSkip bool
	auth    string
}

func (c call) String() string {
	op := strings.TrimPrefix(c.path, "/agent/scope/")
	if c.outcome != "" {
		return op + " " + c.name + " " + c.outcome
	}
	return op + " " + c.name
}

type reply struct {
	status int
	body   string
}

var (
	ok   = reply{200, `{"v":1,"status":"ok"}`}
	skip = func(reason string) reply {
		b, _ := json.Marshal(map[string]any{"v": 1, "status": "ok", "action": "skip", "reason": reason})
		return reply{200, string(b)}
	}
	fail = func(reason string) reply {
		b, _ := json.Marshal(map[string]any{"v": 1, "status": "ok", "action": "fail", "reason": reason})
		return reply{200, string(b)}
	}
)

// fakeAgent answers begin and end like keploy's agent: one open test at a
// time (a begin while another test is open is refused with 422), a skip or a
// scripted answer per test name, and ok otherwise.
type fakeAgent struct {
	mu    sync.Mutex
	calls []call
	open  string
	begin map[string]reply
	end   map[string]reply
	all   *reply // answers every begin and end when set
	url   string
	// claims are the claims made, as "claim <key>"; claim answers them,
	// with a v1 claim answer when unset.
	claims []string
	claim  *reply
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	a := &fakeAgent{begin: map[string]reply{}, end: map[string]reply{}}
	srv := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(srv.Close)
	a.url = srv.URL
	return a
}

func (a *fakeAgent) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Name    string `json:"name"`
		Outcome string `json:"outcome"`
		CanSkip bool   `json:"canSkip"`
	}
	_ = json.Unmarshal(raw, &body)
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.URL.Path == pathInstance {
		var c instanceReq
		_ = json.Unmarshal(raw, &c)
		a.claims = append(a.claims, "claim "+c.Key+" auth="+r.Header.Get("Authorization"))
		rep := reply{200, `{"v":1,"instance":"run","timeFile":""}`}
		if a.claim != nil {
			rep = *a.claim
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
		return
	}
	a.calls = append(a.calls, call{path: r.URL.Path, name: body.Name, outcome: body.Outcome, canSkip: body.CanSkip, auth: r.Header.Get("Authorization")})
	rep := ok
	switch r.URL.Path {
	case pathBegin:
		s, scripted := a.begin[body.Name]
		switch {
		case a.open != "":
			// The agent's own words (keploy pkg/service/agent/scope_proven.go).
			b, _ := json.Marshal(map[string]string{
				"error":   "one_test_at_a_time",
				"message": body.Name + " began while " + a.open + " was still open: two tests ran at once in one test process",
				"remedy":  "run them one at a time: start parallel tests with keploytest.Parallel and subtests with keploytest.Run (keploy mock codemod go writes them); parallel test workers arrive in a later keploy release",
			})
			rep = reply{422, string(b)}
		case scripted:
			rep = s
			if opens(s) {
				a.open = body.Name
			}
		default:
			a.open = body.Name
		}
	case pathEnd:
		if a.open == body.Name {
			a.open = ""
		}
		if s, scripted := a.end[body.Name]; scripted {
			rep = s
		}
	default:
		rep = reply{404, `{"error":"not_found","message":"no such route"}`}
	}
	if a.all != nil {
		rep = *a.all
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	_, _ = io.WriteString(w, rep.body)
}

// opens reports whether a begin answer opens the test: a v1 "run".
func opens(r reply) bool {
	var a answer
	return r.status == 200 && json.Unmarshal([]byte(r.body), &a) == nil &&
		a.V != nil && *a.V == 1 && a.Status == "ok" && a.Action == ""
}

// onBegin, onEnd and onAll script the agent's answers.
func (a *fakeAgent) onBegin(name string, r reply) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.begin[name] = r
}

func (a *fakeAgent) onEnd(name string, r reply) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.end[name] = r
}

func (a *fakeAgent) onAll(r reply) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.all = &r
}

func (a *fakeAgent) got() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.calls))
	for i, c := range a.calls {
		out[i] = c.String()
	}
	return out
}

// active is the environment keploy mock record|replay gives its test
// command: the agent, and the project (here the file system's root, so every
// child's directory is inside it).
func (a *fakeAgent) active() map[string]string {
	return map[string]string{envAgent: a.url, envProject: "/"}
}

func (a *fakeAgent) claimed() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.claims...)
}

// ---------------------------------------------------------------------------
// Running the child.

type childRun struct {
	out  string
	code int
}

// runChild runs the child tests run (a -test.run pattern) with env added to a
// clean copy of this process's environment, in dir ("" is a temp directory).
func runChild(t *testing.T, env map[string]string, dir, run string, args ...string) childRun {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	argv := []string{"-test.run=" + run, "-test.v", "-test.count=1", "-test.parallel=1048576"}
	if deadline, has := t.Deadline(); has {
		// A child that hangs (a deadlock is this package's worst failure)
		// dumps its goroutines within the parent's own budget, so the
		// parent shows the dump instead of dying first.
		argv = append(argv, "-test.timeout="+(time.Until(deadline)*9/10).String())
	}
	argv = append(argv, args...)
	cmd := childCmd(env, dir, argv)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, isExit := err.(*exec.ExitError)
		if !isExit {
			t.Fatalf("running the child: %v\n%s", err, out)
		}
		code = ee.ExitCode()
	}
	return childRun{out: string(out), code: code}
}

// runChildArgs runs the child with exactly argv, in a temp directory, and
// returns its output.
func runChildArgs(t *testing.T, env map[string]string, argv []string) string {
	t.Helper()
	out, _ := childCmd(env, t.TempDir(), argv).CombinedOutput()
	return string(out)
}

// childCmd is this test binary run as a child in dir with argv: none of the
// parent's keploy variables or GOFLAGS, the child marker, and env.
func childCmd(env map[string]string, dir string, argv []string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], argv...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "KEPLOY") || strings.HasPrefix(kv, "GOFLAGS=") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, envChild+"=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

// status is PASS, FAIL or SKIP for the named test, "" when it did not report.
func (c childRun) status(name string) string {
	for _, line := range strings.Split(c.out, "\n") {
		line = strings.TrimSpace(line)
		for _, s := range []string{"PASS", "FAIL", "SKIP"} {
			if strings.HasPrefix(line, "--- "+s+": "+name+" (") {
				return s
			}
		}
	}
	return ""
}

func (c childRun) ktlog() string {
	for _, line := range strings.Split(c.out, "\n") {
		if l, found := strings.CutPrefix(line, "KTLOG "); found {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func (c childRun) want(t *testing.T, name, status string, says ...string) {
	t.Helper()
	if got := c.status(name); got != status {
		t.Errorf("%s: %q, want %s\n%s", name, got, status, c.out)
	}
	for _, s := range says {
		if !strings.Contains(c.out, s) {
			t.Errorf("the output does not say %q\n%s", s, c.out)
		}
	}
}

func wantCalls(t *testing.T, a *fakeAgent, want ...string) {
	t.Helper()
	got := a.got()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("agent calls:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// ---------------------------------------------------------------------------
// The child tests.

// TestChildBody does what KEPLOYTEST_BODY says, after its first line.
func TestChildBody(t *testing.T) {
	childOnly(t)
	defer T(t)()
	fmt.Println(bodyRan)
	switch os.Getenv(envBody) {
	case "fail":
		t.Error("its own assertion failed")
	case "skip":
		t.Skip("it skipped itself")
	case "panic":
		panic("boom")
	case "panic-nil":
		panic(nil)
	case "cleanup-panic":
		t.Cleanup(func() { panic("cleanup boom") })
	case "panic-cleanup-skip":
		// t.Skip in a cleanup during a panic ends the panic: Go reports
		// SKIP and the run goes on.
		t.Cleanup(func() { t.Skip("a cleanup skipped it") })
		panic("boom")
	case "raw-goexit":
		runtime.Goexit()
	case "recover-skip":
		// t.Skip during a panic ends that panic (Goexit): the test is
		// skipped, not failed.
		defer func() {
			if recover() != nil {
				t.Skip("recovered, then skipped")
			}
		}()
		panic("boom")
	case "fatal-then-skip":
		t.Error("its own assertion failed")
		t.Skip("and then it skipped")
	}
}

func TestChildBody2(t *testing.T) {
	childOnly(t)
	defer T(t)()
	fmt.Println(bodyRan)
}

// TestChildClock prints the clock file its process runs on, and the one a
// process it starts inherits.
func TestChildClock(t *testing.T) {
	childOnly(t)
	defer T(t)()
	fmt.Printf("CLOCK-SELF=%s\n", os.Getenv(envTimeFile))
	out, err := exec.Command("sh", "-c", `printf '%s' "$`+envTimeFile+`"`).Output()
	if err != nil {
		t.Fatalf("starting a process: %v", err)
	}
	fmt.Printf("CLOCK-CHILD=%s\n", out)
}

// Inert, Parallel and Run are t.Parallel and t.Run: with -test.parallel=4
// they would refuse if they were not.
func TestChildInert(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	Run(t, "sub", func(t *testing.T) { Parallel(t) })
	fmt.Println(bodyRan)
}

// Two top-level tests that go parallel through t.Parallel: the second one
// begins while the first is open.
func TestChildRawParA(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Parallel()
	work()
}

func TestChildRawParB(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Parallel()
	work()
}

func TestChildRawThenTPRaw(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Parallel()
	work()
}

func TestChildRawThenTPTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	fmt.Println(bodyRan)
	work()
}

func TestChildTPThenRawTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	fmt.Println(bodyRan)
	work()
}

func TestChildTPThenRawRaw(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Parallel()
	work()
}

// lateOpen is closed once TestChildLateOpenTP has begun; lateTDone and
// lateTPDone once TestChildLateT and TestChildLateTP have ended. The three
// run together, once (TestAKeploytestLineAfterTParallelBeginsOutsideTheTurns).
var lateOpen, lateTDone, lateTPDone = make(chan struct{}), make(chan struct{}), make(chan struct{})

// A T line after t.Parallel(): the test reaches it once Go resumes it, here
// while a parallel test is open.
func TestChildLateT(t *testing.T) {
	childOnly(t)
	t.Parallel()
	<-lateOpen
	t.Cleanup(func() { close(lateTDone) }) // runs after T's exit cleanup
	defer T(t)()
	work()
}

func TestChildLateTP(t *testing.T) {
	childOnly(t)
	t.Parallel()
	<-lateOpen
	t.Cleanup(func() { close(lateTPDone) })
	defer TP(t)()
	work()
}

func TestChildLateOpenTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	close(lateOpen)
	<-lateTDone
	<-lateTPDone
	fmt.Println(bodyRan)
}

// savedT is the t of a test that has ended.
var savedT *testing.T

func TestChildSaveT(t *testing.T) { childOnly(t); savedT = t }

// A T test that another test's SkipNow ends: Go ends the process.
func TestChildOthersSkipNow(t *testing.T) {
	childOnly(t)
	defer T(t)()
	savedT.SkipNow()
}

// A parallel subtest whose cleanup fails its parent through the parent's t,
// after its own function returned: Go fails the parent and goes on with the
// run.
func TestChildCleanupFailsParent(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	Run(t, "p", func(c *testing.T) {
		Parallel(c)
		c.Cleanup(func() { t.Fatal("a cleanup failed the parent through its t") })
	})
	Run(t, "q", func(c *testing.T) {
		Parallel(c)
		fmt.Println("Q-RAN")
	})
}

func TestChildCleanupFailsParent2(t *testing.T) { childOnly(t); next(t) }

// endAs ends a test as KEPLOYTEST_BODY says: its function returns (""),
// or its own SkipNow ("skip") or FailNow ("fail") ends it.
func endAs(t *testing.T) {
	switch os.Getenv(envBody) {
	case "skip":
		t.Skip("it skipped itself")
	case "fail":
		t.Fatal("its own fatal")
	}
}

// A raw Goexit in a cleanup, after package testing took the test as
// finished: Go reports the test as it stands and goes on with the run. The
// TP test registers its cleanup before Parallel, so it also runs after
// keploy's own skip there.
func TestChildCleanupGoexitT(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Cleanup(func() { runtime.Goexit() })
	endAs(t)
}

func TestChildCleanupGoexitTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	t.Cleanup(func() { runtime.Goexit() })
	Parallel(t)
	endAs(t)
}

func TestChildCleanupGoexitNext(t *testing.T) { childOnly(t); next(t) }

func TestChildCleanupGoexitSub(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	Run(t, "p", func(c *testing.T) {
		Parallel(c)
		c.Cleanup(func() { runtime.Goexit() })
		endAs(c)
	})
	Run(t, "q", func(c *testing.T) {
		Parallel(c)
		fmt.Println("Q-RAN")
	})
}

func TestChildRawTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	t.Parallel()
	defer act(t.Name())()
	work()
}

func TestChildGoodTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	fmt.Println(bodyRan)
	defer act(t.Name())()
	work()
}

func TestChildRawPanicT(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Parallel()
	panic(slowError{})
}

func TestChildRawPanicTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	fmt.Println(bodyRan)
}

func TestChildFailedThenParentFatal(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	Run(t, "p", func(c *testing.T) {
		Parallel(c)
		c.Error("its own failure first")
		t.Fatal("then the parent's t")
	})
	Run(t, "q", func(c *testing.T) {
		Parallel(c)
		fmt.Println("Q-RAN")
	})
}

func leaf(t *testing.T) {
	Parallel(t)
	defer act(t.Name())()
	work()
}

// The mixed-depth shape: a parallel subtest declared before a sequential
// group whose own subtests are parallel.
func TestChildOrderMix(t *testing.T) {
	childOnly(t)
	defer T(t)()
	windows.mu.Lock()
	windows.entries = append(windows.entries, t.Name())
	windows.mu.Unlock()
	Run(t, "p1", leaf)
	Run(t, "g", func(t *testing.T) {
		Run(t, "a", leaf)
		Run(t, "b", leaf)
	})
	Run(t, "p2", leaf)
	Run(t, "s", func(t *testing.T) {
		defer act(t.Name())()
		work()
	})
}

// A cleanup registered before Parallel still runs inside the test's turn.
func TestChildOrderTP1(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	t.Cleanup(func() { defer act("TestChildOrderTP1/preCleanup")(); work() })
	Parallel(t)
	defer act(t.Name())()
	work()
}

func TestChildOrderTPSkip(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	defer act(t.Name())()
	work()
}

func TestChildOrderTP2(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	defer act(t.Name())()
	work()
}

func TestChildOrderTP3(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	Run(t, "x", leaf)
	Run(t, "y", leaf)
	defer act(t.Name())()
	work()
}

// slowError is a panic value whose message takes long to render: Go renders
// it after the cleanups and before the process ends, while other goroutines
// still run. A test released in that window would begin and run.
type slowError struct{}

func (slowError) Error() string {
	for i := 0; i < 500000; i++ {
		runtime.Gosched()
	}
	return "slow boom"
}

func TestChildPanicTP1(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	panic(slowError{})
}

func TestChildPanicTP2(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	fmt.Println(bodyRan)
}

// A panic that a cleanup's t.Fatal ends: Go reports FAIL and goes on, so the
// next parallel test must get its turn.
func TestChildPanicFatalTP1(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	t.Cleanup(func() { t.Fatal("a cleanup failed it") })
	Parallel(t)
	panic("boom")
}

// next is the parallel test that follows a TP test that ends badly.
func next(t *testing.T) {
	defer TP(t)()
	Parallel(t)
	fmt.Println(bodyRan)
}

func TestChildPanicFatalTP2(t *testing.T) { childOnly(t); next(t) }

func TestChildGoexitTP1(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	runtime.Goexit()
}

func TestChildGoexitTP2(t *testing.T) { childOnly(t); next(t) }

func TestChildParallelFlag(t *testing.T) {
	childOnly(t)
	fmt.Printf("PARALLEL=%s\n", flag.Lookup("test.parallel").Value.String())
}

// A subtest that calls FailNow on its parent's t: t.Run aborts the parent
// through runtime.Goexit, and Go goes on with the run.
func TestChildParentFatalTP1(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	Run(t, "sub", func(*testing.T) { t.Fatal("the subtest failed its parent") })
}

func TestChildParentFatalTP2(t *testing.T) { childOnly(t); next(t) }

func TestChildCapturedFatalTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	Run(t, "p", func(c *testing.T) {
		Parallel(c)
		t.Fatal("a parallel subtest failed its parent through the parent's t")
	})
	Run(t, "q", func(c *testing.T) {
		Parallel(c)
		fmt.Println("Q-RAN")
	})
}

// A TP test that skips itself before Parallel never begins.
func TestChildSkipBeforeParallel(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	t.Skip("skipped before Parallel")
	Parallel(t)
}

func TestChildAfterSkip(t *testing.T) { childOnly(t); next(t) }

// A TP test that starts a subtest before Parallel: refused.
func TestChildRunBeforeParallel(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Run(t, "early", func(*testing.T) { fmt.Println(bodyRan) })
	Parallel(t)
}

// A TP test that fails after Parallel ends failed, and gives up its turn.
func TestChildFatalAfterParallel(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	t.Fatal("failed after Parallel")
}

func TestChildAfterFatal(t *testing.T) { childOnly(t); next(t) }

// A parallel subtest that panics: its test ends failed, and nothing after it
// runs user code while the process goes down.
func TestChildPanickingSubtest(t *testing.T) {
	childOnly(t)
	defer T(t)()
	Run(t, "p", func(t *testing.T) { Parallel(t); panic("subtest boom") })
	Run(t, "q", func(t *testing.T) { Parallel(t); fmt.Println(bodyRan) })
}

func TestChildGuardParallelInT(t *testing.T) {
	childOnly(t)
	defer T(t)()
	Parallel(t)
}

func TestChildGuardTPWithoutParallel(t *testing.T) {
	childOnly(t)
	defer TP(t)()
}

func TestChildGuardNoFirstLine(t *testing.T) {
	childOnly(t)
	Parallel(t)
}

func TestChildGuardRunWithoutFirstLine(t *testing.T) {
	childOnly(t)
	Run(t, "x", func(*testing.T) {})
}

func TestChildGuardRawRun(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Run("x", func(t *testing.T) { Parallel(t) })
}

func TestChildGuardRawParallel(t *testing.T) {
	childOnly(t)
	defer T(t)()
	Run(t, "x", func(t *testing.T) { t.Parallel() })
}

func TestChildGuardOtherGoroutine(t *testing.T) {
	childOnly(t)
	defer T(t)()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(t, "x", func(*testing.T) {})
	}()
	<-done
}

func TestChildGuardLowParallel(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
}

func TestChildGuardTInSubtest(t *testing.T) {
	childOnly(t)
	defer T(t)()
	Run(t, "x", func(t *testing.T) { defer T(t)() })
}

func TestChildGuardRunInRawRun(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Run("x", func(t *testing.T) { Run(t, "y", func(*testing.T) {}) })
}

func TestChildGuardTPRawParallel(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	t.Parallel()
}

func TestChildGuardTwice(t *testing.T) {
	childOnly(t)
	defer T(t)()
	defer T(t)()
}

// ---------------------------------------------------------------------------
// The parent tests.

func TestAQuarantinedTestIsSkippedWithTheReasonAndNoEnd(t *testing.T) {
	a := newFakeAgent(t)
	reason := "keploy: quarantined in keploy/t2-go/verdicts.yaml: flaky/replay_only"
	a.onBegin("TestChildBody", skip(reason))
	c := runChild(t, a.active(), "", "^TestChildBody$")
	c.want(t, "TestChildBody", "SKIP", reason)
	if strings.Contains(c.out, bodyRan) {
		t.Errorf("the body of a skipped test ran\n%s", c.out)
	}
	wantCalls(t, a, "begin TestChildBody")
	if c.code != 0 {
		t.Errorf("exit %d, want 0", c.code)
	}
}

func TestBeginSaysItCanSkip(t *testing.T) {
	a := newFakeAgent(t)
	runChild(t, a.active(), "", "^TestChildBody$")
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) == 0 || !a.calls[0].canSkip {
		t.Fatalf("begin calls %+v, want canSkip", a.calls)
	}
}

func TestKeployFailsATestWithItsReason(t *testing.T) {
	a := newFakeAgent(t)
	reason := "keploy: recorded call POST /audit was never made. If this is an intended change, re-record it"
	a.onEnd("TestChildBody", fail(reason))
	c := runChild(t, a.active(), "", "^TestChildBody$")
	c.want(t, "TestChildBody", "FAIL", reason, bodyRan)
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody passed")
	if c.code != 1 {
		t.Errorf("exit %d, want 1", c.code)
	}
}

// keploy's failure is reported at the test's own keploytest line, not inside
// this package.
func TestKeploysFailureIsReportedAtTheTestsLine(t *testing.T) {
	a := newFakeAgent(t)
	a.onEnd("TestChildBody", fail("keploy: a call had no recorded answer"))
	c := runChild(t, a.active(), "", "^TestChildBody$")
	c.want(t, "TestChildBody", "FAIL")
	line := lineOf(t, "func TestChildBody(", "\tdefer T(t)()")
	if want := fmt.Sprintf("keploytest_test.go:%d: keploy: a call had no recorded answer", line); !strings.Contains(c.out, want) {
		t.Errorf("the failure is not reported at the test's keploytest line (%s):\n%s", want, c.out)
	}
	if strings.Contains(c.out, "keploytest.go:") {
		t.Errorf("the failure is reported inside the package:\n%s", c.out)
	}
}

// lineOf is the line number, in this file, of the first line starting with
// line after the line starting with after.
func lineOf(t *testing.T, after, line string) int {
	t.Helper()
	raw, err := os.ReadFile("keploytest_test.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for i, l := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(l, after):
			seen = true
		case seen && strings.HasPrefix(l, line):
			return i + 1
		}
	}
	t.Fatalf("no %q after %q in keploytest_test.go", line, after)
	return 0
}

func TestTheOutcomeIsReported(t *testing.T) {
	for _, tc := range []struct{ body, outcome, status string }{
		{"", "passed", "PASS"},
		{"fail", "failed", "FAIL"},
		{"skip", "skipped", "SKIP"},
		// Go reports a test that failed and then skipped as failed.
		{"fatal-then-skip", "failed", "FAIL"},
	} {
		t.Run(tc.outcome+tc.body, func(t *testing.T) {
			a := newFakeAgent(t)
			env := a.active()
			env[envBody] = tc.body
			c := runChild(t, env, "", "^TestChildBody$")
			c.want(t, "TestChildBody", tc.status, bodyRan)
			wantCalls(t, a, "begin TestChildBody", "end TestChildBody "+tc.outcome)
		})
	}
}

func TestAPanicOfNilIsReportedFailedAndNotSwallowed(t *testing.T) {
	// A module whose go.mod says go < 1.21 gets panicnil=1: recover() then
	// returns nil for panic(nil), and a recover would swallow it.
	a := newFakeAgent(t)
	env := a.active()
	env[envBody] = "panic-nil"
	env["GODEBUG"] = "panicnil=1"
	c := runChild(t, env, "", "^TestChildBody$")
	if c.code == 0 || c.status("TestChildBody") == "PASS" {
		t.Errorf("exit %d, status %q: the panic was swallowed\n%s", c.code, c.status("TestChildBody"), c.out)
	}
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody failed")
}

func TestAPanicInACleanupIsReportedFailed(t *testing.T) {
	a := newFakeAgent(t)
	env := a.active()
	env[envBody] = "cleanup-panic"
	c := runChild(t, env, "", "^TestChildBody$")
	if !strings.Contains(c.out, "cleanup boom") || c.code == 0 {
		t.Errorf("exit %d, want the cleanup's panic\n%s", c.code, c.out)
	}
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody failed")
}

func TestARecoveredPanicThenSkipIsSkipped(t *testing.T) {
	a := newFakeAgent(t)
	env := a.active()
	env[envBody] = "recover-skip"
	c := runChild(t, env, "", "^TestChildBody$")
	c.want(t, "TestChildBody", "SKIP", "recovered, then skipped")
	if c.code != 0 {
		t.Errorf("exit %d, want 0", c.code)
	}
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody skipped")
}

func TestAPanickingTestKeepsItsTurn(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildPanicTP[12]$")
	if !strings.Contains(c.out, "slow boom") || c.code == 0 {
		t.Errorf("exit %d, want the panic\n%s", c.code, c.out)
	}
	if strings.Contains(c.out, bodyRan) {
		t.Errorf("the next parallel test ran while the panicking one went down\n%s", c.out)
	}
	wantCalls(t, a, "begin TestChildPanicTP1", "end TestChildPanicTP1 failed")
}

func TestAPanicEndedByACleanupsFatalPassesTheTurn(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildPanicFatalTP[12]$")
	c.want(t, "TestChildPanicFatalTP1", "FAIL", "a cleanup failed it")
	c.want(t, "TestChildPanicFatalTP2", "PASS", bodyRan)
	wantCalls(t, a, "begin TestChildPanicFatalTP1", "end TestChildPanicFatalTP1 failed",
		"begin TestChildPanicFatalTP2", "end TestChildPanicFatalTP2 passed")
	if c.code != 1 {
		t.Errorf("exit %d, want 1", c.code)
	}
}

func TestAPanicEndedByACleanupsSkipIsSkipped(t *testing.T) {
	a := newFakeAgent(t)
	env := a.active()
	env[envBody] = "panic-cleanup-skip"
	c := runChild(t, env, "", "^TestChildBody$")
	c.want(t, "TestChildBody", "SKIP", "a cleanup skipped it")
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody skipped")
}

func TestARawGoexitIsReportedFailed(t *testing.T) {
	a := newFakeAgent(t)
	env := a.active()
	env[envBody] = "raw-goexit"
	c := runChild(t, env, "", "^TestChildBody$")
	if c.code == 0 || !strings.Contains(c.out, "runtime.Goexit") {
		t.Errorf("exit %d, want Go's Goexit failure\n%s", c.code, c.out)
	}
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody failed")
}

func TestARawGoexitKeepsTheTurn(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildGoexitTP[12]$")
	if c.code == 0 || strings.Contains(c.out, bodyRan) {
		t.Errorf("exit %d; the next test must not run while the process goes down\n%s", c.code, c.out)
	}
	wantCalls(t, a, "begin TestChildGoexitTP1", "end TestChildGoexitTP1 failed")
}

// A subtest that fails its parent through the parent's t (t.Run's own
// Goexit) is not a dying test: the parent ends failed, gives up its turn, and
// the next parallel test runs. It hung until -test.timeout when the turn was
// kept.
func TestASubtestFailingItsParentGivesUpTheTurn(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildParentFatalTP[12]$")
	if c.code != 1 || !strings.Contains(c.out, bodyRan) || strings.Contains(c.out, "timed out") {
		t.Errorf("exit %d; want the next test to run and the binary to fail\n%s", c.code, c.out)
	}
	wantCalls(t, a, "begin TestChildParentFatalTP1", "end TestChildParentFatalTP1 failed",
		"begin TestChildParentFatalTP2", "end TestChildParentFatalTP2 passed")
}

// A TP test that calls t.Parallel() itself, never keploytest.Parallel, runs
// outside the turns: a parallel test's turn waits for it, so the two never
// overlap, and it alone fails, by name.
func TestARawParallelTPTestNeverOverlapsAParallelTest(t *testing.T) {
	for i := 0; i < 5; i++ {
		a := newFakeAgent(t)
		c := runChild(t, a.active(), "", "^TestChild(Raw|Good)TP$")
		c.want(t, "TestChildRawTP", "FAIL", "keploy: TestChildRawTP starts with keploytest.TP but ended without calling keploytest.Parallel; call keploytest.Parallel(t), not t.Parallel()")
		c.want(t, "TestChildGoodTP", "PASS", bodyRan)
		if strings.Contains(c.ktlog(), "OVL(") {
			t.Fatalf("the raw parallel test overlapped the parallel test: %s\n%s", c.ktlog(), c.out)
		}
		wantCalls(t, a, "begin TestChildGoodTP", "end TestChildGoodTP passed")
	}
}

// A T test that calls t.Parallel() itself and then panics stays outside the
// turns while the process goes down: the parallel test waiting for it never
// runs its body.
func TestARawParallelTTestThatPanicsHoldsTheTurns(t *testing.T) {
	for i := 0; i < 5; i++ {
		a := newFakeAgent(t)
		c := runChild(t, a.active(), "", "^TestChildRawPanicTP?$")
		if c.code == 0 || strings.Contains(c.out, bodyRan) {
			t.Fatalf("exit %d; the parallel test ran while the process went down\n%s", c.code, c.out)
		}
		wantCalls(t, a, "begin TestChildRawPanicT", "end TestChildRawPanicT failed")
	}
}

// A parallel subtest that failed itself and then fails its parent through
// the parent's t ends the process. Package testing's exported API does not
// say which t ended it, so it is taken for one its own FailNow ended, and
// gives up its turn: its sibling may run while the process goes down. The
// run fails all the same: the binary exits non-zero, and Go runs the
// parent's cleanups on the dying subtest's goroutine, so the test ends
// failed on the agent.
func TestAFailedParallelSubtestFailingItsParentFailsTheRun(t *testing.T) {
	for i := 0; i < 5; i++ {
		a := newFakeAgent(t)
		c := runChild(t, a.active(), "", "^TestChildFailedThenParentFatal$")
		if c.code == 0 || c.status("TestChildFailedThenParentFatal") == "PASS" || strings.Contains(c.out, "timed out") {
			t.Fatalf("exit %d; want the process to end with the failure, without a hang\n%s", c.code, c.out)
		}
		wantCalls(t, a, "begin TestChildFailedThenParentFatal", "end TestChildFailedThenParentFatal failed")
	}
}

// A Goexit in a cleanup of a test package testing takes as finished (its
// function returned, or its own Skip or Fatal ended it) does not end the
// process: Go reports the test and goes on. Such a test gives up its turn
// (it hung the next test until -test.timeout when it kept it), and keploy
// gets the outcome Go reports. The parent's t failed from a subtest's
// cleanup; a raw runtime.Goexit in a T test, a TP test and a parallel
// subtest, after each way of finishing, and after keploy's own skip.
func TestAGoexitInACleanupOfAFinishedTestGivesUpTheTurn(t *testing.T) {
	t.Run("parent's FailNow", func(t *testing.T) {
		a := newFakeAgent(t)
		c := runChild(t, a.active(), "", "^TestChildCleanupFailsParent2?$")
		c.want(t, "TestChildCleanupFailsParent", "FAIL", "a cleanup failed the parent through its t", "Q-RAN")
		c.want(t, "TestChildCleanupFailsParent/p", "PASS")
		c.want(t, "TestChildCleanupFailsParent/q", "PASS")
		c.want(t, "TestChildCleanupFailsParent2", "PASS", bodyRan)
		if c.code != 1 || strings.Contains(c.out, "timed out") {
			t.Errorf("exit %d, want 1 and no hang\n%s", c.code, c.out)
		}
		wantCalls(t, a, "begin TestChildCleanupFailsParent", "end TestChildCleanupFailsParent failed",
			"begin TestChildCleanupFailsParent2", "end TestChildCleanupFailsParent2 passed")
	})
	for _, tc := range []struct {
		// body is how each test ends (endAs), status and outcome what Go
		// reports and keploy hears; a failed subtest fails its parent.
		body, status, outcome, parent, parentOutcome string
		code                                         int
	}{
		{"", "PASS", "passed", "PASS", "passed", 0},
		{"skip", "SKIP", "skipped", "PASS", "passed", 0},
		{"fail", "FAIL", "failed", "FAIL", "failed", 1},
	} {
		t.Run("raw Goexit after "+tc.outcome, func(t *testing.T) {
			a := newFakeAgent(t)
			env := a.active()
			env[envBody] = tc.body
			c := runChild(t, env, "", "^TestChildCleanupGoexit(T|TP|Next|Sub)$")
			for _, n := range []string{"TestChildCleanupGoexitT", "TestChildCleanupGoexitTP", "TestChildCleanupGoexitSub/p"} {
				c.want(t, n, tc.status)
			}
			c.want(t, "TestChildCleanupGoexitSub", tc.parent)
			c.want(t, "TestChildCleanupGoexitSub/q", "PASS", "Q-RAN")
			c.want(t, "TestChildCleanupGoexitNext", "PASS", bodyRan)
			if c.code != tc.code || strings.Contains(c.out, "timed out") {
				t.Errorf("exit %d, want %d and no hang\n%s", c.code, tc.code, c.out)
			}
			wantCalls(t, a, "begin TestChildCleanupGoexitT", "end TestChildCleanupGoexitT "+tc.outcome,
				"begin TestChildCleanupGoexitTP", "end TestChildCleanupGoexitTP "+tc.outcome,
				"begin TestChildCleanupGoexitNext", "end TestChildCleanupGoexitNext passed",
				"begin TestChildCleanupGoexitSub", "end TestChildCleanupGoexitSub "+tc.parentOutcome)
		})
	}
	t.Run("raw Goexit after keploy's skip", func(t *testing.T) {
		a := newFakeAgent(t)
		a.onBegin("TestChildCleanupGoexitTP", skip("keploy: quarantined, see verdicts.yaml"))
		c := runChild(t, a.active(), "", "^TestChildCleanupGoexit(TP|Next)$")
		c.want(t, "TestChildCleanupGoexitTP", "SKIP", "keploy: quarantined, see verdicts.yaml")
		c.want(t, "TestChildCleanupGoexitNext", "PASS", bodyRan)
		if c.code != 0 || strings.Contains(c.out, "timed out") {
			t.Errorf("exit %d, want 0 and no hang\n%s", c.code, c.out)
		}
		wantCalls(t, a, "begin TestChildCleanupGoexitTP", "begin TestChildCleanupGoexitNext", "end TestChildCleanupGoexitNext passed")
	})
}

// The package reads no unexported state of package testing: no reflection,
// unsafe or linkname into testing's own fields, which a Go release may
// rename or drop (design v14 §6.5, "no reflection").
func TestThePackageUsesOnlyTestingsExportedAPI(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "reflect" || p == "unsafe" {
				t.Errorf("%s imports %s", fset.Position(imp.Pos()), p)
			}
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.HasPrefix(c.Text, "//go:linkname") {
					t.Errorf("%s: %s", fset.Position(c.Pos()), c.Text)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source file of the package was checked")
	}
}

// A parallel subtest that fails its parent through the parent's t makes Go
// end the process: the subtest and its parent keep their turns, so its
// sibling never starts while the process goes down.
func TestAParallelSubtestFailingItsParentKeepsTheTurns(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildCapturedFatalTP$")
	if c.code == 0 || strings.Contains(c.out, "Q-RAN") || strings.Contains(c.out, "timed out") {
		t.Errorf("exit %d; the sibling ran, or the binary hung, while the process went down\n%s", c.code, c.out)
	}
	wantCalls(t, a, "begin TestChildCapturedFatalTP", "end TestChildCapturedFatalTP failed")
}

// exit keeps the turn of a test a raw Goexit ends before package testing
// takes it as finished (the process goes down with it), and gives up the
// turn of one testing takes as finished, also when a cleanup's raw Goexit
// follows (Go goes on).
func TestExitKeepsTheTurnOnlyOfADyingTest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		finished bool
		end      func()
		kept     bool
	}{
		{"a raw Goexit", false, runtime.Goexit, true},
		{"a normal return", false, func() {}, false},
		{"a raw Goexit after the test finished", true, runtime.Goexit, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &queue{}
			_, _ = q.take()
			st := &tstate{q: q, finished: tc.finished}
			before := q.serving
			done := make(chan struct{})
			go func() {
				defer close(done)
				st.gid = gid() // the test's own goroutine
				defer exit(t, st)
				tc.end()
			}()
			<-done
			if kept := q.serving == before; kept != tc.kept {
				t.Fatalf("turn kept = %v, want %v", kept, tc.kept)
			}
		})
	}
}

func TestATPTestSkippedBeforeParallelNeverBegins(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChild(SkipBeforeParallel|AfterSkip)$")
	c.want(t, "TestChildSkipBeforeParallel", "SKIP", "skipped before Parallel")
	c.want(t, "TestChildAfterSkip", "PASS", bodyRan)
	wantCalls(t, a, "begin TestChildAfterSkip", "end TestChildAfterSkip passed")
}

func TestASubtestBeforeParallelInATPTestIsRefused(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildRunBeforeParallel$")
	c.want(t, "TestChildRunBeforeParallel", "FAIL", "before keploytest.Parallel")
	if strings.Contains(c.out, bodyRan) {
		t.Errorf("the early subtest ran:\n%s", c.out)
	}
}

func TestAFatalAfterParallelEndsFailedAndTheNextRuns(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChild(FatalAfterParallel|AfterFatal)$")
	c.want(t, "TestChildFatalAfterParallel", "FAIL", "failed after Parallel")
	c.want(t, "TestChildAfterFatal", "PASS", bodyRan)
	wantCalls(t, a, "begin TestChildFatalAfterParallel", "end TestChildFatalAfterParallel failed",
		"begin TestChildAfterFatal", "end TestChildAfterFatal passed")
}

func TestAPanickingParallelSubtestFailsItsTest(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildPanickingSubtest$")
	if c.code == 0 || !strings.Contains(c.out, "subtest boom") || strings.Contains(c.out, bodyRan) {
		t.Errorf("exit %d; want the panic and no later subtest\n%s", c.code, c.out)
	}
	wantCalls(t, a, "begin TestChildPanickingSubtest", "end TestChildPanickingSubtest failed")
}

// -failfast with serialized parallel tests ends, failing, as in plain Go:
// tests that already paused still run (Go releases them), one at a time, and
// nothing hangs.
func TestFailfastEndsWithoutAHang(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChild(FatalAfterParallel|AfterFatal)$", "-test.failfast")
	if c.code != 1 || strings.Contains(c.out, "timed out") {
		t.Errorf("exit %d; want 1 and no hang\n%s", c.code, c.out)
	}
	wantCalls(t, a, "begin TestChildFatalAfterParallel", "end TestChildFatalAfterParallel failed",
		"begin TestChildAfterFatal", "end TestChildAfterFatal passed")
}

func TestAPanicIsReportedFailed(t *testing.T) {
	a := newFakeAgent(t)
	env := a.active()
	env[envBody] = "panic"
	c := runChild(t, env, "", "^TestChildBody$")
	if !strings.Contains(c.out, "panic: boom") || c.code == 0 {
		t.Errorf("exit %d, want the panic\n%s", c.code, c.out)
	}
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody failed")
}

func TestOffIsInert(t *testing.T) {
	a := newFakeAgent(t)
	// Off wins over keploy's other variables.
	env := a.active()
	env[envAgent] = "off"
	c := runChild(t, env, "", "^TestChild(Body|Inert)$", "-test.parallel=4")
	c.want(t, "TestChildBody", "PASS", bodyRan)
	c.want(t, "TestChildInert", "PASS")
	wantCalls(t, a)
}

func TestNoVariableIsInert(t *testing.T) {
	c := runChild(t, nil, "", "^TestChild(Body|Inert)$", "-test.parallel=4")
	c.want(t, "TestChildBody", "PASS", bodyRan)
	c.want(t, "TestChildInert", "PASS")
}

// A run that names no project gives the tests no ID: every test with the
// line fails, and says why.
func TestARunWithoutAProjectFailsEveryTest(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, map[string]string{envAgent: a.url}, "", "^TestChildBody2?$")
	for _, n := range []string{"TestChildBody", "TestChildBody2"} {
		c.want(t, n, "FAIL", "did not name the project", envProject)
	}
	wantCalls(t, a)
}

// The test binary claims keploy's run once, before its first test, for its
// package directory relative to the project, with the run's token; the
// agent refusing the claim fails every test with its words.
func TestTheBinaryClaimsTheRunOnce(t *testing.T) {
	a := newFakeAgent(t)
	env := a.active()
	env[envToken] = "tok"
	dir := t.TempDir()
	c := runChild(t, env, dir, "^TestChildBody2?$")
	c.want(t, "TestChildBody", "PASS", bodyRan)
	want := "claim " + strings.TrimPrefix(realPath(dir), "/") + " auth=Bearer tok"
	if claims := a.claimed(); len(claims) != 1 || claims[0] != want {
		t.Fatalf("claims %v, want [%s]: one, for the child's directory relative to the project (/)", claims, want)
	}

	b := newFakeAgent(t)
	b.mu.Lock()
	b.claim = &reply{422, `{"error":"one_test_process_at_a_time","message":"two test processes ran at once","remedy":"go test -p 1"}`}
	b.mu.Unlock()
	c = runChild(t, b.active(), "", "^TestChildBody2?$")
	for _, n := range []string{"TestChildBody", "TestChildBody2"} {
		c.want(t, n, "FAIL", "two test processes ran at once", "go test -p 1")
	}
	wantCalls(t, b)
}

// The clock file keploy's claim answers with is exported into the test
// process's real environment, where every process it starts inherits it
// (release1-cut §2.2 MUST 2). A claim answered without one changes nothing:
// a value keploy put in the command's environment stays.
func TestTheClaimsClockIsExported(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to start a process with")
	}
	clock := filepath.Join(t.TempDir(), "kclk1", "t")
	a := newFakeAgent(t)
	a.mu.Lock()
	a.claim = &reply{200, `{"v":1,"instance":"run","timeFile":` + strconv.Quote(clock) + `}`}
	a.mu.Unlock()
	env := a.active()
	env[envTimeFile] = "/inherited/from/the/command"
	c := runChild(t, env, "", "^TestChildClock$")
	c.want(t, "TestChildClock", "PASS", "CLOCK-SELF="+clock+"\n", "CLOCK-CHILD="+clock+"\n")
	if len(a.claimed()) != 1 {
		t.Fatalf("claims %v, want one", a.claimed())
	}

	b := newFakeAgent(t)
	env = b.active()
	env[envTimeFile] = "/from/the/command"
	c = runChild(t, env, "", "^TestChildClock$")
	c.want(t, "TestChildClock", "PASS", "CLOCK-SELF=/from/the/command\n", "CLOCK-CHILD=/from/the/command\n")
}

// A clock file the package cannot export is a claim answer it cannot trust:
// every test fails, rather than run on a clock other than the run's.
func TestAClockFileThatCannotBeExportedFailsEveryTest(t *testing.T) {
	for _, tc := range []struct{ name, file, says string }{
		{"relative", "kclk1/t", `is not an absolute path`},
		{"NUL", "/tmp/kclk1/t\x00x", `cannot be exported`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newFakeAgent(t)
			body, _ := json.Marshal(map[string]any{"v": 1, "instance": "run", "timeFile": tc.file})
			a.mu.Lock()
			a.claim = &reply{200, string(body)}
			a.mu.Unlock()
			c := runChild(t, a.active(), "", "^TestChildBody2?$")
			for _, n := range []string{"TestChildBody", "TestChildBody2"} {
				c.want(t, n, "FAIL", tc.says)
			}
			wantCalls(t, a)
		})
	}
}

// inheritedClaim runs the child as keploy-go-exec will: a launcher that
// claims keploy's run for the test binary, exports KEPLOY_TEST_PID (its own
// pid), and execs the binary, which keeps the pid. A shell that exports the
// variable as $$ and execs does exactly that, minus the claim. Unlike
// runChild it leaves -test.parallel to the package, which must set it on
// this path too for Parallel to serialize.
func inheritedClaim(t *testing.T, env map[string]string, run string) childRun {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to exec the binary with")
	}
	argv := []string{"-test.run=" + run, "-test.v", "-test.count=1"}
	if deadline, has := t.Deadline(); has {
		// As in runChild: a hang dumps its goroutines within the parent's
		// own budget.
		argv = append(argv, "-test.timeout="+(time.Until(deadline)*9/10).String())
	}
	cmd := childCmd(env, t.TempDir(), argv)
	cmd.Path = sh
	cmd.Args = append([]string{"sh", "-c", "export " + envTestPID + `=$$; exec "$0" "$@"`, os.Args[0]}, argv...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, isExit := err.(*exec.ExitError)
		if !isExit {
			t.Fatalf("running the child: %v\n%s", err, out)
		}
		code = ee.ExitCode()
	}
	return childRun{out: string(out), code: code}
}

// A binary whose claim was made before it started (KEPLOY_TEST_PID already
// names it) does not claim again: it only begins and ends its tests. It
// needs no project, which only the claim's key is built from, and runs on
// the clock file the launcher exported with the claim's answer.
func TestAnInheritedClaimIsNotMadeAgain(t *testing.T) {
	a := newFakeAgent(t)
	env := map[string]string{envAgent: a.url, envTimeFile: "/from/the/launcher"}
	c := inheritedClaim(t, env, "^TestChild(Body2?|Clock|OrderTP2)$")
	c.want(t, "TestChildBody", "PASS", bodyRan)
	c.want(t, "TestChildBody2", "PASS")
	c.want(t, "TestChildClock", "PASS", "CLOCK-SELF=/from/the/launcher\n", "CLOCK-CHILD=/from/the/launcher\n")
	c.want(t, "TestChildOrderTP2", "PASS")
	if claims := a.claimed(); len(claims) != 0 {
		t.Fatalf("claims %v, want none: the run was claimed for this process before it started", claims)
	}
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody passed",
		"begin TestChildBody2", "end TestChildBody2 passed",
		"begin TestChildClock", "end TestChildClock passed",
		"begin TestChildOrderTP2", "end TestChildOrderTP2 passed")
	if c.code != 0 {
		t.Errorf("exit %d, want 0", c.code)
	}
}

// When it joins, the package gives the binary its -test.parallel, so
// Parallel can serialize without the user passing it.
func TestAJoinedBinaryRunsWithKeploysParallel(t *testing.T) {
	a := newFakeAgent(t)
	argv := []string{"-test.run=^TestChildParallelFlag$", "-test.v", "-test.count=1"}
	out := runChildArgs(t, a.active(), argv)
	if !strings.Contains(out, "PARALLEL=1048576") {
		t.Fatalf("the joined binary's -test.parallel is not keploy's:\n%s", out)
	}
	out = runChildArgs(t, map[string]string{envAgent: "off"}, argv)
	if strings.Contains(out, "PARALLEL=1048576") {
		t.Fatalf("an inert binary's -test.parallel was changed:\n%s", out)
	}
}

func TestAMemberIsInert(t *testing.T) {
	a := newFakeAgent(t)
	// Another live process is the test process: this binary is its helper.
	env := map[string]string{envAgent: a.url, envTestPID: strconv.Itoa(os.Getpid())}
	c := runChild(t, env, "", "^TestChild(Body|Inert)$", "-test.parallel=4")
	c.want(t, "TestChildBody", "PASS", bodyRan)
	c.want(t, "TestChildInert", "PASS")
	wantCalls(t, a)
}

// A marker naming a process the binary does not run inside (inherited from
// a stale environment) does not make it a member: it claims the run and
// reports its tests. The marker here names a live process, so it is the
// ancestry that counts, not the liveness.
func TestAStaleMarkerStillClaims(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc on this system: the marker is taken at its word")
	}
	sib := exec.Command("sleep", "60")
	if err := sib.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sib.Process.Kill(); _ = sib.Wait() }()
	a := newFakeAgent(t)
	env := a.active()
	env[envTestPID] = strconv.Itoa(sib.Process.Pid)
	c := runChild(t, env, "", "^TestChildBody$")
	c.want(t, "TestChildBody", "PASS", bodyRan)
	if len(a.claimed()) != 1 {
		t.Fatalf("claims %v, want one", a.claimed())
	}
	wantCalls(t, a, "begin TestChildBody", "end TestChildBody passed")
}

// Membership is the package's to decide (KEPLOY_TEST_PID), not the agent's:
// a begin answer without a status is not a v1 answer, and fails the test.
func TestAMemberAnswerIsNotPartOfV1(t *testing.T) {
	a := newFakeAgent(t)
	a.onBegin("TestChildBody", reply{200, `{"v":1,"member":true}`})
	c := runChild(t, a.active(), "", "^TestChildBody$")
	c.want(t, "TestChildBody", "FAIL", `with status ""`)
	wantCalls(t, a, "begin TestChildBody")
}

func TestAnAnswerInAnotherProtocolFailsEveryTest(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		says string
	}{
		{"no version", `{"status":"ok"}`, "names no scope protocol version"},
		{"v2", `{"v":2,"status":"ok"}`, "scope protocol v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newFakeAgent(t)
			a.onAll(reply{200, tc.body})
			c := runChild(t, a.active(), "", "^TestChildBody2?$")
			for _, n := range []string{"TestChildBody", "TestChildBody2"} {
				c.want(t, n, "FAIL", tc.says, "speaks scope protocol v1")
			}
			if strings.Contains(c.out, bodyRan) {
				t.Errorf("a body ran after a begin it could not read\n%s", c.out)
			}
			// No end for a test that was never begun.
			wantCalls(t, a, "begin TestChildBody", "begin TestChildBody2")
		})
	}
}

func TestAnErrorAnswerFailsTheTestWithItsMessageAndRemedy(t *testing.T) {
	a := newFakeAgent(t)
	a.onBegin("TestChildBody", reply{422, `{"error":"upgrade_integration","message":"this agent needs canSkip","remedy":"upgrade keploy-go-sdk"}`})
	c := runChild(t, a.active(), "", "^TestChildBody$")
	c.want(t, "TestChildBody", "FAIL", "keploy: this agent needs canSkip (upgrade keploy-go-sdk)")
	wantCalls(t, a, "begin TestChildBody")
}

func TestAnEndTheTestCannotReadFailsIt(t *testing.T) {
	a := newFakeAgent(t)
	a.onEnd("TestChildBody", reply{500, `agent broke`})
	c := runChild(t, a.active(), "", "^TestChildBody$")
	c.want(t, "TestChildBody", "FAIL", "500 Internal Server Error", `"agent broke"`)
}

// hangingUp is an address whose listener accepts and closes every connection:
// a transport error that no other process can turn into an answer.
func hangingUp(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return "http://" + l.Addr().String()
}

func TestAnAgentThatIsGoneFailsTheTest(t *testing.T) {
	url := hangingUp(t)
	c := runChild(t, map[string]string{envAgent: url, envProject: "/"}, "", "^TestChildBody$")
	c.want(t, "TestChildBody", "FAIL", "cannot reach the keploy agent at "+url)
	if strings.Contains(c.out, bodyRan) {
		t.Errorf("the body ran without a begin\n%s", c.out)
	}
}

func TestTheTokenIsSentOnEveryCall(t *testing.T) {
	a := newFakeAgent(t)
	env := a.active()
	env[envToken] = "s3cret"
	runChild(t, env, "", "^TestChildBody2?$")
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) != 4 {
		t.Fatalf("calls %+v, want 4", a.calls)
	}
	for _, c := range a.calls {
		if c.auth != "Bearer s3cret" {
			t.Errorf("%s sent Authorization %q", c, c.auth)
		}
	}
}

// A T test that calls t.Parallel() itself stays open on keploy's agent while
// Go pauses it. The package reads only testing's exported API, so it does not
// see the call: keploy's agent refuses to begin the next T test (one test at
// a time), which fails with the agent's words, naming both tests and the fix
// (release1-cut §5.5: the agent's refusal plus the documented rule).
func TestARawTopLevelParallelIsRefusedByTheAgent(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildRawPar[AB]$")
	c.want(t, "TestChildRawParA", "PASS")
	c.want(t, "TestChildRawParB", "FAIL", "keploy: TestChildRawParB began while TestChildRawParA was still open",
		"start parallel tests with keploytest.Parallel and subtests with keploytest.Run (keploy mock codemod go writes them)")
	wantCalls(t, a, "begin TestChildRawParA", "begin TestChildRawParB", "end TestChildRawParA passed")
	if c.code != 1 {
		t.Errorf("exit %d, want 1", c.code)
	}
}

// A keploytest line after t.Parallel() is not the test's first statement:
// the test reaches it whenever Go resumes it, outside the turns. A T line
// reached while a parallel test is open is refused by keploy's agent, with
// its words, and the test never runs past it; a TP line there makes a test
// that never calls Parallel, which fails by name. The open parallel test is
// untouched.
func TestAKeploytestLineAfterTParallelBeginsOutsideTheTurns(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChildLate(T|TP|OpenTP)$")
	c.want(t, "TestChildLateT", "FAIL", "keploy: TestChildLateT began while TestChildLateOpenTP was still open")
	c.want(t, "TestChildLateTP", "FAIL", "keploy: TestChildLateTP starts with keploytest.TP but ended without calling keploytest.Parallel")
	c.want(t, "TestChildLateOpenTP", "PASS", bodyRan)
	wantCalls(t, a, "begin TestChildLateOpenTP", "begin TestChildLateT", "end TestChildLateOpenTP passed")
}

// A top-level test that another test's SkipNow ends makes Go end the
// process: keploy hears it failed, as Go reports it, not passed.
func TestATopLevelTestEndedThroughAnotherTestsTIsFailed(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, a.active(), "", "^TestChild(SaveT|OthersSkipNow)$")
	if c.code == 0 || c.status("TestChildOthersSkipNow") != "FAIL" {
		t.Errorf("exit %d, status %q; want Go to end the process with the test failed\n%s", c.code, c.status("TestChildOthersSkipNow"), c.out)
	}
	wantCalls(t, a, "begin TestChildOthersSkipNow", "end TestChildOthersSkipNow failed")
}

// A T test that calls t.Parallel() itself never runs at the same time as a
// parallel test, whichever comes first: the parallel test's turn waits for
// it to end. With no other T test to begin while it is paused, nothing is
// refused, and both pass, each judged alone. Run repeatedly: the outcome
// must not depend on scheduling.
func TestARawParallelTTestNeverOverlapsAParallelTest(t *testing.T) {
	for _, tc := range []struct {
		run, raw, tp string
	}{
		{"^TestChildRawThenTP(Raw|TP)$", "TestChildRawThenTPRaw", "TestChildRawThenTPTP"},
		{"^TestChildTPThenRaw(TP|Raw)$", "TestChildTPThenRawRaw", "TestChildTPThenRawTP"},
	} {
		for i := 0; i < 5; i++ {
			a := newFakeAgent(t)
			c := runChild(t, a.active(), "", tc.run)
			c.want(t, tc.raw, "PASS")
			c.want(t, tc.tp, "PASS", bodyRan)
			wantCalls(t, a, "begin "+tc.raw, "end "+tc.raw+" passed", "begin "+tc.tp, "end "+tc.tp+" passed")
		}
	}
}

// The order of v12-x/n1: every parallel test runs alone, in source order,
// with all of its subtests and cleanups; top-level begins and ends never
// overlap; a TP test begins once its turn has come.
func TestParallelTestsRunOneAtATimeInSourceOrder(t *testing.T) {
	const run = "^TestChildOrder(Mix|TP1|TPSkip|TP2|TP3)$"
	wantLog := strings.Join([]string{
		"TestChildOrderMix", "TestChildOrderMix/g/a", "TestChildOrderMix/g/b", "TestChildOrderMix/s",
		"TestChildOrderMix/p1", "TestChildOrderMix/p2",
		"TestChildOrderTP1", "TestChildOrderTP1/preCleanup",
		"TestChildOrderTP2",
		"TestChildOrderTP3", "TestChildOrderTP3/x", "TestChildOrderTP3/y",
	}, ",")
	wantAgent := []string{
		"begin TestChildOrderMix", "end TestChildOrderMix passed",
		"begin TestChildOrderTP1", "end TestChildOrderTP1 passed",
		"begin TestChildOrderTPSkip",
		"begin TestChildOrderTP2", "end TestChildOrderTP2 passed",
		"begin TestChildOrderTP3", "end TestChildOrderTP3 passed",
	}
	for i := 0; i < 9; i++ {
		env := map[string]string{}
		name := "default"
		var args []string
		count := 1
		switch {
		case i == 8:
			args, count, name = []string{"-test.count=2"}, 2, "count=2"
		case i >= 5:
			env["GOMAXPROCS"] = "1"
			name = "GOMAXPROCS=1"
		}
		t.Run(fmt.Sprintf("%s#%d", name, i), func(t *testing.T) {
			a := newFakeAgent(t)
			a.onBegin("TestChildOrderTPSkip", skip("keploy: quarantined: manual"))
			for k, v := range a.active() {
				env[k] = v
			}
			c := runChild(t, env, "", run, args...)
			if c.code != 0 || strings.Contains(c.out, "DATA RACE") {
				t.Fatalf("exit %d\n%s", c.code, c.out)
			}
			var log, calls []string
			for n := 0; n < count; n++ {
				log = append(log, wantLog)
				calls = append(calls, wantAgent...)
			}
			if got := c.ktlog(); got != strings.Join(log, ",") {
				t.Errorf("order:\n  %s\nwant:\n  %s", got, strings.Join(log, ","))
			}
			c.want(t, "TestChildOrderTPSkip", "SKIP", "keploy: quarantined: manual")
			wantCalls(t, a, calls...)
		})
	}
}

func TestTheGuardsFailTheTestWithTheFix(t *testing.T) {
	for _, tc := range []struct {
		test string
		says string
		args []string
	}{
		{"TestChildGuardParallelInT", "keploy: TestChildGuardParallelInT calls keploytest.Parallel; make its first statement defer keploytest.TP(t)()", nil},
		{"TestChildGuardTPWithoutParallel", "keploy: TestChildGuardTPWithoutParallel starts with keploytest.TP but ended without calling keploytest.Parallel", nil},
		{"TestChildGuardNoFirstLine", "keploy: make TestChildGuardNoFirstLine's first statement defer keploytest.TP(t)()", nil},
		{"TestChildGuardRunWithoutFirstLine", "keploy: TestChildGuardRunWithoutFirstLine has no keploytest line; run keploy mock codemod go", nil},
		{"TestChildGuardRawRun", "keploy: TestChildGuardRawRun/x was started by t.Run; start it with keploytest.Run", nil},
		{"TestChildGuardRawParallel", "keploy: TestChildGuardRawParallel/x calls t.Parallel() directly; use keploytest.Parallel(t)", nil},
		{"TestChildGuardOtherGoroutine", `keploy: keploytest.Run(TestChildGuardOtherGoroutine, "x") from another goroutine; start subtests from the test's own goroutine`, nil},
		{"TestChildGuardLowParallel", "keploy: -test.parallel=4 is too few", []string{"-test.parallel=4"}},
		{"TestChildGuardTInSubtest", "keploy: keploytest.T is the first statement of a top-level test, and TestChildGuardTInSubtest/x is a subtest", nil},
		{"TestChildGuardTwice", "keploy: TestChildGuardTwice calls keploytest.T or keploytest.TP twice", nil},
		{"TestChildGuardRunInRawRun", "keploy: TestChildGuardRunInRawRun/x was started by t.Run; start it with keploytest.Run", nil},
		{"TestChildGuardTPRawParallel", "keploy: TestChildGuardTPRawParallel starts with keploytest.TP but ended without calling keploytest.Parallel; call keploytest.Parallel(t), not t.Parallel()", nil},
	} {
		t.Run(tc.test, func(t *testing.T) {
			a := newFakeAgent(t)
			c := runChild(t, a.active(), "", "^"+tc.test+"$", tc.args...)
			c.want(t, tc.test, "FAIL", tc.says)
			if c.code != 1 {
				t.Errorf("exit %d, want 1", c.code)
			}
		})
	}
}

// The end is posted while the test still holds its turn: the next parallel
// test cannot begin before the agent has ended this one. In process, against
// the package's own client.
func TestTheEndIsPostedBeforeTheTurnPasses(t *testing.T) {
	q := &queue{}
	if _, wait := q.take(); wait != nil {
		t.Fatal("the first ticket waits")
	}
	_, next := q.take()
	if next == nil {
		t.Fatal("the second ticket does not wait")
	}
	servingAtEnd, nextWokenAtEnd := -1, false
	var locked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mu.TryLock() {
			// exit holds the package lock across the end call.
			locked.Store(true)
			_, _ = io.WriteString(w, `{"v":1,"status":"ok"}`)
			return
		}
		servingAtEnd = q.serving
		select {
		case <-next:
			nextWokenAtEnd = true
		default:
		}
		mu.Unlock()
		_, _ = io.WriteString(w, `{"v":1,"status":"ok"}`)
	}))
	defer srv.Close()
	// The parent process never joins keploy (no test of it calls T), so
	// this test may point the package's client at its own agent.
	agentOnce.Do(func() {})
	was := agentC
	agentC = newClient(srv.URL, "")
	defer func() { agentC = was }()
	st := &tstate{kind: kindTP, began: true, q: q}
	t.Run("holder", func(h *testing.T) {
		st.gid = gid() // the test's own goroutine
		exit(h, st)
	})
	mu.Lock()
	defer mu.Unlock()
	if locked.Load() {
		t.Fatal("exit holds the package lock while it calls the agent")
	}
	if servingAtEnd != 0 || nextWokenAtEnd {
		t.Fatalf("at the end call the turn was %d (next woken: %v), want 0: the turn passed before the end", servingAtEnd, nextWokenAtEnd)
	}
	select {
	case <-next:
	default:
		t.Fatal("the next ticket was not woken after the end")
	}
	if q.serving != 1 || len(q.wake) != 0 {
		t.Fatalf("serving %d, waiting %d; want 1, 0", q.serving, len(q.wake))
	}
}

// go test caches a package's result unless the test reads other environment
// variables than last time. A cached result would run no test under keploy
// (its agent hears nothing), so the package reads keploy's variables while a
// test runs: a run with them changed is never served from the cache.
func TestKeploysVariablesDefeatTheTestCache(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a module with go test")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on the PATH")
	}
	src, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	mod := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module cachecheck\n\ngo 1.21\n\nrequire github.com/keploy/go-sdk/v3 v3.0.0\n\nreplace github.com/keploy/go-sdk/v3 => "+src+"\n")
	// The SDK's own checksums: its requirements are then verified from the
	// module cache this test's own build filled, with no download.
	sums, err := os.ReadFile(filepath.Join(src, "go.sum"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	write("go.sum", string(sums))
	write("x_test.go", "package cachecheck\n\nimport (\n\t\"testing\"\n\n\t\"github.com/keploy/go-sdk/v3/keploytest\"\n)\n\nfunc TestX(t *testing.T) { defer keploytest.T(t)() }\n")
	run := func(env ...string) string {
		t.Helper()
		cmd := exec.Command(goBin, "test", "./...")
		cmd.Dir = mod
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "KEPLOY") && !strings.HasPrefix(kv, "GOFLAGS=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		// The shared build cache: the module's directory is new, so no
		// earlier run's result can be served for it.
		cmd.Env = append(cmd.Env, "GOFLAGS=")
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go test: %v\n%s", err, out)
		}
		return string(out)
	}
	run()
	if out := run(); !strings.Contains(out, "(cached)") {
		t.Fatalf("precondition: an unchanged run is cached:\n%s", out)
	}
	if out := run(envAgent + "=off"); strings.Contains(out, "(cached)") {
		t.Fatalf("a run with KEPLOY_MOCK_AGENT changed was served from the cache:\n%s", out)
	}
	if out := run(envAgent+"=off", envToken+"=another"); strings.Contains(out, "(cached)") {
		t.Fatalf("a run with KEPLOY_MOCK_AGENT_TOKEN changed was served from the cache:\n%s", out)
	}
}
