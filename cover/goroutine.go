package cover

import (
	"bytes"
	"log"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// envGoroutineTracking disables goroutine-tail stack diffing when set to
	// "off" (or "false", "0", "no", case-insensitive).
	envGoroutineTracking = "KEPLOY_GO_GOROUTINE_TRACKING"
	// envEndWaitMS sets how long END polls for newly-spawned app goroutines
	// and busy pre-existing app worker goroutines to settle before declaring
	// windowCut=true (default 200ms, capped at 2000ms; 0 checks immediately).
	envEndWaitMS = "KEPLOY_GO_END_WAIT_MS"
	// envIgnoreGoroutines is an optional comma-separated list of function or
	// package substrings to exclude from goroutine-tail detection (for example
	// a known benign lazy-init background worker). Empty by default.
	envIgnoreGoroutines = "KEPLOY_GO_IGNORE_GOROUTINES"

	defaultEndWaitMS = 200
	maxEndWaitMS     = 2000

	endWaitPollMin = 5 * time.Millisecond
	endWaitPollMax = 25 * time.Millisecond

	stackInitBufSize     = 64 << 10 // 64 KiB initial stack buffer
	stackShrinkThreshold = 1 << 20  // shrink stackBuf when >=1 MiB and mostly unused
	stackMaxBufSize      = 16 << 20 // 16 MiB cap so pathological apps cannot OOM

	maxLingeringTails     = 64
	maxOverflowTailGids   = 512
	maxResumeWatches      = 512
	rebaselineStreakLimit = 2
)

// goroutineInfo is the parsed identity and stack summary of one live goroutine
// from runtime.Stack(buf, true).
type goroutineInfo struct {
	id            uint64
	baseStatus    string // wait/run state without duration suffix (e.g. "chan receive", "sleep")
	topLine       string // source file:line of frames[0] without "+0x..." offset
	frames        []string
	framePkgs     []string
	frameLines    []string // source file:line for each frame (without "+0x..." offset)
	createdByFn   string
	createdByPkg  string
	createdByGoid uint64 // parent goroutine ID from "created by X in goroutine N" (Go 1.21+)
}

// goroutineSnapshot holds all live goroutines parsed from one runtime.Stack
// capture, plus the caller's own goroutine ID and whether the capture hit
// stackMaxBufSize and may have truncated.
type goroutineSnapshot struct {
	selfID    uint64
	all       map[uint64]*goroutineInfo
	truncated bool
}

// goroutineSig is a compact, allocation-free signature of a goroutine's stack
// and wait status. A zero-value goroutineSig (active == false) represents a
// goroutine currently executing no app-relevant frames (e.g. an idle
// net/http.(*conn).serve blocked in readRequest).
type goroutineSig struct {
	active        bool
	status        string
	appFrameCount int // count of app-package frames only (ignores stdlib wrappers)
	hash          uint64
}

// startBaseline records a pre-existing goroutine's START baseline. Normally
// only idle is set (the quiescent idle signature to compare against at END).
// When START observes a goroutine at a quiescent signature startSig that
// differs from its established lastQuiescentSigs entry (idle), drifted is set
// so END fails closed both when the goroutine stays at startSig (caught
// mid-job at START, keeping idle as the return target) AND when it transitions
// from startSig back to idle (when idle itself was a first-START mid-job
// capture and startSig is the true idle, or when a gap job finished during the
// window).
type startBaseline struct {
	idle     goroutineSig
	startSig goroutineSig
	drifted  bool
}

// lingeringTarget tracks one lingering goroutine belonging to a cut test.
// When mustExit is true (goroutine created by app code), it remains active
// until its monotonic goroutine ID disappears from runtime.Stack. Otherwise
// (pre-existing worker/handler or stdlib/third-party goroutine running app
// code), it remains active until it exits, stops executing app frames
// (active == false), returns to idleSig, or moves onto a one-time resumeWatch.
type lingeringTarget struct {
	gid      uint64
	mustExit bool
	idleSig  goroutineSig
}

// lingeringTail tracks one test whose END was cut with lingering goroutines.
// preExistingChildren holds ONLY the goroutine IDs already alive at capture
// whose parent was one of this tail's targets, preventing false adoption of
// pre-existing children while keeping memory O(children) instead of O(all G).
type lingeringTail struct {
	testID              string
	targets             []lingeringTarget
	preExistingChildren map[uint64]struct{}
}

// truncatedCutWindow retains the START baseline (or last complete baseline on
// a truncated START) of a test whose snapshot was truncated so the next
// complete snapshot can resolve goroutines hidden past the 16 MiB stack cap.
type truncatedCutWindow struct {
	testID    string
	startGids map[uint64]struct{}
}

// quiescentStreak tracks consecutive START snapshots in which a lingering
// pre-existing goroutine remained at the same quiescent signature no deeper
// than its baseline app-frame depth.
type quiescentStreak struct {
	sig   goroutineSig
	count int
}

// resumeWatch holds a one-time watch on a pre-existing goroutine that settled
// into a different quiescent state no deeper than its baseline app-frame depth
// across consecutive STARTs (e.g. startup-backoff consumer settling into
// steady-state IO wait). While the goroutine remains parked at watchSig,
// subsequent tests run clean; the moment it leaves watchSig (including inside
// a gap), the watch fires, marks the window/gap contaminated, and re-attaches
// target to lingeringTails if the goroutine has not returned to its original
// idle state.
type resumeWatch struct {
	testID              string
	target              lingeringTarget
	watchSig            goroutineSig
	preExistingChildren map[uint64]struct{}
}

var (
	// Goroutine-tail tracking state, all guarded by controlMu.
	activeTracking       bool
	activeTruncatedStart bool
	startGids            map[uint64]struct{}
	startIdleStates      map[uint64]startBaseline
	lastCompleteGids     map[uint64]struct{}
	lastQuiescentSigs    = map[uint64]goroutineSig{}
	lingeringStreaks     = map[uint64]quiescentStreak{}
	resumeWatches        = map[uint64]resumeWatch{}
	activeContaminated   []string
	lingeringTails       []*lingeringTail
	prevWindowCutTestID  string
	pendingTailTestIDs   []string
	pendingTruncatedCuts []truncatedCutWindow
	saidLingering        = map[string]bool{}
	saidStackTruncated   bool

	// pendingEnd* carry finalized END-time tail metadata into reportCoverage
	// while controlMu is held, keeping publishCoverage's signature unchanged.
	pendingEndWindowCut    bool
	pendingEndLingering    []string
	pendingEndContaminated []string

	// stackBuf is lazily allocated on first armed START and reused under
	// controlMu so unarmed apps allocate nothing and armed runs avoid repeated
	// 64 KiB allocations. Shrunk back down after transient spikes.
	stackBuf []byte

	// snapshotGoroutines captures all live goroutines; a variable for tests.
	snapshotGoroutines = liveGoroutineSnapshot

	// runtimeGoVersion returns the Go toolchain version; a variable for tests.
	runtimeGoVersion = runtime.Version
)

// supportsGoroutineChildAdoption reports whether goVersion (as returned by
// runtime.Version()) is Go 1.21+ (which emits "created by ... in goroutine N"
// parent-goid lines required for child adoption) or a custom devel build
// without an embedded pre-1.21 Go version. Returns false (fail-closed) for
// Go < 1.21 or unrecognized non-Go/non-devel version strings.
func supportsGoroutineChildAdoption(goVersion string) bool {
	v := strings.TrimSpace(goVersion)
	if strings.HasPrefix(v, "devel") {
		fields := strings.Fields(v)
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "go") {
			v = fields[1]
		} else {
			return true
		}
	}
	if !strings.HasPrefix(v, "go") {
		return false
	}
	rest := v[2:]
	major, n1 := parseLeadingDigits(rest)
	if n1 == 0 {
		return false
	}
	if major > 1 {
		return true
	}
	if major < 1 {
		return false
	}
	rest = rest[n1:]
	if len(rest) == 0 || rest[0] != '.' {
		return false
	}
	minor, n2 := parseLeadingDigits(rest[1:])
	if n2 == 0 {
		return false
	}
	return minor >= 21
}

