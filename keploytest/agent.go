package keploytest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// The scope protocol this package speaks. Every answer of the agent carries
// it as "v"; an answer without it, or with another version, fails the test,
// so a new keploytest on an old agent (or the reverse) fails loudly instead
// of running unjudged.
const protocolVersion = 1

const (
	pathBegin = "/agent/scope/begin"
	pathEnd   = "/agent/scope/end"

	actionSkip = "skip"
	actionFail = "fail"

	outcomePassed  = "passed"
	outcomeFailed  = "failed"
	outcomeSkipped = "skipped"

	// maxAnswer bounds what is read of one answer.
	maxAnswer = 1 << 20
	// maxQuoted bounds how much of an answer an error message quotes.
	maxQuoted = 512
)

// beginReq is the body of POST /agent/scope/begin. Go sends the top-level
// test's name; the agent qualifies it with the package the claim named.
type beginReq struct {
	Name    string `json:"name"`
	CanSkip bool   `json:"canSkip"`
}

// endReq is the body of POST /agent/scope/end.
type endReq struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
}

// answer is a 2xx answer of the agent.
type answer struct {
	V      *int   `json:"v"`
	Status string `json:"status"`
	Action string `json:"action"`
	Reason string `json:"reason"`
	// Instance is the claim's answer: the run itself in keploy's first
	// release.
	Instance string `json:"instance"`
	// TimeFile is the clock file the claim's test process must run on,
	// empty when keploy's run is not frozen.
	TimeFile string `json:"timeFile"`
	// Variable is a given's answer: the variable keploy holds the value
	// as (given:<name>, numbered for a name the test gave again).
	Variable string `json:"variable"`
}

// agentError is the body of a non-2xx answer.
type agentError struct {
	Code    string `json:"error"`
	Message string `json:"message"`
	Remedy  string `json:"remedy"`
}

// client talks to one keploy agent.
type client struct {
	url   string
	token string
	http  *http.Client
}

var (
	agentOnce sync.Once
	agentC    *client
)

// agent is the client of the agent this process joined.
func agent() *client {
	agentOnce.Do(func() {
		j := join()
		agentC = newClient(j.url, j.token)
	})
	return agentC
}

