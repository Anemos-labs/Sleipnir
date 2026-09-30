# Repository setup: what GitHub does for this repository

The aim: CI proves stability, a merge that passes it becomes a release by itself, and everything that is linked to
something else is checked against it, so that nobody has to remember. Everything below is in the repository as files,
because the tool that opens this repository's pull requests cannot change repository settings. A person with admin
rights applies them once, in the order of section 2. After that GitHub does the work.

## 1. What exists

| File | What it is |
|---|---|
| [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) | The gate: lint and drift checks, tests on four runner and Go combinations, a Windows run that is informational, cross-compiles and vet for every shipped platform, cache-policy guards, vulnerability scan and dependency review, workflow lint, and the one check that branch protection requires, `ci-gate`. |
| [`.github/workflows/release.yml`](../.github/workflows/release.yml) | After ci passes on main: plan the release, build it with goreleaser (SBOMs, provenance attestations), publish it with generated notes. Off until the variable `AUTO_RELEASE` is `true`. |
| [`.github/workflows/codeql.yml`](../.github/workflows/codeql.yml) | CodeQL for Go and for the workflows themselves, on every pull request, every push to main and weekly. |
| [`.github/workflows/pr.yml`](../.github/workflows/pr.yml) | Checks that a pull request title is a conventional commit and labels the pull request from it. Never checks out code. |
| [`.github/workflows/nightly.yml`](../.github/workflows/nightly.yml) | Fuzzing, the suite three times under `-race`, coverage, the drift checks and a vulnerability scan. Informational. |
| [`.github/rulesets/main.json`](../.github/rulesets/main.json), [`tags.json`](../.github/rulesets/tags.json) | The rules for the default branch and for `v*` tags, importable in the web UI and posted by the script. |
| [`.github/release.yml`](../.github/release.yml) | The categories of the generated release notes. |
| [`.github/dependabot.yml`](../.github/dependabot.yml) | Weekly, grouped updates of the Go modules and of the pinned actions. |
| [`.github/CODEOWNERS`](../.github/CODEOWNERS) | Who is asked to review what, with a line for every hand-edited security zone. |
| [`.github/pull_request_template.md`](../.github/pull_request_template.md) | The checklist of linked things. |
| [`scripts/protect-main.sh`](../scripts/protect-main.sh) | Applies the rulesets and the repository settings through the API (`--dry-run` prints every call). |
| [`scripts/release-plan.sh`](../scripts/release-plan.sh) | Decides whether a commit on main is released and as which version (section 2, step 6). |
| [`scripts/check-deps.sh`](../scripts/check-deps.sh), [`check-pins.sh`](../scripts/check-pins.sh), [`check-declared.sh`](../scripts/check-declared.sh) | The drift checks that are shell, each with a `--help` and a test (`*_test.sh`). |
| [`scripts/deps-allowlist.txt`](../scripts/deps-allowlist.txt) | The modules the project may depend on. |
| [`internal/repocheck/`](../internal/repocheck) | The repository's own invariants, as Go tests (section 4). |

## 2. The order

### Step 1. Make `main` the default branch

The workflows trigger on `main`, and `release.yml` starts from a `workflow_run` event, which GitHub only delivers for a
workflow file on the default branch. The rulesets apply to the default branch. The repository has one branch today
(`claude/intelligent-ptolemy-zp1wbt`). Either rename it: **Settings > General > Default branch**, the pencil next to the
branch name, enter `main`. GitHub retargets open pull requests. Or create `main` from its tip and switch:

```sh
git push origin claude/intelligent-ptolemy-zp1wbt:refs/heads/main
```

then **Settings > General > Default branch**, the arrows icon, choose `main`, Update. Make sure the tip holds this work
(`.github/`, `scripts/`, `internal/repocheck/`) before you do; the required check `ci-gate` has to exist on `main`.

### Step 2. Add a LICENSE

