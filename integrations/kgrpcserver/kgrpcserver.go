package kgrpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
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
		if os.Getenv("KEPLOY_MODE") == "off" {
			return handler(ctx, req)
		}
		// requestMeta is used to retrieve the testcase id from the context
		requestMeta, metaExist := metadata.FromIncomingContext(ctx)
		if !metaExist {
			fmt.Println("\nUnable to Start Keploy !!")
			return handler(ctx, req)
		}
		id := ""
		requestId := len(requestMeta["tid"])
		if requestId != 0 {
			id = requestMeta["tid"][0]
		}
		if id != "" {
			ctx = context.WithValue(ctx, keploy.KCTX, &keploy.Context{
				Mode:   keploy.MODE_TEST,
				TestID: id,
				Deps:   k.GetDependencies(id),
			})
			c, err := handler(ctx, req)
			if err != nil {
				panic(err)
			}
			respByte, err := json.Marshal(c)
			if err != nil {
				panic(err)
			}
			resp := string(respByte)
			k.PutRespGrpc(id, resp)
			return c, err
		}
		ctx = context.WithValue(ctx, keploy.KCTX, &keploy.Context{
			Mode: keploy.MODE_RECORD,
		})
		reqByte, err := json.Marshal(req)
		if err != nil {
			panic(err)
		}
		requestJson := string(reqByte)
		infoByte, err := json.Marshal(info)
		if err != nil {
			panic(err)
		}
		serverInfo := grpc.UnaryServerInfo{}
		json.Unmarshal(infoByte, &serverInfo)
		// serverInfo.FullMethod contains the method name with "/" character
		// Here, we remove this redundant character.
		fullMethod := strings.Split(serverInfo.FullMethod, "/")
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
			panic(err)
		}
		respByte, err := json.Marshal(c)
		resp := string(respByte)
		if err != nil {
			panic(err)
		}
		emptyHttpResp := models.HttpResp{}
		keploy.CaptureTestcase(k, nil, nil, emptyHttpResp, nil, ctx, requestJson, method, resp, "grpc")
		return c, err
	}
}

func UnaryInterceptor(k *keploy.Keploy) grpc.ServerOption {
	return grpc.UnaryInterceptor(serverInterceptor(k))
}
