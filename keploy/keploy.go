package keploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"
	// "github.com/benbjohnson/clock"
	"go.keploy.io/server/http/regression"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"
)

var result = make(chan bool, 1)

// mode is set to record, if unset
var mode = MODE_RECORD

func init() {
	m := Mode(os.Getenv("KEPLOY_MODE"))
	if m == "" {
		return
	}
	err := SetMode(m)
	if err != nil {
		fmt.Println("warning: ", err)
	}
}

func AssertTests(t *testing.T) {
	r := <-result
	if !r {
		t.Error("Keploy test suite failed")
	}
}

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
// host and port parameters contains the host and port of API to be tested.

type Config struct {
	App    AppConfig
	Server ServerConfig
}

type AppConfig struct {
	Name    string        `validate:"required"`
	Host    string        `default:"0.0.0.0"`
	Port    string        `validate:"required"`
	Delay   time.Duration `default:"5s"`
	Timeout time.Duration `default:"60s"`
	Filter  Filter
}

type Filter struct {
	UrlRegex    string
	HeaderRegex []string
}

type ServerConfig struct {
	URL        string `default:"https://api.keploy.io"`
	LicenseKey string
}

func New(cfg Config) *Keploy {
	zcfg := zap.NewDevelopmentConfig()
	zcfg.EncoderConfig.CallerKey = zapcore.OmitKey
	zcfg.EncoderConfig.LevelKey = zapcore.OmitKey
	zcfg.EncoderConfig.TimeKey = zapcore.OmitKey

	logger, err := zcfg.Build()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()
	if err != nil {
		panic(err)
	}
	// set defaults
	if err = defaults.Set(&cfg); err != nil {
		logger.Error("failed to set default values to keploy conf", zap.Error(err))
	}

	validate := validator.New()
	err = validate.Struct(&cfg)
	if err != nil {
		logger.Error("conf missing important field", zap.Error(err))
	}

	k := &Keploy{
		cfg: cfg,
		Log: logger,
		client: &http.Client{
			Timeout: cfg.App.Timeout,
		},
		deps:     sync.Map{},
		resp:     sync.Map{},
		mocktime: sync.Map{},
	}
	if mode == MODE_TEST {
		go k.Test()
	}
	return k
}

type Keploy struct {
	cfg    Config
	Log    *zap.Logger
	client *http.Client
	deps   sync.Map
	//Deps map[string][]models.Dependency
	resp sync.Map
	//Resp map[string]models.HttpResp
	mocktime sync.Map
}

func (k *Keploy) GetDependencies(id string) []models.Dependency {
	val, ok := k.deps.Load(id)
	if !ok {
		return nil
	}
	deps, ok := val.([]models.Dependency)
	if !ok {
		k.Log.Error("failed fetching dependencies for testcases", zap.String("test case id", id))
		return nil
	}
	return deps
}

func (k *Keploy) GetClock(id string) int64 {
	val, ok := k.mocktime.Load(id)
	if !ok {
		return 0
	}
	mocktime, ok := val.(int64)
	if !ok {
		k.Log.Error("failed getting time for http request", zap.String("test case id", id))
		return 0
	}

	return mocktime
}

func (k *Keploy) GetResp(id string) models.HttpResp {
	val, ok := k.resp.Load(id)
	if !ok {
		return models.HttpResp{}
	}
	resp, ok := val.(models.HttpResp)
	if !ok {
		k.Log.Error("failed getting response for http request", zap.String("test case id", id))
		return models.HttpResp{}
	}
	return resp
}

func (k *Keploy) PutResp(id string, resp models.HttpResp) {
	k.resp.Store(id, resp)
}

// Capture will capture request, response and output of external dependencies by making Call to keploy server.
func (k *Keploy) Capture(req regression.TestCaseReq) {
	go k.put(req)
}

// Test fetches the testcases from the keploy server and current response of API. Then, both of the responses are sent back to keploy's server for comparision.
func (k *Keploy) Test() {
	// fetch test cases from web server and save to memory
	k.Log.Info("test starting in " + k.cfg.App.Delay.String())
	time.Sleep(k.cfg.App.Delay)
	tcs := k.fetch()
	total := len(tcs)

	// start a test run
	id, err := k.start(total)
	if err != nil {
		k.Log.Error("failed to start test run", zap.Error(err))
		return
	}

	k.Log.Info("starting test execution", zap.String("id", id), zap.Int("total tests", total))
	passed := true
	// call the service for each test case
	var wg sync.WaitGroup
	maxGoroutines := 10
	guard := make(chan struct{}, maxGoroutines)
	for i, tc := range tcs {
		k.Log.Info(fmt.Sprintf("testing %d of %d", i+1, total), zap.String("testcase id", tc.ID))
		guard <- struct{}{}
		wg.Add(1)
		tcCopy := tc
		go func() {
			ok := k.check(id, tcCopy)
			if !ok {
				passed = false
			}
			k.Log.Info("result", zap.String("testcase id", tcCopy.ID), zap.Bool("passed", ok))
			<-guard
			wg.Done()
		}()
	}
	wg.Wait()

	// end the test run
	err = k.end(id, passed)
	if err != nil {
		k.Log.Error("failed to end test run", zap.Error(err))
		return
	}
	k.Log.Info("test run completed", zap.String("run id", id), zap.Bool("passed overall", passed))
	result <- passed
}

