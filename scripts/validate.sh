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

echo "== 3. compaction recovery (small window)"
git checkout -q -- . && git clean -fdq
"$BIN" run --model "$MODEL" --mode accept-edits --trust-project --budget-usd "$BUDGET_USD" --context-window 24000 --session-dir "$WORK/s3" --json \
  "Make the tests in slug_test.go pass without changing the tests. Explore the repository thoroughly first and explain each step." > "$WORK/run3.jsonl" || true
"$BIN" inspect --json "$WORK/s3" > "$WORK/inspect3.json" 2>/dev/null || true

echo "== 4. swarm dispatch"
git checkout -q -- . && git clean -fdq
"$BIN" swarm 4 --model "$MODEL" --mode accept-edits --trust-project --budget-usd "$BUDGET_USD" --verify "go test ./..." --session-dir "$WORK/s4" --json \
  "Fix Slugify so the tests pass, add a doc comment with examples, and add a benchmark. Split the work across workers." > "$WORK/run4.jsonl" || true
"$BIN" inspect --json "$WORK/s4" > "$WORK/inspect4.json" 2>/dev/null || true

echo "== 6. RL smoke"
"$BIN" rl verify "$WORK/s2" > "$WORK/verify2.txt" 2>&1 || true

# Assemble the report (tools: jq if present, else raw file paths).
if command -v jq >/dev/null 2>&1; then
  jq -n --arg model "$MODEL" --arg date "$DATE" \
     --slurpfile doctor "$WORK/doctor.json" \
     --slurpfile i2 "$WORK/inspect2.json" --slurpfile i3 "$WORK/inspect3.json" --slurpfile i4 "$WORK/inspect4.json" \
     '{model:$model, date:$date, doctor:$doctor[0], steady_state:$i2[0], compaction:$i3[0], swarm:$i4[0]}' > "$REPORT" 2>/dev/null || true
fi
[ -s "$REPORT" ] || cp "$WORK/doctor.json" "$REPORT"
echo "report: $REPORT"
