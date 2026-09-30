# Sleipnir

**A coding-agent harness for OpenAI-style and Anthropic-style endpoints, built around one idea: the prompt cache is
the shared memory of a team of agents.**

Odin's eight-legged horse: one manager brain, many legs. Sleipnir is designed to run **10, 20, 50 agents over one
repository** without briefing them, without re-reading the project fifty times, and without letting context bloat eat the
bill.

```
G0 constitution + tools      frozen; identical bytes for every agent, every role
G1 shared pin                project map + instructions; identical for every agent; changes only at epochs
G2 role pin                  manager / backend / frontend / tester / reviewer / ...; shared by agents of a role
G3 notes                     one agent's tenured facts, decisions, instructions
G4 spine                     one agent's append-only one-line resumes of its own past
G5 thread                    verbatim recent turns; append-only between compactions
G6 hot                       always-fresh view of the swarm (board, mail); never cached, a few hundred tokens
```

The layers are ordered by volatility, because provider prompt caches match **byte prefixes**: stable things sit deep and
are cached once for everyone; active things sit near the end. A spawned worker inherits G0-G2 from the provider's cache
for the price of a cache read plus a two-line assignment. Background compactors (forks of the agent's own request, so they
read the same cache) fold old history into one-line resumes and promote important facts, and the harness commits each patch
when the cache economics say so, or at a cold moment for free.

## Why this is different

| | typical harness | Sleipnir |
|---|---|---|
| history | one growing list, cached at the end | layered by volatility; generational compaction; nothing is lost (`recall`) |
| compaction | summarise everything, rewrite the prefix, hope | model proposes a *patch*, the harness validates and commits it at the cheapest moment; masking before summarising |
| subagents | each has its own cache and re-explores the project; the manager writes long briefings | one shared prefix; a worker is assigned a task card and already knows the project |
| coordination | JSON files, chat between agents | a typed board, rate-limited typed mail through a router, leases and scopes, harness-owned "done" |
| cost control | hope | every change to a stable layer is a priced, declared event; a guard flags silent cache regressions |
| training | export transcripts | the harness *is* an RL environment: exact-prompt trajectories, verifiable rewards, trainer-ready data |

## Quick start

```sh
# 1. install (Linux/macOS)                                   (or: go install github.com/reee344/sleipnir/cmd/sleipnir@latest)
curl -fsSL https://raw.githubusercontent.com/reee344/sleipnir/main/scripts/install.sh | sh
sleipnir demo                          # no key, no network: a scripted team of 14 agents, the cache at work, the bill

# 2. point it at a model: any OpenAI-compatible endpoint works (marketplaces, OpenRouter, OpenAI, vLLM, ...), and so does an Anthropic Messages endpoint
export HEIMDALL_API_KEY=...            # or OPENROUTER_API_KEY / OPENAI_API_KEY
sleipnir init --user                   # your providers and permission mode (project files cannot set these unless trusted)
cd your-project && sleipnir init       # shareable project settings, AGENTS.md (read only with --trust-project)
sleipnir models | head                 # catalogue with prices
sleipnir doctor --model <model> --deep # measures streaming, tools, cache reporting, granularity, warm-up needs

# 3. work
sleipnir chat                          # interactive; /cost /context /compact /agents /rewind /diff /plan; --resume ID or --continue
sleipnir run "fix the failing test in ./server"
sleipnir swarm 8 "add pagination to every list endpoint and update the client" --verify "make test"
```

`sleipnir recon` prints the project map that seeds the shared layer (layout, build/test commands, package docs, ranked
declarations); it is deliberately dense and budgeted (default 5k tokens) because the shared pin is read on every request of
every agent.

## Train a model on this way of working

The harness records every model call's exact prompt (as content hashes), the completion, and, from a self-hosted policy
server, token ids and logprobs. The RL pipeline turns that into trainer-ready data (`docs/TRAINING-DATA.md`):

