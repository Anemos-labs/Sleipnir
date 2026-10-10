# Sleipnir Web: shared brief for all five mocks

## What this is

Sleipnir is a Go coding-agent harness that today lives in a terminal: one manager agent plus up to eight workers
(the horse with eight legs), a layered prompt cache, a verifying merge queue, permissions, goals, checkpoints and a
recorded event log. The owner wants `sleipnir web`: it serves a browser UI on `http://127.0.0.1:6969` with **every
function the CLI has**. The terminal program stays; the web UI is a second front end over the same session.

We are making **five completely different mock interfaces**. The owner will pick one, and it will then be built for
real (Go `net/http` server, embedded static assets, SSE/WebSocket to a live session). So a mock is:

* a **living prototype**, not a screenshot: a scripted live simulation runs on load (text streams, timers tick, the
  cache clock counts down, agents change state, a question appears and can be answered);
* one **self-contained HTML file** (inline CSS + JS, inline SVG, no frameworks, no build step needed to open it);
* judged on **concept, craft and clarity**. It must look like a real product someone shipped, in a way that is
  unmistakably *this* paradigm and could not be mistaken for the other four.

The other four paradigms (do **not** drift into them, and do not borrow their signature ideas):

| # | Name | Paradigm |
|---|------|----------|
| 1 | Console  | terminal-true single scrollback column, keyboard first, monospace, "tmux with a mouse" |
| 2 | Cockpit  | swarm-first HUD / mission control; the team in motion is the screen; chat is a side rail |
| 3 | Saga     | an editorial *document* on cream paper; library of sagas, marginalia, plan as chapters |
| 4 | Longhouse| a team messenger (channels, DMs, threads); the agents are teammates in a room |
| 5 | Forge    | a review-first code workbench; files, diffs and checkpoints are the unit; the agent is a pane |

## Hard requirements (all mocks)

1. **One file**: `docs/design/web-mocks/NN-name.html` (NN = 01..05). Everything inline. The only allowed external
   resource is a Google Fonts `<link>` (it must degrade gracefully to a good system stack; check the page with the
   fallback too, because the real product will embed or use system fonts). No other network use, no CDN scripts.
   Vanilla JS only. Keep the file under ~350 KB.
2. **Alive on load.** A deterministic scripted timeline (see "Scenario", "Live script") drives the page. It loops or
   settles sensibly; the user can also interact at any moment (type, open the palette, answer the question).
3. **Interactions that must really work**: the command palette / slash menu listing every command in the CLI surface
   below, filterable, keyboard navigable (arrows, Enter, Esc) and *doing something visible* for at least the 8
   commands you choose to implement (`/model`, `/mode`, `/stats`, `/agents`, `/goal`, `/rewind`, `/diff`,
   `/sessions`, `/help`, `/cost`, `/compact`, `/permissions` are good candidates); the other commands open a tasteful
   stub that shows the real CLI help text for that command, never a blank panel. The approval question (3 choices,
   keys 1/2/3, Esc) works and the simulation reacts to the answer. A message typed in the composer appears and
   gets a scripted reply. Navigation between your screens works.
4. **At least five real screens/views**, genuinely designed (not placeholders): the main working screen plus four
   more drawn from: team/cockpit, cache & stats, sessions/history, models, permissions/trust/config, MCP/skills,
   goal/plan, checkpoints/diff, schedule, login/providers, doctor, rl lab. Which ones suit your paradigm is part of
   the design. Everything else in the CLI surface must still be *reachable* (palette entry or nav item) and open a
   designed stub.
5. **Responsive**: designed for 1440x900; must stay clean and usable down to 1024x700; at 390x844 (phone) it must
   not break (a deliberate simplified layout, not overflow). No horizontal page scroll at any width.
6. **Motion** respects `prefers-reduced-motion` (stand the animations still; the simulation still advances).
   **Accessible basics**: real buttons, labels, visible `:focus-visible` rings, body text contrast >= 4.5:1,
   state never conveyed by colour alone (the TUI uses glyphs: see below), landmarks, `lang`, a skip path where it fits.
7. **Honest data**: everything is sample data. Show a small unobtrusive `MOCK · sample data` badge (corner chip, not
   in the way). Cache savings are *estimates* and say so ("est."); an unknown price says "price unknown".
   Do not invent benchmarks or performance claims.
