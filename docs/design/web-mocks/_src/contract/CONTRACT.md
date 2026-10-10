# CONTRACT: the HTTP and SSE API of `sleipnir web`

Binding for A2 (the envelope, committed in 08c4482, plus the deltas of section 2.9), B1 (host, approvals, the stream route), B3
(workspace routes), B4 (settings, tools, runner routes), C1..C3 (callers). Request and response bodies are the Go types of package
`internal/web/wire` (OWNERSHIP.md section 5); the JSON examples here are normative for spelling. UI event shapes are in VOCAB.md.

**What is already built.** `internal/web` (A2) is the hardened server: loopback listener, Host and Origin/fetch-metadata checks, the
run token and the session cookie, the `X-Sleipnir-Web` header rule, JSON-only bodies with per-route caps, single-use confirmation ids,
throttling, the concurrency cap, the panic recoverer, the CSP and security headers, the SSE hub with replay and loss policy, and the
embedded UI. Its routes are added by the other builders with `Server.Handle(pattern, handler, web.RouteOpts{...})` inside the
`webHost` seam of `cmd/sleipnir/web.go`. `internal/web/doc.go` and `docs/SECURITY.md` section 5 describe it; this contract does not
restate what they say, it binds the routes built on top and the few changes listed as deltas.

Contents: 0 Conventions · 1 The command · 2 SECURITY (binding) · 3 Built-in and static routes · 4 The stream · 5 Boot · 6 Tabs ·
7 Recorded sessions · 8 Chat · 9 Approvals · 10 Goal · 11 Session settings · 12 Workspace · 13 Settings pages · 14 Providers and
sign-in · 15 MCP · 16 Trust · 17 Schedule and daemon · 18 Runner and the CLI spec · 19 Doctor, update and other tools ·
20 Confirmation scopes · 21 Error codes · 22 Limits

---

## 0. Conventions

