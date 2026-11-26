package cover

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// tableFrameMagic is the 4-byte prefix of every KTB1 table frame
	// (enterprise/pkg/service/agent/dedup_table.go).
	tableFrameMagic = "KTB1"
	partTable       = "table"
	partProbes      = "probes"

	// maxLeafLines is api-server's per-leaf line span bound (1 << 16 = 65536).
	maxLeafLines = 1 << 16
	// maxLeafVal is api-server's per-field integer bound (1 << 30).
	maxLeafVal = 1 << 30

	// indexRecordV is the schema version of the INDEX record.
	indexRecordV = 1
	// tableFrameV is the protocol version of KTB1 frames.
	tableFrameV = 1

	// emptySHA256 is the hex SHA-256 of zero bytes, used for the empty probes part.
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// maxTableFrame bounds one KTB1 frame (8-byte prefix + body), matching
// enterprise/pkg/service/agent/dedup_table.go (1 MiB). A variable so tests
// can lower it to exercise multi-frame streaming.
var maxTableFrame = 1 << 20

// tableReadTimeout bounds waiting for the agent's "OK\n" reply to one KTB1 frame.
const tableReadTimeout = 30 * time.Second

// tableLeaf is one schema-1 Go leaf: [sl, sc, el, ec, nstmts, kind] with kind=0.
type tableLeaf [6]int

// tableMethod is one schema-1 Go function record in a Unit.
type tableMethod struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Line   int         `json:"line"`
	Leaves []tableLeaf `json:"leaves"`
}

// tableUnit is one schema-1 Go file record of the build table, matching
// enterprise/pkg/service/codecoverage.Unit byte-for-byte.
type tableUnit struct {
	Kind    string        `json:"kind"`
	ID      string        `json:"id"`
	Pkg     string        `json:"pkg"`
	Name    string        `json:"name"`
	Path    string        `json:"path"`
	Hash    string        `json:"hash"`
	State   string        `json:"state"`
	Methods []tableMethod `json:"methods"`
}

// indexRecord is the generic INDEX JSON record sent on /tmp/coverage_data.sock.
// "kind" is the first key so the agent's recordKind identifies it in O(1).
type indexRecord struct {
	Kind            string `json:"kind"`
	V               int    `json:"v"`
	Language        string `json:"language"`
	Build           string `json:"build"`
	Producer        string `json:"producer"`
	ProducerVersion string `json:"producerVersion"`
	Precision       string `json:"precision"`
	Files           int    `json:"files"`
	Leaves          int    `json:"leaves"`
}

// tableFrameHeader is the JSON header line of a KTB1 frame.
type tableFrameHeader struct {
	V       int    `json:"v"`
	Part    string `json:"part"`
	Build   string `json:"build"`
	Seq     int    `json:"seq"`
	Last    bool   `json:"last"`
	Records *int64 `json:"records,omitempty"`
	Sha256  string `json:"sha256,omitempty"`
}

// funcBlockMap maps one metaFunc's block indices (0..len(fn.units)-1) to its
// canonical unit index and unit-relative leaf ordinals (0..unitLeaves-1).
type funcBlockMap struct {
	unitIdx  int32   // index into goTable.units, or -1 if skipped/no valid leaves
	leafOrds []int32 // len == len(fn.units); -1 if that block was invalid/skipped
}

// goTable is the immutable canonical Go build table derived from metaFile.
// Only the encoded NDJSON stream, unit paths, counts, and O(1) block lookup
// are retained after encoding so full method/leaf trees are not held twice.
type goTable struct {
	build       string
	precision   string
	unitPaths   []string
	totalLeaves int
	ndjson      []byte
	ndjsonSha   string
	// pkgFuncs[pkgIdx][funcIdx] provides O(1) mapping from (pkgIdx, funcIdx, unitIdx)
	// to (unitPath, relLeafIdx).
	pkgFuncs [][]funcBlockMap
}

