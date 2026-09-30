#!/bin/sh
# Tests for scripts/check-declared.sh, on throw-away repositories that hold a copy of the script (it works on the repository
# it lives in). Run: sh scripts/check-declared_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
CHECK=$here/check-declared.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
GIT_CONFIG_GLOBAL=/dev/null
GIT_CONFIG_SYSTEM=/dev/null
GIT_CONFIG_NOSYSTEM=1
HOME=$tmp
export GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_NOSYSTEM HOME

passed=0
failed=0
eq() { # eq DESCRIPTION WANT GOT
  if [ "$2" = "$3" ]; then passed=$((passed + 1)); else
    failed=$((failed + 1)); printf 'FAIL: %s\n  want: %s\n  got:  %s\n' "$1" "$2" "$3" >&2
  fi
}
has() { # has DESCRIPTION NEEDLE HAYSTACK
  case $3 in *"$2"*) passed=$((passed + 1)) ;; *)
    failed=$((failed + 1)); printf 'FAIL: %s\n  want it to contain: %s\n  got: %s\n' "$1" "$2" "$3" >&2 ;;
  esac
}
hasnt() { # hasnt DESCRIPTION NEEDLE HAYSTACK
  case $3 in *"$2"*)
    failed=$((failed + 1)); printf 'FAIL: %s\n  want it not to contain: %s\n  got: %s\n' "$1" "$2" "$3" >&2 ;; *) passed=$((passed + 1)) ;;
  esac
}

# mk: a repository with the script, a CHANGELOG and one golden file in each of the three packages; prints its path.
mk() {
  d=$(mktemp -d "$tmp/repo.XXXXXX")
  git init -q "$d"
  git -C "$d" symbolic-ref HEAD refs/heads/main
  git -C "$d" config user.name tester
  git -C "$d" config user.email tester@example.invalid
  git -C "$d" config commit.gpgsign false
  mkdir -p "$d/scripts" "$d/internal/kv/testdata/golden/render" "$d/internal/agent/testdata/golden" "$d/internal/core/testdata/golden"
  cp "$CHECK" "$d/scripts/check-declared.sh"
  printf '# Changelog\n\n## [Unreleased]\n\n- first entry\n' > "$d/CHANGELOG.md"
  printf 'kv\n' > "$d/internal/kv/testdata/golden/render/layers.txt"
  printf 'agent\n' > "$d/internal/agent/testdata/golden/constitution_solo.txt"
  printf 'core\n' > "$d/internal/core/testdata/golden/canonical_raw.txt"
  git -C "$d" add -A
  git -C "$d" commit -q -m "start"
  printf '%s\n' "$d"
}
# edit REPO FILE [TEXT]: append a line and commit
edit() {
  mkdir -p "$1/$(dirname "$2")"
  printf '%s\n' "${3:-changed}" >> "$1/$2"
  git -C "$1" add -A
  git -C "$1" commit -q -m "edit $2"
}
# check REPO BASE sets out and rc
check() {
  rc=0
  out=$(cd "$1" && sh scripts/check-declared.sh "$2" 2>&1) || rc=$?
}

# --- golden files of the three packages need a CHANGELOG entry
for g in internal/kv/testdata/golden/render/layers.txt internal/agent/testdata/golden/constitution_solo.txt internal/core/testdata/golden/canonical_raw.txt; do
  d=$(mk)
  base=$(git -C "$d" rev-parse HEAD)
  edit "$d" "$g"
  check "$d" "$base"
  eq "$g without CHANGELOG: exit status" 1 "$rc"
  has "$g without CHANGELOG: names the file" "    $g" "$out"
  has "$g without CHANGELOG: says what to do" "add an entry to CHANGELOG.md" "$out"
  edit "$d" CHANGELOG.md "- the prompt got a line; priced with sleipnir sim"
  check "$d" "$base"
  eq "$g with CHANGELOG: exit status" 0 "$rc"
  has "$g with CHANGELOG: message" "CHANGELOG.md has 1 added line" "$out"
done

# --- a CHANGELOG that only lost lines is not a declaration
d=$(mk)
base=$(git -C "$d" rev-parse HEAD)
edit "$d" internal/kv/testdata/golden/render/layers.txt
printf '# Changelog\n' > "$d/CHANGELOG.md"
git -C "$d" commit -qam "shrink the changelog"
check "$d" "$base"
eq "CHANGELOG with deletions only: exit status" 1 "$rc"

