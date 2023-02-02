package ksql

import (
	"context"
	"fmt"
	"reflect"

	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

type sqlOutput struct {
	Table *proto.Table
	Count int
	Err   []string
}

func CaptureSqlMocks(kctx *internal.Context, log *zap.Logger, meta map[string]string, specType string, output sqlOutput, outputs ...interface{}) {
	// append outputs of depemdency call in mock array
	var (
		obj = []*proto.Mock_Object{}
	)
	sqlMock := &proto.Mock{
		Version: string(models.V1Beta2),
		Name:    kctx.TestID,
		Kind:    string(models.SQL),
		Spec: &proto.Mock_SpecSchema{
			Metadata: meta,
			Objects:  obj,
			Err:      output.Err,
			Type:     specType,
			Table:    output.Table,
			Int:      int64(output.Count),
		},
	}
	if internal.GetGrpcClient() != nil && kctx.FileExport && internal.MockId.Unique(kctx.TestID) {
		recorded := internal.PutMock(context.Background(), internal.MockPath, sqlMock)
		if recorded {
			fmt.Println("🟠 Captured the mocked outputs for Http dependency call with meta: ", meta)
		}
		return
	}
	kctx.Mock = append(kctx.Mock, sqlMock)

	// append outputs of depemdency call in dep array
	res := make([][]byte, len(outputs))
	for indx, t := range outputs {
		err := keploy.Encode(t, res, indx)
		if err != nil {
			log.Error("dependency capture failed: failed to encode object", zap.String("type", reflect.TypeOf(t).String()), zap.String("test id", kctx.TestID), zap.Error(err))
			return
		}
	}
	kctx.Deps = append(kctx.Deps, models.Dependency{
		Name: meta["name"],
		Type: models.DependencyType(meta["type"]),
		Data: res,
		Meta: meta,
	})
}

func MockSqlFromYaml(kctx *internal.Context, meta map[string]string) (sqlOutput, bool) {
	var (
		res = sqlOutput{}
	)
	if len(kctx.Mock) > 0 && kctx.Mock[0].Kind == string(models.SQL) {
		mocks := kctx.Mock
		if len(mocks) > 0 {
			for i, j := range mocks {
				//
				if meta["operation"] == j.Spec.Metadata["operation"] &&
					meta["type"] == j.Spec.Metadata["type"] &&
					meta["name"] == j.Spec.Metadata["name"] {
					res.Table = mocks[i].Spec.Table
					res.Count = int(mocks[i].Spec.Int)
					res.Err = mocks[i].Spec.Err
					if kctx.FileExport {
						fmt.Println("🤡 Returned the mocked outputs for SQL dependency call with meta: ", meta)
					}
					mocks = append(mocks[:i], mocks[i+1:]...)
					kctx.Mock = mocks
					break
				}
			}
		}
		return res, true
	}
	return res, false
}

func cloneMap(meta map[string]string) map[string]string {
	// copy a map
	res := make(map[string]string)
	for k, v := range meta {
		res[k] = v
	}
	return res
}
