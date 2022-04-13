package khttpclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/gob"
	"errors"
	"fmt"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
)

// ReadCloser is used so that gob could encode-decode http.Response.
type ReadCloser struct {
	*bytes.Reader
	Body io.ReadCloser
}

func (rc ReadCloser) Close() error {
	return nil
}

func (rc *ReadCloser) UnmarshalBinary(b []byte) error {

	// copy the byte array elements into copyByteArr. See https://www.reddit.com/r/golang/comments/tddjdd/gob_is_appending_gibberish_to_my_object/
	copyByteArr := make([]byte, len(b))
	copy(copyByteArr, b)
	rc.Reader = bytes.NewReader(copyByteArr)
	return nil
}

func (rc *ReadCloser) MarshalBinary() ([]byte, error) {
	if rc.Body != nil {
		b, err := ioutil.ReadAll(rc.Body)
		rc.Body.Close()
		rc.Reader = bytes.NewReader(b)
		return b, err
	}
	return nil, nil
}

type Interceptor struct {
	core http.RoundTripper
	log  *zap.Logger
	kctx *keploy.Context
}

// NewInterceptor constructs and returns the pointer to Interceptor. Interceptor is used
// to intercept every http client calls and store their responses into keploy context.
func NewInterceptor(core http.RoundTripper) *Interceptor {
	// Initialize a logger
	logger, _ := zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()

	// Register types to gob encoder
	gob.Register(ReadCloser{})
	gob.Register(elliptic.P256())
	gob.Register(ecdsa.PublicKey{})
	gob.Register(rsa.PublicKey{})
	return &Interceptor{
		core: core,
		log:  logger,
	}
}

// SetContext is used to store the keploy context from request context into the Interceptor
// kctx field.
func (i *Interceptor) SetContext(requestContext context.Context) {
	// ctx := context.TODO()
	if kctx, err := keploy.GetState(requestContext); err == nil {
		i.kctx = kctx
		i.log.Debug("http client keploy interceptor's context has been set to : ", zap.Any("keploy.Context ", i.kctx))
	}
}

// setRequestContext returns the context with keploy context as value. It is called only
// when kctx field of Interceptor is not null.
func (i *Interceptor) setRequestContext(ctx context.Context) context.Context {
	rctx := context.WithValue(ctx, keploy.KCTX, i.kctx)
	return rctx
}

// RoundTrip is the custom method which is called before making http client calls to
// capture or replay the outputs of external http service.
func (i Interceptor) RoundTrip(r *http.Request) (*http.Response, error) {
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

	// adds the keploy context stored in Interceptor's ctx field into the http client request context.
	if _, err := keploy.GetState(r.Context()); err != nil && i.kctx != nil {
		ctx := i.setRequestContext(r.Context())
		r = r.WithContext(ctx)
	}

	if keploy.GetModeFromContext(r.Context()) == keploy.MODE_OFF {
		return i.core.RoundTrip(r)
	}
	var (
		err       error
		kerr      *keploy.KError = &keploy.KError{}
		resp      *http.Response = &http.Response{}
		isRespNil bool           = false
	)
	kctx, er := keploy.GetState(r.Context())
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":       "http-client",
		"type":       string(models.HttpClient),
		"operation":  r.Method,
		"URL":        r.URL.String(),
		"Header":     fmt.Sprint(r.Header),
		"Body":       string(reqBody),
		"Proto":      r.Proto,
		"ProtoMajor": strconv.Itoa(r.ProtoMajor),
		"ProtoMinor": strconv.Itoa(r.ProtoMinor),
	}
	switch mode {
	case "test":
		//don't call i.core.RoundTrip method
	case "capture":
		resp, err = i.core.RoundTrip(r)
		if resp == nil {
			isRespNil = true
			resp = &http.Response{}
		}
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}

	resp.Body = &ReadCloser{Body: resp.Body}
	if resp.Request != nil {
		resp.Request.Body = &ReadCloser{Body: resp.Request.Body}
	}

	mock, res := keploy.ProcessDep(r.Context(), i.log, meta, resp, kerr)
	if mock {
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return resp, mockErr
	}
	if isRespNil {
		return nil, err
	}
	return resp, err

}
