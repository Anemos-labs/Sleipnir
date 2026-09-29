# Building Sleipnir: conventions for contributors

Sleipnir is a Go (1.24, stdlib-first) coding-agent harness whose defining feature is a
multi-layer prompt-cache engine shared by swarms of agents. This page is the short
version of "how code here is written". Read it before touching a package.

## Toolchain

- `go env -w GOTOOLCHAIN=local` (already set in the dev container). Do not let tooling
  bump the `go` directive in `go.mod`; it must stay `1.24`.
- Do not edit `go.mod` / `go.sum`. Allowed non-stdlib deps are already declared:
  `golang.org/x/net`, `golang.org/x/sys`, `golang.org/x/term`. If you believe another
  dependency is essential, write a stdlib fallback and say so in your report.
- Check your work with: `gofmt -l ./internal` (must print nothing), `go vet ./...`,
  `go test -race -count=1 ./<your packages>/...`.

## Layout

```
cmd/sleipnir        the binary
internal/core       provider-neutral vocabulary (Turn, Block, Prompt, Usage, hashing)
internal/events     append-only event log + content-addressed blobs (source of truth)
internal/kv         layered prompt-cache engine (layers, renderer, planner, compaction)
internal/cost       provider cache models + prices
internal/provider   provider interface; openaichat/ (marketplace + OpenAI), mock/ (test server)
internal/tools      tool contract + shared helpers; tools/fs, tools/shell, tools/web ...
internal/perm       permission engine
internal/swarm      board, mailbox, roles, scheduling
internal/agent      the agent loop
internal/train      training-data export
```

## Style

- Comments explain *why* (invariants, cache/consistency reasoning, security stance), not
  what the next line does. Package docs state the package's one job.
- Errors are values that carry enough context to act on. Errors that a **model** can act
  on (bad arguments, file not found, command failed) are returned as
  `tools.Result{IsError: true}` with a short, actionable message; Go errors are for
  harness failures only.
- Model-visible text (tool descriptions, schemas, error strings) costs tokens on every
  request of every agent. Keep tool descriptions under ~120 words and schemas minimal.
- No global mutable state. Anything shared across agents is protected and documented.
- Deterministic output: sort map iteration, never embed timestamps or random ids in text
  that becomes part of a prompt.

## Tests

- Table-driven, with adversarial cases (empty input, huge input, unicode, CRLF, symlinks,
  path traversal, concurrent callers). Use `t.TempDir()`; never touch the real HOME.
- Anything concurrent must pass `-race`.
- Prefer testing observable behaviour over internals.

## Why tools look the way they do (swarm implications)

- **Every agent gets the same tool list**, byte for byte, so the provider caches the tool
  schemas once for the whole swarm. Role restrictions are enforced at run time
  (`perm.Requester`, `tools.Guard`), never by hiding tools.
- **Many agents edit one repo.** Writes go through `tools.FileState` (an edit is accepted
  only if the agent's last read matches the file now), `tools.Guard` (leases/ownership from
  the swarm layer) and `tools.Snapshotter` (checkpoints). Never write a file without all
  three.
- **Tool output is context bloat.** Every result passes through `Env.Finish`, which
  truncates (head + tail) and stores the full text in the blob store behind a recall handle.
- **Requests-per-minute is the scarce resource**, not tokens: prefer tool designs that do
  more per call (batched edits, multi-file patches, grep with context) over chatty ones.
