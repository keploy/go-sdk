//go:build linux || darwin

package cover

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests build real apps with the go command, the way an app's build
// does, and drive them as Keploy does: START, a request, END, and the record
// read off the data socket.

const appModule = "example.com/kcapp"

// appSource is the app the tests build: twin answers each request on the same
// lines whatever its n, unique on others, and on more of them for deep=1.
const appSource = `package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
)

func twin(w http.ResponseWriter, r *http.Request) {
	n := r.URL.Query().Get("n")
	fmt.Fprintf(w, "twin %s", n)
}

func unique(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("deep") == "1" {
		fmt.Fprint(w, "deep")
		return
	}
	fmt.Fprint(w, "unique")
}

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	http.HandleFunc("/twin", twin)
	http.HandleFunc("/unique", unique)
	fmt.Println(ln.Addr().String())
	_ = os.Stdout.Sync()
	_ = http.Serve(ln, nil)
}
`

// lineOf is the 1-based line of appSource that holds s.
func lineOf(t *testing.T, s string) int {
	t.Helper()
	for i, l := range strings.Split(appSource, "\n") {
		if strings.Contains(l, s) {
			return i + 1
		}
	}
	t.Fatalf("appSource has no line holding %q", s)
	return 0
}

func needGo(t testing.TB) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command to build the test apps with")
	}
	return goBin
}

