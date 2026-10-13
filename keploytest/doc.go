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
// KEPLOY_MOCK_PROJECT (the project's directory). On Linux, keploy has go test
// run each test binary through its Go launcher (keploy-go-exec, -exec in
// GOFLAGS), which claims the run for the binary before any init runs (so a
// call an init or TestMain makes is the binary's), freezes its clock, refuses
// a go test that keploy's first release cannot judge, sets its -test.parallel
// and names the binary's pid in KEPLOY_TEST_CLAIMED; the package then claims
// nothing, and makes its checks at the first test. A run whose clock is frozen
// refuses a test binary started without the launcher, which only the launcher
// can freeze, and tells keploy's agent. Without the launcher, as a test binary
// starts, before the test package's own init and TestMain run, the package
// claims the run for that binary (its tests' IDs carry its package directory,
// relative to the project), and refuses what keploy's first release cannot
// judge: go test running more than one test binary at once (-p above 1, read
// from go test's command line, GOFLAGS or go env), and, at the first test, a
// run without -v or -json (a skip and its reason would be invisible), -shuffle
// and -fuzz. keploy's first release runs the test command natively on Linux,
// where the agent reads which process claims the run; elsewhere its agent
// refuses the claim. It sets the binary's -test.parallel, so Parallel can
// serialize. A refusal fails every test that has the line, and names the fix;
// the -p refusal is also told to keploy's agent, so keploy's verdict names it.
// Processes the binary starts (a helper, a binary built and run by a test)
// inherit KEPLOY_TEST_PID, which names it, and stay inert while they run
// inside it; one started in the background that outlives its link to the
// binary (sh -c 'helper &') claims like a test binary, and the agent refuses
// it while the binary runs.
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
// subtests after the body returns. A T test that calls t.Parallel() itself, or
// a TP test that calls it in place of Parallel, runs outside the turns and
// fails, by name, and every parallel test's turn waits for it to end. A test
// whose keploytest line comes after t.Parallel() is refused at that line; what
// it ran before the line is not serialized, and its calls can be charged to
// the open test. A raw-parallel T test stays open on the agent, so every later
// sequential T test fails too, saying which test to fix; raw-parallel tests
// are not serialized with each other, and the calls of one can be charged to
// another, which fails as well. A test with no keploytest line is not tracked
// at all: it runs whenever Go runs it, and its calls are charged to whichever
// test is open. Parallel tests that wait on each other (one closes a channel
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
