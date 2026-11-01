package keploytest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// answering is an agent that answers every call with status and body, and
// keeps what it was sent.
type answering struct {
	status int
	body   string
	path   string
	auth   string
	sent   map[string]any
}

func (a *answering) serve(t *testing.T) *client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.path = r.URL.Path
		a.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		a.sent = map[string]any{}
		_ = json.Unmarshal(raw, &a.sent)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "")
}

func TestBeginSendsTheNameAndCanSkip(t *testing.T) {
	a := &answering{status: 200, body: `{"v":1,"status":"ok"}`}
	c := a.serve(t)
	if _, err := c.begin("TestUserName"); err != nil {
		t.Fatal(err)
	}
	if a.path != pathBegin || a.sent["name"] != "TestUserName" || a.sent["canSkip"] != true || len(a.sent) != 2 {
		t.Fatalf("sent %s %v, want %s {name, canSkip:true}", a.path, a.sent, pathBegin)
	}
}

func TestEndSendsTheNameAndOutcome(t *testing.T) {
	a := &answering{status: 200, body: `{"v":1,"status":"ok"}`}
	c := a.serve(t)
	if _, err := c.end("TestUserName", outcomeFailed); err != nil {
		t.Fatal(err)
	}
	if a.path != pathEnd || a.sent["name"] != "TestUserName" || a.sent["outcome"] != "failed" || len(a.sent) != 2 {
		t.Fatalf("sent %s %v, want %s {name, outcome}", a.path, a.sent, pathEnd)
	}
}

// A refusal this package made is sent whole; an answer other than ok is an
// error.
func TestARefusalSendsItsCodeMessageAndRemedy(t *testing.T) {
	a := &answering{status: 200, body: `{"v":1,"status":"ok"}`}
	r := runRefusal{Code: codeOneTestProcess, Message: "go test runs up to 2 at once (-p 2)", Remedy: "add -p 1"}
	if err := a.serve(t).refusal(r); err != nil {
		t.Fatal(err)
	}
	if a.path != pathRefusal || a.sent["code"] != r.Code || a.sent["message"] != r.Message || a.sent["remedy"] != r.Remedy || len(a.sent) != 3 {
		t.Fatalf("sent %s %v, want %s {code, message, remedy}", a.path, a.sent, pathRefusal)
	}
	b := &answering{status: 200, body: `{"v":1,"status":"busy"}`}
	if err := b.serve(t).refusal(r); err == nil || !strings.Contains(err.Error(), `status "busy"`) {
		t.Fatalf("got %v, want an answer other than ok to be an error", err)
	}
}

func TestTheTokenIsSentOnlyWhenSet(t *testing.T) {
	a := &answering{status: 200, body: `{"v":1,"status":"ok"}`}
	c := a.serve(t)
	if _, err := c.begin("TestX"); err != nil {
		t.Fatal(err)
	}
	if a.auth != "" {
		t.Fatalf("Authorization %q sent without a token", a.auth)
	}
	c.token = "s3cret"
	if _, err := c.begin("TestX"); err != nil {
		t.Fatal(err)
	}
	if a.auth != "Bearer s3cret" {
		t.Fatalf("Authorization %q, want Bearer s3cret", a.auth)
	}
}

