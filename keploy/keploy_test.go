package keploy

import (
	"os"
	"testing"
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
