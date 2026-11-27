package cover

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"runtime/coverage"
	"sort"
	"strings"
	"sync"
	"time"
)

// controlSocketPath is where Keploy sends this app its commands (START, END).
// Keploy's agent reaches it in the app's own /tmp: on the same host, through
// a shared /tmp volume (docker, compose), or, in a Kubernetes replay pod,
// through /proc/<app pid>/root/tmp. A variable, not a constant, only so this
// package's tests (and a test build's -ldflags -X) bind a path of their own.
var controlSocketPath = "/tmp/coverage_control.sock"

// dataSocketPath is where this app sends Keploy each test's coverage. A
// variable for the same reason as controlSocketPath.
var dataSocketPath = "/tmp/coverage_data.sock"

// The answers on the control socket: one line for every command read, written
// before the connection is closed. ACK says the command was taken: the
// counters were reset for a START, or the test's coverage was sent for an
// END. IGNORED says it was read and not taken. proto=3 says this SDK answers
// every command it reads (compatible with proto>=2 parsers) and advertises
// live honesty capabilities in caps=<k=v,...>.
const (
	replyIgnored = "IGNORED proto=3\n"
	// A START taken is answered with whose BACKGROUND ask this process
	// holds: "bg=-" until Keploy's agent asks, then "bg=<the ask's token>",
	// so an agent asks when the token is not its own (a new run, a restarted
	// agent, a restarted app). An SDK not to be asked at a START says no bg:
	// an older Go SDK, which sends no BACKGROUND record, and the Java SDK,
	// which is asked after its INDEX.
	bgUnasked = "-"
)

// capsString formats the v3 honesty capability token (no spaces) from live
// runtime state. Called with controlMu held (or after verifying coverage).
//
// Go honestly declares asyncExecutors=false because runtime.Stack snapshots
// cannot observe work handed off to third-party async writers/producers (e.g.
// kafka-go / sarama), dormant time.AfterFunc timers not yet fired by END, or
// pre-existing app batchers/debouncers that re-park at the same idle signature
// before END; Go removal eligibility therefore comes from a collector-side
// isolation check rather than SDK capabilities alone. Go also declares
// windowCut=false on Go < 1.21 toolchains (which lack "created by ... in
// goroutine N" parent-goid lines) or when KEPLOY_GO_GOROUTINE_TRACKING=off.
func capsString(coverageActive bool) string {
	tracking := goroutineTrackingActive()
	windowCut := coverageActive && tracking && supportsGoroutineChildAdoption(runtimeGoVersion())
	contaminated := coverageActive
	background := coverageActive
	return fmt.Sprintf(
		"windowCut=%t,contaminated=%t,classMismatch=false,background=%t,asyncExecutors=false,listenerContamination=false",
		windowCut, contaminated, background,
	)
}

// ackReply formats a taken non-START command's v3 ACK line with live caps.
func ackReply() string {
	return "ACK proto=3 caps=" + capsString(true) + "\n"
}

var (
	// controlMu makes each command atomic: one START or END at a time.
	controlMu sync.Mutex
	// currentTestID is the test whose START was taken and whose END has not
	// come yet, or "".
	currentTestID string

	// prevTestID is the test whose END was taken last and whose counters
	// were cleared after it: the window from then to the next START is a gap
	// between tests, and prevTestID opened it. "" when there is no such gap
	// to trust (no END yet, a command not taken, a reset that failed).
	prevTestID string
	// bgAsked says Keploy's agent asked for BACKGROUND records (the
	// BACKGROUND command), bgToken is the token its ask named (its receiver;
	// "" for an ask that named none), and bgLost counts the gap records
	// since that ask that could not be read or sent.
	bgAsked bool
	bgToken string
	bgLost  int
	// bgShown is the lost count the last record sent to that receiver
	// carried, and lastTest the last test whose START was taken: once asked,
	// a START after a test with no gap to send (its END not taken, its reset
	// failed) counts a lost gap.
	bgShown  int
	lastTest string
	// everGap is every line any gap window ran, of any test set, since the
	// app started: a test whose lines meet it may hold the app's
	// between-tests work (overlapBy).
	everGap = map[string]map[int]struct{}{}

	// prepareMeta reads the app's coverage meta-data at a START, once;
	// clearCounters and publishCoverage reset the counters at a START and
	// send a test's coverage at an END. Variables for tests.
	prepareMeta     = func() error { _, err := loadMeta(); return err }
	clearCounters   = coverage.ClearCounters
	publishCoverage = reportCoverage
	// snapshotLines reads the lines run since the counters were last
	// cleared (used for between-tests gaps), snapshotCoverage reads both
	// lines and unit-relative blocks in one counter snapshot (used on END),
	// sendRecord sends one record to Keploy, and buildID names the app's
	// build. Variables for tests.
	snapshotLines    = executedLines
	snapshotCoverage = executedLinesAndBlocks
	sendRecord       = sendToSocket
	buildID          = goBuildID

	// INDEX delivery state, guarded by controlMu: sent once per data socket
	// file (sameSocketFile) and re-sent whenever a new receiver appears or the
	// BACKGROUND token changes.
	indexSent     bool
	indexSockInfo os.FileInfo
	indexBgToken  string

	// TABLE streaming state, guarded by tableStreamMu so concurrent TABLE asks
	// coalesce cleanly and never interleave KTB1 frames on the data socket.
	tableStreamMu  sync.Mutex
	tableStreaming bool
	tableRequeue   bool
	streamTableFn  = streamTable

	// saidWhy holds the reasons a START was not taken that were already
	// logged, so an app that cannot be measured says why once, not per test.
	saidWhy = map[string]bool{}
)

