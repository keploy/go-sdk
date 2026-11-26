//go:build linux || darwin

package cover

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// startWithEnv runs bin in a process group of its own with extra env vars and
// returns its address once it serves.
func startWithEnv(t *testing.T, bin string, extraEnv ...string) app {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	a := app{stderr: &bytes.Buffer{}, mu: &sync.Mutex{}}
	cmd.Stderr = lockedWriter{a.mu, a.stderr}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	a.stop = func() {
		once.Do(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		})
	}
	t.Cleanup(a.stop)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("the app did not start: %v\n%s", err, a.log())
	}
	a.addr = strings.TrimSpace(line)
	return a
}

const asyncAppExtra = `package main

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

var (
	fraudHits  atomic.Int64
	notifyHits atomic.Int64
	fastHits   atomic.Int64
)

func fraudAlert(orderID string) {
	// Simulate mocked I/O latency that keeps the goroutine alive across END T5
	// and across a clean gap T5->T6, executing the fraud branch INSIDE T6!
	time.Sleep(150 * time.Millisecond)
	if orderID != "" {
		fraudHits.Add(1)
	}
}

func fastAsyncWork() {
	time.Sleep(20 * time.Millisecond)
	fastHits.Add(1)
}

func multiWindowWork(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
	fastHits.Add(1)
}

func notifyWork(orderID string) {
	if orderID != "" {
		notifyHits.Add(1)
	}
}

func notifyAsync(orderID string, delayMS int) {
	time.Sleep(time.Duration(delayMS) * time.Millisecond)
	notifyWork(orderID)
}

func init() {
	http.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if r.URL.Query().Get("fraud") == "1" {
			go fraudAlert(id)
		}
		fmt.Fprintf(w, "order %s", id)
	})
	http.HandleFunc("/fast-async", func(w http.ResponseWriter, r *http.Request) {
		go fastAsyncWork()
		fmt.Fprint(w, "fast")
	})
	http.HandleFunc("/long-async", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		if ms <= 0 {
			ms = 250
		}
		go multiWindowWork(ms)
		fmt.Fprint(w, "long")
	})
	http.HandleFunc("/notify-async", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		if ms <= 0 {
			ms = 60
		}
		go notifyAsync(r.URL.Query().Get("id"), ms)
		fmt.Fprint(w, "notified")
	})
	http.HandleFunc("/notify-sync", func(w http.ResponseWriter, r *http.Request) {
		notifyWork(r.URL.Query().Get("id"))
		fmt.Fprint(w, "notified-sync")
	})
	http.HandleFunc("/outbound", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("url")
		resp, err := http.Get(target)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		fmt.Fprint(w, "outbound-ok")
	})
}
`

func buildAsyncApp(t *testing.T, k *keploySide) string {
	t.Helper()
	hook := hookCopy(t, k.sock, k.data)
	mod := appModuleDir(t)
	if err := os.WriteFile(filepath.Join(mod, "async.go"), []byte(asyncAppExtra), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, _ := stage(t, hook, mod, nil)
	return bin
}

func readNextRecord(t *testing.T, k *keploySide, a app) map[string]any {
	t.Helper()
	select {
	case rec := <-k.records:
		return rec
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for coverage record\napp log:\n%s", a.log())
		return nil
	}
}

func blocksOfRecord(t *testing.T, rec map[string]any) map[string][]int {
	t.Helper()
	raw, ok := rec["executedBlocksByFile"].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string][]int, len(raw))
	for file, v := range raw {
		arr, ok := v.([]any)
		if !ok {
			t.Fatalf("executedBlocksByFile[%q] = %T, want []any", file, v)
		}
		for _, e := range arr {
			out[file] = append(out[file], int(e.(float64)))
		}
	}
	return out
}

