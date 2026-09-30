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
| 2 | **Steady-state cache** | single-agent task on a fixture repo (`scripts/fixtures/`), `sleipnir run --model $MODEL --mode accept-edits` | hit ratio >= 0.8 from the third request; no `cache.anomaly{drift}`; every prompt replays (`sleipnir rl verify`) |
| 3 | **Compaction recovery** | same task with `--context-window 24000` to force compactions | at least one commit; hit ratio >= 0.6 within 3 requests after each commit; the task still passes its tests |
| 4 | **Swarm dispatch** | `sleipnir swarm 6 ...` on a multi-part task | workers' first-request hit ratio >= 0.5 (they read the manager-warmed prefix); requests/min under the endpoint limit; task board consistent (all tasks accepted); leases without conflicts |
| 5 | **Cost vs the simulator** | `sleipnir sim --agents 6 --mode compare`, compared with the inspector's totals for step 4 | measured cost within 25% of the simulated layered bill, or the difference explained (the simulator is a model, not a benchmark; its assumptions are printed with its output) |
| 6 | **RL smoke** | `sleipnir rl taskgen git`, `rl rollout --group 2`, `rl export --format steps`, `rl verify` | every exported prompt equals `openaichat.Build` of the replayed prompt; rewards computed; no `replay_mismatch` |

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

## Recording a baseline

Commit `validation/<date>-<model>.json` (written by the script) when you change anything in `internal/kv`, the
renderer or the swarm, so regressions in real hit ratios are visible in review.