// init binds the control socket before the app's main runs, so it is there
// before the app can say it is ready: bound on a goroutine, the bind could
// come after the app's port, and the first test's START then found no one
// listening and its coverage was lost. It then serves Keploy's commands on a
// goroutine. Go's coverage runtime is only initialized in package main's init
// (which runs after this package's init), so coverage metadata and INDEX are
// read and sent lazily on the first START rather than from init.
func init() {
	if _, err := startControlServer(controlSocketPath); err != nil {
		log.Printf("[keploy coverage] could not listen for Keploy's coverage commands on %s, so no test of this run is measured: %v", controlSocketPath, err)
	}
}

// startControlServer binds the Unix socket at path, replacing a stale one,
// and returns its listener once it is bound, serving Keploy's commands on a
// goroutine until the listener is closed.
func startControlServer(path string) (net.Listener, error) {
	if err := os.RemoveAll(path); err != nil {
		return nil, fmt.Errorf("remove the old control socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go serveControl(ln)
	return ln, nil
}

// commandReadTimeout bounds how long a connection may take to send its
// command: one that sends none holds no goroutine for ever.
const commandReadTimeout = 30 * time.Second

// serveControl handles each connection on ln until ln is closed. An accept
// that keeps failing (no file descriptor left) is retried after a pause that
// grows to a second, never in a tight loop.
func serveControl(ln net.Listener) {
	defer func() { _ = ln.Close() }()
	pause := 5 * time.Millisecond
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("[keploy coverage] error accepting a command: %v", err)
			time.Sleep(pause)
			if pause *= 2; pause > time.Second {
				pause = time.Second
			}
			continue
		}
		pause = 5 * time.Millisecond
		go handleControlRequest(conn)
	}
}

// handleControlRequest reads one command ("START <set>/<test>", "END
// <set>/<test>"), runs it, and answers it before it closes the connection.
func handleControlRequest(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(commandReadTimeout))
	command, err := bufio.NewReader(conn).ReadString('\n')
	switch {
	case errors.Is(err, io.EOF) && strings.TrimSpace(command) == "":
		// A connection closed with no command (a probe of the socket).
		return
	case err != nil && !errors.Is(err, io.EOF):
		log.Printf("[keploy coverage] error reading a command: %v", err)
		return
	}
	// A command closed by EOF with no newline is run and answered too.
	reply := runCommand(strings.TrimSpace(command))
	if _, err := conn.Write([]byte(reply)); err != nil {
		log.Printf("[keploy coverage] error answering %q: %v", strings.TrimSpace(reply), err)
	}
}

// backgroundAsk is the control command with which Keploy's agent asks for
// BACKGROUND records: the lines the app ran between two tests.
const backgroundAsk = "BACKGROUND"

// tableAsk is the control command with which Keploy's agent asks for the
// build's coverage table as KTB1 frames on the data socket.
const tableAsk = "TABLE"

// replyIgnoredWith returns a single-line IGNORED proto=3 reply carrying reason.
func replyIgnoredWith(reason string) string {
	reason = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(reason, "\r", " "), "\n", " "))
	if reason == "" {
		return replyIgnored
	}
	return "IGNORED proto=3 " + reason + "\n"
}

// sameSocketFile reports whether a and b describe the same Unix socket file
// instance. Checking ModTime alongside os.SameFile catches Linux tmpfs/ext4
// inode reuse when an agent unlinks and immediately re-binds the data socket.
func sameSocketFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode() && a.Size() == b.Size()
}

