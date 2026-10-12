package keploytest

import (
	"bytes"
	"flag"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	kindT = iota + 1
	kindTP
	kindSub
)

// queue hands out tickets in the order Parallel is called, and serves them in
// that order: one parallel test (with its subtests and cleanups) at a time.
// A ticket that must wait has its own channel, closed when its turn comes, so
// a release wakes exactly the next test, not every waiting one.
type queue struct {
	next, serving int
	wake          map[int]chan struct{}
}

// take hands out the next ticket, and the channel to wait on when it is not
// its turn yet (nil when it is). mu is held.
func (q *queue) take() (int, chan struct{}) {
	my := q.next
	q.next++
	if q.serving == my {
		return my, nil
	}
	if q.wake == nil {
		q.wake = map[int]chan struct{}{}
	}
	ch := make(chan struct{})
	q.wake[my] = ch
	return my, ch
}

// release ends the turn being served and starts the next one. mu is held.
func (q *queue) release() {
	q.serving++
	if ch, waiting := q.wake[q.serving]; waiting {
		delete(q.wake, q.serving)
		close(ch)
	}
}

// tstate is what this package knows of one test that started with T or TP,
// or of one subtest Run started.
type tstate struct {
	kind int
	// gid is the goroutine that runs the test's function.
	gid uint64
	// began: the agent opened the test, so its end is owed.
	began bool
	// paralleled: Parallel was called for the test.
	paralleled bool
	// q is the queue whose ticket the test holds, nil when it holds none.
	q *queue
}

var (
	mu sync.Mutex
	// top is the queue of every top-level test of the process.
	top      = &queue{}
	parentOf = map[*testing.T]*testing.T{}
	queueOf  = map[*testing.T]*queue{}
	state    = map[*testing.T]*tstate{}
	// loose holds the top-level tests that run outside the turns: a T test
	// from its first line to its end, a TP test until Parallel gives it a
	// ticket. In the sequential phase that is the one test running. One
	// still there once a parallel test's turn comes called t.Parallel()
	// itself, and runs at the same time as the parallel tests: their turns
	// wait for it to end. looseIdle is closed when loose empties.
	loose     = map[*testing.T]bool{}
	looseIdle chan struct{}
)

// addLoose puts t in loose. mu is held.
func addLoose(t *testing.T) {
	if len(loose) == 0 {
		looseIdle = make(chan struct{})
	}
	loose[t] = true
}

// dropLoose takes t out of loose. mu is held.
func dropLoose(t *testing.T) {
	if !loose[t] {
		return
	}
	delete(loose, t)
	if len(loose) == 0 && looseIdle != nil {
		close(looseIdle)
		looseIdle = nil
	}
}

// waitNoLoose waits until no top-level test runs outside the turns, so a
// parallel test never begins, or runs its body, at the same time as one.
func waitNoLoose() {
	for {
		mu.Lock()
		if len(loose) == 0 {
			mu.Unlock()
			return
		}
		idle := looseIdle
		mu.Unlock()
		<-idle
	}
}

// testingState is package testing's own record of t: whether it called
// t.Parallel (common.isParallel), and whether its function finished, by
// returning or through its own FailNow or SkipNow (common.finished). ok is
// false when this Go's testing has no such fields, and joinTop then refuses
// the run; TestTestingState pins them on every Go the package is tested
// with. Read only from t's own goroutine, which wrote them, or after testing
// has synchronized with it. finished is read without testing's lock, which
// only a misuse can race with (a goroutine of the test's own calling
// t.SkipNow while its cleanups run).
func testingState(t *testing.T) (parallel, finished, ok bool) {
	c := reflect.ValueOf(t).Elem().FieldByName("common")
	if !c.IsValid() {
		return false, false, false
	}
	p, f := c.FieldByName("isParallel"), c.FieldByName("finished")
	if !p.IsValid() || !f.IsValid() || p.Kind() != reflect.Bool || f.Kind() != reflect.Bool {
		return false, false, false
	}
	return p.Bool(), f.Bool(), true
}

// gid is the id of the calling goroutine.
func gid() uint64 {
	var b [64]byte
	f := bytes.Fields(b[:runtime.Stack(b[:], false)])
	if len(f) < 2 {
		return 0
	}
	id, _ := strconv.ParseUint(string(f[1]), 10, 64)
	return id
}

// T is the first statement of every top-level test that does not call
// Parallel:
//
//	defer keploytest.T(t)()
//
// It asks keploy's agent to begin the test, and skips it with keploy's reason
// when keploy quarantines it. The test's exit cleanup, which T registers
// first so that it runs last, ends the test with its outcome and fails it
// when keploy saw a dependency call it could not answer from the recording.
// Without keploy, T does nothing.
func T(t *testing.T) func() {
	t.Helper()
	if !joinTop(t, "T") {
		return func() {}
	}
	st := enter(t, kindT)
	open(t, st)
	// The exit cleanup does the work; the deferred call is what keeps the
	// line one statement that codemod can write and check.
	return func() {}
}

