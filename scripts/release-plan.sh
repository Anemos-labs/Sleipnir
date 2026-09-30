#!/bin/sh
# Decide whether the commit that was just merged is released, and as which version.
#
#   scripts/release-plan.sh [--dry-run] [--ref REV]
#
# Prints five key=value lines: release (true|false), tag, version, previous, reason. In a workflow the same lines are
# appended to $GITHUB_OUTPUT. The decision comes from git alone (run it in a full clone: a shallow one has no tags and no
# history, and the script refuses it), so it is the same on a laptop and in CI. Exit status: 0 for every decision,
# 2 for a problem (not a repository, shallow clone, unknown revision, bad argument).
#
#   --dry-run      compute and print; write no $GITHUB_OUTPUT and call no network command (same as DRY_RUN=1)
#   --ref REV      plan for this commit instead of HEAD
#
# Environment: AUTO_RELEASE (the repository variable of that name; anything but exactly "true" keeps releases off),
# DRY_RUN, GITHUB_OUTPUT, and, for one optional network check (does the release already exist on GitHub?),
# GITHUB_REPOSITORY, GH_TOKEN or GITHUB_TOKEN and `gh` on PATH.
#
# The rules, in the order they are applied:
#   1. previous = the highest plain vX.Y.Z tag reachable from the commit (other tags are ignored); none means a first release.
#   2. A plain vX.Y.Z tag on the commit or on any descendant of it: release=false, "already released as vX.Y.Z".
#   3. Nothing user-facing since previous: release=false, "docs/tests/CI only". User-facing is a file under cmd/ or
#      internal/ that is not a test (_test.go), not under testdata/ and not markdown (so every .go file that can be compiled
#      in, and assets that are embedded, such as internal/inspect/web), or go.mod, go.sum, .goreleaser.yaml; or a subject
#      that says feat, fix, perf, revert or breaking.
#   4. The next version: a first release is v0.1.0. Before 1.0 a breaking change (type!: in a subject, or a BREAKING CHANGE
#      footer) bumps the minor and everything else the patch. From 1.0: breaking = major, feat = minor, everything else
#      (fix, perf, refactor, subjects that are not conventional commits) = patch. Squash merges carry the pull request title
#      as the subject: type(scope)!: subject (#123). A footer is not read in a build(deps) commit: Dependabot pastes the
#      dependency's own release notes into its bodies (write build(deps)!: to declare a dependency update breaking).
#   5. The tag already exists on a commit that is not in this history: release=false.
#   6. AUTO_RELEASE is not exactly "true": release=false. Nothing publishes itself until the owner sets that variable.
#   7. No LICENSE, LICENSE.md or LICENSE.txt in the commit's root: release=false, "no LICENSE file: choose one before the
#      first release".
#   8. gh is usable and the release for the tag already exists: release=false.
# tag and version are printed whenever a next version was computed (also when 6 to 8 say no), so a dry run shows what a
# release would be. reason never contains text from a commit, a file name or a pull request.
set -eu
LC_ALL=C
export LC_ALL

die() { printf 'release-plan: %s\n' "$*" >&2; exit 2; }
note() { printf 'release-plan: %s\n' "$*" >&2; }

dry=0
case "${DRY_RUN:-}" in 1|true) dry=1 ;; esac
ref=HEAD
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) dry=1 ;;
    --ref) shift; [ "$#" -gt 0 ] || die "--ref needs a revision"; ref=$1 ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) die "unknown argument: $1 (try --help)" ;;
  esac
  shift
done

git rev-parse --git-dir >/dev/null 2>&1 || die "not inside a git repository"
cd "$(git rev-parse --show-toplevel)"
[ "$(git rev-parse --is-shallow-repository)" = false ] ||
  die "this is a shallow clone: the plan needs every tag and the history (actions/checkout with fetch-depth: 0, or git fetch --unshallow --tags)"
head=$(git rev-parse --verify --quiet "$ref^{commit}") || die "$ref is not a commit of this repository"

SEMVER='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

# highest_tag / lowest_tag: the plain vX.Y.Z tags on stdin ordered by version, not by name (v0.10.0 is above v0.9.0).
ordered_tags() {
  grep -E "$SEMVER" | sed 's/^v\([0-9][0-9]*\)\.\([0-9][0-9]*\)\.\([0-9][0-9]*\)$/\1 \2 \3 &/' |
    sort -k1,1n -k2,2n -k3,3n | cut -d' ' -f4
}

# emit RELEASE TAG VERSION PREVIOUS REASON prints the result; in CI it also goes to $GITHUB_OUTPUT.
emit() {
  out=$(printf 'release=%s\ntag=%s\nversion=%s\nprevious=%s\nreason=%s\n' "$1" "$2" "$3" "$4" "$5")
  printf '%s\n' "$out"
  if [ "$dry" = 0 ] && [ -n "${GITHUB_OUTPUT:-}" ]; then printf '%s\n' "$out" >> "$GITHUB_OUTPUT"; fi
}

merged=$(git tag --merged "$head" --list 'v[0-9]*' | ordered_tags || true)
previous=$(printf '%s\n' "$merged" | tail -n 1)

# 2. Already in a release? (the commit itself may be tagged, or a later commit may be)
containing=$(git tag --contains "$head" --list 'v[0-9]*' | ordered_tags || true)
if [ -n "$containing" ]; then
  first=$(printf '%s\n' "$containing" | head -n 1)
  note "$head is in $first"
  emit false "" "" "$previous" "already released as $first"
  exit 0