# --- what is not a prompt golden file
d=$(mk)
mkdir -p "$d/internal/provider/anthropic/testdata/golden" "$d/internal/rl/export/testdata/golden" "$d/internal/kv/testdata/fuzz/FuzzX" "$d/internal/tui/state/testdata/golden"
base=$(git -C "$d" rev-parse HEAD)
for f in internal/provider/anthropic/testdata/golden/warm.json internal/rl/export/testdata/golden/x.jsonl internal/kv/testdata/fuzz/FuzzX/seed \
  internal/tui/state/testdata/golden/end.json internal/kv/golden_test.go internal/kv/testdata/notgolden/x.txt README.md; do
  edit "$d" "$f"
done
check "$d" "$base"
eq "other goldens and files do not need a CHANGELOG entry: exit status" 0 "$rc"
has "other goldens: message" "no prompt golden file changed" "$out"
d=$(mk)
base=$(git -C "$d" rev-parse HEAD)
edit "$d" internal/kv/sim/testdata/golden/sim.txt
check "$d" "$base"
eq "a golden file in a subpackage of internal/kv counts" 1 "$rc"
edit "$d" CHANGELOG.md "- priced"
check "$d" "$base"
eq "a golden file in a subpackage of internal/kv, declared" 0 "$rc"

# --- a new golden file, a deleted one, a renamed one
d=$(mk)
base=$(git -C "$d" rev-parse HEAD)
edit "$d" internal/core/testdata/golden/new_shape.txt new
check "$d" "$base"
eq "a new golden file counts" 1 "$rc"
d=$(mk)
base=$(git -C "$d" rev-parse HEAD)
git -C "$d" rm -q internal/core/testdata/golden/canonical_raw.txt
git -C "$d" commit -qm "delete a golden file"
check "$d" "$base"
eq "a deleted golden file counts" 1 "$rc"
d=$(mk)
base=$(git -C "$d" rev-parse HEAD)
git -C "$d" mv internal/core/testdata/golden/canonical_raw.txt internal/core/testdata/golden/canonical_raw2.txt
git -C "$d" commit -qm "rename a golden file"
check "$d" "$base"
eq "a renamed golden file counts" 1 "$rc"

# --- the working tree counts too (what a developer has before committing)
d=$(mk)
base=$(git -C "$d" rev-parse HEAD)
printf 'more\n' >> "$d/internal/kv/testdata/golden/render/layers.txt"
check "$d" "$base"
eq "an uncommitted golden change: exit status" 1 "$rc"
printf -- '- priced\n' >> "$d/CHANGELOG.md"
check "$d" "$base"
eq "an uncommitted golden change with an uncommitted CHANGELOG entry" 0 "$rc"
d=$(mk)
base=$(git -C "$d" rev-parse HEAD)
printf 'new\n' > "$d/internal/agent/testdata/golden/constitution_new.txt"
check "$d" "$base"
eq "an untracked new golden file: exit status" 1 "$rc"

# --- BASE is compared through the merge base: what main gained meanwhile is not this change's
d=$(mk)
fork=$(git -C "$d" rev-parse HEAD)
git -C "$d" checkout -q -b feature
edit "$d" docs/notes.md
git -C "$d" checkout -q main
edit "$d" internal/kv/testdata/golden/render/layers.txt
edit "$d" CHANGELOG.md "- main's entry"
git -C "$d" checkout -q feature
check "$d" main
eq "changes that landed on the base branch are not counted: exit status" 0 "$rc"
has "changes that landed on the base branch: compared with the fork point" "since $(printf '%s' "$fork" | cut -c1-8)" "$out"
git -C "$d" checkout -q -b feature2 "$fork"
edit "$d" internal/core/testdata/golden/canonical_raw.txt
check "$d" main
eq "the branch's own golden change is counted: exit status" 1 "$rc"

# --- nothing to compare
d=$(mk)
for b in "" 0000000000000000000000000000000000000000 0; do
  check "$d" "$b"
  eq "BASE '$b' is skipped: exit status" 0 "$rc"
  has "BASE '$b' is skipped: message" "skipped" "$out"
done

# --- the check itself cannot run
check "$d" 1234567890123456789012345678901234567890
eq "BASE that is not a commit: exit status" 2 "$rc"
has "BASE that is not a commit: message" "not a commit of this clone" "$out"
rc=0
out=$(cd "$d" && sh scripts/check-declared.sh 2>&1) || rc=$?
eq "no BASE argument: exit status" 2 "$rc"
has "no BASE argument: message" "usage:" "$out"
rc=0
out=$(cd "$d" && sh scripts/check-declared.sh a b 2>&1) || rc=$?
eq "two arguments: exit status" 2 "$rc"
rc=0
out=$(cd "$d" && sh scripts/check-declared.sh --help 2>&1) || rc=$?
eq "--help: exit status" 0 "$rc"
has "--help: text" "scripts/check-declared.sh BASE" "$out"
hasnt "--help: does not run the check" "check-declared: skipped" "$out"

printf 'check-declared_test.sh: %d checks passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
