package keploy

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"

	"go.uber.org/zap"
)

const SDKMode = "KeploySDKMode"
const Deps = "KeployDeps"
const TestID = "KeployTestID"

// type keyType string

const KCTX = "KeployContext"

func GetMode() string {
	return os.Getenv("KEPLOY_SDK_MODE")
}

func compareHeaders(h1 http.Header, h2 http.Header) bool {
	return !(cmpHeader(h1, h2) && cmpHeader(h2, h1))

}

func cmpHeader(h1 http.Header, h2 http.Header) bool {
	for k, v := range h1 {
		val, ok := h2[k]
		if !ok {
			fmt.Println("header not present", k)
			return false
		}
		for i, e := range v {
			if val[i] != e {
				fmt.Println("value not same", k, v, val)
				return false
			}
		}
	}
	return true
}

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

func GetState(ctx context.Context) (*Context, error) {
	kctx := ctx.Value(KCTX)
	if kctx == nil {
		return nil, errors.New("failed to get Keploy context")
	}
	return kctx.(*Context), nil
}

func ProcessDep(ctx context.Context, log *zap.Logger, meta map[string]string, outputs ...interface{}) (bool, []interface{}) {
	kctx, err := GetState(ctx)
	if err != nil {
		log.Error("failed to get state from context", zap.Error(err))
		return false, nil
	}
	// capture the object
	switch kctx.Mode {
	case "test":
		if kctx.Deps == nil || len(kctx.Deps) == 0 {
			log.Error("incorrect number of dynamodb dependencies in keploy context", zap.String("test id", kctx.TestID))
			return false, nil
		}
		if len(kctx.Deps[0].Data) != len(outputs) {
			log.Error("incorrect number of dynamodb dependencies in keploy context", zap.String("test id", kctx.TestID))
			return false, nil
		}
		var res []interface{}
		for i, t := range outputs {
			r, err := Decode(kctx.Deps[0].Data[i], t)
			if err != nil {
				log.Error("failed to decode object", zap.String("type", reflect.TypeOf(r).String()), zap.String("test id", kctx.TestID))
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
				log.Error("failed to encode object", zap.String("type", reflect.TypeOf(t).String()), zap.String("test id", kctx.TestID), zap.Error(err))
				return false, nil
			}
		}
		//err = keploy.Encode(err1,res, 1)
		//if err != nil {
		//	c.log.Error("failed to encode ddb resp", zap.String("test id", id))
		//}
		kctx.Deps = append(kctx.Deps, Dependency{
			Name: "dynamodb",
			Type: NoSqlDB,
			Data: res,
			Meta: meta,
		})
	}
	return false, nil
}
