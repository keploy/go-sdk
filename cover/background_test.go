package cover

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// gapHarness drives the hook as Keploy's agent does, with the app's windows
// given in turn: each END reads its test's lines, each START after an END the
// gap's, from windows, and every record sent is kept.
type gapHarness struct {
	t       *testing.T
	path    string
	windows []map[string][]int
	sent    []map[string]interface{}
	clears  int
}

func newGapHarness(t *testing.T, windows ...map[string][]int) *gapHarness {
	t.Helper()
	stubCoverage(t, nil, nil)
	h := &gapHarness{t: t, windows: windows}
	publishCoverage = reportCoverage
	clearCounters = func() error { h.clears++; return nil }
	// Called on the hook's goroutine: t.Errorf, never t.Fatalf.
	snapshotLines = func() (map[string][]int, error) {
		if len(h.windows) == 0 {
			t.Errorf("the hook read a window the test did not give")
			return nil, errors.New("no window given")
		}
		w := h.windows[0]
		h.windows = h.windows[1:]
		return w, nil
	}
	sendRecord = func(b []byte) error {
		var rec map[string]interface{}
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Errorf("the hook sent a record that is not JSON: %s", b)
			return err
		}
		h.sent = append(h.sent, rec)
		return nil
	}
	h.path = startTestServer(t)
	return h
}

func (h *gapHarness) do(command, want string) {
	h.t.Helper()
	if got := send(h.t, h.path, command); got != want {
		h.t.Fatalf("%q: answer %q, want %q", command, got, want)
	}
}

// run runs one test, START then END.
func (h *gapHarness) run(id string) {
	h.t.Helper()
	want := replyStartUnasked
	if bgAsked {
		want = replyStartAskedBy(bgToken)
	}
	h.do("START "+id, want)
	h.do("END "+id, replyTaken)
}

// ask is the agent's BACKGROUND ask, naming token.
func (h *gapHarness) ask(token string) {
	h.t.Helper()
	h.do(backgroundAsk+" "+token, replyTaken)
}

func (h *gapHarness) backgrounds() []map[string]interface{} {
	var out []map[string]interface{}
	for _, r := range h.sent {
		if r["kind"] == "background" {
			out = append(out, r)
		}
	}
	return out
}

// test is the record sent for id.
func (h *gapHarness) test(id string) map[string]interface{} {
	h.t.Helper()
	for _, r := range h.sent {
		if r["id"] == id {
			return r
		}
	}
	h.t.Fatalf("no record of %s in %v", id, h.sent)
	return nil
}

func lines(file string, ls ...int) map[string][]int { return map[string][]int{file: ls} }

func none() map[string][]int { return map[string][]int{} }

const (
	appFile    = "example.com/app/handler.go"
	workerFile = "example.com/app/worker.go"
)

// TestAGapIsSentAsABackgroundRecordOnceAsked: once asked, the window from a
// test's END to the next START is one BACKGROUND record naming both tests, in
// the shape the agent keeps (kind and before first).
func TestAGapIsSentAsABackgroundRecordOnceAsked(t *testing.T) {
	h := newGapHarness(t, lines(appFile, 3), lines(workerFile, 7, 8), lines(appFile, 3))
	h.ask("r1")
	h.run("set-0/test-1")
	h.run("set-0/test-2")
	bg := h.backgrounds()
	if len(bg) != 1 {
		t.Fatalf("background records %v, want one", bg)
	}
	want := map[string]interface{}{
		"kind": "background", "v": float64(1), "build": "go1-00112233445566778899aabbccddeeff",
		"after": "set-0/test-1", "before": "set-0/test-2", "lost": float64(0),
		"executedLinesByFile": map[string]interface{}{workerFile: []interface{}{float64(7), float64(8)}},
	}
	if !reflect.DeepEqual(bg[0], want) {
		t.Fatalf("background record %v, want %v", bg[0], want)
	}
	if _, partial := h.test("set-0/test-2")["contaminated"]; partial {
		t.Fatalf("test-2 ran no gap line but is marked partial: %v", h.test("set-0/test-2"))
	}
	if h.clears != 4 {
		t.Fatalf("the counters were reset %d times, want 4: at each START and after each END", h.clears)
	}
	data, _ := encodeBackground("set-0/test-1", "set-0/test-2", lines(workerFile, 7))
	if !strings.HasPrefix(string(data), `{"kind":"background","before":"set-0/test-2",`) {
		t.Fatalf("a background record does not open with its kind and before: %s", data)
	}
}

