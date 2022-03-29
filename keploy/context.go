package keploy

import "go.keploy.io/server/pkg/models"

type Context struct {
	Mode    Mode
	TestID  string
	Deps    []models.Dependency
}
