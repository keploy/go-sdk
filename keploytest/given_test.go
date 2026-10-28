package keploytest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// envGiven names the given TestChildGivenOne gives.
const envGiven = "KEPLOYTEST_GIVEN"

// newApp is a fixture like a suite's own: it makes the value and gives it,
// through testing.TB, so the given's source is this line.
func newApp(t testing.TB, n int) string {
	t.Helper()
	return Given(t, "app", "[E2E] app-176160960012345678"+strconv.Itoa(n)) //at:fixture
}

// TestChildGiven gives values the way a suite does: whole, again under the
// same name, through a fixture, and from a sequential and a parallel subtest
// started with Run, which give to the top-level test.
func TestChildGiven(t *testing.T) {
	childOnly(t)
	defer T(t)()
	v := Given(t, "nonce", "1761609600123456789") //at:top
	fmt.Println("GIVEN-RETURNED " + v)
	fmt.Println("GIVEN-RETURNED " + newApp(t, 0))
	Run(t, "seq", func(t *testing.T) {
		Given(t, "nonce", "1761609600123456790") //at:seq
	})
	Run(t, "par", func(t *testing.T) {
		Parallel(t)
		newApp(t, 1)
	})
}

// TestChildGivenOne gives the value named by KEPLOYTEST_GIVEN, whose answer
// the parent scripts.
// TestChildGivenTP gives once Parallel has begun it.
func TestChildGivenTP(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Parallel(t)
	Given(t, "nonce", "1761609600123456799") //at:tp
}

func TestChildGivenOne(t *testing.T) {
	childOnly(t)
	defer T(t)()
	Given(t, os.Getenv(envGiven), "v-1761609600123456789") //at:one
	fmt.Println(bodyRan)
}

func TestChildGivenBeforeParallel(t *testing.T) {
	childOnly(t)
	defer TP(t)()
	Given(t, "early", "early-1761609600123456789")
	Parallel(t)
	fmt.Println(bodyRan)
}

func TestChildGivenNoLine(t *testing.T) {
	childOnly(t)
	Given(t, "app", "app-1761609600123456789")
	fmt.Println(bodyRan)
}

func TestChildGivenRawRun(t *testing.T) {
	childOnly(t)
	defer T(t)()
	t.Run("raw", func(t *testing.T) {
		Given(t, "app", "app-1761609600123456789")
		fmt.Println(bodyRan)
	})
}

func TestChildGivenOtherGoroutine(t *testing.T) {
	childOnly(t)
	defer T(t)()
	done := make(chan string)
	go func() { done <- Given(t, "app", "app-1761609600123456789") }()
	fmt.Println("GIVEN-RETURNED " + <-done)
}

func BenchmarkChildGiven(b *testing.B) {
	if os.Getenv(envChild) != "1" {
		b.Skip("runs only as a child of this package's tests")
	}
	Given(b, "app", "app-1761609600123456789")
	fmt.Println(bodyRan)
}

// givenEnv is keploy's environment for a child run in this package's
// directory, with the module's root as the project, so a given's source is
// keploytest/given_test.go:<line>.
func givenEnv(t *testing.T, a *fakeAgent) (map[string]string, string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{envAgent: a.url, envProject: filepath.Dir(dir)}, dir
}

// at is the source of the line of given_test.go marked //at:<mark>.
func at(t *testing.T, mark string) string {
	t.Helper()
	src, err := os.ReadFile("given_test.go")
	if err != nil {
		t.Fatal(err)
	}
	found := ""
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasSuffix(line, "//at:"+mark) {
			if found != "" {
				t.Fatalf("two lines are marked %s", mark)
			}
			found = "keploytest/given_test.go:" + strconv.Itoa(i+1)
		}
	}
	if found == "" {
		t.Fatalf("no line is marked %s", mark)
	}
	return found
}