func parseLeadingDigits(s string) (int, int) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, 0
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, 0
	}
	return n, i
}

// logStackTruncatedOnce logs a one-time operator diagnostic when runtime.Stack
// reaches the 16 MiB cap and truncates, explaining the cap and recovery options.
// Called with controlMu held.
func logStackTruncatedOnce() {
	if saidStackTruncated {
		return
	}
	saidStackTruncated = true
	log.Printf("[keploy coverage] runtime.Stack hit the %d MiB cap and was truncated; tests will be marked windowCut/stack-truncated until stacks fit (reduce live goroutines or set %s=off; note %s filters parsed stacks only and does not shrink runtime.Stack)",
		stackMaxBufSize>>20, envGoroutineTracking, envIgnoreGoroutines)
}

// goroutineTrackingEnabled reports whether honest goroutine-tail tracking is
// enabled. Enabled by default; disabled by KEPLOY_GO_GOROUTINE_TRACKING=off.
func goroutineTrackingEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(envGoroutineTracking)))
	switch v {
	case "off", "false", "0", "no":
		return false
	default:
		return true
	}
}

// blockCoverageActive reports whether block-level coverage is active (true in
// production under -cover -covermode=atomic, and in unit tests that stub
// prepareMeta without populating meta; false for per-func granularity).
func blockCoverageActive() bool {
	return meta == nil || meta.granularity == ctrGranularityPerBlk
}

// goroutineTrackingActive reports whether goroutine-tail tracking should arm
// for the current process configuration and coverage mode.
func goroutineTrackingActive() bool {
	return goroutineTrackingEnabled() && blockCoverageActive()
}

// ignoreGoroutinePatterns returns any user-configured substring patterns from
// KEPLOY_GO_IGNORE_GOROUTINES (nil in the common unset case).
func ignoreGoroutinePatterns() []string {
	raw := strings.TrimSpace(os.Getenv(envIgnoreGoroutines))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// endWaitDuration returns the bounded END drain wait configured by
// KEPLOY_GO_END_WAIT_MS (default 200ms, capped at 2000ms; 0 disables waiting).
func endWaitDuration() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envEndWaitMS))
	if raw == "" {
		return time.Duration(defaultEndWaitMS) * time.Millisecond
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms < 0 {
		return time.Duration(defaultEndWaitMS) * time.Millisecond
	}
	if ms > maxEndWaitMS {
		ms = maxEndWaitMS
	}
	return time.Duration(ms) * time.Millisecond
}

// currentGoroutineID returns the Go runtime goroutine ID of the calling
// goroutine by parsing runtime.Stack(buf, false).
func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	id, _, _ := parseGoroutineHeader(string(buf[:n]))
	return id
}

// liveGoroutineSnapshot captures and parses all live goroutines using
// runtime.Stack(buf, true). Called with controlMu held.
func liveGoroutineSnapshot() goroutineSnapshot {
	selfID := currentGoroutineID()
	raw, truncated := captureStackAll()
	snap := parseGoroutineStacks(raw, selfID, truncated)
	maybeShrinkStackBuf(len(raw))
	return snap
}

// captureStackAll reads runtime.Stack(buf, true), growing stackBuf until the
// full stack dump fits or stackMaxBufSize is reached. Called with controlMu held.
func captureStackAll() ([]byte, bool) {
	if len(stackBuf) < stackInitBufSize {
		stackBuf = make([]byte, stackInitBufSize)
	}
	for {
		n := runtime.Stack(stackBuf, true)
		if n < len(stackBuf) {
			return stackBuf[:n], false
		}
		if len(stackBuf) >= stackMaxBufSize {
			return stackBuf[:n], true
		}
		nextSize := len(stackBuf) * 2
		if nextSize > stackMaxBufSize {
			nextSize = stackMaxBufSize
		}
		stackBuf = make([]byte, nextSize)
	}
}

// maybeShrinkStackBuf releases excess capacity if stackBuf grew past
// stackShrinkThreshold during a transient spike and the latest dump is much
// smaller. Called with controlMu held after parseGoroutineStacks copied needed
// strings out of stackBuf.
func maybeShrinkStackBuf(used int) {
	if len(stackBuf) < stackShrinkThreshold || used >= len(stackBuf)/4 {
		return
	}
	target := stackInitBufSize
	for target < used*2 && target < len(stackBuf)/2 {
		target *= 2
	}
	stackBuf = make([]byte, target)
}

// parseGoroutineStacks parses the output of runtime.Stack(buf, true) into a
// goroutineSnapshot keyed by unique monotonic goroutine ID.
func parseGoroutineStacks(raw []byte, selfID uint64, truncated bool) goroutineSnapshot {
	snap := goroutineSnapshot{
		selfID:    selfID,
		all:       make(map[uint64]*goroutineInfo),
		truncated: truncated,
	}
	var cur *goroutineInfo
	expectFrameLine := false
	for len(raw) > 0 {
		var lineBytes []byte
		if nl := bytes.IndexByte(raw, '\n'); nl >= 0 {
			lineBytes = raw[:nl]
			raw = raw[nl+1:]
		} else {
			lineBytes = raw
			raw = nil
		}
		if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\r' {
			lineBytes = lineBytes[:len(lineBytes)-1]
		}
		if len(lineBytes) == 0 {
			cur = nil
			expectFrameLine = false
			continue
		}
		if bytes.HasPrefix(lineBytes, []byte("goroutine ")) {
			expectFrameLine = false
			if id, status, ok := parseGoroutineHeader(string(lineBytes)); ok {
				cur = &goroutineInfo{id: id, baseStatus: status}
				snap.all[id] = cur
			} else {
				cur = nil
			}
			continue
		}
		if cur == nil {
			continue
		}
		if lineBytes[0] == '\t' {
			if expectFrameLine && len(cur.frameLines) > 0 {
				srcLine := parseSourceLine(lineBytes[1:])
				cur.frameLines[len(cur.frameLines)-1] = srcLine
				if len(cur.frameLines) == 1 {
					cur.topLine = srcLine
				}
				expectFrameLine = false
			}
			continue
		}
		expectFrameLine = false
		if bytes.HasPrefix(lineBytes, []byte("...")) {
			continue
		}
		if bytes.HasPrefix(lineBytes, []byte("[originating from goroutine")) {
			// Ignore tracebackancestors sections so ancestor frames do not
			// overwrite this goroutine's own frames or created-by line.
			cur = nil
			continue
		}
		line := string(lineBytes)
		if strings.HasPrefix(line, "created by ") {
			fn, pkg, parentID := parseCreatedBy(line)
			if fn != "" {
				cur.createdByFn = fn
				cur.createdByPkg = pkg
				cur.createdByGoid = parentID
			}
			continue
		}
		fn, pkg := parseFrameFunc(line)
		if fn != "" {
			cur.frames = append(cur.frames, fn)
			cur.framePkgs = append(cur.framePkgs, pkg)
			cur.frameLines = append(cur.frameLines, "")
			expectFrameLine = true
		}
	}
	return snap
}

// parseSourceLine extracts "<file>:<line>" from an indented runtime.Stack
// location line such as "/app/worker.go:95 +0x54".
func parseSourceLine(b []byte) string {
	b = bytes.TrimSpace(b)
	if sp := bytes.LastIndexByte(b, ' '); sp >= 0 && bytes.HasPrefix(b[sp+1:], []byte("+0x")) {
		b = b[:sp]
	}
	return string(b)
}

// parseGoroutineHeader parses the monotonic goroutine ID and base wait/run
// status (stripping any ", N minutes" duration suffix) from a runtime.Stack
// header line such as "goroutine 42 [chan receive, 2 minutes]:".
func parseGoroutineHeader(line string) (uint64, string, bool) {
	if !strings.HasPrefix(line, "goroutine ") {
		return 0, "", false
	}
	rest := line[len("goroutine "):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, "", false
	}
	id, err := strconv.ParseUint(rest[:end], 10, 64)
	if err != nil {
		return 0, "", false
	}
	status := ""
	if open := strings.IndexByte(rest[end:], '['); open >= 0 {
		after := rest[end+open+1:]
		if closeIdx := strings.IndexByte(after, ']'); closeIdx >= 0 {
			status = after[:closeIdx]
		} else {
			status = after
		}
		if comma := strings.IndexByte(status, ','); comma >= 0 {
			status = status[:comma]
		}
		status = strings.TrimSpace(status)
	}
	return id, status, true
}