* Base path `/api`. JSON in and out; responses written with `web.WriteJSON` (HTML-safe escaping, `no-store`), bodies read with
  `web.DecodeJSON` (one value, no unknown fields, within the route's `MaxBody`).
* Every request other than GET/HEAD carries `X-Sleipnir-Web: 1` (`web.RequestHeader`) and, with a body, `Content-Type:
  application/json`; the envelope refuses it otherwise (`403 csrf`, `415 unsupported_media_type`). A route without a body declares
  `RouteOpts{NoBody: true}`. GET routes never change state.
* Errors: status + `{"error": "<one sentence for a person>", "code": "<identifier>"}` (`web.Error`), plus `"detail": {...}` where a
  row says so (written with the `web.ErrorDetail` helper of delta Δ2). The sentence is safe to show in a toast: no stack, no path
  outside the project, no secret, at most 512 bytes. Codes in section 21.
* Paths name resources by id; ids are looked up in registries (maps), never joined into filesystem paths. Path parameters: `{id}`
  tab id (`[a-z0-9-]{1,40}`), `{sid}` recorded session id (`\d{8}-\d{6}-[0-9a-f]{6}`), `{qid}` question id (`q_[a-z2-7]{26}`),
  `{run}` run id (`r_[a-z2-7]{16}`), `{rid}` revert id (`v_[a-z2-7]{16}`), `{job}` schedule job id (`j\d{1,6}`), `{name}` MCP server
  or provider name (`[A-Za-z0-9_.-]{1,64}`). A parameter that does not match: `400 bad_request` before any lookup.
* Idempotency per row: `idem` (repeating it has the same effect), `once` (single use; a repeat is `409`), `new` (each call creates
  something; the body's optional `clientId` (≤ 64 chars) makes a repeat within 60 s return the first result).
* Auth class per row: `public` (the envelope's own public routes only), `auth` (cookie or bearer; every route below is at least this),
  `confirm` (also an `X-Confirm` id for the scope of section 20; the route declares `RouteOpts{NeedsConfirm: true, ConfirmScope}` or
  calls `Server.RequireConfirm` when only some inputs need it).
* Phase: the builder that registers the route and implements the behaviour. UI caller: the mock function that triggers the call.

## 1. The command (committed by A2; owned by B1 from now on, OWNERSHIP.md 3)

`sleipnir web [flags]` as committed (`cmd/sleipnir/web.go`, documented in `docs/CLI.md`): `--addr` (default `127.0.0.1:6969`,
loopback only, `:0` picks a port), `--open`, `--cwd`, and the chat flags as **defaults of the sessions started in the page**
(`--model --mode --swarm --budget-usd --isolation --verify --commit --mailman --role-model --allow --trust-project --no-mcp --resume
--continue`); no positional arguments; the first stdout line is `http://127.0.0.1:PORT/?token=<run token>`; everything else on
stderr; Ctrl-C ends with status 0. B1 adds, through `webHost` and the flag set (docs/CLI.md's flags block regenerated with
`scripts/gen-cli-docs.sh`):

| Flag | Default | Meaning |
|---|---|---|
| `--ask-timeout` | `0` (wait) | refuse a question nobody answered in this long (`perm.Config.AskTimeout` of every tab) |
| `--ask-grace` | `10m` | refuse open questions when no page has had a stream connected for this long (VOCAB.md 9) |
| `--project DIR` | none, repeatable | add a directory to the projects a new session may start in (section 6, "Projects") |
| `--fixture NAME` | none, hidden (not in `-h`) | deterministic tabs on `internal/provider/mock` (TEST-PLAN.md 3); requires a temp state directory |

The first tab: the host starts one session at launch in `--cwd` with the defaults (as `sleipnir chat` would, `--resume`/`--continue`
included). A missing model or key runs the chat's first-run setup on the terminal first (`ensureModel`,
`cmd/sleipnir/pick.go:101`) when stdin is a terminal; when the first tab cannot start, the server still serves and the page opens with
no tab (FEATURES.md D-12).

## 2. SECURITY (binding for every implementer)

### 2.1 What is defended, against whom

As `docs/SECURITY.md` section 5 (A2): other local users, other websites (CSRF, DNS rebinding, another localhost port), a
prompt-injected agent with the user's uid, the network. Not defended: a same-uid process that reads the terminal, a redirected stdout
file, the browser's cookie store or memory; root. Everything below is binding on the code that registers routes, in addition to the
envelope.

### 2.2 The envelope (committed; every route inherits it)

Order (`internal/web/doc.go` "The envelope"): security headers (CSP `default-src 'none'; script-src 'self'; style-src 'self';
style-src-attr 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'none';
base-uri 'none'`, nosniff, `X-Frame-Options: DENY`, no referrer, COOP/CORP same-origin, Permissions-Policy, `no-store`) → panic
recoverer → in-flight cap (128, `503 busy`) → Host allowlist with the bound port (`403 bad_host`) → fetch metadata and exact
`Origin` (scheme, host, port) on every non-GET (`403 forbidden_site`, `403 forbidden_origin`; a request with neither `Origin` nor
`Sec-Fetch-Site` must use the bearer token) → authentication (run token as bearer on any route, as `?token=` on `/` only; or the
`sleipnir_web` cookie: HttpOnly, SameSite=Strict, a random session id unrelated to the token, 24 h, server-side sessions; failed
token attempts throttled server-wide after 10 a minute) → method and pattern → for non-GET: `X-Sleipnir-Web: 1`, JSON content type,
body cap, `X-Confirm` where declared → handler.

### 2.3 Secrets

* The run token (256 bits, `crypto/rand`) is printed only on the first stdout line; never accepted from argv, env or a file; never
  logged (A2 masks it). The cookie holds an unrelated id. Confirmation ids are single use, one minute, bound to the credential and
  the scope.
* Route code never returns, frames or logs: provider keys, stored or held credentials (`harden.Secret`; `harden.Held` lists only
  their names), ChatGPT tokens, the run token, cookie values, confirmation ids (except in the response that issues one), request
  bodies. Logging goes through `web.Logf`/`Server.Logf` (masked).
* Configuration DTOs are allow-lists (B4): provider `headers` values, hook `headers`, MCP `env` values, URL userinfo and query values
  become `(set, not shown)`; MCP `args` as `mcp.ServerConfig.Redacted()` plus a `harden.LooksSecret` check per argument.
* UI events carry untrusted text sanitized (`tools.SanitizeForTerminal`), capped and masked (`internal/rl/redact`) (VOCAB.md
  principle 4). Workspace file content is served raw (it is the person's own project) except denied paths (never served) and known
  secret carriers (`.env*`, `*.pem`, `id_*` keys) whose secret-shaped values are masked.
* Agent tools never see the token, a cookie or a confirmation id: none is in the environment children inherit, in argv, or in any file
  under the project or the session directory (TEST-PLAN.md S-12).

### 2.4 The prompt-injected agent

It runs as the user and can `curl 127.0.0.1:6969`. Without the token or a cookie every route answers 401. It cannot obtain them from
its environment, its argv or the session's files. Its `web_fetch` refuses loopback addresses (`internal/tools/web/guard.go`). What
remains is same-uid reach (terminal, browser profile, memory), stated in docs/SECURITY.md, and the `--open` window of Δ1.

### 2.5 Approvals from a browser

A question is answered only by `POST /api/questions/{qid}/answer` with an unguessable single-use id; the server enforces a 350 ms
floor after the question appeared and after the tab's previous answer (`409 too_soon`); a question is refused when its context ends,
on `--ask-timeout`, or when no page has been connected for `--ask-grace` (VOCAB.md section 9). The page cannot choose the remember
scope (it sends `choice` 1, 2 or 3; the server maps it) and cannot choose a working directory outside the projects list.

### 2.6 Privileged actions

Routes in section 20 need an `X-Confirm` id. The page obtains it with `POST /api/confirm {"scope": "..."}` at the moment the person
confirms (the mock's confirm dialog OK, the typed mode name, "Trust these files"), then sends the action with the header. A
confirmation stops forged, replayed and mistaken requests and makes an escalation an explicit act; it is not a second credential
(docs/SECURITY.md section 5).

### 2.7 Files

All file access is the Workspace's (B3): project-relative paths with `/`; resolved under the tab's root with `os.Root` so that `..`,
absolute paths and symlinks out of the root are refused (`400 bad_path`); `.git/` and the state directory never served; paths the
permission engine denies to agents for reading (`Engine.Classify`) never served (`403 denied`); opened `O_NOFOLLOW|O_NONBLOCK`
(FIFOs and devices refused, as `internal/inspect/open_unix.go`); text over 2 MiB truncated; NUL in the first 8,000 bytes = binary.

### 2.8 Limits and deadlines

Per route `MaxBody` (section 22), the envelope's in-flight cap and stream cap, `WriteTimeout` per route (30 s default; longer only
for snapshot and diff routes, 60 s), hub write deadlines and heartbeats (committed). Route-level caps: 16 tabs (`409 limit`), 4 runner
runs at once (`409 busy`), 64 open questions per tab (more wait in the engine).

### 2.9 Deltas to the committed foundation (A2 implements; small, additive)

| # | Change | Why |
|---|---|---|
| Δ1 | `--open` hands the opener a URL with a **single-use launch code** valid 30 s (accepted by `?token=` on `/` like the token, spent on first use), not the run token | the opener's argv is readable by every same-uid process (`/proc/<pid>/cmdline`); the run token is reusable and also a bearer credential |
| Δ2 | `func ErrorDetail(w http.ResponseWriter, status int, code, msg string, detail any)`: the error body plus `"detail"` | `409 trust_required` carries the trust challenge; `409 too_soon` carries `retryAfterMs` |
| Δ3 | `web.Config.Hub` for `sleipnir web`: `ReplayEvents 20000`, `ReplayBytes 32 MiB`, `Buffer 2048`, `BufferBytes 8 MiB`, `MaxEventBytes 1 MiB` (set in `web.go`, B1 after the handover) | one page topic carries every tab (section 4) |
| Δ4 | Recommended: the session cookie without `Max-Age`/`Expires` (a browser-session cookie, still ending with the run) | fewer cookies persisted in the profile on disk; optional, the owner decides with A2 |
| Δ5 | `GET /api/hello` is registered by B1, not A2 (listed here so that nobody builds it twice) | |

## 3. Built-in and static routes (committed, A2)

| Method | Path | Auth | Response |
|---|---|---|---|
| GET | `/` and assets | auth for the page (an unauthenticated navigation gets the sign-in page, `auth.go` `signInPage`); `?token=` exchanged for the cookie and a redirect to `/` keeping the other query parameters | the embedded UI |
| GET | `/healthz` | public (Host-checked) | `200 {"ok":true}` |
| GET | `/api/ping` | auth | `200 {"ok":true}` |
| POST | `/api/confirm` | auth | body `{"scope": "<1..120 printable ASCII>"}` → `{"id", "scope", "expires_in": 60}`; `429 rate_limited` when too many are outstanding |
| POST | `/api/auth/logout` | auth, no body | ends this session, clears the cookie |
| POST | `/api/auth/rotate` | auth, no body | ends every session, replaces the token (not printed); the caller keeps a new session |
| any | `/api/...` unknown | auth | `404 not_found` JSON |

## 4. The stream: `GET /api/stream` (B1 registers it on the committed hub)

* One topic for the page: `seam.Topic` = `"ui"`. B1 registers `srv.Handle("GET /api/stream", srv.Hub().ServeSSE(func(*http.Request)
  (string, bool) { return seam.Topic, true }), web.RouteOpts{})`. Every tab's frames and the global ones are published to it (a page
  needs one connection whatever the number of tabs; browsers keep six per origin).
* Framing (committed): `id: <n>` (the hub's per-topic id, from 1 per server run), `event: <frame type>`, `data: <JSON on one line>`;
  heartbeats are SSE comment lines every 15 s; the hub sends `retry: 3000`.
* Resume (committed): `Last-Event-ID` (EventSource sends it on reconnect) or `?after=<id>` on the first connection (the page uses
  `hello.streamAfter`). If the id is still in the topic's ring the hub replays from there; otherwise it sends `gap`.
* Hub frames (committed): `gap {"reason", "dropped", "last"}` (the page may have missed frames: it refetches the snapshots of every
  tab); `lagged` (the page was cut off for not keeping up with critical frames: EventSource reconnects with Last-Event-ID); `closed`
  (the topic closed or the server shuts down: the page shows the disconnected state, D-13).
* Host frames (VOCAB.md section 10): `ev`, `meta`, `roster`, `tab`, `reset`, `recorded`, `run`, `toast`, `ping`, `bye`. Each is a
  `wire.Frame{Type, Tab, Data, Critical, Coalescable, Key}` that B1's adapter publishes as `web.Event{Type, Data, Critical,
  Coalescable, Key}` on `"ui"`. Classes (the hub's loss policy, committed):

  | Frame / UI event kind | Hub class | Key |
  |---|---|---|
  | `tab`, `reset`, `roster`, `meta`, `bye`; `ev` of kinds `ask answer state task goal final turn merge mail tool say stream sys note refuse interrupt steer break compact req stall handover`, and the first `ckpt` of an id | `Critical` | |
  | `ev` of kinds `use` | `Coalescable` | `use/<tab>/<agent>` |
  | `ev` of kinds `layers` | `Coalescable` | `layers/<tab>/<agent>` |
  | `ev` of kinds `gov`, `warm`, `plan`, `verdict`, `queue` | `Coalescable` | `<kind>/<tab>` |
  | `ev` of kind `diff` | `Coalescable` | `diff/<tab>/<file>` |
  | later `ckpt` of the same id | `Coalescable` | `ckpt/<tab>/<cid>` |
  | `ping` | `Coalescable` | `ping` |
  | `ev` of kind `more`, `recorded`, `run`, `toast` | ordinary (dropped after coalescable ones, with a `gap` to the page) | |

  A dropped frame is never lost to the page: the tab journal (B2) has every event, and `gap` makes the page refetch snapshots.
* Auth `auth`. Phase B1 (route, adapter), B2 (`ev` frames through the translator), B4 (`run`, `recorded`). UI caller: `live.js`.

## 5. Boot

| Method | Path | Body → Response | Errors | Idem | Auth | Phase | UI caller |
|---|---|---|---|---|---|---|---|
| GET | `/api/hello` | → `wire.Hello` | | idem | auth | B1 | `live.js` boot |

```json
{ "boot": "7f3a9c21", "now": 1760050000123, "version": "v0.9.0", "streamAfter": 1841,
  "server": { "addr": "127.0.0.1:6969", "loopback": true, "version": "v0.9.0" },
  "limits": { "maxBody": 65536, "maxMessage": 262144, "maxQuestions": 64 },
  "ui": { "version": "sha256:4be1…" }, "active": "shop",
  "tabs": [ { "id": "shop", "sid": "20261009-221530-a91c3e", "name": "shop", "cwd": "/home/me/projects/shop", "gen": 1, "createdAt": 1760049000000, "order": 0 } ] }
```

`boot` is random per server run; a page that sees another `boot` after a reconnect reloads itself.

## 6. Tabs (live sessions)

A tab is a live harness session hosted in this process (`session.Session`, several per process, registry keyed by tab id). `id` is
stable for the tab's life; `sid` is the harness session id and changes on a fresh restart; `gen` counts generations.

| Method | Path | Request → Response | Errors | Idem | Auth | Phase | UI caller |
|---|---|---|---|---|---|---|---|
| GET | `/api/sessions` | → `{tabs: [TabSummary]}` | | idem | auth | B1 | `live.js` |
| POST | `/api/sessions` | `NewSessionRequest` → `201 {tab: TabSummary}` | `400 bad_flags`, `403 not_a_project`, `409 trust_required` (detail `TrustChallenge`), `409 limit`, `422 model` (unknown model or no key; message from `CheckModel`), `500 start` | new | auth; confirm (scope `trust:<d16>`) for the trust step | B1 | `87-ui-sheets.js` `dialogs.newSession` → `SL.act.newSession` |
| GET | `/api/sessions/{id}/snapshot` | → `TabSnapshot` | `404 not_found` | idem | auth | B1+B2 | `live.js` `loadTab` |
| PATCH | `/api/sessions/{id}` | `{name}` → `{tab}` | `400 bad_name` (empty, over 60 characters, control characters) | idem | auth | B1 (B4 sidecar) | `dialogs.rename` → `SL.act.renameSession` |
| POST | `/api/sessions/{id}/stop` | no body → `{ok}` | `409 idle` ("nothing was running") | idem | auth | B1 (`Tab.Interrupt(ctx, "turn")`) | session menu "Stop the run…", Sessions view "Stop run" |
| DELETE | `/api/sessions/{id}` | no body → `{ok}` | `409 last` ("the last session cannot be closed: start another first") | idem | auth | B1 | `ui.closeSessionAsk` → `SL.act.closeSession`; `/exit` |
| POST | `/api/sessions/resume` | `ResumeRequest` → `201 {tab}` | `404 no_session`, `409 not_resumable` (`CheckResume`/`ResolveResume` message), `409 locked` (another process holds it), `409 hosted` (detail `{tab}`: switch to it) | new | auth | B1 | `dialogs.resume`, Sessions "↺ Resume", "↺ --continue", `/resume [id]` |
| POST | `/api/sessions/{id}/restart` | `RestartRequest` → `202 {gen}` | `400 bad_flags`, `422 model`; a restart cancels the running turn and refuses its open questions | once | auth; confirm (scope `restart:<id>:<d16 of the flags>`) when the flags raise privileges (`--mode bypass\|yolo`, `--trust-project` on an untrusted directory) | B1 | `SL.act.restartTeam`, `SL.act.newChat`, `/new /clear /swarm /restart`, Run settings Apply, footer "↺ run it again" |
| GET | `/api/projects` | → `{projects: [Project]}` | | idem | auth | B1 (B4 trust state) | New session directory select, Schedule `--cwd` select |

`NewSessionRequest` (every field of the New session dialog):

```json
{ "name": "shop-2", "cwd": "/home/me/projects/shop", "model": "anthropic/claude-sonnet-5-5", "mode": "default", "swarm": 8,
  "isolation": "worktree", "verify": "go test {dirs}", "commit": false, "mailman": false, "budget": 5, "rules": ["tests"],
  "trustProject": true, "noMcp": false, "goalText": "add pagination to /orders", "effort": "default",
  "roleModels": { "backend": "heimdall/demo-model" }, "clientId": "ns-3" }
```

* `cwd` must be one of `GET /api/projects`: `--cwd`, each `--project`, the directories of live tabs and of recorded sessions, and the
  trust ledger's directories that exist. Anything else: `403 not_a_project`. (The dialog is a `<select>`; the page never sends a free
  path.)
* The fields become `sleipnir chat` arguments (`argsFor`, B1) parsed by the same parser `cmdChat` uses, so validation, defaults and
  messages are the CLI's: `swarm` omitted = the server's default (8 unless `--swarm`), `budget` absent or 0 = none, `rules` `tests`
  expands, `mode` bypass/yolo allowed here (owner decision: the dialog's choice is the confirmation).
* Trust step: when `trustProject` is true and the directory's footprint is not `Trusted` in the ledger (`trust.Scan` +
  `Ledger.Check`, `internal/trust/ledger.go:70`), and the request has no `X-Confirm`, the answer is `409 trust_required` with
  `detail` = `TrustChallenge{dir, files, digest, changed, partial, confirm, scope}` (the handler issues the id with
  `Server.IssueConfirm(r, "trust:<first 16 hex of the digest>")`). The page shows the mock's "Trust this project?" confirm and, on yes,
  repeats the request with `X-Confirm: <confirm>`; the server rescans, requires the same digest (else `409 trust_required` again),
  records it (`Ledger.Remember`; a partial footprint is trusted for this session only and a `sys` row says so) and starts the session
  with `TrustProject: true`.
* The host's prompter never asks `ToolProjectTrust` or `ToolMCPServer` during `session.New`: trust is decided by the step above;
  project MCP servers that need approval are refused at start and show "needs approval" on the MCP page (section 15).
* `goalText` is set after the start as `/goal TEXT` would be (section 10). `meta.launch` is the equivalent command line.
* `201` is sent once the tab exists; its session may still be starting (`meta.running: false`, a `say sys` "starting…" row); a start
  that fails afterwards arrives as a `sys ⚠` row and a `tab update`.

`RestartRequest.kind`:

| kind | Mock trigger | Harness equivalent | `fresh` default | Result |
|---|---|---|---|---|
| `new`, `clear` | `/new`, `/clear` | `restartArgs(s, nil, true)` (`cmd/sleipnir/restart.go:52`) | true | new generation and sid, empty chat |
| `swarm` | `/swarm N`, Run settings Apply, footer "run it again" | `restartArgs(s, ["--swarm", N, ...staged flags], fresh)` | true (D-06) | new generation |
| `restart` | `/restart [flags]` | `restartArgs(s, flags, false)` | false | new generation, same sid (conversation carried) |
| `model` | a team's manager model change | `restartArgs(s, ["--model", ref], false)` | false | as `restart` |
| `roles` | a team's role model change | `restartArgs(s, ["--role-model", "r=m", ...], false)` | false | as `restart` |

In process: the host closes the old session (`Session.Close`, isolation finish included), builds the new one from the arguments with
the shared parser and `session.New`, keeps the tab id, and publishes `reset` (VOCAB.md 11). A single agent's model change does not
restart (`Session.SwitchModel`, section 11).

## 7. Recorded sessions (B4)

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| GET | `/api/recorded` | → `{recorded: [RecordedSession], mb}` | | idem | auth | Sessions view, Resume dialog, `live.js` on `recorded` |
| POST | `/api/recorded/prune` | `PruneRequest{apply:false}` → `PrunePlan` | `400 bad_age` ("bad --older-than: 3x") | idem | auth | Sessions view preview, runner `sessions prune` |
| POST | `/api/recorded/prune` | `PruneRequest{apply:true}` → `PrunePlan` | `409 changed` (the plan differs from the confirmed one) | once | confirm (`prune:<d16 of the sorted ids>`) | "Prune with --yes…" confirm |
| POST | `/api/recorded/delete` | `DeleteRecordedRequest` → `PrunePlan` | `409 hosted`, `409 locked` | once | confirm (`delete:<d16 of the sorted ids>`) | none in v3 (D-02) |
| GET | `/api/recorded/{sid}/events?from=&limit=` | → `{events: [UI events], next}` | `404 not_found` | idem | auth | Sessions "Replay" (D-10) |

Prune arithmetic is the CLI's (`planPrune`, `cmd/sleipnir/sessions_prune.go:50`, extracted): older than `olderThan` AND not among the
newest `keep` session directories (live tabs count; they are the newest) AND not written in the last ten minutes; directories locked by
another process are skipped. Deletion uses `session.RemoveStoredSession` (`internal/session/prune.go:20`). `RecordedSession` has the
fields the Sessions view and the Resume dialog read (`id first model cost mb ageS agents resumable dur interrupted`) plus `name`,
`cwd`, `lastWritten`, `locked`.

## 8. Chat (B1)

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| POST | `/api/sessions/{id}/messages` | `MessageRequest` → `SendResult` | `400 empty`, `413 body_too_large` (256 KiB) | new | auth | `84-ui-chat.js` `submit` → `SL.act.send` |
| POST | `/api/sessions/{id}/command` | `CommandRequest` → `CommandResult` | `404 unknown_command` ("unknown command /x: / lists them") | once | auth | `SL.palette.runLine` for lines without a page handler |
| POST | `/api/sessions/{id}/steer` | `{text}` → `{ok}` | `400 empty`, `409 idle` ("no turn is running: send it as a message") | once | auth | `/steer` → `SL.act.steer` |
| POST | `/api/sessions/{id}/interrupt` | `{target:"turn"}` → `{ok}` | `409 idle` ("no turn is running") | idem | auth | Esc, "Interrupt turn" → `SL.act.interrupt` |
| GET | `/api/sessions/{id}/complete?prefix=&limit=8` | → `{paths: [string]}` | | idem | auth | `@` completion (registered and implemented by B3) |
| GET | `/api/sessions/{id}/slash` | → `{slash: [SlashEntry]}` | | idem | auth | `/` menu and palette (`D.slash` per tab) |

* **Send.** `text` reaches the agent; `display` (optional) is what the transcript shows (the composer's paste chips; the page
  expands them into `text`). The host echoes `say {who:"you", text: display || text}` on delivery. No turn running: the host starts
  one (`Session.Run`, the goal loop when a goal is active). A turn running (or a woken team manager working): the line is queued (the
  TUI's typed-ahead queue, `internal/tui/app/chat.go:696-731`, reimplemented in the host), `meta.queued` lists the queue; lines are
  delivered in order when the turn ends and the goal has no continuation (`app/chat.go:908`).
* **Command.** Built-in slash commands have page handlers (93-palette.js `H`) calling the dedicated routes; this route takes custom
  commands, skills and MCP prompts (`expandSlash`, `cmd/sleipnir/chat.go:519`: the prompt is sent as a message, `sent: true`) and
  `/mcp reconnect NAME`; `output` is what the CLI prints (`slashTo`'s writer, captured), shown by the page as a local card.
* **Steer.** `Agent.Steer` on the manager (`internal/agent/agent.go:497`); emits `steer {to:"mgr"}`; logs `user.steer`.
* **Interrupt.** Cancels the turn's context, refuses the open questions of the agent the person talks to (`by:"canceled"`), pauses the
  goal ("you interrupted it", `internal/session/goal.go:128`); emits `interrupt {id:"turn"}`, `goal {s:"paused"}`, the manager's `state`.
* **Complete.** Project paths containing `prefix`, walked like the `glob` tool (gitignore honoured, `.git` skipped, no symlink
  followed), denied paths excluded, at most `limit` (≤ 50).
* **Slash.** The spec's `chatSlash` built-ins, then the tab's custom commands (`commands.Registry.List`), skills and MCP prompts
  (`Session.MCPPrompts`, `internal/session/mcp.go:274`), each as a `SlashEntry` with `custom: true`.

## 9. Approvals (B1)

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| GET | `/api/questions` | → `{questions: [OpenQuestion]}` | | idem | auth | tests and resync (the page derives the inbox from snapshots) |
| POST | `/api/questions/{qid}/answer` | `AnswerRequest` → `AnswerResult` | `404 no_question`, `409 answered`, `409 too_soon` (detail `{retryAfterMs}`), `400 bad_choice` | once | auth | `85-ui-approvals.js` `answer`/`tellInstead` → `SL.act.answerQuestion` |

`AnswerRequest {choice: 1|2|3, note?}`; `note` (≤ 2,000 chars) only with 3. Semantics: VOCAB.md section 9. Choice 2 also publishes
a `meta` patch with the new rule (origin `don't ask again`).

## 10. Goal (B1)

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| POST | `/api/sessions/{id}/goal` | `GoalRequest` → `{ok, state}` | `400 empty` ("/goal needs text"), `409 no_goal`, `409 not_paused`, `409 not_active` | set: once; others: idem | auth | Goal sheet, `/goal ...` |

`set`: `goal.New(text)` and a turn with `goal.Start` (`internal/goal/goal.go`); emits `say you "/goal TEXT"`, `goal {s:"active"}`,
`say sys "goal set; plan:" plan:true`. `pause`: interrupts the running turn first, then pauses ("you paused it"). `resume`: clears the
pause and starts a continuation. `clear`: interrupts first, clears goal and plan, `goal {s:"cleared"}`. The judge runs after each turn
in the host's goal loop (extracted from `sessionHost.Turn`/`judgeGoal`, `cmd/sleipnir/chat_tty.go:345-372`, shared with the TUI) and
logs `goal.judge`. Budget reached while a goal is active: pause with "budget reached".

## 11. Session settings (B1; the check route B3)

Each is acknowledged by a `sys` row (the mock's texts) and a `meta` patch.

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| POST | `/api/sessions/{id}/mode` | `ModeRequest` → `{ok}` | `400 bad_mode` | idem | auth; confirm (`mode:<mode>:<id>`) for bypass and yolo (`RequireConfirm` from the handler) | mode menu, Mode sheet, Permissions page, shift+tab |
| POST | `/api/sessions/{id}/model` | `ModelRequest` → `{ok, restarted}` | `422 model` (CheckModel's message) | idem | auth | Models "Use for", Roles selects, `/model ref`, `/roles r=m` |
| POST | `/api/sessions/{id}/effort` | `EffortRequest` → `EffortResult` | `400 bad_level` | idem | auth | Roles & effort seg, `/effort` |
| POST | `/api/sessions/{id}/budget` | `BudgetRequest` → `{ok, paused}` | `400 bad_budget` ("a budget is a number of dollars, or off") | idem | auth | Budget page, `/budget` |
| POST | `/api/sessions/{id}/compact` | `CompactRequest` → `{from, to}` | `409 busy` (a compaction runs), `409 nothing` | once | auth | Compact dialog, `/compact` |
| PATCH | `/api/sessions/{id}/launch` | `LaunchPatch` → `{ok}` | `400 bad_isolation` | idem | auth | Run settings isolation, verify, flags |
| GET | `/api/sessions/{id}/rules` | → `{rules: [Rule]}` | | idem | auth | tests (meta carries them) |
| POST | `/api/sessions/{id}/rules` | `RuleRequest` → `RuleResult` | `400 bad_rule` (`perm.ParseRule`'s message, or "a rule needs text, e.g. tests or Bash(go test:*)") | idem | auth | "Add for this session", "+ the tests preset", `/allow` |
| POST | `/api/sessions/{id}/rules/remove` | `RuleRequest` → `RuleResult` | `404 no_rule`, `409 fixed` ("built in or from a file: change it there") | idem | auth | Permissions "remove" |
| POST | `/api/sessions/{id}/permissions/check` | `PermCheckRequest` → `PermVerdict` | `400 bad_tool` | idem (no effect) | auth | "Would it ask?" Check (B3) |

* Mode: `Perm.SetMode` (`internal/perm/engine.go:287`). Texts: `mode: <m>`, `mode: plan (read-only)`, `⚠ mode: <m> (dangerous:
  <description>)`. The server refuses bypass/yolo without the confirmation whatever the page did.
* Model: single agent → `Session.SwitchModel` (`internal/session/switch.go:22`), text `model: <ref> (the conversation carries over;
  the prompt cache starts over)`; team or role → restart kind `model`/`roles`, `restarted: true`. A model of unknown price is allowed;
  the text says `(price unknown)`.
* Effort: `Session.SetEffort` (`internal/session/effort.go:13`), text `reasoning effort: <level> (closest supported level)`.
* Budget: `Session.SetBudget` (`switch.go:79`) for a single agent; the team budget at run time is new (B1 adds it in
  `internal/session/web_access.go` over the swarm's budget); `off` = 0; when already over and a goal is active: pause it and say
  `budget reached: the goal is paused`.
* Compact: `Session.Compact` (`internal/session/session.go:1130`); the `compact` event comes from the log.
* Launch: staged in the tab, applied by the next restart; texts `--isolation worktree (applies when the team starts again)`,
  `--verify "<cmd>"`, `--commit on (applies when the team starts again)`.
* Rules: allow → `Session.AllowForSession` (`switch.go:91`); deny and ask → `Engine.AddRule(ScopeSession, rule)` (`engine.go:300`);
  `tests` → `perm.TestsAllow` (`internal/perm/presets.go:6`); removal → `Engine.RemoveRule` (B3). Origins: `this session`, `/allow`,
  `don't ask again`, `the tests preset`, `--allow flag`; configuration rules carry their layer and file (B4's rule origins). Texts:
  `<effect> this session: <rule> · from <origin>`, `rule removed: <rule>`.
* Check: `Engine.Classify` (B3, never prompts) → `{d: allowed|ask|refused, why, cls: ok|warm|err}`; `why` names the deciding rule with
  its origin or the mode's default, as the mock's texts do.

## 12. Workspace (B3)

Paths are project-relative (`/`); `at`, `from`, `to` are checkpoint ids (`c07`), `base` (before the session) or `live` (now).

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| GET | `/api/sessions/{id}/ws/index` | → `WsIndex` (ETag = `version`) | | idem | auth | `96b-ws-data.js` `info()` |
| GET | `/api/sessions/{id}/ws/file?path=&at=` | → `WsContent` | `400 bad_path`, `403 denied`, `404 no_file` | idem | auth | `textAt`, `blame`, Whole file |
| GET | `/api/sessions/{id}/ws/diff?path=&from=&to=` | → `WsDiff` | as above | idem | auth | `hunks`, `counts`, Diff, revert preview |
| POST | `/api/sessions/{id}/ws/revert` | `RevertRequest` → `WsRevert` | `409 changed` (the file changed since the diff was drawn), `409 busy` (a turn runs), `422 conflict` (the hunk does not apply) | once | confirm (`revert:<id>:<d16 of path,key,from,to>`) | Revert hunk dialog → `SL.act.revertHunk` |
| POST | `/api/sessions/{id}/ws/revert/{rid}/undo` | no body → `{ok}` | `409 changed` | once | auth | "Undo" → `SL.act.unrevertHunk` |
| POST | `/api/sessions/{id}/ws/restore` | `RestoreRequest{dryRun:true}` → `RestorePlan` | `404 no_checkpoint`, `409 nothing` ("c04 has nothing to put back") | idem | auth | Restore dialog preview |
| POST | `/api/sessions/{id}/ws/restore` | `RestoreRequest{dryRun:false}` → `RestorePlan` | `409 busy`, `409 conflict` (detail: files edited since; force is not offered) | once | confirm (`restore:<id>:<cid>`) | "Restore the files" → `SL.act.rewind` |
| POST | `/api/sessions/{id}/ws/restore/undo` | no body → `RestorePlan` | `409 nothing`, `409 changed` | once | confirm (`restore.undo:<id>`) | "Undo the restore" → `SL.act.undoRewind` |
| PUT | `/api/sessions/{id}/ws/reviewed` | `ReviewedRequest` → `{ok}` | `400 bad_path` | idem | auth | Reviewed marks → `SL.act.markReviewed` |
| GET | `/api/sessions/{id}/ws/worktrees` | → `{worktrees: [Worktree]}` | `409 not_isolated` | idem | auth | none in v3 (D-03) |
| GET | `/api/sessions/{id}/ws/queue` | → `MergeQueueStatus` | `409 not_isolated` | idem | auth | none in v3 |
| GET | `/api/sessions/{id}/ws/verify/{task}` | → `VerifyOutput` | `404 not_found` | idem | auth | none in v3 (D-03) |
| POST | `/api/sessions/{id}/ws/accept` | `AcceptRequest` → `AcceptResult` | `409 dirty`, `409 moved`, `409 nothing` | once | confirm (`accept:<id>`) | none in v3 (D-03) |
| GET | `/api/sessions/{id}/complete` | section 8 | | | | |

* **Index.** One `WsCheckpoint` per `Store.List()` entry (`internal/checkpoint/store.go:798`), arrival order, with its own change set
  (`Store.Changes`, B3: files written while it was current, status, `+/-`, writer agents, the writer's task at write time). `Tree`:
  files that exist now or existed at any checkpoint (walked like `glob`), with `status` against the base, `owner` (last writer),
  `lease` (an agent's lease or task scope covering it: `workspace.Match`, `internal/workspace/scope.go:486`), `protected` (Read deny
  from `Engine.Classify`), `ask` (write ask rule). Isolated teams: change sets and authorship from the workers' commits and the
  integration branch.
* **Content at a point.** `Store.ContentAt(id, path)` (B3): the earliest pre-image among checkpoints at or after `id`, else the current
  file; `base` = the pre-image of the first checkpoint that touched the file. Blame: exact from the write journal (`Store.Writes`, B3)
  when every change was a tool write; `exact: false` otherwise (unexplained lines `ag: "-"`); isolated teams: `git blame` on the
  integration branch (`Repo.Blame`, B3).
* **Revert.** The server takes the hunk from its own diff of `from..to` (the page sends the key `oldStart:newStart`, never patch
  text), checks the file's hash is the one the diff was computed from, takes `Before("person")`, reverse-applies the hunk
  (`gitx.ApplyWith{Reverse, Check}`, `internal/gitx/apply.go:38`, or an in-memory line patch outside git), records `After`, emits `sys
  "↺ reverted a hunk of P"`, and notifies the agent that last wrote the file. Undo reapplies forward under the same checks.
* **Restore.** Preview `Store.Restore(id, RestoreOpts{DryRun: true})` (`internal/checkpoint/restore.go:182`); apply: an undo capture of
  every file the restore writes (`Store.CaptureUndo`, B3), the restore, the rewind note to the agents (`rewindNote`,
  `cmd/sleipnir/chat.go:744`, moved to a shared helper), `ckpt {safety: true}`, `sys "restored the files of c07 (N files): <label> ·
  safety checkpoint cNN taken first"`. Refused while a turn runs. Undo: `Store.RestoreUndo` with the same conflict rules.
* **Reviewed.** Stored in the session sidecar (`<session dir>/web.json`, `session.UpdateMeta`, B4) and returned in `WsIndex.reviewed`.

## 13. Settings pages (B4 unless noted)

| Page | Read | Writes |
|---|---|---|
| Models | `GET /api/models?refresh=0\|1` → `ModelsView` | `POST /api/models/fav` `FavRequest` (idem, auth); "Use for" → `POST /api/sessions/{id}/model` |
| Roles & effort | `ModelsView.roles/roleModels/efforts` + tab meta | role select → `.../model {ref, role}`; effort → `.../effort` |
| Budget | tab meta and model | `.../budget` |
| Permissions | `GET /api/sessions/{id}/permissions` → `PermissionsView` | mode (confirm for bypass/yolo), rules add/remove, check (section 11) |
| Trust | `GET /api/sessions/{id}/trust` → `TrustView` | `POST /api/trust` `TrustRequest`: `on:true` needs the X-Confirm id from `GET /api/trust/challenge?dir=` (scope `trust:<d16>`); the server rescans and answers `409 changed` when the digest moved; `on:false` is auth only |
| Run settings | tab meta | `PATCH .../launch`, `POST .../restart` |
| MCP servers | `GET /api/sessions/{id}/mcp` → `MCPView` | `POST /api/sessions/{id}/mcp/{name}/approve` (confirm `mcp.approve:<d16 of root,name,fingerprint>`), `/revoke`, `/test`, `/reconnect` (auth) → `MCPResult` |
| Skills, commands & hooks | `GET /api/sessions/{id}/skills` → `SkillsView` | none ("Insert in the chat" is page-local) |
| Providers & login | `GET /api/providers` → `ProvidersView` | section 14 |
| Config layers | `GET /api/sessions/{id}/config` → `ConfigView` | none |
| Appearance & motion | none (browser-local) | none (localStorage) |

Read models use the active tab's project (`root`, `cwd`, trust); without a tab, the server's `--cwd`.

* **Models.** `internal/catalog` (B4, from `cmd/sleipnir/models.go:49-308`: `modelFilter.keep`, `fetchModels`, `usableSources`,
  `planModels`), with `gateway.Fetch` (`internal/provider/gateway/catalog.go:222`), local servers' ids and ChatGPT plan models;
  cached 6 h in `<state>/cache/catalog-<h>.json`; `refresh=1` refetches (network). Unknown prices `null`. Favourites: `config.Save` of
  `models.favorites` (`toggleFavorite`, `models.go:398`), writes serialized per file in the server.
* **Permissions.** `rules` grouped `allow deny ask`, each with `origin` (`user config`, `project config`, `local config`, `built-in
  protection`, `flag`) and `file` (B4's per-rule origins), plus built-in protections (`internal/perm/builtin.go`) and `session` (the
  tab's session rules). `modes`, `order`, `managerWrites` (the swarm's manager refusal text), `testsPreset` (`perm.TestsAllow`, summary
  moved from `cmd/sleipnir/allow.go`).
* **Trust.** `trust.Scan(root, cwd, home)` (`internal/trust/trust.go:85`), `Ledger.All()` (`ledger.go:126`) mapped to `trusted`,
  `changed: <what>`, `gone: directory is gone`; on → `Ledger.Remember` (`ledger.go:89`) after a fresh scan matching the confirmed
  digest; off → `Ledger.Forget` (`ledger.go:106`). Writes serialized per ledger file and re-read before write.
* **MCP.** `session.MCPEntries` (`internal/session/mcp.go:422`) + the tab's `MCPStatus()` (`mcp.go:249`): `running`, `needs
  approval`, `failed: <error>`, `off`. Approve → `OpenMCPApprovals(home).Approve` (`mcp.go:393,401`), store re-read on write (B4 fixes
  the load-once of `mcp.go:313-377`); effective at the tab's next start (D-08), result text `approved: it starts when the team starts
  again (the tool list of a running session is fixed)`. Revoke → `Revoke` (`mcp.go:406`). Test → the extracted `mcpTest`
  (`cmd/sleipnir/mcp.go:159`) without creating a session directory, refused `409 needs_approval` for an unapproved project entry.
  Reconnect → `Session.MCPReconnect` (`mcp.go:266`). `envRefs` lists `${NAME}` references.
* **Skills.** `skills.Discover` (`internal/skills/skills.go:172`), `commands.Load` (`internal/commands/commands.go:198`), hooks
  (`hooks.ParseAs`, `internal/hooks/parse.go:49`) without headers or env values.
* **Config.** `config.Load` `Report` (`internal/config/load.go:45`) → layers and effective values with layer and file, through B4's
  `config.Redact` allow-list.

## 14. Providers and sign-in (B4)

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| GET | `/api/providers` | → `ProvidersView` | | idem | auth | Providers page, Doctor endpoints |
| POST | `/api/providers/{name}/signout` | no body → `ProviderRow` | `409 env` ("the key comes from the environment variable X: unset it there") | idem | auth | "Sign out" |
| POST | `/api/providers/recheck` | no body → `ProvidersView` | | idem | auth | sign-in modal's "I ran it" button (D-01) |
| POST | `/api/providers/{name}/key` | `KeySaveRequest` → `ProviderRow` | `400 bad_key`, `422 rejected` | once | confirm (`key:<name>`) | none in v3 (D-01: built only if the owner picks browser sign-in) |
| POST | `/api/providers/chatgpt/signin` | no body → `202 SignInStart` | `409 running` | new | confirm (`signin`) | none in v3 (D-01) |
| GET | `/api/providers/chatgpt/signin/{sid}` | → `SignInState` | `404 not_found` | idem | auth | none in v3 (D-01) |

`key`: `stored` (auth.json), `env` (a variable set at start: B4 captures `harden.Status.Moved` in `main` before
`config.LoadStoredKeys`), `signed in` (ChatGPT: `chatgptauth.Connected`, `internal/chatgptauth/chatgptauth.go:171`; `Who()` email only),
`none`. Sign out: stored key removed from auth.json and `harden.Provide(env, "")`; ChatGPT → `chatgptauth.Logout` (network). If D-01
picks browser sign-in: key save → `config.SaveStoredKey` (`internal/config/auth.go:47`) then `harden.Provide`, optional check request
(`checkKey`, `cmd/sleipnir/pick.go:230`, extracted); ChatGPT → `chatgptauth.Login` in a goroutine with a `LoginIO.Open` that hands
the URL to the page; 5-minute limit.

## 15. MCP

Routes and semantics in section 13. `approve` needs the confirmation; `test` may start the server's process (as `sleipnir mcp test`).

## 16. Trust

`GET /api/sessions/{id}/trust` → `TrustView`; `GET /api/trust/challenge?dir=` → `TrustChallenge` (scan and a confirmation id for
`trust:<d16>`; used by "Trust these files" and by the New session trust step); `POST /api/trust` `TrustRequest`.

## 17. Schedule and daemon (B4)

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| GET | `/api/schedule` | → `ScheduleView` | | idem | auth | Schedule `draw` |
| GET | `/api/schedule/next?cron=` | → `CronCheck` | | idem | auth | Add form `nextHint` (debounced 150 ms) |
| POST | `/api/schedule/jobs` | `JobRequest` → `201 ScheduleJob` | `400 bad_cron` ("the cron expression is not valid"), `400 empty` ("a goal is required"), `400 bad_budget`, `403 dangerous_mode` ("bypass and yolo cannot be scheduled from here") | new | confirm (`job.add`) | "Add the job" |
| PUT | `/api/schedule/jobs/{job}` | `JobRequest` → `ScheduleJob` | as add, `404 not_found` | idem | confirm (`job.edit:<job>`) | none in v3 (D-02) |
| POST | `/api/schedule/jobs/{job}/pause` | `{paused}` → `ScheduleJob` | `404 not_found` | idem | auth | none in v3 (D-02) |
| DELETE | `/api/schedule/jobs/{job}` | no body → `{ok}` | `404 not_found` | idem | auth | "Remove" confirm |
| POST | `/api/schedule/jobs/{job}/run` | no body → `202 RunStarted` | `409 running` | new | auth | "Run now" (output as `run` frames) |
| GET | `/api/schedule/jobs/{job}/log` | → `JobLog` | `404 not_found` | idem | auth | "Log" |
| POST | `/api/schedule/daemon` | `{action: "start"\|"stop"\|"once"}` → `DaemonState` | `409 external` ("a daemon outside this page holds the lock (pid N): stop it there") | idem | auth | daemon buttons |

Store `sched.Store` (`internal/sched/store.go`) with B4's `Update`, `Paused`, a file lock `<state>/schedule.lock` shared with
`sleipnir daemon` (two daemons can no longer double-start a job), and `daemonTick`/`runJob`/`jobEnv` moved from
`cmd/sleipnir/schedule.go:156-240` into `internal/sched`. `start` runs the daemon loop in this process (it stops with the server);
`external` when another process holds the lock. Run now: `RunJob` as a child of this binary (`sleipnir run --quiet ...`), held keys
re-injected by `JobEnv`, output tee'd to the job log and streamed. Bypass/yolo jobs refused at add and edit.

## 18. Runner and the CLI spec (B4)

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| GET | `/api/cli` | → `CLISpec` | | idem | auth | `D.spec` (boot) |
| POST | `/api/runs` | `RunRequest` → `202 RunStarted` | `400 bad_flags` (flag parse message), `403 tty_only` (18.2), `409 busy` (4 runs) | new | auth; confirm (`run:<d16 of argv>`) for `priv` commands | runner `run()` |
| DELETE | `/api/runs/{run}` | no body → `{ok}` | `404 not_found` | idem | auth | runner `stop()` (Reset, another command, leaving the view: D-05) |
| GET | `/api/runs` | → `{runs: [RunInfo]}` | | idem | auth | recent runs (`G.runs`) |

### 18.1 Execution

A run is a child process of this binary, `sleipnir <path...> <flags...> <positionals...>` from the spec (never a shell), in the tab's
directory (`RunRequest.tab`, else `--cwd`); environment = the server's (keys moved out at start) plus held keys via `JobEnv` only for
`net` commands; stdin closed; its own process group, killed as a group on cancel (SIGTERM, SIGKILL after 5 s); 1-hour limit. Stdout
lines → `run` frames `{k:"out"}`, stderr → `{k:"err"}`, sanitized, 4 KiB per line, 20,000 lines per run, batched per 50 ms; the end
is `{result: {exit, ms, card}}`. B4 may run a command in process where the CLI path is a clean function, with identical output.

### 18.2 Command modes (field `mode` of the spec; binding)

| `mode` | Commands | Behaviour |
|---|---|---|
| `run` | `config`, `sessions`, `sessions prune` (without `--yes`), `models fav list`, `recon`, `sim`, `friction`, `inspect` (only with `--json`; else `tty_only` with "use the Cache view here, or --json"), `replay` (only with `--final` or `--record`; else `tty_only`), `trust`, `trust list`, `mcp`, `mcp list`, `schedule`, `version`, `help`, `rl show`, `rl report`, `rl compare`, `rl expand`, `rl tasks stats`, `rl tasks validate`, `rl tasks filter`, `rl tasks split`, `rl verify`, `rl`, `rl taskgen`, `rl tasks` (groups print their subcommands) | runs as is |
| `net` | `models`, `doctor`, `update` (with `--check`), `rl eval`, `rl rollout`, `rl taskgen composite\|fixture\|git\|mutate\|recall`, `rl tasks check`, `rl reward`, `rl export`, `run`, `swarm`, `demo` | held keys re-injected; network allowed |
| `priv` | `sessions prune --yes`, `trust add` (`--yes` forced), `trust forget`, `mcp approve` (`--yes` forced), `mcp revoke`, `schedule add`, `schedule rm`, `models fav add`, `models fav rm`, `update` (install), `init`, `logout`, and `run`/`swarm` with `--mode bypass\|yolo` | needs the confirmation |
| `server` | `mock`, `rl serve`, `daemon` | runs until cancelled (D-05) |
| `tty_only` | `chat`, `watch`, `login` | refused `403 tty_only` with a sentence that names the web equivalent (`chat`: "+ New session starts the same session here"; `login`: "Settings › Providers & login"; `watch`: "the Cockpit shows it live") (D-11) |

### 18.3 The CLI spec source and its freshness

* `internal/web/clispec/clispec.json`: checked in, generated, embedded (`//go:embed`), served by `GET /api/cli`; the shape of the
  mock's `cli-spec.json` (`wire.CLISpec`) plus `mode`.
* Generator: `go generate ./internal/web/clispec` → `go run ./gen` (B4), reading `docs/CLI.md` (its `<!-- flags: NAME -->` blocks are
  the binary's own `-h` output, kept current by `scripts/gen-cli-docs.sh --check`), the curated positionals and enumerations (ported
  from `docs/design/web-mocks/_src/data/gen-cli-spec.mjs`), the mode table of 18.2, and `chatHelp` of `cmd/sleipnir/chat.go:282`
  (read as text) for `chatSlash`; output deterministic.
* Drift: a test in `internal/web/clispec` regenerates in memory and compares with the file ("run go generate ./internal/web/clispec");
  a second test asserts every command listed by `sleipnir --help` has an entry with a mode. `scripts/check.sh` and CI need no change.

## 19. Doctor, update and other tools (B4)

| Method | Path | Request → Response | Errors | Idem | Auth | UI caller |
|---|---|---|---|---|---|---|
| GET | `/api/doctor/endpoints` | → `{endpoints: [DoctorEndpoint]}` | | idem | auth | Doctor endpoint select |
| POST | `/api/doctor` | `{model?, baseUrl?, deep}` → `202 RunStarted` | `400 bad_flags` ("a custom endpoint needs --base-url and --model") | new | auth | Doctor "Run the probe" |
| GET | `/api/update` | → `UpdateStatus` | `502 network` | idem | auth | runner `update --check` card |
| POST | `/api/update/install` | no body → `202 RunStarted` | `409 none` | once | confirm (`update:<version>`) | runner `update` |

Doctor: B4 extracts `cmdDoctor`'s body (`cmd/sleipnir/main.go:426`) into a function returning the `internal/provider/probe` report
with a per-step callback; each step is a `run` frame `{step: DoctorStep}`, the end `{verdict: DoctorVerdict, result}`.

## 20. Confirmation scopes

The page asks `POST /api/confirm {"scope": S}` and sends `X-Confirm: <id>`; the route declares or requires the same `S`. `<d16>` is the
first 16 hex characters of SHA-256 over the canonical JSON of the listed values (sorted keys, no spaces); `<id>` is the tab id.

| Action | Scope |
|---|---|
| bypass / yolo on a running tab | `mode:<mode>:<id>` |
| New session trust step, Trust these files | `trust:<d16 of {dir, digest}>` (issued by the server in the challenge) |
| restart with privilege-raising flags | `restart:<id>:<d16 of the flags>` |
| prune with `--yes` | `prune:<d16 of the sorted ids>` |
| delete recorded sessions | `delete:<d16 of the sorted ids>` |
| approve an MCP server | `mcp.approve:<d16 of {root, name, fingerprint}>` |
| add / edit a schedule job | `job.add` / `job.edit:<job>` |
| store a key / ChatGPT sign-in | `key:<name>` / `signin` |
| install an update | `update:<version>` |
| revert a hunk | `revert:<id>:<d16 of {path, key, from, to}>` |
| restore / undo a restore | `restore:<id>:<cid>` / `restore.undo:<id>` |
| accept verified → commit | `accept:<id>` |
| a `priv` runner command | `run:<d16 of the argument vector>` |

## 21. Error codes

Envelope (committed, `internal/web/doc.go`): `unauthenticated` (401), `bad_host` (403), `forbidden_origin` (403), `forbidden_site`
(403), `csrf` (403), `unsupported_media_type` (415), `body_too_large` (413), `bad_json` (400), `bad_request` (400), `not_found` (404),
`method_not_allowed` (405), `confirm_required` (428, with `X-Confirm-Scope`), `confirm_invalid` (403), `rate_limited` (429),
`busy` (503), `too_many_streams` (429), `shutting_down` (503), `internal` (500).

Routes (this contract):

| Status | `code` | Meaning |
|---|---|---|
| 400 | `bad_path`, `bad_flags`, `bad_mode`, `bad_rule`, `bad_level`, `bad_budget`, `bad_cron`, `bad_age`, `bad_choice`, `bad_tool`, `bad_name`, `bad_key`, `bad_isolation`, `empty` | the request is malformed; the sentence says what to change |
| 403 | `denied`, `not_a_project`, `tty_only`, `dangerous_mode` | refused by policy |
| 404 | `no_session`, `no_question`, `no_file`, `no_checkpoint`, `no_rule`, `unknown_command` | no such thing |
| 409 | `answered`, `too_soon`, `busy`, `idle`, `last`, `limit`, `locked`, `hosted`, `not_resumable`, `trust_required`, `changed`, `conflict`, `nothing`, `fixed`, `not_isolated`, `needs_approval`, `external`, `running`, `env`, `dirty`, `moved`, `no_goal`, `not_paused`, `not_active`, `none` | the state does not allow it now |
| 422 | `model`, `rejected`, `conflict` | the harness refused the value (its message, sanitized) |
| 500 | `start` | a session failed to start (sanitized) |
| 502 | `network` | an outbound check failed |

## 22. Limits

| What | Limit |
|---|---|
| request body (`RouteOpts.MaxBody`) | 64 KiB by default (`web.DefaultMaxBody`); `messages` 256 KiB; `providers/{name}/key` 8 KiB; `runs` 64 KiB |
| string fields | names 60; rules 500; goal 4,000; steer 4,000; note 2,000; cron 100; paths 4,096 |
| `ws/file` | 2 MiB of text, then truncated |
| `ws/diff` | 2 MiB per file in, 256 KiB of hunks out (`[diff truncated]`, as `internal/checkpoint/diff.go:40`) |
| snapshot | 16 MiB; above it the journal's front folds into the keyframe |
| stream event | 1 MiB (Δ3); larger text continues in `more`, larger output is truncated with a marker |
| runner output | 20,000 lines per run, 4 KiB per line |
| open questions | 64 per tab |
| tabs | 16 |
| `complete` | 50 paths |
