package inspect

import (
	"encoding/json"
	"time"
)

// This file holds the JSON contract between the model and the UI. Field names
// are stable; add fields rather than renaming them.

// LayerKeys names the seven layers of the prompt stack in prefix order
// (docs/CACHE-DESIGN.md section 2).
var LayerKeys = [7]string{"G0", "G1", "G2", "G3", "G4", "G5", "G6"}

// LayerTitles describes each layer in one phrase.
var LayerTitles = [7]string{
	"constitution + tools", "shared pin", "role pin", "notes", "spine", "thread", "hot tail",
}

// sectionLayer maps the section names model.request records to layer indexes.
// G0, G5 and G6 are not sections: they are derived (see Layers).
var sectionLayer = map[string]int{"shared": 1, "role": 2, "notes": 3, "spine": 4}

// Session states as shown in the header.
const (
	StateLive  = "live"  // written to recently and not ended
	StateEnded = "ended" // session.end seen
	StateIdle  = "idle"  // no session.end and no writes for a while: interrupted, or an old log
	StateEmpty = "empty" // no events yet
)

// LogMeta describes the log file as read so far.
type LogMeta struct {
	File      string           `json:"file"`
	Events    int64            `json:"events"`
	Bytes     int64            `json:"bytes"`      // consumed (complete lines)
	FileBytes int64            `json:"file_bytes"` // size on disk at the last poll
	LastSeq   uint64           `json:"last_seq"`
	TornBytes int64            `json:"torn_bytes"` // unparsed bytes after the last complete line
	BadLines  int64            `json:"bad_lines"`  // complete lines that were not events
	Reloads   int              `json:"reloads,omitempty"`
	Schema    int              `json:"schema,omitempty"`
	Updated   time.Time        `json:"updated"` // file mtime
	Blobs     bool             `json:"blobs"`   // a blobs/ directory is present
	Types     map[string]int64 `json:"types"`   // events per type
}

// SessionMeta is what the header shows about a session.
type SessionMeta struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Model       string    `json:"model"`
	Models      []string  `json:"models,omitempty"`
	Provider    string    `json:"provider"`
	Dialect     string    `json:"dialect,omitempty"`
	Renderer    string    `json:"renderer,omitempty"`
	Version     string    `json:"version,omitempty"`
	Swarm       bool      `json:"swarm"`
	Root        string    `json:"root,omitempty"`
	Goal        string    `json:"goal,omitempty"`
	SharedHash  string    `json:"shared_hash,omitempty"`
	ReconTokens int       `json:"recon_tokens,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"` // session.end, else the last event
	Ended       bool      `json:"ended"`
	DurationMs  int64     `json:"duration_ms"`
	EndCostUSD  float64   `json:"end_cost_usd,omitempty"` // as recorded by session.end
	EndReason   string    `json:"end_reason,omitempty"`   // why the session ended (session.end): completed, exit, interrupted, budget, error, other
	Isolation   string    `json:"isolation,omitempty"`    // "worktree" when writers had trees of their own
	Mailman     bool      `json:"mailman,omitempty"`      // worker mail went through the mailman
}

// Totals are exact over the whole log, whatever the retention window.
type Totals struct {
	Requests    int   `json:"requests"`
	Main        int   `json:"main"`
	Side        int   `json:"side"` // compactor and other side calls
	Pending     int   `json:"pending"`
	Failed      int   `json:"failed"`
	Retries     int   `json:"retries"`
	RateLimited int   `json:"rate_limited"`
	Input       int64 `json:"input"` // uncached input tokens
	CacheRead   int64 `json:"cache_read"`
	CacheWrite  int64 `json:"cache_write"`
	Output      int64 `json:"output"`
	Prompt      int64 `json:"prompt"` // input + read + write
	Agents      int   `json:"agents"`
	ToolCalls   int   `json:"tool_calls"`
	ToolErrors  int   `json:"tool_errors"`
	Turns       int   `json:"turns"`
}

