# PARITY: the web UI against the command line and the terminal UI

The owner approved the v3 mock and asked for "whatever is needed to make it usable and at least on par with args and tui". This file
checks every command, flag, environment variable and configuration key of the CLI, and every key, page, dialog, slash command, status
indicator and recovery path of the terminal UI, against what v3 does (`v3/sleipnir-web.html`, `_src/v3/src/js`, FEATURES.md,
CONTRACT.md). For each gap it proposes the smallest **additive** control, in the mock's own vocabulary (COMPONENTS.md: `panel`/`ph`,
`btn sm|pri|danger`, `tag ok|warm|err`, `seg`, `field`, `.tbl`, `ui.modal`, `ui.confirm`, `popover`, the Schedule log pane `term`,
the Workspace diff body, `.qopt`, toasts). No existing control changes look or behaviour except where an approved decision already
changes it.

Source references are to this tree at 08c4482. "TUI" is `sleipnir chat` on a terminal (`internal/tui/app`, `cmd/sleipnir/chat*.go`);
"watch program" is the full-screen program of `watch`, `replay` and `demo` (`internal/tui/app/program.go`).

**Status words.** `covered`: a view, control, palette entry or page does it. `runner`: only the generic Runner form (generated from
the CLI spec, every flag settable) does it; acceptable for batch and lab tools. `weak`: present but incomplete or not usable as is.
`MISSING`: nothing in v3 does it. `n/a`: irrelevant in a browser, with the reason (the reasons are collected in the REJECT-LIST,
section 4.2). The `A`-numbers point to the gap specifications of section 3.

**Decisions taken as approved** (FEATURES.md section 12, as relayed by the owner): D-01 (a) terminal sign-in with "check again";
D-04 **(c) a ✗ mark on a failed task's card** (FEATURES.md recommended (a)); D-06 **(b) `/swarm N`, Run settings Apply and "run it
again" carry the conversation** (FEATURES.md recommended (a)); D-09 (a) question header by kind; D-10 (a) a recorded session opens as
a read-only tab; D-02 and D-03 move to **(b)** because the owner named those controls (delete selected recorded sessions, schedule
edit and pause, accept verified, worktrees list, verify output); every other D as recommended. FEATURES.md section 12, the Run
settings copy "the chat starts empty" (98-ui-settings.js:117, 87-ui-sheets.js:171) and VOCAB.md 8.4 must follow D-04 (c) and D-06
(b); those edits belong to their owners and are not add-list items.

Contents: 1 CLI matrix · 2 TUI matrix · 3 Gap specifications · 4 ADD-LIST and REJECT-LIST · 5 Findings outside the web

---

## 1. CLI matrix

### 1.1 `sleipnir chat`: every flag

NS = New session dialog (87-ui-sheets.js:187), RS = Settings › Run settings (98-ui-settings.js:113).

| Flag | Web today | Status | Gap |
|---|---|---|---|
| `--allow` (repeatable) | NS allow-rule tags; Permissions "Add for this session", `+ the tests preset`, `/allow` | `covered` | |
| `--budget-usd` | NS budget; Settings › Budget; `/budget` | `covered` | NS seed is the literal `5`, not the server's default: A4 |
| `--commit` | NS and RS checkbox | `covered` | |
| `--continue` | Sessions `↺ --continue`; Resume dialog | `covered` | with other flags: A24 |
| `--cwd` | NS directory select (the projects list) | `covered` | free paths are refused by design (CONTRACT 6); `sleipnir web --project DIR` adds one |
| `--isolation` | NS and RS seg | `weak` | seeded `worktree` whatever `swarm.isolation` says: A4 |
| `--mailman` | NS and RS checkbox | `weak` | cannot express `--mailman=false` against `swarm.mailman: true`: A4 |
| `--mode` | NS radio of five; mode menu; Mode sheet; Permissions | `covered` | |
| `--model` | NS select; Settings › Models | `covered` | NS seed literal: A4 |
| `--no-anim` | Appearance › Motion; `/anim` | `covered` | per browser |
| `--no-mcp` | NS and RS checkbox | `covered` | |
| `--plain` | none | `n/a` | how a pipe is served (CLI.md:1827) |
| `--resume` | Resume dialog; Sessions `↺ Resume`; `/resume id` | `covered` | with other flags: A24 |
| `--role-model` (repeatable) | NS role tags; Settings › Roles & effort | `covered` | |
| `--swarm` | NS workers (0..12); RS stepper; `/swarm N` | `weak` | the cap 12 is the mock's; the CLI's ceiling is `swarm.max_workers`: A4 |
| `--trust-project` | NS and RS checkbox; trust step | `covered` | |
| `--verbose` | `/verbose` (team feed) | `covered` | |
| `--verify` | NS and RS field | `covered` | |

### 1.2 `sleipnir run` and `sleipnir swarm N GOAL`

| Flags | Web today | Status | Gap |
|---|---|---|---|
| `PROMPT` / `N GOAL`, `--allow --ask-timeout --budget-usd --capture --commit --context-window --continue --cwd --isolation --json --mailman --max-steps --mode --model --no-mcp --no-recon --no-web --quiet --resume --role-model --session-dir --swarm --trust-project --verbose --verify` | Runner form (mode `net`, `priv` with bypass/yolo) | `runner` | |
| Watching the run's agents while it works (cockpit, board, questions refused) | the run's output only, in the runner pane; its session cannot be opened live | `MISSING` | A7 |
| A long run outliving the Runner view | D-05 (a) kills it when the person leaves the view | `weak` | A22 |
| End report: `integration: …`, `verify … the gate ran N times`, `changed: …`, exit 3 `unfinished` | stdout/stderr lines in the runner pane | `runner` | |

### 1.3 `sleipnir web`

| Flags | Web today | Status | Gap |
|---|---|---|---|
| `--addr --open` | start of the server | `n/a` | |
| `--model --mode --swarm --budget-usd --isolation --verify --commit --mailman --role-model --allow --trust-project --no-mcp --cwd` ("the defaults of the sessions started in the page", CLI.md:302) | the first tab honours them (CONTRACT 1); the New session dialog ignores them (literal seeds) | `MISSING` | A4 |
| `--resume --continue` | the first tab | `covered` | |
| `--ask-timeout --ask-grace --project` (B1, CONTRACT 1) | server; the `headless` tag shows `askTimeout` | `covered` | |

### 1.4 Every other command

