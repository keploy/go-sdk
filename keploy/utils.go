package keploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"go.keploy.io/server/http/regression"
	"go.keploy.io/server/pkg/models"

	"go.uber.org/zap"
)

type KctxType string

const KCTX KctxType = "KeployContext"

// Decode returns the decoded data by using gob decoder on bin parameter.
func Decode(bin []byte, obj interface{}) (interface{}, error) {
	if len(bin) == 0 {
		return nil, nil
	}

	dec := gob.NewDecoder(bytes.NewBuffer(bin))
	err := dec.Decode(obj)
	if err != nil {
		return nil, err
	}

	return obj, nil
}

// Encode takes obj parameter and encodes its contents into arr parameter. If obj have no
// exported field then, it returns an error.
func Encode(obj interface{}, arr [][]byte, pos int) error {
	if obj == nil {
		arr[pos] = nil
		return nil
	}
	var buf bytes.Buffer        // Stand-in for a network connection
	enc := gob.NewEncoder(&buf) // Will write to network.
	// Encode (send) some values.
	err := enc.Encode(obj)
	if err != nil {
		return err
	}
	arr[pos] = buf.Bytes()
	return nil
}

// GetState returns value of "KeployContext" key-value pair which is stored in the request context.
func GetState(ctx context.Context) (*Context, error) {
	kctx := ctx.Value(KCTX)
	if kctx == nil {
		return nil, errors.New("failed to get Keploy context")
	}
	return kctx.(*Context), nil
}

// ProcessDep is a generic method to encode and decode the outputs of external dependecies.
// If request is on "test" mode, it returns (true, decoded outputs of stored binaries in keploy context).
// Else in "capture" mode, it encodes the outputs of external dependencies and stores in keploy context. Returns (false, nil).
func ProcessDep(ctx context.Context, log *zap.Logger, meta map[string]string, outputs ...interface{}) (bool, []interface{}) {
	kctx, err := GetState(ctx)
	if err != nil {
		log.Error("dependency mocking failed: failed to get Keploy state from context", zap.Error(err))
		return false, nil
	}
	// capture the object
	switch kctx.Mode {
	case "test":
		if kctx.Deps == nil || len(kctx.Deps) == 0 {
			log.Error("dependency mocking failed: incorrect number of dependencies in keploy context", zap.String("test id", kctx.TestID))
			return false, nil
		}
		if len(kctx.Deps[0].Data) != len(outputs) {
			log.Error("dependency mocking failed: incorrect number of dependencies in keploy context", zap.String("test id", kctx.TestID))
			return false, nil
		}
		var res []interface{}
		for i, t := range outputs {
			r, err := Decode(kctx.Deps[0].Data[i], t)
			if err != nil {
				log.Error("dependency mocking failed: failed to decode object", zap.String("type", reflect.TypeOf(r).String()), zap.String("test id", kctx.TestID))
				return false, nil
			}
			res = append(res, r)
		}
		//res, err := keploy.Decode(deps.Deps[0][0], &dynamodb.QueryOutput{})
		//if err != nil {
		//	log.Error("failed to decode ddb resp", zap.String("test id", id))
		//	return nil
		//}
		//var err1h error
		//err1, err := keploy.Decode(deps.Deps[0][1], err1h)
		//if err != nil {
		//	log.Error("failed to decode ddb error object", zap.String("test id", id))
		//	return nil
		//}
		kctx.Deps = kctx.Deps[1:]
		return true, res

	case "capture":
		res := make([][]byte, len(outputs))
		for i, t := range outputs {
			err = Encode(t, res, i)
			if err != nil {
				log.Error("dependency capture failed: failed to encode object", zap.String("type", reflect.TypeOf(t).String()), zap.String("test id", kctx.TestID), zap.Error(err))
				return false, nil
			}
		}

		//err = keploy.Encode(err1,res, 1)
		//if err != nil {
		//	c.log.Error("failed to encode ddb resp", zap.String("test id", id))
		//}
		kctx.Deps = append(kctx.Deps, models.Dependency{
			Name: meta["name"],
			Type: models.DependencyType(meta["type"]),
			Data: res,
			Meta: meta,
		})
	}
	return false, nil
}

func CaptureTestcase(k *Keploy, r *http.Request, reqBody []byte, resp models.HttpResp, params map[string]string) {

	d := r.Context().Value(KCTX)
	if d == nil {
		k.Log.Error("failed to get keploy context")
		return
	}
	deps := d.(*Context)

	// u := &url.URL{
	// 	Scheme: r.URL.Scheme,
	// 	//User:     url.UserPassword("me", "pass"),
	// 	Host:     r.URL.Host,
	// 	Path:     r.URL.Path,
	// 	RawQuery: r.URL.RawQuery,
	// }
	k.Capture(regression.TestCaseReq{
		Captured: time.Now().Unix(),
		AppID:    k.cfg.App.Name,
		URI:      urlPath(r.URL.Path, params),
		HttpReq: models.HttpReq{
			Method:     models.Method(r.Method),
			ProtoMajor: r.ProtoMajor,
			ProtoMinor: r.ProtoMinor,
			URL:        r.URL.String(),
			URLParams:  urlParams(r, params),
			Header:     r.Header,
			Body:       string(reqBody),
		},
		HttpResp: resp,
		Deps:     deps.Deps,
	})

}

func urlParams(r *http.Request, params map[string]string) map[string]string {
	result := params
	qp := r.URL.Query()
	for i, j := range qp {
		var s string
		if _, ok := result[i]; ok {
			s = result[i]
		}
		for _, e := range j {
			if s != "" {
				s += ", " + e
			} else {
				s = e
			}
		}
		result[i] = s
	}
	return result
}

func urlPath(url string, params map[string]string) string {
	res := url
	for i, j := range params {
		res = strings.Replace(res, "/"+j+"/", "/:"+i+"/", -1)
		if strings.HasSuffix(res, "/"+j) {
			res = strings.TrimSuffix(res, "/"+j) + "/:" + i
		}
	}
	return res
}

type BodyDumpResponseWriter struct {
	io.Writer
	http.ResponseWriter
	Status int
}

func (w *BodyDumpResponseWriter) WriteHeader(code int) {
	w.Status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *BodyDumpResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (w *BodyDumpResponseWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *BodyDumpResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}
