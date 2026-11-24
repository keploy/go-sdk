//go:build linux || darwin

package cover

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
)

// dumpSource is an app that writes its own coverage meta-data and, after f
// ran once on its x > 1 branch, its counters, to the files its arguments
// name.
const dumpSource = `package main

import (
	"bytes"
	"os"
	"runtime/coverage"
)

func f(x int) int {
	if x > 1 {
		return x * 2
	}
	return x
}

func main() {
	var m, c bytes.Buffer
	if err := coverage.WriteMeta(&m); err != nil {
		panic(err)
	}
	_ = f(len(os.Args))
	if err := coverage.WriteCounters(&c); err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[1], m.Bytes(), 0o644); err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[2], c.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
`

// dumpedOnce holds what dumped read, built once for every test.
var dumpedOnce struct {
	sync.Once
	meta, counters []byte
	failed         bool
}

// dumped returns the meta-data and counter-data dumpSource writes, built
// with -cover -covermode=atomic and run once for all the tests.
func dumped(t testing.TB) (metaData, counterData []byte) {
	t.Helper()
	dumpedOnce.Do(func() {
		dumpedOnce.failed = true
		dumpedOnce.meta, dumpedOnce.counters = dumpOnce(t)
		dumpedOnce.failed = false
	})
	if dumpedOnce.failed {
		t.Fatal("building or running the dump app failed in an earlier test")
	}
	return append([]byte(nil), dumpedOnce.meta...), append([]byte(nil), dumpedOnce.counters...)
}

func dumpOnce(t testing.TB) (metaData, counterData []byte) {
	t.Helper()
	goBin := needGo(t)
	mod := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":  "module example.com/dump\n\ngo 1.21\n",
		"main.go": dumpSource,
	} {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(t.TempDir(), "dump")
	if out, err := run(t, mod, nil, goBin, "build", "-cover", "-covermode=atomic", "-o", bin, "."); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	mf, cf := filepath.Join(dir, "meta"), filepath.Join(dir, "counters")
	if out, err := run(t, dir, nil, bin, mf, cf); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var err error
	if metaData, err = os.ReadFile(mf); err != nil {
		t.Fatal(err)
	}
	if counterData, err = os.ReadFile(cf); err != nil {
		t.Fatal(err)
	}
	return metaData, counterData
}

func dumpLine(t *testing.T, s string) int {
	t.Helper()
	for i, l := range strings.Split(dumpSource, "\n") {
		if strings.Contains(l, s) {
			return i + 1
		}
	}
	t.Fatalf("dumpSource has no line holding %q", s)
	return 0
}

