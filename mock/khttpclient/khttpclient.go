package khttpclient

import (
	"bytes"
	"context"
	"errors"
	"os"

	"io/ioutil"
	"net/http"

	"github.com/keploy/go-sdk/keploy"
	"github.com/keploy/go-sdk/mock"

	proto "go.keploy.io/server/grpc/regression"
	"go.uber.org/zap"
)

type Interceptor struct {
	core http.RoundTripper
	log  *zap.Logger
}

// NewInterceptor constructs and returns the pointer to Interceptor. Interceptor is used
// to intercept every http client calls and store their responses into keploy context.
func NewInterceptor(core http.RoundTripper) *Interceptor {
	// Initialize a logger
	logger, _ := zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()

	return &Interceptor{
		core: core,
		log:  logger,
	}
}

func (i Interceptor) RoundTrip(r *http.Request) (*http.Response, error) {
	if keploy.GetModeFromContext(r.Context()) == keploy.MODE_OFF {
		return i.core.RoundTrip(r)
	}

	// Read the request body to store in meta
	var reqBody []byte
	if r.Body != nil { // Read
		var err error
		reqBody, err = ioutil.ReadAll(r.Body)
		if err != nil {
			// TODO right way to log errors
			i.log.Error("Unable to read request body", zap.Error(err))
			return nil, err
		}
	}
	r.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset

	var (
		err  error
		resp *http.Response = &http.Response{}
	)
	kctx, er := keploy.GetState(r.Context())
	if er != nil {
		return nil, er
	}

	mode := kctx.Mode
	switch mode {
	case "test":
		//don't call i.core.RoundTrip method
		mock := kctx.Mock
		if len(mock) > 0 {
			resp.Body = ioutil.NopCloser(bytes.NewBuffer([]byte(mock[0].Spec.Response.Body)))
			resp.Header = mock[0].Spec.Response.Header
			resp.StatusCode = mock[0].Spec.Response.StatusCode
			mock = mock[1:]
		}
	case "record":
		resp, err = i.core.RoundTrip(r)
		var (
			respBody   []byte
			statusCode int64
			respHeader http.Header
		)
		if resp != nil {
			// Read the response body to capture
			if resp.Body != nil { // Read
				var err error
				respBody, err = ioutil.ReadAll(resp.Body)
				if err != nil {
					// TODO right way to log errors
					i.log.Error("Unable to read request body", zap.Error(err))
					return nil, err
				}
			}
			resp.Body = ioutil.NopCloser(bytes.NewBuffer(respBody)) // Reset
			statusCode = int64(resp.StatusCode)
			respHeader = resp.Header
		}

		path, err := os.Getwd()
		if err != nil {
			i.log.Error("cannot find current directory", zap.Error(err))
			return nil, err
		}
		mock.PostMock(context.Background(), &proto.PutMockReq{Path: path, Mock: &proto.Mock{
			Version: "api.keploy.io/v2",
			Kind:    "Mock",
			Name:    kctx.TestID,
			Spec: &proto.Mock_SpecSchema{
				Type:     "Http-Client",
				Metadata: map[string]string{"foo": "bar"},
				Objects:  []*proto.Mock_Object{},
				Req: &proto.Mock_Request{
					Method:     r.Method,
					ProtoMajor: int64(r.ProtoMajor),
					ProtoMinor: int64(r.ProtoMinor),
					URL:        r.URL.String(),
					Headers:    mock.GetProtoMap(r.Header),
					Body:       string(reqBody),
				},
				Res: &proto.Mock_Response{
					StatusCode: int64(statusCode),
					Headers:    mock.GetProtoMap(respHeader),
					Body:       string(respBody),
				},
			},
		}})
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}

	return resp, err

}
