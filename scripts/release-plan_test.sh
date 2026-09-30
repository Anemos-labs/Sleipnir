#!/bin/sh
# Tests for scripts/release-plan.sh. Every case builds a throw-away git repository in a temp directory (no network, no gh,
# no signing, no global git configuration) and asserts what the plan says. Run: sh scripts/release-plan_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
PLAN=$here/release-plan.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

# git must not read the developer's configuration (signing, hooks, templates) nor write outside the temp directory.
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

newrepo() { # prints the path of an empty repository on the branch main
  d=$(mktemp -d "$tmp/repo.XXXXXX")
  git init -q "$d"
  git -C "$d" symbolic-ref HEAD refs/heads/main
  git -C "$d" config user.name tester
  git -C "$d" config user.email tester@example.invalid
  git -C "$d" config commit.gpgsign false
  git -C "$d" config tag.gpgsign false
  printf '%s\n' "$d"
}
commit() { # commit REPO SUBJECT [FILE...]: change every file (create it if needed) and commit; BODY is an optional message body
  repo=$1 subject=$2
  shift 2
  for f in "$@"; do
    mkdir -p "$repo/$(dirname "$f")"
    printf 'change: %s\n' "$subject" >> "$repo/$f"
  done
  git -C "$repo" add -A
  if [ -n "${BODY:-}" ]; then
    git -C "$repo" commit -q --allow-empty -m "$subject" -m "$BODY"
  else
    git -C "$repo" commit -q --allow-empty -m "$subject"
  fi
}
seed() { # a repository whose first commit has a LICENSE and user-facing code
  d=$(newrepo)
  commit "$d" "chore: start" LICENSE cmd/sleipnir/main.go internal/core/core.go README.md docs/guide.md go.mod
  printf '%s\n' "$d"
}
released() { git -C "$1" tag "$2"; } # released REPO TAG: tag the current commit

# plan REPO [ARGS...] runs the script inside REPO with AUTO_RELEASE=$AUTO (default true; "unset" leaves it unset) and sets
# out (stdout), err (stderr) and rc (exit status).
plan() {
  repo=$1
  shift
  rc=0
  out=$(
    cd "$repo"
    unset AUTO_RELEASE DRY_RUN GITHUB_OUTPUT GITHUB_REPOSITORY GH_TOKEN GITHUB_TOKEN
    if [ "${AUTO-true}" != unset ]; then AUTO_RELEASE=${AUTO-true}; export AUTO_RELEASE; fi
    sh "$PLAN" "$@" 2>"$tmp/err"
  ) || rc=$?
  err=$(cat "$tmp/err")
}
val() { printf '%s\n' "$out" | sed -n "s/^$1=//p"; }
# verdict REPO DESCRIPTION WANT_TAG: the plan releases WANT_TAG (or, with "-", does not release)
verdict() {
  plan "$1"
  if [ "$3" = - ]; then
    eq "$2: release" false "$(val release)"
  else
    eq "$2: release" true "$(val release)"
    eq "$2: tag" "$3" "$(val tag)"
    eq "$2: version" "${3#v}" "$(val version)"
  fi
}

# --- the output format: five keys, in this order, exit status 0
d=$(seed)
plan "$d"
eq "exit status of a decision" 0 "$rc"
eq "keys and their order" "release tag version previous reason" "$(printf '%s\n' "$out" | cut -d= -f1 | tr '\n' ' ' | sed 's/ $//')"
eq "no text on stderr is needed for the result" 5 "$(printf '%s\n' "$out" | wc -l | tr -d ' ')"

# --- first release
d=$(seed)
plan "$d"
eq "first release" true "$(val release)"
eq "first release tag" v0.1.0 "$(val tag)"
eq "first release version" 0.1.0 "$(val version)"
eq "first release has no previous" "" "$(val previous)"
eq "first release reason" "first release" "$(val reason)"

