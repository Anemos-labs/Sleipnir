# Getting started

The short version is in the README: install, then `sleipnir`. This page is the rest of what the command line does.

```sh
# 1. try it with no key and no network
sleipnir demo                          # no key, no network: on a terminal, watch nine scripted agents build a shop in git worktrees (20 s, the live cockpit: the cache, the mail, the merge queue), then the bill
sleipnir demo --scenario handbook      # a one-second survey by a scripted team and the bill, as text (what a pipe gets)
sleipnir replay latest                 # play a recorded session back as the cockpit; `sleipnir watch` shows one being written

# 2. point it at a model. Heimdall is the recommended start; built in too: OpenRouter, OpenAI, Anthropic, Together, Fireworks, Groq, Cerebras, DeepInfra,
#    and local servers with no key (ollama/<model>, lmstudio/, llamacpp/, vllm/). Any other OpenAI-compatible or Anthropic Messages endpoint is a config entry.
sleipnir login                         # paste a key (again): kept in ~/.sleipnir/auth.json, mode 0600; or export HEIMDALL_API_KEY / OPENROUTER_API_KEY / ... instead (it wins)
cd your-project && sleipnir init       # shareable project settings, AGENTS.md (used once you trust the project: below)
sleipnir trust                         # what this project's own files would add to every prompt; `trust add` keeps your yes until one of them changes
sleipnir models qwen --tools           # every provider with a key at once: search words, --reasoning, --max-price, --min-context, favorites (models fav add REF)
sleipnir doctor --model <model> --deep # measures streaming, tools, cache reporting, granularity, warm-up needs

# 3. work
sleipnir chat                          # (the same as `sleipnir` alone on a terminal) interactive; /model (a searchable menu) /status /permissions /trust /cost /context /compact /agents /rewind /diff /plan; --resume ID or --continue
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