// TestDecodingWhatTheRuntimeWrote decodes a real app's meta-data and
// counters: its package, its two functions with their blocks, and the lines
// f ran: its first block and its x > 1 branch, not its last return.
func TestDecodingWhatTheRuntimeWrote(t *testing.T) {
	metaData, counterData := dumped(t)
	m, err := decodeMeta(metaData)
	if err != nil {
		t.Fatal(err)
	}
	if m.mode != ctrModeAtomic || m.granularity != ctrGranularityPerBlk {
		t.Fatalf("mode %d granularity %d, want atomic per block", m.mode, m.granularity)
	}
	var pkg *metaPkg
	for i := range m.pkgs {
		if m.pkgs[i].path == "example.com/dump" {
			pkg = &m.pkgs[i]
		}
	}
	if pkg == nil {
		t.Fatalf("no package example.com/dump in %+v", m.pkgs)
	}
	if pkg.module != "example.com/dump" {
		t.Fatalf("package example.com/dump is of module %q, want example.com/dump", pkg.module)
	}
	names := map[string]metaFunc{}
	for _, f := range pkg.funcs {
		names[f.name] = f
	}
	fl := dumpLine(t, "func f(")
	f, ok := names["f"]
	if !ok || f.file != "example.com/dump/main.go" || len(f.units) != 3 {
		t.Fatalf("function f decoded as %+v", f)
	}
	// Three blocks, one statement each, inside f: the if, its body and the
	// return after it (where each starts is cmd/cover's, which this does not
	// pin).
	for _, u := range f.units {
		if u.nxStmts != 1 || u.enLine < u.stLine || u.stLine < uint32(fl) || u.enLine > uint32(fl+5) || u.stCol == 0 || u.enCol == 0 {
			t.Fatalf("f's block %+v: want one statement within lines %d-%d", u, fl, fl+5)
		}
	}
	if _, ok := names["main"]; !ok {
		t.Fatalf("no function main in %v", names)
	}
	got, err := linesOf(m, counterData)
	if err != nil {
		t.Fatal(err)
	}
	// main ran too, up to the WriteCounters call: its lines are there, and f's
	// last return (fl+4) is not.
	fileLines := got["example.com/dump/main.go"]
	for _, l := range []int{fl + 1, fl + 2, fl + 3} {
		if !containsInt(fileLines, l) {
			t.Fatalf("lines %v miss f's line %d", fileLines, l)
		}
	}
	if containsInt(fileLines, fl+4) {
		t.Fatalf("lines %v hold f's last return (%d), which did not run", fileLines, fl+4)
	}
	if len(got) != 1 {
		t.Fatalf("lines of %d files, want 1: %v", len(got), got)
	}
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// TestDecodingRefusesWhatItCannotRead pins that no cut, foreign or other-build
// input is decoded or panics: every prefix of the data is an error, and so are
// another format version, another build's counters and a block past the
// function's counters.
func TestDecodingRefusesWhatItCannotRead(t *testing.T) {
	metaData, counterData := dumped(t)
	for n := 0; n < len(metaData); n++ {
		if _, err := decodeMeta(metaData[:n]); err == nil {
			t.Fatalf("the meta-data's first %d of %d bytes decoded", n, len(metaData))
		}
		// The same cut with its length set to the cut's, so the decoder
		// reads on into the package table, the payloads and the functions.
		if n >= 56 {
			b := append([]byte(nil), metaData[:n]...)
			binary.LittleEndian.PutUint64(b[8:], uint64(n))
			if _, err := decodeMeta(b); err == nil {
				t.Fatalf("the meta-data's first %d of %d bytes, its length set to them, decoded", n, len(metaData))
			}
		}
		// A cut inside the last package's payload, with that package's
		// length and its payload's own length set to the cut too, so the
		// package decoder reads truncated input.
		if entries := binary.LittleEndian.Uint64(metaData[16:]); entries > 0 && n >= 56 {
			offAt := 56 + 8*int(entries-1)
			lenAt := 56 + 8*int(entries) + 8*int(entries-1)
			last := int(binary.LittleEndian.Uint64(metaData[offAt:]))
			if n >= last+4 {
				b := append([]byte(nil), metaData[:n]...)
				binary.LittleEndian.PutUint64(b[8:], uint64(n))
				binary.LittleEndian.PutUint64(b[lenAt:], uint64(n-last))
				binary.LittleEndian.PutUint32(b[last:], uint32(n-last))
				if _, err := decodeMeta(b); err == nil {
					t.Fatalf("the last package cut at %d of %d bytes, its lengths set to the cut, decoded", n, len(metaData))
				}
			}
		}
	}
	m, err := decodeMeta(metaData)
	if err != nil {
		t.Fatal(err)
	}
	foot := counterData[len(counterData)-counterFooterSize:]
	for n := 0; n < len(counterData); n++ {
		if _, err := linesOf(m, counterData[:n]); err == nil {
			t.Fatalf("the counter-data's first %d of %d bytes decoded", n, len(counterData))
		}
		// The same cut with the footer after it, so the decoder reads on
		// into the segment and the functions.
		if n >= counterFileHeaderSize && n < len(counterData)-counterFooterSize {
			b := append(append([]byte(nil), counterData[:n]...), foot...)
			if _, err := linesOf(m, b); err == nil {
				t.Fatalf("the counter-data's first %d of %d bytes, with its footer, decoded", n, len(counterData))
			}
		}
	}
	for _, v := range []byte{0, 2} {
		b := append([]byte(nil), metaData...)
		b[4] = v
		if _, err := decodeMeta(b); err == nil || !strings.Contains(err.Error(), "format version") {
			t.Fatalf("meta-data of version %d: %v", v, err)
		}
		c := append([]byte(nil), counterData...)
		c[4] = v
		if _, err := linesOf(m, c); err == nil || !strings.Contains(err.Error(), "format version") {
			t.Fatalf("counter-data of version %d: %v", v, err)
		}
	}
	long := append(append(append([]byte(nil), counterData[:len(counterData)-counterFooterSize]...), 0), counterData[len(counterData)-counterFooterSize:]...)
	if _, err := linesOf(m, long); err == nil || !strings.Contains(err.Error(), "past its last function") {
		t.Fatalf("counter-data with a byte past its last function: %v", err)
	}
	c := append([]byte(nil), counterData...)
	c[8] ^= 0xff
	if _, err := linesOf(m, c); err == nil || !strings.Contains(err.Error(), "another build") {
		t.Fatalf("counters of another build: %v", err)
	}
	short := *m
	short.pkgs = append([]metaPkg(nil), m.pkgs...)
	for i := range short.pkgs {
		funcs := append([]metaFunc(nil), short.pkgs[i].funcs...)
		for j := range funcs {
			funcs[j].units = append(append([]unit(nil), funcs[j].units...), unit{stLine: 1, enLine: 1})
		}
		short.pkgs[i].funcs = funcs
	}
	if _, err := linesOf(&short, counterData); err == nil || !strings.Contains(err.Error(), "counters for") {
		t.Fatalf("a function with more blocks than counters: %v", err)
	}
}

// TestPerFunctionCountersCountEveryBlock pins per-function granularity: one
// counter stands for every block of the function.
func TestPerFunctionCountersCountEveryBlock(t *testing.T) {
	metaData, counterData := dumped(t)
	m, err := decodeMeta(metaData)
	if err != nil {
		t.Fatal(err)
	}
	perBlock, err := linesOf(m, counterData)
	if err != nil {
		t.Fatal(err)
	}
	m.granularity = ctrGranularityPerFunc
	perFunc, err := linesOf(m, counterData)
	if err != nil {
		t.Fatal(err)
	}
	fl := dumpLine(t, "func f(")
	if got := perFunc["example.com/dump/main.go"]; !containsInt(got, fl+4) || containsInt(perBlock["example.com/dump/main.go"], fl+4) {
		t.Fatalf("per function: %v; per block: %v; want f's last return only per function", got, perBlock)
	}
}

// TestReadingNumbersOfManyBytes pins ULEB128 numbers of more than one byte,
// which the small apps above never write.
func TestReadingNumbersOfManyBytes(t *testing.T) {
	r := &reader{b: []byte{0xe5, 0x8e, 0x26, 0x7f, 0x80, 0x01}}
	if v := r.uleb(); v != 624485 || r.err != nil {
		t.Fatalf("read %d (%v), want 624485", v, r.err)
	}
	if v := r.uleb(); v != 127 {
		t.Fatalf("read %d, want 127", v)
	}
	if v := r.uleb(); v != 128 || r.err != nil {
		t.Fatalf("read %d (%v), want 128", v, r.err)
	}
	if r.uleb(); r.err == nil {
		t.Fatal("a read past the end is no error")
	}
}

// counterData encodes one segment of ULEB128 counter-data for meta-data hash
// h, as runtime/coverage.WriteCounters does, with funcs as (package index,
// function index, counters).
func counterData(h [16]byte, funcs [][]uint32) []byte {
	b := []byte{0x00, 0x63, 0x77, 0x6d, 1, 0, 0, 0}
	b = append(b, h[:]...)
	b = append(b, ctrULeb128, 0, 0, 0, 0, 0, 0, 0)
	seg := make([]byte, 16)
	binary.LittleEndian.PutUint64(seg, uint64(len(funcs)))
	binary.LittleEndian.PutUint32(seg[8:], 1)
	binary.LittleEndian.PutUint32(seg[12:], 1)
	b = append(b, seg...)
	b = append(b, 0, 0) // an empty string table, no args
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	uleb := func(v uint32) {
		for {
			c := byte(v & 0x7f)
			v >>= 7
			if v != 0 {
				c |= 0x80
			}
			b = append(b, c)
			if v == 0 {
				return
			}
		}
	}
	for _, f := range funcs {
		uleb(uint32(len(f) - 2))
		for _, v := range f {
			uleb(v)
		}
	}
	foot := []byte{0x00, 0x63, 0x77, 0x6d, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}
	return append(b, foot...)
}

// TestCountingBlocksByGranularity pins how counters become lines: per block,
// a block ran when its own counter is not zero; per function, the one counter
// stands for every block.
func TestCountingBlocksByGranularity(t *testing.T) {
	h := [16]byte{1, 2, 3}
	m := &metaFile{hash: h, pkgs: []metaPkg{{path: "example.com/p", funcs: []metaFunc{{
		name: "g", file: "example.com/p/a.go",
		units: []unit{{stLine: 1, enLine: 1}, {stLine: 2, enLine: 3}, {stLine: 5, enLine: 5}},
	}}}}}
	for _, row := range []struct {
		name     string
		gran     uint8
		counters []uint32
		want     []int
	}{
		{"per block, the second ran", ctrGranularityPerBlk, []uint32{0, 300, 0}, []int{2, 3}},
		{"per block, the first and last ran", ctrGranularityPerBlk, []uint32{1, 0, 200000}, []int{1, 5}},
		{"per function, it ran", ctrGranularityPerFunc, []uint32{200}, []int{1, 2, 3, 5}},
	} {
		t.Run(row.name, func(t *testing.T) {
			m.granularity = row.gran
			got, err := linesOf(m, counterData(h, [][]uint32{append([]uint32{0, 0}, row.counters...)}))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, map[string][]int{"example.com/p/a.go": row.want}) {
				t.Fatalf("lines %v, want %v", got, row.want)
			}
		})
	}
	m.granularity = ctrGranularityPerBlk
	if _, err := linesOf(m, counterData(h, [][]uint32{{0, 1, 5}})); err == nil || !strings.Contains(err.Error(), "function 1 of 1") {
		t.Fatalf("a function the meta-data does not hold: %v", err)
	}
	if _, err := linesOf(m, counterData(h, [][]uint32{{1, 0, 5}})); err == nil || !strings.Contains(err.Error(), "package 1 of 1") {
		t.Fatalf("a package the meta-data does not hold: %v", err)
	}
}

