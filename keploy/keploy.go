package keploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"google.golang.org/grpc"

	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"

	// "github.com/benbjohnson/clock"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keploy/go-sdk/mock"
	"github.com/keploy/go-sdk/pkg/keploy"
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/grpc/utils"
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

type GrpcResp struct {
	Resp models.GrpcResp
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
	App         AppConfig
	Server      ServerConfig
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
	Remove         []string
	Replace        map[string]string
	RejectUrlRegex []string
}

type ServerConfig struct {
	URL        string `default:"http://localhost:6789/api"`
	LicenseKey string
	AsyncCalls bool
	GrpcEnabled bool
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
	if cfg.Server.GrpcEnabled {
		conn, err := grpc.Dial("localhost:6789", grpc.WithInsecure())
		if err != nil {
			logger.Error(":x: Failed to connect to keploy server via grpc. Please ensure that keploy server is running", zap.Error(err))
		}
		grpcClient := proto.NewRegressionServiceClient(conn)
		keploy.SetGrpcClient(grpcClient)
		k.grpcClient = grpcClient
	}
	k.Ctx = context.Background()
	if k.cfg.Server.AsyncCalls {
		k.Ctx = mock.NewContext(mock.Config{
			Mode:      GetMode(),
			Name:      k.cfg.App.Name,
			CTX:       context.Background(),
			Path:      strings.TrimSuffix(cfg.App.MockPath, "/mocks"),
			OverWrite: true,
		})
	}
	if GetMode() == keploy.MODE_TEST {
		go k.Test()
	}
	return k
}

