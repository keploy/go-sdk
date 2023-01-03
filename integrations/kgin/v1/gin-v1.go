package kgin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/ioutil"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	internal "github.com/keploy/go-sdk/internal/keploy"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

// GinV1 adds keploy instrumentation for Gin V1 router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
//
// r is the gin v1 router instance
func GinV1(k *keploy.Keploy, r *gin.Engine) {
	if internal.GetMode() == internal.MODE_OFF {
		return
	}
	r.Use(mw(k))
}

func captureRespGin(c *gin.Context) models.HttpResp {
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(c.Writer, resBody)
	writer := &bodyDumpResponseWriterGin{
		Writer:         mw,
		ResponseWriter: c.Writer,
	}
	c.Writer = writer

	c.Next()
	return models.HttpResp{
		//Status
		StatusCode: c.Writer.Status(),
		Header:     c.Writer.Header(),
		Body:       resBody.String(),
	}
}

// from here https://stackoverflow.com/questions/67267065/how-to-propagate-context-values-from-gin-middleware-to-gqlgen-resolvers
func setContextValGin(c *gin.Context, val interface{}) {
	ctx := context.WithValue(c.Request.Context(), internal.KCTX, val)
	c.Request = c.Request.WithContext(ctx)
}

func mw(k *keploy.Keploy) gin.HandlerFunc {
	if k == nil {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	return func(c *gin.Context) {
		id := c.Request.Header.Get("KEPLOY_TEST_ID")
		if id != "" {
			// id is only present during simulation
			// run it similar to how testcases would run
			setContextValGin(c, &internal.Context{
				Mode:   internal.MODE_TEST,
				TestID: id,
				Deps:   k.GetDependencies(id),
				Mock:   k.GetMocks(id),
			})
			resp := captureRespGin(c)
			response := k.GetResp(id)
			response.Resp = resp
			k.PutResp(id, response)

			// Continue further execution after client call in simulate function
			response.L.Unlock()
			// k.PutResp(id, keploy.HttpResp{Resp: resp})
			return
		}

		setContextValGin(c, &internal.Context{Mode: internal.MODE_RECORD})

		// Request
		var reqBody []byte
		var err error
		if c.Request.Body != nil { // Read
			reqBody, err = ioutil.ReadAll(c.Request.Body)
			if err != nil {
				// TODO right way to log errors
				k.Log.Error("Unable to read request body", zap.Error(err))
				return
			}
		}
		c.Request.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset

		resp := captureRespGin(c)
		params := urlParamsGin(c, k)
		keploy.CaptureTestcase(k, c.Request, reqBody, resp, params)
	}
}

func urlParamsGin(c *gin.Context, k *keploy.Keploy) map[string]string {
	gp := c.Params
	data, err := json.Marshal(gp)
	if err != nil {
		k.Log.Error("", zap.Error(err))
	}
	var gi interface{}
	err = json.Unmarshal(data, &gi)
	if err != nil {
		k.Log.Error("", zap.Error(err))
	}
	var params = make(map[string]string)
	if gi == nil {
		return params
	}

	for _, k := range gi.([]interface{}) {
		j := k.(map[string]interface{})
		key := j["Key"].(string)
		val := j["Value"].(string)
		if len(val) > 0 && val[0] == '/' {
			params[key] = val[1:]
		} else {
			params[key] = val
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
