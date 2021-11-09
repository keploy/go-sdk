package integrations

import (
	"bufio"
	"bytes"
	"github.com/keploy/go-agent/keploy"
	"github.com/labstack/echo/v4"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

func Start(app *keploy.App, e *echo.Echo, host, port string)  {
	mode := os.Getenv("KEPLOY_SDK_MODE")
	switch mode {
	case "test":
		go app.Test(host, port)
	case "off":
		// dont run the SDK
		return
	default:
		e.Use(captureMW(app))
	}
	e.Logger.Fatal(e.Start(host + ":" + port))
}

func captureMW(app *keploy.App) func(echo.HandlerFunc) echo.HandlerFunc {
	if nil == app {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			// Request
			reqBody := []byte{}
			if c.Request().Body != nil { // Read
				reqBody, _ = ioutil.ReadAll(c.Request().Body)
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