// Each value goes to the agent while the test is open, with where the test
// gave it relative to the project, and Given returns it; a subtest gives to
// its top-level test, and the agent numbers a name given again.
func TestAGivenGoesToTheOpenTest(t *testing.T) {
	a := newFakeAgent(t)
	env, dir := givenEnv(t, a)
	c := runChild(t, env, dir, "^TestChildGiven(TP)?$")
	c.want(t, "TestChildGiven", "PASS", "GIVEN-RETURNED 1761609600123456789", "GIVEN-RETURNED [E2E] app-1761609600123456780")
	c.want(t, "TestChildGiven/seq", "PASS")
	c.want(t, "TestChildGiven/par", "PASS")
	wantCalls(t, a,
		"begin TestChildGiven",
		"given nonce=1761609600123456789 @"+at(t, "top"),
		"given app=[E2E] app-1761609600123456780 @"+at(t, "fixture"),
		"given nonce=1761609600123456790 @"+at(t, "seq"),
		"given app=[E2E] app-1761609600123456781 @"+at(t, "fixture"),
		"end TestChildGiven passed",
		"begin TestChildGivenTP",
		"given nonce=1761609600123456799 @"+at(t, "tp"),
		"end TestChildGivenTP passed")
}

// Without keploy, Given returns the value and calls nothing.
func TestGivenIsInertWithoutKeploy(t *testing.T) {
	a := newFakeAgent(t)
	env := a.active()
	env[envAgent] = "off"
	c := runChild(t, env, "", "^TestChildGiven(NoLine)?$")
	c.want(t, "TestChildGiven", "PASS", "GIVEN-RETURNED 1761609600123456789", "GIVEN-RETURNED [E2E] app-1761609600123456780")
	c.want(t, "TestChildGivenNoLine", "PASS", bodyRan)
	wantCalls(t, a)
}

// A test that cannot give a value fails, saying why, and nothing is given:
// a refused run, no keploytest line, a subtest t.Run started, another
// goroutine, a TP test before Parallel, and a benchmark.
func TestAGivenOutsideAnOpenTestFails(t *testing.T) {
	a := newFakeAgent(t)
	c := runChild(t, map[string]string{envAgent: a.url}, "", "^TestChildGivenNoLine$")
	c.want(t, "TestChildGivenNoLine", "FAIL", "did not name the project", envProject)
	if strings.Contains(c.out, bodyRan) {
		t.Errorf("a refused run went on past Given\n%s", c.out)
	}

	for _, tc := range []struct {
		test, says string
		calls      []string
	}{
		{"TestChildGivenNoLine", "keploy: TestChildGivenNoLine has no keploytest line, so it cannot give app; run keploy mock codemod go", nil},
		{"TestChildGivenRawRun", "keploy: TestChildGivenRawRun/raw was started by t.Run, so it cannot give app; start it with keploytest.Run",
			[]string{"begin TestChildGivenRawRun", "end TestChildGivenRawRun failed"}},
		{"TestChildGivenBeforeParallel", "keploy: TestChildGivenBeforeParallel gives early before keploytest.Parallel, while it has not begun; give it after keploytest.Parallel(t)", nil},
	} {
		a := newFakeAgent(t)
		c := runChild(t, a.active(), "", "^"+tc.test+"$")
		c.want(t, tc.test, "FAIL", tc.says)
		if strings.Contains(c.out, bodyRan) {
			t.Errorf("%s went on past Given\n%s", tc.test, c.out)
		}
		wantCalls(t, a, tc.calls...)
	}

	a = newFakeAgent(t)
	c = runChild(t, a.active(), "", "^TestChildGivenOtherGoroutine$")
	c.want(t, "TestChildGivenOtherGoroutine", "FAIL",
		"keploy: keploytest.Given(app) from another goroutine of TestChildGivenOtherGoroutine; give values from the test's own goroutine",
		"GIVEN-RETURNED app-1761609600123456789")
	wantCalls(t, a, "begin TestChildGivenOtherGoroutine", "end TestChildGivenOtherGoroutine failed")

	a = newFakeAgent(t)
	c = runChild(t, a.active(), "", "^$", "-test.bench=^BenchmarkChildGiven$", "-test.benchtime=1x")
	for _, s := range []string{"--- FAIL: BenchmarkChildGiven", "keploy: keploytest.Given(app) needs the *testing.T of a test that starts with a keploytest line, and BenchmarkChildGiven is not a test"} {
		if !strings.Contains(c.out, s) {
			t.Errorf("the benchmark's output does not say %q\n%s", s, c.out)
		}
	}
	if strings.Contains(c.out, bodyRan) || c.code != 1 {
		t.Errorf("the benchmark went on past Given or exited %d\n%s", c.code, c.out)
	}
	wantCalls(t, a)
}

