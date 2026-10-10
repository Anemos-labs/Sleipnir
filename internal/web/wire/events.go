// Package wire holds the JSON shapes of `sleipnir web`: the events of a session that the page's reducer consumes, the frames of
// the page's event stream, and the request and response bodies of the HTTP API (docs/WEB-API.md lists the routes and the stream that
// carry them). It imports nothing from the harness, so every package of the web interface can depend on it.
//
// Field names are the json tags. An optional field that has no value is omitted, never sent as null, unless its comment says that
// null is meaningful. Times are seconds unless a name ends in Ms or At (epoch milliseconds). Text that came from a model, a tool, a
// file or a tool server is sent as a plain string, sanitized for display (terminal controls, bidirectional and invisible characters
// removed), masked where it looks like a secret and cut to the caps noted on the fields; no event carries markup, and the page
// escapes every string it shows.
package wire

import "encoding/json"

// Base is the head every event carries. T is the session time in seconds (three decimals, never negative, non-decreasing in Seq
// order within a tab), K the kind, Seq the sequence number of the event in the tab's journal (it starts at 1 in each generation of the
// tab, rises by one per event and is absent from the synthetic events of a keyframe) and, for history events of an earlier run whose
// time was clamped to zero, At the real time in epoch milliseconds.
type Base struct {
	T   float64 `json:"t"`
	K   string  `json:"k"`
	Seq uint64  `json:"seq,omitempty"`
	At  int64   `json:"at,omitempty"`
}

// base gives access to the head of any event type.
func (b *Base) base() *Base { return b }

// Event is one event of a session: a flat JSON object whose head is Base and whose other fields depend on the kind. Only the types
// of this package implement it. The events of one tab are delivered in Seq order and never cross tabs.
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

// Say is a transcript message: the person's (Who "you"), the manager's ("mgr") or a harness status line ("sys"). Text is at most
// 16 KiB; a longer message continues in More events that name its Mid. Stream is true while the message is still being written: the
// page types it out at Rate characters per second. Glyph is the one character that heads a status line, Plan marks a status line that
// announces a goal plan, Task the task a status line is about, and Mid the id of the message ("m" and the Seq of the event that
// opened it).
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
	// Open names the settings page the line offers to open ("providers" or "models"), for a notice about a missing key or model.
	Open string `json:"open,omitempty"`
}

// Sys is a system line in a channel: a notice, a lease conflict, an acknowledgment of a person's action. Ch is the channel (mgr or an
// agent id), Glyph the mark that heads the line, Text the line (at most 300 characters), Ag the agent and Task the task it is about.
type Sys struct {
	Base
	Ch    string `json:"ch"`
	Glyph string `json:"glyph"`
	Text  string `json:"text"`
	Ag    string `json:"ag,omitempty"`
	Task  string `json:"task,omitempty"`
	// Open names the settings page the line offers to open ("providers" or "models"), for a notice about a missing key or model.
	Open string `json:"open,omitempty"`
}

// Tool is a finished tool call of an agent (ID). Name is the tool's display name: Bash, Read, Write, Edit (also for a patch), Glob,
// Grep, Ls, WebFetch, WebSearch, Plan, TaskBoard, Spawn, Mail, Notes, Wait, Recall, Skill, or server.tool for a tool server's tool.
// Arg is a one-line argument (the command, the path, the pattern; at most 160 characters) and Out a one-line result (at most 200).
// OK is false for a call that failed; Refused, with Reason (at most 300 characters), marks a call that the permission engine
// refused. File, Add and Del describe a write, an edit or a patch: the project-relative path (the first file of a patch) and the
// lines added and removed. Task is the agent's current task and TID the harness's own id of the call (opaque).
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

// Note is a one-line note of an agent (a submission, its evidence). G is the mark of the feed row: done, tool, edit, mail, ask,
// steer or x. Text is at most 160 characters; a text that starts with "submitted" counts as a submission.
type Note struct {
	Base
	ID   string `json:"id"`
	G    string `json:"g"`
	Text string `json:"text"`
	Task string `json:"task,omitempty"`
}

// State is an agent's status change. S is one of idle, think, tool, edit, wait, ask, done or stuck; Doing is a one-line account of what
// it is doing (at most 120 characters). Task is always sent: the agent's current task, or null for none.
type State struct {
	Base
	ID    string  `json:"id"`
	S     string  `json:"s"`
	Doing string  `json:"doing"`
	Task  *string `json:"task"`
}

// Task creates or moves a task of the board. The first event of a task creates it and later events move it. S is its column: todo,
// running, verify or merged. Scope is the task's file globs joined by ", " ("-" when it has none); Closure is the way the board closed
// it, as the board writes it (verified, agreed, blocked_on(T2), superseded(T7), canceled, denied, verifier, exhausted,
// handed_off(be-3)). A task the board failed stays in todo and carries its Closure (the server's TaskX adds failed, attempts and
// blocked).
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

