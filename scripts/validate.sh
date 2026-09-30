#!/bin/sh
# End-to-end validation against a real endpoint. See docs/VALIDATION.md.
#   MODEL=<model> [BUDGET_USD=3] [OUT=validation] [KEEP_WORK=1] scripts/validate.sh
set -eu

: "${MODEL:?set MODEL to a tool-capable model id (see: sleipnir models)}"
# The agents run the fixture's tests themselves. In accept-edits mode a command that is not read-only is refused when
# nobody can be asked, and the model then spends its steps looking for ways round the refusal (a first run against a real
# model made 24 tool calls that way), so the test and vet commands are allowed. Set the variable yourself to allow more or less.
export SLEIPNIR_PERMISSIONS_ALLOW="${SLEIPNIR_PERMISSIONS_ALLOW:-Bash(go test:*),Bash(go vet:*)}"
BUDGET_USD="${BUDGET_USD:-3}"
BIN="${SLEIPNIR:-sleipnir}"
OUT="${OUT:-validation}"
DATE=$(date -u +%Y%m%d-%H%M%S)
WORK=$(mktemp -d)
# KEEP_WORK=1 keeps the fixture and the session directories (events, inspector input) for a look after a failed step.
trap 'if [ -n "${KEEP_WORK:-}" ]; then echo "kept: $WORK"; else rm -rf "$WORK"; fi' EXIT
mkdir -p "$OUT"
# The steps below run inside the fixture repository: paths given as relative ones must not follow them there.
OUT=$(cd "$OUT" && pwd)
case "$BIN" in */*) BIN="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")" ;; esac
REPORT="$OUT/$DATE-$(echo "$MODEL" | tr '/' '_').json"

echo "== 1. endpoint profile"
# Two probes (one for the report, one to read) run side by side: they use prompts of their own and the endpoint's limit is far above what they need.
"$BIN" doctor --model "$MODEL" --deep --json > "$WORK/doctor.json" &
doctor_json=$!
"$BIN" doctor --model "$MODEL" --deep | sed 's/^/   /' || true
wait "$doctor_json" || true

# A small Go project with a failing test for the agent to fix.
mkdir -p "$WORK/repo" && cd "$WORK/repo"
git init -q
cat > go.mod <<'EOM'
module example.com/slug

go 1.24
EOM
cat > slug.go <<'EOM'
package slug

import "strings"

// Slugify lowercases s and joins its words with hyphens.
func Slugify(s string) string {
	return strings.ReplaceAll(s, " ", "-")
}
EOM
cat > slug_test.go <<'EOM'
package slug

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello World":         "hello-world",
		"  Trim   me  ":       "trim-me",
		"Already-Slugged":     "already-slugged",
		"Punctuation, please!": "punctuation-please",
		"Ünïcode Text":        "ünïcode-text",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
EOM
git add -A && git -c user.name=v -c user.email=v@v commit -qm init

echo "== 2. steady-state cache (single agent)"
"$BIN" run --model "$MODEL" --mode accept-edits --trust-project --budget-usd "$BUDGET_USD" --session-dir "$WORK/s2" --json \
  "Make the tests in slug_test.go pass without changing the tests. Run them to check." > "$WORK/run2.jsonl" || true
"$BIN" inspect --json "$WORK/s2" > "$WORK/inspect2.json" 2>/dev/null || true
go test ./... >/dev/null 2>&1 && echo pass > "$WORK/tests2" || echo fail > "$WORK/tests2"

echo "== 3. compaction recovery (small thresholds)"
git checkout -q -- . && git clean -fdq
# The fixture is small: the default thresholds (compaction is considered at 20k tokens of thread, forced at 60k) are never
# reached, so lower them, and give the agent twenty short notes to read one by one so the thread grows slowly over many
# requests, and a task of five phases so that there are requests left after the compaction is ready. It has to: a compaction is a
# model call over the whole thread (30 to 60 seconds on a reasoning model), a patch is discarded when the thread has grown by more
# than a quarter of what it covered (and at least 3,000 tokens) meanwhile, and one that is ready only for the last request is
# never committed. (A first run with only --context-window 24000 made no compaction at all; with big notes the first patch was
# stale and the second came too late; with thresholds of 300 and 900 tokens the patch folded so little that the planner rightly
# refused it: the compactor keeps a tail of about 700 tokens, so there must be a few thousand more to fold.) The notes are untracked and are cleaned away before the next step.
mkdir -p docs
i=1
while [ "$i" -le 20 ]; do
  {
    echo "# Note $i"
    j=1
    while [ "$j" -le 4 ]; do
      echo "Line $j of note $i: the slug package turns titles into URL fragments; this line only pads the note so that reading it costs some tokens ($i/$j)."
      j=$((j + 1))
    done
  } > "docs/note$i.md"
  i=$((i + 1))
done
SLEIPNIR_CACHE_THREAD_SOFT_LIMIT_TOKENS=2500 SLEIPNIR_CACHE_COMPACT_THRESHOLD_TOKENS=3500 \
"$BIN" run --model "$MODEL" --mode accept-edits --trust-project --budget-usd "$BUDGET_USD" --context-window 24000 --session-dir "$WORK/s3" --json \
  "Do these in order, one at a time, and run the tests after each: (1) read the twenty notes docs/note1.md to docs/note20.md, one file per step, and say in one sentence what each adds; (2) make the tests in slug_test.go pass without changing that file; (3) add a doc comment with two examples above Slugify; (4) add BenchmarkSlugify in a new file slug_bench_test.go; (5) run go vet ./... and fix what it reports." > "$WORK/run3.jsonl" || true
"$BIN" inspect --json "$WORK/s3" > "$WORK/inspect3.json" 2>/dev/null || true
go test ./... >/dev/null 2>&1 && echo pass > "$WORK/tests3" || echo fail > "$WORK/tests3"

echo "== 4. swarm dispatch"
git checkout -q -- . && git clean -fdq
"$BIN" swarm 4 --model "$MODEL" --mode accept-edits --trust-project --budget-usd "$BUDGET_USD" --verify "go test ./..." --session-dir "$WORK/s4" --json \
  "Fix Slugify so the tests pass, add a doc comment with examples, and add a benchmark. Split the work across workers." > "$WORK/run4.jsonl" || true
"$BIN" inspect --json "$WORK/s4" > "$WORK/inspect4.json" 2>/dev/null || true
go test ./... >/dev/null 2>&1 && echo pass > "$WORK/tests4" || echo fail > "$WORK/tests4"

echo "== 6. RL smoke (one task, two samples, replay check, export)"
git checkout -q -- . && git clean -fdq
BASE=$(git rev-parse HEAD)
cat > "$WORK/tasks.jsonl" <<EOT
{"id":"slug-0001","kind":"fix","repo":{"path":"$WORK/repo","commit":"$BASE"},"prompt":"Make the tests in slug_test.go pass without changing the tests.","team":{"mode":"single"},"verifier":{"cmd":"go test ./... -count=1","timeout_s":120,"pass":"exit0","protected":["*_test.go","go.mod"]},"budget":{"steps":40,"requests":60,"wall_s":600},"tags":["go","validation"]}
EOT
"$BIN" rl rollout --tasks "$WORK/tasks.jsonl" --model "$MODEL" --group 2 --out "$WORK/rl" --json > "$WORK/rollout.json" 2>"$WORK/rollout.err" || true
"$BIN" rl verify "$WORK/rl" > "$WORK/verify.txt" 2>&1 || true
"$BIN" rl export "$WORK/rl" --format steps --advantage grpo -o "$WORK/steps.jsonl" > "$WORK/export.txt" 2>&1 || true

# The verdict: the pass criteria of docs/VALIDATION.md, computed from the inspector's summaries.
if command -v jq >/dev/null 2>&1; then
  echo "== verdict (criteria: docs/VALIDATION.md)"
  verdict() { # label file tests-file criteria-jq-expression
    [ -s "$2" ] || { printf '   %-16s no inspector summary (the run failed before it logged anything)\n' "$1"; return; }
    jq -r --arg label "$1" --arg tests "$(cat "$3" 2>/dev/null || echo '?')" '
      def pct: (. * 100 | floor | tostring) + "%";
      [ $label,
        "requests \(.totals.requests)",
        "hit \(.cache.hit_ratio | pct) (steady \(.cache.steady_hit_ratio | pct))",
        "first requests warm \(.cache.warm_first)/\(.cache.first_requests)",
        "compactions \(.compaction.commits)",
        "anomalies \(.anomalies.total)",
        "cost $\(.cost.actual.total * 1000000 | round / 1000000)",
        "tests \($tests)" ] | "   " + join("  ·  ")' "$2"
  }
  verdict "2 steady state"   "$WORK/inspect2.json" "$WORK/tests2"
  verdict "3 compaction"     "$WORK/inspect3.json" "$WORK/tests3"
  verdict "4 swarm"          "$WORK/inspect4.json" "$WORK/tests4"
  [ ! -s "$WORK/inspect2.json" ] || jq -r '
    "   step 2 criteria: steady hit ratio >= 80% " + (if .cache.steady_hit_ratio >= 0.8 then "OK" else "NOT MET" end) +
    ", no drift anomalies " + (if .anomalies.drift == 0 then "OK" else "NOT MET (\(.anomalies.drift))" end)' "$WORK/inspect2.json"
  [ ! -s "$WORK/inspect3.json" ] || jq -r '
    "   step 3 criteria: at least one compaction commit " + (if .compaction.commits >= 1 then "OK" else "NOT MET (no patch was committed: see the compact.* events; enlarge the task)" end) +
    ", hit ratio after a commit >= 60% " + (if .cache.rebase_requests == 0 then "n/a (no request after a commit)" elif .cache.rebase_hit_ratio >= 0.6 then "OK (\(.cache.rebase_hit_ratio * 100 | floor)%)" else "NOT MET (\(.cache.rebase_hit_ratio * 100 | floor)%)" end)' "$WORK/inspect3.json"
  [ ! -s "$WORK/inspect4.json" ] || jq -r '
    "   step 4 criteria: workers'"'"' first requests warm >= 50% " + (if .cache.first_requests > 0 and (.cache.warm_first / .cache.first_requests) >= 0.5 then "OK" else "NOT MET" end)' "$WORK/inspect4.json"
  echo "   step 6 RL: rollout $(jq -c '{completed: .completed, infra: .infra, cancelled: .cancelled}' "$WORK/rollout.json" 2>/dev/null || echo 'no summary')"
  echo "   step 6 replay check: $(head -c 300 "$WORK/verify.txt" | tr '\n' ' ')"
  echo "   step 6 export: $(wc -l < "$WORK/steps.jsonl" 2>/dev/null || echo 0) step records"
fi

# Assemble the report (tools: jq if present, else raw file paths).
if command -v jq >/dev/null 2>&1; then
  jq -n --arg model "$MODEL" --arg date "$DATE" \
     --slurpfile doctor "$WORK/doctor.json" \
     --slurpfile i2 "$WORK/inspect2.json" --slurpfile i3 "$WORK/inspect3.json" --slurpfile i4 "$WORK/inspect4.json" \
     '{model:$model, date:$date, doctor:$doctor[0], steady_state:$i2[0], compaction:$i3[0], swarm:$i4[0]}' > "$REPORT" 2>/dev/null || true
fi
[ -s "$REPORT" ] || cp "$WORK/doctor.json" "$REPORT"
echo "report: $REPORT"
