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

// asyncAppExtra adds deterministic gate-controlled async endpoints to the
// real -cover test app so tests never depend on wall-clock timing races.
const asyncAppExtra = `package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

var (
	fraudHits  atomic.Int64
	notifyHits atomic.Int64
	fastHits   atomic.Int64
	childHits  atomic.Int64
	poolHits   atomic.Int64

	gateMu   sync.Mutex
	gates    = map[string]*asyncGate{}
	poolJobs = make(chan string, 8)
)

type asyncGate struct {
	release chan struct{}
	done    chan struct{}
}

func getGate(name string) *asyncGate {
	gateMu.Lock()
	defer gateMu.Unlock()
	g := gates[name]
	if g == nil {
		g = &asyncGate{release: make(chan struct{}), done: make(chan struct{})}
		gates[name] = g
	}
	return g
}

func fraudAlert(orderID string, g *asyncGate) {
	defer close(g.done)
	<-g.release
	if orderID != "" {
		fraudHits.Add(1)
	}
}

func fastAsyncWork() {
	time.Sleep(20 * time.Millisecond)
	fastHits.Add(1)
}

func gatedWork(g *asyncGate) {
	defer close(g.done)
	<-g.release
	fastHits.Add(1)
}

func notifyWork(orderID string) {
	if orderID != "" {
		notifyHits.Add(1)
	}
}

func notifyAsync(orderID string, g *asyncGate) {
	defer close(g.done)
	<-g.release
	notifyWork(orderID)
}

func publishChildAlert(childGate *asyncGate) {
	defer close(childGate.done)
	<-childGate.release
	childHits.Add(1)
}

func parentSpawnsChildAndExits(parentGate, childGate *asyncGate) {
	defer close(parentGate.done)
	<-parentGate.release
	started := make(chan struct{})
	go func() {
		close(started)
		publishChildAlert(childGate)
	}()
	<-started
}

func poolProcess(gateName string) {
	g := getGate(gateName)
	defer close(g.done)
	<-g.release
	if gateName != "" {
		poolHits.Add(1)
	}
}

func poolWorker() {
	for gateName := range poolJobs {
		poolProcess(gateName)
	}
}

func init() {
	go poolWorker()

	http.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if r.URL.Query().Get("fraud") == "1" {
			g := getGate(r.URL.Query().Get("gate"))
			started := make(chan struct{})
			go func() {
				close(started)
				fraudAlert(id, g)
			}()
			<-started
		}
		fmt.Fprintf(w, "order %s", id)
	})
	http.HandleFunc("/fast-async", func(w http.ResponseWriter, r *http.Request) {
		go fastAsyncWork()
		fmt.Fprint(w, "fast")
	})
	http.HandleFunc("/gated-async", func(w http.ResponseWriter, r *http.Request) {
		g := getGate(r.URL.Query().Get("gate"))
		started := make(chan struct{})
		go func() {
			close(started)
			gatedWork(g)
		}()
		<-started
		fmt.Fprint(w, "gated")
	})
	http.HandleFunc("/notify-async", func(w http.ResponseWriter, r *http.Request) {
		g := getGate(r.URL.Query().Get("gate"))
		started := make(chan struct{})
		go func() {
			close(started)
			notifyAsync(r.URL.Query().Get("id"), g)
		}()
		<-started
		fmt.Fprint(w, "notified")
	})
	http.HandleFunc("/notify-sync", func(w http.ResponseWriter, r *http.Request) {
		notifyWork(r.URL.Query().Get("id"))
		fmt.Fprint(w, "notified-sync")
	})
	http.HandleFunc("/parent-child", func(w http.ResponseWriter, r *http.Request) {
		pg := getGate(r.URL.Query().Get("parent"))
		cg := getGate(r.URL.Query().Get("child"))
		started := make(chan struct{})
		go func() {
			close(started)
			parentSpawnsChildAndExits(pg, cg)
		}()
		<-started
		fmt.Fprint(w, "parent-child")
	})
	http.HandleFunc("/pool-job", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("gate")
		_ = getGate(name)
		poolJobs <- name
		fmt.Fprint(w, "pool-queued")
	})
	http.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		g := getGate(r.URL.Query().Get("gate"))
		close(g.release)
		if r.URL.Query().Get("wait") == "1" {
			<-g.done
			// Poll runtime.Stack until the released worker has fully exited
			// (or returned to idle <-poolJobs), avoiding sleep-based races.
			fn := r.URL.Query().Get("fn")
			if fn != "" {
				buf := make([]byte, 128<<10)
				needle := []byte(fn)
				for i := 0; i < 200; i++ {
					n := runtime.Stack(buf, true)
					if !bytes.Contains(buf[:n], needle) {
						break
					}
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
		fmt.Fprint(w, "released")
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

func asyncLineOf(t *testing.T, needle string) int {
	t.Helper()
	for i, l := range strings.Split(asyncAppExtra, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("asyncAppExtra has no line holding %q", needle)
	return 0
}

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
// P6 deterministically on a real -cover -covermode=atomic Go binary:
//   - T5 calls /orders?id=5&fraud=1&gate=p6, spawning go fraudAlert() blocked on gate.
//   - END T5 waits KEPLOY_GO_END_WAIT_MS=30ms -> fraudAlert is still blocked ->
//     T5 emits windowCut:true + lingeringGoroutines naming fraudAlert.
//   - Gap T5->T6 is CLEAN (fraudAlert stays blocked, 0 gap hits).
//   - T6 runs /orders?id=6 (no fraud!), then releases gate p6 so fraudAlert
//     executes fraudHits.Add(1) INSIDE T6 and exits before END T6.
//   - Assert: T5 has windowCut:true and did NOT hit fraudHits.Add(1), T6 DID
//     hit fraudHits.Add(1) AND has contaminated:["tail:set-0/T5"] (even though
//     gap T5->T6 was clean!), and T7 (after fraudAlert exited) is clean!
func TestRealGoApp_P6FraudAlertGoroutineTailAcrossCleanGap(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")

	if got := send(t, k.sock, backgroundAsk+" p6"); got != replyTaken {
		t.Fatalf("BACKGROUND: %q, want %q", got, replyTaken)
	}

	fraudHitLine := asyncLineOf(t, "fraudHits.Add(1)")
	asyncFile := appModule + "/async.go"

	// T5: spawns fraudAlert() blocked on gate "p6".
	if got := send(t, k.sock, "START set-0/T5"); got != replyStartAskedBy("p6") {
		t.Fatalf("START T5: %q", got)
	}
	get(t, a, "/orders?id=5&fraud=1&gate=p6")
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
	if containsInt(linesOfRecord(t, rec5)[asyncFile], fraudHitLine) {
		t.Fatalf("fraudHits.Add(1) line %d should not have run during T5: %v", fraudHitLine, linesOfRecord(t, rec5)[asyncFile])
	}

	// Clean gap (fraudAlert is still blocked on gate "p6").
	time.Sleep(20 * time.Millisecond)

	// T6: normal non-fraud order, then release fraudAlert INSIDE T6!
	if got := send(t, k.sock, "START set-0/T6"); got != replyStartAskedBy("p6") {
		t.Fatalf("START T6: %q", got)
	}
	get(t, a, "/orders?id=6")
	get(t, a, "/release?gate=p6&wait=1&fn=fraudAlert")
	if got := send(t, k.sock, "END set-0/T6"); got != replyTaken {
		t.Fatalf("END T6: %q", got)
	}

	// Gap T5->T6 was clean (zero lines), so T6's own record is next.
	rec6 := readNextRecord(t, k, a)
	if rec6["id"] != "set-0/T6" {
		t.Fatalf("expected clean gap T5->T6 and rec6 next, got %v", rec6)
	}
	if !reflect.DeepEqual(rec6["contaminated"], []any{"tail:set-0/T5"}) {
		t.Fatalf("P6 failed: T6 contaminated = %v, want [tail:set-0/T5] (rec=%v)", rec6["contaminated"], rec6)
	}
	if _, hasCut := rec6["windowCut"]; hasCut {
		t.Fatalf("T6 did not spawn a lingering goroutine but got windowCut: %v", rec6)
	}
	if !containsInt(linesOfRecord(t, rec6)[asyncFile], fraudHitLine) {
		t.Fatalf("P6 setup check: fraudHits.Add(1) line %d did not spill into T6: %v", fraudHitLine, linesOfRecord(t, rec6)[asyncFile])
	}

	// T7: after fraudAlert exited inside T6, T7 MUST be 100% clean!
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
	fastLine := asyncLineOf(t, "fastHits.Add(1)")
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

// TestRealGoApp_MultiWindowLingeringGoroutine spawns a gate-controlled
// goroutine in T1 that stays alive across T2 and T3 and exits before T4 ->
// asserts T1 windowCut:true, T2 and T3 contaminated:["tail:set-0/T1"], and T4 clean!
func TestRealGoApp_MultiWindowLingeringGoroutine(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=20")

	rec1 := measure(t, k, a, "set-0/T1", "/gated-async?gate=multi")
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true", rec1["windowCut"])
	}

	rec2 := measure(t, k, a, "set-0/T2", "/twin?n=2")
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}
	if _, hasCut := rec2["windowCut"]; hasCut {
		t.Fatalf("T2 unexpectedly marked windowCut: %v", rec2)
	}

	rec3 := measure(t, k, a, "set-0/T3", "/twin?n=3")
	if !reflect.DeepEqual(rec3["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T3 contaminated = %v, want [tail:set-0/T1]", rec3["contaminated"])
	}
	if _, hasCut := rec3["windowCut"]; hasCut {
		t.Fatalf("T3 unexpectedly marked windowCut: %v", rec3)
	}

	// Deterministically release T1's goroutine and wait for it to finish before T4!
	get(t, a, "/release?gate=multi&wait=1&fn=gatedWork")

	rec4 := measure(t, k, a, "set-0/T4", "/twin?n=4")
	if _, hasCut := rec4["windowCut"]; hasCut {
		t.Fatalf("T4 should be clean, got windowCut: %v", rec4)
	}
	if _, hasCont := rec4["contaminated"]; hasCont {
		t.Fatalf("T4 should be clean, got contaminated: %v", rec4)
	}
}

// TestRealGoApp_B1ChildSpawnedInGapStaysTracked verifies that if T1's lingering
// goroutine spawns a child goroutine in gap T2->T3 and exits, T3 STILL gets
// contaminated:["tail:set-0/T1"] via Go 1.21+ parent-goid child adoption, and
// T4 is clean once the child exits!
func TestRealGoApp_B1ChildSpawnedInGapStaysTracked(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=20")

	if got := send(t, k.sock, backgroundAsk+" b1"); got != replyTaken {
		t.Fatalf("BACKGROUND: %q", got)
	}

	// T1 spawns parent goroutine blocked on "p1".
	if got := send(t, k.sock, "START set-0/T1"); got != replyStartAskedBy("b1") {
		t.Fatalf("START T1: %q", got)
	}
	get(t, a, "/parent-child?parent=p1&child=c1")
	if got := send(t, k.sock, "END set-0/T1"); got != replyTaken {
		t.Fatalf("END T1: %q", got)
	}
	rec1 := readNextRecord(t, k, a)
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true", rec1["windowCut"])
	}

	// T2 runs while parent is still alive.
	if got := send(t, k.sock, "START set-0/T2"); got != replyStartAskedBy("b1") {
		t.Fatalf("START T2: %q", got)
	}
	get(t, a, "/twin?n=2")
	if got := send(t, k.sock, "END set-0/T2"); got != replyTaken {
		t.Fatalf("END T2: %q", got)
	}
	rec2 := readNextRecord(t, k, a)
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}

	// In gap T2->T3: release parent "p1" -> parent spawns child (blocked on "c1") and EXITS!
	get(t, a, "/release?gate=p1&wait=1&fn=parentSpawnsChildAndExits")

	// T3 starts: parent is DEAD, child is ALIVE -> child MUST be adopted into T1's tail!
	if got := send(t, k.sock, "START set-0/T3"); got != replyStartAskedBy("b1") {
		t.Fatalf("START T3: %q", got)
	}
	get(t, a, "/twin?n=3")
	// Release child INSIDE T3 so child executes childHits.Add(1) inside T3 and exits!
	get(t, a, "/release?gate=c1&wait=1&fn=publishChildAlert")
	if got := send(t, k.sock, "END set-0/T3"); got != replyTaken {
		t.Fatalf("END T3: %q", got)
	}
	var rec3 map[string]any
	for {
		r := readNextRecord(t, k, a)
		if r["kind"] == "background" {
			if r["afterCut"] != true {
				t.Fatalf("B1 gap background missing afterCut:true: %v", r)
			}
			continue
		}
		rec3 = r
		break
	}
	if !reflect.DeepEqual(rec3["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("B1 failed: T3 contaminated = %v, want [tail:set-0/T1] (adopted child)", rec3["contaminated"])
	}

	// T4 is clean once child exited!
	if got := send(t, k.sock, "START set-0/T4"); got != replyStartAskedBy("b1") {
		t.Fatalf("START T4: %q", got)
	}
	get(t, a, "/twin?n=4")
	if got := send(t, k.sock, "END set-0/T4"); got != replyTaken {
		t.Fatalf("END T4: %q", got)
	}
	rec4 := readNextRecord(t, k, a)
	if _, hasCut := rec4["windowCut"]; hasCut {
		t.Fatalf("T4 should be clean, got windowCut: %v", rec4)
	}
	if _, hasCont := rec4["contaminated"]; hasCont {
		t.Fatalf("T4 should be clean, got contaminated: %v", rec4)
	}
}

// TestRealGoApp_B2PreExistingWorkerPoolTailDetected verifies that a job
// dispatched over a channel to a pre-existing init()-started worker goroutine
// is detected when it lingers past END, contaminates T2 while busy, and clears
// cleanly on T3 once the worker returns to idle <-poolJobs!
func TestRealGoApp_B2PreExistingWorkerPoolTailDetected(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")

	// T0: poolWorker is idle -> ZERO false positive!
	rec0 := measure(t, k, a, "set-0/T0", "/twin?n=0")
	if _, hasCut := rec0["windowCut"]; hasCut {
		t.Fatalf("idle pool worker caused false-positive windowCut on T0: %v", rec0)
	}

	// T1: sends job to pre-existing poolWorker blocked on gate "pool1".
	rec1 := measure(t, k, a, "set-0/T1", "/pool-job?gate=pool1")
	if rec1["windowCut"] != true {
		t.Fatalf("B2 failed: T1 windowCut = %v, want true for busy pre-existing poolWorker (rec=%v)", rec1["windowCut"], rec1)
	}
	ling1, _ := rec1["lingeringGoroutines"].([]any)
	if len(ling1) == 0 || !strings.Contains(fmt.Sprint(ling1), "poolProcess") {
		t.Fatalf("T1 lingeringGoroutines = %v, want entry naming poolProcess", ling1)
	}

	// T2: runs while poolWorker is busy, then releases gate "pool1" inside T2.
	if got := send(t, k.sock, "START set-0/T2"); got != replyStartUnasked {
		t.Fatalf("START T2: %q", got)
	}
	get(t, a, "/twin?n=2")
	get(t, a, "/release?gate=pool1&wait=1&fn=poolProcess")
	if got := send(t, k.sock, "END set-0/T2"); got != replyTaken {
		t.Fatalf("END T2: %q", got)
	}
	rec2 := readNextRecord(t, k, a)
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("B2 failed: T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}
	poolLine := asyncLineOf(t, "poolHits.Add(1)")
	if !containsInt(linesOfRecord(t, rec2)[appModule+"/async.go"], poolLine) {
		t.Fatalf("expected poolHits.Add(1) line %d in T2: %v", poolLine, linesOfRecord(t, rec2))
	}

	// T3: poolWorker is back to idle <-poolJobs -> T3 MUST be 100% clean!
	rec3 := measure(t, k, a, "set-0/T3", "/twin?n=3")
	if _, hasCut := rec3["windowCut"]; hasCut {
		t.Fatalf("T3 should be clean after poolWorker returned to idle, got windowCut: %v", rec3)
	}
	if _, hasCont := rec3["contaminated"]; hasCont {
		t.Fatalf("T3 should be clean after poolWorker returned to idle, got contaminated: %v", rec3)
	}
}

// TestRealGoApp_P7RecurringAsyncTailsNeverPolluteBackground reproduces P7
// deterministically: T1 and T2 both spawn recurring go notifyAsync() tails that
// linger past END and execute inside gaps T1->T2 and T2->T3. Asserts:
//   - T1 windowCut:true, T2 windowCut:true + contaminated:["tail:set-0/T1"],
//     T3 contaminated:["tail:set-0/T2"],
//   - EVERY BACKGROUND record emitted for gaps after cut windows carries
//     afterCut:true AND empty executedLinesByFile (safe for both v3 and older v2
//     collectors!),
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

	// T1: spawns notifyAsync blocked on "n1".
	if got := send(t, k.sock, "START set-0/T1"); got != replyStartAskedBy("p7") {
		t.Fatalf("START T1: %q", got)
	}
	get(t, a, "/notify-async?id=1&gate=n1")
	if got := send(t, k.sock, "END set-0/T1"); got != replyTaken {
		t.Fatalf("END T1: %q", got)
	}
	rec1 := readTestRecord("set-0/T1")
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true", rec1["windowCut"])
	}
	// Deterministically release T1's notifyAsync INSIDE gap T1->T2.
	get(t, a, "/release?gate=n1&wait=1&fn=notifyAsync")

	// T2: also spawns notifyAsync blocked on "n2".
	if got := send(t, k.sock, "START set-0/T2"); got != replyStartAskedBy("p7") {
		t.Fatalf("START T2: %q", got)
	}
	get(t, a, "/notify-async?id=2&gate=n2")
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
	// Deterministically release T2's notifyAsync INSIDE gap T2->T3.
	get(t, a, "/release?gate=n2&wait=1&fn=notifyAsync")

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

	// Verify BOTH gap BACKGROUND records carried afterCut:true AND empty executedLinesByFile!
	if len(bgRecords) < 2 {
		t.Fatalf("expected >=2 afterCut BACKGROUND records from P7 gaps, got %d: %v", len(bgRecords), bgRecords)
	}
	for i, bg := range bgRecords {
		if bg["afterCut"] != true {
			t.Fatalf("P7 failed: BACKGROUND[%d] missing afterCut:true: %v", i, bg)
		}
		if ls := linesOfRecord(t, bg); len(ls) != 0 {
			t.Fatalf("P7 failed: afterCut BACKGROUND[%d] carried non-empty executedLinesByFile %v (unsafe for v2 collectors)", i, ls)
		}
	}

	// T4: runs /notify-sync synchronously. Must NOT be falsely contaminated!
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

	k2 := newKeploySide(t)
	bin2 := buildAsyncApp(t, k2)
	aOff := startWithEnv(t, bin2, "KEPLOY_GO_GOROUTINE_TRACKING=off", "KEPLOY_GO_END_WAIT_MS=2000")
	wantOffCaps := "windowCut=false,contaminated=true,classMismatch=false,background=true,asyncExecutors=false,listenerContamination=false"
	wantStartOff := "ACK proto=3 bg=- caps=" + wantOffCaps + "\n"
	wantEndOff := "ACK proto=3 caps=" + wantOffCaps + "\n"

	if got := send(t, k2.sock, "START set-0/K1"); got != wantStartOff {
		t.Fatalf("kill-switch START reply = %q, want %q", got, wantStartOff)
	}
	get(t, aOff, "/gated-async?gate=killswitch")
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