// maybeSendIndexLocked sends the Go INDEX record on START when it has not yet
// been delivered to the current receiver (or the receiver's socket file or
// BACKGROUND token changed). Best-effort: never fails START. Called with
// controlMu held.
func maybeSendIndexLocked() {
	if meta == nil {
		return
	}
	fi, statErr := os.Stat(dataSocketPath)
	if statErr != nil {
		if indexSent && indexSockInfo != nil {
			// The old data socket disappeared; re-send when a new one appears.
			indexSent = false
			indexSockInfo = nil
		}
		return
	}
	tbl, err := loadTable()
	if err != nil || tbl == nil {
		return
	}
	need := !indexSent || (bgAsked && bgToken != indexBgToken) || !sameSocketFile(indexSockInfo, fi)
	if !need {
		return
	}
	payload, err := tbl.indexBytes()
	if err != nil {
		return
	}
	if err := sendRecord(payload); err != nil {
		return
	}
	indexSent = true
	indexSockInfo = fi
	indexBgToken = bgToken
}

// handleTableCommand handles the TABLE control command: lazily builds the
// cached Go build table, replies IGNORED proto=3 <reason> when unavailable or
// empty, otherwise replies ACK proto=3 caps=... immediately and streams KTB1
// frames asynchronously, coalescing concurrent TABLE asks so two streams never
// interleave.
func handleTableCommand() string {
	controlMu.Lock()
	if err := prepareMeta(); err != nil {
		sayWhyNotMeasured(err)
		controlMu.Unlock()
		return replyIgnoredWith(err.Error())
	}
	tbl, err := loadTable()
	if err != nil {
		controlMu.Unlock()
		return replyIgnoredWith(err.Error())
	}
	if tbl == nil || tbl.unitCount() == 0 {
		controlMu.Unlock()
		return replyIgnoredWith("no first-party Go source files in coverage metadata")
	}
	reply := ackReply()
	controlMu.Unlock()

	tableStreamMu.Lock()
	if tableStreaming {
		tableRequeue = true
		tableStreamMu.Unlock()
		return reply
	}
	tableStreaming = true
	tableRequeue = false
	streamFn := streamTableFn
	tableStreamMu.Unlock()

	go runTableStream(tbl, streamFn)
	return reply
}

// runTableStream streams tbl's KTB1 frames on a single goroutine, coalescing
// concurrent TABLE asks cleanly.
func runTableStream(tbl *goTable, streamFn func(*goTable) error) {
	for {
		startFI, _ := os.Stat(dataSocketPath)
		err := streamFn(tbl)
		if err != nil {
			log.Printf("[keploy coverage] build table stream stopped: %v", err)
		}
		tableStreamMu.Lock()
		again := tableRequeue
		tableRequeue = false
		if again && err == nil {
			curFI, curErr := os.Stat(dataSocketPath)
			sameTarget := (startFI != nil && curErr == nil && sameSocketFile(startFI, curFI)) ||
				(startFI == nil && curErr != nil)
			if sameTarget {
				again = false
			}
		}
		if !again {
			tableStreaming = false
			tableStreamMu.Unlock()
			return
		}
		streamFn = streamTableFn
		tableStreamMu.Unlock()
	}
}

// startReply is a taken START's answer: whose BACKGROUND ask this process
// holds, plus the live v3 honesty capabilities. Called with controlMu held, in
// the START's own hold, so the answer is the state the START left.
func startReply() string {
	token := bgUnasked
	if bgAsked {
		token = bgToken
	}
	return "ACK proto=3 bg=" + token + " caps=" + capsString(true) + "\n"
}

// runCommand runs one command and returns its answer.
func runCommand(command string) string {
	if command == tableAsk {
		return handleTableCommand()
	}
	if command == backgroundAsk || strings.HasPrefix(command, backgroundAsk+" ") {
		token := strings.TrimSpace(strings.TrimPrefix(command, backgroundAsk))
		controlMu.Lock()
		defer controlMu.Unlock()
		// An app that cannot be measured has no gap to tell of either.
		if err := prepareMeta(); err != nil {
			sayWhyNotMeasured(err)
			return replyIgnored
		}
		// The records go to the receiver that asked: a new one (a new keploy
		// run, an agent restarted: another token) counts their losses
		// afresh. A gap open now is still read and sent at the next START;
		// one whose "after" is an earlier run's test is no window of the
		// new run's (keploy's ranNext), but its lines still mark the tests
		// that meet them.
		// lastTest stays: the ask comes after this receiver's first START,
		// so lastTest is already its test.
		if !bgAsked || token != bgToken {
			bgLost, bgShown = 0, 0
		}
		bgAsked, bgToken = true, token
		return ackReply()
	}
	return runTestCommand(command)
}

