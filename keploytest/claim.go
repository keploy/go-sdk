package keploytest

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The test binary joins keploy's run in the package's init: before the test
// package's own init and TestMain run (they come after the init of every
// package it imports; other imported packages that do not import keploytest
// may initialize first, in import-path order), it claims the run for itself,
// and refuses what keploy's first release cannot judge. A refusal fails
// every test that has a keploytest line, with the fix.

// keployParallel is the -test.parallel the package gives the binary.
// Parallel serializes parallel tests by tickets: each waiting test holds a
// slot of -test.parallel, so fewer slots than waiting tests deadlock.
const keployParallel = 1048576

func init() {
	joinOnce.Do(func() { joined = claimRun(os.Getenv, os.Args, os.Getpid(), os.Getppid()) })
}

// claimRun joins the test binary to keploy's run, or says why not.
func claimRun(getenv func(string) string, args []string, pid, ppid int) joining {
	if listing(args) {
		return joining{mode: inert} // an editor listing the tests
	}
	j := decide(getenv, pid)
	if j.mode != active {
		return j
	}
	// Processes this one starts are members of its run.
	_ = os.Setenv(envTestPID, strconv.Itoa(pid))
	if r := oneTestProcess(getenv, ppid); r != nil {
		return refuseRun(newClient(j.url, j.token), r)
	}
	key, dir, err := packageKey(getenv(envProject))
	if err != nil {
		return refuse("%s", err.Error())
	}
	if err := newClient(j.url, j.token).claim(key, dir); err != nil {
		return refuse("%s", err.Error())
	}
	testing.Init()
	if f := flag.Lookup("test.parallel"); f != nil {
		_ = f.Value.Set(strconv.Itoa(keployParallel))
	}
	return j
}

// listing reports whether the binary was asked to list its tests
// (-test.list): it runs no test.
func listing(args []string) bool {
	for _, a := range args[min(1, len(args)):] {
		a = strings.TrimLeft(a, "-")
		if a == "test.list" || strings.HasPrefix(a, "test.list=") {
			return true
		}
	}
	return false
}

// packageKey is the binary's package directory relative to the keploy
// project (its instance key), and the directory itself. cmd/go runs a test
// binary in its package's directory.
func packageKey(project string) (string, string, error) {
	if project == "" {
		return "", "", fmt.Errorf("keploy: this keploy did not name the project of the test command (%s); run the tests through keploy mock record or keploy mock replay -c, in a project whose keploy.yml sets mock.command", envProject)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("keploy: cannot read the test binary's directory: %v", err)
	}
	dir, proj := realPath(wd), realPath(project)
	rel, err := filepath.Rel(proj, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("keploy: the package in %s is outside the keploy project %s; run the tests from the project", dir, proj)
	}
	return filepath.ToSlash(rel), dir, nil
}

func realPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// procRoot is where the parent's command line is read; a var for tests.
var procRoot = "/proc"

// oneTestProcess refuses a go test that may run two test binaries at once:
// keploy's first release judges one test process at a time, and cannot tell
// two apart. It reads cmd/go's own configuration, never timing: the -p it
// was given, else GOFLAGS' (the environment's, else go env -w's), else its
// default (GOMAXPROCS). cmd/go is the binary's parent, or, under go test
// -exec, the nearest go test among its ancestors. A binary no go test runs
// (one a test binary starts, say) runs alone. It reads /proc: keploy's
// first release runs the test command natively on Linux only.
func oneTestProcess(getenv func(string) string, ppid int) *runRefusal {
	args, direct, ok := ancestorGoTest(ppid)
	if !ok || (!direct && !execs(args, getenv)) {
		return nil
	}
	if p, from := effectiveP(args, getenv); p > 1 {
		return &runRefusal{
			Code:    codeOneTestProcess,
			Message: fmt.Sprintf("keploy runs one test process at a time in this release, and go test runs up to %d at once (%s)", p, from),
			Remedy:  "add -p 1 (go test -p 1 -json ./...); parallel test workers arrive in a later keploy release",
		}
	}
	return nil
}

// codeOneTestProcess is the scope protocol's code for a second test process
// in one keploy run: the agent refuses a second claim with it, and this
// package a go test that would run one.
const codeOneTestProcess = "one_test_process_at_a_time"

// runRefusal is a refusal of the whole test process that this package makes
// before it claims keploy's run: its code, what it refused, and the fix.
type runRefusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Remedy  string `json:"remedy,omitempty"`
}

// text is the refusal as the tests fail with it: what was refused, and the
// fix. (Not an Error method: a nil *runRefusal must never become a non-nil
// error.)
func (r *runRefusal) text() string {
	if r.Remedy == "" {
		return "keploy: " + r.Message
	}
	return "keploy: " + r.Message + ": " + r.Remedy
}

// refuseRun refuses this test process for r, and tells keploy's agent (POST
// /agent/refusal), so the run's verdict names the refusal rather than
// saying only that no test ran. The tests fail with r whether or not the
// agent heard; when it did not, they say so.
func refuseRun(c *client, r *runRefusal) joining {
	if err := c.refusal(*r); err != nil {
		return refuse("%s (keploy's agent was not told, so keploy's verdict cannot name it: %s)", r.text(), strings.TrimPrefix(err.Error(), "keploy: "))
	}
	return refuse("%s", r.text())
}