// TestLoadingMetaRefusesANonAtomicBuild pins that meta-data of a build whose
// counters are not atomic is refused, and that a refusal is not kept, so a
// later read tries again.
func TestLoadingMetaRefusesANonAtomicBuild(t *testing.T) {
	metaData, _ := dumped(t)
	setMode := append([]byte(nil), metaData...)
	setMode[48] = 1 // CMode: set
	oldMeta, oldWrite := meta, writeMeta
	t.Cleanup(func() { meta, writeMeta = oldMeta, oldWrite })
	meta = nil
	writeMeta = func(w io.Writer) error { _, err := w.Write(setMode); return err }
	if _, err := loadMeta(); err == nil || !strings.Contains(err.Error(), "-covermode=atomic") {
		t.Fatalf("meta-data of a set-mode build: %v", err)
	}
	if meta != nil {
		t.Fatal("a refused meta-data was kept")
	}
	writeMeta = func(w io.Writer) error { _, err := w.Write(metaData); return err }
	m, err := loadMeta()
	if err != nil || m == nil || meta != m {
		t.Fatalf("the atomic meta-data after a refusal: %v", err)
	}
}

// corruptions are data, each one byte of b set to 0x00 or to 0xff, over the
// first n bytes from start: inputs whose length and footer stay whole, so the
// decoders read on into what each byte means.
func corruptions(b []byte, start, n int) [][]byte {
	var out [][]byte
	for i := start; i < len(b) && i < start+n; i++ {
		for _, v := range []byte{0x00, 0xff} {
			c := append([]byte(nil), b...)
			c[i] = v
			out = append(out, c)
		}
	}
	return out
}

