#!/bin/sh
# Regenerate the flag listings in docs/CLI.md from the binary's own --help output.
#
#   scripts/gen-cli-docs.sh              rewrite docs/CLI.md in place
#   scripts/gen-cli-docs.sh --check      change nothing; exit 1 if docs/CLI.md is out of date
#   scripts/gen-cli-docs.sh --bin PATH   use an existing binary instead of building one
#                                        (or set SLEIPNIR=PATH)
#
# docs/CLI.md keeps its prose. Only the block between a pair of markers is replaced:
#
#   <!-- flags: rl taskgen git -->
#   ...generated...
#   <!-- /flags -->
#
# The words after "flags:" are the command line given to the binary, followed by -h.
# The script also fails when a command that the binary lists (sleipnir --help, rl help,
# rl taskgen help, rl tasks help) has no marker in the document, so a new command cannot
# be forgotten. `swarm` (which is `run --swarm`), `version` and the command groups
# themselves (rl, rl taskgen, rl tasks) take no marker. A subcommand of a listed command
# (`sessions prune`) may have one when its help starts "usage: sleipnir sessions prune".
set -eu
LC_ALL=C
export LC_ALL
cd "$(dirname "$0")/.."

DOC=docs/CLI.md
mode=write
BIN=${SLEIPNIR:-}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check) mode=check ;;
    --bin) shift; BIN=${1:?--bin needs a path} ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) echo "gen-cli-docs: unknown argument: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done
[ -f "$DOC" ] || { echo "gen-cli-docs: $DOC does not exist" >&2; exit 2; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
mkdir -p "$tmp/help" "$tmp/home" "$tmp/state"

if [ -z "$BIN" ]; then
  BIN="$tmp/sleipnir"
  go build -o "$BIN" ./cmd/sleipnir
fi

# The help text must not depend on the caller's environment: some flag defaults echo it
# (sleipnir inspect prints $SLEIPNIR_INSPECT_TOKEN as the default of --token).
run() {
  env -u SLEIPNIR_INSPECT_TOKEN HOME="$tmp/home" SLEIPNIR_HOME="$tmp/state" "$BIN" "$@" 2>&1 || true
}

tab=$(printf '\t')

# help_of "rl taskgen git" prints that command's help: tabs expanded, trailing blanks
# removed, and without the "flag: help requested" error the rl commands add after -h.
help_of() {
  # shellcheck disable=SC2086 # the words are the command line
  run $1 -h | sed "s/$tab/    /g; s/[[:space:]]*\$//; /^sleipnir: flag: help requested\$/d"
}

# names_in prints the command names of a usage block: the lines "  name  description".
names_in() {
  awk '/^  [a-z][a-z0-9-]* +[^ ]/ { print $1 }'
}

# --- what the document asks for
markers=$(sed -n 's/^<!-- flags: \(.*\) -->$/\1/p' "$DOC")
[ -n "$markers" ] || { echo "gen-cli-docs: $DOC has no <!-- flags: ... --> markers" >&2; exit 2; }

echo "$markers" | while IFS= read -r m; do
  f=$(echo "$m" | tr ' ' '_')
  help_of "$m" > "$tmp/help/$f"
  if [ ! -s "$tmp/help/$f" ] || head -n 1 "$tmp/help/$f" | grep -q '^sleipnir: unknown command'; then
    echo "gen-cli-docs: '$m' is not a command of this binary" >&2
    : > "$tmp/bad"
  fi
done
[ ! -e "$tmp/bad" ] || exit 1

# --- what the binary offers
{
  run --help | awk '/^Every day:/ { on = 1 } /^A model is written/ { on = 0 } on' | names_in \
    | grep -v -x -e swarm -e version -e rl || true
  run rl help | names_in | grep -v -x -e taskgen -e tasks | sed 's/^/rl /' || true
  run rl taskgen help | names_in | sed 's/^/rl taskgen /'
  run rl tasks help | names_in | sed 's/^/rl tasks /'
} | sort -u > "$tmp/offered"
echo "$markers" | sort -u > "$tmp/asked"
comm -23 "$tmp/offered" "$tmp/asked" > "$tmp/missing"
comm -13 "$tmp/offered" "$tmp/asked" > "$tmp/extra"
# A subcommand of a listed command ("sessions prune") is not in any listing. Its marker is accepted when the command's own help
# says it is the help of that subcommand: a renamed or removed one would print its parent's help, which names the parent.
if [ -s "$tmp/extra" ]; then
  : > "$tmp/extra.kept"
  while IFS= read -r m; do
    f=$(echo "$m" | tr ' ' '_')
    first=${m%% *}
    if [ "$first" != "$m" ] && [ "$first" != rl ] && grep -qx -e "$first" "$tmp/offered" \
      && head -n 1 "$tmp/help/$f" | grep -q "^usage: sleipnir $m\b"; then
      continue
    fi
    echo "$m" >> "$tmp/extra.kept"
  done < "$tmp/extra"
  mv "$tmp/extra.kept" "$tmp/extra"
fi

# --- rewrite the document
awk -v dir="$tmp/help" '
  /^<!-- flags: .* -->$/ {
    name = $0
    sub(/^<!-- flags: /, "", name)
    sub(/ -->$/, "", name)
    gsub(/ /, "_", name)
    print
    print "```text"
    file = dir "/" name
    while ((getline line < file) > 0) print line
    close(file)
    print "```"
    skipping = 1
    next
  }
  skipping && /^<!-- \/flags -->$/ { skipping = 0 }
  !skipping { print }
' "$DOC" > "$tmp/new.md"

status=0
if cmp -s "$DOC" "$tmp/new.md"; then
  echo "$DOC is up to date"
elif [ "$mode" = check ]; then
  echo "$DOC is out of date; run scripts/gen-cli-docs.sh" >&2
  diff -u "$DOC" "$tmp/new.md" | head -60 >&2 || true
  status=1
else
  cp "$tmp/new.md" "$DOC"
  echo "updated $DOC"
fi

if [ -s "$tmp/missing" ]; then
  echo "gen-cli-docs: commands without a <!-- flags: ... --> marker in $DOC:" >&2
  sed 's/^/  /' "$tmp/missing" >&2
  status=1
fi
if [ -s "$tmp/extra" ]; then
  echo "gen-cli-docs: markers for commands the binary does not list (renamed or removed?):" >&2
  sed 's/^/  /' "$tmp/extra" >&2
  status=1
fi
exit "$status"
