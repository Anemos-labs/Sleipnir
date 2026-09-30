# Changelog

All notable changes to Sleipnir are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [0.1.0] - unreleased

The first release.

### Prompt engine

- Layered, generational prompt cache. Seven layers ordered by volatility: constitution and tools (G0), shared
  project pin (G1), role pin (G2), notes (G3), spine (G4), thread (G5) and a small always-fresh hot view (G6).
  Every change to a stable layer is a declared, priced event; a guard flags silent cache regressions.
- Compaction as a patch: a background compactor forks the agent's own request (same cache), returns a JSON patch,
  and the harness validates it and commits it at the cheapest moment (or at a cold moment for free). Masking before
  summarising; nothing is lost (`recall`).
- Cache models for OpenAI-style automatic prefix caching (routing keys, gateway-reported cost) and Anthropic explicit
  breakpoints (max four, 20-block lookback, 5m/1h TTL), including preserved thinking, turn-scoped system messages
  and declared rebases.
- `sleipnir sim`: a policy simulator with printed assumptions, and the rule it found: layering wins when the shared
  pin is dense.

### Swarm

- One manager and up to dozens of workers over one repository, dispatched from a task card without a briefing.
- A typed board (immutable snapshots), rate-limited typed mail through a router, write leases and task scopes,
  content-hash staleness on every edit, checkpoints, a writer cap, and harness-owned "done" (a task cannot leave
  `doing` without the evidence gate and the verifier).
- Warm gate (one primer per cold prefix), governor (requests per minute, priorities, `Retry-After`, AIMD), watchdog
  and failure recovery.
- Worktree isolation (`swarm.isolation: worktree`, `--isolation`): every writer edits a git worktree of its own, and its
  work reaches your checkout through a serial merge queue that verifies every integration and undoes a merge whose check
  fails; the result is applied at the end of the run (as edits, or as commits with `--commit`) and is never lost when it
  cannot be. The manager is told so, so it does not ask you to merge anything. Several managers, or a swarm and your own git,
  may work in one repository at once: git itself fails a command that lists the worktrees while another process is creating or
  removing one ("failed to read .../commondir: Success", found by CI; a vanished `worktrees` directory; a `locked` file that went
  between the check and the read), and the harness waits those moments out like any other lock. A worktree made with a new branch
  is a branch and then a worktree here, so that a repeated add does not meet the branch the failed one left, and a branch made for
  a worktree that could not be made is taken back. Under load, four processes doing this at once failed 11 of 60 runs before and none of 100 after.
- `--verify` may contain `{dirs}`, the directories a task's scope covers, so tasks of a decomposed job are verified on their
  own work (`go test {dirs}`): in an isolated run a command over the whole repository cannot pass until every part is merged,
  and a real swarm of three workers deadlocked on it.
- Optional mailman (`swarm.mailman`, `--mailman`): worker mail is digested in bursts by a small read-only agent that has
  a model of its own if you give it one; the router's checks are unchanged, a mailman that is absent or slow costs delay,
  never mail, and every digest names its senders.
- The manager is supervised: in a batch run its final answer is held while its workers or reviews are unfinished (a
  worker that is only writing its closing message is waited for, not counted), in a chat it is woken, with a short
  harness-written note, when workers finish or fail.
- Bounded by default: a swarm has a built-in budget (US$50, `swarm.budget_usd`, only you can raise or remove it), a
  ceiling on its size (`swarm.max_agents`), and peer mail can wake one worker only 40 times per task; budget checks fail
  closed. Read-only roles are denied the background-job tools by name.

### Providers

- OpenAI-style chat completions (OpenAI, marketplaces such as Heimdall and OpenRouter, vLLM, SGLang), with reasoning
  replay, optional token-id capture, and exact gateway costs.
- Native Anthropic Messages adapter with explicit cache breakpoints and per-gateway limits declared as options.
- `sleipnir doctor` measures a real endpoint: streaming, tools, cache reporting, block granularity, warm-up needs. Its
  cache verdict counts nine repeat requests (`yes`, `partly`, `NO`), because a marketplace cache can hit on some and miss on
  others.
- A repetition guard: an agent that makes the same call with the same failing result eight times among its last twenty
  calls is stopped (`agent stuck`), after being told at the fourth. A first run against a real 2B model made two hundred
  requests over two refused commands.
- A run with no one to ask (`run`, a swarm, a rollout) says so in every refusal that needs approval, so a model stops looking
  for another way to the same action instead of spending its steps on it.
- Every model of a session, roles' models included, is described from its endpoint's catalogue when the built-in table
  does not know it, and `session.start` records the prices each was described with, so `inspect` shows the gateway's
  figures instead of a generic estimate.
- What a person is told is specific: the notice while a request is repeated names the failure, its HTTP status and what the
  endpoint said (`server (http 502): The provider returned an error … [provider X]; retrying in 877ms`), and a cache-miss
  warning says when the prompt prefix did not change, so the miss was the endpoint's (a real marketplace served 5% to 96% of an
  identical prefix, request after request).

### Tools, permissions and extensions