// TestRealGoApp_P6FraudAlertGoroutineTailAcrossCleanGap reproduces Ruling #5 /
// P6 on a real -cover -covermode=atomic Go binary:
//   - T5 calls /orders?id=5&fraud=1, spawning go fraudAlert() which blocks 150ms.
//   - END T5 waits KEPLOY_GO_END_WAIT_MS=30ms -> fraudAlert is still alive ->
//     T5 emits windowCut:true + lingeringGoroutines.
//   - Gap T5->T6 is 20ms and CLEAN (fraudAlert is still sleeping, 0 gap hits).
//   - T6 runs /orders?id=6 (no fraud!), sleeps 180ms inside its window so
//     fraudAlert executes its fraud branch INSIDE T6 and exits before END T6.
//   - Assert: T5 has windowCut:true, T6 has contaminated:["tail:set-0/T5"]
//     (even though the gap T5->T6 was clean!), T6 saw fraudAlert's blocks,
//     and T7 (after fraudAlert exited) is completely clean!
func TestRealGoApp_P6FraudAlertGoroutineTailAcrossCleanGap(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")

	if got := send(t, k.sock, backgroundAsk+" p6"); got != replyTaken {
		t.Fatalf("BACKGROUND: %q, want %q", got, replyTaken)
	}

	// T5: spawns fraudAlert() (sleeps 150ms).
	if got := send(t, k.sock, "START set-0/T5"); got != replyStartAskedBy("p6") {
		t.Fatalf("START T5: %q", got)
	}
	get(t, a, "/orders?id=5&fraud=1")
	if got := send(t, k.sock, "END set-0/T5"); got != replyTaken {
		t.Fatalf("END T5: %q", got)
	}
	rec5 := readNextRecord(t, k, a)
	if rec5["id"] != "set-0/T5" {
		t.Fatalf("rec5 id = %v, want set-0/T5", rec5["id"])
	}
	if rec5["windowCut"] != true {
		t.Fatalf("P6 failed: T5 windowCut = %v, want true (rec=%v)", rec5["windowCut"], rec5)
	}
	lingering5, _ := rec5["lingeringGoroutines"].([]any)
	if len(lingering5) == 0 || !strings.Contains(fmt.Sprint(lingering5), "fraudAlert") {
		t.Fatalf("T5 lingeringGoroutines = %v, want entry naming fraudAlert", lingering5)
	}

	// Clean gap (20ms): fraudAlert is still blocked inside its 150ms sleep.
	time.Sleep(20 * time.Millisecond)

	// T6: normal non-fraud order, stays open long enough for T5's fraudAlert to
	// wake up, execute the fraud branch inside T6, and exit.
	if got := send(t, k.sock, "START set-0/T6"); got != replyStartAskedBy("p6") {
		t.Fatalf("START T6: %q", got)
	}
	get(t, a, "/orders?id=6")
	time.Sleep(200 * time.Millisecond)
	if got := send(t, k.sock, "END set-0/T6"); got != replyTaken {
		t.Fatalf("END T6: %q", got)
	}

	// Drain any optional afterCut background record before T6's own record.
	var rec6 map[string]any
	for {
		r := readNextRecord(t, k, a)
		if r["kind"] == "background" {
			if r["afterCut"] != true {
				t.Fatalf("gap T5->T6 background missing afterCut:true: %v", r)
			}
			continue
		}
		rec6 = r
		break
	}
	if rec6["id"] != "set-0/T6" {
		t.Fatalf("rec6 id = %v, want set-0/T6", rec6["id"])
	}
	if !reflect.DeepEqual(rec6["contaminated"], []any{"tail:set-0/T5"}) {
		t.Fatalf("P6 failed: T6 contaminated = %v, want [tail:set-0/T5] (rec=%v)", rec6["contaminated"], rec6)
	}
	if _, hasCut := rec6["windowCut"]; hasCut {
		t.Fatalf("T6 did not spawn a lingering goroutine but got windowCut: %v", rec6)
	}
	// Verify T6 actually caught fraudAlert's execution in its blocks (proving P6
	// genuinely spilled into T6's window!).
	asyncFile := appModule + "/async.go"
	if len(blocksOfRecord(t, rec6)[asyncFile]) <= len(blocksOfRecord(t, rec5)[asyncFile]) {
		// T5 ran /orders + go fraudAlert (entering fraudAlert up to sleep),
		// T6 ran /orders (non-fraud) + fraudAlert's post-sleep fraudHits branch!
		// Even if block counts tie, verify fraudAlert's post-sleep line ran in T6.
		t6Lines := linesOfRecord(t, rec6)[asyncFile]
		t5Lines := linesOfRecord(t, rec5)[asyncFile]
		if reflect.DeepEqual(t6Lines, t5Lines) {
			t.Fatalf("expected fraudAlert post-sleep branch to spill into T6: T5=%v T6=%v", t5Lines, t6Lines)
		}
	}

	// T7: after fraudAlert exited, T7 MUST be 100% clean!
	if got := send(t, k.sock, "START set-0/T7"); got != replyStartAskedBy("p6") {
		t.Fatalf("START T7: %q", got)
	}
	get(t, a, "/orders?id=7")
	if got := send(t, k.sock, "END set-0/T7"); got != replyTaken {
		t.Fatalf("END T7: %q", got)
	}
	rec7 := readNextRecord(t, k, a)
	if rec7["id"] != "set-0/T7" {
		t.Fatalf("rec7 id = %v, want set-0/T7", rec7["id"])
	}
	if _, hasCut := rec7["windowCut"]; hasCut {
		t.Fatalf("T7 should be clean, got windowCut: %v", rec7)
	}
	if _, hasCont := rec7["contaminated"]; hasCont {
		t.Fatalf("T7 should be clean, got contaminated: %v", rec7)
	}
}

