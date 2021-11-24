package integrations

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/keploy/go-agent/keploy"
	"github.com/labstack/echo/v4"
)

func Start(app *keploy.App, e *echo.Echo, host, port string)  {
	mode := os.Getenv("KEPLOY_SDK_MODE")
	switch mode {
	case "test":
		e.Use(NewMiddlewareContextValue)
		e.Use(testMW(app))
		go app.Test(host, port)
	case "off":
		// dont run the SDK
	default:
		e.Use(NewMiddlewareContextValue)
		e.Use(captureMW(app))
	}
	e.Logger.Fatal(e.Start(host + ":" + port))
}

func testMW(app *keploy.App) func(echo.HandlerFunc) echo.HandlerFunc {
	if nil == app {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			id := c.Request().Header.Get("KEPLOY_TEST_ID")
			if id == "" {
				return next(c)
			}
			tc := app.Get(id)
			if tc == nil {
				return next(c)
			}
			c.Set(fmt.Sprintf("%v", keploy.KCTX), &keploy.Context{
				Mode:   "test",
				TestID: id,
				Deps:   tc.Deps,
			})
			return next(c)
		}
	}
}


func captureMW(app *keploy.App) func(echo.HandlerFunc) echo.HandlerFunc {
	if nil == app {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			c.Set(fmt.Sprintf("%v", keploy.KCTX), &keploy.Context{
				Mode:   "capture",
			})
			// Request
			var reqBody []byte
			if c.Request().Body != nil { // Read
				reqBody, err = ioutil.ReadAll(c.Request().Body)
				if err != nil {
					// TODO right way to log errors
					return
				}
			}
			c.Request().Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset

			// Response
			resBody := new(bytes.Buffer)
			mw := io.MultiWriter(c.Response().Writer, resBody)
			writer := &bodyDumpResponseWriter{Writer: mw, ResponseWriter: c.Response().Writer}
			c.Response().Writer = writer

			if err = next(c); err != nil {
				c.Error(err)
			}

			d := c.Request().Context().Value(keploy.KCTX)
			if d == nil {
				app.Log.Error("failed to get keploy context")
				return
			}
			deps := d.(*keploy.Context)

			u := &url.URL{
				Scheme:   c.Scheme(),
				//User:     url.UserPassword("me", "pass"),
				Host:     c.Request().Host,
				Path:     c.Request().URL.Path,
				RawQuery: c.Request().URL.RawQuery,
			}
			
			app.Capture(keploy.TestCaseReq{
				Captured: time.Now().Unix(),
				AppID:    app.Name,
				HttpReq:  keploy.HttpReq{
					Method: keploy.Method(c.Request().Method),
					ProtoMajor: c.Request().ProtoMajor,
					ProtoMinor: c.Request().ProtoMinor,
					URL:        u.String(),
					Header:     c.Request().Header,
					Body:       string(reqBody),
				},
				HttpResp: keploy.HttpResp{
					//Status
					StatusCode:   c.Response().Status,
					Header:       c.Response().Header(),
					Body:         resBody.String(),
				},
				Deps: deps.Deps,
			})


			//fmt.Println("This is the request", c.Request().Proto, u.String(), c.Request().Header, "body - " + string(reqBody), c.Request().Cookies())
			//fmt.Println("This is the response", resBody.String(), c.Response().Header())

			return
		}
	}

}

type bodyDumpResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w *bodyDumpResponseWriter) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
}

func (w *bodyDumpResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (w *bodyDumpResponseWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *bodyDumpResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func NewMiddlewareContextValue(fn echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		return fn(contextValue{ctx})
	}
}

// from here https://stackoverflow.com/questions/69326129/does-set-method-of-echo-context-saves-the-value-to-the-underlying-context-cont

type contextValue struct {
	echo.Context
}

// Get retrieves data from the context.
func (ctx contextValue) Get(key string) interface{} {
	// get old context value
	val := ctx.Context.Get(key)
	if val != nil {
		return val
	}
	type keyType string
	return ctx.Request().Context().Value(keyType(key))
}

// Set saves data in the context.
func (ctx contextValue) Set(key string, val interface{}) {
	
	type keyType string
	ctx.SetRequest(ctx.Request().WithContext(context.WithValue(ctx.Request().Context(), keyType(key), val)))
}