//go:build !windows && !darwin

package keploytest

import "os"

// namesFoldCase: these systems' file names are matched as written.
const namesFoldCase = false

// readsParents reports whether the package reads this system's process
// table: Linux's /proc. Without it the package cannot tell which process
// runs inside which.
func readsParents() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}

func systemParent(pid int) (int, bool)       { return procfsParent(pid) }
func systemCmdline(pid int) ([]string, bool) { return procfsCmdline(pid) }

// systemCreated: on Linux keploy's Go launcher execs the test binary, which
// keeps the claim the launcher made for its pid, so no launcher marker names
// a parent there, and none is honored.
func systemCreated(int) (int64, bool) { return 0, false }