// TestNoBackgroundRecordBeforeTheAsk: an agent that never asked gets none,
// and a test whose lines a gap ran too is coverage-partial.
func TestNoBackgroundRecordBeforeTheAsk(t *testing.T) {
	h := newGapHarness(t, lines(appFile, 3), lines(workerFile, 7), map[string][]int{appFile: {3}, workerFile: {7}})
	h.run("set-0/test-1")
	h.run("set-0/test-2")
	if bg := h.backgrounds(); len(bg) != 0 {
		t.Fatalf("sent %v before the agent asked", bg)
	}
	got, _ := h.test("set-0/test-2")["contaminated"].([]interface{})
	if !reflect.DeepEqual(got, []interface{}{"between-tests:" + workerFile}) {
		t.Fatalf("test-2 contaminated %v, want the worker's file", got)
	}
	if _, partial := h.test("set-0/test-1")["contaminated"]; partial {
		t.Fatalf("test-1, before any gap, is marked partial")
	}
}

// TestATestMeetingAGapLineIsPartialEvenOnceKeployCouldLeaveItOut: asked,
// nothing lost, and gaps of two different tests ran the line, so keploy's
// BackgroundOf leaves it out of the set's keys; the test is still sent
// coverage-partial, since keploy does not confirm per test that it did.
func TestATestMeetingAGapLineIsPartialEvenOnceKeployCouldLeaveItOut(t *testing.T) {
	for _, ask := range []bool{true, false} {
		h := newGapHarness(t,
			lines(appFile, 3), lines(workerFile, 7), // test-1, gap
			lines(appFile, 4), lines(workerFile, 7), // test-2, gap
			map[string][]int{appFile: {3}, workerFile: {7}}) // test-3
		if ask {
			h.ask("r1")
		}
		h.run("set-0/test-1")
		h.run("set-0/test-2")
		h.run("set-0/test-3")
		got, _ := h.test("set-0/test-3")["contaminated"].([]interface{})
		if !reflect.DeepEqual(got, []interface{}{"between-tests:" + workerFile}) {
			t.Fatalf("asked %v: test-3 contaminated %v, want the worker's file", ask, got)
		}
		for _, id := range []string{"set-0/test-1", "set-0/test-2"} {
			if _, partial := h.test(id)["contaminated"]; partial {
				t.Fatalf("asked %v: %s ran no gap line but is partial", ask, id)
			}
		}
	}
}

// TestAGapLineOfAnEarlierSetStillMarksATest: the lines gaps ran are kept for
// the app's life, whatever the test set.
func TestAGapLineOfAnEarlierSetStillMarksATest(t *testing.T) {
	h := newGapHarness(t,
		lines(appFile, 1), lines(workerFile, 7), // set-0/test-1, gap into set-1
		map[string][]int{workerFile: {7}}) // set-1/test-1
	h.ask("r1")
	h.run("set-0/test-1")
	h.run("set-1/test-1")
	if n := len(h.backgrounds()); n != 1 {
		t.Fatalf("%d background records, want the cross-set gap's", n)
	}
	if _, partial := h.test("set-1/test-1")["contaminated"]; !partial {
		t.Fatalf("set-1's test met a gap line and is not partial: %v", h.test("set-1/test-1"))
	}
}

// TestAStartSaysWhetherTheAgentAsked: bg=- until BACKGROUND is taken, then
// bg=<the ask's token>, so an agent asks once, and again when the token is
// not its own (a new run, a restarted agent or app).
func TestAStartSaysWhetherTheAgentAsked(t *testing.T) {
	h := newGapHarness(t, lines(appFile, 3), none(), lines(appFile, 3))
	h.do("START set-0/test-1", replyStartUnasked)
	h.do("END set-0/test-1", replyTaken)
	h.ask("r1")
	h.do("START set-0/test-2", replyStartAskedBy("r1"))
	h.do("END set-0/test-2", replyTaken)
}

