// Package cover lets Keploy measure which code each replayed test runs in a Go
// app, for Keploy's dynamic dedup and per-test code coverage.
//
// It needs the app built with coverage counters:
//
//	go build -cover -covermode=atomic ...
//
// and this package linked in, which an app does in one of two ways:
//
//   - a blank import in package main:
//
//     import _ "github.com/keploy/go-sdk/v3/cover"
//
//   - or, with no change to the app's code or go.mod, Keploy's build stage,
//     keploy-go-cover-build.sh (beside this file), run in a copy of the app's
//     module, such as a Dockerfile build stage: it copies this package into
//     the module as zz_keploy_cover, adds a blank import of it to package
//     main, builds with -cover -covermode=atomic over the module's own
//     packages, and removes both again.
//
// Once linked, the package listens before main runs on
// /tmp/coverage_control.sock for Keploy's START and END of each test: START
// resets the counters, END reads them in this process (no Go toolchain is
// needed where the app runs, so a distroless image, or a scratch image given
// a writable /tmp, works) and sends the lines each source file ran to
// /tmp/coverage_data.sock as one JSON record, {"executedLinesByFile": {"<import path>/<file>": [lines]}, "id":
// "<set>/<test>"}. An app built without -cover, or without
// -covermode=atomic, answers each START IGNORED and logs why once, so Keploy
// reports its tests as not measured rather than guessing.
//
// The package is stdlib-only and has no subpackages, so the build stage can
// copy it into any module. Its decoder is derived from Go's own
// internal/coverage (see LICENSE-go).
package cover
