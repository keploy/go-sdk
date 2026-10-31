package keploytest

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// maxSource is the longest source the agent takes (keploy's
// models.MaxGivenSource): a longer file:line is sent as its base name.
const maxSource = 1 << 10

// Given tells keploy that the test chose value itself and hands it to the
// application, which sends it on to a dependency: a nonce made from the
// test's own clock, a name it made up, a token it signed. It returns value,
// so it wraps the place the test makes it:
//
//	name := keploytest.Given(t, "app", fmt.Sprintf("[E2E] app-%d", time.Now().UnixNano()))
//
// Such a value differs on every run, and no clock read of the application's
// matches it: without Given, keploy can only learn it as noise, unchecked, or
// fail the call that carries it. With Given, keploy holds it as the variable
// given:<name> of the open test (given:<name>#2, #3 and on for a name the
// test gives again). In a record, keploy follows the value into the
// dependency calls that carry it whole, and each such mock's variables say
// where the test gave it (this call's file and line, relative to the project)
// and how to undo it. Each replay checks that the application sends on the
// value the test gives then: a call that carries another value is one the
// recording cannot answer, and fails the test. For an HTTP call, keploy's
// run summary names the given (given:<name>), never a value, and says why:
// the call carried another value, or a value of another JSON type, at the
// part of the call it names (body, a header, a path segment), or the test
// did not give the value before the call.
//
// Give a distinctive value, such as a UnixNano nonce or a prefixed name, and
// give the value the application sends on whole. keploy finds a given by its
// exact text, at any length. A call value that holds it inside a longer value
// and changed with it ("app-<nonce>" for the given <nonce>) is refused, naming
// the given (a red, never a false green), so a short given that stands in
// another value by chance ("14" in "slot14", then 15 in "slot15") can be
// refused too. A longer value that held the given's text but did not change
// with it ("slot14", then "slot99") learns as it would with no given. A bare
// Unix time in seconds or milliseconds, or an RFC 3339 time at the second,
// cannot be told from the application's own clock read of that moment in the
// same call, and is taken as the given's. A value the application makes from
// a given without keeping its text (a hash, a cut, a reformat) is not
// followed: keploy learns it as it would with no given, which can be noise,
// unchecked.
//
// keploy follows a given fully into HTTP calls only, and not yet into MongoDB
// or PostgreSQL calls: a MongoDB field that holds a given whole is followed,
// and a PostgreSQL statement value that is a given is refused, naming it. A
// given inside a longer value in a MongoDB or PostgreSQL call is not followed
// yet, and a MongoDB call's miss of a given does not name it yet.
//
// name is 1 to 64 letters, digits, '_' or '-'; give a number as its decimal
// text (strconv.Itoa). Call Given inside the test, from the test's own
// goroutine, after its keploytest line (in a TP test, after Parallel). A
// subtest started with Run gives to its top-level test, which keploy has
// open. A value a fixture makes is given by the test that sends it; keploy
// takes no given outside a test. A recording made before the test gave the
// value holds no given for it: that test checks nothing for the value, and
// when it fails, its reason says to re-record it.
//
// Given fails the test with keploy's words when keploy refuses the value, and
// when the test cannot give it (no keploytest line, a subtest started with
// t.Run). Without keploy, Given returns value and does nothing.
func Given(t testing.TB, name, value string) string {
	t.Helper()
	switch j := join(); j.mode {
	case inert:
		return value
	case refused:
		t.Fatal(j.refusal)
	}
	_, file, line, _ := runtime.Caller(1)
	tt, isT := t.(*testing.T)
	if !isT {
		t.Fatalf("keploy: keploytest.Given(%s) needs the *testing.T of a test that starts with a keploytest line, and %s is not a test", name, t.Name())
	}
	mu.Lock()
	st := state[tt]
	// A T test's body runs once T has begun it, and a TP test's once
	// Parallel has; Run refuses a subtest before that. Only a TP test before
	// Parallel is not open.
	waiting := st != nil && st.kind == kindTP && !st.paralleled
	mu.Unlock()
	switch {
	case st == nil && strings.Contains(tt.Name(), "/"):
		t.Fatalf("keploy: %s was started by t.Run, so it cannot give %s; start it with keploytest.Run", tt.Name(), name)
	case st == nil:
		t.Fatalf("keploy: %s has no keploytest line, so it cannot give %s; run keploy mock codemod go", tt.Name(), name)
	case st.gid != gid():
		// t.Fatal ends only the goroutine that calls it.
		t.Errorf("keploy: keploytest.Given(%s) from another goroutine of %s; give values from the test's own goroutine", name, tt.Name())
		return value
	case waiting:
		t.Fatalf("keploy: %s gives %s before keploytest.Parallel, while it has not begun; give it after keploytest.Parallel(t)", tt.Name(), name)
	}
	if _, err := agent().given(name, value, givenSource(os.Getenv(envProject), file, line)); err != nil {
		t.Fatal(err.Error())
	}
	return value
}

// givenSource is where a test gave a value, as file:line: relative to the
// project when the file is inside it, else as Go names it.
func givenSource(project, file string, line int) string {
	if file == "" {
		return ""
	}
	if project != "" {
		// A path Go names relative (a -trimpath build) has no relative
		// path from the project's, and is kept.
		rel, err := filepath.Rel(realPath(project), realPath(file))
		if err == nil && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			file = rel
		}
	}
	file = filepath.ToSlash(file)
	s := file + ":" + strconv.Itoa(line)
	if len(s) > maxSource {
		s = filepath.Base(file) + ":" + strconv.Itoa(line)
	}
	return s
}
