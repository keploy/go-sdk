package kecho

import (
	"context"
	"github.com/keploy/go-sdk/keploy"
	"github.com/labstack/echo/v4"
	"net/http"
)

// EchoMiddlewareV4 adds keploy instrumentation for Echo V4 router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
func EchoMiddlewareV4(k *keploy.Keploy) func(echo.HandlerFunc) echo.HandlerFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			return keploy.Middleware(k, &echoV4{
				ctx:  c,
				next: next,
			})
		}
	}

}

func pathParamsEcho(c echo.Context) map[string]string {
	var result map[string]string = make(map[string]string)
	paramNames := c.ParamNames()
	paramValues := c.ParamValues()
	for i := 0; i < len(paramNames); i++ {
		result[paramNames[i]] = paramValues[i]
	}
	return result
}

type echoV4 struct {
	ctx  echo.Context
	next echo.HandlerFunc
}

func (m *echoV4) GetRequest() *http.Request {
	return m.ctx.Request()
}

func (m *echoV4) GetResponseWriter() http.ResponseWriter {
	return m.ctx.Response().Writer
}

func (m *echoV4) SetRequest(r *http.Request) {
	m.ctx.SetRequest(r)
}

func (m *echoV4) SetResponseWriter(writer http.ResponseWriter) {
	m.ctx.Response().Writer = writer
}

func (m *echoV4) Context() context.Context {
	return m.ctx.Request().Context()
}

func (m *echoV4) Next() error {
	err := m.next(m.ctx)
	return err
}

func (m *echoV4) GetURLParams() map[string]string {
	return pathParamsEcho(m.ctx)
}
