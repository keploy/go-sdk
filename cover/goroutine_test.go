package cover

import (
	"encoding/json"
	"fmt"
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
	if !reflect.DeepEqual(g42.frames, wantFrames42) {
		t.Fatalf("g42 frames = %v, want %v", g42.frames, wantFrames42)
	}
	if !reflect.DeepEqual(g42.framePkgs, wantPkgs42) {
		t.Fatalf("g42 framePkgs = %v, want %v", g42.framePkgs, wantPkgs42)
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

	// Default: enabled + active block coverage.
	t.Setenv(envGoroutineTracking, "")
	got := capsString(true)
	if got != defaultCaps {
		t.Fatalf("default caps = %q, want %q", got, defaultCaps)
	}
	if strings.ContainsAny(got, " \t\r\n") {
		t.Fatalf("caps must not contain whitespace: %q", got)
	}

	// Kill switch disables windowCut and asyncExecutors.
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

	// Per-func granularity (non-block coverage) disables windowCut, asyncExecutors,
	// AND runtime goroutine tracking consistently.
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
		startMarks := collectStartContamination("", goroutineSnapshot{all: alive}, true, nil, nil)
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

	t.Run("N3 startup-backoff consumer re-baselines cleanly after settling in steady-state IO wait", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5), none(), lines(appFile, 6))
		backoff := &goroutineInfo{
			id: 15, baseStatus: "sleep", topLine: "/app/consumer.go:12",
			frames: []string{"time.Sleep", "main.consumerLoop"}, framePkgs: []string{"time", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		steady := &goroutineInfo{
			id: 15, baseStatus: "IO wait", topLine: "/app/consumer.go:25",
			frames: []string{"internal/poll.runtime_pollWait", "main.consume", "main.consumerLoop"}, framePkgs: []string{"internal/poll", "main", "main"},
			createdByFn: "main.main", createdByPkg: "main",
		}
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}, 15: backoff}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		// T1 starts while consumer is in backoff Sleep; consumer connects into steady IO wait.
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

		// T3: STILL at steady IO wait (streak 2 -> re-baselined & cleared!).
		h.run("set-0/T3")
		if _, hasCont := h.test("set-0/T3")["contaminated"]; hasCont {
			t.Fatalf("N3 failed: startup-backoff consumer stuck forever on T3: %v", h.test("set-0/T3"))
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
}