// runTestCommand runs a START or END and returns its answer: replyIgnored for
// one not taken, startReply (made in the same hold of controlMu) for a START
// taken, ackReply for an END taken.
func runTestCommand(command string) string {
	action, id, ok := strings.Cut(command, " ")
	if !ok || id == "" {
		log.Printf("[keploy coverage] command %q names no test", command)
		return replyIgnored
	}
	controlMu.Lock()
	defer controlMu.Unlock()
	switch action {
	case "START":
		// The meta-data is read before the counters are reset, so reading it
		// is no test's work, and an app whose coverage cannot be read says
		// so at its first START, not at each END.
		if err := prepareMeta(); err != nil {
			currentTestID, prevTestID = "", ""
			activeTracking, activeTruncatedStart, startGids, startIdleStates, activeContaminated = false, false, nil, nil, nil
			sayWhyNotMeasured(err)
			return replyIgnored
		}
		// Read gap counters BEFORE taking the stop-the-world stack dump so a
		// watched goroutine that wakes during stack parsing cannot land lines
		// into a gap marked afterCut=false.
		var gapLines map[string][]int
		var gapErr error
		haveGap := prevTestID != "" && currentTestID == ""
		if haveGap {
			gapLines, gapErr = snapshotLines()
		}
		tracking := goroutineTrackingActive()
		covered := coveredPackageSet()
		ignore := ignoreGoroutinePatterns()
		var curSnap goroutineSnapshot
		haveSnap := false
		if tracking || len(lingeringTails) > 0 || len(pendingTruncatedCuts) > 0 || len(resumeWatches) > 0 {
			curSnap = snapshotGoroutines()
			haveSnap = true
			if curSnap.truncated {
				logStackTruncatedOnce()
			}
		}
		// Check one-time resume watches BEFORE unclosed-window handling and
		// BEFORE computing afterCut so a watched goroutine that woke up during
		// the gap/unclosed window marks the gap afterCut=true and re-attaches
		// cleanly without being duplicated into two tails.
		var firedWatches []string
		var skipStreak map[uint64]struct{}
		if haveSnap && !curSnap.truncated {
			firedWatches, _, skipStreak = checkResumeWatches(curSnap, startIdleStates, covered, ignore)
		}
		unclosedID := currentTestID
		if unclosedID != "" && activeTracking && haveSnap {
			if curSnap.truncated || activeTruncatedStart || startGids == nil {
				// Fail closed on stack truncation during unclosed window.
				prevWindowCutTestID = unclosedID
				base := startGids
				if base == nil {
					base = buildTruncatedStartUnion(curSnap)
				}
				pendingTruncatedCuts = append(pendingTruncatedCuts, truncatedCutWindow{
					testID:    unclosedID,
					startGids: base,
				})
				if len(pendingTruncatedCuts) > maxLingeringTails {
					pendingTruncatedCuts = pendingTruncatedCuts[len(pendingTruncatedCuts)-maxLingeringTails:]
				}
			} else if alive := findActiveTestGoroutines(curSnap, startGids, startIdleStates, covered, ignore); len(alive) > 0 {
				var targets []lingeringTarget
				for _, ag := range alive {
					if ag.alreadyAtIdle {
						armMarkOnlyWatch(unclosedID, ag, curSnap)
						continue
					}
					targets = append(targets, lingeringTarget{gid: ag.info.id, mustExit: ag.mustExit, idleSig: ag.idleSig})
					if skipStreak == nil {
						skipStreak = make(map[uint64]struct{}, len(alive))
					}
					skipStreak[ag.info.id] = struct{}{}
				}
				if len(targets) > 0 {
					addLingeringTail(unclosedID, targets, capturePreExistingChildren(curSnap, targets, startGids))
				}
			}
		}
		// Compute whether the gap since prevTestID followed a cut window, ran
		// while lingering goroutines were still tracked, woke a resume watch,
		// or hit a truncated snapshot while watches were armed (fail-closed)
		// BEFORE pruning lingeringTails.
		afterCut := prevWindowCutTestID != "" || len(lingeringTails) > 0 || len(pendingTruncatedCuts) > 0 ||
			len(firedWatches) > 0 || len(pendingTailTestIDs) > 0 || (haveSnap && curSnap.truncated && len(resumeWatches) > 0)
		// The counters hold the gap since the last test's END: what the app
		// ran between the two tests, never this test's or that one's.
		switch {
		case haveGap:
			noteGap(prevTestID, id, gapLines, gapErr, afterCut)
		case bgAsked && lastTest != "":
			// The window since the last test holds more than a gap (a
			// test whose END was not taken, a failed reset): keploy is
			// told it lost one.
			bgLost++
		}
		prevTestID = ""
		// Lazily deliver INDEX before resetting counters so any work done to
		// send it never leaks into this test's counter window.
		maybeSendIndexLocked()
		// A START whose counters were not reset is not taken: the test's
		// coverage would hold the lines of the tests before it.
		if err := clearCounters(); err != nil {
			currentTestID = ""
			activeTracking, activeTruncatedStart, startGids, startIdleStates, activeContaminated = false, false, nil, nil, nil
			if len(firedWatches) > 0 {
				pendingTailTestIDs = append(pendingTailTestIDs, firedWatches...)
			}
			sayWhyNotMeasured(err)
			return replyIgnored
		}
		activeContaminated = collectStartContamination(unclosedID, curSnap, haveSnap, firedWatches, skipStreak, covered, ignore)
		if tracking {
			activeTracking = true
			if curSnap.truncated {
				// Record the union of the last complete baseline and visible
				// gids so END can register a pending truncated cut window that
				// resolves hidden goroutines on the next complete snapshot.
				activeTruncatedStart = true
				startGids = buildTruncatedStartUnion(curSnap)
				startIdleStates = nil
			} else {
				activeTruncatedStart = false
				startGids, startIdleStates = snapshotStartBaseline(curSnap, covered, ignore)
			}
		} else {
			activeTracking = false
			activeTruncatedStart = false
			startGids = nil
			startIdleStates = nil
		}
		currentTestID, lastTest = id, id
		return startReply()
	case "END":
		if currentTestID != id {
			// An app that cannot be measured already said why at its START.
			if currentTestID != "" || len(saidWhy) == 0 {
				log.Printf("[keploy coverage] END for %q while the test started is %q: its coverage is not sent", id, currentTestID)
			}
			prevTestID = ""
			return replyIgnored
		}
		currentTestID = ""
		testTracking := activeTracking
		testTruncatedStart := activeTruncatedStart
		testStartGids := startGids
		testStartIdle := startIdleStates
		testContaminated := activeContaminated
		activeTracking, activeTruncatedStart, startGids, startIdleStates, activeContaminated = false, false, nil, nil, nil

		windowCut := false
		var lingeringNames []string
		if testTracking {
			covered := coveredPackageSet()
			ignore := ignoreGoroutinePatterns()
			if testTruncatedStart {
				windowCut = true
				lingeringNames = []string{"stack-truncated"}
				prevWindowCutTestID = id
				// Fail closed beyond T+1: retain the pre-truncation union
				// baseline so the next complete snapshot adopts any goroutine
				// spawned during this truncated-START test.
				pendingTruncatedCuts = append(pendingTruncatedCuts, truncatedCutWindow{
					testID:    id,
					startGids: testStartGids,
				})
				if len(pendingTruncatedCuts) > maxLingeringTails {
					pendingTruncatedCuts = pendingTruncatedCuts[len(pendingTruncatedCuts)-maxLingeringTails:]
				}
			} else {
				waitDur := endWaitDuration()
				preEndLingering := lingeringGidSet()
				var watchOrigIdle map[uint64]goroutineSig
				snap := snapshotGoroutines()
				if !snap.truncated {
					testContaminated = checkEndWatchesAndStreaks(snap, testContaminated, testStartIdle, &watchOrigIdle, covered, ignore)
				}
				active := findActiveTestGoroutinesWithLingering(snap, testStartGids, testStartIdle, preEndLingering, covered, ignore)
				// Never burn END_WAIT repeating stop-the-world 16 MiB dumps when
				// already truncated, or when the only active entries are drifted
				// goroutines that have already returned to their idle baseline.
				if !snap.truncated && hasWaitableActive(active) && waitDur > 0 {
					deadline := time.Now().Add(waitDur)
					poll := endWaitPollMin
					for !snap.truncated && hasWaitableActive(active) {
						rem := time.Until(deadline)
						if rem <= 0 {
							break
						}
						sleep := poll
						if rem < sleep {
							sleep = rem
						}
						time.Sleep(sleep)
						if poll < endWaitPollMax {
							poll *= 2
							if poll > endWaitPollMax {
								poll = endWaitPollMax
							}
						}
						snap = snapshotGoroutines()
						if !snap.truncated {
							testContaminated = checkEndWatchesAndStreaks(snap, testContaminated, testStartIdle, &watchOrigIdle, covered, ignore)
						}
						active = findActiveTestGoroutinesWithLingering(snap, testStartGids, testStartIdle, preEndLingering, covered, ignore)
					}
				}
				// Resolve any prior truncated cuts and adopt/prune lingering
				// goroutines before this END completes (resumeWatches and
				// broken streaks were already checked on every non-truncated
				// END snapshot above).
				if !snap.truncated {
					resolveTruncatedCutWindows(snap, covered, ignore)
					pruneLingeringTails(snap, nil, covered, ignore, false)
					// Refresh lastCompleteGids with non-SDK gids from this complete END.
					complete := make(map[uint64]struct{}, len(snap.all))
					for gid, g := range snap.all {
						if !isSDKGoroutine(g, snap.selfID) {
							complete[gid] = struct{}{}
						}
					}
					lastCompleteGids = complete
				}
				if snap.truncated {
					logStackTruncatedOnce()
					windowCut = true
					var targets []lingeringTarget
					for _, ag := range active {
						if ag.alreadyAtIdle {
							armMarkOnlyWatch(id, ag, snap)
							continue
						}
						targets = append(targets, lingeringTarget{gid: ag.info.id, mustExit: ag.mustExit, idleSig: ag.idleSig})
					}
					if len(targets) > 0 {
						addLingeringTail(id, targets, capturePreExistingChildren(snap, targets, testStartGids))
					}
					pendingTruncatedCuts = append(pendingTruncatedCuts, truncatedCutWindow{
						testID:    id,
						startGids: testStartGids,
					})
					if len(pendingTruncatedCuts) > maxLingeringTails {
						pendingTruncatedCuts = pendingTruncatedCuts[len(pendingTruncatedCuts)-maxLingeringTails:]
					}
					lingeringNames = []string{"stack-truncated"}
					prevWindowCutTestID = id
				} else if len(active) > 0 {
					windowCut = true
					var targets []lingeringTarget
					for _, ag := range active {
						if ag.alreadyAtIdle {
							armMarkOnlyWatch(id, ag, snap)
							continue
						}
						targets = append(targets, lingeringTarget{gid: ag.info.id, mustExit: ag.mustExit, idleSig: ag.idleSig})
					}
					lingeringNames = summarizeActiveNames(active, covered)
					if len(targets) > 0 {
						addLingeringTail(id, targets, capturePreExistingChildren(snap, targets, testStartGids))
						logLingeringOnce(id, lingeringNames)
					}
					prevWindowCutTestID = id
				}
			}
		}

		if bgAsked && bgLost > bgShown {
			sendLost(id)
		}
		pendingEndWindowCut = windowCut
		pendingEndLingering = lingeringNames
		pendingEndContaminated = testContaminated
		err := publishCoverage(id)
		pendingEndWindowCut = false
		pendingEndLingering = nil
		pendingEndContaminated = nil

		// The counters are reset after every END, so the window to the next
		// START is a gap between tests alone. One whose reset failed holds
		// this test's lines too, and is no gap to trust.
		cerr := clearCounters()
		if cerr == nil {
			prevTestID = id
		} else {
			prevTestID = ""
			log.Printf("[keploy coverage] could not reset the counters after test %s, so the window to the next test is not taken as a gap: %v", id, cerr)
		}
		if err != nil {
			log.Printf("[keploy coverage] could not send the coverage of test %s: %v", id, err)
			return replyIgnored
		}
		return ackReply()
	default:
		log.Printf("[keploy coverage] unknown command %q", action)
		return replyIgnored
	}
}

