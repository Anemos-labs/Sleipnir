package state

import "time"

// TaskState is the column of the kanban a task is in, derived from the board's status and the merge queue (docs/UX.md: todo,
// running, verifying, merged, failed):
//
//	todo       the board says todo
//	running    doing or blocked (Task.Status tells them apart): a worker holds it
//	verifying  review (the worker finished and the harness's gate passed; the manager has not accepted it) or, while it is
//	           doing, a submission of it is waiting in the merge queue
//	merged     done (accepted), or review with a merge recorded for it (task.merge merged or empty)
//	failed     failed
type TaskState string

// The task states.
const (
	TaskTodo      TaskState = "todo"
	TaskRunning   TaskState = "running"
	TaskVerifying TaskState = "verifying"
	TaskMerged    TaskState = "merged"
	TaskFailed    TaskState = "failed"
)

// Task is one task of the board, as the board.op events rebuild it.
type Task struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Owner string `json:"owner,omitempty"`
	Role  string `json:"role,omitempty"` // the suggested role
	// Status is the board's own word: todo, doing, blocked, review, done or failed. State is the derived column.
	Status string    `json:"status"`
	State  TaskState `json:"state"`
	// Deps are the ids of the tasks it waits for, Files its scope.
	Deps     []string  `json:"deps,omitempty"`
	Files    []string  `json:"files,omitempty"`
	Line     string    `json:"line,omitempty"`     // the latest one-line progress
	Result   string    `json:"result,omitempty"`   // what the worker said when it finished
	Evidence string    `json:"evidence,omitempty"` // what the harness observed
	Attempts int       `json:"attempts,omitempty"`
	Rev      uint64    `json:"rev,omitempty"`
	Merge    string    `json:"merge,omitempty"`  // the merge queue's outcome for the current assignment: merged or empty
	Commit   string    `json:"commit,omitempty"` // the integration commit, 12 characters
	Seq      uint64    `json:"seq,omitempty"`    // the seq of the board.op that last changed it
	Updated  time.Time `json:"updated,omitzero"`
}

// TaskCounts is the number of tasks in each column, and how many of them are blocked.
type TaskCounts struct {
	Todo      int `json:"todo"`
	Running   int `json:"running"`
	Verifying int `json:"verifying"`
	Merged    int `json:"merged"`
	Failed    int `json:"failed"`
	Blocked   int `json:"blocked,omitempty"` // counted in Running as well
}

// Alert is one of the board's short-lived warnings (a lease conflict, a stalled agent).
type Alert struct {
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	Key  string    `json:"key,omitempty"`
	Seq  uint64    `json:"seq"`
	T    time.Time `json:"t,omitzero"`
}

// Board is the task board.
type Board struct {
	Version uint64     `json:"version,omitempty"`
	Tasks   []Task     `json:"tasks,omitempty"` // by number: T2 before T10
	Counts  TaskCounts `json:"counts"`
	Notes   int        `json:"notes,omitempty"` // proposed shared notes not yet folded into a layer
	Alerts  []Alert    `json:"alerts,omitempty"`
	// Dropped counts board events the swarm could not write ("dropped" ops).
	Dropped int `json:"dropped,omitempty"`
}

// MergeStage is where a submission to the merge queue stands. The queue's phases (rebase, verify, publish) are not events; what
// the log says is that a submission was queued and how it ended.
type MergeStage string

// The merge stages.
const (
	MergeQueued      MergeStage = "queued"
	MergeMerged      MergeStage = "merged"        // integrated, verified, the new tip
	MergeEmpty       MergeStage = "empty"         // nothing to merge
	MergeConflict    MergeStage = "conflict"      // bounced back to its worker with the conflicting files
	MergeVerifyFail  MergeStage = "verify_failed" // the verifier rejected the merged result; rolled back
	MergeRolledBack  MergeStage = "rolled_back"
	MergeRejected    MergeStage = "rejected" // refused before merging: out of scope, oversize, unresolved markers
	MergeError       MergeStage = "error"    // the merge or the verifier could not give a verdict
	MergeFastForward MergeStage = "fast_forward"
)

