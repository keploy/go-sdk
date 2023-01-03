package mock

import (
	"context"
	"errors"
	"fmt"

	proto "go.keploy.io/server/grpc/regression"
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

func StartRecordingMocks(ctx context.Context, path, mode, name string, overWrite bool) bool {
	resp, err := grpcClient.StartMocking(ctx, &proto.StartMockReq{
		Path:      path,
		Mode:      mode,
		OverWrite: overWrite,
		//Name: ,
	})
	if err != nil {
		logger.Error(fmt.Sprint("Failed to make StartMocking grpc call to keploy server", name, " mock"), zap.Error(err))
		return false
	}
	return resp.Exists
}

func PostHttpMock(ctx context.Context, path string, mock *proto.Mock) bool {

	_, err := grpcClient.PutMock(ctx, &proto.PutMockReq{Path: path, Mock: mock})

	if err != nil {
		logger.Error("Failed to call the putMock method", zap.Error(err))
		return false
	}
	return true
}

func GetAllMocks(ctx context.Context, req *proto.GetMockReq) ([]*proto.Mock, error) {

	resp, err := grpcClient.GetMocks(ctx, req)
	if resp != nil {
		return resp.Mocks, err
	}
	return nil, errors.New("returned nil as array mocks from keploy server")
}