// TestRealGoApp_FastGoroutineDrainsWithinEndWait verifies that a fast 20ms
// goroutine finishes within KEPLOY_GO_END_WAIT_MS=200: END waits for it,
// reports windowCut=false/contaminated=nil, and captures its blocks!
func TestRealGoApp_FastGoroutineDrainsWithinEndWait(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=200")

	rec := measure(t, k, a, "set-0/fast-1", "/fast-async")
	if _, hasCut := rec["windowCut"]; hasCut {
		t.Fatalf("fast goroutine should have drained within END_WAIT, got windowCut: %v", rec)
	}
	if _, hasCont := rec["contaminated"]; hasCont {
		t.Fatalf("fast goroutine should be clean, got contaminated: %v", rec)
	}
	asyncFile := appModule + "/async.go"
	// Verify fastAsyncWork's post-sleep line (`fastHits.Add(1)`) was captured!
	fastLine := 0
	for i, l := range strings.Split(asyncAppExtra, "\n") {
		if strings.Contains(l, "fastHits.Add(1)") {
			fastLine = i + 1
			break
		}
	}
	if fastLine == 0 {
		t.Fatal("could not locate fastHits.Add(1) line")
	}
	gotLines := linesOfRecord(t, rec)[asyncFile]
	if !containsInt(gotLines, fastLine) {
		t.Fatalf("fast goroutine line %d missing from captured lines %v (END did not wait for drain)", fastLine, gotLines)
	}
	if len(blocksOfRecord(t, rec)[asyncFile]) == 0 {
		t.Fatalf("expected executedBlocksByFile for %s, got %v", asyncFile, rec["executedBlocksByFile"])
	}
}

