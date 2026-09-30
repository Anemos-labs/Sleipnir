#!/bin/sh
# Tests for scripts/check-deps.sh. The modules under test are throw-away ones whose dependencies are local directories
# (replace directives), so nothing here needs the network or the module cache. Run: sh scripts/check-deps_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
CHECK=$here/check-deps.sh
command -v go >/dev/null 2>&1 || { echo "check-deps_test.sh: skipped, the go command is needed"; exit 0; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
GOPROXY=off
GOSUMDB=off
GOTOOLCHAIN=local
GOWORK=off
GOFLAGS=
export GOPROXY GOSUMDB GOTOOLCHAIN GOWORK GOFLAGS

passed=0
failed=0
eq() { # eq DESCRIPTION WANT GOT
  if [ "$2" = "$3" ]; then passed=$((passed + 1)); else
    failed=$((failed + 1)); printf 'FAIL: %s\n  want: %s\n  got:  %s\n' "$1" "$2" "$3" >&2
  fi
}
has() { # has DESCRIPTION NEEDLE HAYSTACK
  case $3 in *"$2"*) passed=$((passed + 1)) ;; *)
    failed=$((failed + 1)); printf 'FAIL: %s\n  want it to contain: %s\n  got: %s\n' "$1" "$2" "$3" >&2 ;;
  esac
}

allow=$tmp/allow.txt
cat > "$allow" <<'EOF'
# a list for the tests
direct golang.org/x/net   # the network
direct golang.org/x/sys
graph  golang.org/x/text
EOF

dir_of() { printf '%s/fake/%s\n' "$tmp" "$(printf '%s' "$1" | tr '/' '_')"; }
# fake PATH [IMPORTS...] [-- REQUIRES...]: (re)write the fake module PATH, which has one package; IMPORTS are module paths
# whose root package it imports, REQUIRES are what its go.mod lists.
fake() {
  path=$1
  shift
  d=$(dir_of "$path")
  mkdir -p "$d"
  imports=""
  while [ "$#" -gt 0 ] && [ "$1" != -- ]; do imports="$imports $1"; shift; done
  [ "$#" -eq 0 ] || shift
  {
    printf 'module %s\n\ngo 1.24\n' "$path"
    for r in "$@"; do printf 'require %s v1.0.0\n' "$r"; done
  } > "$d/go.mod"
  {
    printf 'package %s\n' "$(basename "$path")"
    for i in $imports; do printf 'import _ "%s"\n' "$i"; done
  } > "$d/x.go"
}
fake golang.org/x/net
fake golang.org/x/sys
fake golang.org/x/text
fake github.com/evil/lib

# mkmod NAME direct:PATH indirect:PATH ...: a main module that requires those, replaces each (and every module named in
# EXTRA_REPLACE) by its fake, and imports every "direct" one. Prints its directory.
mkmod() {
  m=$tmp/$1
  shift
  mkdir -p "$m"
  {
    printf 'module example.com/m\n\ngo 1.24\n\nrequire (\n'
    for r in "$@"; do
      case $r in
        direct:*) printf '\t%s v1.0.0\n' "${r#direct:}" ;;
        indirect:*) printf '\t%s v1.0.0 // indirect\n' "${r#indirect:}" ;;
      esac
    done
    printf ')\n\n'
    for r in "$@"; do printf 'replace %s => %s\n' "${r#*:}" "$(dir_of "${r#*:}")"; done
    for p in ${EXTRA_REPLACE:-}; do printf 'replace %s => %s\n' "$p" "$(dir_of "$p")"; done
  } > "$m/go.mod"
  {
    printf 'package main\n\n'
    for r in "$@"; do case $r in direct:*) printf 'import _ "%s"\n' "${r#direct:}" ;; esac; done
    printf '\nfunc main() {}\n'
  } > "$m/main.go"
  printf '%s\n' "$m"
}
# run DIR [ARGS...] sets out (stdout and stderr together) and rc.
run() {
  d=$1
  shift
  rc=0
  out=$(sh "$CHECK" --allowlist "$allow" "$@" "$d" 2>&1) || rc=$?
}

# --- a module that stays inside the list
m=$(mkmod ok direct:golang.org/x/net direct:golang.org/x/sys)
run "$m" --allow-replace
eq "allowed direct modules: exit status" 0 "$rc"
has "allowed direct modules: message" "ok (" "$out"
run "$m"
eq "a replace directive is a finding by default" 1 "$rc"
has "a replace directive: message" "replaces a module" "$out"

# --- a direct requirement outside the list
m=$(mkmod evil direct:golang.org/x/net direct:github.com/evil/lib)
run "$m" --allow-replace
eq "direct requirement outside the list: exit status" 1 "$rc"
has "direct requirement: names the module" "go.mod requires github.com/evil/lib" "$out"
has "direct requirement: says what to do" "scripts/deps-allowlist.txt" "$out"
has "direct requirement: it is also compiled" "packages of github.com/evil/lib are compiled" "$out"
has "direct requirement: the header" "the dependency set has drifted" "$out"

