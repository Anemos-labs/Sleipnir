# Benchmarking

The benchmark compares task outcomes and resource use across harness builds.
It is a controlled comparison tool, not a leaderboard.

## Suite

`bench/suite.json` defines tasks and the suite lock. `bench/build.sh` assembles
the suite under `BENCH_HOME` (default `~/.sleipnir-bench`).
`bench/build.sh --check` checks reproducibility with the same toolchain.

Tasks include standard-library mutations, repository-derived fixes, small
language fixtures, multi-part team tasks, and memory/recall tasks.
Code-task admission requires the verifier to fail on the starting tree and pass
on the reference solution. Composite tasks apply all recorded component patches
before checking the solution. Inspect `admit.json`: tasks without reference
patches are reported as skipped, including recall tasks scored by an expected
answer. A skipped check does not establish task soundness.

A verifier must test requirements specified by the prompt. Hidden tests that
require unnamed symbols invalidate a comparison even when the reference
solution passes. Exclude or repair such tasks before interpreting pass rates.

The recipe excludes these mined tasks because their hidden tests require names
absent from both the prompt and the starting code:

| Task | Hidden requirement |
|---|---|
| `sl-81caac08` | The private `managerIsolationCard` constant and exact isolation-card wording |
| `sl-828c42bc` | The exported `env.MaxWireSeed` constant, beyond the specified 31-bit seed range |
| `sl-b957b26d` | The private `holdSettleMax` variable, `unfinishedWork.idle` field, and `windingDown` method |
| `sl-2515b34e` | The exported `agent.DefaultToolTimeout` constant, beyond the specified configuration field and duration |

These tasks must have their specifications or verifiers repaired and be admitted
again before use in comparisons. Passing baseline/reference checks alone does not
establish that a task's requirements are recoverable.

## Run

Configure credentials through the provider's environment variable or the
script's key-file option. Do not commit credentials or run directories.

```sh
bench/build.sh
scripts/bench.sh run --model PROVIDER/MODEL --tag '!swarm,!long' --group 3 \
    --concurrency 3 --rpm 110 --budget-usd 0.25 --max-spend-usd 3 --out RUNDIR
scripts/bench.sh ab --model PROVIDER/MODEL --bin-a OLD --bin-b NEW --group 3
scripts/bench.sh status RUNDIR
```

Use the same model, toolchain, fixtures, budgets, permission policy, and
verification commands for both builds. Alternate samples to reduce endpoint
load and cache-placement bias.

Runs can resume from their output directory. Inspect the attempt ledger to
separate infrastructure failures from task failures.

## Interpret

Compare:

- Independent verification success and confidence intervals.
- Ordinary input, cache reads, cache writes, and output tokens.
- Total duration, including tool execution and approvals.
- Actual charges when reported, or estimates with declared prices.
- Tool errors, refused operations, retries, compactions, and goal continuations.

Do not average per-request cache percentages. Sum cached input and divide by
total input. A better ratio can accompany more tokens and higher total cost.

Small samples cannot establish small quality changes. An equal pass rate does
not prove equivalence, and a passing test suite does not establish that the
interactive workflow is usable.

## Reports

`sleipnir rl report` summarizes a run; `sleipnir rl compare` compares runs.
Keep generated reports and raw trajectories in the run directory. Publish
results only with the workload, sample size, binary revisions, model, and
measurement limitations.

[Training data](TRAINING-DATA.md) · [Validation](VALIDATION.md)

## Against other harnesses

Sleipnir was compared with Claude Code, opencode and aider on the
[Aider Polyglot](https://github.com/Aider-AI/polyglot-benchmark) benchmark in October 2026. All four used the same model
on the same endpoint (Heimdall staging), the same prompt and the same grading.

| Muse Spark Open | Pass rate | $ per solved task | Cache hit rate | Median time | Runs |
|---|---|---|---|---|---|
| Sleipnir | 96.7% | 0.0093 | 90% | 304s | 153 |
| Claude Code | 92.7% | 0.0157 | 79% | 461s | 150 |
| opencode | 89.1% | 0.0147 | 85% | 400s | 147 |
| aider | 70.3% | 0.0155 | 47% | 1066s | 145 |

| DeepSeek V4.1 Flash | Pass rate | $ per solved task | Cache hit rate | Median time | Runs |
|---|---|---|---|---|---|
| opencode | 96.7% | 0.00044 | 81% | 87s | 180 |
| Sleipnir | 95.6% | 0.00047 | 90% | 104s | 180 |
| Claude Code | 95.0% | 0.00071 | 77% | 113s | 180 |
| aider | 71.7% | 0.00054 | 41% | 306s | 180 |

Comparing task by task on Muse, Sleipnir solved 10 tasks that Claude Code missed, against 3 the other way, and 11 that
opencode missed, against 2. On DeepSeek those pairs are near-ties (7 against 6, 6 against 8), and these runs do not
separate the three agents there.

| Pass rate by language, Muse | C++ | Go | Java | JavaScript | Python | Rust |
|---|---|---|---|---|---|---|
| Sleipnir | 100% | 100% | 100% | 88% | 100% | 92% |
| Claude Code | 100% | 77% | 100% | 95% | 100% | 83% |
| opencode | 88% | 92% | 91% | 86% | 96% | 81% |
| aider | 74% | 91% | 27% | 73% | 89% | 71% |

### Method

- Tasks: a stratified, seeded sample of 60 exercises (10 per language). Before any run, each reference solution passed
  and each stub failed.
- Prompt: the exercise instructions plus aider's own addendum, the same for every harness. The three agents were told
  where the tests are and could run them in one session. aider followed its published two-try protocol: one attempt,
  then one retry shown the failing tests.
- Grading: the test files were restored first, so an edited test could not help, then the language's test command
  ran as in aider's benchmark.
- Harnesses: Sleipnir (bypass mode), Claude Code (`-p`, permissions skipped, web tools off), opencode (`run`, edits and
  bash allowed, web off), aider 0.86.2 (diff edit format, no repository map).
- Runs: 3 reps per harness and model, shuffled within each rep and run 5 at a time, so the endpoint's load fell on
  all harnesses alike. Time limit per task: 900s on DeepSeek, 1800s on Muse, the same for every harness.
- Isolation: each agent ran in its own mount and PID namespace that showed only its task directory and a throwaway
  home. It could see no reference solutions, no other run and no credentials.
- Cost and cache: a pass-through proxy recorded each request's usage as the endpoint reported it. Cost is uncached
  input plus cache reads plus output, at the endpoint's list prices, which match the cost the endpoint itself
  reports.

### Caveats

- One container, one endpoint and two days of runs. This is a comparison under the same conditions, not a
  leaderboard score. aider's numbers are not comparable with its own leaderboard, which has no time limit and uses a
  repository map.
- DeepSeek rep 1 ran into endpoint rate limits (HTTP 429). Those account for nearly all of the three agents' rep-1
  failures; reps 2 and 3 had none. The table includes rep 1.
- Muse was taken offline after 595 of 720 runs (the four row counts above). Reps 1 and 2 are complete for every harness, and rep 3 is partial.
- aider first sent non-streaming requests, which the test machine's egress proxy cut after 300 silent seconds.
  opencode's request timeout caps a whole request rather than a silence. Both were corrected, and every result of the
  affected harness was re-run.
- Team (swarm) mode was not measured.
