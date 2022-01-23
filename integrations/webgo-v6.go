package integrations

import (
	"bytes"
	"context"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/bnkamalesh/webgo/v6"
	"github.com/keploy/go-sdk/keploy"
	"go.uber.org/zap"
)

// WebGoV6 method should be used for integrarting webgo router version 6. It should be called just before
// starting the router. This method adds middlewares for API tesing according to environment
// variable "KEPLOY_SDK_MODE".
//
// app parameter is the Keploy App instance created by keploy.NewApp method. If app is nil then,
// logic for capture or test middleware won't be added.
//
// w parameter is the WebGo version 6 router of your API.
func WebGoV6(app *keploy.App, w *webgo.Router) {
	mode := os.Getenv("KEPLOY_SDK_MODE")
	switch mode {
	case "test":
		w.Use(testMWWebGo(app))
		go app.Test()
	case "off":
		// dont run the SDK
	case "capture":
		w.Use(captureMWWebGoV6(app))
	}
}

func captureMWWebGoV6(app *keploy.App) func(http.ResponseWriter, *http.Request, http.HandlerFunc) {
	if nil == app {
		return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
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

		// Request
		var reqBody []byte
		var err error
		if r.Body != nil { // Read
			reqBody, err = ioutil.ReadAll(r.Body)
			if err != nil {
				// TODO right way to log errors
				app.Log.Error("Unable to read request body", zap.Error( err))
				return
			}
		}
		r.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset
		
		resp := captureRespWebGo(w, r, next)
		params := webgo.Context(r).Params()
		keploy.CaptureTestcase(app, r, reqBody, resp, params)
	}
}
