// Package cover lets Keploy measure which code each replayed test runs in a Go
// app, for Keploy's dynamic dedup and per-test code coverage.
//
// It needs the app built with coverage counters:
//
//	go build -cover -covermode=atomic ...
//
// and this package linked in, which an app does in one of two ways:
//
//   - a blank import in package main:
//
//     import _ "github.com/keploy/go-sdk/v3/cover"
//
//   - or, with no change to the app's code or go.mod, Keploy's build stage,
//     keploy-go-cover-build.sh (beside this file), run in a copy of the app's
//     module, such as a Dockerfile build stage: it copies this package into
//     the module as zz_keploy_cover, adds a blank import of it to package
//     main, builds with -cover -covermode=atomic over the module's own
//     packages, and removes both again.
//
// Once linked, the package listens before main runs on
// /tmp/coverage_control.sock for Keploy's START and END of each test: START
// resets the counters, END reads them in this process (no Go toolchain is
// needed where the app runs, so a distroless image, or a scratch image given
// a writable /tmp, works) and sends the lines each source file ran to
// /tmp/coverage_data.sock as one JSON record, {"executedLinesByFile": {"<import path>/<file>": [lines]}, "id":
// "<set>/<test>"}. An app built without -cover, or without
// -covermode=atomic, answers each START IGNORED and logs why once, so Keploy
// reports its tests as not measured rather than guessing.
//
// Between tests. The counters are reset at each START and again after each
// END, so every window is either one test's or a gap between two. A gap's
// lines are the app's own work while no test ran (timers, workers, pollers).
// A taken START answers "ACK proto=2 bg=-" until Keploy's agent asks for them
// (the BACKGROUND command, "BACKGROUND <token>") and "ACK proto=2 bg=<token>"
// after, so an agent whose token it is not (a new run, a restarted agent or
// app) asks; once asked, each gap
// goes to the agent at the next START as a BACKGROUND record, {"kind":
// "background", "before": <next test>, "after": <previous test>, "v": 1,
// "build": "go1-<meta hash>", "lost": <gaps lost since the ask>,
// "executedLinesByFile": ...}, and keploy dedup leaves the lines gaps of at
// least two different tests of a set ran out of that set's dedup keys. A test
// whose lines include any line a gap ran before its END, in this process, is
// sent coverage-partial ("contaminated": ["between-tests:<file>", ...]):
// keploy never drops it as a duplicate, and never drops another as its
// duplicate. A gap lost (not read or sent, or not taken as a gap: after an END
// not taken or a failed reset) is told to the set of the test after it by a
// lines-free BACKGROUND record ahead of that test's own, so keploy never takes
// the set as complete. Not marked: a test before the gap that shows a worker
// and any test after the app restarts (it starts with no gap seen), though
// keploy's set-wide background still leaves the worker's lines out of their
// keys; and a worker that never runs between tests, only inside them, which
// no gap shows, so its lines stay in the keys of the tests it runs inside.
//
// The package is stdlib-only and has no subpackages, so the build stage can
// copy it into any module. Its decoder is derived from Go's own
// internal/coverage (see LICENSE-go).
package cover