The owner has not chosen one, so none is added. Put `LICENSE` (or `LICENSE.md`, `LICENSE.txt`) at the repository root and
replace "To be decided before the first release" in the README. Until then `scripts/release-plan.sh` answers
`no LICENSE file: choose one before the first release` and nothing is ever released. Do this before step 3 (a push to an
unprotected `main` is allowed) or as the first pull request after it.

### Step 3. Protect `main` and the tags

With the GitHub CLI, logged in as an administrator of the repository (`gh auth login`):

```sh
sh scripts/protect-main.sh --dry-run        # prints every gh call, changes nothing
sh scripts/protect-main.sh                  # applies them; safe to repeat
```

It creates or updates the two rulesets (found by name), then sets the merge settings (squash only, the pull request title
and body as the commit message, delete the branch on merge, auto-merge allowed, "suggest updating" on), Dependabot alerts
and security updates, secret scanning with push protection, private vulnerability reporting, a read-only default workflow
token that cannot approve pull requests, and approval for the workflow runs of every outside contributor. It refuses to
run while the default branch is not `main` (`--any-default-branch` overrides), and a call that GitHub refuses is listed at
the end instead of stopping the others. `--only rulesets,merge,security,actions` runs a part.

By hand instead: **Settings > Rules > Rulesets > New ruleset > Import a ruleset**, choose `.github/rulesets/main.json`,
then **Create**; again with `.github/rulesets/tags.json`. The other settings are in **Settings > General** (Pull
Requests), **Settings > Advanced Security** and **Settings > Actions > General**; `--dry-run` lists every call, and each call is one setting.

What `main.json` enforces on the default branch, with nobody allowed to bypass it: no deletion, no force push, linear
history, a pull request (squash merge only, stale reviews dismissed on push, conversations resolved, zero approvals
because there is one maintainer and nobody can approve their own pull request), the status check `ci-gate` from the GitHub
Actions app with the branch up to date (`strict`), and code scanning results from CodeQL with no new alert of high
severity or worse. `tags.json` makes `v*` tags undeletable and immovable, except for the Actions app and repository
administrators; creating one stays allowed, because the release API creates them.

**Required signatures are left out on purpose.** Squash merges are made by GitHub and signed by it, so the rule would add no
protection against anything the other rules do not stop; what it would add is a failure mode: any administrator push, and
any automation without a signing key, would be refused, with no bypass to fall back on. If you later want every commit on
main to carry a signature you made, add `{ "type": "required_signatures" }` to the rules of `main.json` and commit through
a signed-commit-capable path.

### Step 4. Settings no script sets

| Setting | Where | Why |
|---|---|---|
| CodeQL **default setup** off | **Settings > Advanced Security > Code scanning > CodeQL analysis**: if it says "Default", open the menu and switch it off (or `gh api -X PATCH repos/OWNER/REPO/code-scanning/default-setup -f state=not-configured`) | `codeql.yml` is the advanced setup. With both, GitHub refuses the results of the workflow and the `code_scanning` rule has nothing to read. |
| Dependency graph on | **Settings > Advanced Security > Dependency graph** (on by default in a public repository) | The dependency review job of ci fails without it. |
| Require SHA pinning | **Settings > Actions > General > Actions permissions**: optionally "Require actions to be pinned to a full-length commit SHA" (if your page shows it) | Enforces at run time what `scripts/check-pins.sh` checks in the files. |
| Anything the script reported as failed | The page named in its output | A token without the right scope, or an endpoint your GitHub does not have yet. Approval for outside contributors lives in **Settings > Actions > General > Fork pull request workflows from outside collaborators** ("Require approval for all outside collaborators"). |
| `AUTO_RELEASE` | step 5 | The switch that lets a merge publish. |

### Step 5. Switch the release on, when you are ready

First a dry run: **Actions > release > Run workflow**, on `main`, with "dry run" ticked (the default). It plans, builds a
snapshot, uploads the files as a workflow artifact and publishes and attests nothing; the run's summary says what the plan
decided and why. Then:

