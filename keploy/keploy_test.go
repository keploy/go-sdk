package keploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/coverage"
	"strings"
	"testing"
	"time"
)

func TestTempDirPattern(t *testing.T) {
	tests := []struct {
		name   string
		testID string
		want   string
	}{
		{"set and case", "test-set-0/test-2", "keploy-coverage-test-2-"},
		{"no slash", "test-2", "keploy-coverage-test-2-"},
		{"slash in case name", "test-set-0/a/b", "keploy-coverage-a_b-"},
		{"backslash in case name", `test-set-0/a\b`, "keploy-coverage-a_b-"},
		{"empty", "", "keploy-coverage--"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tempDirPattern(tt.testID); got != tt.want {
				t.Errorf("tempDirPattern(%q) = %q, want %q", tt.testID, got, tt.want)
			}
		})
	}
}

func TestSocketDir(t *testing.T) {
	if got := socketDir(""); got != "/tmp" {
		t.Errorf(`socketDir("") = %q, want "/tmp"`, got)
	}
	if got := socketDir("/keploy-sockets"); got != "/keploy-sockets" {
		t.Errorf(`socketDir("/keploy-sockets") = %q, want "/keploy-sockets"`, got)
	}
}

// Every pattern must be accepted by os.MkdirTemp; a raw "a/b" case name made
// it fail with "pattern contains path separator".
func TestTempDirPatternWorksWithMkdirTemp(t *testing.T) {
	for _, testID := range []string{"test-set-0/test-2", "test-2", "test-set-0/a/b", `test-set-0/a\b`} {
		dir, err := os.MkdirTemp(t.TempDir(), tempDirPattern(testID))
		if err != nil {
			t.Fatalf("MkdirTemp with pattern for %q: %v", testID, err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("temp dir for %q not created: %v", testID, err)
		}
	}
}

// A normal record must look exactly as before: no "reason" key.
func TestCoverageRecordWithoutReason(t *testing.T) {
	data, err := json.Marshal(coverageRecord{ID: "set-0/test-1", ExecutedLinesByFile: map[string][]int{"a.go": {3}}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "reason") || !strings.Contains(got, `"executedLinesByFile":{"a.go":[3]}`) {
		t.Fatalf("record JSON = %s", got)
	}
}

func TestEndWithoutStartSendsReason(t *testing.T) {
	records := listenData(t)
	setCurrentTestID(t, "")

	if reply := sendCommand(t, "END set-0/test-1"); reply != "ACK\n" {
		t.Fatalf("reply = %q, want ACK", reply)
	}
	rec := receive(t, records)
	if rec.ID != "set-0/test-1" || rec.Reason == nil || rec.Reason.Code != reasonNoStart {
		t.Fatalf("record = %+v, want a %s reason for set-0/test-1", rec, reasonNoStart)
	}
}

func TestMismatchedEndSendsReason(t *testing.T) {
	records := listenData(t)
	setCurrentTestID(t, "set-0/test-0")

	if reply := sendCommand(t, "END set-0/test-1"); reply != "ACK\n" {
		t.Fatalf("reply = %q, want ACK", reply)
	}
	rec := receive(t, records)
	if rec.ID != "set-0/test-1" || rec.Reason == nil || rec.Reason.Code != reasonEndMismatch {
		t.Fatalf("record = %+v, want an %s reason for set-0/test-1", rec, reasonEndMismatch)
	}
	if got := getCurrentTestID(); got != "set-0/test-0" {
		t.Fatalf("currentTestID = %q, want it unchanged", got)
	}
}

func TestFailedDumpSendsReason(t *testing.T) {
	if coverage.ClearCounters() == nil {
		t.Skip("test binary built with -covermode=atomic, so the dump would succeed")
	}
	records := listenData(t)
	setCurrentTestID(t, "set-0/test-2")

	if reply := sendCommand(t, "END set-0/test-2"); reply != "ACK\n" {
		t.Fatalf("reply = %q, want ACK", reply)
	}
	rec := receive(t, records)
	if rec.ID != "set-0/test-2" || rec.Reason == nil || rec.Reason.Code != reasonDumpFailed {
		t.Fatalf("record = %+v, want a %s reason for set-0/test-2", rec, reasonDumpFailed)
	}
	if got := getCurrentTestID(); got != "" {
		t.Fatalf("currentTestID = %q, want it reset", got)
	}
}

func TestCovdataCommandPrefersKeployCovdata(t *testing.T) {
	dir := t.TempDir()
	setCoverageDir(t, dir)
	tool := filepath.Join(dir, "keploy-covdata-"+runtime.GOARCH)
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, err := covdataCommand(context.Background(), "in", "out")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != tool || strings.Join(cmd.Args[1:], " ") != "textfmt -i=in -o=out" {
		t.Fatalf("command = %s %v, want %s textfmt -i=in -o=out", cmd.Path, cmd.Args, tool)
	}
}

func TestCovdataCommandFallsBackToGo(t *testing.T) {
	setCoverageDir(t, t.TempDir())
	bin := t.TempDir()
	goTool := filepath.Join(bin, "go")
	if err := os.WriteFile(goTool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	cmd, err := covdataCommand(context.Background(), "in", "out")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != goTool || strings.Join(cmd.Args[1:], " ") != "tool covdata textfmt -i=in -o=out" {
		t.Fatalf("command = %s %v, want go tool covdata textfmt", cmd.Path, cmd.Args)
	}
}

func TestCovdataCommandMissing(t *testing.T) {
	setCoverageDir(t, t.TempDir())
	t.Setenv("PATH", t.TempDir())

	if _, err := covdataCommand(context.Background(), "in", "out"); !errors.Is(err, errCovdataMissing) {
		t.Fatalf("err = %v, want errCovdataMissing", err)
	}
}

// Keploy's covdata is there but cannot start (e.g. /tmp is mounted noexec):
// 'go tool covdata' converts the data instead, and is used from then on.
func TestCovdataFallsBackToGoWhenKeployCovdataCannotStart(t *testing.T) {
	covDir := writeCovdata(t)
	dir := t.TempDir()
	setCoverageDir(t, dir)
	setUseGoCovdata(t, false)
	tool := filepath.Join(dir, "keploy-covdata-"+runtime.GOARCH)
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := processCoverageProfilesUsingCovdata(covDir)
	if err != nil {
		t.Fatalf("err = %v, want 'go tool covdata' to convert the data", err)
	}
	if len(got.executedLines) == 0 {
		t.Fatal("no executed lines: the data was not converted")
	}

	// The next test must not try Keploy's covdata again. Make it one that
	// starts, leaves a mark and fails.
	mark := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\ntouch "+mark+"\nexit 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := processCoverageProfilesUsingCovdata(covDir); err != nil {
		t.Fatalf("second test: err = %v, want 'go tool covdata' to convert the data", err)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("Keploy's covdata was tried again after it could not start")
	}
}

// Keploy's covdata started and exited non-zero: its error is returned, and
// 'go tool covdata' is not tried.
func TestCovdataNoFallbackWhenKeployCovdataFails(t *testing.T) {
	covDir := writeCovdata(t)
	dir := t.TempDir()
	setCoverageDir(t, dir)
	setUseGoCovdata(t, false)
	tool := filepath.Join(dir, "keploy-covdata-"+runtime.GOARCH)
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := processCoverageProfilesUsingCovdata(covDir)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want the exit error of Keploy's covdata", err)
	}
	if useGoCovdata {
		t.Fatal("useGoCovdata was set, want Keploy's covdata kept")
	}
}

func TestFileKey(t *testing.T) {
	const mod = "example.com/app"
	tests := []struct {
		name, fileName, modulePath, want string
		ok                               bool
	}{
		{"main module file", "example.com/app/handlers/user.go", mod, "handlers/user.go", true},
		{"main module root file", "example.com/app/main.go", mod, "main.go", true},
		{"module with the same prefix", "example.com/apptools/x.go", mod, "", false},
		{"other module", "github.com/lib/pq/conn.go", mod, "", false},
		{"absolute name, no module", "/src/app/main.go", "command-line-arguments", "/src/app/main.go", true},
		{"no build info", "example.com/app/main.go", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := fileKey(tt.fileName, tt.modulePath)
			if got != tt.want || ok != tt.ok {
				t.Errorf("fileKey(%q, %q) = %q, %v; want %q, %v", tt.fileName, tt.modulePath, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// sampleProfile is a covdata text profile as `go tool covdata textfmt` writes
// it: every block of the build, count 0 included.
const sampleProfile = `mode: atomic
example.com/app/handlers/hello.go:6.2,6.40 1 1
example.com/app/handlers/hello.go:6.40,9.3 2 0
example.com/app/handlers/hello.go:10.2,10.48 1 1
example.com/app/main.go:10.13,13.2 2 1
github.com/lib/x/x.go:1.1,2.2 1 1
`

func TestParseProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.txt")
	if err := os.WriteFile(path, []byte(sampleProfile), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := parseProfiles(path, "example.com/app")
	if err != nil {
		t.Fatal(err)
	}
	wantLines := map[string][]int{"handlers/hello.go": {6, 10}, "main.go": {10, 11, 12, 13}}
	if !reflect.DeepEqual(got.executedLines, wantLines) {
		t.Errorf("executedLines = %v, want %v", got.executedLines, wantLines)
	}
	if n := len(got.blocks["handlers/hello.go"]); n != 3 {
		t.Errorf("hello.go has %d blocks, want 3 (the block that did not run is kept)", n)
	}
	if _, ok := got.blocks["github.com/lib/x/x.go"]; ok {
		t.Error("a file of another module was kept")
	}

	wantBlocks := map[string][]codeBlock{
		"handlers/hello.go": {{6, 2, 6, 40, 1}, {10, 2, 10, 48, 1}},
		"main.go":           {{10, 13, 13, 2, 2}},
	}
	if blocks := got.codeBlocks(true); !reflect.DeepEqual(blocks, wantBlocks) {
		t.Errorf("codeBlocks(true) = %v, want %v", blocks, wantBlocks)
	}

	wantAll := map[string][]codeBlock{
		"handlers/hello.go": {{6, 2, 6, 40, 1}, {6, 40, 9, 3, 2}, {10, 2, 10, 48, 1}},
		"main.go":           {{10, 13, 13, 2, 2}},
	}
	if blocks := got.codeBlocks(false); !reflect.DeepEqual(blocks, wantAll) {
		t.Errorf("codeBlocks(false) = %v, want %v", blocks, wantAll)
	}

	if totals := got.totals(); totals != (statementTotals{Statements: 6, CoveredStatements: 4}) {
		t.Errorf("totals() = %+v, want 6 statements, 4 covered", totals)
	}
}

func TestTestSetOf(t *testing.T) {
	for id, want := range map[string]string{"test-set-0/test-2": "test-set-0", "a/b/c": "a", "test-2": ""} {
		if got := testSetOf(id); got != want {
			t.Errorf("testSetOf(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestMetaHash(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"covcounters.abc.1.2", "covmeta.5f3e9a"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := metaHash(dir); got != "5f3e9a" {
		t.Errorf("metaHash = %q, want 5f3e9a", got)
	}
	if got := metaHash(t.TempDir()); got != "" {
		t.Errorf("metaHash of an empty dir = %q, want empty", got)
	}
}

// Blocks go out as plain arrays, next to the old lines field.
func TestCoverageRecordWithBlocks(t *testing.T) {
	data, err := json.Marshal(coverageRecord{
		ID:                   "set-0/test-1",
		ExecutedLinesByFile:  map[string][]int{"a.go": {3}},
		ExecutedBlocksByFile: map[string][]codeBlock{"a.go": {{3, 2, 3, 20, 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"set-0/test-1","executedLinesByFile":{"a.go":[3]},"executedBlocksByFile":{"a.go":[[3,2,3,20,1]]}}`
	if string(data) != want {
		t.Fatalf("record JSON = %s, want %s", data, want)
	}
}

func TestMainModulePath(t *testing.T) {
	if got := mainModulePath(); got != "github.com/keploy/go-sdk/v3" {
		t.Errorf("mainModulePath() = %q, want the test binary's module", got)
	}
}

func setCoverageDir(t *testing.T, dir string) {
	t.Helper()
	old := coverageDir
	coverageDir = dir
	t.Cleanup(func() { coverageDir = old })
}

func setUseGoCovdata(t *testing.T, v bool) {
	t.Helper()
	old := useGoCovdata
	useGoCovdata = v
	t.Cleanup(func() { useGoCovdata = old })
}

// writeCovdata builds a small program with -cover, runs it, and returns the
// folder with its binary coverage data.
func writeCovdata(t *testing.T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("'go' is not on PATH")
	}
	src, covDir := t.TempDir(), t.TempDir()
	app := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln()\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command(goTool, "build", "-cover", "-o", app, "main.go")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -cover: %v\n%s", err, out)
	}
	run := exec.Command(app)
	run.Env = append(os.Environ(), "GOCOVERDIR="+covDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("running the covered program: %v\n%s", err, out)
	}
	return covDir
}

// listenData points dataSocketPath at a fresh socket and returns the records
// sent to it.
func listenData(t *testing.T) <-chan coverageRecord {
	t.Helper()
	// A short folder: a unix socket path must stay under ~107 bytes.
	dir, err := os.MkdirTemp("", "gsdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	old := dataSocketPath
	dataSocketPath = path
	t.Cleanup(func() { dataSocketPath = old })

	records := make(chan coverageRecord, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var rec coverageRecord
			if err := json.NewDecoder(conn).Decode(&rec); err == nil {
				records <- rec
			}
			_ = conn.Close()
		}
	}()
	return records
}

// sendCommand runs one control command through handleControlRequest and
// returns the app's reply.
func sendCommand(t *testing.T, command string) string {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		handleControlRequest(server)
		close(done)
	}()
	if _, err := client.Write([]byte(command + "\n")); err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(client)
	<-done
	return string(reply)
}

func receive(t *testing.T, records <-chan coverageRecord) coverageRecord {
	t.Helper()
	select {
	case rec := <-records:
		return rec
	case <-time.After(2 * time.Second):
		t.Fatal("no record arrived on the data socket")
		return coverageRecord{}
	}
}

func setCurrentTestID(t *testing.T, id string) {
	t.Helper()
	controlMu.Lock()
	currentTestID = id
	controlMu.Unlock()
	t.Cleanup(func() {
		controlMu.Lock()
		currentTestID = ""
		controlMu.Unlock()
	})
}

func getCurrentTestID() string {
	controlMu.Lock()
	defer controlMu.Unlock()
	return currentTestID
}
