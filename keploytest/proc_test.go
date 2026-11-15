package keploytest

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The roles this test binary plays as a child of the rows below. init runs a
// role before TestMain parses any flag, so a child started with go test's own
// command line (test -p 4 ./...) plays its role and runs no test.
const (
	// envProcRole is the role: idle (wait, then exit), gocmd (this binary
	// named go, with go test's command line: it starts the probe as cmd/go
	// starts a test binary) or probe (print what the package reads of the go
	// test above it).
	envProcRole = "KEPLOYTEST_PROC_ROLE"
	// envProcSelf is the test binary the gocmd role starts as the probe.
	envProcSelf = "KEPLOYTEST_PROC_SELF"
)

func init() {
	switch os.Getenv(envProcRole) {
	case "":
		return
	case "idle":
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	case "gocmd":
		c := exec.Command(os.Getenv(envProcSelf))
		c.Env = append(os.Environ(), envProcRole+"=probe")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			fmt.Printf("PROBE-RUN %v\n", err)
			os.Exit(3)
		}
		os.Exit(0)
	case "probe":
		args, direct, ok := ancestorGoTest(os.Getppid())
		fmt.Printf("PROBE ok=%v direct=%v args=%q\n", ok, direct, args)
		r := oneTestProcess(func(k string) string {
			if k == "GOENV" {
				return "off"
			}
			return ""
		}, os.Getppid())
		if r == nil {
			fmt.Println("REFUSAL none")
		} else {
			fmt.Printf("REFUSAL %s\n", r.text())
		}
		os.Exit(0)
	}
	fmt.Printf("unknown %s %q\n", envProcRole, os.Getenv(envProcRole))
	os.Exit(3)
}

// readsTable skips a row on a system whose process table the package does
// not read: there it cannot tell which process runs inside which, and takes
// keploy's marker at its word.
func readsTable(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "linux", "windows":
	default:
		t.Skipf("the package reads no process table on %s", runtime.GOOS)
	}
}

// procChild is program path run as a child playing role, with args, none of
// the parent's keploy variables or GOFLAGS, and this test binary as the
// probe.
func procChild(t *testing.T, path, role string, args ...string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(path, args...)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(kv), "KEPLOY") || strings.HasPrefix(strings.ToUpper(kv), "GOFLAGS=") {
			continue
		}
		c.Env = append(c.Env, kv)
	}
	c.Env = append(c.Env, envProcRole+"="+role, envProcSelf+"="+self)
	return c
}

// startIdle starts this test binary as an idle child, and stops it when the
// test ends.
func startIdle(t *testing.T) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := procChild(t, self, "idle")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	return c
}

// keploy's test process, which KEPLOY_TEST_PID names, is one this process
// runs inside only when it is its parent or an ancestor, read from the
// system's process table (Linux's /proc, Windows' process snapshot). A
// process this one started is neither: a marker naming it was inherited from
// elsewhere, and the binary joins keploy's run as a test process of its own,
// rather than run inert with its tests unjudged.
func TestAMarkerNamingAProcessItStartedJoins(t *testing.T) {
	readsTable(t)
	if !runsInside(os.Getppid()) {
		t.Fatal("not inside its parent")
	}
	if gp, ok := procParent(os.Getppid()); ok && gp > 0 && !runsInside(gp) {
		t.Fatal("not inside its parent's parent")
	}
	c := startIdle(t)
	if runsInside(c.Process.Pid) {
		t.Fatal("inside a process it started")
	}
	env := map[string]string{envAgent: "http://127.0.0.1:16789", envTestPID: strconv.Itoa(c.Process.Pid)}
	if got := decide(vars(env), os.Getpid()); got.mode != active {
		t.Fatalf("KEPLOY_TEST_PID names a process it started: mode %d (%s), want active", got.mode, got.refusal)
	}
	env[envTestPID] = strconv.Itoa(os.Getppid())
	if got := decide(vars(env), os.Getpid()); got.mode != inert {
		t.Fatalf("KEPLOY_TEST_PID names its parent: mode %d (%s), want inert (a member)", got.mode, got.refusal)
	}
}