```sh
gh variable set AUTO_RELEASE --body true        # or Settings > Secrets and variables > Actions > Variables
```

Anything but exactly `true` keeps releases off. Delete the variable to stop them again.

### Step 6. What a merge does, end to end

1. A branch is pushed and a pull request opened. `pr` checks the title and labels the pull request; `ci` runs
   (`ci-gate` is the check that counts); CodeQL runs; the dependency review comments if a dependency is at fault.
2. Everything is green, the conversations are resolved, the branch is up to date with `main`: the pull request is squash
   merged (or set to auto-merge). The title becomes the commit subject, the description its body.
3. `ci` runs again on the push to `main`. When it succeeds, `release` starts (for a `push` event of this repository, nothing
   else).
4. `plan` runs `scripts/release-plan.sh` on that commit. Table in section 5. If it says `release=false`, the run ends there and
   its summary says why (docs, tests and CI only; AUTO_RELEASE is off; no LICENSE; the commit is already in a release).
5. `build` tags the commit locally, runs goreleaser without publishing, writes an SBOM per archive, attests the archives and
   `checksums.txt`, and uploads them as an artifact.
6. `publish`, the only job that can write, creates the GitHub release and its tag at that commit with `gh release create`,
   notes generated from the pull requests since the previous release and sorted by `.github/release.yml`. If the release
   exists it does nothing.

`scripts/install.sh` and `go install` pick up the new version.

## 3. Every automatic protection

| Protection | Guards against | Lives in | Run it locally |
|---|---|---|---|
| Branch ruleset | A direct push, a force push, a merge that skipped review, CI or code scanning, a merge commit that breaks linear history | `.github/rulesets/main.json` | `sh scripts/protect-main.sh --dry-run` |
| Tag ruleset | A released tag moved or deleted | `.github/rulesets/tags.json` | same |
| `ci-gate` | Merging while any required job failed or was cancelled; one check name to protect, so a new job is not forgotten | `ci.yml` (`gate`) | `sh scripts/check.sh` |
| lint | Unformatted code, an untidy `go.mod`, a `go.sum` that does not match the downloads, vet findings | `ci.yml` | `sh scripts/check.sh` |
| Dependency allow-list | A new module, direct or through an update, or a `replace` that swaps one | `scripts/check-deps.sh` | `sh scripts/check-deps.sh` |
| SHA pins | An action that can change under us (a tag or branch) | `scripts/check-pins.sh`, `internal/repocheck` | `sh scripts/check-pins.sh` |
| Declared prompt bytes | A change to a golden file of the prompt engine without a priced CHANGELOG entry | `scripts/check-declared.sh` | `sh scripts/check-declared.sh origin/main` |
| Documents agree with code | A stale `docs/CLI.md`, a stale simulator block in the README, a recording that no longer matches the interface | `ci.yml`, `nightly.yml` | `sh scripts/gen-cli-docs.sh --check`, `make readme-sim` |
| Tests | A regression on Linux (amd64 and arm64), macOS, with the Go of `go.mod` and with the newest stable Go, under the race detector | `ci.yml` `test` | `go test -race -count=1 ./...` |
| Windows run (informational) | Windows breakage, seen but not blocking | `ci.yml` `windows` | `go test ./...` |
| Cross-compile and vet | A file for another platform that stopped compiling; release day finding out | `ci.yml` `cross` | `sh scripts/check.sh` |
| Cache-policy guards | A policy change that makes the cache model lose | `ci.yml` `sim` | `make sim` |
| govulncheck | A known vulnerability in a dependency or the standard library that the code reaches | `ci.yml` `security`, `nightly.yml` | `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...` |
| Dependency review | A pull request that adds a dependency with an advisory | `ci.yml` `security` | none (needs the pull request) |
| actionlint, zizmor | A mistake in a workflow; a workflow that can be attacked (injection, excess permissions, persisted credentials) | `ci.yml` `workflows` | `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`, `pipx run zizmor .` |
| CodeQL | A security bug in the Go code or in a workflow | `codeql.yml` | none |
| Repository invariants | A link that does not resolve, an unpinned action, a renamed job that the ruleset names, a stale CODEOWNERS line, a platform missing from the cross-compile, an installer that asks for a file goreleaser does not make, and more (section 4) | `internal/repocheck` | `go test ./internal/repocheck` |
| Conventional title and labels | A pull request whose title the release plan cannot read; release notes without categories | `pr.yml` | none |
| Release gate | A release nobody meant: AUTO_RELEASE off, no LICENSE, nothing user-facing changed, the commit already released, the release exists | [`scripts/release-plan.sh`](../scripts/release-plan.sh) | `sh scripts/release-plan.sh --dry-run` |
| Provenance and SBOM | A binary nobody can trace to a commit and a workflow | `release.yml`, `.goreleaser.yaml` | `gh attestation verify FILE --repo OWNER/REPO` |
| Dependabot | Stale modules and stale pins, weekly and grouped | [`.github/dependabot.yml`](../.github/dependabot.yml) | none |
| CODEOWNERS | A security zone changed without its owner being asked | [`.github/CODEOWNERS`](../.github/CODEOWNERS) | `go test ./internal/repocheck` |
| Secret scanning, push protection, private vulnerability reporting | A leaked credential; a vulnerability reported in public | settings, `scripts/protect-main.sh`, `docs/SECURITY.md` | none |
| Read-only token, approval of outside runs | A workflow with more power than it needs; a fork's pull request running code unasked | settings, `scripts/protect-main.sh`, top-level `permissions:` of every workflow | none |

