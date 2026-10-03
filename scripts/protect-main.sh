#!/bin/sh
# Apply the repository settings that docs/REPO-SETUP.md describes, through the GitHub API with the gh CLI.
#
#   scripts/protect-main.sh [--dry-run] [--repo OWNER/REPO] [--only LIST] [--any-default-branch]
#
#   --dry-run              print every gh call and change nothing (gh is not needed, nothing is read from GitHub)
#   --repo OWNER/REPO      the repository (default: $GH_REPO, else the origin remote of this checkout)
#   --only LIST            comma-separated subset of: rulesets, merge, security, actions (default: all four)
#   --any-default-branch   go on although the default branch is not main (the rulesets protect the default branch)
#
# What it sets, each in its own call, so that one refusal does not stop the others (they are listed at the end, exit 1):
#   rulesets   .github/rulesets/main.json and .github/rulesets/tags.json: created, or updated when a ruleset of that
#              name exists (looked up by name)
#   merge      squash merge only; the squash commit is the pull request title and body; delete the branch on merge;
#              allow auto-merge; suggest updating pull request branches
#   security   Dependabot alerts and security updates, secret scanning with push protection, private vulnerability reporting
#   actions    the default workflow token is read-only and Actions cannot approve pull requests; workflows of every
#              external contributor need approval
# It needs gh, logged in as a repository admin (gh auth login), and jq. It never stores or prints a credential.
# Review automation and cost controls are documented in docs/REPO-SETUP.md.
set -eu
LC_ALL=C
export LC_ALL

dry=0
repo=${GH_REPO:-}
only=rulesets,merge,security,actions
anybranch=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) dry=1 ;;
    --repo) shift; [ "$#" -gt 0 ] || { echo "protect-main: --repo needs OWNER/REPO" >&2; exit 2; }; repo=$1 ;;
    --only) shift; [ "$#" -gt 0 ] || { echo "protect-main: --only needs a list" >&2; exit 2; }; only=$1 ;;
    --any-default-branch) anybranch=1 ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) echo "protect-main: unknown argument: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done
cd "$(dirname "$0")/.."

die() { echo "protect-main: $*" >&2; exit 2; }

[ -n "$only" ] || die "--only needs at least one of rulesets, merge, security, actions"
for s in $(printf '%s' "$only" | tr ',' ' '); do
  case $s in rulesets|merge|security|actions) ;; *) die "--only: '$s' is not one of rulesets, merge, security, actions" ;; esac
done
want() { case ",$only," in *",$1,"*) return 0 ;; esac; return 1; }

# The repository: --repo, $GH_REPO, or the origin remote (https, ssh and scp-like URLs).
if [ -z "$repo" ]; then
  url=$(git remote get-url origin 2>/dev/null || true)
  repo=$(printf '%s\n' "$url" | sed 's#^.*github\.com[:/]##; s#\.git$##; s#/$##')
fi
printf '%s\n' "$repo" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' ||
  die "cannot tell the repository (got '$repo'): pass --repo OWNER/REPO"

if [ "$dry" = 0 ]; then
  command -v gh >/dev/null 2>&1 || die "gh is needed (https://cli.github.com); --dry-run prints the calls without it"
  command -v jq >/dev/null 2>&1 || die "jq is needed"
  gh auth status >/dev/null 2>&1 || die "gh is not logged in: run gh auth login (as an administrator of $repo)"
else
  command -v jq >/dev/null 2>&1 || die "jq is needed (it reads the names of the ruleset files)"
  echo "# dry run: nothing is sent and gh is not run; these are the calls a real run makes, for $repo"
fi

failed=""
say() { printf '\n# %s\n' "$*"; }
fail() { failed="$failed
  - $*"; echo "protect-main: FAILED: $*" >&2; }

# gh_api DESCRIPTION METHOD PATH [--input FILE | --body JSON]: one call; a failure is recorded, not fatal.
gh_api() {
  desc=$1 method=$2 path=$3
  shift 3
  mode=none
  arg=""
  if [ "$#" -ge 2 ]; then mode=$1; arg=$2; fi
  if [ "$dry" = 1 ]; then
    case $mode in
      none) echo "gh api -X $method $path" ;;
      --input) echo "gh api -X $method $path --input $arg" ;;
      --body) printf "gh api -X %s %s --input - <<'JSON'\n%s\nJSON\n" "$method" "$path" "$arg" ;;
    esac
    return 0
  fi
  ok=0
  case $mode in
    none) gh api -X "$method" "$path" >/dev/null && ok=1 ;;
    --input) gh api -X "$method" "$path" --input "$arg" >/dev/null && ok=1 ;;
    --body) printf '%s\n' "$arg" | gh api -X "$method" "$path" --input - >/dev/null && ok=1 ;;
  esac
  if [ "$ok" = 1 ]; then echo "ok: $desc"; else fail "$desc ($method $path)"; fi
}