func (t *goTable) unitCount() int {
	if t == nil {
		return 0
	}
	return len(t.unitPaths)
}

// funcBlockLookup resolves (pkgIdx, funcIdx) to its funcBlockMap and canonical
// unitPath in O(1), returning (nil, "") when skipped or out of range.
func (t *goTable) funcBlockLookup(pkgIdx, funcIdx int) (*funcBlockMap, string) {
	if t == nil || pkgIdx < 0 || pkgIdx >= len(t.pkgFuncs) {
		return nil, ""
	}
	funcs := t.pkgFuncs[pkgIdx]
	if funcIdx < 0 || funcIdx >= len(funcs) {
		return nil, ""
	}
	fm := &funcs[funcIdx]
	if fm.unitIdx < 0 {
		return nil, ""
	}
	return fm, t.unitPaths[fm.unitIdx]
}

// indexBytes encodes the Go INDEX JSON record for t with HTML escaping off.
func (t *goTable) indexBytes() ([]byte, error) {
	rec := indexRecord{
		Kind:            "index",
		V:               indexRecordV,
		Language:        "go",
		Build:           t.build,
		Producer:        "go",
		ProducerVersion: runtime.Version(),
		Precision:       t.precision,
		Files:           len(t.unitPaths),
		Leaves:          t.totalLeaves,
	}
	return marshalJSONNoEscape(rec)
}

// taggedLeaf binds a canonical tableLeaf to its source position in metaFile so
// unit-relative ordinals are assigned in canonical sorted order.
type taggedLeaf struct {
	leaf     tableLeaf
	pkgIdx   int
	funcIdx  int
	blockIdx int
}

