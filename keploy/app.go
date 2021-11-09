package keploy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"
)

func NewApp(name, licenseKey, host string) *App {
	if host == "" {
		host = "http://localhost:8081"
	}
	return &App{
		Name:       name,
		LicenseKey: licenseKey,
		Host: host,
	}
}

type App struct {
	Name string
	LicenseKey string
	Host string
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
	req, err := http.NewRequest(string(tc.HttpReq.Method), tc.HttpReq.URL, bytes.NewBufferString(tc.HttpReq.Body))
	if err != nil {
		panic(err)
	}
	req.Header = tc.HttpReq.Header
	req.ProtoMajor = tc.HttpReq.ProtoMajor
	req.ProtoMinor = tc.HttpReq.ProtoMinor

	client := &http.Client{
		Timeout: time.Second * 600,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("An error occurred %v", err)
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalln(err)
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
		fmt.Println("incorrect headers", resp.Header,tc.HttpResp.Header)
		return false
	case tc.HttpResp.Body != string(body):
		fmt.Println("body mismatch", tc.HttpResp.Body,string(body))
		return true
	}
	return true
}

func (a *App) put(tcs TestCaseReq) {
	bin, err := json.Marshal(tcs)
	if err != nil {
		panic(err)
	}
	req, err := http.NewRequest("POST", a.Host + "/regression/testcase", bytes.NewBuffer(bin))
	if err != nil {
		log.Fatalf("An error occurred %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("key", a.LicenseKey)
	req.Header.Set("content-type", "application/json")

	client := &http.Client{
		Timeout: time.Second * 600,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("An error occurred %v", err)
	}

	defer resp.Body.Close()
	_, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalln(err)
	}
	return
}

func (a *App) fetch() []TestCase {
	url := fmt.Sprintf("%s/regression/testcase?app=%s", a.Host, a.Name)

	req, err := http.NewRequest("GET", url, http.NoBody)
	req.Header.Set("key", a.LicenseKey)
	req.Header.Set("content-type", "application/json")
	if err != nil {
		log.Fatalf("An error occurred %v", err)
	}

	client := &http.Client{
		Timeout: time.Second * 600,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("An error occurred %v", err)
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalln(err)
	}

	var tcs []TestCase

	err = json.Unmarshal(body, &tcs)
	if err != nil {
		panic(err)
	}
	return tcs
}
