package kgrpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// serverInterceptor is grpc middleware which is used to get the grpc method
// which is being called and the request data.
func serverInterceptor(k *keploy.Keploy) func(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if internal.GetMode() == internal.MODE_OFF {
			return handler(ctx, req)
		}
		// requestMeta is used to retrieve the testcase id from the context
		requestMeta, metaExist := metadata.FromIncomingContext(ctx)
		if !metaExist {
			fmt.Println("\nUnable to Start Keploy !!")
			return handler(ctx, req)
		}
		errStr := "nil"
		id := ""
		requestId := len(requestMeta["tid"])
		if requestId != 0 {
			id = requestMeta["tid"][0]
		}
		if id != "" {
			ctx = context.WithValue(ctx, internal.KCTX, &internal.Context{
				Mode:   keploy.MODE_TEST,
				TestID: id,
				Deps:   k.GetDependencies(id),
				Mock:   k.GetMocks(id),
			})
			c, err := handler(ctx, req)
			if err != nil {
				errStr = err.Error()
			}
			respByte, err1 := json.Marshal(c)
			if err1 != nil {
				k.Log.Error("failed to unmarshal grpc response body", zap.Error(err1))
				return c, err
			}
			resp := string(respByte)
			res := k.GetRespGrpc(id)
			res.Resp = models.GrpcResp{Body: resp, Err: errStr}
			k.PutRespGrpc(id, res)
			res.L.Unlock()
			return c, err
		}
		ctx = context.WithValue(ctx, internal.KCTX, &internal.Context{
			Mode: keploy.MODE_RECORD,
		})
		reqByte, err1 := json.Marshal(req)
		if err1 != nil {
			k.Log.Error("failed to marshal grpc request body and tcs is not captured", zap.Error(err1))
		}
		requestJson := string(reqByte)
		infoByte, err1 := json.Marshal(info)
		if err1 != nil {
			k.Log.Error("", zap.Error(err1))
		}
		serverInfo := grpc.UnaryServerInfo{}
		err1 = json.Unmarshal(infoByte, &serverInfo)
		if err1 != nil {
			k.Log.Error("", zap.Error(err1))
		}
		// serverInfo.FullMethod contains the method name with "/" character
		// Here, we remove this redundant character.
		fullMethod := strings.Split(info.FullMethod, "/")
		method := ""
		for i := 1; i < len(fullMethod); i++ {
			if i == len(fullMethod)-1 {
				method = method + fullMethod[i]
				break
			}
			method = method + fullMethod[i] + "."
		}
		c, err := handler(ctx, req)
		if err != nil {
			errStr = err.Error()
		}
		respByte, err1 := json.Marshal(c)
		if err1 != nil {
			k.Log.Error("failed to marshal grpc response", zap.Error(err1))
			return c, err1
		}
		resp := string(respByte)
		keploy.CaptureGrpcTC(k, ctx, models.GrpcReq{Body: requestJson, Method: method}, models.GrpcResp{Body: resp, Err: errStr})
		return c, err
	}
}

func UnaryInterceptor(k *keploy.Keploy) grpc.ServerOption {
	return grpc.UnaryInterceptor(serverInterceptor(k))
}
