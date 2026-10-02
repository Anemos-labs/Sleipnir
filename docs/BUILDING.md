# Building Sleipnir: conventions for contributors

Sleipnir is a Go (1.25, stdlib-first) coding-agent harness whose defining feature is a
multi-layer prompt-cache engine shared by swarms of agents. This page is the short
version of "how code here is written". Read it before touching a package.

## Toolchain

- Go 1.25 or newer: `go.mod` says `go 1.25.0`, the oldest Go the project builds with, and the `go.mod` leg of the CI matrix proves
  it still does. It became 1.25 with `golang.org/x/net` v0.58 (the HTML parser that reads fetched pages had seven advisories, fixed
  from v0.55, which needs 1.25); Go 1.24 is out of support. When the `go` on your path is older the go command fetches the toolchain
  the directive asks for (`GOTOOLCHAIN=auto`, its default). In an offline container install it and pin with `GOTOOLCHAIN=local`
  (`GOTOOLCHAIN=go1.25.1` names one that is installed). Do not let tooling raise the directive without a reason of this kind.
  Do not set the toolchain in a machine-wide `go env -w` where benchmarks run (`docs/BENCHMARKS.md`): a rollout's own `go test`
  must use the Go its tasks were built with.
- Do not edit `go.mod` / `go.sum` without a reason. Allowed non-stdlib deps are already declared:
  `golang.org/x/net`, `golang.org/x/sys`, `golang.org/x/term`. If you believe another
  dependency is essential, write a stdlib fallback and say so in your report. CI runs
  `go mod tidy -diff`, so a change that leaves the files untidy fails.
- Check your work with: `gofmt -l cmd internal` (must print nothing), `go vet ./...`,
  `go test -race -count=1 ./<your packages>/...`. `scripts/check.sh` (`make check`) runs what CI runs (format, tidy,
  `go mod verify`, the dependency and action-pin checks, generated docs, vet, build, race tests, cross-compiles);
  `docs/REPO-SETUP.md` lists everything CI and GitHub check and what each check is linked to.

## Layout

One line per package, from the first sentence of each package's doc comment
(`go list -f '{{.ImportPath}}: {{.Doc}}' ./...` prints them; update this list when a package is added).
`docs/ARCHITECTURE.md` has the system map and how the packages fit together.

```
cmd/sleipnir            the binary: every command, the chat loop and the RL subcommands (package main); the hidden `chat-record` records the chat session of docs/media

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
  provider/providertest scripted-stream harness and the contract every adapter owes its caller (used by the adapters' tests)

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
internal/demo           the scripted teams behind `sleipnir demo` (a handbook in a second; a shop built in git worktrees in twenty), run against the mock provider; and the scripted model and project of the chat recording
internal/ptytest        runs a command on a pseudo-terminal, so that a test can type at it, press Ctrl-C and wait for what it prints
internal/tui            the terminal interface (docs/UX.md): term, cell, render, vt (an emulator the tests read), widget, state, input (the editor), app (the programs: chat, watch, replay), svg

internal/tui            the terminal interface (docs/UX.md): everything a screen is made of, each package small and pure where it can be
  tui/cell              styled spans and lines, display widths; no escape codes
  tui/term              what the terminal can do (colour, size, Unicode, animation allowed), raw mode, resize
  tui/vt                a minimal terminal emulator: the tests read the screen a renderer made, and pty tests read a real command's
  tui/render            the renderers: inline (scrollback plus a live region) and full screen (alternate screen, cell diff)
  tui/input             key decoding, the line editor (history, completion, paste chips), no I/O
  tui/widget            widgets as pure functions from data, a width and a frame to lines: markdown, diff, dialog, table, the prompt stack, sparkline, TTL clock, fold, horse, cockpit
    widget/showtest     data and helpers of the signature widgets' tests
    widget/widgettest   shared checks of the text widgets' tests (width, control characters, hostile text, goldens)
  tui/state             the session as a pure function of its event log: one reducer, read live (Follow), at any speed (Replay) or all at once (Fold)
    state/statetest     hand-made event sequences and the recorded demo log, for tests
  tui/svg               the headless recorder: screens to an animated SVG (CSS only), deterministic
  tui/app               the programs (chat, watch, replay) and the recordings of docs/media (the cockpit's from a log, the chat's from a transcript of a session played on a virtual clock): views as functions of a snapshot, the loop with its keys, sizes and ticks as channels

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

internal/testutil       test helpers shared by suites: the goroutine-leak check (`CheckLeaks`, `VerifyNone`); no product code imports it
internal/repocheck      the repository's own invariants as tests (links resolve, actions are pinned, the required check is a job, CODEOWNERS and goreleaser agree with the tree); no product code
```

