package mock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/keploy/go-sdk/internal/keploy"
	proto "go.keploy.io/server/grpc/regression"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

var (
	grpcClient proto.RegressionServiceClient
	logger     *zap.Logger
)

type Config struct {
	Mode      keploy.Mode
	Name      string
	CTX       context.Context
	Path      string
	OverWrite bool
}

func init() {
	// Initialize a logger
	logger, _ = zap.NewDevelopment()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()

	var err error
	conn, err := grpc.Dial("localhost:6789", grpc.WithInsecure())
	if err != nil {
		logger.Error("❌ Failed to connect to keploy server via grpc. Please ensure that keploy server is running", zap.Error(err))
	}
	grpcClient = proto.NewRegressionServiceClient(conn)
	keploy.SetGrpcClient(grpcClient)
}

func NewContext(conf Config) context.Context {
	var (
		mode  = keploy.MODE_TEST
		mocks []*proto.Mock
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
			logger.Error("❌ Failed to get the mocks from keploy server. Please ensure that keploy server is running.", zap.Error(err))
		}
	}

	k := &keploy.Context{
		TestID:     conf.Name,
		Mock:       mocks,
		Mode:       mode,
		FileExport: true,
		Mu:         &sync.Mutex{},
	}
	ctx := conf.CTX
	if ctx == nil {
		ctx = context.Background()
	}

	name := ""
	if conf.Name != "" {
		name = " for " + conf.Name
	}

	fmt.Printf("\n💡⚡️ Keploy created new mocking context in %v mode %v.\n If you dont see any logs about your dependencies below, your dependency/s are NOT wrapped.\n", mode, name)
	exists := StartRecordingMocks(context.Background(), path+"/"+conf.Name+".yaml", string(mode), name, conf.OverWrite)
	if exists && !conf.OverWrite {
		logger.Error(fmt.Sprintf("🚨 Keploy failed to record dependencies because yaml file already exists%v in directory: %v.\n", name, path))
		// fmt.Printf("🚨 Keploy failed to record dependencies because yaml file already exists%v in directory: %v.\n", name, path)
		keploy.MockId.Load(conf.Name)
	}
	return context.WithValue(ctx, keploy.KCTX, k)
}