# --- the AUTO_RELEASE gate
d=$(seed)
AUTO='unset' plan "$d"
eq "AUTO_RELEASE unset: release" false "$(val release)"
has "AUTO_RELEASE unset: the reason names the variable" AUTO_RELEASE "$(val reason)"
eq "AUTO_RELEASE unset: the would-be tag is still printed" v0.1.0 "$(val tag)"
for v in false yes TRUE 1 " true" ""; do
  AUTO=$v plan "$d"
  eq "AUTO_RELEASE='$v' is not exactly true: release" false "$(val release)"
done
AUTO=true plan "$d"
eq "AUTO_RELEASE=true: release" true "$(val release)"

# --- the LICENSE gate
d=$(newrepo)
commit "$d" "chore: start" cmd/sleipnir/main.go internal/core/core.go
plan "$d"
eq "no LICENSE: release" false "$(val release)"
eq "no LICENSE: reason" "no LICENSE file: choose one before the first release" "$(val reason)"
eq "no LICENSE: tag is still printed" v0.1.0 "$(val tag)"
commit "$d" "chore: licence in a subdirectory only" docs/LICENSE
plan "$d"
eq "LICENSE in a subdirectory does not count" false "$(val release)"
for name in LICENSE LICENSE.md LICENSE.txt; do
  d=$(newrepo)
  commit "$d" "chore: start" cmd/sleipnir/main.go "$name"
  plan "$d"
  eq "$name counts" true "$(val release)"
done
d=$(newrepo)
commit "$d" "chore: start" cmd/sleipnir/main.go LICENSE.rst license
plan "$d"
eq "only LICENSE, LICENSE.md and LICENSE.txt count" false "$(val release)"
# the commit's tree decides, not whatever lies in the working directory
d=$(newrepo)
commit "$d" "chore: start" cmd/sleipnir/main.go
: > "$d/LICENSE"
plan "$d"
eq "an untracked LICENSE does not count" false "$(val release)"

# --- patch releases
d=$(seed)
released "$d" v0.1.0
commit "$d" "fix: handle the empty case (#12)" internal/core/core.go
verdict "$d" "fix after v0.1.0" v0.1.1
eq "previous is reported" v0.1.0 "$(val previous)"
has "the reason says what happened" "patch bump since v0.1.0" "$(val reason)"

# --- before 1.0: breaking = minor, everything else = patch
d=$(seed)
released "$d" v0.3.4
commit "$d" "feat: a new command (#1)" cmd/sleipnir/main.go
verdict "$d" "0.x feat is a patch" v0.3.5
d=$(seed)
released "$d" v0.3.4
commit "$d" "feat(cli)!: rename --model (#2)" cmd/sleipnir/main.go
verdict "$d" "0.x breaking subject is a minor" v0.4.0
d=$(seed)
released "$d" v0.3.4
commit "$d" "fix!: change the log format (#3)" internal/core/core.go
verdict "$d" "0.x fix! is a minor" v0.4.0
d=$(seed)
released "$d" v0.3.4
BODY="Some text.

