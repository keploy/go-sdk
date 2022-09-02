package mock

import (
	"context"
	"encoding/base64"

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

func PostHttpMock(ctx context.Context, path string, mock *proto.Mock) bool {
	c := proto.NewRegressionServiceClient(grpcClient)

	_, err := c.PutMock(ctx, &proto.PutMockReq{Path: path, Mock: mock})

	if err != nil {
		logger.Error("failed to call the putMock method", zap.Error(err))
		return false
	}
	return true
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

func GetAllMocks(ctx context.Context, req *proto.GetMockReq) ([]*proto.Mock, error) {
	c := proto.NewRegressionServiceClient(grpcClient)

	resp, err := c.GetMocks(ctx, req)
	return resp.Mocks, err
}