## 4. What is linked to what

The left column is where the truth is written; the right columns are the copy and what notices when the two differ.

| Source of truth | Derived or named elsewhere | Check | Runs |
|---|---|---|---|
| The binary's flags and commands (`--help`) | `docs/CLI.md` | `scripts/gen-cli-docs.sh --check` | lint, nightly, `check.sh` |
| `sleipnir sim` output | The simulator block of `README.md` | `scripts/readme-sim.sh` and a diff | lint, nightly, `check.sh` |
| Golden files under `internal/{kv,agent,core}/testdata/golden/` (the prompt bytes) | An entry in `CHANGELOG.md`, priced with `sleipnir sim` | `scripts/check-declared.sh BASE` | lint, `check.sh` |
| `go.mod` and `go.sum` | `scripts/deps-allowlist.txt`; the modules the build compiles | `scripts/check-deps.sh`, `go mod tidy -diff`, `go mod verify`, dependency review, govulncheck | lint, security |
| The commits pinned in every workflow | The `# vX.Y.Z` comment; Dependabot's updates | `scripts/check-pins.sh`, `TestWorkflowUsesArePinned`, zizmor | lint, workflows, `go test` |
| The job names of `ci.yml` | The required check in `main.json` | `TestRulesetRequiresJobsOfCI` | `go test` |
| The jobs of `ci.yml` | The `needs` of `ci-gate` | `TestCIGate` | `go test` |
| The name of the `ci` workflow | `workflow_run` in `release.yml` | `TestReleaseIsGated` | `go test` |
| The platforms of `.goreleaser.yaml` | The `cross` job of `ci.yml`, the loop of `scripts/check.sh` | `TestCrossCompileCoversGoreleaserTargets` | `go test` |
| The archive name and checksum file of `.goreleaser.yaml` | `scripts/install.sh` | `TestInstallScriptMatchesGoreleaser` | `go test` |
| The files of the repository | [`.github/CODEOWNERS`](../.github/CODEOWNERS) | `TestCodeownersPatternsMatchFiles` | `go test` |
| The files of the repository | Every relative link of every `.md` | `TestMarkdownLinksResolve` | `go test` |
| The labels `pr.yml` sets | The categories of `.github/release.yml` | `TestPullRequestLabelsMatchReleaseNotes` | `go test` |
| The drift checks in `scripts/` | `ci.yml` and `scripts/check.sh` | `TestDriftChecksAreWired` | `go test` |
| The interface code | The recorded demo, when `scripts/record-demo.sh` exists | `scripts/record-demo.sh --check` | lint, nightly |
| The version of govulncheck | Both workflows that run it | `TestToolVersionsAgree` | `go test` |

