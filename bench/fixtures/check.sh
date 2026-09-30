#!/bin/sh
# Check one benchmark fixture: its starting state fails the hidden verifier, its reference solution passes it, and the
# verdict is stable. No network, no dependencies beyond the language's own toolchain.
#
#   bench/fixtures/check.sh <id> [repeats]      (default 3 repeats of the passing run)
#
# A fixture is a directory <id>/ holding
#   task.json    id, kind (fix|feature|refactor|greenfield), lang, difficulty, prompt, verify (a shell command run in the
#                repository root), timeout_s, protected (globs the agent must not change), team, tags, budget
#   start/       the repository the agent starts in (visible tests included)
#   hidden/      files written over start/ only when the result is verified (the hidden tests)
#   solution/    a reference solution: the files that differ from start/ (whole files)
set -eu
cd "$(dirname "$0")"
id=${1:?usage: check.sh <fixture id> [repeats]}
reps=${2:-3}
dir=$id
[ -f "$dir/task.json" ] || { echo "no $dir/task.json" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is needed" >&2; exit 2; }
verify=$(jq -r .verify "$dir/task.json")
tmo=$(jq -r '.timeout_s // 120' "$dir/task.json")
[ -n "$verify" ] && [ "$verify" != null ] || { echo "$id: task.json has no verify command" >&2; exit 2; }
for f in id kind lang difficulty prompt; do
  [ "$(jq -r ".$f // empty" "$dir/task.json")" != "" ] || { echo "$id: task.json lacks \"$f\"" >&2; exit 2; }
done
[ -d "$dir/start" ] && [ -d "$dir/hidden" ] && [ -d "$dir/solution" ] || { echo "$id: start/, hidden/ and solution/ are all required" >&2; exit 2; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export HOME="$work/home" TMPDIR="$work/tmp" GOFLAGS=-mod=mod GOTOOLCHAIN=local CARGO_NET_OFFLINE=true
mkdir -p "$HOME" "$TMPDIR"
run() { # run the verifier in $1, print its exit status
  ( cd "$1" && set +e && timeout "$tmo" sh -c "$verify" >"$work/out.log" 2>&1; echo $? )
}
copy() { mkdir -p "$2" && cp -R "$1"/. "$2"/; }

# 1. the start, with the hidden files over it, must fail
copy "$dir/start" "$work/a"; copy "$dir/hidden" "$work/a"
st=$(run "$work/a")
if [ "$st" = 0 ]; then echo "FAIL $id: the starting state already passes the hidden verifier (nothing to do)"; tail -20 "$work/out.log"; exit 1; fi
echo "ok   $id: start fails (exit $st)"

# 2. the start alone (visible tests only) should fail too, or at least the task is not a trick: report it, do not fail
copy "$dir/start" "$work/v"
sv=$(run "$work/v")
echo "info $id: visible tests alone on the start: exit $sv"

# 3. the solution, with the hidden files, must pass, every time
i=0
while [ "$i" -lt "$reps" ]; do
  i=$((i+1))
  rm -rf "$work/b"; copy "$dir/start" "$work/b"; copy "$dir/solution" "$work/b"; copy "$dir/hidden" "$work/b"
  st=$(run "$work/b")
  if [ "$st" != 0 ]; then echo "FAIL $id: the reference solution fails the verifier (run $i, exit $st)"; tail -40 "$work/out.log"; exit 1; fi
done
echo "ok   $id: the solution passes $reps of $reps runs"

# 4. the solution must not touch protected paths
prot=$(jq -r '(.protected // [])[]' "$dir/task.json")
if [ -n "$prot" ]; then
  (cd "$dir/solution" && find . -type f | sed 's#^\./##') | while read -r f; do
    for g in $prot; do
      case "$f" in $g|*/$g) echo "FAIL $id: the solution changes the protected path $f"; exit 1 ;; esac
    done
  done || exit 1
fi
echo "ok   $id"
