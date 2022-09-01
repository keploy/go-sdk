package mock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keploy/go-sdk/keploy"
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

var (
	grpcClient *grpc.ClientConn
	logger     *zap.Logger
)

type Config struct {
	Mode keploy.Mode
	Name string
	CTX  context.Context
	Path string
}

func init() {
	// Initialize a logger
	logger, _ = zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()

	var err error
	grpcClient, err = grpc.Dial("localhost:8081", grpc.WithInsecure())
	if err != nil {
		logger.Error("🚨 Failed to connect to keploy server via grpc. Please ensure that keploy server is running", zap.Error(err))
	}
	keploy.SetGrpcClient(grpcClient)
}

func NewContext(conf Config) context.Context {
	var (
		mode  = keploy.MODE_TEST
		mocks []models.Mock
		err   error
		path  string = conf.Path
	)

	// use current directory, if path is not provided or relative in config
	if conf.Path == "" {
		path, err = os.Getwd()
		if err != nil {
			logger.Error("Failed to get the path of current directory", zap.Error(err))
		}
	} else if conf.Path[0] != '/' {
		path, err = filepath.Abs(conf.Path)
		if err != nil {
			logger.Error("Failed to get the absolute path from relative conf.path", zap.Error(err))
		}
	}
	path += "/mocks"
	keploy.SetPath(path)

	if keploy.Mode(os.Getenv("KEPLOY_MODE")).Valid() {
		mode = keploy.Mode(os.Getenv("KEPLOY_MODE"))
	}
	// mode mostly dependent on conf.Mode
	if keploy.Mode(conf.Mode).Valid() {
		mode = keploy.Mode(conf.Mode)
	}
	keploy.SetMode(mode)

	if mode == keploy.MODE_TEST {
		if conf.Name == "" {
			logger.Error("🚨 Please enter the auto generated name to mock the dependencies using Keploy.")
		}
		mocks, err = GetAllMocks(context.Background(), &proto.GetMockReq{Path: path, Name: conf.Name})
		if err != nil {
			logger.Error("🚨 Failed to get the mocks from keploy server. Please ensure that keploy server is running.", zap.Error(err))
		}
	}

	k := &keploy.Context{
		TestID:     conf.Name,
		Mock:       mocks,
		Mode:       mode,
		FileExport: true,
	}
	ctx := conf.CTX
	if ctx == nil {
		ctx = context.Background()
	}

	fmt.Println("\n💡⚡️ Keploy created new mocking context for ", conf.Name, ". Please ensure that dependencies are integerated with Keploy")
	return context.WithValue(ctx, keploy.KCTX, k)
}