## Finding what has been forgotten

`scripts/heatmap.sh` colours every file by when it last changed (rank among all files, red the coldest tenth, green the hottest): a table by directory in
the terminal (`--since-days N` keeps the directories untouched for N days, `--files N` lists the coldest files), or `--html FILE` for a page with one square per
file, its area the file's lines. Run it before choosing what to polish: a user-facing feature that is red has not been looked at since it was written, so
**use it** (run the command, read what it prints) before trusting that its tests mean it works. A mass reformat makes everything hot; read the files.

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
- The suites of the packages that start goroutines (`agent`, `swarm`, `mcp`, `session`, `workspace`) end with
  `testutil.CheckLeaks` in `TestMain`: a goroutine of this module still running ten seconds after the last test is a
  failure, and its stack and its creator are printed. A test that starts a reader on a pipe or a server closes it;
  `testutil.VerifyNone(t)` (first line of a test) holds one test to the same.
- Prefer testing observable behaviour over internals.
- What a command does with Ctrl-C, its streams and its exit status is decided outside its functions, so it is tested outside
  them. `cmd/sleipnir/e2e_test.go` runs the command in a child process (`TestMain` makes the test binary run `main()`), in a
  private home, with a scripted mock model that a test can hold in the middle of a turn; `internal/ptytest` runs it on a
  pseudo-terminal, where a test types, presses Ctrl-C (the terminal turns the byte into SIGINT, as it does for a person) and
  waits for the output, with the transcript in every failure. `ptytest.WaitInputRead` is the barrier between "I typed a line"
  and "the program has it". A bug in how a program and its terminal fit together (chat's Ctrl-C ended the session) does not
  show in a test that calls a function or sends a signal itself. A program that draws (`chat`, `watch`, `replay`) is read the way a
  person reads it: `internal/tui/vt` is given what it wrote and the test waits for what the screen shows
  (`cmd/sleipnir/e2e_chat_test.go`, `e2e_watch_test.go`); `internal/tui/app` runs the programs on a scripted terminal and session,
  with the keys, the ticks and the size sent by the test, so that nothing waits for a clock.
- CI runs the suite with `-race` on Linux (amd64 and arm64, as an ordinary user, not root) and macOS with the Go of `go.mod`,
  on Linux with the newest stable Go too (`.github/workflows/ci.yml`, also runnable by hand from the Actions tab); Windows
  runs without `-race` as an informational job that blocks nothing (every package but the ones `scripts/windows-excluded.txt` lists, each
  with the POSIX assumption its tests make: taking one off the list is how it is ported), and every shipped platform is built and vetted. Things
  the first runs found, so that the next test does not repeat them:
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
    `TestConcurrentWorktreeCommandsDoNotFail` is what to extend when git shows a new one;
  - a runner's git is newer than a developer's (2.55 on the first run), and what it changed was in fixtures: it starts
    `git maintenance run --auto` detached after a commit, which removes a half-made worktree a few milliseconds later, so a fixture runs
    git without background work (`fixtureEnv`, as the product's `hardenedConfig` does). To see what the runners see, build their git
    (the tarball on kernel.org; `make prefix=$HOME/git-2.55 NO_GETTEXT=1 NO_TCLTK=1 NO_PERL=1 NO_PYTHON=1 NO_EXPAT=1 NO_CURL=1
    NO_OPENSSL=1 install`) and put its `bin` first on `PATH`. git also decides that a file is unchanged from its stat data unless
    the entry is not older than its index file: whatever copies an index keeps its time (`gitx.copyIndex`), and a test that needs
    the situation sets the times with `os.Chtimes` instead of racing a clock;
  - a test that measures memory must not keep what it measures in memory: the first soak test (`internal/agent/soak_test.go`) said that a
    run leaks 17 KB a step, and what leaked was the test's blob store and event log, which a session keeps on disk. Put them there (or
    drop them), read the live heap after `runtime.GC()` at fractions of the run rather than at its end, bound the growth per step, and
    break the code on purpose (keep each result in a slice) to see the test fail before trusting that it can;
  - a `select` between a context and a channel that the context closes (`ReadKeys`, `mergeInterrupts`) chooses at random when both are
    ready, so a loop that treats the channel's end as "the user quit" reports a signal as a quit now and then: ask `ctx.Err()` first. A
    test makes both ready before the loop starts and runs it a few hundred times (`internal/tui/app/signal_race_test.go`);
  - a status that a worker shows ("idle") is set before the harness has finished with the worker's last run: a test that sends the
    next message the moment it sees it meets the harness in that moment. Make the moment the test's own (a hook on the event the run
    ends with, a model reply that is held) rather than hoping to hit it, and wait for an absence only with a generous quiet time
    (`internal/swarm/wakebound_test.go`);
  - macOS takes a terminal from every holder when the process that leads its session exits, so a question put to the terminal after
    that has no answer (`internal/ptytest`, `WaitInputRead`); Windows checks text files out with CRLF unless `.gitattributes` says
    not (every golden test failed at its first line, with a diff that looked identical).
  A load test finds what a quiet machine hides: run three `go test -race -count=1 ./...` at once (a CI runner runs several
  packages at a time) and read every failure as a finding, not as noise.
  To see what the runners see before pushing, run the test binaries as an unprivileged user with a symlinked `TMPDIR`
  (`go test -c`, then `setpriv --reuid=65534 ...`); it catches most of the above. A change to a workflow is checked the way the
  `workflows` job checks it, with `shellcheck` on the path, since `actionlint` runs it over every `run:` script and says nothing
  about them without it: `pip install shellcheck-py`, then `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`.