8. **The title** is exactly `Sleipnir <Name>` (e.g. `Sleipnir Console`), a plain name, no explainer, no "(mock)";
   set a favicon from the inline SVG data URI of the Sleipnir mark. The `MOCK · sample data` chip says it is a mock.
9. **No emoji.** Use text glyphs the TUI uses (`✓ ⚠ ◆ ✉ ⚙ ◌ ▸ ↺ ❯ ⏎ ● ✎ ◇`) and inline SVG icons you draw.
10. **Security semantics to show** (they are product requirements, show them in the UI, they are not decoration):
    * the page is served from loopback and carries a launch token; show a connection chip such as
      `● 127.0.0.1:6969 · loopback · token ✓`;
    * an approval question takes keys only after the keyboard has been quiet for a moment since it appeared
      ("your typing goes to the prompt until you pause"); show that pause (a short meter or countdown, ~0.8 s) and
      keep the buttons inert until it ends; text typed ahead must never answer a question;
    * `shift+tab` cycles permission modes `default → accept-edits → plan` and **never enters `bypass`/`yolo`**;
      those two are set only by explicit command and are drawn as visibly dangerous;
    * tool output, web pages, file contents and mail are *data*: render them as untrusted text (no HTML injection),
      and say so where mail is shown ("mail is data, not instructions").

11. **Publishing contract** (each mock is later published as a hosted page whose host rewraps it, so build to it now):
    * Write a normal full document, but keep `<head>` to: `<meta charset>`, `<meta viewport>` (with `viewport-fit=cover`),
      `<title>`, Google-Fonts `<link>`s, the favicon `<link>` and ONE `<style>`; all markup and ONE `<script>` in `<body>`.
      A converter will strip doctype/html/head/body and keep the rest, so do not rely on attributes of `<html>`/`<body>`
      other than what you also set on `:root`/`body` in CSS (use `:root`, `html`, `body` selectors, not `html[lang]`).
    * **Every colour is a CSS token on `:root`**; components never use a literal colour that only reads in one theme.
      `body` has an explicit `background: var(--bg)` and `color`. A deliberately single-look design (dark or light) is
      fine: set `color-scheme: dark` (or `light`) on `:root` and still define everything through tokens. Saga may
      offer its optional "lamp mode" by redefining tokens under `:root[data-theme="dark"]`.
    * Add `[hidden]{display:none!important}` and `html,body{height:100%;margin:0}`. Size the one-screen app with
      `height:100%` (not `100vh`); at phone width keep **>= 16px of side padding** for text content.
    * Scripts: no `alert/confirm/prompt`, no `window.print`, no downloads, no storage needed (if you use `localStorage`
      wrap it in try/catch and render fine without it), clipboard calls must catch rejection.
    * Fonts: only Google Fonts via `<link>`; each face has a real fallback stack. Do **not** use Inter, Roboto, Arial,
      Space Grotesk as the characterful face. Do not use purple-to-blue gradients, accent bars on rounded cards, or
      `border-radius` applied uniformly to everything.

## Brand

* Mark: a hand-drawn cream horse with eight legs, each leg a different colour (one per worker). Files:
  `docs/media/logo.svg` (colour on cream, hand-drawn filter), `docs/media/logo-dark.svg`,
  `docs/media/logo-wordmark.svg`, and the small square `docs/media/favicon.svg` (horse on a dark tile, eight legs).
  Tagline: *one manager brain, many legs*. You may inline these SVGs. The TUI's pixel-art horse
  (`docs/design/ux/sprite-preview.png`, generator `docs/design/ux/horse.py`) is the "cockpit horse".
* Palette = Tokyo Night. Role colours are fixed across the product (the eight legs); keep them recognisable even if
  you re-tone them for a light theme:

  | token | hex | used for |
  |---|---|---|
  | manager / purple | `#bb9af7` | manager, goal, shared prefix (G0) |
  | backend / blue   | `#7aa2f7` | backend workers, links |
  | frontend / sky   | `#7dcfff` | frontend worker, info |
  | scout / teal     | `#73daca` | scouts |
  | tester / green   | `#9ece6a` | tester, success, cache hit |
  | writer / amber   | `#e0af68` | warnings, "warm" cache clock, cost |
  | reviewer / orange| `#ff9e64` | attention |
  | error / pink-red | `#f7768e` | cache break, stuck, deny |
  | ink / bg-dark    | `#24232e` / `#16161e` / `#1a1b26` | the logo ink, deep backgrounds |
  | fg / comment     | `#c0caf5` / `#565f89` | text, dim text on dark |
  | cream / mane     | `#fbf8f1` / `#7048e8` | the logo paper, the logo's violet mane |

  Dark-theme backgrounds in the TUI captures are about `#0b0e17`. Departing from the dark palette is welcome
  where the paradigm demands it (Saga is light, Longhouse is split-tone, Forge is charcoal+ember), but the eight role
  colours must still read as the same family.
