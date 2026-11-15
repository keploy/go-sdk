//go:build windows

package keploytest

import (
	"os"
	"testing"
	"time"
)

// A path none of whose directories resolves, on a drive that does not
// exist, comes back as it is, and at once.
func TestRealPathOfAPathOnNoDriveIsThePathItself(t *testing.T) {
	drive := ""
	for c := 'Z'; c >= 'D'; c-- {
		if _, err := os.Stat(string(c) + `:\`); err != nil {
			drive = string(c) + ":"
			break
		}
	}
	if drive == "" {
		t.Skip("every drive letter from D to Z is in use")
	}
	p := drive + `\keploy\gone\apps_test.go`
	got := make(chan string, 1)
	go func() { got <- realPath(p) }()
	select {
	case g := <-got:
		if g != p {
			t.Fatalf("realPath(%q) = %q, want the path itself", p, g)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("realPath(%q) did not return in 10s", p)
	}
}