func TestAnswersAreRead(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       answer
		end        bool
	}{
		{name: "run", body: `{"v":1,"status":"ok"}`, want: answer{Status: "ok"}},
		{name: "skip", body: `{"v":1,"status":"ok","action":"skip","reason":"keploy: flaky"}`, want: answer{Status: "ok", Action: "skip", Reason: "keploy: flaky"}},
		{name: "fail", body: `{"v":1,"status":"ok","action":"fail","reason":"keploy: missed"}`, want: answer{Status: "ok", Action: "fail", Reason: "keploy: missed"}, end: true},
		{name: "unknown fields are ignored", body: `{"v":1,"status":"ok","extra":[1]}`, want: answer{Status: "ok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := (&answering{status: 200, body: tc.body}).serve(t)
			call := c.begin
			if tc.end {
				call = func(n string) (answer, error) { return c.end(n, outcomePassed) }
			}
			got, err := call("TestX")
			if err != nil {
				t.Fatal(err)
			}
			got.V = nil
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEveryUntrustworthyAnswerIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		end    bool
		want   []string
	}{
		{name: "no version", status: 200, body: `{"status":"ok"}`,
			want: []string{"names no scope protocol version", "speaks scope protocol v1", `"{\"status\":\"ok\"}"`}},
		{name: "another version", status: 200, body: `{"v":2,"status":"ok"}`,
			want: []string{"in scope protocol v2", "speaks scope protocol v1"}},
		{name: "null", status: 200, body: `null`, want: []string{"names no scope protocol version"}},
		{name: "not JSON", status: 200, body: `ok`, want: []string{"cannot be read", `"ok"`}},
		{name: "empty", status: 200, body: ``, want: []string{"cannot be read"}},
		{name: "error with remedy", status: 422,
			body: `{"error":"one_test_at_a_time","message":"TestB began while TestA is open","remedy":"start its first line with keploytest.TP and call keploytest.Parallel"}`,
			want: []string{"keploy: TestB began while TestA is open (start its first line with keploytest.TP and call keploytest.Parallel)"}},
		{name: "error without remedy", status: 422, body: `{"error":"x","message":"refused"}`, want: []string{"keploy: refused"}},
		{name: "error not JSON", status: 500, body: `boom`, want: []string{"500 Internal Server Error", `"boom"`}},
		{name: "begin with another status", status: 200, body: `{"v":1,"status":"busy"}`, want: []string{`with status "busy"`}},
		{name: "begin with an unknown action", status: 200, body: `{"v":1,"status":"ok","action":"fail"}`, want: []string{`unknown action "fail"`}},
		{name: "end with an unknown action", status: 200, body: `{"v":1,"status":"ok","action":"skip"}`, end: true, want: []string{`unknown action "skip"`}},
		{name: "a 2xx that is not 200 is still read", status: 201, body: `{"status":"ok"}`, want: []string{"names no scope protocol version"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := (&answering{status: tc.status, body: tc.body}).serve(t)
			var err error
			if tc.end {
				_, err = c.end("TestX", outcomePassed)
			} else {
				_, err = c.begin("TestX")
			}
			if err == nil {
				t.Fatalf("no error for %d %s", tc.status, tc.body)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not say %q", err, w)
				}
			}
			if !strings.HasPrefix(err.Error(), "keploy: ") {
				t.Errorf("error %q does not start with keploy:", err)
			}
		})
	}
}

// keploy's agent answers the claim, the refusal and Given's route with a
// plain 404 when the keploy run has the proven switch off (KEPLOY_PROVEN
// unset): a plain 404 on any route fails with ErrNotProven, saying to run
// keploy with KEPLOY_PROVEN=1, never as an unexplained 404. A 404 that is the agent's own error keeps its words, and
// another status is not ErrNotProven.
func TestAPlain404SaysKeployRunsWithoutTheProvenSwitch(t *testing.T) {
	const want = "(it answered 404 Not Found): keploytest needs keploy run with KEPLOY_PROVEN=1 " +
		"(export KEPLOY_PROVEN=1 before keploy mock record or keploy mock replay), and a keploy of the same release as this keploy-go-sdk"
	c := (&answering{status: 404, body: "404 page not found\n"}).serve(t)
	for path, call := range map[string]func() error{
		pathInstance: func() error { return c.claim("pkg", t.TempDir()) },
		pathBegin:    func() error { _, err := c.begin("TestX"); return err },
		pathEnd:      func() error { _, err := c.end("TestX", outcomePassed); return err },
		pathRefusal:  func() error { return c.refusal(runRefusal{Code: "x", Message: "m"}) },
		pathGiven:    func() error { _, err := c.given("nonce", "1", ""); return err },
	} {
		err := call()
		if !errors.Is(err, ErrNotProven) {
			t.Errorf("%s: err = %v, want ErrNotProven", path, err)
			continue
		}
		if got := err.Error(); got != "keploy: the keploy agent has no "+path+" "+want {
			t.Errorf("%s: err = %q", path, got)
		}
	}
	own := (&answering{status: 404, body: `{"error":"not_found","message":"no open test"}`}).serve(t)
	if _, err := own.begin("TestX"); errors.Is(err, ErrNotProven) || err == nil || err.Error() != "keploy: no open test" {
		t.Errorf("the agent's own 404: err = %v, want its words and not ErrNotProven", err)
	}
	other := (&answering{status: 500, body: "boom"}).serve(t)
	if _, err := other.begin("TestX"); errors.Is(err, ErrNotProven) || err == nil {
		t.Errorf("a 500: err = %v, want an error that is not ErrNotProven", err)
	}
}

