package keploytest

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The system's process table: each process's parent and command line. The
// package reads it to tell whether this process runs inside keploy's test
// process (runsInside) and which go test runs it (oneTestProcess). On Linux
// it is /proc; on Windows, the system's process snapshot and each process's
// own command line (proc_windows.go). On a system it does not read (macOS),
// readsParents is false.

// procRoot is where /proc is read; a var for tests.
var procRoot = "/proc"

// procParent is process pid's parent and procCmdline its command line, read
// from the system's process table; vars so tests can stand in a /proc tree
// (procfsParent, procfsCmdline) on any system.
var (
	procParent  = systemParent
	procCmdline = systemCmdline
)

// procfsParent is process pid's parent, from <procRoot>/<pid>/stat.
func procfsParent(pid int) (int, bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	// The command name (field 2) is parenthesised and may hold spaces and
	// parentheses: the fields that follow come after its last ')'.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	return ppid, err == nil
}

// procfsCmdline is process pid's command line, from <procRoot>/<pid>/cmdline.
func procfsCmdline(pid int) ([]string, bool) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	return strings.Split(string(bytes.TrimRight(raw, "\x00")), "\x00"), true
}

// trueParent is process pid's parent, given the parent pid the system
// recorded for it and each process's creation time (created, in the
// system's ticks): Windows keeps a process's parent pid after that parent
// exits, and may give the pid to a later process, which is not pid's parent.
// A parent is the process that started pid, so it was created no later than
// pid. Both times are read while the processes are open, so neither pid can
// pass to another process meanwhile. Recorded 0 is no parent (the system's
// first processes).
func trueParent(pid, recorded int, created func(pid int) (int64, bool)) (int, bool) {
	if recorded == 0 {
		return 0, true
	}
	child, ok := created(pid)
	if !ok {
		return 0, false
	}
	parent, ok := created(recorded)
	if !ok || parent > child {
		return 0, false
	}
	return recorded, true
}

// splitCommandLine is a Windows command line's arguments as a Go program
// reads them into os.Args (and as the C runtime does): arguments are
// separated by spaces and tabs outside double quotes; 2n backslashes and a
// quote are n backslashes and the quote opens or closes a quoted part, 2n+1
// backslashes and a quote are n backslashes and a literal quote; inside a
// quoted part two quotes are one literal quote and end the part (the rule
// before 2008, which Go keeps); any other backslash is itself. cmd/go is a
// Go program, so this is the argv it runs with.
func splitCommandLine(cmd string) []string {
	var args []string
	for {
		cmd = strings.TrimLeft(cmd, " \t")
		if cmd == "" {
			return args
		}
		var arg []byte
		quoted, slashes := false, 0
		i := 0
	scan:
		for ; i < len(cmd); i++ {
			switch c := cmd[i]; c {
			case '\\':
				slashes++
				continue
			case '"':
				arg = append(arg, bytes.Repeat([]byte{'\\'}, slashes/2)...)
				if slashes%2 == 1 {
					arg = append(arg, '"')
				} else {
					if quoted && i+1 < len(cmd) && cmd[i+1] == '"' {
						arg = append(arg, '"')
						i++
					}
					quoted = !quoted
				}
				slashes = 0
				continue
			case ' ', '\t':
				if !quoted {
					break scan
				}
			}
			arg = append(arg, bytes.Repeat([]byte{'\\'}, slashes)...)
			slashes = 0
			arg = append(arg, cmd[i])
		}
		arg = append(arg, bytes.Repeat([]byte{'\\'}, slashes)...)
		args = append(args, string(arg))
		cmd = cmd[i:]
	}
}

// isGoCommand reports whether a program named base (its file name) is
// cmd/go: go, or go.exe, matched as the system matches file names.
func isGoCommand(base string) bool {
	if namesFoldCase {
		return strings.EqualFold(base, "go") || strings.EqualFold(base, "go.exe")
	}
	return base == "go" || base == "go.exe"
}
