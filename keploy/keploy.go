// Package keploy measures which code each replayed test runs in a Go app,
// for Keploy's dynamic dedup. To activate it, import it for its side effects:
//
//	import _ "github.com/keploy/go-sdk/v3/keploy"
//
// and build the app with atomic coverage counters:
//
//	go build -cover -covermode=atomic -o your-app .
//
// It is the same as importing github.com/keploy/go-sdk/v3/cover, which does
// the work: see that package. It reads the counters in the app's own process,
// so no Go toolchain is needed where the app runs.
package keploy

import (
	// The coverage hook: it binds Keploy's control socket in its init.
	_ "github.com/keploy/go-sdk/v3/cover"
)
