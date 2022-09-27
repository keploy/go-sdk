package khttpclient

import (
	"net/http"

	"github.com/keploy/go-sdk/keploy"
)

func toGobType(err error, resp *http.Response) (*keploy.KError, *http.Response) {
	kerr := &keploy.KError{}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}

	resp.Body = &ReadCloser{Body: resp.Body}
	if resp.Request != nil {
		resp.Request.Body = &ReadCloser{Body: resp.Request.Body}
	}
	return kerr, resp
}
