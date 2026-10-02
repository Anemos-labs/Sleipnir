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
  On real models (a benchmark of 349 sessions on four of them) between a tenth and two thirds of the patches were refused, and the
  agent compacted mechanically instead. Three causes are fixed: a field of the wrong type (`notes` as an object, `keep_from` as a
  number) costs that field and not the patch, a reply that holds a valid patch and also calls a tool uses the patch (the call is never
  run), and a patch whose JSON is broken is refused as that, not as having no `keep_from`. `sleipnir rl rollout --thread-soft-limit N`
  sets the size at which an agent's thread is considered for compaction, so that how soon is a setting to compare (a soft limit
  above the default hard limit of 60,000 tokens raises the hard limit with it, unless that was set too: the hard limit forces the
  compaction first, so a soft limit above it compared nothing).
- Cache models for OpenAI-style automatic prefix caching (routing keys, gateway-reported cost) and Anthropic explicit
  breakpoints (max four, 20-block lookback, 5m/1h TTL), including preserved thinking, turn-scoped system messages
  and declared rebases.
- `sleipnir sim`: a policy simulator with printed assumptions, and the rule it found: layering wins when the shared
  pin is dense.
- Declared layout change `sleipnir-kv/3`: two user messages in a row (a request cancelled while the model was thinking, then another
  one) still reach the provider as one message, as the providers require, and the second is now set off from the first by a blank line
  (in the text of its own block: a block of nothing but whitespace is refused by some providers). A chat template that lays the parts
  of a message end to end ran them together ("fix the parserdo the scanner instead"). No layer G0-G2 changes and nothing that was
  sent before does, so no session pays for it; only a thread with such a pair renders differently. The canonical session of the render
  goldens has such a pair now.
- Declared byte change in `core.MarshalStable` (what wire bodies, manifests and the hashes of recorded prompts are made of): a byte
  of invalid UTF-8 in a string is written as the replacement character itself, whatever Go built the binary. The encoding/json of
  Go 1.26 and before wrote the escape `\ufffd` for it and Go 1.27's writes the character, so a binary built with one and a log
  written with the other hashed the same block differently (the replay check of a recorded prompt, the cache key of a thread). The
  character is what every Go already wrote for a U+FFFD that was in the text and what `Canonical`, which decodes first, always
  produced. Only text that contains invalid UTF-8 (a binary file read as text) has other bytes, in the golden
  `canonical_shapes.txt`; no layer G0 to G2, no tool schema and no valid text does, so no session pays for it and `sleipnir sim` has
  nothing to price. The Anthropic adapter's own writer is unchanged: it never depended on the Go version.

### Swarm

- A task the manager creates without a description is created and the answer says what the worker will not see (it sees only the title and
  files), so a weak manager gets one chance to write the brief. It is a hint, not a refusal: a title can say it all.
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
- The bound on peer mail counts every run that mail starts, not only the ones that find the worker idle. Mail left waiting by a run
  that ended (it came in while the worker ran, or in the moment after the board said idle and before the harness looked at the
  inbox) started a run that nothing counted, so two workers whose mail always arrived while the other was running could keep each
  other going. The first run on GitHub caught it once (four runs where the bound is three; one in about a hundred under load, and
  every failure was that restart). The harness now knows who wrote what is waiting, the manager's and the harness's mail still
  always start a run, and the check, the wake and the count are one step. The tests send the mail from the worker's own last event
  and hold the model's reply while it arrives, so the moment is theirs and not luck's.

### Providers

- Sixteen more providers are built in, and two more local servers: SambaNova, Hyperbolic, Nebius, Novita, NVIDIA, Parasail, Baseten, Chutes, SiliconFlow, Ollama Cloud and OpenCode Zen (hosts of
  open-weight models); Moonshot, Z.ai, MiniMax and DashScope (the labs of Kimi, GLM, MiniMax and Qwen) and Cohere's compatibility route; SGLang and Jan on this machine. Every base URL was asked
  for `GET /models` and answered (a catalogue, or the 401 that asks for a key); none has run on a live key (`docs/PROVIDERS.md`). The provider menu of the first run and `/login` is searchable now
  (it has thirty rows), a bare model id goes to any provider that has a key when none of the usual ones does (a ChatGPT plan comes after them all), and the usage text (`sleipnir` with no arguments)
  lists the providers from the table itself, a test keeps `docs/PROVIDERS.md` naming every one of them with its key variable, and the tests that start like a first run clear the key of every provider
  instead of a list of ten.
- **The OpenAI Responses dialect** (`openai-responses`, `internal/provider/openairesp`): OpenAI's newer route with an API key, stateless (`store: false`), the
  reasoning kept encrypted and sent back with the items it came with, the automatic prefix cache and its cache key, streamed events with limits and the
  adapter contract of the others (`providertest`). The shared error handling of the OpenAI-style adapters moved into `internal/provider`.