* Glyph vocabulary (from the TUI; reuse it so the web UI feels like the same product):
  `●` agent/turn marker, `✓` ok/done, `⚠` anomaly/stuck, `◆` compaction, `✉` mail, `⚙` tool running, `✎` editing,
  `◌` idle, `↺` resumable session, `▸` queue head, `❯` prompt, `⏎ queued:` typed-ahead line, `◇` thinking.
* Tone of copy: plain, exact, a little dry. The product never says "AI-powered" or "supercharge". Documentation in
  this repo is timeless and impersonal: no jokes in system text. Microcopy may be warm but not cute.

## The CLI surface the web UI must cover (reachable from your UI)

Everything below exists in the CLI today; the real strings are given so the stubs are truthful.

```text
conversation
/goal TEXT         work until it is met, judged on evidence (/goal: status)
/new               start again, empty (same model and mode)
/clear             the same as /new
/resume [id]       pick an earlier session from a menu, and continue it
/sessions          the newest sessions
/compact [focus]   fold the older thread now; focus says what to keep in view
/rewind [id]       list checkpoints, or restore files to before a turn
/diff [id]         what changed in the newest checkpoint, or in <id>
/exit              quit (Ctrl-D, or Ctrl-C twice at the prompt)

model and cost
/model [ref]       pick a model from a menu; a team starts again on it
/effort [level]    show or change reasoning effort (closest supported level)   levels: default none minimal low medium high xhigh max
/fav [ref]         star a model, or unstar it; starred ones lead in /model
/login [provider]  add a key, or sign in with ChatGPT (the chat comes back)
/budget [usd|off]  the dollar budget for the turns from now on
/cost              tokens, cost and cache hit ratio so far
/stats             the stats page (ctrl+t): cost, cache, savings, layers
/context           what each layer of the prompt weighs
/status            model, mode, session, budget and cost at a glance

permissions
/mode <m>          default | accept-edits | plan | bypass | yolo
/plan [prompt]     switch to read-only mode; with a prompt, start planning it
/allow <rule>      allow, this session, what would ask: tests, Bash(go test:*)
/permissions       the mode and the rules in force
/trust             this project's own instructions and settings, and your yes

a team, and the program
/roles [role=m]    which model each role runs on; change one (restarts)
/swarm <n> [flags] start again as a manager and n workers
/restart [flags]   start again with other flags: --no-mcp, --cwd DIR, ...
/agents            the team's agents and tasks (ctrl+g: cockpit)
/steer TEXT        tell the running turn something, without stopping it
/verbose [on|off]  notices and tool errors
/anim [on|off]     motion
/cwd               the directory this session works in

what the model knows
/recon             the project map in the shared layer
/skills            the skills the model can load
/mcp               tool servers: state and tools (/mcp reconnect NAME)
/help              this text, and your custom commands and skills
```

Beyond the chat, the CLI also has these commands that the web UI should reach (palette/nav entries, stubs ok):
`sessions` (+ `sessions prune --older-than 30d --keep 20 --yes`), `inspect` (cache dashboard over a recorded
session), `watch` (live cockpit of a running session), `replay` (play a recorded session: space pause, arrows seek,
+/- speed), `config` (effective configuration and where each value came from), `init`, `trust` (add/forget/list),
`mcp` (list/approve/revoke/test), `login`/`logout` (Heimdall, OpenRouter, OpenAI, Anthropic, ChatGPT plan, a `local`
server), `models` (catalogue with context and $/M prices, filters: tools, reasoning, max price, min context,
favourites), `doctor` (probe an endpoint: streaming, tools, prefix-cache behaviour, warm-up needs), `recon`,
`schedule` + `daemon` (cron goals), `sim` (cache-policy simulation: a model, not a benchmark), `friction`
(what slowed sessions down), `update`, `demo`, `mock`, and the `rl` lab (taskgen, rollout, eval, reward, report,
compare, export). Flags of `chat`/`run` the web UI should expose as settings: `--swarm N`, `--model`, `--mode`,
`--budget-usd`, `--isolation none|worktree`, `--verify "go test {dirs}"`, `--commit`, `--mailman`, `--role-model`,
`--allow`, `--trust-project`, `--no-mcp`, `--resume`, `--continue`.