// noteGap records the window from after's END to before's START (pre-read
// before stack snapshotting so a watched goroutine waking during stack parsing
// cannot land lines in an afterCut=false gap), keeps its lines in everGap
// (unless afterCut is true so async tails from cut windows never pollute
// between-tests tracking), and once the agent asked, sends it as a BACKGROUND
// record. When afterCut is true, the record carries "afterCut":true with an
// empty executedLinesByFile map so neither v3 collectors nor older v2
// collectors (which ignore unknown fields) can ever absorb an async tail into
// calibrated stable background. Called with controlMu held.
func noteGap(after, before string, lines map[string][]int, err error, afterCut bool) {
	if err != nil {
		if bgAsked {
			bgLost++
		}
		log.Printf("[keploy coverage] could not read what the app ran between tests %s and %s: %v", after, before, err)
		return
	}
	if !afterCut {
		for file, ls := range lines {
			ever := everGap[file]
			if ever == nil {
				ever = map[int]struct{}{}
				everGap[file] = ever
			}
			for _, l := range ls {
				ever[l] = struct{}{}
			}
		}
	}
	if !bgAsked || len(lines) == 0 {
		return
	}
	emitLines := lines
	if afterCut {
		emitLines = map[string][]int{}
	}
	if err := sendBackgroundWithMeta(after, before, emitLines, afterCut); err != nil {
		log.Printf("[keploy coverage] could not send what the app ran between tests %s and %s: %v", after, before, err)
	}
}

