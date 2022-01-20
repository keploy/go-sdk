package integrations

import (
	"bytes"
	"context"
	"github.com/bnkamalesh/webgo/v4"
	"io"
	"net/http"
	"os"
	"github.com/keploy/go-sdk/keploy"
)

// WebGoV4 method should be used for integrarting webgo router version 4. It should be called just before 
// starting the router. This method adds middlewares for API tesing according to environment 
// variable "KEPLOY_SDK_MODE".
//
// app parameter is the Keploy App instance created by keploy.NewApp method. If app is nil then, 
// raise a warning to provide non-empty app instance.
//
// w parameter is the WebGo version 4 router of your API.
func WebGoV4(app *keploy.App, w *webgo.Router) {
	mode := os.Getenv("KEPLOY_SDK_MODE")
	switch mode {
	case "test":
		w.Use(testMWWebGo(app))
		go app.Test()
	case "off":
		// dont run the SDK
	case "capture":
		w.Use(captureMWWebGoV4(app))
	}
}

func testMWWebGo(app *keploy.App) func(http.ResponseWriter, *http.Request, http.HandlerFunc) {
	if nil == app {
		return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
			app.Log.Warn("keploy app is nil.")
			next(w, r)
		}
	}
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		id := r.Header.Get("KEPLOY_TEST_ID")
		if id == "" {
			next(w, r)
		}
		tc := app.Get(id)
		if tc == nil {
			next(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
			Mode:   "test",
			TestID: id,
			Deps:   tc.Deps,
		})
		r = r.WithContext(ctx)
		resp := captureRespWebGo(w, r, next)
		app.Resp[id] = resp

	}
}

func captureRespWebGo(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) keploy.HttpResp {
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(w, resBody)
	writer := &keploy.BodyDumpResponseWriter{
		Writer: mw, 
		ResponseWriter: w, 
		Status: http.StatusOK,
	}
	w = writer

	next(w, r)
	return keploy.HttpResp{
		//Status

		StatusCode: writer.Status,
		Header:     w.Header(),
		Body:       resBody.String(),
	}
}

func captureMWWebGoV4(app *keploy.App) func(http.ResponseWriter, *http.Request, http.HandlerFunc) {
	if nil == app {
		return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
			app.Log.Warn("keploy app is nil.")
			next(w, r)
		}
	}
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {

		ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
			Mode: "capture",
		})

		r = r.WithContext(ctx)

		id := r.Header.Get("KEPLOY_TEST_ID")
		if id != "" {
			// id is only present during simulation
			// run it similar to how testcases would run
			ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
				Mode:   "test",
				TestID: id,
				Deps:   app.Deps[id],
			})

			r = r.WithContext(ctx)
			resp := captureRespWebGo(w, r, next)
			app.Resp[id] = resp
			return
		}
		resp := captureRespWebGo(w, r, next)
		params := webgo.Context(r).Params()
		keploy.CaptureTestcase(app, r, resp, params)

	}
}