// CacheStats summarises how well the cache worked.
type CacheStats struct {
	HitRatio         float64 `json:"hit_ratio"`        // all requests: what the bill sees
	MainHitRatio     float64 `json:"main_hit_ratio"`   // agent requests only
	SideHitRatio     float64 `json:"side_hit_ratio"`   // compactor forks: they should read the whole prefix
	SteadyHitRatio   float64 `json:"steady_hit_ratio"` // main, not first, no rebase just before
	RebaseHitRatio   float64 `json:"rebase_hit_ratio"` // main, first request after a declared rebase
	SteadyRequests   int     `json:"steady_requests"`
	RebaseRequests   int     `json:"rebase_requests"`
	ColdStarts       int     `json:"cold_starts"`    // main requests that read nothing from cache
	FirstRequests    int     `json:"first_requests"` // first main request of each agent
	WarmFirst        int     `json:"warm_first"`     // ...of which read the shared prefix (fan-out win)
	AvgContext       float64 `json:"avg_context"`
	MaxContext       int     `json:"max_context"`
	AvgContextNaive  float64 `json:"avg_context_naive"` // estimate: no compaction
	ContextReduction float64 `json:"context_reduction"` // 1 - avg/naive
	ExpectedRead     int64   `json:"expected_read"`     // guard's expectation, summed over requests that had one
	ActualRead       int64   `json:"actual_read"`       // provider-reported reads on those requests
	NoReadReports    bool    `json:"no_read_reports"`   // the provider never reported a cache read
}

// PriceUsed is one model's price as the inspector applied it.
type PriceUsed struct {
	Model       string  `json:"model"`
	Canonical   string  `json:"canonical"`
	Source      string  `json:"source"` // "table" | "fallback"
	InputPerM   float64 `json:"input_per_m"`
	OutputPerM  float64 `json:"output_per_m"`
	ReadPerM    float64 `json:"read_per_m"`
	Write5mPerM float64 `json:"write5m_per_m"`
	Write1hPerM float64 `json:"write1h_per_m"`
	TTLSeconds  int     `json:"ttl_s"`
	Explicit    bool    `json:"explicit"`
	Requests    int     `json:"requests"`
}

// Money splits a bill by what was paid for.
type Money struct {
	Uncached float64 `json:"uncached"` // input tokens at the plain price
	Read     float64 `json:"read"`     // cache reads
	Write    float64 `json:"write"`    // cache writes (with premium)
	Output   float64 `json:"output"`
	Total    float64 `json:"total"`
}

// CostReport compares what was paid with two counterfactuals. Every figure but
// Reported is priced from the inspector's price table so the scenarios are
// comparable with each other; Reported is what the log says was billed.
type CostReport struct {
	Reported         float64     `json:"reported"`          // sum of recorded cost_usd
	ReportedRequests int         `json:"reported_requests"` // responses that carried cost_usd
	GatewayRequests  int         `json:"gateway_requests"`  // ...of which the gateway reported
	Actual           Money       `json:"actual"`            // usage x table prices
	NoCache          Money       `json:"no_cache"`          // all prompt tokens at the plain input price
	Naive            Money       `json:"naive"`             // ESTIMATE: whole history, end-of-turn caching
	SavedNoCache     float64     `json:"saved_no_cache"`
	SavedNoCachePct  float64     `json:"saved_no_cache_pct"`
	SavedNaive       float64     `json:"saved_naive"`
	SavedNaivePct    float64     `json:"saved_naive_pct"`
	CompactorUSD     float64     `json:"compactor_usd"`
	Divergence       float64     `json:"divergence"` // (reported - actual) / actual, when both are known
	Prices           []PriceUsed `json:"prices"`
	Assumptions      []string    `json:"assumptions"`
}

// CompactionTotals summarise generational compaction over the session.
type CompactionTotals struct {
	Commits      int     `json:"commits"`
	Fork         int     `json:"fork"`
	Mask         int     `json:"mask"`
	Emergency    int     `json:"emergency"`
	Fallbacks    int     `json:"fallbacks"` // fork commits that fell back to a mechanical patch
	Rejects      int     `json:"rejects"`
	Held         int     `json:"held"`   // planner said "not yet" to a ready patch
	Folded       int64   `json:"folded"` // thread tokens removed (gross)
	SpineAdded   int64   `json:"spine_added"`
	Net          int64   `json:"net"` // folded - spine added: what the prompt shrank by
	CompactorUSD float64 `json:"compactor_usd"`
	Rebases      int     `json:"rebases"` // declared rebases: commits + shared syncs/epochs
}

// AnomalyTotals counts guard findings.
type AnomalyTotals struct {
	Total  int `json:"total"`
	Drift  int `json:"drift"`
	LowHit int `json:"low_hit"`
	// Undeclared counts requests whose layers changed with no declared rebase,
	// found by comparing consecutive requests independently of the guard.
	Undeclared int `json:"undeclared"`
}