// TestNoGapIsTrustedAfterACommandNotTaken: after an END not taken, or an END
// whose reset failed, the next START sends no gap: its window holds a test's
// lines.
func TestNoGapIsTrustedAfterACommandNotTaken(t *testing.T) {
	t.Run("an END whose reset failed", func(t *testing.T) {
		h := newGapHarness(t, lines(appFile, 3), lines(appFile, 4))
		h.ask("r1")
		clearCounters = func() error { h.clears++; return nil }
		h.do("START set-0/test-1", replyStartAskedBy("r1"))
		clearCounters = func() error { return errors.New("reset failed") }
		h.do("END set-0/test-1", replyTaken)
		clearCounters = func() error { return nil }
		h.do("START set-0/test-2", replyStartAskedBy("r1")) // no gap read: windows has one left
		h.do("END set-0/test-2", replyTaken)
		bg := h.backgrounds()
		if len(h.windows) != 0 || len(bg) != 1 || !isLostMark(bg[0], "set-0/test-2", 1) {
			t.Fatalf("after a failed reset: records %v, %d windows left; want no gap read and test-2's set told of one lost", bg, len(h.windows))
		}
	})
	t.Run("a mismatched END", func(t *testing.T) {
		h := newGapHarness(t, lines(appFile, 3))
		h.ask("r1")
		h.run("set-0/test-1")
		h.do("END set-0/test-9", replyIgnored)
		snapshotLines = func() (map[string][]int, error) {
			t.Errorf("the START after a mismatched END read a gap")
			return nil, errors.New("no window given")
		}
		h.do("START set-0/test-2", replyStartAskedBy("r1"))
		if bgLost != 1 {
			t.Fatalf("a START after a mismatched END counted %d gaps lost, want 1", bgLost)
		}
	})
}

// isLostMark says rec is the lines-free BACKGROUND record that tells test
// before's set of lost gaps: no "after", so no window.
func isLostMark(rec map[string]any, before string, lost int) bool {
	ls, _ := rec["executedLinesByFile"].(map[string]any)
	return rec["kind"] == "background" && rec["before"] == before && rec["after"] == "" &&
		rec["lost"] == float64(lost) && ls != nil && len(ls) == 0
}

// TestBackgroundIsIgnoredByAnAppThatCannotBeMeasured.
func TestBackgroundIsIgnoredByAnAppThatCannotBeMeasured(t *testing.T) {
	stubCoverage(t, nil, nil)
	prepareMeta = func() error { return errors.New("not built with -cover") }
	path := startTestServer(t)
	if got := send(t, path, backgroundAsk); got != replyIgnored {
		t.Fatalf("BACKGROUND answered %q, want %q", got, replyIgnored)
	}
	if bgAsked {
		t.Fatalf("an app that cannot be measured took the ask")
	}
}

// TestAPartialRecordNamesAtMostEightFiles.
func TestAPartialRecordNamesAtMostEightFiles(t *testing.T) {
	resetGaps(t)
	test := map[string][]int{}
	for i := 0; i < 11; i++ {
		f := "example.com/app/w" + string(rune('a'+i)) + ".go"
		everGap[f] = map[int]struct{}{1: {}}
		test[f] = []int{1}
	}
	got := partialBy(test)
	if len(got) != maxPartialNames+1 || got[maxPartialNames] != "+3" || !strings.HasPrefix(got[0], "between-tests:") {
		t.Fatalf("partial %v, want 8 names then +3", got)
	}
}

// TestALostGapRecordIsCountedInTheNext: a gap record that could not be sent
// is counted in the next one's lost, so keploy dedup trusts none of the
// set's gaps.
// The set of the lost gap's later test is told of it before that test's own
// record, so keploy never takes it as complete; and the next gap's record
// carries the count too.
func TestALostGapRecordIsCountedInTheNext(t *testing.T) {
	h := newGapHarness(t,
		lines(appFile, 3), lines(workerFile, 7), // test-1, gap (lost)
		lines(appFile, 4), lines(workerFile, 8), // test-2, gap
		lines(appFile, 5)) // test-3
	h.ask("r1")
	keep := sendRecord
	fail := true
	sendRecord = func(b []byte) error {
		if fail && strings.HasPrefix(string(b), `{"kind":"background"`) {
			fail = false
			return errors.New("agent gone")
		}
		return keep(b)
	}
	h.run("set-0/test-1")
	h.run("set-0/test-2")
	h.run("set-0/test-3")
	bg := h.backgrounds()
	if len(bg) != 2 || !isLostMark(bg[0], "set-0/test-2", 1) || bg[1]["lost"] != float64(1) || bg[1]["after"] != "set-0/test-2" {
		t.Fatalf("background records %v, want test-2's set told of the lost gap, then the second gap's with lost 1", bg)
	}
	mark, own := -1, -1
	for i, r := range h.sent {
		switch {
		case isLostMark(r, "set-0/test-2", 1):
			mark = i
		case r["id"] == "set-0/test-2":
			own = i
		}
	}
	if mark < 0 || own < 0 || mark > own {
		t.Fatalf("records sent %v: want test-2's set told of the loss ahead of test-2's own record", h.sent)
	}
}