# --- an indirect requirement outside the list
m=$(mkmod indirect direct:golang.org/x/net indirect:github.com/evil/lib)
run "$m" --allow-replace
eq "indirect requirement outside the list: exit status" 1 "$rc"
has "indirect requirement: go.mod" "go.mod requires github.com/evil/lib" "$out"
has "indirect requirement: the graph" "module graph (go list -m all) holds github.com/evil/lib" "$out"

# --- a module that arrives through a listed one (what a dependency bump does)
fake golang.org/x/net -- github.com/evil/lib
m=$(EXTRA_REPLACE="github.com/evil/lib" mkmod bump direct:golang.org/x/net)
run "$m" --allow-replace
eq "a new module in a listed module's requirements: exit status" 1 "$rc"
has "a new module in a listed module's requirements: the graph" "module graph (go list -m all) holds github.com/evil/lib" "$out"
fake golang.org/x/net

# --- a graph-only module that gets compiled
fake golang.org/x/net golang.org/x/text -- golang.org/x/text
m=$(mkmod compiled direct:golang.org/x/net indirect:golang.org/x/text)
run "$m" --allow-replace
eq "a graph module that is compiled in: exit status" 1 "$rc"
has "a graph module that is compiled in: message" "packages of golang.org/x/text are compiled" "$out"
fake golang.org/x/net -- golang.org/x/text
m=$(mkmod graphok direct:golang.org/x/net indirect:golang.org/x/text)
run "$m" --allow-replace
eq "a graph module that is only in the graph is fine" 0 "$rc"
fake golang.org/x/net

# --- a graph-only module required directly
m=$(mkmod graphdirect direct:golang.org/x/text)
run "$m" --allow-replace
eq "a graph module required directly: exit status" 1 "$rc"
has "a graph module required directly: message" "requires golang.org/x/text directly" "$out"

# --- go.sum
m=$(mkmod sum direct:golang.org/x/net)
printf 'github.com/evil/lib v1.0.0 h1:abc=\ngithub.com/evil/lib v1.0.0/go.mod h1:def=\n' > "$m/go.sum"
run "$m" --allow-replace
eq "an unlisted module in go.sum: exit status" 1 "$rc"
has "an unlisted module in go.sum: message" "go.sum holds github.com/evil/lib" "$out"
printf 'golang.org/x/net v1.0.0 h1:abc=\n' > "$m/go.sum"
run "$m" --allow-replace
eq "a listed module in go.sum is fine" 0 "$rc"

# --- a single-line require, and a replace that swaps a listed module for other code
m=$tmp/single
mkdir -p "$m"
printf 'module example.com/m\n\ngo 1.24\n\nrequire github.com/evil/lib v1.0.0\n\nreplace github.com/evil/lib => %s\n' "$(dir_of github.com/evil/lib)" > "$m/go.mod"
printf 'package main\n\nfunc main() {}\n' > "$m/main.go"
run "$m" --allow-replace
eq "a single-line require is read: exit status" 1 "$rc"
has "a single-line require is read: message" "go.mod requires github.com/evil/lib" "$out"
mkdir -p "$tmp/swapped"
printf 'module golang.org/x/net\n\ngo 1.24\n' > "$tmp/swapped/go.mod"
printf 'package net\n' > "$tmp/swapped/x.go"
m=$tmp/swap
mkdir -p "$m"
printf 'module example.com/m\n\ngo 1.24\n\nrequire golang.org/x/net v1.0.0\n\nreplace golang.org/x/net => %s\n' "$tmp/swapped" > "$m/go.mod"
printf 'package main\n\nfunc main() {}\n' > "$m/main.go"
run "$m"
eq "a replace that swaps a listed module: exit status" 1 "$rc"
has "a replace that swaps a listed module: message" "golang.org/x/net => $tmp/swapped" "$out"

# --- the check itself
run "$tmp/nowhere"
eq "no go.mod: exit status" 2 "$rc"
has "no go.mod: message" "go.mod does not exist" "$out"
printf 'direct\nnonsense here too\n' > "$tmp/bad.txt"
rc=0
out=$(sh "$CHECK" --allowlist "$tmp/bad.txt" "$tmp/ok" 2>&1) || rc=$?
eq "malformed allow-list: exit status" 2 "$rc"
has "malformed allow-list: message" "every line must be" "$out"
rc=0
out=$(sh "$CHECK" --bogus 2>&1) || rc=$?
eq "unknown argument: exit status" 2 "$rc"
rc=0
out=$(sh "$CHECK" --help 2>&1) || rc=$?
eq "--help: exit status" 0 "$rc"
has "--help: text" "scripts/check-deps.sh [--allowlist FILE]" "$out"

# --- and the repository this script belongs to, with the list that ships
rc=0
out=$(sh "$CHECK" 2>&1) || rc=$?
eq "this repository passes its own allow-list" 0 "$rc"
has "this repository: message" "check-deps: ok" "$out"

printf 'check-deps_test.sh: %d checks passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