- **A ChatGPT plan instead of an API key** (`sleipnir login chatgpt`, provider `chatgpt`, `internal/chatgptauth`): OpenAI's "Sign in with ChatGPT" token sharing for
  open-source apps, as its documentation describes it: OpenID discovery, PKCE with a loopback redirect, dynamic client registration (this app's own, not the Codex
  CLI's), a host id, a refresh token that rotates (renewals are serialized and a renewal another process made is taken from the file), revocation on logout; the plan's
  model list, no price shown, a limit that is reached is not retried. Built from the documentation and tested against a fake issuer and a fake endpoint: **it has not
  met a real account yet** (`docs/PROVIDERS.md`). A Claude plan is not offered: Anthropic's terms do not allow it.
- Five more providers are built in: Gemini (`GEMINI_API_KEY`), Mistral, xAI, DeepSeek's own API and Hugging Face's router (`HF_TOKEN`). `docs/PROVIDERS.md` lists every built-in provider with its key
  variable, its endpoint and what has actually been verified: only Heimdall has been run on a live key.
- A server that cuts the prompt off is noticed. Ollama's OpenAI-compatible endpoint reads only as much as the model's window (4096 tokens on a small graphics card) and says nothing,
  so a weak setup shows up as a model that forgets its instructions and calls tools as plain text. For a model whose window was never told, when the prompt grows by 5% and the tokens
  the server reports do not, the harness says so once, names the window it saw, keeps the conversation inside it from then on, and points at `OLLAMA_CONTEXT_LENGTH` and
  `options.context_window`.
- Local servers are found, not configured: the first run lists a running Ollama, LM Studio, llama.cpp or vLLM beside the hosted providers (no key), and `sleipnir models` and the
  `/model` menu list their models. A model on a keyless server of this machine costs nothing and is assumed to have an 8192-token window (a catalogue of ids only does not say, and
  Ollama cuts a longer prompt off silently), which `providers.<name>.options.context_window` raises. A catalogue entry that carries nothing no longer replaces a known price and window with zeros.
- `/model provider/model` in the chat moves the conversation to another model, keeping the thread, notes, spine and bill (the agent is
  rebuilt and the old one's snapshot restored: the same path as a resume). `/model ` opens a menu of every model of the providers whose key is set (favorites first, typing filters it with the same fuzzy match as the commands); `/model` alone shows the current one. In a team the chat starts the team again on that model,
  with the manager's conversation (a team's workers run on their roles' models). The errors of a first run now say Heimdall is the recommended start.
- `sleipnir` with no arguments on a terminal that is not the chat's (`--help`) and the chat's `/help` fit 80 columns: the descriptions of the commands were up to 139 characters and were broken at the
  right edge with the rest under the names, and the line of `/agents` started its description a column early. Tests hold every line of both to 79 characters and the descriptions to one column.
- The line `sleipnir run` ends on (how long, steps, cost, cache, and the session's directory) puts the directory under the rest, shortened with `~`, when the terminal is too narrow for one line
  (at 80 columns the long path made it three). On a pipe or a log it is the one line it always was.
- Every command's help, tables and errors fit an 80-column terminal. A flag's description was one line of up to 370 characters (`run -h`, `chat -h`), `sleipnir models` a table 99 wide,
  and `rl help`, `login -h`, `config`, an error that names a path and the footer of `sleipnir sessions` broke in the middle of a word. Descriptions are now broken at spaces under their flag, a
  paragraph written in lines of about the width is flowed again as a whole, `sleipnir models` drops the REASONING, cached-price and input-price columns, in that order, when the terminal is too
  narrow for them, and `sleipnir sessions` drops the model and cuts the prompt with an ellipsis. On a pipe or in a file the text is as it was (`docs/CLI.md` is made from it). A test runs every
  command's `-h` on a terminal of 80 columns.
- A test an agent writes is not tampering with the verifier when the verifier is `node --test test/*.test.js`: the pattern handed to an interpreter was taken for the script it runs, so
  any new file under it was flagged `hack:verifier_touched`, which zeroed the episode's reward and kept it out of a training export (2 of 94 episodes of the last benchmark; one had
  passed its verifier). A script under an interpreter is still the verifier, and so is a test file named exactly.
- A model that is refused and has nobody to ask no longer spends the run on ways round the refusal. `sleipnir run --mode accept-edits` refuses `go test` (nobody can say yes): glm-5.3-flash made
  23 more attempts in five minutes (another command each time, so the guard against a repeated call never saw a loop), and minimax-m2.7, unable to run the tests, traced its fix by hand and reported it
  correct (it was not). The refusals of a run with no one to ask now count together, among the last twenty actions: at five the model is told once that nobody is here to approve and to finish saying which
  permission it needed, and at ten the run ends ("10 of its last 10 actions were refused and this run has no one to ask"), with the usual list of what to `--allow`. A worker that is refused
  now and then in a long run of other work never gets there.
- The list a run with no one to ask prints at its end names the edits that were refused as well as the commands, and the hint says what lets each through: `--mode accept-edits` for an edit, a
  rule for a command. It listed only the commands, so a run whose edit was refused (`sleipnir run` in a cron job, a script) was told about `go test` and was refused its edit again the
  next time; the model, meanwhile, had said "I applied the patch". `run --json` carries an edit as `{"Command":"","Path":"...","Times":n}` in `refused_no_one_to_ask`.
- `/fav [provider/model]` stars a model, or unstars it (the session's own when none is named), from the chat: the `/model` menu puts it first at once, `sleipnir models` lists it first, and
  it is kept as `models.favorites` in the user's own configuration (in the session's home, not the machine's: the tests prove it). Favorites could only be changed by `sleipnir models fav add|rm`.
- The chat's lines for the manager's coordination say what was done: `Spawn backend · T1` (who was started, on which task) and `Task accept T1` (the action and the task) where
  they said `Spawn` and `Task accept` alone, three times over in a team of three.
- `/roles ` opens a menu in the chat: the roles that can run on a model of their own, each with the model it runs on now and where that came from, and, once one is chosen
  (it is inserted with its `=`), the menu of models that `/model` has, filtered as you type. A model per role was a flag (`--role-model`) or a line of `models.roles`, or `/roles
  role=model` typed whole; the choice is now made from the keys, in the chat. The editor keeps a menu open after a choice that ends in `=`, as it does after one that ends in `/`.
- Built in beside Heimdall, OpenRouter and OpenAI: Anthropic, the open-weight hosts Together, Fireworks, Groq, Cerebras and DeepInfra
  (each needs only its key variable), and the local servers Ollama, LM Studio, llama.cpp and vLLM (no key: `ollama/qwen3:8b`).
  A marketplace id that starts with `openai/` or `anthropic/` goes to your default provider when that vendor's own key is not set.
- A model of its own for the compaction summaries: `models.roles.compactor` or `--role-model compactor=M`. It is used while the
  thread fits its window and falls back to the agent's own model otherwise.
- OpenAI-style chat completions (OpenAI, marketplaces such as Heimdall and OpenRouter, vLLM, SGLang), with reasoning
  replay, optional token-id capture, and exact gateway costs.
- Native Anthropic Messages adapter with explicit cache breakpoints and per-gateway limits declared as options.
- `sleipnir doctor` measures a real endpoint: streaming, tools, cache reporting, block granularity, warm-up needs. Its
  cache verdict counts nine repeat requests (`yes`, `partly`, `NO`), because a marketplace cache can hit on some and miss on
  others.
- A repetition guard: an agent that makes the same call with the same failing result eight times among its last twenty
  calls is stopped (`agent stuck`), after being told at the fourth. A first run against a real 2B model made two hundred
  requests over two refused commands.
- A message that holds the markup of a tool call (`<|call|>`, `to=functions.read`, `<tool_call>`) and no call is not taken for the
  answer: the model is told what it wrote and asked to call again or to answer in words, twice at most in a run. gpt-oss-20b through a
  gateway that does not parse its format ended a run this way, with the work undone and exit status 0.
- First impressions, from a trial by an agent that had only the binary: `sleipnir -h` opens with what it is (a team of eight) and how to start,
  and groups the commands by how often they are needed; an unknown command and a command line with no goal (or too many arguments) are one
  line that says what is wrong and where the flags are listed, not forty lines of flags above it (`run` and `swarm` give an example);
  Ctrl-C at the hidden prompt for a key ends the login at once (the first press did nothing, the second killed the process with the
  terminal's echo off); the demo says "a handbook of 8 topics" (it said "a 8-topic") and suggests `swarm 8`.
- A team asks several permission questions at once and the person answers them one at a time: "Yes, and don't ask again" to the first now
  settles the ones queued behind it that the new rule covers (it did not: a team of three asked the same kind of edit and `go test` question
  three times each, seen live). A tool name with a piece of the model's chat format stuck to it (`apply_patch<|channel|>commentary`) is shown
  in the feed, the agents table and the evidence of what an agent did as the tool that ran.
- `/resume ` in the chat lists the earlier sessions of the project as you type (when, what it cost, what was asked first), as `/model ` lists
  models; it only restarted into the newest one, and a trial by someone new to it found no way to choose.
- After Esc the manager of a team was recorded as "failed" and the agents page showed it as "stuck"; an interrupted run is idle, ready for
  the next goal. A model request that goes unanswered for 45 seconds is called that on the status line ("Waiting for the model (1m12s)")
  instead of a playful verb: a trial with a real model saw "Reasoning..." for four minutes of an endpoint that had not answered.
- `gofmt -l .`, `gofmt -d` and `gofmt -s -l` run without a question in the default mode, as `go vet` and `go build` do: they read files and
  print. `gofmt -w` (writes the files) and the profile flags still ask. A trial with a real model was asked about `gofmt -l . ; go vet`.
- `sleipnir --continue` (and `/resume`) shows where the conversation was under the banner: the last thing you asked and the start of what it
  answered. The screen was empty, and the only sign of the earlier work was a count of turns.
- From two more trials by agents that had only the binary, one in the chat on a Python project and one running `swarm 8` headless:
  - A team of eight can have all its workers writing at once (the cap was four): the manager of `swarm 8 ... --verify "go test ./..."` could not
    start its fifth worker, and the four that had finished their part waited for the two packages nobody was working on (4m42s for six
    one-line functions). The cap is now the team's workers (at least four); the leases keep two writers off the same files.
  - Declared change of the manager's role layer (the constant prefix is untouched): one line, "size the team to the job": a change of a few
    lines or inside one or two files is the manager's own or one worker's. A trial had a manager spawn two workers for a trivial change.
    Cost: about 45 tokens in the manager's first request of a session, once (a few thousandths of a cent).
  - "Yes, and don't ask again" for `python3 -m unittest`, `python -m pytest`, `yarn test`, `pnpm test`, `mvn test`, `gradle test` and `dotnet
    test` remembers the command and not the exact line, as it does for `go test` (about 25 questions in a quarter of an hour, most of them
    for variants of one test run).
  - A write or an edit of a file of the project that was not read is refused before the person is asked: the question was asked, approved
    and then answered "read it first", and the same diff was asked about again.
  - `/allow` typed while a turn runs takes effect at once (it waited for the end of the turn, behind the questions it was meant to stop).
  - The live output of `run` and `swarm` says why a call was refused (a `spawn` over the cap, a `task done` that the verifier rejected),
    not only that it was: the ✗ had no reason without --verbose.
- An endpoint that cannot be reached is said in plain words ("cannot connect to 127.0.0.1:9: connection refused (is the server running, and is
  the address right?)", "cannot find HOST (no such host): check the address and your network") where the retry notices and the final error
  were Go's `Post "http://...": dial tcp ...: connect: connection refused`.
- A model the provider does not have (a 404) and an account with no credit (a 402) say what to do ("`sleipnir models` lists the names it has", "/model"
  in the chat; "add funds there, or choose another model"), as a refused key already did.
- From three more trials (a Node project in the chat, interrupts and session commands, the real first run):
  - First run: an empty line at the prompt for a key goes back to the list of providers (it ended the program, as a stray Enter did); a provider
    that lists no models here (Anthropic's own protocol) asks for the model id and tries the key and the model together with the one small
    request (it ended with "the key was saved, but no provider with a model list is ready", and a refused key stayed saved).
  - A patch that changes several files is one permission question that names them all (it was one per file: creating a file and its test asked
    twice). A patch the policy refuses for any file is refused whole, before any write.
  - What the person allowed while the session ran (`/allow`, "don't ask again") goes with `/swarm`, `/model`, `/restart` and `/login`: the session
    continues, and it asked for `go test` again after each.
  - `sleipnir --continue` without a terminal says what to type (`sleipnir chat --continue`); the resume recap no longer pairs the last goal with
    the answer to an earlier one when that goal was cancelled; `sleipnir sessions` does not list a session in which nothing was asked.
  - A budget or a bill under a cent is "$0.0006", not "$0.00" (`cost.Dollars`); `/compact` says "669→146 tokens" and not "0k→0k"; `/allow tests` says
    it covers the build and test commands of go, cargo, npm, pytest and the rest (it printed the first two Go rules), and `/allow` alone says what
    tests is; an edit of a file that was only `cat` in a shell says that the read tool is what counts.
- The first screen of a team says what the eight are for, in one line: the manager plans and hands parts to workers that write in parallel, and does a small job itself.
- `sleipnir sessions` says "no sessions yet" when the directory exists but holds nothing worth listing (a fresh home after a chat that was opened and closed), not nothing at all.
- `sleipnir doctor` with no `--model` probes the configured model (it asked for one even when a default was set), and its live log says when a model answered the tools request without calling the tool, instead of a ✓ followed by `tool calling NO`.
- A flag that does not exist is answered with the one line that says so and where the flags are (`sleipnir chat -h lists the flags`), not with forty lines of flags under it; `-h` still lists them.
- `sleipnir run` without a terminal and without `--allow` says so in its first lines (edits and commands that need an answer are refused; `--mode accept-edits --allow tests` lets the usual ones through), instead of only at the end, after the model has been refused a step at a time.
- `sleipnir sessions` says how long ago each session was (`2h ago`) after its id, so the list can be read as a history.
- `ctrl+t` and `ctrl+g` write their page without `/stats` or `/agents` typed out in front of it (nobody typed it); the commands typed by hand are shown as typed.
- "Don't ask again" for a Node project's tests covers every spelling of `node --test` (a directory, a glob like `test/*.test.js`, `2>&1`): five of them were five questions in a trial. A test run with a glob in its arguments (`pytest tests/*.py`) is remembered as its runner too, where it was remembered as nothing.
- The chat now says "Waiting for the model (1m12s)" when a request has gone unanswered for 45 s. The line was written, and tested on its own, in an earlier change, but nothing in the live chat ever set it: a trial saw 3.5 minutes of "Weighing... Sketching..." from a slow endpoint.
- `--continue` and `--resume latest` no longer skip the newest session of the project when it ran its team in git worktrees (which cannot be resumed yet) to resume an older one: they say which session it is and that it cannot be resumed. A trial's `--continue` brought back a "say hi" run instead of the stalled team.
- `/steer TEXT` tells the running turn something without stopping it ("use the other file"): it is read with the agent's next step, and answers
  at once beside the turn. What was typed ahead waited for the turn to end. The end of a turn that took half a minute or more rings the
  terminal's bell, as a question does (ideas from reading crush, opencode, codex, aider, goose, hermes-agent, gemini-cli and cline).
- A `for` loop over files that are written out (`for f in p1/p1.go p2/p2.go; do cat "$f"; done`, the commonest first command of a model) is judged as
  the commands it expands to, one for each word of its list, and no longer asks "cannot tell statically which path $f is" (and is no longer refused
  in a run with nobody to ask). Only where it is sound: plain words, a lower-case variable that one loop sets and nothing else assigns, nothing in
  the line that can change variables (`read`, `eval`, `export`, `IFS` ...); `docs/SECURITY.md`. The explanations that a person is shown for a command
  the engine cannot check say "is a file name that is only known when the command runs, so it cannot be checked beforehand".
- An answer sent back for an open plan, for tests not run, or for a call written as text is shown as that in the feed ("the answer was
  sent back: ..."); it was shown as "stuck: the same call failed again and again", and the chat's status line said "Stuck on a failing call"
  while the model was only asked to run the tests.
- `run` and `swarm` take `--ask-timeout`: a question to the person that nobody has answered in that time is refused (as when there is no one to
  ask, and the log says `by: no one`), and the worker is told in a fixed sentence that nothing was approved and what it can do instead. The time
  counts from the moment the person is asked, not from the moment the request arrived behind other questions. Found by a swarm left alone for
  two hours at a question about a path the model had guessed; without the flag the question waits as it always did.
- A run with no one to ask (`run`, a swarm, a rollout) says so in every refusal that needs approval, so a model stops looking
  for another way to the same action instead of spending its steps on it.
- Every model of a session, roles' models included, is described from its endpoint's catalogue when the built-in table
  does not know it, and `session.start` records the prices each was described with, so `inspect` shows the gateway's
  figures instead of a generic estimate.
- What a person is told is specific: the notice while a request is repeated names the failure, its HTTP status and what the
  endpoint said (`server (http 502): The provider returned an error … [provider X]; retrying in 877ms`), and a cache-miss
  warning says when the prompt prefix did not change, so the miss was the endpoint's (a real marketplace served 5% to 96% of an
  identical prefix, request after request).
- A streaming request that the endpoint answers with the whole completion as JSON (a gateway whose upstream does not stream) is read
  as that, and a 200 whose body is an HTML page (a proxy, a captive portal, a gateway whose service is away) is a server failure that
  quotes the page, not "the stream ended". These two came from `internal/provider/chaos`, a handler that wraps any provider and
  breaks it the way the real endpoint was seen to (a 503 whose JSON says the database is away, a 502 with an HTML page, a 429 with a
  Retry-After, a connection reset, a response cut off after half of it or ended without its last frame, a server that says
  nothing, one that says nothing halfway), deterministic for a seed. The agent is run through every fault at each request of a run
  and through eighty random mixtures (a thousand nightly): it ends with the answer, runs each tool once and leaves a thread that can
  be sent, unless the endpoint gave it the right to stop (a request that got no response twice is not tried a third time, a failure
  that is not an outage is tried six times). The mixtures found nothing in the agent.

### Tools, permissions and extensions

- An answer given after code was changed and before any test ran is sent back once, with the project's own test command from the survey (`go test ./...`), to run it and
  fix what fails or to say why it cannot. Edits to documentation do not arm it, and a project whose test command the survey could not find is not nagged. On the benchmark's first
  build 21% of the answers that said "done" were wrong; **whether this lowers that is not measured yet** (`docs/ROADMAP.md`): if the benchmark says it does not, take it out.
- A `plan` tool, and the harness keeps the plan: the model sets a short list of steps (pending, doing, done), the list is shown back at the end of every request in the
  hot tail (never cached, so it costs the cache nothing), the chat's tool line says how far along it is, and an answer given while steps are open is sent back once to finish
  them or change the plan. The constitution gains one line (plan first for a task of three steps or more) and every agent's tool list one tool: a declared change of the
  constant prefix, about 230 tokens written to the cache once for everyone (`internal/agent` golden files and the constitution bound moved with it). Why: a plan
  the harness holds and repeats is the best-evidenced help for a small model (`docs/LANDSCAPE.md`). A swarm worker has the same tool; its plan rides inside the board's frame.
- A command that starts with a `cd` to an absolute directory that does not exist (a weak model invents `/Users/someone/project` though it starts in the project) is
  answered at once, saying where commands run and to leave the `cd` out, instead of putting a question about a place that is not there to the person. So is
  a `cd` to the project's own name from inside it (the survey names the project, and a weak model takes the name for a directory to enter), with or without a `2>/dev/null`.
- A model whose test run fails and that then edits only test files is told once, in the results of that edit, to change a test only when it contradicts
  the task and otherwise to fix the code (a small model will often rewrite the test to match its bug). The prompt is not touched.
- File, shell, web and recall tools; every agent sends the same tool list and roles are restricted at run time by the
  permission engine (modes, rules, shell-syntax analysis, role profiles, hard denies for credentials).
- Skills (listing in the shared layer, loaded on demand), custom slash commands, markdown role definitions, hooks
  (SessionStart/End with the reason a session ended, UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest,
  Notification, Stop for the agent you talk to and SubagentStart/Stop for a swarm's workers, PreCompact/PostCompact around
  every compaction, automatic ones included), MCP servers (tools frozen per
  session, per-entry approval for project servers, prompts as slash commands, `sleipnir mcp`, `/mcp`), checkpoints
  and rewind. Repository-supplied skills, commands, roles, hooks and instruction files are read only when the project
  is trusted, and writes to the directories that hold them always ask.
- Tool argument errors name the argument whatever Go built the binary: `argument "offset" must be an integer (got string)`. With
  Go 1.27 the decoder stopped naming the field of an error that a `UnmarshalJSON` method returns, so `"offset": "5"` was answered
  with "arguments must be a JSON object" (a model cannot act on that), it numbered array positions in the path, and with two wrong
  arguments the Go versions reported different ones. The first wrong argument in the order written is found by trying each member
  alone.

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

- Long-term memory: a `memory` tool (add, remove, list) keeps short notes in `~/.sleipnir/MEMORY.md`, which every later session reads into its shared
  layer, in any project. A note is one line of at most 300 characters; the file holds 40 notes at most, and a full memory answers with the numbered
  notes so the model merges or removes before it adds (nothing is dropped silently). Each change is a write under `~/.sleipnir`, which asks in
  every mode and is refused when nobody is there to answer, and the question shows the exact note, so text from a web page that talked the model into
  saving something is seen first. The tool adds to every agent's tool list: a declared change of the constant prefix, once.
- `sleipnir sessions prune` deletes recorded sessions that are old (`--older-than`, 30 days), except the newest `--keep` (20) and any written
  to in the last ten minutes, and lists them with their sizes instead unless it is given `--yes`. Nothing deleted a session before, and
  a benchmark or a daily user has thousands within a year. A directory that has no event log is never touched.
- Every finished turn saves a snapshot of the agent (thread, notes, spine) in the event log. `sleipnir chat|run --resume
  <id|latest>` (or `--continue`) picks the conversation up where it stopped, in the same session directory and log; the
  first request writes the cached prefix again, once, and says so. A team's session resumes too: its manager's conversation and its board; the workers start again.
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
- **`golang.org/x/net` v0.58 and Go 1.25.** The HTML parser that turns a fetched page into text (`html.Parse`, reached from the
  `web_fetch` tool) had seven advisories (GO-2026-4440, 4441, 5025, 5027, 5028, 5029 and 5030). `govulncheck` is clean from v0.55,
  which needs Go 1.25, so `go.mod` says `go 1.25.0` (Go 1.24 is out of support) and `golang.org/x/sys` and `golang.org/x/term` moved
  with it. The dependencies are still the standard library and those three modules.

- **Remembered project trust (`sleipnir trust`).** What a repository brings that the harness reads as text and settings (its instruction
  files and what they import, `.sleipnir/config.json` and `config.local.json`, `.mcp.json`, and the skills, commands and agents under
  `.sleipnir` and `.claude`) was used only with `--trust-project`, every run. A yes can now be kept for exactly the files that were seen: a
  SHA-256 over their kind, path and content (what the loaders read; `internal/trust`) is written to `trust.json` in the state directory,
  for the working directory, and a session started there uses the files without asking until any of them is edited, added or removed
  (then the notice, and the question, name the file). `chat` asks at its start (a dialog with three answers, or `y` / `r` / `n` on the line
  prompt; Ctrl-C at the question is Ctrl-C at the start); `run`, `swarm` and `rl rollout` have no one to ask and use the answer;
  `--trust-project` stays the answer for one run and writes nothing. `sleipnir trust` shows what the project has and where you stand,
  `add` says yes after showing the files, `forget` takes it back, `list` shows every directory and whether its files still are the ones
  you saw. A settings file that is a link out of the project, an unreadable file, or more than 400 files or 16 MB makes the footprint
  partial, and a partial one is never remembered. It vouches for what the harness reads, not for the repository's code, and a project's tool
  servers still each need their own approval (`docs/SECURITY.md`, "Trusting a project"). `session.start` records how the project came to be
  trusted (`trust`: `how`, `digest`, `files`, `saved`, `changed`) unless a flag did. Also: `perm.Config.StateDir`, so that a write to the state
  directory asks wherever `SLEIPNIR_HOME` puts it, as a write to `~/.sleipnir` already did.

### Configuration

- Every key in the file format is read by something: the keys of earlier drafts that nothing read are gone (an old file
  that still has them gets an "unknown key" warning with a suggestion). `cache.shared_ttl` reaches the breakpoints of
  single agents and swarms, and provider options are validated per dialect (unknown names warn, wrong kinds and values
  the adapter does not take are errors; a test keeps the list and the code that reads it together).
- `sleipnir config` lists each warning once, with file, line and column; `init --user` invents no provider (`--local-url`
  adds a self-hosted one); `swarm -h` and `-h` everywhere exit 0 and print usage; a bad worker count, a role model for a role
  that does not exist and a budget that is not a positive number are errors that say what was expected.

### Interfaces

- The chat page keeps what matters on a narrow screen and what a person can act on: the banner's directory gives way (keeping its tail) before the budget and the size of the team, which were
  cut off the end of the line; the footer drops the session id before the keys of the pages (`ctrl+t stats · ctrl+g agents · / commands`); and a miss of the endpoint's own cache with the
  prompt unchanged is no longer said under every answer (three warnings and a note on an endpoint like Heimdall's, whose cache serves a third of the repeats, cost less than a cent
  between them): it is said when it cost a cent or more, or with `/verbose`; the agent's notice for it is information, not a warning. A break the harness caused (a layer changed) is
  always a warning. Found by watching a real team work in `scripts/look.sh`.
- `/login [provider]` in the chat: add a key, or sign in with your ChatGPT plan, without another terminal. The chat ends, `sleipnir login` runs on the terminal (a key
  is typed hidden; a browser sign-in prints its address), and the chat comes back, a single agent with its conversation, a team starting again. A name that is not a
  provider ends nothing. A key the provider refused, a model of a provider that has none and a ChatGPT sign-in that has ended now say `/login` where they said
  `sleipnir login`.
- `scripts/look.sh` takes `--pause SECONDS` for a screen that comes late (a program that starts another one), and `sleipnir term-svg` takes a frame of a screen that stood
  still: output that ended within a step of the last frame had none until the next output came, so a picture taken in a pause showed the screen before the end of the
  burst (the chat that came back after `/login` was missing from its own picture).
- `/model` works in the default chat. It was refused for a team, and the chat on a terminal is a team now: `/model REF` starts the team again on
  that model (its roles that name their own model are kept) and the model menu opens after `/model `. `/model` and `/roles` check the reference
  first (an unknown provider, a provider without a key), so a typo cannot end the chat. Found by looking at the palette picture and typing `/model `.
- The chat page is as clean as it can be: the status line, the input and the footer. What the cache saved, the prompt stack bar and the hit
  ratio sparkline are gone from it, and so is the cache hit of a turn's record (`── 12s · 7 steps · $0.08`). They are on the stats page, `ctrl+t`
  or `/stats` (cost, tokens, cache hit, what it saved at list price, the prompt layer by layer), and a team has the agents page, `ctrl+g` or
  `/agents`, the cockpit's table in the chat. The footer names both keys (`default · ctrl+t stats · ctrl+g agents · / commands`), and the
  banner says the size of the team. Asked for after looking at the README picture, where nothing said how to reach any other page.
- `--swarm N` (and `swarm N`, `/swarm N`, `--mode swarm:N`) now counts agents in all, the manager included, so the number is the number of
  agents you see; it counted workers before. The name is the horse with eight legs, and eight is the default team.
- The chat on a terminal is a team of eight agents by default, the manager included (kept under `swarm.max_agents`; it does a small job
  itself). `--swarm 0` is a single agent, and so are the line chat and a restart of a single agent. The first-run pictures in the README were
  recorded again with the arrow-key menus.
- First-run setup and `sleipnir login` try the key just typed with one small request before keeping it: a catalogue is often public, so a typo used to be found
  only at the first goal. A refusal (401, 403) ends the setup and the key is forgotten; any other failure lets it through.
- The first-run menus (which provider, which model) are driven by the arrow keys with the chosen row highlighted: Enter chooses, Esc quits,
  typing narrows the model list, a digit jumps in the short provider list. Where there is no terminal they keep their typed form (numbers, words).
- `/roles` shows which model each role runs on and where it came from, and `/roles role=model` changes one. A restart (`/restart`,
  `/new`, `/resume`, `/roles`) now keeps the flags the session started with (`--swarm`, `--verify`, `--isolation`, `--role-model`
  and so on) unless the line changes them: found by use, a role change from a team chat used to come back as a single agent.
- Every flag of `sleipnir chat` has an equivalent inside the chat: `/budget`, `/allow`, `/verbose`, `/anim`, `/sessions`, `/cwd`, and `/restart [flags]` and
  `/new`, `/resume [id]`, and `/swarm <n> [flags]` for the flags that decide the shape of a session (the chat starts again with them, keeping the conversation when it is a single agent).
  `docs/CLI.md` has the table, flag by flag.
- `sleipnir` alone, or followed by flags (`sleipnir --model provider/model`), opens the chat in the current directory when it runs on a terminal; a script or a
  pipe still gets the usage message.
- `sleipnir login` (and the first run, by itself) asks which provider and for its key, without echoing it, and keeps it in `~/.sleipnir/auth.json` (mode 0600,
  never in the shared `config.json`); `logout` removes it. The key is held in memory like one moved out of the environment, and an environment variable of the usual
  name still wins. No more "export the key and run again".
- First-time setup: with no model configured, the chat asks the provider (the catalogue it serves right now: Sleipnir knows no model names) which model to use, with
  search words and numbers, and writes `~/.sleipnir/config.json` with that model and the permission mode, saying where. It is asked once. With no key at all it prints
  a short guide, Heimdall first. `sleipnir init --user` is the same setup on a terminal; the model it used to guess from the key is gone.
- `sleipnir schedule add --cron "0 9 * * 1-5" "summarize yesterday's commits"` keeps a goal in `~/.sleipnir/schedule.json`; `sleipnir daemon` (or `daemon --once`
  from cron or a systemd timer) starts each job that is due as a headless `sleipnir run` of its own, with its model, directory, permission mode and budget
  (default US$1 a run), one hour at most, its output in `~/.sleipnir/schedule-logs/`. A daemon that was down starts an overdue job once, not once per missed slot.
  A job's mode defaults to `default`, which refuses whatever needs a person to say yes (nobody is there).
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
- `sleipnir watch` and `sleipnir replay`: the session in the terminal, drawn from its event log and nothing else. Four screens: the
  swarm cockpit (agents and what each is doing, the horse and the one prefix its riders share, the gantt, the task board, the merge
  queue, mail, the governor, the live feed), the cache of one agent (the layers of its prompt sized by tokens and bright where the
  provider read them from its cache, the clock of the cache, the hit ratio of every request with the breaks and compactions marked
  on it, the fold of a compaction and whether the cache was cold when it was made, the anomalies and the layer that diverged, every
  agent's cache side by side), the mail and the board with the merge queue. `watch` follows a log that is being written (open it
  beside a run, in a second terminal or a tmux pane); `replay` plays a finished one on a clock of its own (pause, seek, speed).
  `replay --record FILE.svg` writes an animated SVG (CSS only, deterministic, plays in a README) and `--final` prints the last screen
  as text; both need no terminal. The program draws on the alternate screen, reads keys in raw mode, follows the window and puts the
  terminal back on every way out, a signal and a panic included (`internal/tui/app`, run on a pseudo-terminal by
  `cmd/sleipnir/e2e_watch_test.go`).
- `sleipnir demo --scenario shop`: a second scripted team, about twenty seconds, with the things the cockpit and the cache view exist to
  show, all of them done by the real harness around a scripted model: nine agents (a manager, three scouts reading the same prefix at
  once, four writers each in a git worktree with a scope, a reviewer); worker mail; a worker that runs the same failing check four times
  until the repetition guard tells it so; a compaction at a warm moment (a priced rebase) and one at a cold moment (free); the provider
  losing its cache for a few seconds (`cache.anomaly` with what each miss cost); and a merge that the queue sends back because two
  workers each kept a rule in their own tree and broke it together. The cache lives 25 seconds in it instead of minutes, so it can be
  watched cooling (`--scale` stretches or squeezes everything it does). Tested end to end (the merge, the bounce, the mail, the nudge,
  the fold, and that the result in the checkout passes the project's own check).
- `sleipnir demo` on a terminal is watched: it runs the shop and shows it in the live cockpit (the program of `watch`, over the log the
  harness is writing), keeps the last screen, with the other screens to look at, until you press `q`, and then prints the report; a `q`
  before the end stops the team, and a run that fails gives the terminal back at once with its error. Without a terminal, with `--plain`
  or without git and sh it is the handbook's text report as before. The mock endpoint stops waiting for a reply's latency when its
  client goes away, so that stopping the demo does not wait for the reply nobody is waiting for.
- The pictures in the README (`docs/media`) are recordings of the program, made by `scripts/record-demo.sh` from the event log of one
  recorded demo session and listed in `docs/media/gallery.json`: the swarm cockpit, the cache of one agent, a compaction at a cold moment.
  `sleipnir replay --gallery` draws them, and `scripts/record-demo.sh --check` and a Go test fail when the committed files are not what the
  code draws from the committed log. The mock endpoint got a cache outage (`Server.CacheOutage`) for the demo's break.
- The chat has a recording in the README (`docs/media/chat.svg`, stills `chat.png` and `chat-ask.png`), made from a transcript of a chat
  session because a log cannot say what a person typed: `scripts/record-demo.sh --new-chat` (the hidden command `sleipnir chat-record`)
  runs a real session, the harness and its tools with `go test` among them, against the mock endpoint with a script for the model and one
  for the person (who presses `y` at the permission question, which answers nothing, and then `1`), and writes everything the chat was
  given as `docs/media/chat/transcript.jsonl`; the chat program is then played from it
  on a virtual clock, one record at a time, so the picture is the same bytes on every machine, and `scripts/record-demo.sh --check` and a
  Go test fail when it is not what the code draws. The README says what is real and what is scripted.
- `sleipnir chat` on a terminal is a program, no longer a line REPL (`internal/tui/app`, `cmd/sleipnir/chat_tty.go`; `docs/UX.md`). What
  is said goes into the terminal's own scrollback, so copy, search, tmux and SSH work on it: the banner, what you typed, the answer
  streamed as markdown, each tool call as `● Bash go test ./...  ✓ 1.4s` with its output under it (long output is its head and tail,
  `ctrl+o` writes the rest), an edit as a diff with line numbers, a compaction as one line (it folds first, where animation is on),
  a cache break as a warning, notices and retries. The last rows are redrawn in place: the status line (spinner and verb, elapsed,
  tokens, cost, what the cache saved at list price, `esc to interrupt`), the prompt stack bar (G0..G6, bright where the provider
  served it from its cache, dim where it was paid for, the clock of the cache), the hit ratio of every request with `⚠ ◆ ↻` marks,
  the input box, and a footer with the mode, the model and the session. All its numbers come from the session's log
  (`state.State` over `Log.Subscribe`), not from new instrumentation in the agent, and the program changes nothing the model sees.
  The program is the only writer to the terminal and the only owner of its input; the session reaches it through a sink and a
  prompter that only forward, and a turn runs on a goroutine it starts and can cancel. The keys: an editor with history kept under
  the state directory, a `/` palette, `@path` completion, a paste of many lines as a chip that is sent whole, typing ahead (queued,
  and shown), `shift+tab` for the permission mode, `ctrl+t` for the stack panel. An approval is a box with the command or the
  diff: `1` yes, `2` yes and do not ask again for this exact request this session, `3` no, arrows with enter, `esc` no, and never a
  letter, because a question takes keys only after the keyboard has been quiet for a moment since it appeared; what is typed ahead,
  or half typed when it appears, goes to the prompt and cannot approve anything (questions that come together, from the agents of a
  swarm, wait their turn, each armed again when it comes to the front, and the one in front says how many wait). Ctrl-C keeps the semantics of the line chat
  (it cancels the turn and its question and never the session; on an empty prompt a second one within two seconds quits; Ctrl-D and
  SIGTERM as before), and Ctrl-C or SIGTERM while the session is still being made ends the chat with 130 or 143 and
  `sleipnir: interrupted` (a project's tool server asks whether it may start during that time, on the screen like any question).
  `--no-anim`, `SLEIPNIR_ANIM=0`, `REDUCE_MOTION=1` and `NO_COLOR` are honoured (`NO_COLOR` keeps the program and takes the colour
  away), and the glyphs are Unicode or ASCII by the locale. A pipe, a file, `TERM=dumb` and the new `--plain` get the line chat, byte
  for byte as before. Everything printed passes the sanitiser, and a fuzz target and a run with hostile text through every door of
  the session say so; the tests read the screen through the terminal emulator (`cmd/sleipnir/e2e_chat_test.go`, and goldens of the
  live region at 60, 80 and 120 columns and of a whole conversation).
- `sleipnir chat` (slash commands, Ctrl-C per turn, Ctrl-C twice at the prompt to quit), `run`, `swarm`, `recon`, `init`, `config`, `sessions`, `models`,
  `demo` (a scripted 14-agent team on a mock endpoint, no key needed) and `inspect` (a live or after-the-fact web
  dashboard: layers, hit ratio, compactions, swarm, cost; for a swarm also its worktrees and merge queue, the mailman and
  the manager's supervision).

- Performance of what runs on every request is measured: benchmarks of `kv.Render` (every route and hot mode, and a thread of 200
  exchanges), `core.Canonical`, the event log (emit and scan), the SSE reader, `perm.Check`, the board and the governor, grep, and the
  project survey; allocation gates as ordinary tests (`allocs_gate_test.go`, not under `-race`) hold the number of allocations of
  each of those calls to what it is, and how `Render` grows with the thread to linear; `scripts/perf.sh` runs the benchmarks and
  `bench/tools/benchcmp` compares two runs (a slowdown only when the two ranges do not overlap). `docs/BUILDING.md`, Performance.

### Repository automation

- **One gate, one release path.** `ci` (lint and drift checks, `-race` tests on Linux amd64 and arm64 and macOS with the Go of `go.mod` and on
  Linux with the newest Go, the allocation gates, Windows for information, build and vet for every shipped platform, the cache-policy
  guards, govulncheck, dependency review, actionlint, zizmor) ends in one required check, `ci-gate`. After `ci` passes on `main`,
  `release` plans the release (`scripts/release-plan.sh`: conventional commit titles give the version, docs, tests and CI alone release
  nothing), builds with goreleaser without publishing, makes SBOMs and provenance attestations, and creates the GitHub release with
  generated notes. It does nothing until the repository variable `AUTO_RELEASE` is `true`, and never without a `LICENSE`. Every action
  is pinned to a commit.
- **Drift fails a check.** The dependency allow-list (`scripts/check-deps.sh`), SHA pins (`scripts/check-pins.sh`), a prompt-byte change
  without its CHANGELOG entry (`scripts/check-declared.sh`), and `internal/repocheck` (links, the ruleset against the jobs it names,
  CODEOWNERS, platforms, the installer against goreleaser, the Go version in the documents, tests CI would never run, the figures that docs/TESTING.md opens with, counted from the tree), all in
  `scripts/check.sh` and `make check`.
- **Protection is in the repository.** Rulesets for `main` and for `v*` tags (`.github/rulesets`), `scripts/protect-main.sh`
  (`--dry-run`) for the merge, security and Actions settings, CodeQL, Dependabot, CODEOWNERS, a conventional-title check with labels
  for the release notes, a vulnerability-reporting section in `docs/SECURITY.md`; `docs/REPO-SETUP.md` gives the order of the steps
  that only the owner can take.
- Fixed on the way: the allocation gates ran in no CI job (every test run used `-race`, which they are not built under); `go vet` for
  Windows failed on the signal test of `cmd/sleipnir`; two tests that Go 1.26 broke.
- Found by the first run on GitHub: the swarm bound above; `ptytest` on macOS (the system takes the terminal from every holder when
  the program that leads its session exits, so "what did it leave unread" has no answer after an exit there, and a program that
  had read its line and exited was reported as not having read it); `replay` and `watch` told to stop by SIGTERM exiting 0 instead
  of 143 now and then (the end of the keys, which `ReadKeys` causes when the context ends, and the end of the context were ready
  together and `select` chose between them at random: the signal counted as the person's `q`; the chat had the same coin toss
  between "exit" and "interrupted" as the reason a session ended); and every golden file checked out with CRLF on Windows
  (`.gitattributes` pins LF for the whole repository, and `internal/repocheck` keeps that line from going). The Windows run, which
  failed in 26 packages (the rest of them for POSIX assumptions: paths, file modes, a shell, a finer clock), now tests every package
  but those `scripts/windows-excluded.txt` lists with a reason each, so a red Windows run means something that worked stopped, or a
  new package that never did; taking a package off the list is how it is ported.
- Found by the first run with the git of the runners (2.55, newer than a developer's), which failed `internal/gitx` two ways. **A
  snapshot of the work tree could miss an edit.** `SnapshotTree` (what decides that the work tree is dirty, what a diff holds and what an
  isolated swarm starts from) works on a private copy of the index, and the copy had the time of its copying. git trusts size, inode
  and times for a file unless its entry is not older than the index file ("racily clean": a rewrite that kept the size and fell in the
  same second leaves nothing in the stat data), and a copy that is newer than every entry says none is. An edit of that kind, made
  after the index was last written, was in no snapshot, one run in a hundred of a test on a loaded runner. The copy keeps the time of
  the index (`TestSnapshotSeesARewriteThatKeepsTheSizeInTheSecondOfTheIndex` sets the times itself and fails without it). **A fixture
  raced a background process.** Newer git starts `git maintenance run --auto` detached after a commit, and its worktree-prune task
  removes a worktree entry that has no index and whose gitdir is missing: the half-made worktrees that some tests build, a few
  milliseconds after the commit before them (one run in sixteen). A real `worktree add` is locked while it initializes, and
  everything the product runs is hardened with `maintenance.auto=false`, so the product was never exposed; the fixtures now run the
  same way.
- Found by the first nightly run (twenty seconds a fuzz target; the suite three times under `-race` in shuffled order, a thousand chaos
  seeds and `govulncheck` passed): `GuardFrame` let `<live <live>0</live>` through with two opening tags, because it ended a frame's opening
  tag at the first `>` and took whatever came before it as the tag's own (a `<` inside the tag now makes the text escaped whole; the frames the
  harness writes are unchanged and no golden file moved); the event log's fuzz target had a different idea of "a log at its last sequence number"
  than the log has (Open's own record of damage takes a number too); and every end-to-end test that reads a command's stderr failed under the
  coverage run, since a test binary built for coverage that runs as the command says on stderr that it has nowhere to write its counters.
- Found by the second nightly run (a minute a fuzz target): **a refusal could put a control character on the terminal.** The configuration
  that a refused endpoint shows, so that the person can allow it deliberately (`allow_hosts`), was the host and the provider's name run
  through `encoding/json`, which leaves the C1 controls (U+009B is a CSI and U+009D an OSC that writes the clipboard, on a terminal that
  honours the 8-bit forms), the bidirectional and zero-width characters and the tag characters as they are, and both come from a
  project's configuration, which is not the reader's file. The snippet writes them as `\u` escapes now, which paste into a
  configuration file as the same host (`FuzzCheckEndpoint` found U+009F in a host; the sentence above the snippet was sanitised all
  along). **A killed process group could outlive the kill.** After SIGKILL `killTree` waited for the leader to be reaped and not for the
  members of the group, which are not its children and are gone a moment after the signal, so a command that ignored SIGTERM could still be
  running when `Shutdown` returned (once in the nightly run's three passes of the suite under `-race`); it waits, bounded, until the group
  is empty, as the MCP transport already did.
- Found by the third nightly run: `FuzzScope` failed on a name of about 2,500 newlines. A pattern over `maxScopeBytes` (4,096) matches nothing,
  by design, and the fuzz target's invariant ("the escaped literal matches itself") forgot that escaping puts a backslash before every
  space and special character, so a name that fits can have an escape that does not. The invariant now holds for patterns inside the cap and
  asserts that one over it matches nothing (`TestAPatternOverTheCapMatchesNothingEvenItsOwnLiteral`); the product did what it was meant to.
- A long run is measured (`TestALongRunKeepsABoundedFootprint`): a thousand steps on every push and twenty thousand in the nightly run, a
  tool result each, the thread folded as it grows, the live heap read after a collection a quarter, a half and three quarters of the way,
  and held to what the archive's index takes for the turns it ever had (a few hundred bytes each; a kilobyte a step is the figure, the
  bound twice that, and a result of four kilobytes kept for every step fails it by three times). It first said 17 KB a step, which was
  the test's own blob store: held in memory it kept every result, where a session keeps them on disk. With the store on disk a heap
  profile accounted for all of the growth, the index and the set of blobs already verified (which is bounded); the harness leaks nothing.
- `scripts/look.sh` ends the program it looks at with SIGKILL, and before the terminal goes. Told to end (the terminal hung up), the chat clears the live region it drew,
  `script` logged that, and a picture of the last moment was of a screen with the banner and nothing else: found by a menu that was on the real screen (tmux said so) and not in
  the picture. Choosing a time for the picture did not work: script's timing file does not always carry the wait before the end.
- `scripts/look.sh` quotes every word of the command it runs (it joined them with spaces, so a goal with brackets in it was a syntax error of the shell), and `docs/UX.md` no longer says that
  `sleipnir swarm` draws a live region of its own: it never did (found by looking at it at 80 columns); it prints one line for each tool call. The header of `run`, `swarm` and the line chat
  breaks at spaces to the terminal's width (the session id was cut in two at 80 columns).
- The agent tests' rig closes its agent when a test ends. A compaction a test never waited for (a second turn starts one) was still running
  when the mock endpoint was closed and the next test began, and read `agent.RetryBase` in its retry while that test's rig wrote it: `-race`
  failed two tests that did nothing wrong, once in a full run of the packages together on a busy machine.

### Found by running it on real models

A benchmark (`bench/`, `scripts/bench.sh`, `sleipnir rl report`) run on real models found what the tests did not. Each line is a
defect the runs showed, with the evidence, and what changed.

- **The first run did not fit an 80-column terminal.** Seen by running it in a terminal of the default size: "your settings are kept in ~/.sleipnir/config.j" and "son.", a chosen model's line
  ending "$0.01/M ou" and "t  tools", the confirmation broken before its semicolon. A line wider than the terminal is broken by the terminal wherever the column falls, and a menu that
  counts its rows to draw them again (the arrow-key menus of the first run and `sleipnir login`) counts a wrapped row twice. The messages are now broken at spaces to the width of the
  terminal they are written to, the menus cut a row that is too long (with an ellipsis) and say what was chosen on one line, and the model menu's columns are as wide as its longest row
  needs (the price column was one character short for "$0.019", so "tools" moved). Tests drive the menu at a width of forty and check every line. The explanations that `init`, `trust`,
  `inspect` and the refusals of `run` print break at spaces the same way.

- **Esc did not stop a team.** Found by pressing it in the default chat with three workers running: the workers were stopped and their tasks went back to todo, and a second and a half later the
  swarm woke the manager for those very changes ("While you were idle: T1 went back to todo; ...") and it checked the board and started the workers again. Then the next goal was answered
  "error: the manager is already running", because the wake run was still on. An interrupt now holds the automatic wakes until the person writes again (`waker.hold`, with the count of the person's
  inputs, so that a slow goroutine cannot hold what a later input has released), and a goal typed during a wake run ends that run (the workers are left alone) and has the manager
  (`supersedeWake`). Three tests in `internal/swarm` that failed on the parent: the wake after the interrupt, the goal during a wake run, and the release by the next goal.

- **The pages of a resumed chat were empty.** After `sleipnir --continue` the agents page said "the team starts with your first goal" and the stats page began at zero, though the manager
  was back with its fourteen turns and the board with three merged tasks. The chat draws its pages from a state made of the events that come after it attaches to the session, and a
  resumed session's history is in its log only. The chat now folds the log of the earlier runs into the state before it starts (a log of up to 64 MB; past that the pages start empty), a
  session that is resumed is not "ended" any more (the log holds the end of the run before: `session.start` clears it, and so does the agent that is brought back, since `session.start`
  waits for the first goal), and a resumed manager is done, not "thinking", until a goal comes (`Swarm.StartIdleManager`); the workers of a run that was killed (nothing ended their runs) are idle once the manager
  is restored. Found by looking at the agents page of the session the team-resume
  change had just made resumable; a test in the state package for each of the two states, one in the swarm package, and an end-to-end test that opens the page after `--continue`.

- **A team's session could not be resumed, and `sleipnir sessions` said it could.** Found by running `sleipnir --continue` in the default chat: "resuming into a swarm is not
  supported yet". The chat on a terminal is a team, so `--continue`, `--resume ID`, `/resume` and the restart that `/model` and `/login` make had nothing to come back to (`/model` started
  the team over with an empty conversation). A team's session now resumes: the manager's conversation (the snapshot of its last finished turn) and the board (replayed from the log;
  a task that was being worked on, reviewed or blocked goes back to todo with nobody on it, and the board says that the session was resumed), and the workers start again, since
  what a worker knew was about a task it no longer holds. A session that ran its team in git worktrees (`--isolation`) is refused with that reason, as its worktrees are not rebuilt. A
  session can be continued in another shape (a single agent's conversation as a team's manager, and the other way round). `/model` in a team keeps the manager's conversation and
  `/login` comes back where you were. Tests for the board, the session and the chat (`TestE2EChatTeamResumesWithContinue` fails on the parent).

- **Two refusals that told a weak model nothing.** Read from the 47 episodes of the first A/B run (`sleipnir friction`, and the tool errors in the trajectories). An `edit` whose `old_string` was a
  block with one wrong line in it was answered "the closest line is line 219 (100% similar)": every line of the block exists, so the closest is exact, and the model could not tell which line was wrong. It
  now says where the block is right and where it stops: "the first 2 lines of old_string match the file at lines 1-2; line 3 of old_string is `b := 3` but the file has `b := 2` there". And a path whose
  directory is mangled in the middle (`internal/provider/openaichar/limits_test.go`) said only that its directory does not exist; it now looks for the directory that was meant, and answers with the file when it is there
  (`did you mean internal/provider/openaichat/limits_test.go?`) or the directory when it is not.

- **The first keys typed after a restart were lost.** `/restart`, `/swarm`, `/new`, `/resume`, `/roles` and `/model` (for a team) end the chat program and start the chat again in a child
  that has the terminal. The program's read of the keyboard cannot be called off, and it stayed waiting in the process that had ended: it took the first line typed for the new
  chat (typing `abc` right after `/restart` showed nothing; `def` after it arrived), and at the key prompt of `/login` it took the whole line, the key. The keyboard is now read
  through `term.Reader`, which is stopped for good when the program ends (`select(2)` on the terminal and a pipe; macOS's poll and kqueue do not work with terminals). Found while
  building `/login`; a test in `internal/tui/term` on a pty, and an end-to-end test that types after a restart (it failed for the whole hang guard on the parent).

- **The recordings had no colour, and a highlighted menu row was unreadable.** `internal/tui/svg` put the default text colour in a CSS rule
  (`text{fill:…}`), which beats the `fill` attribute of every text element in a browser: no cell's own colour ever showed in a picture, and
  the dark text of a reversed cell (the highlighted row of a menu) was drawn light on a light bar, white on white. Found by looking at the
  README picture, not by a test; the default colour is now the grid group's, a test refuses a stylesheet that sets one, and every picture of
  `docs/media` was drawn again. The menu also paints one frame per write, so it no longer shows half drawn.

- **Reasoning written into the answer.** An endpoint that does not separate reasoning can leave the model's thoughts in the answer, closed by a
  bare `</think>`. They are now a thinking block, and the answer starts after the tag.
- The refusal of a read outside the workspace no longer says where scratch files go (that hint is for writes).
- **Scratch files had no place to go.** `mktemp`, `go build -o /tmp/x` and test runs were refused in `/tmp`, and a model lost minutes to retries.
  Commands now get a private `TMPDIR` under the session's directory that the permission engine treats as workspace, and the refusal of
  any other place names it.
- **Two refusals that stopped nothing worth stopping.** `set -e` and `set -euo pipefail` (strict mode, which changes how a script stops and
  touches nothing) no longer ask, and `--allow tests` covers `go mod init` and `go mod tidy`, which a new project's first minutes need.
- **A write over an existing file showed the whole file as added.** The approval now diffs against what the file holds.
- **A verify command over the whole repository stalled an isolated swarm.** The run says so at the start (`--verify` without `{dirs}`)
  and names the command to use.
- **"Don't ask again" for an edit never fired, and for `go test ./a` asked again for `go test ./b`.** A yes for the session to an edit inside the project is a yes to edits
  inside it. For the runner commands (`go test|build|vet`, `npm test|run`, `pytest`,
  `cargo test|build|check`, `make`, `git add|commit|status`) the second answer remembers the prefix for the session; anything else stays exact.
- The chat rings the terminal bell once when a question waits for you (`SLEIPNIR_BELL=0` turns it off).
- A notice of the session itself (not of an agent) no longer prints as `[] warn: ...`.
- A chat says the first three cache breaks that are the endpoint's own (the prompt did not change), then one line that it will not say more; a break caused by
  a changed layer is always said. (Roadmap item 8, plateau learning, would do better.)
- The hint after a run that had nobody to ask no longer suggests `--allow 'Bash(python:*)'` or `Bash(rm:*)` (rules to run anything): an interpreter run on a script
  gets the rule for that script, and the programs that cannot be named safely get none.
- **A garbled tool call ended a run.** Arguments the model cut off or garbled were sent back in the history as they were, and an endpoint that checks the
  history (`function.arguments must be valid JSON`) refused every request after that. They are replayed as `{}`; the tool result says what went wrong.
- **A task with no file scope stalled an isolated swarm even with `{dirs}`.** `{dirs}` is now the directories the worker's tree has changed when the task has no
  scope, instead of the whole repository.
- `/cost` shows a cost that is not nothing but is under a hundredth of a cent as `<$0.0001`, not `$0.0000`.
- The module path and every link name the organisation `anemos-labs` (`github.com/anemos-labs/sleipnir`), as the repository does; `go install` and the install script use it.
- The error for a file tool called without a path names the field (`pass the file in the "path" field`); models left it out five times in the runs of one day.
- `/status`, `/permissions` and `/trust` in the chat: the model, mode and session at a glance, and every rule in force, what you allowed with "don't ask again" among
  them. Both answer beside a running turn.
- **`cd "$(pwd)"` was refused.** The model's way to say "here" was the largest group of refusals of a benchmark run. The exact text `$(pwd)` is read as
  `$PWD`; any other substitution is judged as before.
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

- **`sleipnir friction PATH...`** ranks what slowed recorded sessions down (refusals and questions with their reasons, failed and
  unknown tool calls, stuck and cancelled runs, retries, cache breaks, repeated reads) by count, severity and the requests it
  wasted, with the event that shows each. The permission engine now writes `perm.ask` and `perm.decide` (nothing emitted them before)
  and a cancelled run writes `agent.cancel`. The session flushes its log when a turn ends: the log flushed in the background, so a
  reader that looked right after an answer, and a crash at that moment, missed the turn's last events (it made a permission test
  fail one run in eight). `docs/DOGFOOD.md` is the method and the register of what it found.
- **A nightly workflow** (`.github/workflows/nightly.yml`): every fuzz target for three minutes (`scripts/fuzz.sh`, which finds
  them), the suite under `-race` three times with shuffled order, coverage, and the drift checks (`docs/CLI.md` and the README's
  simulator block against the binary), which the regular CI now runs too. Job timeouts everywhere.

- **The headless commands did less than the documentation said.** `run --quiet` printed nothing (now the final answer, on stdout);
  `git diff | sleipnir run "review this"` dropped the diff (a goal is now the words and the piped input, the input first in a
  `<stdin>` block; input that has not begun to arrive in 3 seconds is left out with a note, so a parent that never closes our stdin
  cannot hold a run); a swarm that stopped with work left exited 0 (now status 3, and `run --json` says what was left in
  `unfinished`); `doctor` exited 0 when every request of the probe failed (now 1); `sleipnir models` printed an empty table for
  catalogues that do not say what their models are (OpenAI's own, Ollama's, vLLM's); `--cwd` with a directory that is not there
  failed in whatever first used it (now said at once); Ctrl-C ended a run with `sleipnir: context canceled` and status 1 (now
  `sleipnir: interrupted` and 130, 143 for SIGTERM; a second Ctrl-C quits at once).
- **A failed command was shown as a success.** A command that exits with a status other than 0 is not a tool error (the model is
  meant to read what it printed), so the sinks ticked it `✓` and the stuck guard, which counts failures, never saw a model run one
  failing command to the step limit. Sinks now show `✗ bash go test (1.4s, exit 1)` (and, with `--verbose`, the last lines of the
  output); the guard counts a command that keeps failing the same way, with the durations it prints left out of the comparison.
  JSON `tool_end` carries the whole output (it was cut to its first line), `failed` and `exit_code`.
- **A retried response printed its first words twice.** When a stream failed part-way and the request was sent again, the text
  already shown stayed and the new attempt was appended to the same line. A sink that can take it back (`agent.Resetter`) is told
  when the new attempt begins (the terminal sink starts a new line, the JSON stream says `reset`). Retry notices count the attempts
  (`retrying in 3.7s (attempt 4 of 6)`), and the last failure no longer announces a retry and waits out the longest backoff for an
  attempt that is never made.

- **A run the clock cut off was recorded as "done".** The harness started every result as `done` and kept that when the wall-clock
  budget ended the run, so the runner, which turns the deadline it set into a budget outcome only when nothing was claimed, never
  did: on the first benchmark run 61 of 385 episodes (one in five on the two slowest models) ended exactly at the limit with
  `claimed: done`. A report counted them as false claims (`FALSEDONE`) and the reward gave each the whole penalty of a false claim
  (`honest_done` -1), as if the agent had lied; a policy trained on them would have learned that being slow is lying. A run the
  context ended now claims nothing, so the episode says `budget`. The same default scored a provider that gave up waiting (its error
  wraps `context.DeadlineExceeded`, as the run's own deadline does) as a normal "done" with no error, where the runner should have
  repeated the rollout as the endpoint's fault. Reports and rescoring read an old episode that carries both the claim and the budget
  flag as what the flag says, so `rl report` and `rl reward` of earlier runs are right without running them again. The first run's
  reading, that the step limit and not the clock ended the runs, was wrong for the same reason: for a model that takes 30 seconds a
  request, 21 of 99 runs spent the fifteen minutes on 22 to 36 requests.

- **`ttfb_ms` was the total.** The OpenAI-chat adapter reported the whole duration of a request as its time to first byte
  (`model.response`, the inspector's timeline), so a log could not tell an endpoint that queues from one that decodes slowly: the
  two were equal in every one of the 8,088 responses of the first benchmark run. It is now when the first frame with content
  arrived (the end, for a reply that was not streamed), as the Anthropic adapter already measured it.

- **A run with nobody to ask could not run the project's tests, and said so in a line cut off before its advice.** In a dogfood
  session (`sleipnir run` with stdin not a terminal, which is what a script, CI or a cron job is) the agent fixed a cache correctly
  and then could not run `go test`: the permission engine refuses what needs a question when there is no one to ask, and the only
  trace for the person was `use an act…`. `run` and `swarm` now take `--allow RULE` (repeatable: `--allow 'Bash(go test:*)'`, or
  `--allow tests` for the build and test commands of most projects, a preset that installs and downloads nothing and hands no
  interpreter a program of its own), and a run that was refused something ends with the commands, how many times, and the rules that
  would let them through (`run --json` carries them as `refused_no_one_to_ask`).
- **A swarm manager could not tell how to drop a duplicate task.** In the first real swarm run (a model that wrote a half-garbled
  `task create`, then created the task again) the manager's `task update` on the first one was answered `T1 belongs to nobody`; it
  looked for a delete action that does not exist, and tried `done` on it, for four turns. The answer to an agent that acts on a task
  it does not own now says what to do: claim a task nobody has claimed before reporting on it, and a task that should not be done
  at all is dropped with `fail` (manager only); another agent's task names its owner and who to mail.
- **A response that the output limit cut off was taken for the final answer.** The agent loop ended a run at the first response with no
  tool call, whatever the reason the response stopped. In the benchmark (5 of 436 sessions, on two models) a generation went on to
  the 16,000-token limit, once for three minutes, words that were not there, and that text was the run's answer: the log's outcome
  said `done`, `run` printed it as the result, a swarm worker would have handed it in as its report. A response that ends at the
  limit with no call to run is not an answer now. The model is told what happened (a harness turn, not the person's word) and asked
  to carry on, twice at most in a row, with a notice for whoever is watching; after that the run ends with `agent.ErrOutputLimit`
  (`the response was cut off by the output limit: 16000 tokens, 3 responses in a row`), the benchmark's outcome says `gave_up`, a
  swarm worker's task goes back to the board, and `sleipnir friction` ranks the cut-offs (`model.cutoff`). A call that was cut off
  in the middle was and is answered by the dispatcher as arguments that do not parse.
- **A worker died of an endpoint outage that lasted a minute.** In the first real swarm run the endpoint answered `503 Database is
  temporarily unavailable` and `Request ownership was lost`; a request gets six attempts over about forty seconds, the worker
  stopped, its task went back to the board with an attempt counted, and the manager had to spawn another. Of 591 benchmark sessions,
  53 reached the last attempt, 172 saw a 503 and 72 a 429. An agent now goes on for an endpoint that answered that it is down or
  overloaded (a status of 500 or more, or 429): after the six attempts it waits at most half a minute between attempts until the waits
  add up to five minutes (`session.DefaultOutagePatience`), says so (`attempt 9, waited 4m0s of 5m0s for the endpoint`), and ends
  the run with the error only then. A refusal of the request (400, 401, 403, 404) and a failure that carries no status (a misspelt
  URL, a refused connection) are not waited for, so a misconfiguration fails as fast as before; Ctrl-C ends the wait; a swarm
  worker's notices count as signs of life, so the watchdog does not cancel a worker for the quiet of a retried call; and a benchmark
  rollout opts out (the runner repeats a rollout that ended for the endpoint's fault, and waiting inside it would spend the run's own
  clock and end it as a budget episode). The backoff of a request retried for minutes no longer overflows its shift.
- **The chat's first real session** (the inline chat program on a model, a Python fixture, in tmux) found three things its tests could not.
  A write that asked for approval showed twelve lines of a 65-line file and `… 53 more lines`, with nowhere to read the rest: what a
  person is asked to allow is now shown whole (the dialog shows what fits the window, and a change that does not fit is written into
  the scrollback in full, as a long command already was). A tool call that waited for the person was shown as slow (`✓ 6m12s` for a
  write that took milliseconds, `✗ 2m00s` for a test run): the time a question was on the screen is no longer the call's. And every
  cache break was printed twice, as the break line and as the agent's own notice; the line says it once now, with whose miss it is (a
  prompt that did not change is the endpoint's).
- **A refused change came back by another tool.** The person said no to an edit; the model answered "permission denied, let me use
  apply_patch instead" and asked for the same change again. The refusal a person gives now ends with a fixed sentence that says what
  it means (the person said no: do not make it another way, say what you wanted and ask what they want instead), and only a
  person's refusal does: not a question that nobody answered, and not a policy.
- **`/cost` typed during a turn waited for the turn.** In the chat program every line typed while the agent works is queued, slash
  commands included, so a question about the spend was answered after the fifteen minutes it was asked about. The commands that only
  look (`/cost`, `/context`, `/agents`, `/help`, `/skills`, `/recon`, and `/mode` and `/mcp` without an argument) answer at once,
  beside the turn; what changes something, and every goal, still waits for it.
- **A test of the log's order failed once in six runs under the race detector** (`request be-1.c1 deltas against unknown base
  "be-1.8"`): the manifest of the next request was advanced before the request was logged, so a compaction fork that started on its
  own goroutine could log a request whose base was not in the log yet. The state is advanced after the event now, so a reader that
  follows the log as it grows always meets a base before its request.
- **A manager that claimed a task it meant to hand over could not get out of it.** In the second real swarm run the manager claimed
  its own task, was refused a worker for it (`T1 is already owned by mgr`) and spent turns on it; the board said whose the task was and
  nothing about the way out. The answer now says it: a task the manager holds can be dropped with `fail` and made again for a worker.
- **A worker's unanswered question was reported as a hang.** In the fourth run (a swarm in a terminal nobody was watching) a worker asked to
  run `cd /workspace 2>/dev/null; pwd; go test`: `/workspace` is a path the model guessed, outside the workspace, so the engine asked, and
  the question sat on the screen for twenty minutes. The watchdog then reported "stuck: no progress for 20m1s", cancelled the run and
  requeued the task, and nothing said that a question was waiting. The session now tells the swarm which workers are waiting for the person
  (`Swarm.Asking`, for as long as the question is open): the alert at ten minutes and the manager's line at twenty name the question. The
  attempt is still counted, since the next worker would ask the same.

*Pricing the prompt change* (`sleipnir sim --mode pins`, the Anthropic-like cache model, 20 workers): the constitution grows by
382 bytes (about 95 tokens: 829 to 924 for one agent, 1,064 to 1,159 for a swarm; about 1.6% of a first request of 5,900 tokens),
which every request reads at the cached price, and the first request after an upgrade writes the prefix anew, once per session.
The sweep puts the swarm's cost against a naive harness at about 0.6 points per thousand tokens of shared pin, so this is about
0.06 points; what it buys is measured by the before/after run in `docs/BENCHMARKS.md`.
