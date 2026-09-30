#!/bin/sh
# Run the benchmark suite (bench/build.sh) with a model on a real endpoint, resumably.
#
#   scripts/bench.sh run    --model M [options]        one run directory
#   scripts/bench.sh ab     --model M --bin-a A --bin-b B [options]
#                                                      two binaries, alternating sample by sample
#   scripts/bench.sh status RUN_DIR...                 one line per run directory
#
# options (run, ab)
#   --suite DIR        the built suite (default: $BENCH_HOME/suite; BENCH_HOME defaults to ~/.sleipnir-bench)
#   --out DIR          the run directory (default: $BENCH_HOME/runs/<timestamp>-<model>[-<label>])
#   --label TEXT       added to the default run directory's name
#   --bin PATH         the sleipnir binary (default: $SLEIPNIR, else sleipnir on PATH)
#   --tag EXPR         tasks to run: comma-separated tags, ! excludes (default: smoke)
#   --group N          samples per task (default 1; in ab mode, the number of rounds)
#   --mode MODE        single | swarm:N | empty for what each task's team says (default: empty)
#   --concurrency N    rollouts in flight (default 4)
#   --rpm N            policy requests per minute for this process (default 120; the endpoint allows 600 in all)
#   --budget-usd X     spend cap of one rollout (default 0.25)
#   --max-spend-usd X  spend cap of the whole run, failed attempts included (default 2)
#   --seed N           run seed (default 1)
#   --attempts N       invocations before giving up (default 24); each one resumes what is unfinished
#   --key-file FILE    a shell file exporting the endpoint key (must be mode 0600); never printed or logged
#   --api-key-env NAME variable holding the key (default: HEIMDALL_API_KEY)
#   --pin-cpus LIST    run the benchmark on these cpus (taskset -c), e.g. 0,1
#   --min-free-gb N    refuse to start, and pause between attempts, below this much free disk (default 3)
#   --extra "FLAGS"    more flags for sleipnir rl rollout, verbatim (e.g. "--allow none --ignore-repo-instructions")
#
# The run loops: sleipnir rl rollout into one directory resumes what is unfinished, so when the endpoint was down
# (exit 75: nothing completed) or some rollouts failed for infrastructure reasons, the next invocation picks them up,
# after a pause that doubles up to ten minutes. It stops when everything has a verdict, the spend cap is reached, or
# the attempts are used up. Exit: 0 everything has a verdict; 75 some rollouts are still unfinished; 3 the spend cap
# stopped the run; 2 usage or a failed preflight.
#
# The suite's repositories are named relative to the suite directory, so the benchmark runs from there; the work
# directory and the Go build cache (BENCH_GOCACHE, default BENCH_HOME/gocache) are the benchmark's own, apart from your
# development ones.
set -eu
LC_ALL=C
export LC_ALL
set +x

BENCH_HOME=${BENCH_HOME:-$HOME/.sleipnir-bench}
cmd=${1:-}
[ -n "$cmd" ] || { sed -n '2,/^set -eu/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2; exit 2; }
shift || true

SUITE=$BENCH_HOME/suite OUT= LABEL= BIN=${SLEIPNIR:-} BIN_A= BIN_B=
MODEL= TAG=smoke GROUP=1 MODE= CONC=4 RPM=120 BUDGET=0.25 CAP=2 SEED=1 ATTEMPTS=24
KEYFILE= KEYENV=HEIMDALL_API_KEY PIN= MINFREE=3 EXTRA=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --suite) shift; SUITE=${1:?} ;;
    --out) shift; OUT=${1:?} ;;
    --label) shift; LABEL=${1:?} ;;
    --bin) shift; BIN=${1:?} ;;
    --bin-a) shift; BIN_A=${1:?} ;;
    --bin-b) shift; BIN_B=${1:?} ;;
    --model) shift; MODEL=${1:?} ;;
    --tag) shift; TAG=${1:?} ;;
    --group) shift; GROUP=${1:?} ;;
    --mode) shift; MODE=${1:?} ;;
    --concurrency) shift; CONC=${1:?} ;;
    --rpm) shift; RPM=${1:?} ;;
    --budget-usd) shift; BUDGET=${1:?} ;;
    --max-spend-usd) shift; CAP=${1:?} ;;
    --seed) shift; SEED=${1:?} ;;
    --attempts) shift; ATTEMPTS=${1:?} ;;
    --key-file) shift; KEYFILE=${1:?} ;;
    --api-key-env) shift; KEYENV=${1:?} ;;
    --pin-cpus) shift; PIN=${1:?} ;;
    --min-free-gb) shift; MINFREE=${1:?} ;;
    --extra) shift; EXTRA=${1:?} ;;
    -h|--help) sed -n '2,/^set -eu/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) case "$cmd" in status) break ;; *) echo "bench: unknown argument: $1 (try --help)" >&2; exit 2 ;; esac ;;
  esac
  shift
