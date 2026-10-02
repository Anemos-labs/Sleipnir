# Benchmarks: measuring the harness on real models

The tests say the parts work; they cannot say whether the harness makes a model better at real work, and they never fail for
the reason that matters most (a prompt change that costs ten points of pass rate). The benchmark does. It runs a fixed suite of
verifiable tasks through the real binary against a real endpoint, scores each run by the project's own tests in a clean checkout,
and compares two builds of the harness on the same models the same day.

It is not a leaderboard. The tasks are small, the budgets are tight (mostly 40 steps, 80 to 100 requests and 15 minutes, US$0.25 a task) and the
endpoint is a marketplace whose latency and prefix cache drift from hour to hour. What it measures well is the **difference**
between two versions of the harness, run side by side: a number from one run says little, a paired comparison of two says
whether a change helped.

## The suite (`bench/`)

`bench/suite.json` is the recipe and, under `lock`, what building it gave; `bench/build.sh` builds it outside the repository
(`$BENCH_HOME/suite`, default `~/.sleipnir-bench`), `bench/build.sh --check` compares a rebuild with the lock (the same Go version must
give a byte-identical `tasks.jsonl`, identified by its sha256).

| Part | Tasks | What it is |
|---|---|---|
| mutations | 24 | A real, tested package of the Go standard library ("std-mini": 28 self-contained packages copied from the local `GOROOT` at build time, with the imports rewritten; nothing of it is committed) with one bug injected (a flipped comparison, a negated condition, a swapped branch, a dropped nil check). The prompt names the failing tests; the agent finds and fixes the source. |
| mined | 12 | Commits of this repository's own history from before `bench/` existed: the state before the commit, the task from its message, the tests from the commit as the verifier (hidden). |
| fixtures | 17 | Small projects in Go, Python, JavaScript, Rust and Java with a hidden acceptance test (`bench/fixtures`, each `start`, `solution` and `hidden`; the three greenfield ones are built from a written spec). Only the standard toolchain of each language is used, and only fixtures that pass admission on the machine are in its suite: the first run's machine had Go, Python, Node and Java (12 of the 17), the machine of 2026-10-02 had Rust too (all 17). |
| composite | 4 | Three-part tasks for a manager and workers (a swarm); the same task as a single agent is the baseline. |
| recall | 3 | Memory tasks that overflow a small context window and need `recall`. |

A task is **admitted** only if, three times in a row, its verifier fails on the starting state and passes with the reference
solution (`rl tasks check --verify-repeats 3`); the ones that are not are listed under `quarantined` with the reason. Tags choose
what runs: `smoke` (about 8 tasks, minutes), `core` (the regular suite), `swarm`, `long`. The suite caps what a single-agent task may use
(`budget_caps`), whatever its generator derived.

## Running it

