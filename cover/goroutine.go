package cover

import (
	"bytes"
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
	// envEndWaitMS sets how long END polls for newly-spawned app goroutines to
	// exit before declaring windowCut=true (default 200ms, capped at 2000ms;
	// 0 disables waiting and checks immediately).
	envEndWaitMS = "KEPLOY_GO_END_WAIT_MS"

	defaultEndWaitMS = 200
	maxEndWaitMS     = 2000
	endWaitPollEvery = 5 * time.Millisecond

	stackInitBufSize  = 64 << 10 // 64 KiB initial stack buffer
	stackMaxBufSize   = 16 << 20 // 16 MiB cap so pathological apps cannot OOM
	maxLingeringTails = 64
)

// goroutineInfo is the parsed identity and stack summary of one live goroutine
// from runtime.Stack(buf, true).
type goroutineInfo struct {
	id           uint64
	frames       []string
	framePkgs    []string
	createdByFn  string
	createdByPkg string
}

// goroutineSnapshot holds all live goroutines parsed from one runtime.Stack
// capture, plus the caller's own goroutine ID and whether the capture hit
// stackMaxBufSize and may have truncated.
type goroutineSnapshot struct {
	selfID    uint64
	all       map[uint64]*goroutineInfo
	truncated bool
}

// lingeringTail tracks one test whose END was cut with app-relevant goroutines
// still alive. It remains active until all of its goroutine IDs exit.
type lingeringTail struct {
	testID string
	gids   []uint64
	names  []string
}

var (
	// Goroutine-tail tracking state, all guarded by controlMu.
	activeTracking      bool
	startGids           map[uint64]struct{}
	activeContaminated  []string
	lingeringTails      []*lingeringTail
	prevWindowCutTestID string

	// pendingEnd* carry finalized END-time tail metadata into reportCoverage
	// while controlMu is held, keeping publishCoverage's signature unchanged.
	pendingEndWindowCut    bool
	pendingEndLingering    []string
	pendingEndContaminated []string

	// stackBuf is lazily allocated on first armed START and reused under
	// controlMu so unarmed apps allocate nothing and armed runs avoid repeated
	// 64 KiB allocations.
	stackBuf []byte

	// snapshotGoroutines captures all live goroutines; a variable for tests.
	snapshotGoroutines = liveGoroutineSnapshot
)

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
	id, _ := parseGoroutineHeader(string(buf[:n]))
	return id
}

// liveGoroutineSnapshot captures and parses all live goroutines using
// runtime.Stack(buf, true). Called with controlMu held.
func liveGoroutineSnapshot() goroutineSnapshot {
	selfID := currentGoroutineID()
	raw, truncated := captureStackAll()
	return parseGoroutineStacks(raw, selfID, truncated)
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

// parseGoroutineStacks parses the output of runtime.Stack(buf, true) into a
// goroutineSnapshot keyed by unique monotonic goroutine ID.
func parseGoroutineStacks(raw []byte, selfID uint64, truncated bool) goroutineSnapshot {
	snap := goroutineSnapshot{
		selfID:    selfID,
		all:       make(map[uint64]*goroutineInfo),
		truncated: truncated,
	}
	var cur *goroutineInfo
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
			continue
		}
		if bytes.HasPrefix(lineBytes, []byte("goroutine ")) {
			if id, ok := parseGoroutineHeader(string(lineBytes)); ok {
				cur = &goroutineInfo{id: id}
				snap.all[id] = cur
			} else {
				cur = nil
			}
			continue
		}
		if cur == nil {
			continue
		}
		if lineBytes[0] == '\t' || bytes.HasPrefix(lineBytes, []byte("...")) {
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
			fn, pkg := parseCreatedBy(line)
			if fn != "" {
				cur.createdByFn = fn
				cur.createdByPkg = pkg
			}
			continue
		}
		fn, pkg := parseFrameFunc(line)
		if fn != "" {
			cur.frames = append(cur.frames, fn)
			cur.framePkgs = append(cur.framePkgs, pkg)
		}
	}
	return snap
}

