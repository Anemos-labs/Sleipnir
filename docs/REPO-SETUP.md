# Repository configuration

## Continuous integration

[ci.yml](../.github/workflows/ci.yml) checks formatting, dependency drift, generated
documentation, prompt-change declarations, tests, builds, and security tooling.
The aggregate required check is `ci-gate`. Windows results are informational;
see [platform limitations](SECURITY.md).

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

## Releases

[release.yml](../.github/workflows/release.yml) publishes after CI passes on `main`
when the repository variable `AUTO_RELEASE` is `true`. A root `LICENSE` file is
required. [release-plan.sh](../scripts/release-plan.sh) derives the version from
conventional commit titles; documentation, test, and CI-only changes do not release.
The release workflow produces binaries, generated release notes, SBOMs, and
provenance attestations.

## Maintained configuration

- Pin GitHub Actions to full commit SHAs, with version comments.
- Keep released platforms and CI cross-compilation targets consistent.
- Keep [CODEOWNERS](../.github/CODEOWNERS) aligned with security-sensitive code.
- Declare new modules in [deps-allowlist.txt](../scripts/deps-allowlist.txt).
- Keep the pull request template and protection rules consistent with CI.

`internal/repocheck` tests these relationships. `scripts/check.sh` runs local checks.