// SwarmTotals summarise coordination.
type SwarmTotals struct {
	Agents        int `json:"agents"`
	Spawns        int `json:"spawns"`
	BoardOps      int `json:"board_ops"`
	MailSent      int `json:"mail_sent"`
	MailDelivered int `json:"mail_delivered"`
	LeaseEvents   int `json:"lease_events"`
	Alerts        int `json:"alerts"`
	Tasks         int `json:"tasks"`
	Running       int `json:"running"`
	PeakInFlight  int `json:"peak_in_flight"`
}

// SeriesPoint aggregates a run of consecutive requests.
type SeriesPoint struct {
	I       int     `json:"i"` // index of the first request in the group
	T       int64   `json:"t"` // ms since session start of the group's last request
	N       int     `json:"n"` // requests in the group
	Prompt  int64   `json:"prompt"`
	In      int64   `json:"in"`
	Read    int64   `json:"read"`
	Write   int64   `json:"write"`
	Out     int64   `json:"out"`
	Hit     float64 `json:"hit"`
	USD     float64 `json:"usd"`    // reported cost, else priced
	Priced  float64 `json:"priced"` // usage x table prices: comparable with no_cache and naive
	NoCache float64 `json:"no_cache"`
	Naive   float64 `json:"naive"`
	Anom    int     `json:"anom"`
	Commits int     `json:"commits"`
}

// Series is a downsampled view of the request history for overview charts.
type Series struct {
	Points []SeriesPoint `json:"points"`
	Group  int           `json:"group"`  // requests per point
	Window int           `json:"window"` // requests represented (the retained window)
}

// Outcome is an "outcome" event: a reward signal recorded for training.
type Outcome struct {
	Seq     uint64    `json:"seq"`
	T       time.Time `json:"t"`
	Agent   string    `json:"agent,omitempty"`
	Kind    string    `json:"kind"`
	Score   float64   `json:"score"`
	Pass    bool      `json:"pass"`
	Version string    `json:"version,omitempty"`
}

// EpisodeInfo is the subset of an RL episode.json the inspector shows.
type EpisodeInfo struct {
	ID         string             `json:"id"`
	TaskID     string             `json:"task_id,omitempty"`
	Group      string             `json:"group,omitempty"`
	Sample     int                `json:"sample"`
	Policy     string             `json:"policy,omitempty"`
	Pass       *bool              `json:"pass,omitempty"`
	Score      *float64           `json:"score,omitempty"`
	Claimed    string             `json:"claimed,omitempty"`
	Reward     float64            `json:"reward"`
	Components map[string]float64 `json:"components,omitempty"`
	Flags      []string           `json:"flags,omitempty"`
	CostUSD    float64            `json:"cost_usd,omitempty"`
	CostITE    float64            `json:"cost_ite,omitempty"`
	Requests   int                `json:"requests,omitempty"`
	Agents     int                `json:"agents,omitempty"`
}

// RLSummary gathers the RL fields that are present in a log.
type RLSummary struct {
	Episode     *EpisodeInfo   `json:"episode,omitempty"`
	Outcomes    []Outcome      `json:"outcomes,omitempty"`
	Kinds       map[string]int `json:"kinds,omitempty"` // requests per kind
	WireHashes  int            `json:"wire_hashes"`     // requests that recorded a wire hash
	TokenTraces int            `json:"token_traces"`    // responses that captured token ids
	Renderers   []string       `json:"renderers,omitempty"`
}