// newClient talks to the agent directly: no proxy from the environment, no
// redirect, and no timeout. The agent answers once the work it owes is done,
// and a deadline would decide a test's outcome by how loaded the machine is.
func newClient(url, token string) *client {
	return &client{
		url:   url,
		token: token,
		http: &http.Client{
			// One connection per call, closed with it: no idle connection
			// outlives a call (a test's goroutine-leak check would see its
			// goroutines), and the transport never resends a call on a
			// reused connection that broke, so nothing is retried.
			Transport: &http.Transport{
				Proxy:             nil,
				DialContext:       (&net.Dialer{}).DialContext,
				DisableKeepAlives: true,
			},
			// A redirect is an answer like any other non-2xx: following it
			// would send the call again, or to somewhere else.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// begin asks the agent to open the test.
func (c *client) begin(name string) (answer, error) {
	a, err := c.post(pathBegin, beginReq{Name: name, CanSkip: true})
	if err != nil {
		return a, err
	}
	switch {
	case a.Status != "ok":
		return a, fmt.Errorf("keploy: the keploy agent answered the begin of %s with status %q", name, a.Status)
	case a.Action == "" || a.Action == actionSkip:
		return a, nil
	}
	return a, fmt.Errorf("keploy: the keploy agent answered the begin of %s with the unknown action %q", name, a.Action)
}

// end tells the agent the test ended, with its outcome.
func (c *client) end(name, outcome string) (answer, error) {
	a, err := c.post(pathEnd, endReq{Name: name, Outcome: outcome})
	if err != nil {
		return a, err
	}
	switch {
	case a.Status != "ok":
		return a, fmt.Errorf("keploy: the keploy agent answered the end of %s with status %q", name, a.Status)
	case a.Action == "" || a.Action == actionFail:
		return a, nil
	}
	return a, fmt.Errorf("keploy: the keploy agent answered the end of %s with the unknown action %q", name, a.Action)
}

// post sends one call and reads its answer. Every way the answer can fail to
// be trusted is an error; nothing is retried.
func (c *client) post(path string, body any) (answer, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return answer{}, fmt.Errorf("keploy: cannot encode the call to %s: %v", path, err)
	}
	target, err := url.JoinPath(c.url, path)
	if err != nil {
		return answer{}, fmt.Errorf("keploy: cannot call the keploy agent at %s: %v", c.url, err)
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return answer{}, fmt.Errorf("keploy: cannot call the keploy agent at %s: %v", c.url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, fmt.Errorf("keploy: cannot reach the keploy agent at %s: %v", c.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return answer{}, fmt.Errorf("keploy: cannot read the keploy agent's answer to %s: %v", path, err)
	}
	if len(data) > maxAnswer {
		return answer{}, fmt.Errorf("keploy: the keploy agent's answer to %s is larger than %d bytes", path, maxAnswer)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e agentError
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			if e.Remedy != "" {
				return answer{}, fmt.Errorf("keploy: %s (%s)", e.Message, e.Remedy)
			}
			return answer{}, fmt.Errorf("keploy: %s", e.Message)
		}
		return answer{}, &statusError{code: resp.StatusCode, status: resp.Status, path: path, body: quoted(data)}
	}
	var a answer
	if err := json.Unmarshal(data, &a); err != nil {
		return answer{}, fmt.Errorf("keploy: the keploy agent's answer to %s cannot be read (%v): %s", path, err, quoted(data))
	}
	if a.V == nil {
		return answer{}, fmt.Errorf("keploy: the keploy agent answered %s with %s, which names no scope protocol version; "+
			"this keploy-go-sdk needs a keploy agent that speaks scope protocol v%d: upgrade keploy", path, quoted(data), protocolVersion)
	}
	if *a.V != protocolVersion {
		return answer{}, fmt.Errorf("keploy: the keploy agent answered %s in scope protocol v%d (%s); "+
			"this keploy-go-sdk speaks scope protocol v%d: use a keploy and a keploy-go-sdk of the same release", path, *a.V, quoted(data), protocolVersion)
	}
	return a, nil
}

// ErrNotProven is what a call to keploy's agent fails with, wrapped, when
// the agent answers its route with a plain 404 (no error of its own): the
// agent has no such route. The claim a test binary makes before its first
// test (POST /agent/instance), the refusal and Given's route exist only in
// a keploy run with the proven switch on (KEPLOY_PROVEN=1 in the
// environment of keploy mock record and keploy mock replay); without it the
// agent answers each with a plain 404. A keploy older than this
// keploy-go-sdk answers its newer routes so too.
var ErrNotProven = errors.New("keploytest needs keploy run with KEPLOY_PROVEN=1 " +
	"(export KEPLOY_PROVEN=1 before keploy mock record or keploy mock replay), " +
	"and a keploy of the same release as this keploy-go-sdk")

// statusError is a non-2xx answer that is not the agent's own error: no
// message and remedy to show, only its HTTP status. A 404 is ErrNotProven.
type statusError struct {
	code         int
	status, path string
	body         string // quoted
}

func (e *statusError) Error() string {
	if e.code == http.StatusNotFound {
		return fmt.Sprintf("keploy: the keploy agent has no %s (it answered %s): %v", e.path, e.status, ErrNotProven)
	}
	return fmt.Sprintf("keploy: the keploy agent answered %s to %s: %s", e.status, e.path, e.body)
}

func (e *statusError) Unwrap() error {
	if e.code == http.StatusNotFound {
		return ErrNotProven
	}
	return nil
}

// quoted is a bounded, quoted copy of an answer for an error message.
func quoted(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > maxQuoted {
		return fmt.Sprintf("%q...", s[:maxQuoted])
	}
	return fmt.Sprintf("%q", s)
}

const pathRefusal = "/agent/refusal"

// refusal tells the agent what this test process refused (runRefusal),
// before it claimed keploy's run or for the claim's answer. The agent records
// it for the run's verdict; it records the refusals it answers a claim or a
// begin with itself.
func (c *client) refusal(r runRefusal) error {
	a, err := c.post(pathRefusal, r)
	if err != nil {
		return err
	}
	if a.Status != "ok" {
		return fmt.Errorf("keploy: the keploy agent answered the refusal %s with status %q", r.Code, a.Status)
	}
	return nil
}

const pathInstance = "/agent/instance"

// instanceReq is the body of POST /agent/instance: the test process claims
// keploy's run. Key is its package directory relative to the project, and
// Dir the directory itself. The agent reads which process calls from the
// connection.
type instanceReq struct {
	Key string `json:"key"`
	Dir string `json:"dir"`
}

// claim claims keploy's run for this test process, before any test runs.
func (c *client) claim(key, dir string) error {
	a, err := c.post(pathInstance, instanceReq{Key: key, Dir: dir})
	if err != nil {
		return err
	}
	if a.Instance == "" {
		return fmt.Errorf("keploy: the keploy agent answered the claim of this test process without an instance")
	}
	if a.TimeFile != "" {
		// The run's clock is frozen, and only keploy's Go launcher freezes a
		// test binary's (it patches the binary before it starts): this one
		// was started without it, and would run on the real clock against
		// recordings made on another.
		return &refusalError{runRefusal{
			Code:    codeClockNotFrozen,
			Message: "this keploy run freezes its tests' clock, which only keploy's Go launcher can do for a test binary, and this one was not started through it",
			Remedy:  "let go test start it (keploy adds -exec=keploy-go-exec to GOFLAGS, which a go test -exec or a GOFLAGS of the test command's own replaces), or start it in its package's directory as keploy-go-exec <test binary> -test.v",
		}}
	}
	return nil
}

// codeClockNotFrozen is the scope protocol's code for a test binary that
// would run on the real clock in a run keploy freezes; keploy's Go launcher
// refuses with it too.
const codeClockNotFrozen = "clock_not_frozen"

// refusalError is a claim's answer this package refuses the run for, after
// the agent took the claim: the refusal is told to the agent too, so keploy's
// verdict names it.
type refusalError struct{ r runRefusal }

func (e *refusalError) Error() string { return e.r.text() }

const pathGiven = "/agent/scope/given"

// givenReq is the body of POST /agent/scope/given: a value the open test
// chose and hands the application, under the name the test gave it, and
// where the test gave it (file:line, relative to the project), which keploy
// uses only in wording.
type givenReq struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

// given tells the agent that the open test gave value under name. The agent
// answers with the variable it holds the value as.
func (c *client) given(name, value, source string) (answer, error) {
	a, err := c.post(pathGiven, givenReq{Name: name, Value: value, Source: source})
	if err != nil {
		return a, err
	}
	switch {
	case a.Status != "ok":
		return a, fmt.Errorf("keploy: the keploy agent answered the given %s with status %q", name, a.Status)
	case a.Variable == "":
		return a, fmt.Errorf("keploy: the keploy agent answered the given %s without the variable it holds it as", name)
	}
	return a, nil
}
