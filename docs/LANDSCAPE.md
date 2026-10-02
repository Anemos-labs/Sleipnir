# What other harnesses do, and what people want

Notes from reading other coding-agent harnesses and what their users say, kept so that a choice here can be checked against them. Dated 2026-10-02; the field
moves monthly, so treat a row as a lead to verify, not a fact to quote.

## What the best ones do that Sleipnir should match

| harness | what it does | Sleipnir today |
|---|---|---|
| [opencode](https://opencode.ai/docs/tui/) | `/connect` adds a provider and its key; `/models` lists models; `/sessions` (aliases `/resume`, `/continue`) switches session; `/undo` and `/redo` step back and forth through messages; `/new` starts fresh; `/share`; `/themes`; `/init` writes AGENTS.md. Credentials in `~/.local/share/opencode/auth.json` | `sleipnir login` and the first run store keys; `/model` has a searchable menu; `/rewind` restores files; no `/sessions`, `/new`, `/redo`, `/share`, themes or `/init` in the chat |
| [pi](https://github.com/earendil-works/pi) | four tools and a very short prompt; credentials in `~/.pi/agent/auth.json` (0600) via `/login`; a stored key outranks the environment; sessions are a **tree** you can branch (`/tree`, `/fork`); everything else is an extension | stored keys in the same shape; the environment outranks a stored key here; no session branching |
| [Hermes Agent](https://hermes-agent.nousresearch.com/docs/user-guide/configuration) | keys in `~/.hermes/.env`, settings in `config.yaml`; memory that persists between sessions; runs on a server and answers over chat gateways | memory (`memory` tool) and schedules (`schedule`, `daemon`) exist; no gateway |

## What people complain about

* "Almost right, but not quite": 66% of developers name it as the biggest frustration with AI tools (Stack Overflow survey, quoted by [Faros](https://www.faros.ai/blog/best-ai-coding-agents-2026)). For a weak model this is the common case; the harness's job is to make "not quite" visible (a verifier, a refused "done", a note when a test is rewritten to match the bug).
* Cost and limits: usage cut off mid-task, credits gone faster than expected. Sleipnir shows tokens, dollars and what the cache saved while it works, and a budget stops a run (`--budget-usd`).
* Long multi-agent runs hit a wall when the context fills and nothing degrades gracefully ([agents-radar](https://github.com/stevenko2002/agents-radar/issues/1572)). Compaction at the cheapest moment, a spine of one-line resumes and `recall` are the answer here.
* Open-weight models: tool calls that arrive as plain text under the wrong parser, repeated plans that never act, editing before reading, small models generating invalid tool calls far more often than large ones ([AgentFloor](https://arxiv.org/pdf/2605.00334), [Modal's survey](https://modal.com/resources/best-open-source-models-autonomous-coding-agents)).
* Hermes: slow, busy start-up; hard caps on memory that return an error; a CLI-first setup that assumes a server ([review](https://www.eesel.ai/blog/hermes-agent-review)).

## What follows for Sleipnir

1. Every flag of the chat has an equivalent inside it, so nobody restarts to change a setting (`docs/CLI.md`, "Inside the chat").
2. Sessions are a first-class thing to list, resume and start anew from inside the chat (opencode's `/sessions` and `/new`); branching a session (pi's `/fork`) is worth building on top of `agent.Snapshot`, which already restores a thread.
3. A weak model needs the harness to do its planning checks: briefs, guards and a verifier, not a longer prompt.
4. Setup is a conversation that ends with a working chat (done: `sleipnir`), and a key is never a reason to leave the terminal.