Versions that only a person can move, because Dependabot cannot see a version inside a command: goreleaser (`release.yml`,
`version:`), govulncheck and actionlint (`@v...` in `ci.yml`, `nightly.yml`), zizmor (`version:` in `ci.yml`). Bump them on
purpose, now and then.

## 5. The release plan

`scripts/release-plan.sh` prints `release`, `tag`, `version`, `previous` and `reason`, from git alone. `--dry-run` prints them
without writing `$GITHUB_OUTPUT` and without the one network question; try it on any checkout.

| Situation | Result |
|---|---|
| No version tag reachable | A first release, `v0.1.0` |
| A `vX.Y.Z` tag on the commit, or on a later commit | `release=false`, "already released as vX.Y.Z" |
| Since the last tag only docs, tests and CI changed, and no subject says feat, fix, perf, revert or breaking | `release=false`, "docs/tests/CI only" |
| Before 1.0, a breaking change (`type!:` in a subject, or a `BREAKING CHANGE` footer) | minor bump |
| Before 1.0, anything else user-facing | patch bump (a `feat` too) |
| From 1.0, breaking / `feat` / anything else | major / minor / patch |
| A subject that is not a conventional commit | counts as patch when a user-facing file changed |
| `AUTO_RELEASE` is not exactly `true` | `release=false`; the reason says what would be released |
| No `LICENSE`, `LICENSE.md` or `LICENSE.txt` at the root | `release=false`, "no LICENSE file: choose one before the first release" |
| The tag exists on a commit outside this history, or the release exists on GitHub | `release=false`, with the reason |

User-facing: a non-test, non-markdown file under `cmd/` or `internal/` (testdata excluded; embedded assets count), `go.mod`,
`go.sum`, `.goreleaser.yaml`. Tags that are not plain `vX.Y.Z` are ignored. The plan refuses a shallow clone.

## 6. Other decisions you may want to know

* **`ci-gate` is the only required check.** It needs every job except `windows`. `security` is in it: a vulnerability
  published today turns every pull request red until a fixed version is merged (Dependabot proposes it; the nightly run
  tells you first). If that is too strict, remove `security` from the `needs` of `gate` in a pull request of its own; the
  tests will say if you forgot something else.
* **The title check is not required.** `pr.yml` reports it on the pull request; to make it binding add a second required
  check named `conventional title` to `main.json`. A title that is not a conventional commit is released as a patch, never
  silently dropped.
* **Code owners are asked, not required.** One maintainer cannot approve their own pull request.
* **A waiting release run is replaced by a newer one** (GitHub's rule for a concurrency group without cancellation). The newer
  run releases everything since the last tag, so nothing is lost; it may only come later.
* **The release builds with the newest stable Go**, because Go 1.24 is out of support; `go.mod` keeps saying `go 1.24`, and the
  `go.mod` leg of the test matrix proves it still builds and passes.
* **A failed upload leaves a draft release**, never a published one without files: delete the draft on the Releases page and
  run the workflow again.
* **After a release is wrong:** an administrator deletes the release and the tag (the tag ruleset lets administrators), fixes
  the cause, and merges the next pull request; the plan recomputes.
* **First run.** `release` only runs from the default branch's copy of the workflow. `ci-gate` appears as a required check
  once `ci` has run on a pull request; if a pull request shows "Expected — waiting for status to be reported", `ci` did not run
  for it (look for a path filter or a disabled workflow).
