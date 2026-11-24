// This file reads the coverage meta-data and counter-data that
// runtime/coverage.WriteMeta and WriteCounters write, in the running process,
// so no Go toolchain (go tool covdata) is needed where the app runs.
//
// It is derived from Go's internal/coverage packages (defs.go, decodemeta,
// decodecounter, slicereader and stringtab), Copyright 2022 The Go Authors,
// used under the BSD-style license in LICENSE-go beside this file. Unlike
// them, it reads only from memory and never panics on malformed input: every
// read is bounds-checked and a bad input is an error.

package cover

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// The format versions this package reads (internal/coverage's
// MetaFileVersion and CounterFileVersion). A newer Go that writes a newer
// format is refused by name, never decoded as if it were this one.
const (
	metaFileVersion    = 1
	counterFileVersion = 1
)

// The magic strings, as slices: this package compiles at the language
// version of any module the build stage copies it into (go 1.16 and up), so
// it never converts a slice to an array (go 1.20).
var (
	covMetaMagic    = []byte{'\x00', '\x63', '\x76', '\x6d'}
	covCounterMagic = []byte{'\x00', '\x63', '\x77', '\x6d'}
)

// maxBlockLines bounds the lines of one block a record names: no source file
// holds a block of more, and the bound keeps an odd block from costing
// billions of iterations (linesOf).
const maxBlockLines = 1 << 20

// Counter modes and granularities (internal/coverage's CounterMode and
// CounterGranularity values).
const (
	ctrModeAtomic         = 3
	ctrGranularityPerBlk  = 1
	ctrGranularityPerFunc = 2
)

// Counter flavors (internal/coverage's CounterFlavor values).
const (
	ctrRaw     = 1
	ctrULeb128 = 2
)

// Fixed sizes of the binary headers (unsafe.Sizeof of internal/coverage's
// CounterFileHeader and CounterFileFooter, and its CovMetaHeaderSize).
const (
	metaSymbolHeaderSize  = 44
	counterFileHeaderSize = 32
	counterFooterSize     = 16
)

var errTruncated = errors.New("coverage data ends early")

// unit is one coverable block of a function: its first and last line and
// column, and how many statements it holds.
type unit struct {
	stLine, stCol, enLine, enCol, nxStmts uint32
}

// metaFunc is one function of a package's meta-data.
type metaFunc struct {
	name  string
	file  string
	units []unit
}

// metaPkg is one package of the meta-data: its import path and functions,
// indexed as the counter data names them.
type metaPkg struct {
	path string
	// module is the path of the module the package is of, as the compiler
	// recorded it ("" when it recorded none).
	module string
	funcs  []metaFunc
	// skip says no test's lines name the package (leftOut).
	skip bool
}

// metaFile is a decoded coverage meta-data file.
type metaFile struct {
	hash        [16]byte
	mode        uint8
	granularity uint8
	pkgs        []metaPkg
}

// reader reads little-endian values and ULEB128 numbers from a byte slice,
// recording the first error instead of panicking.
type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) seek(off int) {
	if r.err != nil {
		return
	}
	if off < 0 || off > len(r.b) {
		r.err = errTruncated
		return
	}
	r.off = off
}

func (r *reader) bytes(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.b)-r.off {
		r.err = errTruncated
		return nil
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}

