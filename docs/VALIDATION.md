# Validating Sleipnir against real models

Everything in the test suite runs against a cache-faithful mock. The claims that matter most (hit ratios, savings,
compaction recovery, swarm dispatch cost) must also be measured on a real endpoint. This page is the protocol; the
script `scripts/validate.sh` runs it end to end and writes a report you can diff between runs and between harness
versions.

## Setup

```sh
export HEIMDALL_API_KEY=...          # or OPENROUTER_API_KEY / OPENAI_API_KEY, or configure a provider (sleipnir init)
sleipnir models | head               # pick a cheap tool-capable model; DeepSeek/Qwen-class models cost cents per run
MODEL=deepseek/deepseek-v4.1-flash scripts/validate.sh
```

Costs: the whole protocol is designed to stay under a few US dollars on a cheap model. Every step prints the
gateway-reported cost (`usage.cost`) and the script stops when `--budget-usd` is reached.

## What is measured

| # | Step | Command | Pass criteria |
|---|---|---|---|
| 1 | **Endpoint profile** | `sleipnir doctor --model $MODEL --deep` | streaming, tools and usage reported; cached tokens reported; note granularity, minimum cached prefix and whether a parallel burst needs a warm-up |
| 2 | **Steady-state cache** | single-agent task on a small fixture repo (the script creates it: a Go package with one failing test), `sleipnir run --model $MODEL --mode accept-edits` | hit ratio >= 0.8 from the third request; no `cache.anomaly{drift}`; every prompt replays (`sleipnir rl verify`) |
| 3 | **Compaction recovery** | a five-phase task that starts with reading twenty short notes one by one, with `--context-window 24000` and the compaction thresholds lowered (`SLEIPNIR_CACHE_THREAD_SOFT_LIMIT_TOKENS=2500`, `SLEIPNIR_CACHE_COMPACT_THRESHOLD_TOKENS=3500`: the fixture's thread never reaches the defaults) | at least one commit; hit ratio >= 0.6 within 3 requests after each commit; the task still passes its tests |
| 4 | **Swarm dispatch** | `sleipnir swarm 6 ...` on a multi-part task | workers' first-request hit ratio >= 0.5 (they read the manager-warmed prefix); requests/min under the endpoint limit; task board consistent (all tasks accepted); leases without conflicts |
| 5 | **Cost vs the simulator** | `sleipnir sim --agents 6 --mode compare`, compared with the inspector's totals for step 4 | measured cost within 25% of the simulated layered bill, or the difference explained (the simulator is a model, not a benchmark; its assumptions are printed with its output) |
| 6 | **RL smoke** | a one-task file over the fixture repo (the script writes it), `sleipnir rl rollout --group 2`, `rl verify`, `rl export --format steps --advantage grpo` | every recorded prompt replays to its wire hash (`rl verify`: 0 mismatches); both samples complete without infra errors; rewards are computed (an export of 0 steps is normal when both samples get the same reward, both pass or both fail: a group with no reward variance carries no learning signal and is dropped as `flat_group`) |

At the end the script prints a verdict: for each step the requests, hit ratios (overall and steady), how many workers'
first requests were served from the warm prefix, compactions, anomalies, cost and whether the fixture's tests pass,
followed by whether each step's criteria above were met. It needs `jq` for that; without it the raw summaries are in the
report file.

## Reading the results

`sleipnir inspect <session dir>` opens the live/after-the-fact dashboard (layer stack per request, hit ratio over
time, compaction timeline, swarm board, cost vs a no-cache bill). `sleipnir sessions` lists recorded runs.

When a step fails:

* **Low hit ratio right from the start**: the provider may not cache short prefixes (see the doctor's minimum), or
  the routing key is not honoured (compare `--no-affinity`). Marketplace routes to Anthropic models do not forward
  `cache_control`; use the chat route or a native Anthropic key.
* **Hit ratio drops after every commit**: expected once (one partial rewrite); persistent drops mean the guard
  logged `cache.anomaly` (a layer changed without a declared rebase) - the event names the layer.
* **Thinking-binding 400s** on Claude models: the hot tail must be delivered turn-scoped or persisted
  (`docs/CACHE-DESIGN.md`, HotMode); check the profile the doctor reports.
* **429s**: lower `swarm.requests_per_minute`; the governor adapts but starts from what you configure.

## First measurements

The first real-endpoint runs (Heimdall staging, `deepseek/deepseek-v4.1-flash`, 30 September 2026, about US$0.002 in all
per run) found the bugs listed in the changelog and left one baseline, `validation/20260930-081737-deepseek_deepseek-v4.1-flash.json`:

| step | result of the baseline run |
|---|---|
| 1 endpoint profile | streaming, exact cost, tools, cached tokens reported; cache granularity ~128 tokens (the smallest cached size came out as ~128, ~256 or undetermined in different runs); 5 of 9 repeat requests hit ("partly"); a parallel burst needs a warm-up; 600 requests/min |
| 2 steady state | 5 requests, steady hit ratio 91% (criterion 80%: met), no drift, tests pass |
| 3 compaction | 32 requests, two model-authored compactions (about 2.3k tokens removed each), tests pass; hit ratio in the requests after a commit 57% (criterion 60%: not met) |
| 4 swarm | manager and two workers, 20 requests, hit ratio 68%; the workers' first requests were all warm (criterion 50%: met), tests pass |
| 6 RL | two rollouts complete, `rl verify` 0 mismatches; 0 step records (both samples got the same reward, so the group carries no signal) |

**Read the hit ratios as a range, not a number.** The endpoint's prefix cache is erratic: the same script, run five times
on the same day, gave a steady hit ratio of 64% to 96% for one agent and 42% to 80% for the swarm, and `doctor` counted 5 to
9 hits in nine repeat requests. The harness's side is steady: no run had a `drift` anomaly, and every recorded prompt replays to
its wire hash. What the misses look like (a conversation that alternates between reading its whole prefix and reading only the
first 640 tokens) and how the routing key behaved is in `docs/CACHE-DESIGN.md`. Criteria on hit ratios therefore need a rerun
before they are read as a regression; drift anomalies, replay mismatches, failed tests and rollouts that end in `infra` do not.

## Recording a baseline

Commit `validation/<date>-<model>.json` (written by the script) when you change anything in `internal/kv`, the
renderer or the swarm, so regressions in real hit ratios are visible in review.
