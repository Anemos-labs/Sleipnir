#!/bin/sh
# Look at a screen: run a command in a real terminal, press keys, and write what the terminal showed as a PNG, in colour, to open and read.
#
#   scripts/look.sh OUT.png [--cols N] [--rows N] [--wait SECONDS] [--home DIR] [--cwd DIR] [--key NAME | --type TEXT | --pause SECONDS]... -- COMMAND [ARGS...]
#
#   scripts/look.sh /tmp/menu.png --key Down --key Down -- sleipnir login
#   scripts/look.sh /tmp/stats.png --home ~/.sleipnir-look --type 'hello' --key Enter --pause 20 --key C-t -- sleipnir --swarm 0
#
# --key is a tmux key name (Enter, Down, Escape, C-t, ...) and --type literal text, typed a character at a time; they are pressed in the order
# given, with --wait seconds (default 3) before the first and after the last, a short pause between, and --pause SECONDS where a screen
# takes longer to come (a program that starts another one). The picture is the last thing the
# terminal showed. `sleipnir` in the command is bin/sleipnir, built here; HOME is a fresh empty one unless --home says another (a key for a
# provider comes from the environment, which the command inherits). Needs tmux, script (util-linux), go and node with Playwright's Chromium.
# It is what AGENTS.md means by looking at what you make: text taken from a terminal has no colour, and a highlight that is white on white
# passes every test of text.
set -eu
cd "$(dirname "$0")/.."
root=$(pwd)

out=${1:?usage: scripts/look.sh OUT.png [options] -- COMMAND}
shift
cols=110 rows=34 wait=3 home= cwd=
tmp=$(mktemp -d)
# stop ends the run: the tmux session does not take script (and the program on its terminal) with it, so they are ended by their own pid
# (found by script's first argument and the log it writes, not by a pattern over every command line). The program is killed outright, and
# first: told to end, it says it was cancelled and clears what it drew, which script would log, and the picture would be of that.
stop() {
  for p in $(ps -eo pid,args | awk -v t="$tmp/o" '$2 == "script" && index($0, t) { print $1 }'); do
    pkill -KILL -P "$p" 2>/dev/null || true
    kill -KILL "$p" 2>/dev/null || true
  done
  tmux kill-session -t "look-$$" 2>/dev/null || true
}
trap 'stop; [ -n "${LOOK_KEEP:-}" ] || rm -rf "$tmp"' EXIT
: > "$tmp/actions"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --cols) shift; cols=${1:?} ;;
    --rows) shift; rows=${1:?} ;;
    --wait) shift; wait=${1:?} ;;
    --home) shift; home=${1:?} ;;
    --cwd) shift; cwd=${1:?} ;;
    --key) shift; printf 'key\t%s\n' "${1:?}" >> "$tmp/actions" ;;
    --type) shift; printf 'type\t%s\n' "${1:?}" >> "$tmp/actions" ;;
    --pause) shift; printf 'pause\t%s\n' "${1:?}" >> "$tmp/actions" ;;
    --) shift; break ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) echo "look: unknown option $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done
[ "$#" -gt 0 ] || { echo "look: a command is needed after --" >&2; exit 2; }
for t in tmux script go node; do command -v "$t" >/dev/null || { echo "look: $t is needed" >&2; exit 2; }; done

mkdir -p "$root/bin"
go build -o "$root/bin/sleipnir" ./cmd/sleipnir
[ -n "$home" ] || home=$tmp/home
[ -n "$cwd" ] || cwd=$tmp/work
mkdir -p "$home" "$cwd"

s=look-$$
# the command is written to a file with every word quoted, so that a goal with spaces and brackets in it reaches the program as one word
{ printf 'exec'; for a in "$@"; do printf " '%s'" "$(printf '%s' "$a" | sed "s/'/'\\\\''/g")"; done; printf '\n'; } > "$tmp/run.sh"
cmd="sh $tmp/run.sh"
tmux new-session -d -s "$s" -x "$cols" -y "$rows" "cd '$cwd' && env HOME='$home' PATH='$root/bin':\"\$PATH\" LC_ALL=C.UTF-8 TERM=xterm-256color COLORTERM=truecolor script -q --flush --log-out '$tmp/o' --log-timing '$tmp/t' --logging-format advanced -c '$cmd'; touch '$tmp/done'"
sleep "$wait"
while IFS="$(printf '\t')" read -r kind arg; do
  case "$kind" in
    key) tmux send-keys -t "$s" "$arg" ;;
    pause) sleep "$arg" ;;
    type) printf '%s' "$arg" | awk 'BEGIN{ORS=""} {for(i=1;i<=length($0);i++) print substr($0,i,1) "\n"}' | while IFS= read -r ch; do
        if [ -z "$ch" ]; then tmux send-keys -t "$s" Space; else tmux send-keys -t "$s" -l -- "$ch"; fi
        sleep 0.04
      done ;;
  esac
  sleep 0.8
done < "$tmp/actions"
sleep "$wait"
stop
sleep 1
# script may still be writing the end of its log for a moment (the timing file can be ahead of the log): try again
for _ in 1 2 3 4 5 6 7 8 9 10; do
  "$root/bin/sleipnir" term-svg --log "$tmp/o" --timing "$tmp/t" --out "$tmp/all.svg" --cols "$cols" --rows "$rows" --max-gap 1h --still "$tmp/still.svg" --hold 1s >/dev/null 2>"$tmp/err" && break
  grep -q 'does not match the log' "$tmp/err" || { cat "$tmp/err" >&2; exit 1; }
  sleep 1
done
[ -s "$tmp/still.svg" ] || { cat "$tmp/err" >&2; exit 1; }
node "$root/scripts/svg2png.mjs" "$tmp/still.svg" "$out" --scale 1
