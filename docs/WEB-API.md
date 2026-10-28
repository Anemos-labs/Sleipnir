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
Request headers are capped at 16 KiB. Every API response is `no-store`; the page's own files (`GET /` and its assets) are
`no-cache` with an `ETag`, so that a browser asks again on every load and gets `304` when nothing changed.

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
| 400 | `bad_json`, `bad_request`, `bad_research`, `bad_export`, `bad_scan`, `bad_finding`, `bad_url`, `bad_depth`, `bad_path`, `bad_flags`, `bad_mode`, `bad_rule`, `bad_level`, `bad_budget`, `bad_cron`, `bad_age`, `bad_choice`, `bad_tool`, `bad_name`, `bad_key`, `bad_isolation`, `bad_layout`, `not_a_dir`, `empty` |
| 401 | `unauthenticated` |
| 403 | `bad_host`, `forbidden_site`, `forbidden_origin`, `csrf`, `forbidden`, `confirm_invalid`, `not_a_project`, `denied`, `tty_only`, `dangerous_mode`, `unreadable` |
| 404 | `not_found`, `no_session`, `no_question`, `no_file`, `no_checkpoint`, `no_rule`, `no_dir`, `unknown_command`, `no_scan`, `bad_format` |
| 405, 413, 415 | `method_not_allowed`, `body_too_large`, `unsupported_media_type` |
| 409 | `answered`, `no_session`, `plugin_off`, `too_soon`, `busy`, `idle`, `last`, `limit`, `locked`, `hosted`, `not_resumable`, `budget_configured`, `trust_required`, `changed`, `conflict`, `nothing`, `fixed`, `not_isolated`, `needs_approval`, `external`, `running`, `env`, `dirty`, `moved`, `no_goal`, `not_paused`, `not_active`, `none`, `git_init`, `mkdir` |
| 422 | `model`, `rejected`, `conflict` |
| 428 | `confirm_required` |
| 429, 500, 502, 503, 504 | `rate_limited`; `internal`; `network`, `issue_failed`, `clone_failed`; `busy`, `too_many_streams`, `shutting_down`; `slow` |

**Retries.** `POST /api/sessions`, `POST /api/sessions/resume` and `POST /api/sessions/{id}/messages` accept an optional
`clientId` (64 characters at most): a repeat with the same id within 60 seconds returns the first answer instead of doing it
again. A repeat that arrives while the first is still being handled waits for it and returns its answer; when the first did not
succeed, nothing is kept and the repeat acts in its place.

**Identifiers in paths** are matched before anything is looked up, and a mismatch is `400 bad_request`: `{id}` a session (tab)
id, `[a-z0-9-]{1,40}`; `{sid}` a recorded session, `YYYYMMDD-HHMMSS-xxxxxx`; `{qid}` a question, `q_` and 26 base32 characters;
`{run}` a run; `{rid}` a revert; `{job}` a schedule job; `{name}` a tool server or provider.

## Confirmations

An action that raises privilege needs a second step. A bypass or yolo mode, trusting a project's own files, an allow rule the
session does not have, a `--verify` command, taking back a deny or ask rule, approving a tool server, adding or editing a
schedule job, installing an update, locating or installing a program a plugin needs, deleting recorded sessions, restoring or reverting files, applying a team's verified work, and
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
| Locate a program a plugin needs; run one of its install methods | `need.locate:<d16 of the need and path>`; `need.install:<d16 of the need, method, command and server entry>` |
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
   of the last event included, and `metaV` and `rosterV`, the versions of the `meta` and `roster` frames its `meta` and `roster`
   already hold.
4. Apply the buffered `ev` frames whose `ev.seq` is above the snapshot's `seq`, then the later ones as they arrive; an event
   whose `seq` is not above the last one applied is dropped. `meta` and `roster` frames carry a version `v` per tab (1, 2, ...,
   across generations): a frame whose `v` is not above the snapshot's `metaV` (`rosterV`) is already in it, and a snapshot whose
   `metaV` (`rosterV`) is below the version of a frame already applied is older than that frame for its `meta` (`roster`).

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
| Coalescable, with a key | `ping`; `ev` of kinds `use`, `layers`, `gov`, `warm`, `plan`, `verdict`, `queue`, `mailstat`, `svc`, `diff`, and `ckpt` after its first event | a newer frame with the same type and key replaces it; otherwise it is dropped first |
| Ordinary | `recorded`, a run's output lines and steps, `ev` of kind `more` | dropped after the coalescable ones; the page is told with `gap` |

Nothing dropped is lost: the journal of the session holds it and a snapshot brings it back.

**Frames.** `data` is JSON.