// buildGoTable builds the canonical schema-1 Go build table and O(1)
// block-to-unit-relative-leaf mapping from m.
func buildGoTable(m *metaFile) (*goTable, error) {
	if m == nil {
		return nil, fmt.Errorf("no coverage meta-data")
	}
	build := formatGoBuildID(m.hash)
	precision := "block"
	if m.granularity == ctrGranularityPerFunc {
		precision = "func"
	}

	type pendingFunc struct {
		name    string
		line    int
		pkgIdx  int
		funcIdx int
		leaves  []taggedLeaf
		id      string
	}
	type pendingUnit struct {
		pkg   string
		name  string
		path  string
		funcs []pendingFunc
	}

	pkgFuncs := make([][]funcBlockMap, len(m.pkgs))
	unitMap := make(map[string]*pendingUnit)

	for pkgIdx := range m.pkgs {
		mp := &m.pkgs[pkgIdx]
		fms := make([]funcBlockMap, len(mp.funcs))
		for i := range fms {
			fms[i].unitIdx = -1
		}
		pkgFuncs[pkgIdx] = fms
		if mp.skip {
			continue
		}
		pkg := mp.path
		if pkg == "" || strings.HasPrefix(pkg, "/") || strings.HasSuffix(pkg, "/") || strings.Contains(pkg, "//") {
			continue
		}
		for funcIdx := range mp.funcs {
			fn := &mp.funcs[funcIdx]
			name := baseFileName(fn.file)
			if name == "" || name == "." || name == "/" || strings.Contains(name, "/") {
				continue
			}
			var leaves []taggedLeaf
			minLine := 0
			for blockIdx, u := range fn.units {
				lf, ok := clampLeaf(u)
				if !ok {
					continue
				}
				if minLine == 0 || lf[0] < minLine {
					minLine = lf[0]
				}
				leaves = append(leaves, taggedLeaf{
					leaf:     lf,
					pkgIdx:   pkgIdx,
					funcIdx:  funcIdx,
					blockIdx: blockIdx,
				})
			}
			if len(leaves) == 0 {
				continue
			}
			sort.SliceStable(leaves, func(i, j int) bool {
				return compareTableLeaf(leaves[i].leaf, leaves[j].leaf) < 0
			})
			unitPath := pkg + "/" + name
			pu := unitMap[unitPath]
			if pu == nil {
				pu = &pendingUnit{pkg: pkg, name: name, path: unitPath}
				unitMap[unitPath] = pu
			}
			pu.funcs = append(pu.funcs, pendingFunc{
				name:    fn.name,
				line:    minLine,
				pkgIdx:  pkgIdx,
				funcIdx: funcIdx,
				leaves:  leaves,
			})
		}
	}

	punits := make([]*pendingUnit, 0, len(unitMap))
	for _, pu := range unitMap {
		punits = append(punits, pu)
	}
	sort.Slice(punits, func(i, j int) bool {
		if c := comparePkg(punits[i].pkg, punits[j].pkg); c != 0 {
			return c < 0
		}
		return "f:"+punits[i].path < "f:"+punits[j].path
	})

	units := make([]tableUnit, len(punits))
	totalLeaves := 0
	for ui, pu := range punits {
		// Sort deterministically before disambiguating colliding (name, line)
		// pairs so method IDs never depend on map iteration order.
		sort.Slice(pu.funcs, func(i, j int) bool {
			a, b := &pu.funcs[i], &pu.funcs[j]
			if a.line != b.line {
				return a.line < b.line
			}
			if a.name != b.name {
				return a.name < b.name
			}
			if c := compareTaggedLeafSlice(a.leaves, b.leaves); c != 0 {
				return c < 0
			}
			if a.pkgIdx != b.pkgIdx {
				return a.pkgIdx < b.pkgIdx
			}
			return a.funcIdx < b.funcIdx
		})

		usedIDs := make(map[string]bool, len(pu.funcs))
		for fi := range pu.funcs {
			pf := &pu.funcs[fi]
			base := "m:" + pu.path + "#" + pf.name + "@" + strconv.Itoa(pf.line)
			id := base
			if usedIDs[id] {
				for seq := 2; ; seq++ {
					cand := base + "#" + strconv.Itoa(seq)
					if !usedIDs[cand] {
						id = cand
						break
					}
				}
			}
			usedIDs[id] = true
			pf.id = id
		}

		// Final canonical method order: (line, id), matching
		// enterprise/pkg/service/codecoverage/table.go and api-server.
		sort.Slice(pu.funcs, func(i, j int) bool {
			if pu.funcs[i].line != pu.funcs[j].line {
				return pu.funcs[i].line < pu.funcs[j].line
			}
			return pu.funcs[i].id < pu.funcs[j].id
		})

		methods := make([]tableMethod, len(pu.funcs))
		relOrd := int32(0)
		for fi, pf := range pu.funcs {
			fm := &pkgFuncs[pf.pkgIdx][pf.funcIdx]
			fm.unitIdx = int32(ui)
			numUnits := len(m.pkgs[pf.pkgIdx].funcs[pf.funcIdx].units)
			ords := make([]int32, numUnits)
			for k := range ords {
				ords[k] = -1
			}
			mLeaves := make([]tableLeaf, len(pf.leaves))
			for li, tl := range pf.leaves {
				mLeaves[li] = tl.leaf
				ords[tl.blockIdx] = relOrd
				relOrd++
			}
			fm.leafOrds = ords
			methods[fi] = tableMethod{
				ID:     pf.id,
				Name:   pf.name,
				Line:   pf.line,
				Leaves: mLeaves,
			}
		}

		methodsJSON, err := marshalJSONNoEscape(methods)
		if err != nil {
			return nil, fmt.Errorf("hash unit %s: %w", pu.path, err)
		}
		hsum := sha256.Sum256(methodsJSON)

		units[ui] = tableUnit{
			Kind:    "file",
			ID:      "f:" + pu.path,
			Pkg:     pu.pkg,
			Name:    pu.name,
			Path:    pu.path,
			Hash:    hex.EncodeToString(hsum[:]),
			State:   "ok",
			Methods: methods,
		}
		totalLeaves += int(relOrd)
	}

	unitPaths := make([]string, len(units))
	var ndjsonBuf bytes.Buffer
	enc := json.NewEncoder(&ndjsonBuf)
	enc.SetEscapeHTML(false)
	for i := range units {
		unitPaths[i] = units[i].Path
		if err := enc.Encode(units[i]); err != nil {
			return nil, fmt.Errorf("encode table unit %s: %w", units[i].ID, err)
		}
	}
	ndjson := ndjsonBuf.Bytes()
	nsum := sha256.Sum256(ndjson)

	return &goTable{
		build:       build,
		precision:   precision,
		unitPaths:   unitPaths,
		totalLeaves: totalLeaves,
		ndjson:      ndjson,
		ndjsonSha:   hex.EncodeToString(nsum[:]),
		pkgFuncs:    pkgFuncs,
	}, nil
}

