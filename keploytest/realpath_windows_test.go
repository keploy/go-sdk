//go:build windows

package keploytest

import (
	"os"
	"testing"
	"time"
)

// A path on a drive that does not exist comes back as it is, and at once:
// its directories do not resolve, and the drive's root is answered as
// itself (filepath.EvalSymlinks reads no disk for a volume's root).
func TestRealPathOfAPathOnNoDriveIsThePathItself(t *testing.T) {
	p := missingDrive(t) + `\keploy\gone\apps_test.go`
	if got := realPathAtOnce(t, p); got != p {
		t.Fatalf("realPath(%q) = %q, want the path itself", p, got)
	}
}

// A path none of whose directories resolves comes back as it is, and at
// once. One that filepath.Abs refuses (a NUL in it), relative to a drive
// that does not exist, is walked as given up to that drive's current
// directory ("Q:."), which does not resolve either and is its own
// directory: the walk ends there, with the path itself.
func TestRealPathOfAPathNoDirectoryOfWhichResolvesIsThePathItself(t *testing.T) {
	p := missingDrive(t) + "keploy\x00gone"
	if got := realPathAtOnce(t, p); got != p {
		t.Fatalf("realPath(%q) = %q, want the path itself", p, got)
	}
}

// missingDrive is a drive letter, with its colon, that names no drive.
func missingDrive(t *testing.T) string {
	t.Helper()
	for c := 'Z'; c >= 'D'; c-- {
		if _, err := os.Stat(string(c) + `:\`); err != nil {
			return string(c) + ":"
		}
	}
	t.Skip("every drive letter from D to Z is in use")
	return ""
}

// realPathAtOnce is realPath(p), failing the row if it takes 10s.
func realPathAtOnce(t *testing.T, p string) string {
	t.Helper()
	got := make(chan string, 1)
	go func() { got <- realPath(p) }()
	select {
	case g := <-got:
		return g
	case <-time.After(10 * time.Second):
		t.Fatalf("realPath(%q) did not return in 10s", p)
		return ""
	}
}
