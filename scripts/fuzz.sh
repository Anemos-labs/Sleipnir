#!/bin/sh
# Fuzz every fuzz target, each for a while. Go runs one target per invocation, so this finds them and runs them in turn.
#
#   scripts/fuzz.sh [FUZZTIME]              every target of every package, FUZZTIME each (default 30s)
#   scripts/fuzz.sh FUZZTIME ./internal/perm  only the packages given
#   scripts/fuzz.sh --packages              print the packages that have fuzz targets, as a JSON array (for a CI matrix)
#
# A target that fails leaves its input under <package>/testdata/fuzz/<Target>/, which is what to commit as a regression
# seed once the bug is fixed. The exit status is 1 if any target failed; the others still run.
set -eu
cd "$(dirname "$0")/.."

packages() {
  grep -rl --include='*_test.go' '^func Fuzz' cmd internal 2>/dev/null | xargs -n1 dirname | sort -u | sed 's#^#./#'
}

if [ "${1:-}" = "--packages" ]; then
  packages | awk 'BEGIN { printf "[" } { printf "%s\"%s\"", (NR > 1 ? "," : ""), $0 } END { print "]" }'
  exit 0
fi

time=${1:-30s}
[ "$#" -gt 0 ] && shift
if [ "$#" -gt 0 ]; then pkgs=$*; else pkgs=$(packages); fi

failed=0
total=0
for p in $pkgs; do
  for t in $(go test -list '^Fuzz' "$p" 2>/dev/null | grep '^Fuzz' || true); do
    total=$((total + 1))
    echo "== $p $t ($time)"
    if ! go test -run '^$' -fuzz "^$t\$" -fuzztime "$time" "$p"; then
      failed=$((failed + 1))
      echo "FAILED: $p $t (the input is under $p/testdata/fuzz/$t/)"
    fi
  done
done
echo "fuzzed $total targets, $failed failed"
[ "$failed" -eq 0 ]
