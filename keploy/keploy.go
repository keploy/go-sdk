package keploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"
	"github.com/keploy/go-sdk/internal/keploy"
	"github.com/keploy/go-sdk/mock"
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/http/regression"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (

	// mode   = keploy.MODE_OFF
	result       = make(chan bool, 1)
	RespChannels = map[string]chan bool{}
)

type HttpResp struct {
	Resp models.HttpResp
	L    *sync.Mutex
}

// // To avoid creating the duplicate mock yaml file
// func (m *MockLib) Unique(name string) bool {
// 	_, ok := m.mockIds.Load(name)
// 	return !ok
// }
// func (m *MockLib) Load(name string) {
// 	m.mockIds.Store(name, true)
// }

func init() {
	m := keploy.Mode(os.Getenv("KEPLOY_MODE"))
	if m == "" {
		return
	}
	err := keploy.SetMode(m)
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
	Name     string        `validate:"required"`
	Host     string        `default:"0.0.0.0"`
	Port     string        `validate:"required"`
	Delay    time.Duration `default:"5s"`
	Timeout  time.Duration `default:"60s"`
	Filter   Filter
	TestPath string `default:""`
	MockPath string `default:""`
}

type Filter struct {
	AcceptUrlRegex string
	HeaderRegex    []string
	RejectUrlRegex []string
}

