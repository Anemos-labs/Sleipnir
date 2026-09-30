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

# 2. point it at a model: any OpenAI-compatible endpoint works (marketplaces, OpenRouter, OpenAI, vLLM, ...)
export HEIMDALL_API_KEY=...            # or OPENROUTER_API_KEY / OPENAI_API_KEY
sleipnir init --user                   # your providers and permission mode (project files cannot set these unless trusted)
cd your-project && sleipnir init       # shareable project settings, AGENTS.md
sleipnir models | head                 # catalogue with prices
sleipnir doctor --model <model> --deep # measures streaming, tools, cache reporting, granularity, warm-up needs

# 3. work
sleipnir chat                          # interactive; /cost /context /agents /rewind /diff /plan
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
sleipnir rl taskgen git --repo ./myrepo --test "go test ./..." -o tasks.jsonl
sleipnir rl rollout --tasks tasks.jsonl --model local/my-policy --group 8 --capture --out runs/r1
sleipnir rl export runs/r1 --format steps --advantage grpo -o data/r1.steps.jsonl   # or tokens / groups / sft / dpo / kto / atif
sleipnir rl eval --tasks holdout.jsonl --model local/my-policy
```

Rewards are verifier outcomes in a clean checkout the agent cannot tamper with, plus cost repriced for your target
provider, protocol quality, and per-role signals (manager dispatch, compactor fidelity, mail usefulness).

## What the layering buys (and where it does not)

<!-- SIM:BEGIN -->
(see `sleipnir sim` and `docs/CACHE-DESIGN.md` section 8)
<!-- SIM:END -->

The result is a model with printed assumptions, not a benchmark, and it says where layering loses: **the shared pin must
be dense** (roughly no larger than the orientation it replaces). `docs/VALIDATION.md` is the protocol for measuring the real
thing on your endpoint.

## Providers

| dialect | status | notes |
|---|---|---|
| OpenAI-style chat completions (OpenAI, Heimdall, OpenRouter, vLLM, SGLang, ...) | supported | automatic prefix caching, routing key for engine affinity, exact gateway costs, reasoning replay, optional token capture |
| Anthropic Messages | see `docs/ARCHITECTURE.md` (status table) | explicit breakpoints (max 4, 20-block lookback, 5m/1h TTL), preserved thinking, turn-scoped hot tail |
| OpenAI Responses | planned | |

## Safety model

Tool output, web pages, file contents and mail are data, never instructions; pins and notes are user-role context, never
system. Permissions are an engine (modes, rules, shell-syntax analysis, role profiles), not a prompt; writes pass a lease
guard, a content-hash staleness check and a checkpoint; verifier files are never visible to a training policy. Real
isolation for untrusted repositories needs an OS sandbox; see `docs/ARCHITECTURE.md`.

## Documentation

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