func (r *reader) u8() uint8 {
	b := r.bytes(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *reader) u32(bigEndian bool) uint32 {
	b := r.bytes(4)
	if b == nil {
		return 0
	}
	if bigEndian {
		return binary.BigEndian.Uint32(b)
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *reader) u64() uint64 {
	b := r.bytes(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *reader) uleb() uint64 {
	var v uint64
	var shift uint
	for {
		b := r.bytes(1)
		if b == nil {
			return 0
		}
		if shift >= 64 {
			r.err = errors.New("coverage data holds a number too large to read")
			return 0
		}
		v |= uint64(b[0]&0x7f) << shift
		if b[0]&0x80 == 0 {
			return v
		}
		shift += 7
	}
}

// stringTable reads a string table: a count, then each string's length and
// bytes.
func (r *reader) stringTable() []string {
	n := r.uleb()
	if r.err != nil {
		return nil
	}
	if n > uint64(len(r.b)-r.off) {
		r.err = errTruncated
		return nil
	}
	strs := make([]string, 0, int(n))
	for i := uint64(0); i < n && r.err == nil; i++ {
		l := r.uleb()
		if l > uint64(len(r.b)) {
			r.err = errTruncated
			return nil
		}
		strs = append(strs, string(r.bytes(int(l))))
	}
	return strs
}

func lookup(strs []string, idx uint64, what string) (string, error) {
	if idx >= uint64(len(strs)) {
		return "", fmt.Errorf("coverage meta-data names %s %d of a string table of %d", what, idx, len(strs))
	}
	return strs[idx], nil
}

// decodeMeta decodes the meta-data runtime/coverage.WriteMeta writes: a file
// header, each package's offset and length, a string table, then each
// package's payload.
func decodeMeta(b []byte) (*metaFile, error) {
	r := &reader{b: b}
	magic := r.bytes(4)
	version := r.u32(false)
	total := r.u64()
	entries := r.u64()
	hash := r.bytes(16)
	r.u32(false) // string table offset
	r.u32(false) // string table length
	mode := r.u8()
	gran := r.u8()
	r.bytes(6)
	if r.err != nil {
		return nil, fmt.Errorf("coverage meta-data header: %w", r.err)
	}
	if !bytes.Equal(magic, covMetaMagic) {
		return nil, errors.New("coverage meta-data has no meta-data magic")
	}
	if version != metaFileVersion {
		return nil, fmt.Errorf("coverage meta-data is format version %d; this go-sdk reads version %d: use a go-sdk release that names this Go version", version, metaFileVersion)
	}
	if total != uint64(len(b)) {
		return nil, fmt.Errorf("coverage meta-data says it is %d bytes, got %d", total, len(b))
	}
	if entries > uint64(len(b))/16 {
		return nil, fmt.Errorf("coverage meta-data names %d packages in %d bytes", entries, len(b))
	}
	m := &metaFile{mode: mode, granularity: gran, pkgs: make([]metaPkg, 0, int(entries))}
	copy(m.hash[:], hash)
	offs := make([]uint64, entries)
	for i := range offs {
		offs[i] = r.u64()
	}
	lens := make([]uint64, entries)
	for i := range lens {
		lens[i] = r.u64()
	}
	if r.err != nil {
		return nil, fmt.Errorf("coverage meta-data package table: %w", r.err)
	}
	for i := range offs {
		if offs[i] > total || lens[i] > total-offs[i] {
			return nil, fmt.Errorf("coverage meta-data package %d lies outside the data (offset %d, length %d, total %d)", i, offs[i], lens[i], total)
		}
		pkg, err := decodeMetaPkg(b[offs[i] : offs[i]+lens[i]])
		if err != nil {
			return nil, fmt.Errorf("coverage meta-data package %d: %w", i, err)
		}
		m.pkgs = append(m.pkgs, pkg)
	}
	return m, nil
}

// decodeMetaPkg decodes one package's payload: its header, each function's
// offset, its string table, then the functions.
func decodeMetaPkg(b []byte) (metaPkg, error) {
	r := &reader{b: b}
	length := r.u32(false)
	r.u32(false) // package name
	pkgPathIdx := r.u32(false)
	modulePathIdx := r.u32(false)
	r.bytes(16)  // package meta hash
	r.bytes(4)   // unused byte and padding
	r.u32(false) // number of files
	numFuncs := r.u32(false)
	if r.err != nil {
		return metaPkg{}, fmt.Errorf("header: %w", r.err)
	}
	if uint64(length) != uint64(len(b)) {
		return metaPkg{}, fmt.Errorf("says it is %d bytes, got %d", length, len(b))
	}
	if uint64(numFuncs) > uint64(len(b))/4 {
		return metaPkg{}, fmt.Errorf("names %d functions in %d bytes", numFuncs, len(b))
	}
	offs := make([]uint32, numFuncs)
	for i := range offs {
		offs[i] = r.u32(false)
	}
	r.seek(metaSymbolHeaderSize + 4*int(numFuncs))
	strs := r.stringTable()
	if r.err != nil {
		return metaPkg{}, fmt.Errorf("string table: %w", r.err)
	}
	path, err := lookup(strs, uint64(pkgPathIdx), "package path")
	if err != nil {
		return metaPkg{}, err
	}
	module, err := lookup(strs, uint64(modulePathIdx), "module path")
	if err != nil {
		return metaPkg{}, err
	}
	pkg := metaPkg{path: path, module: module, funcs: make([]metaFunc, 0, numFuncs)}
	// Each block takes 5 numbers of a byte at least, and functions may share
	// a body, so the package's blocks together are bounded too.
	var totalUnits uint64
	for i, off := range offs {
		if int(off) < metaSymbolHeaderSize+4*i || uint64(off) > uint64(len(b)) {
			return metaPkg{}, fmt.Errorf("function %d at offset %d lies outside the package", i, off)
		}
		r.seek(int(off))
		numUnits := r.uleb()
		nameIdx := r.uleb()
		fileIdx := r.uleb()
		if r.err != nil {
			return metaPkg{}, fmt.Errorf("function %d: %w", i, r.err)
		}
		totalUnits += numUnits
		if numUnits > uint64(len(b))/5 || totalUnits > uint64(len(b))/5 {
			return metaPkg{}, fmt.Errorf("function %d names %d blocks in a package of %d bytes", i, numUnits, len(b))
		}
		f := metaFunc{units: make([]unit, 0, int(numUnits))}
		if f.name, err = lookup(strs, nameIdx, "function name"); err != nil {
			return metaPkg{}, err
		}
		if f.file, err = lookup(strs, fileIdx, "file name"); err != nil {
			return metaPkg{}, err
		}
		for k := uint64(0); k < numUnits; k++ {
			f.units = append(f.units, unit{
				stLine:  uint32(r.uleb()),
				stCol:   uint32(r.uleb()),
				enLine:  uint32(r.uleb()),
				enCol:   uint32(r.uleb()),
				nxStmts: uint32(r.uleb()),
			})
		}
		r.uleb() // function literal or not
		if r.err != nil {
			return metaPkg{}, fmt.Errorf("function %d: %w", i, r.err)
		}
		pkg.funcs = append(pkg.funcs, f)
	}
	return pkg, nil
}

// visitCounters reads the counter-data runtime/coverage.WriteCounters writes,
// of the build whose meta-data hash is metaHash, and calls visit for each
// function that ran: its package and function index and its counters, a
// slice visit must not keep.
func visitCounters(b []byte, metaHash [16]byte, visit func(pkgIdx, funcIdx uint32, counters []uint32) error) error {
	if len(b) < counterFileHeaderSize+counterFooterSize {
		return fmt.Errorf("coverage counter-data is %d bytes, too short to hold its header and footer", len(b))
	}
	// The functions are read from what precedes the footer, so none reads
	// into it.
	r := &reader{b: b[:len(b)-counterFooterSize]}
	magic := r.bytes(4)
	version := r.u32(false)
	hash := r.bytes(16)
	flavor := r.u8()
	bigEndian := r.u8() != 0
	r.bytes(6)
	if !bytes.Equal(magic, covCounterMagic) {
		return errors.New("coverage counter-data has no counter-data magic")
	}
	if version != counterFileVersion {
		return fmt.Errorf("coverage counter-data is format version %d; this go-sdk reads version %d: use a go-sdk release that names this Go version", version, counterFileVersion)
	}
	if !bytes.Equal(hash, metaHash[:]) {
		return errors.New("coverage counter-data is of another build than the meta-data")
	}
	if flavor != ctrRaw && flavor != ctrULeb128 {
		return fmt.Errorf("coverage counter-data has unknown counter flavor %d", flavor)
	}
	foot := &reader{b: b[len(b)-counterFooterSize:]}
	fmagic := foot.bytes(4)
	foot.bytes(4)
	segments := foot.u32(false)
	if !bytes.Equal(fmagic, covCounterMagic) {
		return errors.New("coverage counter-data has no footer magic")
	}
	// runtime/coverage.WriteCounters writes one segment; more are what the
	// go tool merges from files, which this package never reads.
	if segments != 1 {
		return fmt.Errorf("coverage counter-data holds %d segments, want the 1 WriteCounters writes", segments)
	}
	next := func() uint32 {
		if flavor == ctrULeb128 {
			v := r.uleb()
			if v > 1<<32-1 {
				r.err = errors.New("coverage counter-data holds a value over 32 bits")
			}
			return uint32(v)
		}
		return r.u32(bigEndian)
	}
	var counters []uint32
	for seg := uint32(0); seg < segments; seg++ {
		fcnEntries := r.u64()
		strTabLen := r.u32(false)
		argsLen := r.u32(false)
		r.bytes(int(strTabLen))
		r.bytes(int(argsLen))
		if pad := r.off % 4; pad != 0 {
			r.bytes(4 - pad)
		}
		if r.err != nil {
			return fmt.Errorf("coverage counter-data segment %d: %w", seg, r.err)
		}
		if fcnEntries > uint64(len(b)) {
			return fmt.Errorf("coverage counter-data segment %d names %d functions", seg, fcnEntries)
		}
		for f := uint64(0); f < fcnEntries; f++ {
			n := next()
			pkgIdx := next()
			funcIdx := next()
			if r.err != nil {
				return fmt.Errorf("coverage counter-data function %d: %w", f, r.err)
			}
			if uint64(n) > uint64(len(b)) {
				return fmt.Errorf("coverage counter-data function %d names %d counters", f, n)
			}
			counters = counters[:0]
			for i := uint32(0); i < n; i++ {
				counters = append(counters, next())
			}
			if r.err != nil {
				return fmt.Errorf("coverage counter-data function %d: %w", f, r.err)
			}
			if err := visit(pkgIdx, funcIdx, counters); err != nil {
				return err
			}
		}
	}
	if r.off != len(r.b) {
		return fmt.Errorf("coverage counter-data holds %d bytes past its last function", len(r.b)-r.off)
	}
	return nil
}