type ServerConfig struct {
	URL        string `default:"http://localhost:6789/api"`
	LicenseKey string
	AsyncCalls bool
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

	if len(cfg.App.TestPath) > 0 && cfg.App.TestPath[0] != '/' {
		path, err := filepath.Abs(cfg.App.TestPath)
		if err != nil {
			logger.Error("Failed to get the absolute path from relative conf.path", zap.Error(err))
		}
		cfg.App.TestPath = path
	} else if len(cfg.App.TestPath) == 0 {
		path, err := os.Getwd()
		if err != nil {
			logger.Error("Failed to get the path of current directory", zap.Error(err))
		}
		cfg.App.TestPath = path + "/keploy/tests"
	}
	if len(cfg.App.MockPath) > 0 && cfg.App.MockPath[0] != '/' {
		path, err := filepath.Abs(cfg.App.MockPath)
		if err != nil {
			logger.Error("Failed to get the absolute path from relative conf.path", zap.Error(err))
		}
		cfg.App.MockPath = path
	} else if len(cfg.App.MockPath) == 0 {
		path, err := os.Getwd()
		if cfg.App.TestPath == "" {
			logger.Error("Failed to get the path of current directory", zap.Error(err))
		}
		cfg.App.MockPath = path + "/keploy/mocks"
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
	if k.cfg.Server.AsyncCalls {
		k.Ctx = mock.NewContext(mock.Config{
			Mode:      GetMode(),
			Name:      k.cfg.App.Name,
			CTX:       context.Background(),
			Path:      cfg.App.TestPath,
			OverWrite: true,
		})
	}
	if GetMode() == keploy.MODE_TEST {
		go k.Test()
	}
	return k
}

type Keploy struct {
	cfg    Config
	Ctx    context.Context
	Log    *zap.Logger
	client *http.Client
	deps   sync.Map
	//Deps map[string][]models.Dependency
	resp sync.Map
	//Resp map[string]models.HttpResp
	mocktime sync.Map
	mocks    sync.Map
}

func (k *Keploy) GetMocks(id string) []*proto.Mock {
	val, ok := k.mocks.Load(id)
	if !ok {
		return nil
	}
	mocks, ok := val.([]*proto.Mock)
	if !ok {
		k.Log.Error("failed fetching dependencies for testcases", zap.String("test case id", id))
		return nil
	}
	return mocks
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

func (k *Keploy) GetResp(id string) HttpResp {
	val, ok := k.resp.Load(id)
	if !ok {
		return HttpResp{}
	}
	resp, ok := val.(HttpResp)
	if !ok {
		k.Log.Error("failed getting response for http request", zap.String("test case id", id))
		return HttpResp{}
	}
	return resp
}

func (k *Keploy) PutResp(id string, resp HttpResp) {
	k.resp.Store(id, resp)
}

// Capture will capture request, response and output of external dependencies by making Call to keploy server.
func (k *Keploy) Capture(req regression.TestCaseReq) {
	// req.Path, _ = os.Getwd()
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
	url := fmt.Sprintf("%s/regression/start?app=%s&total=%d&testCasePath=%s&mockPath=%s", k.cfg.Server.URL, k.cfg.App.Name, total, k.cfg.App.TestPath, k.cfg.App.MockPath)
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

	// add mocks to shared context
	k.mocks.Store(tc.ID, tc.Mocks)
	defer k.mocks.Delete(tc.ID)

	k.mocktime.Store(tc.ID, tc.Captured)
	defer k.mocktime.Delete(tc.ID)

	ctx := context.WithValue(context.Background(), keploy.KTime, tc.Captured)
	req, err := http.NewRequestWithContext(ctx, string(tc.HttpReq.Method), "http://"+k.cfg.App.Host+":"+k.cfg.App.Port+tc.HttpReq.URL, bytes.NewBufferString(tc.HttpReq.Body))
	if err != nil {
		panic(err)
	}
	req.Header = tc.HttpReq.Header
	req.Header.Set("KEPLOY_TEST_ID", tc.ID)
	req.ProtoMajor = tc.HttpReq.ProtoMajor
	req.ProtoMinor = tc.HttpReq.ProtoMinor
	req.Close = true

	m := sync.Mutex{}
	m.Lock()
	k.PutResp(tc.ID, HttpResp{L: &m})

	httpresp, err := k.client.Do(req)
	if err != nil {
		k.Log.Error("failed sending testcase request to app", zap.Error(err))
		return nil, err
	}

	_, err = ioutil.ReadAll(httpresp.Body)
	if err != nil {
		k.Log.Error("failed reading simulated response from app", zap.Error(err))
		return nil, err
	}

	// Since, execution of simulate function continues post http.ResponseWriter.Flush therefore it needs to ensure that
	// response has been written to map for the testcase id before accessing
	m.Lock()
	defer m.Unlock()

	resp := k.GetResp(tc.ID)
	defer k.resp.Delete(tc.ID)

	return &resp.Resp, nil
}

func (k *Keploy) check(runId string, tc models.TestCase) bool {
	resp, err := k.simulate(tc)
	if err != nil {
		k.Log.Error("failed to simulate request on local server", zap.Error(err))
		return false
	}
	bin, err := json.Marshal(&regression.TestReq{
		ID:           tc.ID,
		AppID:        k.cfg.App.Name,
		RunID:        runId,
		Resp:         *resp,
		TestCasePath: k.cfg.App.TestPath,
		MockPath:     k.cfg.App.MockPath,
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

// isValidHeader checks the valid header to filter out testcases
// It returns true when any of the header matches with regular expression and returns false when it doesn't match.
func (k *Keploy) isValidHeader(tcs regression.TestCaseReq) bool {
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
	return valid
}

// isRejectedUrl checks whether the request url matches any of the excluded
// urls which should not be recorded. It returns true, if any of the RejectUrlRegex
// matches to current url.
func (k *Keploy) isRejectedUrl(tcs regression.TestCaseReq) bool {
	var fil = k.cfg.App.Filter
	var t = tcs.HttpReq.URL
	var valid bool = true
	for _, v := range fil.RejectUrlRegex {
		headReg := regexp.MustCompile(v)
		if headReg.FindString(t) != "" {
			valid = false
			break
		}

		if !valid {
			break
		}
	}
	return valid
}

func (k *Keploy) put(tcs regression.TestCaseReq) {

	var fil = k.cfg.App.Filter

	if fil.HeaderRegex != nil {
		if !k.isValidHeader(tcs) {
			return
		}
	}
	if fil.RejectUrlRegex != nil {
		if !k.isRejectedUrl(tcs) {
			return
		}
	}

	reg := regexp.MustCompile(fil.AcceptUrlRegex)
	if fil.AcceptUrlRegex != "" && reg.FindString(tcs.URI) == "" {
		return
	}

	if strings.Contains(strings.Join(tcs.HttpReq.Header["Content-Type"], ", "), "multipart/form-data") {
		tcs.HttpReq.Body = base64.StdEncoding.EncodeToString([]byte(tcs.HttpReq.Body))
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
	if strings.Contains(strings.Join(tcs.HttpReq.Header["Content-Type"], ", "), "multipart/form-data") {
		bin, err := base64.StdEncoding.DecodeString(tcs.HttpReq.Body)
		if err != nil {
			k.Log.Error("failed to decode the base64 encoded request body", zap.Error(err))
			return
		}
		tcs.HttpReq.Body = string(bin)
	}

	resp2, err := k.simulate(models.TestCase{
		ID:       id,
		Captured: tcs.Captured,
		URI:      tcs.URI,
		HttpReq:  tcs.HttpReq,
		Deps:     tcs.Deps,
		Mocks:    tcs.Mocks,
	})
	if err != nil {
		k.Log.Error("failed to simulate request on local server", zap.Error(err))
		return
	}

	bin2, err := json.Marshal(&regression.TestReq{
		ID:           id,
		AppID:        k.cfg.App.Name,
		Resp:         *resp2,
		TestCasePath: k.cfg.App.TestPath,
		MockPath:     k.cfg.App.MockPath,
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
	pageSize := 25

	for i := 0; ; i += pageSize {
		url := fmt.Sprintf("%s/regression/testcase?app=%s&offset=%d&limit=%d&testCasePath=%s&mockPath=%s", k.cfg.Server.URL, k.cfg.App.Name, i, 25, k.cfg.App.TestPath, k.cfg.App.MockPath)

		req, err := http.NewRequest("GET", url, http.NoBody)
		if err != nil {
			k.Log.Error("failed to fetch testcases from keploy cloud", zap.Error(err))
			return nil
		}
		k.setKey(req)
		resp, err := k.client.Do(req)
		if err != nil {
			k.Log.Error("failed to fetch testcases from keploy cloud", zap.Error(err))
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			k.Log.Error("failed to fetch testcases from keploy cloud", zap.Error(errors.New("failed to send get request: "+resp.Status)))
			return nil
		}

		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
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
		tcs = append(tcs, res...)
		if len(res) < pageSize {
			break
		}
		eof := resp.Header.Get("EOF")
		if eof == "true" {
			break
		}
	}

	for i, j := range tcs {
		if strings.Contains(strings.Join(j.HttpReq.Header["Content-Type"], ", "), "multipart/form-data") {
			bin, err := base64.StdEncoding.DecodeString(j.HttpReq.Body)
			if err != nil {
				k.Log.Error("failed to decode the base64 encoded request body", zap.Error(err))
				return nil
			}
			tcs[i].HttpReq.Body = string(bin)
		}
	}
	return tcs
}

func (k *Keploy) setKey(req *http.Request) {
	if k.cfg.Server.LicenseKey != "" {
		req.Header.Set("key", k.cfg.Server.LicenseKey)
	}
}
