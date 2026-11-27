//go:build linux || darwin

package cover

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	reGoBuildID  = regexp.MustCompile(`^go1-[0-9a-f]{32}$`)
	reUnitSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// lookupBlock resolves a raw coverage counter block via funcBlockLookup for tests.
func lookupBlock(t *goTable, pkgIdx, funcIdx, blockIdx int) (unitPath string, relLeafIdx uint32, ok bool) {
	fm, unitPath := t.funcBlockLookup(pkgIdx, funcIdx)
	if fm == nil || blockIdx < 0 || blockIdx >= len(fm.leafOrds) {
		return "", 0, false
	}
	ord := fm.leafOrds[blockIdx]
	if ord < 0 {
		return "", 0, false
	}
	return unitPath, uint32(ord), true
}

// validateSchema1GoTable verifies every invariant enforced by
// enterprise/pkg/service/codecoverage/table.go and
// api-server/pkg/service/codecoverage/table.go over an NDJSON stream.
func validateSchema1GoTable(t *testing.T, ndjson []byte) []tableUnit {
	t.Helper()
	if len(ndjson) > 0 && ndjson[len(ndjson)-1] != '\n' {
		t.Fatalf("NDJSON stream does not end with newline")
	}
	var units []tableUnit
	seenUnits := map[string]bool{}
	seenMethods := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(ndjson))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var prevUnit *tableUnit
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			t.Fatalf("empty line inside NDJSON table stream")
		}
		// Reject unknown fields just like strict readers do.
		var u tableUnit
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&u); err != nil {
			t.Fatalf("invalid table unit JSON %q: %v", line, err)
		}
		if u.Kind != "file" {
			t.Fatalf("unit %q: kind %q, want file", u.ID, u.Kind)
		}
		if u.Pkg == "" || strings.HasPrefix(u.Pkg, "/") || strings.HasSuffix(u.Pkg, "/") || strings.Contains(u.Pkg, "//") {
			t.Fatalf("unit %q: invalid pkg %q", u.ID, u.Pkg)
		}
		if u.Name == "" || strings.Contains(u.Name, "/") {
			t.Fatalf("unit %q: invalid name %q", u.ID, u.Name)
		}
		unitPath := u.Pkg + "/" + u.Name
		if u.Path != unitPath {
			t.Fatalf("unit %q: path %q, want %q", u.ID, u.Path, unitPath)
		}
		if u.ID != "f:"+unitPath {
			t.Fatalf("unit %q: id want f:%s", u.ID, unitPath)
		}
		if seenUnits[u.ID] {
			t.Fatalf("duplicate unit id %q", u.ID)
		}
		seenUnits[u.ID] = true
		if prevUnit != nil {
			c := comparePkg(prevUnit.Pkg, u.Pkg)
			if c > 0 || (c == 0 && prevUnit.ID >= u.ID) {
				t.Fatalf("unit %q out of order after %q", u.ID, prevUnit.ID)
			}
		}
		if !reUnitSHA256.MatchString(u.Hash) {
			t.Fatalf("unit %q: hash %q is not 64 lowercase hex", u.ID, u.Hash)
		}
		if u.State != "ok" {
			t.Fatalf("unit %q: state %q, want ok", u.ID, u.State)
		}
		if u.Methods == nil {
			t.Fatalf("unit %q: methods is nil, want JSON array", u.ID)
		}
		var prevMethod *tableMethod
		for mi := range u.Methods {
			m := &u.Methods[mi]
			rest, ok := strings.CutPrefix(m.ID, "m:"+unitPath)
			if !ok || !strings.HasPrefix(rest, "#") || len(rest) <= 1 {
				t.Fatalf("unit %q: invalid method id %q", u.ID, m.ID)
			}
			if seenMethods[m.ID] {
				t.Fatalf("duplicate method id %q", m.ID)
			}
			seenMethods[m.ID] = true
			if m.Line < 1 {
				t.Fatalf("method %q: line %d < 1", m.ID, m.Line)
			}
			if prevMethod != nil && (prevMethod.Line > m.Line || (prevMethod.Line == m.Line && prevMethod.ID >= m.ID)) {
				t.Fatalf("method %q out of order after %q", m.ID, prevMethod.ID)
			}
			prevMethod = m
			var prevLeaf *tableLeaf
			for li, lf := range m.Leaves {
				for _, v := range lf {
					if v < 0 || v > maxLeafVal {
						t.Fatalf("method %q leaf %d: value %d outside 0..%d", m.ID, li, v, maxLeafVal)
					}
				}
				if lf[5] != 0 {
					t.Fatalf("method %q leaf %d: kind %d, want 0", m.ID, li, lf[5])
				}
				if lf[0] < 1 || lf[2] < lf[0] || (lf[2] == lf[0] && lf[3] < lf[1]) {
					t.Fatalf("method %q leaf %d: bad range %v", m.ID, li, lf)
				}
				if lf[2]-lf[0]+1 > maxLeafLines {
					t.Fatalf("method %q leaf %d: span %d exceeds maxLeafLines %d", m.ID, li, lf[2]-lf[0]+1, maxLeafLines)
				}
				if prevLeaf != nil && compareTableLeaf(*prevLeaf, lf) > 0 {
					t.Fatalf("method %q leaf %d out of order: %v after %v", m.ID, li, lf, *prevLeaf)
				}
				cur := lf
				prevLeaf = &cur
			}
		}
		uCopy := u
		prevUnit = &uCopy
		units = append(units, u)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan NDJSON: %v", err)
	}
	return units
}