```sh
sleipnir doctor --model my-policy --capture                      # does the server return token ids? are they prefix-stable?
sleipnir rl taskgen git --repo ./myrepo -o tasks/all.jsonl       # tasks from history; each proven to fail before and pass after
sleipnir rl taskgen recall --repo ./myrepo -o tasks/recall.jsonl # memory tasks that force compaction
sleipnir rl tasks split tasks/all.jsonl --spec train:0.9,test:0.1   # whole repositories per split
sleipnir rl rollout --tasks tasks/all.train.jsonl --model local/my-policy --group 8 --capture --out runs/r1
sleipnir rl export runs/r1 --format steps --advantage grpo -o data/r1.steps.jsonl   # or tokens / groups / sft / dpo / kto / atif
sleipnir rl eval --tasks tasks/all.test.jsonl --exclude tasks/all.train.jsonl --model local/my-policy
sleipnir inspect runs/r1/<task>/0                                # the cache inspector works on every rollout
```

Rewards are verifier outcomes in a clean checkout the agent cannot tamper with, plus cost repriced for your target
provider, protocol quality, and per-role signals (manager dispatch, compactor fidelity, mail usefulness).

## What the layering buys (and where it does not)

<!-- SIM:BEGIN -->
`sleipnir sim` replays one synthetic swarm workload (20 workers, 40 tasks, about 32 work steps per task; an agent
without a shared pin spends 30k tokens orienting itself in the repository) under a plain harness, the same with
summary compaction, and the layered policy, pricing every request against an explicit model of the provider's cache.
Costs are in millions of input-token equivalents, lower is better. Regenerate with `make readme-sim`.

Provider with explicit cache breakpoints (reads 0.1x, writes 1.25x, 5 minute TTL):

```
scenario              naive  naive+sum   sleipnir  vs naive  vs n+sum  ctx n/s wall n/s
typical               25.4M      22.1M      16.2M      -36%      -26%  68/43 k 100/81 m
short tasks            8.5M       8.5M       4.7M      -45%      -45%  38/38 k  48/26 m
long tasks            96.8M      50.7M      45.3M      -53%      -11% 135/45 k 214/201m
cold launches         25.6M      22.3M      16.4M      -36%      -26%  68/43 k 203/179m
small repo            17.1M      16.4M      15.5M       -9%       -5%  51/42 k  86/81 m
bloated pins          25.4M      22.1M      22.8M      -10%       +3%  68/85 k 100/81 m
huge exploration      50.8M      32.5M      18.0M      -64%      -44% 106/45 k 130/81 m
```

Provider with an automatic prefix cache (reads 0.25x, no write premium, 3 engines behind a marketplace):

```
scenario              naive  naive+sum   sleipnir  vs naive  vs n+sum  ctx n/s wall n/s
typical               33.1M      28.4M      22.4M      -32%      -21%  68/43 k  99/81 m
short tasks            9.7M       9.7M       6.6M      -33%      -33%  38/39 k  48/26 m
long tasks           143.8M      68.5M      62.3M      -57%       -9% 135/46 k 210/199m
cold launches         33.0M      28.4M      22.0M      -33%      -23%  68/43 k 201/178m
small repo            21.6M      20.5M      21.4M       -1%       +4%  51/42 k  85/81 m
bloated pins          33.1M      28.4M      38.5M      +17%      +36%  68/85 k  99/81 m
huge exploration      67.6M      39.8M      24.0M      -65%      -40% 106/45 k 128/81 m
```

Columns: `ctx` is the average context per request in thousands of tokens and `wall` the simulated duration in minutes,
each as naive/sleipnir. The rows: `short tasks` (about 8 work steps each: pins and orientation dominate), `long tasks` (about 90: compaction
dominates), `cold launches` (three launches 12 idle minutes apart, so every launch starts cold), `small repo` (a cold
agent needs only 6k tokens to orient, so there is little for a pin to replace), `bloated pins` (a sloppy 40k shared and
20k role pin against the same 30k of exploration) and `huge exploration` (a monorepo where a cold agent reads 90k).