// TP is the first statement of a top-level test that calls Parallel:
//
//	defer keploytest.TP(t)()
//	keploytest.Parallel(t)
//
// It leaves the test's begin to Parallel, which asks once the test's turn
// has come. Without keploy, TP does nothing.
func TP(t *testing.T) func() {
	t.Helper()
	if !joinTop(t, "TP") {
		return func() {}
	}
	st := enter(t, kindTP)
	return func() {
		t.Helper()
		if howEnding() != endNormal {
			return
		}
		mu.Lock()
		p := st.paralleled
		mu.Unlock()
		if raw, _, _ := testingState(t); !p && !t.Skipped() && !t.Failed() && !raw {
			t.Errorf("keploy: %s starts with keploytest.TP but ended without calling keploytest.Parallel; "+
				"call keploytest.Parallel(t) on every path through it, or start it with keploytest.T", t.Name())
		}
	}
}

// Run replaces t.Run: it runs f as a subtest of t. With keploy, a subtest that
// calls Parallel runs after its parent's body returns, one at a time in source
// order. Without keploy, Run calls t.Run.
func Run(t *testing.T, name string, f func(t *testing.T)) bool {
	t.Helper()
	switch j := join(); j.mode {
	case inert:
		return t.Run(name, f)
	case refused:
		t.Fatal(j.refusal)
	}
	mu.Lock()
	st := state[t]
	if st == nil {
		mu.Unlock()
		if strings.Contains(t.Name(), "/") {
			t.Errorf("keploy: %s was started by t.Run; start it with keploytest.Run", t.Name())
		} else {
			t.Errorf("keploy: %s has no keploytest line; run keploy mock codemod go", t.Name())
		}
		return false
	}
	if st.gid != gid() {
		mu.Unlock()
		t.Errorf("keploy: keploytest.Run(%s, %q) from another goroutine; start subtests from the test's own goroutine", t.Name(), name)
		return false
	}
	if st.kind == kindTP && !st.paralleled {
		// The test has not begun: a subtest started now would run outside
		// it, its calls charged to no test.
		mu.Unlock()
		t.Errorf("keploy: %s starts subtest %q before keploytest.Parallel; call keploytest.Parallel(t) first", t.Name(), name)
		return false
	}
	if queueOf[t] == nil {
		queueOf[t] = &queue{}
	}
	mu.Unlock()
	var done atomic.Bool
	var child *testing.T
	var cst *tstate
	ok := t.Run(name, func(c *testing.T) {
		s := enter(c, kindSub) // the subtest's first cleanup, so its last
		mu.Lock()
		parentOf[c] = t
		child, cst = c, s
		mu.Unlock()
		defer done.Store(true)
		f(c)
	})
	mu.Lock()
	c := child
	viaKeploy := cst != nil && cst.paralleled
	mu.Unlock()
	if c != nil && !done.Load() && !viaKeploy {
		// t.Run returned before the subtest's function ended, and Parallel
		// was not called: the subtest went parallel through t.Parallel.
		c.Helper()
		c.Errorf("keploy: %s calls t.Parallel() directly; use keploytest.Parallel(t)", c.Name())
	}
	return ok
}

// Parallel replaces t.Parallel. With keploy, the test still calls t.Parallel,
// so Go runs it after its sequential siblings, as plain Go does; but parallel
// tests then run one at a time, in the order they called Parallel (source
// order), each with all of its subtests and cleanups. Without keploy,
// Parallel calls t.Parallel.
func Parallel(t *testing.T) {
	t.Helper()
	switch j := join(); j.mode {
	case inert:
		t.Parallel()
		return
	case refused:
		t.Fatal(j.refusal)
	}
	if f := flag.Lookup("test.parallel"); f != nil {
		if n, err := strconv.Atoi(f.Value.String()); err == nil && n < keployParallel {
			t.Fatalf("keploy: -test.parallel=%d is too few for keploytest.Parallel to serialize the parallel tests; run go test without -parallel", n)
		}
	}
	mu.Lock()
	st := state[t]
	switch {
	case st != nil && st.gid != gid():
		mu.Unlock()
		t.Fatalf("keploy: keploytest.Parallel(%s) from another goroutine; call it from the test's own goroutine", t.Name())
	case st == nil && strings.Contains(t.Name(), "/"):
		mu.Unlock()
		t.Fatalf("keploy: %s was started by t.Run; start it with keploytest.Run", t.Name())
	case st == nil:
		mu.Unlock()
		t.Fatalf("keploy: make %s's first statement defer keploytest.TP(t)()", t.Name())
	case st.kind == kindT:
		mu.Unlock()
		t.Fatalf("keploy: %s calls keploytest.Parallel; make its first statement defer keploytest.TP(t)()", t.Name())
	case st.paralleled:
		mu.Unlock()
		t.Fatalf("keploy: %s calls keploytest.Parallel twice", t.Name())
	}
	q := top
	if st.kind == kindSub {
		q = queueOf[parentOf[t]]
	}
	if q == nil {
		mu.Unlock()
		t.Fatalf("keploy: %s has no parent keploytest knows; start it with keploytest.Run", t.Name())
	}
	_, turn := q.take()
	st.q, st.paralleled = q, true
	dropLoose(t) // it runs in its turn from here
	mu.Unlock()
	t.Parallel()
	if turn != nil {
		<-turn
	}
	if st.kind == kindTP {
		waitNoLoose()
		open(t, st)
	}
}

