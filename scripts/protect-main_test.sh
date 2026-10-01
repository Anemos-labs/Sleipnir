#!/bin/sh
# Tests for scripts/protect-main.sh with a stub gh on PATH: the dry run must print every call and run nothing, and a real
# run must send what the documentation says. No network, no credentials. Run: sh scripts/protect-main_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
SCRIPT=$here/protect-main.sh
root=$(cd "$here/.." && pwd)
command -v jq >/dev/null 2>&1 || { echo "protect-main_test.sh: skipped, jq is needed"; exit 0; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
GIT_CONFIG_GLOBAL=/dev/null
GIT_CONFIG_SYSTEM=/dev/null
GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_NOSYSTEM

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

# The stub gh: one line per call in $GH_LOG, the body of an `--input -` call on the next line. Answers: the ruleset list
# ($GH_RULESETS), the default branch ($GH_DEFAULT_BRANCH), everything else {}. It fails a call that contains $GH_FAIL.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
{ printf 'gh'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; } >> "$GH_LOG"
case " $* " in *" --input - "*) printf 'BODY %s\n' "$(cat)" >> "$GH_LOG" ;; esac
if [ -n "${GH_FAIL:-}" ]; then case " $* " in *"$GH_FAIL"*) echo "stub: HTTP 403" >&2; exit 1 ;; esac; fi
[ "$1 $2" = "auth status" ] && exit 0
case "$*" in
  "api repos/o/r/rulesets --paginate") printf '%s\n' "${GH_RULESETS:-[]}" ;;
  "api repos/o/r --jq .default_branch") printf '%s\n' "${GH_DEFAULT_BRANCH:-main}" ;;
  *) printf '{}\n' ;;
esac
EOF
chmod +x "$tmp/bin/gh"
log=$tmp/ghlog

# run [ARGS...] runs the script with the stub first on PATH; sets out (stdout and stderr) and rc
run() {
  : > "$log"
  rc=0
  out=$(GH_LOG="$log" PATH="$tmp/bin:$PATH" sh "$SCRIPT" "$@" 2>&1) || rc=$?
}
calls() { cat "$log"; }
body_of() { # body_of SUBSTRING: the JSON body sent with the gh call that contains SUBSTRING
  grep -A1 -F -- "$1" "$log" | sed -n 's/^BODY //p' | head -n 1
}

# --- the dry run: every call is printed, gh is never run, nothing is read from GitHub
run --dry-run --repo o/r
eq "dry run: exit status" 0 "$rc"
eq "dry run: gh was not run at all" "" "$(calls)"
has "dry run: says it is a dry run" "dry run: nothing is sent" "$out"
has "dry run: the default branch check" "gh api repos/o/r --jq .default_branch" "$out"
has "dry run: looks for the main ruleset" "gh api repos/o/r/rulesets --paginate" "$out"
has "dry run: update main when it exists" "gh api -X PUT repos/o/r/rulesets/<id> --input .github/rulesets/main.json" "$out"
has "dry run: create main otherwise" "gh api -X POST repos/o/r/rulesets --input .github/rulesets/main.json" "$out"
has "dry run: the tags ruleset" "gh api -X POST repos/o/r/rulesets --input .github/rulesets/tags.json" "$out"
has "dry run: merge settings call" "gh api -X PATCH repos/o/r --input - <<'JSON'" "$out"
has "dry run: squash only" '"allow_squash_merge":true,"allow_merge_commit":false,"allow_rebase_merge":false' "$out"
has "dry run: squash title" '"squash_merge_commit_title":"PR_TITLE","squash_merge_commit_message":"PR_BODY"' "$out"
has "dry run: delete branch on merge" '"delete_branch_on_merge":true' "$out"
has "dry run: auto-merge" '"allow_auto_merge":true' "$out"
has "dry run: suggest updating branches" '"allow_update_branch":true' "$out"
has "dry run: Dependabot alerts" "gh api -X PUT repos/o/r/vulnerability-alerts" "$out"
has "dry run: Dependabot security updates" "gh api -X PUT repos/o/r/automated-security-fixes" "$out"
has "dry run: secret scanning" '"secret_scanning":{"status":"enabled"}' "$out"
has "dry run: push protection" '"secret_scanning_push_protection":{"status":"enabled"}' "$out"
has "dry run: private vulnerability reporting" "gh api -X PUT repos/o/r/private-vulnerability-reporting" "$out"
has "dry run: read-only token" '"default_workflow_permissions":"read","can_approve_pull_request_reviews":false' "$out"
has "dry run: the workflow permissions endpoint" "gh api -X PUT repos/o/r/actions/permissions/workflow" "$out"
has "dry run: approval for external contributors" '"approval_policy":"all_external_contributors"' "$out"
has "dry run: the approval endpoint" "actions/permissions/fork-pr-contributor-approval" "$out"
# every printed body is valid JSON
eq "dry run: four bodies are printed" 4 "$(printf '%s\n' "$out" | grep -c "<<'JSON'")"
bodies=$(printf '%s\n' "$out" | awk '/<<.JSON.$/ { getline; print }')
bad=$(printf '%s\n' "$bodies" | while IFS= read -r b; do printf '%s' "$b" | jq -e . >/dev/null 2>&1 || echo "invalid JSON: $b"; done)
eq "dry run: every body is valid JSON" "" "$bad"
# It needs neither gh nor a login: a PATH of its own tools, without any gh this machine may have.
mkdir -p "$tmp/tools"
for t in sh jq git sed grep awk tr head cat dirname basename mktemp env; do
  p=$(command -v "$t" 2>/dev/null || true)
  [ -z "$p" ] || ln -sf "$p" "$tmp/tools/$t"