| Command and flags | Web today | Status | Gap |
|---|---|---|---|
| `init` (`--user --model --local-url`) | Runner (`priv`) | `runner` | `init --user` without `--model` asks on a terminal; the runner's stdin is closed |
| `config` (`--trust-project`) | Settings › Config layers: layers, effective values, origins | `weak` | warnings with file:line and "configuration is valid" are not shown: A19 |
| `config --json` | Runner | `runner` | |
| `sessions` (`--dir -n`) | Sessions › Recorded | `covered` | the model column of the CLI is absent: A27; `--dir`, `-n` `n/a` |
| `sessions prune` (`--older-than --keep --yes --dir`) | Sessions › Prune panel | `weak` | the "kept Git branch X: reason" salvage lines are not shown: A23 |
| delete chosen recorded sessions (no CLI; owner) | none | `MISSING` | A6 |
| `mcp`, `mcp list` (`--trust-project --cwd`) | Settings › MCP servers | `covered` | |
| `mcp approve NAME` (`--yes`) | Approve (confirm) | `covered` | D-08: applies at the next start; A21 offers the restart |
| `mcp revoke NAME` | Revoke | `covered` | |
| `mcp test [NAME...]` | Test per card | `covered` | |
| `trust`, `trust list` (`--cwd`) | Settings › Trust | `covered` | |
| `trust add` (`--yes --cwd`) | "Trust these files" (confirm) | `covered` | |
| `trust forget` (`--all --cwd`) | Forget per row | `weak` | `--all`: A31 |
| `models` (`WORDS --filter --tools --reasoning --fav --min-context`) | Settings › Models filters | `covered` | |
| `models --max-price` | a "max input $/M" select | `weak` | the flag is the output price (models.go:166): A20 |
| `models --all`; catalogue refresh | none | `MISSING` | A20 |
| `models --provider --base-url --api-key-env` | Runner | `runner` | |
| `models fav add`, `fav rm`, `fav list` | star; `★ favourites` | `covered` | |
| `doctor` (`--model --base-url --deep`) | Doctor view | `covered` | |
| `doctor` (`--provider --api-key-env --json --no-affinity --capture --trust-project`) | Doctor "Open in the runner" | `runner` | |
| `recon` (`DIR --budget`) | Runner; palette `/recon` | `runner` | `/recon` means the session's pinned layer in the chat: A25 |
| `inspect` (`--json --session`) | Runner | `runner` | |
| `inspect` dashboard (`SESSION --addr --token --open --interval`) | Cache, Replay views; recorded sessions through D-10 | `covered` | its own server: `n/a` |
| `watch SESSION` (`--view --agent --poll --no-anim`) of a session this server hosts | Cockpit, Cache, Mail, Board | `covered` | |
| `watch SESSION` of a session another process writes (a runner `run`, a daemon job, a terminal chat) | none | `MISSING` | A7 |
| `replay SESSION` (`--view --agent --from --until --length --speed`) | Replay view; recorded read-only tab (D-10) | `covered` | |
| `replay --record --final --gallery --out --fps --hold --cols --rows` | Runner | `runner` | |
| `login [PROVIDER]` | Providers "Sign in…": the terminal command, "I ran it: check again" (D-01 a) | `covered` | by decision |
| `logout PROVIDER` | Sign out | `covered` | |
| `schedule`, `schedule list` | Schedule › Jobs | `covered` | |
| `schedule add` (`--cron --cwd --mode --budget-usd GOAL`) | Add a job | `covered` | |
| `schedule add --model` | none in the form | `MISSING` | A12 |
| `schedule rm ID` | Remove (confirm) | `covered` | |
| run a job now (no CLI; owner) | Run now + Log (10.3.3) | `covered` | |
| edit or pause a job (no CLI; owner) | none | `MISSING` | A11 |
| `daemon` (`--once`) | Start/Stop the daemon; "Run what is due (--once)" | `covered` | |
| `daemon --every` | none | `MISSING` | A12 |
| `update` (install) | Tools › update → Runner (`priv`), `POST /api/update/install` | `runner` | |
| `update --check`; the chat's update notice | `GET /api/update` exists; nothing shows it unless asked | `MISSING` | A14; the spec lacks `--check`: A30 |
| `demo` (`--scenario --scale --topics --dir --view --plain --no-anim`) | Runner (the report) | `runner` | |
| `mock` (`--addr --engines`) | Runner (`server`) | `runner` | |
| `sim` (`--agents --engines --mode --provider --seed --json`) | Runner | `runner` | |
| `friction` (`PATH --top --json --category --min-count --examples`) | Runner | `runner` | |
| `rl compare, eval, expand, export, report, reward, rollout, serve, show, verify`; `rl taskgen composite, fixture, git, mutate, recall`; `rl tasks check, filter, split, stats, validate` (every flag) | Runner (`run`/`net`/`server`) | `runner` | hours-long `rl eval`/`rollout` die when the view is left: A22 |
| `version`, `help` | Runner; `?` Help sheet | `covered` | |
| `web` | the page itself | `n/a` | needs a spec entry that refuses: A30 |
| `chat` | New session dialog and the composer (the runner refuses it, D-11) | `covered` | |
| `chat-record`, `term-svg` (hidden) | none | `n/a` | |

### 1.5 Environment variables

| Variable | Web today | Status | Gap |
|---|---|---|---|
| `SLEIPNIR_MODEL`, `SLEIPNIR_MODEL_<ROLE>`, `SLEIPNIR_PERMISSION_MODE`, `SLEIPNIR_<SECTION>_<FIELD>` (config layer, `internal/config/env.go`) | Config layers: the environment layer and each value's origin | `covered` | |
| provider keys (`<NAME>_API_KEY`, `HF_TOKEN`) and `<NAME>_BASE_URL` | Providers: `✓ env VAR`, base URL; sign-out `409 env` | `covered` | |
| `SLEIPNIR_HOME` | the server's state directory (sessions, schedule, trust) | `covered` | |
| `SLEIPNIR_NO_UPDATE_CHECK` | no update notice exists yet | `MISSING` | A14 must honour it |
| `SLEIPNIR_BELL` | no bell analog | `MISSING` | A3, A29 |
| `SLEIPNIR_ANIM`, `REDUCE_MOTION` | Motion "auto" follows the browser, not the server's environment | `weak` | A29 |
| `NO_COLOR`, `TERM`, `COLORTERM`, `LANG`/`LC_*`, `COLUMNS`/`LINES`, terminal detection | none | `n/a` | |
| `SLEIPNIR_INSPECT_TOKEN`, `SLEIPNIR_DUMPABLE`, proxies, `BRAVE_API_KEY`/`TAVILY_API_KEY`/`SEARXNG_URL` | none (the TUI shows none of them either) | `n/a` | |

### 1.6 Configuration keys

Neither the CLI nor the TUI edits `config.json` except `models.favorites` (`/fav`, `models fav`) and the first-run `models.default`
(pick.go); everything else is read, and changed for one session at run time.

