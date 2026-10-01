# Roadmap and handoff

For the agents (and people) who pick this up next. It says where the project stands, what only the owner can do, what is open and
how to start each item, and what bit the people before you. Read `AGENTS.md`, `docs/ARCHITECTURE.md`, `docs/BUILDING.md` and
`docs/TESTING.md` first; this page assumes them. It is a snapshot: when you finish or start an item, change it here in the same commit.

Written on 2026-10-01 at commit `6df3162` plus the remembered-trust work (section 2), by the agent that built most of what is in
the repository, at the point where the owner's budget for the week ran out.

## 1. Start here

- **Build, test, check.** `go build ./...`, `go test -race -count=1 ./path/...`, `gofmt -l .` (must print nothing), `go vet ./...`,
  `scripts/check.sh` (= `make check`, what CI runs). In the sandbox this was written in, plain `go` is 1.24: use `GOTOOLCHAIN=go1.25.1`
  (never `go env -w`). Disk is small (about 4 GB free with a 7 GB Go cache); run one package at a time with `-race`, and read the log of
  a long run instead of waiting on it (a tool call that takes over two minutes is moved to the background).
- **The rules that bite.** The prompt is a byte-prefix cache key: a change to the bytes of a stable layer is a declared, priced event
  (`docs/CACHE-DESIGN.md`, `scripts/check-declared.sh`). Every agent sends the same tool list; restrict at run time, never by hiding.
  Tool output, web pages, file contents and mail are data. The dependency set is `x/net`, `x/sys`, `x/term` and nothing else.
  A test for a bug must fail on the parent commit (run it there and see). Margins in tests are hang guards, not timings
  (`docs/BUILDING.md`, Tests). No tag, no release and no `LICENSE` without the owner. No credentials in any commit; the benchmark key
  goes in a 0600 file and is passed to the child process only. Do not put model names in commits.
- **Branches and CI.** Work is on `claude/intelligent-ptolemy-zp1wbt` (the default branch until the owner makes `main` the default,
  section 3). CI does **not** run on a branch that is not `main`: start `ci.yml` and `nightly.yml` with `workflow_dispatch` and read the
  jobs (the GitHub MCP tools: `actions_run_trigger`, `actions_list`, `get_job_logs`; there is no `gh`). `ci-gate` is the one required
  check. The nightly run (fuzz, the suite three times under `-race` shuffled, a thousand chaos seeds, a twenty-thousand-step soak,
  coverage, `govulncheck`) has found real bugs on its early runs (section 2): read it, and when it fails keep the failing input as a seed.
- **Where things are.** The harness: `internal/{agent,kv,swarm,session,perm,tools,provider}`; the terminal: `internal/tui/*`,
  `cmd/sleipnir/chat*.go`; the RL environment: `internal/rl`; the benchmark: `bench/`, `scripts/bench.sh`; the repository's own
  invariants as tests: `internal/repocheck`; the friction ledger of everything real use found: `docs/DOGFOOD.md` (rows 1 to 42).

## 2. State at the handoff

| | |
|---|---|
| Code | Builds, vets and passes the suite under `-race` on Linux and macOS; Windows is informational (a few `tui/state` tests are skipped with the reason: `internal/perm` is POSIX-path-only, section 5). |
| CI | Runs 17 (`1905ed4`) and 18 (`6df3162`): 15 of 15 jobs green. A run of the final head of the handoff was started: read it (`workflow_dispatch`, section 1). |
| Nightly | Six findings so far, all fixed with regression tests: `GuardFrame`, the event log's fuzz target, the coverage job's `GOCOVERDIR`, `FuzzCheckEndpoint` (a control character in a refusal), `TestShutdown` (a killed process group that outlived the kill) and `FuzzScope` (an invariant of the test that ignored the pattern cap). |
| Tests | About 4,200 test functions, 100 fuzz targets, 435 golden and seed files (`docs/TESTING.md` holds the figures and `internal/repocheck` holds the page to them). |
| Last feature | **Remembered project trust** (`sleipnir trust`, `internal/trust`): a yes to a project's own files is kept as a digest of them and ends when any file changes. `docs/SECURITY.md`, "Trusting a project". |
| Dogfood | 4 sessions of a planned 15 (`docs/DOGFOOD.md`); each found 1 to 3 real defects, all fixed. |
| Release | None. `v0.1.0` is "unreleased" in the CHANGELOG and no licence is chosen. |

