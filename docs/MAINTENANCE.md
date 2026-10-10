# Repository maintenance

## Sources of truth

| Contract | Source | Drift check |
|---|---|---|
| Required Go version | `go.mod` | Repository tests compare toolchain statements across root documents and `docs/`; workflows read `go-version-file` |
| Current release and installed version | Git tags and published releases; binary build metadata | Install documentation uses `releases/latest` and `@latest`; release CI checks source identity, checksums, and provenance |
| CLI commands and flags | Binary help | `scripts/gen-cli-docs.sh --check` |
| Command specification of the browser interface | `docs/CLI.md` | `scripts/gen-clispec.sh --check` |
| Route set of the browser interface (`docs/WEB-API.md`) | The routes the host registers (`web.Server.Routes`) | `TestWebAPIDocumentListsEveryRegisteredRoute` in `cmd/sleipnir` compares them with the route tables of the document |
| Provider option names | Adapter configuration | Configuration tests compare documented options with supported keys |
| Cache simulation output | Simulator | CI regenerates `docs/CACHE-ECONOMICS.md` and compares it |
| Stable prompt bytes | Renderers, schemas, golden fixtures | Golden tests and `scripts/check-declared.sh` require a compatibility declaration |
| Supported release targets | `.goreleaser.yaml` | Repository tests compare release, cross-build, and test configuration |
| Main protection | `.github/rulesets/` and CI gate | Repository tests check definitions; `scripts/protect-main.sh` reads/applies GitHub settings |
| Links, production doc comments, platform coverage | Source tree | `go test ./internal/repocheck` |

[Releases](https://github.com/Anemos-labs/Sleipnir/releases) contain the versioned
history. `CHANGELOG.md` holds cumulative compatibility declarations, including
prompt-cache implications. It does not require a manual version rollover after
each release. Preserve historical versions in tests and compatibility examples;
installation instructions should resolve the current release.

When behavior changes, update its public contract and regression tests in the
same pull request. Generated listings detect structural drift; they do not prove
that surrounding prose or comments are accurate. Review those against the code.

## File and line age audit

Run with the existing Go toolchain and Git, without additional dependencies:

```sh
go run ./cmd/codeage -require-full-history
# Or: make audit
```

Open `.sleipnir/tmp/code-age/index.html`. `report.json` contains the same inventory
for comparison or other analysis. `scripts/heatmap.sh` forwards the command's
flags. Use `-rev <commit>` to inspect a specific revision, `-out <directory>` to
choose a report directory, and `-jobs <n>` to limit concurrent Git processes.
Reports are generated under ignored local state, never committed to source.

The CI lint job generates and uploads the `code-age` artifact on each push to
main and manual CI run. It uses the existing standard runner and retains reports
for seven days. Collection failures fail the job; age itself never fails a build.
Pull requests test the collector without rescanning the entire history.

The heatmap includes every tracked file at one pinned commit. It reports:

- The last commit that modified each current path.
- The oldest and median surviving nonblank line dates, with up to ten oldest
  line locations linked to that exact source revision when origin is on GitHub.
- Categories for code, tests, documentation, automation, fixtures, media, and
  other files; filters and separate rankings for file and line dates.
- Explicit line-analysis exclusions and collection errors. Binary, non-UTF-8,
  symlink, submodule, and files over 2 MiB retain file dates. Submodule contents
  are not traversed. Blank lines are excluded; comments count as lines.

Line attribution uses `git blame -w -M`: whitespace edits do not reset age,
renames are followed, and detectable moves within a file retain provenance.
Copies between files are not tracked. Dates use Git committer time, so rebases,
squashes, imported history, and inaccurate clocks affect them. Shallow history
is clearly marked and rejected by `-require-full-history`. Uncommitted and
untracked files are excluded. No report executes project code or contacts a
remote service; missing local Git objects are reported as errors.

## Reviewing older areas

Review both rankings: a recently changed file can contain much older logic.
Filter by subsystem so large fixture collections do not hide a small package.
Check the oldest surviving contracts against their callers, error handling,
concurrency, platform support, documentation, and relevant tests. Use profiling
or task evaluations to investigate performance; timestamps cannot establish it.

Keep correct, tested code unchanged. Fix a demonstrated defect with a focused
regression and update its contract. Track unfinished product work in the roadmap
or an issue with acceptance criteria. Do not refresh timestamps, add diary notes,
or rewrite stable code to make a heatmap appear newer.