fi

# 3. What changed since the last release.
if [ -n "$previous" ]; then
  range="refs/tags/$previous..$head"
  files=$(git -c core.quotepath=off diff --name-only --no-renames "refs/tags/$previous" "$head")
  since=$previous
else
  range=$head
  files=$(git -c core.quotepath=off ls-tree -r --name-only "$head")
  since="the first commit"
fi
ncommits=$(git rev-list --count --no-merges "$range")

# Subjects: how many say breaking, feat, fix/perf/revert, another conventional type, or are not conventional at all.
counts=$(git log --no-merges --format=%s "$range" | awk '
  {
    s = $0
    if (match(s, /^[A-Za-z]+(\([^)]*\))?!?:/)) {
      h = substr(s, 1, RLENGTH - 1)
      if (substr(h, length(h), 1) == "!") { brk++; h = substr(h, 1, length(h) - 1) }
      sub(/\(.*$/, "", h)
      h = tolower(h)
      if (h == "feat") feat++
      else if (h == "fix" || h == "perf" || h == "revert") fix++
      else conv++
    } else other++
  }
  END { printf "%d %d %d %d %d\n", brk, feat, fix, conv, other }')
IFS=' ' read -r n_break n_feat n_fix n_conv n_other <<EOF
$counts
EOF
footer=$(git log --no-merges --format='%x1e%B' "$range" | awk -v RS='\036' '
  {
    n = split($0, l, "\n")
    if (l[1] ~ /^[Bb]uild\(deps[^)]*\)/) next
    for (i = 2; i <= n; i++) if (l[i] ~ /^BREAKING[ -]CHANGE(: | #)/) { c++; break }
  }
  END { print c + 0 }')
breaking=$((n_break + footer))

# User-facing files: what can end up in the binary or its release. Tests, testdata and markdown do not; a file that is
# embedded (internal/inspect/web) does, which is why this is not limited to .go files.
n_user=$(printf '%s\n' "$files" | awk '
  $0 == "go.mod" || $0 == "go.sum" || $0 == ".goreleaser.yaml" { n++; next }
  $0 ~ /^(cmd|internal)\// && $0 !~ /_test\.go$/ && $0 !~ /(^|\/)testdata\// && $0 !~ /\.md$/ { n++ }
  END { print n + 0 }')
note "since $since: $ncommits commits; $n_break breaking subjects, $footer breaking footer, $n_feat feat, $n_fix fix/perf/revert, $n_conv other conventional, $n_other not conventional; $n_user user-facing files"

if [ "$n_user" -eq 0 ] && [ $((n_feat + n_fix + breaking)) -eq 0 ]; then
  emit false "" "" "$previous" "docs/tests/CI only since $since: nothing user-facing changed"
  exit 0
fi

# 4. The next version.
if [ -z "$previous" ]; then
  bump=first
  next=0.1.0
else
  v=${previous#v}
  major=${v%%.*}
  rest=${v#*.}
  minor=${rest%%.*}
  patch=${rest#*.}
  if [ "$major" -eq 0 ]; then
    if [ "$breaking" -gt 0 ]; then bump=minor; else bump="patch"; fi
  elif [ "$breaking" -gt 0 ]; then bump=major
  elif [ "$n_feat" -gt 0 ]; then bump=minor
  else bump="patch"
  fi
  case $bump in
    major) next="$((major + 1)).0.0" ;;
    minor) next="$major.$((minor + 1)).0" ;;
    *) next="$major.$minor.$((patch + 1))" ;;
  esac
fi
tag=v$next
if [ "$bump" = first ]; then
  why="first release"
else
  why="$bump bump since $previous ($ncommits commits, $n_user user-facing files)"
fi

# 5. A tag by that name elsewhere would make `git tag` and the release fail later: say so now.
if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null; then
  emit false "$tag" "$next" "$previous" "tag $tag already exists on a commit that is not in this history: resolve it by hand"
  exit 0
fi

# 6. and 7. The two switches.
if [ "${AUTO_RELEASE:-}" != true ]; then
  emit false "$tag" "$next" "$previous" "AUTO_RELEASE is not true: automatic releases are off (would release $tag: $why)"
  exit 0
fi
if ! git cat-file -e "$head:LICENSE" 2>/dev/null && ! git cat-file -e "$head:LICENSE.md" 2>/dev/null &&
  ! git cat-file -e "$head:LICENSE.txt" 2>/dev/null; then
  emit false "$tag" "$next" "$previous" "no LICENSE file: choose one before the first release"
  exit 0
fi

# 8. The one network question, only when everything else says yes (never in a dry run).
if [ "$dry" = 0 ] && [ -n "${GITHUB_REPOSITORY:-}" ] && { [ -n "${GH_TOKEN:-}" ] || [ -n "${GITHUB_TOKEN:-}" ]; } &&
  command -v gh >/dev/null 2>&1; then
  if GH_TOKEN=${GH_TOKEN:-${GITHUB_TOKEN:-}} gh release view "$tag" --repo "$GITHUB_REPOSITORY" >/dev/null 2>&1; then
    emit false "$tag" "$next" "$previous" "release $tag already exists on GitHub"
    exit 0
  fi
fi

emit true "$tag" "$next" "$previous" "$why"
