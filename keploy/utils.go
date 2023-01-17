package keploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/keploy/go-sdk/internal/keploy"
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/http/regression"
	"go.keploy.io/server/pkg/models"

	"go.uber.org/zap"
)

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

// ProcessDep is a generic method to encode and decode the outputs of external dependecies.
// If request is on keploy.MODE_TEST mode, it returns (true, decoded outputs of stored binaries in keploy context).
// Else in keploy.MODE_RECORD mode, it encodes the outputs of external dependencies and stores in keploy context. Returns (false, nil).
func ProcessDep(ctx context.Context, log *zap.Logger, meta map[string]string, outputs ...interface{}) (bool, []interface{}) {
	kctx, err := keploy.GetState(ctx)
	if err != nil {
		log.Error("dependency mocking failed: failed to get Keploy state from context", zap.Error(err))
		return false, nil
	}
	// capture the object
	switch kctx.Mode {
	case keploy.MODE_TEST:
		if len(kctx.Mock) == 0 {
			if kctx.Deps == nil || len(kctx.Deps) == 0 {
				log.Error("dependency mocking failed: New unrecorded dependency call. Please record again and delete current tcs with", zap.String("test id", kctx.TestID))
				return false, nil
			}
			if len(kctx.Deps[0].Data) != len(outputs) {
				log.Error("dependency mocking failed: Async or Unrecorded dependency call. Please record again and delete current tcs with", zap.String("test id", kctx.TestID))
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
			kctx.Deps = kctx.Deps[1:]
			return true, res
		}

		if kctx.Mock == nil || len(kctx.Mock) == 0 {
			log.Error("mocking failed: New unrecorded dependency call. Please record again and delete current tcs with", zap.String("test id", kctx.TestID))
			return false, nil
		}
		if len(kctx.Mock[0].Spec.Objects) != len(outputs) {
			log.Error("mocking failed: Async or Unrecorded dependency call. Please record again and delete current tcs with", zap.String("test id", kctx.TestID))
			return false, nil
		}
		var res []interface{}
		for i, t := range outputs {
			bin := kctx.Mock[0].Spec.Objects[i].Data
			if err != nil {
				log.Error("failed to decode base64 data from yaml file into byte array", zap.Error(err))
				return false, nil
			}
			r, err := Decode(bin, t)
			if err != nil {
				typ := "nil"
				if r != nil {
					typ = reflect.TypeOf(r).String()
				}
				log.Error("dependency mocking failed: failed to decode object", zap.String("type", typ), zap.String("test id", kctx.TestID))
				return false, nil
			}
			res = append(res, r)
		}

		if kctx.FileExport {
			fmt.Println("🤡 Returned the mocked outputs for Generic dependency call with meta: ", meta)
		}
		kctx.Mock = kctx.Mock[1:]
		return true, res

	case keploy.MODE_RECORD:
		res := make([][]byte, len(outputs))
		for i, t := range outputs {
			err = Encode(t, res, i)
			if err != nil {
				log.Error("dependency capture failed: failed to encode object", zap.String("type", reflect.TypeOf(t).String()), zap.String("test id", kctx.TestID), zap.Error(err))
				return false, nil
			}
		}
		protoObjs := []*proto.Mock_Object{}
		for i, j := range res {
			protoObjs = append(protoObjs, &proto.Mock_Object{
				Type: reflect.TypeOf(outputs[i]).String(),
				Data: j,
			})
		}
		if keploy.GetGrpcClient() != nil && kctx.FileExport && keploy.MockId.Unique(kctx.TestID) {
			recorded := keploy.PutMock(ctx, keploy.MockPath, &proto.Mock{
				Version: string(models.V1Beta2),
				Kind:    string(models.GENERIC),
				Name:    kctx.TestID,
				Spec: &proto.Mock_SpecSchema{
					Metadata: meta,
					Objects:  protoObjs,
				},
			})
			if recorded {
				fmt.Println("🟠 Captured the mocked outputs for Generic dependency call with meta: ", meta)
			}
			return false, nil

		}

		kctx.Deps = append(kctx.Deps, models.Dependency{
			Name: meta["name"],
			Type: models.DependencyType(meta["type"]),
			Data: res,
			Meta: meta,
		})
		kctx.Mock = append(kctx.Mock, &proto.Mock{
			Version: string(models.V1Beta2),
			Kind:    string(models.GENERIC),
			Name:    "",
			Spec: &proto.Mock_SpecSchema{
				Metadata: meta,
				Objects:  protoObjs,
			},
		})
	}
	return false, nil
}

func CaptureTestcase(k *Keploy, r *http.Request, reqBody []byte, resp models.HttpResp, params map[string]string, grpcCtx context.Context, grpcReq string, grpcMethod string, grpcResp string, reqType string) {
	var d interface{}
	switch reqType {
	case "http":
		d = r.Context().Value(keploy.KCTX)

	case "grpc":
		d = grpcCtx.Value(keploy.KCTX)
	}
	if d == nil {
		k.Log.Error("failed to get keploy context")
		return
	}
	deps := d.(*keploy.Context)

	// u := &url.URL{
	// 	Scheme: r.URL.Scheme,
	// 	//User:     url.UserPassword("me", "pass"),
	// 	Host:     r.URL.Host,
	// 	Path:     r.URL.Path,
	// 	RawQuery: r.URL.RawQuery,
	// }
	switch reqType {
	case "grpc":
		k.Capture(regression.TestCaseReq{
			Captured:     time.Now().Unix(),
			AppID:        k.cfg.App.Name,
			GrpcReq:      grpcReq,
			GrpcResp:     grpcResp,
			GrpcMethod:   grpcMethod,
			Deps:         deps.Deps,
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
			Mocks:        deps.Mock,
			Type:         "grpc",
		})
	case "http":
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
			HttpResp:     resp,
			Deps:         deps.Deps,
			TestCasePath: k.cfg.App.TestPath,
			MockPath:     k.cfg.App.MockPath,
			Mocks:        deps.Mock,
			Type:         "http",
		})
	}
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

func ProcessRequest(rw http.ResponseWriter, r *http.Request, k *Keploy) (*BodyDumpResponseWriter, *http.Request, *bytes.Buffer, []byte, error) {
	// Response body
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(rw, resBody)
	writer := &BodyDumpResponseWriter{
		Writer:         mw,
		ResponseWriter: rw,
		Status:         http.StatusOK,
	}
	// rw = writer

	// Request context
	id := r.Header.Get("KEPLOY_TEST_ID")
	if id != "" {
		// id is only present during simulation
		// run it similar to how testcases would run
		ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
			Mode:   keploy.MODE_TEST,
			TestID: id,
			Deps:   k.GetDependencies(id),
			Mock:   k.GetMocks(id),
			Mu:     &sync.Mutex{},
		})

		r = r.WithContext(ctx)
		return writer, r, resBody, nil, nil
	}
	ctx := context.WithValue(r.Context(), keploy.KCTX, &keploy.Context{
		Mode: keploy.MODE_RECORD,
		Mu:   &sync.Mutex{},
	})
	r = r.WithContext(ctx)

	// Request Body
	var reqBody []byte
	var err error
	if r.Body != nil { // Read
		reqBody, err = ioutil.ReadAll(r.Body)
		if err != nil {
			// TODO right way to log errors
			k.Log.Error("Unable to read request body", zap.Error(err))
			return writer, r, resBody, nil, err
		}
	}
	r.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset

	return writer, r, resBody, reqBody, nil
}