// TestBuildGoTable_CanonicalSchemaAndOrdering pins every rule of canonical
// schema-1 Go table construction: package filtering, segment-wise comparePkg
// order, command-line-arguments normalization, method line/ID disambiguation,
// leaf sorting, deterministic SHA-256 hash without HTML escaping, and strict
// compliance with enterprise & api-server validators.
func TestBuildGoTable_CanonicalSchemaAndOrdering(t *testing.T) {
	hash := [16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}
	m := &metaFile{
		hash:        hash,
		mode:        ctrModeAtomic,
		granularity: ctrGranularityPerBlk,
		pkgs: []metaPkg{
			{
				// Skipped package (e.g. stdlib / dependency / ownPkgPath) must be excluded.
				path: "net/http",
				skip: true,
				funcs: []metaFunc{{
					name:  "Serve",
					file:  "net/http/server.go",
					units: []unit{{stLine: 10, stCol: 1, enLine: 12, enCol: 2, nxStmts: 1}},
				}},
			},
			{
				// Must sort AFTER "example.com/a/b" because '-' > '/' in segment comparePkg.
				path: "example.com/a-x",
				funcs: []metaFunc{{
					name:  "Dash",
					file:  "example.com/a-x/dash.go",
					units: []unit{{stLine: 5, stCol: 2, enLine: 6, enCol: 3, nxStmts: 1}},
				}},
			},
			{
				path: "example.com/a/b",
				funcs: []metaFunc{{
					name:  "Sub",
					file:  "example.com/a/b/sub.go",
					units: []unit{{stLine: 20, stCol: 1, enLine: 22, enCol: 5, nxStmts: 2}},
				}},
			},
			{
				path: "example.com/a",
				funcs: []metaFunc{
					{
						// Unsorted leaves + generic/HTML characters in name.
						name: "Generic[T<int>&1]",
						file: "example.com/a/app.go",
						units: []unit{
							{stLine: 30, stCol: 5, enLine: 31, enCol: 2, nxStmts: 2},
							{stLine: 15, stCol: 8, enLine: 15, enCol: 20, nxStmts: 1},
							{stLine: 15, stCol: 2, enLine: 15, enCol: 10, nxStmts: 1},
							{stLine: 15, stCol: 2, enLine: 15, enCol: 10, nxStmts: 3},
						},
					},
					{
						// Two functions sharing (name, line) in one file -> deterministic disambiguation.
						name:  "init",
						file:  "example.com/a/app.go",
						units: []unit{{stLine: 10, stCol: 10, enLine: 10, enCol: 20, nxStmts: 1}},
					},
					{
						name:  "init",
						file:  "example.com/a/app.go",
						units: []unit{{stLine: 10, stCol: 2, enLine: 10, enCol: 8, nxStmts: 1}},
					},
					{
						// 0 valid leaves -> skipped cleanly.
						name:  "Empty",
						file:  "example.com/a/only_invalid.go",
						units: []unit{{stLine: 0, stCol: 1, enLine: 5, enCol: 1, nxStmts: 1}},
					},
				},
			},
			{
				path: "command-line-arguments",
				funcs: []metaFunc{{
					name:  "main",
					file:  "/tmp/workspace/hello.go",
					units: []unit{{stLine: 7, stCol: 1, enLine: 9, enCol: 2, nxStmts: 2}},
				}},
			},
		},
	}

	tbl, err := buildGoTable(m)
	if err != nil {
		t.Fatalf("buildGoTable: %v", err)
	}
	wantBuild := "go1-0123456789abcdeffedcba9876543210"
	if tbl.build != wantBuild {
		t.Fatalf("build = %q, want %q", tbl.build, wantBuild)
	}
	if tbl.precision != "block" {
		t.Fatalf("precision = %q, want block", tbl.precision)
	}
	// HTML characters (<, >, &) must NOT be escaped in NDJSON!
	if bytes.Contains(tbl.ndjson, []byte(`\u003c`)) || bytes.Contains(tbl.ndjson, []byte(`\u003e`)) || bytes.Contains(tbl.ndjson, []byte(`\u0026`)) {
		t.Fatalf("NDJSON escaped HTML characters:\n%s", tbl.ndjson)
	}
	if !bytes.Contains(tbl.ndjson, []byte(`Generic[T<int>&1]`)) {
		t.Fatalf("NDJSON missing unescaped function name:\n%s", tbl.ndjson)
	}

	units := validateSchema1GoTable(t, tbl.ndjson)
	wantOrder := []string{
		"f:command-line-arguments/hello.go",
		"f:example.com/a/app.go",
		"f:example.com/a/b/sub.go",
		"f:example.com/a-x/dash.go",
	}
	gotOrder := make([]string, len(units))
	for i, u := range units {
		gotOrder[i] = u.ID
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("unit order = %v, want %v", gotOrder, wantOrder)
	}

	// Verify disambiguated colliding methods and sorted leaves on example.com/a/app.go.
	appUnit := units[1]
	if len(appUnit.Methods) != 3 {
		t.Fatalf("app.go methods = %d, want 3: %+v", len(appUnit.Methods), appUnit.Methods)
	}
	if appUnit.Methods[0].ID != "m:example.com/a/app.go#init@10" || appUnit.Methods[1].ID != "m:example.com/a/app.go#init@10#2" {
		t.Fatalf("disambiguated init IDs = %q, %q", appUnit.Methods[0].ID, appUnit.Methods[1].ID)
	}
	// The init with sc=2 sorts before init with sc=10 deterministically.
	if appUnit.Methods[0].Leaves[0][1] != 2 || appUnit.Methods[1].Leaves[0][1] != 10 {
		t.Fatalf("init collision order wrong: %+v, %+v", appUnit.Methods[0].Leaves, appUnit.Methods[1].Leaves)
	}
	wantGenLeaves := []tableLeaf{
		{15, 2, 15, 10, 1, 0},
		{15, 2, 15, 10, 3, 0},
		{15, 8, 15, 20, 1, 0},
		{30, 5, 31, 2, 2, 0},
	}
	if !reflect.DeepEqual(appUnit.Methods[2].Leaves, wantGenLeaves) {
		t.Fatalf("generic leaves = %v, want %v", appUnit.Methods[2].Leaves, wantGenLeaves)
	}
	if appUnit.Methods[2].Line != 15 {
		t.Fatalf("generic method line = %d, want 15 (min sl)", appUnit.Methods[2].Line)
	}

	// Verify hash matches SHA-256 of canonical methods JSON (HTML escaping off).
	methJSON, err := marshalJSONNoEscape(appUnit.Methods)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(methJSON)
	if appUnit.Hash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("app.go hash = %q, want %q", appUnit.Hash, hex.EncodeToString(wantHash[:]))
	}

	// Rebuilding with reversed colliding funcs in meta must produce IDENTICAL NDJSON bytes & hashes!
	m2 := *m
	m2.pkgs = append([]metaPkg(nil), m.pkgs...)
	funcs3 := append([]metaFunc(nil), m.pkgs[3].funcs...)
	funcs3[1], funcs3[2] = funcs3[2], funcs3[1]
	m2.pkgs[3].funcs = funcs3
	tbl2, err := buildGoTable(&m2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tbl.ndjson, tbl2.ndjson) || tbl.ndjsonSha != tbl2.ndjsonSha {
		t.Fatalf("table encoding is not deterministic across function reordering")
	}
}

// TestBuildGoTable_LineDirectiveValidationAndClamping pins //line invalid-block
// skipping and api-server maxLeafLines (65536) clamping.
func TestBuildGoTable_LineDirectiveValidationAndClamping(t *testing.T) {
	m := &metaFile{
		hash:        [16]byte{0xaa},
		mode:        ctrModeAtomic,
		granularity: ctrGranularityPerBlk,
		pkgs: []metaPkg{{
			path: "example.com/gen",
			funcs: []metaFunc{{
				name: "Parse",
				file: "example.com/gen/parser.go",
				units: []unit{
					{stLine: 0, stCol: 1, enLine: 5, enCol: 2, nxStmts: 1},                        // invalid: stLine == 0
					{stLine: 20, stCol: 1, enLine: 10, enCol: 2, nxStmts: 1},                      // invalid: enLine < stLine
					{stLine: 30, stCol: 10, enLine: 30, enCol: 5, nxStmts: 1},                     // invalid: enLine == stLine && enCol < stCol
					{stLine: 40, stCol: 1, enLine: 40 + maxLeafLines - 1, enCol: 9, nxStmts: 2},   // boundary: span == 65536 (kept exact)
					{stLine: 50, stCol: 1, enLine: 50 + maxLeafLines + 999, enCol: 9, nxStmts: 3}, // over bound: clamped to 50+65535
					{stLine: 60, stCol: 1, enLine: math.MaxUint32, enCol: 9, nxStmts: 4},          // uint32 overflow guard: clamped to 60+65535
					{stLine: maxLeafVal, stCol: 10, enLine: maxLeafVal + 5, enCol: 2, nxStmts: 1}, // invalid after clamping el to maxLeafVal (ec < sc on same line)
				},
			}},
		}},
	}

	tbl, err := buildGoTable(m)
	if err != nil {
		t.Fatalf("buildGoTable: %v", err)
	}
	units := validateSchema1GoTable(t, tbl.ndjson)
	if len(units) != 1 || len(units[0].Methods) != 1 {
		t.Fatalf("unexpected units: %+v", units)
	}
	wantLeaves := []tableLeaf{
		{40, 1, 40 + maxLeafLines - 1, 9, 2, 0},
		{50, 1, 50 + maxLeafLines - 1, 9, 3, 0},
		{60, 1, 60 + maxLeafLines - 1, 9, 4, 0},
	}
	if !reflect.DeepEqual(units[0].Methods[0].Leaves, wantLeaves) {
		t.Fatalf("leaves = %v, want %v", units[0].Methods[0].Leaves, wantLeaves)
	}

	// Skipped blocks (including post-clamp invalid block 6) must map to ok=false; kept blocks map to unit-relative 0, 1, 2.
	for _, badIdx := range []int{0, 1, 2, 6} {
		if _, _, ok := lookupBlock(tbl, 0, 0, badIdx); ok {
			t.Fatalf("invalid block %d unexpectedly mapped", badIdx)
		}
	}
	for i, blockIdx := range []int{3, 4, 5} {
		up, ord, ok := lookupBlock(tbl, 0, 0, blockIdx)
		if !ok || up != "example.com/gen/parser.go" || ord != uint32(i) {
			t.Fatalf("block %d mapped to (%q, %d, %v), want (example.com/gen/parser.go, %d, true)", blockIdx, up, ord, ok, i)
		}
	}
}

