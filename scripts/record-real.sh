#!/bin/sh
# Record a REAL session of the chat for docs/media: a real model, real tools, real timing, nothing scripted but the person's keys.
#
#   scripts/record-real.sh --scenario chat|first-run|swarm --model provider/model --out docs/media/real-chat.svg [--key-env HEIMDALL_API_KEY] [--goal TEXT] [--max-gap 1.5s]
#   STILL=file.svg [STILLAT=40s] in the environment also writes one moment as a static SVG
#
# --scenario swarm runs a real team (a manager and three workers (four agents) in git worktrees, a verifier) on three small independent tasks and draws the session's
# event log as the cockpit with `sleipnir replay --record`, SPEED (default 6) times faster than it happened. --scenario first-run starts with no configuration at all: the first provider chosen with the arrows and enter, the key typed at the hidden prompt, --search WORDS typed to narrow the model list, enter, then a one-line goal. A temporary project with a failing test and a temporary home (which receives the key from the environment variable named by --key-env, in its own
# auth.json, and is removed afterwards) are made; tmux runs `sleipnir` under script(1), which writes everything the terminal showed with its timing;
# a typist (this script) types `/allow tests`, then the goal, answers the approval question with the key 1, opens the stats page (ctrl+t) and the agents page (ctrl+g), and ends the chat; `sleipnir term-svg`
# plays the recording into the repository's own terminal emulator and writes the animated SVG. A wait longer than --max-gap is shortened to it
# (the model thinks for seconds, and the picture would be still): the pictures say so. Costs a few cents; needs tmux, script (util-linux), go, git.
set -eu
LC_ALL=C
export LC_ALL
cd "$(dirname "$0")/.."

STILL=${STILL:-} STILLAT=${STILLAT:-}
SCENARIO=chat SEARCH= SPEED=${SPEED:-6}
MODEL= OUT= KEYENV=HEIMDALL_API_KEY MAXGAP=1500ms COLS=110 ROWS=30
DEFAULTGOAL="Slugify turns \"Hello, World!\" into \"hello,-world!\" and the test in slug_test.go fails. Fix it so the tests pass, and run them."
GOAL=$DEFAULTGOAL
while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    --model) shift; MODEL=${1:?} ;;
    --out) shift; OUT=${1:?} ;;
    --key-env) shift; KEYENV=${1:?} ;;
    --goal) shift; GOAL=${1:?} ;;
    --scenario) shift; SCENARIO=${1:?} ;;
    --search) shift; SEARCH=${1:?} ;;
    --max-gap) shift; MAXGAP=${1:?} ;;
    *) echo "record-real: unknown option $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done
[ -n "$MODEL" ] && [ -n "$OUT" ] || { echo "record-real: --model and --out are required" >&2; exit 2; }
eval "key=\${$KEYENV:-}"
[ -n "$key" ] || { echo "record-real: $KEYENV is not set" >&2; exit 2; }
for t in tmux script go git; do command -v "$t" >/dev/null || { echo "record-real: $t is needed" >&2; exit 2; }; done

BIN=$(pwd)/bin/sleipnir
go build -o "$BIN" ./cmd/sleipnir
tmp=/tmp/rec # a short, tidy path: it shows in the picture
rm -rf "$tmp"
mkdir -p "$tmp"
trap 'tmux kill-session -t sleipnir-rec 2>/dev/null || true; sleep 1; rm -rf "$tmp" 2>/dev/null || true' EXIT
home=$tmp/home proj=$tmp/slug
mkdir -p "$home/.sleipnir" "$proj"
umask 077
if [ "$SCENARIO" != first-run ]; then
  printf '{"%s":"%s"}\n' "$KEYENV" "$key" > "$home/.sleipnir/auth.json"
  umask 022
  printf '{"models":{"default":"%s"},"permissions":{"mode":"default"}}\n' "$MODEL" > "$home/.sleipnir/config.json"
fi
umask 022
cat > "$proj/go.mod" <<'GO'
module example.com/slug

go 1.24
GO
if [ "$SCENARIO" != swarm ]; then
cat > "$proj/slug.go" <<'GO'
package slug

import "strings"

// Slugify makes a URL slug: lower case, words joined by single hyphens, nothing else.
func Slugify(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), " ", "-")
}
GO
cat > "$proj/slug_test.go" <<'GO'
package slug

import "testing"

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Hello World":     "hello-world",
		"Hello, World!":   "hello-world",
		"  many   spaces": "many-spaces",
		"Go 1.25 is out":  "go-1-25-is-out",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
GO
fi
git -C "$proj" init -q && git -C "$proj" add -A && git -C "$proj" -c user.name=rec -c user.email=rec@example.com commit -qm "start"
[ "$SCENARIO" = swarm ] || (cd "$proj" && go test ./... >/dev/null 2>&1) || true # warm the build cache: the recording shows the tests, not the toolchain

