package integrations

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	// "fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/keploy/go-sdk/keploy"
	"go.uber.org/zap"
)

// GinV1 method should be used for integrarting Gin router version 1. It should be called just before 
// routing for paths. This method adds middlewares for API tesing according to environment 
// variable "KEPLOY_SDK_MODE".
//
// app parameter is the Keploy App instance created by keploy.NewApp method. If app is nil then, 
// logic for capture or test middleware won't be added.
//
// r parameter is the Gin version 1 router of your API.
func GinV1(app *keploy.App, r *gin.Engine){
	mode := os.Getenv("KEPLOY_SDK_MODE")
	switch mode {
	case "test":
		r.Use(testMWGin(app))
		go app.Test()
	case "off":
		// dont run the SDK
	case "capture":
		r.Use(captureMWGin(app))
	}
}

func testMWGin(app *keploy.App) gin.HandlerFunc {
	if app==nil{
		return func (c *gin.Context)  {
			c.Next()
		}
	}
	return func(c *gin.Context){
		id := c.Request.Header.Get("KEPLOY_TEST_ID")
		if id == "" {
			c.Next()
			return
		}
		tc := app.Get(id)
		if tc == nil {
			c.Next()
			return
		}
		setContextValGin(c, &keploy.Context{
			Mode:   "test",
			TestID: id,
			Deps:   tc.Deps,
		})
		resp := captureRespGin(c)
		app.Resp[id] = resp
	}
}

func captureRespGin(c *gin.Context) keploy.HttpResp {
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(c.Writer, resBody)
	writer := &bodyDumpResponseWriterGin{
		Writer: mw,
		ResponseWriter: c.Writer, 
	}
	c.Writer = writer

	c.Next()
	return keploy.HttpResp{
		//Status
		StatusCode: c.Writer.Status(),
		Header:     c.Writer.Header(),
		Body:       resBody.String(),
	}
}

// from here https://stackoverflow.com/questions/67267065/how-to-propagate-context-values-from-gin-middleware-to-gqlgen-resolvers
func setContextValGin(c *gin.Context,  val interface{}){
	ctx := context.WithValue(c.Request.Context(), keploy.KCTX, val)
	c.Request = c.Request.WithContext(ctx)
}

func captureMWGin(app *keploy.App) gin.HandlerFunc {
	if app==nil{
		return func (c *gin.Context)  {
			c.Next()
		}
	}
	return func(c *gin.Context){
		fmt.Println("gin middleware")
		setContextValGin(c, &keploy.Context{Mode: "capture",})
		id := c.Request.Header.Get("KEPLOY_TEST_ID")
		if id != "" {
			// id is only present during simulation
			// run it similar to how testcases would run
			setContextValGin(c, &keploy.Context{
				Mode:   "test",
				TestID: id,
				Deps:   app.Deps[id],
			})
			resp := captureRespGin(c)
			app.Resp[id] = resp
			return
		}

		resp := captureRespGin(c)
		params := urlParamsGin(c, app)
		keploy.CaptureTestcase(app, c.Request, resp, params)
	}
}

func urlParamsGin(c *gin.Context, app *keploy.App) map[string]string{
	gp := c.Params
	data,err := json.Marshal(gp)
	if err!=nil{
		app.Log.Error("", zap.Error(err))
	}
	var gi interface{}
	err = json.Unmarshal(data, &gi)
	if err!=nil{
		app.Log.Error("", zap.Error(err))
	}
	var params map[string]string = make(map[string]string)

	for _,k := range gi.([]interface{}){
		j := k.(map[string]interface{})
		key := j["Key"].(string)
		val := j["Value"].(string)
		if val[0]!='/'{
			params[key] = val
		} else {
			params[key] = val[1:]
		}
	}
	return params
}

type bodyDumpResponseWriterGin struct {
	io.Writer
	gin.ResponseWriter
}

func (w *bodyDumpResponseWriterGin) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
}

func (w *bodyDumpResponseWriterGin) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (w *bodyDumpResponseWriterGin) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *bodyDumpResponseWriterGin) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}
