//go:build unix

package keploytest

import (
	"os"
	"syscall"
)

// reexecs: the binary can exec itself in place, keeping its pid.
const reexecs = true

// reexec replaces the test binary with a new image of itself, same pid.
func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
