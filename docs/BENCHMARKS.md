# Benchmarks: measuring the harness on real models

The tests say the parts work; they cannot say whether the harness makes a model better at real work, and they never fail for
the reason that matters most (a prompt change that costs ten points of pass rate). The benchmark does. It runs a fixed suite of
verifiable tasks through the real binary against a real endpoint, scores each run by the project's own tests in a clean checkout,
and compares two builds of the harness on the same models the same day.

It is not a leaderboard. The tasks are small, the budgets are tight (40 steps, 100 requests, 15 minutes, US$0.25 a task) and the
endpoint is a marketplace whose latency and prefix cache drift from hour to hour. What it measures well is the **difference**
between two versions of the harness, run side by side: a number from one run says little, a paired comparison of two says
whether a change helped.

## The suite (`bench/`)

`bench/suite.json` is the recipe and, under `lock`, what building it gave; `bench/build.sh` builds it outside the repository
(`$BENCH_HOME/suite`, default `~/.sleipnir-bench`), `bench/build.sh --check` compares a rebuild with the lock (the same Go version must
give a byte-identical `tasks.jsonl`, identified by its sha256).

| Part | Tasks | What it is |
|---|---|---|
| mutations | 28 | A real, tested package of the Go standard library ("std-mini": 18 self-contained packages copied from the local `GOROOT` at build time, with the imports rewritten; nothing of it is committed) with one bug injected (a flipped comparison, a negated condition, a swapped branch, a dropped nil check). The prompt names the failing tests; the agent finds and fixes the source. |
| mined | 12 | Commits of this repository's own history from before `bench/` existed: the state before the commit, the task from its message, the tests from the commit as the verifier (hidden). |
| fixtures | 12 of 17 | Small projects in Go, Python, JavaScript, Rust and Java with a hidden acceptance test (`bench/fixtures`, each `start`, `solution` and `hidden`; the three greenfield ones are built from a written spec). Only the standard toolchain of each language is used, and only fixtures that pass admission on the machine are in its suite: the first run's machine had Go, Python, Node and Java. |
| composite | 4 | Three-part tasks for a manager and workers (a swarm); the same task as a single agent is the baseline. |
| recall | 3 | Memory tasks that overflow a small context window and need `recall`. |

A task is **admitted** only if, three times in a row, its verifier fails on the starting state and passes with the reference
solution (`rl tasks check --verify-repeats 3`); the ones that are not are listed under `quarantined` with the reason. Tags choose
what runs: `smoke` (about 8 tasks, minutes), `core` (the regular suite), `swarm`, `long`. The suite caps what a single-agent task may use
(`budget_caps`), whatever its generator derived.

## Running it

```sh
bench/build.sh                                   # once per Go version: the suite, admitted
export HEIMDALL_API_KEY=...                      # or --key-file FILE (mode 0600; never printed or logged)
scripts/bench.sh run --model heimdall/deepseek/deepseek-v4.1-flash --tag '!swarm,!long' --group 3 \
    --concurrency 3 --rpm 110 --budget-usd 0.25 --max-spend-usd 3 --out RUNDIR
scripts/bench.sh ab --model M --bin-a OLD --bin-b NEW --group 3        # two binaries, alternating sample by sample
scripts/bench.sh status RUNDIR...                # one line per run directory
```

`scripts/bench.sh run` is resumable: `sleipnir rl rollout` into the same directory continues what is unfinished, so a run that was
killed, or an endpoint that was down (exit 75), or rollouts that failed for infrastructure reasons are picked up by the next invocation
after a pause that doubles up to ten minutes, and nothing is paid for twice (`ledger.jsonl` lists every attempt, the failed ones
included). It refuses to start, and pauses, when the disk is nearly full (`--min-free-gb`), keeps the work directory and the Go
build cache of the benchmark apart from the development ones, and passes the key to the child process only. `--pin-cpus` runs it on some
cores, leaving the rest to whatever else the machine does.

Every flag that changes what the agents do is a flag of `sleipnir rl rollout` (`--mode single|swarm:N`, `--perm-mode`, `--allow`,
`--ignore-repo-instructions`, `--thread-soft-limit`, `--budget-usd`, `--rpm`, `--max-spend-usd`, …), passed through `--extra "FLAGS"`.

## Reading a run

```sh
sleipnir rl report RUNDIR...            # table, markdown or json: pass rate with its 95% interval, cost, cache hits, friction
sleipnir rl compare A B                 # two runs over the tasks both ran: paired bootstrap over tasks, verdict per metric
sleipnir friction RUNDIR                # what slowed the runs down, ranked (docs/DOGFOOD.md)
```