// Plan replaces the goal plan: the steps (at most 160 characters each) and, in St, their states (pending, act, done) in the same order.
type Plan struct {
	Base
	Steps []string `json:"steps"`
	St    []string `json:"st"`
}

// Verdict is the goal judge's verdict as one line. Kind is checking (the judge is reading the evidence of a turn), continue, done or
// blocked; Left lists what is still missing (at most 8 items of 160 characters).
type Verdict struct {
	Base
	Text string   `json:"text"`
	Kind string   `json:"kind,omitempty"`
	Left []string `json:"left,omitempty"`
}

// Req is one answered main request of an agent (ID): Ratio is the exact share of the prompt that was read from the cache (0 to 1,
// cache read over input plus cache read plus cache write), P the prompt tokens and O the output tokens. Hist marks a request of the
// agent's earlier history in a keyframe: it adds to the ratio series and the request count, not to the token counters.
type Req struct {
	Base
	ID    string  `json:"id"`
	Ratio float64 `json:"ratio"`
	P     int64   `json:"p,omitempty"`
	O     int64   `json:"o,omitempty"`
	Hist  bool    `json:"hist,omitempty"`
}

// Use sets an agent's absolute token table, reported cost and estimated saving. Rd is the tokens read from the cache, Un the prompt
// tokens that were not (input plus cache write), Out the output tokens and Wr the tokens written to the cache; Cost is the cost in US
// dollars that the provider reported, compactor calls included, and Saved the estimated saving from cache reads in US dollars. The
// values replace the agent's table; they are not increments.
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

// Warm says the shared prefix was refreshed at T (the start of the request that refreshed it) and stays warm for TTL seconds.
type Warm struct {
	Base
	TTL int `json:"ttl,omitempty"`
}

// Gov is the governor gauge: the requests per minute, the rate limits (R429) and retries counted so far, and the requests in flight
// and queued.
type Gov struct {
	Base
	RPM      int `json:"rpm"`
	R429     int `json:"r429"`
	Retries  int `json:"retries"`
	Inflight int `json:"inflight,omitempty"`
	Queued   int `json:"queued,omitempty"`
}

// Mail is one message between agents (data, not instructions). Text is at most 400 characters. Mail from the harness itself is not an
// agent's and is not sent as a Mail event.
type Mail struct {
	Base
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
}

// Ckpt announces or updates a checkpoint (the same CID updates in place). CID is the checkpoint's id, "c" and its number in at least
// two digits; TS the time of day (hh:mm:ss, server local time); Files the number of files it holds and Note its label. Skipped marks a
// checkpoint in which no file was touched, Safety one that the web interface took before a restore. Agents are the agents that wrote
// in it, Add and Del the lines added and removed.
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

// Question is an approval question as the page shows it. ID is "q_" and 26 lowercase base32 characters; Agent and Task name who asks.
// Cmd is the command of a shell request, else the summary of the request; Cwd the directory it runs in, relative to the project, with a
// trailing "/" ("." for the project itself); Why the reason the question was asked; Scope where the request applies (the directory,
// and the agent's lease when it has one). What names what a "don't ask again" would cover ("this command", "this change" or "this
// request" when nothing more) and Rule the rules it would add, joined by ", ". Kind is command, edit, read, web, trust, mcp or other,
// and Tool the harness tool that asks.
// OffersTests says that a fourth answer is offered, allowing the builds and tests of most projects for the session.
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
	// Path is the file an edit, write or patch asks to change, relative to the project, and Change the unified diff of that change
	// (at most 256 KiB, cut with a note when longer): the page shows what the person is asked to allow.
	Path   string `json:"path,omitempty"`
	Change string `json:"change,omitempty"`
}

// Ask opens a question. It is delivered for every question, from any page, and is never dropped.
type Ask struct {
	Base
	Q Question `json:"q"`
}

// Answer closes a question: Choice 1 yes, 2 yes and remember for the session, 3 no, 4 yes and allow builds and tests for the session;
// Note is what the person told the agent with a no. By says who or what resolved it: you, timeout (--ask-timeout passed), canceled
// (the turn was interrupted), closed (the session ended) or nobody (no page had the interface open for --ask-grace); for any but you the
// choice is 3. Rule is the allow rule that choice 2 or 4 added.
type Answer struct {
	Base
	QID    string `json:"qid"`
	Choice int    `json:"choice"`
	Note   string `json:"note,omitempty"`
	By     string `json:"by"`
	Rule   string `json:"rule,omitempty"`
}

// Queue is the merge queue's head of an isolated team; QHead nil (null) means the queue is empty. Cmd is the verification command
// that runs, Step is verifying or verified and Ms how long a merge took. Conflicts (submissions that conflicted with what was
// merged) and Bounced (submissions sent back to their worker: a conflict, a failed verification, a refusal of the queue) are
// absolute counters. A team that shares the tree has no merge queue and sends no Queue events.
type Queue struct {
	Base
	QHead     *string `json:"head"`
	Cmd       string  `json:"cmd,omitempty"`
	Step      string  `json:"step,omitempty"`
	Ms        int64   `json:"ms,omitempty"`
	Conflicts int     `json:"conflicts"`
	Bounced   int     `json:"bounced"`
}

