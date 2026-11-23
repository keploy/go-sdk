// Package keploytest joins a Go test suite to keploy's Universal Mocker.
//
// keploy records the dependency calls each test makes (keploy mock record) and
// replays them in CI, so a suite that needs databases, queues and HTTP
// services runs with every dependency answered from the recording. keploy
// judges every recorded test, and lists the ones it cannot replay in
// keploy/<set>/verdicts.yaml with a reason. This package is how a test tells
// keploy where it begins and ends, and how keploy skips a listed test or
// fails a test whose dependency calls changed.
//
// # The first line
//
// Every top-level test starts with one line:
//
//	func TestPlaceOrder(t *testing.T) {
//		defer keploytest.T(t)()
//		...
//	}
//
// A top-level test that calls Parallel starts with TP instead:
//
//	func TestExport(t *testing.T) {
//		defer keploytest.TP(t)()
//		keploytest.Parallel(t)
//		...
//	}
//
// Subtests are started with Run instead of t.Run, and go parallel with
// Parallel instead of t.Parallel. `keploy mock codemod go` writes all of this.
// The line must be the test's first statement: it registers the test's exit
// cleanup first, so that cleanup runs after every other cleanup of the test,
// and keploy sees each test's whole run, cleanups included. A cleanup
// registered above the line runs after the test has ended, outside it (for a
// parallel test, while the next one runs); `keploy mock codemod go --check`
// finds such tests.
//
// # Inert without keploy
//
// Without keploy nothing changes: with KEPLOY_MOCK_AGENT unset or "off", T
// and TP do nothing, Run calls t.Run and Parallel calls t.Parallel. Only
// keploy mock record and keploy mock replay set the variable.
//
// # With keploy
//
// keploy runs the suite inside one of its runs, as the project's own test
// command:
//
//	keploy mock record -c "go test -p 1 -json ./..."
//	keploy mock replay -c "go test -p 1 -json ./..."
//
// The command is the one keploy.yml's mock.command names. keploy hands it
// KEPLOY_MOCK_AGENT (its agent), KEPLOY_MOCK_AGENT_TOKEN and
// KEPLOY_MOCK_PROJECT (the project's directory). As a test binary starts,
// before the test package's own init and TestMain run, the package claims the
// run for that binary (its tests' IDs carry its package directory, relative
// to the project), and refuses what keploy's first release cannot judge: go
// test running more than one test binary at once (-p above 1, read from go
// test's command line, GOFLAGS or go env), and, at the first test, a run
// without -v or -json (a skip and its reason would be invisible), -shuffle
// and -fuzz. When keploy's run is on a frozen clock, the claim's answer
// names the run's clock file, and the package exports it as
// KEPLOY_TIME_FILE, the variable keploy's clock shim (LD_PRELOAD) reads: a
// process the binary starts that loads the shim runs on the run's clock. Go
// programs, this test binary included, do not load it; keploy moves their
// clock only in binaries it patched as they were built. A binary whose run
// was claimed before it started does not claim again, and only begins and
// ends its tests: keploy's launcher, keploy-go-exec (not yet released), run
// by go test -exec, will claim the run for the binary, export KEPLOY_TEST_PID
// (its own pid) and KEPLOY_TIME_FILE, and exec the binary, which keeps that
// pid. keploy's
// first release runs the test command natively on Linux, where the agent
// reads which process claims the run; elsewhere its agent refuses the claim.
// The package sets the binary's -test.parallel, so Parallel can serialize. A
// refusal fails every test that has the line, and names the fix. Processes
// the binary starts (a helper, a binary built and run by a test) inherit
// KEPLOY_TEST_PID, which names it, and stay inert while they run inside it;
// one started in the background that outlives its link to the binary (sh -c
// 'helper &') claims like a test binary, and the agent refuses it while the
// binary runs.
//
// Examples, benchmarks and fuzz targets have no keploytest line, so they do
// not report to keploy: a call they make that the recording cannot answer
// fails the run as a call outside every test.
//
// For each top-level test, T (or Parallel, for TP) asks keploy's agent to
// begin the test. A test verdicts.yaml lists is skipped through t.Skip with
// keploy's reason. The test's exit cleanup ends the test with its outcome
// (passed, failed or skipped; a panic is failed), and when keploy saw a
// dependency call with no recorded answer, a recorded call the test no longer
// made, or a call keploy could not capture, it fails the test with t.Error
// and keploy's text, which says how to re-record or quarantine it. The exit
// code is go test's own.
//
// Any answer the package cannot trust fails the test: an agent it cannot
// reach, an error answer, an unreadable answer, or an answer that does not
// speak scope protocol v1. Nothing is retried and no clock decides anything.
//
// # One test at a time
//
// keploy attributes every dependency call to the one test that is open, so a
// test binary runs one test at a time. Parallel and Run keep Go's parallel
// tests: each parallel test still calls t.Parallel, and they run one at a time
// in source order, each with all of its subtests and cleanups. The order is
// fixed: a test's sequential subtests run inside its body, and its parallel
// subtests after the body returns.
//
// So in a test with a keploytest line, the line is the first statement, the
// test goes parallel with Parallel, never t.Parallel(), and its subtests
// start with Run, never t.Run (`keploy mock codemod go` writes them, and
// `keploy mock codemod go --check` lists the tests that break the rule). The
// package reads no unexported state of package testing, and its exported API
// does not say whether a test called t.Parallel(), so the package cannot
// serialize a test that does. keploy's agent is the backstop: it refuses to
// begin a test while another test is open, and the test it refuses fails
// with the agent's message, which names the open test. A T test that calls
// t.Parallel() itself stays open on the agent while Go pauses it, so every
// later sequential T test fails, naming it, and every parallel test's turn
// waits for it to end; when no T test comes after it, nothing is refused,
// and it runs alone without a word. A TP test that calls t.Parallel() in
// place of Parallel never begins, and fails by name. A test whose keploytest
// line comes after t.Parallel() begins whenever Go resumes it, outside the
// turns: of it and a parallel test that begin at about the same time,
// whichever begins second fails, and that can be the parallel test. A TP
// line after t.Parallel() makes Parallel call t.Parallel() a second time,
// which Go refuses with a panic, as it does without keploy. A subtest
// Run starts that calls t.Parallel() fails by name. What a test runs before
// its keploytest line is not serialized, and its calls can be charged to the
// open test.
//
// A test with no keploytest line is not tracked at all: it runs whenever Go
// runs it, and its calls are charged to whichever test is open. Parallel
// tests that wait on each other (one closes a channel
// another reads) cannot run one at a time, and hang. In a TP test, the
// statements before Parallel run before the test begins, outside it: keploy
// serves their dependency calls from the recording's pool, and counts a call
// it has no answer for against the test that begins next. Run refuses to start
// a subtest there.
//
// Parallel test workers (several test binaries at once) arrive in a later
// release of keploy; until then run go test with -p 1, which the package
// requires.
package keploytest
