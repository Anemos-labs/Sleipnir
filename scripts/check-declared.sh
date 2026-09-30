#!/bin/sh
# Drift check: a change to the bytes of a stable prompt layer is a declared, priced event (AGENTS.md, docs/CACHE-DESIGN.md).
#
#   scripts/check-declared.sh BASE
#
# BASE is the commit to compare with: in CI the pull request's base commit, or github.event.before for a push. The check
# compares BASE (more exactly, the merge base of BASE and HEAD) with the working tree. If that diff touches a golden file
# of the prompt engine (internal/kv, internal/agent or internal/core, under testdata/golden/), CHANGELOG.md must be
# in the same diff with at least one added line: the entry that says what changed and what it costs
# (`sleipnir sim`; docs/BUILDING.md, "Changing prompt bytes").
#
# An empty BASE, or one of all zeros (a first push, a manual run), is not an error: there is nothing to compare, the
# check says so and passes. Exit status: 0 passes, 1 a golden file changed without a CHANGELOG entry, 2 the check
# cannot run (no BASE argument; BASE is not a commit of this clone: use fetch-depth: 0).
set -eu
LC_ALL=C
export LC_ALL

case "${1:-}" in
  -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
esac
[ "$#" -eq 1 ] || { echo "usage: scripts/check-declared.sh BASE (an empty BASE skips the check; try --help)" >&2; exit 2; }
base=$1

cd "$(dirname "$0")/.."

if [ -z "$(printf '%s' "$base" | tr -d 0)" ]; then
  echo "check-declared: skipped: no base commit to compare with"
  exit 0
fi
git rev-parse --verify --quiet "$base^{commit}" >/dev/null 2>&1 ||
  { echo "check-declared: $base is not a commit of this clone (a shallow checkout? use fetch-depth: 0)" >&2; exit 2; }
from=$(git merge-base "$base" HEAD 2>/dev/null || true)
[ -n "$from" ] || from=$base
short=$(printf '%s' "$from" | cut -c1-8)

# Tracked files that differ from the merge base (committed or not), and new files that are not ignored.
changed=$( { git -c core.quotepath=off diff --name-only --no-renames "$from" --; git ls-files --others --exclude-standard; } | sort -u)

golden=$(printf '%s\n' "$changed" | grep -E '^internal/(kv|agent|core)/(.*/)?testdata/golden/' || true)
if [ -z "$golden" ]; then
  echo "check-declared: ok: no prompt golden file changed since $short"
  exit 0
fi

added=$(git diff --numstat "$from" -- CHANGELOG.md | awk '{ n += $1 } END { print n + 0 }')
if [ "$added" -gt 0 ]; then
  echo "check-declared: ok: $(printf '%s\n' "$golden" | wc -l | tr -d ' ') golden file(s) changed since $short and CHANGELOG.md has $added added line(s)"
  exit 0
fi

{
  echo "check-declared: prompt bytes changed, but CHANGELOG.md has no new entry."
  echo "  golden files changed since $short:"
  printf '%s\n' "$golden" | sed 's/^/    /'
  echo "  A change to the bytes of a stable prompt layer is a declared, priced event (AGENTS.md, docs/CACHE-DESIGN.md):"
  echo "  add an entry to CHANGELOG.md that says what changed and what it costs (priced with \`sleipnir sim\`;"
  echo "  docs/BUILDING.md, \"Changing prompt bytes\")."
} >&2
exit 1
