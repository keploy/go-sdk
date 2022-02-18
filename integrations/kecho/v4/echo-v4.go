package kecho

import (
	"bytes"
	"context"
	"fmt"
	"go.keploy.io/server/pkg/models"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/keploy/go-sdk/keploy"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// EchoV4 adds keploy instrumentation for Echo V4 router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
//
// e is the echo v4 router instance
func EchoV4(k *keploy.Keploy, e *echo.Echo) {
	if keploy.GetMode() == keploy.MODE_OFF {
		return
	}
	e.Use(mw(k))
}

// Similar to gin.Context. Visit https://stackoverflow.com/questions/67267065/how-to-propagate-context-values-from-gin-middleware-to-gqlgen-resolvers
func setContextValEchoV4(c echo.Context, val interface{}) {
	ctx := context.WithValue(c.Request().Context(), keploy.KCTX, val)
	c.SetRequest(c.Request().WithContext(ctx))
}

func mw(k *keploy.Keploy) func(echo.HandlerFunc) echo.HandlerFunc {
	if nil == k {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			// c.Set(string(keploy.KCTX), &keploy.Context{
			// 	Mode: "capture",
			// })

			id := c.Request().Header.Get("KEPLOY_TEST_ID")
			if id != "" {
				// id is only present during simulation
				// run it similar to how testcases would run
				// c.Set(string(keploy.KCTX), &keploy.Context{
				// 	Mode:   "test",
				// 	TestID: id,
				// 	Deps:   app.Deps[id],
				// })
				setContextValEchoV4(c, &keploy.Context{
					Mode:   "test",
					TestID: id,
					Deps:   k.GetDependencies(id),
				})
				resp := captureResp(c, next)
				k.PutResp(id, resp)
				return
			}
			setContextValEchoV4(c, &keploy.Context{
				Mode: "capture",
			})

			// Request
			var reqBody []byte
			if c.Request().Body != nil { // Read
				reqBody, err = ioutil.ReadAll(c.Request().Body)
				if err != nil {
					// TODO right way to log errors
					k.Log.Error("Unable to read request body", zap.Error(err))
					return
				}
			}
			c.Request().Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset

			resp := captureResp(c, next)
			params := pathParamsEcho(c)
			keploy.CaptureTestcase(k, c.Request(), reqBody, resp, params)
			return
		}
	}

}

func captureResp(c echo.Context, next echo.HandlerFunc) models.HttpResp {
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(c.Response().Writer, resBody)
	writer := &keploy.BodyDumpResponseWriter{
		Writer:         mw,
		ResponseWriter: c.Response().Writer,
		Status:         http.StatusOK,
	}
	c.Response().Writer = writer

	if err := next(c); err != nil {
		c.Error(err)
	}
	return models.HttpResp{
		//Status
		StatusCode: writer.Status,
		Header:     c.Response().Header(),
		Body:       resBody.String(),
	}
}

func pathParamsEcho(c echo.Context) map[string]string {
	var result map[string]string = make(map[string]string)
	paramNames := c.ParamNames()
	paramValues := c.ParamValues()
	for i := 0; i < len(paramNames); i++ {
		fmt.Printf("paramName : %v, paramValue : %v\n", paramNames[i], paramValues[i])
		result[paramNames[i]] = paramValues[i]
	}
	return result
}
