# Roadmap and handoff

For the agents (and people) who pick this up next. It says where the project stands, what only the owner can do, what is open and
how to start each item, and what bit the people before you. Read `AGENTS.md`, `docs/ARCHITECTURE.md`, `docs/BUILDING.md` and
`docs/TESTING.md` first; this page assumes them. It is a snapshot: when you finish or start an item, change it here in the same commit.

Written on 2026-10-01 at commit `6df3162` plus the remembered-trust work (section 2), by the agent that built most of what is in
the repository, at the point where the owner's budget for the week ran out.

## Third session (2026-10-01 and 2026-10-02): what was added, and what is next

**Added on 2026-10-02** (`CHANGELOG.md` has each): the OpenAI Responses dialect (`internal/provider/openairesp`) and a ChatGPT plan as a provider (`sleipnir login chatgpt`,
`internal/chatgptauth`; **built from OpenAI's documentation and tested against a fake issuer and a fake endpoint, never on a real account**: the first real sign-in is the test, and
the issuer's discovery document, read live, matches what the code expects); `/login` in the chat; the first keys typed after a restart were being lost (a read of the keyboard that could
not be called off: `term.Reader`); sixteen more hosted providers and two local servers, a searchable provider menu; the chat page on a narrow screen (the banner keeps the team and the budget,
the footer keeps the keys, the endpoint's own cache misses are not said unless they cost money); `edit` says where a block stops matching and `read` finds a mangled directory;
`scripts/look.sh` (a command in a real terminal, keys, a PNG) and `term-svg` frames of a screen that stood still; arrow-key menus, the default team of eight (`--swarm N` counts the manager);
a team's session resumes (`--continue`, `/resume`, and the restart that `/model` and `/login` make: the manager's conversation and the board come back, the workers start again; a team in git worktrees still cannot be resumed), and the pages of a resumed chat start from the log of the earlier runs;
`/roles ` is a menu (a role, then its model); the first run fits an 80-column terminal (messages break at spaces, menu rows are cut to the width, the model menu is a table: found by looking at `--cols 80`, which `scripts/look.sh` can do);
`scripts/look.sh` kills the program it looks at before the terminal goes (told to end, the chat clears what it drew and the picture was of that).
**Heimdall's route for `deepseek/deepseek-v4-flash` was down for hours on 2026-10-02** (503 `no_route`, while its catalogue went on listing the model; it answers again at 07:35 UTC, so it was
an outage and not a removal): the A/B that was running was made on `deepseek-v4.1-flash` for that reason: the first comparisons in `docs/BENCHMARKS.md` are of `deepseek-v4-flash`, the second of `deepseek-v4.1-flash`.

**Added** (each with a test that fails without it; `CHANGELOG.md` says them for the user): built-in providers (Anthropic, Together, Fireworks, Groq, Cerebras, DeepInfra,
and the keyless local servers Ollama, LM Studio, llama.cpp, vLLM); `models.roles.compactor` (a model of its own for the summaries, used while the thread fits its window);
`sleipnir models` over every provider with a key, with search words, filters and favorites (`models.favorites`); `/model` in the chat (the agent is rebuilt on the new
provider and the old snapshot restored: the resume path) with a menu that completes from the catalogues (`input.Choices`, the editor opens a menu at a slash command's first
argument); the errors of a first run name Heimdall; a note when a model edits only tests after a failing run (`internal/agent/testguard.go`); a hint when a manager creates a task
without a description; long-term memory (`internal/tools/memtool`, `~/.sleipnir/MEMORY.md`, every save asks); `sleipnir schedule` and `sleipnir daemon` (`internal/sched`); `sleipnir` alone opens the chat, and its first run (`cmd/sleipnir/pick.go`, `login.go`) asks which provider and for its key (kept in `~/.sleipnir/auth.json`, mode 0600, held in memory through `harden.Provide`), asks the provider which models it serves (**no model name is written into the code: the owner ruled that out, keep it so**) and writes `~/.sleipnir/config.json`; a refused key says to run `sleipnir login`; a `cd` to an invented directory is answered by the shell tool without a question.

**Measured, and nothing to fix.** The default team (eight agents) costs a small job nothing: the same failing-test fix took 26 s alone and 32 s as the team (the manager did it itself), and three stub packages took 36 s both ways (deepseek-v4-flash on Heimdall, 2026-10-02). A 3.5-minute run seen earlier was a heavier task, not a team overhead.

