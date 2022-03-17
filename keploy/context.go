package keploy

import (
	// "time"

	// "github.com/golang/protobuf/ptypes/timestamp"
	// "go.keploy.io/server/http/regression"
	"time"

	// "github.com/golang/protobuf/ptypes/timestamp"
	"go.keploy.io/server/pkg/models"
)

type Context struct {
	Mode    Mode
	TestID  string
	Deps    []models.Dependency
	Capture time.Time 
}
