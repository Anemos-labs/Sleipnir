#!/bin/sh
# The recordings of docs/media, made from a recorded session and nothing else.
#
#   scripts/record-demo.sh               draw docs/media/*.svg (and the PNG stills, when Chromium is there) from
#                                        docs/media/showcase/events.jsonl, as docs/media/gallery.json lists them
#   scripts/record-demo.sh --new-session run the shop demo (about 20 seconds, no key, no network), keep its event log as
#                                        docs/media/showcase/events.jsonl (paths made anonymous), then draw as above
#   scripts/record-demo.sh --check       change nothing; exit 1 if the committed SVGs are not what the code draws from the
#                                        committed log (the same check runs as a Go test, internal/tui/app)
#   --no-png                             leave the stills alone (they need node and Playwright's Chromium)
#   --bin PATH                           use an existing binary (or set SLEIPNIR=PATH)
#
# The pictures are the program's own screens, drawn by `sleipnir replay --gallery` from the session's events: nothing is
# typed, edited or photographed, so a change to a screen or to a widget shows up here as a diff to read and commit.
# The session is a scripted team against a mock endpoint (`sleipnir demo --scenario shop`): the harness around the model is real,
# the model is a script, and the README says so.
set -eu
LC_ALL=C
export LC_ALL
cd "$(dirname "$0")/.."

media=docs/media
log=$media/showcase/events.jsonl
manifest=$media/gallery.json
mode=draw
png=yes
BIN=${SLEIPNIR:-}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --new-session) mode=new ;;
    --check) mode=check ;;
    --no-png) png=no ;;
    --bin) shift; BIN=${1:?--bin needs a path} ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) echo "record-demo: unknown argument: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
if [ -z "$BIN" ]; then
  BIN="$tmp/sleipnir"
  go build -o "$BIN" ./cmd/sleipnir
fi

if [ "$mode" = new ]; then
  command -v git >/dev/null 2>&1 || { echo "record-demo: the shop demo needs git" >&2; exit 2; }
  echo "record-demo: running the shop demo (about 20 seconds)" >&2
  "$BIN" demo --scenario shop --dir "$tmp/demo" >/dev/null
  mkdir -p "$media/showcase"
  # The log names the directories the demo used; the recording does not need them, and a committed file should not carry where a
  # machine keeps its temporary files.
  esc=$(printf '%s' "$tmp/demo" | sed 's/[][\.*^$|]/\\&/g')
  sed "s|$esc|/work/demo|g" "$tmp/demo/session/events.jsonl" > "$log"
  if grep -nE '/(tmp|var|Users|home|root)/' "$log" | grep -v '/work/demo/' | head -3 | grep .; then
    echo "record-demo: the log still names a path of this machine; not keeping it" >&2
    exit 1
  fi
  echo "record-demo: wrote $log ($(wc -c < "$log" | tr -d ' ') bytes)" >&2
fi

[ -f "$log" ] || { echo "record-demo: $log does not exist (run with --new-session to make it)" >&2; exit 2; }

if [ "$mode" = check ]; then
  out="$tmp/out"
  "$BIN" replay "$log" --gallery "$manifest" --out "$out" >/dev/null 2>&1 || { echo "record-demo: the gallery could not be drawn" >&2; "$BIN" replay "$log" --gallery "$manifest" --out "$out" >/dev/null; exit 1; }
  bad=0
  for f in "$out"/*.svg; do
    name=$(basename "$f")
    if ! cmp -s "$f" "$media/$name"; then
      echo "record-demo: $media/$name is not what the code draws from $log" >&2
      bad=1
    fi
  done
  for f in "$media"/*.svg; do
    name=$(basename "$f")
    case "$name" in logo*|favicon*) continue ;; esac
    [ -f "$out/$name" ] || { echo "record-demo: $media/$name is not in $manifest" >&2; bad=1; }
  done
  [ "$bad" -eq 0 ] && echo "record-demo: the recordings are what the code draws"
  [ "$bad" -eq 0 ] || echo "record-demo: run scripts/record-demo.sh and commit the result" >&2
  exit "$bad"
fi

stills=$("$BIN" replay "$log" --gallery "$manifest" --out "$media")
if [ "$png" = yes ] && [ -n "$stills" ]; then
  if command -v node >/dev/null 2>&1; then
    echo "$stills" | while read -r _ name at; do
      node scripts/svg2png.mjs "$media/$name.svg" "$media/$name.png" --scale 2 --at "$at"
    done
  else
    echo "record-demo: node is not installed: the stills were left as they were" >&2
  fi
fi
