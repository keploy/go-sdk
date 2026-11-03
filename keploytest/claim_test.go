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

// go test runs as many test binaries at once as its -p, from its command
// line, else GOFLAGS, else its default; a binary go test does not run runs
// alone.
func TestGoTestWorkers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		argv  []string
		flags string
		n     int
		from  string
	}{
		{"-p 1", []string{"/usr/local/go/bin/go", "test", "-p", "1", "-json", "./..."}, "", 1, "-p 1"},
		{"-p=1 after the packages", []string{"go", "test", "./...", "-p=1"}, "", 1, "-p 1"},
		{"-p 4", []string{"go", "test", "-p", "4", "./..."}, "", 4, "-p 4"},
		{"GOFLAGS", []string{"go", "test", "./..."}, "-p=1", 1, "GOFLAGS -p=1"},
		{"GOFLAGS -p=2", []string{"go", "test", "./..."}, "-p=2", 2, "GOFLAGS -p=2"},
		{"-p after -args is the binary's", []string{"go", "test", "-p", "1", "./...", "-args", "-p", "8"}, "", 1, "-p 1"},
		{"-C before test", []string{"go", "-C", "/src", "test", "-p", "3", "./..."}, "", 3, "-p 3"},
		{"not go test", []string{"/bin/sh", "-c", "x"}, "-p=4", 1, ""},
		{"not go test, GOFLAGS -exec", []string{"/bin/sh", "-c", "x"}, "-exec=/tmp/wrap -p=4", 1, ""},
		{"go build", []string{"go", "build", "-p", "4"}, "", 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeProc(t, "77", tc.argv...)
			w := goTestWorkers(func(k string) string {
				if k == "GOFLAGS" {
					return tc.flags
				}
				return ""
			}, 77)
			if w != (workers{n: tc.n, from: tc.from}) {
				t.Fatalf("got %+v, want %d from %q", w, tc.n, tc.from)
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
func TestGoTestWorkersFindsGoTestAboveAnExecWrapper(t *testing.T) {
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
	if w := goTestWorkers(func(string) string { return "" }, 77); w != (workers{n: 4, from: "-p 4"}) {
		t.Fatalf("got %+v, want the -p 4 of the go test above the wrapper", w)
	}
	if w := goTestWorkers(func(string) string { return "" }, 88); w != (workers{n: 1}) {
		t.Fatalf("a binary another test binary started was counted by the go test above it: %+v", w)
	}
}

// A -p set with go env -w is in cmd/go's env file, which cmd/go does not
// hand the binary: it is read from there.
func TestGoTestWorkersReadsGoEnvW(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	fakeProc(t, "77", "go", "test", "./...")
	for _, tc := range []struct {
		file string
		want workers
	}{
		{"GOFLAGS=-p=1\n", workers{n: 1, from: "go env GOFLAGS -p=1"}},
		{"GOTOOLCHAIN=local\nGOFLAGS=-mod=mod -p=3\n", workers{n: 3, from: "go env GOFLAGS -p=3"}},
	} {
		if err := os.WriteFile(envFile, []byte(tc.file), 0o644); err != nil {
			t.Fatal(err)
		}
		w := goTestWorkers(func(k string) string {
			if k == "GOENV" {
				return envFile
			}
			return ""
		}, 77)
		if w != tc.want {
			t.Fatalf("%q: got %+v, want %+v", tc.file, w, tc.want)
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

// A test process of a go test that runs two at once claims the run as one
// of two test workers. An agent that takes it as a worker joins it; one
// that refuses the count (too_many_workers) refuses it with its own words
// and where the count came from, and records that refusal itself; one that
// predates test workers takes it as the run's only test process, and the
// process refuses itself and tells the agent, so keploy's verdict names it.
// The tests fail with the refusal whether or not the agent heard; when it
// did not, they say so.
func TestATestWorkersClaim(t *testing.T) {
	t.Setenv(envTestPID, "") // claimRun exports it
	t.Setenv(envTestClaimed, "")
	keepParallel(t)
	fakeProc(t, "77", "go", "test", "-p", "2", "./...")
	alone := "keploy: this keploy runs one test process at a time, and go test runs up to 2 test processes at once (-p 2), each a test worker of keploy's run: " +
		"add -p 1 (go test -p 1 -json ./...), or use a keploy that runs test workers at once"
	for _, tc := range []struct {
		name          string
		claim         string
		claimStatus   int
		refusalStatus int
		refusalBody   string
		want          string
		calls         []string
	}{
		{"taken as a worker", `{"v":1,"instance":"1"}`, 200, 0, "", "", []string{pathInstance}},
		{"refused by the agent", `{"error":"too_many_workers","message":"process 4242 declared 2 test workers, but this keploy runs one test worker at a time","remedy":"run one worker"}`, 422, 0, "",
			"keploy: process 4242 declared 2 test workers, but this keploy runs one test worker at a time (run one worker); go test runs up to 2 test processes at once (-p 2), each a test worker of keploy's run",
			[]string{pathInstance}},
		{"taken alone, the agent told", `{"v":1,"instance":"run"}`, 200, 200, `{"v":1,"status":"ok"}`, alone, []string{pathInstance, pathRefusal}},
		{"taken alone, an agent without /agent/refusal", `{"v":1,"instance":"run"}`, 200, 404, "404 page not found", alone, []string{pathInstance, pathRefusal}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envTestPID, "")
			t.Setenv(envTestClaimed, "")
			var mu sync.Mutex
			var calls []string
			var claimed, sent map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("%s without the token", r.URL.Path)
				}
				calls = append(calls, r.URL.Path)
				raw, _ := io.ReadAll(r.Body)
				if r.URL.Path == pathRefusal {
					_ = json.Unmarshal(raw, &sent)
					w.WriteHeader(tc.refusalStatus)
					_, _ = io.WriteString(w, tc.refusalBody)
					return
				}
				_ = json.Unmarshal(raw, &claimed)
				w.WriteHeader(tc.claimStatus)
				_, _ = io.WriteString(w, tc.claim)
			}))
			t.Cleanup(srv.Close)
			// The project is the root, so this process's directory is in it.
			env := map[string]string{envAgent: srv.URL, envToken: "tok", envProject: "/"}
			j := claimRun(func(k string) string { return env[k] }, []string{"x.test"}, 4242, 77)
			mu.Lock()
			defer mu.Unlock()
			if fmt.Sprint(calls) != fmt.Sprint(tc.calls) {
				t.Fatalf("calls %v, want %v", calls, tc.calls)
			}
			if claimed["workers"] != float64(2) {
				t.Fatalf("claimed %v, want 2 workers", claimed)
			}
			if tc.want == "" {
				if j.mode != active {
					t.Fatalf("got %+v, want the run joined", j)
				}
				return
			}
			if j.mode != refused || !strings.HasPrefix(j.refusal, tc.want) {
				t.Fatalf("got %+v, want refused with %q", j, tc.want)
			}
			told := tc.refusalStatus == 200 || tc.refusalStatus == 0
			if (j.refusal == tc.want) != told {
				t.Fatalf("refusal %q: told the agent %v, want %v", j.refusal, j.refusal == tc.want, told)
			}
			if !told && !strings.Contains(j.refusal, "keploy's agent was not told") {
				t.Fatalf("refusal %q does not say keploy was not told", j.refusal)
			}
			if tc.refusalStatus != 0 && (sent["code"] != codeOneTestProcess || !strings.Contains(fmt.Sprint(sent["message"]), "up to 2 test processes at once (-p 2)")) {
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

// keepParallel restores this process's -test.parallel after a test whose
// claimRun joins in-process, which sets keploy's.
func keepParallel(t *testing.T) {
	t.Helper()
	testing.Init()
	f := flag.Lookup("test.parallel")
	prev := f.Value.String()
	t.Cleanup(func() { _ = f.Value.Set(prev) })
}

// A test binary keploy's Go launcher (keploy-go-exec) started has its claim
// already: the launcher made it for this process (KEPLOY_TEST_CLAIMED names
// it) before exec'ing it. The package claims nothing and refuses no go test
// the launcher checked, though go test runs with -p 2 here and the run is
// frozen, which only the launcher applies.
func TestAnInheritedClaimIsNotMadeAgain(t *testing.T) {
	// A claimRun that set the markers would leave them in this process.
	t.Setenv(envTestPID, "")
	t.Setenv(envTestClaimed, "")
	keepParallel(t)
	fakeProc(t, "77", "go", "test", "-p", "2", "./...")
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"v":1,"instance":"run","timeFile":"/tmp/kclk1/t"}`)
	}))
	t.Cleanup(srv.Close)
	// Only KEPLOY_TEST_CLAIMED says the claim was inherited; the processes the
	// binary starts are its members whatever KEPLOY_TEST_PID it was given.
	env := map[string]string{envAgent: srv.URL, envToken: "tok", envProject: t.TempDir(), envTestClaimed: "4242"}
	j := claimRun(func(k string) string { return env[k] }, []string{"x.test"}, 4242, 77)
	if got := os.Getenv(envTestPID); got != "4242" {
		t.Fatalf("KEPLOY_TEST_PID=%q, want this pid: the processes it starts are members", got)
	}
	if j.mode != active || j.url != srv.URL || j.token != "tok" {
		t.Fatalf("got %+v, want the run joined through the launcher's claim", j)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("called the agent %v: the launcher's claim is this process's", calls)
	}
}

// A binary that claims the run itself marks the processes it starts as
// members (KEPLOY_TEST_PID) before it claims, so a helper of a refused binary
// stays inert too; it marks the run as claimed by its pid
// (KEPLOY_TEST_CLAIMED) only once the agent took the claim, so an image a
// refused binary execs claims again, and is refused again. A claim whose
// answer freezes the clock, which only keploy's Go launcher applies, is
// refused, and the refusal is told to the agent, so keploy's verdict names it.
func TestTheMarkersOfAClaimedAndOfARefusedBinary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		claim    string
		claimed  string
		calls    string
		refusal  string
		joinedAs mode
	}{
		{"taken", `{"v":1,"instance":"run","timeFile":""}`, "4242", pathInstance, "", active},
		{"a frozen clock", `{"v":1,"instance":"run","timeFile":"/tmp/kclk1/t"}`, "", pathInstance + " " + pathRefusal, "keploy: this keploy run freezes its tests' clock", refused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envTestPID, "")
			t.Setenv(envTestClaimed, "")
			keepParallel(t)
			fakeProc(t, "77", "go", "test", "-p", "1", "./...")
			var mu sync.Mutex
			var calls []string
			var sent map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, r.URL.Path)
				if r.URL.Path == pathRefusal {
					raw, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(raw, &sent)
					_, _ = io.WriteString(w, `{"v":1,"status":"ok"}`)
					return
				}
				_, _ = io.WriteString(w, tc.claim)
			}))
			t.Cleanup(srv.Close)
			// The project is the root, so this process's directory is in it.
			env := map[string]string{envAgent: srv.URL, envToken: "tok", envProject: "/"}
			j := claimRun(func(k string) string { return env[k] }, []string{"x.test"}, 4242, 77)
			if j.mode != tc.joinedAs || !strings.HasPrefix(j.refusal, tc.refusal) || strings.Contains(j.refusal, "was not told") {
				t.Fatalf("got %+v", j)
			}
			if got := os.Getenv(envTestPID); got != "4242" {
				t.Fatalf("KEPLOY_TEST_PID=%q, want this pid: the processes it starts are members, refused or not", got)
			}
			if got := os.Getenv(envTestClaimed); got != tc.claimed {
				t.Fatalf("KEPLOY_TEST_CLAIMED=%q, want %q", got, tc.claimed)
			}
			mu.Lock()
			defer mu.Unlock()
			if strings.Join(calls, " ") != tc.calls {
				t.Fatalf("calls %v, want %s", calls, tc.calls)
			}
			if tc.joinedAs == refused && (sent["code"] != "clock_not_frozen" || !strings.Contains(fmt.Sprint(sent["remedy"]), "keploy-go-exec <test binary> -test.v")) {
				t.Fatalf("told the agent %v", sent)
			}
		})
	}
}

// A test process that runs alone (go test -p 1) and that the agent refuses
// fails with the agent's own words: no worker count to account for.
func TestAnAgentsRefusalOfALoneTestProcessIsItsOwnWords(t *testing.T) {
	t.Setenv(envTestPID, "")
	t.Setenv(envTestClaimed, "")
	keepParallel(t)
	fakeProc(t, "77", "go", "test", "-p", "1", "./...")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = io.WriteString(w, `{"error":"one_test_process_at_a_time","message":"process 4242 asked to run tests while process 9 still runs this keploy run's tests","remedy":"run one test process at a time"}`)
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{envAgent: srv.URL, envToken: "tok", envProject: "/"}
	j := claimRun(func(k string) string { return env[k] }, []string{"x.test"}, 4242, 77)
	const want = "keploy: process 4242 asked to run tests while process 9 still runs this keploy run's tests (run one test process at a time)"
	if j.mode != refused || j.refusal != want {
		t.Fatalf("got %+v, want refused with %q", j, want)
	}
}
