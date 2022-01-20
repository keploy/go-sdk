package integrations

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"github.com/keploy/go-sdk/keploy"
	"github.com/labstack/echo/v4"
)

// EchoV4 method should be used for integrarting echo router version 4. It should be called just before 
// starting the routes handling. This method adds middlewares for API tesing according to environment 
// variable "KEPLOY_SDK_MODE".
//
// app parameter is the Keploy App instance created by keploy.NewApp method. If app is nil then, 
// logic for capture or test middleware won't be added.
//
// w parameter is the Echo version 4 router of your API.
func EchoV4(app *keploy.App, e *echo.Echo) {
	mode := os.Getenv("KEPLOY_SDK_MODE")
	switch mode {
	case "test":
		e.Use(testMWEchoV4(app))
		go app.Test()
	case "off":
		// dont run the SDK
	case "capture":
		e.Use(captureMWEchoV4(app))
	}
}

// Similar to gin.Context. Visit https://stackoverflow.com/questions/67267065/how-to-propagate-context-values-from-gin-middleware-to-gqlgen-resolvers
func setContextValEchoV4(c echo.Context,  val interface{}){
	ctx := context.WithValue(c.Request().Context() , keploy.KCTX, val)
	c.SetRequest( c.Request().WithContext(ctx) ) 
}

func testMWEchoV4(app *keploy.App) func(echo.HandlerFunc) echo.HandlerFunc {
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
			// c.Set(string(keploy.KCTX), &keploy.Context{
			// 	Mode:   "test",
			// 	TestID: id,
			// 	Deps:   tc.Deps,
			// })
			setContextValEchoV4(c, &keploy.Context{
				Mode:   "test",
				TestID: id,
				Deps:   tc.Deps,
			})
			resp := captureResp(c, next)
			app.Resp[id] = resp
			return
		}
	}
}

func captureMWEchoV4(app *keploy.App) func(echo.HandlerFunc) echo.HandlerFunc {
	if nil == app {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			// c.Set(string(keploy.KCTX), &keploy.Context{
			// 	Mode: "capture",
			// })
			setContextValEchoV4(c, &keploy.Context{
				Mode: "capture",
			})
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
					Deps:   app.Deps[id],
				})
				resp := captureResp(c, next)
				app.Resp[id] = resp
				return
			}

			resp := captureResp(c, next)
			params := pathParamsEcho(c)
			keploy.CaptureTestcase(app, c.Request(), resp, params)
			return
		}
	}

}

func captureResp(c echo.Context, next echo.HandlerFunc) keploy.HttpResp {
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(c.Response().Writer, resBody)
	writer := &keploy.BodyDumpResponseWriter{
		Writer: mw,
		ResponseWriter: c.Response().Writer, 
		Status: http.StatusOK,
	}
	c.Response().Writer = writer

	if err := next(c); err != nil {
		c.Error(err)
	}
	return keploy.HttpResp{
		//Status
		StatusCode: writer.Status,
		Header:     c.Response().Header(),
		Body:       resBody.String(),
	}
}

func pathParamsEcho(c echo.Context) map[string]string{
	var result map[string]string = make(map[string]string)
	paramNames := c.ParamNames()
	paramValues:= c.ParamValues()
	for i:= 0;i<len(paramNames);i++{
		fmt.Printf("paramName : %v, paramValue : %v\n", paramNames[i], paramValues[i])
		result[paramNames[i]] = paramValues[i]
	}
	return result
}