// Summary is /api/summary: everything the header and overview need.
type Summary struct {
	Session    SessionMeta      `json:"session"`
	State      string           `json:"state"`
	Rev        uint64           `json:"rev"`
	Log        LogMeta          `json:"log"`
	Totals     Totals           `json:"totals"`
	Cache      CacheStats       `json:"cache"`
	Cost       CostReport       `json:"cost"`
	Compaction CompactionTotals `json:"compaction"`
	Anomalies  AnomalyTotals    `json:"anomalies"`
	Swarm      SwarmTotals      `json:"swarm"`
	Series     Series           `json:"series"`
	RL         *RLSummary       `json:"rl,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
}

// ToolStat aggregates one tool's calls.
type ToolStat struct {
	Name      string  `json:"name"`
	Calls     int     `json:"calls"`
	Errors    int     `json:"errors"`
	Truncated int     `json:"truncated"`
	TotalMs   int64   `json:"total_ms"`
	MaxMs     int64   `json:"max_ms"`
	AvgMs     float64 `json:"avg_ms"`
	Chars     int64   `json:"chars"`
}

// AgentView is one agent's row.
type AgentView struct {
	ID          string    `json:"id"`
	Role        string    `json:"role"`
	Model       string    `json:"model,omitempty"`
	Parent      string    `json:"parent,omitempty"`
	Task        string    `json:"task,omitempty"`
	State       string    `json:"state"` // running | waiting | idle | failed | done
	Line        string    `json:"line,omitempty"`
	Started     time.Time `json:"started"`
	Last        time.Time `json:"last"`
	Requests    int       `json:"requests"`
	Main        int       `json:"main"`
	Side        int       `json:"side"`
	Input       int64     `json:"input"`
	CacheRead   int64     `json:"cache_read"`
	CacheWrite  int64     `json:"cache_write"`
	Output      int64     `json:"output"`
	HitRatio    float64   `json:"hit_ratio"`
	CostUSD     float64   `json:"cost_usd"`
	Context     int       `json:"context"` // prompt tokens of the latest main request
	AvgContext  float64   `json:"avg_context"`
	MaxContext  int       `json:"max_context"`
	Compactions int       `json:"compactions"`
	Folded      int64     `json:"folded"` // net tokens folded by compaction
	Anomalies   int       `json:"anomalies"`
	ToolCalls   int       `json:"tool_calls"`
	ToolErrors  int       `json:"tool_errors"`
	Turns       int       `json:"turns"`
	Ended       bool      `json:"ended"`
	EndState    string    `json:"end_state,omitempty"`
	Evidence    string    `json:"evidence,omitempty"`
	SparkCtx    []int     `json:"spark_ctx,omitempty"`
	SparkHit    []float64 `json:"spark_hit,omitempty"`
}

// AgentDetail is /api/agent/{id}.
type AgentDetail struct {
	Agent       AgentView    `json:"agent"`
	Tools       []ToolStat   `json:"tools"`
	Compactions []Compaction `json:"compactions"`
	Anomalies   []Anomaly    `json:"anomalies"`
	Rebases     int          `json:"rebases"`
}

// Req is one model call, as a row.
type Req struct {
	ID          string   `json:"id"`
	Seq         uint64   `json:"seq"`
	Rev         uint64   `json:"rev"`
	Agent       string   `json:"agent"`
	Role        string   `json:"role,omitempty"`
	Kind        string   `json:"kind"`
	Model       string   `json:"model,omitempty"`
	TS          int64    `json:"ts"` // unix ms
	T           int64    `json:"t"`  // ms since session start
	Done        bool     `json:"done"`
	Failed      bool     `json:"failed,omitempty"`
	Err         string   `json:"err,omitempty"`
	In          int      `json:"in"`
	Read        int      `json:"read"`
	Write       int      `json:"write"`
	Out         int      `json:"out"`
	Prompt      int      `json:"prompt"`
	Hit         float64  `json:"hit"`
	Expected    int      `json:"expected"`
	Anomaly     bool     `json:"anomaly,omitempty"`
	AnomalyKind string   `json:"anomaly_kind,omitempty"` // drift | low_hit
	USD         float64  `json:"usd"`                    // the log's cost_usd for this call, or Priced when the log has none
	Gateway     bool     `json:"gateway,omitempty"`      // USD is the gateway's own figure
	Priced      float64  `json:"priced"`                 // this call's usage at the inspector's price table
	NoCache     float64  `json:"no_cache"`
	Naive       float64  `json:"naive,omitempty"`
	NaiveCtx    int      `json:"naive_ctx,omitempty"`
	TTFB        int64    `json:"ttfb_ms,omitempty"`
	TotalMs     int64    `json:"total_ms,omitempty"`
	Stop        string   `json:"stop,omitempty"`
	Cold        bool     `json:"cold,omitempty"`
	First       bool     `json:"first,omitempty"`
	Rebase      string   `json:"rebase,omitempty"`  // declared rebase just before this request
	Changed     []string `json:"changed,omitempty"` // layers whose bytes changed since the agent's previous request
	Undeclared  bool     `json:"undeclared,omitempty"`
	ThreadFrom  int64    `json:"thread_from,omitempty"`
	ThreadTo    int64    `json:"thread_to,omitempty"`
	Layers      [7]int   `json:"layers"` // tokens per layer G0..G6; see LayerReport for provenance
	G0Known     bool     `json:"g0_known"`
	Epoch       int      `json:"epoch"` // commits before this request
	WireHash    string   `json:"wire_hash,omitempty"`
}

// RequestPage is /api/requests.
type RequestPage struct {
	Total    int    `json:"total"`    // retained requests matching the filter
	Retained int    `json:"retained"` // retained requests overall
	Dropped  int    `json:"dropped"`  // requests evicted from the window
	Rev      uint64 `json:"rev"`      // pass as since= to receive changes only
	More     bool   `json:"more"`
	Requests []Req  `json:"requests"`
}

// RequestQuery filters /api/requests.
type RequestQuery struct {
	Agent string
	Kind  string
	Since uint64 // rev cursor: only requests created or changed after it
	Tail  bool   // newest Limit requests instead of changes since Since
	Limit int
}

// CompStep is one line of a compaction's decision trail.
type CompStep struct {
	T    time.Time `json:"t"`
	Seq  uint64    `json:"seq"`
	Kind string    `json:"kind"` // start | request | ready | reject | plan | commit
	Text string    `json:"text"`
}

// CompNext is what the first request after a commit paid.
type CompNext struct {
	Req     string  `json:"req"`
	Hit     float64 `json:"hit"`
	Rewrite int     `json:"rewrite"` // uncached + written tokens: the visible part of the rebase
	Prompt  int     `json:"prompt"`
}

// Compaction is one compaction episode: plan, patch, decision, commit.
type Compaction struct {
	N              int        `json:"n"`
	Agent          string     `json:"agent"`
	Seq            uint64     `json:"seq"` // first event
	Start          time.Time  `json:"start"`
	Commit         time.Time  `json:"commit,omitempty"`
	CommitMs       int64      `json:"commit_ms"` // ms since session start (0 until committed)
	StartMs        int64      `json:"start_ms"`
	Status         string     `json:"status"` // committed | ready | running | failed
	Mode           string     `json:"mode"`   // fork | mask | emergency
	Patch          string     `json:"patch"`  // model | mechanical
	Trigger        string     `json:"trigger,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	Warm           *bool      `json:"warm,omitempty"`
	ThreadTokens   int        `json:"thread_tokens"`
	Removed        int        `json:"removed"`
	Retained       int        `json:"retained"`
	Spine          int        `json:"spine"`
	Net            int        `json:"net"`
	Masked         int        `json:"masked"`
	MechLines      int        `json:"mech_lines"`
	NotesChanged   bool       `json:"notes_changed"`
	NotesOver      bool       `json:"notes_over_budget,omitempty"`
	HeldMs         int64      `json:"held_ms"`
	ProposeMs      int64      `json:"propose_ms"` // plan to ready patch
	Holds          int        `json:"holds"`      // "commit?" answered no before the commit
	NetITE         float64    `json:"net_ite"`
	Calls          int        `json:"compactor_calls"`
	CompactorUSD   float64    `json:"compactor_usd"`
	CompactorITE   float64    `json:"compactor_ite"`
	FallbackReason string     `json:"fallback_reason,omitempty"`
	Rejects        []string   `json:"rejects,omitempty"`
	Warnings       []string   `json:"warnings,omitempty"`
	PromptBefore   int        `json:"prompt_before"`
	Next           *CompNext  `json:"next,omitempty"`
	Steps          []CompStep `json:"steps"`
}