// An answer to a given the package cannot trust fails the test with what
// went wrong; only a 404, an agent without the route, says to upgrade.
func TestAGivenTheAgentRefusesFailsTheTest(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  reply
		says string
	}{
		{"refused", reply{400, `{"error":"bad_request","message":"a given's name is 1 to 64 letters, digits, '_' or '-', not \"a b\"","remedy":"name it with 1 to 64 letters, digits, '_' or '-'"}`},
			`keploy: a given's name is 1 to 64 letters, digits, '_' or '-', not "a b" (name it with 1 to 64 letters, digits, '_' or '-')`},
		{"old", reply{404, "404 page not found\n"},
			"keploy: this keploy agent does not take a value a test gave (it answered 404 Not Found to /agent/scope/given); use a keploy and a keploy-go-sdk of the same release"},
		{"broken", reply{500, "boom"},
			`keploy: the keploy agent answered 500 Internal Server Error to /agent/scope/given: "boom"`},
		{"novar", reply{200, `{"v":1,"status":"ok"}`},
			"keploy: the keploy agent answered the given novar without the variable it holds it as"},
		{"busy", reply{200, `{"v":1,"status":"busy","variable":"given:busy"}`},
			`keploy: the keploy agent answered the given busy with status "busy"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newFakeAgent(t)
			a.onGiven(tc.name, tc.rep)
			env, dir := givenEnv(t, a)
			env[envGiven] = tc.name
			c := runChild(t, env, dir, "^TestChildGivenOne$")
			c.want(t, "TestChildGivenOne", "FAIL", tc.says)
			if strings.Contains(c.out, bodyRan) || (tc.name != "old" && strings.Contains(c.out, "does not take a value")) {
				t.Errorf("wrong failure\n%s", c.out)
			}
			wantCalls(t, a, "begin TestChildGivenOne", "given "+tc.name+"=v-1761609600123456789 @"+at(t, "one"), "end TestChildGivenOne failed")
		})
	}
}

func TestGivenSource(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	link := filepath.Join(t.TempDir(), "proj")
	if err := os.Symlink(proj, link); err != nil {
		t.Fatal(err)
	}
	long := "/" + strings.Repeat("d/", maxSource/2) + "x_test.go"
	for _, tc := range []struct {
		project, file string
		want          string
	}{
		{proj, filepath.Join(proj, "tests", "e2e", "apps_test.go"), "tests/e2e/apps_test.go:20"},
		{link, filepath.Join(proj, "tests", "apps_test.go"), "tests/apps_test.go:20"},
		{proj, filepath.Join(filepath.Dir(proj), "other", "apps_test.go"), filepath.ToSlash(filepath.Join(filepath.Dir(proj), "other", "apps_test.go")) + ":20"},
		{proj, "github.com/acme/api/tests/apps_test.go", "github.com/acme/api/tests/apps_test.go:20"},
		{"", filepath.Join(wd, "apps_test.go"), filepath.ToSlash(filepath.Join(wd, "apps_test.go")) + ":20"},
		{proj, long, "x_test.go:20"},
		{proj, "", ""},
	} {
		if got := givenSource(tc.project, tc.file, 20); got != tc.want {
			t.Errorf("givenSource(%q, %q) = %q, want %q", tc.project, tc.file, got, tc.want)
		}
	}
}
