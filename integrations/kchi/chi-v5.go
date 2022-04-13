package kchi

import (
	"net/http"

	"github.com/go-chi/chi"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
)

func ChiMiddlewareV5(k *keploy.Keploy) func(http.Handler) http.Handler {
	if k == nil || keploy.GetMode() == keploy.MODE_OFF {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r)
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writer, r, resBody, reqBody, err := keploy.ProcessRequest(w, r, k)
			if err != nil {
				return
			}
			w = writer

			// Store the responses
			next.ServeHTTP(w, r)
			resp := models.HttpResp{
				//Status
				StatusCode: writer.Status,
				Header:     w.Header(),
				Body:       resBody.String(),
			}

			id := r.Header.Get("KEPLOY_TEST_ID")
			if id != "" {
				k.PutResp(id, resp)
				return
			}

			params := urlParamsChi(chi.RouteContext(r.Context()), k)
			keploy.CaptureTestcase(k, r, reqBody, resp, params)
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