// sendLost tells keploy, ahead of test's own record, of the gaps lost since
// the last record sent: a BACKGROUND record of test's set with no lines and
// no "after" (so it is no window), only the lost count, so the set the loss
// is of is never taken as complete. Called with controlMu held.
func sendLost(test string) {
	if err := sendBackgroundWithMeta("", test, map[string][]int{}, false); err != nil {
		log.Printf("[keploy coverage] could not tell keploy of the gaps between tests it lost before test %s: %v", test, err)
	}
}

// sendBackgroundWithMeta sends one BACKGROUND record, carrying bgLost and
// optional afterCut; one not sent counts as lost too. Called with controlMu held.
func sendBackgroundWithMeta(after, before string, lines map[string][]int, afterCut bool) error {
	data, err := encodeBackgroundWithMeta(after, before, lines, afterCut)
	if err == nil {
		err = sendRecord(data)
	}
	if err != nil {
		bgLost++
		return err
	}
	bgShown = bgLost
	return nil
}

// backgroundRecordV is the version of the BACKGROUND records this package
// sends.
const backgroundRecordV = 1

// encodeBackground encodes a clean BACKGROUND record (afterCut=false).
func encodeBackground(after, before string, lines map[string][]int) ([]byte, error) {
	return encodeBackgroundWithMeta(after, before, lines, false)
}