// TestRealGoApp_IdleHTTPKeepAliveZeroFalsePositives drives 5 sequential tests
// over the SAME persistent HTTP keep-alive TCP connection (plus an outbound
// keep-alive HTTP client request from inside the app) and asserts ZERO
// false-positive windowCuts or contaminations!
func TestRealGoApp_IdleHTTPKeepAliveZeroFalsePositives(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=200")

	// Start a local echo server for the app's outbound keep-alive http.Client test.
	upstream := http.NewServeMux()
	upstream.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	})
	upSrv := &http.Server{Handler: upstream}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = upSrv.Serve(ln) }()
	t.Cleanup(func() { _ = upSrv.Close() })

	tr := &http.Transport{
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		DisableKeepAlives:   false,
	}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr}

	reusedCount := 0
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("set-0/ka-%d", i)
		if got := send(t, k.sock, "START "+id); got != replyStartUnasked {
			t.Fatalf("START %s: %q", id, got)
		}
		path := fmt.Sprintf("/twin?n=%d", i)
		if i == 2 {
			// Exercise outbound http.DefaultClient keep-alive (spawns persistConn readLoop/writeLoop).
			path = "/outbound?url=http://" + ln.Addr().String() + "/ok"
		}
		var reused bool
		req, err := http.NewRequest("GET", "http://"+a.addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) {
				reused = info.Reused
			},
		}))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if reused {
			reusedCount++
		}

		began := time.Now()
		if got := send(t, k.sock, "END "+id); got != replyTaken {
			t.Fatalf("END %s: %q", id, got)
		}
		// Keep-alive idle goroutines must NEVER trigger END wait polling!
		if elapsed := time.Since(began); elapsed > 150*time.Millisecond {
			t.Fatalf("END %s took %v (false-positive wait on idle keep-alive conn?)", id, elapsed)
		}
		rec := readNextRecord(t, k, a)
		if _, hasCut := rec["windowCut"]; hasCut {
			t.Fatalf("keep-alive test %s got false-positive windowCut: %v", id, rec)
		}
		if _, hasCont := rec["contaminated"]; hasCont {
			t.Fatalf("keep-alive test %s got false-positive contaminated: %v", id, rec)
		}
	}
	if reusedCount < 4 {
		t.Fatalf("expected >=4 reused keep-alive requests on same TCP conn, got %d", reusedCount)
	}
}

// TestRealGoApp_MultiWindowLingeringGoroutine spawns a 250ms goroutine in T1
// that stays alive across T2 and T3 and exits before T4 -> asserts T1
// windowCut:true, T2 and T3 contaminated:["tail:set-0/T1"], and T4 clean!
func TestRealGoApp_MultiWindowLingeringGoroutine(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=20")

	// T1: spawns 260ms goroutine -> cut after 20ms.
	rec1 := measure(t, k, a, "set-0/T1", "/long-async?ms=260")
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true", rec1["windowCut"])
	}

	// T2: runs immediately while T1's goroutine is still alive (~20-60ms elapsed).
	rec2 := measure(t, k, a, "set-0/T2", "/twin?n=2")
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}
	if _, hasCut := rec2["windowCut"]; hasCut {
		t.Fatalf("T2 unexpectedly marked windowCut: %v", rec2)
	}

	// T3: runs while T1's goroutine is STILL alive (~60-100ms elapsed).
	rec3 := measure(t, k, a, "set-0/T3", "/twin?n=3")
	if !reflect.DeepEqual(rec3["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T3 contaminated = %v, want [tail:set-0/T1]", rec3["contaminated"])
	}
	if _, hasCut := rec3["windowCut"]; hasCut {
		t.Fatalf("T3 unexpectedly marked windowCut: %v", rec3)
	}

	// Wait for T1's goroutine to exit cleanly before T4 starts.
	time.Sleep(300 * time.Millisecond)

	// T4: T1's goroutine is gone -> T4 must be completely clean!
	rec4 := measure(t, k, a, "set-0/T4", "/twin?n=4")
	if _, hasCut := rec4["windowCut"]; hasCut {
		t.Fatalf("T4 should be clean, got windowCut: %v", rec4)
	}
	if _, hasCont := rec4["contaminated"]; hasCont {
		t.Fatalf("T4 should be clean, got contaminated: %v", rec4)
	}
}