func TestAnAnswerOverTheBoundIsAnError(t *testing.T) {
	big := `{"v":1,"status":"ok","reason":"` + strings.Repeat("x", maxAnswer) + `"}`
	c := (&answering{status: 200, body: big}).serve(t)
	_, err := c.begin("TestX")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want the size bound", err)
	}
}

func TestAnErrorQuotesABoundedBody(t *testing.T) {
	c := (&answering{status: 500, body: strings.Repeat("y", 4*maxQuoted)}).serve(t)
	_, err := c.begin("TestX")
	if err == nil || len(err.Error()) > 2*maxQuoted || !strings.Contains(err.Error(), "...") {
		t.Fatalf("err = %v, want a bounded quote", err)
	}
}

func TestAnAgentThatIsNotThereIsAnError(t *testing.T) {
	url := hangingUp(t)
	_, err := newClient(url, "").begin("TestX")
	if err == nil || !strings.Contains(err.Error(), "keploy: cannot reach the keploy agent at "+url) {
		t.Fatalf("err = %v, want cannot reach", err)
	}
}

func TestTheClientIgnoresTheEnvironmentsProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	a := &answering{status: 200, body: `{"v":1,"status":"ok"}`}
	c := a.serve(t)
	// httptest listens on loopback, which Go never proxies; check the
	// transport itself so a non-loopback agent is covered too.
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("the client's transport reads a proxy")
	}
	if _, err := c.begin("TestX"); err != nil {
		t.Fatal(err)
	}
}

func TestARedirectIsAnErrorAndIsNotFollowed(t *testing.T) {
	for _, code := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var calls []string
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls = append(calls, r.Method+" "+r.URL.Path)
			mu.Unlock()
			if r.URL.Path == pathBegin {
				http.Redirect(w, r, "/elsewhere", code)
				return
			}
			_, _ = io.WriteString(w, `{"v":1,"status":"ok"}`)
		}))
		_, err := newClient(srv.URL, "").begin("TestX")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), strconv.Itoa(code)) {
			t.Errorf("%d: err = %v, want the redirect as an error", code, err)
		}
		mu.Lock()
		if len(calls) != 1 {
			t.Errorf("%d: calls %v, want the begin alone", code, calls)
		}
		mu.Unlock()
	}
}

// A claim answered with a frozen clock is refused: only keploy's Go launcher
// freezes a test binary's clock, and a binary that claims the run itself was
// not started through it, so its tests would run on the real clock against
// recordings made on another.
func TestAClaimWithAFrozenClockIsRefused(t *testing.T) {
	a := &answering{status: 200, body: `{"v":1,"instance":"run"}`}
	if err := a.serve(t).claim("orders", "/p/orders"); err != nil {
		t.Fatalf("a run without a frozen clock: %v", err)
	}
	b := &answering{status: 200, body: `{"v":1,"instance":"run","timeFile":"/tmp/keploy-time"}`}
	err := b.serve(t).claim("orders", "/p/orders")
	if err == nil || !strings.Contains(err.Error(), "freezes its tests' clock") || !strings.Contains(err.Error(), "keploy-go-exec <test binary> -test.v") {
		t.Fatalf("got %v, want the frozen clock refused with the fix: start the binary through keploy's Go launcher", err)
	}
}
