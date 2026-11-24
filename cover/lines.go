package cover

import (
	"bytes"
	"fmt"
	"reflect"
	"runtime/coverage"
	"runtime/debug"
	"sort"
	"strings"
)

// ownPkgPath is this package's import path, wherever it was built from: the
// go-sdk's own (github.com/keploy/go-sdk/v3/cover) or the copy
// keploy-go-cover-build.sh puts in the app's module (<module>/zz_keploy_cover).
// Its code never counts toward a test: it runs at every END.
var ownPkgPath = reflect.TypeOf(unit{}).PkgPath()

// firstParty is the app's own modules, whose packages alone a test's lines
// name, as the SDK always named only the main module's: its main module, a
// go.work workspace's other modules (build information lists them at version
// "(devel)") and modules replaced by a directory (firstPartyOf). A build
// that also covers the standard library or dependencies (-coverpkg=all)
// would otherwise send their lines, the hook's own socket work and the
// runtime's among them. Nil when the binary carries no build information,
// and then no package is left out.
var firstParty = func() map[string]bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return firstPartyOf(bi)
}()

// firstPartyOf is firstParty of a build's information. A module replaced by
// a directory is one whose replacement has no version (or "(devel)"): a
// replacement by another module always names one.
func firstPartyOf(bi *debug.BuildInfo) map[string]bool {
	if bi.Main.Path == "" {
		return nil
	}
	mods := map[string]bool{bi.Main.Path: true}
	for _, d := range bi.Deps {
		switch {
		case d.Replace == nil && d.Version == "(devel)":
			mods[d.Path] = true
		case d.Replace != nil && (d.Replace.Version == "" || d.Replace.Version == "(devel)"):
			mods[d.Path] = true
		}
	}
	return mods
}

// leftOut reports whether a package's lines are never a test's: this
// package's own, and any outside the app's modules (mods; nil for none
// known). A package is of the module the compiler recorded for it, or, where
// it recorded none, of the module its import path is under. A package built
// from files named on the command line is the app's.
func leftOut(p metaPkg, mods map[string]bool) bool {
	switch {
	case p.path == ownPkgPath:
		return true
	case mods == nil, p.path == "command-line-arguments":
		return false
	case p.module != "":
		return !mods[p.module]
	}
	for m := range mods {
		if p.path == m || strings.HasPrefix(p.path, m+"/") {
			return false
		}
	}
	return true
}

// The runtime/coverage calls, variables for this package's tests.
var (
	writeMeta     = coverage.WriteMeta
	writeCounters = coverage.WriteCounters
)

// meta is the app's decoded coverage meta-data. It never changes while the
// process runs, so it is read once, at the first START (before its counters
// are cleared, so its reading is no test's), and kept. Guarded by controlMu,
// as every command is.
var meta *metaFile

// loadMeta returns the app's meta-data, reading it the first time.
func loadMeta() (*metaFile, error) {
	if meta != nil {
		return meta, nil
	}
	var buf bytes.Buffer
	if err := writeMeta(&buf); err != nil {
		return nil, fmt.Errorf("could not read the app's coverage meta-data (build it with go build -cover -covermode=atomic): %w", err)
	}
	m, err := decodeMeta(buf.Bytes())
	if err != nil {
		return nil, err
	}
	if m.mode != ctrModeAtomic {
		return nil, fmt.Errorf("the app was built with counter mode %d; Keploy needs -covermode=atomic", m.mode)
	}
	if m.granularity != ctrGranularityPerBlk && m.granularity != ctrGranularityPerFunc {
		return nil, fmt.Errorf("the app's coverage meta-data has unknown counter granularity %d", m.granularity)
	}
	for i := range m.pkgs {
		m.pkgs[i].skip = leftOut(m.pkgs[i], firstParty)
	}
	meta = m
	return meta, nil
}

// executedLines snapshots the app's counters and returns, per source file,
// the lines of every block that ran since the counters were last cleared.
// A file is named by its build path: its package's import path and its base
// name ("github.com/acme/svc/orders/handler.go"); a package built from files
// named on the command line keeps the absolute path the compiler recorded.
func executedLines() (map[string][]int, error) {
	m, err := loadMeta()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCounters(&buf); err != nil {
		return nil, fmt.Errorf("could not read the app's coverage counters: %w", err)
	}
	return linesOf(m, buf.Bytes())
}

// linesOf is executedLines over counter-data already written.
func linesOf(m *metaFile, counterData []byte) (map[string][]int, error) {
	hit := make(map[string]map[int]struct{})
	err := visitCounters(counterData, m.hash, func(pkgIdx, funcIdx uint32, counters []uint32) error {
		if uint64(pkgIdx) >= uint64(len(m.pkgs)) {
			return fmt.Errorf("coverage counter-data names package %d of %d", pkgIdx, len(m.pkgs))
		}
		pkg := &m.pkgs[pkgIdx]
		if pkg.skip {
			return nil
		}
		if uint64(funcIdx) >= uint64(len(pkg.funcs)) {
			return fmt.Errorf("coverage counter-data names function %d of %d in %s", funcIdx, len(pkg.funcs), pkg.path)
		}
		fn := &pkg.funcs[funcIdx]
		for i, u := range fn.units {
			var count uint32
			switch m.granularity {
			case ctrGranularityPerFunc:
				if len(counters) > 0 {
					count = counters[0]
				}
			default:
				if i >= len(counters) {
					return fmt.Errorf("coverage counter-data has %d counters for %s's %d blocks", len(counters), fn.name, len(fn.units))
				}
				count = counters[i]
			}
			if count == 0 {
				continue
			}
			lines := hit[fn.file]
			// A block can end before its start, where a //line directive
			// moved its end (generated parsers and templates): it names no
			// line, as the SDK always did, and the app's other blocks still
			// count. One longer than any file is cut at maxBlockLines.
			end := u.enLine
			if end >= u.stLine && end-u.stLine > maxBlockLines {
				end = u.stLine + maxBlockLines
			}
			for l := u.stLine; l <= end && l != 0; l++ {
				if lines == nil {
					// Made at the first line, so a file whose blocks name
					// none is not sent, as the SDK never sent one.
					lines = make(map[int]struct{})
					hit[fn.file] = lines
				}
				lines[int(l)] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string][]int, len(hit))
	for file, lines := range hit {
		sorted := make([]int, 0, len(lines))
		for l := range lines {
			sorted = append(sorted, l)
		}
		sort.Ints(sorted)
		out[file] = sorted
	}
	return out, nil
}
