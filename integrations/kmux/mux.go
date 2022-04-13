package kmux

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
)

// MuxMiddleware adds keploy instrumentation for Mux router.
// It should be ideally used after other instrumentation libraries like APMs.
//
// k is the Keploy instance
func MuxMiddleware(k *keploy.Keploy) func(http.Handler) http.Handler {
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
				// id is only present during simulation
				// run it similar to how testcases would run
				k.PutResp(id, resp)
				return
			}

			params := mux.Vars(r)
			keploy.CaptureTestcase(k, r, reqBody, resp, params)

		})
	}
}