## Recordings

**Real recordings first.** `scripts/record-real.sh` records a real chat: a temporary project with a failing test, a real model, tmux typing the keys, `script(1)` writing
what the terminal showed with its timing, and `sleipnir term-svg` playing that into the repository's own terminal emulator (`internal/tui/vt`) and drawing the frames with
`internal/tui/svg`. It costs a few cents and about a minute, so redo it whenever the interface changes enough to show: `docs/media/real-chat.svg` is the README's picture.
The scripted recordings below are made from a mock model; they show the harness around a model and are labelled so in `docs/GALLERY.md`.

**Look at every picture you make.** A recording is only done once its frames have been seen in a browser, at the moments that matter (a menu is
up, a row is highlighted, a dialog asks): `node scripts/svg2png.mjs docs/media/real-first-run.svg /tmp/f.png --at 2` writes the frame at second 2, and
the image is then opened and read. For a screen that is not in a recording yet, `scripts/look.sh OUT.png [--key NAME | --type TEXT | --pause SECONDS]... -- sleipnir ...`
runs the command in a real terminal under tmux, presses the keys, and writes what the terminal showed as a PNG (`--home DIR` is the home it runs in, empty by default;
the key of a provider comes from the environment). Text that is checked only as text misses what a person sees (a highlighted row drawn white on white passed every
test and shipped).

The animated SVGs in the README (`docs/media/*.svg`, with PNG stills) are the terminal interface's own screens, drawn by the code from a
recorded session and by nothing else: no one types, edits or photographs them. `docs/media/gallery.json` is the one manifest (which
screen, how big, which stretch, which stills); `sleipnir replay --gallery`, `scripts/record-demo.sh` and the tests all read it, so a
recording cannot be made one way and checked another. There are two kinds of source, and both are committed:

- the swarm, cache and fold recordings are the cockpit played over the event log of the shop demo (`docs/media/showcase/events.jsonl`);
- the chat recording is the chat program itself, played from a transcript (`docs/media/chat/transcript.jsonl`) of a session of the real
  harness against the mock endpoint, with a scripted model and a scripted person (`internal/demo/chat.go`, `cmd/sleipnir/chat_record.go`).
  A log of events cannot say what a person typed, so the transcript also holds the keys. The program is run on a virtual clock in the
  `vt` emulator and given one record at a time, drawing once for each (`internal/tui/app/chat_play.go`), so the same transcript is the
  same bytes on any machine at any load. The time in the transcript is designed (`cmd/sleipnir/chat_pace.go`), not measured, for the
  reason in its header: a recording once took three and a half minutes because the machine was busy. Everything else is the session's:
  the order, the tools (the real `go test`), the permission question and the keys that answer it, the cache planner and the accounting.

