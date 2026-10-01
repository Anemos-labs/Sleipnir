#!/bin/sh
# Build the benchmark suite that bench/suite.json describes, outside the repository.
#
#   bench/build.sh [--out DIR] [--bin PATH] [--parts LIST] [--no-admit] [--force] [--check] [--update-lock]
#
#   --out DIR       where the suite is built (default: $BENCH_HOME/suite; BENCH_HOME defaults to ~/.sleipnir-bench)
#   --bin PATH      the sleipnir binary to use (default: $SLEIPNIR, else built from this checkout into DIR/bin)
#   --parts LIST    comma-separated subset of: mutations,mined,fixtures,composite,recall (default: all); a partial
#                   build writes no lock
#   --no-admit      skip the admission check (every task is then unverified: for development only)
#   --force         rebuild parts that already exist in DIR
#   --check         compare the result with the lock in bench/suite.json (same Go version: the suite must be
#                   byte-identical; another Go version: the std-mini corpus may differ, the comparison is skipped)
#   --update-lock   write the result into the lock in bench/suite.json
#
# DIR receives
#   tasks.jsonl       the admitted tasks, one per line, in a fixed order
#   blobs/            hidden verifier files and reference solutions (keep it next to tasks.jsonl)
#   repos/            the repositories the tasks name, relative to the directory a run starts in: run from DIR
#   suite.lock.json   what was built: Go version, corpus identity, counts, quarantined tasks, sha256 of tasks.jsonl
#
# The Go corpus ("std-mini": self-contained standard library packages) is copied from the local GOROOT; the
# mined tasks come from this repository's history at the revision the recipe pins (a full clone is needed: a
# shallow checkout lacks it). Needs go, git, jq.
#
# A task is admitted when, three times in a row, its verifier fails on the starting state and passes with the
# reference solution (sleipnir rl tasks check --verify-repeats 3). Tasks that are not stay out of tasks.jsonl and
# are listed in the lock as quarantined, with the reason.
set -eu
LC_ALL=C
export LC_ALL
cd "$(dirname "$0")/.."
ROOT=$(pwd)
RECIPE=bench/suite.json

OUT=${BENCH_HOME:-$HOME/.sleipnir-bench}/suite
BIN=${SLEIPNIR:-}
PARTS=mutations,mined,fixtures,composite,recall
ADMIT=1 FORCE=0 CHECK=0 UPDATE=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --out) shift; OUT=${1:?--out needs a directory} ;;
    --bin) shift; BIN=${1:?--bin needs a path} ;;
    --parts) shift; PARTS=${1:?--parts needs a list} ;;
    --no-admit) ADMIT=0 ;;
    --force) FORCE=1 ;;
    --check) CHECK=1 ;;
    --update-lock) UPDATE=1 ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) echo "bench/build.sh: unknown argument: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done

for tool in go git jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "bench/build.sh: $tool is needed" >&2; exit 2; }
done
[ -f "$RECIPE" ] || { echo "bench/build.sh: $RECIPE is missing" >&2; exit 2; }
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
case "$OUT" in *[[:space:]]*) echo "bench/build.sh: $OUT contains whitespace; choose another --out" >&2; exit 2 ;; esac
mkdir -p "$OUT/parts" "$OUT/repos" "$OUT/blobs" "$OUT/work"

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
want() { case ",$PARTS," in *",$1,"*) return 0 ;; *) return 1 ;; esac; }
cfg() { jq -r "$1" "$RECIPE"; }
log() { printf '%s\n' "bench/build.sh: $*" >&2; }

# The binary.
if [ -z "$BIN" ]; then
  mkdir -p "$OUT/bin"
  BIN="$OUT/bin/sleipnir"
  log "building $BIN"
  go build -o "$BIN" ./cmd/sleipnir
fi
[ -x "$BIN" ] || { echo "bench/build.sh: $BIN is not executable" >&2; exit 2; }

# Every sleipnir rl command that runs code uses the same environment: one Go cache shared by all the work, no network, no toolchain switching.
GOCACHE_DIR=${BENCH_GOCACHE:-$OUT/gocache}
mkdir -p "$GOCACHE_DIR"
RIG="--work-dir $OUT/work --set-env GOCACHE=$GOCACHE_DIR --set-env GOFLAGS=-mod=mod --set-env GOTOOLCHAIN=local"
[ "${BENCH_NO_NETNS:-0}" = 1 ] && RIG="$RIG --no-net-isolation"