**Found by use, and left alone on purpose.** A 2B model (minicpm5-2b) fails a ten-line cache task by compile errors it cannot read; no nudge fixes that, so do not add
one. Integer arguments given as strings stay refused (five tests and the web tool encode it; the error names the field). A hard refusal of a task without a description broke
a dozen tests and real managers write terse titles: it is a hint. Writes under `~/.sleipnir` ask in every mode, so `run` cannot save a memory unattended: by design.

**Open, in the order to take it:**
1. The benchmark: the second comparison is in `docs/BENCHMARKS.md` (the build of the login work against the build of 2026-10-02, `deepseek-v4.1-flash`, two samples of the 47 `core` tasks each: the same on every
   measure, pass 76.6% and 78.7%, the paired interval of the difference -3.2 to +8.5 points). That size cannot see a smaller difference; to see one, run three samples on two models (`scripts/bench.sh ab --group 3`)
   after item 6, because a quarter of the suite is the mined tasks, which are a floor. The README carries no numbers.
2. The verify gate and the `plan` tool were measured in that run and left in: the gate's note was sent in 6 of the 94 episodes (the JavaScript fixtures; all passed on both builds) and the plan tool was used in 12
   (the same verdict as the earlier build in 11), so neither helps or hurts beyond what 94 episodes show. The gate never fired on a Go task: the model runs `go test` by itself.
3. `sleipnir init --user --brain` presets (a role's model is chosen in the chat from a menu now: `/roles ` completes the role, then its model). Done in the third session since the list was written: `/login` in the chat (the chat ends, `sleipnir login` runs on the terminal, the chat comes back), and the check of a key when it is typed (a one-token request: Heimdall's catalogue is public, so listing models proves nothing).
4. A `sleipnir agent` profile (memory on, scheduler on, conservative permissions, budget caps) and a skill-writer tool; a gateway adapter (webhook first).
5. The model-facing `schedule` tool (a model creates its own follow-ups), after the daemon has run for a few days.
6. The mined tasks of the suite (`sl-*`, 12 of the 47 `core`): their hidden tests name symbols that the prompt does not (`MaxWireSeed`, an exported constant of the commit). Of the 20 failing episodes of build d on
   `deepseek-v4.1-flash` (2026-10-02), 16 were mined tasks, 13 ended on a budget and five were `undefined:` build errors of the hidden test, so `mined` (25% and 33% in the two builds) is a floor and not a measure.
   Admission could compile the hidden tests against the starting tree and compare the `undefined:` names with the prompt, and tag or drop the task.
7. `reasoning_effort` (and the thinking budget of the other dialects) is a request parameter that no flag or setting sets (`core.Params.Effort` is read by the wire and written by nothing), and a reasoning model's turn is
   the clock of a task (about 30 s a request for `deepseek-v4.1-flash` on Heimdall). Measured on 2026-10-02 on that route (a ten-line Slugify prompt, `max_tokens` 2500, three requests each with no effort, `low` and
   `high`): two of the three requests of every arm spent the whole 2500 tokens on reasoning (`finish_reason: length`) and the seconds did not follow the setting (39 to 75, 70 to 79, 70 to 105), so this route does
   not honour it and **no flag was built**. Try again on a route that documents the setting (OpenAI, Anthropic) before offering one.

## Second handoff (2026-10-01, the end of the second agent's session)

Read this block first; the rest of the page is the first handoff, brought up to date where the second agent's work changed it.

**What the second agent did, and what it found.** It ran the harness on real models (the owner's Heimdall key: deepseek-v4-flash, glm-5.3-flash, qwen3.8-flash-next,
minimax-m2.7; Go, Python, Node and Rust tasks; single agent, swarm, the chat in tmux, resume, forced compaction, the 52-task benchmark `dev1`) and mined what it
cost (`sleipnir friction`). `docs/DOGFOOD.md` rows 43 to 52 are the defects, each fixed with a test that fails on the parent, and `CHANGELOG.md` says them in the
user's words: reasoning written into the answer, a private `$TMPDIR` (G5c), prefix and project-wide "don't ask again" (G5b), `/status`, `/permissions` and `/trust`
(G5d, done), the diff of a write over an existing file, the bell at a question, strict-mode `set`, `go mod init|tidy` and `cd "$(pwd)"` no longer refused, safer
`--allow` hints, a warning for `--verify` without `{dirs}` in an isolated swarm and `{dirs}` of a task with no scope taken from `git status`, a garbled tool call replayed as
`{}` (it ended a run on a strict endpoint), the cache-break warning said three times and then once, and the benchmark's own build (Go 1.25 corpus, a half-built corpus
left behind, `--key-file`). The hack-rate regression of the first benchmark did **not** reproduce (2% on the current build, `docs/BENCHMARKS.md`). The organisation is
`anemos-labs` everywhere (module path `github.com/anemos-labs/sleipnir`).