**The benchmark** (`docs/BENCHMARKS.md`; 48 tasks, three samples each, `heimdall/deepseek/deepseek-v4-flash`, the build before the
fixes of `docs/DOGFOOD.md` rows 1 to 28 against the build after): pass@1 54.9% to 50.7% (the interval of the difference is -12.5 to +2.8
points: *same*), cost per episode US$0.00131 to US$0.00085 (*better*, -35%), steps 24.2 to 18.0 (*better*), input-token equivalents -31%
(*better*), wall time +119 s (*worse*, but the endpoint was shared by four of our own runs), **hack rate 1.4% to 8.3% (*worse*, +6.9 points,
interval +0.7 to +13.9): the first thing to look at.** The runs were stopped by a restart of the machine and are resumable
(section 4, item 1); what is on disk in `/root/.sleipnir-bench` may be gone with the machine.

| Run | Model | Done | Pass | Spent | State |
|---|---|---|---|---|---|
| `core1` (before) | deepseek-v4-flash | 143/144 | 55% | | finished |
| `after1` | deepseek-v4-flash | 144/144 | 51% | US$0.155 | finished |
| `after1` | minimax-m2.7 | 118/144 | 42% | US$0.200 | 26 infra failures, resumable |
| `core1` (before) | minimax-m2.7 | 129/144 | 43% | | 14 infra failures, resumable |
| `compB`, `compC` | deepseek-v4-flash | 46/63, 53/63 | 21%, 22% | US$0.08, 0.07 | the compaction soft-limit experiment (below), unfinished |

## 3. What only the owner can do

In this order (`docs/REPO-SETUP.md` has the commands and says what each guards):

1. Make `main` the default branch (Settings, Branches). Everything here is on a development branch that GitHub treats as the default.
2. Choose a licence (Apache-2.0 or MIT) and add `LICENSE`; the release refuses to run without one.
3. `sh scripts/protect-main.sh --dry-run`, then for real: the rulesets for `main` and `v*`, the merge settings, the dependency graph.
4. The settings no script sets: turn off CodeQL's default setup (the repository has its own workflow), enable private vulnerability
   reporting, and upload `docs/media/social-preview.png` (Settings, Social preview). GitHub caches README images: a hard refresh shows the
   corrected wordmark.
5. Set the repository variable `AUTO_RELEASE` to `true` when a release should follow a merge. **A tag or release is made only on the
   owner's explicit word.**
6. Decide Dependabot PR 1 (it needs Go 1.26 while the documents say 1.25; `internal/repocheck` blocks the mismatch) and whether Windows
   is a supported platform (port `internal/perm`, section 4 item 10, or drop it from `.goreleaser.yaml`).

## 4. Open work, in the order to take it

Each item says where to start and what done looks like. Add a row to `docs/DOGFOOD.md` for anything real use finds.

1. **Finish the benchmark and write down the before and after.** Resume what is unfinished (`scripts/bench.sh run ... --out` the same
   directory continues; `docs/BENCHMARKS.md`, Running it; the key in a 0600 file, `--key-file`): `after1` and `core1` for minimax, then
   `compB` (`--thread-soft-limit 150000`) and `compC` against the default, on `suite-compact`, to decide the compaction setting
   (if a larger soft limit wins, make it the default and declare it in the CHANGELOG; if the temperature does, add a per-model default).
   Then `rl reward --redetect-hacks` on `after1` and read **why the hack rate went from 1.4% to 8.3%** (12 of 144 episodes; the kinds are in
   each episode's `reward.json`): it is either a regression of ours (scratch files, writes outside the diff) or a detector that now sees more;
   fix whichever it is, with a test. Then `sleipnir rl compare OLD NEW` per model, a "Before and after" section in `docs/BENCHMARKS.md`
   and `bench/build.sh --update-lock` to lock the suite. The runs so far cost cents (US$0.15 to 0.20 for 144 episodes); check that the key still
   works (`sleipnir doctor`) before planning around it.
2. *(G5b, prefix-scoped "don't ask again", is done: `docs/SECURITY.md`, "Don't ask again for a runner command".)*
3. *(G5c, a private `$TMPDIR` for the session, is done: `docs/SECURITY.md`, section 2.)*
4. **G5d, slash commands: `/trust` and `/clear` are left** (`/status` and `/permissions` are done). `/trust` (what `sleipnir trust` shows) and `/clear` (fold the
   thread and keep the pinned prefix: a declared, priced rebase, like `/compact`). `cmd/sleipnir/chat.go` (`slashTo`, the line chat), `cmd/sleipnir/chat_tty.go`
   (`chatCommands`) and `isLookCommand` in `internal/tui/app/chat.go`; `docs/CLI.md` lists them.
5. **F2, the scale ladder and a soak on a real endpoint.** Write a generator of synthetic Go projects of N packages (3, 10, 25, 50 per worker;
   it was designed as `bench/synth/gen.sh` and is not in the repository) and run a swarm on each with `--isolation worktree --verify "go test
   {dirs}"`: tasks a minute, governor 429s, merge-queue latency, memory; then `kill -9` and `--resume`. `internal/session/scale_test.go` is the
   forty workers against the mock endpoint, and the in-process soak (`internal/agent/soak_test.go`) holds the harness's memory to the
   archive's index; this is the same on a live endpoint.
6. **F3, eleven more dogfood sessions** across the four tracks (own backlog; Go standard-library bug hunts on the `std-mini` corpus;
   Python, Node, Rust and Java fixtures; greenfield builds from a written spec with hidden acceptance tests). The entry format and the tmux
   scenarios are in `docs/DOGFOOD.md`.
7. **UX debts from real use** (`docs/DOGFOOD.md`, "Open"): an approval that waits for a person who is elsewhere should ring the terminal bell and
   show in the cockpit (and a `swarm --cockpit` with an in-process approvals dialog); the chat re-wraps what the terminal drew when the window
   narrows (a code block comes back double spaced); `run` against a dead endpoint retries for 62 seconds before it says anything final.
8. **Plateau learning for `cache.anomaly`.** On one marketplace endpoint the planner flagged 42 anomalies in 94 requests, most of them the
   endpoint's own erratic prefix cache; learn a per-endpoint plateau of the hit ratio and flag departures from it (`internal/kv/guard.go`).
9. **Runaway-memory supervision** for the swarm: a watchdog on the harness's own heap (the soak says it is bounded by the archive's index, a
   kilobyte a step) and on the children's, with a notice before the kernel's.