// TestLookupBlockAndLinesAndBlocksOf_UnitRelativeOrdinals verifies that
// executedBlocksByFile emits sorted unique unit-relative ordinals (starting at
// 0 per unit for demotion safety), works across reordered methods/leaves,
// normalizes command-line-arguments paths, and respects per-func granularity.
func TestLookupBlockAndLinesAndBlocksOf_UnitRelativeOrdinals(t *testing.T) {
	h := [16]byte{0x11, 0x22, 0x33}
	m := &metaFile{
		hash:        h,
		mode:        ctrModeAtomic,
		granularity: ctrGranularityPerBlk,
		pkgs: []metaPkg{
			{
				// Unit 0 in sorted order (example.com/a/first.go): 2 methods given in reverse line order.
				path: "example.com/a",
				funcs: []metaFunc{
					{
						name: "Later",
						file: "example.com/a/first.go",
						units: []unit{
							{stLine: 50, stCol: 1, enLine: 52, enCol: 2, nxStmts: 1}, // canonical ord 2
							{stLine: 40, stCol: 1, enLine: 41, enCol: 2, nxStmts: 1}, // canonical ord 1
						},
					},
					{
						name: "Earlier",
						file: "example.com/a/first.go",
						units: []unit{
							{stLine: 10, stCol: 1, enLine: 12, enCol: 2, nxStmts: 1}, // canonical ord 0
						},
					},
				},
			},
			{
				// Unit 1 in sorted order (command-line-arguments/cli.go): ordinals MUST also start at 0!
				path: "command-line-arguments",
				funcs: []metaFunc{{
					name: "Run",
					file: "/abs/tmp/cli.go",
					units: []unit{
						{stLine: 25, stCol: 1, enLine: 26, enCol: 2, nxStmts: 1}, // canonical ord 1
						{stLine: 15, stCol: 1, enLine: 16, enCol: 2, nxStmts: 1}, // canonical ord 0
					},
				}},
			},
		},
	}

	tbl, err := buildGoTable(m)
	if err != nil {
		t.Fatal(err)
	}
	// Verify unit-relative ordinals of BOTH units start at 0 (demotion-safe).
	if up, ord, ok := lookupBlock(tbl, 0, 1, 0); !ok || up != "example.com/a/first.go" || ord != 0 {
		t.Fatalf("Earlier[0] = (%q, %d, %v), want ord 0", up, ord, ok)
	}
	if up, ord, ok := lookupBlock(tbl, 0, 0, 1); !ok || up != "example.com/a/first.go" || ord != 1 {
		t.Fatalf("Later[1] = (%q, %d, %v), want ord 1", up, ord, ok)
	}
	if up, ord, ok := lookupBlock(tbl, 0, 0, 0); !ok || up != "example.com/a/first.go" || ord != 2 {
		t.Fatalf("Later[0] = (%q, %d, %v), want ord 2", up, ord, ok)
	}
	if up, ord, ok := lookupBlock(tbl, 1, 0, 1); !ok || up != "command-line-arguments/cli.go" || ord != 0 {
		t.Fatalf("cli Run[1] = (%q, %d, %v), want unit-relative ord 0", up, ord, ok)
	}
	if up, ord, ok := lookupBlock(tbl, 1, 0, 0); !ok || up != "command-line-arguments/cli.go" || ord != 1 {
		t.Fatalf("cli Run[0] = (%q, %d, %v), want unit-relative ord 1", up, ord, ok)
	}

	// Hit Later[0] (ord 2) + Earlier[0] (ord 0), and cli Run[1] (ord 0).
	cd := counterData(h, [][]uint32{
		{0, 0, 5, 0}, // pkg 0, func 0 (Later): unit 0 hit (ord 2), unit 1 not hit
		{0, 1, 1},    // pkg 0, func 1 (Earlier): unit 0 hit (ord 0)
		{1, 0, 0, 3}, // pkg 1, func 0 (Run): unit 0 not hit, unit 1 hit (ord 0)
	})
	lines, blocks, err := linesAndBlocksOf(m, tbl, cd)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := map[string][]int{
		"example.com/a/first.go": {10, 11, 12, 50, 51, 52},
		"/abs/tmp/cli.go":        {15, 16},
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Fatalf("lines = %v, want %v", lines, wantLines)
	}
	wantBlocks := map[string][]int{
		"example.com/a/first.go":        {0, 2},
		"command-line-arguments/cli.go": {0},
	}
	if !reflect.DeepEqual(blocks, wantBlocks) {
		t.Fatalf("blocks = %v, want %v", blocks, wantBlocks)
	}

	// Zero hits -> blocks must be nil (omitted from JSON).
	zeroCD := counterData(h, [][]uint32{{0, 0, 0, 0}})
	lines0, blocks0, err := linesAndBlocksOf(m, tbl, zeroCD)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines0) != 0 || blocks0 != nil {
		t.Fatalf("zero hits: lines=%v blocks=%v, want empty & nil", lines0, blocks0)
	}

	// Per-func granularity: single counter hits every valid block of the function.
	mFunc := *m
	mFunc.granularity = ctrGranularityPerFunc
	tblFunc, err := buildGoTable(&mFunc)
	if err != nil {
		t.Fatal(err)
	}
	if tblFunc.precision != "func" {
		t.Fatalf("per-func precision = %q, want func", tblFunc.precision)
	}
	_, blocksFunc, err := linesAndBlocksOf(&mFunc, tblFunc, counterData(h, [][]uint32{{0, 0, 1}}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(blocksFunc, map[string][]int{"example.com/a/first.go": {1, 2}}) {
		t.Fatalf("per-func blocks = %v, want [1 2]", blocksFunc)
	}
}

// TestIndexRecord_FormatAndLazyDelivery pins the exact Go INDEX JSON payload
// (including "kind" as first key) and verifies lazy delivery on START as well
// as re-delivery when the data socket inode or BACKGROUND token changes.
func TestIndexRecord_FormatAndLazyDelivery(t *testing.T) {
	stubCoverage(t, nil, nil)
	m := &metaFile{
		hash:        [16]byte{0xde, 0xad, 0xbe, 0xef, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0xa, 0xb},
		mode:        ctrModeAtomic,
		granularity: ctrGranularityPerBlk,
		pkgs: []metaPkg{{
			path: "example.com/svc",
			funcs: []metaFunc{{
				name: "Handle",
				file: "example.com/svc/handler.go",
				units: []unit{
					{stLine: 10, stCol: 1, enLine: 12, enCol: 2, nxStmts: 1},
					{stLine: 13, stCol: 1, enLine: 15, enCol: 2, nxStmts: 2},
				},
			}},
		}},
	}
	meta = m

	var sent [][]byte
	sendRecord = func(b []byte) error {
		sent = append(sent, append([]byte(nil), b...))
		return nil
	}

	// Point dataSocketPath to a temp file that we can replace (new inode).
	dir := shortTempDir(t)
	sock1 := filepath.Join(dir, "d.sock")
	if err := os.WriteFile(sock1, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldData := dataSocketPath
	dataSocketPath = sock1
	t.Cleanup(func() { dataSocketPath = oldData })

	path := startTestServer(t)

	// First START delivers INDEX lazily.
	if got := send(t, path, "START set-0/test-1"); got != replyStartUnasked {
		t.Fatalf("START 1: %q", got)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d records after first START, want 1 INDEX", len(sent))
	}
	raw := string(sent[0])
	if !strings.HasPrefix(raw, `{"kind":"index","v":1,"language":"go","build":"go1-deadbeef000102030405060708090a0b","producer":"go",`) {
		t.Fatalf("INDEX prefix mismatch: %s", raw)
	}
	var idx map[string]any
	if err := json.Unmarshal(sent[0], &idx); err != nil {
		t.Fatalf("unmarshal INDEX: %v", err)
	}
	wantIdx := map[string]any{
		"kind":            "index",
		"v":               float64(1),
		"language":        "go",
		"build":           "go1-deadbeef000102030405060708090a0b",
		"producer":        "go",
		"producerVersion": runtime.Version(),
		"precision":       "block",
		"files":           float64(1),
		"leaves":          float64(2),
	}
	if !reflect.DeepEqual(idx, wantIdx) {
		t.Fatalf("INDEX = %v, want %v", idx, wantIdx)
	}

	// Second START on the same receiver/token must NOT re-send INDEX.
	if got := send(t, path, "END set-0/test-1"); got != replyTaken {
		t.Fatalf("END 1: %q", got)
	}
	if got := send(t, path, "START set-0/test-2"); got != replyStartUnasked {
		t.Fatalf("START 2: %q", got)
	}
	if len(sent) != 1 {
		t.Fatalf("re-sent INDEX on same receiver/token: %d sends", len(sent))
	}

	// Changing BACKGROUND token triggers INDEX re-delivery on next START.
	if got := send(t, path, "BACKGROUND tok-1"); got != replyTaken {
		t.Fatalf("BACKGROUND: %q", got)
	}
	if got := send(t, path, "END set-0/test-2"); got != replyTaken {
		t.Fatalf("END 2: %q", got)
	}
	if got := send(t, path, "START set-0/test-3"); got != replyStartAskedBy("tok-1") {
		t.Fatalf("START 3: %q", got)
	}
	if len(sent) != 2 {
		t.Fatalf("expected INDEX re-delivery after BACKGROUND token change; got %d sends", len(sent))
	}

	// Replacing data socket file with identical 0-byte size AND identical ModTime
	// (creating new file before rename guarantees a distinct inode) isolates
	// os.SameFile inode detection from size and mtime checks.
	fi1, err := os.Stat(sock1)
	if err != nil {
		t.Fatal(err)
	}
	sockNew := filepath.Join(dir, "d.sock.new")
	if err := os.WriteFile(sockNew, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sockNew, fi1.ModTime(), fi1.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sockNew, sock1); err != nil {
		t.Fatal(err)
	}
	if got := send(t, path, "END set-0/test-3"); got != replyTaken {
		t.Fatalf("END 3: %q", got)
	}
	if got := send(t, path, "START set-0/test-4"); got != replyStartAskedBy("tok-1") {
		t.Fatalf("START 4: %q", got)
	}
	if len(sent) != 3 {
		t.Fatalf("expected INDEX re-delivery after new receiver socket inode; got %d sends", len(sent))
	}

	// Updating ModTime on the same inode (simulating tmpfs/ext4 inode reuse
	// across agent restarts) ALSO triggers INDEX re-delivery.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(sock1, future, future); err != nil {
		t.Fatal(err)
	}
	if got := send(t, path, "END set-0/test-4"); got != replyTaken {
		t.Fatalf("END 4: %q", got)
	}
	if got := send(t, path, "START set-0/test-5"); got != replyStartAskedBy("tok-1") {
		t.Fatalf("START 5: %q", got)
	}
	if len(sent) != 4 {
		t.Fatalf("expected INDEX re-delivery after same-inode mtime change; got %d sends", len(sent))
	}
}

// ktb1Collector acts like enterprise/pkg/service/agent/dedup_table.go's frame
// receiver: verifies magic, big-endian length, maxFrame bound, seq order,
// record count, trailing newline, and SHA-256 across both "table" and "probes" parts.
type ktb1Collector struct {
	mu           sync.Mutex
	maxFrame     int
	curPart      string
	curBuild     string
	curSeq       int
	curPayload   bytes.Buffer
	tableDone    bool
	tableBuild   string
	tablePayload []byte
	tableRecords int64
	tableFrames  int
	probesDone   bool
	probesFrames int
	doneCh       chan struct{}
	doneOnce     sync.Once
	err          error
	replyFn      func(part string, seq int) string
}

func newKTB1Collector(maxFrame int) *ktb1Collector {
	return &ktb1Collector{
		maxFrame: maxFrame,
		doneCh:   make(chan struct{}),
	}
}

func (c *ktb1Collector) handle(conn net.Conn, raw []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(raw) < 8 || string(raw[:4]) != tableFrameMagic {
		c.err = errors.New("bad KTB1 magic")
		return
	}
	if len(raw) > c.maxFrame {
		c.err = errors.New("frame exceeds maxFrame")
		return
	}
	n := int(binary.BigEndian.Uint32(raw[4:8]))
	body := raw[8:]
	if len(body) != n {
		c.err = errors.New("body length mismatch")
		return
	}
	hdrBytes, chunk, ok := bytes.Cut(body, []byte("\n"))
	if !ok {
		c.err = errors.New("missing header newline")
		return
	}
	var hd tableFrameHeader
	if err := json.Unmarshal(hdrBytes, &hd); err != nil {
		c.err = err
		return
	}
	if hd.V != 1 {
		c.err = errors.New("wrong frame version")
		return
	}
	if c.replyFn != nil {
		if r := c.replyFn(hd.Part, hd.Seq); r != "OK\n" {
			if r != "" {
				_, _ = conn.Write([]byte(r))
			}
			return
		}
	}
	if hd.Seq == 0 {
		if hd.Part == partProbes && (!c.tableDone || c.tableBuild != hd.Build) {
			c.err = errors.New("probes did not follow matching table")
			return
		}
		c.curPart = hd.Part
		c.curBuild = hd.Build
		c.curSeq = 0
		c.curPayload.Reset()
	} else if c.curPart != hd.Part || c.curBuild != hd.Build || c.curSeq != hd.Seq {
		c.err = errors.New("out-of-order frame or interleaved stream")
		return
	}
	c.curPayload.Write(chunk)
	c.curSeq++
	if hd.Last {
		if hd.Records == nil {
			c.err = errors.New("last frame missing records")
			return
		}
		all := append([]byte(nil), c.curPayload.Bytes()...)
		if len(all) > 0 && all[len(all)-1] != '\n' {
			c.err = errors.New("stream does not end with newline")
			return
		}
		recs := int64(bytes.Count(all, []byte("\n")))
		if *hd.Records != recs {
			c.err = errors.New("records mismatch")
			return
		}
		sum := sha256.Sum256(all)
		if hex.EncodeToString(sum[:]) != hd.Sha256 {
			c.err = errors.New("sha256 mismatch")
			return
		}
		switch hd.Part {
		case partTable:
			c.tableDone = true
			c.tableBuild = hd.Build
			c.tablePayload = all
			c.tableRecords = recs
			c.tableFrames = c.curSeq
		case partProbes:
			if recs != 0 || len(all) != 0 {
				c.err = errors.New("expected empty probes part for Go")
				return
			}
			c.probesDone = true
			c.probesFrames = c.curSeq
			c.doneOnce.Do(func() { close(c.doneCh) })
		default:
			c.err = errors.New("unknown part")
			return
		}
	}
	_, _ = conn.Write([]byte("OK\n"))
}

// TestTableCommand_KTB1FramingMultiFrameAndErrors verifies:
//   - IGNORED proto=3 <reason> when meta is unavailable or has 0 first-party files
//   - ACK proto=3 caps=... immediately + async multi-frame KTB1 streaming with SHA-256 verification
//   - clean abort on non-OK reply (no further frames sent)
//   - concurrent TABLE asks coalesce cleanly without interleaving frames
func TestTableCommand_KTB1FramingMultiFrameAndErrors(t *testing.T) {
	t.Run("meta unavailable replies IGNORED with reason", func(t *testing.T) {
		stubCoverage(t, nil, nil)
		prepareMeta = func() error { return errors.New("not built with -cover") }
		path := startTestServer(t)
		got := send(t, path, "TABLE")
		if !strings.HasPrefix(got, "IGNORED proto=3 ") || !strings.Contains(got, "not built with -cover") || !strings.HasSuffix(got, "\n") {
			t.Fatalf("TABLE reply = %q, want IGNORED proto=3 <reason>", got)
		}
	})

	t.Run("zero first-party files replies IGNORED with reason", func(t *testing.T) {
		stubCoverage(t, nil, nil)
		meta = &metaFile{
			hash:        [16]byte{1},
			mode:        ctrModeAtomic,
			granularity: ctrGranularityPerBlk,
			pkgs:        []metaPkg{{path: "net/http", skip: true}},
		}
		path := startTestServer(t)
		got := send(t, path, "TABLE")
		if !strings.HasPrefix(got, "IGNORED proto=3 ") || !strings.Contains(got, "no first-party") {
			t.Fatalf("TABLE reply = %q, want IGNORED proto=3 no first-party...", got)
		}
	})

	t.Run("multi-frame KTB1 round-trip and concurrent TABLE coalescing", func(t *testing.T) {
		stubCoverage(t, nil, nil)
		meta = &metaFile{
			hash:        [16]byte{0x42, 0x43, 0x44},
			mode:        ctrModeAtomic,
			granularity: ctrGranularityPerBlk,
			pkgs: []metaPkg{
				{
					path: "example.com/multi/p1",
					funcs: []metaFunc{{
						name: "Alpha",
						file: "example.com/multi/p1/alpha.go",
						units: []unit{
							{stLine: 10, stCol: 1, enLine: 12, enCol: 2, nxStmts: 1},
							{stLine: 14, stCol: 1, enLine: 18, enCol: 2, nxStmts: 2},
						},
					}},
				},
				{
					path: "example.com/multi/p2",
					funcs: []metaFunc{{
						name: "Beta",
						file: "example.com/multi/p2/beta.go",
						units: []unit{
							{stLine: 5, stCol: 1, enLine: 8, enCol: 2, nxStmts: 1},
						},
					}},
				},
			},
		}

		// Lower maxTableFrame to 300 bytes so the table splits across multiple KTB1 frames!
		oldMax := maxTableFrame
		maxTableFrame = 300
		t.Cleanup(func() { maxTableFrame = oldMax })

		k := newKeploySide(t)
		col := newKTB1Collector(maxTableFrame)
		// Pause on frame 0 so concurrent TABLE asks arrive mid-stream!
		frame0Arrived := make(chan struct{})
		releaseFrame0 := make(chan struct{})
		var frame0Once sync.Once
		var totalFramesSeen atomic.Int32
		col.replyFn = func(part string, seq int) string {
			totalFramesSeen.Add(1)
			if part == partTable && seq == 0 {
				frame0Once.Do(func() {
					close(frame0Arrived)
					<-releaseFrame0
				})
			}
			return "OK\n"
		}
		k.onFrame = col.handle

		oldData := dataSocketPath
		dataSocketPath = k.data
		t.Cleanup(func() { dataSocketPath = oldData })

		path := startTestServer(t)

		// First TABLE triggers streaming and returns ACK immediately (does not block on frame 0!).
		if got := send(t, path, "TABLE"); got != replyTaken {
			t.Fatalf("TABLE 1 reply = %q, want %q", got, replyTaken)
		}
		select {
		case <-frame0Arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for frame 0")
		}

		// Fire 6 concurrent TABLE asks while frame 0 is blocked; all must get ACK immediately
		// and coalesce into the single active stream with ZERO interleaving!
		// Use a non-fatal dial helper because goroutines must call t.Errorf, never t.Fatalf.
		sendNoFatal := func(sockPath, command string) (string, error) {
			c, err := net.Dial("unix", sockPath)
			if err != nil {
				return "", err
			}
			defer func() { _ = c.Close() }()
			if _, err := c.Write([]byte(command + "\n")); err != nil {
				return "", err
			}
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			line, err := bufio.NewReader(c).ReadString('\n')
			if err != nil && line == "" {
				return "", err
			}
			return line, nil
		}
		var wg sync.WaitGroup
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := sendNoFatal(path, "TABLE")
				if err != nil || got != replyTaken {
					t.Errorf("concurrent TABLE reply = %q (err=%v), want %q", got, err, replyTaken)
				}
			}()
		}
		wg.Wait()
		close(releaseFrame0)

		select {
		case <-col.doneCh:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for KTB1 stream completion")
		}
		// Wait briefly for runTableStream to finish clearing tableStreaming.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			tableStreamMu.Lock()
			busy := tableStreaming
			tableStreamMu.Unlock()
			if !busy {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		col.mu.Lock()
		defer col.mu.Unlock()
		if col.err != nil {
			t.Fatalf("KTB1 collector error: %v", col.err)
		}
		if !col.tableDone || !col.probesDone {
			t.Fatalf("incomplete pair: tableDone=%v probesDone=%v", col.tableDone, col.probesDone)
		}
		if col.tableFrames < 2 {
			t.Fatalf("expected multi-frame split with maxTableFrame=%d, got %d frames", maxTableFrame, col.tableFrames)
		}
		if int(totalFramesSeen.Load()) != col.tableFrames+col.probesFrames {
			t.Fatalf("concurrent TABLE asks were not coalesced: saw %d total frames, want %d", totalFramesSeen.Load(), col.tableFrames+col.probesFrames)
		}
		units := validateSchema1GoTable(t, col.tablePayload)
		if len(units) != 2 {
			t.Fatalf("decoded %d units, want 2", len(units))
		}
	})

	t.Run("non-OK reply aborts stream cleanly", func(t *testing.T) {
		oldMax := maxTableFrame
		maxTableFrame = 300
		t.Cleanup(func() { maxTableFrame = oldMax })

		m := &metaFile{
			hash:        [16]byte{0x99},
			mode:        ctrModeAtomic,
			granularity: ctrGranularityPerBlk,
			pkgs: []metaPkg{{
				path: "example.com/abort",
				funcs: []metaFunc{{
					name: "Big",
					file: "example.com/abort/big.go",
					units: []unit{
						{stLine: 1, stCol: 1, enLine: 2, enCol: 2, nxStmts: 1},
						{stLine: 3, stCol: 1, enLine: 4, enCol: 2, nxStmts: 1},
						{stLine: 5, stCol: 1, enLine: 6, enCol: 2, nxStmts: 1},
						{stLine: 7, stCol: 1, enLine: 8, enCol: 2, nxStmts: 1},
						{stLine: 9, stCol: 1, enLine: 10, enCol: 2, nxStmts: 1},
					},
				}},
			}},
		}
		tbl, err := buildGoTable(m)
		if err != nil {
			t.Fatal(err)
		}
		k := newKeploySide(t)
		var framesReceived atomic.Int32
		k.onFrame = func(conn net.Conn, raw []byte) {
			if framesReceived.Add(1) == 1 {
				_, _ = conn.Write([]byte("OK\n"))
				return
			}
			_, _ = conn.Write([]byte("ERR bad frame\n"))
		}
		oldData := dataSocketPath
		dataSocketPath = k.data
		t.Cleanup(func() { dataSocketPath = oldData })

		err = streamTable(tbl)
		if err == nil || !strings.Contains(err.Error(), "unexpected frame reply") {
			t.Fatalf("streamTable err = %v, want unexpected frame reply", err)
		}
		if got := framesReceived.Load(); got != 2 {
			t.Fatalf("sent %d frames after non-OK on frame 2, want exactly 2 (clean abort)", got)
		}
	})
}

