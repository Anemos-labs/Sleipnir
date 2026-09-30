package state

import (
	"time"
)

// Status is what an agent is doing now, derived from its events (docs/UX.md: think, tool, edit, wait, idle, done, stuck).
type Status string

// The statuses. Starting is an agent that has been spawned or given work and has not yet sent a request; Thinking one whose
// model request is in flight (or whose tool results are being turned into the next request); Tool and Editing one that runs a
// tool (Editing for edit, write and apply_patch); Waiting one inside the swarm's wait tool (waiting for the team's mail or
// tasks); Idle one whose run ended and can be given more work; Stuck one that the repetition guard has told it is repeating
// one failing call (until a call succeeds or its run ends); Done one whose run finished for good (a manager that has answered,
// or a worker whose task was accepted); Error one that stopped with a failure.
const (
	StatusStarting Status = "starting"
	StatusThinking Status = "thinking"
	StatusTool     Status = "tool"
	StatusEditing  Status = "editing"
	StatusWaiting  Status = "waiting"
	StatusIdle     Status = "idle"
	StatusStuck    Status = "stuck"
	StatusDone     Status = "done"
	StatusError    Status = "error"
)

// Active reports whether the status is one of a working agent (not idle, done or failed).
func (s Status) Active() bool {
	switch s {
	case StatusStarting, StatusThinking, StatusTool, StatusEditing, StatusWaiting, StatusStuck:
		return true
	}
	return false
}

// Session is what session.start, session.end and the first events say about the run.
type Session struct {
	ID       string `json:"id,omitempty"`
	Version  string `json:"version,omitempty"` // the harness version
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Dialect  string `json:"dialect,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Root     string `json:"root,omitempty"`
	// Mode is the permission mode. session.start does not carry it today (internal/session/session.go writes the map), so it is
	// empty unless a producer adds "mode" (or "permission_mode") to it.
	Mode     string `json:"mode,omitempty"`
	Renderer string `json:"renderer,omitempty"` // the prompt layout version (kv.RendererVersion)
	// Swarm is true for a session with a manager and workers: session.start says so, and so does the first agent.spawn.
	Swarm       bool   `json:"swarm,omitempty"`
	Isolation   string `json:"isolation,omitempty"` // "worktree" when every writer has a tree of its own
	Mailman     bool   `json:"mailman,omitempty"`
	Resumed     bool   `json:"resumed,omitempty"`
	Schema      int    `json:"schema,omitempty"`
	ReconTokens int    `json:"recon_tokens,omitempty"`
	SharedHash  string `json:"shared_hash,omitempty"`

	Started    time.Time `json:"started,omitzero"` // session.start, else the first event
	Ended      bool      `json:"ended,omitempty"`
	EndedAt    time.Time `json:"ended_at,omitzero"`
	EndReason  string    `json:"end_reason,omitempty"`
	EndCostUSD float64   `json:"end_cost_usd,omitempty"` // what the harness reported at session.end

	// Goal is the first thing a person typed, one line.
	Goal string `json:"goal,omitempty"`
	// Epochs counts the shared-epoch layer commits: a change of the prefix every agent shares.
	Epochs int `json:"epochs,omitempty"`
}

// Tokens is token accounting as core.Usage reports it: Input is the uncached input, so the whole prompt is
// Input+CacheRead+CacheWrite.
type Tokens struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning,omitempty"`
}

// Prompt is the size of the prompts: what was processed, whatever it was billed as.
func (t Tokens) Prompt() int64 { return t.Input + t.CacheRead + t.CacheWrite }

// HitRatio is the fraction of the prompt tokens that were read from cache, weighted by tokens (not the mean of the ratios of
// the requests), or zero when there was no prompt.
func (t Tokens) HitRatio() float64 {
	if p := t.Prompt(); p > 0 {
		return float64(t.CacheRead) / float64(p)
	}
	return 0
}