if [ "$SCENARIO" = swarm ]; then
  for pkg in a b c; do mkdir -p "$proj/$pkg"; done
  printf 'package a\n\n// Reverse returns s with its runes in reverse order.\nfunc Reverse(s string) string { panic("todo") }\n' > "$proj/a/a.go"
  printf 'package a\n\nimport "testing"\n\nfunc TestReverse(t *testing.T) {\n\tif got := Reverse("héllo"); got != "olléh" {\n\t\tt.Fatalf("got %%q", got)\n\t}\n}\n' > "$proj/a/a_test.go"
  printf 'package b\n\n// Fib returns the nth Fibonacci number, with Fib(0) = 0 and Fib(1) = 1.\nfunc Fib(n int) int { panic("todo") }\n' > "$proj/b/b.go"
  printf 'package b\n\nimport "testing"\n\nfunc TestFib(t *testing.T) {\n\tif got := Fib(10); got != 55 {\n\t\tt.Fatalf("got %%d", got)\n\t}\n}\n' > "$proj/b/b_test.go"
  printf 'package c\n\n// Dedup returns xs without repeated values, keeping the first of each, in order.\nfunc Dedup(xs []int) []int { panic("todo") }\n' > "$proj/c/c.go"
  printf 'package c\n\nimport (\n\t"reflect"\n\t"testing"\n)\n\nfunc TestDedup(t *testing.T) {\n\tif got := Dedup([]int{3, 1, 3, 2, 1}); !reflect.DeepEqual(got, []int{3, 1, 2}) {\n\t\tt.Fatalf("got %%v", got)\n\t}\n}\n' > "$proj/c/c_test.go"
  git -C "$proj" add -A && git -C "$proj" -c user.name=rec -c user.email=rec@example.com commit -qm "three stubs"
  SG="Each of the packages a, b and c has one function that panics with todo, and a test. Implement all three in parallel, one worker for each package, and make go test ./... pass."
  [ "$GOAL" != "$DEFAULTGOAL" ] || GOAL=$SG
  (cd "$proj" && env -u $KEYENV HOME=$home LC_ALL=C.UTF-8 "$BIN" swarm 4 --model "$MODEL" --mode accept-edits --verify "go test {dirs}" --isolation worktree --allow tests "$GOAL") > "$tmp/swarm.out" 2>&1 || true
  tail -3 "$tmp/swarm.out" >&2
  id=$(ls -t "$home/.sleipnir/sessions" | head -1)
  HOME=$home "$BIN" replay "$id" --final --cols "$COLS" --rows 36 >&2 || true
  HOME=$home "$BIN" replay "$id" --record "$OUT" --cols "$COLS" --rows 36 --speed "$SPEED"
  exit 0
fi

s=sleipnir-rec
tmux kill-session -t $s 2>/dev/null || true
tmux new-session -d -s $s -x "$COLS" -y "$ROWS" "cd $proj && env -u $KEYENV HOME=$home LC_ALL=C.UTF-8 TERM=xterm-256color COLORTERM=truecolor script -q --log-out $tmp/o --log-timing $tmp/t --logging-format advanced -c '$BIN'; touch $tmp/done"
pane() { tmux capture-pane -t $s -p 2>/dev/null || true; }
waitfor() { # waitfor PATTERN SECONDS
  i=0
  while [ "$i" -lt $(( $2 * 2 )) ]; do pane | grep -Eq "$1" && return 0; sleep 0.5; i=$((i + 1)); done
  echo "record-real: gave up waiting for: $1" >&2; pane >&2; return 1
}
typist() { # one key at a time, as a hand would
  printf '%s' "$1" | awk 'BEGIN{ORS=""} {for(i=1;i<=length($0);i++) print substr($0,i,1) "\n"}' | while IFS= read -r ch; do
    if [ -z "$ch" ]; then tmux send-keys -t $s Space; else tmux send-keys -t $s -l -- "$ch"; fi
    sleep 0.045
  done
}
if [ "$SCENARIO" = first-run ]; then
  waitfor 'up/down move' 60; sleep 2
  tmux send-keys -t $s Down; sleep 0.8; tmux send-keys -t $s Up; sleep 1; tmux send-keys -t $s Enter # the arrows move the highlight; Heimdall, the first row, is chosen
  waitfor 'Paste your' 20; sleep 2
  typist "$key"; sleep 0.8; tmux send-keys -t $s Enter
  waitfor 'type to search' 60; sleep 2.5
  typist "$SEARCH"; sleep 1.2; tmux send-keys -t $s Enter # typing narrows the list, enter chooses the highlighted row
fi
waitfor 'Type a goal' 60
sleep 1.5
typist "/allow tests"; sleep 0.6; tmux send-keys -t $s Enter; sleep 1.5
typist "$GOAL"; sleep 0.8; tmux send-keys -t $s Enter
# the run: answer every question with 1, until the turn's summary line shows
i=0
while [ "$i" -lt 900 ]; do
  p=$(pane)
  if printf '%s' "$p" | grep -q 'esc says no'; then sleep 1.6; tmux send-keys -t $s 1; sleep 1; fi
  if printf '%s' "$p" | grep -Eq '[0-9]+ steps? .*cache hit' && ! printf '%s' "$p" | grep -q 'esc to interrupt'; then break; fi
  sleep 0.5; i=$((i + 1))
done
[ "$i" -lt 900 ] || { echo "record-real: the turn did not end in 450 s" >&2; pane >&2; exit 1; }
echo "record-real: the turn took about $((i / 2)) s" >&2
sleep 3
# the two pages the footer names: ctrl+t is the stats page, ctrl+g the agents page
tmux send-keys -t $s C-t; sleep 3.5; tmux send-keys -t $s C-g; sleep 4
typist "/exit"; sleep 0.5; tmux send-keys -t $s Enter
i=0; while [ ! -e "$tmp/done" ] && [ "$i" -lt 60 ]; do sleep 0.5; i=$((i + 1)); done
"$BIN" term-svg --log "$tmp/o" --timing "$tmp/t" --out "$OUT" --cols "$COLS" --rows "$ROWS" --max-gap "$MAXGAP" ${STILL:+--still "$STILL" ${STILLAT:+--still-at "$STILLAT"}} --title "sleipnir  ($MODEL, a real session)"