type Keploy struct {
	cfg        Config
	Ctx        context.Context
	Log        *zap.Logger
	client     *http.Client
	grpcClient proto.RegressionServiceClient
	deps       sync.Map
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

func (k *Keploy) GetRespGrpc(id string) GrpcResp {
	val, ok := k.resp.Load(id)
	if !ok {
		k.Log.Error("failed getting response for grpc request", zap.String("test case id", id))
		return GrpcResp{}
	}
	resp, ok := val.(GrpcResp)
	if !ok {
		k.Log.Error("stored grpc response type is invalid", zap.String("test case id", id))
		return GrpcResp{}
	}
	return resp
}

func (k *Keploy) PutResp(id string, resp HttpResp) {
	k.resp.Store(id, resp)
}

func (k *Keploy) PutRespGrpc(id string, resp GrpcResp) {
	k.resp.Store(id, resp)
}

// Capture will capture request, response and output of external dependencies by making Call to keploy server.
func (k *Keploy) Capture(req regression.TestCaseReq) {
	// req.Path, _ = os.Getwd()
	req.Remove = k.cfg.App.Filter.Remove   //Setting the Remove field from config
	req.Replace = k.cfg.App.Filter.Replace //Setting the Replace field from config
	go k.put(req)
}

// Test fetches the testcases from the keploy server and current response of API. Then, both of the responses are sent back to keploy's server for comparision.
func (k *Keploy) Test() {
	// fetch test cases from web server and save to memory
	k.Log.Info("test starting in " + k.cfg.App.Delay.String())
	time.Sleep(k.cfg.App.Delay)
	tcs := k.fetch(models.HTTP)
	tcs = append(tcs, k.fetch(models.GRPC_EXPORT)...)
	total := len(tcs)

	// start a http test run

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

	// end the http test run
	err = k.end(id, passed)
	if err != nil {
		k.Log.Error("failed to end test run", zap.Error(err))
		return
	}
	k.Log.Info("test run completed", zap.String("run id", id), zap.Bool("passed overall", passed))
	result <- passed
}

func (k *Keploy) start(total int) (string, error) {
	var resp map[string]string
	if k.cfg.Server.GrpcEnabled{
		res, err := k.grpcClient.Start(k.Ctx, &proto.StartRequest{
			Total:        strconv.Itoa(total),
			App:          k.cfg.App.Name,
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
		})
		if err != nil {
			return "", err
		}
		result, _ := res.Descriptor()
		err = json.Unmarshal(result, &resp)

	} else {

		url := fmt.Sprintf("%s/regression/start?app=%s&total=%d&testCasePath=%s&mockPath=%s", k.cfg.Server.URL, k.cfg.App.Name, total, k.cfg.App.TestPath, k.cfg.App.MockPath)
		body, err := k.newGet(url)
		if err != nil {
			return "", err
		}

		err = json.Unmarshal(body, &resp)
		if err != nil {
			return "", err
		}
	}
	return resp["id"], nil
}

func (k *Keploy) end(id string, status bool) error {
	if k.cfg.Server.GrpcEnabled {
		_, err := k.grpcClient.End(k.Ctx, &proto.EndRequest{
			Id:     id,
			Status: strconv.FormatBool(status),
		})
		if err != nil {
			return err
		}

	} else {
		url := fmt.Sprintf("%s/regression/end?status=%t&id=%s", k.cfg.Server.URL, status, id)
		_, err := k.newGet(url)
		if err != nil {
			return err
		}
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

func (k *Keploy) simulateGrpc(tc models.TestCase) (models.GrpcResp, error) {
	// add dependencies to shared context
	k.deps.Store(tc.ID, tc.Deps)
	defer k.deps.Delete(tc.ID)
	// add mocks to shared context
	k.mocks.Store(tc.ID, tc.Mocks)
	defer k.mocks.Delete(tc.ID)
	k.mocktime.Store(tc.ID, tc.Captured)
	defer k.mocktime.Delete(tc.ID)
	tid := string(tc.ID)
	port := k.cfg.App.Port
	if port[0] != ':' {
		port = ":" + port
	}
	m := sync.Mutex{}
	m.Lock()
	k.PutRespGrpc(tc.ID, GrpcResp{L: &m})

	// The simulate call is done via grpcurl which acts as a grpc client
	err := GrpCurl(tc.GrpcReq.Body, `tid:`+tid, "localhost"+port, tc.GrpcReq.Method)
	if err != nil {
		k.Log.Error("failed to simulate grpc request", zap.String("testcase id:", tc.ID), zap.Error(err))
	}
	m.Lock()
	m.Unlock()
	resp := k.GetRespGrpc(tc.ID)
	defer k.resp.Delete(tc.ID)
	return resp.Resp, nil
}

func (k *Keploy) check(runId string, tc models.TestCase) bool {
	var (
		resp     *models.HttpResp
		respGrpc models.GrpcResp
		bin      []byte
		err      error
	)
	switch tc.Type {
	case string(models.HTTP):
		resp, err = k.simulate(tc)
		if err != nil {
			k.Log.Error("failed to simulate request on local server", zap.Error(err))
			return false
		}

		bin, err = json.Marshal(&regression.TestReq{
			ID:           tc.ID,
			AppID:        k.cfg.App.Name,
			RunID:        runId,
			Resp:         *resp,
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
			Type:         models.HTTP,
		})

	case string(models.GRPC_EXPORT):
		respGrpc, err = k.simulateGrpc(tc)
		if err != nil {
			k.Log.Error("failed to simulate request on local server", zap.Error(err))
			return false
		}

		bin, err = json.Marshal(&regression.TestReq{
			ID:           tc.ID,
			AppID:        k.cfg.App.Name,
			RunID:        runId,
			GrpcResp:     respGrpc,
			Type:         models.GRPC_EXPORT,
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
		})
	}

	if err != nil {
		k.Log.Error("failed to marshal testcase request", zap.String("url", tc.URI), zap.Error(err))
		return false
	}

	// test application reponse
	if k.cfg.Server.GrpcEnabled {
		r, err := k.grpcClient.Test(k.Ctx, &proto.TestReq{
			ID:    tc.ID,
			AppID: k.cfg.App.Name,
			RunID: runId,
			Resp: &proto.HttpResp{
				Body:          resp.Body,
				Header:        utils.GetProtoMap(resp.Header),
				StatusCode:    int64(resp.StatusCode),
				StatusMessage: resp.StatusMessage,
				ProtoMajor:    int64(resp.ProtoMajor),
				ProtoMinor:    int64(resp.ProtoMinor),
				Binary:        resp.Binary,
			},
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
		})
		if err != nil {
			k.Log.Error("failed to create test request request server", zap.String("id", tc.ID), zap.String("url", tc.URI), zap.Error(err))
			return false
		}
		res := r.GetPass()
		if res["pass"] {
			return true
		}

	} else {
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

	if tcs.Type == models.HTTP {
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
	}
	if k.cfg.Server.GrpcEnabled {
		resp, err := k.grpcClient.PostTC(k.Ctx, &proto.TestCaseReq{
			Captured: tcs.Captured,
			URI:      tcs.URI,
			AppID:    tcs.AppID,
			HttpReq: &proto.HttpReq{
				Method:     string(tcs.HttpReq.Method),
				ProtoMajor: int64(tcs.HttpReq.ProtoMajor),
				ProtoMinor: int64(tcs.HttpReq.ProtoMinor),
				URL:        tcs.HttpReq.URL,
				URLParams:  tcs.HttpReq.URLParams,
				Header:     utils.GetProtoMap(tcs.HttpReq.Header),
				Body:       tcs.HttpReq.Body,
				Binary:     tcs.HttpReq.Binary,
				Form:       GetProtoFormData(tcs.HttpReq.Form),
			},
			HttpResp: &proto.HttpResp{
				StatusCode:    int64(tcs.HttpResp.StatusCode),
				ProtoMajor:    int64(tcs.HttpResp.ProtoMajor),
				ProtoMinor:    int64(tcs.HttpResp.ProtoMinor),
				Header:        utils.GetProtoMap(tcs.HttpResp.Header),
				Body:          tcs.HttpResp.Body,
				StatusMessage: tcs.HttpResp.StatusMessage,
				Binary:        tcs.HttpResp.Binary,
			},
			Dependency:   ModelDepsToProtoDeps(tcs.Deps),
			TestCasePath: tcs.TestCasePath,
			MockPath:     tcs.MockPath,
			Mocks:        tcs.Mocks,
			Type:         string(tcs.Type),
			Remove:       tcs.Remove,
			Replace:      tcs.Replace,
			GrpcReq: &proto.GrpcReq{
				Body:   tcs.GrpcReq.Body,
				Method: tcs.GrpcReq.Method,
			},
			GrpcResp: &proto.GrpcResp{
				Body: tcs.GrpcResp.Body,
				Err:  tcs.GrpcResp.Err,
			},
		})
		if err != nil {
			k.Log.Error("failed to send testcase to backend", zap.String("url", tcs.URI), zap.Error(err))
			return
		}
		res := resp.GetTcsId()

		id := res["id"]
		if id == "" {
			return
		}
		k.denoise(id, tcs)
	} else {
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
}

func (k *Keploy) denoise(id string, tcs regression.TestCaseReq) {
	// run the request again to find noisy fields
	time.Sleep(2 * time.Second)
	var (
		err       error
		resp2     *models.HttpResp
		resp2Grpc models.GrpcResp
		bin2      []byte
	)
	switch tcs.Type {
	case models.HTTP:
		if strings.Contains(strings.Join(tcs.HttpReq.Header["Content-Type"], ", "), "multipart/form-data") {
			bin, err := base64.StdEncoding.DecodeString(tcs.HttpReq.Body)
			if err != nil {
				k.Log.Error("failed to decode the base64 encoded request body", zap.Error(err))
				return
			}
			tcs.HttpReq.Body = string(bin)
		}
		resp2, err = k.simulate(models.TestCase{
			ID:       id,
			Captured: tcs.Captured,
			URI:      tcs.URI,
			HttpReq:  tcs.HttpReq,
			Deps:     tcs.Deps,
			Mocks:    tcs.Mocks,
		})
		if err != nil {
			k.Log.Error("failed to simulate request on local http server", zap.Error(err))
			return
		}

		bin2, err = json.Marshal(&regression.TestReq{
			ID:           id,
			AppID:        k.cfg.App.Name,
			Resp:         *resp2,
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
			Type:         models.HTTP,
		})

	case models.GRPC_EXPORT:
		resp2Grpc, err = k.simulateGrpc(models.TestCase{
			ID:       id,
			Captured: tcs.Captured,
			Deps:     tcs.Deps,
			Mocks:    tcs.Mocks,
			// GrpcMethod: tcs.GrpcMethod,
			GrpcReq: tcs.GrpcReq,
		})
		if err != nil {
			k.Log.Error("failed to simulate request on local grpc server", zap.Error(err))
			return
		}

		bin2, err = json.Marshal(&regression.TestReq{
			ID:           id,
			AppID:        k.cfg.App.Name,
			GrpcResp:     resp2Grpc,
			Type:         models.GRPC_EXPORT,
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
		})
	}
	if err != nil {
		k.Log.Error("failed to marshall testcase request", zap.String("url", tcs.URI), zap.Error(err))
		return
	}

	// send de-noise request to server
	if k.cfg.Server.GrpcEnabled {
		_, err = k.grpcClient.DeNoise(k.Ctx, &proto.TestReq{
			ID:    id,
			AppID: k.cfg.App.Name,
			Resp: &proto.HttpResp{
				Body:          resp2.Body,
				Header:        utils.GetProtoMap(resp2.Header),
				StatusCode:    int64(resp2.StatusCode),
				StatusMessage: resp2.StatusMessage, //when it has to be done it will be done and it should be doe
				ProtoMajor:    int64(resp2.ProtoMajor),
				ProtoMinor:    int64(resp2.ProtoMinor),
				Binary:        resp2.Binary,
			},
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
		})
		if err != nil {
			k.Log.Error("failed to send de-noise request to backend", zap.String("url", tcs.URI), zap.Error(err))
			return
		}

	} else {
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

func (k *Keploy) fetch(reqType models.Kind) []models.TestCase {
	var tcs []models.TestCase = []models.TestCase{}
	pageSize := 25
	for i := 0; ; i += pageSize {
		var res []models.TestCase
		if k.cfg.Server.GrpcEnabled {
			resp, err := k.grpcClient.GetTCS(k.Ctx, &proto.GetTCSRequest{
				App:          k.cfg.App.Name,
				Offset:       strconv.Itoa(i),
				Limit:        strconv.Itoa(pageSize),
				TestCasePath: k.cfg.App.TestPath,
				MockPath:     k.cfg.App.MockPath,
			})
			if err != nil {
				k.Log.Error("failed to fetch testcases from keploy cloud", zap.Error(err))
				return nil
			}
			res = ProtoToModelsTestCase(resp.GetTcs())
			tcs = append(tcs, res...)
			if len(res) < pageSize {
				break
			}
			if resp.Eof {
				break
			}

		} else {
			url := fmt.Sprintf("%s/regression/testcase?app=%s&offset=%d&limit=%d&testCasePath=%s&mockPath=%s&reqType=%s", k.cfg.Server.URL, k.cfg.App.Name, i, 25, k.cfg.App.TestPath, k.cfg.App.MockPath, reqType)

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
