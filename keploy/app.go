package keploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"io"
	"io/ioutil"
	"net/http"
	"time"
)

func NewApp(name, licenseKey, keployHost, host, port string) *App {
	if keployHost == "" {
		keployHost = "http://localhost:8081"
	}
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer logger.Sync() // flushes buffer, if any

	return &App{
		Name:       name,
		LicenseKey: licenseKey,
		KeployHost: keployHost,
		Host:       host,
		Port:       port,
		Log:        logger,
		client: &http.Client{
			Timeout: time.Second * 600,
		},
		Deps: map[string][]Dependency{},
	}
}

type App struct {
	Name       string
	LicenseKey string
	KeployHost string
	Host       string
	Port       string
	Log        *zap.Logger
	client     *http.Client
	Deps       map[string][]Dependency
}

func (a *App) Capture(req TestCaseReq) {
	a.put(req)
}

func (a *App) Test() {
	// fetch test cases from web server and save to memory
	time.Sleep(time.Second * 5)
	tcs := a.fetch()
	total := len(tcs)

	// start a test run
	id, err := a.start(total)
	if err != nil {
		a.Log.Error("failed to start test run", zap.Error(err))
		return
	}

	a.Log.Info("starting test execution", zap.String("id", id), zap.Int("total tests", total))
	passed := true
	// call the service for each test case
	for i, tc := range tcs {
		a.Log.Info(fmt.Sprintf("testing %d of %d", i, total), zap.String("testcase id", tc.ID))
		ok := a.check(id, tc)
		if !ok {
			passed = false
		}
		a.Log.Info("result", zap.Bool("passed", ok))
	}

	// end the test run
	err = a.end(id, passed)
	if err != nil {
		a.Log.Error("failed to end test run", zap.Error(err))
		return
	}
	a.Log.Info("test run completed", zap.String("run id", id), zap.Bool("passed overall", passed))

}

func (a *App) start(total int) (string, error) {
	url := fmt.Sprintf("%s/regression/start?app=%s&total=%d", a.KeployHost, a.Name, total)
	body, err := a.newGet(url)
	if err != nil {
		return "", err
	}
	var resp map[string]string

	err = json.Unmarshal(body, &resp)
	if err != nil {
		return "", err
	}

	return resp["id"], nil
}

func (a *App) end(id string, status bool) error {
	url := fmt.Sprintf("%s/regression/end?status=%t&id=%s", a.KeployHost, status, id)
	_, err := a.newGet(url)
	if err != nil {
		return err
	}
	return nil
}

func (a *App) simulate(tc TestCase) (*HttpResp, error) {
	req, err := http.NewRequest(string(tc.HttpReq.Method), "http://"+a.Host+":"+a.Port+tc.URI, bytes.NewBufferString(tc.HttpReq.Body))
	if err != nil {
		panic(err)
	}
	req.Header = tc.HttpReq.Header
	req.Header.Set("KEPLOY_TEST_ID", tc.ID)
	req.ProtoMajor = tc.HttpReq.ProtoMajor
	req.ProtoMinor = tc.HttpReq.ProtoMinor

	resp, err := a.client.Do(req)
	if err != nil {
		a.Log.Error("failed sending testcase request to app", zap.Error(err))
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		a.Log.Error("failed reading simulated response from app", zap.Error(err))
		return nil, err
	}
	return &HttpResp{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       string(body),
	}, nil
}

func (a *App) check(runId string, tc TestCase) bool {
	resp, err := a.simulate(tc)
	if err != nil {
		a.Log.Error("failed to simulate request on local server", zap.Error(err))
		return false
	}

	bin, err := json.Marshal(&TestReq{
		ID:    tc.ID,
		AppID: a.Name,
		RunID: runId,
		Resp:  *resp,
	})
	if err != nil {
		a.Log.Error("failed to marshal testcase request", zap.String("url", tc.URI), zap.Error(err))
		return false
	}

	// test application reponse
	r, err := http.NewRequest("POST", a.KeployHost+"/regression/test", bytes.NewBuffer(bin))
	if err != nil {
		a.Log.Error("failed to create test request request server", zap.String("id", tc.ID), zap.String("url", tc.URI), zap.Error(err))
		return false
	}

	r.Header.Set("key", a.LicenseKey)
	r.Header.Set("Content-Type", "application/json")

	resp2, err := a.client.Do(r)
	if err != nil {
		a.Log.Error("failed to send test request to backend", zap.String("url", tc.URI), zap.Error(err))
		return false
	}
	var res map[string]bool
	b, err := ioutil.ReadAll(resp2.Body)
	if err != nil {
		a.Log.Error("failed to read response from backend", zap.String("url", tc.URI), zap.Error(err))
	}
	err = json.Unmarshal(b, &res)
	if err != nil {
		a.Log.Error("failed to read test result from keploy cloud", zap.Error(err))
		return false
	}
	if res["pass"] {
		return true
	}
	return false
}

