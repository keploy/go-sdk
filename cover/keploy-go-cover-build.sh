#!/bin/sh
# keploy-go-cover-build.sh: build a Go app for Keploy's per-test coverage and
# dynamic dedup, with no change to the app's code, go.mod or go.sum.
#
# Usage, from the root of the app's module (the directory of its go.mod),
# typically in a Dockerfile build stage that holds a copy of it:
#
#   sh /path/to/go-sdk/cover/keploy-go-cover-build.sh [-o OUT] [PKG] [-- GO_BUILD_FLAGS...]
#
# PKG is the app's main package, as a package path (default "."), and
# GO_BUILD_FLAGS the flags the app's own build passes go build (-tags,
# -ldflags "-X main.version=...", -trimpath, ...), each one argument, quoted
# as for go build itself. $KEPLOY_GO_LDFLAGS, when set, is passed as one
# -ldflags argument, spaces and all ("-s -w -X main.version=1.2"), for a
# Dockerfile that sets it with ENV or ARG; an -ldflags in GO_BUILD_FLAGS
# comes after it, and go build takes the last. The script:
#   1. copies the coverage hook (the .go files beside this script, without
#      tests, and LICENSE-go) into the module as ./zz_keploy_cover;
#   2. adds PKG/zz_keploy_cover_main.go, a blank import of it, to package main;
#   3. runs go build -cover -covermode=atomic -coverpkg=M/... for each
#      module M of the app's own code (the module, a go.work's modules, and
#      modules replaced by a directory) and the GO_BUILD_FLAGS;
#   4. removes both again, however the build ends.
# The hook is a package of the app's own module, so a module with a vendor
# directory (-mod=vendor) builds as before: nothing new is fetched. An app
# that already links this hook (it imports github.com/keploy/go-sdk/v3/cover,
# or /keploy of a release that has cover) is only built with -cover.
#
# It refuses (exit 2) when not run at the root of PKG's module, when PKG is
# not one main package of a main module or is named by its files, when PKG
# links the go-sdk's hook of a release before cover (which needs the go
# command where the app runs), and when either path it would add is already
# there.
set -eu

refuse() {
	echo "keploy-go-cover-build: $*" >&2
	exit 2
}

out=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o)
		[ $# -ge 2 ] && [ -n "$2" ] || refuse "-o needs a path"
		out="$2"
		shift 2
		;;
	-o=*)
		out="${1#-o=}"
		[ -n "$out" ] || refuse "-o needs a path"
		shift
		;;
	-h | --help)
		awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
		exit 0
		;;
	--) break ;;
	-*) refuse "unknown flag $1 (usage: keploy-go-cover-build.sh [-o OUT] [PKG] [-- GO_BUILD_FLAGS...])" ;;
	*) break ;;
	esac
done
pkg="."
if [ $# -gt 0 ] && [ "$1" != "--" ]; then
	pkg="$1"
	shift
fi
if [ $# -gt 0 ]; then
	[ "$1" = "--" ] || refuse "one package at most, then -- and go build flags; got: $*"
	shift
fi
# From here the positional parameters are GO_BUILD_FLAGS, passed whole to
# every go command that reads the build, so a tag-gated import is seen too.
if [ -n "${KEPLOY_GO_BUILD_FLAGS:-}" ]; then
	refuse "KEPLOY_GO_BUILD_FLAGS is not read: pass go build flags after -- (keploy-go-cover-build.sh [-o OUT] [PKG] -- FLAGS...), and -ldflags as KEPLOY_GO_LDFLAGS or after --"
fi
for f in "$@"; do
	case "$f" in
	-o | -o=* | --o | --o=*) refuse "give -o before PKG (keploy-go-cover-build.sh -o OUT PKG -- ...), not after --: every go command the script runs reads the flags after --" ;;
	-C | -C=* | --C | --C=*) refuse "run the script from the module's directory instead of passing -C" ;;
	-cover | -cover=* | --cover | --cover=* | -covermode | -covermode=* | --covermode | --covermode=* | -coverpkg | -coverpkg=* | --coverpkg | --coverpkg=*)
		refuse "$f is not taken after --: the stage sets -cover -covermode=atomic and -coverpkg itself, and a later value would replace them"
		;;
	-json | -json=* | --json | --json=*) refuse "-json is not taken after --: every go command the script runs reads those flags, and go list's -f cannot go with it" ;;
	esac
done
if [ -n "${KEPLOY_GO_LDFLAGS:-}" ]; then
	set -- -ldflags="$KEPLOY_GO_LDFLAGS" "$@"
fi
case "$pkg" in
*.go) refuse "name the main package by its path (such as ./cmd/server), not by its files ($pkg)" ;;
esac

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
for f in "$here"/hook.go "$here"/decode.go "$here"/LICENSE-go; do
	[ -f "$f" ] || refuse "the coverage hook's files are not beside this script ($here): run the script from the go-sdk's cover/ directory"
done

tab=$(printf '\t')
# PKG's package and module, one line a package (module mode only: GOPATH
# mode has no module to put the hook in). Tab-separated: no path holds one.
info=$(go list "$@" -f '{{.Name}}	{{.Standard}}	{{.Dir}}	{{with .Module}}{{.Path}}	{{.Dir}}	{{.Main}}{{end}}' "$pkg") ||
	refuse "cannot read package $pkg"