// Bounced reports whether the stage sends the work back to its worker.
func (s MergeStage) Bounced() bool {
	switch s {
	case MergeConflict, MergeVerifyFail, MergeRejected, MergeRolledBack:
		return true
	}
	return false
}

// MergeEntry is one submission to the merge queue.
type MergeEntry struct {
	Seq   uint64     `json:"seq"`
	T     time.Time  `json:"t,omitzero"`
	Agent string     `json:"agent,omitempty"`
	Task  string     `json:"task,omitempty"`    // T3
	Title string     `json:"subject,omitempty"` // "T3: the task's title", as the swarm names the submission
	Stage MergeStage `json:"stage"`
	// Position is the place in the queue when it was queued (1: next).
	Position  int      `json:"position,omitempty"`
	Commit    string   `json:"commit,omitempty"`
	Files     []string `json:"files,omitempty"`
	FileCount int      `json:"file_count,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	ExitCode  *int     `json:"exit_code,omitempty"` // of the verifier, when it failed
	Verified  bool     `json:"verified,omitempty"`
}

// MergeCounts tallies the queue's outcomes.
type MergeCounts struct {
	Queued     int `json:"queued"`
	Merged     int `json:"merged"`
	Empty      int `json:"empty,omitempty"`
	Conflicts  int `json:"conflicts"`
	VerifyFail int `json:"verify_failed,omitempty"`
	RolledBack int `json:"rolled_back,omitempty"`
	Rejected   int `json:"rejected,omitempty"`
	Errors     int `json:"errors,omitempty"`
	// Bounced is the number of submissions sent back to their worker (conflict, verify failure, rejection).
	Bounced int `json:"bounced,omitempty"`
}

// Integration is the end of an isolated run: the integration branch and whether its result reached the person's checkout.
type Integration struct {
	T         time.Time `json:"t,omitzero"`
	Branch    string    `json:"branch,omitempty"`
	Tip       string    `json:"tip,omitempty"`
	Applied   bool      `json:"applied"`
	Committed bool      `json:"committed,omitempty"`
	Files     int       `json:"files,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// MergeQueue is the verified merge queue of a worktree-isolated run.
type MergeQueue struct {
	// Waiting are the submissions queued and not yet settled, oldest first; Recent the settled ones, oldest first.
	Waiting []MergeEntry `json:"waiting,omitempty"`
	Recent  []MergeEntry `json:"recent,omitempty"`
	Counts  MergeCounts  `json:"counts"`
	// Trees counts existing worktrees, including trees retained for resume:
	// workspace.create less workspace.remove.
	Trees       int          `json:"trees,omitempty"`
	Integration *Integration `json:"integration,omitempty"`
	// Seen is true once the log holds any worktree or merge event.
	Seen bool `json:"seen,omitempty"`
}

// MailStage is how far a message got.
type MailStage string

// The mail stages.
const (
	MailSent      MailStage = "sent"
	MailRouted    MailStage = "routed" // taken by the mailman's ledger
	MailDelivered MailStage = "delivered"
	MailDropped   MailStage = "dropped" // refused: the recipient was retired or the swarm shut down
	MailDigested  MailStage = "digested"
)

// MailMsg is one message between agents.
type MailMsg struct {
	ID      string `json:"id"`
	Seq     uint64 `json:"seq"`
	From    string `json:"from"`
	To      string `json:"to"`
	Kind    string `json:"kind,omitempty"` // info, request, blocker, answer, contract
	Summary string `json:"summary,omitempty"`
	// Tokens is the size of the text, estimated at 3.6 bytes a token as the harness's own estimator starts (the log does not count them).
	Tokens    int       `json:"tokens"`
	Stage     MailStage `json:"stage"`
	Sent      time.Time `json:"sent,omitzero"`
	Delivered time.Time `json:"delivered,omitzero"`
	Via       string    `json:"via,omitempty"`     // the mailman that wrote the digest
	Origins   []string  `json:"origins,omitempty"` // who sent what a digest stands for
	Acked     bool      `json:"acked,omitempty"`
	Reason    string    `json:"reason,omitempty"` // why it was dropped
}

// MailCounts tallies the mail. Ignored counts the messages that were not delivered (mail.drop).
type MailCounts struct {
	Sent      int `json:"sent"`
	Routed    int `json:"routed,omitempty"`
	Delivered int `json:"delivered"`
	Ignored   int `json:"ignored,omitempty"`
	Acked     int `json:"acked,omitempty"`
	Digests   int `json:"digests,omitempty"`
	Direct    int `json:"direct,omitempty"` // messages the mailman mode delivered without a digest
	Batches   int `json:"batches,omitempty"`
}

// Mailman is the state of the optional mailman mode.
type Mailman struct {
	State   string `json:"state,omitempty"` // up or down
	Reason  string `json:"reason,omitempty"`
	Outages int    `json:"outages,omitempty"`
}

// Mail is the message traffic: a ring of recent messages and the counts.
type Mail struct {
	Recent  []MailMsg  `json:"recent,omitempty"` // oldest first
	Counts  MailCounts `json:"counts"`
	Mailman Mailman    `json:"mailman,omitzero"`
}

// Lease is one write lease: the agent that holds a path.
type Lease struct {
	Agent string    `json:"agent"`
	Path  string    `json:"path"`
	Seq   uint64    `json:"seq"`
	Since time.Time `json:"since,omitzero"`
}

// Leases is the lease table and the warnings it raised.
type Leases struct {
	Held            []Lease `json:"held,omitempty"`             // sorted by path
	Conflicts       int     `json:"conflicts,omitempty"`        // a write was refused because another agent held the file
	ScopeViolations int     `json:"scope_violations,omitempty"` // a write was refused as outside its task's scope
	Overlaps        int     `json:"overlaps,omitempty"`         // two writers touched one file in separate trees
}

// Governor is the request governor and the endpoint's rate limits.
type Governor struct {
	// Episodes counts rate-limit episodes the governor reported (governor events: one burst of 429s is one episode);
	// RatePerMin is the request rate it allowed after the last one, PauseUntil when admission resumes, Inflight and Queued what it held then.
	Episodes   int       `json:"episodes,omitempty"`
	RatePerMin float64   `json:"rate_per_min,omitempty"`
	PauseUntil time.Time `json:"pause_until,omitzero"`
	LastAt     time.Time `json:"last_at,omitzero"`
	Inflight   int       `json:"inflight,omitempty"`
	Queued     int       `json:"queued,omitempty"`
	// RPM is how many model requests were sent in the last 60 seconds before the snapshot's now.
	RPM int `json:"rpm"`
	// RateLimited counts rate-limit answers (model.error retries of kind rate_limit), Retries every repeated attempt.
	RateLimited int `json:"rate_limited,omitempty"`
	Retries     int `json:"retries,omitempty"`
}

// Who decided a permission question, as perm.decide's "by" says (internal/perm Audit.By).
const (
	// PermByUser: a person answered (the terminal prompter, a hook, the dialog of the UI).
	PermByUser = "user"
	// PermByNoOne: the engine had to ask and there was no one to ask (a batch run, a swarm worker, a rollout): the request was refused.
	PermByNoOne = "no one"
	// PermByPolicy: a rule settled it without a question: a deny rule, a built-in protection or the mode (a refusal, never an allow).
	PermByPolicy = "policy"
	// PermByCanceled: the run was cancelled while the question waited, which is not an answer.
	PermByCanceled = "canceled"
)

// PermAsk is a permission question, as perm.ask records it: the permission engine could not settle a tool call by its rules and
// had to put a question. The log gives a question no id; the State tells it from another by Seq, and pairs it with its answer by
// who asked, the tool, the command and the paths (they are the same in the perm.decide).
type PermAsk struct {
	Seq uint64    `json:"seq"`
	T   time.Time `json:"t,omitzero"`
	// Agent is who asked: the agent whose tool call is held at the question. Role is the role it runs in, where the log says.
	Agent string `json:"agent,omitempty"`
	Role  string `json:"role,omitempty"`
	// Tool is what it wants to use ("bash", "write"), Command the shell command line of a bash call (the producer cuts it at 400
	// characters, this type at 200) and Paths the files the call touches (at most MaxPermPaths; shown relative to the project when
	// under it).
	Tool    string   `json:"tool,omitempty"`
	Command string   `json:"command,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	// Reason is why the engine asked ("reads /etc/hosts outside the workspace": a sentence of its own, not of the model's).
	Reason string `json:"reason,omitempty"`
	// Summary is the line a dialog shows first: the tool and what it is to act on, the command or the paths.
	Summary string `json:"summary"`
}

// PermDecision is a permission question that has been settled, or a request that was refused without one.
type PermDecision struct {
	// Ask is what was asked. Asked is false when the State saw no perm.ask for it: a refusal by policy, which is never put as a
	// question, or an answer whose question came before the part of the log that was read; Ask is then what the perm.decide itself
	// says (Seq and T are zero).
	Ask   PermAsk `json:"ask"`
	Asked bool    `json:"asked"`
	Seq   uint64  `json:"seq"`
	// T is when it was settled, WaitedMs how long a question waited for its answer (event time), zero when it was not asked.
	T        time.Time `json:"t,omitzero"`
	WaitedMs int64     `json:"waited_ms,omitempty"`
	// Allow is the answer; a decision that does not say it is a refusal.
	Allow bool `json:"allow"`
	// By is who decided (PermByUser, PermByNoOne, PermByPolicy, PermByCanceled) and Remember how long the answer is kept as a rule:
	// "session", "project", or empty for this once.
	By       string `json:"by,omitempty"`
	Remember string `json:"remember,omitempty"`
	// Reason is why it was decided so: the rule, the protection or the mode that settled it, or what the person said.
	Reason string `json:"reason,omitempty"`
}

// Perms is the state of the permission dialog: the questions waiting, the last few answers and the counts.
type Perms struct {
	// Pending are the questions nobody has answered yet, oldest first (at most MaxPending); Recent the last PermLog settled
	// ones, oldest first. A question stops being pending when its answer comes, when its agent's run ends or is cancelled, and when
	// the session ends.
	Pending []PermAsk      `json:"pending,omitempty"`
	Recent  []PermDecision `json:"recent,omitempty"`
	// Asked counts perm.ask events, Allowed and Denied the decisions by their answer, and ByUser, ByNoOne, ByPolicy and Canceled
	// by who made them (a decision by someone else counts in Allowed or Denied only).
	Asked    int `json:"asked,omitempty"`
	Allowed  int `json:"allowed,omitempty"`
	Denied   int `json:"denied,omitempty"`
	ByUser   int `json:"by_user,omitempty"`
	ByNoOne  int `json:"by_no_one,omitempty"`
	ByPolicy int `json:"by_policy,omitempty"`
	Canceled int `json:"canceled,omitempty"`
	// Abandoned counts the questions that never got their answer in the log: their agent's run ended or was cancelled first, or the
	// session ended (or a log was read from the middle).
	Abandoned int `json:"abandoned,omitempty"`
}

// Supervision counts how the swarm held and woke its manager.
type Supervision struct {
	Holds      int    `json:"holds,omitempty"`
	Unfinished int    `json:"unfinished,omitempty"`
	Wakes      int    `json:"wakes,omitempty"`
	WakePaused int    `json:"wake_paused,omitempty"`
	WakeLimits int    `json:"wake_limits,omitempty"`
	LastHold   string `json:"last_hold,omitempty"`
	LastWake   string `json:"last_wake,omitempty"`
}

// FeedKind classifies a feed line, so that a renderer can colour it without parsing its text.
type FeedKind string

// The kinds of feed line.
const (
	FeedTool    FeedKind = "tool"    // a tool result
	FeedToolErr FeedKind = "toolerr" // a tool that failed
	FeedError   FeedKind = "error"   // a model request that failed for good, a panic, a timeout
	FeedRetry   FeedKind = "retry"   // an attempt repeated after a retryable failure
	FeedStuck   FeedKind = "stuck"
	FeedCompact FeedKind = "compact"
	FeedCache   FeedKind = "cache" // a cache anomaly
	FeedEpoch   FeedKind = "epoch"
	FeedMail    FeedKind = "mail"
	FeedSpawn   FeedKind = "spawn"
	FeedEnd     FeedKind = "end"
	FeedBoard   FeedKind = "board" // a task finished, failed or was sent back
	FeedMerge   FeedKind = "merge"
	FeedLease   FeedKind = "lease"
	FeedPerm    FeedKind = "perm"
	FeedBudget  FeedKind = "budget"
	FeedInput   FeedKind = "input"   // something a person typed
	FeedSession FeedKind = "session" // the session began or ended
	FeedNote    FeedKind = "note"    // a notice the session logged, a wake, a hold
	FeedJob     FeedKind = "job"     // a background shell job
	FeedCancel  FeedKind = "cancel"  // a run that was cancelled
)

// The glyphs of the feed. Kind says what a line is; Glyph is the default picture for it, which a renderer that cannot draw it
// (plain mode, ASCII) replaces by its own.
const (
	GlyphOK      = "✓"
	GlyphFail    = "✗"
	GlyphWarn    = "⚠"
	GlyphCompact = "◆"
	GlyphMail    = "✉"
	GlyphSpawn   = "+"
	GlyphEnd     = "■"
	GlyphEpoch   = "↻"
	GlyphTask    = "▣"
	GlyphMerge   = "▸"
	GlyphInput   = "❯"
	GlyphInfo    = "·"
	GlyphAsk     = "?"
)

// FeedLine is one line of the live feed.
type FeedLine struct {
	Seq   uint64    `json:"seq"`
	T     time.Time `json:"t,omitzero"`
	Agent string    `json:"agent,omitempty"`
	Kind  FeedKind  `json:"kind"`
	Glyph string    `json:"glyph"`
	Text  string    `json:"text"`
	// Detail is the short tail of the line ("1.4 s"), "" when there is none.
	Detail string `json:"detail,omitempty"`
}

// TTLEntry is how long a cached prefix is expected to live: when it was last read or written, and its lifetime. There is one per
// prefix (the shared layers every rider reads) and one per agent (its whole prompt).
type TTLEntry struct {
	// Kind is "prefix" or "agent"; Key the prefix_key (12 characters) or the agent id.
	Kind string `json:"kind"`
	Key  string `json:"key"`
	// Agents are the riders of a prefix (at most MaxRiders), sorted.
	Agents []string `json:"agents,omitempty"`
	Model  string   `json:"model,omitempty"`
	Tokens int      `json:"tokens,omitempty"` // the size of the prefix where known: the shared and role layers, or the agent's prompt
	// Last is when the prefix was last read or written (the start of the request that did), How what that request reported:
	// "read" (cache read tokens), "write" (cache write tokens) or "sent" (it reported neither: the provider may not report cache usage).
	Last time.Time `json:"last,omitzero"`
	How  string    `json:"how,omitempty"`
	// TTLSeconds is the lifetime; Default is true when the events did not say and the default applies.
	TTLSeconds int  `json:"ttl_s"`
	Default    bool `json:"default,omitempty"`
}

// Expires is when the entry goes cold.
func (e TTLEntry) Expires() time.Time { return e.Last.Add(time.Duration(e.TTLSeconds) * time.Second) }

// Remaining is how long is left at now: zero once it has gone cold, the whole lifetime when now precedes Last (a clock that
// went backwards must not make a prefix look older than it is).
func (e TTLEntry) Remaining(now time.Time) time.Duration {
	ttl := time.Duration(e.TTLSeconds) * time.Second
	if e.Last.IsZero() || now.Before(e.Last) {
		return ttl
	}
	left := ttl - now.Sub(e.Last)
	if left < 0 {
		return 0
	}
	return left
}

// TTLStatus is an entry seen at a moment.
type TTLStatus struct {
	TTLEntry
	Remaining time.Duration `json:"remaining"`
	// Frac is the remaining fraction of the lifetime, in [0, 1].
	Frac float64 `json:"frac"`
	// Cold is true when the entry has expired.
	Cold bool `json:"cold,omitempty"`
}

// Prefix is a group of agents that share one prefix_key: the same model, tools, constitution, shared pin and role pin, so that
// one cached copy serves them all (the fan of docs/UX.md).
type Prefix struct {
	Key string `json:"key"`
	// SharedHash is the hash of the shared layer (G1), which every role shares.
	SharedHash string `json:"shared_hash,omitempty"`
	Role       string `json:"role,omitempty"`
	// Tokens is the size of the shared and role layers of the latest request with this key; the constitution and tools, which
	// every rider also shares, are not in it (the request does not size them).
	Tokens int `json:"tokens"`
	// Agents are the riders, sorted (at most MaxRiders); Riders the number of them.
	Agents   []string `json:"agents"`
	Riders   int      `json:"riders"`
	FirstSeq uint64   `json:"first_seq,omitempty"`
	LastSeq  uint64   `json:"last_seq,omitempty"`
}

// Levels is one agent's busy level for each second of the activity matrix, oldest first: 0 is idle and 1 to 8 the fraction of
// the second that a model request or a tool call was in flight, in eighths, rounded up (any activity is at least 1). It marshals as
// a string of digits, one per second, which reads well in a golden file.
type Levels []uint8

// MarshalJSON writes the levels as a string of digits.
func (l Levels) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, len(l)+2)
	b = append(b, '"')
	for _, v := range l {
		b = append(b, '0'+min(v, 8))
	}
	return append(b, '"'), nil
}

// Marks is one agent's markers for each second of the activity matrix, as a bit set of the Mark* flags below. It marshals as a
// string of hexadecimal digits.
type Marks []uint8

// The bits of a Marks cell.
const (
	ActMail    uint8 = 1 << iota // a message was sent by or delivered to the agent
	ActCompact                   // a compaction was committed
	ActStuck                     // the repetition guard fired
	ActAnomaly                   // a cache anomaly
)

// MarshalJSON writes the marks as a string of hexadecimal digits.
func (m Marks) MarshalJSON() ([]byte, error) {
	const hex = "0123456789abcdef"
	b := make([]byte, 0, len(m)+2)
	b = append(b, '"')
	for _, v := range m {
		b = append(b, hex[v&15])
	}
	return append(b, '"'), nil
}

// ActivityRow is one agent's lane of the swarm gantt.
type ActivityRow struct {
	Agent  string `json:"agent"`
	Levels Levels `json:"levels"`
	Marks  Marks  `json:"marks"`
}

// Activity is the swarm gantt's data: for every agent, the last ActivitySeconds one-second buckets of event time, the newest
// last, ending at End. Each row has exactly ActivitySeconds cells.
type Activity struct {
	// End is the start of the newest bucket: the second of the snapshot's clock.
	End  time.Time     `json:"end,omitzero"`
	Rows []ActivityRow `json:"rows,omitempty"`
}
