#!/bin/sh
# Drift check: every `uses:` in the workflows is pinned to a full commit SHA, with its version in a comment.
#
#   scripts/check-pins.sh [DIR...]
#
# Reads every .yml and .yaml file under each DIR (default: .github in this repository, which holds the workflows and any
# local action). A tag or a branch can be moved to other code after it was reviewed; a commit cannot. A `uses:` passes
# when it is
#   ./path                       a local action or workflow of this repository
#   OWNER/REPO[/PATH]@<40 hex>   followed by a comment that starts with the version, "# v1.2.3": Dependabot keeps
#                                the SHA and that comment in step when it proposes an update
#   docker://IMAGE@sha256:<64 hex>
# A tool that a workflow fetches by `go run` or `go install` must not be "@latest" (or @main, @master) either.
# Exit status 1 with one line per finding; 2 when no workflow file is found. Paths must not contain spaces.
set -eu
LC_ALL=C
export LC_ALL

case "${1:-}" in
  -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
esac
if [ "$#" -eq 0 ]; then
  cd "$(dirname "$0")/.."
  set -- .github
fi

files=$(find "$@" -type f \( -name '*.yml' -o -name '*.yaml' \) 2>/dev/null | sort || true)
if [ -z "$files" ]; then
  echo "check-pins: no .yml or .yaml file under: $*" >&2
  exit 2
fi

# shellcheck disable=SC2086 # the file names have no spaces
findings=$(awk '
  function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
  function ishex(s, n) { return length(s) == n && s !~ /[^0-9a-f]/ }
  BEGIN { q = sprintf("%c", 39) }
  /^[ \t]*#/ { next }
  /(^|[ \t;&|])go[ \t]+(run|install)[ \t]+[^ \t]*@(latest|main|master|HEAD)([ \t]|$)/ {
    printf "%s:%d: a tool fetched with go run/go install must be pinned to a version, not @latest, @main or @master\n", FILENAME, FNR
  }
  match($0, /^[ \t]*(-[ \t]+)?uses:[ \t]*/) {
    rest = substr($0, RLENGTH + 1)
    comment = ""
    if (match(rest, /[ \t]+#/)) { comment = substr(rest, RSTART); rest = substr(rest, 1, RSTART - 1) }
    v = trim(rest)
    if (substr(v, 1, 1) == "\"" && substr(v, length(v), 1) == "\"" && length(v) > 1) v = substr(v, 2, length(v) - 2)
    else if (substr(v, 1, 1) == q && substr(v, length(v), 1) == q && length(v) > 1) v = substr(v, 2, length(v) - 2)
    where = sprintf("%s:%d: uses: %s", FILENAME, FNR, v)
    if (v ~ /^\.\.?\//) next
    if (v ~ /^docker:\/\//) {
      at = index(v, "@sha256:")
      if (at == 0 || !ishex(substr(v, at + 8), 64)) print where ": a docker image must be pinned by digest (docker://IMAGE@sha256:<64 hex>)"
      next
    }
    at = index(v, "@")
    if (at == 0) { print where ": no @ref: pin it to a full commit SHA"; next }
    name = substr(v, 1, at - 1)
    ref = substr(v, at + 1)
    if (name !~ /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+(\/.*)?$/) { print where ": not OWNER/REPO[/PATH]@SHA or a ./local path"; next }
    if (!ishex(ref, 40)) {
      print where ": @" ref " is a tag, a branch or a short SHA, which can move or collide; pin the full 40-hex commit and keep the version as a comment: " name "@<sha> # vX.Y.Z"
      next
    }
    if (comment !~ /^[ \t]+#[ \t]*v?[0-9]+(\.[0-9]+)*/) print where ": the SHA has no version comment after it (# vX.Y.Z); Dependabot and readers rely on it"
  }' $files)

if [ -n "$findings" ]; then
  echo "check-pins: actions that are not pinned as the repository requires:" >&2
  printf '%s\n' "$findings" | sed 's/^/  /' >&2
  exit 1
fi
echo "check-pins: ok ($(printf '%s\n' "$files" | wc -l | tr -d ' ') files)"