What to run:

```sh
sh scripts/record-demo.sh                # draw docs/media/*.svg from the committed sources, and the PNG stills (needs node and Playwright's Chromium)
sh scripts/record-demo.sh --check        # change nothing; exit 1 if a committed SVG is not what the code draws now (CI runs it, nightly)
sh scripts/record-demo.sh --new-session  # run the shop demo again (20 s, no key) and keep its log, then draw
sh scripts/record-demo.sh --new-chat     # record the chat session again (half a minute, no key, needs the go command), then draw
```

A change to a screen or a widget makes `--check` and `go test ./internal/tui/app` fail until the recordings are drawn again: run the
script, read the diff, and look at the PNGs (an SVG diff says little; a picture says whether a glyph is clipped, a line wraps or a colour
vanishes on the page) before committing the SVGs and PNGs together. The tests also hold the sources to the story the README tells
(the swarm's bounced merge, the chat's failing and then passing tests, its one question, its one compaction and its one cache break), so
a recording made again that no longer tells it is found there, and `chat-record` refuses to write a transcript that does not. A new chat
recording is a new session: its real timings (the `go` command's own, and what the tools reported of it) differ from the committed
one's, which is why the transcript is committed and the check is on the picture drawn from it. The README says what is real in a recording and what is scripted, and so must a new one.

## Performance

What runs on every request is measured, in two ways that do not depend on each other.

- **Allocations are tests.** The number of allocations of a call does not depend on the machine, so it can fail a build:
  `allocs_gate_test.go` (`//go:build !race`, the race detector allocates for itself) in `internal/kv` (`Render` on every route and hot
  mode, and how it grows with the thread), `internal/core` (`Canonical`), `internal/swarm` (reading the board costs nothing, a
  claim, the governor), `internal/perm` (`Check`), `internal/events` (`Emit`) and `internal/provider` (the SSE reader) holds each
  call to what it is, with a fifth to spare for another version of Go. A change that makes `Render`
  allocate a block more per turn fails there, and one that makes it quadratic in the thread fails the scaling gate. Raising a limit is
  a decision: say in the commit why the call now needs more.
- **Time is benchmarks.** `Benchmark*` next to the code (`kv` Render and the keys, `core` Canonical, `events` Emit and Scan, `provider`
  the SSE reader, `perm` Check, `swarm` the board and the governor, `tools/fs` grep, `session` BuildRecon, and the terminal UI's own).
  `scripts/perf.sh run OUT.txt` runs them (median of `COUNT` repeats), `scripts/perf.sh compare OLD.txt NEW.txt` says what got slower
  (`bench/tools/benchcmp`: a slowdown is reported only when the two ranges do not overlap, since a shared machine is noisy). Run both
  runs in the same quiet minute, pinned to the same cpus (`CPUS=2,3`). Nothing fails a build on a wall-clock number; a run on a busy
  machine is a hint, and the tests above are the guard.

## Changing prompt bytes

The stable prompt bytes are cache keys, so golden files pin them, one test per thing (a change fails one named test):
`internal/core/testdata/golden/canonical_*.txt` (`TestCanonicalGolden`: canonical JSON of blocks, messages, tools, the wire hash),
`internal/kv/testdata/golden/render/` (`TestGoldenRenderedStack`: layer hashes and the whole `kv.Render` output for every hot mode
and route), `internal/kv/testdata/golden/tools/` (`TestGoldenToolSpecs`: the tool list every agent sends) and
`internal/agent/testdata/golden/constitution_*.txt` (`TestGoldenConstitution`). An intended change, in one commit:
1. run that test with `-update` (`go test ./internal/kv -run Golden -update`) and read the `git diff` of `testdata/`: it is what
   you are pricing. Never update to turn a test green unread;
2. if what `kv.Render` sends the model changed, bump `kv.RendererVersion` and add the row `TestGoldenRenderedStack/renderer_version`
   prints to `renderHistory` (`TestRendererVersion` fails until both agree);
3. add a CHANGELOG entry priced with `sleipnir sim`: a changed byte re-writes the prefix after it for every agent (CACHE-DESIGN §1).

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