done
rc=0
out=$(PATH="$tmp/tools" "$tmp/tools/sh" "$SCRIPT" --dry-run --repo o/r 2>&1) || rc=$?
eq "dry run without gh on PATH: exit status" 0 "$rc"
has "dry run without gh on PATH: output" "gh api -X PUT repos/o/r/vulnerability-alerts" "$out"

# --- which repository: --repo, GH_REPO, the origin remote in its URL forms
for url in https://github.com/Anemos-labs/Sleipnir.git https://github.com/Anemos-labs/Sleipnir git@github.com:Anemos-labs/Sleipnir.git ssh://git@github.com/Anemos-labs/Sleipnir.git https://github.com/Anemos-labs/Sleipnir/; do
  c=$(mktemp -d "$tmp/copy.XXXXXX")
  mkdir -p "$c/scripts" "$c/.github"
  cp "$SCRIPT" "$c/scripts/"
  cp -R "$root/.github/rulesets" "$c/.github/"
  git init -q "$c"
  git -C "$c" remote add origin "$url"
  rc=0
  out=$(cd "$tmp" && env -u GH_REPO sh "$c/scripts/protect-main.sh" --dry-run 2>&1) || rc=$?
  eq "origin $url: exit status" 0 "$rc"
  has "origin $url: repository" "repos/Anemos-labs/Sleipnir" "$out"
done
c=$(mktemp -d "$tmp/copy.XXXXXX")
mkdir -p "$c/scripts" "$c/.github"
cp "$SCRIPT" "$c/scripts/"
cp -R "$root/.github/rulesets" "$c/.github/"
git init -q "$c"
rc=0
out=$(cd "$tmp" && env -u GH_REPO sh "$c/scripts/protect-main.sh" --dry-run 2>&1) || rc=$?
eq "no origin and no --repo: exit status" 2 "$rc"
has "no origin and no --repo: message" "pass --repo OWNER/REPO" "$out"
rc=0
out=$(GH_REPO=x/y sh "$c/scripts/protect-main.sh" --dry-run 2>&1) || rc=$?
has "GH_REPO is used" "repos/x/y" "$out"
rc=0
out=$(GH_REPO=x/y sh "$c/scripts/protect-main.sh" --dry-run --repo a/b 2>&1) || rc=$?
has "--repo wins over GH_REPO" "repos/a/b" "$out"
hasnt "--repo wins over GH_REPO: the other is absent" "repos/x/y" "$out"
run --dry-run --repo 'not a repo'
eq "a malformed repository name: exit status" 2 "$rc"

# --- a real run (against the stub): nothing exists yet, so both rulesets are created
run --repo o/r
eq "real run: exit status" 0 "$rc"
has "real run: says it worked" "All calls succeeded" "$out"
has "real run: reminds of the manual steps" "CodeQL" "$out"
has "real run: default branch read" "gh api repos/o/r --jq .default_branch" "$(calls)"
has "real run: rulesets listed" "gh api repos/o/r/rulesets --paginate" "$(calls)"
has "real run: main created" "gh api -X POST repos/o/r/rulesets --input .github/rulesets/main.json" "$(calls)"
has "real run: tags created" "gh api -X POST repos/o/r/rulesets --input .github/rulesets/tags.json" "$(calls)"
hasnt "real run: nothing updated" "-X PUT repos/o/r/rulesets/" "$(calls)"
m=$(body_of "gh api -X PATCH repos/o/r --input -" )
eq "real run: merge settings, squash" true "$(printf '%s' "$m" | jq -r .allow_squash_merge)"
eq "real run: merge settings, no merge commits" false "$(printf '%s' "$m" | jq -r .allow_merge_commit)"
eq "real run: merge settings, no rebase" false "$(printf '%s' "$m" | jq -r .allow_rebase_merge)"
eq "real run: merge settings, title" PR_TITLE "$(printf '%s' "$m" | jq -r .squash_merge_commit_title)"
eq "real run: merge settings, message" PR_BODY "$(printf '%s' "$m" | jq -r .squash_merge_commit_message)"
eq "real run: merge settings, delete branch" true "$(printf '%s' "$m" | jq -r .delete_branch_on_merge)"
eq "real run: merge settings, auto-merge" true "$(printf '%s' "$m" | jq -r .allow_auto_merge)"
eq "real run: merge settings, update branch" true "$(printf '%s' "$m" | jq -r .allow_update_branch)"
for endpoint in vulnerability-alerts automated-security-fixes private-vulnerability-reporting; do
  has "real run: PUT $endpoint" "gh api -X PUT repos/o/r/$endpoint" "$(calls)"
