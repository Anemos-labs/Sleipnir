#!/bin/sh
# What the calls that run on every request cost: the benchmarks of the hot paths, and the comparison of two runs.
#
#   scripts/perf.sh run OUT.txt [PKG...]     run the benchmarks (the packages below, or the ones named) into OUT.txt
#   scripts/perf.sh compare OLD.txt NEW.txt  say what got slower or allocates more (bench/tools/benchcmp); exit 1 if anything did
#
#   COUNT=5 BENCHTIME=200ms   repeats and time of each benchmark (the median of the repeats is compared)
#   CPUS=2,3                  run on these cpus (taskset -c); leave the others to whatever else the machine is doing
#
# A machine that is busy gives numbers that are a hint: run both runs in the same quiet minute, one after the other, and look at the
# ranges (benchcmp reports a slowdown only when the two ranges do not overlap). The allocations of a call do not depend on the machine:
# the allocation gates (allocs_gate_test.go next to the code) are tests, and are what fail a build.
set -eu
LC_ALL=C
export LC_ALL
cd "$(dirname "$0")/.."

PKGS="./internal/kv ./internal/core ./internal/events ./internal/provider ./internal/perm ./internal/swarm ./internal/tools/fs ./internal/session ./internal/tui/state ./internal/tui/widget ./internal/tui/render ./internal/tui/input"

case "${1:-}" in
  run)
    out=${2:?usage: scripts/perf.sh run OUT.txt [PKG...]}
    shift 2
    [ "$#" -gt 0 ] && PKGS="$*"
    pin=
    [ -n "${CPUS:-}" ] && pin="taskset -c $CPUS"
    {
      echo "# $(date -u +%Y-%m-%dT%H:%M:%SZ) $(go version) $(uname -sm) load $(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null || uptime | sed 's/.*load average: //')"
      # shellcheck disable=SC2086 # the packages are words
      $pin go test -run '^$' -bench . -benchmem -count "${COUNT:-5}" -benchtime "${BENCHTIME:-200ms}" $PKGS
    } > "$out"
    echo "perf: wrote $out ($(grep -c '^Benchmark' "$out") lines)" >&2
    ;;
  compare)
    old=${2:?usage: scripts/perf.sh compare OLD.txt NEW.txt}
    new=${3:?usage: scripts/perf.sh compare OLD.txt NEW.txt}
    go run ./bench/tools/benchcmp ${THRESHOLD:+-threshold "$THRESHOLD"} "$old" "$new"
    ;;
  -h|--help|"")
    awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
    ;;
  *)
    echo "perf: unknown command: $1 (try --help)" >&2
    exit 2
    ;;
esac
