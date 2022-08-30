package mock

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"

	"github.com/keploy/go-sdk/keploy"
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

func GetProtoMap(m map[string][]string) map[string]*proto.StrArr {
	res := map[string]*proto.StrArr{}
	for k, v := range m {
		arr := &proto.StrArr{}
		arr.Value = append(arr.Value, v...)
		res[k] = arr
	}
	return res
}

func GetHttpHeader(m map[string]*proto.StrArr) map[string][]string {
	res := map[string][]string{}
	for k, v := range m {
		res[k] = v.Value
	}
	return res
}

func CreateMockFile(path string) {
	if _, err := os.Stat(filepath.Join(path, "mock.yaml")); err != nil {
		err := os.MkdirAll(filepath.Join(path), os.ModePerm)
		if err != nil {
			logger.Error("failed to create a mock dir", zap.Error(err))
		}
		_, err = os.Create(filepath.Join(path, "mock.yaml"))
		if err != nil {
			logger.Error("failed to create a yaml file", zap.Error(err))
		}
	}
}

func PostMock(ctx context.Context, path string, mock models.Mock) {
	c := proto.NewRegressionServiceClient(grpcClient)

	_, err := c.PutMock(ctx, &proto.PutMockReq{Path: path, Mock: &proto.Mock{
		Version: string(keploy.V1_BETA1),
		Kind:    string(keploy.KIND_MOCK),
		Name:    mock.Name,
		Spec: &proto.Mock_SpecSchema{
			Type:     mock.Spec.Type,
			Metadata: mock.Spec.Metadata,
			Objects:  toProtoObjects(mock.Spec.Objects),
			Req: &proto.Mock_Request{
				Method:     string(mock.Spec.Request.Method),
				ProtoMajor: int64(mock.Spec.Request.ProtoMajor),
				ProtoMinor: int64(mock.Spec.Request.ProtoMinor),
				URL:        mock.Spec.Request.URL,
				Headers:    GetProtoMap(mock.Spec.Request.Header),
				Body:       string(mock.Spec.Request.Body),
			},
			Res: &proto.Mock_Response{
				StatusCode: int64(mock.Spec.Response.StatusCode),
				Headers:    GetProtoMap(mock.Spec.Response.Header),
				Body:       string(mock.Spec.Response.Body),
			},
		},
	}})
	if err != nil {
		logger.Error("failed to call the putMock method", zap.Error(err))
	}
}

func toProtoObjects(objs []models.Object) []*proto.Mock_Object {
	res := []*proto.Mock_Object{}
	for _, j := range objs {
		bin, err := base64.StdEncoding.DecodeString(j.Data)
		if err != nil {
			logger.Error("failed to decode base64 data from yaml file into byte array", zap.Error(err))
			continue
		}
		res = append(res, &proto.Mock_Object{
			Type: j.Type,
			Data: bin,
		})
	}
	return res
}

func toModelObjects(objs []*proto.Mock_Object) []models.Object {
	res := []models.Object{}
	for _, j := range objs {
		res = append(res, models.Object{
			Type: j.Type,
			Data: base64.StdEncoding.EncodeToString(j.Data),
		})
	}
	return res
}

func GetAllMocks(ctx context.Context, req *proto.GetMockReq) ([]models.Mock, error) {
	c := proto.NewRegressionServiceClient(grpcClient)

	resp, err := c.GetMocks(ctx, req)
	mocks := []models.Mock{}
	if err != nil {
		return mocks, err
	}
	for _, j := range resp.Mocks {
		mocks = append(mocks, models.Mock{
			Version: j.Version,
			Kind:    j.Kind,
			Name:    j.Name,
			Spec: models.SpecSchema{
				Type:     j.Spec.Type,
				Metadata: j.Spec.Metadata,
				Objects:  toModelObjects(j.Spec.Objects),
				Request: models.HttpReq{
					Method:     models.Method(j.Spec.Req.Method),
					ProtoMajor: int(j.Spec.Req.ProtoMajor),
					ProtoMinor: int(j.Spec.Req.ProtoMinor),
					URL:        j.Spec.Req.URL,
					Header:     GetHttpHeader(j.Spec.Req.Headers),
					Body:       j.Spec.Req.Body,
				},
				Response: models.HttpResp{
					StatusCode: int(j.Spec.Res.StatusCode),
					Header:     GetHttpHeader(j.Spec.Res.Headers),
					Body:       j.Spec.Res.Body,
				},
			},
		})
	}
	return mocks, err
}