BREAKING CHANGE: the flag is gone" commit "$d" "refactor: drop the flag (#4)" internal/core/core.go
verdict "$d" "0.x BREAKING CHANGE footer is a minor" v0.4.0
d=$(seed)
released "$d" v0.3.4
BODY="BREAKING-CHANGE: hyphenated token" commit "$d" "refactor: x (#5)" internal/core/core.go
verdict "$d" "0.x BREAKING-CHANGE footer is a minor" v0.4.0
d=$(seed)
released "$d" v0.3.4
BODY="This is not a BREAKING CHANGE: it is a sentence." commit "$d" "fix: y (#6)" internal/core/core.go
verdict "$d" "a mid-line mention is not a footer" v0.3.5
d=$(seed)
released "$d" v0.3.4
commit "$d" "fix: one (#7)" internal/core/core.go
commit "$d" "feat!: two (#8)" cmd/sleipnir/main.go
commit "$d" "fix: three (#9)" internal/core/core.go
verdict "$d" "0.x: one breaking commit among several" v0.4.0

# --- from 1.0: breaking = major, feat = minor, everything else = patch
for t in fix perf refactor chore; do
  d=$(seed)
  released "$d" v1.2.3
  commit "$d" "$t: something (#1)" internal/core/core.go
  verdict "$d" "1.x $t" v1.2.4
done
d=$(seed)
released "$d" v1.2.3
commit "$d" "feat: something (#1)" internal/core/core.go
verdict "$d" "1.x feat" v1.3.0
d=$(seed)
released "$d" v1.9.9
commit "$d" "feat(x): something (#1)" internal/core/core.go
verdict "$d" "1.x feat carries into the tens" v1.10.0
d=$(seed)
released "$d" v1.2.3
commit "$d" "feat!: something (#1)" internal/core/core.go
verdict "$d" "1.x feat!" v2.0.0
d=$(seed)
released "$d" v1.2.3
BODY="BREAKING CHANGE: gone" commit "$d" "fix: something (#1)" internal/core/core.go
verdict "$d" "1.x footer" v2.0.0
d=$(seed)
released "$d" v1.2.3
commit "$d" "fix: a (#1)" internal/core/core.go
commit "$d" "feat: b (#2)" internal/core/core.go
commit "$d" "docs: c (#3)" README.md
verdict "$d" "1.x fix and feat together" v1.3.0
d=$(seed)
released "$d" v1.2.3
commit "$d" "Update the thing (#1)" internal/core/core.go
verdict "$d" "1.x subject that is not conventional" v1.2.4
d=$(seed)
released "$d" v1.2.3
commit "$d" "Feat: capital letter is still a type (#1)" internal/core/core.go
verdict "$d" "1.x type is case-insensitive" v1.3.0

# --- nothing user-facing: docs, tests and CI only
d=$(seed)
released "$d" v0.1.0
commit "$d" "docs: explain the thing (#1)" README.md docs/guide.md
commit "$d" "test: more cases (#2)" internal/core/core_test.go
commit "$d" "ci: pin actions (#3)" .github/workflows/ci.yml
commit "$d" "chore: a fixture (#4)" internal/core/testdata/golden/x.go internal/core/testdata/data.json
commit "$d" "docs: a note next to the code (#5)" internal/core/NOTES.md
commit "$d" "build: a script (#6)" scripts/check.sh Makefile
verdict "$d" "docs/tests/CI only" -
has "docs/tests/CI only: the reason" "docs/tests/CI only" "$(val reason)"
eq "docs/tests/CI only: no tag" "" "$(val tag)"
d=$(seed)
released "$d" v0.1.0
commit "$d" "Tidy up the docs (#1)" docs/guide.md
verdict "$d" "a non-conventional subject with no user-facing file" -

# --- a subject is enough
for t in fix perf revert feat; do
  d=$(seed)
  released "$d" v0.1.0
  commit "$d" "$t: change that touches only docs (#1)" docs/guide.md
  verdict "$d" "$t subject, docs only" v0.1.1
done
d=$(seed)
released "$d" v0.1.0
commit "$d" "fix(api)!: only docs changed but it says breaking (#1)" docs/guide.md
verdict "$d" "breaking subject, docs only" v0.2.0
d=$(seed)
released "$d" v0.1.0
commit "$d" "docs: only docs (#1)" README.md
BODY="BREAKING CHANGE: said in a footer" commit "$d" "docs: and a footer (#2)" README.md
verdict "$d" "breaking footer, docs only" v0.2.0

# --- what counts as a user-facing file
for f in go.mod go.sum .goreleaser.yaml cmd/sleipnir/main.go internal/core/core.go internal/inspect/web/app.js internal/tools/fs/fs.go; do
  d=$(seed)
  released "$d" v0.1.0
  commit "$d" "chore: touch $f (#1)" "$f"
  verdict "$d" "user-facing: $f" v0.1.1
done
for f in README.md CHANGELOG.md docs/CLI.md .github/CODEOWNERS scripts/check.sh Makefile internal/core/core_test.go \
  internal/core/testdata/golden/canonical.txt internal/core/testdata/x.go bench/suite.json cmd/sleipnir/e2e_test.go \
  internal/notes.md; do
  d=$(seed)
  released "$d" v0.1.0
  commit "$d" "chore: touch $f (#1)" "$f"
  verdict "$d" "not user-facing: $f" -
done
# a change that is undone is no change
d=$(seed)
released "$d" v0.1.0
commit "$d" "chore: edit (#1)" internal/core/core.go
git -C "$d" revert --no-edit HEAD >/dev/null 2>&1
verdict "$d" "a change and its revert leave no user-facing diff" -

# --- already released
d=$(seed)
released "$d" v0.1.0
verdict "$d" "HEAD is tagged" -
eq "HEAD is tagged: reason" "already released as v0.1.0" "$(val reason)"
eq "HEAD is tagged: previous" v0.1.0 "$(val previous)"
d=$(seed)
commit "$d" "fix: before the tag (#1)" internal/core/core.go
early=$(git -C "$d" rev-parse HEAD)
commit "$d" "fix: after (#2)" internal/core/core.go
released "$d" v0.1.0
plan "$d" --ref "$early"
eq "an ancestor of a tag is released too" false "$(val release)"
eq "an ancestor of a tag: reason" "already released as v0.1.0" "$(val reason)"
git -C "$d" tag v0.2.0
plan "$d" --ref "$early"
eq "the first release that contains the commit is named" "already released as v0.1.0" "$(val reason)"

# --- which tag is the previous one
d=$(seed)
released "$d" v0.9.0
commit "$d" "fix: a (#1)" internal/core/core.go
released "$d" v0.10.0
commit "$d" "fix: b (#2)" internal/core/core.go
verdict "$d" "v0.10.0 is above v0.9.0" v0.10.1
eq "previous is the highest tag by version" v0.10.0 "$(val previous)"
d=$(seed)
released "$d" v0.1.0
released "$d" v2-beta
released "$d" v1.0.0-rc1
released "$d" v01.2.3
released "$d" release-1
commit "$d" "fix: a (#1)" internal/core/core.go
verdict "$d" "tags that are not plain vX.Y.Z are ignored" v0.1.1
eq "previous ignores them" v0.1.0 "$(val previous)"
d=$(seed)
released "$d" v0.5.0
git -C "$d" checkout -q -b side
commit "$d" "fix: only on the side branch (#1)" internal/core/core.go
released "$d" v0.6.0
git -C "$d" checkout -q main
commit "$d" "fix: on main (#2)" internal/core/core.go
verdict "$d" "a tag on another branch is not a previous release" v0.5.1
eq "previous is reachable from HEAD" v0.5.0 "$(val previous)"

# --- a tag that already exists elsewhere
d=$(seed)
released "$d" v0.1.0
git -C "$d" checkout -q -b side
commit "$d" "fix: a (#1)" internal/core/core.go
released "$d" v0.1.1
git -C "$d" checkout -q main
commit "$d" "fix: b (#2)" internal/core/core.go
plan "$d"
eq "the next tag exists on another branch: release" false "$(val release)"
has "the next tag exists on another branch: reason" "already exists" "$(val reason)"

# --- dry run and $GITHUB_OUTPUT
d=$(seed)
: > "$tmp/gho"
(cd "$d" && AUTO_RELEASE=true GITHUB_OUTPUT="$tmp/gho" sh "$PLAN" >/dev/null 2>&1)
eq "GITHUB_OUTPUT receives the five lines" "release=true
tag=v0.1.0
version=0.1.0
previous=
reason=first release" "$(cat "$tmp/gho")"
: > "$tmp/gho"
(cd "$d" && AUTO_RELEASE=true GITHUB_OUTPUT="$tmp/gho" sh "$PLAN" --dry-run >/dev/null 2>&1)
eq "--dry-run writes no GITHUB_OUTPUT" "" "$(cat "$tmp/gho")"
(cd "$d" && AUTO_RELEASE=true GITHUB_OUTPUT="$tmp/gho" DRY_RUN=1 sh "$PLAN" >/dev/null 2>&1)
eq "DRY_RUN=1 writes no GITHUB_OUTPUT" "" "$(cat "$tmp/gho")"
(cd "$d" && AUTO_RELEASE=true GITHUB_OUTPUT="$tmp/gho" DRY_RUN=0 sh "$PLAN" >/dev/null 2>&1)
has "DRY_RUN=0 is a normal run" "release=true" "$(cat "$tmp/gho")"
plan "$d" --dry-run
eq "--dry-run still prints the plan" true "$(val release)"

# --- gh: only when every other answer is yes, never in a dry run
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
[ "$1 $2" = "release view" ] || exit 9
[ "${GH_RELEASE_EXISTS:-0}" = 1 ]
EOF
chmod +x "$tmp/bin/gh"
d=$(seed)
released "$d" v0.1.0
commit "$d" "fix: a (#1)" internal/core/core.go
ghplan() { # ghplan EXISTS [ARGS...]: run with the stub gh, a token and a repository name
  e=$1
  shift
  : > "$tmp/ghlog"
  rc=0
  out=$(cd "$d" && AUTO_RELEASE=true GH_LOG="$tmp/ghlog" GH_RELEASE_EXISTS=$e GITHUB_REPOSITORY=o/r GH_TOKEN=x PATH="$tmp/bin:$PATH" sh "$PLAN" "$@" 2>/dev/null) || rc=$?
}
ghplan 0
eq "gh: the release does not exist" true "$(val release)"
eq "gh: it was asked once, for the right tag and repository" "release view v0.1.1 --repo o/r" "$(cat "$tmp/ghlog")"
ghplan 1
eq "gh: the release exists" false "$(val release)"
has "gh: the release exists: reason" "already exists on GitHub" "$(val reason)"
ghplan 1 --dry-run
eq "gh: a dry run does not ask" "" "$(cat "$tmp/ghlog")"
eq "gh: a dry run decides without it" true "$(val release)"
: > "$tmp/ghlog"
out=$(cd "$d" && GH_LOG="$tmp/ghlog" GH_RELEASE_EXISTS=1 GITHUB_REPOSITORY=o/r GH_TOKEN=x PATH="$tmp/bin:$PATH" sh "$PLAN" 2>/dev/null) || rc=$?
eq "gh: not asked when AUTO_RELEASE is off" "" "$(cat "$tmp/ghlog")"
eq "gh: and AUTO_RELEASE is what says no" false "$(val release)"
: > "$tmp/ghlog"
out=$(cd "$d" && AUTO_RELEASE=true GH_LOG="$tmp/ghlog" GH_RELEASE_EXISTS=1 PATH="$tmp/bin:$PATH" sh "$PLAN" 2>/dev/null) || rc=$?
eq "gh: without a token or a repository name it is not asked" "" "$(cat "$tmp/ghlog")"
eq "gh: without a token or a repository name the plan stands" true "$(val release)"
# the plain path must also work where gh does not exist at all
out=$(cd "$d" && AUTO_RELEASE=true GITHUB_REPOSITORY=o/r GH_TOKEN=x PATH="/usr/bin:/bin" sh "$PLAN" 2>/dev/null) || rc=$?
eq "no gh on PATH" true "$(val release)"

# --- problems are exit status 2 with a message, not a decision
d=$(seed)
plan "$d" --bogus
eq "unknown argument: exit status" 2 "$rc"
has "unknown argument: message" "unknown argument" "$err"
plan "$d" --ref
eq "--ref without a value: exit status" 2 "$rc"
plan "$d" --ref no-such-revision
eq "unknown revision: exit status" 2 "$rc"
has "unknown revision: message" "not a commit" "$err"
plan "$tmp"
eq "not a repository: exit status" 2 "$rc"
has "not a repository: message" "not inside a git repository" "$err"
git clone -q --depth 1 "file://$d" "$tmp/shallow"
plan "$tmp/shallow"
eq "shallow clone: exit status" 2 "$rc"
has "shallow clone: message" "shallow" "$err"
plan "$d" --help
eq "--help: exit status" 0 "$rc"
has "--help: text" "release-plan.sh [--dry-run]" "$out"
has "--help: it prints the rules" "AUTO_RELEASE" "$out"
# untrusted text never reaches the reason
d=$(seed)
released "$d" v0.1.0
commit "$d" "fix: evil
reason=injected (#1)" internal/core/core.go
plan "$d"
eq "a subject with a newline cannot add a key" 5 "$(printf '%s\n' "$out" | wc -l | tr -d ' ')"

printf 'release-plan_test.sh: %d checks passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
