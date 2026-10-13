package keploytest

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeProc writes a /proc with process pid's command line.
func fakeProc(t *testing.T, pid string, argv ...string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, pid, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
}

// go test may run two test binaries at once unless -p is 1, from its command
// line, else GOFLAGS, else its default; a binary go test does not run is not
// checked.
func TestOneTestProcess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		argv   []string
		flags  string
		refuse string
	}{
		{"-p 1", []string{"/usr/local/go/bin/go", "test", "-p", "1", "-json", "./..."}, "", ""},
		{"-p=1 after the packages", []string{"go", "test", "./...", "-p=1"}, "", ""},
		{"-p 4", []string{"go", "test", "-p", "4", "./..."}, "", "up to 4 at once (-p 4)"},
		{"GOFLAGS", []string{"go", "test", "./..."}, "-p=1", ""},
		{"GOFLAGS -p=2", []string{"go", "test", "./..."}, "-p=2", "GOFLAGS -p=2"},
		{"-p after -args is the binary's", []string{"go", "test", "-p", "1", "./...", "-args", "-p", "8"}, "", ""},
		{"-C before test", []string{"go", "-C", "/src", "test", "-p", "1", "./..."}, "", ""},
		{"not go test", []string{"/bin/sh", "-c", "x"}, "", ""},
		{"go build", []string{"go", "build"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeProc(t, "77", tc.argv...)
			r := oneTestProcess(func(k string) string {
				if k == "GOFLAGS" {
					return tc.flags
				}
				return ""
			}, 77)
			switch {
			case tc.refuse == "" && r != nil:
				t.Fatalf("refused: %s", r.text())
			case tc.refuse != "" && (r == nil || !strings.Contains(r.text(), tc.refuse)):
				t.Fatalf("got %+v, want a refusal saying %q", r, tc.refuse)
			}
			if r != nil && !strings.Contains(r.text(), "go test -p 1") {
				t.Errorf("the refusal does not name the fix: %s", r.text())
			}
		})
	}
}

// go test's default -p is GOMAXPROCS: refused when above 1.
func TestTheDefaultPIsGOMAXPROCS(t *testing.T) {
	p, from := effectiveP([]string{"./..."}, func(k string) string {
		if k == "GOENV" {
			return "off"
		}
		return ""
	})
	if p != runtime.GOMAXPROCS(0) || !strings.Contains(from, "GOMAXPROCS") {
		t.Fatalf("p %d from %q", p, from)
	}
}

