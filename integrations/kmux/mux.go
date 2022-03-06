package kmux

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

// Mux adds keploy instrumentation for Mux router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
//
// w is the mux router instance
func Mux(k *keploy.Keploy, w *mux.Router) {
	if keploy.GetMode() == keploy.MODE_OFF {
		return
	}
	w.Use(mw(k))
}

func captureRespMux(w http.ResponseWriter, r *http.Request, next http.Handler) models.HttpResp {
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(w, resBody)
	writer := &keploy.BodyDumpResponseWriter{
		Writer:         mw,
		ResponseWriter: w,
		Status:         http.StatusOK,
	}
	w = writer

	next.ServeHTTP(w, r)
	return models.HttpResp{
		//Status

		StatusCode: writer.Status,
		Header:     w.Header(),
		Body:       resBody.String(),
	}
}

func mw(k *keploy.Keploy) func( http.Handler) http.Handler {
	if k == nil {
		return func(next http.Handler) http.Handler{
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request)  {
				next.ServeHTTP(w, r)
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				resp := captureRespMux(w, r, next)
				k.PutResp(id, resp)
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

			resp := captureRespMux(w, r, next)
			// params := webgo.Context(r).Params()
			params := mux.Vars(r)
			keploy.CaptureTestcase(k, r, reqBody, resp, params)

		})
	}
}