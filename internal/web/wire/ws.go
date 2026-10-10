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

// Hunk is one hunk; its key in the page is OldStart:NewStart. Scope is the confirmation scope of reverting exactly this hunk of
// the file as it is now (set on a diff whose new side is the live file's history; the page confirms it and sends it back).
type Hunk struct {
	OldStart int        `json:"oldStart"`
	OldLines int        `json:"oldLines"`
	NewStart int        `json:"newStart"`
	NewLines int        `json:"newLines"`
	Section  string     `json:"section,omitempty"`
	Lines    []HunkLine `json:"lines"`
	Scope    string     `json:"scope,omitempty"`
}

// HunkLine is one line of a hunk: T is " ", "+", "-" or "…" (an elided run), S its text.
type HunkLine struct {
	T string `json:"t"`
	S string `json:"s"`
}

// RevertRequest reverts one hunk of the diff of Path between From and To (checkpoint ids, "base" or "live"). Scope is the hunk's
// scope from the diff the person saw: when the hunk or the file changed since, the answer is 409 changed with the new diff.
type RevertRequest struct {
	Path  string `json:"path"`
	Key   string `json:"key"`
	From  string `json:"from"`
	To    string `json:"to"`
	Scope string `json:"scope,omitempty"`
}

// WsRevert is a hunk revert the person made (it can be undone while the file has not changed since).
type WsRevert struct {
	ID   string  `json:"id"`
	Path string  `json:"path"`
	Key  string  `json:"key"`
	At   float64 `json:"t"`
}

// RestoreRequest previews (DryRun) or applies /rewind ID. Scope is the preview's scope: when the plan changed since, the answer is
// 409 changed with the new plan.
type RestoreRequest struct {
	ID     string `json:"id"`
	DryRun bool   `json:"dryRun"`
	Scope  string `json:"scope,omitempty"`
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
	// Scope is the confirmation scope of applying exactly this plan (restore:<id>:<cid>:<digest>).
	Scope string `json:"scope,omitempty"`
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
	// Runs are every gate run of the task, oldest first (the last one is the fields above).
	Runs []VerifyRun `json:"runs,omitempty"`
}

// VerifyRun is one run of a task's verification gate: its attempt (1-based, in order),
// the command, its exit status, how long it took (Ms) and when it ended (At, epoch ms),
// where it ran (Gate "done" for a worker's done or a manager's accept in the shared
// tree, "merge" for the merge queue of an isolated team), and the tail of its output.
type VerifyRun struct {
	Attempt   int    `json:"attempt"`
	Cmd       string `json:"cmd"`
	Exit      int    `json:"exit"`
	OK        bool   `json:"ok"`
	TimedOut  bool   `json:"timedOut,omitempty"`
	Ms        int64  `json:"ms"`
	At        int64  `json:"at"`
	Agent     string `json:"agent,omitempty"`
	Gate      string `json:"gate,omitempty"`
	Out       string `json:"out"`
	Truncated bool   `json:"truncated,omitempty"`
}

// AcceptRequest commits the verified, merged work of the team onto the person's branch now. Mode "edits" applies it as
// uncommitted edits instead ("" or "commits": commits); DryRun only reports what would happen (no confirmation needed).
type AcceptRequest struct {
	Message string `json:"message,omitempty"`
	Mode    string `json:"mode,omitempty"`
	DryRun  bool   `json:"dryRun,omitempty"`
	// Scope is the dry run's scope: when what would be applied changed since, the answer is 409 changed with the new result.
	Scope string `json:"scope,omitempty"`
}

// AcceptResult is the commit that accept made, or for a dry run what it would apply: the files and tasks, whether anything
// verified is waiting, and whether commits are possible now (CommitBlocked says why not).
type AcceptResult struct {
	Commit        string   `json:"commit"`
	Files         []string `json:"files"`
	Branch        string   `json:"branch"`
	Tasks         []string `json:"tasks,omitempty"`
	Applied       bool     `json:"applied,omitempty"`
	Committed     bool     `json:"committed,omitempty"`
	Waiting       bool     `json:"waiting,omitempty"`
	CanCommit     bool     `json:"canCommit,omitempty"`
	CommitBlocked string   `json:"commitBlocked,omitempty"`
	Message       string   `json:"message,omitempty"`
	DryRun        bool     `json:"dryRun,omitempty"`
	// Scope is the confirmation scope of applying exactly what a dry run reported (accept:<id>:<digest>).
	Scope string `json:"scope,omitempty"`
}
