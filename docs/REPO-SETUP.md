# Repository configuration

## Continuous integration

[ci.yml](../.github/workflows/ci.yml) checks formatting, dependency drift, generated
documentation, prompt-change declarations, tests, builds, and security tooling.
The required checks are `ci-gate` and `conventional title`, both reported by
GitHub Actions. Windows session ownership, file permissions, and command execution tests are required; the broader Windows
suite is informational. See [platform limitations](SECURITY.md).

[codeql.yml](../.github/workflows/codeql.yml) scans Go and workflows.
[pr.yml](../.github/workflows/pr.yml) validates conventional pull request titles.
[nightly.yml](../.github/workflows/nightly.yml) runs extended checks.

## Branch protection

The workflows target `main`. Set it as the default branch and run CI and CodeQL
before enabling protections. The checked-in rulesets describe the intended
configuration; repository settings must be applied separately by an administrator.

```sh
sh scripts/protect-main.sh --dry-run
sh scripts/protect-main.sh
```

The script applies [branch](../.github/rulesets/main.json) and
[tag](../.github/rulesets/tags.json) rules, squash merging, branch deletion after
merge, and related repository settings. Review its dry-run output before applying it.

The main ruleset requires an up-to-date PR, resolved discussions, linear history,
and no new high-severity CodeQL findings. It has no bypass actors. Approval counts
remain zero so a sole maintainer can merge; CI and review-thread resolution still
apply. Release tags cannot be updated or deleted except by repository admins for
recovery. Creating a new tag is allowed, so the release workflow needs no bypass.

## Agent workflow and review

[AGENTS.md](../AGENTS.md) defines the implementation, validation, review, and merge
workflow. The development-task issue form captures observable acceptance criteria.
The PR template records behavior, validation, and compatibility implications.

[CodeRabbit](https://www.coderabbit.ai/oss) provides free reviews for public
repositories. Install its GitHub App for **Anemos-labs/Sleipnir only** and keep
usage-based billing and paid agent features off. The repository's
[configuration](../.coderabbit.yaml) enables reviews of ready PRs and incremental
updates, with guidance for cache contracts, cancellation, trust boundaries, and
terminal behavior. It disables decorative comments and automatic issue planning.
Bot findings are advisory; required CI and resolved discussions govern merging.

Dependabot groups weekly Go-module and pinned-action updates. CodeQL,
govulncheck, dependency review, actionlint, and zizmor provide independent checks.
Secret scanning and push protection are enabled through repository settings.
Keep CodeQL default setup off: the checked-in workflow uses advanced setup.

## Cost controls

Use standard hosted runners while the repository is public. Their compute is
[free for public repositories](https://docs.github.com/en/actions/reference/runners/github-hosted-runners).
Larger runners are billable; artifact storage has a separate allowance. Release
build artifacts expire after seven days.

Keep organization budgets for Actions, Packages, Codespaces, and Git LFS at **$0**
with **Stop usage when budget limit is reached** enabled. Inspect these under
organization Settings > Billing and licensing > Budgets and alerts, or through
`gh api organizations/Anemos-labs/settings/billing/budgets`.
These budgets do not govern external review-bot subscriptions or licensed GitHub
security products. Do not enable paid plans or overage billing for them.

Billing reports distinguish gross usage, discounts, and `netAmount`. Use the net
amount to identify charges; a fully discounted gross amount is not a bill.

## Releases

[release.yml](../.github/workflows/release.yml) publishes after CI passes on `main`
when the repository variable `AUTO_RELEASE` is `true`. A root `LICENSE` file is
required. [release-plan.sh](../scripts/release-plan.sh) derives the version from
conventional commit titles; documentation, test, and CI-only changes do not release.
The release workflow produces binaries, generated release notes, SBOMs, and
provenance attestations.

An automatic release requires the CI commit to match the release workflow's
signing identity. If `main` advances before CI finishes, the older run is skipped;
a passing run for the newer commit includes the pending changes. Before
publication, the workflow verifies asset checksums and checks each archive's
attestation against the planned source commit and release workflow.

## Maintained configuration

- Pin GitHub Actions to full commit SHAs, with version comments.
- Keep released platforms and CI cross-compilation targets consistent.
- Keep [CODEOWNERS](../.github/CODEOWNERS) aligned with security-sensitive code.
- Declare new modules in [deps-allowlist.txt](../scripts/deps-allowlist.txt).
- Keep the pull request template and protection rules consistent with CI.

`internal/repocheck` tests these relationships. `scripts/check.sh` runs local checks.