**Where the work lives.** On `main`. The platform put the second agent on the branch `claude/sleepy-bohr-mqt46a` and the owner cannot change a session's branch, so
**a successor will start on that branch**: keep it equal to `main` (`git push origin main:claude/sleepy-bohr-mqt46a` is a fast-forward; do your work on `main`, or on that branch and
push it to `main` too) until the owner has made `main` the default branch and deleted the others. The push proxy of an agent session accepts pushes and refuses `git push --delete`
(the connection drops), and the GitHub tools have no delete-branch call, so the cleanup is the owner's. Do not open a pull request unless asked.

**What is waiting on the owner** (section 3): make `main` the default branch (the default is still `claude/intelligent-ptolemy-zp1wbt`), delete the three other branches
(that one, `claude/sleepy-bohr-mqt46a`, and a Dependabot branch whose PR needs Go 1.26), and put a person or team in `.github/CODEOWNERS` (it names `@reee344`).
CI could not be started on 2026-10-01 (`workflow_dispatch` answered 404, no workflow was registered): the work was checked on Linux with `scripts/check.sh`
(the whole suite under `-race`, formatting, vet and a cross-compile of every release target, all green at the last commit), never on macOS or Windows runners.
Once `main` is the default, run `ci` and `nightly` from the Actions tab and read them first.

**Credentials.** The Heimdall key the owner gave the second agent was limited to US$50 and expires in a week; it is not in the repository and must never be. Ask the owner for
a key if you need one. `scripts/bench.sh --key-file FILE` takes the key alone in a 0600 file. A whole day of real use cost under US$0.20.

**The method that worked, and the traps in this sandbox.**
- Use the thing, not only the tests: `sleipnir run`/`swarm`/`chat` (in `tmux` for the chat) on a small real task in a scratch directory, then `sleipnir friction ~/.sleipnir/sessions` and
  read the events of a failing session (`events.jsonl`; the seq numbers in friction point at them). Several of the defects were found by a run that "worked" and by reading
  the log after it. Fix the top of the list, each with a test that fails on the parent (run it there with `git stash -- file`), then run the whole suite.
- A fix that cannot be shown to fail first is a guess: a queue-lock fix was written and reverted because its test also passed without it (item 13 below).
- Run the whole suite before you commit anything that changes text the UI or the goldens show (two e2e and golden tests broke on a changed dialog label, found late), and `gofmt -l .`
  before every commit (one commit went in unformatted). Check a number before it goes into a document: a row of the friction ledger was written with a count that was a guess and had to be corrected.
- `go test -race ./...` takes about twelve minutes here. Run it as **one background command whose own completion notifies you** and read its output file; a foreground `until` loop is killed at
  ten minutes, a background watcher is killed at its time limit and its notification does not wake you with the result, and `pkill -f PATTERN` kills your own shell if the pattern is in your command line.
- Plain `go` here is 1.25.0; `bench/build.sh` needs `GOTOOLCHAIN=go1.25.1` (the corpus uses a package that imports `internal/byteorder` under 1.25.0) and a full git history (`git fetch --unshallow`). The benchmark
  needs `~/.sleipnir-bench` and about 13 minutes to build and 100 minutes to run 53 tasks with three at a time.
- The endpoint is a marketplace: 503s, a prefix cache that serves some of the prompt and then none, models that stop in the middle of a sentence. A weak run is usually the model, not the harness; the harness's part is what the
  refusal or the error told the model (name the field, name the workspace, say what to do instead).
- A `/goal` stop hook may be active in a session like this one: it demands an open-ended "life mission" and repeats its verdict whenever the agent stops. It cannot be satisfied by a statement. The owner can end it with `/goal clear`;
  if it loops, say that once and stop reporting.