| Frame | Data | Meaning |
|---|---|---|
| `ev` | `wire.EvFrame`: `{tab, ev}` | one event of a session (below) |
| `meta` | `wire.MetaFrame` and its version: `{tab, patch, v}` | a change to the tab's settings and state (`wire.MetaPatch`: only the fields that changed; `jobs` lists the background jobs, `wire.Job`, with the local addresses they serve, and `/jobs stop ID` through `POST .../command` ends one); `v` counts the tab's `meta` frames, which reach a page in its order |
| `roster` | `wire.RosterFrame` and its version: `{tab, roster, v}` | the agents of the tab, replacing the list; `v` counts the tab's `roster` frames |
| `tab` | `wire.TabFrame`: `{op, tab}` | a session was added, renamed or updated, or removed (`op` is `add`, `update`, `remove`) |
| `reset` | `wire.ResetFrame`: `{tab, gen}` | the tab started a new generation (restart, `/new`, `/clear`): fetch its snapshot |
| `recorded` | `{}` | the list of recorded sessions changed: fetch `GET /api/recorded` |
| `run` | `wire.RunFrame`: `{id, lines?, step?, verdict?, result?}` | output of a run started from the page: output lines, a doctor probe's steps and verdict, and the end |
| `ping` | `wire.Ping`: `{now: {tab: seconds}}` | every 15 seconds, the session time of each tab |
| `bye` | `wire.ReasonFrame`: `{reason}` | the server is stopping: the last frame of every stream, delivered after what the stream holds (for half a second at most); the stream then ends |
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
| Coalescable (key) | `use` and `layers` (per agent), `gov`, `warm`, `plan`, `verdict`, `queue`, `mailstat` and `svc` (per tab), `diff` (per file), `ckpt` after the first of an id |
| Ordinary | `more` |