`PASS` is passed episodes over completed ones with a Wilson interval; `SOLVED` the tasks that at least one sample passed; `HIT`
cache-read tokens over input tokens; `FALSEDONE` the share of episodes that claimed to be done and failed the verifier; `HACK` the share
flagged by the reward detectors (an edit of a protected path, a write outside the workspace). Hacks are a property of how an episode was
scored: `sleipnir rl reward RUNDIR --redetect-hacks` scores an old run again with the current detectors (two false positives of the
first run came from detector bugs, `docs/DOGFOOD.md` rows 6 and 7).

`rl compare` pairs the two runs by task and resamples tasks (2,000 times) to give each metric an interval, so that a verdict is
`better`, `worse` or `same` and not a difference between two noisy numbers; `--gate pass_at_1:0.05` makes it exit non-zero when a
metric regresses beyond a margin.

## How a change is judged

1. **Same day, same models, same suite**, the old build and the new one, concurrently or alternating (`bench.sh ab`): the endpoint
   drifts (its prefix cache alone varies by thirty points between identical runs), so a committed number from last week shows a trend and
   nothing more.
2. **The primary metric is pass@1 on the tasks both ran**, with its paired interval; the protocol metrics (steps, requests, wall time,
   tool errors, refused calls) say why. Cost and cache hit ratio are reported and never gated: the endpoint's prefix cache is erratic
   and the prices are tiny.
3. **Steps and time are the scarce resources.** A step is a turn of the model. On the first run 107 of 385 episodes (28%) ended on
   a budget: 46 on the step or request limit, 61 on the fifteen-minute clock (a model that takes 30 seconds a request cannot make
   forty steps in 900 seconds; the slowest spent the clock on 22 to 36 requests). The episodes that passed finished in a median of
   nine to thirteen steps. Anything that wastes a step or a wait (a refused command, a file read in many small windows, a compaction
   that makes the agent read again, a request that took a minute) is paid for in pass rate, not in dollars, and on a slow model a
   comparison of two builds is partly a comparison of how many requests each needs inside the clock.
4. A change that cannot be shown to help on the suite is a hypothesis, and says so in the CHANGELOG.

## What a run found

The first run (`core1`, 349 sessions on four models) is what `docs/DOGFOOD.md` rows 1 to 7 and 23 to 27 came from: three quarters of the
episodes hit a permission refusal (mostly commands starting with `cd` to a path the model had guessed, because nothing told it where it
starts), correct work was flagged as a hack, one Ctrl-C ended a whole chat, a model's compaction patch was refused for one field of the
wrong type. Each is a fix with a test that failed first; the second run measures them together.

## Caveats

- **The endpoint is a staging marketplace** (`api-staging.impossiblecarrot.cc`): latency of 10 to 30 seconds a request, HTTP 503
  ("Database is temporarily unavailable", "Request ownership was lost") when many rollouts run at once, a prefix cache that serves
  the stable prefix and often not the thread (every `cache.anomaly` of a run says what was expected and what was read). A run is
  resumable for this reason, and the rollouts that hit an outage are repeated, not scored as failures.
- **One shared machine.** CPU contention slows `go test` in the workspaces; a rollout that hits its wall-clock budget because of it is
  a failure that is not the harness's. On the first run 61 of the 107 budget exhaustions were the clock, and the machine was not why:
  an agent's time is mostly spent waiting for the endpoint. Read the `budget_exceeded` flag with the number of requests and the time
  per request before blaming either. (Those episodes were recorded as claiming to be done, which counted them as false claims;
  `docs/DOGFOOD.md` row 28.)
- **The sandbox has no network isolation**, so tasks with `network=false` run with host networking; the verifiers do not need it.
- **Reading the standard library's own source** (`/usr/local/go*/src`) is refused to the agents: on the mutation tasks the original is
  the answer, a diff away.
- **Models differ in how they use steps** (batching tool calls, windowed reads, compaction patches that parse): a change that helps
  one family can do nothing for another, so each comparison is per model.

## Files

| Path | What |
|---|---|
| `bench/suite.json`, `bench/build.sh` | the recipe, its lock, the builder |
| `bench/fixtures/` | the committed fixtures, each `start`, `solution`, `hidden` (`check.sh` verifies them) |
| `bench/tools/stdmini`, `bench/stdmini/` | the standard-library corpus builder and its pinned package list |
| `bench/tools/benchcmp` | the comparison of two `go test -bench` runs (`docs/BUILDING.md`, Performance) |
| `scripts/bench.sh` | the resumable runner (`run`, `ab`, `status`) |
| `RUNDIR/manifest.json`, `bench.log`, `ledger.jsonl`, `TASK/N/` | what a run wrote: its configuration, a line per rollout, every attempt's spend, and each rollout's event log, diff, verifier output and scored episode |
