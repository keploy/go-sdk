//go:build !unix

package keploytest

import "errors"

const reexecs = false

func reexec() error { return errors.New("exec in place is Unix's") }