done

die() { echo "bench: $*" >&2; exit 2; }
log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

free_gb() { df -Pk "$BENCH_HOME" 2>/dev/null | awk 'NR==2 { printf "%d", $4 / 1048576 }'; }

# status RUN_DIR...: what a run holds, without opening the episodes.
if [ "$cmd" = status ]; then
  [ "$#" -gt 0 ] || die "status needs run directories"
  for d in "$@"; do
    if [ -f "$d/summary.json" ]; then
      jq -r --arg d "$d" '"\($d): \(.completed)/\(.rollouts) done, \(.infra) infra, \(.capped // 0) capped, pass \((.pass_rate * 100 | floor))%, spent $\(.spent_usd * 10000 | round / 10000)\(if .partial then " (live)" else "" end)"' "$d/summary.json"
    else
      echo "$d: no summary yet"
    fi
  done
  exit 0
fi
case "$cmd" in run|ab) ;; *) die "unknown command: $cmd (run, ab, status)" ;; esac

# ---- preflight
command -v jq >/dev/null 2>&1 || die "jq is needed"
[ -n "$MODEL" ] || die "--model is required"
[ -f "$SUITE/tasks.jsonl" ] && [ -d "$SUITE/blobs" ] || die "no suite in $SUITE (run bench/build.sh)"
case "$SUITE$BENCH_HOME" in *[[:space:]]*) die "paths with whitespace are not supported" ;; esac
mkdir -p "$BENCH_HOME"
SUITE=$(cd "$SUITE" && pwd)
if [ "$cmd" = ab ]; then
  [ -n "$BIN_A" ] && [ -n "$BIN_B" ] || die "ab needs --bin-a and --bin-b"
  [ -x "$BIN_A" ] && [ -x "$BIN_B" ] || die "--bin-a and --bin-b must be executable"
  BIN_A=$(cd "$(dirname "$BIN_A")" && pwd)/$(basename "$BIN_A"); BIN_B=$(cd "$(dirname "$BIN_B")" && pwd)/$(basename "$BIN_B")
else
  [ -n "$BIN" ] || BIN=$(command -v sleipnir || true)
  [ -n "$BIN" ] && [ -x "$BIN" ] || die "no sleipnir binary (--bin, \$SLEIPNIR or PATH)"
  BIN=$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")
fi
if [ -n "$KEYFILE" ]; then
  [ -f "$KEYFILE" ] || die "--key-file: $KEYFILE does not exist"
  perms=$(ls -ld "$KEYFILE" | cut -c2-10)
  [ "$perms" = "rw-------" ] || die "--key-file must be mode 0600 (it is $perms)"
  # shellcheck disable=SC1090 # the file is the user's own
  . "$KEYFILE"
fi
eval "keyval=\${$KEYENV:-}"
[ -n "$keyval" ] || die "no API key: export $KEYENV or pass --key-file"
unset keyval
gb=$(free_gb)
[ "${gb:-0}" -ge "$MINFREE" ] || die "only ${gb:-0} GB free under $BENCH_HOME (need $MINFREE; --min-free-gb)"

