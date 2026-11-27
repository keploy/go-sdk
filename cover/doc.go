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
// /tmp/coverage_control.sock for Keploy's commands (START, END, BACKGROUND,
// TABLE): START resets the counters, END reads them in this process (no Go
// toolchain is needed where the app runs, so a distroless image, or a scratch
// image given a writable /tmp, works) and sends each test's coverage to
// /tmp/coverage_data.sock as one JSON record:
//
//	{"executedBlocksByFile":{"<pkg>/<file>":[0,2]},"executedLinesByFile":{"<file>":[lines]},"id":"<set>/<test>"}
//
// executedBlocksByFile holds sorted unique unit-relative leaf ordinals
// (0..len(unitLeaves)-1 in canonical build-table order, so ordinals remain
// stable if another unit is demoted) keyed by canonical "<pkg>/<base>"
// ("command-line-arguments/<base>" for command-line builds, whereas
// executedLinesByFile keeps the raw file path recorded by the compiler for
// backward compatibility); executedBlocksByFile is omitted when empty. An app
// built without -cover, or without -covermode=atomic, answers each START
// IGNORED and logs why once, so Keploy reports its tests as not measured
// rather than guessing.
//
// Build table and INDEX. Once coverage meta-data is available, the package
// sends an INDEX JSON record on /tmp/coverage_data.sock:
//
//	{"kind":"index","v":1,"language":"go","build":"go1-<meta hash>","producer":"go","producerVersion":"<runtime.Version()>","precision":"block"|"func","files":N,"leaves":M}
//
// It sends INDEX lazily on START (before resetting counters, after package
// main's init has initialized Go's coverage runtime) and again whenever START
// observes a new data socket file (receiver restart) or a changed BACKGROUND
// token.
// On the TABLE control command, it lazily builds and caches the canonical
// schema-1 Go build table (first-party packages only): if coverage meta-data
// is unavailable or holds no first-party files it answers
// "IGNORED proto=3 <reason>\n"; otherwise it answers "ACK proto=3 caps=...\n"
// immediately and streams KTB1 frames asynchronously on a single goroutine
// (coalescing concurrent TABLE requests so frames never interleave). Each KTB1
// frame carries "KTB1", a 4-byte big-endian body length (total frame at most
// 1 MiB), a JSON header line and payload chunk, streaming part "table" (NDJSON
// unit records, with record count and SHA-256 on last:true) followed by an
// empty part "probes" (records:0), and requires an "OK\n" reply per frame.
//
// Protocol v3 capabilities and goroutine-tail tracking. Every taken control
// command replies with "ACK proto=3 [bg=<token>] caps=..." declaring live
// runtime honesty capabilities (windowCut, contaminated, classMismatch,
// background, asyncExecutors, listenerContamination), compatible with proto>=2
// control-socket parsers. Go ALWAYS declares asyncExecutors=false in R1
// because runtime.Stack snapshots cannot observe all deferred hand-offs (see
// Scope limits below); collectors that gate removal eligibility on
// asyncExecutors=true therefore keep Go soft-mark-only. Go declares
// windowCut=true when block-level coverage, goroutine-tail tracking, and a
// Go 1.21+ toolchain (for parent-goid child adoption) are active, and
// windowCut=false on Go < 1.21 toolchains, under per-func granularity, or when
// KEPLOY_GO_GOROUTINE_TRACKING=off.
//
// At each START, the hook snapshots live goroutines and quiescent pre-existing
// app worker baselines via runtime.Stack (zero overhead when unarmed or when
// KEPLOY_GO_GOROUTINE_TRACKING=off), preserving each goroutine's established
// idle signature so a worker caught mid-job at START fails closed instead of
// adopting the mid-job stack as idle. At END, it checks for newly-spawned
// app-relevant goroutines AND pre-existing app workers/handlers still off
// their quiescent idle baseline (excluding go-sdk's own goroutines, idle
// stdlib keep-alive/housekeeping goroutines including idle HTTP keep-alive
// reads through a wrapped app net.Conn, and any patterns listed in
// KEPLOY_GO_IGNORE_GOROUTINES), polling adaptively up to KEPLOY_GO_END_WAIT_MS
// (default 200ms, max 2000ms; 0 checks immediately) for them to drain. If any
// remain active when the wait expires (or if runtime.Stack hits its 16 MiB
// cap, marked "stack-truncated" with a one-time diagnostic log advising to
// reduce live goroutines or set KEPLOY_GO_GOROUTINE_TRACKING=off, noting that
// KEPLOY_GO_IGNORE_GOROUTINES filters parsed stacks only), the test record
// carries "windowCut":true, "lingeringGoroutines", and
// "contaminated":["window-cut"] (when no start-time contamination mark is
// already present, so collectors reading contaminated still fail closed;
// k8s-proxy requires R1 to decode windowCut/contaminated) with
// overlapLinesByFile omitted. Subsequent tests carry
// "contaminated":["tail:<test>"] (with overlapLinesByFile omitted) on the
// immediate next test and while any lingering goroutine (or newly-spawned
// app-relevant child adopted via Go 1.21+ "created by ... in goroutine N")
// remains active without settling onto a resume watch.
//
// Pre-existing goroutines that transition to a different quiescent steady
// state no deeper than their baseline app-frame depth, as well as lazily
// started non-HTTP background goroutines parked in a non-sleep event wait
// (such as a sync.Once janitor or a lazily dialed third-party client over a
// wrapped net.Conn), move onto a one-time resume watch (bounded at 512 active
// watches) once stably parked at the same quiescent signature across
// consecutive windows (START -> END -> START, after birth test T1 was cut and
// T2 was tainted tail:T1). Subsequent tests run clean while the watched
// goroutine stays parked, and the gap or test in which any snapshot (including
// intermediate END_WAIT polls) observes it awake, exiting, or spawning a child
// is marked with afterCut / "tail:<test>" (re-attaching if still active).
// Stuck HTTP handlers (net/http, x/net/http2, ServeHTTP, RoundTrip) are never
// re-baselined. Any BACKGROUND record emitted for a gap after a cut window
// carries "afterCut":true with an empty executedLinesByFile (and does not
// pollute everGap) so collectors never absorb recurring async tails into
// calibrated stable background. A START arriving without a preceding END marks
// the new test with "contaminated":["unclosed:<test>"].
//
// Scope limits (why asyncExecutors=false in R1): runtime.Stack snapshots
// cannot detect (a) work handed off to library-owned third-party async
// writers/producers (such as kafka-go Writer{Async:true, Completion} or sarama
// AsyncProducer) whose goroutines park solely in uncovered library frames
// until invoking an app callback later, (b) dormant time.AfterFunc timers that
// have not yet fired by END (no goroutine exists yet), or (c) pre-existing app
// batchers/debouncers that dequeue work and re-park at the exact same idle
// signature before END. Likewise, a test-spawned periodic loop parked on a
// channel/select timer (for example select{case <-time.After(d):}) settles
// onto a resume watch after two windows (T1 cut, T2 tail:T1), so a later tick
// that runs and re-parks at the same signature strictly between two snapshots
// is not observed; snapshots also cannot observe a child goroutine or watched
// wakeup that starts and returns/exits strictly between two snapshots (nor a
// grandchild orphaned when its intermediate parent spawns and exits between
// snapshots), nor work queued in a channel buffer for a busy pre-existing
// worker until dequeued; parent-goid child adoption relies on Go 1.21+
// "created by ... in goroutine N" stack lines (windowCut=false on Go < 1.21
// toolchains).
//
// Between tests. The counters are reset at each START and again after each
// END, so every window is either one test's or a gap between two. A gap's
// lines are the app's own work while no test ran (timers, workers, pollers).
// A taken START answers "ACK proto=3 bg=- caps=..." until Keploy's agent asks
// for them (the BACKGROUND command, "BACKGROUND <token>") and
// "ACK proto=3 bg=<token> caps=..." after, so an agent whose token it is not
// (a new run, a restarted agent or app) asks; once asked, each gap
// goes to the agent at the next START as a BACKGROUND record, {"kind":
// "background", "before": <next test>, "after": <previous test>, "v": 1,
// "build": "go1-<meta hash>", "lost": <gaps lost since the ask>,
// "executedLinesByFile": ...}, and keploy dedup leaves the lines gaps of at
// least two different tests of a set ran out of that set's dedup keys. A test
// whose lines include any line a gap ran before its END, in this process, is
// sent coverage-partial ("contaminated": ["between-tests:<file>", ...], capped
// at 8 files plus "+N") alongside "overlapLinesByFile": {"<file>": [lines]}
// (every overlapping file with its sorted, unique overlapping lines, uncapped).
// An agent that ignores overlapLinesByFile, or whose set's background is
// incomplete, still fails closed (keploy never drops the test as a duplicate,
// and never drops another as its duplicate); an agent with a complete set
// background that removes every overlapping line from the set's keys may
// confirm and trust the test. A gap lost (not read or sent, or not taken as a
// gap: after an END not taken or a failed reset) is told to the set of the
// test after it by a lines-free BACKGROUND record ahead of that test's own, so
// keploy never takes the set as complete. Not marked: a test before the gap that shows a worker
// and any test after the app restarts (it starts with no gap seen), though
// keploy's set-wide background still leaves the worker's lines out of their
// keys; and a worker that never runs between tests, only inside them, which
// no gap shows, so its lines stay in the keys of the tests it runs inside.
//
// The package is stdlib-only and has no subpackages, so the build stage can
// copy it into any module. Its decoder is derived from Go's own
// internal/coverage (see LICENSE-go).
package cover
