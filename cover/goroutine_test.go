package cover

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseGoroutineStacks(t *testing.T) {
	raw := strings.Join([]string{
		"goroutine 19 [running]:",
		ownPkgPath + ".captureStackAll()",
		"\t/src/cover/goroutine.go:100 +0x25",
		ownPkgPath + ".handleControlRequest({0x1234, 0x5678})",
		"\t/src/cover/hook.go:184 +0x120",
		"created by " + ownPkgPath + ".serveControl in goroutine 6",
		"\t/src/cover/hook.go:165 +0x90",
		"",
		"goroutine 42 [sleep, 2 minutes]:\r",
		"time.Sleep(0x8f0d180)\r",
		"\t/usr/local/go/src/runtime/time.go:200 +0x105\r",
		"example.com/svc/orders.(*Engine[net/http.Request]).CheckFraud.func1(0xc000123456)\r",
		"\t/app/orders/fraud.go:55 +0x3c\r",
		"...additional frames elided...\r",
		"created by main.handleOrder in goroutine 35\r",
		"\t/app/main.go:42 +0x120\r",
		"[originating from goroutine 1]:\r",
		"main.ancestorFunc()\r",
		"\t/app/main.go:10 +0x10\r",
		"created by runtime.main\r",
		"\t/usr/local/go/src/runtime/proc.go:100 +0x10\r",
		"",
		"goroutine 55 [IO wait]:",
		"internal/poll.runtime_pollWait(0x7f, 0x72)",
		"\t/usr/local/go/src/runtime/netpoll.go:345 +0x85",
		"net/http.(*conn).serve(0xc000, {0x1, 0x2})",
		"\t/usr/local/go/src/net/http/server.go:1994 +0x5e8",
		"created by net/http.(*Server).Serve",
		"\t/usr/local/go/src/net/http/server.go:3285 +0x4b4",
		"",
		"goroutine 77 [chan receive, 5 minutes]:",
		"example.com/app%2ev1/worker.Run(0x1)",
		"\t/app/worker.go:12 +0x10",
		"created by example.com/app%2ev1/api.(*Handler).Post in goroutine 35",
		"\t/app/api/handler.go:20 +0x20",
	}, "\n")

	snap := parseGoroutineStacks([]byte(raw), 19, false)
	if len(snap.all) != 4 {
		t.Fatalf("parsed %d goroutines, want 4: %+v", len(snap.all), snap.all)
	}

	g42 := snap.all[42]
	if g42 == nil {
		t.Fatal("missing goroutine 42")
	}
	if g42.baseStatus != "sleep" {
		t.Fatalf("g42 baseStatus = %q, want sleep (duration stripped)", g42.baseStatus)
	}
	if g42.topLine != "/usr/local/go/src/runtime/time.go:200" {
		t.Fatalf("g42 topLine = %q", g42.topLine)
	}
	wantFrames42 := []string{"time.Sleep", "example.com/svc/orders.(*Engine[net/http.Request]).CheckFraud.func1"}
	wantPkgs42 := []string{"time", "example.com/svc/orders"}
	wantLines42 := []string{"/usr/local/go/src/runtime/time.go:200", "/app/orders/fraud.go:55"}
	if !reflect.DeepEqual(g42.frames, wantFrames42) {
		t.Fatalf("g42 frames = %v, want %v", g42.frames, wantFrames42)
	}
	if !reflect.DeepEqual(g42.framePkgs, wantPkgs42) {
		t.Fatalf("g42 framePkgs = %v, want %v", g42.framePkgs, wantPkgs42)
	}
	if !reflect.DeepEqual(g42.frameLines, wantLines42) {
		t.Fatalf("g42 frameLines = %v, want %v", g42.frameLines, wantLines42)
	}
	if g42.createdByFn != "main.handleOrder" || g42.createdByPkg != "main" || g42.createdByGoid != 35 {
		t.Fatalf("g42 createdBy = (%q, %q, %d), want (main.handleOrder, main, 35) [ancestor must not overwrite]",
			g42.createdByFn, g42.createdByPkg, g42.createdByGoid)
	}

	g55 := snap.all[55]
	if g55 == nil || g55.createdByFn != "net/http.(*Server).Serve" || g55.createdByPkg != "net/http" || g55.createdByGoid != 0 {
		t.Fatalf("g55 pre-1.21 createdBy parsed wrong: %+v", g55)
	}

	g77 := snap.all[77]
	if g77 == nil || g77.baseStatus != "chan receive" || g77.topLine != "/app/worker.go:12" ||
		g77.framePkgs[0] != "example.com/app.v1/worker" || g77.createdByPkg != "example.com/app.v1/api" || g77.createdByGoid != 35 {
		t.Fatalf("g77 percent-escaped pkg parsed wrong: %+v", g77)
	}
}