// CompactionReport is /api/compactions.
type CompactionReport struct {
	Totals      CompactionTotals `json:"totals"`
	Compactions []Compaction     `json:"compactions"`
}

// Anomaly is one guard finding with the evidence the log holds for it.
type Anomaly struct {
	N            int       `json:"n"`
	Seq          uint64    `json:"seq"`
	T            time.Time `json:"t"`
	TMs          int64     `json:"t_ms"`
	Agent        string    `json:"agent"`
	Kind         string    `json:"kind"`     // drift | low_hit
	Severity     string    `json:"severity"` // error | warning
	Req          string    `json:"req,omitempty"`
	Diverged     string    `json:"diverged,omitempty"`
	SharedBlocks int       `json:"shared_blocks,omitempty"`
	Expected     int       `json:"expected,omitempty"`
	Actual       int       `json:"actual,omitempty"`
	GapMs        int64     `json:"gap_ms,omitempty"` // since the agent's previous request
	TTLMs        int64     `json:"ttl_ms,omitempty"`
	Changed      []string  `json:"changed,omitempty"`
	Rebase       string    `json:"rebase,omitempty"`
	KeyChanged   bool      `json:"key_changed,omitempty"`
	Title        string    `json:"title"`
	Explain      []string  `json:"explain"`
	Causes       []string  `json:"causes,omitempty"`
}

