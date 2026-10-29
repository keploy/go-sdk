package keploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
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

func setCoverageDir(t *testing.T, dir string) {
	t.Helper()
	old := coverageDir
	coverageDir = dir
	t.Cleanup(func() { coverageDir = old })
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
