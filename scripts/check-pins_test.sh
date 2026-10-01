#!/bin/sh
# Tests for scripts/check-pins.sh on throw-away workflow files. Run: sh scripts/check-pins_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
CHECK=$here/check-pins.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

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
hasnt() { # hasnt DESCRIPTION NEEDLE HAYSTACK
  case $3 in *"$2"*)
    failed=$((failed + 1)); printf 'FAIL: %s\n  want it not to contain: %s\n  got: %s\n' "$1" "$2" "$3" >&2 ;; *) passed=$((passed + 1)) ;;
  esac
}

sha=3d3c42e5aac5ba805825da76410c181273ba90b1
n=0
# try LINE...: a workflow whose steps are the given lines; sets out and rc
try() {
  n=$((n + 1))
  d=$tmp/w$n
  mkdir -p "$d/.github/workflows"
  {
    printf 'name: t\non: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n'
    for l in "$@"; do printf '%s\n' "$l"; done
  } > "$d/.github/workflows/t.yml"
  rc=0
  out=$(sh "$CHECK" "$d/.github" 2>&1) || rc=$?
}

# --- what passes
try "      - uses: actions/checkout@$sha # v7.0.1"
eq "a pinned action with its version: exit status" 0 "$rc"
has "a pinned action: message" "check-pins: ok" "$out"
try "      - uses: ./.github/actions/local"
eq "a local action" 0 "$rc"
try "      - uses: ../shared/action"
eq "a relative local action" 0 "$rc"
try "      - uses: github/codeql-action/init@$sha # v4.38.2"
eq "an action in a subdirectory of a repository" 0 "$rc"
try "      - uses: \"actions/checkout@$sha\" # v7.0.1"
eq "a double-quoted value" 0 "$rc"
try "      - uses: 'actions/checkout@$sha' # 7.0.1"
eq "a single-quoted value, and a version without the v" 0 "$rc"
try "      -   uses:   actions/checkout@$sha    #   v7.0.1 some words"
eq "unusual spacing" 0 "$rc"
try "        uses: actions/checkout@$sha # v7.0.1"
eq "a uses: without the list dash" 0 "$rc"
try "      - uses: docker://ghcr.io/o/i@sha256:0000000000000000000000000000000000000000000000000000000000000000"
eq "a docker image by digest" 0 "$rc"
try "      # - uses: actions/checkout@v1" "      - run: echo uses: actions/checkout@v1"
eq "commented-out text and uses: inside a command are not reads" 0 "$rc"
try "      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./..."
eq "a go tool at a version" 0 "$rc"

# --- what fails
try "      - uses: actions/checkout@v4"
eq "a tag: exit status" 1 "$rc"
has "a tag: names file and line" "t.yml:7: uses: actions/checkout@v4" "$out"
has "a tag: says what to do" "pin the full 40-hex commit" "$out"
has "a tag: header" "actions that are not pinned" "$out"
try "      - uses: actions/checkout@main"
eq "a branch" 1 "$rc"
try "      - uses: actions/checkout@3d3c42e"
eq "a short SHA" 1 "$rc"
try "      - uses: actions/checkout@$(printf '%s' "$sha" | cut -c1-39)"
eq "a SHA of 39 characters" 1 "$rc"
try "      - uses: actions/checkout@${sha}0"
eq "a SHA of 41 characters" 1 "$rc"
try "      - uses: actions/checkout@$(printf '%s' "$sha" | tr 'a-f' 'A-F')"
eq "an upper-case SHA" 1 "$rc"
try "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90bz"
eq "a SHA with a non-hex character" 1 "$rc"
try "      - uses: actions/checkout"
eq "no @ref" 1 "$rc"
has "no @ref: message" "no @ref" "$out"
try "      - uses: actions/checkout@$sha"
eq "a SHA without a version comment" 1 "$rc"
has "a SHA without a version comment: message" "no version comment" "$out"
try "      - uses: actions/checkout@$sha # the checkout action"
eq "a comment that is not a version" 1 "$rc"
try "      - uses: docker://alpine:3.20"
eq "a docker image by tag" 1 "$rc"
has "a docker image by tag: message" "pinned by digest" "$out"
try "      - uses: docker://alpine@sha256:abc"
eq "a docker digest that is too short" 1 "$rc"
try "      - uses: \${{ matrix.action }}"
eq "an expression" 1 "$rc"
try "      - run: go run golang.org/x/vuln/cmd/govulncheck@latest ./..."
eq "a go tool @latest" 1 "$rc"
has "a go tool @latest: message" "@latest" "$out"
try "      - run: go install github.com/rhysd/actionlint/cmd/actionlint@main"
eq "a go tool @main" 1 "$rc"
try "      - run: |" "          GOBIN=/usr/local/bin go install example.com/x@latest"
eq "a go tool @latest inside a script block" 1 "$rc"
try "      - uses: actions/checkout@v4" "      - uses: actions/setup-go@$sha # v7.0.0" "      - uses: actions/cache@v3"
eq "two of three wrong: exit status" 1 "$rc"
has "two of three wrong: first" "actions/checkout@v4" "$out"
has "two of three wrong: third" "actions/cache@v3" "$out"
hasnt "two of three wrong: the good one is not named" "setup-go" "$out"

# --- several files and directories, .yaml as well
n=$((n + 1))
d=$tmp/w$n
mkdir -p "$d/.github/workflows" "$d/.github/actions/x"
printf 'jobs:\n  j:\n    steps:\n      - uses: actions/checkout@%s # v7.0.1\n' "$sha" > "$d/.github/workflows/a.yml"
printf 'jobs:\n  j:\n    steps:\n      - uses: actions/checkout@v4\n' > "$d/.github/workflows/b.yaml"
printf 'runs:\n  using: composite\n  steps:\n    - uses: actions/setup-go@v5\n' > "$d/.github/actions/x/action.yml"
rc=0
out=$(sh "$CHECK" "$d/.github" 2>&1) || rc=$?
eq "several files: exit status" 1 "$rc"
has "a .yaml file is read" "b.yaml:4" "$out"
has "a composite action is read" "action.yml:4" "$out"
hasnt "the pinned file is not named" "a.yml" "$out"

# --- the check itself
rc=0
out=$(sh "$CHECK" "$tmp/nothing-here" 2>&1) || rc=$?
eq "no workflow files: exit status" 2 "$rc"
rc=0
out=$(sh "$CHECK" --help 2>&1) || rc=$?
eq "--help: exit status" 0 "$rc"
has "--help: text" "scripts/check-pins.sh [DIR...]" "$out"

printf 'check-pins_test.sh: %d checks passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
