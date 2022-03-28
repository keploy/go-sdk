package kwebgo

import (
	"go.keploy.io/server/pkg/models"
	"net/http"

	"github.com/bnkamalesh/webgo/v4"
	"github.com/keploy/go-sdk/keploy"
)

// WebgoMiddlewareV4 adds keploy instrumentation for WebGo V4 router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
func WebgoMiddlewareV4(k *keploy.Keploy) func(http.ResponseWriter, *http.Request, http.HandlerFunc) {
	if k == nil || keploy.GetMode() == keploy.MODE_OFF {
		return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
			k.Log.Warn("keploy is nil.")
			next(w, r)
		}
	}
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		writer, r, resBody, reqBody, err := keploy.ProcessRequest(w, r, k)
		if err != nil {
			return
		}
		w = writer

		// Store the responses
		next(w, r)
		resp := models.HttpResp{
			//Status
			StatusCode: writer.Status,
			Header:     w.Header(),
			Body:       resBody.String(),
		}

		id := r.Header.Get("KEPLOY_TEST_ID")
		if id != "" {
			// id is only present during simulation
			// run it similar to how testcases would run
			k.PutResp(id, resp)
			return
		}
		params := webgo.Context(r).Params()
		keploy.CaptureTestcase(k, r, reqBody, resp, params)

	}
}