// FuzzDecodeMeta: no meta-data, whatever its bytes, panics the decoder; the
// seeds are a real app's and each one-byte corruption of it.
func FuzzDecodeMeta(f *testing.F) {
	metaData, _ := dumped(f)
	f.Add(metaData)
	for _, c := range corruptions(metaData, 0, len(metaData)) {
		f.Add(c)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := decodeMeta(b)
		if err != nil {
			return
		}
		if m == nil {
			t.Fatal("no error and no meta-data")
		}
	})
}

// FuzzLinesOf: no counter-data, whatever its bytes, panics the decoder or
// turns into lines of no block; the seeds are a real app's and each one-byte
// corruption of it but its footer.
func FuzzLinesOf(f *testing.F) {
	metaData, counterData := dumped(f)
	m, err := decodeMeta(metaData)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(counterData)
	for _, c := range corruptions(counterData, 0, len(counterData)-counterFooterSize) {
		f.Add(c)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := linesOf(m, b)
		if err != nil {
			return
		}
		for file, lines := range got {
			if file != "example.com/dump/main.go" || len(lines) == 0 {
				t.Fatalf("lines %v", got)
			}
		}
	})
}

// TestWhichPackagesATestsLinesName pins leftOut: the hook's own package and
// any outside the app's modules are left out; a package is of the module the
// compiler recorded, else of the module its path is under (at a "/").
func TestWhichPackagesATestsLinesName(t *testing.T) {
	mods := map[string]bool{"example.com/app": true, "example.com/ws": true}
	for _, row := range []struct {
		pkg  metaPkg
		mods map[string]bool
		want bool
	}{
		{metaPkg{path: ownPkgPath, module: "github.com/keploy/go-sdk/v3"}, mods, true},
		{metaPkg{path: "example.com/app", module: "example.com/app"}, mods, false},
		{metaPkg{path: "example.com/app/x", module: "example.com/app"}, mods, false},
		{metaPkg{path: "example.com/ws/y", module: "example.com/ws"}, mods, false},
		{metaPkg{path: "example.com/dep", module: "example.com/dep"}, mods, true},
		{metaPkg{path: "example.com/app/fork", module: "example.com/fork"}, mods, true},
		{metaPkg{path: "net/http"}, mods, true},
		{metaPkg{path: "example.com/app/z"}, mods, false},
		{metaPkg{path: "example.com/appish"}, mods, true},
		{metaPkg{path: "command-line-arguments"}, mods, false},
		{metaPkg{path: "net/http"}, nil, false},
		{metaPkg{path: ownPkgPath}, nil, true},
	} {
		if got := leftOut(row.pkg, row.mods); got != row.want {
			t.Errorf("leftOut(%+v, %v) = %v, want %v", row.pkg, row.mods, got, row.want)
		}
	}
}

