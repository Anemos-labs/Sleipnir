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

## What goes wrong with local models (read 2026-10-02)

* **Silent truncation.** Ollama picks its default window from video memory (4096 tokens below 24 GiB, 32768 up to 48 GiB, 262144 above), its OpenAI-compatible endpoint ignores `num_ctx`, and a prompt longer than the window is cut without a word. A big system prompt and tool schemas fill 4096 tokens at once, the model loses its instructions for the tool format, and the symptom is raw `<function=...>` text where a tool call should be. When the user turn is the part cut, Ollama answers HTTP 500 "no user query found" after the first tool call ([ollama#18107](https://github.com/ollama/ollama/issues/18107), [openclaw#4028](https://github.com/openclaw/openclaw/issues/4028), [Ollama silently truncates your prompts](https://repofold.dev/blog/ollama-silently-truncates-your-prompts)). The fix on the server is `OLLAMA_CONTEXT_LENGTH` or `PARAMETER num_ctx` in a Modelfile; the sign in the client is `usage.prompt_tokens` stopping at the window while the prompt grows. Sleipnir watches for exactly that (`agent.noteTruncation`), assumes 8192 tokens for a model on a server of this machine, and has `options.context_window`.
* **Catalogues with ids only** (Ollama, LM Studio, vLLM list nothing else) give no price and no window, so nothing a harness plans with is known: say so rather than guess in a way that hides it.

## What the harness literature says helps (read 2026-10-02)

* **A plan the harness owns.** A tool that holds a todo list, with the current plan shown back to the model before every turn, and a first action of "write the plan" for any task of about three steps or more. For a 30B open-weight model planning raised the SWE-Bench success rate by 11.6 points, and a predefined, fixed tool set by 15.0 ([Beyond the Model](https://arxiv.org/abs/2609.32459), [An Empirical Study of Harness Design](https://arxiv.org/html/2609.20804v1)). Sleipnir has no plan tool yet; it is the best-evidenced thing to build next, and the tool list is already fixed and identical for every agent.
* **Verification inside the edit.** Edits are the most common failed tool call; returning the compiler's verdict with the edit result saves the separate verify step and the model's habit of skipping it.
* **Closed-loop repair of tool calls** lifts weak models across sizes: the harness fixes what it can and says plainly what it could not.
* **Self-improving skills** (Hermes writes skills from complex repeatable work and nudges itself to save memory) work best with a person correcting, and a self-written skill can drift into unsafe behaviour ([Practice Makes Unsafe](https://arxiv.org/pdf/2608.12851)): a skill the model writes should be shown to the person before it is kept, as Sleipnir's memory notes are.

## What follows for Sleipnir

1. Every flag of the chat has an equivalent inside it, so nobody restarts to change a setting (`docs/CLI.md`, "Inside the chat").
2. Sessions are a first-class thing to list, resume and start anew from inside the chat (opencode's `/sessions` and `/new`); branching a session (pi's `/fork`) is worth building on top of `agent.Snapshot`, which already restores a thread.
3. A weak model needs the harness to do its planning: a `plan` tool whose list is shown back every turn, briefs, guards and a verifier, not a longer prompt.
4. Setup is a conversation that ends with a working chat (done: `sleipnir`), and a key is never a reason to leave the terminal.