Kinds and fields that the server adds to the base vocabulary are in `internal/web/translate/events.go`: the kinds `alert`,
`mailstat`, `svc` and `tally`, and extra fields on `state`, `task`, `mail`, `use`, `tool`, `compact` and `req`. A `compact` event says in
`moment` whether the shared prefix's cache was `warm` (a declared, priced rebase) or `cold` (the rewrite costs nothing extra) when
the compaction was decided, and has none for a compaction that no plan preceded (an emergency compaction, or one a person asked
for): nobody chose its moment and the log does not tell its price, so a page states none for it. `mode` says how the thread was
folded, in the terminal's words: `fork` (the compactor model's patch), `mask` (bulky results folded without a model) or `emergency`
(the harness's own safety net); a page shows a compaction with no `moment` as, say, "emergency compaction", as the terminal's feed
does. A `break` event carries `layer`, the layer of the prompt that changed (none for the endpoint's own miss), `missUsd`, what
the miss cost at list price when the price is known, and `quiet` when the terminal's chat would not say it without `/verbose`: the
endpoint's own miss that cost less than a cent, or one after the first three that did (`state.BreakNotices`; one `sys` row says that
further ones are not said). A page keeps a quiet break for its cache view and leaves it out of its feed (unless `/verbose` is on), its
cards and its counts. A `req` event carries `mark` when the agent took a new shared prefix (`epoch`) or dropped its thinking blocks (`rebase`) before the request it reports, the places the terminal marks
on an agent's hit-ratio line. An `svc` event carries, as absolute values, the
tokens, cost and estimated saving of the harness's own service agents (the mailman) together; they are in no roster and have no
`use` event, and a page adds them to the totals of its agents so that the totals are those of the run. A `use` or `svc` event
whose usage a ChatGPT plan covered carries `plan: true` and `list`, that usage at the API list price (its `cost` is what was
billed: nothing); a page shows `cost` plus `list` with a "≈" and a `plan` tag, "at API list price; billed to your ChatGPT
plan", and fills a budget gauge with `cost` only. A recorded session (`GET /api/recorded`) carries the same two fields from its
`session.end`; when its last run wrote none (it is still running, or its process died), the figures of the responses after the last
`session.end` are added, as the terminal's state sums them. Its `agents` are the manager and the workers that wrote to the log (the
harness's own writers and its mailman are not counted), and `mailman` says that the last run had the mailman mode on, which a replay
of it shows.

A keyframe carries only the newest mails (400), cache breaks and compactions (100 each). A `tally` event, sent only in a keyframe,
says how many of each came before the ones it carries: `breaks`, `loud` (the breaks among them without `quiet`), `compacts` and
`mails`, numbers to add to what a page counts as it reduces the events, never totals. A page that counts every `break`, `compact`
and `mail` it reduces and adds a tally's numbers has the run's counts whatever the journal kept; a list a page keeps is bounded too,
and its length is never shown as a count.

Events that have a line in the terminal's feed and no kind of their own become `sys` rows of the manager's channel, with `ag` the
agent they are about, in the words of that line (the translator reads them from the same state the terminal folds, so the two
cannot say different things; the line's small print follows its text after a middle dot): a compaction that was rejected, the
shared prefix re-written (a new epoch), a recovered panic of an agent's output sink, the manager's supervision (a hold, a run that
ended with work unfinished, a wake, the bound on wakes, a worker whose mail no longer wakes it, a shutdown with agents still
running) and a background job that finished, was killed or exited with an error. A merge that was rolled back after its
verification failed is a `sys` row of its own after the row of the failure. These rows are held to the rate limit of notices (20 in
10 seconds, the rest counted in one row). While a compaction is worked out, from the planner's decision to its commit or rejection,
the agent's `state` event says it folds its thread.

In a hosted session three of these events are also given to the session's sink as a notice by the swarm, with the same words and more
(a feed line cuts its text at 160 characters and its small print at 80): a wake (the note the manager is woken with), the bound on
wakes (how many, and what the manager waits for) and a run that ended with work unfinished (the whole list). The page shows the
notice, in the manager's channel, in the place of the line, so that the fact is one row; the notice is matched to its event by the
words the swarm computes from the event (`swarm.WakePausedNotice`, `swarm.UnfinishedNotice`), whichever of the two arrives first. A
line whose notice has not come after 2 seconds is shown after all, and so is every line of a log that no sink goes with (a followed or
replayed one). Every other notice is a row of its own, as it is given.

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
| `GET` | `/api/hello` | `wire.Hello` and more: `boot`, `now` (epoch ms), `tabs`, `active`, `server` (`addr`, `loopback`, `version`, `label`: the version as the top bar shows it, `build`: version, commit, toolchain and platform), `limits` (`maxBody`, `maxMessage`, `maxQuestions`), `ui` (build, `reduceMotion`, `bell`), `streamAfter`, `defaults` (the New session dialog's values, `maxWorkers`, `testsPreset`, what the allow rule `tests` lets through, and `isolationSet`, whether `--isolation` or `swarm.isolation` named the isolation) and `update` (a newer release, when one is known) | - |
| `GET` | `/api/snapshot` | `{streamAfter, active, tabs: [wire.TabSnapshot]}`: every tab's snapshot | - |

### Sessions (tabs)

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/sessions` | `{tabs: [wire.TabSummary]}`, in strip order | - |
| `GET` | `/api/projects` | `{projects: [wire.Project]}`: the directories a new session may start in (`--cwd`, `--project`, the open and recorded sessions' directories, the trust ledger), each with its trust state | - |
| `GET` | `/api/fs/dirs` | `wire.DirListing` for the query `path`: the subfolders that complete the path typed (`entries`, at most 60, `more` when there are others; the folders whose names start with the last part of the path, hidden ones only when that part starts with a dot) and `state`, what the path is: its absolute form (`~` is the home folder, a relative path is read from the folder the server was started in), whether it exists, is a folder, can be read, is one of the projects, is empty, is a git repository (`repo`), is inside one (`inside`, with `gitRoot`), is a bare repository (`bare`) or neither, and the checks its files suggest for `--verify`. For a repository's root or a bare repository, `repo` says what git says of it (`bare`, `hasCommits`, `branch`, `detached`, `clean`) when git answers within 2 s; the New session dialog disables worktree isolation and `--commit` from it. Only folder names are read, and only those that are safe to show; `400 bad_request` for an empty path, `400 bad_path` for one that cannot be read as a path, `504 slow` when the folder does not answer, `429 busy` while four listings are in flight | - |
| `POST` | `/api/sessions` | Starts a session: `goalText` is limited to 32,000 Unicode code points after trimming; `400 bad_goal` rejects an oversized goal before creating a folder or tab. `wire.NewSessionRequest` and optional `resume` (a recorded session's id) and `clientId`; answers `201 {tab}`. `cwd` is the absolute path of a folder that exists (`400 bad_path` for another path, `400 not_a_dir` for a file, `404 no_dir`), of a project or of any other; `createDir` makes the last part of the path when it does not exist and its parent does; `gitInit` makes a new or empty folder, outside any repository, a git repository with an empty first commit (`409 git_init`). A folder that is not a project, a folder to make and a repository to make are each a reason of the confirmation, and nothing is made until it is given. `budget` in US dollars: absent leaves the session to the configuration, `0` is no limit (a team then has none, not even the built-in cap the terminal applies where nobody chose a budget). The `defaults` of `GET /api/hello` carry a `budget` only when `sleipnir web --budget-usd` or `swarm.budget_usd` of the configuration set one, so that the dialog starts at off. `409 limit` at 16 sessions, `409 hosted` when the recorded session is open, `409 locked` when another process uses it. A team whose isolation is chosen by nobody (the request, `sleipnir web --isolation`, `swarm.isolation`) in the root of a repository with a commit, or in a folder that `gitInit` makes one, starts with worktree isolation, and the `defaults` of `GET /api/hello` say `worktree` for the server's folder on the same terms. A request that names no `model` where nothing configures one (`sleipnir web --model`, `models.default`, `SLEIPNIR_MODEL`) is `422 model` before a tab opens. `managerApproves: false` starts the team with `--manager-approves=false`. `teamName`, the saved team that `team` was loaded from and still is, starts the team with `--team NAME` in place of the `--role-model` values. `compactAt` (10 to 90) starts the session with `--compact-at`; absent or `0` leaves the configuration's share, and another value is `400 bad_flags`. The `defaults` of `GET /api/hello` carry `compactAt` when `sleipnir web --compact-at` set one, and `compactAtDefault`, the share a session takes without the flag (`cache.compact_at_percent`, else 25; `0` where the configuration's token limits apply). A request to a tab whose session is still starting (a goal, a command, the effort) waits for the start, up to 3 minutes, instead of being refused; `409 no_session` when the start failed. Isolation settings the session would refuse as it starts (`commit` without worktree isolation, isolation for a single agent, worktree isolation outside a repository's root with a commit, `commit` on a detached HEAD or a checkout with changes) are `409 isolation` with the session's own reason, before the confirmation and before a tab opens; a folder that `gitInit` makes a repository passes the checks of the repository | When it raises privilege: `session:<d16>`; project files: the trust challenge |
| `POST` | `/api/sessions/resume` | Continues a recorded session (`wire.ResumeRequest`: `from` is an id or `latest`) in a new tab; `201 {tab}` | As above |
| `GET` | `/api/sessions/{id}/snapshot` | `wire.TabSnapshot` | - |
| `PATCH` | `/api/sessions/{id}` | Renames: `{name}`; answers `{tab}` | - |
| `POST` | `/api/sessions/{id}/stop` | Interrupts the running turn; `409 idle` when none runs | - |
| `DELETE` | `/api/sessions/{id}` | Closes the session; an isolated team's verified work is applied to the checkout first (`integration` in the answer). `409 last` for the only session | - |
| `POST` | `/api/sessions/{id}/restart` | `wire.RestartRequest`: `kind` is `new`, `clear`, `swarm`, `restart`, `model` or `roles` (with `roleModels`, `team`, or both); `flags` are what a person would type after `/restart` and win over the flags that `PATCH .../launch` staged (a `--cwd` other than the tab's own names a folder that exists, else `404 no_dir`, and one that is not a listed project is a reason of the confirmation; `--resume` takes a session id or `latest`, else `400 bad_flags`; a session to resume that is not recorded is `404 no_session`, one that cannot be resumed `409 not_resumable`, one another tab holds `409 hosted` with `detail.tab`, one another process uses `409 locked`: nothing is closed then); `new` and `clear` always start the chat empty, the others do so only with `fresh`. Answers `202 {gen}`; the tab starts its next generation in the background | When the final arguments raise privilege: `restart:<tab>:<d16>` |

### Chat

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `POST` | `/api/sessions/{id}/messages` | Sends a line (`wire.MessageRequest` (`autoFix` and `autoMerge` authorize GitHub delivery for this objective only): `text` reaches the agent, `display` is what the transcript shows) up to 256 KiB; `wire.SendResult` says whether it was queued behind the running turn; `409 no_session`, with the reason, when the tab's session did not start (nothing would run the line) | - |
| `POST` | `/api/sessions/{id}/command` | Runs a slash line the page has no handler for (`wire.CommandRequest`); `wire.CommandResult` has the text to show and whether a prompt was sent. `/mode`, `/allow`, `/model`, `/roles`, `/restart`, `/swarm`, `/resume` and `/rewind` act as the routes they stand for | As that route |
| `POST` | `/api/sessions/{id}/steer` | `{text}`: guidance for the manager | - |
| `DELETE` | `/api/sessions/{id}/queue/{qid}` | Removes a line that waits for the turn (`meta.queued`, whose `kind` is `message`, `command` or `action`) and answers `wire.QueuedDrop` (`id`, `text`: the whole line, `kind`), so the page can put the text back in the message box. An action (a goal change) stays: `409 not_droppable`; a line that ran or was removed is `404 not_queued` | - |
| `POST` | `/api/sessions/{id}/queue/{qid}/now` | Takes a queued message out of the queue and delivers it now: to the running turn as a steer, read with the agent's next step (`steered: true`), or, when no turn runs, as the next thing that runs. Answers `wire.QueuedDrop`. A slash line is `409 not_message`; a steer the turn refuses puts the line back where it was | - |
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
| `POST` | `/api/sessions/{id}/goal` | `wire.GoalRequest` (`autoFix` and `autoMerge` apply only to `set`, for that objective): `action` is `set` (with `text`), `pause`, `resume` or `clear`; answers `{ok, state, queued}`: `queued` says that the action waits to run (`set` and `resume` run as the tab's next item; `pause` and `clear` while a turn stops), and `state` is then the goal's state before it | - |
| `GET` | `/api/sessions/{id}/research` | The project's deep research runs, newest first (`wire.ResearchRun`: its phase, evidence counts, whether a build matches the ledger, whether a goal drives it now), the depths a run can have (`wire.ResearchDepth`) and `on`, whether the research plugin is on for the session | - |
| `POST` | `/api/sessions/{id}/research` | `wire.ResearchRequest`: `action` `start` (with `question` and `depth`: the run's goal is set as `/research` sets it, answering `{ok, queued}`) or `export` (with `slug` and `dir`, a directory inside the project: the wiki is rendered there from the ledger, answering `{ok, dir}`); `400 bad_research`, `400 bad_export`, `409 plugin_off` | - |
| `GET` | `/research/{id}/{slug}/{file...}` | A file of a run's wiki (`index.html` when none is named), rendered by the server from the run's ledger as it is now: pages, the stylesheet, the script, the search index and the fonts. Nothing is read from the project as it is, so the page's origin serves only what the renderer escaped; `404 not_found` for another file | - |
| `GET` | `/api/sessions/{id}/scan` | The project's deep scans, newest first (`wire.ScanRun`: its phase, the inventory and shard counts, leads by status and confirmed findings by severity, the team it asks for, whether the session works on it now), the depths a scan can have (`wire.ScanDepth`), `on`, whether the scan plugin is on for the session, and `sandbox`, whether the session has one to reproduce leads in | - |
| `GET` | `/api/sessions/{id}/scan/{slug}` | One scan in full: its numbers and what it still needs, its findings (with their traces, verdicts and reproductions), its shards with their maps, the attack surface's points and the edges; `404 no_scan` | - |
| `GET` | `/api/sessions/{id}/scan/{slug}/export/{format}` | The scan's report rendered from its ledger as it is now, as a download: `json` (findings.json), `csv` (one row per lead, a cell a spreadsheet would read as a formula quoted), `md` (REPORT.md) or `sarif`; `html` is the whole report as a page with no script that prints to PDF, served for a frame of the page; `404 bad_format`, `404 no_scan` | - |
| `POST` | `/api/sessions/{id}/scan` | `wire.ScanRequest`: `action` `start` (with `depth` and an optional `focus`, a directory of the project: the scan's goal is set as `/scan` sets it, answering `{ok, queued}`), `issue-draft` (with `slug` and `lead`, a confirmed finding: answers `{ok, title, body}`, the issue written up), `issue` (with the `title` and `body` the person approved: `gh issue create` in the project, answering `{ok, url}`) or `fix` (with `slug`, `lead`, and `autoFix` and `autoMerge` for its pull request: the goal that fixes the finding is set, answering `{ok, queued}`); `400 bad_scan`, `400 bad_finding`, `502 issue_failed`, `409 plugin_off` | - |
| `POST` | `/api/scan/clone` | `wire.ScanCloneRequest`: `url` (https, ssh or user@host:path only) and `depth`. The repository is cloned into a new folder of `~/sleipnir-scans` without its submodules, with git's hooks, fsmonitor and symbolic links off and only the https and ssh transports allowed, and the scan is created there; answers `{ok, dir, slug, team, shards, goal}`, for the New session dialog to start a sandboxed team in `dir` with `goal`; `400 bad_url`, `400 bad_depth`, `400 bad_scan`, `502 clone_failed` | - |
| `POST` | `/api/sessions/{id}/mode` | `wire.ModeRequest`: `default`, `accept-edits`, `plan`, `bypass`, `yolo` | `mode:<mode>:<tab>` for `bypass` and `yolo` |
| `POST` | `/api/sessions/{id}/model` | `wire.ModelRequest`: the manager's model, or a role's; a team starts again on it. `roles` (role to model, `manager` for the team's model) changes several at once: every model is checked first and one restart carries them all. `team` (one `{role, model}` per worker slot, in team order; `model` empty is the role's) is the whole team to go on with, in the same restart: how many workers each role has and the model of each slot; a team has at least 8 slots, each a worker role (`422 team` otherwise). A restart for a change of models takes up what the team was doing: a goal that was active is resumed, and a turn that was running is followed by one that tells the agent to go on. A tab whose session did not start (no model, a provider without a key) starts again on the models named, unchecked: the start says in a row why it fails again. Answers `{ok, restarted}`; `422 model` for a model that cannot be used | Through the restart, when it raises |
| `POST` | `/api/sessions/{id}/effort` | `wire.EffortRequest`; `wire.EffortResult` has the level the model applies | - |
| `POST` | `/api/sessions/{id}/budget` | `wire.BudgetRequest`: `usd`, or `off`; a team's budget applies when it starts again, and `off` for a team whose configuration sets `swarm.budget_usd` is `409 budget_configured` (the team would start with that budget again). Answers `{ok, paused}`, `paused` being whether the goal paused because spending is already above the new limit | - |
| `POST` | `/api/sessions/{id}/compact` | Folds the thread now (`{focus}`); answers `{from, to}` in tokens | - |
| `PATCH` | `/api/sessions/{id}/launch` | `wire.LaunchPatch`: flags (`isolation`, `verify`, `commit`, `mailman`, `autoDocs`, `managerApproves`, `noMcp`, `trustProject`) staged for the next start of the team | - (checked at the restart that applies them) |
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
| `GET` | `/api/sessions/{id}/ws/raw` | `?path=&at=`: the bytes of an image (SVG included), a video, an audio file or a PDF at a point (live by default), with its type, for the page's previews; byte ranges are honoured. Served with a policy of its own: an image, a video or an audio file is sandboxed and loads nothing; a PDF is not sandboxed (a browser's PDF viewer does not run in a sandbox), loads nothing of the page's and may be framed by the page only. Another kind of file is `415 not_media` | - |
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
| `POST` | `/api/providers/{name}/key` | `wire.KeySaveRequest` (body at most 8 KiB): stores the provider's key in `auth.json` as `sleipnir login` does, and this process takes it up at once; with `check` one small request tries it first and a key the provider refuses is not kept (`422 rejected`). The key is never returned or logged; `400 bad_key` for a key with spaces, `404 not_found` for a provider that takes none. Answers the provider's `wire.ProviderRow` | `key:<name>` |
| `POST` | `/api/providers/{name}/signout` | Removes the stored key; a key from the environment is `409 env` | - |
| `GET` | `/api/sessions/{id}/config` | `wire.ConfigView`: the configuration layers, every effective value (redacted) with the layer that supplied it, and the warnings of `sleipnir config` | - |

A provider key can be typed in the page once (`POST /api/providers/{name}/key`); it is never sent back. The ChatGPT plan signs in with `sleipnir login chatgpt` in a terminal.

### Memory

What the agents carry from one session to the next (the Memory view): the notes of the memory tool (`~/.sleipnir/MEMORY.md`, and the
project's `.sleipnir/MEMORY.local.md`), what each role of a team learned in the project (its lore, read-only), every
place an instruction file is read from (`~/.sleipnir/SLEIPNIR.md`, the project's `AGENTS.md`, `CLAUDE.md`, `SLEIPNIR.md`,
`.sleipnir/SLEIPNIR.md` and the local files of each directory from the root down to the session's directory) with the files they
import, and the lessons of earlier goals (`~/.sleipnir/lessons.json`). Under `/api/sessions/{id}/memory` the routes read the tab's
project and its session's home; under `/api/memory` (a page with no tab yet) only the user's own files. Every answer is the whole
`wire.MemoryView`. A write names the `version` it was edited from (a hash of the file's bytes, `""` for a file that is not there) and
is refused with `409 changed` when the file changed since, so a note an agent saved meanwhile is not lost. A project file is written
inside the project root only, and a project file that is a link, a file over 64 KB and an imported file are not written (`400
bad_path`). A change applies to the sessions started after it.

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/sessions/{id}/memory` | `wire.MemoryView`: `notes` (the list, its version, whether the memory plugin is on, the limits), `project` (the same for the project's notes; absent with no tab), `lore` (`dir`, and per role its `path` and `items`: `stage` insight or canon, `confirmed`, `text`; absent with no tab), `files` (each place with `scope`, `exists`, `loaded` by a session started now, `text`, `version`, `editable` and `why` not) and `lessons` (kind, text, the goal it came from, uses, wins, losses, utility) | - |
| `PUT` | `/api/sessions/{id}/memory/notes` | `wire.MemoryNotesWrite`: the whole list of notes (`scope` `project` for the project's, which also keeps them out of git; `400 bad_path` when `.sleipnir` or the file is a link), cleaned and held to the memory tool's limits (`400 bad_request` says which); `409 changed` | - |
| `PUT` | `/api/sessions/{id}/memory/file` | `wire.MemoryFileWrite`: one instruction file by its `path` as `files` shows it, created with its folder when it is not there; `400 bad_path` for another path or a file not edited here, `400 bad_request` for text that is not UTF-8 or over 64 KB; `409 changed` | - |
| `DELETE` | `/api/sessions/{id}/memory/lessons/{lesson}` | Removes a lesson; `404 not_found` for an id the file does not have | - |
| `GET` | `/api/memory` | As `GET /api/sessions/{id}/memory`, for the user's own files only | - |
| `PUT` | `/api/memory/notes` | As `PUT /api/sessions/{id}/memory/notes`, for the user's notes only | - |
| `PUT` | `/api/memory/file` | As `PUT /api/sessions/{id}/memory/file`, for `~/.sleipnir/SLEIPNIR.md` | - |
| `DELETE` | `/api/memory/lessons/{lesson}` | As `DELETE /api/sessions/{id}/memory/lessons/{lesson}` | - |

### Plugins

Everything Sleipnir does beyond its core is a plugin that can be switched on and off (docs/PLUGINS.md). The routes without a
session read the tab of `?session=`, else the page's active tab, else the user's configuration alone.

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/plugins` | The catalogue: `profile`, `profiles`, `restart` (a saved change waits for the team to start again) and `plugins`, one row per plugin with `id`, `title`, `description`, `category`, `core` (and `core_reason`), `experimental`, `external`, `default` (on in the profile), `enabled`, `state` (`on`, `off`, `pending`, `error`, `restart`), `reason`, `layer` (who decided), `requires`, `contributes`, `config_schema`, `config` (values whose names look secret are withheld) and `unavailable` (why a plugin that is on cannot work here: a required need is missing, such as `delivery` without the GitHub CLI), and `needs`, the rows of `GET /api/needs` | - |
| `GET` | `/api/sessions/{id}/plugins` | The same, with each row compared with the running session: a row that differs is in state `restart` | - |
| `POST` | `/api/plugins/{id}` | `{"enabled": bool, "config": {...}, "scope": "user"\|"project"}` (each optional; scope `user` by default): writes a minimal difference to that file, so a choice equal to the profile's default removes the row. Answers `{"plugin": row, "restart": bool}`. `409 core` for a core plugin, `404 no_plugin` for an unknown id | - |
| `POST` | `/api/plugins/profile` | `{"profile": id, "scope": ...}`: picks the profile (`standard` removes the key); answers the catalogue | - |
| `DELETE` | `/api/plugins/{id}` | `?scope=`: the row goes back to the profile's default | - |
| `GET` | `/api/needs` | The programs and tool servers the plugins that are on use (docs/PLUGINS.md): one row per need with `id`, `plugin`, `title`, `description`, `homepage`, `required`, `server` (a tool server), `found`, `path`, `via` (`located`, `path`, `default`, `config`), `version`, `methods` (this machine's install methods: `id`, `title`, `env` (variables the commands get, each with the command that prints it as `NAME=value`), `setup` (commands run before `argv`, in order), `argv`, `connect`, `note`, `available`, `reason`, `scope`) and `install` (the last install since the server started: `method`, `running`, `output`, `error`). The catalogue carries the same rows as `needs`, and a plugin whose required need is missing has `unavailable` | - |
| `POST` | `/api/needs/{id}/locate` | `{"path": "/abs/path"}`: checks the file (an executable; its version command succeeds), remembers it and links it into `~/.sleipnir/bin`; answers the row. `422 not_located` with why the file was not taken, `404 no_need` when no plugin that is on needs it | `need.locate:<d16>` |
| `DELETE` | `/api/needs/{id}/locate` | Forgets the located file and its link; answers the row | - |
| `POST` | `/api/needs/{id}/install` | `{"method": id}`: works out that declared method's `env`, runs its `setup` commands and then its `argv` in the background (no shell), stopping at the first that fails, then for a tool server adds it to the user's `mcp`; answers the row with `install.running`, and `GET /api/needs` reports the output until it ends. `400` for a method that is not declared for this machine, `409 unavailable` when its program is missing, `409 running` while one runs | the method's `scope` |

A plugin id may hold a colon (`view:mail`); the page sends it URL-encoded. A running session keeps its plugin set: the change
applies when its team starts again (`POST /api/sessions/{id}/restart`).

### Card layout

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/layout` | The person's card layout: `{"version", "pages": {page: [{"id", "x", "y", "w", "h", "hidden", "opts"}]}}`, read from `~/.sleipnir/web-layout.json`; nothing stored (or a file that does not read) is `{"version": 0, "pages": {}}` | - |
| `PUT` | `/api/layout` | Replaces the whole layout (`204`). Ids are words of letters, digits and `.:_-`; places and sizes are grid units within 0..1000; `opts` hold strings, numbers and booleans. Anything else is `400 bad_layout`. The file is written through a temporary file and a rename, mode 0600 | - |
| `DELETE` | `/api/layout` | `?page=ID` forgets one page's cards, no `page` forgets every page (`204`, also when nothing was stored) | - |

A page is a view (`cockpit`, `cache`, `mail`, `board`, `replay`, `sessions`, `tools`, `runner`, `doctor`, `schedule`), a tab of the
Workspace (`files`, `changes`, `checkpoints`, `merge`), a page of Settings (`settings.models`, `settings.look`, ...), the Radio rail
(`rail`) or the header (`hud`). A card is listed under the page that shows it, with its place even while it is hidden; a card of one
page may be shown on another. The page keeps the same document in the browser's storage and works from it when the server cannot be
reached; `version` is the page's format (2: cards on every page), and an older Cockpit layout kept in the browser is carried over.

### Saved teams

A saved team (a loadout) is the file `--team NAME` reads: `.sleipnir/teams/NAME.json` in a project, or `~/.sleipnir/teams/NAME.json`
for the person alone (`internal/teamfile`). The page's team library (Saved teams in the New session dialog and in Team & run) and the
chat's `/team` work on the same files. A `dir` the server does not know as a project (its own folder, the configured projects, and
those of its sessions and recorded sessions, as `GET /api/projects` lists them) is never looked at: the request sees only the person's
teams, and a project scope there is `422 team`.

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/teams` | `{teams: [{name, scope, path, shadowed, team, error}], project, user}`: the saved teams of the project of `?dir=` (the folder the server was started in when it is absent) and of the person, the project's first, each scope by name. `scope` is `project` or `user`; `team` is the file (`{description, slots, models}`); a file that is no team is listed with `error` instead, so that it can be removed; `shadowed` marks a team of the person's that a project team of the same name hides from `--team NAME`. `project` and `user` are the two directories (`project` is empty outside a project) | - |
| `POST` | `/api/teams/check` | Reads `{text}`, a team file pasted or picked from a file: `{team}` when it is one, else `422 team` with the reason (unknown fields, no slots, a slot that is not a worker role, a model under `models` for a worker role, more than 64 KiB) | - |
| `PUT` | `/api/teams/{name}` | Saves `{team, scope, dir, overwrite}` as the team `name` (letters, digits, `-`, `_` and `.`, at most 64, starting with a letter or a digit) for the project of `dir` (`scope: project`) or for the person (`user`, the default); answers `{entry}`. A team of that name there already is `409 exists` unless `overwrite`; a team that is no team, a bad name or a project scope outside a project is `422 team`. The file is written whole and renamed into place | - |
| `DELETE` | `/api/teams/{name}` | Removes the team `name` of `?scope=` (`user` by default) for the project of `?dir=`; `404 no_team` when there is none | - |
| `GET` | `/api/sessions/{id}/team` | The team the session runs as it would be saved: `{team, dir}`, every slot with the model it runs on, the manager's model and those named for the other roles that are no slot. `409 single` for a single agent, `409 starting` before the session has started | - |

Starting again as a saved team is the chat's `/team load NAME` (`POST /api/sessions/{id}/command`): the team starts again slot by
slot with every model the file names, and the conversation and the board carry over. A team with fewer slots of a role than that
role has workers at work is refused with the reason (each worker keeps a slot of its role); `/team load NAME --fresh` starts again
as it with an empty conversation.

### Previews

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/preview/mermaid` | The diagram frame of the file previews: a document that draws mermaid diagrams (the vendored `@mermaid-js/tiny` build, `internal/web/preview`). It is served sandboxed with scripts and no origin, its two inline scripts named by their hashes, framed by this server's page only, gzip-compressed when accepted. The page posts it `{sl: "mermaid", id, src, theme}` and gets `{sl: "mermaid", id, svg, w, h}` or `{..., error}` back, and shows the SVG as an image | - |

### Recorded sessions

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/recorded` | The session directories no tab hosts, newest first (`wire.RecordedSession`), and their size | - |
| `POST` | `/api/recorded/prune` | `wire.PruneRequest`: the plan of `sleipnir sessions prune`, or with `apply` its deletion; `olderThan` and `keep` left out are the CLI's `30d` and `20`. Sessions open in a tab (one that restarts included) or locked by a process are kept | `prune:<d16>` for `apply` |
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
| `POST` | `/api/schedule/jobs` | `wire.JobRequest`: adds a job; `bypass` and `yolo` are refused. `dir` is the absolute path of a folder that exists, a project of the page or any other (`400 bad_path`, `404 no_dir`, `403 unreadable` for one the server cannot look at); the confirmation is where the person sees what the job will run. `budgetUsd` left out is US$1, as `schedule add`; `0` is no limit. A job with no `model` where the configuration of its folder names no `models.default` (and `SLEIPNIR_MODEL` is unset) is `422 model`, as `schedule add` refuses it: every run would fail. A job never takes the id of a removed job that left logs | `job.add` |
| `PUT` | `/api/schedule/jobs/{job}` | Edits a job's cron, goal, directory, model, mode and budget; a budget left out stays what it was | `job.edit:<job>` |
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
| `POST` | `/api/runs` | `wire.RunRequest` and `keep` (let the run continue when the page leaves): starts a run, `202 wire.RunStarted` | `run:<d16>` for a `priv` command, including any whose flags widen its privileges, and a `run` with no `--mode` (with nobody to answer it would choose bypass) |
| `GET` | `/api/runs` | `{runs: [wire.RunInfo]}`: running and recent runs | - |
| `DELETE` | `/api/runs/{run}` | Stops a run | - |
| `GET` | `/api/runs/{run}/output` | `?from=`: the lines a run sent, from a line number on: its first 20,000 lines and 4 MiB of text (a line is cut at 4 KiB), then one line saying how many were left out | - |

### Doctor and update

| Method | Path | Does | Confirmation |
|---|---|---|---|
| `GET` | `/api/doctor/endpoints` | `[wire.DoctorEndpoint]`: the models the configuration names, where each is served and which variable holds its key | - |
| `POST` | `/api/doctor` | `{model?, baseUrl?, deep}`: starts a probe of an endpoint; `202 wire.RunStarted`, then `run` frames with steps and a verdict | - |
| `GET` | `/api/update` | `wire.UpdateStatus`: the version and the latest release (`502 network` when it cannot be asked) | - |
| `POST` | `/api/update/install` | Runs `sleipnir update` in this process, its output as `run` frames; `409 none` when nothing newer exists or the build is from source | `update:<version>` |