// AnomalyReport is /api/anomalies.
type AnomalyReport struct {
	Totals    AnomalyTotals `json:"totals"`
	Anomalies []Anomaly     `json:"anomalies"`
	Checked   int           `json:"checked"` // main requests the guard observed
}

// TaskView is a board task, reconstructed from tool calls and board ops.
type TaskView struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
	Owner  string `json:"owner,omitempty"`
	Role   string `json:"role,omitempty"`
	Line   string `json:"line,omitempty"`
	Result string `json:"result,omitempty"`
	// Evidence is what the harness itself recorded when the worker submitted
	// (the verifier's verdict), as opposed to Result, which is the worker's word.
	Evidence string    `json:"evidence,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	Deps     []string  `json:"deps,omitempty"`
	Files    []string  `json:"files,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}

// MinutePoint is one minute of coordination activity.
type MinutePoint struct {
	T         int64 `json:"t"` // unix ms at the start of the minute
	Board     int   `json:"board"`
	MailSent  int   `json:"mail_sent"`
	MailDeliv int   `json:"mail_delivered"`
	Spawns    int   `json:"spawns"`
	Leases    int   `json:"leases"`
	Requests  int   `json:"requests"`
}

// MailView is one message.
type MailView struct {
	T    time.Time `json:"t"`
	ID   string    `json:"id"`
	From string    `json:"from"`
	To   string    `json:"to"`
	Kind string    `json:"kind,omitempty"`
	Text string    `json:"text,omitempty"`
	// LatencyMs is send to deliver (both are logged by the router).
	LatencyMs int64 `json:"latency_ms"`
}

// MailPair counts messages from one agent to another.
type MailPair struct {
	From string `json:"from"`
	To   string `json:"to"`
	N    int    `json:"n"`
}

// SpawnView is one agent.spawn.
type SpawnView struct {
	T      time.Time `json:"t"`
	ID     string    `json:"id"`
	Role   string    `json:"role"`
	Parent string    `json:"parent,omitempty"`
	Task   string    `json:"task,omitempty"`
	Model  string    `json:"model,omitempty"`
}

// LeaseView is one lease event (leases are logged generically: producers may add fields).
type LeaseView struct {
	T      time.Time `json:"t"`
	Agent  string    `json:"agent,omitempty"`
	Action string    `json:"action,omitempty"`
	Path   string    `json:"path,omitempty"`
	Holder string    `json:"holder,omitempty"`
}

// GovernorStats combines what the request stream implies with any governor events.
type GovernorStats struct {
	PeakInFlight int            `json:"peak_in_flight"`
	InFlight     int            `json:"in_flight"`
	PeakRPM      int            `json:"peak_rpm"`
	AvgRPM       float64        `json:"avg_rpm"`
	Requests     int            `json:"requests"`
	Retries      int            `json:"retries"`
	RateLimited  int            `json:"rate_limited"`
	Errors       map[string]int `json:"errors,omitempty"`
	Events       int            `json:"events"`
	Last         map[string]any `json:"last,omitempty"` // last governor event payload, if any
}

// SwarmReport is /api/swarm.
type SwarmReport struct {
	Totals    SwarmTotals    `json:"totals"`
	Agents    []AgentView    `json:"agents"`
	Tasks     []TaskView     `json:"tasks"`
	TaskCount map[string]int `json:"task_count"`
	Tasksrc   string         `json:"tasks_source"` // how tasks were obtained
	BoardOps  map[string]int `json:"board_ops"`
	MailKinds map[string]int `json:"mail_kinds"`
	Pairs     []MailPair     `json:"mail_pairs"`
	Mail      []MailView     `json:"mail"`
	Spawns    []SpawnView    `json:"spawns"`
	Leases    []LeaseView    `json:"leases"`
	Minutes   []MinutePoint  `json:"minutes"`
	Governor  GovernorStats  `json:"governor"`
	Alerts    int            `json:"alerts"`
	// Isolation, Mailman and Supervision are present only when the log has something of
	// them: a worktree run, the mailman, a held or woken manager.
	Isolation   *IsolationView   `json:"isolation,omitempty"`
	Mailman     *MailmanView     `json:"mailman,omitempty"`
	Supervision *SupervisionView `json:"supervision,omitempty"`
}

