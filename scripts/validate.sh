#!/bin/sh
# End-to-end validation against a real endpoint. See docs/VALIDATION.md.
#   MODEL=<model> [BUDGET_USD=3] scripts/validate.sh
set -eu

: "${MODEL:?set MODEL to a tool-capable model id (see: sleipnir models)}"
BUDGET_USD="${BUDGET_USD:-3}"
BIN="${SLEIPNIR:-sleipnir}"
OUT="${OUT:-validation}"
DATE=$(date -u +%Y%m%d-%H%M%S)
REPORT="$OUT/$DATE-$(echo "$MODEL" | tr '/' '_').json"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT"

echo "== 1. endpoint profile"
"$BIN" doctor --model "$MODEL" --deep --json > "$WORK/doctor.json"
"$BIN" doctor --model "$MODEL" --deep | sed 's/^/   /' || true

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

echo "== 3. compaction recovery (small window)"
git checkout -q -- . && git clean -fdq
"$BIN" run --model "$MODEL" --mode accept-edits --trust-project --budget-usd "$BUDGET_USD" --context-window 24000 --session-dir "$WORK/s3" --json \
  "Make the tests in slug_test.go pass without changing the tests. Explore the repository thoroughly first and explain each step." > "$WORK/run3.jsonl" || true
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
        "cost $\(.cost.actual.total * 10000 | floor / 10000)",
        "tests \($tests)" ] | "   " + join("  ·  ")' "$2"
  }
  verdict "2 steady state"   "$WORK/inspect2.json" "$WORK/tests2"
  verdict "3 compaction"     "$WORK/inspect3.json" "$WORK/tests3"
  verdict "4 swarm"          "$WORK/inspect4.json" "$WORK/tests4"
  [ ! -s "$WORK/inspect2.json" ] || jq -r '
    "   step 2 criteria: steady hit ratio >= 80% " + (if .cache.steady_hit_ratio >= 0.8 then "OK" else "NOT MET" end) +
    ", no drift anomalies " + (if .anomalies.drift == 0 then "OK" else "NOT MET (\(.anomalies.drift))" end)' "$WORK/inspect2.json"
  [ ! -s "$WORK/inspect3.json" ] || jq -r '
    "   step 3 criteria: at least one compaction commit " + (if .compaction.commits >= 1 then "OK" else "NOT MET (raise the task size or lower --context-window)" end)' "$WORK/inspect3.json"
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
