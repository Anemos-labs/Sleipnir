#!/bin/sh
# Drift check: Sleipnir depends on the standard library plus golang.org/x/{net,sys,term}, and on nothing else.
#
#   scripts/check-deps.sh [--allowlist FILE] [--allow-replace] [DIR]
#
# DIR is the module to check (default: the repository this script is in). FILE lists the allowed modules, one per line
# as "direct PATH" or "graph PATH" (default: scripts/deps-allowlist.txt); text after # is a comment.
#   direct   may be required by go.mod and may supply packages to the build
#   graph    may appear in the module graph (go list -m all) but no package of it may be built
# Exit status 1, with one line per finding, when
#   - go.mod requires a module that is not listed, or requires without "// indirect" one that is not "direct";
#   - go list -m all, or go.sum, names a module that is not listed;
#   - a package of a module that is not "direct" is compiled into the build or its tests (go list -deps -test ./...);
#   - go.mod has a replace directive (it could swap a listed module for other code); --allow-replace lifts this, for tests.
# Exit status 2 when the check itself cannot run (no go command, a malformed list, go list failing).
# To allow a module on purpose, add it to the list in the same change and say why in the change.
set -eu
LC_ALL=C
export LC_ALL

here=$(cd "$(dirname "$0")" && pwd)
allow=$here/deps-allowlist.txt
dir=$here/..
allow_replace=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --allowlist) shift; [ "$#" -gt 0 ] || { echo "check-deps: --allowlist needs a file" >&2; exit 2; }; allow=$1 ;;
    --allow-replace) allow_replace=1 ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    -*) echo "check-deps: unknown argument: $1 (try --help)" >&2; exit 2 ;;
    *) dir=$1 ;;
  esac
  shift
done

die() { echo "check-deps: $*" >&2; exit 2; }
command -v go >/dev/null 2>&1 || die "the go command is needed"
[ -f "$allow" ] || die "$allow does not exist"
[ -f "$dir/go.mod" ] || die "$dir/go.mod does not exist"
allow=$(cd "$(dirname "$allow")" && pwd)/$(basename "$allow")
cd "$dir"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

# The allow-list, without comments; every line must be "direct PATH" or "graph PATH".
sed 's/#.*$//' "$allow" | awk 'NF > 0' > "$tmp/allow"
if awk '!(NF == 2 && ($1 == "direct" || $1 == "graph")) { bad = 1 } END { exit !bad }' "$tmp/allow"; then
  die "$allow: every line must be 'direct MODULE' or 'graph MODULE'"
fi
class_of() { awk -v m="$1" '$2 == m { print $1; exit }' "$tmp/allow"; }

findings="$tmp/findings"
: > "$findings"
flag() { printf '%s\n' "$*" >> "$findings"; }
hint="if it is deliberate, add it to scripts/deps-allowlist.txt in the same change and say why (AGENTS.md: the dependency set is tiny on purpose)"

# 1. go.mod: what it requires, and whether directly.
awk '
  /^[ \t]*\/\// { next }
  /^require[ \t]*\(/ { inreq = 1; next }
  inreq && /^[ \t]*\)/ { inreq = 0; next }
  inreq || /^require[ \t]+[^(]/ {
    line = $0
    sub(/^require[ \t]+/, "", line)
    kind = (line ~ /\/\/ *indirect/) ? "indirect" : "direct"
    sub(/\/\/.*$/, "", line)
    n = split(line, f, /[ \t]+/)
    i = (f[1] == "") ? 2 : 1
    if (f[i] != "") print f[i], kind
  }' go.mod > "$tmp/required"
while read -r mod kind; do
  c=$(class_of "$mod")
  if [ -z "$c" ]; then
    flag "go.mod requires $mod, which is not in the allow-list ($hint)"
  elif [ "$kind" = direct ] && [ "$c" != direct ]; then
    flag "go.mod requires $mod directly, but the allow-list only lets it into the module graph ($hint)"
  fi
done < "$tmp/required"

# 2. go.sum: every module it holds.
if [ -f go.sum ]; then
  awk '{ print $1 }' go.sum | sort -u > "$tmp/sum"
  while read -r mod; do
    [ -n "$(class_of "$mod")" ] || flag "go.sum holds $mod, which is not in the allow-list ($hint)"
  done < "$tmp/sum"
fi

# 3. The module graph, and replace directives.
go list -m -f '{{if not .Main}}{{.Path}}{{end}}' all > "$tmp/graph" 2> "$tmp/err" || die "go list -m all failed: $(cat "$tmp/err")"
while read -r mod; do
  [ -n "$mod" ] || continue
  [ -n "$(class_of "$mod")" ] || flag "the module graph (go list -m all) holds $mod, which is not in the allow-list ($hint)"
done < "$tmp/graph"
if [ "$allow_replace" = 0 ]; then
  go list -m -f '{{if .Replace}}{{.Path}} => {{.Replace.Path}}{{end}}' all > "$tmp/replaced" 2> "$tmp/err" || die "go list -m all failed: $(cat "$tmp/err")"
  while read -r line; do
    [ -n "$line" ] || continue
    flag "go.mod replaces a module ($line): a replace directive can swap an allowed module for other code"
  done < "$tmp/replaced"
fi

# 4. What is really compiled, tests included.
go list -deps -test -f '{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}' ./... > "$tmp/built.raw" 2> "$tmp/err" ||
  die "go list -deps failed: $(cat "$tmp/err")"
sort -u "$tmp/built.raw" | awk 'NF > 0' > "$tmp/built"
while read -r mod; do
  [ -n "$mod" ] || continue
  c=$(class_of "$mod")
  if [ "$c" != direct ]; then
    flag "packages of $mod are compiled into the build or its tests, but the allow-list does not make it a direct dependency ($hint)"
  fi
done < "$tmp/built"

if [ -s "$findings" ]; then
  echo "check-deps: the dependency set has drifted:" >&2
  sed 's/^/  /' "$findings" >&2
  exit 1
fi
echo "check-deps: ok ($(wc -l < "$tmp/graph" | tr -d ' ') modules in the graph, $(wc -l < "$tmp/built" | tr -d ' ') built, all on the allow-list)"
