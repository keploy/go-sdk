package khttpclient

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"github.com/araddon/dateparse"
	"github.com/go-test/deep"
	"github.com/keploy/go-sdk/keploy"
	"github.com/keploy/go-sdk/mock"
	internal "github.com/keploy/go-sdk/pkg/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
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

func MockRespFromYaml(kctx *internal.Context, logger *zap.Logger, req *http.Request, reqBody []byte, meta map[string]string) (*http.Response, error, bool) {
	var (
		resp                = &http.Response{}
		err           error = nil
		matchPriority       = map[int]int{} // stores degree of match for all http mocks. (<indx_of_mocks>-<degree_of_match> as key value pair)
	)
	// thread safe
	if kctx.Mu != nil {
		kctx.Mu.Lock()
		defer kctx.Mu.Unlock()
	}
	mocks := kctx.Mock
	if len(mocks) > 0 {
		// determine the degree of match for all mocked http request in mock
		// array with current http call
		for i, j := range mocks {
			reqUrl, er := url.Parse(j.Spec.Req.URL)
			if er != nil {
				continue
			}
			if j.Kind == string(models.HTTP) &&
				req.Method == j.Spec.Req.Method &&
				req.URL.RequestURI() == reqUrl.RequestURI() {
				matchPriority[i] = 0
				// headers macthes ignoring date fields
				if compareHttpHeaders(req.Header, mock.GetHttpHeader(j.Spec.Req.Header)) {
					matchPriority[i] = matchPriority[i] + 3
				}
				if req.ProtoMajor == int(j.Spec.Req.ProtoMajor) && req.ProtoMinor == int(j.Spec.Req.ProtoMinor) {
					matchPriority[i] = matchPriority[i] + 2
				}
				if deep.Equal(reqBody, j.Spec.Req.BodyData) == nil {
					matchPriority[i] = matchPriority[i] + 1
				}
			}
		}
		indx := -1 // indx of mock which matches mostly with the http request

		// determines the highest match for http request
		for k, v := range matchPriority {
			if indx == -1 && v > 0 {
				indx = k
			} else if indx != -1 && matchPriority[indx] < v {
				indx = k
			}
		}
		// return the closest match for http request
		if indx != -1 {
			if matchPriority[indx] < 6 {
				// 	fmt.Println("🎉 returned http response of exact http request match")
				// } else {
				fmt.Println(" ≅ returned http response of approximate http request match")
			}
			// assign the mocked outputs for http call
			errStr := string(mocks[indx].Spec.Objects[0].Data)
			if errStr != "" {
				err = errors.New(string(errStr))
			}
			bin := []byte{}
			if mocks[indx].Spec.Res.BodyData != nil {
				bin = mocks[indx].Spec.Res.BodyData
			} else {
				bin = []byte(mocks[indx].Spec.Res.Body)
			}
			resp.Body = ioutil.NopCloser(bytes.NewBuffer(bin))
			resp.Header = mock.GetHttpHeader(mocks[indx].Spec.Res.Header)
			resp.StatusCode = int(mocks[indx].Spec.Res.StatusCode)
			if kctx.FileExport {
				fmt.Println("🤡 Returned the mocked outputs for Http dependency call with meta: ", meta)
			}

			// remove the closest matched mock from mocks array
			mocks = append(mocks[:indx], mocks[indx+1:]...)
			kctx.Mock = mocks
			return resp, err, true
		}
		logger.Error("Failed to match http request with recorded http calls", zap.String("request method", req.Method), zap.String("request path", req.URL.RequestURI()))
		return nil, nil, true
	}
	return resp, err, false
}

// comparator checks for time field in header's value
func comparator(v []string) bool {
	for _, j := range v {
		if IsTime(j) {
			return true
		}
	}

	// maybe we need to concatenate the values
	return IsTime(strings.Join(v, ", "))
}

func compareHttpHeaders(a, b map[string][]string) bool {
	for k, v := range a {
		// ignores the date/time header fields
		if comparator(v) {
			continue
		}
		// the header field is not present in b
		if _, ok := b[k]; !ok {
			return false
		}
		// value do not matches of headers
		if val := b[k]; deep.Equal(v, val) != nil {
			return false
		}
	}
	// checks for fields which are present in "b" header and not in "a"
	for k, v := range b {
		// ignores the date/time header fields
		if comparator(v) {
			continue
		}
		// the header field is not present
		if _, ok := a[k]; !ok {
			return false
		}
	}
	return true
}

// IsTime checks whether the given string is of time format
func IsTime(stringDate string) bool {
	s := strings.TrimSpace(stringDate)
	_, err := dateparse.ParseAny(s)
	return err == nil
}