Layering wins when the shared pin is dense. The pin replaces each agent's own orientation; once it is larger than
that orientation it costs every request of every agent more than it saves (`bloated pins` above loses on the
marketplace cache and roughly ties on the explicit one). Sweeping the total pin size (`pins(k)`, split into shared and
role pin in thousands of tokens) against a fixed 30k orientation on the explicit-cache provider:

```
pins(k)    shared/role vs pin/explore   vs naive   vs n+sum
4             1/2     0.13                 -44%       -36%
9             4/4     0.30                 -41%       -33%
18            8/9     0.60                 -36%       -26%
30           13/16    1.00                 -29%       -18%
45           20/24    1.50                 -20%        -8%
70           31/38    2.33                  -5%       +10%
100          45/55    3.33                 +14%       +31%
```

The simulator is a model, not a benchmark: its assumptions are printed with every run, and it exists so that a change
to the policy has to survive an explicit cost comparison (`go test ./internal/kv/sim` guards the shape of these results).
<!-- SIM:END -->

The result is a model with printed assumptions, not a benchmark, and it says where layering loses: **the shared pin must
be dense** (roughly no larger than the orientation it replaces). `docs/VALIDATION.md` is the protocol for measuring the real
thing on your endpoint.

## Providers

| dialect | status | notes |
|---|---|---|
| OpenAI-style chat completions (OpenAI, Heimdall, OpenRouter, vLLM, SGLang, ...) | supported | automatic prefix caching, routing key for engine affinity, exact gateway costs, reasoning replay, optional token capture |
| Anthropic Messages (Anthropic, and gateways that speak it) | implemented: dialect `anthropic` (`docs/CONFIGURATION.md`) | explicit breakpoints (max 4, 20-block lookback, 5m/1h TTL), preserved thinking, turn-scoped hot tail; per-gateway switches for what a route drops (`cache_control`, thinking, mid-conversation system text); measure your endpoint with `sleipnir doctor --deep` |
| OpenAI Responses | not built yet (`openai-responses` is accepted in config, but a session that uses it stops with an error) | |

## Safety model

Tool output, web pages, file contents and mail are data, never instructions; pins and notes are user-role context, never
system. Permissions are an engine (modes, rules, shell-syntax analysis, role profiles), not a prompt; writes pass a lease
guard, a content-hash staleness check and a checkpoint; verifier files are never visible to a training policy. What a
repository brings (config keys that redirect keys or run code, instruction files, skills, slash commands, agent
definitions, hooks) is ignored unless you pass `--trust-project`. Real isolation for untrusted repositories needs an OS
sandbox; see `docs/ARCHITECTURE.md`.

## Documentation

* `docs/CONFIGURATION.md` - where configuration lives, every key, providers and their options, permissions, trust, worked examples
* `docs/EXTENDING.md` - instruction files, skills, slash commands, agent definitions, hooks, sessions and resume, compaction
* `docs/CLI.md` - every command and flag (generated from `--help`), slash commands, exit codes
* `docs/ARCHITECTURE.md` - system map, package map, one request end to end, decisions
* `docs/CACHE-DESIGN.md` - the layered generational cache, planner, guard, simulation results
* `docs/SWARM-PROTOCOL.md` - board, mail, leases, roles, warm gate, verifier-gated done
* `docs/TRAINING-DATA.md` - the RL environment, rewards, formats, governance
* `docs/VALIDATION.md` - validating against real endpoints
* `docs/research/` - the landscape, provider caching and swarm/training research this design rests on
* `docs/reviews/` - adversarial reviews and what was done about them

## Development

```sh
make build test race lint sim
```

Go 1.24, standard library plus `golang.org/x/{net,sys,term}`. See `AGENTS.md` and `docs/BUILDING.md`.

## License

To be decided before the first release.
