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

// NewApp creates and returns an App instance for API testing. It should be called before router
// and dependency integration. It takes 5 strings as parameters.
// 
// name parameter should be the name of project app, It should not contain spaces.
//
// licenseKey parameter should be the license key for the API testing.
//
// keployHost parameter is the keploy's server address. If it is empty, requests are made to the 
// hosted Keploy server.
//
// host and port parameters containes the host and port of API to be tested.
func NewApp(name, licenseKey, keployHost, host, port string) *App {
	if keployHost == "" {
		keployHost = "http://localhost:8081"
	}
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()

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
		Resp: map[string]HttpResp{},
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
	Resp       map[string]HttpResp
}

// KError stores the error for encoding and decoding as errorString has no exported fields due
// to gob wasn't able to encode the unexported fields.
type KError struct{
	Err error
}

// Error method returns error string stored in Err field of KError.
func (e *KError) Error() string{
	return e.Err.Error()
}

const version = 1

// GobEncode encodes the Err and returns the binary data.
func (e *KError) GobEncode() ([]byte, error) {
	r := make([]byte, 0)
	r = append(r, version)
	
	if e.Err!=nil{
		r =append(r, e.Err.Error()...)
	}
	return r, nil
}

// GobDecode decodes the b([]byte) into error struct.
func (e *KError) GobDecode(b []byte) error {
	if b[0] != version {
		return errors.New("gob decode of errors.errorString failed: unsupported version")
	}
	if len(b)==1{
		e.Err = nil
	}else{
		str := string(b[1:])
		e.Err = errors.New(str)
	}

	return nil
}

// Capture will capture request, response and output of external dependencies by making Call to keploy server.
func (a *App) Capture(req TestCaseReq) {
	go a.put(req)
}

// Test fetches the testcases from the keploy server and current response of API. Then, both of the responses are sent back to keploy's server for comparision.
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
	req, err := http.NewRequest(string(tc.HttpReq.Method), "http://"+a.Host+":"+a.Port+tc.HttpReq.URL, bytes.NewBufferString(tc.HttpReq.Body))
	if err != nil {
		panic(err)
	}
	req.Header = tc.HttpReq.Header
	req.Header.Set("KEPLOY_TEST_ID", tc.ID)
	req.ProtoMajor = tc.HttpReq.ProtoMajor
	req.ProtoMinor = tc.HttpReq.ProtoMinor

	_, err = a.client.Do(req)
	if err != nil {
		a.Log.Error("failed sending testcase request to app", zap.Error(err))
		return nil, err
	}

	//defer resp.Body.Close()

	resp := a.Resp[tc.ID]
	delete(a.Resp, tc.ID)

	//body, err := ioutil.ReadAll(resp.Body)
	//if err != nil {
	//	a.Log.Error("failed reading simulated response from app", zap.Error(err))
	//	return nil, err
	//}
	return &resp, nil
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
			// a.Log.Error("failed to close connecton reader", zap.String("url", tcs.URI), zap.Error(err))
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
