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

func NewApp(name, licenseKey, host string) *App {
	if host == "" {
		host = "http://localhost:8081"
	}
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer logger.Sync() // flushes buffer, if any

	return &App{
		Name:       name,
		LicenseKey: licenseKey,
		Host: host,
		Log: logger,
	}
}

type App struct {
	Name string
	LicenseKey string
	Host string
	Log *zap.Logger
}

func (a *App) Capture(req TestCaseReq) {
	a.put(req)
}

func(a *App) Test(host, port string)  {
	// fetch test cases from web server and save to memory
	time.Sleep(time.Second*5)
	tcs := a.fetch()
	// call the service for each test case
	for _, tc := range tcs {
		fmt.Println("testing: ", tc.ID)
		fmt.Println("testcase result: ", a.check(host, port, tc))
	}
	//
}

func (a *App) check(host , port string, tc TestCase) bool{
	req, err := http.NewRequest(string(tc.HttpReq.Method), "http://" + host + ":" + port + tc.URI, bytes.NewBufferString(tc.HttpReq.Body))
	if err != nil {
		panic(err)
	}
	req.Header = tc.HttpReq.Header
	req.Header.Set("KEPLOY_TEST_ID", tc.ID)
	req.ProtoMajor = tc.HttpReq.ProtoMajor
	req.ProtoMinor = tc.HttpReq.ProtoMinor

	client := &http.Client{
		Timeout: time.Second * 600,
	}
	resp, err := client.Do(req)
	if err != nil {
		a.Log.Error("failed sending testcase request to backend", zap.Error(err))
		return false
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		a.Log.Error("failed getting testcases from backend", zap.Error(err))
		return false
	}

	// TODO move this diff logic to server
	switch {
	//case tc.HttpResp.ProtoMajor != resp.ProtoMajor:
	//	fmt.Println("incorrect proto major", tc.HttpResp.ProtoMajor, resp.ProtoMajor)
	//	return false
	//case tc.HttpResp.ProtoMinor != resp.ProtoMinor:
	//	fmt.Println("incorrect proto minor", tc.HttpResp.ProtoMinor, resp.ProtoMinor)
	//	return false
	case compareHeaders(tc.HttpResp.Header, resp.Header):
		a.Log.Info("incorrect headers", zap.String("id", tc.ID), zap.String("uri", tc.URI), zap.Any("expected headers", tc.HttpResp.Header), zap.Any("actual headers", resp.Header))

		return false
	case tc.HttpResp.Body != string(body):
		a.Log.Info("body mismatch", zap.String("id", tc.ID), zap.String("uri", tc.URI), zap.Any("expected body", tc.HttpResp.Body), zap.String("actual body", string(body)))
		return false
	}
	return true
}

func (a *App) put(tcs TestCaseReq) {
	bin, err := json.Marshal(tcs)
	if err != nil {
		a.Log.Error("failed to marshall testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
	req, err := http.NewRequest("POST", a.Host + "/regression/testcase", bytes.NewBuffer(bin))
	if err != nil {
		a.Log.Error("failed to create testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("key", a.LicenseKey)
	req.Header.Set("content-type", "application/json")

	client := &http.Client{
		Timeout: time.Second * 600,
	}
	resp, err := client.Do(req)
	if err != nil {
		a.Log.Error("failed to send testcase to backend", zap.String("url", tcs.URI), zap.Error(err))
		return
	}

	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {
			a.Log.Error("failed to close connecton reader", zap.String("url", tcs.URI), zap.Error(err))
		}
	}(resp.Body)
	_, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		a.Log.Error("failed to read response from backend", zap.String("url", tcs.URI), zap.Error(err))
	}
	return
}

func (a *App) Get(id string) *TestCase {
	url := fmt.Sprintf("%s/regression/testcase/%s", a.Host, id)
	body,err := a.newGet(url)
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

func (a *App) newGet(url string) ([]byte, error){
	req, err := http.NewRequest("GET", url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("key", a.LicenseKey)
	req.Header.Set("content-type", "application/json")
	client := &http.Client{
		Timeout: time.Second * 600,
	}
	resp, err := client.Do(req)
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
	body,err := a.newGet(url)
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
