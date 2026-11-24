//go:build linux || darwin

package cover

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestEndCost measures what an END costs an app of many counters: the time
// from END sent to its answer, which covers reading the counters, decoding
// them and sending the record, for a test that ran one function and one that
// ran every function. A measurement, not a check: it runs only with
// KEPLOY_COVER_ENDCOST set to the counter counts to measure ("5000,50000").
func TestEndCost(t *testing.T) {
	sizes := os.Getenv("KEPLOY_COVER_ENDCOST")
	if sizes == "" {
		t.Skip("set KEPLOY_COVER_ENDCOST=5000,50000 to measure")
	}
	goBin := needGo(t)
	for _, s := range strings.Split(sizes, ",") {
		counters, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Fatal(err)
		}
		// Each function holds 20 blocks: 10 ifs, each its line and its body,
		// and one more after them.
		funcs := counters / 21
		k := newKeploySide(t)
		hook := hookCopy(t, k.sock, k.data)
		mod := appModuleDir(t)
		var b strings.Builder
		b.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n)\n\n")
		for i := 0; i < funcs; i++ {
			fmt.Fprintf(&b, "func g%d(x int) int {\n", i)
			for j := 0; j < 10; j++ {
				fmt.Fprintf(&b, "\tif x > %d {\n\t\tx += %d\n\t}\n", j, j+1)
			}
			b.WriteString("\treturn x\n}\n\n")
		}
		b.WriteString("func init() {\n\thttp.HandleFunc(\"/one\", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, g0(20)) })\n")
		b.WriteString("\thttp.HandleFunc(\"/all\", func(w http.ResponseWriter, r *http.Request) {\n\t\ts := 0\n")
		for i := 0; i < funcs; i++ {
			fmt.Fprintf(&b, "\t\ts += g%d(20)\n", i)
		}
		b.WriteString("\t\tfmt.Fprint(w, s)\n\t})\n}\n")
		if err := os.WriteFile(filepath.Join(mod, "gen.go"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(t.TempDir(), "app")
		if out, err := run(t, mod, []string{"PATH=" + filepath.Dir(goBin) + string(os.PathListSeparator) + os.Getenv("PATH")},
			"sh", filepath.Join(hook, "keploy-go-cover-build.sh"), "-o", bin, "."); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
		a := start(t, bin)
		for _, path := range []string{"/one", "/all"} {
			var ends, starts []time.Duration
			lines := 0
			for i := 0; i < 21; i++ {
				id := fmt.Sprintf("set-0/test-%d", i)
				// A START after an END reads the gap between them: as much
				// work as an END over the app's counters.
				sBegan := time.Now()
				if got := send(t, k.sock, "START "+id); got != replyStartUnasked {
					t.Fatalf("START: %q\n%s", got, a.log())
				}
				starts = append(starts, time.Since(sBegan))
				get(t, a, path)
				began := time.Now()
				if got := send(t, k.sock, "END "+id); got != replyTaken {
					t.Fatalf("END: %q\n%s", got, a.log())
				}
				ends = append(ends, time.Since(began))
				var rec map[string]any
				select {
				case rec = <-k.records:
				case <-time.After(30 * time.Second):
					t.Fatalf("END sent no record\n%s", a.log())
				}
				lines = 0
				for _, l := range linesOfRecord(t, rec) {
					lines += len(l)
				}
			}
			// The first END is apart: the first of its kind after the build.
			first := ends[0]
			rest := append([]time.Duration(nil), ends[1:]...)
			sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
			srest := append([]time.Duration(nil), starts[1:]...)
			sort.Slice(srest, func(i, j int) bool { return srest[i] < srest[j] })
			t.Logf("ENDCOST counters=%d funcs=%d path=%s lines=%d first=%v p50=%v p95=%v start_p50=%v start_p95=%v",
				funcs*21, funcs, path, lines, first, rest[len(rest)/2], rest[len(rest)*95/100], srest[len(srest)/2], srest[len(srest)*95/100])
		}
	}
}
