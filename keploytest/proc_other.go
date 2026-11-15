//go:build !windows

package keploytest

import "os"

// namesFoldCase: these systems' file names are matched as written.
const namesFoldCase = false

// readsParents reports whether the package reads this system's process
// table: Linux's /proc. Without it (macOS) the package cannot tell which
// process runs inside which.
func readsParents() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}

func systemParent(pid int) (int, bool)       { return procfsParent(pid) }
func systemCmdline(pid int) ([]string, bool) { return procfsCmdline(pid) }