Key bindings of the terminal chat (keep them working in the browser where they make sense):
Enter send · `\` at line end / alt+enter / ctrl+j newline · Up/Down/ctrl+r history · `/` command palette · `@` path
completion · shift+tab permission mode · ctrl+t stats page · ctrl+g team cockpit · ctrl+o expand collapsed tool
output · Esc interrupt the running turn (and pauses a goal) · Ctrl-C at the prompt discards the line, twice quits ·
a paste of many lines becomes a chip `[pasted text #1 +50 lines]` · a line typed while a turn runs is queued as
`⏎ queued: ...` · slash commands that only look (`/cost /context /agents /help /skills /status /permissions /trust`)
answer immediately beside the running turn.

Approval question (real strings): the box names the agent, the operation and its scope, shows the command or the
change as a diff, and why it asks. Choices:
`1  Yes`
`2  Yes, and don't ask again for <what> this session`  (<what> is e.g. `go test` commands, `edits inside api/`, or the exact request)
`3  No, and tell Sleipnir what to do instead   (esc)`
Several questions that arrive together are asked one at a time, and the one in front says how many wait.

Footer of the chat (real): `default · ctrl+t stats · / commands` on the left, `model · session-id` on the right.
Status line (real): what the agent is doing, for how long, tokens, cost, `esc to interrupt`.
Input placeholder (real): `Message Sleipnir, / for commands, @ for files`.

## Scenario: one snapshot, shared by all mocks

Everyone renders the **same** team run so the owner compares paradigms, not sample data.

**Project** `~/projects/shop` (Go + a static web page), module `example.com/shop`, branch `main`, clean tree.
Layout: `api/{server.go, catalog/items.go, cart/cart.go}`, `web/{index.html, shop.js, shop.css}`,
`seed/items.json` (48 items), `AGENTS.md`, `Makefile`, `.sleipnir/config.json`.
**Session** `20260102-030405-5eed01`, started 03:04:05, now 03:04:43 (elapsed `00:38`), mode `default`,
single provider `heimdall`, manager model `anthropic/claude-sonnet-5-5`, workers `heimdall/demo-model`,
`--swarm 8` (capacity 8 workers; 7 started; 4 active), `--isolation worktree`, `--verify "go test {dirs}"`, mailman off,
budget `$5.00`, spent `$0.0812` (header `$0.08`), overall hit ratio `84%`, shared prefix **warm `0:25`** (the
provider cache lifetime counting down, refreshed whenever a request lands), governor `rpm 44 · 429s 0 · retries 0`.
Trust: this project's files are trusted (you said yes on Jan 1; hash unchanged). MCP: 2 servers running.

**The goal** (typed by the user at 03:04:05):
`/goal Build the shop: a catalogue with pagination, a cart with a running total, and the shop page that shows them. go test ./... must pass.`

Goal state `active`. Plan (from the planner), with the judge's current verdict *"not yet: T4-T7 unfinished, and no
passing `go test ./...` in the evidence"*:
1. Survey the API, the seed data and the paging conventions · done (T1-T3)
2. Catalogue endpoint with pagination · in verification (T4)
3. Cart with a running total, money in cents · editing (T5)
4. Shop page: item grid and pager · waiting for an answer (T6)
5. Tests for the catalogue and the cart · queued behind T4/T5 (T7)
6. `go test ./...` passes on the merged result · pending

**The team** (id, role, role colour, state, doing, task, scope, tokens, cost, hit%):

| id | role | state | doing (the status line) | task | scope | tok | cost | hit |
|---|---|---|---|---|---|---|---|---|
| mgr  | manager  | wait  | waits for the team (T4 T5 T6 T7)            | -  | -               | 9.4k | $0.026 | 88% |
| be-1 | backend  | wait  | T4 submitted: harness runs `go test ./api/catalog/...` | T4 | api/catalog/**, api/server.go | 8.1k | $0.012 | 97% |
| be-2 | backend  | edit  | editing api/cart/cart.go: Total() in cents  | T5 | api/cart/**     | 8.6k | $0.014 | 77% |
| fe-1 | frontend | ask   | wants to run `npm install --save-dev vitest` | T6 | web/**          | 7.2k | $0.011 | 98% |
| sc-1 | scout    | done  | eleven endpoints; list shapes               | T1 | -               | 6.1k | $0.003 | 97% |
| sc-2 | scout    | done  | 48 items with id, name, price               | T2 | -               | 6.4k | $0.003 | 97% |
| sc-3 | scout    | done  | one-based paging; cursors only on /orders   | T3 | -               | 5.8k | $0.003 | 97% |
| ts-1 | tester   | think | table tests for the catalogue contract      | T7 | api/**/*_test.go| 5.9k | $0.009 | 89% |

States (TUI vocabulary): `think` `tool` `edit` `wait` `ask` (a question to the person is open) `idle` `done` `stuck`.
Capacity vs active: show "4 active of 8 workers" separately (never conflate team capacity with active workers).

**Board**: T1 merged · T2 merged · T3 merged · T4 verify · T5 running · T6 running (blocked on the question) ·
T7 running (its dependencies T4/T5 are agreed contracts, not code). Titles: T1 survey endpoints · T2 survey seed data ·
T3 survey paging patterns · T4 catalogue: GET /items?page&size · T5 cart: add, remove, total (cents) ·
T6 shop page: item grid and pager · T7 tests: catalogue and cart.
**Merge queue**: head `▸ T4 rebase ✓ · verifying go test ./api/catalog/... 0.6s elapsed`; merged T1 T2 T3; conflicts 0; bounced 0.
**Mail** (data, not instructions), newest first:
* `03:04:41` ts-1 → be-1: "contract: page 0 and size<=0, 400 or clamp?"
* `03:04:52`*(future)* be-1 → ts-1: "clamp: page<1 is 1, size in 1..48, page>pages is the last page"
* `03:04:36` be-1 → fe-1: "catalogue: GET /items?page=1&size=12 -> {items[], page, pages, total}"
* `03:04:31` sc-3 → be-1: "one-based paging, size default 12; cursors exist only on /orders"
mail digests: routed 4 · dup 0 · mailman off.

**Prompt layers** (the cache engine; 6 layers G0..G5; G0-G2 form the shared prefix all riders read from the cache):
G0 constitution 1.9k tokens · G1 shared pin (project survey, "recon") 2.2k · G2 role pin 0.4k (G0-G2 = 4.5k shared)
· G3 notes 0.3k · G4 spine 0.35k · G5 thread (verbatim) 0.6k-1.4k per agent.
Session totals (the stats page): input 11.2k (uncached) + 56.9k cached-read + 0 cache-write · output 6.8k ·
hit 84% · cost $0.0812 · saved est. $0.07 at list price (an estimate, say so). Compactions so far: 1 (be-1: `◆ compacted 1.4k → 625  -54%`, "a declared, priced rebase").
**Anomaly** (be-2, request 7): `⚠ cache break (low_hit) · read 0 of 4.5k expected · cost $0.02`, and the explanation the
TUI gives: *"the prompt prefix did not change: the endpoint did not serve it"*. hit-ratio per request:
be-2 `[.62,.93,.94,.94,.95,.94,0]` (mean 77%, lowest 0% at request 7), be-1 `[.71,.96,.97,.98,.97,.98]`,
fe-1 `[.64,.97,.98,.98,.99]`, ts-1 `[.70,.91,.92]`, mgr `[.55,.90,.94,.94,.93]`, scouts ~`[.7,.97,.97]`.

**Checkpoints** (`/rewind`): `c07 03:04:38 3 files  T4 catalogue handler` · `c06 03:04:29 1 file  T5 cart total in cents` ·
`c05 03:04:21 2 files  seed items + loader` · `c04 03:04:12 0 files (skipped: nothing to put back)`. `/rewind ID` puts files
back; `/diff ID` shows what changed.

**Changed files** (real code for diff views; colour the line by the agent who wrote it):

`api/catalog/items.go` (A, +31 −0, be-1, T4):
```go
package catalog

import "sort"

// MaxSize is the largest page a client may ask for.
const MaxSize = 48

// Item is one thing the shop sells. Prices are in cents.
type Item struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
}

// Page is one page of the catalogue, counted from 1.
type Page struct {
	Items []Item `json:"items"`
	Page  int    `json:"page"`
	Pages int    `json:"pages"`
	Total int    `json:"total"`
}

// Store holds the catalogue in memory, sorted by ID.
type Store struct{ rows []Item }

// New returns a Store over rows, sorted by ID.
func New(rows []Item) *Store {
	s := &Store{rows: append([]Item(nil), rows...)}
	sort.Slice(s.rows, func(i, j int) bool { return s.rows[i].ID < s.rows[j].ID })
	return s
}

// List returns page number page (from 1) of at most size items. A page below 1 is page 1,
// a page past the end is the last page, and a size outside 1..MaxSize is clamped.
func (s *Store) List(page, size int) Page {
	size = min(max(size, 1), MaxSize)
	pages := (len(s.rows) + size - 1) / size
	page = min(max(page, 1), max(pages, 1))
	lo := (page - 1) * size
	hi := min(lo+size, len(s.rows))
	return Page{Items: s.rows[lo:hi], Page: page, Pages: pages, Total: len(s.rows)}
}
```

`api/server.go` (M, +7 −0, be-1, T4):
```diff
@@ -18,6 +18,7 @@ func (s *Server) routes() http.Handler {
 	mux := http.NewServeMux()
 	mux.HandleFunc("GET /healthz", s.health)
+	mux.HandleFunc("GET /items", s.items)
 	mux.HandleFunc("GET /orders", s.orders)
 	return mux
 }
@@ -44,3 +45,10 @@ func (s *Server) health(w http.ResponseWriter, r *http.Request) {
 	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
 }
+
+// items serves one page of the catalogue: GET /items?page=1&size=12.
+func (s *Server) items(w http.ResponseWriter, r *http.Request) {
+	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
+	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
+	writeJSON(w, http.StatusOK, s.catalog.List(page, size))
+}
```

`api/cart/cart.go` (M, +4 −4 so far, be-2, T5, being edited right now; a caret is on the `+` line):
```diff
@@ -23,10 +23,10 @@ func (c *Cart) Add(id string, price int64, qty int) {
 // Total is what the cart costs, in cents.
-func (c *Cart) Total() float64 {
-	var total float64
+func (c *Cart) Total() int64 {
+	var total int64
 	for _, l := range c.lines {
-		total += l.Price * float64(l.Qty)
+		total += l.PriceCents * int64(l.Qty)
 	}
 	return total
 }
```

`web/shop.js` (A, +14 so far, fe-1, T6, uncommitted until the question is answered):
```js
const grid = document.querySelector('#items');
const pager = document.querySelector('#pager');
let page = 1;

async function load(n) {
  const res = await fetch(`/items?page=${n}&size=12`);
  const data = await res.json();
  page = data.page;
  grid.replaceChildren(...data.items.map(card));
  pager.replaceChildren(...pagerButtons(data.page, data.pages));
}
```
`api/catalog/items_test.go` (not written yet, ts-1 is planning it).

**The question** (opens at 03:04:42, fe-1, 1 waiting):
> fe-1 · frontend · T6 wants to run a command
> `npm install --save-dev vitest`   (cwd web/)
> why it asks: installs a package from the network and edits web/package.json; it is not a build or test command
> `1` Yes · `2` Yes, and don't ask again for `npm install` this session · `3` No, and tell Sleipnir what to do instead (esc)
> your typing goes to the prompt until you pause

**Conversation so far** (what the person sees in the chat of the manager):

1. `03:04:05` You: the `/goal ...` line above.
2. `03:04:06` ◇ goal set; plan: the six steps above.
3. `03:04:09` manager: "I'll have three scouts map the API, the seed data and the paging conventions, then split the build by directory so no two workers touch the same file."
   tool `TaskBoard create T1 T2 T3` ✓ · `Spawn sc-1 sc-2 sc-3` ✓
4. `03:04:21` scouts report (3 lines, quoted from their `doing` strings). manager: "Eleven endpoints, 48 seed items, paging is one-based with cursors only on /orders. Splitting the build now: be-1 takes the catalogue, be-2 the cart, fe-1 the page, ts-1 the tests."
   tool `TaskBoard create T4 T5 T6 T7` ✓ · `Spawn be-1 be-2 fe-1 ts-1` ✓ · lease api/catalog/** → be-1, api/cart/** → be-2, web/** → fe-1
5. `03:04:31`-`03:04:41` the live feed of the workers (compact one-liners): `be-1 Read api/server.go` · `be-1 Write api/catalog/items.go +31` ·
   `be-1 Edit api/server.go +7` · `be-1 submitted T4` · `be-2 Read api/cart/cart.go` · `be-2 Edit api/cart/cart.go` ·
   `fe-1 Write web/shop.js +14` · `fe-1 asks: Bash npm install --save-dev vitest` · `ts-1 mail → be-1`.
6. `03:04:43` status line (live): `● manager waiting for the team · 00:38 · ↑9.4k ↓1.2k · $0.08 · esc to interrupt`.

## Live script (the simulation after load; times are seconds after load, compress freely, loop at the end)

* t+0.0 snapshot above, spinner running, warm clock `0:25` counting down, tokens/cost ticking.
* t+2: be-2 text streams: "Total() keeps cents as int64; the old float64 would drift on 0.1 + 0.2." The `+` line in cart.go finishes.
* t+3: **a cache break is detected on be-2** (the anomaly above) with the TUI's explanation; the shared-prefix bar flashes pink-red for ~2 s; be-2's hit% falls.
* t+4: be-1 → ts-1 mail arrives (the clamp answer). ts-1 `think` → `edit`: creates `api/catalog/items_test.go` (table test, show a few lines).
* t+6: the merge queue verifies T4: `go test ./api/catalog/... ✓ 0.9s` → T4 **merged**, merged count 4. be-1 goes `idle`. A compaction `◆ compacted 1.4k → 625  -54%` happens on be-1 at a warm moment.
* t+8: the question is still waiting (nothing moves for fe-1 until answered; never auto-answer it). Show "waiting for you: 18s".
* when the user answers **1 or 2**: fe-1 runs `npm install --save-dev vitest` `✓ 3.1s`, then `Edit web/shop.js`, `T6 submitted`; **3**: a composer opens "tell Sleipnir what to do instead", send → fe-1 `think`.
* t+14 (or 6 s after the answer): T5 submitted; verify `go test ./api/cart/... ✓ 0.7s`, T5 merged; ts-1 submits T7; `go test ./...` ✓ 2.1s on the merged result; goal judge: "met" → `◇ goal met` and the plan ticks all six; final line `-- 51s · 23 steps · $0.11`.
* The warm clock must visibly refill when a request lands and go amber `0:25` → red under 0:08.

## Process every builder follows

1. Read this file, your concept file, and look at the TUI captures so the web UI is recognisably the same product:
   `docs/media/swarm.png`, `docs/media/chat.png`, `docs/media/cache.png`, `docs/media/logo-wordmark.png`,
   `docs/design/ux/sprite-preview.png`, and skim `docs/UX.md` and `docs/CLI.md`.
2. Work in your own scratch dir `<scratchpad>/work/NN/`, in parts if you like (css/js/html pieces and a tiny concat
   script); write the deliverable to `docs/design/web-mocks/NN-name.html`. Touch no other file in the repo. Run no
   `git` command. Never use the Browser pane tools (other builders use it concurrently).
3. **Look at what you make.** Render with the screenshot driver and *read the PNGs* (the Read tool shows images):
   `node <scratchpad>/shot.mjs docs/design/web-mocks/NN-name.html --w 1440 --h 900 --step wait:1500 --step shot:<scratchpad>/shots/NN/main.png`
   (steps: `shot: full: wait: click:CSS clickxy:X,Y hover:X,Y key:ctrl+k type:TEXT eval:JS size:W,H`; source the
   toolchain first: `. ~/.local/sleipnir-toolchains.sh`). Take shots of every screen, the palette open, the question
   answered, hover/focus states, 1280x720, 1024x700 and 390x844. Fix what is wrong, then look again. At least four
   full review rounds. The driver prints console errors: the final file has none.
4. Critique like an art director: alignment on a grid, one type scale, spacing rhythm, hierarchy (what is the one
   thing the eye lands on?), density appropriate to the paradigm, empty states, truncation (no text clipped
   unintentionally, no overlaps), contrast, consistency of icon weight, motion that means something. If a part
   looks like a generic Bootstrap/Tailwind dashboard, redesign it with the paradigm's own language.
5. Your final message is a *short* report: the concept in two sentences, how to try it (keys/clicks worth trying),
   what is real vs stubbed, any web-only affordance you added beyond the CLI (so the owner can weigh it), known flaws,
   and the list of screenshot paths you reviewed.
