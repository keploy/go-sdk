package kchi

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/go-chi/chi"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
	// "google.golang.org/genproto/googleapis/cloud/aiplatform/v1/schema/predict/params"
)

func ChiV5(k *keploy.Keploy, w *chi.Mux) {
	if keploy.GetMode() == keploy.MODE_OFF {
		return
	}
	w.Use(mw(k))
}

func captureRespChi(w http.ResponseWriter, r *http.Request, next http.Handler) models.HttpResp {
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

func mw(k *keploy.Keploy) func(http.Handler) http.Handler{
	if k == nil {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func (w http.ResponseWriter, r *http.Request)  {
				k.Log.Warn("keploy is nil.")
				next.ServeHTTP(w, r)
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func (w http.ResponseWriter, r *http.Request)  {
			id := r.Header.Get("KEPLOY_TEST_ID")
			if id != "" {
				// id is only present during simulation
				// run it similar to how testcases would run
				ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
					Mode:   "test",
					TestID: id,
					Deps:   k.Deps[id],
				})

				r = r.WithContext(ctx)
				resp := captureRespChi(w, r, next)
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

			resp := captureRespChi(w, r, next)
			// params := webgo.Context(r).Params()
			// params := chi.RouteContext(r.Context()).URLParams
			params := urlParamsChi(chi.RouteContext(r.Context()), k)
			keploy.CaptureTestcase(k, r, reqBody, resp, params)
		})
	}
}

func urlParamsChi(c *chi.Context, k *keploy.Keploy) map[string]string{
	params := c.URLParams
	paramsMap := make(map[string]string)
	for i,j := range params.Keys{
		val := params.Values[i]
		if len(val)>0 && val[0] == '/'{
			val = val[1:]
		}
		paramsMap[j] = val
	}
	return paramsMap
}
