# Web API

`sleipnir web` serves a page, and the page works through an HTTP and event-stream interface of the same server. This document
describes that interface: how a request is authenticated, how privileged actions are confirmed, how the stream is delivered, and
every route. It is the interface of the page and changes between releases; it is not a stability promise, and a script that uses
it should check the routes it depends on against the release it runs against. A script may call it with the run token as a bearer
token (see [Calling the API](#calling-the-api)).

The JSON shapes are the Go types of package `internal/web/wire`: `api.go` (sessions, requests, frames), `events.go` (the events of
a session), `ws.go` (the Workspace), `settings.go` (Settings and Tools pages) and `runner.go` (the command runner). Field names
are the `json` tags. The threat model and what the token protects are in [Security](SECURITY.md) section 5; the command and its
flags are in [Command-line reference](CLI.md).

## Calling the API

**Address and token.** The first line `sleipnir web` prints on standard output is `http://127.0.0.1:PORT/?token=<run token>`. The
token is 256 random bits made when the command starts and valid for that run. A script sends it as `Authorization: Bearer <token>`
on any route. A browser opens the address once: `GET /?token=...` (that path only) answers `303` to `/` and sets the cookie
`sleipnir_web` (HttpOnly, SameSite=Strict, 24 hours), which holds an id unrelated to the token. A request with neither answers
`401 unauthenticated`. Ten wrong tokens in a minute make further attempts wait (`429 rate_limited` with `Retry-After`); sessions
that exist are not slowed.

**What every request must satisfy**, in the order the server checks it:

| Rule | Refusal |
|---|---|
| `Host` names this machine (`localhost`, `127.0.0.1`, `[::1]`) and the bound port | `403 bad_host` |
| `Sec-Fetch-Site`, when sent, is `same-origin` or `none` (a top-level navigation to a page is the one exception) | `403 forbidden_site` |
| Any method but GET or HEAD: `Origin`, when sent, is exactly the server's own (scheme, host, port) | `403 forbidden_origin` |
| A request other than GET or HEAD with neither `Origin` nor `Sec-Fetch-Site` is not a browser's: it must carry the bearer token, never only the cookie | `403 forbidden_origin` |
| Any method but GET or HEAD: the header `X-Sleipnir-Web: 1` | `403 csrf` |
| A body is JSON: `Content-Type: application/json` (UTF-8 only), within the route's cap, one value, no unknown fields | `415 unsupported_media_type`, `413 body_too_large`, `400 bad_json` |
| A route that takes no body (`NoBody`) refuses one | `400 bad_request` |
| At most 128 requests in flight (event streams excluded) | `503 busy` with `Retry-After` |

GET routes never change state and need no header. The default body cap is 64 KiB; the exceptions are `POST /api/confirm` (1 KiB),
`PATCH /api/sessions/{id}` (4 KiB), `POST /api/questions/{qid}/answer` (16 KiB) and `POST /api/sessions/{id}/messages` (256 KiB).
Request headers are capped at 16 KiB, and every response is `no-store`.

**Responses.** A JSON answer has `Content-Type: application/json; charset=utf-8`, with `<`, `>` and `&` escaped as `<`,
`>` and `&`. Every response carries `X-Request-Id`, which the server's log lines also carry. A path that no route
matches is `404 not_found`; a path that has routes only for other methods is `405 method_not_allowed` with `Allow`.

**Errors.** Every non-2xx answer from the server has the body

```json
{"error": "one sentence for a person, 512 bytes at most", "code": "stable_identifier", "detail": {}}
```

`detail` is present only where a route says so (the scope of a confirmation, a trust challenge, the plan that changed). The
message holds no path outside the project, no secret and no body content; match on `code`. The codes the server uses:

| Status | Codes |
|---|---|
| 400 | `bad_json`, `bad_request`, `bad_path`, `bad_flags`, `bad_mode`, `bad_rule`, `bad_level`, `bad_budget`, `bad_cron`, `bad_age`, `bad_choice`, `bad_tool`, `bad_name`, `bad_key`, `bad_isolation`, `empty` |
| 401 | `unauthenticated` |
| 403 | `bad_host`, `forbidden_site`, `forbidden_origin`, `csrf`, `forbidden`, `confirm_invalid`, `not_a_project`, `denied`, `tty_only`, `dangerous_mode` |
| 404 | `not_found`, `no_session`, `no_question`, `no_file`, `no_checkpoint`, `no_rule`, `unknown_command` |
| 405, 413, 415 | `method_not_allowed`, `body_too_large`, `unsupported_media_type` |
| 409 | `answered`, `too_soon`, `busy`, `idle`, `last`, `limit`, `locked`, `hosted`, `not_resumable`, `trust_required`, `changed`, `conflict`, `nothing`, `fixed`, `not_isolated`, `needs_approval`, `external`, `running`, `env`, `dirty`, `moved`, `no_goal`, `not_paused`, `not_active`, `none` |
| 422 | `model`, `rejected`, `conflict` |
| 428 | `confirm_required` |
| 429, 500, 502, 503 | `rate_limited`; `internal`; `network`; `busy`, `too_many_streams`, `shutting_down` |

**Retries.** `POST /api/sessions`, `POST /api/sessions/resume` and `POST /api/sessions/{id}/messages` accept an optional
`clientId` (64 characters at most): a repeat with the same id within 60 seconds returns the first answer instead of doing it
again.

**Identifiers in paths** are matched before anything is looked up, and a mismatch is `400 bad_request`: `{id}` a session (tab)
id, `[a-z0-9-]{1,40}`; `{sid}` a recorded session, `YYYYMMDD-HHMMSS-xxxxxx`; `{qid}` a question, `q_` and 26 base32 characters;
`{run}` a run; `{rid}` a revert; `{job}` a schedule job; `{name}` a tool server or provider.

## Confirmations

An action that raises privilege needs a second step. A bypass or yolo mode, trusting a project's own files, an allow rule the
session does not have, a `--verify` command, taking back a deny or ask rule, approving a tool server, adding or editing a
schedule job, installing an update, deleting recorded sessions, restoring or reverting files, applying a team's verified work, and
a command run from the page whose flags widen its own privileges each need an id for a scope.

1. The request is sent without `X-Confirm`. The server answers `428 confirm_required` with the scope in the header
   `X-Confirm-Scope`. Where the server knows what the action raises, `detail` is `{"scope": "...", "reasons": ["..."]}`, each
   reason one line that is shown whole (terminal controls and bidirectional characters appear as escapes). A command run from the
   page adds `argv` and `cmdline`; a prune adds the `plan`.
2. The client shows the person what is raised and asks. It obtains an id with `POST /api/confirm` and `{"scope": "<scope>"}`
   (1 to 120 printable ASCII characters), answered `{"id", "scope", "expires_in"}`. Several previews (a trust challenge, a restore
   preview, a dry run) carry the scope, and sometimes the id, in their answer instead.
3. The request is repeated with the header `X-Confirm: <id>`. An id is good once, for one minute, for exactly its scope, and
   only for the credential that asked for it. An id that is unknown, used, expired, from another session or for another scope is
   `403 confirm_invalid`, and it is spent by that attempt whether or not it fitted.

The scope names what was shown, so that the confirmation covers that and nothing else. Its parts after a name are a tab id, or
`<d16>`, the first 16 hex digits of the SHA-256 of a JSON encoding of the values the confirmation covers; a client takes the scope
from the answer that names it and does not compute it.

| Action | Scope |
|---|---|
| Start or resume a session that raises privilege above the server's own flags | `session:<d16>` |
| Restart a session, change a team's model or roles, with privileges the session lacks | `restart:<tab>:<d16>` |
| Mode `bypass` or `yolo` | `mode:<mode>:<tab>` |
| Add allow rules the session lacks | `rules:<tab>:<d16>` |
| Remove a deny or ask rule | `rules.remove:<tab>:<d16>` |
| Trust a project's files (`POST /api/trust`) | `trust:<d16 of directory and digest>` |
| Approve a project's tool server | `mcp.approve:<d16 of root, name and fingerprint>` |
| Add a schedule job; edit one | `job.add`; `job.edit:<job>` |
| Install an update | `update:<version>` |
| Prune with `apply`; delete recorded sessions | `prune:<d16 of the sorted ids>`; `delete:<d16 of the sorted ids>` |
| Restore a checkpoint; undo the restore | `restore:<tab>:<checkpoint>:<d16 of the plan>`; `restore.undo:<tab>` |
| Revert a hunk | `revert:<tab>:<d16 of file, hunk and range>` |
| Apply a team's verified work | `accept:<tab>:<d16 of what would be applied>` |
| A `/rewind <checkpoint>` line sent to a session | `restore:<tab>:<checkpoint>` |
| A command run from the page whose mode is `priv` | `run:<d16 of the argument vector>` |

**Trust challenge.** Starting or resuming a session with `trustProject`, for a project whose own files are not trusted, answers
`409 trust_required` with a `wire.TrustChallenge` as `detail`: the directory, the files that would take effect (path, kind, size,
hash; kind `unread` for what could not be read), the digest, what changed since an earlier yes, whether the listing is `partial`,
and `confirm` and `scope`, an id already issued for the scope. Repeating the request with `X-Confirm: <confirm>` trusts exactly
those files (recorded as `sleipnir trust add` records them; a partial listing is trusted for that session only). When the files
or the request changed in between, the answer is a new challenge. `GET /api/trust/challenge?dir=` and `POST /api/trust` work
the same way for the Settings page.

**A plan that changed.** A confirmation covers one preview. Where the client sends the `scope` of the preview it was shown (a
restore, a hunk revert, applying verified work), or the files, the diff or the list of sessions are no longer what was shown, the
answer is `409 changed` with the plan as it is now in `detail`, including its new `scope`; nothing is done, and the client shows
the new plan and asks again. A prune with `apply` answers the same way when its confirmation does not fit, with `detail` holding
the `plan` and the `scope`.

## The stream

`GET /api/stream` is the one event stream of a page: `text/event-stream`, one connection for every session (tab). The server
starts it with `retry: 3000`, sends the comment line `: ping` every 15 seconds while idle, and ends it when the credential is
revoked (logout, rotation) or the server stops. Each message is

```text
id: 1042
event: ev
data: {"tab":"t1","ev":{"t":41.237,"k":"tool","seq":318,"id":"be-2","name":"Edit"}}
```

`id` is the position in the server's history of frames, counting from 1 in each run of the server. At most 32 streams are open at
once (`503 too_many_streams`).

**Snapshot, then delta.** A client that joins late, or loses frames, rebuilds a session from a snapshot and then applies frames:

1. `GET /api/hello` gives `streamAfter`, the id of the last frame sent so far, and `boot`, which identifies the server's run.
2. Open `GET /api/stream?after=<streamAfter>` and keep the `ev` frames of each tab as they arrive.
3. `GET /api/sessions/{id}/snapshot` (or `GET /api/snapshot` for every tab) gives the state of a tab: its `meta`, `roster`, the
   `keyframe` (a short set of events that stands for what the journal no longer holds), the retained `events`, the lines sent
   before (`hist`), the open `questions`, the session time `now` in seconds, the generation `gen` and `seq`, the sequence number
   of the last event included.
4. Apply the buffered `ev` frames whose `ev.seq` is above the snapshot's `seq`, then the later ones as they arrive; an event
   whose `seq` is not above the last one applied is dropped.

Reconnecting sends `Last-Event-ID` (a browser's `EventSource` does it by itself; a script may pass `?after=` instead). The server
replays the frames after that id when it still has them: the last 20,000 frames or 32 MiB. Otherwise, or when the id is ahead of
the server because the server restarted, the first message is `gap` and the client fetches the snapshots again. After a lost
connection a client also asks `GET /api/hello` and starts over when `boot` is not the one it knew. A stream with no `after` and no
`Last-Event-ID` is live only.

**Delivery.** Each page has a bounded queue (2,048 frames or 8 MiB). When a page does not keep up, the server gives up frames in
this order and says so:

| Class | Frames | When the page falls behind |
|---|---|---|
| Critical | `tab`, `reset`, `roster`, `meta`, `bye`, a run's result, and the `ev` kinds marked below | never dropped. A page that cannot hold them is disconnected with `lagged` and reconnects with `Last-Event-ID` |
| Coalescable, with a key | `ping`; `ev` of kinds `use`, `layers`, `gov`, `warm`, `plan`, `verdict`, `queue`, `mailstat`, `diff`, and `ckpt` after its first event | a newer frame with the same type and key replaces it; otherwise it is dropped first |
| Ordinary | `recorded`, a run's output lines and steps, `ev` of kind `more` | dropped after the coalescable ones; the page is told with `gap` |

Nothing dropped is lost: the journal of the session holds it and a snapshot brings it back.

**Frames.** `data` is JSON.

| Frame | Data | Meaning |
|---|---|---|
| `ev` | `wire.EvFrame`: `{tab, ev}` | one event of a session (below) |
| `meta` | `wire.MetaFrame`: `{tab, patch}` | a change to the tab's settings and state (`wire.MetaPatch`: only the fields that changed) |
| `roster` | `wire.RosterFrame`: `{tab, roster}` | the agents of the tab, replacing the list |
| `tab` | `wire.TabFrame`: `{op, tab}` | a session was added, renamed or updated, or removed (`op` is `add`, `update`, `remove`) |
| `reset` | `wire.ResetFrame`: `{tab, gen}` | the tab started a new generation (restart, `/new`, `/clear`): fetch its snapshot |
| `recorded` | `{}` | the list of recorded sessions changed: fetch `GET /api/recorded` |
| `run` | `wire.RunFrame`: `{id, lines?, step?, verdict?, result?}` | output of a run started from the page: output lines, a doctor probe's steps and verdict, and the end |
| `ping` | `wire.Ping`: `{now: {tab: seconds}}` | every 15 seconds, the session time of each tab |
| `bye` | `wire.ReasonFrame`: `{reason}` | the server is stopping |
| `toast` | `wire.Toast`: `{tab?, text, kind?}` | a notice for the page to show; the server sends none at present |
| `gap` | `{reason, dropped, last}` | frames may have been missed: `reason` is `aged` (the id left the history), `ahead` (the server restarted) or `overflow` (`dropped` frames were given up); `last` is the id of the newest frame |
| `lagged` | `{after}` | the page was cut off for not holding critical frames; reconnect after `after` |
| `closed` | `{}` | the stream's topic ended (the server is stopping) |

`gap`, `lagged` and `closed` come from the stream itself. `GET /api/hello` is a route, not a frame.

**Events.** An `ev` frame carries one event of a session, a flat object whose head is `wire.Base`: `t`, the session time in
seconds (non-decreasing in `seq` order; history carried over from an earlier run is at `0` with its real time in `at`, epoch
milliseconds), `k`, the kind, and `seq`, the tab's sequence number, starting at 1 in each generation. The kinds and their fields
are the types of `internal/web/wire/events.go`, found by their `k`:

| Class | Kinds |
|---|---|
| Critical | `ask`, `answer`, `state`, `task`, `goal`, `final`, `turn`, `merge`, `mail`, `tool`, `say`, `stream`, `sys`, `note`, `refuse`, `interrupt`, `steer`, `break`, `compact`, `req`, `stall`, `handover`, `alert`, and the first `ckpt` of an id |
| Coalescable (key) | `use` and `layers` (per agent), `gov`, `warm`, `plan`, `verdict`, `queue`, `mailstat` (per tab), `diff` (per file), `ckpt` after the first of an id |
| Ordinary | `more` |

Kinds and fields that the server adds to the base vocabulary are in `internal/web/translate/events.go`: the kinds `alert` and
`mailstat`, and extra fields on `state`, `task`, `mail`, `use` and `tool`.

## Routes

Every route needs the token or a session cookie, except `GET /healthz`. The last column says whether the route needs a
confirmation: `-` never, otherwise the scope (see [Confirmations](#confirmations)). A route that is confirmed only when the
request raises something says so. Bodies and answers are the `wire` types named; "answers `{...}`" is a plain object.

### Server

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/` | The page and its assets, embedded in the binary. With `?token=` (the run token or a launch code) it sets the session cookie and redirects to `/`. Without a credential a browser gets a plain sign-in page (`401`) | - |
| `GET` | `/api/` | Any other path under `/api/`: `404 not_found`, or `405` when the path has routes for other methods | - |
| `GET` | `/healthz` | Answers `{"ok": true}` and nothing else; needs no credential | - |
| `GET` | `/api/ping` | Answers `{"ok": true}` to a caller with a valid credential | - |
| `POST` | `/api/confirm` | Issues a confirmation id for `{"scope"}` (`429` when too many are outstanding) | - |
| `POST` | `/api/auth/logout` | Ends the caller's session and clears the cookie | - |
| `POST` | `/api/auth/rotate` | Ends every session and the token, and gives the calling browser a fresh session; a bearer caller is refused (`403 forbidden`) | - |

### Stream and boot

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/stream` | The event stream (above). `after=` or `Last-Event-ID` resumes | - |
| `GET` | `/api/hello` | `wire.Hello` and more: `boot`, `now` (epoch ms), `tabs`, `active`, `server` (`addr`, `loopback`, `version`), `limits` (`maxBody`, `maxMessage`, `maxQuestions`), `ui` (build, `reduceMotion`, `bell`), `streamAfter`, `defaults` (the New session dialog's values and `maxWorkers`) and `update` (a newer release, when one is known) | - |
| `GET` | `/api/snapshot` | `{streamAfter, active, tabs: [wire.TabSnapshot]}`: every tab's snapshot | - |

### Sessions (tabs)

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/sessions` | `{tabs: [wire.TabSummary]}`, in strip order | - |
| `GET` | `/api/projects` | `{projects: [wire.Project]}`: the directories a new session may start in (`--cwd`, `--project`, the open and recorded sessions' directories, the trust ledger), each with its trust state | - |
| `POST` | `/api/sessions` | Starts a session: `wire.NewSessionRequest` and optional `resume` (a recorded session's id) and `clientId`; answers `201 {tab}`. `403 not_a_project` for a directory that is not listed, `409 limit` at 16 sessions, `409 hosted` when the recorded session is open | When it raises privilege: `session:<d16>`; project files: the trust challenge |
| `POST` | `/api/sessions/resume` | Continues a recorded session (`wire.ResumeRequest`: `from` is an id or `latest`) in a new tab; `201 {tab}` | As above |
| `GET` | `/api/sessions/{id}/snapshot` | `wire.TabSnapshot` | - |
| `PATCH` | `/api/sessions/{id}` | Renames: `{name}`; answers `{tab}` | - |
| `POST` | `/api/sessions/{id}/stop` | Interrupts the running turn; `409 idle` when none runs | - |
| `DELETE` | `/api/sessions/{id}` | Closes the session; an isolated team's verified work is applied to the checkout first (`integration` in the answer). `409 last` for the only session | - |
| `POST` | `/api/sessions/{id}/restart` | `wire.RestartRequest`: `kind` is `new`, `clear`, `swarm`, `restart`, `model` or `roles`; `flags` are what a person would type after `/restart` and win over the flags that `PATCH .../launch` staged; `new` and `clear` always start the chat empty, the others do so only with `fresh`. Answers `202 {gen}`; the tab starts its next generation in the background | When the final arguments raise privilege: `restart:<tab>:<d16>` |

### Chat

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `POST` | `/api/sessions/{id}/messages` | Sends a line (`wire.MessageRequest`: `text` reaches the agent, `display` is what the transcript shows) up to 256 KiB; `wire.SendResult` says whether it was queued behind the running turn | - |
| `POST` | `/api/sessions/{id}/command` | Runs a slash line the page has no handler for (`wire.CommandRequest`); `wire.CommandResult` has the text to show and whether a prompt was sent. `/mode`, `/allow`, `/model`, `/roles`, `/restart`, `/swarm`, `/resume` and `/rewind` act as the routes they stand for | As that route |
| `POST` | `/api/sessions/{id}/steer` | `{text}`: guidance for the manager | - |
| `POST` | `/api/sessions/{id}/interrupt` | `{target}`: interrupts the turn; the goal pauses and the manager's open questions are refused | - |
| `GET` | `/api/sessions/{id}/slash` | `{slash: [wire.SlashEntry]}`: the commands of the `/` menu, custom commands included | - |
| `GET` | `/api/sessions/{id}/complete` | `?prefix=&limit=`: project paths that contain `prefix` (50 at most), for `@` completion; paths agents may not read are left out | - |
| `POST` | `/api/sessions/{id}/permissions/check` | The "would it ask?" tester: `wire.PermCheckRequest` answered with `wire.PermVerdict`; asks nobody | - |

### Questions

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/questions` | `{questions: [wire.OpenQuestion]}`: the open permission questions of every tab | - |
| `POST` | `/api/questions/{qid}/answer` | `wire.AnswerRequest`: `choice` 1 yes, 2 yes and remember for the session (offered only for a trust or tool-server question, or when the question's `rule` is not empty: that is the engine's exact rule text, joined by ", "; otherwise `400 bad_choice`), 3 no (with `note`, which the agent reads), 4 yes and allow builds and tests (offered when `offersTests`). `wire.AnswerResult` has the rule that was added. A question shows the whole request (a command up to 100,000 bytes, a change up to 256 KiB, an MCP call's whole arguments) or is not asked: the agent is told why it was refused. `409 too_soon` within 350 ms of the question or of the tab's previous answer, `409 answered` | - |

### Goal and session settings

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `POST` | `/api/sessions/{id}/goal` | `wire.GoalRequest`: `action` is `set` (with `text`), `pause`, `resume` or `clear`; answers `{ok, state}` | - |
| `POST` | `/api/sessions/{id}/mode` | `wire.ModeRequest`: `default`, `accept-edits`, `plan`, `bypass`, `yolo` | `mode:<mode>:<tab>` for `bypass` and `yolo` |
| `POST` | `/api/sessions/{id}/model` | `wire.ModelRequest`: the manager's model, or a role's; a team starts again on it. Answers `{ok, restarted}`; `422 model` for a model that cannot be used | Through the restart, when it raises |
| `POST` | `/api/sessions/{id}/effort` | `wire.EffortRequest`; `wire.EffortResult` has the level the model applies | - |
| `POST` | `/api/sessions/{id}/budget` | `wire.BudgetRequest`: `usd`, or `off`; a team's budget applies when it starts again. Answers `{ok, paused}`, `paused` being whether the goal paused because spending is already above the new limit | - |
| `POST` | `/api/sessions/{id}/compact` | Folds the thread now (`{focus}`); answers `{from, to}` in tokens | - |
| `PATCH` | `/api/sessions/{id}/launch` | `wire.LaunchPatch`: flags (`isolation`, `verify`, `commit`, `mailman`, `noMcp`, `trustProject`) staged for the next start of the team | - (checked at the restart that applies them) |
| `GET` | `/api/sessions/{id}/rules` | `{rules: [wire.Rule]}`: the rules in force with their origin | - |
| `POST` | `/api/sessions/{id}/rules` | Adds session rules (`wire.RuleRequest`; `tests` stands for the build and test preset); `wire.RuleResult` | `rules:<tab>:<d16>` for allow rules the session lacks |
| `POST` | `/api/sessions/{id}/rules/remove` | Removes a session rule; built-in and file rules are `409 fixed` | `rules.remove:<tab>:<d16>` for a deny or ask rule |

### Workspace

All of these resolve project-relative paths under the session's root and never serve `.git`, the session's state directory or a
path the permission engine refuses agents to read. A change (revert, restore, accept) waits for the running turn to end (`409 busy`).

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/sessions/{id}/ws/index` | `wire.WsIndex`: the checkpoints with their change sets, the tree at the live edge, the latest restore, reverts and reviewed marks. The `ETag` is its version; `If-None-Match` gets `304` | - |
| `GET` | `/api/sessions/{id}/ws/file` | `?path=&at=`: `wire.WsContent`, the file's text at a checkpoint (live by default) with who wrote each line; 2 MiB of text at most | - |
| `GET` | `/api/sessions/{id}/ws/diff` | `?path=&from=&to=`: `wire.WsDiff` in hunks (from `base` to `live` by default); each hunk carries the `scope` of reverting it | - |
| `POST` | `/api/sessions/{id}/ws/revert` | `wire.RevertRequest`: puts a hunk's old lines back, records it as the person's write and tells the agent that wrote the file. `409 changed` with the new diff when the hunk or file changed | `revert:<tab>:<d16>` |
| `POST` | `/api/sessions/{id}/ws/revert/{rid}/undo` | Brings the reverted lines back while the file has not changed since | - |
| `POST` | `/api/sessions/{id}/ws/restore` | `wire.RestoreRequest`: `dryRun` previews `/rewind` (`wire.RestorePlan` with its `scope`); otherwise restores, after a safety checkpoint. Refused whole when a file was edited after the agents' last write (`409 conflict`) | `restore:<tab>:<checkpoint>:<d16>`; none for `dryRun` |
| `POST` | `/api/sessions/{id}/ws/restore/undo` | Undoes the latest restore from its safety checkpoint, when no file changed since | `restore.undo:<tab>` |
| `PUT` | `/api/sessions/{id}/ws/reviewed` | `wire.ReviewedRequest`: marks or unmarks a file as reviewed at a checkpoint | - |
| `GET` | `/api/sessions/{id}/ws/worktrees` | `[wire.Worktree]`: the worker trees of an isolated team | - |
| `GET` | `/api/sessions/{id}/ws/queue` | `wire.MergeQueueStatus`: the merge queue now | - |
| `GET` | `/api/sessions/{id}/ws/verify/{task}` | `wire.VerifyOutput`: every run of the task's verification gate, with the tail of its output | - |
| `POST` | `/api/sessions/{id}/ws/accept` | `wire.AcceptRequest`: applies the verified, merged work to the checkout as commits (`mode` `commits`, the default) or uncommitted edits (`edits`); `dryRun` reports what would happen (`wire.AcceptResult` with its `scope`). `409 changed` with the new result | `accept:<tab>:<d16>`; none for `dryRun` |

### Settings pages

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/models` | `wire.ModelsView`: the catalogue (cached for 6 hours; `refresh=1` asks every provider again) with favourites, roles and efforts. Query: `q`, `tools=1`, `reasoning=1`, `fav=1`, `max_price`, `min_context`, `all=1`, `tab` | - |
| `POST` | `/api/models/fav` | `wire.FavRequest`: stars or unstars a model in the user's configuration | - |
| `GET` | `/api/sessions/{id}/permissions` | `wire.PermissionsView`: the mode, the order rules are judged in, and the rules in force with their origin | - |
| `GET` | `/api/sessions/{id}/trust` | `wire.TrustView`: the tab's project files and the trust ledger with each directory's state now | - |
| `GET` | `/api/trust/challenge` | `?dir=`: `wire.TrustChallenge` for a project of the server, with a confirmation id for its scope. `409 nothing` when there is nothing to trust | - |
| `POST` | `/api/trust` | `wire.TrustRequest`: `on` true trusts the files of the challenge; `on` false forgets a directory; `all` with `on` false forgets every directory. `409 changed` with a new challenge when the files changed | `trust:<d16>` to trust |
| `GET` | `/api/sessions/{id}/mcp` | `wire.MCPView`: every configured tool server, where it came from, whether it may start, its state and tools; environment and header values are withheld | - |
| `POST` | `/api/sessions/{id}/mcp/{name}/approve` | Remembers a project's tool server entry as it is now; it applies when the team starts again. `wire.MCPResult` | `mcp.approve:<d16>` |
| `POST` | `/api/sessions/{id}/mcp/{name}/revoke` | Forgets the approval of a project entry | - |
| `POST` | `/api/sessions/{id}/mcp/{name}/test` | Starts the server alone, lists its tools, stops it; `409 needs_approval` for an entry not approved | - |
| `POST` | `/api/sessions/{id}/mcp/{name}/reconnect` | Asks a server of the running session that gave up to start again | - |
| `GET` | `/api/sessions/{id}/skills` | `wire.SkillsView`: skills, custom commands and hooks with the layer each came from | - |
| `GET` | `/api/providers` | `wire.ProvidersView`: providers and whether a key is there; a key is never returned | - |
| `POST` | `/api/providers/recheck` | Takes up keys that `sleipnir login` or `logout` changed in a terminal, and lists the providers again | - |
| `POST` | `/api/providers/{name}/signout` | Removes the stored key; a key from the environment is `409 env` | - |
| `GET` | `/api/sessions/{id}/config` | `wire.ConfigView`: the configuration layers, every effective value (redacted) with the layer that supplied it, and the warnings of `sleipnir config` | - |

The page never holds a provider key: signing in is `sleipnir login` in a terminal.

### Recorded sessions

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/recorded` | The session directories no tab hosts, newest first (`wire.RecordedSession`), and their size | - |
| `POST` | `/api/recorded/prune` | `wire.PruneRequest`: the plan of `sleipnir sessions prune`, or with `apply` its deletion. Sessions open in a tab or locked by a process are kept | `prune:<d16>` for `apply` |
| `POST` | `/api/recorded/delete` | `wire.DeleteRecordedRequest`: deletes the named sessions (1,000 at most), with prune's safety; `409 hosted` or `409 locked` for one in use | `delete:<d16>` |
| `GET` | `/api/recorded/{sid}/events` | `?from=&limit=`: a recorded session's history as events, a page at a time; `next` is where the next page starts | - |
| `POST` | `/api/recorded/{sid}/watch` | Follows a session another process is writing, read-only, in a tab of its own: `201 {tab}`, or `200` when already followed | - |
| `DELETE` | `/api/recorded/{sid}/watch` | Stops following it and removes the tab | - |
| `GET` | `/api/recorded/watching` | `{tabs}`: the tabs that follow a session | - |

### Schedule

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/schedule` | `wire.ScheduleView`: the jobs, the daemon and the last logs of each job | - |
| `GET` | `/api/schedule/next` | `?cron=`: `wire.CronCheck`, whether the expression is valid and when it next runs | - |
| `POST` | `/api/schedule/jobs` | `wire.JobRequest`: adds a job; `bypass` and `yolo` are refused | `job.add` |
| `PUT` | `/api/schedule/jobs/{job}` | Edits a job's cron, goal, directory, model, mode and budget | `job.edit:<job>` |
| `POST` | `/api/schedule/jobs/{job}/pause` | `{paused}`: a paused job is skipped by the daemon and can still be run by hand | - |
| `DELETE` | `/api/schedule/jobs/{job}` | Removes the job | - |
| `POST` | `/api/schedule/jobs/{job}/run` | Runs the job now as the daemon would, its output as `run` frames and in its log; answers `202 wire.RunStarted`. `403 dangerous_mode` for a bypass or yolo job, `409 running` | - |
| `GET` | `/api/schedule/jobs/{job}/log` | The job's newest log, its end (256 KiB at most) | - |
| `POST` | `/api/schedule/daemon` | `{action, every}`: `start` the daemon in this process (it stops with the server), `stop`, or `once` to run what is due. `409 external` for a daemon of another process | - |

### Command runner

A run is a child process of this program, `sleipnir <command> <flags>`, never through a shell, with no provider key in its
arguments or environment (commands that call a provider receive the held keys on an inherited pipe). The specification says how
each command runs: `run` and `net` commands run, `priv` commands need a confirmation, `server` commands run until stopped, and
`tty_only` commands are refused (`403 tty_only`) with the page's equivalent named. At most four runs go at once and a run ends
after an hour, servers excepted. Output arrives as `run` frames of lines of kind `out` (standard output) and `err` (standard
error).

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/cli` | `wire.CLISpec`: every command with its flags, positionals and mode, generated from `docs/CLI.md` | - |
| `POST` | `/api/runs` | `wire.RunRequest` and `keep` (let the run continue when the page leaves): starts a run, `202 wire.RunStarted` | `run:<d16>` for a `priv` command, including any whose flags widen its privileges |
| `GET` | `/api/runs` | `{runs: [wire.RunInfo]}`: running and recent runs | - |
| `DELETE` | `/api/runs/{run}` | Stops a run | - |
| `GET` | `/api/runs/{run}/output` | `?from=`: the lines a run kept, from a line number on (20,000 lines of 4 KiB at most) | - |

### Doctor and update

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/doctor/endpoints` | `[wire.DoctorEndpoint]`: the models the configuration names, where each is served and which variable holds its key | - |
| `POST` | `/api/doctor` | `{model?, baseUrl?, deep}`: starts a probe of an endpoint; `202 wire.RunStarted`, then `run` frames with steps and a verdict | - |
| `GET` | `/api/update` | `wire.UpdateStatus`: the version and the latest release (`502 network` when it cannot be asked) | - |
| `POST` | `/api/update/install` | Runs `sleipnir update` in this process, its output as `run` frames; `409 none` when nothing newer exists or the build is from source | `update:<version>` |
