package keploy

import (
	"bytes"
	"encoding/json"
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
		Name:        name,
		LicenseKey:  licenseKey,
		KelployHost: keployHost,
		Host:        host,
		Port:        port,
		Log:         logger,
		client: &http.Client{
			Timeout: time.Second * 600,
		},
	}
}

type App struct {
	Name        string
	LicenseKey  string
	KelployHost string
	Host        string
	Port        string
	Log         *zap.Logger
	client      *http.Client
}

func (a *App) Capture(req TestCaseReq) {
	a.put(req)
}

func (a *App) Test() {
	// fetch test cases from web server and save to memory
	time.Sleep(time.Second * 5)
	tcs := a.fetch()
	// call the service for each test case
	for _, tc := range tcs {
		fmt.Println("testing: ", tc.ID)
		fmt.Println("testcase result: ", a.check(tc))
	}
	//
}

func (a *App) simulate(tc TestCase) (http.Header, []byte, error) {
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
		a.Log.Error("failed sending testcase request to backend", zap.Error(err))
		return nil, nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		a.Log.Error("failed getting testcases from backend", zap.Error(err))
		return nil, nil, err
	}
	return resp.Header, body, nil
}

func (a *App) check(tc TestCase) bool {
	headers, body, err := a.simulate(tc)
	if err != nil {
		a.Log.Error("failed to simulate request on local server", zap.Error(err))
		return false
	}

	bin, err := json.Marshal(&DeNoiseReq{
		ID:      tc.ID,
		AppID:   a.Name,
		Body:    string(body),
		Headers: headers,
	})
	if err != nil {
		a.Log.Error("failed to marshal testcase request", zap.String("url", tc.URI), zap.Error(err))
		return false
	}

	// send de-noise request to server
	r, err := http.NewRequest("POST", a.Host+"/regression/test", bytes.NewBuffer(bin))
	if err != nil {
		a.Log.Error("failed to create test request request server", zap.String("id", tc.ID), zap.String("url", tc.URI), zap.Error(err))
		return false
	}

	r.Header.Set("key", a.LicenseKey)
	resp, err := a.client.Do(r)
	if err != nil {
		a.Log.Error("failed to send test request to backend", zap.String("url", tc.URI), zap.Error(err))
		return false
	}
	var res map[string]bool
	b, err := ioutil.ReadAll(resp.Body)
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
	req, err := http.NewRequest("POST", a.Host+"/regression/testcase", bytes.NewBuffer(bin))
	if err != nil {
		a.Log.Error("failed to create testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("key", a.LicenseKey)
	req.Header.Set("content-type", "application/json")

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
	if res["id"] != "" {
		// run the request again to find noisy fields
		h, b, err := a.simulate(TestCase{
			Captured: tcs.Captured,
			URI:      tcs.URI,
			HttpReq:  tcs.HttpReq,
			Deps:     tcs.Deps,
		})
		if err != nil {
			a.Log.Error("failed to simulate request on local server", zap.Error(err))
			return
		}

		bin, err := json.Marshal(&DeNoiseReq{
			ID:      res["id"],
			AppID:   a.Name,
			Body:    string(b),
			Headers: h,
		})
		if err != nil {
			a.Log.Error("failed to marshall testcase request", zap.String("url", tcs.URI), zap.Error(err))
			return
		}

		// send de-noise request to server
		r, err := http.NewRequest("POST", a.Host+"/regression/denoise", bytes.NewBuffer(bin))
		if err != nil {
			a.Log.Error("failed to create de-noise request", zap.String("url", tcs.URI), zap.Error(err))
			return
		}

		r.Header.Set("key", a.LicenseKey)

		_, err = a.client.Do(req)
		if err != nil {
			a.Log.Error("failed to send de-noise request to backend", zap.String("url", tcs.URI), zap.Error(err))
			return
		}
	}

	return
}

func (a *App) Get(id string) *TestCase {
	url := fmt.Sprintf("%s/regression/testcase/%s", a.Host, id)
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
	req.Header.Set("content-type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (a *App) fetch() []TestCase {
	url := fmt.Sprintf("%s/regression/testcase?app=%s", a.Host, a.Name)
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