// TestRealGoApp_IndexTableKTB1AndBlockHits builds a real multi-package Go app
// with keploy-go-cover-build.sh (-cover -covermode=atomic) and verifies
// end-to-end:
//   - INDEX JSON record delivered on startup (and lazily when app starts before agent)
//   - TABLE command streams valid KTB1 frames (table + empty probes) matching INDEX counts
//   - per-test executedBlocksByFile holds exact unit-relative leaf ordinals that
//     map back through the streamed KTB1 table to the exact branches executed
//   - backward compatibility: executedLinesByFile unchanged, twins identical
func TestRealGoApp_IndexTableKTB1AndBlockHits(t *testing.T) {
	k := newKeploySide(t)
	col := newKTB1Collector(maxTableFrame)
	k.onFrame = col.handle

	hook := hookCopy(t, k.sock, k.data)
	mod := appModuleDir(t)
	writeFiles(t, mod, map[string]string{
		"calc/calc.go":    "package calc\n\nfunc Classify(x int) string {\n\tif x > 10 {\n\t\treturn \"big\"\n\t}\n\treturn \"small\"\n}\n",
		"calc_handler.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"strconv\"\n\t\"" + appModule + "/calc\"\n)\n\nfunc init() {\n\thttp.HandleFunc(\"/calc\", func(w http.ResponseWriter, r *http.Request) {\n\t\tv, _ := strconv.Atoi(r.URL.Query().Get(\"x\"))\n\t\tfmt.Fprint(w, calc.Classify(v))\n\t})\n}\n",
	})

	bin, _ := stage(t, hook, mod, nil)
	a := start(t, bin)

	// 1. Run the first test (START delivers Go INDEX lazily before clearing counters).
	rTwin1 := measure(t, k, a, "set-0/twin-1", "/twin?n=1")

	var idx map[string]any
	select {
	case idx = <-k.indexes:
	case <-time.After(10 * time.Second):
		t.Fatalf("no INDEX record received on first START\napp log:\n%s", a.log())
	}
	if idx["kind"] != "index" || idx["v"] != float64(1) || idx["language"] != "go" ||
		idx["producer"] != "go" || idx["precision"] != "block" {
		t.Fatalf("unexpected INDEX record: %v", idx)
	}
	buildStr, _ := idx["build"].(string)
	if !reGoBuildID.MatchString(buildStr) {
		t.Fatalf("INDEX build = %q, want go1-<32hex>", buildStr)
	}
	if pv, _ := idx["producerVersion"].(string); pv == "" || !strings.HasPrefix(pv, "go") {
		t.Fatalf("INDEX producerVersion = %q, want go runtime version", pv)
	}
	idxFiles := int(idx["files"].(float64))
	idxLeaves := int(idx["leaves"].(float64))
	if idxFiles != 3 || idxLeaves == 0 {
		t.Fatalf("INDEX files=%d leaves=%d, want 3 first-party files and >0 leaves", idxFiles, idxLeaves)
	}

	// 2. Ask TABLE and verify full KTB1 round-trip + schema-1 validation.
	if got := send(t, k.sock, "TABLE"); got != replyTaken {
		t.Fatalf("TABLE reply = %q, want %q\n%s", got, replyTaken, a.log())
	}
	select {
	case <-col.doneCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("TABLE stream did not complete\n%s", a.log())
	}
	col.mu.Lock()
	if col.err != nil {
		col.mu.Unlock()
		t.Fatalf("KTB1 stream error: %v", col.err)
	}
	if col.tableBuild != buildStr {
		col.mu.Unlock()
		t.Fatalf("KTB1 build %q != INDEX build %q", col.tableBuild, buildStr)
	}
	tableBytes := append([]byte(nil), col.tablePayload...)
	col.mu.Unlock()

	units := validateSchema1GoTable(t, tableBytes)
	if len(units) != idxFiles {
		t.Fatalf("table units = %d, INDEX files = %d", len(units), idxFiles)
	}
	unitLeavesByPath := map[string][]tableLeaf{}
	totalLeaves := 0
	for _, u := range units {
		var flat []tableLeaf
		for _, meth := range u.Methods {
			flat = append(flat, meth.Leaves...)
		}
		unitLeavesByPath[u.Path] = flat
		totalLeaves += len(flat)
	}
	if totalLeaves != idxLeaves {
		t.Fatalf("table total leaves = %d, INDEX leaves = %d", totalLeaves, idxLeaves)
	}

	// 3. Measure tests and verify unit-relative executedBlocksByFile match exact branches!
	blocksOf := func(rec map[string]any) map[string][]int {
		raw, ok := rec["executedBlocksByFile"].(map[string]any)
		if !ok {
			t.Fatalf("record missing executedBlocksByFile: %v", rec)
		}
		out := make(map[string][]int, len(raw))
		for file, v := range raw {
			arr, ok := v.([]any)
			if !ok {
				t.Fatalf("executedBlocksByFile[%q] = %T, want []any", file, v)
			}
			for _, elem := range arr {
				out[file] = append(out[file], int(elem.(float64)))
			}
		}
		return out
	}

	rTwin2 := measure(t, k, a, "set-0/twin-2", "/twin?n=2")
	rShallow := measure(t, k, a, "set-0/shallow", "/unique")
	rDeep := measure(t, k, a, "set-0/deep", "/unique?deep=1")
	rCalcBig := measure(t, k, a, "set-0/calc-big", "/calc?x=42")
	rCalcSmall := measure(t, k, a, "set-0/calc-small", "/calc?x=3")

	if !reflect.DeepEqual(blocksOf(rTwin1), blocksOf(rTwin2)) {
		t.Fatalf("twin requests produced different executedBlocksByFile: %v vs %v", blocksOf(rTwin1), blocksOf(rTwin2))
	}

	// Map each test's unit-relative block hits back through the KTB1 table and
	// verify every leaf ordinal is in-bounds AND its lines match executedLinesByFile!
	for _, tc := range []struct {
		name string
		rec  map[string]any
	}{
		{"twin-1", rTwin1},
		{"shallow", rShallow},
		{"deep", rDeep},
		{"calc-big", rCalcBig},
		{"calc-small", rCalcSmall},
	} {
		blks := blocksOf(tc.rec)
		execLines := linesOfRecord(t, tc.rec)
		if len(blks) != len(execLines) {
			t.Fatalf("%s: files in blocks (%v) != files in lines (%v)", tc.name, blks, execLines)
		}
		for file, ords := range blks {
			leaves, ok := unitLeavesByPath[file]
			if !ok {
				t.Fatalf("%s: executedBlocksByFile names %q not in KTB1 table", tc.name, file)
			}
			derivedLines := map[int]bool{}
			for i, ord := range ords {
				if ord < 0 || ord >= len(leaves) {
					t.Fatalf("%s: %s ord %d out of range [0, %d)", tc.name, file, ord, len(leaves))
				}
				if i > 0 && ords[i] <= ords[i-1] {
					t.Fatalf("%s: %s ords not strictly sorted unique: %v", tc.name, file, ords)
				}
				lf := leaves[ord]
				for l := lf[0]; l <= lf[2]; l++ {
					derivedLines[l] = true
				}
			}
			if len(derivedLines) != len(execLines[file]) {
				t.Fatalf("%s: %s lines from blocks (%v) != executedLinesByFile (%v)", tc.name, file, derivedLines, execLines[file])
			}
			for _, l := range execLines[file] {
				if !derivedLines[l] {
					t.Fatalf("%s: %s executed line %d missing from block leaves", tc.name, file, l)
				}
			}
		}
	}

	// Verify branch distinction at block level: shallow vs deep, calc-big vs calc-small.
	if reflect.DeepEqual(blocksOf(rShallow)[appModule+"/main.go"], blocksOf(rDeep)[appModule+"/main.go"]) {
		t.Fatalf("shallow and deep hit the same blocks: %v", blocksOf(rShallow))
	}
	calcFile := appModule + "/calc/calc.go"
	if reflect.DeepEqual(blocksOf(rCalcBig)[calcFile], blocksOf(rCalcSmall)[calcFile]) {
		t.Fatalf("calc-big and calc-small hit the same blocks: %v", blocksOf(rCalcBig)[calcFile])
	}
}

