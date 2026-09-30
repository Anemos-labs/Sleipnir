# Building Sleipnir: conventions for contributors

Sleipnir is a Go (1.24, stdlib-first) coding-agent harness whose defining feature is a
multi-layer prompt-cache engine shared by swarms of agents. This page is the short
version of "how code here is written". Read it before touching a package.

## Toolchain

- `go env -w GOTOOLCHAIN=local` (already set in the dev container). Do not let tooling
  bump the `go` directive in `go.mod`; it must stay `1.24`.
- Do not edit `go.mod` / `go.sum` without a reason. Allowed non-stdlib deps are already declared:
  `golang.org/x/net`, `golang.org/x/sys`, `golang.org/x/term`. If you believe another
  dependency is essential, write a stdlib fallback and say so in your report. CI runs
  `go mod tidy -diff`, so a change that leaves the files untidy fails.
- Check your work with: `gofmt -l cmd internal` (must print nothing), `go vet ./...`,
  `go test -race -count=1 ./<your packages>/...`. `scripts/check.sh` runs what CI runs
  (format, tidy, vet, build, race tests, cross-compiles).

## Layout

One line per package, from the first sentence of each package's doc comment
(`go list -f '{{.ImportPath}}: {{.Doc}}' ./...` prints them; update this list when a package is added).
`docs/ARCHITECTURE.md` has the system map and how the packages fit together.