// TestALostCountStartsAfreshAtANewReceiversAsk: an ask naming another token
// (a new keploy run, an agent restarted) counts losses from zero, and the gap
// open at the ask is still sent; the same receiver asking again (a stale bg
// answer) changes nothing.
func TestALostCountStartsAfreshAtANewReceiversAsk(t *testing.T) {
	h := newGapHarness(t,
		lines(appFile, 3), lines(workerFile, 7), // test-1, gap (lost)
		lines(appFile, 4), lines(workerFile, 8), // test-2, gap
		lines(appFile, 5), lines(workerFile, 9), // test-3, gap (across the asks)
		lines(appFile, 6)) // test-4
	h.ask("r1")
	keep := sendRecord
	fail := true
	sendRecord = func(b []byte) error {
		if fail && strings.HasPrefix(string(b), `{"kind":"background"`) {
			fail = false
			return errors.New("agent gone")
		}
		return keep(b)
	}
	h.run("set-0/test-1")
	h.run("set-0/test-2")
	h.ask("r1") // the same receiver again: the loss stands
	h.run("set-0/test-3")
	if bg := h.backgrounds(); len(bg) != 2 || bg[1]["lost"] != float64(1) {
		t.Fatalf("records %v, want the lost mark and the second gap's with lost 1", bg)
	}
	h.ask("r2") // a new receiver: counted afresh
	h.run("set-1/test-4")
	bg := h.backgrounds()
	if bgLost != 0 || len(bg) != 3 || bg[2]["lost"] != float64(0) || bg[2]["after"] != "set-0/test-3" {
		t.Fatalf("after a new receiver's ask: lost %d, records %v; want the gap across the asks sent with lost 0", bgLost, bg)
	}
}

// TestTheBackgroundRecordsBytes pins a gap record's and a lost mark's bytes:
// enterprise's TestDedupJavaBackground_TheGoSDKsBytesReachBackgroundOf feeds
// these very bytes to the agent and keploy's BackgroundOf.
func TestTheBackgroundRecordsBytes(t *testing.T) {
	resetGaps(t)
	gap, err := encodeBackground("test-set-0/test-1", "test-set-0/test-2", map[string][]int{"example.com/app/worker.go": {7, 8}})
	if err != nil {
		t.Fatal(err)
	}
	const wantGap = `{"kind":"background","before":"test-set-0/test-2","after":"test-set-0/test-1","v":1,"build":"go1-00112233445566778899aabbccddeeff","lost":0,"executedLinesByFile":{"example.com/app/worker.go":[7,8]}}`
	if string(gap) != wantGap {
		t.Fatalf("gap record\n%s\nwant\n%s", gap, wantGap)
	}
	bgLost = 1
	mark, err := encodeBackground("", "test-set-0/test-3", map[string][]int{})
	if err != nil {
		t.Fatal(err)
	}
	const wantMark = `{"kind":"background","before":"test-set-0/test-3","after":"","v":1,"build":"go1-00112233445566778899aabbccddeeff","lost":1,"executedLinesByFile":{}}`
	if string(mark) != wantMark {
		t.Fatalf("lost mark\n%s\nwant\n%s", mark, wantMark)
	}
}

// TestAnUntrustedGapRightAfterTheAskIsLost: the agent asks after the first
// START it saw answered (bg=-), so the first test is already lastTest; an END
// of it whose reset fails leaves a window that is no gap, counted lost and told
// to test-2's set ahead of test-2's record. Mutation: reset lastTest at a new
// receiver's ask.
func TestAnUntrustedGapRightAfterTheAskIsLost(t *testing.T) {
	h := newGapHarness(t, lines(appFile, 3), lines(appFile, 4))
	clearCounters = func() error { h.clears++; return nil }
	h.do("START set-0/test-1", replyStartUnasked)
	h.ask("r1")
	clearCounters = func() error { return errors.New("reset failed") }
	h.do("END set-0/test-1", replyTaken)
	clearCounters = func() error { return nil }
	h.do("START set-0/test-2", replyStartAskedBy("r1"))
	if bgLost != 1 {
		t.Fatalf("lost %d, want 1", bgLost)
	}
	h.do("END set-0/test-2", replyTaken)
	mark, own := -1, -1
	for i, r := range h.sent {
		switch {
		case isLostMark(r, "set-0/test-2", 1):
			mark = i
		case r["id"] == "set-0/test-2":
			own = i
		}
	}
	if mark < 0 || own < 0 || mark > own {
		t.Fatalf("records sent %v: want test-2's set told of the loss ahead of test-2's own record", h.sent)
	}
}