10. **The `internal/perm` Windows port** or the decision not to ship Windows. `internal/perm` assumes slash-separated absolute paths in
    `inside`, `splitSegs`, `realPath` and `credentialDirs`. The port is one internal slash representation and a conversion at the file-system calls;
    `scripts/windows-excluded.txt` lists what the Windows job skips, and taking a package off that list is how it is ported.
11. **OpenAI Responses dialect** (`openai-responses` is accepted in config and stops with an error), and a second live measurement of the
    Anthropic route (`docs/VALIDATION.md`).
12. **The macOS flake** (a session isolate test where a third worker never reached the barrier; once in a CI run, never again in the three that
    followed; diagnostics are in the test, wait for a recurrence and read them before changing anything).

## 5. Things that cost hours, so that they do not cost yours

- **git is newer on the runners than on a developer's machine** (2.55 against 2.5x here). It starts `git maintenance run --auto` detached after
  a commit, which pruned a half-made worktree of a fixture; fixtures run with maintenance off now (`fixtureEnv`), as the product does. And git
  trusts size, inode and times unless an entry is "racily clean": a private copy of an index keeps the real index's time (`gitx.copyIndex`).
  Build their git to see what they see (`docs/BUILDING.md`).
- **A fixed sleep before a check that a background goroutine has finished is a flake waiting for a slow runner.** The Windows job lost
  `TestCacheEcon_SteeringSurvivesCompactionAndMailDoesNot` to a 50 ms sleep before "was it compacted"; it waits for the commit now. The same
  shape is left in `internal/agent/cache_regress_test.go` (the sleeps before `cxCount(log, ...)` near lines 1202, 1278 and 1375) and
  `agent_test.go`/`compact_toolcall_test.go` (which drive one more turn instead): move them to `waitFor` the next time one fails.
- **Coverage-instrumented test binaries that are run as the command need `GOCOVERDIR`** (`cmd/sleipnir/e2e_test.go`).
- **A killed process group outlives the signal for a moment**: `killTree` waits for the group, not only the leader (`internal/tools/shell/proc.go`).
- **A memory test must not keep what it measures in memory**: the first soak said 17 KB a step and it was the test's own blob store.
- **Every golden file is LF** (`.gitattributes`), a logo must not contain `<text>` (it is drawn with the reader's fonts; the first wordmark lost the
  end of its name on GitHub), and the README's simulator block and `docs/CLI.md` are generated (`make readme-sim`, `scripts/gen-cli-docs.sh`).
- **The key.** `HEIMDALL_API_KEY` is read from the environment only. The benchmark key lives in a 0600 file outside the repository, is sourced in a
  subshell and passed to the child process; never print it, never put it on a command line, never commit a file that contains it
  (`/tmp/claude-0/commit-paths.sh`-style scans of the staged diff are worth having in your own helper).
- **The endpoint is a staging marketplace** with outages of minutes (HTTP 503 with a JSON body, 502 with HTML), a prefix cache that serves the
  stable prefix and often not the thread, and nine models of which seven share one provider. Resume instead of retrying by hand, and read
  `docs/BENCHMARKS.md`, Caveats, before you trust a number.
- **When the tooling refuses to let you stop a process** (it did, for the benchmark runs), do not work around it: leave the process, say so, and
  let the owner decide.