// Agent is one worker, the manager, or the single agent of a session.
type Agent struct {
	ID      string `json:"id"`
	Role    string `json:"role,omitempty"`
	Parent  string `json:"parent,omitempty"` // who spawned it
	Model   string `json:"model,omitempty"`
	Task    string `json:"task,omitempty"` // the task it was given or holds (T3)
	Service bool   `json:"service,omitempty"`
	// Retired is true once the swarm removed the agent from its roster (an idle worker is retired after 15 minutes).
	Retired bool `json:"retired,omitempty"`

	Status Status `json:"status"`
	// Line is the harness-derived one-line status that the swarm writes ("running a command"): the kind of tool, never its arguments.
	Line string `json:"line,omitempty"`
	// Tool is the tool running now (the newest, when several run), ToolSummary what it was asked to do ("go test ./...",
	// a path, a pattern), one line.
	Tool        string    `json:"tool,omitempty"`
	ToolSummary string    `json:"tool_summary,omitempty"`
	ToolSince   time.Time `json:"tool_since,omitzero"`
	OpenTools   int       `json:"open_tools,omitempty"`

	Spawned    time.Time `json:"spawned,omitzero"`
	SpawnSeq   uint64    `json:"spawn_seq,omitempty"` // the seq of the agent.spawn, for the fork animation
	LastActive time.Time `json:"last_active,omitzero"`
	EndState   string    `json:"end_state,omitempty"` // the state of the last agent.end: idle, failed
	Evidence   string    `json:"evidence,omitempty"`  // what the harness observed the worker do (edited files, tests run)

	Tokens Tokens `json:"tokens"`
	// CostUSD is what the responses reported (model.response cost_usd, the gateway's number when it gave one), compactor calls included.
	CostUSD  float64 `json:"cost_usd"`
	SavedUSD float64 `json:"saved_usd,omitempty"` // the saving of this agent's cache reads, at list price where the model's price is known
	// CtxTokens is the context size the board last showed for it (board.op agent). The harness writes 0 there today
	// (internal/swarm/lifecycle.go publish does not set it), so a gauge of how full a context is comes from Snapshot.ContextFill:
	// the prompt of the agent's latest answered request over the window of its model.
	CtxTokens int `json:"ctx_tokens,omitempty"`

	Requests     int `json:"requests"`                // main requests that were answered: the length of the hit-ratio history
	SideRequests int `json:"side_requests,omitempty"` // compactor forks and the like
	InFlight     int `json:"in_flight,omitempty"`     // requests sent and not answered
	Errors       int `json:"errors,omitempty"`        // requests that failed for good
	Retries      int `json:"retries,omitempty"`       // attempts repeated after a retryable failure
	ToolCalls    int `json:"tool_calls,omitempty"`
	ToolErrors   int `json:"tool_errors,omitempty"`
	Compactions  int `json:"compactions,omitempty"`

	// Compacting is true while a compaction of its thread is being worked out, from the planner's decision to its commit.
	Compacting bool  `json:"compacting,omitempty"`
	Stuck      Stuck `json:"stuck,omitzero"`
	// Scope is the paths its tasks in progress may touch; Leases the files it holds a write lease on (at most MaxLeasesPerAgent).
	Scope  []string `json:"scope,omitempty"`
	Leases []string `json:"leases,omitempty"`

	Stack     Stack        `json:"stack"`
	Hits      Hits         `json:"hits"`
	Compacts  []Compaction `json:"compacts,omitempty"`
	Anomalies []Anomaly    `json:"anomalies,omitempty"`
}

// Stuck is what agent.stuck said: the repetition guard told the agent it was repeating one failing call (phase "nudge", at the
// fourth failure) or ended its run (phase "stop", at the eighth).
type Stuck struct {
	Phase  string    `json:"phase,omitempty"` // the last phase seen
	Nudges int       `json:"nudges,omitempty"`
	Stops  int       `json:"stops,omitempty"`
	Note   string    `json:"note,omitempty"` // the harness's note to the model, or the error that ended the run
	At     time.Time `json:"at,omitzero"`
	// Active is true from the event until a tool call succeeds or the agent's run ends: the agent has not got out of it yet.
	Active bool `json:"active,omitempty"`
}

// Section is one layer of the prompt as the request recorded it: shared (G1), role (G2), notes (G3) and spine (G4). The
// constitution and tools (G0), the thread (G5) and the hot tail (G6) are not sections: see Stack.Unsectioned.
type Section struct {
	Name       string `json:"name"`
	Hash       string `json:"hash,omitempty"`
	Tokens     int    `json:"tokens"`
	Breakpoint bool   `json:"bp,omitempty"` // the planner put a cache marker after it
}

// Breakpoint is a cache marker of a request: the layer it follows and how long the provider was asked to keep the entry
// (zero: the provider's default).
type Breakpoint struct {
	Label      string `json:"label,omitempty"`
	TTLSeconds int    `json:"ttl_s,omitempty"`
}