func (a *App) put(tcs TestCaseReq) {
	bin, err := json.Marshal(tcs)
	if err != nil {
		a.Log.Error("failed to marshall testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
	req, err := http.NewRequest("POST", a.KeployHost+"/regression/testcase", bytes.NewBuffer(bin))
	if err != nil {
		a.Log.Error("failed to create testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
	req.Header.Set("key", a.LicenseKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		a.Log.Error("failed to send testcase to backend", zap.String("url", tcs.URI), zap.Error(err))
		return
	}

	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {
			a.Log.Error("failed to close connecton reader", zap.String("url", tcs.URI), zap.Error(err))
			return
		}
	}(resp.Body)
	var res map[string]string
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		a.Log.Error("failed to read response from backend", zap.String("url", tcs.URI), zap.Error(err))
	}
	err = json.Unmarshal(body, &res)
	if err != nil {
		a.Log.Error("failed to read testcases from keploy cloud", zap.Error(err))
		return
	}
	id := res["id"]
	if id != "" {
		// run the request again to find noisy fields
		// add dependencies to shared context
		a.Deps[id] = tcs.Deps
		defer delete(a.Deps, id)

		resp2, err := a.simulate(TestCase{
			ID:       id,
			Captured: tcs.Captured,
			URI:      tcs.URI,
			HttpReq:  tcs.HttpReq,
			Deps:     tcs.Deps,
		})
		if err != nil {
			a.Log.Error("failed to simulate request on local server", zap.Error(err))
			return
		}

		bin2, err := json.Marshal(&TestReq{
			ID:    res["id"],
			AppID: a.Name,
			Resp:  *resp2,
		})

		if err != nil {
			a.Log.Error("failed to marshall testcase request", zap.String("url", tcs.URI), zap.Error(err))
			return
		}

		// send de-noise request to server
		r, err := http.NewRequest("POST", a.KeployHost+"/regression/denoise", bytes.NewBuffer(bin2))
		if err != nil {
			a.Log.Error("failed to create de-noise request", zap.String("url", tcs.URI), zap.Error(err))
			return
		}

		r.Header.Set("key", a.LicenseKey)
		r.Header.Set("Content-Type", "application/json")

		_, err = a.client.Do(r)
		if err != nil {
			a.Log.Error("failed to send de-noise request to backend", zap.String("url", tcs.URI), zap.Error(err))
			return
		}
	}

	return
}

func (a *App) Get(id string) *TestCase {
	url := fmt.Sprintf("%s/regression/testcase/%s", a.KeployHost, id)
	body, err := a.newGet(url)
	if err != nil {
		a.Log.Error("failed to fetch testcases from keploy cloud", zap.Error(err))
		return nil
	}
	var tcs TestCase

	err = json.Unmarshal(body, &tcs)
	if err != nil {
		a.Log.Error("failed to read testcases from keploy cloud", zap.Error(err))
		return nil
	}
	return &tcs

}

func (a *App) newGet(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("key", a.LicenseKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("failed to send get request: " + resp.Status)
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (a *App) fetch() []TestCase {
	url := fmt.Sprintf("%s/regression/testcase?app=%s", a.KeployHost, a.Name)
	body, err := a.newGet(url)
	if err != nil {
		a.Log.Error("failed to fetch testcases from keploy cloud", zap.Error(err))
		return nil
	}
	var tcs []TestCase

	err = json.Unmarshal(body, &tcs)
	if err != nil {
		a.Log.Error("failed to reading testcases from keploy cloud", zap.Error(err))
		return nil
	}
	return tcs
}