// joinTop runs the checks of a top-level test's first line, and reports
// whether the test reports to keploy.
func joinTop(t *testing.T, line string) bool {
	t.Helper()
	readKeployEnv()
	j := join()
	switch j.mode {
	case inert:
		return false
	case refused:
		t.Fatal(j.refusal)
	}
	if why := flagsRefusal(); why != "" {
		t.Fatal(why)
	}
	if strings.Contains(t.Name(), "/") {
		t.Fatalf("keploy: keploytest.%s is the first statement of a top-level test, and %s is a subtest; start subtests with keploytest.Run", line, t.Name())
	}
	par, _, ok := testingState(t)
	if !ok {
		t.Fatalf("keploy: this Go (%s) changed package testing in a way this keploytest cannot read (whether a test went parallel or finished); upgrade github.com/keploy/go-sdk", runtime.Version())
	}
	if par {
		t.Fatalf("keploy: %s calls t.Parallel() before keploytest.%s; make defer keploytest.TP(t)() its first statement and call keploytest.Parallel(t) in place of t.Parallel()", t.Name(), line)
	}
	mu.Lock()
	_, twice := state[t]
	var blocker *testing.T
	for lt := range loose {
		// In the sequential phase a loose T test other than this one is a
		// paused one that called t.Parallel() itself, while open: this
		// test cannot begin until it ends. (It wrote isParallel before it
		// paused, and package testing ran this test after that.)
		if st := state[lt]; st != nil && st.kind == kindT && st.began {
			if p, _, _ := testingState(lt); p {
				blocker = lt
			}
		}
	}
	mu.Unlock()
	if twice {
		t.Fatalf("keploy: %s calls keploytest.T or keploytest.TP twice", t.Name())
	}
	if blocker != nil && line == "T" {
		t.Fatalf("keploy: %s cannot begin: %s called t.Parallel() directly and is still open; fix %s (start it with defer keploytest.TP(t)() and call keploytest.Parallel(t))", t.Name(), blocker.Name(), blocker.Name())
	}
	return true
}

var (
	flagsOnce sync.Once
	flagsWhy  string
)

// readKeployEnv reads keploy's variables while a test runs, whatever the
// mode. go test caches a package's result unless the test reads different
// environment variables or files than last time, and only reads made while
// tests run are seen: the package's own reads in init are not. Each keploy
// run hands the tests another agent address and token, so reading them here
// makes every keploy run, and the first run without keploy after one, a
// cache miss: a cached result would report no test to keploy's agent, or a
// replayed one with keploy switched off.
func readKeployEnv() {
	for _, k := range []string{envAgent, envToken, envProject, envTestPID} {
		_ = os.Getenv(k)
	}
}

// flagsRefusal is testFlagsRefusal, read once: at the first test the flags
// are parsed.
func flagsRefusal() string {
	flagsOnce.Do(func() { flagsWhy = testFlagsRefusal() })
	return flagsWhy
}

// enter registers t and its exit cleanup. It is the first cleanup t
// registers, so Go runs it after every other cleanup of t.
func enter(t *testing.T, kind int) *tstate {
	t.Helper()
	st := &tstate{kind: kind, gid: gid()}
	mu.Lock()
	state[t] = st
	if kind == kindT || kind == kindTP {
		addLoose(t)
	}
	mu.Unlock()
	t.Cleanup(func() {
		t.Helper()
		exit(t, st)
	})
	return st
}

// open asks the agent to begin the top-level test t.
func open(t *testing.T, st *tstate) {
	t.Helper()
	a, err := agent().begin(t.Name())
	if err != nil {
		t.Fatal(err.Error())
	}
	if a.Action == actionSkip {
		reason := a.Reason
		if reason == "" {
			reason = "keploy: quarantined in verdicts.yaml (the agent gave no reason)"
		}
		t.Skip(reason)
	}
	mu.Lock()
	st.began = true
	mu.Unlock()
}

