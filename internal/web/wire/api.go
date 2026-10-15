package wire

import "encoding/json"

// Error is the body of every non-2xx response (the shape of web.Error, plus an optional Detail) and the error type the route
// packages return: Status is the HTTP status, Msg one sentence a person can read (it is shown in a toast; never a path outside
// the project, never a secret), Code a stable identifier (docs/WEB-API.md lists them), Detail optional structured data (a trust
// challenge, the scope and reasons of a confirmation, the plan that changed).
type Error struct {
	Status int    `json:"-"`
	Msg    string `json:"error"`
	Code   string `json:"code"`
	Detail any    `json:"detail,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// Frame is one message of the page's stream before it is handed to the hub: Type is the SSE event name (docs/WEB-API.md lists the
// frames), Tab the tab it belongs to ("" for global frames), Data its JSON body. Critical frames are never dropped for a slow page;
// Coalescable frames with the same Type and Key replace each other in a slow page's queue (web.Event has the same flags).
type Frame struct {
	Type        string
	Tab         string
	Data        any
	Critical    bool
	Coalescable bool
	Key         string
}

// EvFrame is the data of an "ev" frame: one event of the tab (its Base carries the kind and the sequence number).
type EvFrame struct {
	Tab string          `json:"tab"`
	Ev  json.RawMessage `json:"ev"`
}

// MetaFrame is the data of a "meta" frame: the fields of the tab's meta that changed.
type MetaFrame struct {
	Tab   string    `json:"tab"`
	Patch MetaPatch `json:"patch"`
}

// RosterFrame is the data of a "roster" frame: the agents of the tab, replacing the earlier list.
type RosterFrame struct {
	Tab    string        `json:"tab"`
	Roster []RosterEntry `json:"roster"`
}

// TabFrame is the data of a "tab" frame: Op is add, update (a rename, a new generation, a new session id) or remove.
type TabFrame struct {
	Op  string     `json:"op"`
	Tab TabSummary `json:"tab"`
}

// ReasonFrame is the data of the "bye" frame: why the server is stopping.
type ReasonFrame struct {
	Reason string `json:"reason"`
}

// ResetFrame is the data of a "reset" frame: the tab started generation Gen (a restart), so its sequence numbers start again and the
// page fetches its snapshot.
type ResetFrame struct {
	Tab string `json:"tab"`
	Gen uint64 `json:"gen"`
}

// Hello is the body of GET /api/hello, which a page reads before it opens the stream. Boot identifies the run of the server (a page
// that finds another Boot after a lost connection starts over), Now is the server's clock in epoch milliseconds, Tabs the live
// sessions in strip order and Active the id of the one in front. Version is the program's version.
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

// ServerInfo describes the server: its listening address as the page shows it (the Host of the request), whether it is bound to
// loopback (true for the server the command starts), and the program version: Version as the build names it, Label as a top bar
// shows it ("v0.2.28") and Build the full description (version, commit, toolchain and platform) a tooltip shows.
type ServerInfo struct {
	Addr     string `json:"addr"`
	Loopback bool   `json:"loopback"`
	Version  string `json:"version"`
	Label    string `json:"label,omitempty"`
	Build    string `json:"build,omitempty"`
}

// Limits are the limits the page respects: the largest request body in bytes, the largest message in bytes, the longest goal in
// characters, and how many questions of one tab may be open at once.
type Limits struct {
	MaxBody      int `json:"maxBody"`
	MaxMessage   int `json:"maxMessage"`
	MaxGoal      int `json:"maxGoal"`
	MaxQuestions int `json:"maxQuestions"`
}

// UIInfo identifies the embedded UI build.
type UIInfo struct {
	Version string `json:"version"`
}

// Toast is the data of a "toast" frame: a notice that is not a row of a session, shown for the tab (Tab, or any when empty) in the
// tone Kind. The page handles it; the server sends none at present.
type Toast struct {
	Tab  string `json:"tab,omitempty"`
	Text string `json:"text"`
	Kind string `json:"kind,omitempty"`
}

// Ping is the data of a "ping" frame: each tab's session time now, in seconds.
type Ping struct {
	Now map[string]float64 `json:"now"`
}

// TabSummary is a live tab as lists show it. ID is the tab's id and SID the id of the session it hosts, which names its directory
// among the recorded sessions. Gen counts the generations of the tab (each restart starts one), CreatedAt is epoch milliseconds and
// Order the position in the strip. Headless marks a session in which nobody is expected to answer questions: its questions are
// refused after --ask-timeout, or it follows a session that another process runs.
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

// RosterEntry is one agent of a tab. ID is the agent's id (mgr for the manager and for a single agent; a role's short code, "-" and a
// number for a worker, such as be-2), Role its role, Code the role's short code and Nth the number in the id. K is the worker's
// 1-based start order and Leg its leg of the horse: a leg is a role, the built-in roles in their order (architect 0 ... reviewer 7)
// and other roles after them, eight to a round (-1 while the role is unknown); the manager has K 0 and Leg -1. Scope is the globs of its current
// task ("- (read-only)" for a read-only role, "**" for the manager or a single agent), RO whether
// its role writes no file, Model the model it runs on and Spawn the session time at which it started.
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

// Rule is a permission rule with its origin as the Permissions page shows it. Effect is allow, deny or ask; Origin says where the rule
// comes from (a configuration layer, a built-in protection, a flag or the session), File the file that holds it and Fixed that it
// cannot be removed from the page.
type Rule struct {
	Effect string `json:"effect"`
	Rule   string `json:"rule"`
	Origin string `json:"origin"`
	File   string `json:"file,omitempty"`
	Note   string `json:"note,omitempty"`
	Fixed  bool   `json:"fixed,omitempty"`
}

// QueuedLine is a message typed while a turn runs, waiting for the next turn. Kind says what it is: "message" (a line for the agent,
// which can be sent now as a steer, edited or dropped), "command" (a slash line, which can be edited or dropped) or "action" (something
// the session does for the page, such as a goal change, which stays).
type QueuedLine struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Kind string `json:"kind,omitempty"`
}

// QueuedDrop is the answer to dropping a queued line or sending it now: the line as it was queued, so the page can put its text back
// in the message box, and Steered when it went to the running turn as a steer (false: it runs next, as nothing was running).
type QueuedDrop struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Kind    string `json:"kind"`
	Steered bool   `json:"steered,omitempty"`
}

// MetaPatch is a change to a tab's meta, the settings and state of the session that the page shows (its directory, model, mode,
// effort, budget, team size, launch flags, rules, goal, queued lines and whether a turn runs); only set fields are sent. Pointers
// distinguish "unset" from zero, and a pointer to an empty slice or map sends an empty value (the last rule removed, the queue
// drained). Launch is the `sleipnir chat` command line that starts the same team again (staged flags included), StartedAt (epoch
// milliseconds) the moment this run began, which the session time of its events counts from, and ResumedFrom the recorded session
// that it continues.
type MetaPatch struct {
	Cwd       *string  `json:"cwd,omitempty"`
	Model     *string  `json:"model,omitempty"`
	Mode      *string  `json:"mode,omitempty"`
	Effort    *string  `json:"effort,omitempty"`
	Budget    *float64 `json:"budget,omitempty"`
	Swarm     *int     `json:"swarm,omitempty"`
	Isolation *string  `json:"isolation,omitempty"`
	Verify    *string  `json:"verify,omitempty"`
	Commit    *bool    `json:"commit,omitempty"`
	Mailman   *bool    `json:"mailman,omitempty"`
	AutoDocs  *bool    `json:"autoDocs,omitempty"`
	// ManagerApproves says whether the team's manager answers its workers' routine permission questions first (--manager-approves).
	ManagerApproves *bool              `json:"managerApproves,omitempty"`
	Team            *[]TeamSlot        `json:"team,omitempty"`
	TrustProject    *bool              `json:"trustProject,omitempty"`
	NoMcp           *bool              `json:"noMcp,omitempty"`
	RoleModels      *map[string]string `json:"roleModels,omitempty"`
	Rules           *[]Rule            `json:"rules,omitempty"`
	GoalText        *string            `json:"goalText,omitempty"`
	Launch          *string            `json:"launch,omitempty"`
	StartedAt       *int64             `json:"startedAt,omitempty"`
	Headless        *bool              `json:"headless,omitempty"`
	SessionDir      *string            `json:"sessionDir,omitempty"`
	AskTimeout      *string            `json:"askTimeout,omitempty"`
	ResumedFrom     *RecordedSession   `json:"resumedFrom,omitempty"`
	Queued          *[]QueuedLine      `json:"queued,omitempty"`
	Running         *bool              `json:"running,omitempty"`
}

// TabSnapshot is the full state of a tab for a late joiner. Gen is the tab's generation, Seq the sequence number of the last event
// included and Now the session time in seconds at which the snapshot was made. Keyframe is a short set of events, stamped with the time
// of the first retained event, that stands for what the tab's journal no longer holds (each agent's token table and request ratios,
// its layers and state, the tasks, the plan, the goal and the checkpoints); Events are the retained journal events in Seq order, so
// that a page's log is Keyframe followed by Events. Hist lists the lines the person sent (for the arrow keys) and Questions the open
// questions. The journal keeps the newest 50,000 events or 32 MiB of JSON.
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

// OpenQuestion is a question with the tab it belongs to (the inbox of every session): T0 is the session time at which it was asked.
type OpenQuestion struct {
	Tab string   `json:"tab"`
	Q   Question `json:"q"`
	T0  float64  `json:"t0"`
}

// NewSessionRequest is the New session dialog: every chat flag it shows, plus a name and a first goal. Cwd is an absolute path of a
// folder on the server's machine: one of the projects the server lists, or any other folder that exists (or that CreateDir makes),
// which raises what the request needs confirmed. CreateDir makes the last part of Cwd when it does not exist yet (its parent must);
// GitInit makes an empty folder, or the one CreateDir makes, a git repository with an empty first commit. Neither is a chat flag.
// Swarm is the number of workers (0 is a single agent; absent takes the server's default), Rules are --allow rules, Budget is in US
// dollars (0 is no limit, not even the cap a team has where nobody chose a budget; absent leaves it to the server's configuration),
// and RoleModels maps a role to the model it runs on.
type NewSessionRequest struct {
	Name         string            `json:"name,omitempty"`
	Cwd          string            `json:"cwd"`
	CreateDir    bool              `json:"createDir,omitempty"`
	GitInit      bool              `json:"gitInit,omitempty"`
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
	// Team is the team's composition, one entry per worker slot (--role-model given per slot); empty is the default team of
	// one worker per role. Swarm then equals its length.
	Team []TeamSlot `json:"team,omitempty"`
	// AutoDocs false makes docs a role the manager assigns (--auto-docs=false); nil or true keeps the docs worker at work by itself.
	AutoDocs *bool `json:"autoDocs,omitempty"`
	// ManagerApproves false puts every worker's permission question to the person (--manager-approves=false); nil or true lets the
	// manager answer the routine ones first.
	ManagerApproves *bool `json:"managerApproves,omitempty"`
}

// TeamSlot is one worker slot of a team: its role, and its model ("" is the role's).
type TeamSlot struct {
	Role  string `json:"role"`
	Model string `json:"model,omitempty"`
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

// MessageRequest is a line sent from the composer: Text is what reaches the agent, Display what the transcript shows (the line as
// typed, when it differs), ClientID makes a repeated request return the first answer.
type MessageRequest struct {
	AutoFix   bool   `json:"autoFix,omitempty"`
	AutoMerge bool   `json:"autoMerge,omitempty"`
	Text      string `json:"text"`
	Display   string `json:"display,omitempty"`
	ClientID  string `json:"clientId,omitempty"`
}

// SendResult says whether a message was delivered now or queued behind the running turn.
type SendResult struct {
	Queued   bool   `json:"queued"`
	Position int    `json:"position,omitempty"`
	ID       string `json:"id"`
}

// CommandRequest is a slash line the page has no handler for (custom commands, skills, MCP prompts, /mcp reconnect) or that the
// server runs as the route it stands for (/mode, /allow, /model, /restart and the like).
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
	AutoFix   bool   `json:"autoFix,omitempty"`
	AutoMerge bool   `json:"autoMerge,omitempty"`
	Action    string `json:"action"`
	Text      string `json:"text,omitempty"`
}

// ModeRequest sets the permission mode (default, accept-edits, plan, bypass or yolo); bypass and yolo need an X-Confirm id.
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
	AutoDocs     *bool   `json:"autoDocs,omitempty"`
	NoMcp        *bool   `json:"noMcp,omitempty"`
	TrustProject *bool   `json:"trustProject,omitempty"`
	// ManagerApproves stages --manager-approves.
	ManagerApproves *bool `json:"managerApproves,omitempty"`
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

// DirListing answers GET /api/fs/dirs: the subfolders that complete a path being typed, and what the path typed is.
type DirListing struct {
	// Dir is the folder whose subfolders are listed and Prefix what a name has to start with (the unfinished last part of the path);
	// both are empty when the path could not be read as a path.
	Dir    string `json:"dir"`
	Prefix string `json:"prefix"`
	// Entries are the subfolders of Dir that start with Prefix, best first; More says there are others than those listed.
	Entries []DirEntry `json:"entries"`
	More    bool       `json:"more,omitempty"`
	// Home is the home folder (what a leading ~ stands for) and Sep the separator of paths on the server's machine.
	Home  string   `json:"home"`
	Sep   string   `json:"sep"`
	State DirState `json:"state"`
}

// DirEntry is a subfolder offered to complete a path: Repo says it holds a git repository, Link that it is a link to a folder.
type DirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Repo bool   `json:"repo,omitempty"`
	Link bool   `json:"link,omitempty"`
}

// DirState is what a path typed names: Path is its absolute form (a leading ~ and a relative path resolved), Exists and Dir say
// whether there is a folder, Readable whether its entries can be listed, ParentOK whether its parent is a folder (so that it can be
// made), Known whether it is one of the server's projects, Empty whether the folder has nothing in it, Inaccessible that the server
// could not look at the path at all (it is there, as far as anyone can tell, and nothing can start in it). Git is "repo" for the top of
// a git repository, "inside" for a folder within another one (GitRoot), "bare" for a bare repository (a git directory with no work
// tree), "none" otherwise. Verify are the checks its files suggest.
type DirState struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Dir      bool   `json:"dir"`
	Readable bool   `json:"readable"`
	ParentOK bool   `json:"parentOk"`
	Known    bool   `json:"known"`
	Empty    bool   `json:"empty"`
	// Inaccessible is a path that could not be looked at (permission, a link that loops): not a path to make or to take for absent.
	Inaccessible bool         `json:"inaccessible,omitempty"`
	Git          string       `json:"git"`
	GitRoot      string       `json:"gitRoot,omitempty"`
	Verify       []VerifyHint `json:"verify"`
	// Repo is what git says of the repository when the path is its root (Git "repo") or a bare repository (Git "bare"); absent when
	// git could not be asked in time.
	Repo *RepoState `json:"repo,omitempty"`
}

// RepoState is what a repository says about starting an isolated team in it: Bare (no work tree), HasCommits (a commit to start the
// workers' trees from), Branch (the checked-out branch; Detached when HEAD is on none), and Clean (nothing staged, modified or
// untracked), which --commit needs.
type RepoState struct {
	Bare       bool   `json:"bare,omitempty"`
	HasCommits bool   `json:"hasCommits"`
	Branch     string `json:"branch,omitempty"`
	Detached   bool   `json:"detached,omitempty"`
	Clean      bool   `json:"clean"`
}

// VerifyHint is a command that checks a project of the kind a folder looks like (Cmd) and the file that says so (Why).
type VerifyHint struct {
	Cmd string `json:"cmd"`
	Why string `json:"why"`
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
	// List is what a subscription plan covered, at the API list price, and Plan says the session ran on one: a page shows Cost+List
	// marked as the plan's (session.Summary). A session whose last run wrote no session.end has the sum of that run's responses added.
	List float64 `json:"list,omitempty"`
	Plan bool    `json:"plan,omitempty"`
	// Mailman says the last run had the mailman mode on (--mailman): a replay of it shows the mailman as on.
	Mailman bool `json:"mailman,omitempty"`
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

// DeleteRecordedRequest deletes the named recorded sessions (the route needs a confirmation for exactly those ids).
type DeleteRecordedRequest struct {
	IDs []string `json:"ids"`
}

// ConfirmRequest is the body of POST /api/confirm (a built-in route of internal/web): the scope of the privileged action, which the
// refusal that asked for the confirmation names (docs/WEB-API.md lists the scopes).
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