case "$info" in
*"
"*) refuse "$pkg names more than one package: name the app's main package" ;;
esac
IFS="$tab" read -r name standard pkgdir module moddir modmain <<EOF
$info
EOF
if [ "$standard" = "true" ]; then
	refuse "$pkg is a standard library package, not a package of your module: name the app's main package"
fi
if [ -z "${module:-}" ]; then
	refuse "$pkg is in no module (GOPATH mode?): build it in module mode"
fi
if [ "${modmain:-}" != "true" ]; then
	refuse "$pkg is of module $module, a dependency: name a package of the app's own module"
fi
# Both physical: go may name the directory by a symbolic link ($PWD).
if [ "$(CDPATH='' cd -- "$moddir" && pwd -P)" != "$(pwd -P)" ]; then
	refuse "run it from the root of $pkg's module, $moddir (here is $(pwd -P))"
fi
if [ "$name" != "main" ]; then
	refuse "$pkg is package $name, not package main: name the app's main package"
fi

deps=$(go list "$@" -deps "$pkg") || refuse "cannot list what $pkg is built from"
inject=1
if printf '%s\n' "$deps" | grep -q -x -F "github.com/keploy/go-sdk/v3/cover"; then
	inject=0
	echo "keploy-go-cover-build: $pkg already links github.com/keploy/go-sdk/v3/cover: building it with -cover only" >&2
elif printf '%s\n' "$deps" | grep -q -x -F "github.com/keploy/go-sdk/v3/keploy"; then
	refuse "$pkg imports github.com/keploy/go-sdk/v3/keploy of a release before cover/, whose hook runs the go command where the app runs: update github.com/keploy/go-sdk/v3 to a release with cover/, or remove that import"
fi

# One pattern a module of the app's own code: each main module (the module,
# or each module of a go.work) and each module replaced by a directory (a
# monorepo's sibling module), as the hook counts them. A pattern, not a list
# of packages, so a large module's flag stays far under the kernel's limit
# on one argument. The hook, a package of the module, is measured too: it
# leaves its own lines out of every record, and its counters cost each END
# an atomic add per block it runs.
mods=$(go list "$@" -m) || refuse "cannot list the build's modules"
replaced=$(go list "$@" -deps -f '{{with .Module}}{{with .Replace}}{{if not .Version}}{{$.Module.Path}}{{end}}{{end}}{{end}}' "$pkg") ||
	refuse "cannot list $pkg's modules"
coverpkg=$(printf '%s\n%s\n' "$mods" "$replaced" | sed '/^$/d' | sort -u | sed 's|$|/...|' | paste -s -d, -)

hookdir="./zz_keploy_cover"
mainfile="$pkgdir/zz_keploy_cover_main.go"
if [ "$inject" = 1 ]; then
	for p in "$hookdir" "$mainfile"; do
		[ ! -e "$p" ] || refuse "$p is already there; remove it (an earlier build that was killed leaves it) and run again"
	done
	# With .git in the build's directory, go build stamps the binary from it,
	# and two untracked files would mark it modified ("+dirty", unlike the
	# production build of the same tree): git is told to ignore them for the
	# build, and its exclude file is put back after.
	# --git-path names it relative to here (no --path-format: git before
	# 2.31 echoes an option it does not know); an answer that is not one
	# path is not used.
	exclude=""
	excludesaved=""
	if command -v git >/dev/null 2>&1 && exclude=$(git rev-parse --git-path info/exclude 2>/dev/null); then
		case "$exclude" in
		"" | -* | *"
"*) exclude="" ;;
		esac
		# Only where its directory is: git before 2.13 names it from the
		# repository's top, not from here, and no .git is ever made.
		if [ -n "$exclude" ] && [ ! -d "$(dirname -- "$exclude")" ]; then
			exclude=""
		fi
	else
		exclude=""
	fi
	if [ -n "$exclude" ] && [ -f "$exclude" ]; then
		excludesaved="$exclude.keploy-go-cover-build.$$"
		cp -p -- "$exclude" "$excludesaved" 2>/dev/null || { exclude=""; excludesaved=""; }
	fi
	cleanup() {
		rm -rf "$hookdir" "$mainfile"
		if [ -n "$excludesaved" ]; then
			mv -f -- "$excludesaved" "$exclude"
		elif [ -n "$exclude" ]; then
			rm -f -- "$exclude"
		fi
	}
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	if [ -n "$exclude" ]; then
		{ printf '\n%s\n%s\n' "zz_keploy_cover/" "zz_keploy_cover_main.go" >>"$exclude"; } 2>/dev/null ||
			echo "keploy-go-cover-build: could not tell git to ignore the hook's files ($exclude): a binary stamped from .git may read modified" >&2
	fi

	echo "keploy-go-cover-build: adding the coverage hook as $module/zz_keploy_cover, imported by $mainfile, for this build only" >&2
	mkdir "$hookdir"
	for f in "$here"/*.go; do
		case "$f" in
		*_test.go) ;;
		*) cp "$f" "$hookdir/" ;;
		esac
	done
	cp "$here/LICENSE-go" "$hookdir/"
	printf '// Code generated by keploy-go-cover-build.sh. DO NOT EDIT.\n\npackage main\n\nimport _ "%s/zz_keploy_cover"\n' "$module" >"$mainfile"
fi

if [ -n "$out" ]; then
	set -- -o "$out" "$@"
fi
set -- go build -cover -covermode=atomic -coverpkg="$coverpkg" "$@" "$pkg"
echo "keploy-go-cover-build: $*" >&2
"$@"