// baseFileName returns the base file name of a Go coverage meta file path,
// handling both Unix and Windows separators.
func baseFileName(file string) string {
	if file == "" {
		return ""
	}
	if strings.Contains(file, "\\") {
		file = strings.ReplaceAll(file, "\\", "/")
	}
	return path.Base(file)
}

// clampLeaf validates and clamps one Go coverage unit for api-server's
// tableBuilder (maxLeafLines = 65536, values in 0..1<<30).
func clampLeaf(u unit) (tableLeaf, bool) {
	if u.stLine == 0 || u.enLine < u.stLine || (u.enLine == u.stLine && u.enCol < u.stCol) {
		return tableLeaf{}, false
	}
	if u.stLine > maxLeafVal {
		return tableLeaf{}, false
	}
	el := u.enLine
	if uint64(el)-uint64(u.stLine)+1 > maxLeafLines {
		el = u.stLine + maxLeafLines - 1
	}
	if el > maxLeafVal {
		el = maxLeafVal
	}
	sc := u.stCol
	if sc > maxLeafVal {
		sc = maxLeafVal
	}
	ec := u.enCol
	if ec > maxLeafVal {
		ec = maxLeafVal
	}
	n := u.nxStmts
	if n > maxLeafVal {
		n = maxLeafVal
	}
	if el == u.stLine && ec < sc {
		return tableLeaf{}, false
	}
	return tableLeaf{int(u.stLine), int(sc), int(el), int(ec), int(n), 0}, true
}

// comparePkg orders package paths segment by segment, matching
// enterprise/pkg/service/codecoverage/table.go: a < a/b < a/b/c < a-x.
func comparePkg(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		ca, cb := a[i], b[i]
		if ca == cb {
			continue
		}
		if ca == '/' {
			return -1
		}
		if cb == '/' {
			return 1
		}
		if ca < cb {
			return -1
		}
		return 1
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// compareTableLeaf orders leaves by (sl, kind, sc, el, ec, n), matching
// enterprise/pkg/service/codecoverage/table.go.
func compareTableLeaf(a, b tableLeaf) int {
	if a[0] != b[0] {
		return compareInt(a[0], b[0])
	}
	if a[5] != b[5] {
		return compareInt(a[5], b[5])
	}
	if a[1] != b[1] {
		return compareInt(a[1], b[1])
	}
	if a[2] != b[2] {
		return compareInt(a[2], b[2])
	}
	if a[3] != b[3] {
		return compareInt(a[3], b[3])
	}
	return compareInt(a[4], b[4])
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareTaggedLeafSlice(a, b []taggedLeaf) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if c := compareTableLeaf(a[i].leaf, b[i].leaf); c != 0 {
			return c
		}
	}
	return compareInt(len(a), len(b))
}