// hookCopy is a copy of this package as the build stage copies it (its
// non-test .go files, LICENSE-go and the script), plus one file that points
// the copy's sockets at sock and data, so the app never binds
// /tmp/coverage_control.sock. A package-level initializer runs before every
// init, as this package's tests' own does.
func hookCopy(t *testing.T, sock, data string) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !strings.HasSuffix(name, ".go") && name != "LICENSE-go" && name != "keploy-go-cover-build.sh" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	paths := fmt.Sprintf("package cover\n\nvar _ = func() int {\n\tcontrolSocketPath = %q\n\tdataSocketPath = %q\n\treturn 0\n}()\n", sock, data)
	if err := os.WriteFile(filepath.Join(dir, "zz_test_paths.go"), []byte(paths), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// appModuleDir writes the app's module: go.mod, an empty go.sum and main.go.
func appModuleDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":  "module " + appModule + "\n\ngo 1.21\n",
		"go.sum":  "",
		"main.go": appSource,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func run(t testing.TB, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOFLAGS=", "GOWORK=off"), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// buildByHand puts the hook in the module and builds it with buildArgs, as an
// app that imports the package does.
func buildByHand(t *testing.T, hook, mod string, buildArgs ...string) string {
	t.Helper()
	goBin := needGo(t)
	if err := os.MkdirAll(filepath.Join(mod, "zz_keploy_cover"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(hook)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			b, err := os.ReadFile(filepath.Join(hook, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(mod, "zz_keploy_cover", e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	imp := "package main\n\nimport _ \"" + appModule + "/zz_keploy_cover\"\n"
	if err := os.WriteFile(filepath.Join(mod, "zz_keploy_cover_main.go"), []byte(imp), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "app")
	args := append(append([]string{"build"}, buildArgs...), "-o", bin, ".")
	if out, err := run(t, mod, nil, goBin, args...); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return bin
}

// keploySide is the agent's side: the data socket's listener and the records
// it got (test & background records on records, INDEX records on indexes, and
// KTB1 table frames via onFrame when set).
type keploySide struct {
	sock, data string
	records    chan map[string]any
	indexes    chan map[string]any
	onFrame    func(conn net.Conn, raw []byte)
}

func newKeploySide(t *testing.T) *keploySide {
	t.Helper()
	dir := shortTempDir(t)
	k := &keploySide{
		sock:    filepath.Join(dir, "c.sock"),
		data:    filepath.Join(dir, "d.sock"),
		records: make(chan map[string]any, 16),
		indexes: make(chan map[string]any, 16),
	}
	ln, err := net.Listen("unix", k.data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b, _ := io.ReadAll(c)
			if bytes.HasPrefix(b, []byte(tableFrameMagic)) {
				if k.onFrame != nil {
					k.onFrame(c, b)
				} else {
					_, _ = c.Write([]byte("OK\n"))
				}
				_ = c.Close()
				continue
			}
			_ = c.Close()
			var rec map[string]any
			if err := json.Unmarshal(b, &rec); err != nil {
				rec = map[string]any{"undecodable": string(b)}
			}
			if rec["kind"] == "index" {
				select {
				case k.indexes <- rec:
				default:
				}
				continue
			}
			k.records <- rec
		}
	}()
	return k
}

// app is a running test app.
type app struct {
	addr   string
	stderr *bytes.Buffer
	mu     *sync.Mutex
	// stop kills the app's process group and waits for it and for its
	// output's copy to end; t's cleanup calls it too.
	stop func()
}

func (a app) log() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stderr.String()
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// start runs bin in a process group of its own, killed when t ends, and
// returns its address once it serves.
func start(t *testing.T, bin string) app {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	a := app{stderr: &bytes.Buffer{}, mu: &sync.Mutex{}}
	cmd.Stderr = lockedWriter{a.mu, a.stderr}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	a.stop = func() {
		once.Do(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		})
	}
	t.Cleanup(a.stop)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("the app did not start: %v\n%s", err, a.log())
	}
	a.addr = strings.TrimSpace(line)
	return a
}

func get(t *testing.T, a app, path string) {
	t.Helper()
	res, err := http.Get("http://" + a.addr + path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
}

// measure runs one test as Keploy does and returns the record END sent.
func measure(t *testing.T, k *keploySide, a app, id, path string) map[string]any {
	t.Helper()
	if got := send(t, k.sock, "START "+id); got != replyStartUnasked {
		t.Fatalf("START %s: answer %q, want %q\napp log:\n%s", id, got, replyStartUnasked, a.log())
	}
	get(t, a, path)
	if got := send(t, k.sock, "END "+id); got != replyTaken {
		t.Fatalf("END %s: answer %q, want %q\napp log:\n%s", id, got, replyTaken, a.log())
	}
	select {
	case rec := <-k.records:
		return rec
	case <-time.After(10 * time.Second):
		t.Fatalf("END %s sent no record\napp log:\n%s", id, a.log())
	}
	return nil
}

// linesOfRecord is a record's executedLinesByFile, decoded.
func linesOfRecord(t *testing.T, rec map[string]any) map[string][]int {
	t.Helper()
	raw, ok := rec["executedLinesByFile"].(map[string]any)
	if !ok {
		t.Fatalf("record %v has no executedLinesByFile object", rec)
	}
	out := make(map[string][]int, len(raw))
	for file, v := range raw {
		list, ok := v.([]any)
		if !ok {
			t.Fatalf("record %v: %s holds no list", rec, file)
		}
		for _, l := range list {
			out[file] = append(out[file], int(l.(float64)))
		}
	}
	return out
}

// TestTheBuildStageMeasuresEachTestWithoutTouchingTheModule builds the app
// with keploy-go-cover-build.sh and drives four tests through it: two twins
// that run the same lines, a unique one and a deeper one. Each END's record
// names exactly the lines its handler ran, under the file's build path, and
// nothing of the hook. The app's go.mod and go.sum are as they were, and the
// two files the stage added are gone.
func TestTheBuildStageMeasuresEachTestWithoutTouchingTheModule(t *testing.T) {
	goBin := needGo(t)
	k := newKeploySide(t)
	hook := hookCopy(t, k.sock, k.data)
	mod := appModuleDir(t)
	goModBefore, _ := os.ReadFile(filepath.Join(mod, "go.mod"))
	goSumBefore, _ := os.ReadFile(filepath.Join(mod, "go.sum"))
	bin := filepath.Join(t.TempDir(), "app")
	out, err := run(t, mod, []string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")},
		"sh", filepath.Join(hook, "keploy-go-cover-build.sh"), "-o", bin, ".")
	if err != nil {
		t.Fatalf("keploy-go-cover-build.sh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "adding the coverage hook as "+appModule+"/zz_keploy_cover") {
		t.Fatalf("the stage did not say it adds the hook:\n%s", out)
	}
	if !strings.Contains(out, "-cover -covermode=atomic -coverpkg="+appModule+"/... ") {
		t.Fatalf("the stage's build did not cover the module's packages alone:\n%s", out)
	}
	goModAfter, _ := os.ReadFile(filepath.Join(mod, "go.mod"))
	goSumAfter, _ := os.ReadFile(filepath.Join(mod, "go.sum"))
	if !bytes.Equal(goModBefore, goModAfter) || !bytes.Equal(goSumBefore, goSumAfter) {
		t.Fatalf("the stage changed go.mod or go.sum:\ngo.mod %q -> %q\ngo.sum %q -> %q", goModBefore, goModAfter, goSumBefore, goSumAfter)
	}
	for _, left := range []string{"zz_keploy_cover", "zz_keploy_cover_main.go"} {
		if _, err := os.Stat(filepath.Join(mod, left)); !os.IsNotExist(err) {
			t.Fatalf("the stage left %s in the module (%v)", left, err)
		}
	}

	a := start(t, bin)
	file := appModule + "/main.go"
	tw := lineOf(t, "func twin(")
	un := lineOf(t, "func unique(")
	paths := map[string]string{
		"set-0/test-1": "/twin?n=1",
		"set-0/test-2": "/twin?n=2",
		"set-0/test-3": "/unique",
		"set-0/test-4": "/unique?deep=1",
	}
	got := map[string][]int{}
	for _, id := range []string{"set-0/test-1", "set-0/test-2", "set-0/test-3", "set-0/test-4"} {
		rec := measure(t, k, a, id, paths[id])
		if rec["id"] != id {
			t.Fatalf("record %v: id %v, want %s", rec, rec["id"], id)
		}
		byFile := linesOfRecord(t, rec)
		if len(byFile) != 1 || len(byFile[file]) == 0 {
			t.Fatalf("%s ran %v, want lines of %s alone", id, byFile, file)
		}
		got[id] = byFile[file]
	}
	// Where a block starts and ends is cmd/cover's (Go 1.21 starts a
	// function's first block at its brace, 1.27 at its first statement), so
	// the lines are checked by what ran, not pinned whole: each test's lines
	// lie in its handler, and hold the statements it ran and none it did not.
	within := func(id string, from, to int) {
		for _, l := range got[id] {
			if l < from || l > to {
				t.Fatalf("%s ran line %d, outside its handler's lines %d-%d: %v", id, l, from, to, got)
			}
		}
	}
	holds := func(id string, want bool, lines ...int) {
		for _, l := range lines {
			if containsInt(got[id], l) != want {
				t.Fatalf("%s: line %d held %v, want %v: %v", id, l, !want, want, got)
			}
		}
	}
	if !reflect.DeepEqual(got["set-0/test-1"], got["set-0/test-2"]) {
		t.Fatalf("the twins ran different lines: %v", got)
	}
	within("set-0/test-1", tw, tw+3)
	holds("set-0/test-1", true, tw+1, tw+2)
	within("set-0/test-3", un, un+6)
	within("set-0/test-4", un, un+6)
	holds("set-0/test-3", true, un+1, un+5)
	holds("set-0/test-3", false, un+2, un+3)
	holds("set-0/test-4", true, un+1, un+2, un+3)
	holds("set-0/test-4", false, un+5)
}

// TestTheRecordKeepsTheSDKsShape pins the bytes of a record: one JSON object,
// its keys sorted (executedLinesByFile, then id), as every SDK release sent
// and every agent reads; an app with no measured line sends {} for its lines.
func TestTheRecordKeepsTheSDKsShape(t *testing.T) {
	for _, row := range []struct {
		lines map[string][]int
		want  string
	}{
		{map[string][]int{"a/b.go": {3, 4}}, `{"executedLinesByFile":{"a/b.go":[3,4]},"id":"set-0/test-1"}`},
		{map[string][]int{}, `{"executedLinesByFile":{},"id":"set-0/test-1"}`},
	} {
		b, err := encodeRecord("set-0/test-1", row.lines, nil, nil)
		if err != nil || string(b) != row.want {
			t.Fatalf("record %s (%v), want %s", b, err, row.want)
		}
	}
}

// TestTheHookIsNeverATestsCode builds the app with -coverpkg naming every
// package of the module, the hook too, and net/http, which serves each
// request, and checks a record names lines of the app alone: never the
// hook's, which runs at every END, nor a package outside the app's module.
func TestTheHookIsNeverATestsCode(t *testing.T) {
	k := newKeploySide(t)
	hook := hookCopy(t, k.sock, k.data)
	bin := buildByHand(t, hook, appModuleDir(t), "-cover", "-covermode=atomic", "-coverpkg="+appModule+"/...,net/http")
	a := start(t, bin)
	got := linesOfRecord(t, measure(t, k, a, "set-0/test-1", "/twin?n=1"))
	for file := range got {
		if !strings.HasPrefix(file, appModule+"/") || strings.Contains(file, "zz_keploy_cover") {
			t.Fatalf("a record names code that is not the app's: %v", got)
		}
	}
	if _, ok := got[appModule+"/main.go"]; !ok {
		t.Fatalf("the record holds no line of the app: %v", got)
	}
}

// TestAnAppThatCannotBeMeasuredSaysWhy builds the app without -cover, and
// with -covermode=set: each answers START IGNORED and says why, once.
func TestAnAppThatCannotBeMeasuredSaysWhy(t *testing.T) {
	for _, row := range []struct {
		name  string
		args  []string
		saysA string
	}{
		{name: "no -cover", args: nil, saysA: "built without -cover"},
		{name: "-covermode=set", args: []string{"-cover", "-covermode=set"}, saysA: "needs -covermode=atomic"},
	} {
		t.Run(row.name, func(t *testing.T) {
			k := newKeploySide(t)
			hook := hookCopy(t, k.sock, k.data)
			a := start(t, buildByHand(t, hook, appModuleDir(t), row.args...))
			for i := 0; i < 2; i++ {
				if got := send(t, k.sock, "START set-0/test-1"); got != replyIgnored {
					t.Fatalf("START: answer %q, want %q", got, replyIgnored)
				}
			}
			if got := send(t, k.sock, "END set-0/test-1"); got != replyIgnored {
				t.Fatalf("END: answer %q, want %q", got, replyIgnored)
			}
			// The log is whole once the app is gone and its stderr copied.
			a.stop()
			log := a.log()
			if strings.Count(log, "not measuring tests") != 1 || !strings.Contains(log, row.saysA) || strings.Contains(log, "END for") {
				t.Fatalf("the app's log says why %d times, want once, saying %q, and nothing of the END:\n%s", strings.Count(log, "not measuring tests"), row.saysA, log)
			}
		})
	}
}

// TestTheBuildStageRefusesWhatItCannotBuild pins its refusals: not at the
// module root, not package main, and a leftover of a killed build.
func TestTheBuildStageRefusesWhatItCannotBuild(t *testing.T) {
	goBin := needGo(t)
	script, err := filepath.Abs("keploy-go-cover-build.sh")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")}
	mod := appModuleDir(t)
	if err := os.MkdirAll(filepath.Join(mod, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "lib", "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name, dir, says string
		args            []string
		before          func(t *testing.T)
	}{
		{name: "not at the module root", dir: filepath.Join(mod, "lib"), says: "run it from the root of", args: []string{"."}},
		{name: "named by its files", dir: mod, says: "not by its files", args: []string{"main.go"}},
		{name: "not package main", dir: mod, says: "not package main", args: []string{"./lib"}},
		{name: "-o with no path", dir: mod, says: "-o needs a path", args: []string{"-o"}},
		{name: "an unknown flag", dir: mod, says: "unknown flag -x", args: []string{"-x", "."}},
		{name: "two packages", dir: mod, says: "one package at most", args: []string{".", "./lib"}},
		{name: "a pattern", dir: mod, says: "more than one package", args: []string{"./..."}},
		{name: "a standard library package", dir: mod, says: "standard library package", args: []string{"fmt"}},
		{name: "-o after --", dir: mod, says: "give -o before PKG", args: []string{".", "--", "-o", "x"}},
		{name: "-C after --", dir: mod, says: "instead of passing -C", args: []string{".", "--", "-C", "lib"}},
		{name: "-json after --", dir: mod, says: "-json is not taken", args: []string{".", "--", "-json"}},
		{name: "-covermode after --", dir: mod, says: "-covermode=set is not taken", args: []string{".", "--", "-covermode=set"}},
		{name: "-o with an empty path", dir: mod, says: "-o needs a path", args: []string{"-o", "", "."}},
		{name: "a leftover", dir: mod, says: "already there", args: []string{"."}, before: func(t *testing.T) {
			if err := os.Mkdir(filepath.Join(mod, "zz_keploy_cover"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(mod, "zz_keploy_cover")) })
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if row.before != nil {
				row.before(t)
			}
			out, err := run(t, row.dir, env, "sh", append([]string{script}, row.args...)...)
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.ExitCode() != 2 || !strings.Contains(out, row.says) {
				t.Fatalf("exit %v, want 2 saying %q:\n%s", err, row.says, out)
			}
		})
	}
	t.Run("-o=PATH before PKG", func(t *testing.T) {
		bin := filepath.Join(t.TempDir(), "app")
		if out, err := run(t, mod, env, "sh", script, "-o="+bin, "."); err != nil {
			t.Fatalf("-o=%s: %v\n%s", bin, err, out)
		}
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("-o= wrote no binary: %v", err)
		}
	})
	t.Run("a git whose answer is not one path", func(t *testing.T) {
		// A git that echoes an option it does not know answers with two
		// lines: the stage must use neither.
		stubDir := t.TempDir()
		stub := "#!/bin/sh\nif [ \"$1\" = rev-parse ]; then printf '%s\\n' --path-format=absolute .git/info/exclude; exit 0; fi\nexit 1\n"
		if err := os.WriteFile(filepath.Join(stubDir, "git"), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, mod)
		bin := filepath.Join(t.TempDir(), "app")
		stubEnv := []string{"PATH=" + stubDir + string(os.PathListSeparator) + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")}
		if out, err := run(t, mod, stubEnv, "sh", script, "-o", bin, "."); err != nil {
			t.Fatalf("with an old git: %v\n%s", err, out)
		}
		if after := snapshot(t, mod); !reflect.DeepEqual(before, after) {
			t.Fatalf("the stage left files with an old git:\nbefore %v\nafter  %v", before, after)
		}
	})
	t.Run("the script run by a link", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "build.sh")
		if err := os.Symlink(script, link); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, mod, env, "sh", link, ".")
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 || !strings.Contains(out, "not beside this script") {
			t.Fatalf("exit %v, want 2 saying the hook is not beside it:\n%s", err, out)
		}
	})
}

// TestTheHookIsStdlibOnlyAndRunsNothing pins what lets the build stage copy
// the package into any module and run it in a scratch image: one package, no
// import outside the standard library, no cgo, no os/exec and no unsafe.
func TestTheHookIsStdlibOnlyAndRunsNothing(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			t.Fatalf("cover holds a directory %s: the build stage copies one package", name)
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(imp, "/")
			if strings.Contains(first, ".") || imp == "C" || imp == "os/exec" || imp == "unsafe" {
				t.Fatalf("%s imports %s: the hook must be stdlib-only and run nothing", name, imp)
			}
		}
	}
}

// writeFiles writes files (path relative to dir: body) under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot is the bytes of each file under dir, by its path.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		out[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// stage runs keploy-go-cover-build.sh of hook in dir, with env and go build
// flags, and returns the binary and what the script said.
func stage(t *testing.T, hook, dir string, env []string, flags ...string) (bin, out string) {
	t.Helper()
	goBin := needGo(t)
	bin = filepath.Join(t.TempDir(), "app")
	env = append([]string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")}, env...)
	args := []string{filepath.Join(hook, "keploy-go-cover-build.sh"), "-o", bin, "."}
	if len(flags) > 0 {
		args = append(append(args, "--"), flags...)
	}
	out, err := run(t, dir, env, "sh", args...)
	if err != nil {
		t.Fatalf("keploy-go-cover-build.sh: %v\n%s", err, out)
	}
	return bin, out
}

// getBody is the body of a GET of path on a.
func getBody(t *testing.T, a app, path string) string {
	t.Helper()
	res, err := http.Get("http://" + a.addr + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTheBuildStageBuildsTheModulesAppsHave builds modules the stage meets
// in apps: one with no go line (language go1.16), one at go 1.16 whose
// dependency is vendored, and one in a go.work workspace. Each builds, its
// files are as they were (vendor/ and go.sum too), and its twin test is
// measured.
func TestTheBuildStageBuildsTheModulesAppsHave(t *testing.T) {
	// /dep runs the dependency's code at request time.
	depUse := "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\n\t\"example.com/dep\"\n)\n\nfunc init() {\n\thttp.HandleFunc(\"/dep\", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, dep.Hello()) })\n}\n"
	dep := map[string]string{"go.mod": "module example.com/dep\n", "dep.go": "package dep\n\nfunc Hello() string { return \"hello\" }\n"}
	for _, row := range []struct {
		name  string
		files map[string]string
		// app is the module's directory under the temp dir; work, a go.work
		// to build with, "" for none.
		app, work string
		setup     func(t *testing.T, root string)
		// depLines says whether /dep's test holds lines of example.com/dep:
		// "some" for a workspace module or one replaced by a directory, both
		// the app's own code; "" for a module with no /dep.
		depLines string
	}{
		{name: "no go line", app: ".", files: map[string]string{
			"go.mod": "module " + appModule + "\n", "main.go": appSource,
		}},
		{name: "go 1.16 with a vendored dependency replaced by a directory", app: ".", depLines: "some", files: map[string]string{
			"go.mod":     "module " + appModule + "\n\ngo 1.16\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n",
			"main.go":    appSource,
			"dep_use.go": depUse,
			"dep/go.mod": dep["go.mod"], "dep/dep.go": dep["dep.go"],
		}, setup: func(t *testing.T, root string) {
			if out, err := run(t, root, nil, needGo(t), "mod", "vendor"); err != nil {
				t.Fatalf("go mod vendor: %v\n%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(root, "vendor", "modules.txt")); err != nil {
				t.Fatalf("no vendor directory: %v", err)
			}
		}},
		{name: "a go.work workspace", app: "app", work: "go.work", depLines: "some", files: map[string]string{
			"go.work":        "go 1.21\n\nuse (\n\t./app\n\t./dep\n)\n",
			"app/go.mod":     "module " + appModule + "\n\ngo 1.21\n",
			"app/main.go":    appSource,
			"app/dep_use.go": depUse,
			"dep/go.mod":     dep["go.mod"], "dep/dep.go": dep["dep.go"],
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			k := newKeploySide(t)
			hook := hookCopy(t, k.sock, k.data)
			root := t.TempDir()
			writeFiles(t, root, row.files)
			if row.setup != nil {
				row.setup(t, root)
			}
			var env []string
			if row.work != "" {
				env = append(env, "GOWORK="+filepath.Join(root, row.work))
			}
			before := snapshot(t, root)
			bin, _ := stage(t, hook, filepath.Join(root, row.app), env)
			if after := snapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("the stage changed the module's files:\nbefore %v\nafter  %v", before, after)
			}
			a := start(t, bin)
			got := linesOfRecord(t, measure(t, k, a, "set-0/test-1", "/twin?n=1"))
			tw := lineOf(t, "func twin(")
			if lines := got[appModule+"/main.go"]; !containsInt(lines, tw+1) || !containsInt(lines, tw+2) {
				t.Fatalf("the twin ran %v, want lines %d and %d of main.go", got, tw+1, tw+2)
			}
			if row.depLines == "" {
				return
			}
			got = linesOfRecord(t, measure(t, k, a, "set-0/test-2", "/dep"))
			_, has := got["example.com/dep/dep.go"]
			if has != (row.depLines == "some") || len(got[appModule+"/dep_use.go"]) == 0 {
				t.Fatalf("/dep ran %v; want lines of dep_use.go, and of dep.go: %s", got, row.depLines)
			}
		})
	}
}

// TestTheBuildStageBuildsWithTheAppsFlags passes go build flags after --: a
// build tag that gates a handler and the package it alone imports, and an
// -ldflags value with a space. The tagged package is measured, and the
// version the flag stamps is the binary's.
func TestTheBuildStageBuildsWithTheAppsFlags(t *testing.T) {
	k := newKeploySide(t)
	hook := hookCopy(t, k.sock, k.data)
	root := appModuleDir(t)
	writeFiles(t, root, map[string]string{
		"version.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n)\n\nvar version = \"dev\"\n\nfunc init() {\n\thttp.HandleFunc(\"/version\", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, version) })\n}\n",
		"tagged.go":  "//go:build enterprise\n\npackage main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\n\t\"" + appModule + "/ent\"\n)\n\nfunc init() {\n\thttp.HandleFunc(\"/tagged\", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ent.Do()) })\n}\n",
		"ent/ent.go": "package ent\n\nfunc Do() string {\n\treturn \"ent\"\n}\n",
	})
	bin, out := stage(t, hook, root, nil, "-tags=enterprise", "-ldflags=-X main.version=1.2.3 -X main.unused=a")
	a := start(t, bin)
	if v := getBody(t, a, "/version"); v != "1.2.3" {
		t.Fatalf("the binary's version is %q, want 1.2.3: the -ldflags value was split\n%s", v, out)
	}
	got := linesOfRecord(t, measure(t, k, a, "set-0/test-1", "/tagged"))
	if len(got[appModule+"/ent/ent.go"]) == 0 {
		t.Fatalf("/tagged ran %v, want lines of the tagged package ent", got)
	}
}

// TestTheBuildStageTakesLdflagsFromTheEnvironment passes KEPLOY_GO_LDFLAGS,
// a value with spaces, as a Dockerfile's ENV or ARG does: it reaches the
// linker whole (-s -w strip the binary and the -X stamps its version).
func TestTheBuildStageTakesLdflagsFromTheEnvironment(t *testing.T) {
	k := newKeploySide(t)
	hook := hookCopy(t, k.sock, k.data)
	root := appModuleDir(t)
	writeFiles(t, root, map[string]string{
		"version.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n)\n\nvar version = \"dev\"\n\nfunc init() {\n\thttp.HandleFunc(\"/version\", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, version) })\n}\n",
	})
	bin, out := stage(t, hook, root, []string{"KEPLOY_GO_LDFLAGS=-s -w -X main.version=1.2.3"})
	if !strings.Contains(out, "-ldflags=-s -w -X main.version=1.2.3") {
		t.Fatalf("the stage did not pass KEPLOY_GO_LDFLAGS as one flag:\n%s", out)
	}
	a := start(t, bin)
	if v := getBody(t, a, "/version"); v != "1.2.3" {
		t.Fatalf("the binary's version is %q, want 1.2.3\n%s", v, out)
	}
	if got := linesOfRecord(t, measure(t, k, a, "set-0/test-1", "/twin?n=1")); len(got[appModule+"/main.go"]) == 0 {
		t.Fatalf("a stripped binary was not measured: %v", got)
	}
	goBin := needGo(t)
	out, err := run(t, root, []string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH"), "KEPLOY_GO_BUILD_FLAGS=-trimpath"},
		"sh", filepath.Join(hook, "keploy-go-cover-build.sh"), ".")
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 || !strings.Contains(out, "KEPLOY_GO_BUILD_FLAGS is not read") {
		t.Fatalf("KEPLOY_GO_BUILD_FLAGS: exit %v, want 2 saying it is not read:\n%s", err, out)
	}
}

// TestTheBuildStageKeepsAGitTreeClean builds a module that is a git
// checkout: the binary is stamped unmodified, as the production build of the
// same tree is, and git's exclude file is as it was.
func TestTheBuildStageKeepsAGitTreeClean(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	k := newKeploySide(t)
	hook := hookCopy(t, k.sock, k.data)
	root := appModuleDir(t)
	// The machine's own git config (signing, hooks, excludes) stays out.
	gitEnv := []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "app"},
	} {
		if out, err := run(t, root, gitEnv, gitBin, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	exclude := filepath.Join(root, ".git", "info", "exclude")
	// An exclude file whose last line has no newline: the stage's lines
	// must not join it.
	if err := os.WriteFile(exclude, []byte("*.tmp"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(exclude)
	bin, out := stage(t, hook, root, gitEnv)
	after, _ := os.ReadFile(exclude)
	if string(before) != string(after) {
		t.Fatalf("git's exclude file changed: %q -> %q", before, after)
	}
	info, err := run(t, root, nil, needGo(t), "version", "-m", bin)
	if err != nil {
		t.Fatalf("go version -m: %v\n%s", err, info)
	}
	if !strings.Contains(info, "vcs.modified=false") || strings.Contains(info, "+dirty") {
		t.Fatalf("the binary is stamped modified:\n%s\nstage:\n%s", info, out)
	}
	if out, err := run(t, root, gitEnv, gitBin, "status", "--porcelain"); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("the checkout is not clean after the stage (%v):\n%s", err, out)
	}
}

// TestTheBuildStageRunsFromAModuleReachedByALink runs the stage from the
// module's directory named by a symbolic link, as go names it then.
func TestTheBuildStageRunsFromAModuleReachedByALink(t *testing.T) {
	k := newKeploySide(t)
	hook := hookCopy(t, k.sock, k.data)
	root := appModuleDir(t)
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	bin, _ := stage(t, hook, link, []string{"PWD=" + link})
	a := start(t, bin)
	if got := linesOfRecord(t, measure(t, k, a, "set-0/test-1", "/twin?n=1")); len(got[appModule+"/main.go"]) == 0 {
		t.Fatalf("the twin ran %v", got)
	}
}

// fakeSDK is a module at the go-sdk's path holding package keploy, and, when
// withCover, package cover (this hook's copy at hook), as a go-sdk release
// before cover/ and one after it are.
func fakeSDK(t *testing.T, root, hook string, withCover bool) {
	t.Helper()
	files := map[string]string{
		"sdk/go.mod":           "module github.com/keploy/go-sdk/v3\n\ngo 1.21\n",
		"sdk/keploy/keploy.go": "package keploy\n",
	}
	if withCover {
		files["sdk/keploy/keploy.go"] = "package keploy\n\nimport _ \"github.com/keploy/go-sdk/v3/cover\"\n"
		entries, err := os.ReadDir(hook)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".go") {
				b, err := os.ReadFile(filepath.Join(hook, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				files["sdk/cover/"+e.Name()] = string(b)
			}
		}
	}
	writeFiles(t, root, files)
}

// TestTheBuildStageMeetsAnAppThatLinksTheSDK: an app that already imports
// the go-sdk's keploy package of a release with cover/ is built with -cover
// only, and measured once (one hook, not two); one of a release before
// cover/, whose hook needs the go command at run time, is refused.
func TestTheBuildStageMeetsAnAppThatLinksTheSDK(t *testing.T) {
	goMod := "module " + appModule + "\n\ngo 1.21\n\nrequire github.com/keploy/go-sdk/v3 v3.0.0\n\nreplace github.com/keploy/go-sdk/v3 => ./sdk\n"
	sdkUse := "package main\n\nimport _ \"github.com/keploy/go-sdk/v3/keploy\"\n"
	t.Run("a release with cover", func(t *testing.T) {
		k := newKeploySide(t)
		hook := hookCopy(t, k.sock, k.data)
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"go.mod": goMod, "main.go": appSource, "sdk_use.go": sdkUse})
		fakeSDK(t, root, hook, true)
		bin, out := stage(t, hook, root, nil)
		if !strings.Contains(out, "already links github.com/keploy/go-sdk/v3/cover") || strings.Contains(out, "adding the coverage hook") {
			t.Fatalf("the stage did not build the app's own hook alone:\n%s", out)
		}
		a := start(t, bin)
		got := linesOfRecord(t, measure(t, k, a, "set-0/test-1", "/twin?n=1"))
		if len(got[appModule+"/main.go"]) == 0 {
			t.Fatalf("the app was not measured: %v", got)
		}
	})
	t.Run("a release before cover, imported under a build tag", func(t *testing.T) {
		hook := hookCopy(t, "/nonexistent/c.sock", "/nonexistent/d.sock")
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"go.mod": goMod, "main.go": appSource, "sdk_use.go": "//go:build enterprise\n\n" + sdkUse})
		fakeSDK(t, root, hook, false)
		goBin := needGo(t)
		out, err := run(t, root, []string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")},
			"sh", filepath.Join(hook, "keploy-go-cover-build.sh"), ".", "--", "-tags=enterprise")
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 || !strings.Contains(out, "release before cover/") {
			t.Fatalf("exit %v, want 2 naming the old release:\n%s", err, out)
		}
	})
	t.Run("a package of a dependency", func(t *testing.T) {
		hook := hookCopy(t, "/nonexistent/c.sock", "/nonexistent/d.sock")
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"go.mod": goMod, "main.go": appSource, "sdk_use.go": sdkUse})
		fakeSDK(t, root, hook, true)
		goBin := needGo(t)
		out, err := run(t, root, []string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")},
			"sh", filepath.Join(hook, "keploy-go-cover-build.sh"), "github.com/keploy/go-sdk/v3/keploy")
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 || !strings.Contains(out, "a dependency") {
			t.Fatalf("exit %v, want 2 naming a dependency:\n%s", err, out)
		}
	})
	t.Run("a release before cover", func(t *testing.T) {
		hook := hookCopy(t, "/nonexistent/c.sock", "/nonexistent/d.sock")
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"go.mod": goMod, "main.go": appSource, "sdk_use.go": sdkUse})
		fakeSDK(t, root, hook, false)
		goBin := needGo(t)
		out, err := run(t, root, []string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")},
			"sh", filepath.Join(hook, "keploy-go-cover-build.sh"), ".")
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 || !strings.Contains(out, "release before cover/") {
			t.Fatalf("exit %v, want 2 naming the old release:\n%s", err, out)
		}
	})
}
