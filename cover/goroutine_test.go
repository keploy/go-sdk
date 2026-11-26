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
		"goroutine 42 [sleep]:\r",
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
		"goroutine 77 [chan receive]:",
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
	wantFrames42 := []string{"time.Sleep", "example.com/svc/orders.(*Engine[net/http.Request]).CheckFraud.func1"}
	wantPkgs42 := []string{"time", "example.com/svc/orders"}
	if !reflect.DeepEqual(g42.frames, wantFrames42) {
		t.Fatalf("g42 frames = %v, want %v", g42.frames, wantFrames42)
	}
	if !reflect.DeepEqual(g42.framePkgs, wantPkgs42) {
		t.Fatalf("g42 framePkgs = %v, want %v", g42.framePkgs, wantPkgs42)
	}
	if g42.createdByFn != "main.handleOrder" || g42.createdByPkg != "main" {
		t.Fatalf("g42 createdBy = (%q, %q), want (main.handleOrder, main) [ancestor must not overwrite]", g42.createdByFn, g42.createdByPkg)
	}

	g55 := snap.all[55]
	if g55 == nil || g55.createdByFn != "net/http.(*Server).Serve" || g55.createdByPkg != "net/http" {
		t.Fatalf("g55 pre-1.21 createdBy parsed wrong: %+v", g55)
	}

	g77 := snap.all[77]
	if g77 == nil || g77.framePkgs[0] != "example.com/app.v1/worker" || g77.createdByPkg != "example.com/app.v1/api" {
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
			got := isAppRelevantGoroutine(tc.g, 19, tc.covered)
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

	// Per-func granularity (non-block coverage) disables windowCut and asyncExecutors.
	meta = &metaFile{mode: ctrModeAtomic, granularity: ctrGranularityPerFunc}
	wantPerFunc := "windowCut=false,contaminated=true,classMismatch=false,background=true,asyncExecutors=false,listenerContamination=false"
	if got := capsString(true); got != wantPerFunc {
		t.Fatalf("per-func caps = %q, want %q", got, wantPerFunc)
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
		if ok != tc.wantOK || proto < 2 || proto != 3 || bg != tc.wantBG || hasBG != tc.wantHas {
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
	t.Run("unclosed START->START marks next window contaminated", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3))
		snapshotGoroutines = func() goroutineSnapshot {
			return goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		}
		h.do("START set-0/test-1", replyStartUnasked)
		// No END set-0/test-1 -> unclosed!
		h.do("START set-0/test-2", replyStartUnasked)
		h.do("END set-0/test-2", replyTaken)
		got, _ := h.test("set-0/test-2")["contaminated"].([]interface{})
		if !reflect.DeepEqual(got, []interface{}{"unclosed:set-0/test-1"}) {
			t.Fatalf("test-2 contaminated = %v, want [unclosed:set-0/test-1]", got)
		}
	})

	t.Run("immediate N+1 contaminated even when goroutine exits right at boundary", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4), none(), lines(appFile, 5))
		h.ask("r1")
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartAskedBy("r1"))
		// T1 spawns lingering goroutine 99.
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{
			1:  {id: 1},
			99: {id: 99, frames: []string{"main.fraudAlert"}, framePkgs: []string{"main"}, createdByFn: "main.handle", createdByPkg: "main"},
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
			t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1] (immediate next test after cut END)", t2["contaminated"])
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

	t.Run("stack truncation fails closed", func(t *testing.T) {
		t.Setenv(envEndWaitMS, "0")
		h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 4))
		cur := goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		snapshotGoroutines = func() goroutineSnapshot { return cur }

		h.do("START set-0/T1", replyStartUnasked)
		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}, truncated: true}
		h.do("END set-0/T1", replyTaken)
		t1 := h.test("set-0/T1")
		if t1["windowCut"] != true {
			t.Fatalf("truncated T1 windowCut = %v, want true", t1["windowCut"])
		}
		if !reflect.DeepEqual(t1["lingeringGoroutines"], []interface{}{"stack-truncated"}) {
			t.Fatalf("truncated T1 lingeringGoroutines = %v, want [stack-truncated]", t1["lingeringGoroutines"])
		}

		cur = goroutineSnapshot{all: map[uint64]*goroutineInfo{1: {id: 1}}}
		h.run("set-0/T2")
		if !reflect.DeepEqual(h.test("set-0/T2")["contaminated"], []interface{}{"tail:set-0/T1"}) {
			t.Fatalf("T2 after truncated T1 contaminated = %v, want [tail:set-0/T1]", h.test("set-0/T2")["contaminated"])
		}
	})

	t.Run("lingeringTails and contaminated stay bounded under sustained leaks", func(t *testing.T) {
		resetGaps(t)
		alive := map[uint64]*goroutineInfo{1: {id: 1}}
		for i := 0; i < 80; i++ {
			gid := uint64(100 + i)
			alive[gid] = &goroutineInfo{id: gid}
			addLingeringTail(fmt.Sprintf("set-0/T%d", i), []uint64{gid}, []string{"main.leak"})
		}
		if len(lingeringTails) != maxLingeringTails {
			t.Fatalf("len(lingeringTails) = %d, want capped at %d", len(lingeringTails), maxLingeringTails)
		}
		marks := collectStartContamination("", goroutineSnapshot{all: alive}, true)
		if len(marks) != maxPartialNames+1 || !strings.HasPrefix(marks[maxPartialNames], "+") {
			t.Fatalf("marks = %v, want %d tail entries + +N summary", marks, maxPartialNames)
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
		cut, err := encodeBackgroundWithMeta("set-0/T1", "set-0/T2", map[string][]int{workerFile: {7}}, true)
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
	})
}