stamp=$(date -u +%Y%m%d-%H%M%S)
slug=$(printf '%s' "$MODEL" | tr '/:' '__')
[ -n "$OUT" ] || OUT=$BENCH_HOME/runs/$stamp-$slug${LABEL:+-$LABEL}
GOCACHE_DIR=${BENCH_GOCACHE:-$BENCH_HOME/gocache}
mkdir -p "$OUT" "$GOCACHE_DIR" "$BENCH_HOME/work"
OUT=$(cd "$OUT" && pwd)
pin() { if [ -n "$PIN" ] && command -v taskset >/dev/null 2>&1; then taskset -c "$PIN" "$@"; else "$@"; fi; }

# rollout BIN RUN_DIR GROUP SEED: one invocation (the tasks file, the blob store, the work directory and the cache
# are the benchmark's own); it resumes what RUN_DIR holds.
rollout() {
  _bin=$1 _run=$2 _group=$3 _seed=$4
  # shellcheck disable=SC2086 # EXTRA is a list of flags
  ( cd "$SUITE" && pin "$_bin" rl rollout --tasks tasks.jsonl --blobs blobs --model "$MODEL" --group "$_group" --out "$_run" \
      --work-dir "$BENCH_HOME/work" --set-env "GOCACHE=$GOCACHE_DIR" --set-env GOFLAGS=-mod=mod --set-env GOTOOLCHAIN=local \
      --tag "$TAG" --concurrency "$CONC" --rpm "$RPM" --budget-usd "$BUDGET" --max-spend-usd "$CAP" --seed "$_seed" \
      ${MODE:+--mode "$MODE"} $EXTRA ) >>"$_run/bench.log" 2>&1
}

# settle BIN RUN_DIR GROUP SEED: rollout until nothing is unfinished, pausing when the endpoint or the disk needs time.
settle() {
  _bin=$1 _run=$2 _group=$3 _seed=$4
  _pause=${BENCH_PAUSE:-30} _n=0
  mkdir -p "$_run"
  while :; do
    _n=$((_n + 1))
    while [ "$(free_gb)" -lt "$MINFREE" ]; do log "free disk is under $MINFREE GB: waiting"; sleep 60; done
    _st=0
    rollout "$_bin" "$_run" "$_group" "$_seed" || _st=$?
    if [ -f "$_run/summary.json" ]; then
      log "$(basename "$_run"): attempt $_n exit $_st; $(scripts_status "$_run")"
      [ "$(jq '(.capped // 0)' "$_run/summary.json")" -eq 0 ] || return 3
      [ "$(jq '(.infra + .cancelled + (.pending // 0))' "$_run/summary.json")" -gt 0 ] || [ "$_st" -ne 0 ] || return 0
    else
      # 75: nothing completed (the endpoint was down, or every attempt failed): later may work. Anything else without a
      # summary failed before it began, and trying again will not change that.
      log "$(basename "$_run"): attempt $_n exit $_st; no summary"
      [ "$_st" -eq 75 ] || { log "the run failed before it started (see $_run/bench.log)"; return 2; }
    fi
    [ "$_n" -lt "$ATTEMPTS" ] || return 75
    sleep "$_pause"
    _pause=$((_pause * 2)); [ "$_pause" -le 600 ] || _pause=600
  done
}

scripts_status() { "$0" status "$1"; }

case "$cmd" in
  run)
    log "run: $MODEL, tags $TAG, group $GROUP, into $OUT"
    settle "$BIN" "$OUT" "$GROUP" "$SEED"
    ;;
  ab)
    # Rounds alternate the two binaries and swap who goes first, so a slow hour or a provider wobble lands on both
    # arms alike; round r uses seed r for both, so the arms are paired sample by sample.
    log "ab: $MODEL, tags $TAG, $GROUP rounds, into $OUT"
    rc=0 r=1
    while [ "$r" -le "$GROUP" ]; do
      if [ $((r % 2)) -eq 1 ]; then first=a second=b; else first=b second=a; fi
      for arm in $first $second; do
        if [ "$arm" = a ]; then b=$BIN_A; else b=$BIN_B; fi
        st=0
        settle "$b" "$OUT/$arm/round-$r" 1 "$r" || st=$?
        [ "$st" -eq 0 ] || rc=$st
        [ "$st" -ne 3 ] || { log "the spend cap stopped the run"; exit 3; }
      done
      r=$((r + 1))
    done
    exit "$rc"
    ;;
esac