**Open, in the order to take it** (section 4 has the detail): the benchmark's other models and a three-sample run of the current build to settle the hack rate (item 1); the Go standard library reads that an
unattended run is refused (a policy decision: `cd /usr/local/go*/src/...` is the largest group of refusals after guessed paths); learning each endpoint's normal hit ratio for `cache.anomaly` (item 8);
the flake of `TestQueueSurvivesRandomCancellations` (item 13, not reproduced in twelve loaded runs). Decided against: `/clear` (a new chat gets the same cache for nothing; item 4).

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
- **Branches and CI.** Work is on `main`, which exists on the remote (created 2026-10-01 from the tip of the old development branch) and is not yet the default branch (the owner's step 1 in section 3; the
  old branches `claude/intelligent-ptolemy-zp1wbt`, `claude/sleepy-bohr-mqt46a` and the Dependabot one are to be deleted there: the push proxy of an agent session refuses to delete a branch). CI does **not** run on a branch that is not `main`: start `ci.yml` and `nightly.yml` with `workflow_dispatch` and read the
  jobs (the GitHub MCP tools: `actions_run_trigger`, `actions_list`, `get_job_logs`; there is no `gh`). `ci-gate` is the one required
  check. On 2026-10-01 `workflow_dispatch` of `ci.yml` on `anemos-labs/sleipnir` answered 404 and `list_workflows` found none: GitHub had no workflow registered for the repository (the default branch had none; the owner's step 1 of section 3 comes before CI can be started from here), so the work since the handoff was checked on Linux only, with the whole suite under `-race`. The nightly run (fuzz, the suite three times under `-race` shuffled, a thousand chaos seeds, a twenty-thousand-step soak,
  coverage, `govulncheck`) has found real bugs on its early runs (section 2): read it, and when it fails keep the failing input as a seed.
- **Where things are.** The harness: `internal/{agent,kv,swarm,session,perm,tools,provider}`; the terminal: `internal/tui/*`,
  `cmd/sleipnir/chat*.go`; the RL environment: `internal/rl`; the benchmark: `bench/`, `scripts/bench.sh`; the repository's own
  invariants as tests: `internal/repocheck`; the friction ledger of everything real use found: `docs/DOGFOOD.md` (rows 1 to 52).

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
6. *(The repository's name is done: the module is `github.com/anemos-labs/sleipnir` and the badges, install commands and release configuration say `Anemos-labs/Sleipnir`, as
   the remote does. `.github/CODEOWNERS` still lists the person `@reee344`: a code owner is a person or a team (`@Anemos-labs/team`), not an organisation, so change it to who should
   review. Check `curl -fsSL .../install.sh` and `go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest` once `main` is the default.)*
7. Decide Dependabot PR 1 (it needs Go 1.26 while the documents say 1.25; `internal/repocheck` blocks the mismatch) and whether Windows
   is a supported platform (port `internal/perm`, section 4 item 10, or drop it from `.goreleaser.yaml`).

## 4. Open work, in the order to take it

Each item says where to start and what done looks like. Add a row to `docs/DOGFOOD.md` for anything real use finds.

1. **Finish the benchmark and write down the before and after.** Resume what is unfinished (`scripts/bench.sh run ... --out` the same
   directory continues; `docs/BENCHMARKS.md`, Running it; the key in a 0600 file, `--key-file`): `after1` and `core1` for minimax, then
   `compB` (`--thread-soft-limit 150000`) and `compC` against the default, on `suite-compact`, to decide the compaction setting
   (if a larger soft limit wins, make it the default and declare it in the CHANGELOG; if the temperature does, add a per-model default).
   (Done once, on the current build: the hack rate is 2%, not reproduced, `docs/BENCHMARKS.md`; the rest of this item stands.) What the `dev1` run's friction showed (`sleipnir friction RUNDIR`): after `$(pwd)` (fixed), the refusals are a model guessing where it is (`/workspace`, `/repo`, `/home`; the refusal names the workspace and they guess again), interpreters given a heredoc (right to refuse), and `javac`, which `--allow tests` does not cover; of 14 episodes that said done and failed, one was a model that stopped mid-sentence with a normal end of turn. Then `rl reward --redetect-hacks` on `after1` and read **why the hack rate went from 1.4% to 8.3%** (12 of 144 episodes; the kinds are in
   each episode's `reward.json`): it is either a regression of ours (scratch files, writes outside the diff) or a detector that now sees more;
   fix whichever it is, with a test. Then `sleipnir rl compare OLD NEW` per model, a "Before and after" section in `docs/BENCHMARKS.md`
   and `bench/build.sh --update-lock` to lock the suite. The runs so far cost cents (US$0.15 to 0.20 for 144 episodes); check that the key still
   works (`sleipnir doctor`) before planning around it.