done
eq "real run: the order of the security calls (alerts before updates)" "vulnerability-alerts automated-security-fixes" \
  "$(grep -o 'vulnerability-alerts\|automated-security-fixes' "$log" | tr '\n' ' ' | sed 's/ $//')"
s=$(grep -A1 -F 'gh api -X PATCH repos/o/r --input -' "$log" | sed -n 's/^BODY //p' | sed -n 2p)
eq "real run: secret scanning" enabled "$(printf '%s' "$s" | jq -r .security_and_analysis.secret_scanning.status)"
eq "real run: push protection" enabled "$(printf '%s' "$s" | jq -r .security_and_analysis.secret_scanning_push_protection.status)"
w=$(body_of "actions/permissions/workflow")
eq "real run: read-only token" read "$(printf '%s' "$w" | jq -r .default_workflow_permissions)"
eq "real run: Actions cannot approve pull requests" false "$(printf '%s' "$w" | jq -r .can_approve_pull_request_reviews)"
f=$(body_of "fork-pr-contributor-approval")
eq "real run: external contributors" all_external_contributors "$(printf '%s' "$f" | jq -r .approval_policy)"

# --- a real run when the rulesets exist: main is updated by id, an organization's ruleset of the same name is ignored
# shellcheck disable=SC2089,SC2090 # the quotes are JSON, meant literally
GH_RULESETS='[{"id":7,"name":"main","source_type":"Organization"},{"id":4242,"name":"main","source_type":"Repository"}]'
# shellcheck disable=SC2090
export GH_RULESETS
run --repo o/r --only rulesets
unset GH_RULESETS
eq "existing ruleset: exit status" 0 "$rc"
has "existing ruleset: main is updated by its id" "gh api -X PUT repos/o/r/rulesets/4242 --input .github/rulesets/main.json" "$(calls)"
hasnt "existing ruleset: the organization's is not touched" "rulesets/7" "$(calls)"
has "existing ruleset: tags are created" "gh api -X POST repos/o/r/rulesets --input .github/rulesets/tags.json" "$(calls)"
hasnt "--only rulesets: no merge settings" "-X PATCH" "$(calls)"

# --- the default branch must be main
GH_DEFAULT_BRANCH=claude/dev-branch
export GH_DEFAULT_BRANCH
run --repo o/r
eq "default branch is not main: exit status" 2 "$rc"
has "default branch is not main: message" "the default branch of o/r is 'claude/dev-branch', not main" "$out"
has "default branch is not main: points at the steps" "docs/REPO-SETUP.md" "$out"
hasnt "default branch is not main: nothing was written" "-X POST" "$(calls)"
run --repo o/r --only merge
eq "default branch is not main: merge settings do not care" 0 "$rc"
run --repo o/r --any-default-branch
eq "--any-default-branch: exit status" 0 "$rc"
has "--any-default-branch: the rulesets are sent" "-X POST repos/o/r/rulesets" "$(calls)"
unset GH_DEFAULT_BRANCH

# --- a refused call does not stop the others, and is reported
GH_FAIL=vulnerability-alerts
export GH_FAIL
run --repo o/r
unset GH_FAIL
eq "a refused call: exit status" 1 "$rc"
has "a refused call: named at the end" "FAILED: Dependabot alerts (PUT repos/o/r/vulnerability-alerts)" "$out"
has "a refused call: says how to go on" "safe to repeat" "$out"
has "a refused call: the calls after it were made" "gh api -X PUT repos/o/r/private-vulnerability-reporting" "$(calls)"
has "a refused call: the calls after it were made (actions)" "actions/permissions/workflow" "$(calls)"

# --- subsets
run --repo o/r --only security,actions
eq "--only security,actions: exit status" 0 "$rc"
hasnt "--only security,actions: no rulesets" "rulesets" "$(calls)"
hasnt "--only security,actions: no default branch read" "default_branch" "$(calls)"
has "--only security,actions: security is sent" "vulnerability-alerts" "$(calls)"
run --repo o/r --only nonsense
eq "--only with an unknown section: exit status" 2 "$rc"
run --repo o/r --only ""
eq "--only with nothing: exit status" 2 "$rc"

# --- the stub is what stands between this test and GitHub: without gh a real run stops before any call
rc=0
out=$(PATH="$tmp/tools" "$tmp/tools/sh" "$SCRIPT" --repo o/r 2>&1) || rc=$?
eq "a real run without gh: exit status" 2 "$rc"
has "a real run without gh: message" "gh is needed" "$out"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
[ "$1 $2" = "auth status" ] && exit 1
exit 0
EOF
run --repo o/r
eq "a real run when gh is not logged in: exit status" 2 "$rc"
has "a real run when gh is not logged in: message" "gh auth login" "$out"

# --- arguments
run --bogus
eq "unknown argument: exit status" 2 "$rc"
run --repo
eq "--repo without a value: exit status" 2 "$rc"
run --help
eq "--help: exit status" 0 "$rc"
has "--help: text" "scripts/protect-main.sh [--dry-run]" "$out"

printf 'protect-main_test.sh: %d checks passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