// ancestorGoTest is parentGoTest of the nearest of pid and its ancestors
// that is cmd/go running go test, and whether that is pid itself.
func ancestorGoTest(pid int) ([]string, bool, bool) {
	for i := 0; i < 16 && pid > 0; i++ {
		if args, ok := parentGoTest(pid); ok {
			return args, i == 0, true
		}
		next, ok := procParent(pid)
		if !ok || next == pid {
			return nil, false, false
		}
		pid = next
	}
	return nil, false, false
}

// execs reports whether go test runs its binaries through a program
// (-exec, on its command line or in GOFLAGS): only then is a go test above
// the binary's parent the one that runs it.
func execs(testArgs []string, getenv func(string) string) bool {
	is := func(a string) bool {
		a = strings.TrimLeft(a, "-")
		return a == "exec" || strings.HasPrefix(a, "exec=")
	}
	for _, a := range testArgs {
		if a == "-args" || a == "--args" {
			break
		}
		if is(a) {
			return true
		}
	}
	flags := getenv("GOFLAGS")
	if flags == "" {
		flags = goEnvFile(getenv, "GOFLAGS")
	}
	for _, a := range strings.Fields(flags) {
		if is(a) {
			return true
		}
	}
	return false
}

// parentGoTest is the arguments after "test" of process ppid's command
// line, when it is cmd/go running go test.
func parentGoTest(ppid int) ([]string, bool) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(ppid), "cmdline"))
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	argv := strings.Split(string(bytes.TrimRight(raw, "\x00")), "\x00")
	if base := filepath.Base(argv[0]); base != "go" && base != "go.exe" {
		return nil, false
	}
	// cmd/go takes one flag before its subcommand: -C <dir>.
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-C" || a == "--C":
			i++
			continue
		case strings.HasPrefix(a, "-C=") || strings.HasPrefix(a, "--C="):
			continue
		}
		if a == "test" {
			return argv[i+1:], true
		}
		return nil, false
	}
	return nil, false
}

// effectiveP is the -p cmd/go runs test binaries with, and where it comes
// from: the command line's last -p (go test takes flags before and after its
// packages, up to -args), else GOFLAGS', else GOMAXPROCS (cmd/go's default,
// as the runtime sets it for this machine).
func effectiveP(testArgs []string, getenv func(string) string) (int, string) {
	if p, ok := lastP(testArgs, true); ok {
		return p, fmt.Sprintf("-p %d", p)
	}
	if p, ok := lastP(strings.Fields(getenv("GOFLAGS")), false); ok {
		return p, fmt.Sprintf("GOFLAGS -p=%d", p)
	}
	if getenv("GOFLAGS") == "" {
		// cmd/go reads GOFLAGS from its go env file when the environment
		// has none, and does not hand that to the binary.
		if p, ok := lastP(strings.Fields(goEnvFile(getenv, "GOFLAGS")), false); ok {
			return p, fmt.Sprintf("go env GOFLAGS -p=%d", p)
		}
	}
	n := runtime.GOMAXPROCS(0)
	return n, fmt.Sprintf("go test's default -p, GOMAXPROCS=%d", n)
}

// goEnvFile is key's value in cmd/go's go env file (go env -w): $GOENV,
// else <user config dir>/go/env; "" when it has none.
func goEnvFile(getenv func(string) string, key string) string {
	path := getenv("GOENV")
	switch path {
	case "off":
		return ""
	case "":
		dir := getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home := getenv("HOME")
			if home == "" {
				return ""
			}
			dir = filepath.Join(home, ".config")
		}
		path = filepath.Join(dir, "go", "env")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// cmd/go keeps the last setting of a key, as written.
	val := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			val = v
		}
	}
	return val
}

// lastP is the last -p in args. stopAtArgs ends the scan at -args, after
// which everything is the test binary's.
func lastP(args []string, stopAtArgs bool) (int, bool) {
	p, found := 0, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if stopAtArgs && (a == "-args" || a == "--args") {
			break
		}
		var v string
		switch {
		case a == "-p" || a == "--p":
			if i+1 >= len(args) {
				continue
			}
			i++
			v = args[i]
		case strings.HasPrefix(a, "-p="):
			v = a[len("-p="):]
		case strings.HasPrefix(a, "--p="):
			v = a[len("--p="):]
		default:
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p, found = n, true
		}
	}
	return p, found
}

// testFlagsRefusal is why the binary's own flags make keploy's verdicts
// unreadable or the run unjudgeable, "" when they do not: read at the first
// test, once the flags are parsed. A plain go test prints only "ok", so a
// skip and its reason would be invisible: -v or -json is required. A
// shuffled order or fuzzing makes the run differ from the recording's.
func testFlagsRefusal() string {
	if f := flag.Lookup("test.v"); f != nil {
		if v := f.Value.String(); v != "true" && v != "test2json" {
			return "keploy: run go test with -v or -json (go test -p 1 -json ./...): without them go test prints only ok, so keploy's skips and failures would be invisible"
		}
	}
	if f := flag.Lookup("test.shuffle"); f != nil && f.Value.String() != "off" {
		return "keploy: -shuffle runs the tests in another order than the recording's; run go test without -shuffle"
	}
	if f := flag.Lookup("test.fuzz"); f != nil && f.Value.String() != "" {
		return "keploy: fuzzing (-fuzz) is not judged by keploy; run it without keploy (KEPLOY_MOCK_AGENT=off)"
	}
	return ""
}
