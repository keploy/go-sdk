package kwebgo

import (
	"context"
	"net/http"
	"github.com/bnkamalesh/webgo/v4"
	"github.com/keploy/go-sdk/keploy"
)

// WebgoMiddlewareV4 adds keploy instrumentation for WebGo V4 router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
func WebgoMiddlewareV4(k *keploy.Keploy) func(http.ResponseWriter, *http.Request, http.HandlerFunc) {
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		keploy.Middleware(k, &webgoV4{
			req:    r,
			writer: w,
			next:   next,
		})
	}
}

type webgoV4 struct {
	writer http.ResponseWriter
	req    *http.Request
	next   http.HandlerFunc
}

func (w *webgoV4) GetRequest() *http.Request {
	return w.req
}

func (w *webgoV4) GetResponseWriter() http.ResponseWriter {
	return w.writer
}

func (w *webgoV4) SetRequest(r *http.Request) {
	w.req = r
}

func (w *webgoV4) SetResponseWriter(writer http.ResponseWriter) {
	w.writer = writer
}

func (w *webgoV4) Context() context.Context {
	return w.req.Context()
}

func (w *webgoV4) Next() error {
	w.next(w.writer, w.req)
	return nil
}

func (w *webgoV4) GetURLParams() map[string]string {
	return webgo.Context(w.req).Params()
}