// A go test -exec wrapper sits between cmd/go and the binary: cmd/go is the
// nearest go test among the binary's ancestors.
func TestOneTestProcessFindsGoTestAboveAnExecWrapper(t *testing.T) {
	root := t.TempDir()
	write := func(pid int, ppid int, argv ...string) {
		t.Helper()
		dir := filepath.Join(root, fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (%s) S %d 1 1 0 -1", pid, filepath.Base(argv[0]), ppid)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(77, 66, "/tmp/wrap (x)", "/tmp/go-build/x.test")
	write(66, 55, "go", "test", "-exec", "/tmp/wrap (x)", "-p", "4", "./...")
	// A test binary that another test binary started: the go test above
	// runs that other binary, not this one.
	write(88, 99, "/tmp/go-build/parent.test", "-test.run=X")
	write(99, 55, "go", "test", "-p", "4", "./...")
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
	r := oneTestProcess(func(string) string { return "" }, 77)
	if r == nil || !strings.Contains(r.text(), "-p 4") {
		t.Fatalf("got %+v, want the -p 4 of the go test above the wrapper refused", r)
	}
	if r := oneTestProcess(func(string) string { return "" }, 88); r != nil {
		t.Fatalf("a binary another test binary started was judged by the go test above it: %s", r.text())
	}
}

// A -p set with go env -w is in cmd/go's env file, which cmd/go does not
// hand the binary: it is read from there.
func TestOneTestProcessReadsGoEnvW(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	fakeProc(t, "77", "go", "test", "./...")
	for _, tc := range []struct{ file, refuse string }{
		{"GOFLAGS=-p=1\n", ""},
		{"GOTOOLCHAIN=local\nGOFLAGS=-mod=mod -p=3\n", "go env GOFLAGS -p=3"},
	} {
		if err := os.WriteFile(envFile, []byte(tc.file), 0o644); err != nil {
			t.Fatal(err)
		}
		r := oneTestProcess(func(k string) string {
			if k == "GOENV" {
				return envFile
			}
			return ""
		}, 77)
		switch {
		case tc.refuse == "" && r != nil:
			t.Fatalf("%q refused: %s", tc.file, r.text())
		case tc.refuse != "" && (r == nil || !strings.Contains(r.text(), tc.refuse)):
			t.Fatalf("%q: got %+v, want a refusal saying %q", tc.file, r, tc.refuse)
		}
	}
}

func TestPackageKey(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "orders")
	if err := os.Mkdir(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(pkg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	key, dir, err := packageKey(root)
	if err != nil || key != "orders" || dir != pkg {
		t.Fatalf("key %q dir %q err %v", key, dir, err)
	}
	if _, _, err := packageKey(""); err == nil || !strings.Contains(err.Error(), envProject) {
		t.Fatalf("a run without a project: %v", err)
	}
	if _, _, err := packageKey(filepath.Join(root, "elsewhere")); err == nil || !strings.Contains(err.Error(), "outside the keploy project") {
		t.Fatalf("a package outside the project: %v", err)
	}
}

func TestAListingRunsInert(t *testing.T) {
	got := claimRun(func(k string) string {
		if k == envAgent {
			return "http://127.0.0.1:1"
		}
		return ""
	}, []string{"x.test", "-test.list=."}, 7, 1)
	if got.mode != inert {
		t.Fatalf("got %+v", got)
	}
}

// A go test that would run two test binaries at once is refused before the
// claim, and the refusal is told to keploy's agent, so the run's verdict
// names it. An agent that does not take it (an older keploy) leaves the
// refusal as it was, saying keploy was not told.
func TestARefusedTestProcessTellsTheAgent(t *testing.T) {
	t.Setenv(envTestPID, "") // claimRun exports it
	fakeProc(t, "77", "go", "test", "-p", "2", "./...")
	want := "keploy: keploy runs one test process at a time in this release, and go test runs up to 2 at once (-p 2): " +
		"add -p 1 (go test -p 1 -json ./...); parallel test workers arrive in a later keploy release"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		told   bool
	}{
		{"the agent takes it", 200, `{"v":1,"status":"ok"}`, true},
		{"an agent without /agent/refusal", 404, "404 page not found", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			var sent map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, r.URL.Path+" "+r.Header.Get("Authorization"))
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &sent)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			env := map[string]string{envAgent: srv.URL, envToken: "tok", envProject: t.TempDir()}
			j := claimRun(func(k string) string { return env[k] }, []string{"x.test"}, 4242, 77)
			if j.mode != refused || !strings.HasPrefix(j.refusal, want) {
				t.Fatalf("got %+v, want refused with %q", j, want)
			}
			if told := j.refusal == want; told != tc.told {
				t.Fatalf("refusal %q: told the agent %v, want %v", j.refusal, told, tc.told)
			}
			if !tc.told && !strings.Contains(j.refusal, "keploy's agent was not told") {
				t.Fatalf("refusal %q does not say keploy was not told", j.refusal)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) != 1 || calls[0] != pathRefusal+" Bearer tok" {
				t.Fatalf("calls %q, want one %s with the token, and no claim", calls, pathRefusal)
			}
			if sent["code"] != codeOneTestProcess || sent["remedy"] != "add -p 1 (go test -p 1 -json ./...); parallel test workers arrive in a later keploy release" ||
				!strings.Contains(fmt.Sprint(sent["message"]), "up to 2 at once (-p 2)") {
				t.Fatalf("sent %v", sent)
			}
		})
	}
}

// Without -v or -json a skip would be invisible; shuffling and fuzzing are
// not judged.
func TestTestFlagsRefusal(t *testing.T) {
	set := func(name, v string) {
		t.Helper()
		f := flag.Lookup(name)
		old := f.Value.String()
		if err := f.Value.Set(v); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Value.Set(old) })
	}
	set("test.v", "true")
	if why := testFlagsRefusal(); why != "" {
		t.Fatalf("-v refused: %s", why)
	}
	set("test.shuffle", "on")
	if why := testFlagsRefusal(); !strings.Contains(why, "-shuffle") {
		t.Fatalf("shuffle: %q", why)
	}
	set("test.shuffle", "off")
	set("test.fuzz", "FuzzX")
	if why := testFlagsRefusal(); !strings.Contains(why, "-fuzz") {
		t.Fatalf("fuzz: %q", why)
	}
	set("test.fuzz", "")
	set("test.v", "false")
	if why := testFlagsRefusal(); !strings.Contains(why, "-v or -json") {
		t.Fatalf("no -v: %q", why)
	}
}