// parseFrameFunc extracts the function name and package path from one
// runtime.Stack frame line such as "example.com/app.(*Srv).Handle(0xc000...)".
func parseFrameFunc(line string) (fn, pkg string) {
	end := len(line)
	if end > 0 && line[end-1] == ')' {
		depth := 0
		for i := end - 1; i >= 0; i-- {
			switch line[i] {
			case ')':
				depth++
			case '(':
				depth--
				if depth == 0 {
					end = i
					i = 0
				}
			}
		}
	}
	fn = strings.TrimSpace(line[:end])
	if fn == "" {
		return "", ""
	}
	return fn, pkgOfFunc(fn)
}

// parseCreatedBy extracts the creator function name, package path, and
// optional parent goroutine ID (Go 1.21+) from a
// "created by <fn>[ in goroutine <id>]" line.
func parseCreatedBy(line string) (fn, pkg string, parentID uint64) {
	rest := strings.TrimPrefix(line, "created by ")
	if idx := strings.Index(rest, " in goroutine "); idx >= 0 {
		idStr := strings.TrimSpace(rest[idx+len(" in goroutine "):])
		end := 0
		for end < len(idStr) && idStr[end] >= '0' && idStr[end] <= '9' {
			end++
		}
		if end > 0 {
			if parsed, err := strconv.ParseUint(idStr[:end], 10, 64); err == nil {
				parentID = parsed
			}
		}
		rest = rest[:idx]
	}
	fn = strings.TrimSpace(rest)
	if fn == "" {
		return "", "", 0
	}
	return fn, pkgOfFunc(fn), parentID
}

// pkgOfFunc extracts the Go package import path from a runtime.Stack function
// symbol name, handling generic type parameters, pointer receivers, closures,
// and percent-escaped dots in the final path segment.
func pkgOfFunc(fn string) string {
	head := fn
	if idx := strings.IndexByte(head, '['); idx >= 0 {
		head = head[:idx]
	}
	slash := strings.LastIndexByte(head, '/')
	rest := head[slash+1:]
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 {
		return ""
	}
	pkg := head[:slash+1+dot]
	if strings.Contains(pkg, "%") {
		pkg = unescapePkgPath(pkg)
	}
	return pkg
}