// encodeBackgroundWithMeta is a BACKGROUND record: the lines the app ran
// between tests after and before. "kind" and "before" come first, so an agent
// that reads only a record's head still knows its test set. When afterCut is
// true (the gap followed a windowCut test or had active lingering goroutines),
// "afterCut":true is set so collectors never absorb an async tail into stable
// background.
func encodeBackgroundWithMeta(after, before string, lines map[string][]int, afterCut bool) ([]byte, error) {
	build, err := buildID()
	if err != nil {
		return nil, err
	}
	head, err := json.Marshal(struct {
		Kind     string `json:"kind"`
		Before   string `json:"before"`
		After    string `json:"after"`
		V        int    `json:"v"`
		Build    string `json:"build"`
		Lost     int    `json:"lost"`
		AfterCut bool   `json:"afterCut,omitempty"`
	}{"background", before, after, backgroundRecordV, build, bgLost, afterCut})
	if err != nil {
		return nil, fmt.Errorf("encode what the app ran between tests: %w", err)
	}
	body, err := json.Marshal(lines)
	if err != nil {
		return nil, fmt.Errorf("encode what the app ran between tests: %w", err)
	}
	// head without its closing brace, then the lines.
	out := append(head[:len(head)-1:len(head)-1], `,"executedLinesByFile":`...)
	out = append(out, body...)
	return append(out, '}'), nil
}

// maxPartialNames bounds how many files a coverage-partial record names.
const maxPartialNames = 8

// overlapBy returns the coverage-partial marks ("between-tests:<file>", at
// most maxPartialNames then "+N") and the exact overlapping lines per file
// for lines (a test's) that some gap between tests ran too, before this test's
// END, in this process.
//
// A record that names any is coverage-partial ("contaminated") so an older
// agent or an incomplete background still fails closed and neither drops the
// test nor drops another as its duplicate. Sending the complete overlap lines
// alongside lets a Keploy that holds the set's complete between-tests
// background confirm that every overlapping line is subtracted from the set's
// dedup keys and trust the test. Called with controlMu held.
func overlapBy(lines map[string][]int) ([]string, map[string][]int) {
	var files []string
	var overlap map[string][]int
	for file, ls := range lines {
		ever := everGap[file]
		if ever == nil {
			continue
		}
		var hit []int
		for _, l := range ls {
			if _, seen := ever[l]; seen {
				hit = append(hit, l)
			}
		}
		if len(hit) == 0 {
			continue
		}
		// linesOf already returns sorted unique lines, but defensively sort
		// and deduplicate so synthetic/test snapshotLines cannot emit dupes.
		sort.Ints(hit)
		hit = compactSortedInts(hit)
		if overlap == nil {
			overlap = make(map[string][]int)
		}
		overlap[file] = hit
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil, nil
	}
	sort.Strings(files)
	names := make([]string, 0, maxPartialNames+1)
	for i, f := range files {
		if i == maxPartialNames {
			names = append(names, fmt.Sprintf("+%d", len(files)-maxPartialNames))
			break
		}
		names = append(names, "between-tests:"+f)
	}
	return names, overlap
}