2. *(G5b, prefix-scoped "don't ask again", is done: `docs/SECURITY.md`, "Don't ask again for a runner command".)*
3. *(G5c, a private `$TMPDIR` for the session, is done: `docs/SECURITY.md`, section 2.)*
4. *(G5d, the slash commands, is done: `/status`, `/permissions` and `/trust` are in. `/clear` is decided against: it would be a new kind of compaction patch, a declared and priced rebase,
   and a new chat gets the same thing for nothing, because G0 to G2 are byte-identical across sessions and the provider's cache serves them to the next chat; `/compact` is the way to
   shrink a thread that must go on.)*
5. **F2, the scale ladder and a soak on a real endpoint.** Write a generator of synthetic Go projects of N packages (3, 10, 25, 50 per worker;
   it was designed as `bench/synth/gen.sh` and is not in the repository) and run a swarm on each with `--isolation worktree --verify "go test
   {dirs}"`: tasks a minute, governor 429s, merge-queue latency, memory; then `kill -9` and `--resume`. `internal/session/scale_test.go` is the
   forty workers against the mock endpoint, and the in-process soak (`internal/agent/soak_test.go`) holds the harness's memory to the
   archive's index; this is the same on a live endpoint.
6. **F3, eleven more dogfood sessions** across the four tracks (own backlog; Go standard-library bug hunts on the `std-mini` corpus;
   Python, Node, Rust and Java fixtures; greenfield builds from a written spec with hidden acceptance tests). The entry format and the tmux
   scenarios are in `docs/DOGFOOD.md`.
7. **UX debts from real use** (`docs/DOGFOOD.md`, "Open"): an approval that waits for a person who is elsewhere should show in the cockpit (the chat rings the terminal bell: done) and a
   `swarm --cockpit` needs an in-process approvals dialog; the chat re-wraps what the terminal drew when the window
   narrows (a code block comes back double spaced); `run` against a dead endpoint says each retry (so it is not silent) but retries a refused connection for 34 seconds, which could be shorter for loopback.
8. **Plateau learning for `cache.anomaly`.** On one marketplace endpoint the planner flagged 42 anomalies in 94 requests, most of them the
   endpoint's own erratic prefix cache; learn a per-endpoint plateau of the hit ratio and flag departures from it (`internal/kv/guard.go`).
9. **Runaway-memory supervision** for the swarm: a watchdog on the harness's own heap (the soak says it is bounded by the archive's index, a
   kilobyte a step) and on the children's, with a notice before the kernel's.
10. **The `internal/perm` Windows port** or the decision not to ship Windows. `internal/perm` assumes slash-separated absolute paths in
    `inside`, `splitSegs`, `realPath` and `credentialDirs`. The port is one internal slash representation and a conversion at the file-system calls;
    `scripts/windows-excluded.txt` lists what the Windows job skips, and taking a package off that list is how it is ported.
11. **The first live run of the OpenAI Responses dialect and of the ChatGPT plan login** (`sleipnir login chatgpt`, `chatgpt/<slug>`; both are built, and
    tested against fakes only: `docs/PROVIDERS.md`, "Subscription logins"), and a second live measurement of the Anthropic route (`docs/VALIDATION.md`). What a
    real account may show: whether the plan's preview takes the tools in a namespace as written, which model slugs it lists, the window of each model (the harness
    assumes a cautious one: `options.context_window`), how fast the plan's weekly limit is reached by a team of eight. A Claude plan is not an option: Anthropic's terms forbid it.
12. **The macOS flake** (a session isolate test where a third worker never reached the barrier; once in a CI run, never again in the three that
    followed; diagnostics are in the test, wait for a recurrence and read them before changing anything).
13. **A flake of `TestQueueSurvivesRandomCancellations`** (`internal/workspace`), seen once on 2026-10-01 under load (the whole suite at once; alone it takes 2 s and has
   passed every time): the final submission failed with `gitx: merge: locked: ... .git/worktrees/_integration/ORIG_HEAD.lock: File exists`. Either a killed
   `git merge` left its lock and `restore`/`rebuild` did not clear it (a rollback that falls back to a rebuild takes about 11 s, and the failing run took 14 s), or a
   cancelled git was still running when the next one started, in which case removing the lock would be wrong. Not reproduced once: twelve runs under `-race` with eight busy loops on four cores all passed. Find out which (run the test under
   `-race -count=20` with the rest of the suite loading the machine) before touching it; a deterministic test must fail on the parent, which a stale lock alone does not
   (the rebuild masks it).

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
