# OWNERSHIP: who builds which files, and the Go interfaces between them

Binding for every builder. File ownership is exclusive: a builder edits only the files listed for it; anything else goes through the
coordinator (section 4). The Go in section 5 compiles: it was built and vetted against this module at 08c4482 (A2's committed
`internal/web` included). A2 adds `internal/web/wire/*.go` and `internal/web/seam/seam.go` verbatim as its next commit; nobody changes
them without a contract change.

Contents: 1 Builders · 2 Layout of the code · 3 Exclusive files · 4 Shared files and the change protocol · 5 Go: package wire, package
seam and the registration functions · 6 Harness extensions per builder (signatures) · 7 Order and dependencies · 8 Integration ·
9 Rules for every builder

---

## 1. Builders

| Id | Scope | State | Delivers next |
|---|---|---|---|
| A1 | the architect | this contract | changes on request |
| A2 | server foundation | committed 08c4482: `internal/web` (envelope, authority, confirmations, hub, assets, `web.Server.Handle`, the `webHost` seam), `cmd/sleipnir/web.go`, `e2e_web_test.go`, docs (CLI, SECURITY section 5, UX, ARCHITECTURE), CODEOWNERS | `internal/web/wire`, `internal/web/seam` (verbatim, section 5); the deltas Δ1, Δ2 (CONTRACT.md 2.9); `internal/web/webtest` (a fake `seam.Host`/`seam.Tab` and a canned snapshot for the C builders); then hands `cmd/sleipnir/web.go` and `e2e_web_test.go` to B1 |
| A3 | UI packaging | committed db38f7d: `internal/web/ui` (index.html, 22 stylesheets, 30 modules, fonts and licences, `DEV-ONLY.md`, `js-inventory.md`), `scripts/web-parity.mjs`, `scripts/web-parity-scenes.json`, `scripts/web-ui-dev.mjs`, `.gitattributes` | `internal/web/uidev/` (the mock's fixture modules and `index-mock.html`, not embedded), `hooks-real.js`, the `--dom` and `--no-sample` parity modes (TEST-PLAN.md 7), the asset rule "no sample data shipped" once C1 removes the fixtures |
| B1 | session host and approvals | | `cmd/sleipnir/web_host.go` (sets `webHost`), `web_tab.go`, `web_goal.go`, `chatflags.go`, `web_fixture.go`, the stream route and frame adapter, `internal/web/approvals`, `internal/session/web_access.go`, the `goal.judge` event, the flags of CONTRACT.md 1, docs/CLI.md's regenerated flags block |
| B2 | translator | | `internal/web/translate` (sinks, log follower, journal, mirror, keyframes, history), `internal/tui/state` extensions, `state.LayerSplit`, `inspect.AnomalyTitle` |
| B3 | workspace | | `internal/web/wsvc` (routes of CONTRACT.md 12 and `complete`, `permissions/check`), `internal/checkpoint`, `internal/gitx`, `internal/perm`, accessors in `internal/swarm`, `internal/workspace`, `internal/tools`, `internal/session/workspace_access.go` |
| B4 | settings, tools, extractions | | `internal/web/settings` (routes of CONTRACT.md 7, 13 to 17, 19), `internal/web/runner` (18), `internal/web/clispec`, `internal/catalog` (new), session listing/prune/sidecar, `internal/sched`, `internal/config`, `internal/update`, the doctor extraction, and the package-main files they come from |
| C1 | front-end: live core, sessions, chat, approvals, cockpit | | `api.js`, `live.js`, `11-data-live.js` and the patched modules of UI-WIRING.md 1 |
| C2 | front-end: workspace | | `96b-ws-data.js` (live), `97-ui-workspace.js` |
| C3 | front-end: settings, tools, runner, sessions pages | | `91-views-b.js`, `92-runner.js`, `96-ui-nav.js`, `98-ui-settings.js`, `98b-ui-tools.js` |
| D | integration and verification | | e2e, parity, security review, docs, `scripts/check.sh` |

## 2. Layout of the code

```
cmd/sleipnir/
  web.go               A2 (committed) → B1: the command, flags, banner, signals, web.Config, the webHost call
  web_host.go          B1  sets webHost: builds the host, registers every route package (section 5.4), the stream route
  web_tab.go           B1  seam.Tab + seam.SessionAccess: one hosted session, its turn goroutine, queue, commands, restarts
  web_goal.go          B1  the goal loop shared with chat_tty.go (extracted)
  chatflags.go         B1  the chat flag parser shared by cmdChat, restartArgs and the web (extracted from chat.go)
  web_fixture.go       B1  hidden --fixture: deterministic sessions on internal/provider/mock (TEST-PLAN.md 3)
  e2e_web_test.go      A2 (committed) → B1 (+ D adds cases)
internal/web/
  api.go assets.go auth.go doc.go envelope.go route.go server.go stream.go (+ tests)   A2 (committed)
  wire/*.go            A2 (verbatim from section 5)
  seam/seam.go         A2 (verbatim from section 5)
  webtest/             A2  fakes for the route packages and the C builders
  ui/                  A3 packaging; js modules owned per section 3 (C1, C2, C3)
  uidev/               A3  not embedded: the mock's fixture modules, index-mock.html, hooks-real.js
  approvals/           B1
  translate/           B2
  wsvc/                B3
  settings/            B4
  runner/              B4
  clispec/             B4  clispec.json (generated, embedded), gen/ (generator), drift tests
internal/catalog/      B4  (new) the model catalogue and favourites, moved from cmd/sleipnir/models.go
```

## 3. Exclusive files

A file not listed is owned by nobody in this program: do not edit it. New files a builder needs inside its own directories are its own.

**A2**: `internal/web/*.go` and their tests, `internal/web/wire/**`, `internal/web/seam/**`, `internal/web/webtest/**`,
`.github/CODEOWNERS`, `scripts/windows-excluded.txt` (only if a web package's tests are POSIX-only, with a `# reason`),
`docs/SECURITY.md` section 5, `docs/ARCHITECTURE.md` (the `internal/web` row). After A2's next commit, `cmd/sleipnir/web.go` and
`cmd/sleipnir/e2e_web_test.go` move to B1.

**A3**: `internal/web/ui/index.html`, `internal/web/ui/css/**`, `internal/web/ui/fonts/**`, `internal/web/ui/js/00-namespace.js`,
`internal/web/ui/DEV-ONLY.md`, `internal/web/ui/js-inventory.md`, `internal/web/uidev/**`, `scripts/web-ui-dev.mjs`,
`scripts/web-parity.mjs`, `scripts/web-parity-scenes.json`, `.gitattributes`. A3 also moves the fixture modules
(`js/data.js`, `outputs.js`, `cli-spec.js`, `10-fixtures.js`, `11-data-adapter.js`, `40-scripts.js`) to `uidev/` when C1's live data
layer lands (one coordinated commit with C1).

**B1**: `cmd/sleipnir/web*.go` (with `web.go` and `e2e_web_test.go` after the handover), `cmd/sleipnir/chatflags.go`,
`cmd/sleipnir/chat.go`, `cmd/sleipnir/chat_tty.go`, `cmd/sleipnir/restart.go` (only to extract the shared parser and goal loop and call
them: `sleipnir chat` behaves as before), `internal/web/approvals/**`, `internal/session/session.go` (only `Options()` and the
checkpoint hook wiring), `internal/session/web_access.go` (new), `internal/session/goal.go` (the `goal.judge` event), `docs/CLI.md`
(the `web` section: regenerated flags block, the new flags described; `scripts/gen-cli-docs.sh --check` passes).

**B2**: `internal/web/translate/**`, `internal/tui/state/**`, `internal/tui/app/cacheview.go` (only to call `state.LayerSplit`),
`internal/inspect/explain.go` (only to export `AnomalyTitle`), `internal/events/log.go` (only if a drop counter export is needed).

**B3**: `internal/web/wsvc/**`, `internal/checkpoint/**`, `internal/gitx/**`, `internal/workspace/**`, `internal/perm/**`,
`internal/swarm/leases.go` and `internal/swarm/isolate.go` (accessors only), `internal/tools/support.go` (the `FileState` accessor),
`internal/session/workspace_access.go` (new).

**B4**: `internal/web/settings/**`, `internal/web/runner/**`, `internal/web/clispec/**`, `internal/catalog/**` (new),
`internal/session/listing.go` and `internal/session/meta.go` (new), `internal/session/prune.go`, `internal/session/mcp.go` (the
approval store re-read), `internal/sched/**`, `internal/config/**`, `internal/update/**`, `internal/provider/probe/**` (a step callback
if needed), `cmd/sleipnir/main.go`, `cmd/sleipnir/models.go`, `cmd/sleipnir/login.go`, `cmd/sleipnir/pick.go`, `cmd/sleipnir/setup.go`,
`cmd/sleipnir/sessions_prune.go`, `cmd/sleipnir/schedule.go`, `cmd/sleipnir/update.go`, `cmd/sleipnir/mcp.go`, `cmd/sleipnir/trust.go`,
`cmd/sleipnir/provider.go`, `cmd/sleipnir/allow.go` (each only to move code into the packages above and call it; every command's
output and `-h` text byte-identical).

**C1**: `internal/web/ui/js/` files `api.js`, `live.js`, `11-data-live.js` (new), `30-model.js`, `50-sessions.js`, `60-actions.js`,
`80-ui-shell.js`, `81-ui-hero.js`, `83-ui-cockpit.js`, `84-ui-chat.js`, `85-ui-approvals.js`, `87-ui-sheets.js`, `88-ui-drawer.js`,
`90-views-a.js`, `93-palette.js`, `95-kit.js` (D-20), `99-app.js`; `docs/UX.md` (completes the web section A2 started). C1 asks A3 for
the `index.html` script list change (UI-WIRING.md 2) in the same coordinated commit.

**C2**: `internal/web/ui/js/96b-ws-data.js`, `internal/web/ui/js/97-ui-workspace.js`.

**C3**: `internal/web/ui/js/91-views-b.js`, `92-runner.js`, `96-ui-nav.js`, `98-ui-settings.js`, `98b-ui-tools.js`.

Unchanged by everyone (byte-identical to the mock): `00-util.js`, `20-clock.js`, `70-view.js`, `82-ui-board.js`, `86-ui-overlays.js`,
`94-keys.js`, and every CSS file except one rule for the conn chip's reconnecting state (D-13, A3).

**D**: `README.md`, `docs/TESTING.md`, `docs/STATUS.md`, `docs/GETTING-STARTED.md`, `docs/ARCHITECTURE.md` (the new packages), new e2e
files; it may edit any test to fix integration, with the code's owner reviewing.

## 4. Shared files and the change protocol

| File | Owner | Others |
|---|---|---|
| `go.mod`, `go.sum` | nobody | no new dependency (stdlib + x/net, x/sys, x/term); `go mod tidy -diff` stays empty |
| `cmd/sleipnir/main.go` | B4 | the `web` usage line is already there (A2); B1 never edits main.go (the command registers itself in `web.go` `init`) |
| `docs/CLI.md` | B1 (the `web` section) | B4 changes no `-h` output; if one must change, B4 regenerates with `scripts/gen-cli-docs.sh` and tells B1 |
| `.github/CODEOWNERS`, `scripts/windows-excluded.txt` | A2 | a builder whose new package needs an entry asks A2 with the path and the reason |
| `internal/session/*` | per file, section 3 | new files only, except those listed; a builder that needs a field of `Session` adds an accessor in its own new file |
| `internal/web/wire`, `internal/web/seam` | A2 (verbatim) | changes through the coordinator: additive ones (a new optional field, a new method with a default in the fakes) within a day; breaking ones need A1 |
| `internal/web/ui/index.html` | A3 | the module list changes once, in the coordinated commit with C1 |
| this contract | A1 / coordinator | builders never edit it |

Commits: small conventional commits on `claude/sleipnir-web-interface-cc5a2f`, ending with the attribution lines the session gives;
never push, never open a PR, never merge (PLAN.md).

## 5. Go: package wire, package seam and the registration functions

`internal/web/wire` imports only `encoding/json` (UI events, frames, request and response bodies). `internal/web/seam` imports
`agent`, `events`, `session` and `wire` (the interfaces between B1, B2, B3 and B4; `internal/web` itself stays free of the harness, as
A2 built it). Route packages import `web` (to register on `*web.Server`), `seam` and `wire`; `web` imports none of them, so there is no
cycle. B1's `webHost` constructs everything and calls the registration functions of 5.4.

### 5.1 `internal/web/wire`

`internal/web/wire/events.go`:

```go
// Package wire holds the JSON shapes of `sleipnir web`: the UI events the page's reducer consumes, the stream frames, and the
// request and response bodies of the HTTP API. It imports nothing from the harness, so every builder can depend on it.
package wire

import "encoding/json"

// Base is the head every UI event carries: session-relative time in seconds, the kind, the journal sequence number and, for
// history events whose time was clamped to zero, the real time in epoch milliseconds.
type Base struct {
	T   float64 `json:"t"`
	K   string  `json:"k"`
	Seq uint64  `json:"seq,omitempty"`
	At  int64   `json:"at,omitempty"`
}

// base gives access to the head of any event type.
func (b *Base) base() *Base { return b }

// Event is one UI event of the vocabulary (VOCAB.md). Only the types of this package implement it.
type Event interface{ base() *Base }

// Stamp sets the kind from the event's type, the time and the sequence number, and returns the event.
func Stamp(e Event, t float64, seq uint64) Event {
	b := e.base()
	b.K, b.T, b.Seq = KindOf(e), t, seq
	return e
}

// HeadOf returns a copy of the event's head.
func HeadOf(e Event) Base { return *e.base() }

// KindOf names the kind of an event by its type ("" for an unknown type).
func KindOf(e Event) string {
	switch e.(type) {
	case *Say:
		return "say"
	case *Sys:
		return "sys"
	case *Tool:
		return "tool"
	case *Note:
		return "note"
	case *State:
		return "state"
	case *Task:
		return "task"
	case *Plan:
		return "plan"
	case *Verdict:
		return "verdict"
	case *Req:
		return "req"
	case *Use:
		return "use"
	case *Warm:
		return "warm"
	case *Gov:
		return "gov"
	case *Mail:
		return "mail"
	case *Ckpt:
		return "ckpt"
	case *Ask:
		return "ask"
	case *Answer:
		return "answer"
	case *Queue:
		return "queue"
	case *Merge:
		return "merge"
	case *Break:
		return "break"
	case *Compact:
		return "compact"
	case *Stream:
		return "stream"
	case *Diff:
		return "diff"
	case *Goal:
		return "goal"
	case *Final:
		return "final"
	case *Steer:
		return "steer"
	case *Interrupt:
		return "interrupt"
	case *Refuse:
		return "refuse"
	case *More:
		return "more"
	case *Turn:
		return "turn"
	case *Stall:
		return "stall"
	case *Handover:
		return "handover"
	case *Layers:
		return "layers"
	}
	return ""
}

// Say is a transcript message: the person's (Who "you"), the manager's ("mgr") or a harness status line ("sys").
type Say struct {
	Base
	Who    string `json:"who"`
	Text   string `json:"text"`
	Glyph  string `json:"glyph,omitempty"`
	Plan   bool   `json:"plan,omitempty"`
	Task   string `json:"task,omitempty"`
	Stream bool   `json:"stream"`
	Rate   int    `json:"rate,omitempty"`
	Mid    string `json:"mid,omitempty"`
}

// Sys is a system line in a channel: a notice, a lease conflict, an acknowledgment of a person's action.
type Sys struct {
	Base
	Ch    string `json:"ch"`
	Glyph string `json:"glyph"`
	Text  string `json:"text"`
	Ag    string `json:"ag,omitempty"`
	Task  string `json:"task,omitempty"`
}

// Tool is a finished tool call of an agent.
type Tool struct {
	Base
	ID      string `json:"id"`
	Name    string `json:"name"`
	Arg     string `json:"arg"`
	Out     string `json:"out"`
	OK      bool   `json:"ok"`
	Refused bool   `json:"refused,omitempty"`
	Reason  string `json:"reason,omitempty"`
	File    string `json:"file,omitempty"`
	Add     int    `json:"add,omitempty"`
	Del     int    `json:"del,omitempty"`
	Task    string `json:"task,omitempty"`
	TID     string `json:"tid,omitempty"`
}

// Note is a one-line note of an agent (a submission, its evidence).
type Note struct {
	Base
	ID   string `json:"id"`
	G    string `json:"g"`
	Text string `json:"text"`
	Task string `json:"task,omitempty"`
}

// State is an agent's status change; Task is always sent (null: no task).
type State struct {
	Base
	ID    string  `json:"id"`
	S     string  `json:"s"`
	Doing string  `json:"doing"`
	Task  *string `json:"task"`
}

// Task creates or moves a task of the board.
type Task struct {
	Base
	ID      string   `json:"id"`
	Title   string   `json:"title,omitempty"`
	Owner   string   `json:"owner,omitempty"`
	Deps    []string `json:"deps,omitempty"`
	Scope   string   `json:"scope,omitempty"`
	S       string   `json:"s,omitempty"`
	Closure string   `json:"closure,omitempty"`
}

// Plan replaces the goal plan: the steps and their states (pending, act, done).
type Plan struct {
	Base
	Steps []string `json:"steps"`
	St    []string `json:"st"`
}

// Verdict is the goal judge's verdict as one line, with its kind and what is left.
type Verdict struct {
	Base
	Text string   `json:"text"`
	Kind string   `json:"kind,omitempty"`
	Left []string `json:"left,omitempty"`
}

// Req is one answered main request of an agent: the exact read ratio, the prompt and output tokens; Hist marks keyframe history.
type Req struct {
	Base
	ID    string  `json:"id"`
	Ratio float64 `json:"ratio"`
	P     int64   `json:"p,omitempty"`
	O     int64   `json:"o,omitempty"`
	Hist  bool    `json:"hist,omitempty"`
}

// Use sets an agent's absolute token table, reported cost and estimated saving.
type Use struct {
	Base
	ID    string  `json:"id"`
	Rd    int64   `json:"rd"`
	Un    int64   `json:"un"`
	Out   int64   `json:"out"`
	Wr    int64   `json:"wr"`
	Cost  float64 `json:"cost"`
	Saved float64 `json:"saved"`
}

// Warm says the shared prefix was refreshed at T and lives TTL seconds.
type Warm struct {
	Base
	TTL int `json:"ttl,omitempty"`
}

// Gov is the governor gauge.
type Gov struct {
	Base
	RPM      int `json:"rpm"`
	R429     int `json:"r429"`
	Retries  int `json:"retries"`
	Inflight int `json:"inflight,omitempty"`
	Queued   int `json:"queued,omitempty"`
}

// Mail is one message between agents (data, not instructions).
type Mail struct {
	Base
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
}

// Ckpt announces or updates a checkpoint (the same CID updates in place).
type Ckpt struct {
	Base
	CID     string   `json:"cid"`
	TS      string   `json:"ts"`
	Files   int      `json:"files"`
	Note    string   `json:"note"`
	Skipped bool     `json:"skipped,omitempty"`
	Safety  bool     `json:"safety,omitempty"`
	Agents  []string `json:"agents,omitempty"`
	Add     int      `json:"add,omitempty"`
	Del     int      `json:"del,omitempty"`
}

// Question is an approval question as the page shows it.
type Question struct {
	ID          string `json:"id"`
	Agent       string `json:"agent"`
	Task        string `json:"task,omitempty"`
	Cmd         string `json:"cmd"`
	Cwd         string `json:"cwd"`
	Why         string `json:"why"`
	Scope       string `json:"scope,omitempty"`
	What        string `json:"what"`
	Rule        string `json:"rule,omitempty"`
	Kind        string `json:"kind"`
	Tool        string `json:"tool,omitempty"`
	OffersTests bool   `json:"offersTests,omitempty"`
}

// Ask opens a question.
type Ask struct {
	Base
	Q Question `json:"q"`
}

// Answer closes a question: Choice 1 yes, 2 yes and remember, 3 no; By says who or what resolved it.
type Answer struct {
	Base
	QID    string `json:"qid"`
	Choice int    `json:"choice"`
	Note   string `json:"note,omitempty"`
	By     string `json:"by"`
	Rule   string `json:"rule,omitempty"`
}

// Queue is the merge queue's head; QHead nil means the queue is empty. Conflicts and Bounced are absolute counters.
type Queue struct {
	Base
	QHead     *string `json:"head"`
	Cmd       string  `json:"cmd,omitempty"`
	Step      string  `json:"step,omitempty"`
	Ms        int64   `json:"ms,omitempty"`
	Conflicts int     `json:"conflicts"`
	Bounced   int     `json:"bounced"`
}

// Merge says a task's work was merged.
type Merge struct {
	Base
	ID  string `json:"id"`
	Cmd string `json:"cmd"`
	Ms  int64  `json:"ms"`
}

// Break is a cache anomaly.
type Break struct {
	Base
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Read     int    `json:"read"`
	Expected int    `json:"expected"`
	Why      string `json:"why"`
}

// Compact is a compaction of an agent's thread: tokens before and after, signed percent.
type Compact struct {
	Base
	ID   string `json:"id"`
	From int    `json:"from"`
	To   int    `json:"to"`
	Pct  int    `json:"pct"`
}

// Stream opens a worker's streamed prose or a file being written (Code, File).
type Stream struct {
	Base
	ID   string `json:"id"`
	Text string `json:"text"`
	Rate int    `json:"rate"`
	Code bool   `json:"code,omitempty"`
	File string `json:"file,omitempty"`
	Mid  string `json:"mid,omitempty"`
}

// Diff says a file's write is complete.
type Diff struct {
	Base
	File string `json:"file"`
	Done bool   `json:"done"`
}

// Goal is the standing goal's state (active, paused, met, cleared) and its details.
type Goal struct {
	Base
	S         string `json:"s"`
	Objective string `json:"objective,omitempty"`
	Turns     int    `json:"turns,omitempty"`
	Max       int    `json:"max,omitempty"`
	Paused    string `json:"paused,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Final ends a turn of the agent the person talks to.
type Final struct{ Base }

// Steer is guidance sent to an agent; Quiet keeps it out of the feed.
type Steer struct {
	Base
	To    string `json:"to"`
	Text  string `json:"text"`
	Quiet bool   `json:"quiet,omitempty"`
}

// Interrupt records that the person interrupted the turn ("turn") or an agent.
type Interrupt struct {
	Base
	ID string `json:"id"`
}

// Refuse is an action refused because nobody could be asked.
type Refuse struct {
	Base
	ID     string `json:"id"`
	Name   string `json:"name"`
	Arg    string `json:"arg"`
	Reason string `json:"reason"`
}

// More continues the message Mid; End closes it; Reset marks a retried request.
type More struct {
	Base
	Mid   string `json:"mid"`
	Text  string `json:"text"`
	End   bool   `json:"end,omitempty"`
	Reset bool   `json:"reset,omitempty"`
}

// Turn marks the start or the end of a turn of the agent the person talks to.
type Turn struct {
	Base
	S string `json:"s"`
}

// Stall is a supervision finding raised or cleared.
type Stall struct {
	Base
	ID   string `json:"id,omitempty"`
	Task string `json:"task,omitempty"`
	Kind string `json:"kind"`
	S    string `json:"s"`
	Text string `json:"text"`
}

// Handover is a task changing hands.
type Handover struct {
	Base
	Task    string `json:"task"`
	From    string `json:"from"`
	To      string `json:"to,omitempty"`
	S       string `json:"s"`
	Closure string `json:"closure,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Layers is an agent's latest prompt by layer G0..G5, in tokens.
type Layers struct {
	Base
	ID   string `json:"id"`
	Toks [6]int `json:"toks"`
}

// Raw is a journal entry kept as encoded JSON (what the journal stores and replays).
type Raw = json.RawMessage
```

`internal/web/wire/api.go`:

```go
package wire

import "encoding/json"

// Error is the body of every non-2xx response (the shape of web.Error, plus an optional Detail) and the error type the route
// packages return: Status is the HTTP status, Msg one sentence a person can read (it is shown in a toast; never a path outside
// the project, never a secret), Code a stable identifier (CONTRACT.md section 21), Detail optional structured data (a trust
// challenge, a retry delay).
type Error struct {
	Status int    `json:"-"`
	Msg    string `json:"error"`
	Code   string `json:"code"`
	Detail any    `json:"detail,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// Frame is one message of the page's stream before it is handed to the hub: Type is the SSE event name (VOCAB.md section 10),
// Tab the tab it belongs to ("" for global frames), Data its JSON body. Critical frames are never dropped for a slow page;
// Coalescable frames with the same Type and Key replace each other in a slow page's queue (web.Event has the same flags).
type Frame struct {
	Type        string
	Tab         string
	Data        any
	Critical    bool
	Coalescable bool
	Key         string
}

// EvFrame is the data of an "ev" frame.
type EvFrame struct {
	Tab string          `json:"tab"`
	Ev  json.RawMessage `json:"ev"`
}

// MetaFrame is the data of a "meta" frame.
type MetaFrame struct {
	Tab   string    `json:"tab"`
	Patch MetaPatch `json:"patch"`
}

// RosterFrame is the data of a "roster" frame.
type RosterFrame struct {
	Tab    string        `json:"tab"`
	Roster []RosterEntry `json:"roster"`
}

// TabFrame is the data of a "tab" frame: Op is add, update or remove.
type TabFrame struct {
	Op  string     `json:"op"`
	Tab TabSummary `json:"tab"`
}

// ReasonFrame is the data of the "resync" and "bye" frames.
type ReasonFrame struct {
	Reason string `json:"reason"`
}

// ResetFrame is the data of a "reset" frame.
type ResetFrame struct {
	Tab string `json:"tab"`
	Gen uint64 `json:"gen"`
}

// Hello is the data of the first frame of a stream and of GET /api/hello.
type Hello struct {
	Boot    string       `json:"boot"`
	Now     int64        `json:"now"`
	Tabs    []TabSummary `json:"tabs"`
	Server  ServerInfo   `json:"server"`
	Limits  Limits       `json:"limits"`
	UI      UIInfo       `json:"ui"`
	Active  string       `json:"active,omitempty"`
	Version string       `json:"version"`
	// StreamAfter is the hub's last event id of the page topic when the hello was made: the page opens the stream with
	// ?after=StreamAfter and loses nothing between the hello and the stream.
	StreamAfter uint64 `json:"streamAfter"`
}

// ServerInfo describes the server: its listening address as the page should show it, and the program version.
type ServerInfo struct {
	Addr     string `json:"addr"`
	Loopback bool   `json:"loopback"`
	Version  string `json:"version"`
}

// Limits are the body and list limits the page must respect.
type Limits struct {
	MaxBody      int `json:"maxBody"`
	MaxMessage   int `json:"maxMessage"`
	MaxQuestions int `json:"maxQuestions"`
}

// UIInfo identifies the embedded UI build (a hash of its files).
type UIInfo struct {
	Version string `json:"version"`
}

// Toast is the data of a "toast" frame.
type Toast struct {
	Tab  string `json:"tab,omitempty"`
	Text string `json:"text"`
	Kind string `json:"kind,omitempty"`
}

// Ping is the data of a "ping" frame: each tab's session time now, in seconds.
type Ping struct {
	Now map[string]float64 `json:"now"`
}

// TabSummary is a live tab as lists show it.
type TabSummary struct {
	ID        string `json:"id"`
	SID       string `json:"sid"`
	Name      string `json:"name"`
	Cwd       string `json:"cwd"`
	Gen       uint64 `json:"gen"`
	Headless  bool   `json:"headless,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	Order     int    `json:"order"`
}

// RosterEntry is one agent of a tab as the page's Session.roster holds it.
type RosterEntry struct {
	ID    string  `json:"id"`
	Role  string  `json:"role"`
	Code  string  `json:"code"`
	Nth   int     `json:"nth"`
	K     int     `json:"k"`
	Leg   int     `json:"leg"`
	Scope string  `json:"scope"`
	RO    bool    `json:"ro"`
	Model string  `json:"model"`
	Spawn float64 `json:"spawn"`
}

// Rule is a permission rule with its origin as the Permissions page shows it.
type Rule struct {
	Effect string `json:"effect"`
	Rule   string `json:"rule"`
	Origin string `json:"origin"`
	File   string `json:"file,omitempty"`
	Note   string `json:"note,omitempty"`
	Fixed  bool   `json:"fixed,omitempty"`
}

// QueuedLine is a message typed while a turn runs, waiting for the next turn.
type QueuedLine struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// MetaPatch is a change to a tab's meta (the page's S.meta); only set fields are sent. Pointers distinguish "unset" from zero, and a
// pointer to an empty slice or map sends an empty value (the last rule removed, the queue drained).
type MetaPatch struct {
	Cwd          *string            `json:"cwd,omitempty"`
	Model        *string            `json:"model,omitempty"`
	Mode         *string            `json:"mode,omitempty"`
	Effort       *string            `json:"effort,omitempty"`
	Budget       *float64           `json:"budget,omitempty"`
	Swarm        *int               `json:"swarm,omitempty"`
	Isolation    *string            `json:"isolation,omitempty"`
	Verify       *string            `json:"verify,omitempty"`
	Commit       *bool              `json:"commit,omitempty"`
	Mailman      *bool              `json:"mailman,omitempty"`
	TrustProject *bool              `json:"trustProject,omitempty"`
	NoMcp        *bool              `json:"noMcp,omitempty"`
	RoleModels   *map[string]string `json:"roleModels,omitempty"`
	Rules        *[]Rule            `json:"rules,omitempty"`
	GoalText     *string            `json:"goalText,omitempty"`
	Launch       *string            `json:"launch,omitempty"`
	StartedAt    *int64             `json:"startedAt,omitempty"`
	Headless     *bool              `json:"headless,omitempty"`
	AskTimeout   *string            `json:"askTimeout,omitempty"`
	ResumedFrom  *RecordedSession   `json:"resumedFrom,omitempty"`
	Queued       *[]QueuedLine      `json:"queued,omitempty"`
	Running      *bool              `json:"running,omitempty"`
}

// TabSnapshot is the full state of a tab for a late joiner (VOCAB.md section 11).
type TabSnapshot struct {
	Tab       TabSummary        `json:"tab"`
	Gen       uint64            `json:"gen"`
	Seq       uint64            `json:"seq"`
	Now       float64           `json:"now"`
	Meta      MetaPatch         `json:"meta"`
	Roster    []RosterEntry     `json:"roster"`
	Keyframe  []json.RawMessage `json:"keyframe"`
	Events    []json.RawMessage `json:"events"`
	Hist      []string          `json:"hist"`
	Questions []Question        `json:"questions"`
}

// OpenQuestion is a question with the tab it belongs to (the cross-session inbox).
type OpenQuestion struct {
	Tab string   `json:"tab"`
	Q   Question `json:"q"`
	T0  float64  `json:"t0"`
}

// NewSessionRequest is the New session dialog: every chat flag it shows, plus a name and a first goal.
type NewSessionRequest struct {
	Name         string            `json:"name,omitempty"`
	Cwd          string            `json:"cwd"`
	Model        string            `json:"model,omitempty"`
	Mode         string            `json:"mode,omitempty"`
	Swarm        *int              `json:"swarm,omitempty"`
	Isolation    string            `json:"isolation,omitempty"`
	Verify       string            `json:"verify,omitempty"`
	Commit       bool              `json:"commit,omitempty"`
	Mailman      bool              `json:"mailman,omitempty"`
	Budget       *float64          `json:"budget,omitempty"`
	Rules        []string          `json:"rules,omitempty"`
	TrustProject bool              `json:"trustProject"`
	NoMcp        bool              `json:"noMcp,omitempty"`
	GoalText     string            `json:"goalText,omitempty"`
	Effort       string            `json:"effort,omitempty"`
	RoleModels   map[string]string `json:"roleModels,omitempty"`
}

// ResumeRequest resumes a recorded session ("latest" or a session id) in a new tab.
type ResumeRequest struct {
	From string `json:"from"`
	Name string `json:"name,omitempty"`
	Cwd  string `json:"cwd,omitempty"`
}

// RestartRequest is the restart family: Kind new, clear, swarm, restart, model, roles; Fresh starts the chat empty.
type RestartRequest struct {
	Kind       string            `json:"kind"`
	Swarm      *int              `json:"swarm,omitempty"`
	Model      string            `json:"model,omitempty"`
	RoleModels map[string]string `json:"roleModels,omitempty"`
	Fresh      bool              `json:"fresh"`
	Flags      []string          `json:"flags,omitempty"`
}

// MessageRequest is a line sent from the composer: Text is what reaches the agent, Display what the transcript shows.
type MessageRequest struct {
	Text     string `json:"text"`
	Display  string `json:"display,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

// SendResult says whether a message was delivered now or queued behind the running turn.
type SendResult struct {
	Queued   bool   `json:"queued"`
	Position int    `json:"position,omitempty"`
	ID       string `json:"id"`
}

// CommandRequest is a slash line the page has no handler for (custom commands, skills, MCP prompts, /mcp reconnect).
type CommandRequest struct {
	Line string `json:"line"`
}

// CommandResult is what a slash line did: Output is its text (shown as a local card), Sent whether a prompt went to the agent.
type CommandResult struct {
	Output string `json:"output,omitempty"`
	Sent   bool   `json:"sent"`
	Title  string `json:"title,omitempty"`
}

// AnswerRequest answers a question.
type AnswerRequest struct {
	Choice int    `json:"choice"`
	Note   string `json:"note,omitempty"`
}

// AnswerResult says what an answer did: Rule is the allow rule a choice 2 added.
type AnswerResult struct {
	OK   bool   `json:"ok"`
	Rule string `json:"rule,omitempty"`
}

// GoalRequest sets, pauses, resumes or clears the standing goal.
type GoalRequest struct {
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
}

// ModeRequest sets the permission mode; bypass and yolo need an X-Confirm id (CONTRACT.md 20).
type ModeRequest struct {
	Mode string `json:"mode"`
}

// ModelRequest sets the manager's model (Role empty or "manager") or a role's (a team restarts that role).
type ModelRequest struct {
	Ref  string `json:"ref"`
	Role string `json:"role,omitempty"`
}

// EffortRequest sets the reasoning effort.
type EffortRequest struct {
	Level string `json:"level"`
}

// EffortResult is the level requested and the level the model applies.
type EffortResult struct {
	Requested string `json:"requested"`
	Applied   string `json:"applied"`
}

// BudgetRequest sets the budget in US dollars; Off removes it.
type BudgetRequest struct {
	USD float64 `json:"usd,omitempty"`
	Off bool    `json:"off,omitempty"`
}

// CompactRequest folds the thread now, keeping Focus in view.
type CompactRequest struct {
	Focus string `json:"focus,omitempty"`
}

// LaunchPatch stages flags that apply when the team starts again.
type LaunchPatch struct {
	Isolation    *string `json:"isolation,omitempty"`
	Verify       *string `json:"verify,omitempty"`
	Commit       *bool   `json:"commit,omitempty"`
	Mailman      *bool   `json:"mailman,omitempty"`
	NoMcp        *bool   `json:"noMcp,omitempty"`
	TrustProject *bool   `json:"trustProject,omitempty"`
}

// RuleRequest adds (Effect allow, deny, ask) or removes a session rule; "tests" stands for the tests preset.
type RuleRequest struct {
	Effect string `json:"effect,omitempty"`
	Rule   string `json:"rule"`
	Origin string `json:"origin,omitempty"`
}

// RuleResult says how many rules were added or removed.
type RuleResult struct {
	Added   int `json:"added,omitempty"`
	Removed int `json:"removed,omitempty"`
}

// PermCheckRequest is the "Would it ask?" tester: Tool Bash, Edit or Read, Arg a command or a path.
type PermCheckRequest struct {
	Tool string `json:"tool"`
	Arg  string `json:"arg"`
}

// PermVerdict is the tester's answer: D allowed, ask or refused; Why names the rule or the mode; Cls ok, warm or err.
type PermVerdict struct {
	D   string `json:"d"`
	Why string `json:"why"`
	Cls string `json:"cls"`
}

// Project is a directory a new session may start in.
type Project struct {
	Dir     string `json:"dir"`
	Root    string `json:"root"`
	Name    string `json:"name"`
	Trust   string `json:"trust"`
	Files   int    `json:"files"`
	Default bool   `json:"default,omitempty"`
}

// RecordedSession is a session directory on disk that no tab hosts, as the Sessions view and the Resume dialog read it.
type RecordedSession struct {
	ID          string  `json:"id"`
	Name        string  `json:"name,omitempty"`
	First       string  `json:"first"`
	Model       string  `json:"model"`
	Cost        float64 `json:"cost"`
	MB          float64 `json:"mb"`
	AgeS        float64 `json:"ageS"`
	Agents      int     `json:"agents"`
	Resumable   bool    `json:"resumable"`
	Dur         float64 `json:"dur"`
	Interrupted bool    `json:"interrupted,omitempty"`
	Cwd         string  `json:"cwd,omitempty"`
	LastWritten int64   `json:"lastWritten"`
	Locked      bool    `json:"locked,omitempty"`
}

// PruneRequest asks what `sessions prune` would delete, or deletes it (Apply: the route needs an X-Confirm id).
type PruneRequest struct {
	OlderThan string `json:"olderThan"`
	Keep      int    `json:"keep"`
	Apply     bool   `json:"apply,omitempty"`
}

// PrunePlan is the sessions a prune deletes and their size.
type PrunePlan struct {
	List    []RecordedSession `json:"list"`
	MB      float64           `json:"mb"`
	Applied bool              `json:"applied"`
	Error   string            `json:"error,omitempty"`
	Kept    []string          `json:"branchesKept,omitempty"`
}

// DeleteRecordedRequest deletes the named recorded sessions (the route needs a confirmation, CONTRACT.md 20).
type DeleteRecordedRequest struct {
	IDs []string `json:"ids"`
}

// ConfirmRequest is the body of POST /api/confirm (a built-in route of internal/web): the scope of the privileged action
// (CONTRACT.md section 20).
type ConfirmRequest struct {
	Scope string `json:"scope"`
}

// ConfirmID is the answer of POST /api/confirm: a single-use id for the scope, sent back as the X-Confirm header.
type ConfirmID struct {
	ID        string `json:"id"`
	Scope     string `json:"scope"`
	ExpiresIn int    `json:"expires_in"`
}

// TrustChallenge is the Detail of a 409 trust_required: what the person is asked to trust, and the confirmation id (issued for
// Scope) that the repeated request sends as X-Confirm.
type TrustChallenge struct {
	Dir     string      `json:"dir"`
	Files   []TrustFile `json:"files"`
	Digest  string      `json:"digest"`
	Changed string      `json:"changed,omitempty"`
	Partial bool        `json:"partial,omitempty"`
	Confirm string      `json:"confirm"`
	Scope   string      `json:"scope"`
}

// TrustFile is one file of a project's footprint.
type TrustFile struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
	Hash  string `json:"hash"`
}
```

`internal/web/wire/ws.go`:

```go
package wire

// WsIndex is a tab's Workspace history: its checkpoints in arrival order, the base, and the tree at the live edge. It is the
// real counterpart of the data pack's files.shop that 96b-ws-data.js read.
type WsIndex struct {
	Root      string            `json:"root"`
	Isolation string            `json:"isolation"`
	Base      WsBase            `json:"base"`
	Cps       []WsCheckpoint    `json:"cps"`
	Tree      []WsFile          `json:"tree"`
	Restore   *WsRestore        `json:"restore,omitempty"`
	Reverted  []WsRevert        `json:"reverted,omitempty"`
	Reviewed  map[string]string `json:"reviewed,omitempty"`
	Version   string            `json:"version"`
}

// WsBase is the state before the first checkpoint with files.
type WsBase struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Time  string `json:"time"`
}

// WsCheckpoint is one checkpoint with the change set it holds (what was written while it was current).
type WsCheckpoint struct {
	ID      string     `json:"id"`
	Time    string     `json:"time"`
	At      int64      `json:"at"`
	Label   string     `json:"label"`
	Skipped bool       `json:"skipped"`
	Safety  bool       `json:"safety,omitempty"`
	Files   []string   `json:"files"`
	Agents  []string   `json:"agents"`
	Tasks   []string   `json:"tasks"`
	Added   int        `json:"added"`
	Removed int        `json:"removed"`
	NFiles  int        `json:"nfiles"`
	Unsaved []string   `json:"unsaved,omitempty"`
	Changes []WsChange `json:"changes"`
}

// WsChange is one file of a checkpoint's change set.
type WsChange struct {
	Path    string   `json:"path"`
	Status  string   `json:"status"`
	Added   int      `json:"added"`
	Removed int      `json:"removed"`
	Agents  []string `json:"agents"`
	Task    string   `json:"task,omitempty"`
	Binary  bool     `json:"binary,omitempty"`
}

// WsFile is one row of the tree.
type WsFile struct {
	Path      string     `json:"path"`
	Dir       string     `json:"dir"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Status    string     `json:"status"`
	Owner     string     `json:"owner,omitempty"`
	Task      string     `json:"task,omitempty"`
	Cp        string     `json:"cp,omitempty"`
	Lease     *WsLease   `json:"lease,omitempty"`
	Protected *WsProtect `json:"protected,omitempty"`
	Ask       *WsAsk     `json:"ask,omitempty"`
	Add       int        `json:"add"`
	Del       int        `json:"del"`
	Size      int64      `json:"size"`
	Exists    bool       `json:"exists"`
}

// WsLease is a write lease (or a task scope) that covers a file.
type WsLease struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
	Glob  string `json:"glob"`
}

// WsProtect is a deny rule that refuses a file to agents.
type WsProtect struct {
	Rule   string `json:"rule"`
	Origin string `json:"origin"`
	Tier   string `json:"tier"`
	Why    string `json:"why"`
}

// WsAsk is an ask rule that makes every write to a file a question.
type WsAsk struct {
	Rule string `json:"rule"`
	Why  string `json:"why"`
}

// WsContent is a file's text at a point in time with its per-line authorship.
type WsContent struct {
	Path      string     `json:"path"`
	At        string     `json:"at"`
	Exists    bool       `json:"exists"`
	Text      string     `json:"text,omitempty"`
	Binary    bool       `json:"binary,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
	Size      int64      `json:"size"`
	Blame     []BlameRun `json:"blame"`
	Exact     bool       `json:"exact"`
}

// BlameRun says that Count lines from Line (1-based) were written by Ag in checkpoint ID for Task ("-": before the session).
type BlameRun struct {
	Line  int    `json:"line"`
	Count int    `json:"count"`
	Ag    string `json:"ag"`
	ID    string `json:"id,omitempty"`
	Task  string `json:"task,omitempty"`
}

// WsDiff is the difference of a file between two points in time, in the hunk format the Workspace draws.
type WsDiff struct {
	Path      string `json:"path"`
	From      string `json:"from"`
	To        string `json:"to"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	Hunks     []Hunk `json:"hunks"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Hunk is one hunk; its key in the page is OldStart:NewStart.
type Hunk struct {
	OldStart int        `json:"oldStart"`
	OldLines int        `json:"oldLines"`
	NewStart int        `json:"newStart"`
	NewLines int        `json:"newLines"`
	Section  string     `json:"section,omitempty"`
	Lines    []HunkLine `json:"lines"`
}

// HunkLine is one line of a hunk: T is " ", "+", "-" or "…" (an elided run), S its text.
type HunkLine struct {
	T string `json:"t"`
	S string `json:"s"`
}

// RevertRequest reverts one hunk of the diff of Path between From and To (checkpoint ids, "base" or "live").
type RevertRequest struct {
	Path string `json:"path"`
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}

// WsRevert is a hunk revert the person made (it can be undone while the file has not changed since).
type WsRevert struct {
	ID   string  `json:"id"`
	Path string  `json:"path"`
	Key  string  `json:"key"`
	At   float64 `json:"t"`
}

// RestoreRequest previews (DryRun) or applies /rewind ID.
type RestoreRequest struct {
	ID     string `json:"id"`
	DryRun bool   `json:"dryRun"`
}

// RestoreFile is one file of a restore: what happens to it and its lines.
type RestoreFile struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	To      string `json:"to"`
	Reason  string `json:"reason,omitempty"`
}

// RestorePlan is a restore's preview or result.
type RestorePlan struct {
	ID      string        `json:"id"`
	Label   string        `json:"label"`
	Time    string        `json:"time"`
	Files   []RestoreFile `json:"files"`
	Applied bool          `json:"applied"`
	Safety  string        `json:"safety,omitempty"`
	Summary string        `json:"summary"`
}

// WsRestore is the latest restore of the tab, while it can be undone.
type WsRestore struct {
	To     string   `json:"to"`
	Files  []string `json:"files"`
	At     float64  `json:"at"`
	Safety string   `json:"safety"`
}

// ReviewedRequest marks (On) or unmarks a file as reviewed at checkpoint Cp.
type ReviewedRequest struct {
	Path string `json:"path"`
	Cp   string `json:"cp"`
	On   bool   `json:"on"`
}

// Worktree is one worker's git worktree in an isolated team.
type Worktree struct {
	Agent  string   `json:"agent"`
	Path   string   `json:"path"`
	Branch string   `json:"branch"`
	Base   string   `json:"base"`
	Head   string   `json:"head,omitempty"`
	Dirty  bool     `json:"dirty"`
	Files  []string `json:"files,omitempty"`
}

// MergeQueueStatus is the merge queue of an isolated team now.
type MergeQueueStatus struct {
	Branch  string        `json:"branch"`
	Tip     string        `json:"tip"`
	Healthy bool          `json:"healthy"`
	Active  string        `json:"active,omitempty"`
	Phase   string        `json:"phase,omitempty"`
	Waiting []string      `json:"waiting"`
	Landed  []LandedEntry `json:"landed"`
	Verify  string        `json:"verify"`
}

// LandedEntry is a submission the queue merged.
type LandedEntry struct {
	Agent  string   `json:"agent"`
	Task   string   `json:"task"`
	Commit string   `json:"commit"`
	Files  []string `json:"files"`
}

// VerifyOutput is the output of the last verification of a task (a failure keeps its tail).
type VerifyOutput struct {
	Task     string `json:"task"`
	Cmd      string `json:"cmd"`
	ExitCode int    `json:"exitCode"`
	TimedOut bool   `json:"timedOut,omitempty"`
	Output   string `json:"output"`
}

// AcceptRequest commits the verified, merged work of the team onto the person's branch now.
type AcceptRequest struct {
	Message string `json:"message,omitempty"`
}

// AcceptResult is the commit that accept made.
type AcceptResult struct {
	Commit string   `json:"commit"`
	Files  []string `json:"files"`
	Branch string   `json:"branch"`
}
```

`internal/web/wire/settings.go`:

```go
package wire

// The shapes in this file mirror the data pack fields the Settings and Tools pages read (SLDATA.models, providers, mcp, skills,
// commands, hooks, config, permissions, trust, schedule, doctor), so that the pages read the same names from live data.

// ModelRow is one model of the catalogue. In, Out and Cached are US dollars per million tokens; nil means the price is unknown.
type ModelRow struct {
	Ref        string   `json:"ref"`
	Provider   string   `json:"provider"`
	Ctx        int      `json:"ctx"`
	In         *float64 `json:"in"`
	Out        *float64 `json:"out"`
	Cached     *float64 `json:"cached"`
	Tools      bool     `json:"tools"`
	Reasoning  bool     `json:"reasoning"`
	Fav        bool     `json:"fav"`
	PriceKnown bool     `json:"priceKnown"`
	Plan       bool     `json:"plan,omitempty"`
}

// ModelsView is the catalogue with the favourites and the sources that could not be read.
type ModelsView struct {
	Models     []ModelRow        `json:"models"`
	Favs       []string          `json:"favs"`
	Errors     []string          `json:"errors,omitempty"`
	Roles      []RoleInfo        `json:"roles"`
	RoleOrder  []string          `json:"roleOrder"`
	RoleModels map[string]string `json:"roleModels"`
	Efforts    []string          `json:"efforts"`
	FetchedAt  int64             `json:"fetchedAt"`
}

// RoleInfo is a role of the swarm: its name, short code, whether it is read-only, and what it is for.
type RoleInfo struct {
	Name string `json:"name"`
	Code string `json:"code"`
	RO   bool   `json:"ro"`
	Desc string `json:"desc"`
}

// FavRequest stars (On) or unstars a model.
type FavRequest struct {
	Ref string `json:"ref"`
	On  bool   `json:"on"`
}

// ProviderRow is a provider and whether a key is there; the key itself is never sent.
type ProviderRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Base        string `json:"base"`
	Key         string `json:"key"`
	Env         string `json:"env,omitempty"`
	State       string `json:"state"`
	Note        string `json:"note,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
	KeyWhere    string `json:"keyWhere,omitempty"`
	Dialect     string `json:"dialect,omitempty"`
	UsedBy      string `json:"usedBy,omitempty"`
	SignedIn    bool   `json:"signedIn,omitempty"`
	Who         string `json:"who,omitempty"`
}

// ProvidersView is the providers page.
type ProvidersView struct {
	Providers []ProviderRow `json:"providers"`
	Note      string        `json:"providerNote"`
}

// KeySaveRequest stores a provider key (write-only: it is never returned, logged or put in argv).
type KeySaveRequest struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Check    bool   `json:"check,omitempty"`
}

// SignInStart is the state of an asynchronous ChatGPT sign-in: the URL to open in this browser, and its id.
type SignInStart struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// SignInState is the progress of a ChatGPT sign-in.
type SignInState struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Who   string `json:"who,omitempty"`
	Error string `json:"error,omitempty"`
}

// MCPServer is a tool server as the MCP page shows it.
type MCPServer struct {
	Name      string   `json:"name"`
	Origin    string   `json:"origin"`
	From      string   `json:"from,omitempty"`
	Transport string   `json:"transport"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Server    string   `json:"server,omitempty"`
	Describe  string   `json:"describe,omitempty"`
	Error     string   `json:"error,omitempty"`
	Note      string   `json:"note,omitempty"`
	EnvRefs   []string `json:"envRefs,omitempty"`
	Approved  bool     `json:"approved,omitempty"`
}

// MCPView is the MCP page.
type MCPView struct {
	Servers     []MCPServer `json:"servers"`
	SessionNote string      `json:"sessionNote"`
	FrozenTools []string    `json:"frozenTools,omitempty"`
}

// MCPResult is the outcome of approve, revoke, test or reconnect: one line for the card, and its tone.
type MCPResult struct {
	T   string `json:"t"`
	Cls string `json:"cls"`
}

// Skill is a skill the model can load.
type Skill struct {
	Name         string `json:"name"`
	Summary      string `json:"summary"`
	Source       string `json:"source"`
	Scope        string `json:"scope"`
	YouOnly      bool   `json:"youOnly"`
	Note         string `json:"note,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
}

// CustomCommand is a markdown command of the user or the project.
type CustomCommand struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	Source       string `json:"source"`
	Scope        string `json:"scope"`
}

// Hook is a configured hook (the command line is shown; headers and environment values are not).
type Hook struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
	Origin  string `json:"origin"`
	Purpose string `json:"purpose"`
}

// SkillsView is the Skills, commands and hooks page.
type SkillsView struct {
	Skills       []Skill         `json:"skills"`
	SkillsBudget *SkillsBudget   `json:"skillsBudget,omitempty"`
	Commands     []CustomCommand `json:"commands"`
	Hooks        *HooksView      `json:"hooks,omitempty"`
}

// SkillsBudget is what the skills listing costs in the shared layer.
type SkillsBudget struct {
	Tokens int    `json:"tokens"`
	Used   int    `json:"used"`
	Note   string `json:"note"`
}

// HooksView lists hooks and the events a hook can attach to.
type HooksView struct {
	Events     []string `json:"events"`
	Configured []Hook   `json:"configured"`
}

// ConfigLayer is one configuration layer.
type ConfigLayer struct {
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	State   string `json:"state"`
	Trusted any    `json:"trusted,omitempty"`
}

// ConfigValue is an effective configuration key, its (redacted) value and the layer that supplied it.
type ConfigValue struct {
	Key   string            `json:"key"`
	Value any               `json:"value"`
	Layer string            `json:"layer"`
	File  string            `json:"file"`
	Note  string            `json:"note,omitempty"`
	Below map[string]string `json:"below,omitempty"`
}

// ConfigView is the Config layers page.
type ConfigView struct {
	Layers     []ConfigLayer `json:"layers"`
	Precedence string        `json:"precedence"`
	Effective  []ConfigValue `json:"effective"`
	Warnings   []string      `json:"warnings,omitempty"`
}

// PermMode is a permission mode with its one-line description.
type PermMode struct {
	ID     string `json:"id"`
	Danger bool   `json:"danger"`
	Cycle  bool   `json:"cycle"`
	Text   string `json:"text"`
}

// PermissionsView is the Permissions page: modes, the order rules are judged in, the manager's rule, the tests preset and the
// rules in force grouped by effect, each with its origin.
type PermissionsView struct {
	Mode          string            `json:"mode"`
	Modes         []PermMode        `json:"modes"`
	Order         []string          `json:"order"`
	ManagerWrites ManagerWrites     `json:"managerWrites"`
	TestsPreset   TestsPreset       `json:"testsPreset"`
	Rules         map[string][]Rule `json:"rules"`
	Session       []Rule            `json:"session"`
}

// ManagerWrites is what a manager's write is refused with.
type ManagerWrites struct {
	Text string `json:"text"`
	Note string `json:"note"`
}

// TestsPreset is the tests preset.
type TestsPreset struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Rules   []string `json:"rules"`
}

// TrustProject is the trust state of the active tab's directory.
type TrustProject struct {
	Dir      string `json:"dir"`
	State    string `json:"state"`
	SavedDay string `json:"savedDay,omitempty"`
	Digest   string `json:"digest"`
	Unlocks  string `json:"unlocks"`
	Partial  bool   `json:"partial,omitempty"`
}

// TrustLedgerRow is a directory the person decided about.
type TrustLedgerRow struct {
	Dir   string `json:"dir"`
	Saved string `json:"saved"`
	Files int    `json:"files"`
	Now   string `json:"now"`
	State string `json:"state"`
}

// TrustView is the Trust page.
type TrustView struct {
	Project  TrustProject     `json:"project"`
	Files    []TrustFile      `json:"files"`
	Covers   string           `json:"covers"`
	Ledger   []TrustLedgerRow `json:"ledger"`
	Question struct {
		Options []string `json:"options"`
	} `json:"question"`
}

// TrustRequest trusts (On: the route needs the X-Confirm id of the trust challenge) or forgets a directory.
type TrustRequest struct {
	Dir string `json:"dir"`
	On  bool   `json:"on"`
}

// ScheduleJob is a scheduled goal.
type ScheduleJob struct {
	ID        string  `json:"id"`
	Cron      string  `json:"cron"`
	Goal      string  `json:"goal"`
	Dir       string  `json:"dir"`
	Model     string  `json:"model"`
	Mode      string  `json:"mode"`
	BudgetUSD float64 `json:"budgetUsd"`
	Created   string  `json:"created"`
	LastRun   string  `json:"lastRun"`
	LastExit  string  `json:"lastExit"`
	Log       string  `json:"log,omitempty"`
	Next      string  `json:"next"`
	Paused    bool    `json:"paused,omitempty"`
}

// DaemonState is the schedule daemon: running here, running elsewhere (another process holds the lock), or stopped.
type DaemonState struct {
	Running bool   `json:"running"`
	Owner   string `json:"owner"`
	PID     int    `json:"pid,omitempty"`
	Every   string `json:"every"`
	Timeout string `json:"timeout"`
	Line    string `json:"line"`
}

// JobLog is a job's log file (text capped).
type JobLog struct {
	Job  string `json:"job"`
	File string `json:"file"`
	Exit string `json:"exit"`
	Text string `json:"text"`
}

// ScheduleView is the Schedule page.
type ScheduleView struct {
	Jobs   []ScheduleJob `json:"jobs"`
	Daemon DaemonState   `json:"daemon"`
	Logs   []JobLog      `json:"logs"`
}

// JobRequest adds or edits a job (bypass and yolo are refused).
type JobRequest struct {
	Cron      string  `json:"cron"`
	Goal      string  `json:"goal"`
	Dir       string  `json:"dir"`
	Model     string  `json:"model,omitempty"`
	Mode      string  `json:"mode,omitempty"`
	BudgetUSD float64 `json:"budgetUsd"`
}

// CronCheck is the validation of a cron expression and its next run.
type CronCheck struct {
	OK   bool   `json:"ok"`
	Next string `json:"next,omitempty"`
	Err  string `json:"error,omitempty"`
}

// DoctorEndpoint is an endpoint the Doctor page can probe.
type DoctorEndpoint struct {
	Ref   string `json:"ref"`
	Where string `json:"where"`
}

// UpdateStatus is the result of an update check.
type UpdateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	Notes     string `json:"notes,omitempty"`
}
```

`internal/web/wire/runner.go`:

```go
package wire

// CLISpec is every `sleipnir` command with its flags, as the runner builds forms from it (the shape of the mock's cli-spec.json).
type CLISpec struct {
	GeneratedFrom string       `json:"generatedFrom"`
	Generator     string       `json:"generator"`
	Commands      []CLICommand `json:"commands"`
	ChatSlash     []SlashEntry `json:"chatSlash"`
	ExitCodes     []ExitCode   `json:"exitCodes"`
}

// CLICommand is one command path with its usage, summary, positionals and flags; Mode says how the web runs it.
type CLICommand struct {
	Path       []string        `json:"path"`
	Usage      string          `json:"usage"`
	Summary    string          `json:"summary"`
	Positional []CLIPositional `json:"positional"`
	Flags      []CLIFlag       `json:"flags"`
	Source     string          `json:"source,omitempty"`
	Mode       string          `json:"mode"`
	Why        string          `json:"why,omitempty"`
}

// CLIPositional is a positional argument.
type CLIPositional struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// CLIFlag is one flag: Arg is bool, string, int, uint, float or duration.
type CLIFlag struct {
	Name        string `json:"name"`
	Arg         string `json:"arg"`
	Default     any    `json:"default"`
	DefaultNote string `json:"defaultNote,omitempty"`
	Repeatable  bool   `json:"repeatable"`
	Desc        string `json:"desc"`
}

// SlashEntry is a chat slash command for the / menu and the palette.
type SlashEntry struct {
	Cmd         string `json:"cmd"`
	Args        string `json:"args"`
	Desc        string `json:"desc"`
	Group       string `json:"group"`
	PaletteArgs string `json:"paletteArgs,omitempty"`
	PaletteDesc string `json:"paletteDesc,omitempty"`
	Custom      bool   `json:"custom,omitempty"`
}

// ExitCode is one exit status of the program and what it means.
type ExitCode struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

// RunRequest runs a command: Path, positional values by name, flag values by name (strings, numbers, booleans; repeatable flags
// as arrays), and the tab whose directory it runs in (a privileged command also needs an X-Confirm id, CONTRACT.md 20).
type RunRequest struct {
	Path  []string          `json:"path"`
	Pos   map[string]string `json:"pos,omitempty"`
	Flags map[string]any    `json:"flags,omitempty"`
	Tab   string            `json:"tab,omitempty"`
}

// RunStarted is the id of a started run and its command line as the person would type it.
type RunStarted struct {
	ID      string `json:"id"`
	Cmdline string `json:"cmdline"`
}

// RunLine is one line of output: K is out, err, dim, head, ok, warn or bad.
type RunLine struct {
	K string `json:"k"`
	T string `json:"t"`
}

// RunCard is the result card: a title and labelled values.
type RunCard struct {
	Title string      `json:"title"`
	Rows  [][2]string `json:"rows"`
}

// RunResult is how a run ended.
type RunResult struct {
	Exit     int      `json:"exit"`
	Ms       int64    `json:"ms"`
	Card     *RunCard `json:"card,omitempty"`
	Canceled bool     `json:"canceled,omitempty"`
}

// DoctorStep is one request of a doctor probe as the Doctor page draws it.
type DoctorStep struct {
	OK     bool   `json:"ok"`
	Name   string `json:"name"`
	Ms     int64  `json:"ms"`
	Grp    string `json:"grp"`
	In     int    `json:"in"`
	Cached int    `json:"cached"`
	Out    int    `json:"out"`
}

// DoctorVerdict is the end of a doctor probe: what the endpoint does, warnings, and the summary card.
type DoctorVerdict struct {
	KV   [][2]string `json:"kv"`
	Warn []string    `json:"warn,omitempty"`
	Card *RunCard    `json:"card,omitempty"`
}

// RunFrame is the data of a "run" frame.
type RunFrame struct {
	ID      string         `json:"id"`
	Lines   []RunLine      `json:"lines,omitempty"`
	Step    *DoctorStep    `json:"step,omitempty"`
	Verdict *DoctorVerdict `json:"verdict,omitempty"`
	Result  *RunResult     `json:"result,omitempty"`
}

// RunInfo is a run in the recent-runs list.
type RunInfo struct {
	ID      string         `json:"id"`
	Path    []string       `json:"path"`
	Flags   map[string]any `json:"flags,omitempty"`
	Cmdline string         `json:"cmd"`
	Exit    *int           `json:"exit,omitempty"`
	Ms      int64          `json:"ms"`
	Running bool           `json:"running"`
}
```

### 5.2 `internal/web/seam/seam.go`

```go
// Package seam holds the Go interfaces between the builders of `sleipnir web`: the session host (cmd/sleipnir), the translator
// (internal/web/translate) and the route packages (internal/web/wsvc, settings, runner). internal/web itself knows nothing about
// sessions; these interfaces do, so they live here.
package seam

import (
	"context"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Topic is the hub topic of the page's stream: every tab's frames and the global ones (one connection per page).
const Topic = "ui"

// Host is the set of live tabs, as the route packages see it. cmd/sleipnir implements it (B1).
type Host interface {
	// Tabs lists the live tabs in strip order.
	Tabs() []wire.TabSummary
	// Tab finds a live tab by its id.
	Tab(id string) (Tab, bool)
	// Active is the tab the page should show first.
	Active() string
	// Projects lists the directories a new session may start in.
	Projects(ctx context.Context) []wire.Project
	// Questions lists the open questions of every tab, oldest first.
	Questions() []wire.OpenQuestion
	// Publish sends a frame to every page (the host's adapter onto web.Hub.Publish(Topic, ...)).
	Publish(f wire.Frame)
}

// Tab is one live tab: a hosted session and its journal. All methods are safe for concurrent use; methods that change the
// session run one at a time per tab (the host serialises them with the running turn as the TUI does).
type Tab interface {
	// Summary describes the tab.
	Summary() wire.TabSummary
	// Snapshot returns the tab's state for a late joiner.
	Snapshot(ctx context.Context) (wire.TabSnapshot, error)
	// Send delivers a message now, or queues it behind the running turn.
	Send(ctx context.Context, req wire.MessageRequest) (wire.SendResult, error)
	// Command runs a slash line the page has no handler for.
	Command(ctx context.Context, req wire.CommandRequest) (wire.CommandResult, error)
	// Steer tells the running turn of the manager something without stopping it.
	Steer(ctx context.Context, text string) error
	// Interrupt cancels the running turn (target "turn"); the goal pauses.
	Interrupt(ctx context.Context, target string) error
	// Rename changes the tab's name (kept in the session's sidecar).
	Rename(ctx context.Context, name string) error
	// Restart is the restart family (/new, /clear, /swarm, /restart, a team's model or role change).
	Restart(ctx context.Context, req wire.RestartRequest) error
	// Goal sets, pauses, resumes or clears the standing goal.
	Goal(ctx context.Context, req wire.GoalRequest) error
	// SetMode changes the permission mode (the route checked the confirmation for bypass and yolo).
	SetMode(ctx context.Context, req wire.ModeRequest) error
	// SetModel changes the manager's model or a role's.
	SetModel(ctx context.Context, req wire.ModelRequest) error
	// SetEffort changes the reasoning effort.
	SetEffort(ctx context.Context, req wire.EffortRequest) (wire.EffortResult, error)
	// SetBudget changes the budget.
	SetBudget(ctx context.Context, req wire.BudgetRequest) error
	// Compact folds the manager's thread now.
	Compact(ctx context.Context, req wire.CompactRequest) error
	// StageLaunch stages flags for the next start of the team.
	StageLaunch(ctx context.Context, p wire.LaunchPatch) error
	// AddRule adds a session rule (allow, deny, ask; "tests" expands).
	AddRule(ctx context.Context, req wire.RuleRequest) (wire.RuleResult, error)
	// RemoveRule removes a session rule.
	RemoveRule(ctx context.Context, req wire.RuleRequest) (wire.RuleResult, error)
	// Rules lists the rules in force for the tab with their origins (configuration and this session).
	Rules(ctx context.Context) ([]wire.Rule, error)
	// Slash lists the slash commands of the tab: the built-ins, then its custom commands, skills and MCP prompts.
	Slash(ctx context.Context) ([]wire.SlashEntry, error)
	// Access gives the workspace and settings routes the current harness session of the tab.
	Access() SessionAccess
}

// SessionAccess is what the workspace and settings routes may use of a tab's session. The session changes on a restart, so the
// routes ask for it each time and never keep it.
type SessionAccess interface {
	// TabID names the tab.
	TabID() string
	// Session returns the current harness session (nil while the tab restarts).
	Session() *session.Session
	// Busy says whether a turn runs or any agent of the team is working.
	Busy() bool
	// Exclusive runs fn while no turn starts and no command of the tab runs; it fails with busy when a turn is running.
	Exclusive(ctx context.Context, fn func(s *session.Session) error) error
	// Emit adds host-originated UI events to the tab's journal (acknowledgment rows, checkpoint updates).
	Emit(evs ...wire.Event)
	// Notify sends a note to the agents that touched files, as /rewind does after a restore.
	Notify(text string)
	// Meta patches the tab's meta and publishes it.
	Meta(p wire.MetaPatch)
}

// Translator turns one tab generation's harness activity into UI events, keeps them in the tab's journal and publishes them
// (B2). The host (B1) creates one per generation with translate.New and installs its sinks before session.New.
type Translator interface {
	// Sink is the agent.Sink to install as session.Options.Sink.
	Sink() agent.Sink
	// NewSink is the per-agent sink constructor for session.Options.NewSink.
	NewSink(agentID string) agent.Sink
	// Attach follows a session's log until the returned function is called; the history of an existing log (VOCAB.md section
	// 12) is translated first.
	Attach(log *events.Log, dir string) (detach func())
	// Emit appends host-originated events (stamped with the current session time) and publishes them.
	Emit(evs ...wire.Event)
	// Question records an open question and emits its ask event.
	Question(q wire.Question)
	// Answered closes a question and emits its answer event.
	Answered(a wire.Answer)
	// Roster is the tab's roster now.
	Roster() []wire.RosterEntry
	// Journal returns the keyframe and the retained events (both encoded), the last seq and the session time now.
	Journal() (keyframe, events []wire.Raw, seq uint64, now float64)
	// Now is the session time now, in seconds.
	Now() float64
	// Close stops the translator; later calls do nothing.
	Close()
}
```

### 5.3 How a frame reaches the hub

B1 implements `seam.Host.Publish` with `publishFrame` (6.1): `web.Event{Type: f.Type, Data: json(f.Data), Critical: f.Critical, Coalescable: f.Coalescable, Key: f.Key}` published with `srv.Hub().Publish(seam.Topic, ev)`. The translator (B2) builds `ev` frames with the class of VOCAB.md 14; B1 builds `meta`, `roster`, `tab`, `reset`, `toast`, `ping`, `bye`; B4 builds `run` and `recorded`.

### 5.4 The registration functions (one per route package; B1 calls them from `webHost`)

`internal/web/wsvc/wsvc.go`:

```go
// Package wsvc serves the Workspace routes of `sleipnir web` (B3).
package wsvc

import (
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
)

// Register adds the Workspace routes (CONTRACT.md section 12) to srv, resolving tabs through h.
func Register(srv *web.Server, h seam.Host) {}
```

`internal/web/settings/settings.go`:

```go
// Package settings serves the Settings, recorded-session, schedule, provider and doctor routes of `sleipnir web` (B4).
package settings

import (
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
)

// Options configure the settings routes: the user's home, the program version (update checks) and the executable (runs).
type Options struct {
	Home    string
	Version string
	Self    string
}

// Register adds the routes of CONTRACT.md sections 7, 13 to 17 and 19 to srv, resolving tabs through h.
func Register(srv *web.Server, h seam.Host, o Options) {}
```

`internal/web/runner/runner.go`:

```go
// Package runner serves the command runner routes of `sleipnir web` (B4).
package runner

import (
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
)

// Options configure the runner: the executable to run and the environment of commands that call a provider.
type Options struct {
	Self string
	Env  func() []string
}

// Register adds the routes of CONTRACT.md section 18 to srv, resolving tabs through h; output goes to h.Publish.
func Register(srv *web.Server, h seam.Host, o Options) {}
```


## 6. Harness extensions per builder (signatures)

Each block is the exported API a builder adds to a core package, with the doc comment it must carry. Bodies are the builder's. Other
builders code against these signatures; until the owner lands them, a caller uses a local stub with the same signature behind a
build-time `TODO(owner)` and removes it at integration.

### 6.1 B1

```go
// package session (internal/session/web_access.go and session.go)

// Options returns a copy of the options the session was built with (an in-process restart rebuilds a session from them);
// Sink, NewSink and Prompter are cleared in the copy.
func (s *Session) Options() Options

// Root is the project root the session works in.
func (s *Session) Root() string

// Turn reports how many turns the session has run.
func (s *Session) Turn() int

// JudgeGoal (existing, internal/session/goal.go:40) additionally emits the log event "goal.judge"
// {"verdict": v.Kind, "reason": v.Reason, "left": v.Left} after a judge answered.
```

```go
// package main (cmd/sleipnir/chatflags.go, web_goal.go): shared by `sleipnir chat` and `sleipnir web`

// chatFlags is a parsed `sleipnir chat` command line.
type chatFlags struct{ /* every flag of cmdChat, unexported fields */ }

// parseChatFlags parses chat arguments with the same defaults, validation and messages as `sleipnir chat` (ContinueOnError).
func parseChatFlags(args []string) (chatFlags, error)

// options turns parsed flags into session options for an interactive session (chatOptions, chat.go:244).
func (f chatFlags) options() session.Options

// argsFor renders the New session dialog's request as chat arguments, in the order the dialog's command line shows them.
func argsFor(req wire.NewSessionRequest) []string

// goalLoop runs a standing goal across turns: judge after each turn, continue, pause on cancel, failure or a judge error
// (sessionHost.Turn and judgeGoal, chat_tty.go:345-372, moved).
type goalLoop struct{ /* goal *goal.State and its lock */ }

// Turn runs one turn of prompt and, while the goal continues, its continuations; it reports through emit and returns the last error.
func (g *goalLoop) Turn(ctx context.Context, s *session.Session, prompt string, emit func(goalEvent)) error
```

```go
// package approvals (internal/web/approvals)

// Config configures the bridge: the quiet floor (350 ms), the grace with no page connected (--ask-grace), the clock, and the
// callbacks that report questions and answers to the tab's translator.
type Config struct {
	Floor    time.Duration
	Grace    time.Duration
	Now      func() time.Time
	OnAsk    func(tab string, q wire.Question)
	OnAnswer func(tab string, a wire.Answer)
}

// New returns a bridge.
func New(cfg Config) *Bridge

// Prompter is the perm.Prompter of one tab; describe gives an agent's task and scope for the question's text; starting reports
// whether the tab's session is still in session.New (trust and MCP questions are refused then, CONTRACT.md 6).
func (b *Bridge) Prompter(tab string, root string, describe func(agent string) (task, scope string), starting func() bool) perm.Prompter

// Open lists the open questions of every tab, oldest first.
func (b *Bridge) Open() []wire.OpenQuestion

// Answer resolves a question; it enforces single use and the 350 ms floor (errors are *wire.Error).
func (b *Bridge) Answer(ctx context.Context, qid string, req wire.AnswerRequest) (wire.AnswerResult, error)

// CancelTab refuses every open question of a tab (by: "closed" or "canceled").
func (b *Bridge) CancelTab(tab, by string)

// CancelAgent refuses the open questions of one agent of a tab (an interrupt refuses the manager's).
func (b *Bridge) CancelAgent(tab, agent, by string)

// Connected tells the bridge how many pages have a stream open now (the --ask-grace clock).
func (b *Bridge) Connected(n int, at time.Time)
```

```go
// package main (cmd/sleipnir/web_host.go)

// publishFrame hands a wire.Frame to the hub on the page topic: the data encoded as JSON, the frame's class carried over
// (Critical, Coalescable and Key, VOCAB.md 14). It is the implementation of seam.Host.Publish.
func publishFrame(h *web.Hub, f wire.Frame) error
```

### 6.2 B2

```go
// package translate (internal/web/translate)

// Config configures one tab generation's translator.
type Config struct {
	Tab       string
	Gen       uint64
	Root      string
	StartedAt time.Time
	// Publish sends frames to the pages (seam.Host.Publish); it never blocks.
	Publish func(wire.Frame)
	Now     func() time.Time
	Limits    Limits
	// Verify returns the session's --verify command and whether the team is isolated (queue texts).
	Verify func() (cmd string, isolated bool)
}

// Limits bound the journal and the sink queue (VOCAB.md section 14); the zero value means the defaults.
type Limits struct {
	JournalEvents int
	JournalBytes  int
	SinkQueue     int
	History       int
}

// New returns a translator for one tab generation; it implements seam.Translator.
func New(cfg Config) *Translator
```

```go
// package state (internal/tui/state)

// Stall is a supervision finding of the swarm (swarm.stall), open until cleared.
type Stall struct {
	Kind, Agent, Task, Detail string
	Raised                    time.Time
}

// Snapshot gains: Stalls []Stall; Handovers []Handover (the last 32); Goal *Goal (the latest goal.state and goal.judge);
// Task gains Closure string and BlockedOn string; Agent gains nothing.

// OnStatus registers fn, called after each applied event for every agent whose Status changed (the gantt segments of the page).
func (s *State) OnStatus(fn func(agent string, from, to Status, at time.Time))

// LayerSplit splits an agent's latest prompt into G0..G5 tokens as the terminal's stack bar does (G6 folded into G5); g0 is the
// constitution estimate. Moved from internal/tui/app/cacheview.go promptLayers.
func LayerSplit(a Agent, g0 int) [6]int
```

```go
// package inspect (internal/inspect/explain.go)

// AnomalyTitle is the one-line explanation of a cache anomaly kind, as the inspector titles it.
func AnomalyTitle(kind string) string
```

### 6.3 B3

```go
// package perm

// Request gains:
//	// Why is the reason the engine asks (the verdict's reason), set on a question; Summary keeps its "[reason]" suffix for the TUI.
//	Why string `json:"why,omitempty"`
//	// RememberRules are the rules a "don't ask again" answer would add, set on a question.
//	RememberRules []string `json:"remember_rules,omitempty"`

// DeclinedWith is the refusal reason of a person's "no" with an instruction: the engine's advice not to work around the refusal,
// then "The person says: " and the note (empty note: "denied by user").
func DeclinedWith(note string) string

// Classification is what the engine would do with a request, without asking anyone.
type Classification struct {
	Verdict Action // Allow, Deny or Ask
	Rule    string // the rule that decided, "" when the mode's default did
	Origin  string // where the rule came from: built-in protection, user config, project config, this session, flag
	Tier    string // hard (no mode lifts it), guarded, rule, mode
	Why     string // one sentence
}

// Classify judges a request in the engine's order (hard denies, ask rules, high-risk shell, allow rules, the mode) and never prompts.
func (e *Engine) Classify(r Request) Classification

// RemoveRule removes a session-scope rule; it reports whether one was removed. Rules from configuration cannot be removed.
func (e *Engine) RemoveRule(rule string) bool
```

```go
// package checkpoint

// OnChange registers fn, called (outside the store's lock) when a checkpoint begins and when its file set changes.
func (s *Store) OnChange(fn func(Info))

// Info gains Added and Removed (lines, over the checkpoint's own change set).

// ContentAt returns a file's content as it was when checkpoint id began: the earliest pre-image among checkpoints at or after id,
// else the current file; exists is false when the file did not exist then.
func (s *Store) ContentAt(id, path string) (data []byte, exists bool, err error)

// Change is one file of a checkpoint's own change set.
type Change struct {
	Path, Status    string
	Added, Removed  int
	Agents          []string
	Binary          bool
}

// Changes returns the change set of one checkpoint (what was written while it was current), not the cumulative diff of Diff.
func (s *Store) Changes(id string) ([]Change, error)

// Write is one recorded tool write: which checkpoint, agent and time, and the blob of the content after it.
type Write struct {
	Checkpoint, Agent, Blob string
	At                      time.Time
}

// Writes returns the recorded writes of a file in order (the write journal, for exact authorship).
func (s *Store) Writes(path string) []Write

// CaptureUndo saves the current state of paths in the undo store and returns its id (taken before a restore or a revert).
func (s *Store) CaptureUndo(paths []string) (id string, err error)

// RestoreUndo puts back the state an undo capture saved, with Restore's conflict rules.
func (s *Store) RestoreUndo(id string) (RestoreReport, error)
```

```go
// package gitx

// BlameLine is the commit and author of one line.
type BlameLine struct {
	Line           int
	Commit, Author string
}

// Blame runs git blame on path at rev (added to the allowlist of Repo.Git).
func (r *Repo) Blame(ctx context.Context, rev, path string) ([]BlameLine, error)

// CommitPaths commits only paths (a path-limited commit), refusing conflict markers as CommitAll does.
func (r *Repo) CommitPaths(ctx context.Context, msg string, author Author, paths []string) (string, error)
```

```go
// package session (internal/session/workspace_access.go)

// MergeQueue is the isolated team's merge queue (nil without isolation).
func (s *Session) MergeQueue() *workspace.Queue

// Worktrees is the isolated team's worktree manager (nil without isolation).
func (s *Session) Worktrees() *workspace.Manager

// LastWriter names the agent that last wrote path through a file tool in this session.
func (s *Session) LastWriter(path string) (agent string, ok bool)

// AcceptVerified commits the verified, merged result onto the person's branch now (a clean checkout and an unmoved branch are
// required), deferring the automatic apply at close for what it committed.
func (s *Session) AcceptVerified(ctx context.Context, msg string) (commit string, files []string, err error)
```

```go
// package tools

// LastWriter names the agent that last wrote path (FileState's lastWriter, tools/support.go:139-231).
func (f *FileState) LastWriter(path string) (agent string, ok bool)
```

### 6.4 B4

```go
// package catalog (internal/catalog, new; moved from cmd/sleipnir/models.go)

// Row is one model of the catalogue; nil prices are unknown.
type Row struct {
	Ref, Provider            string
	Context                  int
	In, Out, Cached          *float64
	Tools, Reasoning, Plan   bool
}

// Options say which catalogues to read.
type Options struct {
	Home, Cwd string
	Config    *config.Config
	Refresh   bool
	Offline   bool
}

// Fetch reads the catalogues of the usable providers, local servers and the ChatGPT plan (cached 6 h unless Refresh).
func Fetch(ctx context.Context, o Options) ([]Row, []error)

// Favorites is the set of starred models of a configuration.
func Favorites(cfg *config.Config) map[string]bool

// ToggleFavorite stars or unstars a model in the user's configuration and reports whether it is starred now.
func ToggleFavorite(home, ref string) (bool, error)
```

```go
// package session (internal/session/listing.go, meta.go)

// StateRoot is the state directory: $SLEIPNIR_HOME, else ~/.sleipnir (the one copy the commands share).
func StateRoot(home string) string

// Recorded is a session directory as lists show it.
type Recorded struct {
	ID, Dir, First, Model, Cwd, Name string
	CostUSD                          float64
	Bytes                            int64
	LastWritten                      time.Time
	Duration                         time.Duration
	Agents                           int
	Resumable, Interrupted, Locked   bool
}

// ListRecorded lists the session directories, newest first.
func ListRecorded(home string, now time.Time) ([]Recorded, error)

// PrunePlan is what `sessions prune` deletes.
type PrunePlan struct {
	Delete []Recorded
	Bytes  int64
}

// PlanPrune applies the prune rule (older than, not among the newest keep, not written in the last ten minutes; live directories
// count toward keep and are never deleted).
func PlanPrune(home string, now time.Time, olderThan time.Duration, keep int, live []string) (*PrunePlan, error)

// ParseAge reads 30d, 36h, 2w or 0.
func ParseAge(s string) (time.Duration, error)

// Locked reports whether another process (or this one) holds a session directory's lock.
func Locked(dir string) bool

// Meta is the web sidecar of a session directory (web.json): its name and the person's reviewed marks.
type Meta struct {
	Name     string            `json:"name,omitempty"`
	Reviewed map[string]string `json:"reviewed,omitempty"`
}

// LoadMeta reads a session's sidecar (missing: the zero Meta).
func LoadMeta(dir string) (Meta, error)

// UpdateMeta changes a session's sidecar atomically (one writer per directory in this process).
func UpdateMeta(dir string, fn func(*Meta)) error
```

```go
// package sched

// Job gains Paused bool `json:"paused,omitempty"` (Due skips a paused job).

// Update changes one job and saves the file.
func (s Store) Update(id string, fn func(*Job)) (Job, error)

// Lock takes the daemon lock next to the job file; pid is the holder's when another process has it.
func Lock(path string) (unlock func(), pid int, err error)

// Tick starts every due job, one at a time, recording the start before the run (daemonTick, moved).
func Tick(ctx context.Context, st Store, now time.Time, launch func(context.Context, Job, time.Time) (exit, log string), logw io.Writer) error

// RunJob runs one job as a child process of self, writing its log, and says how it ended (runJob, moved); out also receives the
// output when not nil.
func RunJob(ctx context.Context, self, logs string, j Job, now time.Time, env []string, out io.Writer) (exit, log string)

// JobEnv is env with the held provider keys put back for a child Sleipnir (jobEnv, moved).
func JobEnv(env []string) []string
```

```go
// package config

// Redact returns the configuration as a JSON-ready map with secret values replaced by "(set, not shown)" (an allow-list over
// providers, hooks and MCP entries, plus harden.LooksSecret on every string).
func Redact(cfg *Config) map[string]any

// RuleOrigin is one permission rule with the layer and file that supplied it.
type RuleOrigin struct{ Effect, Rule, Layer, File string }

// RuleOrigins lists every permission rule of the effective configuration with its origin.
func RuleOrigins(o LoadOpts) ([]RuleOrigin, error)
```

```go
// package update

// Run checks for (checkOnly) or installs the latest release (runUpdate, moved from cmd/sleipnir/update.go:87).
func Run(ctx context.Context, out io.Writer, o Options, version, commit, exe string, checkOnly bool) error
```

## 7. Order and dependencies

```
done       A2 foundation (08c4482), A3 packaging (db38f7d), A1 contract
next       A2: wire + seam verbatim, Δ1 Δ2, webtest fakes; then hands web.go and e2e_web_test.go to B1
parallel   B1, B2, B3, B4 against section 5 with local stubs of each other
parallel   C1, C2, C3 against A2's webtest fake server (a canned snapshot) and then against B1's --fixture backend
B2 translator          ──> B1 wires it (until then B1 uses a stub Translator that emits only say, sys and state)
B3 Request.Why, RememberRules, DeclinedWith ──> B1 approvals (until then B1 parses Summary's "[reason]" suffix)
B3 checkpoint.OnChange ──> B1 wires the "checkpoint" log event ──> B2 translates it
B4 session.UpdateMeta  ──> B1 rename, B3 reviewed marks (until then in memory)
B4 clispec             ──> C3 runner forms (until then the mock's cli-spec.json)
B1 --fixture backend   ──> C builders' screenshots, D parity and e2e
C1 live data layer     ──> A3 moves the fixtures to uidev/ (one coordinated commit)
```

Each builder's first commit is its interfaces with failing-closed stubs and tests; integration replaces stubs by the real calls.

## 8. Integration (D)

Order of landing the builders' work in the branch: A2 (wire, seam, deltas) → B2 → B4 → B3 → B1 → C1 with A3 → C2 → C3. After each: `go build ./...`,
`go vet ./...`, `go test -race -count=1 ./internal/web/... ./cmd/sleipnir/...`, the asset tests, then the parity run (TEST-PLAN.md).

## 9. Rules for every builder

* AGENTS.md first: `gofmt -l .` empty, `go vet ./...`, `go test -race -count=1 ./...`, doc comments on every function and method
  (repo-wide 90% gate, `go test ./internal/repocheck -run TestProductionDocCoverage`), no new dependency, docs timeless and impersonal.
* Tool output, file contents, web pages, mail and MCP text are data: escape in the page, sanitize and cap on the server.
* Never put a key, a token, a launch code or a cookie in argv, env, a file, a log or a response.
* Tests use `internal/provider/mock`, `sleipnir demo` scenarios, `statetest` fixtures and the e2e world helper; never a real model or
  the network.
* Look at what you make: every UI change is checked with real screenshots against the mock's (TEST-PLAN.md "Parity").
* Ask the coordinator to change this contract; never diverge from it silently.