// marshalJSONNoEscape marshals v to compact JSON without HTML escaping and
// without a trailing newline.
func marshalJSONNoEscape(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

// streamTable sends the Go build table to Keploy's data socket as KTB1 frames:
// first the "table" part (NDJSON unit records), then an empty "probes" part
// (matching enterprise/pkg/service/agent/dedup_table.go's pair state machine).
func streamTable(t *goTable) error {
	if t == nil {
		return fmt.Errorf("no build table to stream")
	}
	if err := streamTablePart(partTable, t.build, t.ndjson, int64(len(t.unitPaths)), t.ndjsonSha); err != nil {
		return err
	}
	return streamTablePart(partProbes, t.build, nil, 0, emptySHA256)
}

// streamTablePart cuts payload into KTB1 frames of at most maxTableFrame bytes
// each (8-byte prefix + header + '\n' + chunk) and sends each frame on its own
// connection to dataSocketPath, verifying the agent's "OK\n" reply per frame.
func streamTablePart(part, build string, payload []byte, records int64, sha string) error {
	seq := 0
	off := 0
	for {
		rem := len(payload) - off
		lastHdr, err := marshalJSONNoEscape(tableFrameHeader{
			V:       tableFrameV,
			Part:    part,
			Build:   build,
			Seq:     seq,
			Last:    true,
			Records: &records,
			Sha256:  sha,
		})
		if err != nil {
			return fmt.Errorf("marshal %s frame %d header: %w", part, seq, err)
		}
		if 8+len(lastHdr)+1+rem <= maxTableFrame {
			frame := buildKTB1Frame(lastHdr, payload[off:])
			if err := sendTableFrame(frame); err != nil {
				return fmt.Errorf("send %s frame %d: %w", part, seq, err)
			}
			return nil
		}
		if rem == 0 {
			return fmt.Errorf("maxTableFrame %d is too small for %s final frame %d header (%d bytes)", maxTableFrame, part, seq, len(lastHdr))
		}
		hdr, err := marshalJSONNoEscape(tableFrameHeader{
			V:     tableFrameV,
			Part:  part,
			Build: build,
			Seq:   seq,
			Last:  false,
		})
		if err != nil {
			return fmt.Errorf("marshal %s frame %d header: %w", part, seq, err)
		}
		avail := maxTableFrame - 8 - len(hdr) - 1
		if avail <= 0 {
			return fmt.Errorf("maxTableFrame %d is too small for %s frame %d header (%d bytes)", maxTableFrame, part, seq, len(hdr))
		}
		if avail > rem {
			avail = rem
		}
		frame := buildKTB1Frame(hdr, payload[off:off+avail])
		if err := sendTableFrame(frame); err != nil {
			return fmt.Errorf("send %s frame %d: %w", part, seq, err)
		}
		off += avail
		seq++
	}
}

// buildKTB1Frame builds one wire KTB1 frame: "KTB1" + uint32 BE len(body) +
// headerJSON + "\n" + chunk.
func buildKTB1Frame(headerJSON, chunk []byte) []byte {
	bodyLen := len(headerJSON) + 1 + len(chunk)
	frame := make([]byte, 8+bodyLen)
	copy(frame[:4], tableFrameMagic)
	binary.BigEndian.PutUint32(frame[4:8], uint32(bodyLen))
	copy(frame[8:], headerJSON)
	frame[8+len(headerJSON)] = '\n'
	copy(frame[8+len(headerJSON)+1:], chunk)
	return frame
}

// sendTableFrame sends one KTB1 frame on a Unix connection of its own to
// dataSocketPath and waits for the agent's "OK\n" reply. A variable for tests.
var sendTableFrame = sendKTB1FrameToSocket

func sendKTB1FrameToSocket(frame []byte) error {
	conn, err := net.DialTimeout("unix", dataSocketPath, dataDialTimeout)
	if err != nil {
		return fmt.Errorf("could not reach Keploy's coverage data socket at %s: %w", dataSocketPath, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetWriteDeadline(time.Now().Add(dataWriteTimeout)); err != nil {
		return err
	}
	if _, err := conn.Write(frame); err != nil {
		return err
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	if err := conn.SetReadDeadline(time.Now().Add(tableReadTimeout)); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read frame reply: %w", err)
	}
	if reply != "OK\n" {
		return fmt.Errorf("unexpected frame reply %q", strings.TrimSpace(reply))
	}
	return nil
}