# The rulesets protect the default branch: it must be main, or they would protect the wrong branch.
default_branch_check() {
  if [ "$dry" = 1 ]; then
    say "the default branch must be main (the rulesets protect the default branch)"
    echo "gh api repos/$repo --jq .default_branch"
    return 0
  fi
  db=$(gh api "repos/$repo" --jq .default_branch 2>/dev/null) || die "cannot read $repo: is the name right, and are you an administrator?"
  if [ "$db" != main ] && [ "$anybranch" = 0 ]; then
    die "the default branch of $repo is '$db', not main: the rulesets would protect '$db'. See docs/REPO-SETUP.md, or pass --any-default-branch"
  fi
}

ruleset_upsert() {
  file=$1
  [ -f "$file" ] || { fail "$file is missing"; return 0; }
  name=$(jq -r .name "$file") || { fail "$file is not valid JSON"; return 0; }
  say "ruleset \"$name\" from $file: update it when one by that name exists, else create it"
  if [ "$dry" = 1 ]; then
    echo "gh api repos/$repo/rulesets --paginate    # the id of the ruleset named \"$name\", if any"
    echo "gh api -X PUT repos/$repo/rulesets/<id> --input $file    # when it exists"
    echo "gh api -X POST repos/$repo/rulesets --input $file    # otherwise"
    return 0
  fi
  list=$(gh api "repos/$repo/rulesets" --paginate) || { fail "list the rulesets of $repo"; return 0; }
  id=$(printf '%s\n' "$list" | jq -r --arg n "$name" '.[] | select(.name == $n and .source_type == "Repository") | .id' | head -n 1)
  if [ -n "$id" ]; then
    gh_api "update ruleset \"$name\" (id $id)" PUT "repos/$repo/rulesets/$id" --input "$file"
  else
    gh_api "create ruleset \"$name\"" POST "repos/$repo/rulesets" --input "$file"
  fi
}

if want rulesets; then
  echo "== rulesets"
  default_branch_check
  for f in .github/rulesets/main.json .github/rulesets/tags.json; do ruleset_upsert "$f"; done
fi

if want merge; then
  echo "== merge settings"
  say "squash merge only, the squash commit is the pull request title and body, delete the branch on merge, allow auto-merge, suggest updating the branch"
  gh_api "merge settings" PATCH "repos/$repo" --body '{"allow_squash_merge":true,"allow_merge_commit":false,"allow_rebase_merge":false,"squash_merge_commit_title":"PR_TITLE","squash_merge_commit_message":"PR_BODY","delete_branch_on_merge":true,"allow_auto_merge":true,"allow_update_branch":true}'
fi

if want security; then
  echo "== security features"
  say "Dependabot alerts, then security updates (they need the alerts)"
  gh_api "Dependabot alerts" PUT "repos/$repo/vulnerability-alerts"
  gh_api "Dependabot security updates" PUT "repos/$repo/automated-security-fixes"
  say "secret scanning and push protection"
  gh_api "secret scanning and push protection" PATCH "repos/$repo" --body '{"security_and_analysis":{"secret_scanning":{"status":"enabled"},"secret_scanning_push_protection":{"status":"enabled"}}}'
  say "private vulnerability reporting (the Report a vulnerability button that docs/SECURITY.md points to)"
  gh_api "private vulnerability reporting" PUT "repos/$repo/private-vulnerability-reporting"
fi

if want actions; then
  echo "== Actions"
  say "the default token is read-only, and Actions cannot approve pull requests"
  gh_api "default workflow permissions" PUT "repos/$repo/actions/permissions/workflow" --body '{"default_workflow_permissions":"read","can_approve_pull_request_reviews":false}'
  say "workflow runs of every external contributor need approval (if this endpoint is unknown to your GitHub: Settings > Actions > General > Fork pull request workflows)"
  gh_api "approval for all external contributors" PUT "repos/$repo/actions/permissions/fork-pr-contributor-approval" --body '{"approval_policy":"all_external_contributors"}'
fi

if [ "$dry" = 1 ]; then
  printf '\n# dry run: nothing was sent.\n'
  exit 0
fi
if [ -n "$failed" ]; then
  printf '\nprotect-main: these calls failed; do them by hand (docs/REPO-SETUP.md) or fix the cause and run again (every call is safe to repeat):%s\n' "$failed" >&2
  exit 1
fi
cat <<EOF

All calls succeeded. Keep CodeQL "default setup" off when using the checked-in workflow.
Set AUTO_RELEASE=true only when successful main CI should publish releases. Verify zero-dollar
budgets and review-bot billing separately; see docs/REPO-SETUP.md.
EOF