func TestIsAppRelevantGoroutine(t *testing.T) {
	covered := map[string]bool{
		"main":               true,
		"example.com/app":    true,
		"example.com/app/db": true,
	}
	cases := []struct {
		name    string
		g       *goroutineInfo
		covered map[string]bool
		ignore  []string
		want    bool
	}{
		{
			name:    "self SDK goroutine excluded",
			g:       &goroutineInfo{id: 19, frames: []string{"main.foo"}, framePkgs: []string{"main"}},
			covered: covered,
			want:    false,
		},
		{
			name:    "serveControl SDK goroutine excluded",
			g:       &goroutineInfo{id: 20, frames: []string{ownPkgPath + ".serveControl"}, framePkgs: []string{ownPkgPath}, createdByFn: ownPkgPath + ".startControlServer", createdByPkg: ownPkgPath},
			covered: covered,
			want:    false,
		},
		{
			name:    "runTableStream SDK goroutine excluded",
			g:       &goroutineInfo{id: 21, frames: []string{ownPkgPath + ".runTableStream"}, framePkgs: []string{ownPkgPath}, createdByFn: ownPkgPath + ".handleTableCommand", createdByPkg: ownPkgPath},
			covered: covered,
			want:    false,
		},
		{
			name:    "idle net/http server conn excluded",
			g:       &goroutineInfo{id: 30, frames: []string{"internal/poll.runtime_pollWait", "net/http.(*conn).readRequest", "net/http.(*conn).serve"}, framePkgs: []string{"internal/poll", "net/http", "net/http"}, createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http"},
			covered: covered,
			want:    false,
		},
		{
			name:    "idle net/http persistConn readLoop excluded",
			g:       &goroutineInfo{id: 31, frames: []string{"net/http.(*persistConn).readLoop"}, framePkgs: []string{"net/http"}, createdByFn: "net/http.(*Transport).dialConn", createdByPkg: "net/http"},
			covered: covered,
			want:    false,
		},
		{
			name:    "database/sql connectionOpener excluded",
			g:       &goroutineInfo{id: 32, frames: []string{"database/sql.(*DB).connectionOpener"}, framePkgs: []string{"database/sql"}, createdByFn: "database/sql.OpenDB", createdByPkg: "database/sql"},
			covered: covered,
			want:    false,
		},
		{
			name:    "runtime GC worker excluded",
			g:       &goroutineInfo{id: 33, frames: []string{"runtime.gcBgMarkWorker"}, framePkgs: []string{"runtime"}, createdByFn: "runtime.gcBgMarkStartWorkers", createdByPkg: "runtime"},
			covered: covered,
			want:    false,
		},
		{
			name:    "prod covered mode excludes pure third-party goroutine with no app frames",
			g:       &goroutineInfo{id: 34, frames: []string{"github.com/segmentio/kafka-go.(*Writer).run"}, framePkgs: []string{"github.com/segmentio/kafka-go"}, createdByFn: "github.com/segmentio/kafka-go.NewWriter", createdByPkg: "github.com/segmentio/kafka-go"},
			covered: covered,
			want:    false,
		},
		{
			name:    "prod covered mode catches third-party goroutine executing app callback",
			g:       &goroutineInfo{id: 35, frames: []string{"example.com/app/db.OnComplete", "github.com/segmentio/kafka-go.(*Writer).run"}, framePkgs: []string{"example.com/app/db", "github.com/segmentio/kafka-go"}, createdByFn: "github.com/segmentio/kafka-go.NewWriter", createdByPkg: "github.com/segmentio/kafka-go"},
			covered: covered,
			want:    true,
		},
		{
			name:    "app goroutine blocked in net/http caught via app frame and createdBy",
			g:       &goroutineInfo{id: 40, frames: []string{"internal/poll.runtime_pollWait", "net/http.(*Client).Do", "main.fraudAlert"}, framePkgs: []string{"internal/poll", "net/http", "main"}, createdByFn: "main.handleOrder", createdByPkg: "main"},
			covered: covered,
			want:    true,
		},
		{
			name:    "stdlib call spawned directly by app handler caught via createdBy",
			g:       &goroutineInfo{id: 41, frames: []string{"net/http.(*Client).Do"}, framePkgs: []string{"net/http"}, createdByFn: "example.com/app/db.AsyncSync", createdByPkg: "example.com/app/db"},
			covered: covered,
			want:    true,
		},
		{
			name:    "time.AfterFunc invoking app callback caught via app frame",
			g:       &goroutineInfo{id: 42, frames: []string{"time.Sleep", "main.delayedNotify"}, framePkgs: []string{"time", "main"}, createdByFn: "time.goFunc", createdByPkg: "time"},
			covered: covered,
			want:    true,
		},
		{
			name:    "explicit ignore pattern excludes matching app goroutine",
			g:       &goroutineInfo{id: 43, frames: []string{"main.lazyMetricsLoop"}, framePkgs: []string{"main"}, createdByFn: "main.initMetrics", createdByPkg: "main"},
			covered: covered,
			ignore:  []string{"lazyMetricsLoop"},
			want:    false,
		},
		{
			name:    "fallback mode (covered=nil) excludes stdlib and http2",
			g:       &goroutineInfo{id: 50, frames: []string{"golang.org/x/net/http2.(*serverConn).serve"}, framePkgs: []string{"golang.org/x/net/http2"}, createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http"},
			covered: nil,
			want:    false,
		},
		{
			name:    "fallback mode (covered=nil) catches non-stdlib app goroutine",
			g:       &goroutineInfo{id: 51, frames: []string{"time.Sleep", "example.com/other/worker.Run"}, framePkgs: []string{"time", "example.com/other/worker"}, createdByFn: "example.com/other/api.Handle", createdByPkg: "example.com/other/api"},
			covered: nil,
			want:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isAppRelevantGoroutineWithIgnore(tc.g, 19, tc.covered, tc.ignore)
			if got != tc.want {
				t.Fatalf("isAppRelevantGoroutine = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCapsStringAndProto3BackwardCompat(t *testing.T) {
	stubCoverage(t, nil, nil)
	origGoVersion := runtimeGoVersion
	t.Cleanup(func() { runtimeGoVersion = origGoVersion })

	// Default (Go >= 1.21): windowCut=true, asyncExecutors=false ALWAYS in R1.
	t.Setenv(envGoroutineTracking, "")
	runtimeGoVersion = func() string { return "go1.27.3" }
	got := capsString(true)
	if got != defaultCaps {
		t.Fatalf("default caps = %q, want %q", got, defaultCaps)
	}
	if !strings.Contains(got, "asyncExecutors=false") {
		t.Fatalf("B1 violated: default caps must declare asyncExecutors=false, got %q", got)
	}
	if strings.ContainsAny(got, " \t\r\n") {
		t.Fatalf("caps must not contain whitespace: %q", got)
	}

	// SF3: tie windowCut cap to runtime.Version() (Go < 1.21 -> windowCut=false),
	// and assert asyncExecutors=false in ALL cases.
	for _, tc := range []struct {
		ver           string
		wantAdoption  bool
		wantWindowCut bool
	}{
		{"go1.20.14", false, false},
		{"go1.20", false, false},
		{"go1.20rc1", false, false},
		{"go1.19.13", false, false},
		{"go1.21.0", true, true},
		{"go1.21rc2", true, true},
		{"go1.27.3", true, true},
		{"go1.24.0 X:boringcrypto", true, true},
		{"go2.0.0", true, true},
		{"devel +abc123", true, true},
		{"devel go1.24-abc123 Thu Jan 1 00:00:00 2026 +0000", true, true},
		{"devel go1.20-abc123", false, false},
		{"", false, false},
		{"unknown", false, false},
	} {
		if gotAdopt := supportsGoroutineChildAdoption(tc.ver); gotAdopt != tc.wantAdoption {
			t.Fatalf("supportsGoroutineChildAdoption(%q) = %v, want %v", tc.ver, gotAdopt, tc.wantAdoption)
		}
		runtimeGoVersion = func() string { return tc.ver }
		caps := capsString(true)
		wantCaps := fmt.Sprintf("windowCut=%t,contaminated=true,classMismatch=false,background=true,asyncExecutors=false,listenerContamination=false", tc.wantWindowCut)
		if caps != wantCaps {
			t.Fatalf("runtime.Version()=%q -> caps = %q, want %q", tc.ver, caps, wantCaps)
		}
	}
	runtimeGoVersion = origGoVersion

	// Kill switch disables windowCut and keeps asyncExecutors=false.
	for _, offVal := range []string{"off", "OFF", "false", "0", "no"} {
		t.Setenv(envGoroutineTracking, offVal)
		offCaps := capsString(true)
		wantOff := "windowCut=false,contaminated=true,classMismatch=false,background=true,asyncExecutors=false,listenerContamination=false"
		if offCaps != wantOff {
			t.Fatalf("%s=%q -> caps = %q, want %q", envGoroutineTracking, offVal, offCaps, wantOff)
		}
	}
	t.Setenv(envGoroutineTracking, "")

	// Inactive coverage declares all false.
	wantInactive := "windowCut=false,contaminated=false,classMismatch=false,background=false,asyncExecutors=false,listenerContamination=false"
	if got := capsString(false); got != wantInactive {
		t.Fatalf("inactive caps = %q, want %q", got, wantInactive)
	}

	// Per-func granularity (non-block coverage) disables windowCut, keeps
	// asyncExecutors=false, AND disables runtime goroutine tracking consistently.
	meta = &metaFile{mode: ctrModeAtomic, granularity: ctrGranularityPerFunc}
	wantPerFunc := "windowCut=false,contaminated=true,classMismatch=false,background=true,asyncExecutors=false,listenerContamination=false"
	if got := capsString(true); got != wantPerFunc {
		t.Fatalf("per-func caps = %q, want %q", got, wantPerFunc)
	}
	if goroutineTrackingActive() {
		t.Fatal("goroutineTrackingActive() = true under per-func granularity, want false")
	}
	meta = nil

	// Backward-compat: simulate enterprise's proto>=2 reply parser against v3 replies.
	parseLegacyProto2 := func(reply string) (ok bool, proto int, bg string, hasBG bool) {
		fields := strings.Fields(reply)
		if len(fields) == 0 {
			return false, 0, "", false
		}
		ok = fields[0] == "ACK"
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "proto=") {
				proto, _ = strconv.Atoi(strings.TrimPrefix(f, "proto="))
			} else if strings.HasPrefix(f, "bg=") {
				bg = strings.TrimPrefix(f, "bg=")
				hasBG = true
			}
		}
		return ok, proto, bg, hasBG
	}
	for _, tc := range []struct {
		reply   string
		wantOK  bool
		wantBG  string
		wantHas bool
	}{
		{replyTaken, true, "", false},
		{replyStartUnasked, true, "-", true},
		{replyStartAskedBy("tok99"), true, "tok99", true},
		{replyIgnored, false, "", false},
	} {
		ok, proto, bg, hasBG := parseLegacyProto2(tc.reply)
		if ok != tc.wantOK || proto != 3 || bg != tc.wantBG || hasBG != tc.wantHas {
			t.Fatalf("legacy parse(%q) = (ok=%v proto=%d bg=%q hasBG=%v), want (ok=%v proto=3 bg=%q hasBG=%v)",
				tc.reply, ok, proto, bg, hasBG, tc.wantOK, tc.wantBG, tc.wantHas)
		}
	}
}

func TestEndWaitDurationBounds(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want time.Duration
	}{
		{"", 200 * time.Millisecond},
		{"0", 0},
		{"30", 30 * time.Millisecond},
		{"2000", 2000 * time.Millisecond},
		{"9999", 2000 * time.Millisecond},
		{"-10", 200 * time.Millisecond},
		{"invalid", 200 * time.Millisecond},
	} {
		t.Setenv(envEndWaitMS, tc.env)
		if got := endWaitDuration(); got != tc.want {
			t.Errorf("%s=%q: endWaitDuration() = %v, want %v", envEndWaitMS, tc.env, got, tc.want)
		}
	}
}

func TestGoroutineTrackingKillSwitchDoesNotSnapshot(t *testing.T) {
	t.Setenv(envGoroutineTracking, "off")
	h := newGapHarness(t, lines(appFile, 3))
	var snapCalls atomic.Int32
	snapshotGoroutines = func() goroutineSnapshot {
		snapCalls.Add(1)
		return goroutineSnapshot{all: map[uint64]*goroutineInfo{}}
	}

	wantOffCaps := "windowCut=false,contaminated=true,classMismatch=false,background=true,asyncExecutors=false,listenerContamination=false"
	wantStart := "ACK proto=3 bg=- caps=" + wantOffCaps + "\n"
	wantEnd := "ACK proto=3 caps=" + wantOffCaps + "\n"

	h.do("START set-0/test-1", wantStart)
	h.do("END set-0/test-1", wantEnd)

	if got := snapCalls.Load(); got != 0 {
		t.Fatalf("snapshotGoroutines called %d times with %s=off, want 0", got, envGoroutineTracking)
	}
	rec := h.test("set-0/test-1")
	if _, hasCut := rec["windowCut"]; hasCut {
		t.Fatalf("kill-switched test carried windowCut: %v", rec)
	}
	if _, hasCont := rec["contaminated"]; hasCont {
		t.Fatalf("kill-switched test carried contaminated: %v", rec)
	}
}

func TestUnclosedWindowAndLingeringTailStateMachine(t *testing.T) {
	t.Run("unclosed START->START marks next window contaminated and omits overlapLinesByFile", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, map[string][]int{appFile: {3}, workerFile: {7}})
		everGap[workerFile] = map[int]struct{}{7: {}}
		snapshotGoroutines = func() goroutineSnapshot {
			return goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		}
		h.do("START set-0/test-1", replyStartUnasked)
		// No END set-0/test-1 -> unclosed!
		h.do("START set-0/test-2", replyStartUnasked)
		h.do("END set-0/test-2", replyTaken)
		rec2 := h.test("set-0/test-2")
		got, _ := rec2["contaminated"].([]interface{})
		if !reflect.DeepEqual(got, []interface{}{"unclosed:set-0/test-1", "between-tests:" + workerFile}) {
			t.Fatalf("test-2 contaminated = %v, want [unclosed:set-0/test-1 between-tests:%s]", got, workerFile)
		}
		if _, hasOverlap := rec2["overlapLinesByFile"]; hasOverlap {
			t.Fatalf("test-2 with unclosed mark must omit overlapLinesByFile so confirmBetweenTests cannot trust it: %v", rec2)
		}
	})

	t.Run("immediate N+1 contaminated even when goroutine exits right at boundary", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5))
		h.ask("r1")
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartAskedBy("r1"))
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1:  {id: 1},
			99: {id: 99, baseStatus: "sleep", frames: []string{"main.fraudAlert"}, framePkgs: []string{"main"}, createdByFn: "main.handle", createdByPkg: "main"},
		}}
		h.do("END set-0/T1", replyTaken)
		t1 := h.test("set-0/T1")
		if t1["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", t1["windowCut"])
		}
		if !reflect.DeepEqual(t1["lingeringGoroutines"], []interface{}{"main.fraudAlert"}) {
			t.Fatalf("T1 lingeringGoroutines = %v, want [main.fraudAlert]", t1["lingeringGoroutines"])
		}

		// Goroutine 99 exits right in the boundary BEFORE START T2!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		h.run("set-0/T2")
		t2 := h.test("set-0/T2")
		if !reflect.DeepEqual(t2["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", t2["contaminated"])
		}
		if _, hasCut := t2["windowCut"]; hasCut {
			t.Fatalf("T2 unexpectedly marked windowCut: %v", t2)
		}

		// T3 is completely clean!
		h.run("set-0/T3")
		t3 := h.test("set-0/T3")
		if _, hasCont := t3["contaminated"]; hasCont {
			t.Fatalf("T3 unexpectedly contaminated: %v", t3)
		}
		if _, hasCut := t3["windowCut"]; hasCut {
			t.Fatalf("T3 unexpectedly windowCut: %v", t3)
		}
	})

	t.Run("B1 child goroutine spawned in gap after parent exits stays tracked", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), lines(workerFile, 9), lines(appFile, 5), none(), lines(appFile, 6))
		h.ask("r1")
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1 spawns G1 (99) -> cut.
		h.do("START set-0/T1", replyStartAskedBy("r1"))
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1:  {id: 1},
			99: {id: 99, baseStatus: "sleep", frames: []string{"main.fraudAlert"}, framePkgs: []string{"main"}, createdByFn: "main.handle", createdByPkg: "main"},
		}}
		h.do("END set-0/T1", replyTaken)

		// T2 runs while G1 is alive -> contaminated tail:T1.
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// In gap T2->T3, G1 spawns child G2 (100, createdByGoid=99) and G1 EXITS!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1:   {id: 1},
			100: {id: 100, baseStatus: "sleep", frames: []string{"main.publishAlert"}, framePkgs: []string{"main"}, createdByFn: "main.fraudAlert", createdByPkg: "main", createdByGoid: 99},
		}}
		h.do("START set-0/T3", replyStartAskedBy("r1"))
		// G2 exits during T3.
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		h.do("END set-0/T3", replyTaken)

		if !reflect.DeepEqual(h.test("set-0/T3")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("B1 failed: T3 contaminated = %v, want [tail:set-0/T1] via adopted child G2", h.test("set-0/T3")["contaminated"])
		}
		// Gap T2->T3 must be afterCut with empty executedLinesByFile!
		bgs := h.backgrounds()
		if len(bgs) != 1 || bgs[0]["afterCut"] != true {
			t.Fatalf("B1 gap background = %v, want afterCut:true", bgs)
		}
		if linesMap, _ := bgs[0]["executedLinesByFile"].(map[string]interface{}); len(linesMap) != 0 {
			t.Fatalf("afterCut background must send empty executedLinesByFile for v2 collector safety: %v", bgs[0])
		}

		// T4 is clean after G2 exited!
		h.run("set-0/T4")
		if _, hasCont := h.test("set-0/T4")["contaminated"]; hasCont {
			t.Fatalf("T4 should be clean after G2 exited: %v", h.test("set-0/T4"))
		}
	})

	t.Run("B2 pre-existing worker pool job detected via quiescent START baseline", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5))
		idleWorker := &goroutineInfo{
			id: 14, baseStatus: "chan receive", topLine: "/app/worker.go:20",
			frames: []string{"main.poolWorker"}, framePkgs: []string{"main"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		busyWorker := &goroutineInfo{
			id: 14, baseStatus: "sleep", topLine: "/usr/local/go/src/runtime/time.go:200",
			frames: []string{"time.Sleep", "main.processJob", "main.poolWorker"}, framePkgs: []string{"time", "main", "main"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 14: idleWorker}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1 dispatches a job to pre-existing worker 14; still busy at END T1.
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 14: busyWorker}}
		h.do("END set-0/T1", replyTaken)
		t1 := h.test("set-0/T1")
		if t1["windowCut"] != true {
			t.Fatalf("B2 failed: T1 windowCut = %v, want true for busy pool worker", t1["windowCut"])
		}
		if !reflect.DeepEqual(t1["lingeringGoroutines"], []interface{}{"main.processJob"}) {
			t.Fatalf("T1 lingeringGoroutines = %v, want [main.processJob]", t1["lingeringGoroutines"])
		}

		// T2 runs while worker 14 is still busy, then worker returns to idle during T2.
		h.do("START set-0/T2", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 14: idleWorker}}
		h.do("END set-0/T2", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// T3 is clean once worker 14 returned to idle!
		h.run("set-0/T3")
		if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
			t.Fatalf("T3 should be clean after pool worker returned to idle: %v", h.test("set-0/T3"))
		}
	})

	t.Run("stack truncation at END resolves hidden goroutine on next complete snapshot", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5), none(), lines(appFile, 6))
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartUnasked)
		// END T1 is truncated and hides goroutine 500!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}, truncated: true}
		h.do("END set-0/T1", replyTaken)
		if h.test("set-0/T1")["windowCut"] != true {
			t.Fatalf("truncated T1 windowCut = %v, want true", h.test("set-0/T1")["windowCut"])
		}

		// At START T2, truncation cleared up and hidden goroutine 500 is now visible!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1:   {id: 1},
			500: {id: 500, baseStatus: "sleep", frames: []string{"main.hiddenLong"}, framePkgs: []string{"main"}, createdByFn: "main.handle", createdByPkg: "main"},
		}}
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// At START T3, goroutine 500 is STILL alive -> T3 MUST STILL be contaminated!
		h.do("START set-0/T3", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		h.do("END set-0/T3", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T3")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("SF1 failed: T3 contaminated = %v, want [tail:set-0/T1] for goroutine hidden by truncated END T1", h.test("set-0/T3")["contaminated"])
		}

		// After goroutine 500 exits, T4 is clean!
		h.run("set-0/T4")
		if _, hasCont := h.test("set-0/T4")["contaminated"]; hasCont {
			t.Fatalf("T4 should be clean after hidden goroutine exited: %v", h.test("set-0/T4"))
		}
	})

	t.Run("truncated START fails closed without poisoning baseline", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3))
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}, truncated: true}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		h.do("END set-0/T1", replyTaken)
		t1 := h.test("set-0/T1")
		if t1["windowCut"] != true {
			t.Fatalf("truncated-START T1 windowCut = %v, want true", t1["windowCut"])
		}
		if !reflect.DeepEqual(t1["contaminated"], []interface{}{"stack-truncated"}) {
			t.Fatalf("truncated-START T1 contaminated = %v, want [stack-truncated]", t1["contaminated"])
		}
	})

	t.Run("lingeringTails overflow preserves live gids and mergeContaminated emits single +N", func(t *testing.T) {
		resetGaps(t)
		alive := map[uint64]*goroutineInfo{1: {id: 1}}
		for i := 0; i < 80; i++ {
			gid := uint64(100 + i)
			alive[gid] = &goroutineInfo{id: gid}
			addLingeringTail(fmt.Sprintf("set-0/T%d", i), []lingeringTarget{{gid: gid, mustExit: true}}, nil)
		}
		if len(lingeringTails) != maxLingeringTails {
			t.Fatalf("len(lingeringTails) = %d, want capped at %d", len(lingeringTails), maxLingeringTails)
		}
		// Verify earliest goroutine 100 was preserved in the "overflow" entry!
		found100 := false
		for _, lt := range lingeringTails {
			if lt.testID == "overflow" {
				for _, tgt := range lt.targets {
					if tgt.gid == 100 {
						found100 = true
					}
				}
			}
		}
		if !found100 {
			t.Fatal("oldest live goroutine 100 was dropped instead of merged into overflow catch-all")
		}
		startMarks := collectStartContamination("", goroutineSnapshot{all: alive}, true, nil, nil, nil, nil)
		gapMarks := []string{"between-tests:a.go", "between-tests:b.go", "+4"}
		merged := mergeContaminated(startMarks, gapMarks)
		plusCount := 0
		for _, m := range merged {
			if strings.HasPrefix(m, "+") {
				plusCount++
			}
		}
		if plusCount != 1 || len(merged) != maxPartialNames+1 {
			t.Fatalf("merged = %v, want exactly 8 entries + single +N summary", merged)
		}
	})

	t.Run("N1 adoption excludes pre-existing children, infra persistConn loops, and ignored goroutines", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		t.Setenv(envIgnoreGoroutines, "lazyIgnored")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5))
		meta = &metaFile{
			mode:        ctrModeAtomic,
			granularity: ctrGranularityPerBlk,
			pkgs:        []metaPkg{{path: "example.com/app"}},
		}
		// Pre-existing keep-alive conn (55) previously spawned a sync.Once janitor (20)
		// BEFORE START T1.
		connIdle := &goroutineInfo{
			id: 55, baseStatus: "IO wait", topLine: "/go/net/http/server.go:100",
			frames: []string{"net/http.(*conn).serve"}, framePkgs: []string{"net/http"},
			createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
		}
		connBusy := &goroutineInfo{
			id: 55, baseStatus: "sleep", topLine: "/app/handler.go:40",
			frames: []string{"main.slowFlush", "net/http.(*conn).serve"}, framePkgs: []string{"main", "net/http"},
			createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
		}
		preExistingJanitor := &goroutineInfo{
			id: 20, baseStatus: "sleep", topLine: "/app/cache.go:10",
			frames: []string{"main.cacheJanitor"}, framePkgs: []string{"main"},
			createdByFn: "main.initCache", createdByPkg: "main", createdByGoid: 55,
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 55: connIdle, 20: preExistingJanitor}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1: conn 55 stays inside main.slowFlush past END_WAIT -> cut!
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 55: connBusy, 20: preExistingJanitor}}
		h.do("END set-0/T1", replyTaken)
		if h.test("set-0/T1")["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", h.test("set-0/T1")["windowCut"])
		}
		// T1 itself carries contaminated:["window-cut"] for older v2 collector safety!
		if !reflect.DeepEqual(h.test("set-0/T1")["contaminated"], []interface{}{"window-cut"}) {
			t.Fatalf("N4: T1 contaminated = %v, want [window-cut]", h.test("set-0/T1")["contaminated"])
		}

		// Before START T2: conn 55 returns to idle, and two non-adoptable children
		// exist with createdByGoid=55:
		//  - preExistingJanitor (gid 20, existed before T1 cut!)
		//  - persistConn readLoop (gid 200, stdlib net/http infra!)
		//  - lazyIgnored (gid 201, matched by KEPLOY_GO_IGNORE_GOROUTINES!)
		persistLoop := &goroutineInfo{
			id: 200, baseStatus: "IO wait", topLine: "/go/net/http/transport.go:200",
			frames: []string{"net/http.(*persistConn).readLoop"}, framePkgs: []string{"net/http"},
			createdByFn: "net/http.(*Transport).dialConn", createdByPkg: "net/http", createdByGoid: 55,
		}
		ignoredChild := &goroutineInfo{
			id: 201, baseStatus: "sleep", topLine: "/app/metrics.go:5",
			frames: []string{"main.lazyIgnored"}, framePkgs: []string{"main"},
			createdByFn: "main.slowFlush", createdByPkg: "main", createdByGoid: 55,
		}
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1: {id: 1}, 55: connIdle, 20: preExistingJanitor, 200: persistLoop, 201: ignoredChild,
		}}
		h.run("set-0/T2")
		// Immediate T2 gets tail:T1 (boundary guarantee), and conn 55 + all 3 children clear!
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// T3 MUST be 100% clean (none of gid 20, 200, 201 were falsely adopted!).
		h.run("set-0/T3")
		if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
			t.Fatalf("N1 failed: T3 falsely contaminated by non-adoptable child: %v", h.test("set-0/T3"))
		}
	})

	t.Run("N2 new keep-alive conn.serve clears once handler returns instead of sticking as must-exit", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5))
		meta = &metaFile{
			mode:        ctrModeAtomic,
			granularity: ctrGranularityPerBlk,
			pkgs:        []metaPkg{{path: "example.com/app"}},
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1 opens a NEW keep-alive conn (gid 88, created by net/http.(*Server).Serve)
		// whose handler is still running main.slowFlush at END T1.
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1: {id: 1},
			88: {
				id: 88, baseStatus: "sleep", topLine: "/app/handler.go:40",
				frames: []string{"main.slowFlush", "net/http.(*conn).serve"}, framePkgs: []string{"main", "net/http"},
				createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
			},
		}}
		h.do("END set-0/T1", replyTaken)
		if h.test("set-0/T1")["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", h.test("set-0/T1")["windowCut"])
		}

		// Handler finishes; conn 88 stays alive idle on keep-alive (NO app frames!).
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1: {id: 1},
			88: {
				id: 88, baseStatus: "IO wait", topLine: "/go/net/http/server.go:100",
				frames: []string{"net/http.(*conn).readRequest", "net/http.(*conn).serve"}, framePkgs: []string{"net/http", "net/http"},
				createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
			},
		}}
		h.run("set-0/T2")
		h.run("set-0/T3")
		if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
			t.Fatalf("N2 failed: idle keep-alive conn 88 stuck as must-exit on T3: %v", h.test("set-0/T3"))
		}
	})

	t.Run("N3 outboxPoller parked deeper in deliver is NOT re-baselined and stays tail:T1", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5), none(), lines(appFile, 6))
		idle := &goroutineInfo{
			id: 15, baseStatus: "sleep", topLine: "/go/src/runtime/time.go:300",
			frames: []string{"time.Sleep", "main.outboxPoller"}, framePkgs: []string{"time", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		parked := &goroutineInfo{
			id: 15, baseStatus: "select", topLine: "/go/src/net/http/transport.go:2700",
			frames:      []string{"net/http.(*persistConn).roundTrip", "net/http.(*Transport).roundTrip", "main.deliver", "main.outboxPoller"},
			framePkgs:   []string{"net/http", "net/http", "main", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: idle}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// START T1 (idle) -> END T1 (parked in deliver, appFrameCount 2 > 1): windowCut!
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: parked}}
		h.do("END set-0/T1", replyTaken)
		if h.test("set-0/T1")["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", h.test("set-0/T1")["windowCut"])
		}

		// T2 (still parked): tail:T1!
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// T3 (STILL parked in deeper main.deliver -> MUST NOT be re-baselined!): tail:T1!
		h.do("START set-0/T3", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: idle}}
		h.do("END set-0/T3", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T3")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("outboxPoller regression: T3 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T3")["contaminated"])
		}

		// T4 (back to idle): clean!
		h.run("set-0/T4")
		if _, hasCont := h.test("set-0/T4")["contaminated"]; hasCont {
			t.Fatalf("T4 should be clean after outboxPoller returned to idle: %v", h.test("set-0/T4"))
		}
	})

	t.Run("N3 same-depth consumer moves to one-time resume watch and catches wakeup", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5), none(), lines(appFile, 6), none(), lines(appFile, 7))
		backoff := &goroutineInfo{
			id: 15, baseStatus: "sleep", topLine: "/app/consumer.go:12",
			frames: []string{"time.Sleep", "main.consumerLoop"}, framePkgs: []string{"time", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		steady := &goroutineInfo{
			id: 15, baseStatus: "IO wait", topLine: "/app/consumer.go:25",
			frames: []string{"internal/poll.runtime_pollWait", "main.consumerLoop"}, framePkgs: []string{"internal/poll", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		resumed := &goroutineInfo{
			id: 15, baseStatus: "sleep", topLine: "/app/consumer.go:40",
			frames: []string{"time.Sleep", "main.handleMsg", "main.consumerLoop"}, framePkgs: []string{"time", "main", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: backoff}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1 starts in backoff Sleep; consumer connects into steady IO wait (same appFrameCount=1).
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady}}
		h.do("END set-0/T1", replyTaken)
		if h.test("set-0/T1")["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", h.test("set-0/T1")["windowCut"])
		}

		// T2: steady IO wait (streak 1 -> still marked tail:T1).
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// T3: STILL at steady IO wait (streak 2 -> moves onto one-time resume watch; T3 is clean while parked!).
		h.run("set-0/T3")
		if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
			t.Fatalf("N3 failed: steady consumer should be clean while parked on resume watch, got %v", h.test("set-0/T3"))
		}

		// T4: consumer leaves steady IO wait (resumes inside T4!) -> resume watch MUST fire on T4!
		h.do("START set-0/T4", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: resumed}}
		h.do("END set-0/T4", replyTaken)
		t4 := h.test("set-0/T4")
		if !reflect.DeepEqual(t4["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("resume watch failed: T4 contaminated = %v, want [tail:set-0/T1]", t4["contaminated"])
		}
		if t4["windowCut"] != true {
			t.Fatalf("T4 should also be windowCut while resumed work is active: %v", t4)
		}
	})

	t.Run("S1+S2 resume watch waking in gap marks gap afterCut and keeps runnable goroutine tracked across T4 and T5", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 3), none(), // T1, gap1->2
			lines(appFile, 4), none(), // T2, gap2->3
			lines(appFile, 5), lines(workerFile, 9), // T3, gap3->4 (watch wakes here!)
			lines(appFile, 6), none(), // T4 (goroutine runnable at START T4!), gap4->5
			lines(appFile, 7), none(), // T5 (goroutine still active at START T5, returns to idle during T5!)
			lines(appFile, 8), // T6 (clean!)
		)
		h.ask("r1")
		backoff := &goroutineInfo{
			id: 15, baseStatus: "select", topLine: "/app/consumer.go:12",
			frames: []string{"main.consumerLoop"}, framePkgs: []string{"main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		steady := &goroutineInfo{
			id: 15, baseStatus: "IO wait", topLine: "/app/consumer.go:25",
			frames: []string{"internal/poll.runtime_pollWait", "main.consumerLoop"}, framePkgs: []string{"internal/poll", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		runnableMidJob := &goroutineInfo{
			id: 15, baseStatus: "runnable", topLine: "/app/consumer.go:40",
			frames: []string{"main.handleMsg", "main.consumerLoop"}, framePkgs: []string{"main", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: backoff}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1: backoff -> steady (windowCut).
		h.do("START set-0/T1", replyStartAskedBy("r1"))
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady}}
		h.do("END set-0/T1", replyTaken)
		// T2: streak 1 (tail:T1).
		h.run("set-0/T2")
		// T3: streak 2 -> moves onto resume watch (clean while parked).
		h.run("set-0/T3")
		if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
			t.Fatalf("T3 should be clean on resume watch: %v", h.test("set-0/T3"))
		}

		// In gap T3->T4, goroutine 15 wakes into runnableMidJob (non-quiescent!).
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: runnableMidJob}}
		h.run("set-0/T4")
		// S1 check: gap T3->T4 MUST be emitted with afterCut:true and empty lines, and NOT added to everGap!
		bgs := h.backgrounds()
		if len(bgs) != 1 || bgs[0]["afterCut"] != true {
			t.Fatalf("S1 failed: gap T3->T4 background = %v, want 1 record with afterCut:true", bgs)
		}
		if ls, _ := bgs[0]["executedLinesByFile"].(map[string]interface{}); len(ls) != 0 {
			t.Fatalf("S1 failed: afterCut gap carried lines %v, want empty {}", ls)
		}
		if _, polluted := everGap[workerFile]; polluted {
			t.Fatalf("S1 failed: gap wake polluted everGap: %v", everGap)
		}
		if !reflect.DeepEqual(h.test("set-0/T4")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T4 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T4")["contaminated"])
		}

		// S2 check: goroutine 15 is STILL runnableMidJob at START T5 (was non-quiescent at START T4),
		// so re-attached target MUST keep T5 contaminated tail:T1! Then returns to backoff (original idleSig) during T5.
		h.do("START set-0/T5", replyStartAskedBy("r1"))
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: backoff}}
		h.do("END set-0/T5", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T5")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("S2 failed: T5 contaminated = %v, want [tail:set-0/T1] via re-attached target", h.test("set-0/T5")["contaminated"])
		}

		// T6: back at original idle -> 100% clean!
		h.run("set-0/T6")
		if _, hasCont := h.test("set-0/T6")["contaminated"]; hasCont {
			t.Fatalf("T6 should be clean after returning to original idle: %v", h.test("set-0/T6"))
		}
	})

	t.Run("R5-A post-wake child of watched parent is adopted and stays tracked", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 3), none(), // T1, gap1->2
			lines(appFile, 4), none(), // T2, gap2->3
			lines(appFile, 5), none(), // T3, gap3->4
			lines(appFile, 6), none(), // T4 (post-wake child H alive!)
			lines(appFile, 7), // T5 (child H STILL alive -> MUST stay tail:T1!)
		)
		h.ask("r1")
		backoff := &goroutineInfo{
			id: 15, baseStatus: "select", topLine: "/app/consumer.go:12",
			frames: []string{"main.consumerLoop"}, framePkgs: []string{"main"}, frameLines: []string{"/app/consumer.go:12"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		steady := &goroutineInfo{
			id: 15, baseStatus: "IO wait", topLine: "/app/consumer.go:25",
			frames: []string{"internal/poll.runtime_pollWait", "main.consumerLoop"}, framePkgs: []string{"internal/poll", "main"}, frameLines: []string{"/go/netpoll.go:1", "/app/consumer.go:25"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		childH := &goroutineInfo{
			id: 200, baseStatus: "sleep", topLine: "/app/handler.go:50",
			frames: []string{"main.handleMsg"}, framePkgs: []string{"main"}, frameLines: []string{"/app/handler.go:50"},
			createdByFn: "main.consumerLoop", createdByPkg: "main", createdByGoid: 15,
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: backoff}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1 -> T2 -> T3 moves gid 15 onto resume watch.
		h.do("START set-0/T1", replyStartAskedBy("r1"))
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady}}
		h.do("END set-0/T1", replyTaken)
		h.run("set-0/T2")
		h.run("set-0/T3")

		// Gid 15 spawns post-wake child H (gid 200) and returns to steady!
		// Watch MUST fire on child H, re-attach with preserved preExistingChildren
		// (NOT swallowing 200), and adopt child H so T4 AND T5 stay tail:T1!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady, 200: childH}}
		h.run("set-0/T4")
		if !reflect.DeepEqual(h.test("set-0/T4")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("R5-A failed: T4 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T4")["contaminated"])
		}
		h.run("set-0/T5")
		if !reflect.DeepEqual(h.test("set-0/T5")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("R5-A failed: T5 contaminated = %v, want [tail:set-0/T1] via adopted post-wake child H", h.test("set-0/T5")["contaminated"])
		}
	})

	t.Run("R5-B truncated START with armed watch marks gap afterCut and R5-C caps 9 marks", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 3), none(), // T1, gap1->2
			lines(appFile, 4), none(), // T2, gap2->3
			lines(appFile, 5), lines(workerFile, 9), // T3, gap3->4 (truncated START with armed watch!)
			lines(appFile, 6), // T4
		)
		h.ask("r1")
		backoff := &goroutineInfo{
			id: 15, baseStatus: "select", topLine: "/app/consumer.go:12",
			frames: []string{"main.consumerLoop"}, framePkgs: []string{"main"}, frameLines: []string{"/app/consumer.go:12"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		steady := &goroutineInfo{
			id: 15, baseStatus: "IO wait", topLine: "/app/consumer.go:25",
			frames: []string{"internal/poll.runtime_pollWait", "main.consumerLoop"}, framePkgs: []string{"internal/poll", "main"}, frameLines: []string{"/go/netpoll.go:1", "/app/consumer.go:25"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: backoff}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartAskedBy("r1"))
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady}}
		h.do("END set-0/T1", replyTaken)
		h.run("set-0/T2")
		h.run("set-0/T3")

		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady}, truncated: true}
		h.run("set-0/T4")
		bgs := h.backgrounds()
		if len(bgs) != 1 || bgs[0]["afterCut"] != true {
			t.Fatalf("R5-B failed: truncated START with armed watch emitted background %v, want afterCut:true", bgs)
		}
		if ls, _ := bgs[0]["executedLinesByFile"].(map[string]interface{}); len(ls) != 0 {
			t.Fatalf("R5-B failed: afterCut gap carried lines %v, want empty {}", ls)
		}

		// R5-C: mergeContaminated caps >maxPartialNames even when startMarks is empty.
		nine := []string{"tail:T1", "tail:T2", "tail:T3", "tail:T4", "tail:T5", "tail:T6", "tail:T7", "tail:T8", "tail:T9"}
		got := mergeContaminated(nil, nine)
		if len(got) != maxPartialNames+1 || got[maxPartialNames] != "+1" {
			t.Fatalf("R5-C failed: mergeContaminated(nil, 9 items) = %v, want 8 items + +1", got)
		}
	})

	t.Run("R6 co-target pre-existing parent and child moving onto watches stay clean", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 3), none(), // T1
			lines(appFile, 4), none(), // T2
			lines(appFile, 5), none(), // T3 (both on watches -> MUST be clean!)
			lines(appFile, 6), // T4 (MUST stay clean, no spurious watch fire or mustExit adoption!)
		)
		mgrBackoff := &goroutineInfo{
			id: 15, baseStatus: "select", topLine: "/app/mgr.go:10",
			frames: []string{"main.mgrRun"}, framePkgs: []string{"main"}, frameLines: []string{"/app/mgr.go:10"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		rdrBackoff := &goroutineInfo{
			id: 16, baseStatus: "select", topLine: "/app/mgr.go:30",
			frames: []string{"main.readerRun"}, framePkgs: []string{"main"}, frameLines: []string{"/app/mgr.go:30"},
			createdByFn: "main.mgrRun", createdByPkg: "main", createdByGoid: 15,
		}
		mgrSteady := &goroutineInfo{
			id: 15, baseStatus: "chan receive", topLine: "/app/mgr.go:18",
			frames: []string{"main.mgrRun"}, framePkgs: []string{"main"}, frameLines: []string{"/app/mgr.go:18"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		rdrSteady := &goroutineInfo{
			id: 16, baseStatus: "chan receive", topLine: "/app/mgr.go:38",
			frames: []string{"main.readerRun"}, framePkgs: []string{"main"}, frameLines: []string{"/app/mgr.go:38"},
			createdByFn: "main.mgrRun", createdByPkg: "main", createdByGoid: 15,
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: mgrBackoff, 16: rdrBackoff}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1: both move from backoff to steady -> cut with co-targets {15, 16}.
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: mgrSteady, 16: rdrSteady}}
		h.do("END set-0/T1", replyTaken)
		if h.test("set-0/T1")["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", h.test("set-0/T1")["windowCut"])
		}

		// T2: streak 1 -> tail:T1.
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// T3: both reach streak 2 and move onto watches -> T3 MUST be clean!
		h.run("set-0/T3")
		if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
			t.Fatalf("R6 blocker failed: co-target child triggered spurious watch fire on T3: %v", h.test("set-0/T3"))
		}

		// T4: MUST stay clean!
		h.run("set-0/T4")
		if _, hasCont := h.test("set-0/T4")["contaminated"]; hasCont {
			t.Fatalf("R6 blocker failed: co-target child stuck permanently on T4: %v", h.test("set-0/T4"))
		}
	})

	t.Run("R6 co-target child returning to idle before parent is not falsely adopted as mustExit", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 3), none(), // T1
			lines(appFile, 4), none(), // T2 (child returns to idle first; parent still busy)
			lines(appFile, 5), none(), // T3 (parent returns to idle)
			lines(appFile, 6), // T4 (MUST be clean!)
		)
		coordIdle := &goroutineInfo{
			id: 15, baseStatus: "chan receive", topLine: "/app/pool.go:10",
			frames: []string{"main.coord"}, framePkgs: []string{"main"}, frameLines: []string{"/app/pool.go:10"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		workerIdle := &goroutineInfo{
			id: 16, baseStatus: "chan receive", topLine: "/app/pool.go:30",
			frames: []string{"main.worker"}, framePkgs: []string{"main"}, frameLines: []string{"/app/pool.go:30"},
			createdByFn: "main.coord", createdByPkg: "main", createdByGoid: 15,
		}
		coordBusy := &goroutineInfo{
			id: 15, baseStatus: "sleep", topLine: "/go/time.go:1",
			frames: []string{"time.Sleep", "main.coordWait", "main.coord"}, framePkgs: []string{"time", "main", "main"}, frameLines: []string{"/go/time.go:1", "/app/pool.go:20", "/app/pool.go:12"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		workerBusy := &goroutineInfo{
			id: 16, baseStatus: "sleep", topLine: "/go/time.go:1",
			frames: []string{"time.Sleep", "main.workerJob", "main.worker"}, framePkgs: []string{"time", "main", "main"}, frameLines: []string{"/go/time.go:1", "/app/pool.go:40", "/app/pool.go:32"},
			createdByFn: "main.coord", createdByPkg: "main", createdByGoid: 15,
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: coordIdle, 16: workerIdle}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: coordBusy, 16: workerBusy}}
		h.do("END set-0/T1", replyTaken)

		// Worker 16 returns to idle while coord 15 stays busy across T2.
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: coordBusy, 16: workerIdle}}
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// Coord 15 returns to idle during T3.
		h.do("START set-0/T3", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: coordIdle, 16: workerIdle}}
		h.do("END set-0/T3", replyTaken)

		// T4 MUST be clean (worker 16 was NOT falsely adopted as mustExit!).
		h.run("set-0/T4")
		if _, hasCont := h.test("set-0/T4")["contaminated"]; hasCont {
			t.Fatalf("R6 variant failed: co-target worker 16 was falsely adopted as mustExit on T4: %v", h.test("set-0/T4"))
		}
	})

	t.Run("R6 app-frame lines distinguish two sleeps in the same app function", func(t *testing.T) {
		covered := map[string]bool{"main": true}
		g1 := &goroutineInfo{
			id: 30, baseStatus: "sleep", topLine: "/go/src/runtime/time.go:200",
			frames:     []string{"time.Sleep", "main.poller"},
			framePkgs:  []string{"time", "main"},
			frameLines: []string{"/go/src/runtime/time.go:200", "/app/poller.go:15"},
		}
		g2 := &goroutineInfo{
			id: 30, baseStatus: "sleep", topLine: "/go/src/runtime/time.go:200",
			frames:     []string{"time.Sleep", "main.poller"},
			framePkgs:  []string{"time", "main"},
			frameLines: []string{"/go/src/runtime/time.go:200", "/app/poller.go:42"},
		}
		s1 := goroutineStateSig(g1, 0, covered, nil)
		s2 := goroutineStateSig(g2, 0, covered, nil)
		if s1 == s2 {
			t.Fatalf("two distinct app-frame sleep lines produced identical goroutineStateSig: %+v", s1)
		}
	})

	t.Run("R6 clearCounters failure preserves all fired watches, END dedups gid, overflow honors cap", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4))
		// Arm two resume watches for T1 and T2.
		g10 := &goroutineInfo{id: 10, baseStatus: "select", frames: []string{"main.a"}, framePkgs: []string{"main"}, frameLines: []string{"/app/a.go:1"}}
		g11 := &goroutineInfo{id: 11, baseStatus: "select", frames: []string{"main.b"}, framePkgs: []string{"main"}, frameLines: []string{"/app/b.go:1"}}
		resumeWatches[10] = resumeWatch{testID: "set-0/W1", target: lingeringTarget{gid: 10}, watchSig: goroutineStateSig(g10, 0, nil, nil)}
		resumeWatches[11] = resumeWatch{testID: "set-0/W2", target: lingeringTarget{gid: 11}, watchSig: goroutineStateSig(g11, 0, nil, nil)}

		// Both exit before START T3, AND clearCounters fails on first START T3!
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }
		failNext := true
		clearCounters = func() error {
			if failNext {
				failNext = false
				return fmt.Errorf("reset failed")
			}
			return nil
		}
		h.do("START set-0/T3", replyIgnored)
		// Next successful START T3 MUST carry BOTH tail:set-0/W1 and tail:set-0/W2!
		h.run("set-0/T3")
		if !reflect.DeepEqual(h.test("set-0/T3")["contaminated"], []interface{}{"tail:set-0/W1", "tail:set-0/W2"}) {
			t.Fatalf("clearCounters failure lost fired watches: got %v, want [tail:set-0/W1 tail:set-0/W2]", h.test("set-0/T3")["contaminated"])
		}

		// END dedup: watch fires at END T4 for gid 20; gid 20 must appear in ONLY ONE lingering tail.
		g20Park := &goroutineInfo{id: 20, baseStatus: "chan receive", frames: []string{"main.c"}, framePkgs: []string{"main"}, frameLines: []string{"/app/c.go:10"}}
		g20Busy := &goroutineInfo{id: 20, baseStatus: "sleep", frames: []string{"time.Sleep", "main.cWork", "main.c"}, framePkgs: []string{"time", "main", "main"}, frameLines: []string{"/go/t.go:1", "/app/c.go:20", "/app/c.go:12"}}
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 20: g20Park}}
		resumeWatches[20] = resumeWatch{testID: "set-0/W3", target: lingeringTarget{gid: 20, idleSig: goroutineSig{active: true, status: "select", appFrameCount: 1, hash: 123}}, watchSig: goroutineStateSig(g20Park, 0, nil, nil)}
		h.do("START set-0/T4", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 20: g20Busy}}
		h.do("END set-0/T4", replyTaken)
		count20 := 0
		for _, lt := range lingeringTails {
			for _, tgt := range lt.targets {
				if tgt.gid == 20 {
					count20++
				}
			}
		}
		if count20 != 1 {
			t.Fatalf("END dedup failed: gid 20 present in %d tails, want 1", count20)
		}

		// Overflow same-testID merge honors maxOverflowTailGids.
		resetGoroutineState()
		var batch1, batch2 []lingeringTarget
		for i := 0; i < maxOverflowTailGids; i++ {
			batch1 = append(batch1, lingeringTarget{gid: uint64(1000 + i), mustExit: true})
			batch2 = append(batch2, lingeringTarget{gid: uint64(2000 + i), mustExit: true})
		}
		addLingeringTail("overflow", batch1, nil)
		addLingeringTail("overflow", batch2, nil)
		if len(lingeringTails) != 1 || len(lingeringTails[0].targets) != maxOverflowTailGids {
			t.Fatalf("overflow same-testID merge exceeded cap: len=%d, want %d", len(lingeringTails[0].targets), maxOverflowTailGids)
		}
	})

	t.Run("N3 mid-job worker at START is not permanently whitelisted for later tests", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4))
		idle := &goroutineInfo{
			id: 16, baseStatus: "chan receive", topLine: "/app/worker.go:10",
			frames: []string{"main.worker"}, framePkgs: []string{"main"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		deliver := &goroutineInfo{
			id: 16, baseStatus: "select", topLine: "/go/src/net/http/transport.go:2700",
			frames:      []string{"net/http.(*persistConn).roundTrip", "main.deliver", "main.worker"},
			framePkgs:   []string{"net/http", "main", "main"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		// T1 starts AND ends while worker 16 happens to be parked in deliver
		// (background/startup work), then finishes in the gap before T2.
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 16: deliver}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }
		h.run("set-0/T1")

		// Before T2 starts, worker 16 returned to idle. During T2 it dispatches
		// a job and is parked in deliver at END T2. deliver must NOT have been
		// whitelisted as idle by T1's START!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 16: idle}}
		h.do("START set-0/T2", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 16: deliver}}
		h.do("END set-0/T2", replyTaken)
		if h.test("set-0/T2")["windowCut"] != true {
			t.Fatalf("T2 missed worker parked in deliver because T1 START poisoned idle history: %v", h.test("set-0/T2"))
		}
	})

	t.Run("N4 windowCut omits overlapLinesByFile and carries window-cut mark for v2 collectors", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, map[string][]int{appFile: {3}, workerFile: {7}})
		everGap[workerFile] = map[int]struct{}{7: {}}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1:  {id: 1},
			99: {id: 99, baseStatus: "sleep", frames: []string{"main.fraudAlert"}, framePkgs: []string{"main"}, createdByFn: "main.handle", createdByPkg: "main"},
		}}
		h.do("END set-0/T1", replyTaken)
		t1 := h.test("set-0/T1")
		if t1["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", t1["windowCut"])
		}
		if _, hasOverlap := t1["overlapLinesByFile"]; hasOverlap {
			t.Fatalf("N4 failed: windowCut test T1 must omit overlapLinesByFile: %v", t1)
		}
		if !reflect.DeepEqual(t1["contaminated"], []interface{}{"window-cut", "between-tests:" + workerFile}) {
			t.Fatalf("N4 failed: T1 contaminated = %v, want [window-cut between-tests:%s]", t1["contaminated"], workerFile)
		}
	})

	t.Run("N5 truncated START stays fail-closed past T+1 until hidden goroutine exits", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5), none(), lines(appFile, 6), none(), lines(appFile, 7))
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T0 establishes lastCompleteGids = {1}.
		h.run("set-0/T0")

		// START T1 is truncated, and T1 spawns goroutine 777!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}, truncated: true}
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1:   {id: 1},
			777: {id: 777, baseStatus: "sleep", frames: []string{"main.hiddenFromTruncStart"}, framePkgs: []string{"main"}, createdByFn: "main.handle", createdByPkg: "main"},
		}}
		h.do("END set-0/T1", replyTaken)

		// T2 starts with complete snapshot -> resolves 777 into T1's tail!
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// T3 starts while 777 is STILL alive -> MUST still be contaminated!
		h.do("START set-0/T3", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		h.do("END set-0/T3", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T3")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("N5 failed: T3 contaminated = %v, want [tail:set-0/T1] while 777 is alive", h.test("set-0/T3")["contaminated"])
		}

		// T4 is clean after 777 exited!
		h.run("set-0/T4")
		if _, hasCont := h.test("set-0/T4")["contaminated"]; hasCont {
			t.Fatalf("T4 should be clean after 777 exited: %v", h.test("set-0/T4"))
		}
	})

	t.Run("maybeShrinkStackBuf releases oversized transient buffer", func(t *testing.T) {
		old := stackBuf
		t.Cleanup(func() { stackBuf = old })
		stackBuf = make([]byte, 4<<20)
		maybeShrinkStackBuf(16 << 10)
		if len(stackBuf) >= stackShrinkThreshold {
			t.Fatalf("stackBuf stayed at %d bytes after small snapshot, want shrunk below %d", len(stackBuf), stackShrinkThreshold)
		}
	})

	t.Run("afterCut BACKGROUND wire bytes", func(t *testing.T) {
		resetGaps(t)
		clean, err := encodeBackgroundWithMeta("set-0/T1", "set-0/T2", map[string][]int{workerFile: {7}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(clean), "afterCut") {
			t.Fatalf("clean background unexpectedly contains afterCut: %s", clean)
		}
		cut, err := encodeBackgroundWithMeta("set-0/T1", "set-0/T2", map[string][]int{}, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(cut), `{"kind":"background","before":"set-0/T2",`) {
			t.Fatalf("afterCut background lost kind/before prefix: %s", cut)
		}
		var dec map[string]interface{}
		if err := json.Unmarshal(cut, &dec); err != nil {
			t.Fatalf("invalid JSON: %v (%s)", err, cut)
		}
		if dec["afterCut"] != true {
			t.Fatalf("afterCut = %v, want true (%s)", dec["afterCut"], cut)
		}
		if ls, _ := dec["executedLinesByFile"].(map[string]interface{}); len(ls) != 0 {
			t.Fatalf("afterCut executedLinesByFile = %v, want empty {}", ls)
		}
	})

	t.Run("SF1 pre-existing syscall, sync.Mutex.Lock, chan send, coroutine, and runnable baselines", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		for _, status := range []string{"syscall", "sync.Mutex.Lock", "sync.RWMutex.RLock", "sync.WaitGroup.Wait", "chan send", "coroutine"} {
			t.Run(status, func(t *testing.T) {
				h := newGapHarness(t, lines(appFile, 3), lines(workerFile, 9), lines(appFile, 4), none(), lines(appFile, 5))
				h.ask("sf1")
				idle := &goroutineInfo{
					id: 30, baseStatus: status, topLine: "/app/consumer.go:10",
					frames: []string{"main.cgoConsumer"}, framePkgs: []string{"main"}, frameLines: []string{"/app/consumer.go:10"},
					createdByFn: "main.init", createdByPkg: "main",
				}
				handling := &goroutineInfo{
					id: 30, baseStatus: "IO wait", topLine: "/app/consumer.go:42",
					frames: []string{"main.handleMsg", "main.cgoConsumer"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/consumer.go:42", "/app/consumer.go:15"},
					createdByFn: "main.init", createdByPkg: "main",
				}
				cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 30: idle}}
				snapshotGoroutines = func() goroutineSnapshot { return cur }

				h.do("START set-0/T1", replyStartAskedBy("sf1"))
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 30: handling}}
				h.do("END set-0/T1", replyTaken)
				if h.test("set-0/T1")["windowCut"] != true {
					t.Fatalf("SF1 failed for status %q: T1 missed active pre-existing consumer: %v", status, h.test("set-0/T1"))
				}

				// T2 starts while still handling -> gap T1->T2 must be afterCut and T2 contaminated tail:T1.
				h.do("START set-0/T2", replyStartAskedBy("sf1"))
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 30: idle}}
				h.do("END set-0/T2", replyTaken)
				if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
					t.Fatalf("SF1 failed for status %q: T2 contaminated = %v, want [tail:set-0/T1]", status, h.test("set-0/T2")["contaminated"])
				}
				if _, polluted := everGap[workerFile]; polluted {
					t.Fatalf("SF1 failed for status %q: gap polluted everGap", status)
				}

				// Back at idle -> T3 is clean!
				h.run("set-0/T3")
				if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
					t.Fatalf("SF1 failed for status %q: T3 should be clean after returning to idle: %v", status, h.test("set-0/T3"))
				}
			})
		}

		t.Run("runnable at START falls back to prior quiescent baseline", func(t *testing.T) {
			h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5), none(), lines(appFile, 6))
			idle := &goroutineInfo{
				id: 31, baseStatus: "chan receive", topLine: "/app/worker.go:10",
				frames: []string{"main.worker"}, framePkgs: []string{"main"}, frameLines: []string{"/app/worker.go:10"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			running := &goroutineInfo{
				id: 31, baseStatus: "runnable", topLine: "/app/worker.go:20",
				frames: []string{"main.handleMsg", "main.worker"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/worker.go:20", "/app/worker.go:12"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			handlingWait := &goroutineInfo{
				id: 31, baseStatus: "IO wait", topLine: "/app/worker.go:25",
				frames: []string{"main.handleMsg", "main.worker"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/worker.go:25", "/app/worker.go:12"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			// T0 establishes lastQuiescentSigs[31] = idle.
			cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 31: idle}}
			snapshotGoroutines = func() goroutineSnapshot { return cur }
			h.run("set-0/T0")

			// START T1 catches goroutine 31 in [runnable]; at END T1 it is blocked in handleMsg [IO wait].
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 31: running}}
			h.do("START set-0/T1", replyStartUnasked)
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 31: handlingWait}}
			h.do("END set-0/T1", replyTaken)
			if h.test("set-0/T1")["windowCut"] != true {
				t.Fatalf("SF1 runnable fallback failed: T1 missed worker that was runnable at START: %v", h.test("set-0/T1"))
			}

			// Returns to T0's idle baseline during T2 -> T3 is clean.
			h.do("START set-0/T2", replyStartUnasked)
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 31: idle}}
			h.do("END set-0/T2", replyTaken)
			h.run("set-0/T3")
			if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
				t.Fatalf("T3 should be clean after returning to prior idle: %v", h.test("set-0/T3"))
			}
		})

		t.Run("runnable at first START with no prior baseline fails closed when parked mid-job at same or deeper depth at END", func(t *testing.T) {
			h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4))
			runJobA := &goroutineInfo{
				id: 32, baseStatus: "runnable", topLine: "/app/worker.go:20",
				frames: []string{"main.process", "main.worker"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/worker.go:20", "/app/worker.go:12"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			waitJobB := &goroutineInfo{
				id: 32, baseStatus: "IO wait", topLine: "/app/worker.go:30",
				frames: []string{"main.process", "main.worker"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/worker.go:30", "/app/worker.go:12"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 32: runJobA}}
			snapshotGoroutines = func() goroutineSnapshot { return cur }
			h.do("START set-0/T1", replyStartUnasked)
			// S2 regression: same app-frame depth (2) parked in IO wait on jobB must still be caught!
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 32: waitJobB}}
			h.do("END set-0/T1", replyTaken)
			if h.test("set-0/T1")["windowCut"] != true {
				t.Fatalf("S2 failed: T1 missed worker runnable at first START and parked mid-job at same depth at END: %v", h.test("set-0/T1"))
			}
			h.run("set-0/T2")
			if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
				t.Fatalf("S2 failed: T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
			}
		})
	})

	t.Run("SF2 wrapped keep-alive idle reads clear immediately while stuck handlers never whitelist", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 3), none(), // T1: wrapped inbound keep-alive readRequest -> clean immediately!
			lines(appFile, 4), none(), // T2: wrapped outbound persistConn.readLoop -> clean immediately!
			lines(appFile, 5), none(), // T3: slow body read through countingConn under ServeHTTP -> windowCut!
			lines(appFile, 6), none(), // T4 (B1/S1): g50 stuck in slowFlushHandler -> windowCut!
			lines(appFile, 7), none(), // T5: g50 still stuck -> tail:T4 (never moves onto watch!)
			lines(appFile, 8), none(), // T6: g50 still stuck -> tail:T4 (never moves onto watch!)
			lines(appFile, 9), none(), // T7 (B1): new g51 stuck at EXACT same slowFlushHandler line -> windowCut!
			lines(appFile, 10), none(), // T8: g50 finished, g51 still stuck -> MUST still be tail:T7!
			lines(appFile, 11), // T9: g51 returned to readRequest -> clean!
		)
		wrappedInboundIdle := func(id uint64) *goroutineInfo {
			return &goroutineInfo{
				id: id, baseStatus: "IO wait", topLine: "/go/src/internal/poll/fd_poll_runtime.go:84",
				frames: []string{
					"internal/poll.runtime_pollWait",
					"net.(*conn).Read",
					"main.(*countingConn).Read",
					"net/http.(*connReader).Read",
					"bufio.(*Reader).fill",
					"bufio.(*Reader).Peek",
					"net/http.(*conn).serve",
				},
				framePkgs:   []string{"internal/poll", "net", "main", "net/http", "bufio", "bufio", "net/http"},
				frameLines:  []string{"/go/poll.go:84", "/go/net.go:180", "/app/async.go:50", "/go/server.go:856", "/go/bufio.go:113", "/go/bufio.go:152", "/go/server.go:2193"},
				createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
			}
		}
		wrappedOutboundIdle := func(id uint64) *goroutineInfo {
			return &goroutineInfo{
				id: id, baseStatus: "IO wait", topLine: "/go/src/internal/poll/fd_poll_runtime.go:84",
				frames: []string{
					"internal/poll.runtime_pollWait",
					"net.(*conn).Read",
					"main.(*countingConn).Read",
					"bufio.(*Reader).Peek",
					"net/http.(*persistConn).readLoop",
				},
				framePkgs:   []string{"internal/poll", "net", "main", "bufio", "net/http"},
				frameLines:  []string{"/go/poll.go:84", "/go/net.go:180", "/app/async.go:50", "/go/bufio.go:140", "/go/transport.go:2200"},
				createdByFn: "net/http.(*Transport).dialConn", createdByPkg: "net/http",
			}
		}
		slowBodyRead := func(id uint64) *goroutineInfo {
			return &goroutineInfo{
				id: id, baseStatus: "IO wait", topLine: "/go/src/internal/poll/fd_poll_runtime.go:84",
				frames: []string{
					"internal/poll.runtime_pollWait",
					"net.(*conn).Read",
					"main.(*countingConn).Read",
					"net/http.(*connReader).Read",
					"main.slowBodyHandler",
					"net/http.serverHandler.ServeHTTP",
					"net/http.(*conn).serve",
				},
				framePkgs:   []string{"internal/poll", "net", "main", "net/http", "main", "net/http", "net/http"},
				frameLines:  []string{"/go/poll.go:84", "/go/net.go:180", "/app/async.go:50", "/go/server.go:790", "/app/async.go:180", "/go/server.go:2900", "/go/server.go:1990"},
				createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
			}
		}
		slowFlush := func(id uint64) *goroutineInfo {
			return &goroutineInfo{
				id: id, baseStatus: "chan receive", topLine: "/app/async.go:204",
				frames:      []string{"main.slowFlushHandler", "net/http.serverHandler.ServeHTTP", "net/http.(*conn).serve"},
				framePkgs:   []string{"main", "net/http", "net/http"},
				frameLines:  []string{"/app/async.go:204", "/go/server.go:2900", "/go/server.go:1990"},
				createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
			}
		}

		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1: new wrapped inbound keep-alive conn parked in readRequest -> CLEAN immediately!
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: wrappedInboundIdle(40)}}
		h.do("END set-0/T1", replyTaken)
		if _, hasCut := h.test("set-0/T1")["windowCut"]; hasCut {
			t.Fatalf("SF2 failed: T1 got false-positive windowCut on idle wrapped inbound conn: %v", h.test("set-0/T1"))
		}

		// T2: new wrapped outbound keep-alive conn parked in persistConn.readLoop -> CLEAN immediately!
		h.do("START set-0/T2", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: wrappedInboundIdle(40), 41: wrappedOutboundIdle(41)}}
		h.do("END set-0/T2", replyTaken)
		if _, hasCut := h.test("set-0/T2")["windowCut"]; hasCut {
			t.Fatalf("SF2 failed: T2 got false-positive windowCut on idle wrapped outbound conn: %v", h.test("set-0/T2"))
		}

		// T3: slow request-body read via countingConn under ServeHTTP -> MUST be windowCut!
		h.do("START set-0/T3", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: slowBodyRead(40), 41: wrappedOutboundIdle(41)}}
		h.do("END set-0/T3", replyTaken)
		if h.test("set-0/T3")["windowCut"] != true {
			t.Fatalf("SF2 failed: T3 missed active slowBodyHandler reading wrapped conn: %v", h.test("set-0/T3"))
		}
		// Conn 40 finishes slowBodyHandler before T4.
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: wrappedInboundIdle(40), 41: wrappedOutboundIdle(41)}}

		// T4 (B1 + S1 regression): new conn g50 stuck in slowFlushHandler -> windowCut!
		h.do("START set-0/T4", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: wrappedInboundIdle(40), 41: wrappedOutboundIdle(41), 50: slowFlush(50)}}
		h.do("END set-0/T4", replyTaken)
		if h.test("set-0/T4")["windowCut"] != true {
			t.Fatalf("B1 setup: T4 windowCut = %v, want true", h.test("set-0/T4")["windowCut"])
		}

		// T5 & T6: g50 stays stuck -> MUST NEVER move onto a resumeWatch (S1)!
		for _, id := range []string{"set-0/T5", "set-0/T6"} {
			h.run(id)
			if !reflect.DeepEqual(h.test(id)["contaminated"], []interface{}{"tail:set-0/T4"}) {
				t.Fatalf("S1 failed: %s contaminated = %v, want [tail:set-0/T4]", id, h.test(id)["contaminated"])
			}
			if len(resumeWatches) != 0 {
				t.Fatalf("S1 failed: stuck handler g50 moved onto resumeWatch after %s", id)
			}
		}

		// T7 (B1): new conn g51 stuck at the EXACT same slowFlushHandler line as g50 -> MUST be windowCut!
		h.do("START set-0/T7", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: wrappedInboundIdle(40), 41: wrappedOutboundIdle(41), 50: slowFlush(50), 51: slowFlush(51)}}
		h.do("END set-0/T7", replyTaken)
		if h.test("set-0/T7")["windowCut"] != true {
			t.Fatalf("B1 failed: T7 missed new stuck handler g51 matching g50: %v", h.test("set-0/T7"))
		}

		// T8: g50 returns to idle readRequest, g51 still stuck -> MUST still be contaminated tail:T7!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: wrappedInboundIdle(40), 41: wrappedOutboundIdle(41), 50: wrappedInboundIdle(50), 51: slowFlush(51)}}
		h.do("START set-0/T8", replyStartUnasked)
		// g51 returns to idle readRequest before END T8.
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: wrappedInboundIdle(40), 41: wrappedOutboundIdle(41), 50: wrappedInboundIdle(50), 51: wrappedInboundIdle(51)}}
		h.do("END set-0/T8", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T8")["contaminated"], []interface{}{"tail:set-0/T7"}) {
			t.Fatalf("B1 failed: T8 contaminated = %v, want [tail:set-0/T7]", h.test("set-0/T8")["contaminated"])
		}

		// T9: both back at wrappedInboundIdle -> 100% clean!
		h.run("set-0/T9")
		if _, hasCont := h.test("set-0/T9")["contaminated"]; hasCont {
			t.Fatalf("T9 should be clean after g50/g51 returned to readRequest: %v", h.test("set-0/T9"))
		}
	})

	t.Run("Nit4 START reads gap counters before stop-the-world stack snapshot", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), lines(workerFile, 9), lines(appFile, 4))
		h.ask("n4")
		origLines := snapshotLines
		var order []string
		snapshotLines = func() (map[string][]int, error) {
			order = append(order, "lines")
			return origLines()
		}
		snapshotGoroutines = func() goroutineSnapshot {
			order = append(order, "stack")
			return goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		}
		h.run("set-0/T1")
		order = nil
		h.do("START set-0/T2", replyStartAskedBy("n4"))
		if len(order) < 2 || order[0] != "lines" || order[1] != "stack" {
			t.Fatalf("Nit4 failed: START call order = %v, want [lines stack ...]", order)
		}
		h.do("END set-0/T2", replyTaken)
	})

	t.Run("SF3 unclosed START->START checks resumeWatch first with no duplicate tail or double streak", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 3), none(), // T0 (creates watch)
			lines(appFile, 4), none(), // T_s1 (streak 1)
			lines(appFile, 5), none(), // T_s2 (streak 2 -> watch armed!)
			lines(appFile, 6), none(), // T2 (after unclosed T1)
			lines(appFile, 7), // T3 (parked -> streak must be 1, NOT 2!)
		)
		backoff := &goroutineInfo{
			id: 15, baseStatus: "select", topLine: "/app/consumer.go:12",
			frames: []string{"main.consumerLoop"}, framePkgs: []string{"main"}, frameLines: []string{"/app/consumer.go:12"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		steady := &goroutineInfo{
			id: 15, baseStatus: "IO wait", topLine: "/app/consumer.go:25",
			frames: []string{"internal/poll.runtime_pollWait", "main.consumerLoop"}, framePkgs: []string{"internal/poll", "main"}, frameLines: []string{"/go/poll.go:1", "/app/consumer.go:25"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		handling := &goroutineInfo{
			id: 15, baseStatus: "chan receive", topLine: "/app/consumer.go:50",
			frames: []string{"main.handleMsg", "main.consumerLoop"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/consumer.go:50", "/app/consumer.go:28"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: backoff}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// Arm watch on gid 15 originating from set-0/T0.
		h.do("START set-0/T0", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady}}
		h.do("END set-0/T0", replyTaken)
		h.run("set-0/Ts1")
		h.run("set-0/Ts2")
		if _, armed := resumeWatches[15]; !armed {
			t.Fatalf("expected resumeWatch armed on gid 15 after Ts2")
		}

		// START T1 with gid 15 parked at watchSig, then wake gid 15 into handling,
		// then START T2 with NO END for T1!
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: handling}}
		h.do("START set-0/T2", replyStartUnasked)

		// gid 15 must be in EXACTLY ONE tail (set-0/T0, NOT duplicated in set-0/T1).
		count15 := 0
		var owner string
		for _, lt := range lingeringTails {
			for _, tgt := range lt.targets {
				if tgt.gid == 15 {
					count15++
					owner = lt.testID
				}
			}
		}
		if count15 != 1 || owner != "set-0/T0" {
			t.Fatalf("SF3 failed: gid 15 present in %d tails (owner=%q), want 1 (set-0/T0)", count15, owner)
		}

		// Finish T2 while gid 15 settles back to steady.
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady}}
		h.do("END set-0/T2", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"unclosed:set-0/T1", "tail:set-0/T0"}) {
			t.Fatalf("SF3 failed: T2 contaminated = %v, want [unclosed:set-0/T1 tail:set-0/T0]", h.test("set-0/T2")["contaminated"])
		}

		// START T3 with gid 15 at steady: streak must be 1 (NOT incremented twice to 2!).
		h.do("START set-0/T3", replyStartUnasked)
		if _, armed := resumeWatches[15]; armed {
			t.Fatalf("SF3 failed: gid 15 moved onto watch after 1 START (double streak increment)")
		}
		if st := lingeringStreaks[15]; st.count != 1 {
			t.Fatalf("SF3 failed: lingeringStreaks[15].count = %d, want 1", st.count)
		}
		h.do("END set-0/T3", replyTaken)
	})

	t.Run("Nit1 addLingeringTail deduplicates gid across truncated END and armed watch re-attach", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 3), none(), // T0
			lines(appFile, 4), none(), // Ts1
			lines(appFile, 5), none(), // Ts2 (watch armed)
			lines(appFile, 6), none(), // T1 (truncated END while watched gid 15 is active!)
			lines(appFile, 7), // T2 (complete START fires watch -> must NOT duplicate gid 15!)
		)
		backoff := &goroutineInfo{
			id: 15, baseStatus: "select", topLine: "/app/consumer.go:12",
			frames: []string{"main.consumerLoop"}, framePkgs: []string{"main"}, frameLines: []string{"/app/consumer.go:12"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		steady := &goroutineInfo{
			id: 15, baseStatus: "IO wait", topLine: "/app/consumer.go:25",
			frames: []string{"internal/poll.runtime_pollWait", "main.consumerLoop"}, framePkgs: []string{"internal/poll", "main"}, frameLines: []string{"/go/poll.go:1", "/app/consumer.go:25"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		handling := &goroutineInfo{
			id: 15, baseStatus: "chan receive", topLine: "/app/consumer.go:50",
			frames: []string{"main.handleMsg", "main.consumerLoop"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/consumer.go:50", "/app/consumer.go:28"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: backoff}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T0", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: steady}}
		h.do("END set-0/T0", replyTaken)
		h.run("set-0/Ts1")
		h.run("set-0/Ts2")

		// T1 starts cleanly, gid 15 wakes into handling, END T1 is truncated -> adds gid 15 to T1's tail while watch stays armed.
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: handling}, truncated: true}
		h.do("END set-0/T1", replyTaken)

		// T2 starts with complete snapshot -> watch fires and attempts re-attach; addLingeringTail MUST deduplicate!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: handling}}
		h.do("START set-0/T2", replyStartUnasked)
		count15 := 0
		for _, lt := range lingeringTails {
			for _, tgt := range lt.targets {
				if tgt.gid == 15 {
					count15++
				}
			}
		}
		if count15 != 1 {
			t.Fatalf("Nit1 failed: gid 15 duplicated across %d tails, want 1", count15)
		}
		h.do("END set-0/T2", replyTaken)
	})

	t.Run("SF1 mid-job START baseline compares against lastQuiescentSigs and fails closed", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		t.Run("deeper mid-job worker at START", func(t *testing.T) {
			h := newGapHarness(t,
				lines(appFile, 1), none(), // warm
				lines(appFile, 2), none(), // N (mid-job at START and END!)
				lines(appFile, 3), none(), // N+1 (worker finishes fraud job and returns to idle!)
				lines(appFile, 4), // N+2 (clean!)
			)
			idle := &goroutineInfo{
				id: 40, baseStatus: "chan receive", topLine: "/app/worker.go:193",
				frames: []string{"main.jobWorker"}, framePkgs: []string{"main"}, frameLines: []string{"/app/worker.go:193"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			midJob := &goroutineInfo{
				id: 40, baseStatus: "chan receive", topLine: "/app/worker.go:186",
				frames: []string{"main.processJob", "main.jobWorker"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/worker.go:186", "/app/worker.go:194"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: idle}}
			snapshotGoroutines = func() goroutineSnapshot { return cur }

			// warm establishes lastQuiescentSigs[40] = idle (depth 1).
			h.run("set-0/warm")

			// Gap puts worker 40 mid-job (depth 2); during N it takes a new fraud job and parks at the EXACT same midJob line!
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: midJob}}
			h.run("set-0/N")
			recN := h.test("set-0/N")
			if recN["windowCut"] != true {
				t.Fatalf("SF1 failed: N missed mid-job worker baseline (windowCut=%v, rec=%v)", recN["windowCut"], recN)
			}
			if !reflect.DeepEqual(recN["lingeringGoroutines"], []interface{}{"main.processJob"}) {
				t.Fatalf("SF1: N lingeringGoroutines = %v, want [main.processJob]", recN["lingeringGoroutines"])
			}

			// N+1: worker finishes fraud job and returns to idle before END N+1.
			// N+1 MUST be contaminated tail:set-0/N and MUST NOT get blamed with windowCut!
			h.do("START set-0/N1", replyStartUnasked)
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 40: idle}}
			h.do("END set-0/N1", replyTaken)
			recN1 := h.test("set-0/N1")
			if !reflect.DeepEqual(recN1["contaminated"], []interface{}{"tail:set-0/N"}) {
				t.Fatalf("SF1 failed: N+1 contaminated = %v, want [tail:set-0/N]", recN1["contaminated"])
			}
			if _, hasCut := recN1["windowCut"]; hasCut {
				t.Fatalf("SF1 failed: innocent N+1 was falsely marked windowCut: %v", recN1)
			}

			// N+2: worker is back at idle -> 100% clean!
			h.run("set-0/N2")
			if _, hasCont := h.test("set-0/N2")["contaminated"]; hasCont {
				t.Fatalf("SF1: N+2 should be clean, got %v", h.test("set-0/N2"))
			}
		})

		t.Run("same-depth mid-job at START and non-app idle conn caught mid-handler at START", func(t *testing.T) {
			h := newGapHarness(t,
				lines(appFile, 1), none(), // warm
				lines(appFile, 2), none(), // N
				lines(appFile, 3), none(), // N+1
				lines(appFile, 4), // N+2
			)
			workerIdle := &goroutineInfo{
				id: 41, baseStatus: "chan receive", topLine: "/app/worker.go:10",
				frames: []string{"main.inlineWorker"}, framePkgs: []string{"main"}, frameLines: []string{"/app/worker.go:10"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			workerMid := &goroutineInfo{
				id: 41, baseStatus: "chan receive", topLine: "/app/worker.go:25",
				frames: []string{"main.inlineWorker"}, framePkgs: []string{"main"}, frameLines: []string{"/app/worker.go:25"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			connIdle := &goroutineInfo{
				id: 42, baseStatus: "IO wait", topLine: "/go/net/http/server.go:100",
				frames: []string{"net/http.(*conn).readRequest", "net/http.(*conn).serve"}, framePkgs: []string{"net/http", "net/http"},
				createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
			}
			connMid := &goroutineInfo{
				id: 42, baseStatus: "chan receive", topLine: "/app/h.go:40",
				frames: []string{"main.slowHandler", "net/http.serverHandler.ServeHTTP", "net/http.(*conn).serve"}, framePkgs: []string{"main", "net/http", "net/http"}, frameLines: []string{"/app/h.go:40", "/go/s.go:1", "/go/s.go:2"},
				createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
			}
			cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 41: workerIdle, 42: connIdle}}
			snapshotGoroutines = func() goroutineSnapshot { return cur }
			h.run("set-0/warm")

			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 41: workerMid, 42: connMid}}
			h.run("set-0/N")
			recN := h.test("set-0/N")
			if recN["windowCut"] != true {
				t.Fatalf("SF1 same-depth/conn failed: N windowCut=%v, want true", recN["windowCut"])
			}
			if !reflect.DeepEqual(recN["lingeringGoroutines"], []interface{}{"main.inlineWorker", "main.slowHandler"}) {
				t.Fatalf("SF1 same-depth/conn: lingering = %v, want [main.inlineWorker main.slowHandler]", recN["lingeringGoroutines"])
			}

			h.do("START set-0/N1", replyStartUnasked)
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 41: workerIdle, 42: connIdle}}
			h.do("END set-0/N1", replyTaken)
			if _, hasCut := h.test("set-0/N1")["windowCut"]; hasCut {
				t.Fatalf("innocent N1 falsely marked windowCut: %v", h.test("set-0/N1"))
			}
			h.run("set-0/N2")
			if _, hasCont := h.test("set-0/N2")["contaminated"]; hasCont {
				t.Fatalf("N2 should be clean: %v", h.test("set-0/N2"))
			}
		})

		t.Run("shallower mid-job at START keeps deeper Pop idle baseline and no persistent taint", func(t *testing.T) {
			h := newGapHarness(t,
				lines(appFile, 1), none(), // warm
				lines(appFile, 2), none(), // N (mid-job in shallower vendored Call at START and END)
				lines(appFile, 3), none(), // N+1 (finishes and returns to Pop)
				lines(appFile, 4), // N+2 (clean)
			)
			popIdle := &goroutineInfo{
				id: 43, baseStatus: "chan receive", topLine: "/app/q.go:10",
				frames:      []string{"main.(*appQueue).Pop", "main.worker3"},
				framePkgs:   []string{"main", "main"},
				frameLines:  []string{"/app/q.go:10", "/app/worker.go:20"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			shallowMid := &goroutineInfo{
				id: 43, baseStatus: "chan receive", topLine: "/mod/drv/drv.go:5",
				frames:      []string{"example.com/drv.Call", "main.worker3"},
				framePkgs:   []string{"example.com/drv", "main"},
				frameLines:  []string{"/mod/drv/drv.go:5", "/app/worker.go:21"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 43: popIdle}}
			snapshotGoroutines = func() goroutineSnapshot { return cur }

			h.run("set-0/warm")
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 43: shallowMid}}
			h.run("set-0/N")
			recN := h.test("set-0/N")
			if recN["windowCut"] != true {
				t.Fatalf("shallower mid-job at START missed N (windowCut=%v, rec=%v)", recN["windowCut"], recN)
			}

			h.do("START set-0/N1", replyStartUnasked)
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 43: popIdle}}
			h.do("END set-0/N1", replyTaken)
			recN1 := h.test("set-0/N1")
			if !reflect.DeepEqual(recN1["contaminated"], []interface{}{"tail:set-0/N"}) {
				t.Fatalf("N+1 contaminated = %v, want [tail:set-0/N]", recN1["contaminated"])
			}
			if _, hasCut := recN1["windowCut"]; hasCut {
				t.Fatalf("innocent N+1 falsely marked windowCut: %v", recN1)
			}

			h.run("set-0/N2")
			if _, hasCont := h.test("set-0/N2")["contaminated"]; hasCont {
				t.Fatalf("N+2 should be clean: %v", h.test("set-0/N2"))
			}
		})

		t.Run("first-START shallower mid-job returning to deeper real idle settles onto resumeWatch without permanent taint", func(t *testing.T) {
			h := newGapHarness(t,
				lines(appFile, 1), none(), // N (very first START sees shallowMid, END returns to deeper popIdle)
				lines(appFile, 2), none(), // N+1 (tail:N, streak 1)
				lines(appFile, 3), none(), // N+2 (settled onto resumeWatch -> CLEAN!)
				lines(appFile, 4), // N+3 (CLEAN!)
			)
			popIdle := &goroutineInfo{
				id: 44, baseStatus: "chan receive", topLine: "/app/q.go:10",
				frames:      []string{"main.(*appQueue).Pop", "main.worker3"},
				framePkgs:   []string{"main", "main"},
				frameLines:  []string{"/app/q.go:10", "/app/worker.go:20"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			shallowMid := &goroutineInfo{
				id: 44, baseStatus: "chan receive", topLine: "/mod/drv/drv.go:5",
				frames:      []string{"example.com/drv.Call", "main.worker3"},
				framePkgs:   []string{"example.com/drv", "main"},
				frameLines:  []string{"/mod/drv/drv.go:5", "/app/worker.go:21"},
				createdByFn: "main.init", createdByPkg: "main",
			}
			cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 44: shallowMid}}
			snapshotGoroutines = func() goroutineSnapshot { return cur }

			h.do("START set-0/N", replyStartUnasked)
			cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 44: popIdle}}
			h.do("END set-0/N", replyTaken)
			if h.test("set-0/N")["windowCut"] != true {
				t.Fatalf("N windowCut = %v, want true", h.test("set-0/N")["windowCut"])
			}

			h.run("set-0/N1")
			if !reflect.DeepEqual(h.test("set-0/N1")["contaminated"], []interface{}{"tail:set-0/N"}) {
				t.Fatalf("N1 contaminated = %v, want [tail:set-0/N]", h.test("set-0/N1")["contaminated"])
			}

			for _, id := range []string{"set-0/N2", "set-0/N3"} {
				h.run(id)
				if _, hasCut := h.test(id)["windowCut"]; hasCut {
					t.Fatalf("%s unexpectedly marked windowCut: %v", id, h.test(id))
				}
				if _, hasCont := h.test(id)["contaminated"]; hasCont {
					t.Fatalf("%s stuck in permanent taint: %v", id, h.test(id))
				}
			}
		})
	})

	t.Run("SF2 16 MiB stack-cap truncation logs one-time recovery diagnostic", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		var buf bytes.Buffer
		origOut := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(origOut) })

		h := newGapHarness(t, lines(appFile, 1), none(), lines(appFile, 2))
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}, truncated: true}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// Two truncated tests must emit the diagnostic log EXACTLY ONCE.
		h.run("set-0/T1")
		h.run("set-0/T2")

		logged := buf.String()
		if !strings.Contains(logged, "16 MiB") {
			t.Fatalf("SF2 truncation log missing 16 MiB cap: %q", logged)
		}
		if !strings.Contains(logged, envIgnoreGoroutines) {
			t.Fatalf("SF2 truncation log missing %s: %q", envIgnoreGoroutines, logged)
		}
		if !strings.Contains(logged, envGoroutineTracking+"=off") {
			t.Fatalf("SF2 truncation log missing %s=off: %q", envGoroutineTracking, logged)
		}
		if !strings.Contains(logged, "does not shrink runtime.Stack") {
			t.Fatalf("SF2 truncation log should clarify %s does not shrink runtime.Stack: %q", envIgnoreGoroutines, logged)
		}
		if cnt := strings.Count(logged, "16 MiB"); cnt != 1 {
			t.Fatalf("SF2 truncation diagnostic logged %d times, want once:\n%s", cnt, logged)
		}
	})

	t.Run("SF2 lazy sync.Once janitor and non-HTTP third-party goroutine settle onto resumeWatch after T2", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 1), none(), // T1: spawns lazy janitor + lazy gRPC over wrapped conn
			lines(appFile, 2), none(), // T2: tail:T1 (streak 1)
			lines(appFile, 3), none(), // T3: settled onto resumeWatch -> CLEAN!
			lines(appFile, 4), none(), // T4: still parked -> CLEAN!
			lines(appFile, 5), none(), // T5: janitor wakes up inside T5 -> tail:T1!
			lines(appFile, 6), // T6: janitor exited -> CLEAN!
		)
		meta = &metaFile{
			mode:        ctrModeAtomic,
			granularity: ctrGranularityPerBlk,
			pkgs:        []metaPkg{{path: "example.com/app"}},
		}
		janitorPark := &goroutineInfo{
			id: 70, baseStatus: "chan receive", topLine: "/app/janitor.go:10",
			frames: []string{"main.janitor"}, framePkgs: []string{"main"}, frameLines: []string{"/app/janitor.go:10"},
			createdByFn: "main.initLazy.func1", createdByPkg: "main",
		}
		grpcConnPark := &goroutineInfo{
			id: 71, baseStatus: "IO wait", topLine: "/go/poll.go:84",
			frames:      []string{"internal/poll.runtime_pollWait", "main.(*wrappedConn).Read", "google.golang.org/grpc/internal/transport.(*http2Client).reader"},
			framePkgs:   []string{"internal/poll", "main", "google.golang.org/grpc/internal/transport"},
			frameLines:  []string{"/go/poll.go:84", "/app/conn.go:20", "/mod/grpc/http2_client.go:100"},
			createdByFn: "google.golang.org/grpc/internal/transport.newHTTP2Client", createdByPkg: "google.golang.org/grpc/internal/transport",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1 lazily starts both goroutines -> windowCut:true.
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 70: janitorPark, 71: grpcConnPark}}
		h.do("END set-0/T1", replyTaken)
		if h.test("set-0/T1")["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", h.test("set-0/T1")["windowCut"])
		}

		// T2: tail:T1 (streak 1 across START T2 -> END T2).
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		// T3 & T4: both settled onto resumeWatches -> 100% CLEAN!
		for _, id := range []string{"set-0/T3", "set-0/T4"} {
			h.run(id)
			rec := h.test(id)
			if _, hasCut := rec["windowCut"]; hasCut {
				t.Fatalf("SF2 failed: %s got windowCut: %v", id, rec)
			}
			if _, hasCont := rec["contaminated"]; hasCont {
				t.Fatalf("SF2 failed: %s got contaminated: %v", id, rec)
			}
		}

		// T5: janitor wakes up and exits during T5 -> resumeWatch MUST fire tail:set-0/T1 on T5!
		h.do("START set-0/T5", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 71: grpcConnPark}}
		h.do("END set-0/T5", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T5")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("SF2 wakeup failed: T5 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T5")["contaminated"])
		}

		// T6: clean again!
		h.run("set-0/T6")
		if _, hasCont := h.test("set-0/T6")["contaminated"]; hasCont {
			t.Fatalf("T6 should be clean: %v", h.test("set-0/T6"))
		}
	})

	t.Run("lazy sync.Once time.Sleep janitor settles onto resumeWatch after T2 and catches later wakeup", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 1), none(), // T1: spawns lazy sleep janitor
			lines(appFile, 2), none(), // T2: tail:T1 (streak 1)
			lines(appFile, 3), none(), // T3: settled onto resumeWatch -> CLEAN!
			lines(appFile, 4), none(), // T4: still sleeping -> CLEAN!
			lines(appFile, 5), none(), // T5: janitor wakes up inside T5 -> tail:T1!
			lines(appFile, 6), // T6: janitor exited -> CLEAN!
		)
		sleepJanitor := &goroutineInfo{
			id: 72, baseStatus: "sleep", topLine: "/go/src/runtime/time.go:200",
			frames:      []string{"time.Sleep", "main.sleepJanitor"},
			framePkgs:   []string{"time", "main"},
			frameLines:  []string{"/go/src/runtime/time.go:200", "/app/janitor.go:30"},
			createdByFn: "main.initSleep.func1", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 72: sleepJanitor}}
		h.do("END set-0/T1", replyTaken)
		if h.test("set-0/T1")["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", h.test("set-0/T1")["windowCut"])
		}

		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}

		for _, id := range []string{"set-0/T3", "set-0/T4"} {
			h.run(id)
			if _, hasCut := h.test(id)["windowCut"]; hasCut {
				t.Fatalf("%s got windowCut: %v", id, h.test(id))
			}
			if _, hasCont := h.test(id)["contaminated"]; hasCont {
				t.Fatalf("%s got contaminated: %v", id, h.test(id))
			}
		}

		h.do("START set-0/T5", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		h.do("END set-0/T5", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T5")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T5 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T5")["contaminated"])
		}

		h.run("set-0/T6")
		if _, hasCont := h.test("set-0/T6")["contaminated"]; hasCont {
			t.Fatalf("T6 should be clean: %v", h.test("set-0/T6"))
		}
	})

	t.Run("SF2 END streak reset prevents active mustExit goroutine from prematurely settling", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t,
			lines(appFile, 1), none(), // T1
			lines(appFile, 2), none(), // T2 (goroutine active at END T2 -> resets streak!)
			lines(appFile, 3), // T3 (MUST still be tail:T1, not clean!)
		)
		parked := &goroutineInfo{
			id: 75, baseStatus: "chan receive", topLine: "/app/w.go:10",
			frames: []string{"main.worker"}, framePkgs: []string{"main"}, frameLines: []string{"/app/w.go:10"},
			createdByFn: "main.handle", createdByPkg: "main",
		}
		running := &goroutineInfo{
			id: 75, baseStatus: "runnable", topLine: "/app/w.go:20",
			frames: []string{"main.doWork", "main.worker"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/w.go:20", "/app/w.go:12"},
			createdByFn: "main.handle", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 75: parked}}
		h.do("END set-0/T1", replyTaken)

		// START T2 sees parked (streak 1), END T2 sees running (streak reset!).
		h.do("START set-0/T2", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 75: running}}
		h.do("END set-0/T2", replyTaken)

		// START T3 sees parked again, but streak was reset at END T2 -> T3 MUST stay tail:T1!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 75: parked}}
		h.run("set-0/T3")
		if !reflect.DeepEqual(h.test("set-0/T3")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("SF2 END streak reset failed: T3 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T3")["contaminated"])
		}
	})

	t.Run("R10-SF1 resumeWatch direct return to original idle restores lastQuiescentSigs without false windowCut", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		for _, tc := range []struct {
			name string
			orig *goroutineInfo
		}{
			{
				name: "same-depth original idle",
				orig: &goroutineInfo{
					id: 80, baseStatus: "select", topLine: "/app/c.go:10",
					frames: []string{"main.consumer"}, framePkgs: []string{"main"}, frameLines: []string{"/app/c.go:10"},
					createdByFn: "main.init", createdByPkg: "main",
				},
			},
			{
				name: "deeper original idle",
				orig: &goroutineInfo{
					id: 80, baseStatus: "select", topLine: "/app/c.go:15",
					frames: []string{"main.backoffWait", "main.consumer"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/c.go:15", "/app/c.go:10"},
					createdByFn: "main.init", createdByPkg: "main",
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newGapHarness(t,
					lines(appFile, 1), none(), // T1: orig -> watchState
					lines(appFile, 2), none(), // T2: streak 1
					lines(appFile, 3), none(), // T3: moves onto watchState watch (clean)
					lines(appFile, 4), none(), // T4: returned directly to orig in gap T3->T4 -> tail:T1, NO windowCut!
					lines(appFile, 5), // T5: clean!
				)
				watchState := &goroutineInfo{
					id: 80, baseStatus: "IO wait", topLine: "/app/c.go:30",
					frames: []string{"internal/poll.runtime_pollWait", "main.consumer"}, framePkgs: []string{"internal/poll", "main"}, frameLines: []string{"/go/poll.go:1", "/app/c.go:30"},
					createdByFn: "main.init", createdByPkg: "main",
				}
				cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}}
				snapshotGoroutines = func() goroutineSnapshot { return cur }

				h.do("START set-0/T1", replyStartUnasked)
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: watchState}}
				h.do("END set-0/T1", replyTaken)
				h.run("set-0/T2")
				h.run("set-0/T3")
				if _, armed := resumeWatches[80]; !armed {
					t.Fatal("expected resumeWatch armed on gid 80 after T3")
				}

				// In gap T3->T4, gid 80 returns DIRECTLY to its original idle (tc.orig).
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}}
				h.run("set-0/T4")
				rec4 := h.test("set-0/T4")
				if !reflect.DeepEqual(rec4["contaminated"], []interface{}{"tail:set-0/T1"}) {
					t.Fatalf("T4 contaminated = %v, want [tail:set-0/T1]", rec4["contaminated"])
				}
				if _, hasCut := rec4["windowCut"]; hasCut {
					t.Fatalf("R10-SF1 failed: T4 got false windowCut after direct return to original idle: %v", rec4)
				}

				// T5 MUST be 100% clean (never stuck in tail:T4 forever!).
				h.run("set-0/T5")
				rec5 := h.test("set-0/T5")
				if _, hasCut := rec5["windowCut"]; hasCut {
					t.Fatalf("R10-SF1 failed: T5 got windowCut: %v", rec5)
				}
				if _, hasCont := rec5["contaminated"]; hasCont {
					t.Fatalf("R10-SF1 failed: T5 got contaminated: %v", rec5)
				}
			})

			t.Run(tc.name+"/in-window direct return", func(t *testing.T) {
				t.Setenv(envEndWaitMS, "0")
				h := newGapHarness(t,
					lines(appFile, 1), none(), // T1: orig -> watchState
					lines(appFile, 2), none(), // T2: streak 1
					lines(appFile, 3), none(), // T3: moves onto watchState watch (clean)
					lines(appFile, 4), none(), // T4: START at watchState, returns directly to orig BEFORE END T4 -> tail:T1, NO windowCut!
					lines(appFile, 5), // T5: clean!
				)
				watchState := &goroutineInfo{
					id: 80, baseStatus: "IO wait", topLine: "/app/c.go:30",
					frames: []string{"internal/poll.runtime_pollWait", "main.consumer"}, framePkgs: []string{"internal/poll", "main"}, frameLines: []string{"/go/poll.go:1", "/app/c.go:30"},
					createdByFn: "main.init", createdByPkg: "main",
				}
				cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}}
				snapshotGoroutines = func() goroutineSnapshot { return cur }

				h.do("START set-0/T1", replyStartUnasked)
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: watchState}}
				h.do("END set-0/T1", replyTaken)
				h.run("set-0/T2")
				h.run("set-0/T3")
				if _, armed := resumeWatches[80]; !armed {
					t.Fatal("expected resumeWatch armed on gid 80 after T3")
				}

				// START T4 with gid 80 still at watchState, then return directly to tc.orig BEFORE END T4.
				h.do("START set-0/T4", replyStartUnasked)
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}}
				h.do("END set-0/T4", replyTaken)
				rec4 := h.test("set-0/T4")
				if !reflect.DeepEqual(rec4["contaminated"], []interface{}{"tail:set-0/T1"}) {
					t.Fatalf("in-window return: T4 contaminated = %v, want [tail:set-0/T1]", rec4["contaminated"])
				}
				if _, hasCut := rec4["windowCut"]; hasCut {
					t.Fatalf("in-window return failed: T4 got false windowCut: %v", rec4)
				}

				// T5 MUST be 100% clean (never stuck in tail:T4 forever!).
				h.run("set-0/T5")
				rec5 := h.test("set-0/T5")
				if _, hasCut := rec5["windowCut"]; hasCut {
					t.Fatalf("in-window return failed: T5 got windowCut: %v", rec5)
				}
				if _, hasCont := rec5["contaminated"]; hasCont {
					t.Fatalf("in-window return failed: T5 got contaminated: %v", rec5)
				}
			})

			t.Run(tc.name+"/in-window wake then return on poll 2", func(t *testing.T) {
				t.Setenv(envEndWaitMS, "0")
				h := newGapHarness(t,
					lines(appFile, 1), none(), // T1
					lines(appFile, 2), none(), // T2
					lines(appFile, 3), none(), // T3
					lines(appFile, 4), none(), // T4: END poll #1 awake at busyState, poll #2 returned to tc.orig!
					lines(appFile, 5), // T5: clean!
				)
				watchState := &goroutineInfo{
					id: 80, baseStatus: "IO wait", topLine: "/app/c.go:30",
					frames: []string{"internal/poll.runtime_pollWait", "main.consumer"}, framePkgs: []string{"internal/poll", "main"}, frameLines: []string{"/go/poll.go:1", "/app/c.go:30"},
					createdByFn: "main.init", createdByPkg: "main",
				}
				busyState := &goroutineInfo{
					id: 80, baseStatus: "runnable", topLine: "/app/c.go:45",
					frames: []string{"main.handleMsg", "main.consumer"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/c.go:45", "/app/c.go:31"},
					createdByFn: "main.init", createdByPkg: "main",
				}
				var seq []goroutineSnapshot
				cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}}
				snapshotGoroutines = func() goroutineSnapshot {
					if len(seq) > 0 {
						s := seq[0]
						seq = seq[1:]
						return s
					}
					return cur
				}

				h.do("START set-0/T1", replyStartUnasked)
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: watchState}}
				h.do("END set-0/T1", replyTaken)
				h.run("set-0/T2")
				h.run("set-0/T3")

				t.Setenv(envEndWaitMS, "100")
				h.do("START set-0/T4", replyStartUnasked)
				seq = []goroutineSnapshot{
					{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: busyState}},
					{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}},
				}
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}}
				h.do("END set-0/T4", replyTaken)
				rec4 := h.test("set-0/T4")
				if !reflect.DeepEqual(rec4["contaminated"], []interface{}{"tail:set-0/T1"}) {
					t.Fatalf("wake->orig poll2: T4 contaminated = %v, want [tail:set-0/T1]", rec4["contaminated"])
				}
				if _, hasCut := rec4["windowCut"]; hasCut {
					t.Fatalf("wake->orig poll2 failed: T4 got false windowCut: %v", rec4)
				}

				h.run("set-0/T5")
				rec5 := h.test("set-0/T5")
				if _, hasCut := rec5["windowCut"]; hasCut {
					t.Fatalf("wake->orig poll2 failed: T5 got windowCut: %v", rec5)
				}
				if _, hasCont := rec5["contaminated"]; hasCont {
					t.Fatalf("wake->orig poll2 failed: T5 got contaminated: %v", rec5)
				}
			})

			t.Run(tc.name+"/unclosed in-window return", func(t *testing.T) {
				t.Setenv(envEndWaitMS, "0")
				h := newGapHarness(t,
					lines(appFile, 1), none(), // T1
					lines(appFile, 2), none(), // T2
					lines(appFile, 3), none(), // T3
					lines(appFile, 4), none(), // T5 (after unclosed T4 during which gid 80 returned to tc.orig!)
					lines(appFile, 5), // T6: clean!
				)
				watchState := &goroutineInfo{
					id: 80, baseStatus: "IO wait", topLine: "/app/c.go:30",
					frames: []string{"internal/poll.runtime_pollWait", "main.consumer"}, framePkgs: []string{"internal/poll", "main"}, frameLines: []string{"/go/poll.go:1", "/app/c.go:30"},
					createdByFn: "main.init", createdByPkg: "main",
				}
				cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}}
				snapshotGoroutines = func() goroutineSnapshot { return cur }

				h.do("START set-0/T1", replyStartUnasked)
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: watchState}}
				h.do("END set-0/T1", replyTaken)
				h.run("set-0/T2")
				h.run("set-0/T3")
				if _, armed := resumeWatches[80]; !armed {
					t.Fatal("expected resumeWatch armed on gid 80 after T3")
				}

				// START T4 with gid 80 at watchState; gid 80 returns directly to tc.orig
				// before an unclosed START T5 arrives (no END for T4!).
				h.do("START set-0/T4", replyStartUnasked)
				cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 80: tc.orig}}
				h.do("START set-0/T5", replyStartUnasked)
				h.do("END set-0/T5", replyTaken)
				rec5 := h.test("set-0/T5")
				if !reflect.DeepEqual(rec5["contaminated"], []interface{}{"unclosed:set-0/T4", "tail:set-0/T1"}) {
					t.Fatalf("unclosed in-window return failed: T5 contaminated = %v, want [unclosed:set-0/T4 tail:set-0/T1]", rec5["contaminated"])
				}
				if _, hasCut := rec5["windowCut"]; hasCut {
					t.Fatalf("unclosed in-window return failed: T5 got false windowCut: %v", rec5)
				}

				// T6 MUST be 100% clean (gid 80 was NOT falsely added to a lingering T4 tail!).
				h.run("set-0/T6")
				rec6 := h.test("set-0/T6")
				if _, hasCut := rec6["windowCut"]; hasCut {
					t.Fatalf("unclosed in-window return failed: T6 got windowCut: %v", rec6)
				}
				if _, hasCont := rec6["contaminated"]; hasCont {
					t.Fatalf("unclosed in-window return failed: T6 got contaminated: %v", rec6)
				}
			})
		}
	})

	t.Run("R10-SF2 END_WAIT intermediate poll catches transient watch wakeup and resets broken streak", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "100")
		h := newGapHarness(t,
			lines(appFile, 1), none(), // T1: spawns janitor (90) and worker (91)
			lines(appFile, 2), none(), // T2: streak 1 for 90 and 91
			lines(appFile, 3), none(), // T3: 90 moves onto watch; 91 stays lingering after END T2 transient wakeup!
			lines(appFile, 4), // T4: 90 transiently wakes on END T4 poll #1 and re-parks on poll #2 -> T4 MUST still get tail:T1!
		)
		janitorPark := &goroutineInfo{
			id: 90, baseStatus: "chan receive", topLine: "/app/j.go:10",
			frames: []string{"main.janitor"}, framePkgs: []string{"main"}, frameLines: []string{"/app/j.go:10"},
			createdByFn: "main.initLazy", createdByPkg: "main",
		}
		janitorAwake := &goroutineInfo{
			id: 90, baseStatus: "runnable", topLine: "/app/j.go:15",
			frames: []string{"main.janitorTick", "main.janitor"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/j.go:15", "/app/j.go:11"},
			createdByFn: "main.initLazy", createdByPkg: "main",
		}
		workerPark := &goroutineInfo{
			id: 91, baseStatus: "chan receive", topLine: "/app/w.go:10",
			frames: []string{"main.worker"}, framePkgs: []string{"main"}, frameLines: []string{"/app/w.go:10"},
			createdByFn: "main.handle", createdByPkg: "main",
		}
		workerAwake := &goroutineInfo{
			id: 91, baseStatus: "runnable", topLine: "/app/w.go:20",
			frames: []string{"main.doWork", "main.worker"}, framePkgs: []string{"main", "main"}, frameLines: []string{"/app/w.go:20", "/app/w.go:12"},
			createdByFn: "main.handle", createdByPkg: "main",
		}
		// Helper goroutine 92 forces END_WAIT to take 2 polls when present on poll #1.
		fastChild := &goroutineInfo{
			id: 92, baseStatus: "runnable", topLine: "/app/fast.go:5",
			frames: []string{"main.fastHelper"}, framePkgs: []string{"main"}, frameLines: []string{"/app/fast.go:5"},
			createdByFn: "main.handle", createdByPkg: "main",
		}

		var seq []goroutineSnapshot
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot {
			if len(seq) > 0 {
				s := seq[0]
				seq = seq[1:]
				return s
			}
			return cur
		}

		// T1: spawns 90 and 91 (use END_WAIT=0 for T1 setup).
		t.Setenv(envEndWaitMS, "0")
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 90: janitorPark, 91: workerPark}}
		h.do("END set-0/T1", replyTaken)

		// T2: START sees both parked (streak 1). During END T2 (END_WAIT=100ms),
		// poll #1 sees 91 awake (and fastChild 92 triggers poll #2), poll #2 sees 91
		// re-parked and 92 exited. Poll #1 MUST reset 91's streak!
		t.Setenv(envEndWaitMS, "100")
		h.do("START set-0/T2", replyStartUnasked)
		seq = []goroutineSnapshot{
			{all: map[uint64]*goroutineInfo{1: {id: 1}, 90: janitorPark, 91: workerAwake, 92: fastChild}},
			{all: map[uint64]*goroutineInfo{1: {id: 1}, 90: janitorPark, 91: workerPark}},
		}
		h.do("END set-0/T2", replyTaken)

		// T3: 90 reaches streak 2 and moves onto resumeWatch, while 91 (whose streak
		// was reset on END T2 poll #1) stays in lingeringTails at streak 1 -> T3 gets tail:T1!
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 90: janitorPark, 91: workerPark}}
		h.do("START set-0/T3", replyStartUnasked)
		if _, armed := resumeWatches[90]; !armed {
			t.Fatal("expected gid 90 to move onto resumeWatch at START T3")
		}
		if _, armed := resumeWatches[91]; armed {
			t.Fatal("R10-SF2 failed: gid 91 moved onto resumeWatch despite waking on END T2 poll #1")
		}
		// 91 exits before END T3 so T4 tests 90's watch alone.
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 90: janitorPark}}
		h.do("END set-0/T3", replyTaken)
		if !reflect.DeepEqual(h.test("set-0/T3")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T3 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T3")["contaminated"])
		}

		// T4: START T4 clean (90 parked on watch). At END T4, poll #1 sees 90
		// awake (janitorAwake), and poll #2 sees 90 re-parked at janitorPark!
		// Poll #1 MUST fire the resumeWatch so T4 gets tail:set-0/T1!
		h.do("START set-0/T4", replyStartUnasked)
		seq = []goroutineSnapshot{
			{all: map[uint64]*goroutineInfo{1: {id: 1}, 90: janitorAwake}},
			{all: map[uint64]*goroutineInfo{1: {id: 1}, 90: janitorPark}},
		}
		h.do("END set-0/T4", replyTaken)
		rec4 := h.test("set-0/T4")
		if !reflect.DeepEqual(rec4["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("R10-SF2 failed: transient watch wakeup on END poll #1 was lost (T4 contaminated=%v, want [tail:set-0/T1])", rec4["contaminated"])
		}
		if _, hasCut := rec4["windowCut"]; hasCut {
			t.Fatalf("T4 drained within END_WAIT and should not be windowCut: %v", rec4)
		}
	})

	t.Run("R10 NITs pre-existing mid-handler HTTP conn never rebaselines and resumeWatches caps at maxResumeWatches", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 1), none(), lines(appFile, 2), none(), lines(appFile, 3))
		handlerA := &goroutineInfo{
			id: 95, baseStatus: "chan receive", topLine: "/app/h.go:10",
			frames: []string{"main.stepA", "net/http.serverHandler.ServeHTTP", "net/http.(*conn).serve"}, framePkgs: []string{"main", "net/http", "net/http"}, frameLines: []string{"/app/h.go:10", "/go/s.go:1", "/go/s.go:2"},
			createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
		}
		handlerB := &goroutineInfo{
			id: 95, baseStatus: "chan receive", topLine: "/app/h.go:20",
			frames: []string{"main.stepB", "net/http.serverHandler.ServeHTTP", "net/http.(*conn).serve"}, framePkgs: []string{"main", "net/http", "net/http"}, frameLines: []string{"/app/h.go:20", "/go/s.go:1", "/go/s.go:2"},
			createdByFn: "net/http.(*Server).Serve", createdByPkg: "net/http",
		}
		// Pre-existing HTTP conn first seen mid-handler (active idleSig) transitions to handlerB (same depth).
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 95: handlerA}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }
		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 95: handlerB}}
		h.do("END set-0/T1", replyTaken)
		h.run("set-0/T2")
		h.run("set-0/T3")
		if _, armed := resumeWatches[95]; armed {
			t.Fatal("stuck pre-existing HTTP handler must never move onto resumeWatch")
		}
		if !reflect.DeepEqual(h.test("set-0/T3")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T3 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T3")["contaminated"])
		}

		// Verify resumeWatches respects maxResumeWatches cap.
		resetGoroutineState()
		for i := 0; i < maxResumeWatches; i++ {
			resumeWatches[uint64(1000+i)] = resumeWatch{testID: "set-0/pre"}
		}
		j := &goroutineInfo{
			id: 9999, baseStatus: "chan receive", topLine: "/app/j.go:10",
			frames: []string{"main.janitor"}, framePkgs: []string{"main"}, frameLines: []string{"/app/j.go:10"},
			createdByFn: "main.init", createdByPkg: "main",
		}
		addLingeringTail("set-0/T1", []lingeringTarget{{gid: 9999, mustExit: true}}, nil)
		snap := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 9999: j}}
		pruneLingeringTails(snap, nil, nil, nil, true)
		pruneLingeringTails(snap, nil, nil, nil, true)
		if len(resumeWatches) != maxResumeWatches {
			t.Fatalf("len(resumeWatches) = %d, want capped at %d", len(resumeWatches), maxResumeWatches)
		}
		if len(lingeringTails) != 1 {
			t.Fatalf("target should remain fail-closed in lingeringTails when resumeWatches is full, got %d tails", len(lingeringTails))
		}
	})
}