// compactSortedInts deduplicates an already-sorted slice in place.
func compactSortedInts(s []int) []int {
	if len(s) < 2 {
		return s
	}
	w := 1
	for i := 1; i < len(s); i++ {
		if s[i] != s[w-1] {
			s[w] = s[i]
			w++
		}
	}
	return s[:w]
}

// sayWhyNotMeasured logs, once per reason, why a START was not taken. Called
// with controlMu held.
func sayWhyNotMeasured(err error) {
	why := err.Error()
	switch {
	case strings.Contains(why, "not built with -cover"):
		why = "the app was built without -cover, so Keploy cannot measure its tests' coverage: build it with go build -cover -covermode=atomic (keploy-go-cover-build.sh does)"
	case strings.Contains(why, "covermode"):
		why = "the app needs -covermode=atomic for Keploy to measure each test: " + why
	}
	if saidWhy[why] {
		return
	}
	saidWhy[why] = true
	log.Printf("[keploy coverage] not measuring tests: %s", why)
}

// reportCoverage sends the test's coverage to Keploy: the lines it ran, per
// source file, unit-relative executed blocks when available, goroutine-tail
// windowCut / lingeringGoroutines metadata, and contamination marks (from
// unclosed windows, lingering async tails, or between-tests overlap). Called
// with controlMu held.
func reportCoverage(testID string) error {
	lines, blocks, err := snapshotCoverage()
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		log.Printf("[keploy coverage] test %s ran no measured line", testID)
	}
	gapPartial, overlap := overlapBy(lines)
	startMarks := pendingEndContaminated
	if len(startMarks) > 0 || pendingEndWindowCut {
		// Never emit overlapLinesByFile alongside start-time contamination
		// (tail/unclosed/stack-truncated) or a windowCut test, so a collector's
		// between-tests overlap confirmation cannot override it.
		overlap = nil
	}
	if pendingEndWindowCut && len(startMarks) == 0 {
		// Also mark contaminated:["window-cut"] when no start-time mark is
		// already present so collectors that read contaminated before
		// windowCut still fail closed on the cut test itself.
		startMarks = []string{"window-cut"}
	}
	partial := mergeContaminated(startMarks, gapPartial)
	data, err := encodeRecordWithMeta(testID, lines, blocks, partial, overlap, pendingEndWindowCut, pendingEndLingering)
	if err != nil {
		return err
	}
	return sendRecord(data)
}

// encodeRecordWithBlocks encodes a test's coverage record without windowCut or
// lingeringGoroutines metadata.
func encodeRecordWithBlocks(testID string, lines, blocks map[string][]int, partial []string, overlap map[string][]int) ([]byte, error) {
	return encodeRecordWithMeta(testID, lines, blocks, partial, overlap, false, nil)
}

// encodeRecordWithMeta is a test's coverage record: a map, as the SDK always
// sent, so its keys are written sorted ("contaminated", when partial names a
// reason, "executedBlocksByFile" when blocks holds unit-relative block
// ordinals, "executedLinesByFile", "id", "lingeringGoroutines" when lingering
// is non-empty, "overlapLinesByFile" when overlap holds lines, and "windowCut"
// when windowCut is true).
func encodeRecordWithMeta(testID string, lines, blocks map[string][]int, partial []string, overlap map[string][]int, windowCut bool, lingering []string) ([]byte, error) {
	rec := map[string]interface{}{
		"id":                  testID,
		"executedLinesByFile": lines,
	}
	if len(blocks) > 0 {
		rec["executedBlocksByFile"] = blocks
	}
	if len(partial) > 0 {
		rec["contaminated"] = partial
	}
	if len(overlap) > 0 {
		rec["overlapLinesByFile"] = overlap
	}
	if windowCut {
		rec["windowCut"] = true
	}
	if len(lingering) > 0 {
		rec["lingeringGoroutines"] = lingering
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("encode the coverage: %w", err)
	}
	return data, nil
}

// The bounds of a record's delivery: an agent that stops reading must not
// hold every later command, which waits on controlMu behind it.
const (
	dataDialTimeout  = 5 * time.Second
	dataWriteTimeout = 30 * time.Second
)

// sendToSocket writes one record on a connection of its own to Keploy's data
// socket.
func sendToSocket(data []byte) error {
	conn, err := net.DialTimeout("unix", dataSocketPath, dataDialTimeout)
	if err != nil {
		return fmt.Errorf("could not reach Keploy's coverage data socket at %s: %w", dataSocketPath, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetWriteDeadline(time.Now().Add(dataWriteTimeout)); err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}
