package wire

// The shapes in this file are the answers the Settings and Tools pages read: the model catalogue, providers, tool servers, skills,
// commands and hooks, configuration layers, permissions, trust, the schedule and the doctor. None carries a secret: a key is never
// returned, and environment and header values are withheld.

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
