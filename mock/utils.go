package mock

import (
	"context"
	"os"
	"path/filepath"

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
		_, err := os.Create(filepath.Join(path, "mock.yaml"))
		if err != nil {
			logger.Error("failed to create a yaml file", zap.Error(err))
		}
	}
}

func PostMock(ctx context.Context, req *proto.PutMockReq) {
	c := proto.NewRegressionServiceClient(grpcClient)

	_, err := c.PutMock(ctx, req)
	if err != nil {
		logger.Error("failed to call the putMock method", zap.Error(err))
	}
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
				Objects:  []models.Object{},
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
