package keploy

type Context struct {
	Mode   Mode
	TestID string
	Deps   []Dependency
}
