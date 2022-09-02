package keploy

import (
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/pkg/models"
)

type Context struct {
	Mode       Mode
	TestID     string
	FileExport bool
	Deps       []models.Dependency
	Mock       []*proto.Mock
}