// parseGoroutineHeader parses the monotonic goroutine ID from a runtime.Stack
// header line such as "goroutine 42 [IO wait]:".
func parseGoroutineHeader(line string) (uint64, bool) {
	if !strings.HasPrefix(line, "goroutine ") {
		return 0, false
	}
	rest := line[len("goroutine "):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	id, err := strconv.ParseUint(rest[:end], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
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

// parseCreatedBy extracts the creator function name and package path from a
// "created by <fn>[ in goroutine <id>]" line.
func parseCreatedBy(line string) (fn, pkg string) {
	rest := strings.TrimPrefix(line, "created by ")
	if idx := strings.Index(rest, " in goroutine "); idx >= 0 {
		rest = rest[:idx]
	}
	fn = strings.TrimSpace(rest)
	if fn == "" {
		return "", ""
	}
	return fn, pkgOfFunc(fn)
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

// isAppRelevantGoroutine reports whether g was spawned by or is executing
// application code (as opposed to go-sdk's own goroutines or idle stdlib/runtime
// keep-alive/housekeeping goroutines).
func isAppRelevantGoroutine(g *goroutineInfo, selfID uint64, covered map[string]bool) bool {
	if isSDKGoroutine(g, selfID) {
		return false
	}
	if isAppPkg(g.createdByPkg, covered) {
		return true
	}
	for _, pkg := range g.framePkgs {
		if isAppPkg(pkg, covered) {
			return true
		}
	}
	return false
}

// gidSetOf returns the set of non-SDK goroutine IDs currently alive in snap.
func gidSetOf(snap goroutineSnapshot) map[uint64]struct{} {
	set := make(map[uint64]struct{}, len(snap.all))
	for id, g := range snap.all {
		if isSDKGoroutine(g, snap.selfID) {
			continue
		}
		set[id] = struct{}{}
	}
	return set
}

// findNewAppGoroutines returns all app-relevant goroutines currently alive in
// snap whose unique monotonic ID was not present in startSet, sorted by ID.
func findNewAppGoroutines(snap goroutineSnapshot, startSet map[uint64]struct{}, covered map[string]bool) []*goroutineInfo {
	var out []*goroutineInfo
	for id, g := range snap.all {
		if _, existed := startSet[id]; existed {
			continue
		}
		if !isAppRelevantGoroutine(g, snap.selfID, covered) {
			continue
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
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

// summarizeGoroutineNames returns sorted, unique descriptive function names
// for gs, capped at maxPartialNames plus "+N".
func summarizeGoroutineNames(gs []*goroutineInfo, covered map[string]bool) []string {
	if len(gs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(gs))
	names := make([]string, 0, len(gs))
	for _, g := range gs {
		name := describeAppGoroutine(g, covered)
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

// addLingeringTail records a test whose window was cut with lingering
// goroutines, keeping lingeringTails bounded to maxLingeringTails entries.
// Called with controlMu held.
func addLingeringTail(testID string, gids []uint64, names []string) {
	if len(gids) == 0 {
		return
	}
	// If testID already has an entry (e.g. re-run of the same ID), merge gids.
	for _, lt := range lingeringTails {
		if lt.testID == testID {
			have := make(map[uint64]struct{}, len(lt.gids))
			for _, id := range lt.gids {
				have[id] = struct{}{}
			}
			for _, id := range gids {
				if _, ok := have[id]; !ok {
					lt.gids = append(lt.gids, id)
					have[id] = struct{}{}
				}
			}
			lt.names = names
			return
		}
	}
	if len(lingeringTails) >= maxLingeringTails {
		lingeringTails = append(lingeringTails[:0], lingeringTails[len(lingeringTails)-maxLingeringTails+1:]...)
	}
	gidsCopy := make([]uint64, len(gids))
	copy(gidsCopy, gids)
	lingeringTails = append(lingeringTails, &lingeringTail{
		testID: testID,
		gids:   gidsCopy,
		names:  names,
	})
}

// pruneLingeringTails removes exited goroutine IDs from lingeringTails and
// drops any entry whose goroutines have all exited. Called with controlMu held.
func pruneLingeringTails(snap goroutineSnapshot) {
	if len(lingeringTails) == 0 {
		return
	}
	w := 0
	for _, lt := range lingeringTails {
		gw := 0
		for _, id := range lt.gids {
			if _, alive := snap.all[id]; alive {
				lt.gids[gw] = id
				gw++
			}
		}
		lt.gids = lt.gids[:gw]
		if len(lt.gids) > 0 {
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
// starting test from any unclosed previous window, an immediately preceding
// cut window, and any still-alive lingering goroutines from earlier cut
// windows. It also prunes exited lingering goroutines and clears
// prevWindowCutTestID. Called with controlMu held after clearCounters succeeds.
func collectStartContamination(unclosedID string, snap goroutineSnapshot, haveSnap bool) []string {
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
	if prevWindowCutTestID != "" {
		add("tail:" + prevWindowCutTestID)
		prevWindowCutTestID = ""
	}
	if haveSnap && !snap.truncated {
		pruneLingeringTails(snap)
	}
	tailCount := 0
	extraTails := 0
	for _, lt := range lingeringTails {
		r := "tail:" + lt.testID
		if _, dup := seen[r]; dup {
			continue
		}
		if tailCount < maxPartialNames {
			add(r)
			tailCount++
		} else {
			extraTails++
		}
	}
	if extraTails > 0 {
		reasons = append(reasons, "+"+strconv.Itoa(extraTails))
	}
	return reasons
}

// mergeContaminated combines start-time contamination markers (unclosed/tail)
// with between-tests overlap markers without allocating on the common clean path.
func mergeContaminated(startMarks, gapMarks []string) []string {
	if len(startMarks) == 0 {
		return gapMarks
	}
	if len(gapMarks) == 0 {
		out := make([]string, len(startMarks))
		copy(out, startMarks)
		return out
	}
	out := make([]string, 0, len(startMarks)+len(gapMarks))
	out = append(out, startMarks...)
	out = append(out, gapMarks...)
	return out
}

// resetGoroutineState clears all goroutine-tail tracking state. Used by tests.
func resetGoroutineState() {
	activeTracking = false
	startGids = nil
	activeContaminated = nil
	lingeringTails = nil
	prevWindowCutTestID = ""
	pendingEndWindowCut = false
	pendingEndLingering = nil
	pendingEndContaminated = nil
}