| Keys | Web today | Status | Gap |
|---|---|---|---|
| `models.default`, `models.roles.*` | Models "Use for", Roles selects (session); Config layers (read) | `covered` | first-run write: D-12 |
| `models.favorites` | star | `covered` | |
| `permissions.mode/allow/ask/deny`, `permissions.roles.*` | Permissions: mode, session rules, rules in force with origins | `covered` | |
| `swarm.max_workers`, `swarm.isolation`, `swarm.mailman`, `swarm.budget_usd` | read in Config layers; not used to seed NS | `weak` | A4 |
| `swarm.requests_per_minute`, `swarm.max_concurrent_requests` | HUD governor; Config layers | `covered` | |
| `cache.*`, `tools.*` | Config layers (read-only, as in the TUI) | `covered` | |
| `providers.*` | Providers; Config layers (redacted) | `covered` | |
| `hooks.*` | Skills, commands & hooks › Hooks | `covered` | |
| `mcp.*` | MCP servers | `covered` | |

---

## 2. TUI matrix

### 2.1 Keys of the chat program (`internal/tui/app/chat.go`, `internal/tui/input`)

| Key | TUI | Web | Status | Gap |
|---|---|---|---|---|
| Enter; `\` at line end, alt+Enter, ctrl+j | send; newline | 2.17, 2.18 | `covered` | |
| paste of many lines | `[pasted text #N +L lines]` chip | 2.23 | `covered` | |
| ↑ ↓, ctrl+r | history | 2.20, 2.21 | `covered` | |
| `/`, `@`, Tab | command and path completion | 2.24, 2.25 | `covered` | |
| menus after `/model `, `/resume `, `/roles `, `/effort ` | choose from a list | the pages and dialogs those commands open | `covered` | |
| shift+Tab | default → accept-edits → plan | 2.19 | `covered` | |
| Esc while busy | cancel the turn | 2.2 | `covered` | |
| ctrl+c while busy | cancel the turn or the running command (`/compact`) | ctrl+c only discards the line; nothing cancels a compaction | `weak` | A26 |
| ctrl+c at the prompt, twice | clear, then quit | Close confirm (D-14 a) | `covered` | |
| ctrl+d | quit (queued after a running turn); at a question: refuse | none | `n/a` | Esc answers 3 |
| ctrl+t / ctrl+g | stats page / cockpit | alt+t / alt+g, `o` `c` (2.5, 2.6) | `covered` | |
| ctrl+o | expand the newest collapsed output | 2.7 | `covered` | |
| ctrl+l | redraw | none | `n/a` | |
| question: 1 2 3 | answer | 2.1 | `covered` | |
| question: 4 (build/test command) | allow builds and tests this session | none | `MISSING` | A2 |
| question: ↑ ↓ Tab shift+Tab ctrl+p ctrl+n, Enter | move the choice, answer | Tab to a `.qopt` button, Enter | `covered` | |
| question: Esc | No | 2.2, 4.3.4 | `covered` | |
| stats page: ↑ ↓ PgUp PgDn, Esc/ctrl+t | scroll, close | the Cache view scrolls natively; Esc | `covered` | |

### 2.2 Keys of the watch program (`program.go:162`)

| Key | TUI | Web | Status | Gap |
|---|---|---|---|---|
| `o c m b` | views | 2.11 | `covered` | |
| `1 2 3 4` | views | digits answer questions | `n/a` | |
| Tab, shift+Tab | next and previous view | browser focus order | `n/a` | |
| ↑ ↓ (`j` `k`) | choose the agent | 2.16, 2.29 | `covered` | |
| `f` | follow the agent answered last | the Cache view starts on a fixed agent | `weak` | A28 |
| Space (`p`) | pause | 2.12 | `covered` | |
| `a` | animation off | `/anim`, Appearance › Motion | `covered` | |
| ← → (`H` `L`), shift | seek 10 s, a minute | 2.13 | `covered` | |
| Home (`0`), End (`$`) | start, end | 2.14 | `covered` | |
| `+ = ]`, `- _ [` | speed | 2.15 | `covered` | |
| `?` (`h`, F1) | key help | 2.10 | `covered` | |
| Esc | back to the cockpit | `o` | `covered` | |
| `q`, ctrl+c, ctrl+l | quit, redraw | none | `n/a` | |

### 2.3 Pages and panels

| TUI page or panel | Web | Status | Gap |
|---|---|---|---|
| scrollback transcript (prompts, answers, tool records with ctrl+o, notes, compaction fold record, cache-anomaly lines) | Radio rail transcript and Team activity (4.1) | `covered` | |
| stats page (cost, tokens, requests, hit, saved ≈/at least, layer stack, "estimated reuse window") | Cache view, HUD rings, prefix warm ring | `weak` | "saved at least" when some prices are unknown: A16 |
| cockpit page (auto-opens when a worker starts; a question closes it) | Cockpit view; the rail is always beside it; question banner when collapsed | `covered` | |
| `/agents` in a single agent ("a single agent: /swarm 8 starts …") | solo panel, ghost stalls with the `/swarm N` hint | `covered` | |
| watch › cache view | Cache view | `covered` | |
| watch › mail: sent, delivered, not delivered, digests in batches, mailman state, tokens per message, "who writes to whom" pairs with counts | Mail view: arcs, envelopes, "routed N · dup 0 · mailman on/off" | `weak` | A18 |
| watch › board: counts with blocked and failed, tasks with "try N" and "after T…", merge queue counts, "no worktree isolation" note, alerts band | Board view: kanban, dependency graph, summary of four counts | `weak` | A17 (failed: D-04 c) |
| banner: version, model, cwd, budget, team, update notice, resumed line and recap | HUD, strip, footer, resumed history (4.1.13) | `weak` | update notice: A14; restored permissions in the resumed line: A27 |

### 2.4 Dialogs and menus

| TUI | Web | Status | Gap |
|---|---|---|---|
| approval: title by kind ("Run a command", "Edit a file", "Write a file", "Apply a patch", "Fetch a page", "Search the web", "Allow X?") | header by kind (D-09 a) | `covered` | D-09 must include web search and write/patch: A1 |
| approval body: the whole change of an edit, write or patch as a diff; multi-line commands; "in DIR" | one line `cwd $ cmd` (85-ui-approvals.js:55) | `MISSING` | A1 |
| approval body too tall: written whole into the scrollback | none | `MISSING` | A1 |
| approval: fourth answer for build and test commands (`OffersTests`) | three buttons; VOCAB 9 drops the fourth | `MISSING` | A2 |
| quiet period 350 ms, "+N more waiting", "your typing goes to the prompt until you pause" | 0.8 s (D-17), `+N waiting`, the meter | `covered` | |
| the bell on a question; on a turn of 30 s or more | toast only while the page is in front | `MISSING` | A3 |
| MCP start question; trust question at start | refused at start, MCP page Approve (D-08); NS trust step | `covered` | |
| `/model` menu (starred first, typing filters) | Settings › Models | `covered` | |
| `/resume` menu (age, cost, first prompt) | Resume dialog | `covered` | |
| `/roles` menu | Settings › Roles & effort | `covered` | |
| `/effort` menu | Roles & effort seg | `covered` | |
| `/login` leaves the chat, runs `sleipnir login`, comes back | "Sign in…" modal (D-01 a) | `covered` | by decision |
| first-run provider and model picker (pick.go) | D-12: on the server's terminal; empty page opens New session | `covered` | |

### 2.5 Slash commands (`chatHelp`, cmd/sleipnir/chat.go:282; `programCommand`, chat_tty.go:442)

| Command | Web (FEATURES 11.4) | Status | Gap |
|---|---|---|---|
| `/goal [text\|pause\|resume\|clear]` | P1, Goal sheet | `covered` | |
| `/new`, `/clear` | P2, P3 | `covered` | |
| `/resume [id]` | P4, Resume dialog | `covered` | |
| `/sessions` | P5 | `covered` | |
| `/compact [focus]` | P6, Compact dialog | `covered` | cancel: A26 |
| `/rewind [id]` | P7, Workspace › Checkpoints, Restore | `covered` | |
| `/diff [id]` | P8, Workspace › Changes | `covered` | |
| `/exit`, `/quit` | P9, Close confirm | `covered` | |
| `/model [ref]` | P10 | `covered` | |
| `/effort [level]` | P11 | `weak` | the applied level on this model ("this model: X", chat.go:407) is not said: A27 |
| `/fav [ref]` | P12 | `covered` | |
| `/login [provider]` | P13 | `covered` | |
| `/budget [usd\|off]` | P14 | `covered` | |
| `/cost` | P15 | `covered` | |
| `/stats` | P16 | `covered` | |
| `/context` | P17 | `covered` | |
| `/status` | P18 | `weak` | the session directory is missing: A27 |
| `/mode <m>` | P19 | `covered` | |
| `/plan [prompt]` | P20 | `covered` | |
| `/allow <rule>` | P21 | `covered` | |
| `/permissions` | P22 | `covered` | |
| `/trust` | P23 | `covered` | |
| `/roles [role=m]` | P24 | `covered` | |
| `/swarm <n> [flags]` | P25 (carries the conversation, D-06 b) | `covered` | |
| `/restart [flags]` | P26 | `covered` | |
| `/agents` | P27 | `covered` | |
| `/steer TEXT` | P28 | `covered` | |
| `/verbose [on\|off]` | P29 | `covered` | |
| `/anim [on\|off]` | P30 | `covered` | |
| `/cwd` | P31 | `covered` | |
| `/recon` (the session's pinned shared layer) | P32 runs a fresh survey in the runner | `weak` | A25 |
| `/skills` | P33 | `covered` | |
| `/mcp`, `/mcp reconnect NAME` (and the prompts it lists) | P34, MCP page | `weak` | the page does not list a server's prompts: A21 |
| `/help`, `/?` | P35, Help sheet | `covered` | |
| custom commands, user skills, `/mcp__server__prompt` (args `name=value` or words) | P35+ through `POST .../command` | `covered` | |
| unknown command: "unknown command /x; did you mean /y? try /help" | toast without the suggestion | `weak` | A26 |

### 2.6 Status indicators

| TUI indicator | Web | Status | Gap |
|---|---|---|---|
| spinner and verb; Starting; Running TOOL; Waiting for your answer; Waiting for the team; Compacting; Stuck on a failing call | status line word, dot colour, stall states (1.4.1, 3.1) | `covered` | |
| "Waiting for the model (1m 05s)" after 45 s without an answer (chat_live.go:63, :390) | none | `MISSING` | A13 |
| flash of the status line on a cache break | flash on the prefix bar, cache-break marks | `covered` | |
| elapsed, ↑prompt ↓out of this turn, turn cost, "esc to interrupt" | footer status line | `covered` | |
| running tools with elapsed and worker tag, "+N more running", "waiting for your answer" on the asking call | manager card "doing", stalls, gantt open segments | `covered` | |
| queued lines `⏎ queued: …` | queued lines above the composer (4.2.2) | `covered` | |
| footer: mode in its colour, page keys, model · session | composer mode button, footer model · sid | `covered` | |
| transient hint ("mode: plan") | toast | `covered` | |
| turn summary `── 14s · 12 steps · $0.0021 · cache hit 87%` | `final` row `-- Ns · N steps · $x` | `weak` | no hit: A27 |
| goal judge's note after a turn | plan box verdict line | `covered` | |
| `(cancelled)`; errors and warnings | `sys` rows, toasts | `covered` | |
| provider hints: refused key ("enter it again with /login …"), unknown model (404), no credit (402) (main.go:202) | the error text only | `MISSING` | A15 |
| budget stop message with how to raise it | "budget reached: the goal is paused" | `covered` | |
| update notice under the banner (update.go:32) | none | `MISSING` | A14 |
| "saved ≈ $x" / "saved at least $x" (chat_pages.go:104-110) | "saved est." always | `weak` | A16 |
| unknown prices: "price unknown" on models and roles | Models and Roles pages | `covered` | |
| cost of agents whose model has no price (uncounted tokens) | HUD cost says nothing | `weak` | A16 |
| NO_COLOR, ASCII glyphs by locale | none | `n/a` | |
| REDUCE_MOTION, SLEIPNIR_ANIM=0, `--no-anim` | Appearance › Motion (auto follows the browser) | `weak` | A29 |
| bell (SLEIPNIR_BELL) | none | `MISSING` | A3 |
| tool durations exclude the time a question waited (chat.go:172) | tool row times are not specified to exclude it | `weak` | A27 |

### 2.7 Recovery paths

| TUI path | Web | Status | Gap |
|---|---|---|---|
| interrupt a turn (Esc, ctrl+c): the main agent's questions are refused, workers' survive, the goal pauses | Esc, Interrupt turn, Stop run (CONTRACT 8) | `covered` | |
| cancel the start of a session | close the starting tab | `covered` | |
| cancel a running `/compact` | none | `MISSING` | A26 |
| resume after a crash: `--continue`, `/resume`, `⚠ interrupted`, the resumed line, recap, pages rebuilt from the old log | Resume dialog, Sessions, resumed history (D-15) | `covered` | |
| "Back as they were: the mode X and N allow rules" (chat_tty.go:302) | not in the resumed row | `weak` | A27 |
| compaction on demand | Compact dialog | `covered` | |
| rewind to a checkpoint, with the note to the agent | Restore with a safety checkpoint and undo | `covered` | |
| plan mode, `/plan prompt` | mode menu, P20 | `covered` | |
| goal pause, resume (continuations extended), clear | Goal sheet | `covered` | |
| budget reached → raise it → resume the goal | Budget page, Goal sheet | `covered` | |
| a refused key → `/login` → the chat comes back | Providers "Sign in…" (terminal), "check again" | `covered` | by decision |
| integration of an isolated run at its end (applied, committed, or kept on a branch with the one command that gets it) | the tab closes with "closed X" | `MISSING` | A5 |
| verified work applied whenever the manager stops (`applyMerged`), failures said once | `sys` rows from notices | `covered` | |
| a stuck agent stopped; a stall raised and cleared | stuck state; `sys` rows (VOCAB 6.3) | `covered` | |
| `/mcp reconnect NAME` | MCP Reconnect | `covered` | |
| a failed gate sent back (see what failed) | bounced count only | `MISSING` | A9 |

---

## 3. Gap specifications

Each item: what the TUI or CLI does, the control to add (where, the component it reuses, its copy), the backend, the decisions it
touches. Severity **blocks** = blocks daily use; **nice** = nice to have.

### A1 · Questions show the change they ask for · blocks
- TUI: the dialog body is the whole change (Edit: old→new per edit; Write: a diff against the file's current text; apply_patch: the
  patch), a multi-line command in full, and a body too tall for the screen is written whole into the scrollback
  (`internal/tui/app/chat_dialog.go:170-327`, `chat_pages.go:32-39`). Web: `cwd $ cmd` on one line.
- Control: in the question strip and in each inbox row, under `.qcmd`, a `.qdiff` block that reuses the Workspace diff body
  (97-ui-workspace.js hunk lines, attribution gutter off), at most 40% of the rail's height, scrolling. Head:
  `the change it asks to make · path · +a −d`, with `btn sm` **Open it whole** → `ui.modal` (wide) with the same body. `.qcmd` gets
  `white-space: pre-wrap` so every line of a command shows.
- Backend: B1+B3, new. The bridge adds `q.change` (unified text, at most 256 KiB, `[diff truncated]` as `internal/checkpoint/diff.go`)
  and `q.path` from the call's input, which the host sees as the TUI does (`ToolStart`; `callOf`, chat_pages.go:48). The diff part of
  `changeBody` and `currentText` moves into a pure helper shared with the TUI. VOCAB 9 `ask.q` gains `change`, `path`.
- Decisions: D-09 (a) kinds complete the TUI's titles: "wants to edit a file", "wants to write a file", "wants to apply a patch",
  "wants to fetch a web page", "wants to search the web".

### A2 · The fourth answer: builds and tests for the session · blocks (teams)
- TUI: when `Request.OffersTests`, the options are Yes / Yes, and don't ask again for X / **Yes, and allow builds and tests (go, npm,
  cargo, pytest, make…) for this session** / No (esc); keys 1-4 (chat_dialog.go:93-99, :133; chat_live.go:572). One answer covers
  every worker's `go test`. Web: three buttons (VOCAB 9 drops it).
- Control: when `q.offersTests`, a fourth `.qopt` in third place, `<kbd>3</kbd>` with that label; No becomes `<kbd>4</kbd> … (esc)`;
  meter copy "ready: press 1, 2, 3 or 4 (esc is 4)". Same in the inbox and the answered strip. Keys 2.1 gain `4` when offered.
- Backend: B1. Wire values stay semantic: `choice` 3 = No (unchanged), new `4` = the tests preset, accepted only when the question
  offered it (`400 bad_choice` otherwise) → `perm.Decision{Allow: true, Remember: ScopeSession, Preset: perm.PresetTests}`; `meta`
  rules patch with origin `the tests preset`. The page maps key 3 → `4` and key 4/Esc → `3` when offered.

### A3 · The browser's bell: a title badge · blocks
- TUI: the terminal bell rings for each question and when a turn of 30 s or more ends (chat_pages.go:25, chat.go:924);
  `SLEIPNIR_BELL=0` silences it. Web: toasts reach only a page that is in front.
- Control: page only (C1). `document.title` becomes `(? N) Sleipnir` while any tab has open questions; when a turn of 30 s or more
  ends while `document.hidden`, `✓ <tab> · Sleipnir` until the page is seen. Appearance › Hover card gets one row **Tab title**
  (`seg` on / off, `localStorage`), default from A29.
- Backend: none.

### A4 · New sessions start from the server's defaults · blocks
- CLI: `sleipnir web`'s chat flags are "the defaults of the sessions started in the page" (CLI.md:302); the config's
  `swarm.isolation`, `swarm.mailman`, `swarm.budget_usd`, `swarm.max_workers` apply as in `chat`. Web: NS seeds literals
  (`worktree`, `go test {dirs}`, `$5`, `8`, max 12; 87-ui-sheets.js:188) and always sends them.
- Control: no new control. NS seeds every field from `hello.defaults`; the workers input's `max` is `defaults.maxWorkers`; when the
  mailman box is unchecked against a default of on, "the same command line" shows `--mailman=false`. RS's stepper takes the same max.
- Backend: B1. `wire.Hello.Defaults` (NewSessionRequest shape plus `maxWorkers`) built from the parsed web flags over
  `config.Load` (the values `chatOptions` would get).

### A5 · The end of an isolated run is reported · blocks
- CLI/TUI: closing an isolated session applies what passed verification and prints `integration: <message>`; when it could not be
  applied the message names the branch and the one command that gets it (`finishRun`, cmd/sleipnir/run.go:411; `Session.Finish`,
  internal/session/isolate.go:386; `swarm.IntegrationReport`, internal/swarm/isolate.go:489). Web: Close says "closed X".
- Control: the Close confirm of an isolated team adds one line: "Its verified work is applied to your checkout first (as uncommitted
  edits | as commits on <branch>)." The result toast is the report's message (`ok` when applied, `err` when not). In Sessions ›
  Recorded, a session whose result was not applied gets `⚠ not applied` beside `⚠ interrupted`, the message as its title, and
  `btn sm` **Copy the command** (the report's hint). A restart (D-06 b carries the conversation) puts the report in the new
  generation as a `sys` row.
- Backend: B1+B4. `DELETE /api/sessions/{id}` and `POST .../restart` return `integration` (the report); `RecordedSession` gains
  `integration {applied, committed, message, hint}` from the log's last `swarm.integration` event.

### A6 · Delete selected recorded sessions · blocks (owner)
- Control: Sessions › Recorded gets a leading checkbox column (header box selects all that can be deleted) and, in the panel head next
  to `↺ --continue`, `btn sm danger` **Delete selected…** (disabled while none is checked). A row hosted in a tab or locked by another
  process has its box disabled, title "open in a tab: close it first" / "another process is writing it". Confirm: title "Delete N
  recorded sessions", text "The event log, its blobs and its checkpoints of each. Edits left in worker trees are kept as Git
  branches.", detail the ids with MB, OK "Delete them". Result in the Prune note line: "deleted N sessions, freed M MB" plus the kept
  branches (A23); toast.
- Backend: B4. `POST /api/recorded/delete` (CONTRACT 7, confirm `delete:<d16>`) with prune's salvage and `session.RemoveStoredSession`.
- Decisions: D-02 → (b).

### A7 · Watch a session that another process is writing · blocks (headless work)
- CLI: `watch SESSION` follows the log of any session as it is written: a `run` or `swarm` started in the runner, a daemon job, a chat
  in a terminal. Web: those runs are text in the runner pane, and the strip's `⟳ headless running` glyph (1.2.1) has no source.
- Control: extends D-10's read-only tab. A recorded session that is live (locked by another process, or written in the last 10 s)
  shows `btn sm` **Watch** in place of Replay in Sessions › Recorded; the runner's result card of `run`/`swarm` gets **Watch it**. The
  tab opens with `⟳`, the Replay banner reads "watching <sid> · read-only · the run belongs to another process", the composer and the
  question buttons are disabled with that line as their title, the Cockpit, Board, Mail and Cache views work.
- Backend: B4. A tab kind `recorded` with `follow: true`: the server tails `events.jsonl` (`state.Tail`,
  `internal/tui/state/tail.go`) through B2's translator and publishes `ev` frames; `headless: true` in its `TabSummary`.

### A8 · Workspace › Merge: worktrees and the merge queue · blocks (owner)
- Control: a fourth tab **Merge** in the Workspace `seg` (Files, Changes, Checkpoints, Merge; chord `g` then `q`, palette "Go to
  Merge"). Left list: one row per worker tree (agent colour stripe, agent, branch `sleipnir/<sid>/<agent>`, task, head, `tag` clean /
  dirty / merged, `btn sm` Copy path). Main: the queue in order (head first: task, step rebase / verifying / verified / merged,
  elapsed, outcome tag merged / conflict / verify_failed / rejected with its reason). Not isolated: the TUI's sentence "no worktree
  isolation in this run: work is written to the checkout directly" (boardview.go:255).
- Backend: B3. `GET .../ws/worktrees`, `GET .../ws/queue` (CONTRACT 12).
- Decisions: D-03 → (b).

### A9 · Verify output · blocks (owner)
- CLI: `verify \`cmd\`: the gate ran N times; N failed and the work was sent back` (run.go:462); the swarm keeps each gate's
  `workspace.VerifyResult` with its combined output, head and tail kept (internal/workspace/verify.go:39). Web: a bounced count only.
- Control: in the Merge tab, choosing a task (or a strip chip with alt-click, or the drawer's task stepper `verify` step, which gains
  `open the verify output`) shows its gate runs: one row per attempt (`$ cmd`, exit, duration, time, `tag ok|err`) and the output in
  the Schedule log pane (`term`, out/err lines). The strip's `wmeta` gains `gate <b>ran</b> · <b>failed</b>`.
- Backend: B3, new. Record each gate run per task and attempt (`internal/swarm/verify.go`), log it (`verify.run` with the output as a
  blob) so history and replay have it; `GET .../ws/verify/{task}` → `VerifyOutput{task, cmd, runs: [{attempt, exit, ms, at, out,
  truncated}]}`.
- Decisions: D-03 → (b).

### A10 · Apply the verified work now (accept → commit) · blocks (owner)
- CLI/TUI: verified work reaches the checkout when the manager stops and at the end (`Swarm.Integrate`,
  internal/swarm/isolate.go:541), as uncommitted edits, or as commits on the branch with `--commit`.
- Control: `btn sm pri` **Apply verified work…** in the Verify and merge strip head (disabled with the title "nothing verified is
  waiting" when the integration tip equals what the checkout has). Confirm: lists the files and the tasks, `seg` "as uncommitted edits
  · as commits on <branch>" (the second disabled with its reason when the checkout is dirty or not on a branch), OK "Apply them".
  Result: the report's message as a `sys` row and a toast; `409 dirty`/`moved` show the TUI's message with the hint and **Copy the
  command**.
- Backend: B3. `POST .../ws/accept` (`Session.AcceptVerified` over `Swarm.Integrate` with a commit choice; confirm `accept:<id>`).

### A11 · Schedule: edit, pause and resume a job · blocks (owner)
- Control: each Jobs row gains `btn sm` **Edit** and **Pause** / **Resume** between Run now and Log. Edit fills the Add a job form,
  whose head reads "edit jN" and whose button becomes **Save jN** beside **Cancel**. A paused job shows `tag` paused in its next
  column, and the daemon skips it.
- Backend: B4. `PUT /api/schedule/jobs/{job}`, `POST .../{job}/pause` (CONTRACT 17; `Store.Update`, `Job.Paused`, `daemonTick` skips
  paused jobs, `schedule list` prints "paused"). Optional CLI symmetry: `schedule pause|resume ID`.
- Decisions: D-02 → (b).

### A12 · Schedule: `--model` and `--every` · nice
- Control: Add a job gains a `--model` field (select of the catalogue, first option "the default model"), between `--mode` and
  `--budget-usd`. The Daemon head, while stopped, shows `every` input (default `30s`) before **Start the daemon**.
- Backend: B4. `JobRequest.model` (schedule.go:83); `POST /api/schedule/daemon {action: "start", every}`.

### A13 · "Waiting for the model" · nice
- TUI: after 45 s without an answer the verb becomes "Waiting for the model (1m 05s)" (chat_live.go:63, :390). Web: nothing tells a
  slow or dead endpoint from thinking.
- Control: the footer status word and the manager card's doing read `waiting for the model (1m 05s)` (stalls likewise for a worker).
- Backend: B2. `state` events and keyframes carry `reqSince` (session time) from `state.Agent.ReqSince` (internal/tui/state/types.go:116).

### A14 · The update notice · nice
- Control: a `tag ok` chip `↑ v0.9.1` in the session strip, left of the connection chip, title "a newer release is out: sleipnir
  update installs it"; click opens the Runner on `update` (`priv`, confirmed).
- Backend: B1. `hello.update {current, latest}` from `updateNotice()` (cmd/sleipnir/update.go:32), which honours
  `SLEIPNIR_NO_UPDATE_CHECK`; nothing when there is none.

### A15 · Provider error hints · nice
- Control: a turn error from the provider is a `sys ⚠` row with the hint and a link button: refused key → "The provider refused the key.
  Sign in again in Settings › Providers & login; an environment variable wins over a stored key." + **Open Providers**; 404 → "The
  provider does not know this model; Settings › Models lists the names it has." + **Open Models**; 402 → the TUI's sentence.
- Backend: B1. `providerHint(err, "Settings › Providers & login")` (cmd/sleipnir/main.go:202), extracted.

### A16 · Honest prices · nice
- Control: "saved est." becomes "saved at least $x" when some cache reads had no price (the TUI's rule, chat_pages.go:107); the HUD
  cost title and the `all` spend popover add "N tokens at unknown prices are not counted" when that is so.
- Backend: B2. `use.savedPartial` from `state.Savings.Complete()`; `use.unpriced` tokens per agent.

### A17 · Board: blocked, failed, attempts, alerts · nice
- Control: the board summary adds "(N blocked)" and "N failed"; a failed card carries the ✗ mark (D-04 c) with its closure as title; a
  card whose task took more than one attempt shows `try N` (as the TUI, boardview.go:114). The Task board head gains `tag warm`
  `⚠ N alerts` when the board has alerts; it opens a `popover` listing them (kind · text · time).
- Backend: B2. `task.attempts` (state.Task.Attempts); a new `alert {s: raise|clear, kind, key, text}` kind from board.op
  `alert`/`alert-clear`/`alert-expire` (internal/tui/state/board.go:92) and `stall` raises (m.stalls already stores them).

### A18 · Mail: counts and tokens · nice
- Control: the Mail head reads "N sent · N delivered · N not delivered · N digests in N batches · mailman <state>" (the TUI's
  headline, boardview.go:191; "not delivered" in `warm` when non-zero); each envelope row ends with `· 1.2k tok`; the message's
  delivery steps say "digested by mm-1" when it was.
- Backend: B2. `mail.tok`; a coalescable `mailstat` kind from `state.Mail.Counts` and the mailman's state. Replaces the constant `dup
  0` of D-07.

### A19 · Config warnings · nice
- Control: Settings › Config layers gains a card **Warnings** (`.tbl`: file:line, message) closed by "configuration is valid" or
  "configuration is valid, with N warnings", and the project's security-sensitive settings that were left out (untrusted).
- Backend: B4. `config.Report.Warnings` and `Report.String()`'s sections in `ConfigView` (setup.go:142-168).

### A20 · Models: refresh, all, the price filter · nice
- Control: in the filter row, `btn sm` **Refresh the catalogue** (title "asks every provider again; the list is kept 6 h") and an
  `all` button in the filters `seg` ("include models that do not chat", `--all`). The "max input $/M" select keeps its meaning; the
  page's "Run as CLI" line stops claiming `--max-price` for it (the flag is the output price) and a new "max output $/M" select maps to
  `--max-price`.
- Backend: B4. `GET /api/models?refresh=1` (exists), `?all=1` (`modelFilter.All`, models.go:162).

### A21 · MCP: start again now, and the prompts · nice
- Control: after Approve, the card's outcome line reads "approved: it starts when the team starts again" with `btn sm` **Start again
  now** (restart kind `restart`, which carries the conversation). Each running card lists its prompts as tags `/mcp__srv__name` with
  **Insert in the chat**, as Your commands does.
- Backend: B4. `MCPView.servers[].prompts` from `Session.MCPPrompts` (internal/session/mcp.go:274).
- Decisions: D-08 (a) unchanged; this makes it usable.

### A22 · Long runner commands can keep running · blocks (run, swarm, rl)
- CLI: a terminal keeps `run`, `swarm`, `rl eval` or `rl rollout` going for hours. Web: D-05 (a) cancels the process when the person
  leaves the Runner.
- Control: the Runner form gains a checkbox **Keep running when I leave** (shown for `net` and `server` commands, unchecked by
  default, so D-05 (a) stays the default). A kept run stays in "recent runs" with `● running`; clicking it reattaches its output.
- Backend: B4. `RunRequest.keep`; `GET /api/runs/{run}/output?from=` (the run's retained lines, within the 20,000-line cap).
- Decisions: additive to D-05 (a); the default behaviour does not change.

### A23 · Prune reports kept branches · nice
- Control: the Prune note under "deleted N sessions, freed M MB" lists "kept Git branch X: reason" lines (sessions_prune.go:244).
- Backend: B4. `PrunePlan.kept`.

### A24 · Resume with other settings · nice
- CLI: `chat --resume ID --model X …`; TUI `/resume` keeps the current flags. Web: resume takes no flags.
- Control: Resume dialog rows gain `btn sm` **↺ with settings…**: NS opens with the recorded session's model, mode and team, a
  read-only `resume <id>` line under the title, and "Start the session" resumes.
- Backend: B1. `NewSessionRequest.resume`.

### A25 · `/recon` shows the session's map · nice
- Control: palette `/recon` shows a local card with the project map pinned in the shared layer (`s.Shared.Text()`,
  cmd/sleipnir/chat.go:495) and a button **Run a fresh survey** that opens the runner as P32 does now.
- Backend: B1. `/recon` through `POST .../command` (slashTo's output).

### A26 · Cancel and correct · nice
- Control: in the composer, ctrl+c with an empty line and nothing selected while a turn runs interrupts it (as Esc and the TUI);
  **Interrupt turn** also cancels a running compaction ("compaction cancelled"); the unknown-command toast includes the TUI's "did
  you mean /x?".
- Backend: B1. Compaction under a cancellable context in the host; `didYouMean` (chat.go:839) in the `404 unknown_command` message.

### A27 · Text fidelity · nice (one task)
- The `final` row adds `· cache hit N%`; the resumed `sys` row adds "Back as they were: the mode X and N allow rules"
  (`RestoredPermissions`, internal/session/permstate.go:58); the `/status` card adds the session directory; the effort page and `/effort`
  toast add "this model: <applied level>" (`Session.Effort`); the Recorded first-prompt cell's title adds the model (the CLI's column);
  tool durations exclude time spent waiting for an answer (B2, as `toolRun.waited`).

### A28 · The Cache view follows the newest agent · nice
- Control: until the person chooses, the Cache view and the drawer's Cache tab show the agent answered last (`Newest`,
  cacheview.go:40); key `f` (free outside fields) returns to following; the head shows `following` as a dim tag.
- Backend: none.

### A29 · Motion and bell defaults from the server's environment · nice
- Control: none new. Appearance › Motion "auto" also reduces when the server ran with `SLEIPNIR_ANIM=0` or `REDUCE_MOTION=1`; Tab title
  (A3) defaults off with `SLEIPNIR_BELL=0`. The browser's own setting still wins when the person sets one.
- Backend: B1. `hello.ui {reduceMotion, bell}`.

### A30 · CLI spec drift · nice (runner correctness)
- The spec (data/cli-spec.json, from CLI.md flag blocks) lacks `update --check` (update's help has a usage line and no flags block),
  `mcp --cwd`, `mcp test NAME...` (several names), and `web`. B4's generator curates them: `update --check` (`net`), `mcp ... --cwd`,
  `web` with a refusal "you are in it: sleipnir web serves this page" (mode `tty_only`). The drift test of CONTRACT 18.3 then holds.

### A31 · Trust: forget every directory · nice
- Control: the "Every directory you decided about" card head gains `btn sm danger` **Forget all…** (confirm listing the
  directories).
- Backend: B4. `POST /api/trust {all: true, on: false}` (`trust forget --all`, trust.go:62).

---

## 4. ADD-LIST and REJECT-LIST

### 4.1 ADD-LIST, ranked

Each line is one builder task. Owner phases as in FEATURES.md.

| Rank | Item | View | Phases | Severity |
|---|---|---|---|---|
| 1 | A1 Questions show the change they ask for | Radio rail, inbox | B1+B3, C1 | blocks |
| 2 | A2 Fourth answer: builds and tests | Radio rail, inbox | B1, C1 | blocks |
| 3 | A5 End-of-run integration report | Sessions, Close, restart | B1+B4, C1+C3 | blocks |
| 4 | A4 New sessions from the server's defaults | New session, Run settings | B1, C1 | blocks |
| 5 | A3 Title badge for questions and long turns | Shell | C1 | blocks |
| 6 | A10 Apply verified work (accept → commit) | Workspace strip | B3, C2 | blocks (owner) |
| 7 | A9 Verify output | Workspace › Merge, drawer | B3, C2 | blocks (owner) |
| 8 | A8 Workspace › Merge: worktrees and queue | Workspace | B3, C2 | blocks (owner) |
| 9 | A6 Delete selected recorded sessions | Sessions | B4, C3 | blocks (owner) |
| 10 | A11 Schedule: edit, pause, resume | Schedule | B4, C3 | blocks (owner) |
| 11 | A7 Watch a session another process writes | Sessions, Runner, strip | B2+B4, C1+C3 | blocks |
| 12 | A22 Keep long runner commands running | Runner | B4, C3 | blocks |
| 13 | A12 Schedule `--model`, daemon `--every` | Schedule | B4, C3 | nice |
| 14 | A13 "Waiting for the model" | Footer, cockpit | B2, C1 | nice |
| 15 | A15 Provider error hints with a link | Radio rail | B1, C1 | nice |
| 16 | A17 Board: blocked, failed, attempts, alerts | Board, cockpit band | B2, C1 | nice |
| 17 | A14 Update notice chip | Shell strip | B1, C1 | nice |
| 18 | A16 Honest prices (saved at least, unpriced tokens) | HUD, Cache, Budget | B2, C1 | nice |
| 19 | A18 Mail counts and tokens | Mail | B2, C1 | nice |
| 20 | A30 CLI spec drift | Runner, palette | B4 | nice |
| 21 | A19 Config warnings | Settings › Config layers | B4, C3 | nice |
| 22 | A21 MCP: start again now, prompts | Settings › MCP servers | B4, C3 | nice |
| 23 | A20 Models: refresh, all, price filter | Settings › Models | B4, C3 | nice |
| 24 | A23 Prune reports kept branches | Sessions | B4, C3 | nice |
| 25 | A26 Cancel and correct (ctrl+c, compaction, did you mean) | Composer | B1, C1 | nice |
| 26 | A27 Text fidelity bundle | Radio rail, palette, Settings | B1+B2, C1+C3 | nice |
| 27 | A28 Cache view follows the newest agent | Cache, drawer | C1 | nice |
| 28 | A24 Resume with other settings | Resume dialog, New session | B1, C1 | nice |
| 29 | A25 `/recon` shows the session's map | Palette | B1, C1 | nice |
| 30 | A31 Trust: forget all | Settings › Trust | B4, C3 | nice |
| 31 | A29 Motion and bell defaults from the server | Settings › Appearance | B1, C3 | nice |

By view: Radio rail and inbox A1 A2 A15 A26 A27 · Shell A3 A14 · New session and Run settings A4 A24 · Sessions A5 A6 A7 A23 ·
Workspace A8 A9 A10 · Schedule A11 A12 · Runner A22 A30 · Footer and Cockpit A13 · Board A17 · Mail A18 · Cache A16 A28 · Settings
A19 A20 A21 A29 A31 · Palette A25.

### 4.2 REJECT-LIST

| TUI or CLI behaviour | Why it has no place in the browser |
|---|---|
| ctrl+l redraw; terminal scrollback and live region; 80-column fitting; terminal-width wrapping of CLI output | the page lays itself out (responsive, phone bar) |
| `--plain` line chat and its `allow? [y]es/[a]lways/[n]o` prompt | how a pipe is served, not a setting (CLI.md:1827) |
| NO_COLOR, ASCII glyphs by locale, TERM/COLORTERM detection | the browser renders colour and Unicode with the embedded fonts; forced-colors is CSS's job |
| an audible bell | browsers block sound without a gesture; A3's title badge is the analog |
| ctrl+d to quit, a queued "(quit when this is done)"; ctrl+c twice ends the process; `q` in watch | quitting is closing the tab (D-14 maps the double ctrl+c to Close) |
| watch keys `1`-`4`, Tab/shift+Tab for views, `j k H L 0 $ [ ] _ p h F1` aliases | digits answer questions; Tab is focus order; F1 is the browser's; the primary keys are covered |
| `/login` leaving the chat for the terminal; pasting keys in the page | D-01 (a): the browser never takes a key |
| the first-run provider and model picker | D-12: it runs on the server's terminal |
| a free directory path for a new session | refused by design (CONTRACT 6, `403 not_a_project`); `sleipnir web --project DIR` adds one |
| `inspect` serving its own dashboard (`--addr --token --open --interval`) | the web is that dashboard; `inspect --json` stays in the runner |
| `watch`/`replay` as terminal players, `chat`/`login` in the runner | D-11: the Cockpit, Replay view, New session and Providers are the equivalents |
| `sleipnir web` from the web | a nested server; the spec entry refuses it (A30) |
| `chat-record`, `term-svg` | hidden tools that record and render terminals for the docs |
| `sessions --dir`, `sessions -n` | the server serves its own state directory and lists every session |
| `init --user` without `--model` (interactive picker) | needs a terminal; `--model` in the runner works |
| `SLEIPNIR_DUMPABLE`, `SLEIPNIR_INSPECT_TOKEN`, proxies, `COLUMNS`/`LINES` | process and terminal plumbing |

---

## 5. Findings outside the web

- FEATURES.md section 12 recommends (a) for D-04 and D-06; the approved choices are (c) and (b). FEATURES.md, VOCAB.md 8.4 ("failed →
  todo with closure"), CONTRACT.md 6 (`swarm` restart `fresh: true (D-06)`) and the Run settings and `/swarm` confirm copy follow the
  approved choices.
- VOCAB.md 9 says the TUI's fourth answer is not offered; A2 reverses that.
- CONTRACT.md has no source for the `⟳` headless tab (TabSummary.Headless is never set by any route); A7 gives it one.
- docs/CLI.md:1806 says `/clear` is reserved and answers "unknown command", while `chatHelp` and `programCommand`
  (chat_tty.go:553) run it as `/new`.
- FEATURES.md 9.1.6 prints `models --tools --max-price 5` for a page whose price select is the input price; `--max-price` is the
  output price (models.go:166). A20.