// Merge says a task's work was merged: ID is the task, Cmd the verification command that passed and Ms how long the merge took.
type Merge struct {
	Base
	ID  string `json:"id"`
	Cmd string `json:"cmd"`
	Ms  int64  `json:"ms"`
}

// Break is a cache anomaly of an agent (ID): Kind names it, Read is the tokens read from the cache, Expected the tokens that should
// have been, and Why the explanation of the kind (at most 200 characters).
type Break struct {
	Base
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Read     int    `json:"read"`
	Expected int    `json:"expected"`
	Why      string `json:"why"`
}

// Compact is a compaction of an agent's thread: the tokens before (From) and after (To), and the signed change in percent.
type Compact struct {
	Base
	ID   string `json:"id"`
	From int    `json:"from"`
	To   int    `json:"to"`
	Pct  int    `json:"pct"`
}

// Stream opens a worker's streamed prose, or with Code a file being written (File is its project-relative path and Text the whole
// content, up to 64 KiB, at once). Rate is the characters per second at which the page types it out, and Mid names the message that
// More events continue.
type Stream struct {
	Base
	ID   string `json:"id"`
	Text string `json:"text"`
	Rate int    `json:"rate"`
	Code bool   `json:"code,omitempty"`
	File string `json:"file,omitempty"`
	Mid  string `json:"mid,omitempty"`
}

// Diff says a write to File is complete when Done is true (the page stops the "being written" caret).
type Diff struct {
	Base
	File string `json:"file"`
	Done bool   `json:"done"`
}

// Goal is the standing goal's state: S is active, paused, met or cleared. Objective is the goal's text, Turns the continuation turns
// used out of Max, Paused why it is paused and Reason the judge's last reason.
type Goal struct {
	Base
	S         string `json:"s"`
	Objective string `json:"objective,omitempty"`
	Turns     int    `json:"turns,omitempty"`
	Max       int    `json:"max,omitempty"`
	Paused    string `json:"paused,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Final ends a turn of the agent the person talks to; it is sent with Turn "end".
type Final struct{ Base }

// Steer is guidance sent to an agent (To): the person's steer to the manager, or the note that came with a no to that agent's
// question (which is sent Quiet, out of the feed).
type Steer struct {
	Base
	To    string `json:"to"`
	Text  string `json:"text"`
	Quiet bool   `json:"quiet,omitempty"`
}

// Interrupt records that the person interrupted the turn (ID "turn").
type Interrupt struct {
	Base
	ID string `json:"id"`
}

// Refuse is an action refused because nobody could be asked (a headless run, or no answer within --ask-timeout): the agent's call
// Name with its Arg and the Reason.
type Refuse struct {
	Base
	ID     string `json:"id"`
	Name   string `json:"name"`
	Arg    string `json:"arg"`
	Reason string `json:"reason"`
}

// More continues the message Mid: Text is appended to every entry that the message opened. The server merges the deltas of a message
// into one More per 100 ms. End closes the message (no more text); with Reset it marks a request that is being retried and the page
// leaves the text it shows as it is. A message is cut at 64 KiB.
type More struct {
	Base
	Mid   string `json:"mid"`
	Text  string `json:"text"`
	End   bool   `json:"end,omitempty"`
	Reset bool   `json:"reset,omitempty"`
}

// Turn marks the start or the end of a turn of the agent the person talks to: S is start or end.
type Turn struct {
	Base
	S string `json:"s"`
}

// Stall is a supervision finding of the team about an agent (ID) or a Task, raised or cleared (S is raise or clear). Kind is
// claimed_no_progress, manager_waiting_on_idle, orphaned_task, blocked_cycle or review_starved, and Text the detail (at most 200
// characters). The server also sends a Sys line for it.
type Stall struct {
	Base
	ID   string `json:"id,omitempty"`
	Task string `json:"task,omitempty"`
	Kind string `json:"kind"`
	S    string `json:"s"`
	Text string `json:"text"`
}

// Handover is a task changing hands from one worker (From) to another (To): S is the phase (begin, done or abort), Closure the closure
// recorded with the handover as the board writes it, and Error why an abort happened (at most 200 characters). The server also sends
// a Sys line, and on done a Task event with the new owner.
type Handover struct {
	Base
	Task    string `json:"task"`
	From    string `json:"from"`
	To      string `json:"to,omitempty"`
	S       string `json:"s"`
	Closure string `json:"closure,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Layers is an agent's latest prompt by layer G0..G5, in tokens (the hot tail of the conversation is counted in G5).
type Layers struct {
	Base
	ID   string `json:"id"`
	Toks [6]int `json:"toks"`
}

// Raw is a journal entry kept as encoded JSON (what the journal stores and replays).
type Raw = json.RawMessage