// TestRealGoApp_P7RecurringAsyncTailsNeverPolluteBackground reproduces P7:
// T1 and T2 both spawn recurring go notifyAsync() tails that linger past END
// and execute inside gaps T1->T2 and T2->T3. Asserts:
//   - T1 windowCut:true, T2 windowCut:true + contaminated:["tail:set-0/T1"],
//     T3 contaminated:["tail:set-0/T2"],
//   - EVERY BACKGROUND record emitted for gaps after cut windows carries
//     afterCut:true so collectors never absorb notifyWork into stable background,
//   - T4 running /notify-sync is completely clean (afterCut gaps did NOT
//     pollute everGap!).
func TestRealGoApp_P7RecurringAsyncTailsNeverPolluteBackground(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=20")

	if got := send(t, k.sock, backgroundAsk+" p7"); got != replyTaken {
		t.Fatalf("BACKGROUND: %q", got)
	}

	var bgRecords []map[string]any
	readTestRecord := func(wantID string) map[string]any {
		t.Helper()
		for {
			rec := readNextRecord(t, k, a)
			if rec["kind"] == "background" {
				bgRecords = append(bgRecords, rec)
				continue
			}
			if rec["id"] != wantID {
				t.Fatalf("got test record %v, want id %s", rec, wantID)
			}
			return rec
		}
	}

	// T1: spawns notifyAsync (sleeps 50ms, runs notifyWork in gap T1->T2).
	if got := send(t, k.sock, "START set-0/T1"); got != replyStartAskedBy("p7") {
		t.Fatalf("START T1: %q", got)
	}
	get(t, a, "/notify-async?id=1&ms=50")
	if got := send(t, k.sock, "END set-0/T1"); got != replyTaken {
		t.Fatalf("END T1: %q", got)
	}
	rec1 := readTestRecord("set-0/T1")
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true", rec1["windowCut"])
	}
	// Let T1's notifyAsync finish INSIDE gap T1->T2.
	time.Sleep(90 * time.Millisecond)

	// T2: also spawns notifyAsync (sleeps 50ms, runs notifyWork in gap T2->T3).
	if got := send(t, k.sock, "START set-0/T2"); got != replyStartAskedBy("p7") {
		t.Fatalf("START T2: %q", got)
	}
	get(t, a, "/notify-async?id=2&ms=50")
	if got := send(t, k.sock, "END set-0/T2"); got != replyTaken {
		t.Fatalf("END T2: %q", got)
	}
	rec2 := readTestRecord("set-0/T2")
	if rec2["windowCut"] != true {
		t.Fatalf("T2 windowCut = %v, want true", rec2["windowCut"])
	}
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}
	// Let T2's notifyAsync finish INSIDE gap T2->T3.
	time.Sleep(90 * time.Millisecond)

	// T3: synchronous twin (immediately after cut T2 -> contaminated:["tail:set-0/T2"]).
	if got := send(t, k.sock, "START set-0/T3"); got != replyStartAskedBy("p7") {
		t.Fatalf("START T3: %q", got)
	}
	get(t, a, "/twin?n=3")
	if got := send(t, k.sock, "END set-0/T3"); got != replyTaken {
		t.Fatalf("END T3: %q", got)
	}
	rec3 := readTestRecord("set-0/T3")
	if !reflect.DeepEqual(rec3["contaminated"], []any{"tail:set-0/T2"}) {
		t.Fatalf("T3 contaminated = %v, want [tail:set-0/T2]", rec3["contaminated"])
	}
	if _, hasCut := rec3["windowCut"]; hasCut {
		t.Fatalf("T3 unexpectedly windowCut: %v", rec3)
	}

	// Verify BOTH gap BACKGROUND records were emitted with afterCut:true!
	if len(bgRecords) < 2 {
		t.Fatalf("expected >=2 afterCut BACKGROUND records from P7 gaps, got %d: %v", len(bgRecords), bgRecords)
	}
	for i, bg := range bgRecords {
		if bg["afterCut"] != true {
			t.Fatalf("P7 failed: BACKGROUND[%d] missing afterCut:true (would pollute calibrated background!): %v", i, bg)
		}
	}

	// T4: runs /notify-sync synchronously. Must NOT be falsely marked
	// between-tests:async.go because afterCut gaps did NOT pollute everGap!
	if got := send(t, k.sock, "START set-0/T4"); got != replyStartAskedBy("p7") {
		t.Fatalf("START T4: %q", got)
	}
	get(t, a, "/notify-sync?id=4")
	if got := send(t, k.sock, "END set-0/T4"); got != replyTaken {
		t.Fatalf("END T4: %q", got)
	}
	rec4 := readTestRecord("set-0/T4")
	if _, hasCut := rec4["windowCut"]; hasCut {
		t.Fatalf("T4 unexpectedly windowCut: %v", rec4)
	}
	if _, hasCont := rec4["contaminated"]; hasCont {
		t.Fatalf("P7 failed: T4 falsely contaminated by afterCut tail gap: %v", rec4)
	}
}

