<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/media/logo-wordmark-dark.svg">
    <img src="docs/media/logo-wordmark.svg" alt="Sleipnir: one manager brain, many legs" width="620">
  </picture>
</h1>

**A coding-agent harness for OpenAI-style and Anthropic-style endpoints, built around one idea: the prompt cache is
the shared memory of a team of agents.**

[![ci](https://github.com/Anemos-labs/Sleipnir/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Anemos-labs/Sleipnir/actions/workflows/ci.yml)
[![CodeQL](https://github.com/Anemos-labs/Sleipnir/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/Anemos-labs/Sleipnir/actions/workflows/codeql.yml)
[![release](https://img.shields.io/github/v/release/Anemos-labs/Sleipnir?include_prereleases&sort=semver)](https://github.com/Anemos-labs/Sleipnir/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/Anemos-labs/Sleipnir)](go.mod)

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

## See it

`sleipnir demo` (on a terminal; `--scenario shop` anywhere) runs a team of nine agents through the real harness against a mock
endpoint (no key, no network, about twenty seconds; on a terminal you watch it happen in the live cockpit), and `sleipnir replay`
plays any recorded session back. The pictures below are the terminal's own screens,
drawn by the code from recorded sessions and nothing else (`scripts/record-demo.sh`), not mock-ups: the swarm's, the cache's and the
fold's from the event log of that demo, the chat's from a transcript of a chat session. The model in a recording is a
script, so it says nothing clever; everything around it is the real harness: git worktrees, the merge queue and its checks, the mail
router, the cache planner, the governor, the accounting. In the swarm's recording the cache lives 25 seconds instead of minutes, so you
can watch it cool.

<p align="center"><img src="docs/media/swarm.svg" alt="The swarm cockpit: a pixel horse with eight legs, one prefix shared by eight riders, the agent table with a prompt bar for each (the part they inherit bright, their own dimmer), a gantt of the last minute with mail, compactions and a stuck agent marked, the task board, the verified merge queue, mail, the governor" width="760"></p>

**The swarm** (`sleipnir watch`, `sleipnir replay`). Three scouts read the same prefix at once and pay for it once; four workers
each get a git worktree and a scope; the backend mails the frontend what it will serve; the repetition guard tells a worker that has
run the same failing check four times that it is going in circles; the provider loses its cache and every agent that had a warm
one notices; and the merge queue sends the frontend's piece back because, merged with the catalogue's, the shop has two default
ports, each of which was fine in its own tree. The horse has eight legs, one for each rider on the shared prefix: a leg lifts while
its worker runs a tool.

<p align="center"><img src="docs/media/chat.svg" alt="The chat: a goal typed into the input box and sent; the answer streaming in as markdown with a heading, a list and a code block; a tool line for every call (a read, a go test that fails with the tail of its output, an edit shown as a diff with line numbers, the tests passing); a permission question that a letter does not answer (it lands in the input box) and the key 1 does; the status line with its spinner, elapsed time, tokens, cost and what the cache saved; the prompt stack bar bright where the provider read it from its cache, with the cache's clock; the hit-ratio sparkline with a compaction and a cache break marked; the thread folding into one line; a second goal cancelled with Ctrl-C" width="760"></p>

**The chat** (`sleipnir chat`, the program you are in most of the time). A goal is typed into the box and sent; the answer streams
in as markdown (a heading, a list, a code block); every tool call is a line of its own: a read, `go test` failing (a `✗` and the tail
of its output), an edit as a diff with line numbers, the tests passing. The edit asks first, and the question is answered with the
key `1`: it takes numbers (and the arrows and enter), never letters, because a letter is what a half-typed sentence is made of and a
sentence must not be able to answer for you. The person's hand goes to `y` first, as it does at other tools' prompts; it lands in the
input box and the question says why (your typing goes to the prompt until you pause), so it is deleted, and `1` answers. Under the
thread is what only this harness shows: the status line (spinner, verb,
elapsed time, tokens, cost, and what the cache saved at list price), the prompt stack bar, bright where the provider read it from its
cache, with the cache's clock, and the hit-ratio sparkline with `⚠` where the provider lost its cache (the line above it says what the
miss cost) and `◆` where the thread was folded into a one-line resume, which stays in the scrollback as a record. A second goal ends
in Ctrl-C, which cancels the turn and keeps the session.

*What is real and what is scripted.* The picture is the chat program itself, the code `sleipnir chat` runs, drawn into a terminal
emulator from a transcript of a session (`docs/media/chat/transcript.jsonl`; `scripts/record-demo.sh --new-chat` records another),
not a mock-up. The session behind it is the real harness: the tools (`go test` really ran, in a small project, and failed and then
passed), the permission engine, the cache planner and the accounting, and the numbers on the screen are the ones they worked out (the
dollars are at the mock model's list price). The model is a script served by the repository's mock endpoint, so it says nothing
clever, and the person is a script too (the keys and the pauses to read). Four things are arranged: the provider drops its cache once,
the endpoint stalls part of the way through the second answer (so that there is a turn to cancel), the compaction limit is set low
enough for a fold to happen within twenty seconds, and the clock is designed rather than measured (a fast model's first token and
speed, a key every 55 ms, the real time of each tool) so that a busy machine cannot stretch the recording; what happens, and in what
order, is the session's own. `scripts/record-demo.sh --check` fails when the file is not what the code draws from the transcript.

<p align="center"><img src="docs/media/cache.svg" alt="The cache of one agent: its prompt drawn by layer, bright where the provider read it from its cache; the hit ratio of every request with the compaction and the cache breaks marked; the thread folding into a resume; the anomalies the harness saw" width="760"></p>

**The cache** (press `c`). What only this harness knows: the prompt of one agent by layer, sized by tokens and bright where the
provider read it from its cache (a light runs along it when an answer arrives); the clock of the cache; the hit ratio of every
request with `◆` for a compaction and `⚠` for a break; the thread folding into a one-line resume; and, when the provider drops its
cache, the layer where the prompt stopped matching, what was expected, what was read and what the miss cost.

<p align="center"><img src="docs/media/fold.svg" alt="A compaction at a cold moment: the thread folds step by step into a resume, and the harness says the cache was cold, so the rewrite cost nothing extra" width="760"></p>

**A compaction at the cheapest moment.** The model proposes a patch, the harness validates it and holds it until the moment it is
cheapest to apply. Here the agent was busy building while its cache cooled, so the commit was free (`fork · the cache was cold`);
the other workers' compactions were made while their caches were warm, which is a declared, priced rebase and is shown as one.

Try it: `sleipnir demo` (the last screen stays until you press `q`; `c` `m` `b` `o` choose the screen), then `sleipnir replay latest`
to play it back (space pauses, the arrows seek, `+` and `-` change the speed). Stills for places that do not play animation: [swarm](docs/media/swarm.png),
[chat](docs/media/chat.png), [the chat's question](docs/media/chat-ask.png), [cache](docs/media/cache.png), [fold](docs/media/fold.png).

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
# 1. install (Linux/macOS)                                   (or: go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest)
curl -fsSL https://raw.githubusercontent.com/anemos-labs/sleipnir/main/scripts/install.sh | sh
sleipnir demo                          # no key, no network: on a terminal, watch nine scripted agents build a shop in git worktrees (20 s, the live cockpit: the cache, the mail, the merge queue), then the bill
sleipnir demo --scenario handbook      # a one-second survey by a scripted team and the bill, as text (what a pipe gets)
sleipnir replay latest                 # play a recorded session back as the cockpit; `sleipnir watch` shows one being written

# 2. point it at a model. Heimdall is the recommended start; built in too: OpenRouter, OpenAI, Anthropic, Together, Fireworks, Groq, Cerebras, DeepInfra,
#    and local servers with no key (ollama/<model>, lmstudio/, llamacpp/, vllm/). Any other OpenAI-compatible or Anthropic Messages endpoint is a config entry.
export HEIMDALL_API_KEY=...            # or OPENROUTER_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY / TOGETHER_API_KEY / ...
sleipnir init --user                   # your providers and permission mode (project files cannot set these unless trusted)
cd your-project && sleipnir init       # shareable project settings, AGENTS.md (used once you trust the project: below)
sleipnir trust                         # what this project's own files would add to every prompt; `trust add` keeps your yes until one of them changes
sleipnir models qwen --tools           # every provider with a key at once: search words, --reasoning, --max-price, --min-context, favorites (models fav add REF)
sleipnir doctor --model <model> --deep # measures streaming, tools, cache reporting, granularity, warm-up needs

# 3. work
sleipnir chat                          # interactive; /model (a searchable menu) /status /permissions /trust /cost /context /compact /agents /rewind /diff /plan; --resume ID or --continue
sleipnir run "fix the failing test in ./server"
sleipnir swarm 8 "add pagination to every list endpoint and update the client" --verify "make test"
sleipnir swarm 8 "..." --verify "make test" --isolation worktree   # each writer in its own git worktree; finished work goes through a verifying merge queue
sleipnir swarm 8 "..." --verify "go test {dirs}" --isolation worktree   # {dirs}: each task is verified on the directories it may touch
sleipnir swarm 8 "..." --budget-usd 20 # a swarm stops at US$50 unless you say otherwise (swarm.budget_usd; 0 in your own file removes the cap)
sleipnir swarm 8 "..." --role-model manager=anthropic/claude-opus-5-5 --role-model compactor=heimdall/qwen/qwen3.8-flash-next   # a model per role; one model for all is the default
sleipnir schedule add --cron "0 9 * * 1-5" "summarize yesterday's commits"; sleipnir daemon   # goals on a schedule, run headless
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
thing on your endpoint, and holds the first real measurements (one marketplace, one model family: prompts that do not drift and
workers that start warm, on an endpoint whose own cache is erratic).

## Providers

| dialect | status | notes |
|---|---|---|
| OpenAI-style chat completions (OpenAI, Heimdall, OpenRouter, vLLM, SGLang, ...) | supported; measured against Heimdall (nine models, `docs/VALIDATION.md`) | automatic prefix caching, routing key for engine affinity, exact gateway costs, reasoning replay, optional token capture |
| Anthropic Messages (Anthropic, and gateways that speak it) | implemented: dialect `anthropic` (`docs/CONFIGURATION.md`) | explicit breakpoints (max 4, 20-block lookback, 5m/1h TTL), preserved thinking, turn-scoped hot tail; per-gateway switches for what a route drops (`cache_control`, thinking, mid-conversation system text); measure your endpoint with `sleipnir doctor --deep`; one live measurement, of Heimdall's `/messages` route |
| OpenAI Responses | not built yet (`openai-responses` is accepted in config, but a session that uses it stops with an error) | |

## Safety model

Tool output, web pages, file contents and mail are data, never instructions; pins and notes are user-role context, never
system. Permissions are an engine (modes, rules, shell-syntax analysis, role profiles), not a prompt; writes pass a lease
guard, a content-hash staleness check and a checkpoint; verifier files are never visible to a training policy. What a
repository brings (config keys that redirect keys or run code, instruction files, skills, slash commands, agent
definitions, hooks, MCP servers) is ignored unless you trust the project: `--trust-project` for one run, or a yes that
`sleipnir trust` keeps for exactly the files you saw (a hash of them: a pull that changes one is a new question), and a
project's MCP server is started only after you approve that exact entry. Provider keys go only where you allowed and are held out of the environment the
commands inherit. Real isolation for untrusted repositories needs an OS sandbox: `docs/SECURITY.md` is the threat model, what is
hardened on each OS and how to confine harder.

## Status

Built and tested: the layered cache engine, the swarm (typed board and mail, leases, git-worktree isolation, a verifying merge queue),
the terminal programs (`chat`, the `watch` cockpit, `replay`), MCP, skills, hooks, the RL environment, the permission engine and project
trust. About 4,200 tests, 100 fuzz targets and a nightly run (every fuzz target, the suite three times under the race detector, a thousand
random mixtures of endpoint faults, a twenty-thousand-step soak) back it; CI runs on Linux (amd64 and arm64) and macOS, and builds
Windows, which is informational. It has been measured on real models (nine models of one marketplace, `docs/VALIDATION.md`), compared before
and after its own fixes on a fixed suite of verifiable tasks (`docs/BENCHMARKS.md`, one model so far: about a third cheaper per task, the pass
rate within noise, and one measure that got worse and is open), and used for real work: a register of forty-two findings from the benchmark,
the end-to-end tests and four logged sessions, each with a fix and a test (`docs/DOGFOOD.md`).

Not done: a first release (the version is unreleased and no licence is chosen yet), Windows support for the permission engine, the OpenAI
Responses dialect, the scale ladder on a live endpoint and the rest of the planned dogfooding, and the second half of the benchmark. Those and
what the owner has to switch on are in **`docs/ROADMAP.md`**, which is where an agent or a person picking this up should start.

## Documentation

* `docs/CONFIGURATION.md` - where configuration lives, every key, providers and their options, permissions, trust, worked examples
* `docs/EXTENDING.md` - instruction files, skills, slash commands, agent definitions, hooks, sessions and resume, compaction
* `docs/MCP.md` - MCP servers: configuration, the approval model for project servers, tools, prompts, `sleipnir mcp`
* `docs/SECURITY.md` - threat model, what is protected and what is not, hardening per OS, confining harder
* `docs/CLI.md` - every command and flag (generated from `--help`), slash commands, exit codes
* `docs/ARCHITECTURE.md` - system map, package map, one request end to end, decisions
* `docs/CACHE-DESIGN.md` - the layered generational cache, planner, guard, simulation results
* `docs/SWARM-PROTOCOL.md` - board, mail, leases, roles, warm gate, verifier-gated done
* `docs/TRAINING-DATA.md` - the RL environment, rewards, formats, governance
* `docs/VALIDATION.md` - validating against real endpoints
* `docs/BENCHMARKS.md` - the fixed task suite, how to run it, how a change is judged, what it found; `docs/DOGFOOD.md` - what real use found
* `docs/TESTING.md` - what each kind of test guards and what runs it; `docs/BUILDING.md` - conventions; `docs/UX.md` - the terminal interface
* `docs/ROADMAP.md` - the state of the project, what only the owner can do, and what to take next
* `CONTRIBUTING.md`, `CHANGELOG.md`
* `docs/REPO-SETUP.md` - the GitHub side: protected `main`, what CI enforces, releases, and every setting to apply
* `docs/research/` - the landscape, provider caching and swarm/training research this design rests on
* `docs/reviews/` - adversarial reviews and what was done about them

## Development

```sh
make build test race lint sim
scripts/check.sh                       # what CI runs (make check): format, tidy, dependency list, pins, generated docs, vet, build, race tests, cross-compiles
```

Go 1.25, standard library plus `golang.org/x/{net,sys,term}`. See `AGENTS.md` and `docs/BUILDING.md`.

## License

To be decided before the first release.
