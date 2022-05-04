package kmux

import (
	"context"
	"github.com/gorilla/mux"
	"github.com/keploy/go-sdk/keploy"
	"net/http"
)

// MuxMiddleware adds keploy instrumentation for Mux router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
func MuxMiddleware(k *keploy.Keploy) func(http.Handler) http.Handler {

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			keploy.Middleware(k, &gmux{
				writer: w,
				req:    r,
				next:   next,
			})
		})
	}
}

type gmux struct {
	writer http.ResponseWriter
	req    *http.Request
	next   http.Handler
}

func (m *gmux) GetRequest() *http.Request {
	return m.req
}

func (m *gmux) GetResponseWriter() http.ResponseWriter {
	return m.writer
}

func (m *gmux) SetRequest(r *http.Request) {
	m.req = r
}

func (m *gmux) SetResponseWriter(writer http.ResponseWriter) {
	m.writer = writer
}

func (m *gmux) Context() context.Context {
	return m.req.Context()
}

func (m *gmux) Next() error {
	m.next.ServeHTTP(m.writer, m.req)
	return nil
}

func (m *gmux) GetURLParams() map[string]string {
	return mux.Vars(m.req)
}