```
cmd/sleipnir            the binary: every command, the chat loop and the RL subcommands (package main)

internal/core           provider-neutral vocabulary: messages, blocks, tools, usage, ids, hashing
internal/events         source of truth: append-only session event log plus a content-addressed blob store
internal/cost           economics the cache planner reasons with: per-model prices and each provider family's caching rules
internal/kv             the multi-layer prompt-cache engine: layers, renderer, breakpoint planner, drift guard, compaction
  kv/sim                deterministic cost simulator for prompt-cache policies (behind `sleipnir sim`)

internal/provider       the boundary between the harness and model APIs: Provider interface, errors, SSE
  provider/openaichat   adapter for OpenAI-style /chat/completions (OpenAI, Heimdall, OpenRouter, vLLM, SGLang)
  provider/anthropic    adapter for the Anthropic Messages API, and gateways that speak it
  provider/gateway      marketplace catalogue (Heimdall, OpenRouter): public model list, prices, capabilities
  provider/probe        measures how an endpoint really behaves (behind `sleipnir doctor`)
  provider/mock         deterministic, protocol-strict fake provider with an automatic prefix cache

internal/agent          one model-driven worker: render its layered prompt, call the provider, run tools, keep context healthy
internal/swarm          many agents over one repository: board, mail router, leases, governor, warm gate, roles, spawn
internal/tools          tool contract and shared helpers: output truncation with recall handles, cross-agent file state
  tools/fs              read, write, edit, apply_patch, glob, grep, ls
  tools/shell           bash (foreground or background), bash_output, bash_kill
  tools/web             web_fetch and web_search
  tools/recall          pages folded context back in
  tools/skilltool       loads a skill's full text on demand

internal/harden         makes the harness process opaque to the commands it runs: non-dumpable, environment erasure, provider keys held in memory
internal/perm           the permission engine: modes, rules, shell-syntax analysis, role profiles
internal/shellparse     small, defensive analyser for shell command lines
internal/checkpoint     pre-modification file snapshots, for diff and rewind
internal/hooks          user-defined commands at points of an agent's life, in Claude Code's hook format

internal/config         layered JSONC configuration, with trust gating of project files
internal/memory         instruction files (AGENTS.md, CLAUDE.md, SLEIPNIR.md) rendered as one deterministic block
internal/skills         Agent Skills: a listing in the shared layer, bodies loaded on demand
  skills/mdfile         shared markdown-with-frontmatter reader behind skills, commands and agent definitions
internal/commands       custom slash commands: markdown prompt templates
internal/agentdefs      markdown subagent definitions, turned into swarm roles

internal/session        assembles provider, tools, permissions, layers, event log and one agent or a swarm; CLI, RL and tests share it
internal/inspect        the cache inspector: a read-only model of a session log and an embedded web dashboard
  inspect/web           the dashboard's static assets (not a Go package)
internal/demo           the scripted team behind `sleipnir demo`, run against the mock provider

internal/workspace      isolates writers from each other and integrates their work; not wired into sessions yet
internal/gitx           the only gateway to the git binary: typed helpers over one hardened process runner
internal/mcp            Model Context Protocol client: servers from configuration, per-entry approval of project servers, tools frozen per session
  mcp/mcptest           small MCP server used to test the client

internal/rl             RL vocabulary: the harness as an environment
  rl/env                tasks, isolated rollouts, clean-checkout verification, evaluation, rollout server
  rl/env/taskgen        task generators: git history, mutations, composites
  rl/recall             memory tasks: read a fact early, state it exactly after long unrelated reading
  rl/traj               recorded run to canonical episode, with exact prompt replay
  rl/traj/trajtest      synthetic recorded runs for tests
  rl/reward             episode to reward components, hack flags, counterfactual cost, scalar rewards
  rl/adv                per-step advantages for group-relative policy optimisation
  rl/export             canonical episodes as trainer-ready JSON lines
  rl/redact             removes secrets and personal data from training data
  rl/harness            runs rollouts through the real assembly (internal/session) against a policy endpoint
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
- CI runs the suite on Linux (as an ordinary user, not root) and macOS (`.github/workflows/ci.yml`, also runnable by
  hand from the Actions tab); Windows is built, not tested. Things the first runs found, so that the next test does not
  repeat them:
  - a temp directory can sit behind a symlink (macOS: `/var` is `/private/var`): compare resolved paths
    (`filepath.EvalSymlinks`), and never assume that a path a tool prints is the one you gave it;
  - a file system can be case-insensitive (macOS default) and can refuse names that are not UTF-8: probe for it and skip or
    adapt, do not assume; a random number in a temp dir name can contain any short string, so match whole paths, not
    base names;
  - a test that runs as root here does not run as root there: a read-only directory needs a cleanup that makes it
    writable again, and "permission denied" can be the correct answer;
  - macOS has bash 3.2 (no `{01..03}`, no `|&`), BSD `ps` and `sed`, no `/proc` and no `127.0.0.2`; the process start
    time comes from `sysctl kern.proc.pid` there (`internal/workspace/proc_darwin.go`);
  - the Go command writes to `$HOME` (telemetry, build cache, GOPATH) the first time it runs: a test that snapshots a fake
    HOME must not count directory mtimes or those directories;
  - anything that waits for a background process needs a generous timeout on a loaded runner, and a test on a timer must
    accept every outcome the timer can legitimately produce;
  - an upper bound on time in a test is a hang guard (minutes, for a complexity bomb), not a timing: three suites at
    once under the race detector took sixteen seconds for what takes a tenth of one on a quiet machine. Whether two things
    ran at the same time is answered by something both wait on (a barrier: `mcptest`'s `barrier` mode, a start channel), never
    by a clock; a scaling test (`requireLinear`) repeats a failing measurement before it counts;
  - several git processes in one repository trip over each other in ways one process never sees (a worktree being
    created has an empty `commondir` for a moment); `internal/gitx` waits those out, and
    `TestConcurrentWorktreeCommandsDoNotFail` is what to extend when git shows a new one.
  A load test finds what a quiet machine hides: run three `go test -race -count=1 ./...` at once (a CI runner runs several
  packages at a time) and read every failure as a finding, not as noise.
  To see what the runners see before pushing, run the test binaries as an unprivileged user with a symlinked `TMPDIR`
  (`go test -c`, then `setpriv --reuid=65534 ...`); it catches most of the above.

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
