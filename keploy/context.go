package keploy

type Context struct {
	Mode string
	TestID string
	Deps []Dependency
}