// TreeCounts counts the git worktrees of an isolated run: one per writer.
type TreeCounts struct {
	Created int `json:"created"`
	Removed int `json:"removed"`
	Pruned  int `json:"pruned"` // leftovers of a run that died, removed by a later one
	Commits int `json:"commits"`
	Resets  int `json:"resets"`
}

// MergeView is one submission of a worker's tree to the merge queue (task.merge).
type MergeView struct {
	T       time.Time `json:"t"`
	Agent   string    `json:"agent,omitempty"`
	Task    string    `json:"task"`
	Outcome string    `json:"outcome"` // merged | empty | conflict | verify_failed | rejected | error
	Commit  string    `json:"commit,omitempty"`
	Files   []string  `json:"files,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

// IntegrationView is the end of an isolated run: whether the verified result reached
// the user's checkout, and where it is if it did not.
type IntegrationView struct {
	T         time.Time `json:"t"`
	Branch    string    `json:"branch"`
	Tip       string    `json:"tip,omitempty"`
	Applied   bool      `json:"applied"`
	Committed bool      `json:"committed,omitempty"` // as commits on the user's branch, not as edits
	Files     int       `json:"files"`
	Reason    string    `json:"reason,omitempty"` // why it was not applied
}

// IsolationView is the worktree side of a swarm run with swarm.isolation = worktree.
type IsolationView struct {
	Trees TreeCounts `json:"trees"`
	// Queue counts the merge queue's own events (queued, merged, fast_forward, conflict,
	// verify_failed, rolled_back, rejected); Submissions the swarm's account of each
	// task's submission by outcome.
	Queue       map[string]int   `json:"queue"`
	Submissions map[string]int   `json:"submissions"`
	Merges      []MergeView      `json:"merges"` // newest first
	Integration *IntegrationView `json:"integration,omitempty"`
}

// MailmanView is what the mailman did with worker mail (swarm.mailman).
type MailmanView struct {
	Routed         int            `json:"routed"`          // messages handed to the mailman's ledger
	Batches        int            `json:"batches"`         // requests to the mailman
	Parcels        int            `json:"parcels"`         // messages those covered
	Digests        int            `json:"digests"`         // digests delivered
	Digested       int            `json:"digested"`        // original messages the digests covered
	DirectMessages int            `json:"direct_messages"` // messages delivered directly instead
	Direct         map[string]int `json:"direct"`          // ... by reason
	Outages        int            `json:"outages"`         // times the mailman was given up on for a while
	State          string         `json:"state"`           // up | down, as last seen
	Reason         string         `json:"reason,omitempty"`
}

// SupervisionView is how the manager was held to its board (batch runs) and woken when
// idle (interactive sessions).
type SupervisionView struct {
	Holds      int    `json:"holds"`       // final answers sent back because work was unfinished
	Unfinished int    `json:"unfinished"`  // runs that ended with work still on the board
	Wakes      int    `json:"wakes"`       // automatic manager runs
	WakePaused int    `json:"wake_paused"` // times the bound on automatic runs was reached
	WakeLimits int    `json:"wake_limits"` // workers whose peer mail stopped waking them
	LastHold   string `json:"last_hold,omitempty"`
	LastWake   string `json:"last_wake,omitempty"`
}

// LayerState says what happened to a layer since the agent's previous request.
const (
	LayerNew         = "new"         // did not exist in the previous request
	LayerCached      = "cached"      // same bytes, and nothing before it changed
	LayerRewritten   = "rewritten"   // its own bytes changed (or it is new)
	LayerInvalidated = "invalidated" // same bytes, but a layer before it changed
	LayerAppended    = "appended"    // thread only: previous turns kept, new turns added
	LayerRemoved     = "removed"
	LayerAbsent      = "absent"
	LayerFresh       = "fresh" // hot tail: rewritten every request by design
)

// LayerInfo is one layer of one request.
type LayerInfo struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	Tokens     int    `json:"tokens"`
	Source     string `json:"source"` // recorded | blob | derived | unknown
	Hash       string `json:"hash,omitempty"`
	Breakpoint bool   `json:"breakpoint,omitempty"`
	State      string `json:"state"`
	Changed    bool   `json:"changed"` // this layer's own bytes differ from the previous request's
	Start      int    `json:"start"`   // token offset where the layer begins
	// Billing split: how many of the layer's tokens the provider served from
	// cache, wrote to cache, or processed uncached (by position along the prompt).
	Read     int    `json:"read"`
	Write    int    `json:"write"`
	Fresh    int    `json:"fresh"`
	Bytes    int    `json:"bytes,omitempty"`
	PrevHash string `json:"prev_hash,omitempty"`
	HasText  bool   `json:"has_text"`
	Text     string `json:"text,omitempty"`
	TextCut  bool   `json:"text_cut,omitempty"`
}

// LayerDiff locates the first changed byte of a rewritten layer.
type LayerDiff struct {
	Layer     string `json:"layer"`
	At        int    `json:"at"`
	PrevBytes int    `json:"prev_bytes"`
	Bytes     int    `json:"bytes"`
	Before    string `json:"before"`
	After     string `json:"after"`
	Split     int    `json:"split"` // characters of Before/After that are common context
}

// LayerReport is /api/layers.
type LayerReport struct {
	Req         Req         `json:"req"`
	Prev        string      `json:"prev,omitempty"`
	Next        string      `json:"next,omitempty"`
	Layers      []LayerInfo `json:"layers"`
	Prompt      int         `json:"prompt"`
	Read        int         `json:"read"`
	Write       int         `json:"write"`
	Fresh       int         `json:"fresh"`
	Expected    int         `json:"expected"`
	Breakpoints int         `json:"breakpoints"`
	Rebase      string      `json:"rebase,omitempty"`
	FirstChange string      `json:"first_change,omitempty"`
	KeyChanged  bool        `json:"key_changed"`
	CacheKey    string      `json:"cache_key,omitempty"`
	PrefixKey   string      `json:"prefix_key,omitempty"`
	Ratio       float64     `json:"bytes_per_token"`
	Diffs       []LayerDiff `json:"diffs,omitempty"`
	Notes       []string    `json:"notes,omitempty"`
}

// EventRow is one raw log event for the live view.
type EventRow struct {
	Seq   uint64    `json:"seq"`
	TS    time.Time `json:"ts"`
	Agent string    `json:"agent,omitempty"`
	Type  string    `json:"type"`
	Cause uint64    `json:"cause,omitempty"`
	// Data is the payload as JSON. Payloads over the cap are replaced by
	// {"_truncated":true,"_bytes":N,"_preview":"..."}.
	Data json.RawMessage `json:"data,omitempty"`
}

// EventPage is /api/events.
type EventPage struct {
	Events  []EventRow `json:"events"`
	Next    uint64     `json:"next"`     // pass as since= to continue
	LastSeq uint64     `json:"last_seq"` // newest event known
	More    bool       `json:"more"`
}

// EventQuery filters /api/events.
type EventQuery struct {
	Since uint64
	Tail  bool // since is ignored: return the newest Limit events
	Limit int
	Agent string
	Type  string
}

// Digest is a session's one-line summary for the session list.
type Digest struct {
	Model      string    `json:"model,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	State      string    `json:"state"`
	Swarm      bool      `json:"swarm"`
	Start      time.Time `json:"start"`
	DurationMs int64     `json:"duration_ms"`
	Requests   int       `json:"requests"`
	Agents     int       `json:"agents"`
	Commits    int       `json:"commits"`
	Anomalies  int       `json:"anomalies"`
	HitRatio   float64   `json:"hit_ratio"`
	CostUSD    float64   `json:"cost_usd"`
	SavedPct   float64   `json:"saved_pct"` // versus no cache
	Goal       string    `json:"goal,omitempty"`
}

// SessionInfo is one row of /api/sessions.
type SessionInfo struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Bytes   int64        `json:"bytes"`
	Updated time.Time    `json:"updated"`
	Live    bool         `json:"live"`
	Digest  *Digest      `json:"digest,omitempty"`
	Episode *EpisodeInfo `json:"episode,omitempty"`
}

// SessionList is /api/sessions.
type SessionList struct {
	Mode     string        `json:"mode"` // single | multi
	Root     string        `json:"root"`
	Current  string        `json:"current,omitempty"`
	Sessions []SessionInfo `json:"sessions"`
	Version  string        `json:"version,omitempty"`
}
