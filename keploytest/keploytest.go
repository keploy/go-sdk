package keploytest

import (
	"bytes"
	"flag"
	"os"
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
	// finished: package testing takes the test as finished, as its first
	// line's deferred call (T, TP) or Run's deferred call saw (noteEnd), or
	// as open knows before it skips or fails the test: its function returned
	// normally, or its own FailNow or SkipNow ended it. A Goexit in one of
	// its cleanups then does not end the process, whatever called it.
	finished bool
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
	// The exit cleanup does the work. The deferred call keeps the line one
	// statement that codemod can write and check, and, as the function's
	// first deferred call, runs last of them: it sees how the function ended.
	return func() { noteEnd(t, st) }
}

// noteEnd records whether package testing takes the test as finished, and
// reports whether its function returned normally (no panic and no Goexit is
// under way). It runs as the test function's last deferred call (the first
// line's), or as Run's deferred call around the subtest's function, so
// nothing of the function runs after it. The test is finished when its
// function returned normally, or when package testing's Goexit ended it and
// it is failed or skipped: its own FailNow fails it and its own SkipNow
// skips it before marking it finished. Another test's FailNow or SkipNow
// leaves it neither, unless it had already failed.
func noteEnd(t *testing.T, st *tstate) bool {
	how := howEnding()
	if how == endNormal || (how == endFinished && (t.Failed() || t.Skipped())) {
		markFinished(st)
	}
	return how == endNormal
}

// markFinished records that package testing takes the test as finished.
func markFinished(st *tstate) {
	mu.Lock()
	st.finished = true
	mu.Unlock()
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
		if !noteEnd(t, st) {
			return
		}
		mu.Lock()
		p := st.paralleled
		mu.Unlock()
		if !p && !t.Skipped() && !t.Failed() {
			// Also a test that called t.Parallel() in place of Parallel: it
			// never began, and ran outside the turns.
			t.Errorf("keploy: %s starts with keploytest.TP but ended without calling keploytest.Parallel; "+
				"call keploytest.Parallel(t), not t.Parallel(), on every path through it, or start it with keploytest.T", t.Name())
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
		// This function is the subtest's own: how it ends is how the
		// subtest ends.
		defer noteEnd(c, s)
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
	mu.Lock()
	_, twice := state[t]
	mu.Unlock()
	if twice {
		t.Fatalf("keploy: %s calls keploytest.T or keploytest.TP twice", t.Name())
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
		// Its own FailNow, from its own goroutine (T's line, or Parallel
		// after its goroutine check): testing takes it as finished. T's
		// deferred call is not registered yet, so it cannot see that.
		markFinished(st)
		t.Fatal(err.Error())
	}
	if a.Action == actionSkip {
		reason := a.Reason
		if reason == "" {
			reason = "keploy: quarantined in verdicts.yaml (the agent gave no reason)"
		}
		markFinished(st)
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
	// when another test's t ended a top-level or parallel test (a parallel
	// subtest calling FailNow on its parent's t): Go then ends the process
	// (exit).
	endFinished
	// endPanic: it is panicking. Go marks the test failed after its
	// cleanups, and ends the process.
	endPanic
	// endGoexit: runtime.Goexit called from outside package testing. While
	// the test's function runs, Go fails the test with "test executed
	// panic(nil) or runtime.Goexit" after its cleanups, and ends the
	// process; from a cleanup of a test package testing takes as finished
	// (its function returned, or its own FailNow or SkipNow ended it), Go
	// reports the test as it stands and goes on with the run (exit).
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
	began, paralleled, finished := st.began, st.paralleled, st.finished
	mu.Unlock()
	// The process is about to end when:
	//   - the test or one of its cleanups panics (Go runs the remaining
	//     cleanups, fails the test and ends the process), or this cleanup
	//     runs on another goroutine than the test's (Go does so only on the
	//     path where it fails the test and ends the process);
	//   - package testing does not take the test as finished (noteEnd), and
	//     a raw Goexit ended it, or package testing ended it through another
	//     test's t while it is a top-level test or a parallel one. (Go fails
	//     a sequential subtest its parent's FailNow ended, and goes on.) A
	//     test that package testing ended, and that is neither failed nor
	//     skipped, was ended through another test's t.
	// A Goexit in a cleanup of a finished test, whatever called it, does not
	// end the process. Package testing's exported API does not say which t
	// ended a test, nor whether testing took it as finished. So a test that
	// had already failed (itself, or through one of its subtests) when
	// another test's t ended it, or t.Run ended it, is taken for finished
	// while Go ends the process, and so is a subtest that called t.Parallel()
	// itself: it gives up its turn, and a test after it may start while the
	// process goes down, which fails the run all the same. The other way
	// round, keeping the turn of a test whose process goes on, would hang
	// the run.
	how := howEnding()
	byOther := how == endFinished && (paralleled || st.kind != kindSub) && !t.Failed() && !t.Skipped()
	dying := how == endPanic || gid() != st.gid || (!finished && (how == endGoexit || byOther))
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
