//go:build darwin

package keploytest

import "golang.org/x/sys/unix"

// namesFoldCase: the package reads no command line on macOS (systemCmdline),
// so it matches no program's name there.
const namesFoldCase = false

// readsParents: the package reads macOS's process table (sysctl
// kern.proc.pid).
func readsParents() bool { return true }

// procInfo is process pid's parent pid and start time (ns), as one answer of
// macOS's process table gives them; a var so tests can stand in a table.
var procInfo = kinfoProc

func kinfoProc(pid int) (int, int64, bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, 0, false
	}
	return int(k.Eproc.Ppid), k.Proc.P_starttime.Nano(), true
}

// systemParent is process pid's parent, from macOS's process table, while
// that parent is the process that started pid (trueParent): the table is
// read one process at a time, and a pid read in one answer may have passed
// to a later process by the next. pid's own start time is the one read with
// its parent pid, in the same answer.
func systemParent(pid int) (int, bool) {
	ppid, started, ok := procInfo(pid)
	if !ok {
		return 0, false
	}
	return trueParent(pid, ppid, func(p int) (int64, bool) {
		if p == pid {
			return started, true
		}
		_, s, ok := procInfo(p)
		return s, ok
	})
}

// systemCmdline: the package reads no command line on macOS, so it reads no
// go test -p there (oneTestProcess); keploy's agent refuses a second test
// process of the run.
func systemCmdline(int) ([]string, bool) { return nil, false }

// systemCreated: on macOS keploy's Go launcher execs the test binary, which
// keeps the claim the launcher made for its pid, so no launcher marker names
// a parent there, and none is honored.
func systemCreated(int) (int64, bool) { return 0, false }
