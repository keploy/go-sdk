package kfasthttp

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"net/http"

	//"github.com/fasthttp/router"
	"github.com/keploy/go-sdk/keploy"
	//routing "github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
	// "google.golang.org/genproto/googleapis/cloud/aiplatform/v1/schema/predict/params"
)

func captureResp(c *fasthttp.RequestCtx, next fasthttp.RequestHandler) models.HttpResp {
	resBody := new(bytes.Buffer)
	w := c.Response.BodyWriter()
	mw := io.MultiWriter(w, resBody)
	c.Response.WriteTo(mw)
	writer := &bodyDumpResponseWriterFast{
		Writer:   mw,
		Response: c.Response,
	}

	header := http.Header{}
	c.Response.Header.VisitAll(func(key, value []byte) {
		k, v := string(key), string(value)
		header[k] = []string{v}

	})
	next(c)
	return models.HttpResp{
		StatusCode: writer.Response.StatusCode(),
		Header:     header,
		Body:       resBody.String(),
	}

}

func setContextValFast(c *fasthttp.RequestCtx, val interface{}) {
	c.SetUserValue(string(keploy.KCTX), val)

}
func FastHttpMiddlware(k *keploy.Keploy) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	if k == nil || keploy.GetMode() == keploy.MODE_OFF {
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
			return next
		}
	}
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return fasthttp.RequestHandler(func(c *fasthttp.RequestCtx) {

			id := string(c.Request.Header.Peek("KEPLOY_TEST_ID"))
			if id != "" {
				setContextValFast(c, &keploy.Context{
					Mode:   "test",
					TestID: id,
					Deps:   k.GetDependencies(id),
				})
				resp := captureResp(c, next)
				k.PutResp(id, resp)
				return

			}
			setContextValFast(c, &keploy.Context{
				Mode: "capture",
			})
			var reqBody []byte
			var err error
			z := bytes.NewReader(c.PostBody())
			if z != nil {
				reqBody, err = ioutil.ReadAll(z)
				if err != nil {
					k.Log.Error("Unable to read request body", zap.Error(err))
					return
				}
			}
			r := &http.Request{}
			fasthttpadaptor.ConvertRequest(c, r, true) //converting fasthttp request to http
			resp := captureResp(c, next)
			params := pathParams(c)

			ctx := context.TODO()
			c.VisitUserValues(func(key []byte, val interface{}) {
				if string(key) == string(keploy.KCTX) {
					ctx = context.WithValue(ctx, keploy.KCTX, val)
					return
				}
				ctx = context.WithValue(ctx, string(key), val)

			})

			r = r.WithContext(ctx)

			keploy.CaptureTestcase(k, r, reqBody, resp, params)
			return
		})
	}
}
func pathParams(c *fasthttp.RequestCtx) map[string]string {
	var result map[string]string = make(map[string]string)
	c.URI().QueryArgs().VisitAll(func(key, value []byte) {
		k, v := string(key), string(value)
		result[k] = v
	})
	return result

}

type bodyDumpResponseWriterFast struct {
	io.Writer
	fasthttp.Response
}

func (ctx *bodyDumpResponseWriterFast) SetStatusCode(statusCode int) {
	ctx.Response.SetStatusCode(statusCode)
}
