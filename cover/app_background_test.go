//go:build linux || darwin

package cover

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// workerSource is an app-owned file of the test app: a ticker that runs
// shared, which the /shared handler runs too, while no test runs.
const workerSource = `package main

import (
	"fmt"
	"net/http"
	"time"
)

var sink int

func shared(n int) int {
	return n*2 + 1
}

func sharedHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, shared(3))
}

func init() {
	http.HandleFunc("/shared", sharedHandler)
	go func() {
		for range time.Tick(20 * time.Millisecond) {
			sink += shared(1)
		}
	}()
}
`

// TestTheAppsBetweenTestsWorkIsSentAndMarksTheTestsItMeets drives a real
// -cover build as Keploy's agent does: a START answers bg=- until the agent
// asks, then bg=<its token>; the gap between two tests comes as a BACKGROUND
// record naming both, of the app's go1 build, holding the ticker's lines; and
// a test that runs the ticker's shared code after that gap is sent
// coverage-partial, the test before it not.
func TestTheAppsBetweenTestsWorkIsSentAndMarksTheTestsItMeets(t *testing.T) {
	goBin := needGo(t)
	k := newKeploySide(t)
	hook := hookCopy(t, k.sock, k.data)
	mod := appModuleDir(t)
	if err := os.WriteFile(filepath.Join(mod, "worker.go"), []byte(workerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "app")
	if out, err := run(t, mod, []string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")},
		"sh", filepath.Join(hook, "keploy-go-cover-build.sh"), "-o", bin, "."); err != nil {
		t.Fatalf("keploy-go-cover-build.sh: %v\n%s", err, out)
	}
	a := start(t, bin)
	worker := appModule + "/worker.go"

	// test-1 runs before any gap (a sanity check: no gap is seen yet).
	first := measure(t, k, a, "set-0/test-1", "/shared")
	if _, partial := first["contaminated"]; partial {
		t.Fatalf("the first test, before any gap, is partial: %v", first)
	}
	if _, has := first["overlapLinesByFile"]; has {
		t.Fatalf("the first test, before any gap, carried overlapLinesByFile: %v", first)
	}
	if got := send(t, k.sock, backgroundAsk+" r1"); got != replyTaken {
		t.Fatalf("BACKGROUND: answer %q, want %q\n%s", got, replyTaken, a.log())
	}
	time.Sleep(200 * time.Millisecond) // a gap the ticker runs in
	if got := send(t, k.sock, "START set-0/test-2"); got != replyStartAskedBy("r1") {
		t.Fatalf("START after the ask: answer %q, want %q\n%s", got, replyStartAskedBy("r1"), a.log())
	}
	var bg map[string]any
	select {
	case bg = <-k.records:
	case <-time.After(10 * time.Second):
		t.Fatalf("the gap sent no BACKGROUND record\n%s", a.log())
	}
	if bg["kind"] != "background" || bg["after"] != "set-0/test-1" || bg["before"] != "set-0/test-2" || bg["v"] != float64(1) || bg["lost"] != float64(0) {
		t.Fatalf("background record %v", bg)
	}
	if b, _ := bg["build"].(string); !strings.HasPrefix(b, "go1-") || len(b) != len("go1-")+32 {
		t.Fatalf("background record's build %q, want go1- and 32 hex", bg["build"])
	}
	if gap := linesOfRecord(t, bg); len(gap[worker]) == 0 {
		t.Fatalf("the gap's lines %v hold none of %s, the ticker's", gap, worker)
	}
	get(t, a, "/shared")
	if got := send(t, k.sock, "END set-0/test-2"); got != replyTaken {
		t.Fatalf("END: answer %q\n%s", got, a.log())
	}
	var second map[string]any
	select {
	case second = <-k.records:
	case <-time.After(10 * time.Second):
		t.Fatalf("END sent no record\n%s", a.log())
	}
	marks, _ := second["contaminated"].([]any)
	if len(marks) == 0 || marks[0] != "between-tests:"+worker {
		t.Fatalf("test-2 ran the ticker's shared code after the gap and is not partial on %s: %v", worker, second)
	}
	overlapRaw, ok := second["overlapLinesByFile"].(map[string]any)
	if !ok {
		t.Fatalf("test-2 missing overlapLinesByFile map: %v", second)
	}
	gotOverlap := map[string][]int{}
	for file, raw := range overlapRaw {
		arr, ok := raw.([]any)
		if !ok {
			t.Fatalf("overlapLinesByFile[%q] = %T, want []any", file, raw)
		}
		var ls []int
		for _, v := range arr {
			f, ok := v.(float64)
			if !ok {
				t.Fatalf("overlapLinesByFile[%q] element = %T, want float64", file, v)
			}
			ls = append(ls, int(f))
		}
		gotOverlap[file] = ls
	}
	// Because only one gap ran before test-2, overlapLinesByFile must equal
	// sorted(executed ∩ gap) with no extra files or lines.
	gapLines := linesOfRecord(t, bg)
	execLines := linesOfRecord(t, second)
	wantOverlap := map[string][]int{}
	for file, gl := range gapLines {
		el := execLines[file]
		if len(el) == 0 {
			continue
		}
		gs := map[int]bool{}
		for _, l := range gl {
			gs[l] = true
		}
		var hit []int
		for _, l := range el {
			if gs[l] {
				hit = append(hit, l)
			}
		}
		if len(hit) > 0 {
			sort.Ints(hit)
			wantOverlap[file] = hit
		}
	}
	if len(wantOverlap[worker]) == 0 {
		t.Fatalf("expected non-empty intersection for %s (gap=%v exec=%v)", worker, gapLines[worker], execLines[worker])
	}
	if !reflect.DeepEqual(gotOverlap, wantOverlap) {
		t.Fatalf("test-2 overlapLinesByFile = %v, want exact intersection %v", gotOverlap, wantOverlap)
	}
}
