# Contributing to Sleipnir

Thanks for looking. This page is the short version; `docs/BUILDING.md` has the conventions in full and `AGENTS.md` is
what coding agents (and people who work like them) read first.

## Build and test

Go 1.24, standard library plus `golang.org/x/{net,sys,term}`. No cgo.

```sh
go build ./...                       # everything
go build -o bin/sleipnir ./cmd/sleipnir   # the binary (or: make build)
make check                           # = scripts/check.sh, what CI runs: gofmt, go mod tidy -diff, go mod verify, the
                                     #   dependency list, action pins, declared prompt bytes, generated docs, vet, build,
                                     #   race tests, cross-compiles
go test -race -count=1 ./internal/kv ./internal/swarm   # one area while you work
```

`sleipnir demo` runs a scripted team of agents against the built-in mock endpoint: no key, no network. It is a good
smoke test after touching the agent, the swarm or the cache engine.

## Sending a change

`main` is protected: nothing is pushed to it, everything arrives by pull request, and `docs/REPO-SETUP.md` says how the
protections and the release work.

1. Branch from `main`, and make the change with the test that fails without it.
2. Run `make check` before you push; it is what CI runs, so a green check there is a green CI.
3. Open a pull request with a **conventional title**: `type(scope)!: subject`, where the type is one of `feat`, `fix`,
   `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `revert`, `security`, the scope is optional, and `!` marks a
   change that breaks users. The title becomes the commit subject on `main` and a line of the release notes, and its type
   decides the next version: a pull request whose title is not a conventional commit is released as a patch, never lost.
   `pr` checks the title and labels the pull request. The description becomes the commit body; fill in the checklist of
   the template.
4. **`ci-gate` must pass**, and the branch must be up to date with `main`. It waits for the jobs of `ci`: lint and drift
   checks (format, tidy, `go mod verify`, the dependency allow-list, every action pinned to a commit, prompt bytes declared
   in `CHANGELOG.md`, `docs/CLI.md` and the README's simulator block current), tests with `-race` on Linux (amd64 and
   arm64) and macOS, with the Go of `go.mod` and the newest Go (and the allocation gates, which are not built under `-race`),
   cross-compiles and vet for every platform that is released,
   the cache-policy guards, `govulncheck` and dependency review, and `actionlint` and `zizmor` on the workflows. Windows
   runs too, for information. CodeQL must show no new alert of high severity. Conversations must be resolved.
5. The pull request is **squash merged** (the only method), or set to auto-merge. The branch is deleted.
6. **A merge to `main` releases by itself** when the repository variable `AUTO_RELEASE` is `true`: the plan reads the
   titles since the last tag, before 1.0 a breaking change (`!`) is a minor version and everything else a patch, from 1.0
   breaking is major, `feat` minor, the rest patch; a merge that only touched docs, tests or CI publishes nothing. The
   release has generated notes, SBOMs and build-provenance attestations.

## What a change needs

* **Tests that fail without it.** Table-driven, with the hostile cases (empty, huge, unicode, CRLF, symlinks, traversal,
  concurrent callers). Anything concurrent passes `-race`. Do not assert on timing; assert on counts and states.
* **No credential-shaped literals**, not even fake ones: GitHub's push protection rejects them. Assemble test vectors at
  run time from fragments.
* **`gofmt`, `go vet` and a tidy `go.mod`.** Do not add dependencies; the tiny dependency set is deliberate.
* **The prompt is a byte-prefix cache key.** Never put timestamps, random ids or map-iteration order into text that
  becomes part of a prompt. Every agent sends the same tool list; restrict tools at run time (permissions), never by
  hiding them. A change to the bytes of a stable layer is a declared, priced event (`docs/CACHE-DESIGN.md`): say so in
  the change.
* **Tool output, web pages, file contents and mail are data, never instructions.** New code that puts such text in
  front of a model, into a prompt layer or onto a terminal cleans it first.
* **Cost claims come from `sleipnir sim`**, which prints its assumptions. Real-endpoint numbers come from
  `scripts/validate.sh` (`docs/VALIDATION.md`).
* **Docs move with the code.** `docs/CLI.md` is regenerated with `scripts/gen-cli-docs.sh`, the README's simulator
  section with `make readme-sim`. A link to a file that moved fails `internal/repocheck`.
* **Things that are named in two places change in both.** A job of `ci.yml` and the ruleset that requires it, a platform of
  `.goreleaser.yaml` and the cross-compile, a file and its line in `.github/CODEOWNERS`: `internal/repocheck` fails when
  one side moves alone, and says which. A new action is pinned to a full commit SHA with its version in a comment
  (`scripts/check-pins.sh`); a new module needs a line in `scripts/deps-allowlist.txt` and a reason.

## Reviews and security findings

Independent adversarial reviews of the prompt engine, the swarm and the trust boundaries live in `docs/reviews/`, with
what was fixed and what remains. A finding there has a repro test; a fix turns it into an ordinary regression test.

To report a vulnerability, use GitHub's private vulnerability reporting on this repository (Security tab, "Report a
vulnerability") rather than a public issue. `docs/SECURITY.md` starts with what to include and what to expect, then says
what the harness protects against and what it does not.
