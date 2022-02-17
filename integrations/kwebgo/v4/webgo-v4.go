package kwebgo

import (
	"bytes"
	"context"
	"go.keploy.io/server/pkg/models"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/bnkamalesh/webgo/v4"
	"github.com/keploy/go-sdk/keploy"
	"go.uber.org/zap"
)

// WebGoV4 adds keploy instrumentation for WebGo V4 router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
//
// w is the webgo v4 router instance
func WebGoV4(k *keploy.Keploy, w *webgo.Router) {
	if keploy.GetMode() == keploy.MODE_OFF {
		return
	}
	w.Use(mw(k))
}

func captureRespWebGo(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) models.HttpResp {
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(w, resBody)
	writer := &keploy.BodyDumpResponseWriter{
		Writer:         mw,
		ResponseWriter: w,
		Status:         http.StatusOK,
	}
	w = writer

	next(w, r)
	return models.HttpResp{
		//Status

		StatusCode: writer.Status,
		Header:     w.Header(),
		Body:       resBody.String(),
	}
}

func mw(k *keploy.Keploy) func(http.ResponseWriter, *http.Request, http.HandlerFunc) {
	if k == nil {
		return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
			k.Log.Warn("keploy is nil.")
			next(w, r)
		}
	}
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		id := r.Header.Get("KEPLOY_TEST_ID")
		if id != "" {
			// id is only present during simulation
			// run it similar to how testcases would run
			ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
				Mode:   "test",
				TestID: id,
				Deps:   k.GetDependencies(id),
			})

			r = r.WithContext(ctx)
			resp := captureRespWebGo(w, r, next)
			k.Resp[id] = resp
			return
		}
		ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
			Mode: "capture",
		})

		r = r.WithContext(ctx)

		// Request
		var reqBody []byte
		var err error
		if r.Body != nil { // Read
			reqBody, err = ioutil.ReadAll(r.Body)
			if err != nil {
				// TODO right way to log errors
				k.Log.Error("Unable to read request body", zap.Error(err))
				return
			}
		}
		r.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset

		resp := captureRespWebGo(w, r, next)
		params := webgo.Context(r).Params()
		keploy.CaptureTestcase(k, r, reqBody, resp, params)

	}
}
