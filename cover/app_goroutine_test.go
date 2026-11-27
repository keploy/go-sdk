//go:build linux || darwin

package cover

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type countingConn struct {
	net.Conn
}

func (c *countingConn) Read(b []byte) (int, error) {
	return c.Conn.Read(b)
}

type countingListener struct {
	net.Listener
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: c}, nil
}

var (
	fraudHits  atomic.Int64
	notifyHits atomic.Int64
	fastHits   atomic.Int64
	childHits  atomic.Int64
	poolHits   atomic.Int64

	gateMu         sync.Mutex
	gates          = map[string]*asyncGate{}
	poolJobs       = make(chan string, 8)
	consumerMsgs   = make(chan string, 4)
	connectCh      = make(chan struct{})
	backoffReady   = make(chan struct{})
	supConnectCh   = make(chan struct{})
	supReady       = make(chan struct{})
	rdrReady       = make(chan struct{})
	supSteadyCh    = make(chan struct{})
	rdrSteadyCh    = make(chan struct{})
	consumerHits   atomic.Int64
	wrappedAddr    string
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

func handleConsumerMsg(msg string) {
	g := getGate(msg)
	defer close(g.done)
	<-g.release
	if msg != "" {
		consumerHits.Add(1)
	}
}

func backoffConsumer() {
	// Deterministically park in [select] until /consumer-connect closes
	// connectCh, then settle into steady-state <-consumerMsgs (same app-frame
	// depth = 1, zero timer races).
	close(backoffReady)
	select {
	case <-connectCh:
	case <-time.After(time.Hour):
	}
	for msg := range consumerMsgs {
		handleConsumerMsg(msg)
	}
}

func slowFlushHandler(w http.ResponseWriter, r *http.Request) {
	g := getGate(r.URL.Query().Get("gate"))
	defer close(g.done)
	w.Header().Set("Content-Length", "7")
	_, _ = w.Write([]byte("flushed"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-g.release
}

func supervisorReader() {
	close(rdrReady)
	select {
	case <-supConnectCh:
	case <-time.After(time.Hour):
	}
	<-rdrSteadyCh
}

func supervisorLoop() {
	go supervisorReader()
	<-rdrReady
	close(supReady)
	select {
	case <-supConnectCh:
	case <-time.After(time.Hour):
	}
	<-supSteadyCh
}

func waitForFuncState(fnPrefix, wantStatus string) bool {
	buf := make([]byte, 128<<10)
	for i := 0; i < 1500; i++ {
		n := runtime.Stack(buf, true)
		s := string(buf[:n])
		idx := strings.Index(s, fnPrefix)
		if idx >= 0 {
			hdr := strings.LastIndex(s[:idx], "goroutine ")
			if hdr >= 0 && strings.Contains(s[hdr:idx], "["+wantStatus) {
				return true
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func init() {
	go poolWorker()
	if os.Getenv("ENABLE_BACKOFF_CONSUMER") == "1" {
		go backoffConsumer()
	}
	if os.Getenv("ENABLE_SUPERVISOR_PAIR") == "1" {
		go supervisorLoop()
	}
	if os.Getenv("ENABLE_WRAPPED_LISTENER") == "1" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			wrappedAddr = ln.Addr().String()
			go func() { _ = http.Serve(&countingListener{Listener: ln}, nil) }()
		}
	}

	http.HandleFunc("/wrapped-addr", func(w http.ResponseWriter, r *http.Request) {
		if wrappedAddr != "" {
			_ = waitForFuncState("main.(*countingListener).Accept(", "IO wait")
		}
		_ = waitForFuncState("main.poolWorker(", "chan receive")
		fmt.Fprint(w, wrappedAddr)
	})
	http.HandleFunc("/slow-flush", slowFlushHandler)
	http.HandleFunc("/supervisor-wait-backoff", func(w http.ResponseWriter, r *http.Request) {
		<-supReady
		if !waitForFuncState("main.supervisorLoop(", "select") || !waitForFuncState("main.supervisorReader(", "select") {
			http.Error(w, "timed out waiting for supervisor pair to park in select", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "parked")
	})
	http.HandleFunc("/supervisor-connect", func(w http.ResponseWriter, r *http.Request) {
		close(supConnectCh)
		if !waitForFuncState("main.supervisorLoop(", "chan receive") || !waitForFuncState("main.supervisorReader(", "chan receive") {
			http.Error(w, "timed out waiting for supervisor pair to reach chan receive", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "connected")
	})
	http.HandleFunc("/consumer-wait-backoff", func(w http.ResponseWriter, r *http.Request) {
		<-backoffReady
		if !waitForFuncState("main.backoffConsumer(", "select") {
			http.Error(w, "timed out waiting for backoffConsumer to park in select", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "parked")
	})
	http.HandleFunc("/consumer-connect", func(w http.ResponseWriter, r *http.Request) {
		close(connectCh)
		if !waitForFuncState("main.backoffConsumer(", "chan receive") {
			http.Error(w, "timed out waiting for backoffConsumer to reach chan receive", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "connected")
	})
	http.HandleFunc("/consumer-wait-handle", func(w http.ResponseWriter, r *http.Request) {
		if !waitForFuncState("main.handleConsumerMsg(", "chan receive") {
			http.Error(w, "timed out waiting for handleConsumerMsg to block", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "handling")
	})
	http.HandleFunc("/consumer-send", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("gate")
		_ = getGate(name)
		consumerMsgs <- name
		fmt.Fprint(w, "sent")
	})

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
			// Poll runtime.Stack for "<fn>(" (matches only the function's own
			// call frame, never ".func1(" closures or "created by" lines) so
			// teardown is verified deterministically without sleep races.
			fn := r.URL.Query().Get("fn")
			if fn != "" {
				buf := make([]byte, 128<<10)
				needle := []byte(fn + "(")
				exited := false
				for i := 0; i < 1500; i++ {
					n := runtime.Stack(buf, true)
					if !bytes.Contains(buf[:n], needle) {
						exited = true
						break
					}
					time.Sleep(2 * time.Millisecond)
				}
				if !exited {
					http.Error(w, "timed out waiting for "+fn+" to exit", http.StatusInternalServerError)
					return
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

// TestRealGoApp_FraudAlertGoroutineTailAcrossCleanGap verifies deterministically
// on a real -cover -covermode=atomic Go binary that an async goroutine tail
// spanning a clean gap is caught:
//   - T5 calls /orders?id=5&fraud=1&gate=p6, spawning go fraudAlert() blocked on gate.
//   - END T5 waits KEPLOY_GO_END_WAIT_MS=30ms -> fraudAlert is still blocked ->
//     T5 emits windowCut:true + lingeringGoroutines naming fraudAlert.
//   - Gap T5->T6 is CLEAN (fraudAlert stays blocked, 0 gap hits).
//   - T6 runs /orders?id=6 (no fraud!), then releases gate p6 so fraudAlert
//     executes fraudHits.Add(1) INSIDE T6 and exits before END T6.
//   - Assert: T5 has windowCut:true and did NOT hit fraudHits.Add(1), T6 DID
//     hit fraudHits.Add(1) AND has contaminated:["tail:set-0/T5"] (even though
//     gap T5->T6 was clean!), and T7 (after fraudAlert exited) is clean!
func TestRealGoApp_FraudAlertGoroutineTailAcrossCleanGap(t *testing.T) {
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
		t.Fatalf("T5 windowCut = %v, want true (rec=%v)", rec5["windowCut"], rec5)
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
	getOK(t, a, "/release?gate=p6&wait=1&fn=fraudAlert")
	if got := send(t, k.sock, "END set-0/T6"); got != replyTaken {
		t.Fatalf("END T6: %q", got)
	}

	// Gap T5->T6 was clean (zero lines), so T6's own record is next.
	rec6 := readNextRecord(t, k, a)
	if rec6["id"] != "set-0/T6" {
		t.Fatalf("expected clean gap T5->T6 and rec6 next, got %v", rec6)
	}
	if !reflect.DeepEqual(rec6["contaminated"], []any{"tail:set-0/T5"}) {
		t.Fatalf("T6 contaminated = %v, want [tail:set-0/T5] (rec=%v)", rec6["contaminated"], rec6)
	}
	if _, hasCut := rec6["windowCut"]; hasCut {
		t.Fatalf("T6 did not spawn a lingering goroutine but got windowCut: %v", rec6)
	}
	if !containsInt(linesOfRecord(t, rec6)[asyncFile], fraudHitLine) {
		t.Fatalf("setup check: fraudHits.Add(1) line %d did not spill into T6: %v", fraudHitLine, linesOfRecord(t, rec6)[asyncFile])
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
// goroutine in T1 that stays alive across T2 and wakes/exits inside T3 ->
// asserts T1 windowCut:true, T2 and T3 contaminated:["tail:set-0/T1"] (T3 via
// resumeWatch wakeup), and T4 clean!
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

	// T3: goroutine settles onto resumeWatch at START T3, then wakes and executes
	// INSIDE T3 -> resumeWatch MUST fire and mark T3 contaminated tail:set-0/T1!
	if got := send(t, k.sock, "START set-0/T3"); got != replyStartUnasked {
		t.Fatalf("START T3: %q", got)
	}
	get(t, a, "/twin?n=3")
	getOK(t, a, "/release?gate=multi&wait=1&fn=gatedWork")
	if got := send(t, k.sock, "END set-0/T3"); got != replyTaken {
		t.Fatalf("END T3: %q", got)
	}
	rec3 := readNextRecord(t, k, a)
	if !reflect.DeepEqual(rec3["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T3 contaminated = %v, want [tail:set-0/T1]", rec3["contaminated"])
	}
	if _, hasCut := rec3["windowCut"]; hasCut {
		t.Fatalf("T3 unexpectedly marked windowCut: %v", rec3)
	}

	rec4 := measure(t, k, a, "set-0/T4", "/twin?n=4")
	if _, hasCut := rec4["windowCut"]; hasCut {
		t.Fatalf("T4 should be clean, got windowCut: %v", rec4)
	}
	if _, hasCont := rec4["contaminated"]; hasCont {
		t.Fatalf("T4 should be clean, got contaminated: %v", rec4)
	}
}

// TestRealGoApp_ChildSpawnedInGapStaysTracked verifies that if T1's lingering
// goroutine spawns a child goroutine in gap T2->T3 and exits, T3 STILL gets
// contaminated:["tail:set-0/T1"] via Go 1.21+ parent-goid child adoption, and
// T4 is clean once the child exits!
func TestRealGoApp_ChildSpawnedInGapStaysTracked(t *testing.T) {
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
	getOK(t, a, "/release?gate=p1&wait=1&fn=parentSpawnsChildAndExits")

	// T3 starts: parent is DEAD, child is ALIVE -> child MUST be adopted into T1's tail!
	if got := send(t, k.sock, "START set-0/T3"); got != replyStartAskedBy("b1") {
		t.Fatalf("START T3: %q", got)
	}
	get(t, a, "/twin?n=3")
	// Release child INSIDE T3 so child executes childHits.Add(1) inside T3 and exits!
	getOK(t, a, "/release?gate=c1&wait=1&fn=publishChildAlert")
	if got := send(t, k.sock, "END set-0/T3"); got != replyTaken {
		t.Fatalf("END T3: %q", got)
	}
	var rec3 map[string]any
	for {
		r := readNextRecord(t, k, a)
		if r["kind"] == "background" {
			if r["afterCut"] != true {
				t.Fatalf("gap background missing afterCut:true: %v", r)
			}
			continue
		}
		rec3 = r
		break
	}
	if !reflect.DeepEqual(rec3["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T3 contaminated = %v, want [tail:set-0/T1] (adopted child)", rec3["contaminated"])
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

// TestRealGoApp_PreExistingWorkerPoolTailDetected verifies that a job
// dispatched over a channel to a pre-existing init()-started worker goroutine
// is detected when it lingers past END, contaminates T2 while busy, and clears
// cleanly on T3 once the worker returns to idle <-poolJobs!
func TestRealGoApp_PreExistingWorkerPoolTailDetected(t *testing.T) {
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
		t.Fatalf("T1 windowCut = %v, want true for busy pre-existing poolWorker (rec=%v)", rec1["windowCut"], rec1)
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
	getOK(t, a, "/release?gate=pool1&wait=1&fn=poolProcess")
	if got := send(t, k.sock, "END set-0/T2"); got != replyTaken {
		t.Fatalf("END T2: %q", got)
	}
	rec2 := readNextRecord(t, k, a)
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
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

// TestRealGoApp_RecurringAsyncTailsNeverPolluteBackground verifies
// deterministically: T1 and T2 both spawn recurring go notifyAsync() tails that
// linger past END and execute inside gaps T1->T2 and T2->T3. Asserts:
//   - T1 windowCut:true, T2 windowCut:true + contaminated:["tail:set-0/T1"],
//     T3 contaminated:["tail:set-0/T2"],
//   - EVERY BACKGROUND record emitted for gaps after cut windows carries
//     afterCut:true AND empty executedLinesByFile (safe for both v3 and older v2
//     collectors!),
//   - T4 running /notify-sync is completely clean (afterCut gaps did NOT
//     pollute everGap!).
func TestRealGoApp_RecurringAsyncTailsNeverPolluteBackground(t *testing.T) {
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
	getOK(t, a, "/release?gate=n1&wait=1&fn=notifyAsync")

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
	getOK(t, a, "/release?gate=n2&wait=1&fn=notifyAsync")

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
		t.Fatalf("expected >=2 afterCut BACKGROUND records from recurring tail gaps, got %d: %v", len(bgRecords), bgRecords)
	}
	for i, bg := range bgRecords {
		if bg["afterCut"] != true {
			t.Fatalf("BACKGROUND[%d] missing afterCut:true: %v", i, bg)
		}
		if ls := linesOfRecord(t, bg); len(ls) != 0 {
			t.Fatalf("afterCut BACKGROUND[%d] carried non-empty executedLinesByFile %v (unsafe for v2 collectors)", i, ls)
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
		t.Fatalf("T4 falsely contaminated by afterCut tail gap: %v", rec4)
	}
}

func TestRealGoApp_UnclosedWindowContaminatesNextTest(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=50")
	// Warm the server before U1 so main.main is past os.Stdout.Sync() and
	// parked in http.Serve (and init's poolWorker is parked) before START U1.
	get(t, a, "/twin?n=0")

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

func getOK(t *testing.T, a app, path string) {
	t.Helper()
	res, err := http.Get("http://" + a.addr + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned status %d: %s", path, res.StatusCode, body)
	}
}

// TestRealGoApp_NewKeepAliveSlowFlushClearsWhenHandlerReturns verifies end-to-end
// on a real -cover binary that a slow-flushing handler on a BRAND NEW HTTP/1
// keep-alive connection triggers windowCut+contaminated:["window-cut"] on T1,
// marks T2 tail:T1, and clears on T3 once the handler returns even though the
// same keep-alive connection stays open and reused!
func TestRealGoApp_NewKeepAliveSlowFlushClearsWhenHandlerReturns(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")

	tr := &http.Transport{MaxIdleConns: 1, MaxIdleConnsPerHost: 1, DisableKeepAlives: false}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr}

	// T1 opens a brand-new keep-alive TCP connection to /slow-flush (writes
	// Content-Length body + Flusher.Flush, then stays inside slowFlushHandler).
	if got := send(t, k.sock, "START set-0/T1"); got != replyStartUnasked {
		t.Fatalf("START T1: %q", got)
	}
	resp, err := client.Get("http://" + a.addr + "/slow-flush?gate=sf1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got := send(t, k.sock, "END set-0/T1"); got != replyTaken {
		t.Fatalf("END T1: %q", got)
	}
	rec1 := readNextRecord(t, k, a)
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true (rec=%v)", rec1["windowCut"], rec1)
	}
	if !reflect.DeepEqual(rec1["contaminated"], []any{"window-cut"}) {
		t.Fatalf("T1 contaminated = %v, want [window-cut]", rec1["contaminated"])
	}

	// Release slowFlushHandler so conn.serve returns to idle keep-alive while
	// keeping the TCP connection open!
	getOK(t, a, "/release?gate=sf1&wait=1&fn=slowFlushHandler")

	// T2 reuses that SAME keep-alive connection (immediate post-cut boundary -> tail:T1).
	if got := send(t, k.sock, "START set-0/T2"); got != replyStartUnasked {
		t.Fatalf("START T2: %q", got)
	}
	var reused2 bool
	req2, _ := http.NewRequest("GET", "http://"+a.addr+"/twin?n=2", nil)
	req2 = req2.WithContext(httptrace.WithClientTrace(req2.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused2 = info.Reused },
	}))
	r2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r2.Body)
	_ = r2.Body.Close()
	if !reused2 {
		t.Fatal("expected T2 to reuse the same keep-alive connection from T1")
	}
	if got := send(t, k.sock, "END set-0/T2"); got != replyTaken {
		t.Fatalf("END T2: %q", got)
	}
	rec2 := readNextRecord(t, k, a)
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}

	// T3 reuses the SAME keep-alive connection -> MUST be 100% clean!
	if got := send(t, k.sock, "START set-0/T3"); got != replyStartUnasked {
		t.Fatalf("START T3: %q", got)
	}
	req3, _ := http.NewRequest("GET", "http://"+a.addr+"/twin?n=3", nil)
	r3, err := client.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r3.Body)
	_ = r3.Body.Close()
	if got := send(t, k.sock, "END set-0/T3"); got != replyTaken {
		t.Fatalf("END T3: %q", got)
	}
	rec3 := readNextRecord(t, k, a)
	if _, hasCut := rec3["windowCut"]; hasCut {
		t.Fatalf("T3 got windowCut on idle keep-alive conn: %v", rec3)
	}
	if _, hasCont := rec3["contaminated"]; hasCont {
		t.Fatalf("T3 falsely contaminated on idle keep-alive conn: %v", rec3)
	}
}

// TestRealGoApp_BackoffConsumerRebaselinesAndResumeWatchCatchesWakeup verifies
// end-to-end on a real -cover binary that a pre-existing consumer transitioning
// from startup [select] backoff to steady-state <-consumerMsgs settles cleanly
// onto a one-time resume watch (T3 clean while parked) and catches an in-window
// wakeup (T4 contaminated tail:T1).
func TestRealGoApp_BackoffConsumerRebaselinesAndResumeWatchCatchesWakeup(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30", "ENABLE_BACKOFF_CONSUMER=1")

	// Deterministically verify backoffConsumer is parked in [select] before START T1!
	getOK(t, a, "/consumer-wait-backoff")

	// T1: backoffConsumer transitions from [select] backoff to [chan receive] <-consumerMsgs.
	if got := send(t, k.sock, "START set-0/T1"); got != replyStartUnasked {
		t.Fatalf("START T1: %q", got)
	}
	getOK(t, a, "/consumer-connect")
	if got := send(t, k.sock, "END set-0/T1"); got != replyTaken {
		t.Fatalf("END T1: %q", got)
	}
	rec1 := readNextRecord(t, k, a)
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true (rec=%v)", rec1["windowCut"], rec1)
	}

	// T2: consumer parked at <-consumerMsgs (streak 1 -> tail:T1).
	rec2 := measure(t, k, a, "set-0/T2", "/twin?n=2")
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}

	// T3: consumer STILL parked at <-consumerMsgs (streak 2 -> moves onto resume watch; T3 is clean!).
	rec3 := measure(t, k, a, "set-0/T3", "/twin?n=3")
	if _, hasCut := rec3["windowCut"]; hasCut {
		t.Fatalf("T3 should be clean while consumer parked on resume watch, got windowCut: %v", rec3)
	}
	if _, hasCont := rec3["contaminated"]; hasCont {
		t.Fatalf("T3 should be clean while consumer parked on resume watch, got contaminated: %v", rec3)
	}

	// T4: message wakes consumer off <-consumerMsgs -> resume watch MUST fire on T4!
	rec4 := measure(t, k, a, "set-0/T4", "/consumer-send?gate=cmsg1")
	if !reflect.DeepEqual(rec4["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("resume watch e2e failed: T4 contaminated = %v, want [tail:set-0/T1] (rec=%v)", rec4["contaminated"], rec4)
	}
	if rec4["windowCut"] != true {
		t.Fatalf("T4 windowCut = %v, want true while consumer processes cmsg1", rec4["windowCut"])
	}
	getOK(t, a, "/release?gate=cmsg1&wait=1")
}

// TestRealGoApp_ResumeWatchGapWakeAfterCutAndReattach verifies end-to-end on a
// real -cover binary that when a resume-watched consumer wakes during the gap
// T3->T4:
//   - gap T3->T4 emits BACKGROUND with afterCut:true and empty executedLinesByFile,
//   - T4 gets contaminated:["tail:set-0/T1"] and the consumer is re-attached,
//   - T5 stays contaminated:["tail:set-0/T1"] while the consumer remains blocked.
func TestRealGoApp_ResumeWatchGapWakeAfterCutAndReattach(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30", "ENABLE_BACKOFF_CONSUMER=1")

	if got := send(t, k.sock, backgroundAsk+" rwgap"); got != replyTaken {
		t.Fatalf("BACKGROUND: %q", got)
	}
	getOK(t, a, "/consumer-wait-backoff")

	// T1: connect consumer onto <-consumerMsgs (windowCut).
	if got := send(t, k.sock, "START set-0/T1"); got != replyStartAskedBy("rwgap") {
		t.Fatalf("START T1: %q", got)
	}
	getOK(t, a, "/consumer-connect")
	if got := send(t, k.sock, "END set-0/T1"); got != replyTaken {
		t.Fatalf("END T1: %q", got)
	}
	_ = readNextRecord(t, k, a)

	// T2 (streak 1) & T3 (streak 2 -> moves onto resume watch).
	for _, id := range []string{"set-0/T2", "set-0/T3"} {
		if got := send(t, k.sock, "START "+id); got != replyStartAskedBy("rwgap") {
			t.Fatalf("START %s: %q", id, got)
		}
		getOK(t, a, "/twin?n=1")
		if got := send(t, k.sock, "END "+id); got != replyTaken {
			t.Fatalf("END %s: %q", id, got)
		}
		_ = readNextRecord(t, k, a)
	}

	// Gap T3->T4: send a message to wake the watched consumer and deterministically
	// confirm it entered handleConsumerMsg before START T4.
	getOK(t, a, "/consumer-send?gate=gapmsg")
	getOK(t, a, "/consumer-wait-handle")

	// T4 starts: gap T3->T4 MUST emit afterCut:true empty BACKGROUND, and T4 gets tail:T1!
	if got := send(t, k.sock, "START set-0/T4"); got != replyStartAskedBy("rwgap") {
		t.Fatalf("START T4: %q", got)
	}
	getOK(t, a, "/twin?n=4")
	if got := send(t, k.sock, "END set-0/T4"); got != replyTaken {
		t.Fatalf("END T4: %q", got)
	}
	var gotAfterCutBG bool
	var rec4 map[string]any
	for {
		r := readNextRecord(t, k, a)
		if r["kind"] == "background" {
			if r["afterCut"] != true {
				t.Fatalf("gap-wake BACKGROUND missing afterCut:true: %v", r)
			}
			if ls := linesOfRecord(t, r); len(ls) != 0 {
				t.Fatalf("gap-wake BACKGROUND must have empty executedLinesByFile: %v", ls)
			}
			gotAfterCutBG = true
			continue
		}
		rec4 = r
		break
	}
	if !gotAfterCutBG {
		t.Fatal("expected an afterCut:true BACKGROUND record from gap T3->T4 wakeup")
	}
	if !reflect.DeepEqual(rec4["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T4 contaminated = %v, want [tail:set-0/T1]", rec4["contaminated"])
	}

	// T5 starts while consumer is STILL blocked on gapmsg -> MUST still be tail:T1 via re-attach!
	if got := send(t, k.sock, "START set-0/T5"); got != replyStartAskedBy("rwgap") {
		t.Fatalf("START T5: %q", got)
	}
	getOK(t, a, "/twin?n=5")
	if got := send(t, k.sock, "END set-0/T5"); got != replyTaken {
		t.Fatalf("END T5: %q", got)
	}
	rec5 := readNextRecord(t, k, a)
	if !reflect.DeepEqual(rec5["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T5 contaminated = %v, want [tail:set-0/T1] via re-attached watch target", rec5["contaminated"])
	}
	getOK(t, a, "/release?gate=gapmsg&wait=1")
}

// TestRealGoApp_CoTargetSupervisorAndReaderWatchStaysClean verifies end-to-end
// on a real -cover binary that a pre-existing supervisor goroutine and its
// pre-existing child reader goroutine (both moving from [select] backoff to
// steady [chan receive] in T1) move cleanly onto resume watches at T3 without
// the child triggering a spurious watch fire or permanent mustExit adoption!
func TestRealGoApp_CoTargetSupervisorAndReaderWatchStaysClean(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30", "ENABLE_SUPERVISOR_PAIR=1")

	getOK(t, a, "/supervisor-wait-backoff")

	// T1: both supervisorLoop and supervisorReader transition from [select] to [chan receive].
	if got := send(t, k.sock, "START set-0/T1"); got != replyStartUnasked {
		t.Fatalf("START T1: %q", got)
	}
	getOK(t, a, "/supervisor-connect")
	if got := send(t, k.sock, "END set-0/T1"); got != replyTaken {
		t.Fatalf("END T1: %q", got)
	}
	rec1 := readNextRecord(t, k, a)
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true", rec1["windowCut"])
	}

	// T2: streak 1 -> tail:T1.
	rec2 := measure(t, k, a, "set-0/T2", "/twin?n=2")
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}

	// T3 & T4: both moved onto watches -> MUST be 100% clean (no spurious watch fire)!
	for _, id := range []string{"set-0/T3", "set-0/T4"} {
		rec := measure(t, k, a, id, "/twin?n=3")
		if _, hasCut := rec["windowCut"]; hasCut {
			t.Fatalf("%s unexpectedly got windowCut: %v", id, rec)
		}
		if _, hasCont := rec["contaminated"]; hasCont {
			t.Fatalf("%s falsely contaminated by pre-existing child reader: %v", id, rec)
		}
	}
}

// TestRealGoApp_WrappedConnKeepAlive verifies end-to-end on a real -cover
// binary whose net.Listener wraps net.Conn with an app-package Read method
// (main.(*countingConn).Read):
//   - T1..T4 on wrapped keep-alive conn #1 -> ALL 100% CLEAN (even T1!) with END < 150ms,
//   - T5 on brand new wrapped keep-alive conn #2 -> 100% CLEAN immediately,
//   - T6 triggers /slow-flush on wrapped conn #1 -> honestly caught as windowCut:true,
//   - T7 while /slow-flush is still blocked -> contaminated:["tail:set-0/T6"] (exact origin),
//   - After /release + closing idle conn #2 -> T8 & T9 are 100% CLEAN (zero close-conn false marks)!
func TestRealGoApp_WrappedConnKeepAlive(t *testing.T) {
	k := newKeploySide(t)
	bin := buildAsyncApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=200", "ENABLE_WRAPPED_LISTENER=1")

	resp, err := http.Get("http://" + a.addr + "/wrapped-addr")
	if err != nil {
		t.Fatal(err)
	}
	rawAddr, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	wAddr := strings.TrimSpace(string(rawAddr))
	if wAddr == "" {
		t.Fatal("wrapped listener did not start")
	}

	makeClient := func() (*http.Client, *http.Transport) {
		tr := &http.Transport{MaxIdleConns: 1, MaxIdleConnsPerHost: 1, DisableKeepAlives: false}
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{Transport: tr}, tr
	}
	c1, _ := makeClient()
	c2, tr2 := makeClient()

	doReq := func(c *http.Client, path string) bool {
		var reused bool
		req, err := http.NewRequest("GET", "http://"+wAddr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		}))
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		return reused
	}

	runWrapped := func(id string, c *http.Client, path string) (map[string]any, bool, time.Duration) {
		if got := send(t, k.sock, "START "+id); got != replyStartUnasked {
			t.Fatalf("START %s: %q", id, got)
		}
		reused := doReq(c, path)
		began := time.Now()
		if got := send(t, k.sock, "END "+id); got != replyTaken {
			t.Fatalf("END %s: %q", id, got)
		}
		return readNextRecord(t, k, a), reused, time.Since(began)
	}

	// T1..T4: conn #1 (first request + 3 keep-alive reuses) -> ALL 100% CLEAN immediately!
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("set-0/T%d", i)
		rec, reused, elapsed := runWrapped(id, c1, fmt.Sprintf("/twin?n=%d", i))
		if i > 1 && !reused {
			t.Fatalf("expected %s to reuse wrapped conn #1", id)
		}
		if elapsed > 150*time.Millisecond {
			t.Fatalf("END %s took %v (false-positive wait on idle wrapped keep-alive conn?)", id, elapsed)
		}
		if _, hasCut := rec["windowCut"]; hasCut {
			t.Fatalf("%s got false-positive windowCut: %v", id, rec)
		}
		if _, hasCont := rec["contaminated"]; hasCont {
			t.Fatalf("%s got false-positive contaminated: %v", id, rec)
		}
	}

	// T5: brand new wrapped conn #2 via c2 -> 100% CLEAN immediately!
	rec5, reused5, elapsed5 := runWrapped("set-0/T5", c2, "/twin?n=5")
	if reused5 {
		t.Fatal("expected T5 on c2 to open a brand new wrapped conn #2")
	}
	if elapsed5 > 150*time.Millisecond {
		t.Fatalf("END T5 took %v (false-positive wait on new wrapped keep-alive conn #2?)", elapsed5)
	}
	if _, hasCut := rec5["windowCut"]; hasCut {
		t.Fatalf("T5 got false-positive windowCut on conn #2: %v", rec5)
	}
	if _, hasCont := rec5["contaminated"]; hasCont {
		t.Fatalf("T5 got false-positive contaminated on conn #2: %v", rec5)
	}

	// T6: active /slow-flush handler on conn #1 MUST be caught honestly as windowCut:true!
	rec6, _, _ := runWrapped("set-0/T6", c1, "/slow-flush?gate=wrapflush")
	if rec6["windowCut"] != true {
		t.Fatalf("active handler check failed: T6 missed /slow-flush on wrapped conn #1: %v", rec6)
	}

	// T7 while /slow-flush is still blocked -> contaminated:["tail:set-0/T6"] (exact origin!).
	rec7, _, _ := runWrapped("set-0/T7", c2, "/twin?n=7")
	if !reflect.DeepEqual(rec7["contaminated"], []any{"tail:set-0/T6"}) {
		t.Fatalf("T7 contaminated = %v, want [tail:set-0/T6]", rec7["contaminated"])
	}

	// Release /slow-flush and wait for handler exit; also close idle conn #2!
	getOK(t, a, "/release?gate=wrapflush&wait=1&fn=slowFlushHandler")
	tr2.CloseIdleConnections()

	// T8: back at idle readRequest -> 100% CLEAN (and closing idle conn #2 causes zero false tail marks)!
	rec8, _, _ := runWrapped("set-0/T8", c1, "/twin?n=8")
	if _, hasCut := rec8["windowCut"]; hasCut {
		t.Fatalf("T8 unexpectedly got windowCut after slow-flush finished: %v", rec8)
	}
	if _, hasCont := rec8["contaminated"]; hasCont {
		t.Fatalf("T8 unexpectedly got contaminated after slow-flush finished & conn #2 closed: %v", rec8)
	}
}

// Vendored third-party async writer mimicking kafka-go Writer{Async:true,
// Completion} / sarama AsyncProducer (uncovered third-party module).
const deltaAsyncLib = `package asynclib

import (
	"sync"
)

type Writer struct {
	Completion func(msgs []string)
	once       sync.Once
	q          chan string
	flushCh    chan chan struct{}
}

func (w *Writer) ensure() {
	w.once.Do(func() {
		w.q = make(chan string)
		w.flushCh = make(chan chan struct{})
		go w.run()
	})
}

func (w *Writer) WriteMessages(m string) {
	w.ensure()
	w.q <- m
}

func (w *Writer) Flush() {
	w.ensure()
	done := make(chan struct{})
	w.flushCh <- done
	<-done
}

func (w *Writer) run() {
	var batch []string
	for {
		select {
		case m := <-w.q:
			batch = append(batch, m)
		case done := <-w.flushCh:
			msgs := batch
			batch = nil
			w.Completion(msgs)
			close(done)
		}
	}
}

// Call stands in for a blocking third-party driver or RPC call (e.g. DB driver,
// client.Do) that parks solely in uncovered third-party library frames.
func Call(gate <-chan struct{}) {
	<-gate
}
`

const deltaProbeApp = `package main

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"example.com/asynclib"
)

var (
	deliveredFraud    atomic.Int64
	auditedFraud      atomic.Int64
	flushedFraud      atomic.Int64
	jobFraud          atomic.Int64
	shallowFraud      atomic.Int64
	shallowBG         atomic.Int64
	janitorTicks      atomic.Int64
	sleepJanitorTicks atomic.Int64
	periodicTicks     atomic.Int64
	gapPings          atomic.Int64

	batchQ         = make(chan string)
	batchFlushCh   = make(chan chan struct{})
	jobQ           = make(chan deltaJob, 64)
	shallowQ       = &appQueue{ch: make(chan deltaJob, 64)}
	periodicTickCh = make(chan chan struct{})
	auditDoneMu    sync.Mutex
	auditDone      chan struct{}

	deltaGateMu   sync.Mutex
	deltaGates    = map[string]chan struct{}{}
	lazyOnce      sync.Once
	sleepLazyOnce sync.Once
	periodicOnce  sync.Once
)

type deltaJob struct {
	kind string
	gate chan struct{}
}

// appQueue wraps a channel receive so idle shallowWorker parks inside Pop
// (2 app frames), one app frame deeper than its mid-job call into asynclib.Call
// (1 app frame).
type appQueue struct {
	ch chan deltaJob
}

func (q *appQueue) Pop() deltaJob { return <-q.ch }

func onDelivered(msgs []string) {
	for _, m := range msgs {
		if m == "fraud" {
			deliveredFraud.Add(1)
		}
	}
}

var asyncWriter = &asynclib.Writer{Completion: onDelivered}

func auditFraud(id string) {
	if id != "" {
		auditedFraud.Add(1)
	}
}

func flushBatch(b []string) {
	for _, m := range b {
		if m == "fraud" {
			flushedFraud.Add(1)
		}
	}
}

func batcher() {
	var batch []string
	for {
		select {
		case m := <-batchQ:
			batch = append(batch, m)
		case done := <-batchFlushCh:
			b := batch
			batch = nil
			flushBatch(b)
			close(done)
		}
	}
}

func finishJob(kind string) {
	if kind == "fraud" {
		jobFraud.Add(1)
	}
}

func processJob(j deltaJob) {
	<-j.gate
	finishJob(j.kind)
}

func jobWorker() {
	for j := range jobQ {
		processJob(j)
	}
}

func finishShallowJob(kind string) {
	if kind == "fraud" {
		shallowFraud.Add(1)
	} else {
		shallowBG.Add(1)
	}
}

func shallowWorker() {
	for {
		j := shallowQ.Pop()
		asynclib.Call(j.gate)
		finishShallowJob(j.kind)
	}
}

func janitor() {
	t := time.NewTicker(time.Hour)
	for range t.C {
		janitorTicks.Add(1)
	}
}

func sleepJanitor() {
	for {
		time.Sleep(time.Hour)
		sleepJanitorTicks.Add(1)
	}
}

func waitShallowWorker(inCall bool) bool {
	buf := make([]byte, 256<<10)
	for i := 0; i < 1500; i++ {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if !strings.Contains(g, "main.shallowWorker(") {
				continue
			}
			if inCall && strings.Contains(g, "example.com/asynclib.Call") && strings.Contains(g, "[chan receive") {
				return true
			}
			if !inCall && strings.Contains(g, "main.(*appQueue).Pop") && strings.Contains(g, "[chan receive") {
				return true
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func periodicLoop() {
	for done := range periodicTickCh {
		periodicTicks.Add(1)
		close(done)
	}
}

func waitParkedDelta(fn, status string) bool {
	buf := make([]byte, 256<<10)
	for i := 0; i < 1500; i++ {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, fn) && strings.Contains(g, "["+status) {
				return true
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func waitNotInFrame(fn string) bool {
	buf := make([]byte, 256<<10)
	for i := 0; i < 1500; i++ {
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), fn) {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func init() {
	go batcher()
	go jobWorker()
	go shallowWorker()
	http.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "plain")
	})
	http.HandleFunc("/gap-ping", func(w http.ResponseWriter, r *http.Request) {
		gapPings.Add(1)
		fmt.Fprint(w, "gap-ping")
	})
	http.HandleFunc("/produce", func(w http.ResponseWriter, r *http.Request) {
		asyncWriter.WriteMessages(r.URL.Query().Get("kind"))
		if !waitParkedDelta("example.com/asynclib.(*Writer).run(", "select") {
			http.Error(w, "writer did not re-park", 500)
			return
		}
		fmt.Fprint(w, "produced")
	})
	http.HandleFunc("/flush-writer", func(w http.ResponseWriter, r *http.Request) {
		asyncWriter.Flush()
		fmt.Fprint(w, "flushed-writer")
	})
	http.HandleFunc("/audit", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		done := make(chan struct{})
		auditDoneMu.Lock()
		auditDone = done
		auditDoneMu.Unlock()
		time.AfterFunc(100*time.Millisecond, func() {
			auditFraud(id)
			close(done)
		})
		fmt.Fprint(w, "audit-scheduled")
	})
	http.HandleFunc("/wait-audit", func(w http.ResponseWriter, r *http.Request) {
		auditDoneMu.Lock()
		done := auditDone
		auditDoneMu.Unlock()
		if done != nil {
			<-done
		}
		fmt.Fprint(w, "audit-done")
	})
	http.HandleFunc("/enqueue", func(w http.ResponseWriter, r *http.Request) {
		batchQ <- r.URL.Query().Get("kind")
		if !waitParkedDelta("main.batcher(", "select") {
			http.Error(w, "batcher did not re-park", 500)
			return
		}
		fmt.Fprint(w, "enqueued")
	})
	http.HandleFunc("/flush-batch", func(w http.ResponseWriter, r *http.Request) {
		done := make(chan struct{})
		batchFlushCh <- done
		<-done
		fmt.Fprint(w, "flushed-batch")
	})
	http.HandleFunc("/job", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("gate")
		g := make(chan struct{})
		deltaGateMu.Lock()
		deltaGates[name] = g
		deltaGateMu.Unlock()
		jobQ <- deltaJob{kind: r.URL.Query().Get("kind"), gate: g}
		if rel := r.URL.Query().Get("release"); rel != "" {
			deltaGateMu.Lock()
			rg := deltaGates[rel]
			deltaGateMu.Unlock()
			close(rg)
		}
		if r.URL.Query().Get("waitpark") == "1" && !waitParkedDelta("main.processJob(", "chan receive") {
			http.Error(w, "worker did not park in processJob", 500)
			return
		}
		fmt.Fprint(w, "job")
	})
	http.HandleFunc("/open", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("gate")
		deltaGateMu.Lock()
		g := deltaGates[name]
		deltaGateMu.Unlock()
		close(g)
		if r.URL.Query().Get("waitidle") == "1" && !waitNotInFrame("main.processJob(") {
			http.Error(w, "worker did not leave processJob", 500)
			return
		}
		fmt.Fprint(w, "opened")
	})
	http.HandleFunc("/lazy", func(w http.ResponseWriter, r *http.Request) {
		lazyOnce.Do(func() {
			started := make(chan struct{})
			go func() {
				close(started)
				janitor()
			}()
			<-started
		})
		if !waitParkedDelta("main.janitor(", "chan receive") {
			http.Error(w, "janitor did not park", 500)
			return
		}
		fmt.Fprint(w, "lazy")
	})
	http.HandleFunc("/start-periodic", func(w http.ResponseWriter, r *http.Request) {
		periodicOnce.Do(func() {
			started := make(chan struct{})
			go func() {
				close(started)
				periodicLoop()
			}()
			<-started
		})
		if !waitParkedDelta("main.periodicLoop(", "chan receive") {
			http.Error(w, "periodicLoop did not park", 500)
			return
		}
		fmt.Fprint(w, "periodic-started")
	})
	http.HandleFunc("/tick-periodic", func(w http.ResponseWriter, r *http.Request) {
		done := make(chan struct{})
		periodicTickCh <- done
		<-done
		if !waitParkedDelta("main.periodicLoop(", "chan receive") {
			http.Error(w, "periodicLoop did not re-park", 500)
			return
		}
		fmt.Fprint(w, "periodic-ticked")
	})
	http.HandleFunc("/shallow-wait-idle", func(w http.ResponseWriter, r *http.Request) {
		if !waitShallowWorker(false) {
			http.Error(w, "shallowWorker did not park in Pop", 500)
			return
		}
		fmt.Fprint(w, "shallow-idle")
	})
	http.HandleFunc("/shallow-job", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("gate")
		g := make(chan struct{})
		deltaGateMu.Lock()
		deltaGates[name] = g
		deltaGateMu.Unlock()
		before := shallowBG.Load()
		shallowQ.ch <- deltaJob{kind: r.URL.Query().Get("kind"), gate: g}
		if rel := r.URL.Query().Get("release"); rel != "" {
			deltaGateMu.Lock()
			rg := deltaGates[rel]
			deltaGateMu.Unlock()
			close(rg)
			for i := 0; i < 1500 && shallowBG.Load() == before; i++ {
				time.Sleep(time.Millisecond)
			}
		}
		if !waitShallowWorker(true) {
			http.Error(w, "shallowWorker did not park in asynclib.Call", 500)
			return
		}
		fmt.Fprint(w, "shallow-job")
	})
	http.HandleFunc("/shallow-open", func(w http.ResponseWriter, r *http.Request) {
		deltaGateMu.Lock()
		g := deltaGates[r.URL.Query().Get("gate")]
		deltaGateMu.Unlock()
		close(g)
		if !waitShallowWorker(false) {
			http.Error(w, "shallowWorker did not return to Pop", 500)
			return
		}
		fmt.Fprint(w, "shallow-opened")
	})
	http.HandleFunc("/lazy-sleep", func(w http.ResponseWriter, r *http.Request) {
		sleepLazyOnce.Do(func() {
			started := make(chan struct{})
			go func() {
				close(started)
				sleepJanitor()
			}()
			<-started
		})
		if !waitParkedDelta("main.sleepJanitor(", "sleep") {
			http.Error(w, "sleepJanitor did not park", 500)
			return
		}
		fmt.Fprint(w, "lazy-sleep")
	})
}
`

func buildDeltaProbeApp(t *testing.T, k *keploySide) string {
	t.Helper()
	hook := hookCopy(t, k.sock, k.data)
	mod := appModuleDir(t)
	files := map[string]string{
		"go.mod":                             "module " + appModule + "\n\ngo 1.21\n\nrequire example.com/asynclib v1.0.0\n",
		"delta.go":                           deltaProbeApp,
		"vendor/modules.txt":                 "# example.com/asynclib v1.0.0\n## explicit; go 1.21\nexample.com/asynclib\n",
		"vendor/example.com/asynclib/lib.go": deltaAsyncLib,
	}
	for name, body := range files {
		p := filepath.Join(mod, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin, _ := stage(t, hook, mod, nil)
	return bin
}

func deltaLineOf(t *testing.T, needle string) int {
	t.Helper()
	for i, l := range strings.Split(deltaProbeApp, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("deltaProbeApp has no line holding %q", needle)
	return 0
}

// TestRealGoApp_DeferredAsyncScopeLimitsDeclareAsyncExecutorsFalse builds a real
// -cover -covermode=atomic app with a vendored uncovered third-party async
// writer and pins all four deferred-async scope limits:
//   - (a) library-owned third-party async writer (kafka-go/sarama style)
//   - (b) dormant time.AfterFunc timer firing after END
//   - (c) pre-existing app batcher/debouncer re-parking at the same idle sig
//   - (d) test-spawned periodic loop settled onto a resumeWatch whose later
//     tick runs and re-parks at the same signature between snapshots
//
// In all four patterns, a deferred line lands in a later test without
// goroutine marks, AND every START/END reply honestly declares
// asyncExecutors=false so Go removal eligibility relies on a collector-side
// isolation check rather than SDK capabilities alone.
func TestRealGoApp_DeferredAsyncScopeLimitsDeclareAsyncExecutorsFalse(t *testing.T) {
	k := newKeploySide(t)
	bin := buildDeltaProbeApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=20")
	deltaFile := appModule + "/delta.go"

	runWithCapsCheck := func(id string, paths ...string) map[string]any {
		t.Helper()
		startAck := send(t, k.sock, "START "+id)
		if startAck != replyStartUnasked || !strings.Contains(startAck, "asyncExecutors=false") {
			t.Fatalf("START %s reply = %q, want %q with asyncExecutors=false", id, startAck, replyStartUnasked)
		}
		for _, p := range paths {
			getOK(t, a, p)
		}
		endAck := send(t, k.sock, "END "+id)
		if endAck != replyTaken || !strings.Contains(endAck, "asyncExecutors=false") {
			t.Fatalf("END %s reply = %q, want %q with asyncExecutors=false", id, endAck, replyTaken)
		}
		return readNextRecord(t, k, a)
	}

	// Warm up baseline.
	_ = runWithCapsCheck("set-0/warm", "/plain")

	for _, tc := range []struct {
		name       string
		nPath      string
		n1Path     string
		fraudToken string
	}{
		{"a-library-async-writer", "/produce?kind=fraud", "/flush-writer", "deliveredFraud.Add(1)"},
		{"b-time-afterfunc", "/audit?id=5", "/wait-audit", "auditedFraud.Add(1)"},
		{"c-preexisting-app-batcher", "/enqueue?kind=fraud", "/flush-batch", "flushedFraud.Add(1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := deltaLineOf(t, tc.fraudToken)
			recN := runWithCapsCheck("set-0/N-"+tc.name, tc.nPath)
			recN1 := runWithCapsCheck("set-0/N1-"+tc.name, tc.n1Path)

			inN := containsInt(linesOfRecord(t, recN)[deltaFile], line)
			inN1 := containsInt(linesOfRecord(t, recN1)[deltaFile], line)
			if inN || !inN1 {
				t.Fatalf("%s: expected deferred line %d in N+1 only (inN=%v inN1=%v)", tc.name, line, inN, inN1)
			}
			// Pin documented scope limit: stack snapshots cannot observe these
			// three deferred hand-offs, which is why caps MUST declare asyncExecutors=false.
			if _, cutN := recN["windowCut"]; cutN {
				t.Fatalf("%s: unexpected windowCut on N: %v", tc.name, recN)
			}
			if _, contN1 := recN1["contaminated"]; contN1 {
				t.Fatalf("%s: unexpected contaminated on N+1: %v", tc.name, recN1)
			}
		})
	}

	// (d) Test-spawned periodic loop: T1 cut, T2 tail:T1, T3 settled onto
	// resumeWatch, then T4 fires a tick that completes and re-parks at the same
	// signature before END T4 -> tick line lands in T4 unmarked while caps
	// declare asyncExecutors=false.
	t.Run("d-settled-periodic-loop-tick", func(t *testing.T) {
		tickLine := deltaLineOf(t, "periodicTicks.Add(1)")
		rec1 := runWithCapsCheck("set-0/d-T1", "/start-periodic")
		if rec1["windowCut"] != true {
			t.Fatalf("d-T1 windowCut = %v, want true", rec1["windowCut"])
		}
		rec2 := runWithCapsCheck("set-0/d-T2", "/plain")
		if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/d-T1"}) {
			t.Fatalf("d-T2 contaminated = %v, want [tail:set-0/d-T1]", rec2["contaminated"])
		}
		rec3 := runWithCapsCheck("set-0/d-T3", "/plain")
		if _, cut := rec3["windowCut"]; cut {
			t.Fatalf("d-T3 unexpected windowCut: %v", rec3)
		}
		if _, cont := rec3["contaminated"]; cont {
			t.Fatalf("d-T3 unexpected contaminated: %v", rec3)
		}
		rec4 := runWithCapsCheck("set-0/d-T4", "/tick-periodic")
		if !containsInt(linesOfRecord(t, rec4)[deltaFile], tickLine) {
			t.Fatalf("d-T4 missing periodicTicks.Add(1) line %d: %v", tickLine, linesOfRecord(t, rec4)[deltaFile])
		}
		if _, cut := rec4["windowCut"]; cut {
			t.Fatalf("d-T4 unexpected windowCut: %v", rec4)
		}
		if _, cont := rec4["contaminated"]; cont {
			t.Fatalf("d-T4 unexpected contaminated: %v", rec4)
		}
	})
}

// TestRealGoApp_MidJobWorkerBaselineCatchesCurrentAndNotNext verifies
// on a real -cover binary: a pre-existing jobWorker is mid-job at START N (on
// gap/background work), takes N's fraud job and parks at the exact same line
// in processJob before END N, and finishes the fraud job during N+1. Asserts N
// is honestly marked windowCut:true (using lastQuiescentSigs), N+1 carries
// contaminated:["tail:set-0/N"] WITHOUT being falsely marked windowCut, and
// N+2 is clean.
func TestRealGoApp_MidJobWorkerBaselineCatchesCurrentAndNotNext(t *testing.T) {
	k := newKeploySide(t)
	bin := buildDeltaProbeApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")
	deltaFile := appModule + "/delta.go"
	fraudLine := deltaLineOf(t, "jobFraud.Add(1)")

	// warm establishes jobWorker's idle baseline (for j := range jobQ).
	warm := measure(t, k, a, "set-0/warm", "/plain")
	if _, hasCut := warm["windowCut"]; hasCut {
		t.Fatalf("warm got unexpected windowCut: %v", warm)
	}

	// Gap work: jobWorker takes bg job and parks mid-job in processJob on g0.
	getOK(t, a, "/job?kind=bg&gate=g0&waitpark=1")

	// Test N: enqueues fraud job on g1, releases g0 -> worker finishes bg, picks
	// up fraud job, and parks at the EXACT same <-j.gate line in processJob!
	if got := send(t, k.sock, "START set-0/N"); got != replyStartUnasked {
		t.Fatalf("START N: %q", got)
	}
	getOK(t, a, "/job?kind=fraud&gate=g1&waitpark=1&release=g0")
	if got := send(t, k.sock, "END set-0/N"); got != replyTaken {
		t.Fatalf("END N: %q", got)
	}
	recN := readNextRecord(t, k, a)
	if recN["windowCut"] != true {
		t.Fatalf("N missed mid-job worker (windowCut=%v, rec=%v)", recN["windowCut"], recN)
	}
	lingN, _ := recN["lingeringGoroutines"].([]any)
	if len(lingN) == 0 || !strings.Contains(fmt.Sprint(lingN), "processJob") {
		t.Fatalf("N lingeringGoroutines = %v, want entry naming processJob", lingN)
	}
	if containsInt(linesOfRecord(t, recN)[deltaFile], fraudLine) {
		t.Fatalf("setup: fraud line %d should not have run in N", fraudLine)
	}

	// Test N+1: opens g1 so fraud job runs inside N+1 and worker returns to idle.
	if got := send(t, k.sock, "START set-0/N1"); got != replyStartUnasked {
		t.Fatalf("START N1: %q", got)
	}
	getOK(t, a, "/open?gate=g1&waitidle=1")
	getOK(t, a, "/plain")
	if got := send(t, k.sock, "END set-0/N1"); got != replyTaken {
		t.Fatalf("END N1: %q", got)
	}
	recN1 := readNextRecord(t, k, a)
	if !containsInt(linesOfRecord(t, recN1)[deltaFile], fraudLine) {
		t.Fatalf("setup: fraud line %d did not spill into N+1: %v", fraudLine, linesOfRecord(t, recN1))
	}
	assertTailOnlyOrBetweenTests(t, "N+1", recN1, "tail:set-0/N")

	// Test N+2: worker is back at idle -> 100% CLEAN!
	recN2 := measure(t, k, a, "set-0/N2", "/plain")
	if _, hasCut := recN2["windowCut"]; hasCut {
		t.Fatalf("N+2 unexpectedly got windowCut: %v", recN2)
	}
	if _, hasCont := recN2["contaminated"]; hasCont {
		t.Fatalf("N+2 unexpectedly got contaminated: %v", recN2)
	}
}

// assertTailOnlyOrBetweenTests verifies that rec carries wantTail in
// contaminated, has NO windowCut and NO "window-cut" mark, and that any
// additional contamination entry is solely a legitimate "between-tests:" mark
// for delta.go.
func assertTailOnlyOrBetweenTests(t *testing.T, label string, rec map[string]any, wantTail string) {
	t.Helper()
	if _, hasCut := rec["windowCut"]; hasCut {
		t.Fatalf("%s was falsely marked windowCut: %v", label, rec)
	}
	wantBetween := "between-tests:" + appModule + "/delta.go"
	raw, _ := rec["contaminated"].([]any)
	foundTail := false
	for _, entry := range raw {
		s, _ := entry.(string)
		if s == wantTail {
			foundTail = true
			continue
		}
		if s != wantBetween {
			t.Fatalf("%s contaminated = %v, want %s (and optional %s only)", label, raw, wantTail, wantBetween)
		}
	}
	if !foundTail {
		t.Fatalf("%s contaminated = %v, missing %s", label, raw, wantTail)
	}
}

// assertNonEmptyCleanBG asserts that bgs contains at least one BACKGROUND
// record with afterCut != true and a non-empty executedLinesByFile containing
// wantLine in wantFile.
func assertNonEmptyCleanBG(t *testing.T, label string, bgs []map[string]any, wantFile string, wantLine int) int {
	t.Helper()
	if len(bgs) == 0 {
		t.Fatalf("%s expected at least one BACKGROUND record for non-empty gap, got 0", label)
	}
	clean := 0
	for _, bg := range bgs {
		if bg["afterCut"] == true {
			t.Fatalf("%s gap BACKGROUND unexpectedly stuck afterCut:true: %v", label, bg)
		}
		ls := linesOfRecord(t, bg)
		if len(ls) == 0 || !containsInt(ls[wantFile], wantLine) {
			t.Fatalf("%s gap BACKGROUND missing expected line %d in %s: %v", label, wantLine, wantFile, bg)
		}
		clean++
	}
	return clean
}

// TestRealGoApp_LazyOnceJanitorRebaselinesAfterSecondTest verifies on a real -cover
// binary that a permanent background goroutine lazily started via sync.Once in
// T1 marks T1 windowCut:true and T2 contaminated:["tail:set-0/T1"], then
// settles onto a one-time resumeWatch so T3..T10 are ALL 100% CLEAN AND
// non-empty BACKGROUND records with afterCut=false resume after T3.
func TestRealGoApp_LazyOnceJanitorRebaselinesAfterSecondTest(t *testing.T) {
	k := newKeploySide(t)
	bin := buildDeltaProbeApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")
	deltaFile := appModule + "/delta.go"
	gapPingLine := deltaLineOf(t, "gapPings.Add(1)")

	if got := send(t, k.sock, backgroundAsk+" sf2"); got != replyTaken {
		t.Fatalf("BACKGROUND: %q", got)
	}

	readTest := func(wantID string) (map[string]any, []map[string]any) {
		t.Helper()
		var bgs []map[string]any
		for {
			r := readNextRecord(t, k, a)
			if r["kind"] == "background" {
				bgs = append(bgs, r)
				continue
			}
			if r["id"] != wantID {
				t.Fatalf("got record %v, want id %s", r, wantID)
			}
			return r, bgs
		}
	}

	run := func(id, path string) (map[string]any, []map[string]any) {
		t.Helper()
		if got := send(t, k.sock, "START "+id); got != replyStartAskedBy("sf2") {
			t.Fatalf("START %s: %q", id, got)
		}
		getOK(t, a, path)
		if got := send(t, k.sock, "END "+id); got != replyTaken {
			t.Fatalf("END %s: %q", id, got)
		}
		return readTest(id)
	}

	// warm test before lazy janitor exists.
	_, _ = run("set-0/warm", "/plain")

	// T1: lazily starts sync.Once janitor -> windowCut:true naming janitor.
	rec1, _ := run("set-0/T1", "/lazy")
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true (rec=%v)", rec1["windowCut"], rec1)
	}
	ling1, _ := rec1["lingeringGoroutines"].([]any)
	if len(ling1) == 0 || !strings.Contains(fmt.Sprint(ling1), "janitor") {
		t.Fatalf("T1 lingeringGoroutines = %v, want entry naming janitor", ling1)
	}

	// T2: tainted tail:set-0/T1 (janitor streak 1 across START T2 -> END T2).
	rec2, _ := run("set-0/T2", "/plain")
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}
	if _, hasCut := rec2["windowCut"]; hasCut {
		t.Fatalf("T2 unexpectedly marked windowCut: %v", rec2)
	}

	// T3..T10: janitor moved onto resumeWatch at START T3 -> ALL 8 tests MUST
	// be 100% CLEAN, and app lines executed in gaps before T4..T10 MUST emit
	// non-empty BACKGROUND records with afterCut != true!
	cleanBGs := 0
	for i := 3; i <= 10; i++ {
		if i >= 4 {
			getOK(t, a, "/gap-ping")
		}
		id := fmt.Sprintf("set-0/T%d", i)
		rec, bgs := run(id, "/plain")
		if _, hasCut := rec["windowCut"]; hasCut {
			t.Fatalf("%s got windowCut (lazy janitor not re-baselined): %v", id, rec)
		}
		if _, hasCont := rec["contaminated"]; hasCont {
			t.Fatalf("%s got contaminated (lazy janitor stuck forever): %v", id, rec)
		}
		if i >= 4 {
			cleanBGs += assertNonEmptyCleanBG(t, id, bgs, deltaFile, gapPingLine)
		}
	}
	if cleanBGs < 7 {
		t.Fatalf("expected >= 7 clean non-empty BACKGROUND records after T3, got %d", cleanBGs)
	}
}

// TestRealGoApp_ShallowerMidJobWorkerBaselineAndNoPersistentTaint verifies on a
// real -cover binary a worker that idles inside an app queue helper
// (main.(*appQueue).Pop, 2 app frames) and blocks mid-job inside an uncovered
// vendored driver call made directly from the loop body (1 app frame, strictly
// shallower than idle):
//   - (a) when a gap job is caught mid-call at START N and N's fraud job parks
//     at the exact same driver call before END N, N is honestly marked
//     windowCut:true, N+1 carries tail:set-0/N without a false windowCut, and
//     N+2..N+8 are clean;
//   - (b) when the gap job finishes during N and the worker returns to Pop,
//     N+1..N+8 are clean and normal BACKGROUND resumes;
//   - (c) even when the worker is already mid-call at the very first START
//     (no prior idle history) and returns to its deeper Pop idle during N, it
//     settles onto resumeWatch so N+2..N+8 are clean (no persistent taint).
func TestRealGoApp_ShallowerMidJobWorkerBaselineAndNoPersistentTaint(t *testing.T) {
	deltaFile := appModule + "/delta.go"
	fraudLine := deltaLineOf(t, "shallowFraud.Add(1)")
	gapPingLine := deltaLineOf(t, "gapPings.Add(1)")

	t.Run("same-line-fraud-catches-N-and-not-innocent-N1", func(t *testing.T) {
		k := newKeploySide(t)
		bin := buildDeltaProbeApp(t, k)
		a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")

		// Establish Pop idle baseline in lastQuiescentSigs.
		getOK(t, a, "/shallow-wait-idle")
		if warm := measure(t, k, a, "set-0/warm", "/plain"); warm["windowCut"] != nil {
			t.Fatalf("warm unexpected windowCut: %v", warm)
		}

		// Gap: worker dequeues bg job and parks inside vendored asynclib.Call (1 app frame).
		getOK(t, a, "/shallow-job?kind=bg&gate=g0")

		// N: enqueues fraud job and releases g0 so worker parks at the exact same asynclib.Call line.
		recN := measure(t, k, a, "set-0/N", "/shallow-job?kind=fraud&gate=g1&release=g0")
		if recN["windowCut"] != true {
			t.Fatalf("N missed shallower mid-job worker (windowCut=%v, rec=%v)", recN["windowCut"], recN)
		}
		lingN, _ := recN["lingeringGoroutines"].([]any)
		if len(lingN) == 0 || !strings.Contains(fmt.Sprint(lingN), "shallowWorker") {
			t.Fatalf("N lingeringGoroutines = %v, want entry naming shallowWorker", lingN)
		}
		if containsInt(linesOfRecord(t, recN)[deltaFile], fraudLine) {
			t.Fatalf("setup: shallowFraud line %d should not have run in N", fraudLine)
		}

		// N+1: opens g1 so fraud runs inside N+1 and worker returns to Pop.
		if got := send(t, k.sock, "START set-0/N1"); got != replyStartUnasked {
			t.Fatalf("START N1: %q", got)
		}
		getOK(t, a, "/shallow-open?gate=g1")
		getOK(t, a, "/plain")
		if got := send(t, k.sock, "END set-0/N1"); got != replyTaken {
			t.Fatalf("END N1: %q", got)
		}
		recN1 := readNextRecord(t, k, a)
		if !containsInt(linesOfRecord(t, recN1)[deltaFile], fraudLine) {
			t.Fatalf("setup: shallowFraud line %d did not land in N+1: %v", fraudLine, linesOfRecord(t, recN1))
		}
		assertTailOnlyOrBetweenTests(t, "N+1", recN1, "tail:set-0/N")

		// N+2..N+8: worker is back at Pop -> ALL clean!
		for i := 2; i <= 8; i++ {
			id := fmt.Sprintf("set-0/N%d", i)
			rec := measure(t, k, a, id, "/plain")
			if _, hasCut := rec["windowCut"]; hasCut {
				t.Fatalf("%s unexpectedly marked windowCut: %v", id, rec)
			}
			if _, hasCont := rec["contaminated"]; hasCont {
				t.Fatalf("%s unexpectedly marked contaminated: %v", id, rec)
			}
		}
	})

	t.Run("gap-job-finishes-during-N-no-persistent-taint", func(t *testing.T) {
		k := newKeploySide(t)
		bin := buildDeltaProbeApp(t, k)
		a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")
		if got := send(t, k.sock, backgroundAsk+" sh2"); got != replyTaken {
			t.Fatalf("BACKGROUND: %q", got)
		}

		runBG := func(id, path string) (map[string]any, []map[string]any) {
			t.Helper()
			if got := send(t, k.sock, "START "+id); got != replyStartAskedBy("sh2") {
				t.Fatalf("START %s: %q", id, got)
			}
			getOK(t, a, path)
			if got := send(t, k.sock, "END "+id); got != replyTaken {
				t.Fatalf("END %s: %q", id, got)
			}
			var bgs []map[string]any
			for {
				r := readNextRecord(t, k, a)
				if r["kind"] == "background" {
					bgs = append(bgs, r)
					continue
				}
				if r["id"] != id {
					t.Fatalf("got record %v, want id %s", r, id)
				}
				return r, bgs
			}
		}

		getOK(t, a, "/shallow-wait-idle")
		_, _ = runBG("set-0/warm", "/plain")

		// Gap parks shallowWorker in asynclib.Call; N releases it back to Pop
		// (finishing gap work during N -> N is honestly windowCut, N+1 gets
		// tail:set-0/N, and N+2..N+8 are 100% clean with normal BACKGROUND).
		getOK(t, a, "/shallow-job?kind=bg&gate=g0")
		recN, _ := runBG("set-0/N", "/shallow-open?gate=g0")
		if recN["windowCut"] != true {
			t.Fatalf("N windowCut = %v, want true (gap job finished during N)", recN["windowCut"])
		}
		recN1, _ := runBG("set-0/N1", "/plain")
		assertTailOnlyOrBetweenTests(t, "N+1", recN1, "tail:set-0/N")

		cleanBGs := 0
		for i := 2; i <= 8; i++ {
			if i >= 3 {
				getOK(t, a, "/gap-ping")
			}
			id := fmt.Sprintf("set-0/N%d", i)
			rec, bgs := runBG(id, "/plain")
			if _, hasCut := rec["windowCut"]; hasCut {
				t.Fatalf("%s unexpectedly marked windowCut: %v", id, rec)
			}
			if _, hasCont := rec["contaminated"]; hasCont {
				t.Fatalf("%s persistently tainted: %v", id, rec)
			}
			if i >= 3 {
				cleanBGs += assertNonEmptyCleanBG(t, id, bgs, deltaFile, gapPingLine)
			}
		}
		if cleanBGs < 6 {
			t.Fatalf("expected >= 6 clean non-empty BACKGROUND records, got %d", cleanBGs)
		}
	})

	t.Run("first-start-mid-job-returns-to-deeper-idle-settles-cleanly", func(t *testing.T) {
		k := newKeploySide(t)
		bin := buildDeltaProbeApp(t, k)
		a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")

		// No warm START before the gap: worker is already mid-job at the VERY FIRST START!
		getOK(t, a, "/shallow-job?kind=bg&gate=g0")
		recN := measure(t, k, a, "set-0/N", "/shallow-open?gate=g0")
		if recN["windowCut"] != true {
			t.Fatalf("first-START mid-job N windowCut = %v, want true", recN["windowCut"])
		}
		recN1 := measure(t, k, a, "set-0/N1", "/plain")
		assertTailOnlyOrBetweenTests(t, "N+1", recN1, "tail:set-0/N")

		// N+2..N+8: settled onto resumeWatch at deeper Pop idle -> ALL clean!
		for i := 2; i <= 8; i++ {
			id := fmt.Sprintf("set-0/N%d", i)
			rec := measure(t, k, a, id, "/plain")
			if _, hasCut := rec["windowCut"]; hasCut {
				t.Fatalf("%s unexpectedly marked windowCut: %v", id, rec)
			}
			if _, hasCont := rec["contaminated"]; hasCont {
				t.Fatalf("%s stuck in permanent taint after returning to deeper idle: %v", id, rec)
			}
		}
	})

	t.Run("re-parked-gap-job-after-N-catches-N1-and-N2-without-cutting-N3", func(t *testing.T) {
		k := newKeploySide(t)
		bin := buildDeltaProbeApp(t, k)
		a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")

		// 1. Establish Pop idle baseline in lastQuiescentSigs.
		getOK(t, a, "/shallow-wait-idle")
		if warm := measure(t, k, a, "set-0/warm", "/plain"); warm["windowCut"] != nil {
			t.Fatalf("warm unexpected windowCut: %v", warm)
		}

		// 2. Gap: park worker in asynclib.Call (g0).
		getOK(t, a, "/shallow-job?kind=bg&gate=g0")

		// 3. N: finish g0 -> worker returns to Pop; N is windowCut, Pop stays established idle.
		recN := measure(t, k, a, "set-0/N", "/shallow-open?gate=g0")
		if recN["windowCut"] != true {
			t.Fatalf("N windowCut = %v, want true", recN["windowCut"])
		}

		// 4. Gap: park worker in asynclib.Call (g1) again!
		getOK(t, a, "/shallow-job?kind=bg&gate=g1")

		// 5. N+1: worker is STILL parked in asynclib.Call (g1) -> MUST NOT poison lastQuiescentSigs with D!
		recN1 := measure(t, k, a, "set-0/N1", "/plain")
		if recN1["windowCut"] != true {
			t.Fatalf("N+1 windowCut = %v, want true (worker still parked mid-job in g1)", recN1["windowCut"])
		}

		// 6. N+2: release g1 and park fraud job (g2) at the exact same asynclib.Call line!
		// N+2 MUST be contaminated (NOT trusted as clean!).
		recN2 := measure(t, k, a, "set-0/N2", "/shallow-job?kind=fraud&gate=g2&release=g1")
		if containsInt(linesOfRecord(t, recN2)[deltaFile], fraudLine) {
			t.Fatalf("setup: shallowFraud line %d should not have run in N+2", fraudLine)
		}
		assertTailOnlyOrBetweenTests(t, "N+2", recN2, "tail:set-0/N1")

		// 7. N+3: open g2 so fraud runs inside N+3 and worker returns to Pop.
		// Innocent N+3 MUST carry tail:set-0/N1 and MUST NOT be falsely marked windowCut!
		if got := send(t, k.sock, "START set-0/N3"); got != replyStartUnasked {
			t.Fatalf("START N3: %q", got)
		}
		getOK(t, a, "/shallow-open?gate=g2")
		getOK(t, a, "/plain")
		if got := send(t, k.sock, "END set-0/N3"); got != replyTaken {
			t.Fatalf("END N3: %q", got)
		}
		recN3 := readNextRecord(t, k, a)
		if !containsInt(linesOfRecord(t, recN3)[deltaFile], fraudLine) {
			t.Fatalf("setup: shallowFraud line %d did not land in N+3: %v", fraudLine, linesOfRecord(t, recN3))
		}
		assertTailOnlyOrBetweenTests(t, "N+3", recN3, "tail:set-0/N1")

		// 8. N+4..N+8: worker back at Pop -> ALL 100% CLEAN!
		for i := 4; i <= 8; i++ {
			id := fmt.Sprintf("set-0/N%d", i)
			rec := measure(t, k, a, id, "/plain")
			if _, hasCut := rec["windowCut"]; hasCut {
				t.Fatalf("%s unexpectedly marked windowCut: %v", id, rec)
			}
			if _, hasCont := rec["contaminated"]; hasCont {
				t.Fatalf("%s unexpectedly marked contaminated: %v", id, rec)
			}
		}
	})

	t.Run("first-seen-mid-job-re-parked-at-END-protects-gap-BACKGROUND", func(t *testing.T) {
		k := newKeploySide(t)
		bin := buildDeltaProbeApp(t, k)
		a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")
		if got := send(t, k.sock, backgroundAsk+" sfa"); got != replyTaken {
			t.Fatalf("BACKGROUND: %q", got)
		}

		runBG := func(id, path string) (map[string]any, []map[string]any) {
			t.Helper()
			if got := send(t, k.sock, "START "+id); got != replyStartAskedBy("sfa") {
				t.Fatalf("START %s: %q", id, got)
			}
			getOK(t, a, path)
			if got := send(t, k.sock, "END "+id); got != replyTaken {
				t.Fatalf("END %s: %q", id, got)
			}
			var bgs []map[string]any
			for {
				r := readNextRecord(t, k, a)
				if r["kind"] == "background" {
					bgs = append(bgs, r)
					continue
				}
				if r["id"] != id {
					t.Fatalf("got record %v, want id %s", r, id)
				}
				return r, bgs
			}
		}

		// 1-2. Worker is ALREADY mid-job (g0 in asynclib.Call) at its VERY FIRST START (T0).
		getOK(t, a, "/shallow-job?kind=bg&gate=g0")
		_, _ = runBG("set-0/T0", "/plain")

		// 3. Gap: g0 finishes, worker returns to Pop.
		getOK(t, a, "/shallow-open?gate=g0")

		// 4. T1: starts at Pop (drifted from T0's D), parks fraud job g1 back at D -> windowCut + markOnly watch on D!
		rec1, _ := runBG("set-0/T1", "/shallow-job?kind=fraud&gate=g1")
		if rec1["windowCut"] != true {
			t.Fatalf("T1 windowCut = %v, want true", rec1["windowCut"])
		}

		// 5. T2: plain while g1 is still parked at D -> tail:set-0/T1, markOnly watch stays armed.
		rec2, _ := runBG("set-0/T2", "/plain")
		assertTailOnlyOrBetweenTests(t, "T2", rec2, "tail:set-0/T1")

		// 6. Gap T2->T3: g1 opens, running shallowFraud.Add(1) and returning worker to Pop!
		getOK(t, a, "/shallow-open?gate=g1")

		// 7. START T3: markOnly watch MUST fire -> gap T2->T3 BACKGROUND MUST be afterCut:true
		// with empty executedLinesByFile (never leaking T1's fraud line into clean BACKGROUND),
		// and T3 carries tail:set-0/T1.
		rec3, bgs3 := runBG("set-0/T3", "/plain")
		if len(bgs3) == 0 {
			t.Fatalf("expected afterCut BACKGROUND record for gap T2->T3, got 0")
		}
		for _, bg := range bgs3 {
			if bg["afterCut"] != true {
				t.Fatalf("gap T2->T3 BACKGROUND leaked T1 fraud tail as clean: %v", bg)
			}
			if ls := linesOfRecord(t, bg); len(ls) != 0 {
				t.Fatalf("gap T2->T3 afterCut BACKGROUND must have empty executedLinesByFile: %v", ls)
			}
		}
		if !strings.Contains(fmt.Sprint(rec3["contaminated"]), "tail:set-0/T1") {
			t.Fatalf("T3 contaminated = %v, missing tail:set-0/T1", rec3["contaminated"])
		}

		// 8. T5..T9: worker settles at Pop -> ALL CLEAN!
		_, _ = runBG("set-0/T4", "/plain")
		for i := 5; i <= 9; i++ {
			id := fmt.Sprintf("set-0/T%d", i)
			rec, _ := runBG(id, "/plain")
			if _, hasCut := rec["windowCut"]; hasCut {
				t.Fatalf("%s unexpectedly marked windowCut: %v", id, rec)
			}
			if _, hasCont := rec["contaminated"]; hasCont {
				t.Fatalf("%s unexpectedly marked contaminated: %v", id, rec)
			}
		}
	})
}

// TestRealGoApp_LazyOnceSleepJanitorRebaselinesAfterSecondTest verifies on a real
// -cover binary that a permanent background goroutine lazily started via
// sync.Once running `for { time.Sleep(time.Hour); ticks.Add(1) }` marks T1
// windowCut:true and T2 contaminated:["tail:set-0/T1"], then settles onto a
// resumeWatch so T3..T10 are ALL 100% CLEAN and normal BACKGROUND records
// resume after T3.
func TestRealGoApp_LazyOnceSleepJanitorRebaselinesAfterSecondTest(t *testing.T) {
	k := newKeploySide(t)
	bin := buildDeltaProbeApp(t, k)
	a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=30")
	deltaFile := appModule + "/delta.go"
	gapPingLine := deltaLineOf(t, "gapPings.Add(1)")

	if got := send(t, k.sock, backgroundAsk+" sleepj"); got != replyTaken {
		t.Fatalf("BACKGROUND: %q", got)
	}

	run := func(id, path string) (map[string]any, []map[string]any) {
		t.Helper()
		if got := send(t, k.sock, "START "+id); got != replyStartAskedBy("sleepj") {
			t.Fatalf("START %s: %q", id, got)
		}
		getOK(t, a, path)
		if got := send(t, k.sock, "END "+id); got != replyTaken {
			t.Fatalf("END %s: %q", id, got)
		}
		var bgs []map[string]any
		for {
			r := readNextRecord(t, k, a)
			if r["kind"] == "background" {
				bgs = append(bgs, r)
				continue
			}
			if r["id"] != id {
				t.Fatalf("got record %v, want id %s", r, id)
			}
			return r, bgs
		}
	}

	_, _ = run("set-0/warm", "/plain")

	// T1: lazily starts sync.Once time.Sleep janitor -> windowCut:true naming sleepJanitor.
	rec1, _ := run("set-0/T1", "/lazy-sleep")
	if rec1["windowCut"] != true {
		t.Fatalf("T1 windowCut = %v, want true (rec=%v)", rec1["windowCut"], rec1)
	}
	ling1, _ := rec1["lingeringGoroutines"].([]any)
	if len(ling1) == 0 || !strings.Contains(fmt.Sprint(ling1), "sleepJanitor") {
		t.Fatalf("T1 lingeringGoroutines = %v, want entry naming sleepJanitor", ling1)
	}

	// T2: tainted tail:set-0/T1, no windowCut.
	rec2, _ := run("set-0/T2", "/plain")
	if !reflect.DeepEqual(rec2["contaminated"], []any{"tail:set-0/T1"}) {
		t.Fatalf("T2 contaminated = %v, want [tail:set-0/T1]", rec2["contaminated"])
	}
	if _, hasCut := rec2["windowCut"]; hasCut {
		t.Fatalf("T2 unexpectedly marked windowCut: %v", rec2)
	}

	// T3..T10: sleep janitor settled onto resumeWatch at START T3 -> ALL clean
	// and non-empty BACKGROUND with afterCut != true resumes after T3!
	cleanBGs := 0
	for i := 3; i <= 10; i++ {
		if i >= 4 {
			getOK(t, a, "/gap-ping")
		}
		id := fmt.Sprintf("set-0/T%d", i)
		rec, bgs := run(id, "/plain")
		if _, hasCut := rec["windowCut"]; hasCut {
			t.Fatalf("%s got windowCut (sleep janitor not settled): %v", id, rec)
		}
		if _, hasCont := rec["contaminated"]; hasCont {
			t.Fatalf("%s got contaminated (sleep janitor stuck): %v", id, rec)
		}
		if i >= 4 {
			cleanBGs += assertNonEmptyCleanBG(t, id, bgs, deltaFile, gapPingLine)
		}
	}
	if cleanBGs < 7 {
		t.Fatalf("expected >= 7 clean non-empty BACKGROUND records after T3, got %d", cleanBGs)
	}
}

const gapBornApp = `package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

var (
	gbStarted atomic.Int64
	gbDone    atomic.Int64
	gbInfo    string
	gbClient  *http.Client
)

type gbConn struct{ net.Conn }

func (c *gbConn) Read(b []byte) (int, error)  { return c.Conn.Read(b) }
func (c *gbConn) Write(b []byte) (int, error) { return c.Conn.Write(b) }

type gbListener struct{ net.Listener }

func (l *gbListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &gbConn{Conn: c}, nil
}

func gbFinishJob(s string) int {
	gbDone.Add(1) // GB-JOB-DONE
	return len(s)
}

func gbJob(s string) {
	gbStarted.Add(1)
	time.Sleep(30 * time.Millisecond)
	gbFinishJob(s)
}

func gbConsumeSpawn(c net.Conn) {
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		go gbJob(line)
	}
}

func gbAcceptSpawn(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go gbConsumeSpawn(c)
	}
}

func gbFinishHTTP(m string) int {
	gbDone.Add(1) // GB-HTTP-DONE
	return len(m)
}

func gbSlow(w http.ResponseWriter, r *http.Request) {
	gbStarted.Add(1)
	time.Sleep(30 * time.Millisecond)
	gbFinishHTTP(r.Method)
	fmt.Fprint(w, "slow")
}

func gbListen() net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	return ln
}

func init() {
	spw, hp, hw := gbListen(), gbListen(), gbListen()
	gbInfo = spw.Addr().String() + " " + hp.Addr().String() + " " + hw.Addr().String()
	go gbAcceptSpawn(spw)
	mux := http.NewServeMux()
	mux.HandleFunc("/gb-slow", gbSlow)
	mux.HandleFunc("/gb-fast", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "fast") })
	go func() { _ = http.Serve(hp, mux) }()
	go func() { _ = http.Serve(&gbListener{Listener: hw}, mux) }()
	gbClient = &http.Client{Transport: &http.Transport{
		MaxIdleConnsPerHost: 2,
		DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
			var d net.Dialer
			c, err := d.DialContext(ctx, n, a)
			if err != nil {
				return nil, err
			}
			return &gbConn{Conn: c}, nil
		},
	}}
	http.HandleFunc("/gb-info", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, gbInfo) })
	http.HandleFunc("/gb-started", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, gbStarted.Load()) })
	http.HandleFunc("/gb-out", func(w http.ResponseWriter, r *http.Request) {
		resp, err := gbClient.Get(r.URL.Query().Get("url"))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		fmt.Fprint(w, "out")
	})
}
`

func gapBornLineOf(t *testing.T, needle string) int {
	t.Helper()
	for i, l := range strings.Split(gapBornApp, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("gapBornApp has no line holding %q", needle)
	return 0
}

// TestRealGoApp_GapBornAndEndSeenCarryOverFailClosed verifies on a real -cover
// binary that work handed off just before START and finishing inside the test
// window fails closed (windowCut:true) across goroutines that no prior START
// recorded:
//   - a pre-existing socket consumer spawning `go gbJob(line)` in the gap;
//   - a keep-alive HTTP connection (plain or wrapped net.Conn) dialed inside
//     the previous test and mid-request in gbSlow at START;
//   - a fresh HTTP connection (plain or wrapped net.Conn) dialed in the gap
//     and mid-request in gbSlow at START;
//
// while an app outbound HTTP client over a wrapped dialer stays 100% clean.
func TestRealGoApp_GapBornAndEndSeenCarryOverFailClosed(t *testing.T) {
	file := appModule + "/gapborn.go"
	gbGet := func(t *testing.T, addr, path string) string {
		t.Helper()
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return strings.TrimSpace(string(b))
	}
	gbWaitStarted := func(t *testing.T, a app, after int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			n, _ := strconv.Atoi(gbGet(t, a.addr, "/gb-started"))
			if n > after {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("handed-over job did not start")
			}
			time.Sleep(time.Millisecond)
		}
	}

	buildGapBorn := func(t *testing.T) (*keploySide, app, []string) {
		t.Helper()
		k := newKeploySide(t)
		hook := hookCopy(t, k.sock, k.data)
		mod := appModuleDir(t)
		if err := os.WriteFile(filepath.Join(mod, "gapborn.go"), []byte(gapBornApp), 0o644); err != nil {
			t.Fatal(err)
		}
		bin, _ := stage(t, hook, mod, nil)
		a := startWithEnv(t, bin, "KEPLOY_GO_END_WAIT_MS=20")
		info := strings.Fields(gbGet(t, a.addr, "/gb-info"))
		if len(info) != 3 {
			t.Fatalf("unexpected /gb-info: %v", info)
		}
		return k, a, info
	}

	for _, mode := range []string{
		"spawned-consumer",
		"keepalive-prev-test-plain",
		"keepalive-prev-test-wrapped",
		"fresh-conn-plain",
		"fresh-conn-wrapped",
	} {
		t.Run(mode, func(t *testing.T) {
			k, a, info := buildGapBorn(t)
			var line int
			var wantName string
			var hand func() func()
			inWindow := func() {}

			switch mode {
			case "spawned-consumer":
				cc, err := net.Dial("tcp", info[0])
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cc.Close() })
				s0, _ := strconv.Atoi(gbGet(t, a.addr, "/gb-started"))
				if _, err := cc.Write([]byte("warm\n")); err != nil {
					t.Fatal(err)
				}
				gbWaitStarted(t, a, s0)
				time.Sleep(60 * time.Millisecond)
				line = gapBornLineOf(t, "GB-JOB-DONE")
				wantName = "gbJob"
				hand = func() func() {
					s, _ := strconv.Atoi(gbGet(t, a.addr, "/gb-started"))
					if _, err := cc.Write([]byte("m\n")); err != nil {
						t.Fatal(err)
					}
					gbWaitStarted(t, a, s)
					return func() {}
				}
			default:
				addr := info[1]
				if strings.HasSuffix(mode, "wrapped") {
					addr = info[2]
				}
				line = gapBornLineOf(t, "GB-HTTP-DONE")
				wantName = "gbSlow"
				openedInPrev := strings.HasPrefix(mode, "keepalive-prev-test")
				var next *http.Client
				if openedInPrev {
					inWindow = func() {
						next = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1}}
						resp, err := next.Get("http://" + addr + "/gb-fast")
						if err != nil {
							t.Fatal(err)
						}
						_, _ = io.Copy(io.Discard, resp.Body)
						_ = resp.Body.Close()
					}
				}
				hand = func() func() {
					c := next
					if !openedInPrev {
						c = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1}}
					}
					s, _ := strconv.Atoi(gbGet(t, a.addr, "/gb-started"))
					done := make(chan struct{})
					go func() {
						defer close(done)
						resp, err := c.Get("http://" + addr + "/gb-slow")
						if err == nil {
							_, _ = io.Copy(io.Discard, resp.Body)
							_ = resp.Body.Close()
						}
					}()
					gbWaitStarted(t, a, s)
					return func() {
						<-done
						c.Transport.(*http.Transport).CloseIdleConnections()
					}
				}
			}

			// Warm up baseline (and for keepalive-prev-test, dial the keep-alive
			// conn inside warm1 so it is seen idle at END warm1, never at START).
			for i := 0; i < 2; i++ {
				id := fmt.Sprintf("set-0/warm%d", i)
				if got := send(t, k.sock, "START "+id); got != replyStartUnasked {
					t.Fatalf("START %s: %q", id, got)
				}
				if i == 1 {
					inWindow()
				}
				if got := send(t, k.sock, "END "+id); got != replyTaken {
					t.Fatalf("END %s: %q", id, got)
				}
				readNextRecord(t, k, a)
			}

			for i := 0; i < 5; i++ {
				id := fmt.Sprintf("set-0/g%d", i)
				wait := hand()
				if got := send(t, k.sock, "START "+id); got != replyStartUnasked {
					t.Fatalf("START %s: %q", id, got)
				}
				time.Sleep(60 * time.Millisecond)
				inWindow()
				if got := send(t, k.sock, "END "+id); got != replyTaken {
					t.Fatalf("END %s: %q", id, got)
				}
				rec := readNextRecord(t, k, a)
				wait()
				if !containsInt(linesOfRecord(t, rec)[file], line) {
					t.Fatalf("%s trial %d: setup did not land line %d in window: %v", mode, i, line, linesOfRecord(t, rec))
				}
				if rec["windowCut"] != true {
					t.Fatalf("%s trial %d: falsely reported clean with handed-over work in window: %v", mode, i, rec)
				}
				if !strings.Contains(fmt.Sprint(rec["lingeringGoroutines"]), wantName) {
					t.Fatalf("%s trial %d: lingeringGoroutines = %v, want entry naming %s", mode, i, rec["lingeringGoroutines"], wantName)
				}
			}
		})
	}

	t.Run("wrapped-outbound-keepalive-stays-clean", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "1") }))
		defer up.Close()
		k, a, _ := buildGapBorn(t)
		for i := 0; i < 10; i++ {
			id := fmt.Sprintf("set-0/out%d", i)
			if got := send(t, k.sock, "START "+id); got != replyStartUnasked {
				t.Fatalf("START %s: %q", id, got)
			}
			gbGet(t, a.addr, "/gb-out?url="+up.URL)
			if got := send(t, k.sock, "END "+id); got != replyTaken {
				t.Fatalf("END %s: %q", id, got)
			}
			rec := readNextRecord(t, k, a)
			if _, hasCut := rec["windowCut"]; hasCut {
				t.Fatalf("%s got unexpected windowCut on wrapped outbound keep-alive: %v", id, rec)
			}
			if _, hasCont := rec["contaminated"]; hasCont {
				t.Fatalf("%s got unexpected contaminated on wrapped outbound keep-alive: %v", id, rec)
			}
		}
	})
}
