package mock

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"

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

func StartRecordingMocks(ctx context.Context, path string, mode string, name string) bool {
	resp, err := grpcClient.StartMocking(ctx, &proto.StartMockReq{
		Path: path,
		Mode: mode,
	})
	if err != nil {
		logger.Error(fmt.Sprint("Failed to make StartMocking grpc call to keploy server", name, " mock"), zap.Error(err))
		return false
	}
	return resp.Exists
}

func PostHttpMock(ctx context.Context, path string, mock *proto.Mock) bool {
	if !utf8.ValidString(mock.Spec.Req.Body) {
		logger.Info("request body is not valid UTF-8; will be captured as a base64 encoded string")
		mock.Spec.Req.Body = base64.StdEncoding.EncodeToString([]byte(mock.Spec.Req.Body))
	}

	if !utf8.ValidString(mock.Spec.Res.Body) {
		logger.Info("response body is not valid UTF-8; will be captured as a base64 encoded string")
		mock.Spec.Res.Body = base64.StdEncoding.EncodeToString([]byte(mock.Spec.Res.Body))
	}

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
		mocks := resp.Mocks
		for _, mock := range mocks {
			// TODO(keploy): investigate a better way of identifying if the body is base64 encoded
			decodedB64Req, err := base64.StdEncoding.DecodeString(mock.Spec.Req.Body)
			if err == nil {
				mock.Spec.Req.Body = string(decodedB64Req)
			}

			// TODO(keploy): investigate a better way of identifying if the body is base64 encoded
			decodedB64Resp, err := base64.StdEncoding.DecodeString(mock.Spec.Res.Body)
			if err == nil {
				mock.Spec.Res.Body = string(decodedB64Resp)
			}
		}
		return mocks, err
	}
	return nil, errors.New("returned nil as array mocks from keploy server")
}
