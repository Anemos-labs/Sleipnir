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
