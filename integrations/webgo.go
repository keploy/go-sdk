package integrations

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"

	// "log"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/bnkamalesh/webgo/v6"
	// "github.com/bnkamalesh/webgo/v6/middleware/accesslog"
	// "github.com/bnkamalesh/webgo/v6/middleware/cors"
	"github.com/keploy/go-agent/keploy"
)

func WebGoStart(app *keploy.App, w *webgo.Router, host, port string){
	mode := os.Getenv("KEPLOY_SDK_MODE")
	fmt.Println("------------ In WebGoStart ------------")
	switch mode {
	case "test":
		// w.Use(NewMiddlewareContextValueWebGo)
		w.Use(testMWWebGo(app))
		go app.Test(host, port)
	case "off":
		// dont run the SDK
	default:
		// w.Use(NewMiddlewareContextValueWebGo)
		w.Use(captureMWWebGo(app))
	}
	w.Start()
}

func testMWWebGo(app *keploy.App) func( http.ResponseWriter,  *http.Request,  http.HandlerFunc){
	if nil == app {
		return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
			next(w, r)
		}
	}
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc){
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
		next(w, r)
	}
}

func captureMWWebGo(app *keploy.App) func( http.ResponseWriter,  *http.Request,  http.HandlerFunc){
	if nil == app {
		return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
			next(w, r)
		}
	}
	fmt.Println("---------In Capture middleware--------")
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc){
		
		ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
				Mode:   "capture",
		})
		
		fmt.Println("--------",r.Context().Value(keploy.KCTX),"---------")
		r = r.WithContext(ctx)
		fmt.Println("--------",r.Context().Value(keploy.KCTX),"---------")
		// tmp := r.Context().Value(keploy.KCTX)
		// if tmp.Mode!=nil{
		// 	fmt.Println(tmp.Mode)
		// }

		// Request
		var reqBody []byte
		var  err error
		if r.Body != nil { // Read
			reqBody,err = ioutil.ReadAll(r.Body)
			if err != nil {
				// TODO right way to log errors
				return
			}
		}
		r.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset

		// Response
		resBody := new(bytes.Buffer)
		mw := io.MultiWriter(w, resBody)
		writer := &bodyDumpResponseWriterWebgo{Writer: mw, ResponseWriter: w}
		w = writer

		next(w, r)

		d := r.Context().Value(keploy.KCTX)
		if d == nil {
			app.Log.Error("failed to get keploy context")
			return
		}
		deps := d.(*keploy.Context)
		fmt.Println("-----------",r.Context().Value(keploy.KCTX),"----------")

		u := &url.URL{
			Scheme:   r.URL.Scheme,
			//User:     url.UserPassword("me", "pass"),
			Host:     r.URL.Host,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
		}
		app.Capture(keploy.TestCaseReq{
				Captured: time.Now().Unix(),
				AppID:    app.Name,
				HttpReq:  keploy.HttpReq{
					Method: keploy.Method(r.Method),
					ProtoMajor: r.ProtoMajor,
					ProtoMinor: r.ProtoMinor,
					URL:        u.String(),
					Header:     r.Header,
					Body:       string(reqBody),
				},
				HttpResp: keploy.HttpResp{
					//Status
					// StatusCode:   w.Status,
					Header:       w.Header(),
					Body:         resBody.String(),
				},
				Deps: deps.Deps,
			})

	}
}

type bodyDumpResponseWriterWebgo struct {
	io.Writer
	http.ResponseWriter
}

func (w *bodyDumpResponseWriterWebgo) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
}

func (w *bodyDumpResponseWriterWebgo) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (w *bodyDumpResponseWriterWebgo) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *bodyDumpResponseWriterWebgo) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}


// func NewMiddlewareContextValueWebGo(w http.ResponseWriter, r *http.Request, fn http.HandlerFunc)  {
// 	// return func(ctx echo.Context) error {
// 	// 	return fn(contextValue{ctx})
// 	// }
// 	newContext := contextWebGoValue{r.Context()}
// 	r = r.WithContext(newContext)
// }

// // from here https://stackoverflow.com/questions/69326129/does-set-method-of-echo-context-saves-the-value-to-the-underlying-context-cont

// type contextWebGoValue struct {
// 	context.Context
// }

// // Get retrieves data from the context.
// func (ctx *contextWebGoValue) Get(key string) interface{} {
// 	// get old context value
// 	// val := ctx.Context.Get(key)
// 	// if val != nil {
// 	// 	return val
// 	// }
// 	return ctx.Value(key)
// }

// // Set saves data in the context.
// func (ctx *contextWebGoValue) Set(key string, val interface{}) {
// 	// ctx.SetRequest(ctx.Request().WithContext(
// 		ctx.Context = context.WithValue(ctx, key, val)
// 	// ))
// }