// TestRealGoApp_StartedBeforeAgentDeliversIndexLazily verifies that when a Go
// app starts BEFORE Keploy's data socket exists, the first START delivers
// INDEX lazily, and replacing the data socket (agent restart) re-delivers INDEX.
func TestRealGoApp_StartedBeforeAgentDeliversIndexLazily(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "c.sock")
	dataPath := filepath.Join(dir, "d.sock")

	hook := hookCopy(t, sockPath, dataPath)
	mod := appModuleDir(t)
	bin, _ := stage(t, hook, mod, nil)

	// Start the app BEFORE dataPath exists!
	a := start(t, bin)

	listenData := func() (net.Listener, chan map[string]any) {
		_ = os.Remove(dataPath)
		ln, err := net.Listen("unix", dataPath)
		if err != nil {
			t.Fatal(err)
		}
		ch := make(chan map[string]any, 16)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				b, _ := io.ReadAll(c)
				_ = c.Close()
				var rec map[string]any
				if json.Unmarshal(b, &rec) == nil {
					ch <- rec
				}
			}
		}()
		return ln, ch
	}

	// Receiver 1 appears after app startup -> first START delivers INDEX.
	ln1, ch1 := listenData()
	t.Cleanup(func() { _ = ln1.Close() })
	if got := send(t, sockPath, "START set-0/test-1"); got != replyStartUnasked {
		t.Fatalf("START 1: %q", got)
	}
	select {
	case rec := <-ch1:
		if rec["kind"] != "index" || rec["language"] != "go" {
			t.Fatalf("receiver 1 first record = %v, want Go INDEX", rec)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("receiver 1 got no lazy INDEX on START\n%s", a.log())
	}
	if got := send(t, sockPath, "END set-0/test-1"); got != replyTaken {
		t.Fatalf("END 1: %q", got)
	}
	select {
	case rec := <-ch1:
		if rec["id"] != "set-0/test-1" {
			t.Fatalf("receiver 1 END record = %v", rec)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiver 1 got no END record")
	}

	// Second START on the same real Unix data socket must NOT re-send INDEX.
	if got := send(t, sockPath, "START set-0/test-2"); got != replyStartUnasked {
		t.Fatalf("START 1b: %q", got)
	}
	if got := send(t, sockPath, "END set-0/test-2"); got != replyTaken {
		t.Fatalf("END 1b: %q", got)
	}
	select {
	case rec := <-ch1:
		if rec["id"] != "set-0/test-2" {
			t.Fatalf("receiver 1 sent unexpected record on second START/END (re-sent INDEX?): %v", rec)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiver 1 got no second END record")
	}

	// Receiver 2 replaces data socket -> next START re-delivers INDEX!
	// Wait past a coarse filesystem timestamp tick so even eager tmpfs/ext4
	// inode reuse produces a distinct mtime, matching real agent restarts.
	_ = ln1.Close()
	time.Sleep(25 * time.Millisecond)
	ln2, ch2 := listenData()
	t.Cleanup(func() { _ = ln2.Close() })
	if got := send(t, sockPath, "START set-1/test-1"); got != replyStartUnasked {
		t.Fatalf("START 2: %q", got)
	}
	select {
	case rec := <-ch2:
		if rec["kind"] != "index" || rec["language"] != "go" {
			t.Fatalf("receiver 2 first record = %v, want re-delivered Go INDEX", rec)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("receiver 2 got no re-delivered INDEX on START\n%s", a.log())
	}
}

// TestTableCommand_CoalescedRetryAfterFailure verifies that if a TABLE ask
// arrives while an earlier stream is failing, the single streamer goroutine
// retries once cleanly after the failed stream aborts (never interleaving).
func TestTableCommand_CoalescedRetryAfterFailure(t *testing.T) {
	stubCoverage(t, nil, nil)
	meta = &metaFile{
		hash:        [16]byte{0x77},
		mode:        ctrModeAtomic,
		granularity: ctrGranularityPerBlk,
		pkgs: []metaPkg{{
			path: "example.com/retry",
			funcs: []metaFunc{{
				name:  "F",
				file:  "example.com/retry/f.go",
				units: []unit{{stLine: 3, stCol: 1, enLine: 4, enCol: 2, nxStmts: 1}},
			}},
		}},
	}

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDone := make(chan struct{})
	var calls atomic.Int32
	var inFlight atomic.Int32
	var overlapped atomic.Bool

	oldStream := streamTableFn
	streamTableFn = func(tbl *goTable) error {
		if inFlight.Add(1) > 1 {
			overlapped.Store(true)
		}
		defer inFlight.Add(-1)
		n := calls.Add(1)
		if n == 1 {
			close(firstStarted)
			<-releaseFirst
			return errors.New("simulated stream break")
		}
		if n == 2 {
			close(secondDone)
		}
		return nil
	}
	t.Cleanup(func() {
		// Wait until streamer finishes before restoring streamTableFn.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			tableStreamMu.Lock()
			busy := tableStreaming
			tableStreamMu.Unlock()
			if !busy {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		streamTableFn = oldStream
	})

	path := startTestServer(t)
	if got := send(t, path, "TABLE"); got != replyTaken {
		t.Fatalf("TABLE 1: %q", got)
	}
	<-firstStarted
	// Second TABLE arrives while first stream is in flight and will fail.
	if got := send(t, path, "TABLE"); got != replyTaken {
		t.Fatalf("TABLE 2: %q", got)
	}
	close(releaseFirst)

	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("coalesced retry did not run after failed stream")
	}
	if overlapped.Load() {
		t.Fatal("two table streams ran concurrently")
	}
	if calls.Load() != 2 {
		t.Fatalf("streamTableFn called %d times, want 2", calls.Load())
	}
}

// TestWireContractExactBytes pins fixed literal wire bytes for every artifact
// shared with enterprise and api-server (canonical schema-1 Go Unit NDJSON,
// unit SHA-256 hash, NDJSON stream SHA-256, full KTB1 table and empty probes
// frames, Go INDEX JSON, and per-test JSON carrying executedBlocksByFile via
// reportCoverage's snapshotCoverage seam).
func TestWireContractExactBytes(t *testing.T) {
	m := &metaFile{
		hash:        [16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10},
		mode:        ctrModeAtomic,
		granularity: ctrGranularityPerBlk,
		pkgs: []metaPkg{{
			path: "example.com/app",
			funcs: []metaFunc{{
				name:  "Handle<T>&1",
				file:  "example.com/app/main.go",
				units: []unit{{stLine: 10, stCol: 2, enLine: 12, enCol: 5, nxStmts: 2}},
			}},
		}},
	}
	tbl, err := buildGoTable(m)
	if err != nil {
		t.Fatal(err)
	}

	// Independently computed SHA-256 golden vectors (do not derive from cover's helpers).
	const (
		wantBuild     = "go1-0123456789abcdeffedcba9876543210"
		wantUnitHash  = "ba41a113d929a63b1063aa513b4cac723bf07b10052ed1b3d048b230fb83c126"
		wantNDJSON    = `{"kind":"file","id":"f:example.com/app/main.go","pkg":"example.com/app","name":"main.go","path":"example.com/app/main.go","hash":"ba41a113d929a63b1063aa513b4cac723bf07b10052ed1b3d048b230fb83c126","state":"ok","methods":[{"id":"m:example.com/app/main.go#Handle<T>&1@10","name":"Handle<T>&1","line":10,"leaves":[[10,2,12,5,2,0]]}]}` + "\n"
		wantNDJSONSha = "bb98f99d43ea009d1e46819ff02769c50208f7d2772cb11cfecf196a50ac34ad"
	)
	if tbl.build != wantBuild {
		t.Fatalf("build = %q, want %q", tbl.build, wantBuild)
	}
	if tbl.unitCount() != 1 || tbl.unitPaths[0] != "example.com/app/main.go" {
		t.Fatalf("unitPaths = %v, want [example.com/app/main.go]", tbl.unitPaths)
	}
	if string(tbl.ndjson) != wantNDJSON {
		t.Fatalf("NDJSON:\ngot:  %s\nwant: %s", tbl.ndjson, wantNDJSON)
	}
	if tbl.ndjsonSha != wantNDJSONSha {
		t.Fatalf("ndjsonSha = %q, want %q", tbl.ndjsonSha, wantNDJSONSha)
	}
	if !strings.Contains(string(tbl.ndjson), `"hash":"`+wantUnitHash+`"`) {
		t.Fatalf("NDJSON missing expected unit hash %s", wantUnitHash)
	}

	// Exact Go INDEX JSON bytes.
	idxBytes, err := tbl.indexBytes()
	if err != nil {
		t.Fatal(err)
	}
	wantIndex := `{"kind":"index","v":1,"language":"go","build":"` + wantBuild + `","producer":"go","producerVersion":"` + runtime.Version() + `","precision":"block","files":1,"leaves":1}`
	if string(idxBytes) != wantIndex {
		t.Fatalf("INDEX:\ngot:  %s\nwant: %s", idxBytes, wantIndex)
	}

	// Exact KTB1 wire frames for both part:"table" and empty part:"probes".
	oldSendFrame := sendTableFrame
	var frames [][]byte
	sendTableFrame = func(frame []byte) error {
		frames = append(frames, append([]byte(nil), frame...))
		return nil
	}
	t.Cleanup(func() { sendTableFrame = oldSendFrame })

	if err := streamTable(tbl); err != nil {
		t.Fatalf("streamTable: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d KTB1 frames, want 2 (table + empty probes)", len(frames))
	}
	wantTableBody := `{"v":1,"part":"table","build":"` + wantBuild + `","seq":0,"last":true,"records":1,"sha256":"` + wantNDJSONSha + `"}` + "\n" + wantNDJSON
	wantTableFrame := append(binary.BigEndian.AppendUint32([]byte("KTB1"), uint32(len(wantTableBody))), wantTableBody...)
	if !bytes.Equal(frames[0], wantTableFrame) {
		t.Fatalf("table KTB1 frame mismatch:\ngot:  %q\nwant: %q", frames[0], wantTableFrame)
	}
	wantProbesBody := `{"v":1,"part":"probes","build":"` + wantBuild + `","seq":0,"last":true,"records":0,"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}` + "\n"
	wantProbesFrame := append(binary.BigEndian.AppendUint32([]byte("KTB1"), uint32(len(wantProbesBody))), wantProbesBody...)
	if !bytes.Equal(frames[1], wantProbesFrame) {
		t.Fatalf("probes KTB1 frame mismatch:\ngot:  %q\nwant: %q", frames[1], wantProbesFrame)
	}

	// Exact non-last multi-frame KTB1 header bytes (records and sha256 omitted on last:false).
	oldMax := maxTableFrame
	maxTableFrame = 300
	t.Cleanup(func() { maxTableFrame = oldMax })
	frames = nil
	if err := streamTable(tbl); err != nil {
		t.Fatalf("multi-frame streamTable: %v", err)
	}
	if len(frames) < 3 {
		t.Fatalf("expected >= 3 frames with maxTableFrame=300, got %d", len(frames))
	}
	hdr0, _, ok := bytes.Cut(frames[0][8:], []byte("\n"))
	if !ok {
		t.Fatalf("frame 0 missing header newline")
	}
	wantNonLastHdr := `{"v":1,"part":"table","build":"` + wantBuild + `","seq":0,"last":false}`
	if string(hdr0) != wantNonLastHdr {
		t.Fatalf("non-last KTB1 header:\ngot:  %s\nwant: %s", hdr0, wantNonLastHdr)
	}

	// Tiny maxTableFrame on empty payload must return an error instead of looping.
	maxTableFrame = 32
	if err := streamTablePart(partProbes, wantBuild, nil, 0, emptySHA256); err == nil {
		t.Fatal("streamTablePart with maxTableFrame=32 on empty payload succeeded, want error")
	}
	maxTableFrame = oldMax

	// Exact per-test coverage JSON bytes carrying executedBlocksByFile via reportCoverage's
	// snapshotCoverage seam (both clean and contaminated+overlap records).
	stubCoverage(t, nil, nil)
	var recBytes []byte
	sendRecord = func(b []byte) error {
		recBytes = append([]byte(nil), b...)
		return nil
	}
	snapshotCoverage = func() (map[string][]int, map[string][]int, error) {
		return map[string][]int{"example.com/app/main.go": {10, 11, 12}},
			map[string][]int{"example.com/app/main.go": {0}}, nil
	}
	if err := reportCoverage("set-0/test-1"); err != nil {
		t.Fatalf("reportCoverage: %v", err)
	}
	const wantCleanRec = `{"executedBlocksByFile":{"example.com/app/main.go":[0]},"executedLinesByFile":{"example.com/app/main.go":[10,11,12]},"id":"set-0/test-1"}`
	if string(recBytes) != wantCleanRec {
		t.Fatalf("clean block record:\ngot:  %s\nwant: %s", recBytes, wantCleanRec)
	}

	everGap["example.com/app/main.go"] = map[int]struct{}{11: {}}
	if err := reportCoverage("set-0/test-2"); err != nil {
		t.Fatalf("reportCoverage contaminated: %v", err)
	}
	const wantPartialRec = `{"contaminated":["between-tests:example.com/app/main.go"],"executedBlocksByFile":{"example.com/app/main.go":[0]},"executedLinesByFile":{"example.com/app/main.go":[10,11,12]},"id":"set-0/test-2","overlapLinesByFile":{"example.com/app/main.go":[11]}}`
	if string(recBytes) != wantPartialRec {
		t.Fatalf("contaminated block record:\ngot:  %s\nwant: %s", recBytes, wantPartialRec)
	}
}
