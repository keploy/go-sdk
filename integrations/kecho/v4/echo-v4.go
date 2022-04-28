package kecho

import (
	"github.com/keploy/go-sdk/keploy"
	"github.com/labstack/echo/v4"
	"go.keploy.io/server/pkg/models"
)

// EchoMiddlewareV4 adds keploy instrumentation for Echo V4 router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
func EchoMiddlewareV4(k *keploy.Keploy) func(echo.HandlerFunc) echo.HandlerFunc {
	if nil == k || keploy.GetMode() == keploy.MODE_OFF {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			writer, req, resBody, reqBody, err := keploy.ProcessRequest(c.Response().Writer, c.Request(), k)
			if err != nil {
				return
			}
			c.Response().Writer = writer
			c.SetRequest(req)

			// Store the response
			if err := next(c); err != nil {
				c.Error(err)
			}
			resp := models.HttpResp{
				//Status
				StatusCode: writer.Status,
				Header:     c.Response().Writer.Header(),
				Body:       resBody.String(),
			}

			id := c.Request().Header.Get("KEPLOY_TEST_ID")
			if id != "" {
				// id is only present during simulation
				// run it similar to how testcases would run
				k.PutResp(id, resp)
				return
			}
			params := pathParamsEcho(c)
			keploy.CaptureTestcase(k, c.Request(), reqBody, resp, params)
			return
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
