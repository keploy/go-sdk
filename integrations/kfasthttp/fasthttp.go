package kfasthttp

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"sync"

	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"

	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

func captureResp(c *fasthttp.RequestCtx, next fasthttp.RequestHandler) models.HttpResp {

	header := http.Header{}
	c.Response.Header.VisitAll(func(key, value []byte) {
		k, v := string(key), string(value)
		header[k] = []string{v}

	})
	next(c)
	var resBody []byte = c.Response.Body()

	return models.HttpResp{
		StatusCode: c.Response.StatusCode(),
		Header:     header,
		Body:       string(resBody),
	}

}

func setContextValFast(c *fasthttp.RequestCtx, val interface{}) {
	c.SetUserValue(internal.KCTX, val)

}

func FastHttpMiddleware(k *keploy.Keploy) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	if k == nil || internal.GetMode() == internal.MODE_OFF {
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
			return next
		}
	}
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return fasthttp.RequestHandler(func(c *fasthttp.RequestCtx) {

			id := string(c.Request.Header.Peek("KEPLOY_TEST_ID"))
			if id == "" && internal.GetMode() == internal.MODE_TEST {
				next(c)
				return
			}

			if id != "" {
				setContextValFast(c, &internal.Context{
					Mode:   internal.MODE_TEST,
					TestID: id,
					Deps:   k.GetDependencies(id),
					Mock:   k.GetMocks(id),
					Mu:     &sync.Mutex{},
				})
				resp := captureResp(c, next)
				response := k.GetResp(id)
				response.Resp = resp
				k.PutResp(id, response)

				// Continue further execution after client call in simulate function
				response.L.Unlock()
				// k.PutResp(id, keploy.HttpResp{Resp: resp})
				return

			}
			setContextValFast(c, &internal.Context{
				Mode: internal.MODE_RECORD,
				Mu:   &sync.Mutex{},
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

			// capture request before calling next
			r = r.WithContext(c)
			params := pathParams(c)
			req := models.HttpReq{
				Method:     models.Method(r.Method),
				ProtoMajor: r.ProtoMajor,
				ProtoMinor: r.ProtoMinor,
				URL:        r.URL.String(),
				URLParams:  keploy.UrlParams(r, params),
				Header:     r.Header,
				Body:       string(reqBody),
			}

			resp := captureResp(c, next)

			keploy.CaptureHttpTC(k, r.Context(), req, keploy.UrlPath(r.URL.Path, params), resp, params)
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