have() { [ -s "$OUT/parts/$1.jsonl" ] && [ "$FORCE" = 0 ]; }
GOVER=$(go env GOVERSION)

# 1. The Go corpus.
STDMINI_SHA=
STDMINI_COMMIT=
if want mutations || want composite; then
  if [ ! -d "$OUT/repos/stdmini/.git" ] || [ "$FORCE" = 1 ]; then
    rm -rf "$OUT/repos/stdmini"
    log "building the std-mini corpus ($GOVER)"
    # a corpus that failed halfway is removed: its .git would be taken for a finished one by the next run
    if ! info=$(go run ./bench/tools/stdmini --out "$OUT/repos/stdmini" --packages "$(cfg .stdmini.packages)"); then
      rm -rf "$OUT/repos/stdmini"
      exit 1
    fi
    printf '%s\n' "$info" > "$OUT/repos/stdmini.info"
  fi
  STDMINI_SHA=$(sed -n 's/^tree_sha256 //p' "$OUT/repos/stdmini.info")
  STDMINI_COMMIT=$(sed -n 's/^commit //p' "$OUT/repos/stdmini.info")
fi

# 2. Bugs injected into the corpus: mutations the packages' own tests catch. The first K are the smoke set.
if want mutations && ! have mut; then
  log "mutations"
  # shellcheck disable=SC2086 # RIG is a list of flags
  "$BIN" rl taskgen mutate --repo "$OUT/repos/stdmini" --repo-path repos/stdmini --id-prefix sm \
    --max "$(cfg .parts.mutations.max)" --seed "$(cfg .parts.mutations.seed)" \
    -o "$OUT/parts/mut.raw.jsonl" --blobs "$OUT/blobs" $RIG >/dev/null
  jq -c -s --argjson k "$(cfg .parts.mutations.smoke)" \
    'to_entries[] | .key as $i | .value | .tags = ((.tags + [if $i < $k then "smoke" else "core" end]) | unique)' \
    "$OUT/parts/mut.raw.jsonl" > "$OUT/parts/mut.jsonl"
fi

# This repository's history at the pinned revision: the source of the mined tasks and of the recall tasks (the standard
# library copies hold no distinctive constants to ask about).
ensure_history() {
  REV=$(cfg .parts.mined.rev)
  if [ ! -d "$OUT/repos/sleipnir/.git" ]; then
    log "cloning this repository's history"
    [ "$(git rev-parse --is-shallow-repository)" = false ] || { echo "bench/build.sh: this is a shallow clone; the mined tasks need the history (git fetch --unshallow)" >&2; exit 2; }
    git clone -q --no-local "$ROOT" "$OUT/repos/sleipnir"
  fi
  git -C "$OUT/repos/sleipnir" cat-file -e "$REV^{commit}" 2>/dev/null || { echo "bench/build.sh: revision $REV is not in the history" >&2; exit 2; }
}

# 3. Real commits of this repository: a commit that changes source and tests is a task.
if want mined && ! have mined; then
  ensure_history
  log "mined tasks at $REV"
  # shellcheck disable=SC2086
  "$BIN" rl taskgen git --repo "$OUT/repos/sleipnir" --repo-path repos/sleipnir --rev "$REV" --lang go --id-prefix sl \
    --max "$(cfg .parts.mined.max)" --max-files "$(cfg .parts.mined.max_files)" --max-lines "$(cfg .parts.mined.max_lines)" \
    -o "$OUT/parts/mined.raw.jsonl" --blobs "$OUT/blobs" $RIG >/dev/null
  jq -c '.tags = ((.tags + ["core"]) | unique)' "$OUT/parts/mined.raw.jsonl" > "$OUT/parts/mined.jsonl"
fi

