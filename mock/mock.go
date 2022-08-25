package mock

import (
	"context"
	"os"

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
	ID   string
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
		logger.Error("failed to connect to keploy server", zap.Error(err))
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

	// use current directory, if path is not provided in config
	if conf.Path == "" {
		path, err = os.Getwd()
		if err != nil {
			logger.Error("failed to get the path of current directory", zap.Error(err))
		}
	}
	keploy.SetPath(path)

	if keploy.Mode(os.Getenv("KEPLOY_MODE")).Valid() {
		mode = keploy.Mode(os.Getenv("KEPLOY_MODE"))
	}
	keploy.SetMode(mode)

	if mode == keploy.MODE_TEST {
		mocks, err = GetAllMocks(context.Background(), &proto.GetMockReq{Path: path, Name: conf.Name})
		if err != nil {
			logger.Error("failed to get the mocks from keploy server", zap.Error(err))
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

	// create mock yaml file if not present
	CreateMockFile(path)
	return context.WithValue(ctx, keploy.KCTX, k)
}