- File, shell, web and recall tools; every agent sends the same tool list and roles are restricted at run time by the
  permission engine (modes, rules, shell-syntax analysis, role profiles, hard denies for credentials).
- Skills (listing in the shared layer, loaded on demand), custom slash commands, markdown role definitions, hooks
  (SessionStart/End with the reason a session ended, UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest,
  Notification, Stop for the agent you talk to and SubagentStart/Stop for a swarm's workers, PreCompact/PostCompact around
  every compaction, automatic ones included), MCP servers (tools frozen per
  session, per-entry approval for project servers, prompts as slash commands, `sleipnir mcp`, `/mcp`), checkpoints
  and rewind. Repository-supplied skills, commands, roles, hooks and instruction files are read only when the project
  is trusted, and writes to the directories that hold them always ask.

### RL environment

- The harness is an RL environment: `sleipnir rl taskgen|tasks|rollout|eval|serve|reward|export|expand|verify|show`.
- Exact-prompt capture (content-hash manifests and wire hashes), token ids and logprobs from self-hosted policies,
  verifier rewards in a clean checkout, hack detectors, cost repriced under a target provider, per-role rewards,
  GRPO/RLOO/anchor advantages, and exporters for steps, tokens, groups, SFT, DPO, KTO, ATIF and a lossless canonical
  archive. Redaction happens at export; raw logs stay private.