// How a test's goroutine is ending, as seen from a deferred call or cleanup.
const (
	// endNormal: the test's function returned.
	endNormal = iota
	// endFinished: package testing ended it through runtime.Goexit:
	// t.FailNow or t.SkipNow (t.Fatal, t.Skip), or t.Run aborting a parent
	// whose subtest called FailNow on the parent's t. Go reports the test
	// and the run goes on, even when that Goexit ended a panic, except
	// when a parallel test calls FailNow on an ancestor's t: Go then ends
	// the process, and exit tells that case apart by testing's own record.
	endFinished
	// endPanic: it is panicking. Go marks the test failed after its
	// cleanups, and ends the process.
	endPanic
	// endGoexit: runtime.Goexit called from outside package testing. Go
	// fails the test with "test executed panic(nil) or runtime.Goexit"
	// after its cleanups, and ends the process.
	endGoexit
)

// howEnding reads the calling goroutine's stack, innermost frame first. Go
// runs a test's deferred calls and cleanups on the stack of whatever is ending
// the test: runtime.gopanic for a panic (also while tRunner recovers and
// re-panics), runtime.Goexit for FailNow, SkipNow or a raw Goexit, neither
// for a normal return. The innermost of the two decides: a FailNow or SkipNow
// during a panic ends that panic. A panic the test recovered itself is gone
// from the stack before its remaining deferred calls run. Nothing is
// recovered here, so every panic, panic(nil) included, goes on exactly as it
// would without keploy.
func howEnding() int {
	var pcs [64]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	goexit := false
	for {
		f, more := frames.Next()
		switch {
		case goexit:
			// The caller of runtime.Goexit: package testing ends a test
			// this way and goes on with the run; anything else is a raw
			// Goexit, which ends the process.
			if testingFunc(f.Function) {
				return endFinished
			}
			return endGoexit
		case f.Function == "runtime.gopanic":
			return endPanic
		case f.Function == "runtime.Goexit":
			goexit = true
		}
		if !more {
			if goexit {
				return endGoexit
			}
			return endNormal
		}
	}
}

// testingFunc reports whether a stack frame's function is package testing's
// own: "testing." and no "/", so a function of a module whose path starts
// with "testing." (testing.example.com/x) is not taken for it.
func testingFunc(name string) bool {
	return strings.HasPrefix(name, "testing.") && !strings.Contains(name, "/")
}

// exit is every registered test's first cleanup, so it runs after all the
// others: it ends a begun test with its outcome, fails it when keploy says so,
// and then releases the test's ticket.
func exit(t *testing.T, st *tstate) {
	t.Helper()
	mu.Lock()
	began := st.began
	mu.Unlock()
	// The process is about to end: a panic in the test or in one of its
	// cleanups, or a raw Goexit (Go runs the remaining cleanups, fails the
	// test and ends the process); this cleanup running on another goroutine
	// than the test's (Go does so only on that panic path); or, by
	// testing's own rule, a parallel test whose FailNow did not finish it:
	// FailNow called on an ancestor's t, after which Go ends the process.
	how := howEnding()
	raw, finished := false, false
	onOwn := gid() == st.gid
	if onOwn {
		// joinTop refused a Go whose testing these fields are missing from.
		raw, finished, _ = testingState(t)
	}
	dying := how == endPanic || how == endGoexit || !onOwn || (how == endFinished && raw && !finished)
	// A T test, or a TP test that never called Parallel, that called
	// t.Parallel() itself ran outside the turns: it is failed, by name.
	if raw && (st.kind == kindT || (st.kind == kindTP && !st.paralleled)) && !dying {
		fix := "start it with defer keploytest.TP(t)() and call keploytest.Parallel(t)"
		if st.kind == kindTP {
			fix = "call keploytest.Parallel(t) in place of t.Parallel()"
		}
		t.Errorf("keploy: %s calls t.Parallel() directly; %s", t.Name(), fix)
	}
	if began {
		outcome := outcomePassed
		switch {
		case t.Failed() || dying:
			outcome = outcomeFailed
		case t.Skipped():
			outcome = outcomeSkipped
		}
		a, err := agent().end(t.Name(), outcome)
		switch {
		case err != nil:
			t.Error(err.Error())
		case a.Action == actionFail:
			reason := a.Reason
			if reason == "" {
				reason = "keploy: the keploy agent failed this test and gave no reason"
			}
			t.Error(reason)
		}
	}
	mu.Lock()
	st.began = false
	if !dying {
		// A dying test stays loose, as it keeps its turn: nothing may run
		// while the process goes down.
		dropLoose(t)
	}
	if st.q != nil && !dying {
		// A dying test keeps its turn: the process ends with this panic, and
		// the next test must not begin and run user code before it does.
		st.q.release()
	}
	delete(state, t)
	delete(queueOf, t)
	delete(parentOf, t)
	mu.Unlock()
}