// Stack is the prompt of an agent: its latest main request (the recipe) and what its latest response said about the cache.
type Stack struct {
	Req      string    `json:"req,omitempty"`
	Seq      uint64    `json:"seq,omitempty"`
	At       time.Time `json:"at,omitzero"`
	Model    string    `json:"model,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Dialect  string    `json:"dialect,omitempty"`
	Renderer string    `json:"renderer,omitempty"`

	Sections      []Section    `json:"sections,omitempty"`
	SectionTokens int          `json:"section_tokens,omitempty"` // the sum of the sections
	Breakpoints   []Breakpoint `json:"breakpoints,omitempty"`
	Tools         int          `json:"tools,omitempty"` // how many tools the request carried
	ThreadFrom    int64        `json:"thread_from,omitempty"`
	ThreadTo      int64        `json:"thread_to,omitempty"`
	// PrefixKey names the prefix shared by every agent with the same constitution, tools, shared pin and role pin; CacheKey is the
	// provider routing key.
	PrefixKey string `json:"prefix_key,omitempty"`
	CacheKey  string `json:"cache_key,omitempty"`
	// SharedBlocks and SharedTokens are the exact prefix this request has in common with the agent's previous one (the
	// drift guard's measure), 0 on its first request. The tokens are the harness's own estimate, not the provider's count, so
	// they do not divide by Prompt. Which agents share a prefix with each other is Snapshot.Prefixes.
	SharedBlocks int `json:"shared_blocks,omitempty"`
	SharedTokens int `json:"shared_tokens,omitempty"`
	// Inherited is true when the agent's first request joined a prefix (PrefixKey) that an earlier request of another agent had
	// used: its shared layers should be read from cache, not written.
	Inherited bool `json:"inherited,omitempty"`
	// ToolsHash, SystemHashes and HotHash are blob references to the tool list, the constitution and the hot tail: what a caller that
	// has the blob store needs to size G0 and G6, which the sections do not.
	ToolsHash    string   `json:"tools_hash,omitempty"`
	SystemHashes []string `json:"system_hashes,omitempty"`
	HotHash      string   `json:"hot_hash,omitempty"`

	// What the latest response said. Answered is true when it is the response to Req.
	Answered bool    `json:"answered,omitempty"`
	RespReq  string  `json:"resp_req,omitempty"`
	RespSeq  uint64  `json:"resp_seq,omitempty"` // the seq of the response, for the sweep animation
	Prompt   int     `json:"prompt,omitempty"`   // input+read+write tokens of the prompt
	Read     int     `json:"read,omitempty"`     // tokens the provider served from cache
	Write    int     `json:"write,omitempty"`    // tokens it wrote to cache
	Fresh    int     `json:"fresh,omitempty"`    // tokens it processed uncached
	Hit      float64 `json:"hit,omitempty"`
	// ExpectedRead is what the harness expected to be read (the guard's estimate, scaled to provider tokens), Missed how many
	// tokens short the read fell, Miss whether the harness judged that a cache miss (a response with anomaly true) and
	// ExpectedCold that the entry was past its lifetime, so that a low read was no surprise.
	ExpectedRead int  `json:"expected_read,omitempty"`
	Missed       int  `json:"missed,omitempty"`
	Miss         bool `json:"miss,omitempty"`
	ExpectedCold bool `json:"expected_cold,omitempty"`
	// Unsectioned is the prompt minus the sections: the constitution and tools, the thread and the hot tail together, which the
	// request does not size apart. It is zero until there is a response, and never negative.
	Unsectioned int `json:"unsectioned,omitempty"`

	// Epochs counts the times this agent re-synced to a new shared layer (layer.commit shared-sync), LastCommit the newest
	// layer commit it made.
	Epochs     int    `json:"epochs,omitempty"`
	LastCommit string `json:"last_commit,omitempty"` // "scope: reason"
}

// MarkKind says what a sparkline marker stands for.
type MarkKind string

// The kinds of marker on the hit-ratio history: a cache anomaly, a compaction commit, an epoch (the agent re-synced to a new
// shared layer) and a rebase (thinking stripped after the provider rejected a block).
const (
	MarkAnomaly    MarkKind = "anomaly"
	MarkCompaction MarkKind = "compaction"
	MarkEpoch      MarkKind = "epoch"
	MarkRebase     MarkKind = "rebase"
)

// Mark is a marker on the hit-ratio history. At is the index of the main request it precedes (the request it affects): the
// number of main responses that were folded in before it happened, counted from the agent's first.
type Mark struct {
	At   int      `json:"at"`
	Kind MarkKind `json:"kind"`
	Seq  uint64   `json:"seq"`
	Text string   `json:"text,omitempty"`
}

// Hits is the cache history of an agent: the hit ratio of each of its main requests, oldest first, and the markers for a
// sparkline. Only the last HistCap requests are kept: First is the index of Ratios[0] counted from the agent's first request,
// and a marker's At is on the same scale, so a marker is drawn at At-First.
type Hits struct {
	First  int       `json:"first,omitempty"`
	Ratios []float64 `json:"ratios,omitempty"`
	Marks  []Mark    `json:"marks,omitempty"`
}

// Len is the number of main requests the agent has had answered, the ones dropped from the ring included.
func (h Hits) Len() int { return h.First + len(h.Ratios) }

// Compaction is one compact.commit: what the thread was before and after and when it happened.
type Compaction struct {
	Seq    uint64    `json:"seq"`
	T      time.Time `json:"t,omitzero"`
	Agent  string    `json:"agent"`
	Mode   string    `json:"mode"` // fork (the compactor model's patch), mask (bulky results folded without a model) or emergency
	Reason string    `json:"reason,omitempty"`
	// Before and After are the thread in tokens: what the snapshot held, and what replaced it (the spine added plus the turns kept verbatim).
	Before      int `json:"before"`
	After       int `json:"after"`
	Removed     int `json:"removed,omitempty"`
	Retained    int `json:"retained,omitempty"`
	SpineAdded  int `json:"spine_added,omitempty"`
	RemovedTurn int `json:"removed_turns,omitempty"`
	// Masked counts the bulky results a mask compaction folded (MaskedTokens their size); Squeezed and SqueezedTokens are the same
	// for the results an emergency compaction squeezed, which is where most of its saving comes from.
	Masked         int   `json:"masked,omitempty"`
	MaskedTokens   int   `json:"masked_tokens,omitempty"`
	Squeezed       int   `json:"squeezed,omitempty"`
	SqueezedTokens int   `json:"squeezed_tokens,omitempty"`
	HeldMs         int64 `json:"held_ms,omitempty"`
	Fallback       bool  `json:"fallback,omitempty"` // the harness's mechanical patch was used instead of the model's
	// Moment says whether the cache was cold when the planner decided ("cold": the rewrite that follows costs nothing extra, the
	// entry had expired anyway; "warm": it is a declared, priced rebase), or "unknown" (an emergency compaction has no plan).
	Moment string `json:"moment"`
	// At is the hit-ratio index the commit precedes (see Mark).
	At int `json:"at"`
}

// Anomaly is one cache.anomaly: the harness saw the cache behave otherwise than its prompt said it should.
type Anomaly struct {
	Seq   uint64    `json:"seq"`
	T     time.Time `json:"t,omitzero"`
	Agent string    `json:"agent"`
	// Kind: drift (the prefix changed without a declared rebase), low_hit (the provider read much less than expected),
	// thinking_binding, thinking_dropped, notes_over_budget, or whatever a newer producer writes.
	Kind string `json:"kind"`
	// Layer is the layer that diverged: the first block of the prompt that differs from the previous request's.
	Layer    string `json:"layer,omitempty"`
	Req      string `json:"req,omitempty"`
	Expected int    `json:"expected,omitempty"` // tokens expected to be read
	Actual   int    `json:"actual,omitempty"`   // tokens that were
	Missed   int    `json:"missed,omitempty"`
	// MissUSD is what the miss cost at list price (missed tokens at the difference between the input and the cache-read price);
	// MissKnown is false when the model's price is unknown, and MissUSD is then zero.
	MissUSD   float64 `json:"miss_usd,omitempty"`
	MissKnown bool    `json:"miss_known,omitempty"`
	Note      string  `json:"note,omitempty"`
	At        int     `json:"at"` // the hit-ratio index it concerns (see Mark)
}

// Price is dollars per million tokens, the shape of cost.Price.
type Price struct {
	InputPerM        float64 `json:"input_per_m"`
	OutputPerM       float64 `json:"output_per_m"`
	CacheReadPerM    float64 `json:"cache_read_per_m"`
	CacheWrite5mPerM float64 `json:"cache_write_5m_per_m"`
	CacheWrite1hPerM float64 `json:"cache_write_1h_per_m"`
}

// ModelInfo is what the State knows about one model's price and cache lifetime, and where it learned it.
type ModelInfo struct {
	ID string `json:"id"`
	// Source: "session" (the prices the run recorded in session.start, which is what it was billed by), "table" (the repository's
	// built-in list price, internal/cost), "fallback" (session.start recorded a guess for a model nothing knew) or "unknown".
	Source string `json:"source"`
	// Known is false for "fallback" and "unknown": no saving is computed for such a model.
	Known bool  `json:"known"`
	Price Price `json:"price,omitzero"`
	// TTLSeconds is the cache entry lifetime the provider profile carried, 0 when the events did not say.
	TTLSeconds int `json:"ttl_s,omitempty"`
	// ContextTokens is the model's context window as the run used it (session.start records the main model's window after any
	// override, which is what compaction is measured against), 0 when neither the events nor the price table say.
	ContextTokens int `json:"context,omitempty"`
}

// Savings is what the cache saved, measured: for every response, the tokens read from cache times the difference between the
// model's input price and its cache-read price. It is at list price, which is what Assumption says; it is not the bill.
type Savings struct {
	// SavedUSD is the sum over the responses whose model has a known price.
	SavedUSD float64 `json:"saved_usd"`
	// PricedReadTokens are the cache-read tokens SavedUSD is made of; UnpricedReadTokens those of responses whose model price
	// is unknown: nothing is guessed for them, so SavedUSD is then a lower bound.
	PricedReadTokens   int64  `json:"priced_read_tokens"`
	UnpricedReadTokens int64  `json:"unpriced_read_tokens,omitempty"`
	Assumption         string `json:"assumption"`
}

// Known reports whether any saving could be priced.
func (s Savings) Known() bool { return s.PricedReadTokens > 0 || s.UnpricedReadTokens == 0 }

// Complete reports whether every cache read was priced, so that SavedUSD is exact rather than a lower bound.
func (s Savings) Complete() bool { return s.UnpricedReadTokens == 0 }

// Totals is the session's counters.
type Totals struct {
	// Requests counts model.request events; Main and Side split them (a side request is a compactor fork); Responses counts the
	// answers, Errors the requests that failed for good, Retries the attempts repeated after a retryable failure and RateLimited
	// those of them that were rate limits.
	Requests  int `json:"requests"`
	Main      int `json:"main"`
	Side      int `json:"side,omitempty"`
	Responses int `json:"responses"`
	Errors    int `json:"errors,omitempty"`
	// Cancelled counts the requests that were cancelled because their run was stopped (the manager finished, the session ended):
	// they are not errors and do not count in Errors.
	Cancelled   int `json:"cancelled,omitempty"`
	Retries     int `json:"retries,omitempty"`
	RateLimited int `json:"rate_limited,omitempty"`
	ToolCalls   int `json:"tool_calls,omitempty"`
	ToolErrors  int `json:"tool_errors,omitempty"`
	Compactions int `json:"compactions,omitempty"`
	Anomalies   int `json:"anomalies,omitempty"`
	// CostUSD is the sum of the cost_usd of the responses, as reported.
	CostUSD float64 `json:"cost_usd"`
	// Tokens sums every response (main and side); its HitRatio is weighted by tokens.
	Tokens  Tokens  `json:"tokens"`
	Savings Savings `json:"savings"`
	// BudgetUSD and BudgetSpentUSD are set by swarm.budget, which the swarm emits once, when the budget is spent; until then the
	// budget is not in the log.
	BudgetUSD      float64 `json:"budget_usd,omitempty"`
	BudgetSpentUSD float64 `json:"budget_spent_usd,omitempty"`
	BudgetSpent    bool    `json:"budget_spent,omitempty"`
}

// HitRatio is the token-weighted hit ratio of every response.
func (t Totals) HitRatio() float64 { return t.Tokens.HitRatio() }

// Stats counts what the State did with the events it was given: for a footer, a debug view and the tests.
type Stats struct {
	Events  int    `json:"events"`            // events applied
	Stale   int    `json:"stale,omitempty"`   // events with a seq not above the highest applied: duplicates and replays
	Unknown int    `json:"unknown,omitempty"` // events of a type this package does not know
	Bad     int    `json:"bad,omitempty"`     // payloads that could not be decoded, or whose essential field was missing
	TooBig  int    `json:"too_big,omitempty"` // payloads over the decode limit
	Panics  int    `json:"panics,omitempty"`  // events whose handler panicked (a bug: the tests require zero)
	Corrupt int    `json:"corrupt,omitempty"` // complete log lines that were not events, as the loader saw them
	Dropped int    `json:"dropped,omitempty"` // agents, tasks or entries that did not fit under a cap
	Resets  int    `json:"resets,omitempty"`  // times a followed log was truncated or replaced and the State started again
	LastSeq uint64 `json:"last_seq,omitempty"`
	// UnknownTypes names the unknown event types by count, at most MaxUnknownTypes of them.
	UnknownTypes map[string]int `json:"unknown_types,omitempty"`
	// LastPanic describes the newest handler panic (what panicked, for which event, and the top of the stack); it is empty when
	// Panics is zero.
	LastPanic string `json:"last_panic,omitempty"`
}
