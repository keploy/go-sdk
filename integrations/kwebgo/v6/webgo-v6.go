package kwebgo

import (
	"context"
	"net/http"
	"github.com/bnkamalesh/webgo/v6"
	"github.com/keploy/go-sdk/keploy"
)

// WebgoMiddlewareV6 adds keploy instrumentation for WebGo V6 router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
func WebgoMiddlewareV6(k *keploy.Keploy) func(http.ResponseWriter, *http.Request, http.HandlerFunc) {
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		keploy.Middleware(k, &webgoV6{
			req:    r,
			writer: w,
			next:   next,
		})
	}
}

type webgoV6 struct {
	writer http.ResponseWriter
	req    *http.Request
	next   http.HandlerFunc
}

func (w *webgoV6) GetRequest() *http.Request {
	return w.req
}

func (w *webgoV6) GetResponseWriter() http.ResponseWriter {
	return w.writer
}

func (w *webgoV6) SetRequest(r *http.Request) {
	w.req = r
}

func (w *webgoV6) SetResponseWriter(writer http.ResponseWriter) {
	w.writer = writer
}

func (w *webgoV6) Context() context.Context {
	return w.req.Context()
}

func (w *webgoV6) Next() error {
	w.next(w.writer, w.req)
	return nil
}

func (w *webgoV6) GetURLParams() map[string]string {
	return webgo.Context(w.req).Params()
}