// TestOddBlocksNameFewLines pins blocks the compiler can write that no
// source holds: one that ends before it starts (a //line directive moved its
// end) names no line, and one longer than any file is cut at maxBlockLines;
// the other blocks count as ever.
func TestOddBlocksNameFewLines(t *testing.T) {
	h := [16]byte{9}
	m := &metaFile{hash: h, granularity: ctrGranularityPerBlk, pkgs: []metaPkg{{path: "example.com/p", funcs: []metaFunc{{
		name: "g", file: "example.com/p/gen.go",
		units: []unit{{stLine: 9, enLine: 4}, {stLine: 20, enLine: 21}, {stLine: 30, enLine: 30 + maxBlockLines + 5}},
	}, {
		name: "h", file: "example.com/p/other.go", units: []unit{{stLine: 7, enLine: 3}},
	}}}}}
	got, err := linesOf(m, counterData(h, [][]uint32{{0, 0, 1, 1, 1}, {0, 1, 1}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["example.com/p/other.go"]; ok || len(got) != 1 {
		t.Fatalf("a file whose blocks that ran name no line is sent: %v", got)
	}
	lines := got["example.com/p/gen.go"]
	if len(lines) != 2+maxBlockLines+1 || lines[0] != 20 || lines[1] != 21 || lines[len(lines)-1] != 30+maxBlockLines {
		head := lines
		if len(head) > 3 {
			head = head[:3]
		}
		t.Fatalf("%d lines, starting %v; want 20, 21 and 30 to %d", len(lines), head, 30+maxBlockLines)
	}
}

// TestTheAppsOwnModules pins firstPartyOf: the main module, a go.work's
// modules, and modules replaced by a directory, never a dependency at a
// version nor one replaced by another module.
func TestTheAppsOwnModules(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{
		{Path: "example.com/ws", Version: "(devel)"},
		{Path: "example.com/shared", Version: "v0.0.0", Replace: &debug.Module{Path: "../shared"}},
		{Path: "example.com/shared2", Version: "v0.0.0", Replace: &debug.Module{Path: "./s2", Version: "(devel)"}},
		{Path: "github.com/x/y", Version: "v1.2.3"},
		{Path: "github.com/fork/z", Version: "v1.0.0", Replace: &debug.Module{Path: "github.com/me/z", Version: "v1.0.1"}},
	}}
	want := map[string]bool{"example.com/app": true, "example.com/ws": true, "example.com/shared": true, "example.com/shared2": true}
	if got := firstPartyOf(bi); !reflect.DeepEqual(got, want) {
		t.Fatalf("first-party modules %v, want %v", got, want)
	}
	if got := firstPartyOf(&debug.BuildInfo{}); got != nil {
		t.Fatalf("with no main module: %v, want none known", got)
	}
}
