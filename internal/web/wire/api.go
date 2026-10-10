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