// TestV3WireRepliesAndRecordsAgainstCollectorParsers pins the exact wire
// format of go-sdk's v3 control-socket replies (ACK/IGNORED, proto=3, bg=,
// caps=) and JSON coverage/background records, checking the individual cap
// values against a mirror of the collector ParseCapsString format (cross-repo
// parser golden tests against enterprise and k8s-proxy live in those collector
// PRs), verifying:
//   - replies stay well under the 1024-byte control buffer cap and parse
//     cleanly under both proto>=2 token parsing and v3 caps= parsing,
//   - IGNORED proto=3 <reason> and START replies with bg=<token> parse cleanly,
//   - asyncExecutors is ALWAYS false across all Go versions/modes so Go
//     removal eligibility relies on a collector-side isolation check rather
//     than SDK capabilities alone,
//   - windowCut/lingeringGoroutines/contaminated/afterCut JSON fields decode
//     into the collector record schema.
func TestV3WireRepliesAndRecordsAgainstCollectorParsers(t *testing.T) {
	stubCoverage(t, nil, nil)
	origGoVersion := runtimeGoVersion
	t.Cleanup(func() { runtimeGoVersion = origGoVersion })

	parseV3Reply := func(reply string) (cmd string, proto int, bg string, caps map[string]bool) {
		t.Helper()
		if len(reply) >= 1024 {
			t.Fatalf("reply length %d >= 1024 byte collector cap: %q", len(reply), reply)
		}
		fields := strings.Fields(reply)
		if len(fields) < 2 {
			t.Fatalf("malformed reply %q", reply)
		}
		cmd = fields[0]
		caps = map[string]bool{}
		for _, f := range fields[1:] {
			switch {
			case strings.HasPrefix(f, "proto="):
				proto, _ = strconv.Atoi(strings.TrimPrefix(f, "proto="))
			case strings.HasPrefix(f, "bg="):
				bg = strings.TrimPrefix(f, "bg=")
			case strings.HasPrefix(f, "caps="):
				for _, kv := range strings.Split(strings.TrimPrefix(f, "caps="), ",") {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						t.Fatalf("malformed cap %q in %q", kv, reply)
					}
					b, err := strconv.ParseBool(v)
					if err != nil {
						t.Fatalf("non-bool cap %q in %q: %v", kv, reply, err)
					}
					caps[k] = b
				}
			}
		}
		return cmd, proto, bg, caps
	}

	// Verify IGNORED proto=3 <reason> and START with bg=<token>.
	cmdIgn, protoIgn, _, _ := parseV3Reply(replyIgnoredWith("not built with -cover"))
	if cmdIgn != "IGNORED" || protoIgn != 3 {
		t.Fatalf("IGNORED reply parse = (%q, %d), want (IGNORED, 3)", cmdIgn, protoIgn)
	}
	bgAsked, bgToken = true, "tok-42"
	cmdStartBG, protoStartBG, gotBG, capsStartBG := parseV3Reply(startReply())
	if cmdStartBG != "ACK" || protoStartBG != 3 || gotBG != "tok-42" || capsStartBG["asyncExecutors"] {
		t.Fatalf("START with bg token parsed = (%q, %d, %q, %v)", cmdStartBG, protoStartBG, gotBG, capsStartBG)
	}
	bgAsked, bgToken = false, ""

	for _, tc := range []struct {
		ver           string
		trackingEnv   string
		wantWindowCut bool
	}{
		{"go1.20.14", "", false},
		{"go1.21.0", "", true},
		{"go1.27.3", "", true},
		{"go1.27.3", "off", false},
	} {
		runtimeGoVersion = func() string { return tc.ver }
		t.Setenv(envGoroutineTracking, tc.trackingEnv)
		for _, reply := range []string{startReply(), ackReply()} {
			cmd, proto, _, caps := parseV3Reply(reply)
			if cmd != "ACK" || proto != 3 {
				t.Fatalf("ver=%s env=%q reply=%q -> cmd=%q proto=%d", tc.ver, tc.trackingEnv, reply, cmd, proto)
			}
			if caps["windowCut"] != tc.wantWindowCut {
				t.Fatalf("ver=%s env=%q caps[windowCut]=%v, want %v", tc.ver, tc.trackingEnv, caps["windowCut"], tc.wantWindowCut)
			}
			if caps["asyncExecutors"] {
				t.Fatalf("B1 violated: ver=%s env=%q caps[asyncExecutors]=true, want false", tc.ver, tc.trackingEnv)
			}
			if !caps["contaminated"] || !caps["background"] || caps["classMismatch"] || caps["listenerContamination"] {
				t.Fatalf("ver=%s env=%q unexpected caps: %v", tc.ver, tc.trackingEnv, caps)
			}
		}
	}

	// Verify cut test record and afterCut BACKGROUND record decode cleanly into
	// the collector JSON record schema.
	recBytes, err := encodeRecordWithMeta("set-0/T1", map[string][]int{"app/a.go": {10}}, map[string][]int{"app/a.go": {0}}, []string{"window-cut"}, nil, true, []string{"main.fraudAlert"})
	if err != nil {
		t.Fatal(err)
	}
	var decRec struct {
		ID                   string           `json:"id"`
		ExecutedLinesByFile  map[string][]int `json:"executedLinesByFile"`
		ExecutedBlocksByFile map[string][]int `json:"executedBlocksByFile"`
		Contaminated         []string         `json:"contaminated"`
		OverlapLinesByFile   map[string][]int `json:"overlapLinesByFile"`
		WindowCut            bool             `json:"windowCut"`
		LingeringGoroutines  []string         `json:"lingeringGoroutines"`
	}
	if err := json.Unmarshal(recBytes, &decRec); err != nil {
		t.Fatalf("decode test record: %v", err)
	}
	if decRec.ID != "set-0/T1" || !decRec.WindowCut || len(decRec.OverlapLinesByFile) != 0 ||
		!reflect.DeepEqual(decRec.Contaminated, []string{"window-cut"}) ||
		!reflect.DeepEqual(decRec.LingeringGoroutines, []string{"main.fraudAlert"}) {
		t.Fatalf("decoded test record mismatch: %+v", decRec)
	}

	bgBytes, err := encodeBackgroundWithMeta("set-0/T1", "set-0/T2", map[string][]int{}, true)
	if err != nil {
		t.Fatal(err)
	}
	var decBG struct {
		Kind                string           `json:"kind"`
		Before              string           `json:"before"`
		After               string           `json:"after"`
		V                   int              `json:"v"`
		Build               string           `json:"build"`
		Lost                int              `json:"lost"`
		AfterCut            bool             `json:"afterCut"`
		ExecutedLinesByFile map[string][]int `json:"executedLinesByFile"`
	}
	if err := json.Unmarshal(bgBytes, &decBG); err != nil {
		t.Fatalf("decode background record: %v", err)
	}
	if decBG.Kind != "background" || decBG.Before != "set-0/T2" || decBG.After != "set-0/T1" ||
		!decBG.AfterCut || len(decBG.ExecutedLinesByFile) != 0 {
		t.Fatalf("decoded background record mismatch: %+v", decBG)
	}
}
