package keploytest

import (
	"bytes"
	"encoding/json"
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
	defer resp.Body.Close()
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
		return answer{}, fmt.Errorf("keploy: the keploy agent answered %s to %s: %s", resp.Status, path, quoted(data))
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

// quoted is a bounded, quoted copy of an answer for an error message.
func quoted(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > maxQuoted {
		return fmt.Sprintf("%q...", s[:maxQuoted])
	}
	return fmt.Sprintf("%q", s)
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
		// A frozen clock this package cannot apply would let the tests run
		// on the real one, against recordings made on another.
		return fmt.Errorf("keploy: this keploy run gives its tests a frozen clock, which this version of keploytest cannot apply; upgrade github.com/keploy/go-sdk")
	}
	return nil
}
