# Contributing to Sleipnir

Thanks for looking. This page is the short version; `docs/BUILDING.md` has the conventions in full and `AGENTS.md` is
what coding agents (and people who work like them) read first.

## Build and test

Go 1.25, standard library plus `golang.org/x/{net,sys,term}`. No cgo.

```sh
go build ./...                       # everything
go build -o bin/sleipnir ./cmd/sleipnir   # the binary (or: make build)
scripts/check.sh                     # what CI runs: gofmt, go mod tidy -diff, vet, build, race tests, cross-compiles
go test -race -count=1 ./internal/kv ./internal/swarm   # one area while you work
```

`sleipnir demo` runs a scripted team of agents against the built-in mock endpoint: no key, no network. It is a good
smoke test after touching the agent, the swarm or the cache engine.

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
  section with `make readme-sim`.

## Reviews and security findings

Independent adversarial reviews of the prompt engine, the swarm and the trust boundaries live in `docs/reviews/`, with
what was fixed and what remains. A finding there has a repro test; a fix turns it into an ordinary regression test.

To report a vulnerability, use GitHub's private vulnerability reporting on this repository (Security tab, "Report a
vulnerability") rather than a public issue. `docs/SECURITY.md` says what the harness protects against and what it does
not.
