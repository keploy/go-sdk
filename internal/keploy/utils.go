package keploy

import (
	"context"
	"sync"
	// "time"

	proto "go.keploy.io/server/grpc/regression"
	"go.uber.org/zap"
)

type GrpcClient proto.RegressionServiceClient

type MockLib struct {
	mockIds sync.Map
}

var (
	MockId   = MockLib{mockIds: sync.Map{}}
	MockPath string
	logger   *zap.Logger
	client   GrpcClient // decoupled from keploy instance to use it in unit-test mocking infrastructure
)

func init() {
	// Initialize a logger
	logger, _ = zap.NewDevelopment()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()
}

func SetPath(path string) {
  mu.Lock()
	MockPath = path
  mu.Unlock()
}

// avoids circular dependency between mock and keploy packages
func SetGrpcClient(c GrpcClient) {
	client = c
}
func GetGrpcClient() GrpcClient {
	return client
}

// To avoid creating the duplicate mock yaml file
func (m *MockLib) Unique(name string) bool {
	_, ok := m.mockIds.Load(name)
	return !ok
}
func (m *MockLib) Load(name string) {
	m.mockIds.Store(name, true)
}

func PutMock(ctx context.Context, path string, mock *proto.Mock) bool {

	_, err := client.PutMock(ctx, &proto.PutMockReq{Path: path, Mock: mock})
	if err != nil {
		logger.Error("Failed to call the putMock method", zap.Error(err))
		return false
	}
	return true
}
