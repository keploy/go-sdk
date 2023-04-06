package khttp

import (
	"context"
	"github.com/gorilla/mux"
	"github.com/keploy/go-sdk/keploy"
	"net/http"
)

func KMiddleware(next http.Handler, k *keploy.Keploy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keploy.Middleware(k, &middleware{
			writer: w,
			req:    r,
			next:   next,
		})
	})
}

type middleware struct {
	writer http.ResponseWriter
	req    *http.Request
	next   http.Handler
	k      *keploy.Keploy
}

func (m *middleware) GetRequest() *http.Request {
	return m.req
}

func (m *middleware) GetResponseWriter() http.ResponseWriter {
	return m.writer
}

func (m *middleware) SetRequest(r *http.Request) {
	m.req = r
}

func (m *middleware) SetResponseWriter(writer http.ResponseWriter) {
	m.writer = writer
}

func (m *middleware) Context() context.Context {
	return m.req.Context()
}

func (m *middleware) Next() error {
	m.next.ServeHTTP(m.writer, m.req)
	return nil
}

func (m *middleware) GetURLParams() map[string]string {
	return getUrlParams(m.req)
}

func getUrlParams(r *http.Request) map[string]string {
	vars := mux.Vars(r)
	params := make(map[string]string)

	for key, value := range vars {
		params[key] = value
	}

	return params
}