func (k *Keploy) start(total int) (string, error) {
	url := fmt.Sprintf("%s/regression/start?app=%s&total=%d", k.cfg.Server.URL, k.cfg.App.Name, total)
	body, err := k.newGet(url)
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

func (k *Keploy) end(id string, status bool) error {
	url := fmt.Sprintf("%s/regression/end?status=%t&id=%s", k.cfg.Server.URL, status, id)
	_, err := k.newGet(url)
	if err != nil {
		return err
	}
	return nil
}

func (k *Keploy) simulate(tc models.TestCase) (*models.HttpResp, error) {
	// add dependencies to shared context
	k.deps.Store(tc.ID, tc.Deps)
	defer k.deps.Delete(tc.ID)
	// mock := clock.NewMock()
	// t:=tc.Captured
	// mock.Add(time.Duration(t) * time.Second)
	// tc.Captured = mock.Now().UTC().Unix()
	k.mocktime.Store(tc.ID, tc.Captured)
	defer k.mocktime.Delete(tc.ID)
	//k.Deps[tc.ID] = tc.Deps
	//defer delete(k.Deps, tc.ID)
	req, err := http.NewRequest(string(tc.HttpReq.Method), "http://"+k.cfg.App.Host+":"+k.cfg.App.Port+tc.HttpReq.URL, bytes.NewBufferString(tc.HttpReq.Body))
	if err != nil {
		panic(err)
	}
	req.Header = tc.HttpReq.Header
	req.Header.Set("KEPLOY_TEST_ID", tc.ID)
	req.ProtoMajor = tc.HttpReq.ProtoMajor
	req.ProtoMinor = tc.HttpReq.ProtoMinor

	httpresp, err := k.client.Do(req)
	if err != nil {
		k.Log.Error("failed sending testcase request to app", zap.Error(err))
		return nil, err
	}

	defer httpresp.Body.Close()
	resp := k.GetResp(tc.ID)
	defer k.resp.Delete(tc.ID)

	body, err := ioutil.ReadAll(httpresp.Body)
	if err != nil {
		k.Log.Error("failed reading simulated response from app", zap.Error(err))
		return nil, err
	}
  
	if (resp.StatusCode < 300 || resp.StatusCode >= 400) && resp.Body != string(body) {
		resp.Body = string(body)
		resp.Header = httpresp.Header
		resp.StatusCode = httpresp.StatusCode
	}

	return &resp, nil
}

func (k *Keploy) check(runId string, tc models.TestCase) bool {
	resp, err := k.simulate(tc)
	if err != nil {
		k.Log.Error("failed to simulate request on local server", zap.Error(err))
		return false
	}
	bin, err := json.Marshal(&regression.TestReq{
		ID:    tc.ID,
		AppID: k.cfg.App.Name,
		RunID: runId,
		Resp:  *resp,
	})
	if err != nil {
		k.Log.Error("failed to marshal testcase request", zap.String("url", tc.URI), zap.Error(err))
		return false
	}

	// test application reponse
	r, err := http.NewRequest("POST", k.cfg.Server.URL+"/regression/test", bytes.NewBuffer(bin))
	if err != nil {
		k.Log.Error("failed to create test request request server", zap.String("id", tc.ID), zap.String("url", tc.URI), zap.Error(err))
		return false
	}
	k.setKey(r)
	r.Header.Set("Content-Type", "application/json")

	resp2, err := k.client.Do(r)
	if err != nil {
		k.Log.Error("failed to send test request to backend", zap.String("url", tc.URI), zap.Error(err))
		return false
	}
	var res map[string]bool
	b, err := ioutil.ReadAll(resp2.Body)
	if err != nil {
		k.Log.Error("failed to read response from backend", zap.String("url", tc.URI), zap.Error(err))
	}
	err = json.Unmarshal(b, &res)
	if err != nil {
		k.Log.Error("failed to read test result from keploy cloud", zap.Error(err))
		return false
	}
	if res["pass"] {
		return true
	}
	return false
}

func (k *Keploy) getHeaderFilter(tcs regression.TestCaseReq) bool {
    var fil = k.cfg.App.Filter
    var t = tcs.HttpReq.Header
    var valid bool = false
    for _, v := range fil.HeaderRegex {
        headReg := regexp.MustCompile(v)
        for key := range t {
            if headReg.FindString(key) != "" {
                valid = true
                break
            }
        }
        if valid {
            break
        }
    }
    if !valid {
        return false
    }
    return true
}

func (k *Keploy) put(tcs regression.TestCaseReq) {

	var fil = k.cfg.App.Filter
	
	if fil.HeaderRegex != nil {
        if k.getHeaderFilter(tcs) == false {
            return
        }
    }

	reg := regexp.MustCompile(fil.UrlRegex)
	if fil.UrlRegex != "" && reg.FindString(tcs.URI) == "" {
		return
	}

	bin, err := json.Marshal(tcs)
	if err != nil {
		k.Log.Error("failed to marshall testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
	req, err := http.NewRequest("POST", k.cfg.Server.URL+"/regression/testcase", bytes.NewBuffer(bin))
	if err != nil {
		k.Log.Error("failed to create testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
	k.setKey(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		k.Log.Error("failed to send testcase to backend", zap.String("url", tcs.URI), zap.Error(err))
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
		k.Log.Error("failed to read response from backend", zap.String("url", tcs.URI), zap.Error(err))
	}
	err = json.Unmarshal(body, &res)
	if err != nil {
		k.Log.Error("failed to read testcases from keploy cloud", zap.Error(err))
		return
	}
	id := res["id"]
	if id == "" {
		return
	}
	k.denoise(id, tcs)
}

func (k *Keploy) denoise(id string, tcs regression.TestCaseReq) {
	// run the request again to find noisy fields
	time.Sleep(2 * time.Second)
	resp2, err := k.simulate(models.TestCase{
		ID:       id,
		Captured: tcs.Captured,
		URI:      tcs.URI,
		HttpReq:  tcs.HttpReq,
		Deps:     tcs.Deps,
	})
	if err != nil {
		k.Log.Error("failed to simulate request on local server", zap.Error(err))
		return
	}

	bin2, err := json.Marshal(&regression.TestReq{
		ID:    id,
		AppID: k.cfg.App.Name,
		Resp:  *resp2,
	})

	if err != nil {
		k.Log.Error("failed to marshall testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}

	// send de-noise request to server
	r, err := http.NewRequest("POST", k.cfg.Server.URL+"/regression/denoise", bytes.NewBuffer(bin2))
	if err != nil {
		k.Log.Error("failed to create de-noise request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
	k.setKey(r)
	r.Header.Set("Content-Type", "application/json")

	_, err = k.client.Do(r)
	if err != nil {
		k.Log.Error("failed to send de-noise request to backend", zap.String("url", tcs.URI), zap.Error(err))
		return
	}
}

func (k *Keploy) Get(id string) *models.TestCase {
	url := fmt.Sprintf("%s/regression/testcase/%s", k.cfg.Server.URL, id)
	body, err := k.newGet(url)
	if err != nil {
		k.Log.Error("failed to fetch testcases from keploy cloud", zap.Error(err))
		return nil
	}
	var tcs models.TestCase

	err = json.Unmarshal(body, &tcs)
	if err != nil {
		k.Log.Error("failed to read testcases from keploy cloud", zap.Error(err))
		return nil
	}
	return &tcs
}

func (k *Keploy) newGet(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, http.NoBody)
	if err != nil {
		return nil, err
	}
	k.setKey(req)
	resp, err := k.client.Do(req)
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

func (k *Keploy) fetch() []models.TestCase {
	var tcs []models.TestCase = []models.TestCase{}

	for i := 0; ; i += 25 {
		url := fmt.Sprintf("%s/regression/testcase?app=%s&offset=%d&limit=%d", k.cfg.Server.URL, k.cfg.App.Name, i, 25)
		body, err := k.newGet(url)
		if err != nil {
			k.Log.Error("failed to fetch testcases from keploy cloud", zap.Error(err))
			return nil
		}

		var res []models.TestCase
		err = json.Unmarshal(body, &res)
		if err != nil {
			k.Log.Error("failed to reading testcases from keploy cloud", zap.Error(err))
			return nil
		}
		if len(res) == 0 {
			break
		}
		tcs = append(tcs, res...)
	}
	return tcs
}

func (k *Keploy) setKey(req *http.Request) {
	if k.cfg.Server.LicenseKey != "" {
		req.Header.Set("key", k.cfg.Server.LicenseKey)
	}
}
