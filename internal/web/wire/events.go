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
	// Path is the file an edit, write or patch asks to change, relative to the project, and Change the unified diff of that change
	// (at most 256 KiB, cut with a note when longer): the page shows what the person is asked to allow (PARITY A1).
	Path   string `json:"path,omitempty"`
	Change string `json:"change,omitempty"`
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