// unescapePkgPath decodes %XX escapes Go's compiler emits in symbol package
// paths (for example "%2e" for '.' in a package path's final segment).
func unescapePkgPath(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isSDKFunc reports whether (fn, pkg) belongs to this coverage hook's own
// runtime code (excluding *_test.go test functions when running go test on
// package cover itself).
func isSDKFunc(fn, pkg string) bool {
	if ownPkgPath == "" || pkg != ownPkgPath {
		return false
	}
	sym := strings.TrimPrefix(fn, ownPkgPath+".")
	if strings.HasPrefix(sym, "Test") || strings.HasPrefix(sym, "Benchmark") ||
		strings.HasPrefix(sym, "Fuzz") || strings.HasPrefix(sym, "Example") {
		return false
	}
	return true
}

// isSDKGoroutine reports whether g is one of go-sdk/cover's own goroutines
// (the calling END/START handler, serveControl, or runTableStream).
func isSDKGoroutine(g *goroutineInfo, selfID uint64) bool {
	if g == nil {
		return true
	}
	if selfID != 0 && g.id == selfID {
		return true
	}
	if isSDKFunc(g.createdByFn, g.createdByPkg) {
		return true
	}
	for i, pkg := range g.framePkgs {
		if isSDKFunc(g.frames[i], pkg) {
			return true
		}
	}
	return false
}

// coveredPackageSet returns the set of first-party / covered package paths
// from loaded coverage metadata, or nil when coverage metadata is unavailable.
// Called with controlMu held.
func coveredPackageSet() map[string]bool {
	if meta == nil {
		return nil
	}
	pkgs := make(map[string]bool, len(meta.pkgs)+1)
	pkgs["main"] = true
	for i := range meta.pkgs {
		if meta.pkgs[i].skip {
			continue
		}
		if meta.pkgs[i].path != "" {
			pkgs[meta.pkgs[i].path] = true
		}
	}
	return pkgs
}

// isStdlibOrInfraPkg reports whether pkg is a Go standard-library package or a
// well-known idle transport/housekeeping package (used as the fallback filter
// when coverage metadata is not loaded).
func isStdlibOrInfraPkg(pkg string) bool {
	if pkg == "" || pkg == ownPkgPath {
		return true
	}
	if pkg == "main" {
		return false
	}
	first := pkg
	if slash := strings.IndexByte(pkg, '/'); slash >= 0 {
		first = pkg[:slash]
	}
	// Every Go standard library package has no '.' in its first path segment.
	if !strings.Contains(first, ".") {
		return true
	}
	if pkg == "golang.org/x/net/http2" || strings.HasPrefix(pkg, "golang.org/x/net/http2/") ||
		pkg == "google.golang.org/grpc/internal/transport" || strings.HasPrefix(pkg, "google.golang.org/grpc/internal/transport/") {
		return true
	}
	return false
}

// isAppPkg reports whether pkg belongs to the instrumented application.
func isAppPkg(pkg string, covered map[string]bool) bool {
	if pkg == "" || pkg == ownPkgPath {
		return false
	}
	if pkg == "main" {
		return true
	}
	if covered != nil {
		if covered[pkg] {
			return true
		}
		for m := range firstParty {
			if pkg == m || strings.HasPrefix(pkg, m+"/") {
				return true
			}
		}
		return false
	}
	return !isStdlibOrInfraPkg(pkg)
}

// matchesIgnorePatterns reports whether any frame or creator of g matches a
// user-supplied KEPLOY_GO_IGNORE_GOROUTINES substring.
func matchesIgnorePatterns(g *goroutineInfo, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, p := range patterns {
		if g.createdByFn != "" && strings.Contains(g.createdByFn, p) {
			return true
		}
		for _, fn := range g.frames {
			if strings.Contains(fn, p) {
				return true
			}
		}
	}
	return false
}

// isIdleHTTPReadLoopFrame reports whether fn is a stdlib/x/net HTTP idle
// keep-alive socket read frame (never a request handler dispatch frame).
func isIdleHTTPReadLoopFrame(fn string) bool {
	switch fn {
	case "net/http.(*conn).readRequest",
		"net/http.(*connReader).backgroundRead",
		"net/http.(*persistConn).readLoop",
		"net/http.(*http2ClientConn).readLoop",
		"net/http.(*http2serverConn).readFrames",
		"golang.org/x/net/http2.(*ClientConn).readLoop",
		"golang.org/x/net/http2.(*serverConn).readFrames":
		return true
	default:
		return false
	}
}

// isIdleWrappedHTTPKeepAlive reports whether g is a stdlib/x/net HTTP
// keep-alive goroutine parked in netpoll [IO wait] whose ONLY app frames are
// callees of an idle HTTP read-loop frame (for example an app net.Conn.Read
// wrapper called by net/http.(*conn).serve -> bufio.(*Reader).Peek,
// net/http.(*conn).readRequest, or net/http.(*persistConn).readLoop). Any
// active request handler (ServeHTTP), outbound RoundTrip, or non-[IO wait]
// wait returns false so real handlers and slow reads remain strictly tracked.
func isIdleWrappedHTTPKeepAlive(g *goroutineInfo, covered map[string]bool) bool {
	if g.baseStatus != "IO wait" || len(g.frames) == 0 || g.frames[0] != "internal/poll.runtime_pollWait" {
		return false
	}
	if g.createdByPkg != "net/http" && g.createdByPkg != "golang.org/x/net/http2" {
		return false
	}
	idleIdx := -1
	for i, fn := range g.frames {
		if isIdleHTTPReadLoopFrame(fn) {
			idleIdx = i
			break
		}
		// Between keep-alive requests, net/http.(*conn).serve calls
		// c.bufr.Peek(4) directly before entering readRequest.
		if fn == "net/http.(*conn).serve" && i > 0 && g.frames[i-1] == "bufio.(*Reader).Peek" {
			idleIdx = i
			break
		}
	}
	if idleIdx < 0 {
		return false
	}
	for i := idleIdx + 1; i < len(g.framePkgs); i++ {
		if isAppPkg(g.framePkgs[i], covered) {
			return false
		}
	}
	for i := 0; i < idleIdx; i++ {
		if strings.Contains(g.frames[i], "ServeHTTP") || strings.Contains(g.frames[i], "RoundTrip") {
			return false
		}
	}
	return true
}

// isAppRelevantGoroutineWithIgnore reports whether g was spawned by or is
// executing application code (excluding go-sdk's own goroutines, explicitly
// ignored goroutines, and idle stdlib/runtime keep-alive/housekeeping loops,
// including idle HTTP keep-alive reads through a wrapped app net.Conn).
func isAppRelevantGoroutineWithIgnore(g *goroutineInfo, selfID uint64, covered map[string]bool, ignore []string) bool {
	if isSDKGoroutine(g, selfID) {
		return false
	}
	if matchesIgnorePatterns(g, ignore) {
		return false
	}
	if isAppPkg(g.createdByPkg, covered) {
		return true
	}
	if isIdleWrappedHTTPKeepAlive(g, covered) {
		return false
	}
	for _, pkg := range g.framePkgs {
		if isAppPkg(pkg, covered) {
			return true
		}
	}
	return false
}

// isQuiescentStatus reports whether a pre-existing goroutine's baseStatus is a
// parked waiting state suitable for worker-pool idle baselining at START
// (channel/select, IO wait, sleep, blocking cgo/file syscall, semacquire,
// Go 1.20+ sync.* waits, Go 1.23+ iter.Pull coroutine wait, and GC cycle waits),
// while excluding actively executing states like "running", "runnable",
// "preempted", "copystack", and "GC assist wait".
func isQuiescentStatus(baseStatus string) bool {
	if strings.HasPrefix(baseStatus, "sync.") {
		return true
	}
	switch baseStatus {
	case "chan receive", "chan receive (nil chan)",
		"chan send", "chan send (nil chan)",
		"select", "select (no cases)",
		"semacquire", "IO wait", "sleep", "syscall",
		"coroutine", "wait for GC cycle", "GC weak to strong wait":
		return true
	default:
		return false
	}
}

// fnv64aStr updates an inline 64-bit FNV-1a hash with s and a separator byte,
// allocating zero heap memory.
func fnv64aStr(h uint64, s string) uint64 {
	const prime64 = 1099511628211
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	h ^= 0xff
	h *= prime64
	return h
}

// goroutineStateSig builds a compact, allocation-free signature for g (or a
// zero-value goroutineSig when g is not currently app-relevant, such as an
// idle net/http.(*conn).serve). appFrameCount counts ONLY app-package frames
// so stdlib wrapper depth cannot mask a deeper mid-job app call.
func goroutineStateSig(g *goroutineInfo, selfID uint64, covered map[string]bool, ignore []string) goroutineSig {
	if !isAppRelevantGoroutineWithIgnore(g, selfID, covered, ignore) {
		return goroutineSig{}
	}
	const offset64 = 14695981039346656037
	h := fnv64aStr(offset64, g.baseStatus)
	appFrames := 0
	for i, fn := range g.frames {
		h = fnv64aStr(h, fn)
		if isAppPkg(g.framePkgs[i], covered) {
			appFrames++
			if i < len(g.frameLines) && g.frameLines[i] != "" {
				h = fnv64aStr(h, g.frameLines[i])
			}
		}
	}
	h = fnv64aStr(h, g.topLine)
	return goroutineSig{
		active:        true,
		status:        g.baseStatus,
		appFrameCount: appFrames,
		hash:          h,
	}
}

// checkResumeWatches checks every active one-time resumeWatch against snap.
// A watch fires if the watched goroutine exited, left its watchSig, OR spawned
// a new app-relevant child not in its original preExistingChildren. When it
// fires, its original testID is returned so the caller marks the affected
// gap/window contaminated, and if the goroutine is still active (not back at
// its original idleSig) or spawned a new child, its original lingeringTarget
// is re-attached with its preserved preExistingChildren so post-wake children
// are adopted normally by adoptLingeringChildren.
//
// checkResumeWatches checks every active one-time resumeWatch against snap.
// Any non-mustExit watch that fires also returns its original idleSig in
// firedOrigIdle so END polling can recognize when the goroutine returns to its
// original idle state during the same test window, and returns reattached with
// every gid freshly re-attached to lingeringTails so START pruning does not
// count the re-attach START itself as a re-baseline streak step. When
// activeIdle is non-nil (an open test window's baseline at START->START or END)
// and the goroutine has already returned to its original idle state,
// activeIdle[gid] is updated alongside lastQuiescentSigs[gid] so neither END
// nor an unclosed START->START check treats the return to original idle as a
// new tail. Called with controlMu held.
func checkResumeWatches(snap goroutineSnapshot, activeIdle map[uint64]startBaseline, covered map[string]bool, ignore []string) ([]string, map[uint64]goroutineSig, map[uint64]struct{}) {
	if len(resumeWatches) == 0 {
		return nil, nil, nil
	}
	// Pre-index whether any watched gid currently has a new app-relevant child
	// (excluding pre-existing children and goroutines already tracked in lingeringTails).
	alreadyLingering := lingeringGidSet()
	hasNewChild := map[uint64]bool{}
	for id, g := range snap.all {
		if g.createdByGoid == 0 {
			continue
		}
		rw, watched := resumeWatches[g.createdByGoid]
		if !watched {
			continue
		}
		if _, pre := rw.preExistingChildren[id]; pre {
			continue
		}
		if _, tracked := alreadyLingering[id]; tracked {
			continue
		}
		if isAppRelevantGoroutineWithIgnore(g, snap.selfID, covered, ignore) {
			hasNewChild[g.createdByGoid] = true
		}
	}

	var triggered []string
	var firedOrigIdle map[uint64]goroutineSig
	var reattached map[uint64]struct{}
	for gid, rw := range resumeWatches {
		g, alive := snap.all[gid]
		if !alive {
			triggered = append(triggered, rw.testID)
			delete(resumeWatches, gid)
			if hasNewChild[gid] {
				addLingeringTail(rw.testID, []lingeringTarget{rw.target}, rw.preExistingChildren)
				if reattached == nil {
					reattached = make(map[uint64]struct{})
				}
				reattached[gid] = struct{}{}
			}
			continue
		}
		if matchesIgnorePatterns(g, ignore) {
			delete(resumeWatches, gid)
			continue
		}
		curSig := goroutineStateSig(g, snap.selfID, covered, ignore)
		spawnedChild := hasNewChild[gid]
		if curSig != rw.watchSig || spawnedChild {
			triggered = append(triggered, rw.testID)
			delete(resumeWatches, gid)
			if !rw.target.mustExit {
				if firedOrigIdle == nil {
					firedOrigIdle = make(map[uint64]goroutineSig)
				}
				firedOrigIdle[gid] = rw.target.idleSig
			}
			if !curSig.active || curSig == rw.target.idleSig {
				// Restore lastQuiescentSigs (and the open window's baseline, if
				// any) when the goroutine returned directly to its original
				// idle state so neither snapshotStartBaseline nor an unclosed
				// START->START or END check treats the return to idle as a new
				// cut/tail.
				lastQuiescentSigs[gid] = curSig
				if activeIdle != nil {
					activeIdle[gid] = startBaseline{idle: curSig}
				}
			}
			// Re-attach using the watch's preserved preExistingChildren (never
			// re-capturing snap's live children, which include post-wake work!).
			if (curSig.active && curSig != rw.target.idleSig) || spawnedChild {
				addLingeringTail(rw.testID, []lingeringTarget{rw.target}, rw.preExistingChildren)
				if reattached == nil {
					reattached = make(map[uint64]struct{})
				}
				reattached[gid] = struct{}{}
			}
		}
	}
	if len(triggered) > 1 {
		sort.Strings(triggered)
	}
	return triggered, firedOrigIdle, reattached
}

// snapshotStartBaseline captures the set of live non-SDK goroutine IDs and the
// baseline idle state signatures of pre-existing goroutines at START (skipping
// any goroutine already tracked as busy in lingeringTails). Pre-existing app
// goroutines that happen to be unparked (e.g. runnable) at START fall back to
// their last known quiescent signature from an earlier complete START, or if
// never seen quiescent, an unparked-at-START baseline (status == "") that fails
// closed at END whenever the goroutine still has app frames. It also updates
// lastCompleteGids/lastQuiescentSigs and prunes dead gids.
func snapshotStartBaseline(snap goroutineSnapshot, covered map[string]bool, ignore []string) (map[uint64]struct{}, map[uint64]startBaseline) {
	alreadyLingering := lingeringGidSet()
	gids := make(map[uint64]struct{}, len(snap.all))
	idle := make(map[uint64]startBaseline, len(snap.all))
	for id, g := range snap.all {
		if isSDKGoroutine(g, snap.selfID) {
			continue
		}
		gids[id] = struct{}{}
		if _, busy := alreadyLingering[id]; busy {
			continue
		}
		sig := goroutineStateSig(g, snap.selfID, covered, ignore)
		if !sig.active {
			// Non-app idle goroutine (e.g. net/http.(*conn).serve waiting in
			// readRequest, including wrapped net.Conn idle reads): baseline as
			// zero sig so if it is still inside an app handler at END (e.g.
			// after Flusher.Flush), END detects it.
			idle[id] = startBaseline{idle: goroutineSig{}}
			lastQuiescentSigs[id] = goroutineSig{}
		} else if isQuiescentStatus(g.baseStatus) {
			// Pre-existing app worker goroutine waiting quiescently (e.g.
			// blocked on <-jobsCh, select, syscall, sync.Mutex.Lock). Compare
			// against its established quiescent idle signature in
			// lastQuiescentSigs: whenever a different quiescent idle signature
			// is already on record (whether deeper, equal, or shallower, or
			// non-app idle), fail closed by preserving prevQ alongside sig
			// (drifted=true) instead of adopting sig as idle. A goroutine that
			// genuinely moved to a new steady state settles through the
			// existing streak/resumeWatch path.
			if prevQ, hadPrev := lastQuiescentSigs[id]; hadPrev && prevQ != sig {
				idle[id] = startBaseline{idle: prevQ, startSig: sig, drifted: true}
			} else {
				idle[id] = startBaseline{idle: sig}
				lastQuiescentSigs[id] = sig
			}
		} else if prevQ, ok := lastQuiescentSigs[id]; ok {
			// Pre-existing goroutine was unparked (e.g. runnable) at START,
			// but has a prior quiescent baseline from an earlier complete START.
			idle[id] = startBaseline{idle: prevQ}
		} else {
			// Pre-existing goroutine was unparked at START with no prior
			// quiescent baseline: record its app-frame depth with status == ""
			// so END fails closed whenever the goroutine still has app frames
			// (e.g. still runnable or parked mid-job at the same or deeper depth).
			idle[id] = startBaseline{
				idle: goroutineSig{
					active:        true,
					status:        "",
					appFrameCount: sig.appFrameCount,
					hash:          0,
				},
			}
		}
	}
	for id := range lingeringStreaks {
		if _, alive := snap.all[id]; !alive {
			delete(lingeringStreaks, id)
		}
	}
	for id := range lastQuiescentSigs {
		if _, alive := snap.all[id]; !alive {
			delete(lastQuiescentSigs, id)
		}
	}
	lastCompleteGids = gids
	return gids, idle
}

// lingeringGidSet returns all goroutine IDs currently tracked across
// lingeringTails. Called with controlMu held.
func lingeringGidSet() map[uint64]struct{} {
	if len(lingeringTails) == 0 {
		return nil
	}
	set := make(map[uint64]struct{})
	for _, lt := range lingeringTails {
		for _, t := range lt.targets {
			set[t.gid] = struct{}{}
		}
	}
	return set
}

// activeTestGoroutine is a goroutine still running work attributable to the
// current test at END: either newly spawned since START, or a pre-existing
// goroutine that transitioned out of its quiescent START baseline into active
// app work.
type activeTestGoroutine struct {
	info     *goroutineInfo
	mustExit bool
	idleSig  goroutineSig
}

// findActiveTestGoroutines returns all goroutines currently executing work
// started during the current test (both newly spawned app-relevant goroutines
// and pre-existing goroutines that moved off their quiescent START baseline
// into app code), sorted by ID.
func findActiveTestGoroutines(snap goroutineSnapshot, startSet map[uint64]struct{}, startIdle map[uint64]startBaseline, covered map[string]bool, ignore []string) []activeTestGoroutine {
	return findActiveTestGoroutinesWithLingering(snap, startSet, startIdle, lingeringGidSet(), covered, ignore)
}

// findActiveTestGoroutinesWithLingering is findActiveTestGoroutines using a
// pre-captured alreadyLingering set so mid-END watch re-attachments do not
// hide active work from subsequent END_WAIT polls.
func findActiveTestGoroutinesWithLingering(snap goroutineSnapshot, startSet map[uint64]struct{}, startIdle map[uint64]startBaseline, alreadyLingering map[uint64]struct{}, covered map[string]bool, ignore []string) []activeTestGoroutine {
	var out []activeTestGoroutine
	for id, g := range snap.all {
		if isSDKGoroutine(g, snap.selfID) {
			continue
		}
		if _, existed := startSet[id]; !existed {
			if !isAppRelevantGoroutineWithIgnore(g, snap.selfID, covered, ignore) {
				continue
			}
			// Only require full goroutine exit when the goroutine was created
			// by app code. A new stdlib/third-party goroutine currently running
			// an app handler/callback (e.g. a new keep-alive net/http.(*conn).serve)
			// clears as soon as it returns to an idle state (idleSig.active==false).
			out = append(out, activeTestGoroutine{
				info:     g,
				mustExit: isAppPkg(g.createdByPkg, covered),
				idleSig:  goroutineSig{},
			})
			continue
		}
		if _, busy := alreadyLingering[id]; busy {
			continue
		}
		base, tracked := startIdle[id]
		if !tracked {
			continue
		}
		curSig := goroutineStateSig(g, snap.selfID, covered, ignore)
		if !curSig.active {
			continue
		}
		if base.drifted {
			// START observed a quiescent state (base.startSig) differing from
			// the established idle (base.idle). Fail closed for both outcomes:
			//   - curSig == base.idle: goroutine moved from base.startSig to
			//     base.idle during the window; return target is base.startSig
			//     (or settles via resumeWatch if base.idle is the true idle).
			//   - curSig != base.idle: goroutine is still off its established
			//     idle (e.g. caught mid-job at START); return target is base.idle.
			targetIdle := base.idle
			if curSig == base.idle {
				targetIdle = base.startSig
			}
			out = append(out, activeTestGoroutine{
				info:     g,
				mustExit: false,
				idleSig:  targetIdle,
			})
			continue
		}
		if base.idle.status == "" || curSig != base.idle {
			out = append(out, activeTestGoroutine{
				info:     g,
				mustExit: false,
				idleSig:  base.idle,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].info.id < out[j].info.id })
	return out
}

// describeAppGoroutine returns a short descriptive function name for a
// lingering app goroutine: the topmost app frame on its stack, falling back to
// its created-by function or topmost frame.
func describeAppGoroutine(g *goroutineInfo, covered map[string]bool) string {
	for i, pkg := range g.framePkgs {
		if isAppPkg(pkg, covered) {
			return g.frames[i]
		}
	}
	if g.createdByFn != "" {
		return g.createdByFn
	}
	if len(g.frames) > 0 {
		return g.frames[0]
	}
	return "goroutine"
}

// summarizeActiveNames returns sorted, unique descriptive function names for
// active lingering goroutines, capped at maxPartialNames plus "+N".
func summarizeActiveNames(gs []activeTestGoroutine, covered map[string]bool) []string {
	if len(gs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(gs))
	names := make([]string, 0, len(gs))
	for _, ag := range gs {
		name := describeAppGoroutine(ag.info, covered)
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > maxPartialNames {
		rest := len(names) - maxPartialNames
		names = append(names[:maxPartialNames], "+"+strconv.Itoa(rest))
	}
	return names
}

// logLingeringOnce logs a one-time diagnostic per lingering goroutine function
// name so operators can spot permanent lazy-init goroutines immediately.
// Called with controlMu held.
func logLingeringOnce(testID string, names []string) {
	var fresh []string
	for _, n := range names {
		if strings.HasPrefix(n, "+") {
			continue
		}
		if !saidLingering[n] {
			saidLingering[n] = true
			fresh = append(fresh, n)
		}
	}
	if len(fresh) == 0 {
		return
	}
	log.Printf("[keploy coverage] test %s has lingering app goroutine(s) %v (tune %s, %s, or %s=off if intentional)",
		testID, fresh, envEndWaitMS, envIgnoreGoroutines, envGoroutineTracking)
}

// capturePreExistingChildren returns the non-SDK goroutine IDs already alive
// in snap whose parent (createdByGoid) is one of targets — INCLUDING co-targets
// that were already alive at START (startSet). Including co-targets ensures
// that if a pre-existing child returns to idle before its parent (or moves onto
// a watch with its parent), it is never falsely re-adopted as a new mustExit
// child (ownerByGid already prevents double adoption while both are targets).
func capturePreExistingChildren(snap goroutineSnapshot, targets []lingeringTarget, startSet map[uint64]struct{}) map[uint64]struct{} {
	if len(snap.all) == 0 || len(targets) == 0 {
		return nil
	}
	targetSet := make(map[uint64]struct{}, len(targets))
	for _, t := range targets {
		targetSet[t.gid] = struct{}{}
	}
	var children map[uint64]struct{}
	for id, g := range snap.all {
		if g.createdByGoid == 0 || isSDKGoroutine(g, snap.selfID) {
			continue
		}
		if startSet != nil {
			if _, existedAtStart := startSet[id]; !existedAtStart {
				continue
			}
		}
		if _, isChildOfTarget := targetSet[g.createdByGoid]; isChildOfTarget {
			if children == nil {
				children = make(map[uint64]struct{})
			}
			children[id] = struct{}{}
		}
	}
	return children
}

func mergePreExistingChildren(dst *lingeringTail, src map[uint64]struct{}, maxCap int) {
	if len(src) == 0 {
		return
	}
	if dst.preExistingChildren == nil {
		dst.preExistingChildren = make(map[uint64]struct{}, len(src))
	}
	for id := range src {
		if _, ok := dst.preExistingChildren[id]; ok {
			continue
		}
		if maxCap > 0 && len(dst.preExistingChildren) >= maxCap {
			return
		}
		dst.preExistingChildren[id] = struct{}{}
	}
}

// addLingeringTail records a test whose window was cut with lingering
// goroutine targets, deduplicating every target gid across all existing
// lingeringTails so no goroutine is ever tracked in two tails at once. When
// maxLingeringTails is reached, older entries are merged into a bounded
// catch-all "overflow" entry (retaining up to maxOverflowTailGids unique
// targets and pre-existing children so memory stays bounded under sustained
// leaks). Called with controlMu held.
func addLingeringTail(testID string, targets []lingeringTarget, preExisting map[uint64]struct{}) {
	if len(targets) == 0 {
		return
	}
	alreadyTracked := lingeringGidSet()
	if alreadyTracked == nil {
		alreadyTracked = make(map[uint64]struct{}, len(targets))
	}
	fresh := make([]lingeringTarget, 0, len(targets))
	for _, t := range targets {
		if _, dup := alreadyTracked[t.gid]; dup {
			continue
		}
		alreadyTracked[t.gid] = struct{}{}
		fresh = append(fresh, t)
	}
	if len(fresh) == 0 {
		return
	}
	for _, lt := range lingeringTails {
		if lt.testID == testID {
			maxCap := 0
			if lt.testID == "overflow" {
				maxCap = maxOverflowTailGids
			}
			mergeTargets(lt, fresh, maxCap)
			mergePreExistingChildren(lt, preExisting, maxCap)
			return
		}
	}
	if len(lingeringTails) >= maxLingeringTails {
		var overflow *lingeringTail
		for _, lt := range lingeringTails {
			if lt.testID == "overflow" {
				overflow = lt
				break
			}
		}
		if overflow == nil {
			overflow = &lingeringTail{testID: "overflow"}
			mergeTargets(overflow, lingeringTails[0].targets, maxOverflowTailGids)
			mergePreExistingChildren(overflow, lingeringTails[0].preExistingChildren, maxOverflowTailGids)
			lingeringTails[0] = overflow
		} else if len(lingeringTails) > 1 {
			dropIdx := 0
			if lingeringTails[0] == overflow {
				dropIdx = 1
			}
			mergeTargets(overflow, lingeringTails[dropIdx].targets, maxOverflowTailGids)
			mergePreExistingChildren(overflow, lingeringTails[dropIdx].preExistingChildren, maxOverflowTailGids)
			lingeringTails = append(lingeringTails[:dropIdx], lingeringTails[dropIdx+1:]...)
		}
		if len(lingeringTails) >= maxLingeringTails && len(lingeringTails) > 1 {
			mergeTargets(overflow, lingeringTails[1].targets, maxOverflowTailGids)
			mergePreExistingChildren(overflow, lingeringTails[1].preExistingChildren, maxOverflowTailGids)
			lingeringTails = append(lingeringTails[:1], lingeringTails[2:]...)
		}
	}
	lt := &lingeringTail{
		testID:  testID,
		targets: fresh,
	}
	mergePreExistingChildren(lt, preExisting, 0)
	lingeringTails = append(lingeringTails, lt)
}

func mergeTargets(dst *lingeringTail, add []lingeringTarget, maxCap int) {
	have := make(map[uint64]struct{}, len(dst.targets))
	for _, t := range dst.targets {
		have[t.gid] = struct{}{}
	}
	for _, t := range add {
		if _, ok := have[t.gid]; ok {
			continue
		}
		if maxCap > 0 && len(dst.targets) >= maxCap {
			return
		}
		dst.targets = append(dst.targets, t)
		have[t.gid] = struct{}{}
	}
}

// buildTruncatedStartUnion returns the union of lastCompleteGids and all
// visible non-SDK goroutine IDs from a truncated START snapshot so a pending
// truncated cut window can resolve hidden goroutines at the next complete dump.
func buildTruncatedStartUnion(snap goroutineSnapshot) map[uint64]struct{} {
	union := make(map[uint64]struct{}, len(lastCompleteGids)+len(snap.all))
	for id := range lastCompleteGids {
		union[id] = struct{}{}
	}
	for id, g := range snap.all {
		if isSDKGoroutine(g, snap.selfID) {
			continue
		}
		union[id] = struct{}{}
	}
	return union
}

// resolveTruncatedCutWindows resolves any prior truncated END/START snapshots
// once a complete (non-truncated) snapshot is observed, adopting any still-alive
// app goroutines that were not present at the truncated test's baseline.
// Called with controlMu held.
func resolveTruncatedCutWindows(snap goroutineSnapshot, covered map[string]bool, ignore []string) {
	if snap.truncated || len(pendingTruncatedCuts) == 0 {
		return
	}
	for _, tc := range pendingTruncatedCuts {
		var targets []lingeringTarget
		var names []string
		for id, g := range snap.all {
			if _, existed := tc.startGids[id]; existed {
				continue
			}
			if !isAppRelevantGoroutineWithIgnore(g, snap.selfID, covered, ignore) {
				continue
			}
			targets = append(targets, lingeringTarget{
				gid:      id,
				mustExit: isAppPkg(g.createdByPkg, covered),
				idleSig:  goroutineSig{},
			})
			names = append(names, describeAppGoroutine(g, covered))
		}
		if len(targets) > 0 {
			addLingeringTail(tc.testID, targets, capturePreExistingChildren(snap, targets, tc.startGids))
			sort.Strings(names)
			logLingeringOnce(tc.testID, names)
		}
	}
	pendingTruncatedCuts = nil
}

// adoptLingeringChildren inspects every live goroutine in snap whose parent
// goroutine ID (from "created by X in goroutine N", Go 1.21+) belongs to a
// tracked lingeringTail and adopts the child into that same tail BEFORE
// dead parents are pruned, subject to three strict guards:
//   - the child must NOT be in parentTail.preExistingChildren,
//   - the child must be app-relevant and not matched by KEPLOY_GO_IGNORE_GOROUTINES,
//   - mustExit is set only when the child was created by app code (otherwise it
//     clears as soon as it stops executing app frames).
//
// Runs to a fixed point for multi-hop child chains (G1 -> G2 -> G3) spawned
// between snapshots, and logs any newly adopted function names once.
// Called with controlMu held.
func adoptLingeringChildren(snap goroutineSnapshot, covered map[string]bool, ignore []string) map[uint64]struct{} {
	if len(lingeringTails) == 0 {
		return nil
	}
	var newlyAdopted map[uint64]struct{}
	ownerByGid := make(map[uint64]*lingeringTail)
	for _, lt := range lingeringTails {
		for _, t := range lt.targets {
			ownerByGid[t.gid] = lt
		}
	}
	for {
		adopted := false
		for id, g := range snap.all {
			if g.createdByGoid == 0 || isSDKGoroutine(g, snap.selfID) {
				continue
			}
			if _, already := ownerByGid[id]; already {
				continue
			}
			parentTail := ownerByGid[g.createdByGoid]
			if parentTail == nil {
				continue
			}
			if _, preExisted := parentTail.preExistingChildren[id]; preExisted {
				continue
			}
			if !isAppRelevantGoroutineWithIgnore(g, snap.selfID, covered, ignore) {
				continue
			}
			parentTail.targets = append(parentTail.targets, lingeringTarget{
				gid:      id,
				mustExit: isAppPkg(g.createdByPkg, covered),
				idleSig:  goroutineSig{},
			})
			ownerByGid[id] = parentTail
			if newlyAdopted == nil {
				newlyAdopted = make(map[uint64]struct{})
			}
			newlyAdopted[id] = struct{}{}
			logLingeringOnce(parentTail.testID, []string{describeAppGoroutine(g, covered)})
			adopted = true
		}
		if !adopted {
			break
		}
	}
	return newlyAdopted
}

// isHTTPHandlerGoroutine reports whether g is a stdlib/x/net HTTP server or
// client goroutine (or has an active ServeHTTP / RoundTrip frame). Stuck HTTP
// handlers must never be re-baselined onto a resumeWatch as if they were
// permanent background loops.
func isHTTPHandlerGoroutine(g *goroutineInfo) bool {
	if g.createdByPkg == "net/http" || g.createdByPkg == "golang.org/x/net/http2" {
		return true
	}
	for _, fn := range g.frames {
		if strings.Contains(fn, "ServeHTTP") || strings.Contains(fn, "RoundTrip") {
			return true
		}
	}
	return false
}

// canRebaselineTarget reports whether t is eligible to settle onto a one-time
// resumeWatch when stably parked at curSig:
//   - HTTP handler goroutines (net/http, x/net/http2, ServeHTTP, RoundTrip)
//     are NEVER eligible, even if first observed mid-handler.
//   - Any other active goroutine — pre-existing worker/consumer moving to a
//     new steady state (including returning to a deeper real idle after a
//     shallower first-START mid-job capture), lazily-started app goroutine
//     (t.mustExit, e.g. sync.Once starting a ticker, channel, or time.Sleep
//     janitor), or lazily-started non-HTTP third-party goroutine with app
//     frames (e.g. a lazily dialed gRPC client over an app-wrapped net.Conn) —
//     is eligible once stably parked in a quiescent wait state, skipping the
//     initial START on which a child was freshly adopted.
func canRebaselineTarget(t lingeringTarget, g *goroutineInfo, curSig goroutineSig, justAdopted bool) bool {
	if !curSig.active || !isQuiescentStatus(g.baseStatus) || justAdopted || isHTTPHandlerGoroutine(g) {
		return false
	}
	return true
}

// resetBrokenLingeringStreaks clears any in-progress re-baseline streak whose
// goroutine woke up, exited, or moved off its quiescent signature on an END
// snapshot (including intermediate END_WAIT polls). Called with controlMu held.
func resetBrokenLingeringStreaks(snap goroutineSnapshot, covered map[string]bool, ignore []string) {
	if len(lingeringStreaks) == 0 {
		return
	}
	for _, lt := range lingeringTails {
		for _, t := range lt.targets {
			st, ok := lingeringStreaks[t.gid]
			if !ok {
				continue
			}
			g, alive := snap.all[t.gid]
			if !alive || matchesIgnorePatterns(g, ignore) {
				delete(lingeringStreaks, t.gid)
				continue
			}
			curSig := goroutineStateSig(g, snap.selfID, covered, ignore)
			if !canRebaselineTarget(t, g, curSig, false) || st.sig != curSig {
				delete(lingeringStreaks, t.gid)
			}
		}
	}
}

// checkEndWatchesAndStreaks checks armed resumeWatches, tracks original idle
// signatures of fired non-mustExit watches (updating testStartIdle and
// lastQuiescentSigs whenever a watched goroutine returns to its original idle
// either directly or on a later END_WAIT poll), and resets broken
// lingeringStreaks against a single non-truncated END snapshot. Called with
// controlMu held BEFORE findActiveTestGoroutinesWithLingering on each poll.
func checkEndWatchesAndStreaks(snap goroutineSnapshot, testContaminated []string, testStartIdle map[uint64]startBaseline, watchOrigIdle *map[uint64]goroutineSig, covered map[string]bool, ignore []string) []string {
	triggered, firedOrig, _ := checkResumeWatches(snap, testStartIdle, covered, ignore)
	if len(triggered) > 0 {
		seen := make(map[string]struct{}, len(testContaminated)+len(triggered))
		for _, m := range testContaminated {
			if !strings.HasPrefix(m, "+") {
				seen[m] = struct{}{}
			}
		}
		var addMarks []string
		for _, tid := range triggered {
			mark := "tail:" + tid
			if _, dup := seen[mark]; !dup {
				seen[mark] = struct{}{}
				addMarks = append(addMarks, mark)
			}
		}
		if len(addMarks) > 0 {
			testContaminated = mergeContaminated(testContaminated, addMarks)
		}
	}
	for gid, origIdle := range firedOrig {
		if *watchOrigIdle == nil {
			*watchOrigIdle = make(map[uint64]goroutineSig)
		}
		(*watchOrigIdle)[gid] = origIdle
	}
	for gid, origIdle := range *watchOrigIdle {
		g, alive := snap.all[gid]
		if !alive {
			delete(*watchOrigIdle, gid)
			continue
		}
		curSig := goroutineStateSig(g, snap.selfID, covered, ignore)
		if !curSig.active || curSig == origIdle {
			lastQuiescentSigs[gid] = curSig
			if testStartIdle != nil {
				testStartIdle[gid] = startBaseline{idle: curSig}
			}
			delete(*watchOrigIdle, gid)
		}
	}
	resetBrokenLingeringStreaks(snap, covered, ignore)
	return testContaminated
}

// pruneLingeringTails adopts eligible live children of lingering goroutines
// first, then removes targets whose goroutines have exited, match an ignore
// pattern, or (for non-mustExit targets) stopped running app frames or
// returned to their quiescent START idle state. Eligible targets that remain
// parked at the exact same quiescent signature across consecutive windows
// (START -> END -> START, after their birth test was cut and the next test was
// tainted tail:<testID>) move onto a one-time resumeWatch (bounded by
// maxResumeWatches) rather than staying permanently tainted or being trusted
// silently. Targets in skipStreak (e.g. freshly re-attached from a fired
// resumeWatch on this same START) skip the current START streak increment so
// they still undergo two full windows of observation before settling. Called
// with controlMu held.
func pruneLingeringTails(snap goroutineSnapshot, skipStreak map[uint64]struct{}, covered map[string]bool, ignore []string, onStart bool) {
	if len(lingeringTails) == 0 {
		return
	}
	newlyAdopted := adoptLingeringChildren(snap, covered, ignore)
	w := 0
	for _, lt := range lingeringTails {
		tw := 0
		for _, t := range lt.targets {
			g, alive := snap.all[t.gid]
			if !alive {
				delete(lingeringStreaks, t.gid)
				continue
			}
			if matchesIgnorePatterns(g, ignore) {
				delete(lingeringStreaks, t.gid)
				continue
			}
			curSig := goroutineStateSig(g, snap.selfID, covered, ignore)
			if !t.mustExit && (!curSig.active || curSig == t.idleSig) {
				delete(lingeringStreaks, t.gid)
				if curSig == t.idleSig {
					lastQuiescentSigs[t.gid] = t.idleSig
				}
				continue
			}
			_, justAdopted := newlyAdopted[t.gid]
			_, justAdded := skipStreak[t.gid]
			canRebaseline := canRebaselineTarget(t, g, curSig, justAdopted || justAdded)
			if onStart {
				if canRebaseline {
					st := lingeringStreaks[t.gid]
					if st.sig == curSig {
						st.count++
					} else {
						st = quiescentStreak{sig: curSig, count: 1}
					}
					if st.count >= rebaselineStreakLimit {
						if len(resumeWatches) < maxResumeWatches {
							// Preserve every child of t.gid already alive at watch
							// creation (anything alive now predates the watch).
							var preserved map[uint64]struct{}
							for cid, cg := range snap.all {
								if cg.createdByGoid == t.gid && !isSDKGoroutine(cg, snap.selfID) {
									if preserved == nil {
										preserved = make(map[uint64]struct{})
									}
									preserved[cid] = struct{}{}
								}
							}
							resumeWatches[t.gid] = resumeWatch{
								testID:              lt.testID,
								target:              t,
								watchSig:            curSig,
								preExistingChildren: preserved,
							}
							lastQuiescentSigs[t.gid] = curSig
							delete(lingeringStreaks, t.gid)
							continue
						}
						// Fail closed at maxResumeWatches: keep target in
						// lingeringTails (bounded by overflow cap) instead of
						// growing resumeWatches without bound.
						st.count = rebaselineStreakLimit
					}
					lingeringStreaks[t.gid] = st
				} else {
					delete(lingeringStreaks, t.gid)
				}
			} else if st, ok := lingeringStreaks[t.gid]; ok {
				// On END, reset any in-progress streak if the goroutine woke
				// up or moved off its quiescent signature during the window.
				if !canRebaseline || st.sig != curSig {
					delete(lingeringStreaks, t.gid)
				}
			}
			lt.targets[tw] = t
			tw++
		}
		lt.targets = lt.targets[:tw]
		// Drop exited goroutine IDs from preExistingChildren (goids are never reused).
		for cid := range lt.preExistingChildren {
			if _, alive := snap.all[cid]; !alive {
				delete(lt.preExistingChildren, cid)
			}
		}
		if len(lt.targets) > 0 {
			lingeringTails[w] = lt
			w++
		}
	}
	for i := w; i < len(lingeringTails); i++ {
		lingeringTails[i] = nil
	}
	lingeringTails = lingeringTails[:w]
}

// collectStartContamination computes the contamination markers for a newly
// starting test from any unclosed previous window, truncated START, immediately
// preceding cut window, already-fired resume watches (checked before noteGap),
// and any still-active lingering goroutines from earlier cut windows. It also
// resolves pending truncated cuts, adopts children, prunes finished lingering
// goroutines, and clears prevWindowCutTestID. Called with controlMu held after
// clearCounters succeeds.
func collectStartContamination(unclosedID string, snap goroutineSnapshot, haveSnap bool, firedWatches []string, skipStreak map[uint64]struct{}, covered map[string]bool, ignore []string) []string {
	var reasons []string
	seen := map[string]struct{}{}
	add := func(r string) {
		if _, dup := seen[r]; dup {
			return
		}
		seen[r] = struct{}{}
		reasons = append(reasons, r)
	}
	if unclosedID != "" {
		add("unclosed:" + unclosedID)
	}
	if haveSnap && snap.truncated {
		add("stack-truncated")
	}
	if prevWindowCutTestID != "" {
		add("tail:" + prevWindowCutTestID)
		prevWindowCutTestID = ""
	}
	for _, tid := range pendingTailTestIDs {
		add("tail:" + tid)
	}
	pendingTailTestIDs = nil
	for _, rwTest := range firedWatches {
		add("tail:" + rwTest)
	}
	if haveSnap && !snap.truncated {
		resolveTruncatedCutWindows(snap, covered, ignore)
		pruneLingeringTails(snap, skipStreak, covered, ignore, true)
	}
	for _, tc := range pendingTruncatedCuts {
		add("tail:" + tc.testID)
	}
	for _, lt := range lingeringTails {
		add("tail:" + lt.testID)
	}
	return capContaminated(reasons)
}

// capContaminated caps a contamination slice cleanly at maxPartialNames items
// plus a single "+N" summary entry (avoiding multiple "+N" suffixes).
func capContaminated(items []string) []string {
	if len(items) <= maxPartialNames {
		return items
	}
	out := make([]string, maxPartialNames+1)
	copy(out, items[:maxPartialNames])
	out[maxPartialNames] = "+" + strconv.Itoa(len(items)-maxPartialNames)
	return out
}

// mergeContaminated combines start-time contamination markers (unclosed/tail/
// window-cut/stack-truncated) with between-tests overlap markers without
// allocating on the common clean path, ensuring at most one trailing "+N"
// summary entry.
func mergeContaminated(startMarks, gapMarks []string) []string {
	if len(startMarks) == 0 && (len(gapMarks) <= maxPartialNames || (len(gapMarks) == maxPartialNames+1 && strings.HasPrefix(gapMarks[maxPartialNames], "+"))) {
		return gapMarks
	}
	if len(gapMarks) == 0 && len(startMarks) <= maxPartialNames {
		out := make([]string, len(startMarks))
		copy(out, startMarks)
		return out
	}
	var base []string
	extra := 0
	collect := func(src []string) {
		for _, s := range src {
			if strings.HasPrefix(s, "+") {
				if n, err := strconv.Atoi(s[1:]); err == nil && n > 0 {
					extra += n
					continue
				}
			}
			base = append(base, s)
		}
	}
	collect(startMarks)
	collect(gapMarks)
	if len(base) > maxPartialNames {
		extra += len(base) - maxPartialNames
		base = base[:maxPartialNames]
	}
	if extra > 0 {
		base = append(base, "+"+strconv.Itoa(extra))
	}
	return base
}

// resetGoroutineState clears all goroutine-tail tracking state. Used by tests.
func resetGoroutineState() {
	activeTracking = false
	activeTruncatedStart = false
	startGids = nil
	startIdleStates = nil
	lastCompleteGids = nil
	lastQuiescentSigs = map[uint64]goroutineSig{}
	lingeringStreaks = map[uint64]quiescentStreak{}
	resumeWatches = map[uint64]resumeWatch{}
	activeContaminated = nil
	lingeringTails = nil
	prevWindowCutTestID = ""
	pendingTailTestIDs = nil
	pendingTruncatedCuts = nil
	saidLingering = map[string]bool{}
	saidStackTruncated = false
	pendingEndWindowCut = false
	pendingEndLingering = nil
	pendingEndContaminated = nil
}