// go test's -p is read from cmd/go's own command line in the system's
// process table: on Windows as on Linux, a go test -p 4 that runs a keploy
// test binary is refused, its arguments read as cmd/go split them (quotes,
// spaces, backslashes, a command line longer than a first read takes) and
// its name matched as the system matches file names (Go.exe is go.exe on
// Windows).
func TestTheGoTestAboveIsReadFromItsCommandLine(t *testing.T) {
	readsTable(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "go"
	if runtime.GOOS == "windows" {
		name = "Go.exe"
	}
	gocmd := filepath.Join(t.TempDir(), name)
	copyExecutable(t, self, gocmd)
	args := []string{"test", "-p", "4", "-run", `Test A|B "q" C:\dir\`, "-ldflags", "-X main.v=" + strings.Repeat("v", 700), "./..."}
	out, err := procChild(t, gocmd, "gocmd", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if want := fmt.Sprintf("PROBE ok=true direct=true args=%q\n", args[1:]); !strings.Contains(string(out), want) {
		t.Fatalf("got %s, want %s", out, want)
	}
	if want := "REFUSAL keploy: keploy runs one test process at a time in this release, and go test runs up to 4 at once (-p 4)"; !strings.Contains(string(out), want) {
		t.Fatalf("got %s, want %s", out, want)
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// cmd/go keeps go env -w's settings in os.UserConfigDir's go/env
// (%AppData%\go\env on Windows, ~/Library/Application Support/go/env on
// macOS, $XDG_CONFIG_HOME or ~/.config elsewhere): a -p=1 set there is read
// from there.
func TestGoEnvWIsReadWhereThisSystemsGoKeepsIt(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{}
	var file string
	switch runtime.GOOS {
	case "windows":
		env["AppData"] = dir
		file = filepath.Join(dir, "go", "env")
	case "darwin", "ios":
		env["HOME"] = dir
		file = filepath.Join(dir, "Library", "Application Support", "go", "env")
	default:
		env["HOME"] = dir
		file = filepath.Join(dir, ".config", "go", "env")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("GOFLAGS=-p=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := goEnvFile(vars(env), "GOFLAGS"); got != "-p=1" {
		t.Fatalf("go env GOFLAGS read as %q from %s, want -p=1", got, file)
	}
}

// A parent is the process that started its child: one the system recorded
// as the parent but created after the child holds a reused pid (Windows keeps
// a parent's pid after it exits), and one whose creation cannot be read is
// not taken as the parent. Two processes created in the same tick can be
// parent and child.
func TestTrueParentIsCreatedNoLaterThanItsChild(t *testing.T) {
	created := map[int]int64{10: 100, 20: 200, 30: 300, 40: 200}
	at := func(pid int) (int64, bool) { v, ok := created[pid]; return v, ok }
	for _, tc := range []struct {
		name          string
		pid, recorded int
		want          int
		ok            bool
	}{
		{"older parent", 20, 10, 10, true},
		{"same tick", 20, 40, 40, true},
		{"parent created after the child: a reused pid", 20, 30, 0, false},
		{"parent gone", 20, 50, 0, false},
		{"child unreadable", 60, 10, 0, false},
		{"no parent", 20, 0, 0, true},
	} {
		got, ok := trueParent(tc.pid, tc.recorded, at)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: trueParent(%d, %d) = %d, %v, want %d, %v", tc.name, tc.pid, tc.recorded, got, ok, tc.want, tc.ok)
		}
	}
}

// A Windows command line is split as a Go program splits its own (os.Args):
// the rows are os/exec's quoting of each argv read back, and the forms only a
// hand-written line has.
func TestSplitCommandLineReadsWhatAGoProgramReads(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{`go test -p 4 ./...`, []string{"go", "test", "-p", "4", "./..."}},
		{"  go\t test  ", []string{"go", "test"}},
		{`"C:\Program Files\Go\bin\go.exe" test`, []string{`C:\Program Files\Go\bin\go.exe`, "test"}},
		{`a\\b c\d`, []string{`a\\b`, `c\d`}},
		{`"a\\" b`, []string{`a\`, "b"}},
		{`a\"b`, []string{`a"b`}},
		{`a\\\"b`, []string{`a\"b`}},
		{`"a""b" c`, []string{`a"b c`}},
		{`"a"" b"`, []string{`a"`, "b"}},
		{`"q x"y`, []string{"q xy"}},
		{`trail\`, []string{`trail\`}},
		{`""`, []string{""}},
		{``, nil},
	} {
		if got := splitCommandLine(tc.line); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", tc.want) {
			t.Errorf("splitCommandLine(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// cmd/go is named go or go.exe; on Windows in any case, as Windows matches
// file names, and elsewhere as written.
func TestIsGoCommand(t *testing.T) {
	for _, n := range []string{"go", "go.exe"} {
		if !isGoCommand(n) {
			t.Errorf("%s is not cmd/go", n)
		}
	}
	for _, n := range []string{"gofmt", "go.bat", "x.test", "ago", ""} {
		if isGoCommand(n) {
			t.Errorf("%s is cmd/go", n)
		}
	}
	fold := runtime.GOOS == "windows"
	for _, n := range []string{"GO", "Go.exe", "GO.EXE"} {
		if isGoCommand(n) != fold {
			t.Errorf("isGoCommand(%s) = %v on %s", n, !fold, runtime.GOOS)
		}
	}
}

// userConfigDir is os.UserConfigDir of each system, as cmd/go reads its go
// env file from it.
func TestUserConfigDirIsEachSystemsOwn(t *testing.T) {
	env := map[string]string{"AppData": `C:\Users\u\AppData\Roaming`, "HOME": "/home/u", "XDG_CONFIG_HOME": "/xdg"}
	for _, tc := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"windows", env, `C:\Users\u\AppData\Roaming`},
		{"windows", map[string]string{"HOME": "/home/u"}, ""},
		{"darwin", env, filepath.Join("/home/u", "Library", "Application Support")},
		{"ios", env, filepath.Join("/home/u", "Library", "Application Support")},
		{"darwin", map[string]string{"AppData": "x"}, ""},
		{"linux", env, "/xdg"},
		{"linux", map[string]string{"HOME": "/home/u"}, filepath.Join("/home/u", ".config")},
		{"freebsd", map[string]string{"HOME": "/home/u"}, filepath.Join("/home/u", ".config")},
		{"linux", map[string]string{}, ""},
	} {
		if got := userConfigDir(tc.goos, vars(tc.env)); got != tc.want {
			t.Errorf("%s %v: %q, want %q", tc.goos, tc.env, got, tc.want)
		}
	}
}