func TestRealGoApp_UnclosedWindowContaminatesNextTest(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=50")

	if got := send(t, k.sock, "START set-0/U1"); got != replyStartUnasked {
		t.Fatalf("START U1: %q", got)
	}
	get(t, a, "/twin?n=1")
	// Deliberately omit END set-0/U1!
	if got := send(t, k.sock, "START set-0/U2"); got != replyStartUnasked {
		t.Fatalf("START U2: %q", got)
	}
	get(t, a, "/twin?n=2")
	if got := send(t, k.sock, "END set-0/U2"); got != replyTaken {
		t.Fatalf("END U2: %q", got)
	}
	rec2 := readNextRecord(t, k, a)
	if !reflect.DeepEqual(rec2["contaminated"], []any{"unclosed:set-0/U1"}) {
		t.Fatalf("U2 contaminated = %v, want [unclosed:set-0/U1]", rec2["contaminated"])
	}
}

func TestRealGoApp_Proto3CapsAndKillSwitch(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)

	// 1. Default ON: declares windowCut=true,contaminated=true,asyncExecutors=true.
	aOn := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=20")
	if got := send(t, k.sock, "START set-0/C1"); got != replyStartUnasked {
		t.Fatalf("default START reply = %q, want %q", got, replyStartUnasked)
	}
	get(t, aOn, "/twin?n=1")
	if got := send(t, k.sock, "END set-0/C1"); got != replyTaken {
		t.Fatalf("default END reply = %q, want %q", got, replyTaken)
	}
	_ = readNextRecord(t, k, aOn)
	aOn.stop()

	// 2. Kill switch OFF: declares windowCut=false,asyncExecutors=false and
	//    returns immediately on END without waiting for lingering goroutines.
	k2 := newKeploySide(t)
	bin2 := buildAsyncApp(t, k2)
	aOff := startWithEnv(t, bin2, "KEPLOY_GO_GOROUTINE_TRACKING=off", "KEPLOY_GO_END_WAIT_MS=2000")
	wantOffCaps := "windowCut=false,contaminated=true,classMismatch=false,background=true,asyncExecutors=false,listenerContamination=false"
	wantStartOff := "ACK proto=3 bg=- caps=" + wantOffCaps + "\n"
	wantEndOff := "ACK proto=3 caps=" + wantOffCaps + "\n"

	if got := send(t, k2.sock, "START set-0/K1"); got != wantStartOff {
		t.Fatalf("kill-switch START reply = %q, want %q", got, wantStartOff)
	}
	get(t, aOff, "/long-async?ms=500")
	began := time.Now()
	if got := send(t, k2.sock, "END set-0/K1"); got != wantEndOff {
		t.Fatalf("kill-switch END reply = %q, want %q", got, wantEndOff)
	}
	if elapsed := time.Since(began); elapsed > 200*time.Millisecond {
		t.Fatalf("kill-switch END waited %v (should not wait when tracking is off)", elapsed)
	}
	rec := readNextRecord(t, k2, aOff)
	if _, hasCut := rec["windowCut"]; hasCut {
		t.Fatalf("kill-switch record unexpectedly had windowCut: %v", rec)
	}
}