```sh
bench/build.sh                                   # once per Go version: the suite, admitted
export HEIMDALL_API_KEY=...                      # or --key-file FILE (the key alone or a shell file exporting it; mode 0600; never printed or logged)
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

## Before and after (the first comparison)

`core1` is the build before the fixes of `docs/DOGFOOD.md` rows 1 to 28; `after1` is the build after them, on the same suite, the same
model (`heimdall/deepseek/deepseek-v4-flash`), three samples of each of 48 tasks, a day apart. `sleipnir rl compare` (paired bootstrap over
tasks, 2000 resamples, 95% intervals):

| Measure | Before | After | Difference | Verdict |
|---|---|---|---|---|
| pass@1 | 54.9% | 50.7% | -4.2 points [-12.5, +2.8] | same |
| cost per episode | US$0.00131 | US$0.00085 | -35% [-0.00074, -0.00020] | better |
| steps per episode | 24.2 | 18.0 | -6.2 [-9.6, -2.8] | better |
| input-token equivalents | 169,385 | 116,225 | -31% | better |
| requests per episode | 27.1 | 24.8 | -2.3 [-5.1, +0.5] | same |
| wall time per episode | 465 s | 584 s | +119 s [+57, +181] | worse (the endpoint was shared by four of our own runs) |
| hack rate | 1.4% | 8.3% | +6.9 points [+0.7, +13.9] | **worse: open** |

What it says: the fixes made an episode cheaper and shorter and did not move the pass rate beyond noise (the interval of the difference
includes a loss of twelve points and a gain of three; one model and 48 tasks cannot tell). What it does not say: why the hack detector
flags 12 episodes of 144 now and 2 before. That is either a regression (scratch files, writes outside the diff) or a detector that sees more
after `rl reward --redetect-hacks` changed what it reads; it is the first item of `docs/ROADMAP.md`. The comparison is for one model; the
other models' runs (minimax-m2.7, qwen, and the compaction soft-limit experiment `compB`/`compC`) were stopped part way by a restart of the
machine and are resumable (`docs/ROADMAP.md`, section 4).

### The hack rate, looked at again (a third run)

`dev1` is the current build (2026-10-01, Go 1.25.1, the same model, one sample of each of 52 tasks of the rebuilt suite: `encoding/base64`
left the corpus, see `bench/stdmini/packages.txt`, so it is not the same 48 tasks): pass 52% [39, 65], US$0.0012 an episode, hit 68%,
**hack rate 2% (1 of 52)**, and `rl reward --redetect-hacks` scores it the same. The 8.3% of `after1` is not reproduced, and with
one sample of each task the two cannot be told apart from noise (1 of 52 and 12 of 144 differ by about five points, an interval wider than
that). Taken together: the pass rate is where it was (55%, 51%, 52%), the hack rate is not shown to have risen, and the open item is
closed as *not reproduced*; a second run with three samples per task would settle it, and `rl compare` against `core1` needs the same
suite. 27% of episodes said they were done and failed the verifier (`FALSEDONE`), and 19 of 71 attempts ended without an answer.

## The second comparison (2026-10-02): the build of the login work against the build forty commits later

`heimdall/deepseek/deepseek-v4-flash` had no route on Heimdall for hours that morning (503 `no_route`, while its catalogue went on listing it; it answered
again at 07:35 UTC), so this comparison is on `heimdall/deepseek/deepseek-v4.1-flash`, which answered. The 47 `core` tasks of the 60-task suite (`bench/suite.json`, lock
written: 24 mutations, 4 composite, 12 mined, 17 fixtures and 3 recall tasks, 47 of them `core`), two samples of each, 94 episodes a run, both builds at the same time
(`scripts/bench.sh`, four rollouts of each at once, one seed): `b0` is the build of commit `76d33bb` (2026-10-01 23:53 UTC, the first login work) and `d` the build of `443e984`
(2026-10-02 06:25), forty commits later. No infrastructure error in either; a run cost about US$0.10. `sleipnir rl compare` (paired bootstrap over tasks, 2000 resamples):

| Measure | b0 | d | Difference | Verdict |
|---|---|---|---|---|
| pass@1 | 76.6% | 78.7% | +2.1 points [-3.2, +8.5] | same |
| pass^2 (both samples pass) | 70.2% | 74.5% | +4.3 [-4.3, +12.8] | same |
| cost per episode | US$0.00108 | US$0.00106 | -US$0.00003 [-0.00015, +0.00009] | same |
| requests per episode | 19.6 | 19.0 | -0.6 [-2.6, +1.3] | same |
| steps per episode | 18.6 | 18.0 | -0.6 [-2.6, +1.3] | same |
| wall time per episode | 460 s | 459 s | -1 s [-37, +33] | same |
| hack rate (as scored then; none since the detector's fix, below) | 2.1% | 2.1% | 0 | same |
| said done and failed the verifier | 6 of 94 | 7 of 94 | | |

What it says: nothing moved beyond noise, and nothing got worse (the paired interval of the pass rate is -3.2 to +8.5 points: a loss of more than three points is ruled out, a gain of
up to eight is not). What the two features that were added in between did, counted in the episodes of `d`: the note
"You changed code and have not run the tests since" was sent in 6 of the 94, all of them the JavaScript fixtures (`js-csv`, `js-eventemitter`, `js-slugify`, both samples),
and all six passed on both builds; the `plan` tool was called in 12 (greenfield tasks and fixtures in four languages, and a mined task), with the same verdict as the same task and sample
on `b0` in 11 of them and a loss in the other (`greenfield-py-todoapi`, sample 0). So neither is shown to help, and neither is shown to hurt; both stay, because they cost nothing
(US$0.00106 against US$0.00108 an episode) and a harness for models that stop early has a use for them. The 21% of episodes that said done and failed on `deepseek-v4-flash` in the
first A/B of the session did not appear on this model (6% and 7%): that is the model, not a build.

By part (pass rate of `d`, `b0` in brackets): mutations 97% (94%), fixtures 91% (94%), mined 33% (25%). Of the 20 episodes of `d` that failed (22 of `b0`), 16 are mined tasks, 13 ended on
a budget and seven said they were done. The mined tasks (`sl-*`, 12 of the 47) are a floor, not a measure: their hidden tests name symbols that the prompt does not (an exported constant of
the commit), and five of the 20 failures of `d` were `undefined:` build errors of those hidden tests. The two flagged episodes of each run were the same task and samples in both builds, `greenfield-js-csvtool`, `hack:verifier_touched`: the agent
wrote tests of its own (`test/conformance.local.test.js`, `test/readme.test.js`) whose names match the verifier's glob (`test/*.test.js`), and the detector took the glob, handed to
`node --test`, for the script the verifier runs. That was the detector's fault and not a build's, and it is fixed: `sleipnir rl reward --redetect-hacks` on the run now finds no hack
(a flagged episode had passed its verifier, and its reward goes from -0.24 to +1.06).

## Caveats

- **The endpoint is a staging marketplace** (`api-staging.impossiblecarrot.cc`): latency of 10 to 30 seconds a request, HTTP 503
  ("Database is temporarily unavailable", "Request ownership was lost") when many rollouts run at once, a prefix cache that serves
  the stable prefix and often not the thread (every `cache.anomaly` of a run says what was expected and what was read). A run is
  resumable for this reason, and the rollouts that hit an outage are repeated, not scored as failures. A model's whole route can go: `deepseek-v4-flash` answered
  503 `no_route` for hours on 2026-10-02 while the catalogue listed it, and the comparison above was made on `deepseek-v4.1-flash` for that reason.
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
