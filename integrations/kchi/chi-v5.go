package kchi

import (
	"context"
	"github.com/go-chi/chi"
	"github.com/keploy/go-sdk/keploy"
	"net/http"
)

func ChiMiddlewareV5(k *keploy.Keploy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			keploy.Middleware(k, &chiV5{
				writer: w,
				req:    r,
				next:   next,
				k:      k,
			})
		})
	}
}

func urlParamsChi(c *chi.Context, k *keploy.Keploy) map[string]string {
	params := c.URLParams
	paramsMap := make(map[string]string)
	for i, j := range params.Keys {
		val := params.Values[i]
		if len(val) > 0 && val[0] == '/' {
			val = val[1:]
		}
		paramsMap[j] = val
	}
	return paramsMap
}

type chiV5 struct {
	writer http.ResponseWriter
	req    *http.Request
	next   http.Handler
	k      *keploy.Keploy
}

func (w *chiV5) GetRequest() *http.Request {
	return w.req
}

func (w *chiV5) GetResponseWriter() http.ResponseWriter {
	return w.writer
}

func (w *chiV5) SetRequest(r *http.Request) {
	w.req = r
}

func (w *chiV5) SetResponseWriter(writer http.ResponseWriter) {
	w.writer = writer
}

func (w *chiV5) Context() context.Context {
	return w.req.Context()
}

func (w *chiV5) Next() error {
	w.next.ServeHTTP(w.writer, w.req)
	return nil
}

func (w *chiV5) GetURLParams() map[string]string {
	return urlParamsChi(chi.RouteContext(w.req.Context()), w.k)
}