- Sampling seeds are 31-bit: a real marketplace refused 63-bit and 53-bit seeds ("outside the 0 to 2147483647 this
  endpoint accepts"), which failed every rollout of a first run against it.
- Task generators: mined from git history, mutation, composite swarm tasks and recall (memory) tasks.

### Sessions

- Every finished turn saves a snapshot of the agent (thread, notes, spine) in the event log. `sleipnir chat|run --resume
  <id|latest>` (or `--continue`) picks the conversation up where it stopped, in the same session directory and log; the
  first request writes the cached prefix again, once, and says so. Swarm sessions cannot be resumed yet.
- `/compact [focus]` folds the older thread on request, with an optional hint about what to keep in view; it is a declared
  and priced rebase like any other, and fires the PreCompact and PostCompact hooks.
- `sleipnir sessions` marks the sessions that can be continued.

### Security

`docs/SECURITY.md` is the threat model: what is protected, what is not, what is hardened on each OS, how to confine harder.

- Hostile repositories: instruction files, skills, commands, roles and hooks from a repository are read only when the
  project is trusted; symlinks and imports cannot leave the project; invisible Unicode is stripped; repository text is
  rendered "(scope, unverified)". A repository's configuration can add to your `permissions.deny`/`ask` rules and your
  hooks, never remove them, and cannot define providers unless trusted. A project's MCP server starts only after you
  approve that exact entry.
- Keys: the provider key goes only where you allowed it (the provider's own host, loopback, or a host in your user
  configuration), never over plain `http` to another machine, and no redirect carries it off the original origin. It is held
  out of the harness's environment, so nothing the harness starts inherits it; on Linux the process is non-dumpable and its
  initial environment is erased, on macOS debugger attach is refused. The RL rollout server sends a policy key only where its
  operator listed.
- Endpoints are not believed: responses are bounded as they are read, silent servers are timed out, hostile usage counters,
  costs and catalogue prices are clamped or dropped, error text is sanitised.
- The permission engine hard-denies credential paths; Ask rules apply even in bypass mode; unattended sessions refuse to ask;
  nothing defaults to allow-all (an agent, swarm member or tool environment built without a permission policy is denied).
- The prompt engine treats what it did not write as data: a compactor may write only its own notes sections, every text that
  enters a layer is escaped and bounded, what a person typed is pinned in full (beginning and end above the bound), and the
  harness's own task text is never pinned as the user's word. Tool results per turn are budgeted (the excess is saved behind a
  recall handle) and a tool call has a deadline.
- Session state is private (0700/0600), blobs are verified by hash, a damaged log line is recorded rather than truncating the
  rest, a session directory has one writer at a time, and checkpoint restore refuses anything that would write outside the
  project. Terminal output drops escape sequences and control characters that a model, tool, file or page wrote.
- Swarm: mail, notes, status lines and alerts are framed and bounded as untrusted peer data; scopes are enforced at write
  time; "done" belongs to the harness; Retry-After is capped everywhere.
- Independent adversarial reviews of the prompt engine, the swarm and the trust boundaries are in `docs/reviews/`, with what
  was fixed (all of it) and what remains.

### Configuration

- Every key in the file format is read by something: the keys of earlier drafts that nothing read are gone (an old file
  that still has them gets an "unknown key" warning with a suggestion). `cache.shared_ttl` reaches the breakpoints of
  single agents and swarms, and provider options are validated per dialect (unknown names warn, wrong kinds and values
  the adapter does not take are errors; a test keeps the list and the code that reads it together).
- `sleipnir config` lists each warning once, with file, line and column; `init --user` invents no provider (`--local-url`
  adds a self-hosted one); `swarm -h` and `-h` everywhere exit 0 and print usage; a bad worker count, a role model for a role
  that does not exist and a budget that is not a positive number are errors that say what was expected.

### Interfaces

- `sleipnir chat`: Ctrl-C cancels the running turn and nothing else, as the documentation said and the program did not: one Ctrl-C
  ended the whole session (and exited 0), because Ctrl-C was registered twice, by `main` for the process and by each turn, and a signal
  goes to every channel that asked for it. It is handled in one place now, and `main` leaves it to the chat (SIGTERM still ends the
  process). At the prompt a first Ctrl-C discards the half-typed line and says how to quit, and a second within two seconds quits
  (recorded as `interrupted`; Ctrl-D and `/exit` are `exit`). Found by the first tests that run the command on a pseudo-terminal
  (`internal/ptytest`, `cmd/sleipnir/e2e_chat_test.go`).
- `sleipnir chat` owns its input in one place, which fixes what the signal bug was hiding: the prompt and an approval question each
  read the terminal in a goroutine that was abandoned when its context ended, so a question cancelled by Ctrl-C took the next line the
  person typed (and raced with the prompt's reader), and a line typed while a turn ran answered the approval question that came
  after it. Now a line typed ahead waits for the next prompt, a question takes only a line typed after it was shown, and a cancelled
  question takes nothing. `run` and `mcp test`, which read nothing else from the terminal, still read their answers themselves
  (`session.TerminalPrompter`, whose cancelled read is no longer left running either).
- The answer stream carries no blank lines that only open a model's turn (some models start a turn of tool calls with a newline;
  a small swarm printed thirty before its final answer).
- The isolated manager's shell refusal says what its shell takes (one plain read-only command at a time), not only that it may not
  edit files: a real manager chained two reads with `&&` and was told reading was forbidden.
- `sleipnir chat` (slash commands, Ctrl-C per turn, Ctrl-C twice at the prompt to quit), `run`, `swarm`, `recon`, `init`, `config`, `sessions`, `models`,
  `demo` (a scripted 14-agent team on a mock endpoint, no key needed) and `inspect` (a live or after-the-fact web
  dashboard: layers, hit ratio, compactions, swarm, cost; for a swarm also its worktrees and merge queue, the mailman and
  the manager's supervision).

### Found by running it on real models

A benchmark (`bench/`, `scripts/bench.sh`, `sleipnir rl report`) run on real models found what the tests did not. Each line is a
defect the runs showed, with the evidence, and what changed.

- **Bash refusals were the biggest waste.** In 94 episodes on four models, 77% hit a permission refusal and there were 197 in all:
  45% `cd` to a path the model guessed (`/workspace`, `/repo`, `/home/user`), 14% paths the engine cannot know (`$(pwd)`,
  `$OLDPWD`, a loop variable), 6% scratch files in `/tmp`, 10% `sed -n 'N,Mp' file`, the way models read a range of lines.
  The constitution now says where the agent starts and what to do about paths and scratch files (**a declared prompt change, priced
  below**); a refusal outside the workspace names the workspace; an unattended run gets the engine's reason and what to do instead
  of one generic line; and `sed` is allowed as a reader of lines (`sed -n '120,160p' file`: addresses with `p d = q Q` and nothing
  else, run for real by the test that checks allowed commands change nothing), while `s`, `w`, `e`, `-i` and every other form still ask.
- **Tool names garbled by chat-template tokens** (`bash<|channel|>commentary`, `functions.read`) ended as unknown-tool errors. The
  agent repairs a name only when the repaired form is a registered tool, records it (`tool.call` `as`), and an unknown tool now
  gets the list of tools and the closest name.
- **The answer is no longer cut to six lines when it was a question**: the finishing rule says an answer to a question, a review or
  an explanation is given whole. A command that times out says what to do next (a longer timeout while the cap allows it, else
  `run_in_background`).
- **The reward detector flagged correct work as hacking**: a workspace below `~/.something` looked like the agent writing into the
  home's dotfiles, and a scratch `debug_test.go` (written to reproduce the bug, which is what the task asks for) counted as editing a
  protected path. A hack flag zeroes the outcome and keeps the episode out of the training export, so four of twenty passing or
  near-passing episodes were lost. Scoring now reads the workspace from the run's own log, protected paths are judged by the final
  diff (a new file under a protected glob is scratch; the verifier discards it), and `rl reward --redetect-hacks` repairs data that
  was scored before.
- **Two processes that shared a work directory killed each other's setup** (one marker per directory, swept by the first to finish):
  the marker is unique per build. A test that put fake clocks and real file times in one assertion failed for good after a date
  (`inspect`); the glob-cost test was a 100 ms stopwatch that failed at load 37 and is now a hang guard.

*Pricing the prompt change* (`sleipnir sim --mode pins`, the Anthropic-like cache model, 20 workers): the constitution grows by
382 bytes (about 95 tokens: 829 to 924 for one agent, 1,064 to 1,159 for a swarm; about 1.6% of a first request of 5,900 tokens),
which every request reads at the cached price, and the first request after an upgrade writes the prefix anew, once per session.
The sweep puts the swarm's cost against a naive harness at about 0.6 points per thousand tokens of shared pin, so this is about
0.06 points; what it buys is measured by the before/after run in `docs/BENCHMARKS.md`.