# 4. Authored fixtures (other languages, tasks built from a specification).
if want fixtures && ! have fixtures; then
  FDIR=$(cfg .parts.fixtures.dir)
  if ls "$FDIR"/*/task.json >/dev/null 2>&1; then
    log "fixtures"
    "$BIN" rl taskgen fixture --dir "$FDIR" --repo-root "$OUT/repos" --repo-path-prefix repos \
      -o "$OUT/parts/fixtures.raw.jsonl" --blobs "$OUT/blobs" >/dev/null
    jq -c 'if (.tags | any(. == "smoke" or . == "core" or . == "long" or . == "swarm")) then . else .tags += ["core"] end' \
      "$OUT/parts/fixtures.raw.jsonl" > "$OUT/parts/fixtures.jsonl"
  else
    log "no fixtures in $FDIR yet"
    : > "$OUT/parts/fixtures.jsonl"
  fi
fi

# 5. Composite tasks: k independent mutation tasks of one repository, for a manager and workers.
if want composite && ! have composite; then
  [ -s "$OUT/parts/mut.jsonl" ] || { echo "bench/build.sh: composite tasks need the mutations part" >&2; exit 2; }
  log "composite tasks"
  "$BIN" rl taskgen composite -k "$(cfg .parts.composite.k)" --max "$(cfg .parts.composite.max)" --seed "$(cfg .parts.composite.seed)" \
    -o "$OUT/parts/composite.raw.jsonl" "$OUT/parts/mut.raw.jsonl" >/dev/null
  jq -c '.tags = ((.tags + ["swarm"]) | unique)' "$OUT/parts/composite.raw.jsonl" > "$OUT/parts/composite.jsonl"
fi

# 6. Memory tasks: read a fact, read a dozen files, state the fact; the window is small, so the history is compacted.
if want recall && ! have recall; then
  ensure_history
  log "recall tasks at $REV"
  "$BIN" rl taskgen recall --repo "$OUT/repos/sleipnir" --repo-path repos/sleipnir --rev "$REV" --lang go --max "$(cfg .parts.recall.max)" \
    --seed "$(cfg .parts.recall.seed)" -o "$OUT/parts/recall.raw.jsonl" >/dev/null
  jq -c '.tags = ((.tags + ["long"]) | unique)' "$OUT/parts/recall.raw.jsonl" > "$OUT/parts/recall.jsonl"
fi

# 7. Assemble, in a fixed order, with the recipe's budget caps applied to single-agent tasks: every model gets the same allowance.
: > "$OUT/tasks.all.jsonl"
for p in mut mined fixtures composite recall; do
  [ -s "$OUT/parts/$p.jsonl" ] && cat "$OUT/parts/$p.jsonl" >> "$OUT/tasks.all.jsonl"
done
jq -c --argjson c "$(jq -c .budget_caps "$RECIPE")" '
  def cap($k): if has($k) and .[$k] != null and .[$k] > 0 then .[$k] = ([.[$k], $c[$k]] | min) else .[$k] = $c[$k] end;
  if .team.mode == "swarm" then . else .budget = ((.budget // {}) | cap("steps") | cap("requests") | cap("wall_s")) end' \
  "$OUT/tasks.all.jsonl" > "$OUT/tasks.all.capped" && mv "$OUT/tasks.all.capped" "$OUT/tasks.all.jsonl"
[ -s "$OUT/tasks.all.jsonl" ] || { echo "bench/build.sh: nothing was built" >&2; exit 1; }
dups=$(jq -r .id "$OUT/tasks.all.jsonl" | sort | uniq -d)
[ -z "$dups" ] || { echo "bench/build.sh: duplicate task ids: $dups" >&2; exit 1; }

# 8. Admission: the verifier fails on the start and passes with the reference solution, every time out of three.
rm -f "$OUT/admit.json"
if [ "$ADMIT" = 1 ]; then
  REPEATS=$(cfg .admit.repeats)
  log "admission ($REPEATS repeats each): $(wc -l < "$OUT/tasks.all.jsonl" | tr -d ' ') tasks"
  # rl tasks check exits non-zero when any task is unsound; the report says which. It runs from the suite directory, where
  # the tasks' relative repository paths resolve, exactly as a benchmark run will.
  # shellcheck disable=SC2086
  ( cd "$OUT" && "$BIN" rl tasks check tasks.all.jsonl --blobs blobs --verify-repeats "$REPEATS" --concurrency 3 \
      --report admit.json $RIG ) >"$OUT/admit.log" 2>&1 || true
  [ -s "$OUT/admit.json" ] || { echo "bench/build.sh: the admission check produced no report; see $OUT/admit.log" >&2; exit 1; }
  # A check that rejects most of what was built is a broken check (or a broken build), not a hundred bad tasks.
  bad=$(jq '[.[] | select((.ok or .skipped) | not)] | length' "$OUT/admit.json")
  total=$(jq 'length' "$OUT/admit.json")
  if [ "$((bad * 2))" -gt "$total" ]; then
    echo "bench/build.sh: admission rejected $bad of $total tasks; that points at the build, not the tasks. See $OUT/admit.log and $OUT/admit.json" >&2
    exit 1
  fi
fi
QUAR=$(jq -c '.quarantine' "$RECIPE")
if [ -s "$OUT/admit.json" ]; then
  jq -c --argjson q "$QUAR" --slurpfile a "$OUT/admit.json" \
    '($a[0] | map(select(.ok or .skipped | not) | .id)) as $bad | select((.id as $i | ($bad + $q) | index($i)) | not)' \
    "$OUT/tasks.all.jsonl" > "$OUT/tasks.jsonl"
else
  jq -c --argjson q "$QUAR" 'select((.id as $i | $q | index($i)) | not)' "$OUT/tasks.all.jsonl" > "$OUT/tasks.jsonl"
fi
N=$(wc -l < "$OUT/tasks.jsonl" | tr -d ' ')

# 9. The lock.
SUM=$(sha256 "$OUT/tasks.jsonl")
QLIST='[]'
if [ -s "$OUT/admit.json" ]; then
  QLIST=$(jq -c --argjson q "$QUAR" '[.[] | select((.ok or .skipped) | not) | {id, reason: (.reason | split("\n")[0] | .[0:200])}] + [$q[] | {id: ., reason: "quarantined by the recipe"}]' "$OUT/admit.json")
fi
TAGS=$(jq -c -s '[.[].tags[]] | group_by(.) | map({(.[0]): length}) | add' "$OUT/tasks.jsonl")
jq -n --arg go "$GOVER" --arg sha "$SUM" --argjson n "$N" --argjson tags "$TAGS" --argjson quarantined "$QLIST" \
  --arg sm "$STDMINI_SHA" --arg smc "$STDMINI_COMMIT" --arg rev "$(cfg .parts.mined.rev)" --argjson admitted "$ADMIT" --arg parts "$PARTS" \
  '{go: $go, parts: ($parts | split(",")), admitted: ($admitted == 1), tasks: $n, tasks_sha256: $sha, tags: $tags,
    stdmini: {tree_sha256: $sm, commit: $smc}, mined_rev: $rev, quarantined: $quarantined}' > "$OUT/suite.lock.json"
log "$N tasks in $OUT/tasks.jsonl (sha256 $SUM); tags: $TAGS"
[ "$(jq '.quarantined | length' "$OUT/suite.lock.json")" = 0 ] || log "quarantined: $(jq -c '[.quarantined[].id]' "$OUT/suite.lock.json")"

full=1
[ "$PARTS" = mutations,mined,fixtures,composite,recall ] || full=0
if [ "$UPDATE" = 1 ]; then
  [ "$full" = 1 ] && [ "$ADMIT" = 1 ] || { echo "bench/build.sh: --update-lock needs a full, admitted build" >&2; exit 2; }
  jq --slurpfile l "$OUT/suite.lock.json" '.go = $l[0].go | .lock = {tasks: $l[0].tasks, tasks_sha256: $l[0].tasks_sha256, stdmini: $l[0].stdmini, tags: $l[0].tags, quarantined: $l[0].quarantined}' \
    "$RECIPE" > "$RECIPE.new" && mv "$RECIPE.new" "$RECIPE"
  log "lock written to $RECIPE"
fi
if [ "$CHECK" = 1 ]; then
  [ "$full" = 1 ] && [ "$ADMIT" = 1 ] || { echo "bench/build.sh: --check needs a full, admitted build" >&2; exit 2; }
  locked=$(jq -r '.lock.tasks_sha256 // empty' "$RECIPE")
  [ -n "$locked" ] || { echo "bench/build.sh: $RECIPE has no lock yet (--update-lock)" >&2; exit 2; }
  if [ "$(cfg .go)" != "$GOVER" ]; then
    log "check skipped: the lock was made with $(cfg .go), this is $GOVER (the std-mini corpus is copied from GOROOT)"
  elif [ "$locked" = "$SUM" ]; then
    log "check: the rebuild matches the lock"
  else
    echo "bench/build.sh: the rebuild differs from the lock: $SUM, the lock has $locked" >&2
    exit 1
  fi
fi
