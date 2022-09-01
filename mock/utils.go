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

func PostHttpMock(ctx context.Context, path string, mock models.Mock) {
	c := proto.NewRegressionServiceClient(grpcClient)

	_, err := c.PutMock(ctx, &proto.PutMockReq{Path: path, Mock: &proto.Mock{
		Version: string(models.V1_BETA1),
		Kind:    string(models.HTTP_EXPORT),
		Name:    mock.Name,
		Spec: &proto.Mock_SpecSchema{
			Type:     mock.Spec.Type,
			Metadata: mock.Spec.Metadata,
			// Objects:  toProtoObjects(mock.Spec.Objects),
			Objects: []*proto.Mock_Object{&proto.Mock_Object{
				Type: mock.Spec.Objects[0].Type,
				Data: []byte(mock.Spec.Objects[0].Data),
			}},
			Req: &proto.Mock_Request{
				Method:     string(mock.Spec.Request.Method),
				ProtoMajor: int64(mock.Spec.Request.ProtoMajor),
				ProtoMinor: int64(mock.Spec.Request.ProtoMinor),
				URL:        mock.Spec.Request.URL,
				Header:     GetProtoMap(mock.Spec.Request.Header),
				Body:       string(mock.Spec.Request.Body),
			},
			Res: &proto.HttpResp{
				StatusCode: int64(mock.Spec.Response.StatusCode),
				Header:     GetProtoMap(mock.Spec.Response.Header),
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
		mock := models.Mock{
			Version: j.Version,
			Kind:    j.Kind,
			Name:    j.Name,
			Spec: models.SpecSchema{
				Type:     j.Spec.Type,
				Metadata: j.Spec.Metadata,
				// Objects:  toModelObjects(j.Spec.Objects),
				Request: models.HttpReq{
					Method:     models.Method(j.Spec.Req.Method),
					ProtoMajor: int(j.Spec.Req.ProtoMajor),
					ProtoMinor: int(j.Spec.Req.ProtoMinor),
					URL:        j.Spec.Req.URL,
					Header:     GetHttpHeader(j.Spec.Req.Header),
					Body:       j.Spec.Req.Body,
				},
				Response: models.HttpResp{
					StatusCode: int(j.Spec.Res.StatusCode),
					Header:     GetHttpHeader(j.Spec.Res.Header),
					Body:       j.Spec.Res.Body,
				},
			},
		}

		switch mock.Kind {
		case string(models.HTTP_EXPORT):
			mock.Spec.Objects = []models.Object{models.Object{
				Type: j.Spec.Objects[0].Type,
				Data: string(j.Spec.Objects[0].Data),
			}}
		case string(models.GENERIC_EXPORT):
			mock.Spec.Objects = toModelObjects(j.Spec.Objects)
		default:
			logger.Error("Mock is not of a vaild kind.")
		}

		mocks = append(mocks, mock)
	}
	return mocks, err
}
