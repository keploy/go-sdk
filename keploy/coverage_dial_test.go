//go:build linux || darwin
// +build linux darwin

package keploy

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// TestDialDataSocketWithRetry_DeliversWhenListenerAppearsLate guards the
// coverage-sync "could not connect to keploy data socket" flake: a one-shot
// net.Dial loses the report if the receiver's socket is briefly absent/refusing
// (ENOENT/ECONNREFUSED) under CPU starvation. The retry must connect once the
// listener appears.
func TestDialDataSocketWithRetry_DeliversWhenListenerAppearsLate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cov.sock")
	ready := make(chan net.Listener, 1)
	go func() {
		time.Sleep(200 * time.Millisecond) // socket absent for the first ~200ms
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Errorf("listen: %v", err)
			return
		}
		ready <- ln
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()

	conn, err := dialDataSocketWithRetry(path, 5*time.Second)
	if err != nil {
		t.Fatalf("expected retry to connect once the listener appeared, got: %v", err)
	}
	_ = conn.Close()
	ln := <-ready
	_ = ln.Close()
}

// TestDialDataSocketWithRetry_FailsWithinBudgetWhenNoListener confirms a socket
// that stays down still errors (no masking) and the retry respects its bounded
// budget (cannot hang past the ACK deadline).
func TestDialDataSocketWithRetry_FailsWithinBudgetWhenNoListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.sock")
	start := time.Now()
	conn, err := dialDataSocketWithRetry(path, 600*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected an error when no listener is present")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("dialDataSocketWithRetry exceeded its budget: took %v", elapsed)
	}
}
