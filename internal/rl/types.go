package rl

import (
	"encoding/json"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

// Schema identifiers written into every exported record.
const (
	SchemaEpisode = "sleipnir.rl/1"
	SchemaStep    = "sleipnir.rl.step/1"
)

// Agent roles. One weight set serves all of them; the role is a tag in the
// prompt, not a separate model.
const (
	RoleWorker    = "worker"
	RoleManager   = "manager"
	RoleReviewer  = "reviewer"
	RoleCompactor = "compactor"
	RoleMailman   = "mailman"
)

// Step kinds: what a model call was for. A compactor or mailman call is a fork
// of some agent's request, so it belongs to that agent but trains a different
// role.
const (
	KindMain      = "main"
	KindCompactor = "compactor"
	KindMailman   = "mailman"
	KindRecon     = "recon"
)

// Edge kinds connect steps and agents into the swarm's causal DAG.
const (
	EdgeSpawn   = "spawn"   // manager step -> worker agent
	EdgeMail    = "mail"    // sender step -> recipient step that first saw it
	EdgeCompact = "compact" // compactor step -> first step of the new segment
	EdgePromote = "promote" // note/promotion step -> shared-layer epoch
	EdgeLease   = "lease"   // lease grant/steal between agents
	EdgeBoard   = "board"   // board mutation -> step that acted on it
)

// Episode flags. Exporters drop episodes carrying a hard flag by default.
const (
	FlagInfraError     = "infra_error"     // setup/verifier/provider failure: noise, never trained on
	FlagBudgetExceeded = "budget_exceeded" // stopped by a budget: an outcome, penalised
	FlagTruncated      = "truncated"       // log ended without a clean finish
	FlagHackProtected  = "hack:protected_edit"
	FlagHackTestWeaken = "hack:test_weakened"
	FlagHackVerifier   = "hack:verifier_touched"
	FlagHackNetwork    = "hack:network"
	FlagHackHardcode   = "hack:hardcoded"
	FlagHackEscape     = "hack:outside_worktree"
	FlagWeakLabel      = "weak_label"      // reward from human/heuristic signals, not a verifier
	FlagTokenMismatch  = "token_mismatch"  // token trace failed the consistency check
	FlagReplayMismatch = "replay_mismatch" // rebuilt prompt hash differs from the logged wire hash
	FlagContaminated   = "contaminated"    // transcript contains verifier content or benchmark strings
)

// HardFlag reports whether an episode with this flag must not be used for
// training unless the caller opts in.
func HardFlag(f string) bool {
	switch f {
	case FlagInfraError, FlagTruncated, FlagReplayMismatch, FlagContaminated, FlagTokenMismatch:
		return true
	}
	return len(f) > 5 && f[:5] == "hack:"
}

// Signal names: counts and rates extracted from the event log by traj and
// consumed by reward. Keeping them as a fixed vocabulary lets the two evolve
// independently.
const (
	SigRequests         = "requests"            // model calls of any kind
	SigSteps            = "steps"               // main-kind calls, all agents
	SigCriticalPath     = "critical_path_steps" // longest chain of steps through the spawn/mail DAG
	SigWorkerSteps      = "worker_steps"
	SigInvalidToolCalls = "invalid_tool_calls" // malformed or unknown tool calls
	SigToolErrors       = "tool_errors"
	SigCompactions      = "compactions"
	SigCompactRejects   = "compact_rejects" // patches rejected or replaced by the mechanical fallback
	SigLeaseConflicts   = "lease_conflicts"
	SigScopeViolations  = "scope_violations"
	SigStaleWrites      = "stale_writes" // edits rejected because the file changed since it was read
	SigMailSent         = "mail_sent"
	SigMailIgnored      = "mail_ignored" // delivered, never acted on before the recipient ended
	SigMailDuplicate    = "mail_duplicate"
	SigRecalls          = "recalls"
	SigReReads          = "reads_after_compaction" // reads of a file/range recalled or read shortly before its compaction
	SigCacheAnomalies   = "cache_anomalies"
	SigSpawns           = "spawns"
	SigSpawnNoResult    = "spawn_no_result" // workers whose work never reached the final tree
	SigDuplicateWork    = "duplicate_work"  // overlapping edits or repeated identical tool calls across agents
	SigIdleMs           = "idle_ms"         // worker time spent waiting with nothing assigned
	SigVerifierRuns     = "verifier_runs"   // times an agent ran the task's checks itself
	SigDoneClaims       = "done_claims"
	SigDoneAccepted     = "done_accepted"
)

// Episode is one complete run of a task by a team of one or more agents. It is
// the unit rewards are computed on and the root of the canonical export.
type Episode struct {
	Schema string `json:"schema"`
	ID     string `json:"id"` // "<task>/<sample>"
	TaskID string `json:"task_id"`
	Group  string `json:"group"` // GRPO group: same task and policy snapshot
	Sample int    `json:"sample"`

	Policy  PolicyRef  `json:"policy"`
	Harness HarnessRef `json:"harness"`
	Env     EnvRef     `json:"env"`

	Agents []Agent `json:"agents"`
	Edges  []Edge  `json:"edges,omitempty"`

	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	Outcome Outcome            `json:"outcome"`
	Reward  Reward             `json:"reward"`
	Signals map[string]float64 `json:"signals,omitempty"`
	Cost    CostRef            `json:"cost"`
	Flags   []string           `json:"flags,omitempty"`

	Provenance Provenance `json:"provenance"`
}

// Has reports whether the episode carries a flag.
func (e *Episode) Has(flag string) bool {
	for _, f := range e.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

// AddFlag appends a flag once.
func (e *Episode) AddFlag(flag string) {
	if !e.Has(flag) {
		e.Flags = append(e.Flags, flag)
	}
}

// PolicyRef identifies the model being trained or evaluated.
type PolicyRef struct {
	Model      string          `json:"model"`
	Endpoint   string          `json:"endpoint,omitempty"` // base URL host only; never credentials
	Checkpoint string          `json:"checkpoint,omitempty"`
	Sampling   json.RawMessage `json:"sampling,omitempty"` // temperature, top_p, max_tokens, ...
	// RoleModels maps a role to the model that played it when it differs from Model
	// (train the manager against a fixed worker model, for example).
	RoleModels map[string]string `json:"role_models,omitempty"`
}

// HarnessRef pins what "the harness" meant for this run so data can be
// reproduced or migrated.
type HarnessRef struct {
	Version    string   `json:"version"`
	Commit     string   `json:"commit,omitempty"`
	Renderer   string   `json:"renderer"` // prompt renderer version
	ConfigHash string   `json:"config_hash,omitempty"`
	Roles      []string `json:"roles,omitempty"`
	Agents     int      `json:"agents"`
}

// EnvRef records the task environment (what the verifier and the agent ran in).
type EnvRef struct {
	Repo     string            `json:"repo,omitempty"`
	Commit   string            `json:"commit,omitempty"`
	TreeHash string            `json:"tree_hash,omitempty"` // starting tree, for anchor-state grouping
	Image    string            `json:"image,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
	Network  bool              `json:"network,omitempty"`
}

// Provenance carries what governance filters need.
type Provenance struct {
	License string `json:"license,omitempty"`
	Consent string `json:"consent,omitempty"`
	// Teacher is true when any completion in the episode came from a model that
	// is not the policy under training (a proprietary teacher, for example).
	Teacher       bool     `json:"teacher,omitempty"`
	TeacherModels []string `json:"teacher_models,omitempty"`
}

// Agent is one participant, with its steps in order.
type Agent struct {
	ID       string    `json:"id"`
	Role     string    `json:"role"`
	Parent   string    `json:"parent,omitempty"`
	Model    string    `json:"model,omitempty"`
	Steps    []Step    `json:"steps"`
	Segments []Segment `json:"segments,omitempty"`
	Reward   Reward    `json:"reward"`
	// Status is how the agent ended: done | blocked | budget | error | killed.
	Status string `json:"status,omitempty"`
}

// Segment is a maximal run of an agent's steps whose prompts are append-only
// extensions of each other. It starts at the agent's first step and after every
// declared rebase (compaction commit, shared epoch, thinking strip).
type Segment struct {
	Index  int    `json:"index"`
	From   int    `json:"from"` // step index, inclusive
	To     int    `json:"to"`   // step index, inclusive
	Epoch  int    `json:"epoch"`
	Reason string `json:"reason"` // start | compact | epoch | strip
}

// Step is one model call and what came of it. A training sample is derived
// from a step (prompt -> completion) or from a segment of steps.
type Step struct {
	ID      string `json:"id"`   // model.request id, "<agent>.<n>"
	Kind    string `json:"kind"` // KindMain, KindCompactor, ...
	Role    string `json:"role"` // role this call trains (a compactor call of a worker agent trains "compactor")
	Model   string `json:"model"`
	Epoch   int    `json:"epoch"`
	Segment int    `json:"segment"`
	// At is when the request was issued (repricing needs cache lifetimes).
	At time.Time `json:"at,omitempty"`

	Prompt PromptRef `json:"prompt"`
	// Inline holds the fully expanded prompt when the exporter was asked to
	// inline prompts; otherwise consumers expand Prompt.
	Inline *core.Prompt `json:"inline,omitempty"`

	Completion   Completion    `json:"completion"`
	Tokens       *TokenTrace   `json:"tokens,omitempty"`
	Usage        core.Usage    `json:"usage"`
	Cache        CacheInfo     `json:"cache"`
	LatencyMs    int64         `json:"latency_ms,omitempty"`
	Observations []Observation `json:"observations,omitempty"`

	// Trainable is false for calls the policy under training did not sample
	// (teacher models, replayed or copied context).
	Trainable bool `json:"trainable"`
	// Reward is the step-level shaped reward when the reward config defines one;
	// Advantage is filled by the adv package.
	Reward    float64 `json:"reward,omitempty"`
	Advantage float64 `json:"advantage,omitempty"`
}

// PromptRef locates a step's exact prompt without copying it.
type PromptRef struct {
	Req      string    `json:"req"`
	WireHash core.Hash `json:"wire_hash"`
	Tokens   int       `json:"tokens,omitempty"`
	// SharedPrefix names the longest prefix (tools, constitution, shared and role
	// pins) that is byte-identical across agents, and how many leading messages
	// it spans, so trainers with prefix or tree packing compute it once.
	SharedPrefix   string `json:"shared_prefix,omitempty"`
	SharedMessages int    `json:"shared_messages,omitempty"`
}

// Completion is the action the policy took.
type Completion struct {
	Turn core.Turn       `json:"turn"`
	Stop core.StopReason `json:"stop"`
}

// TokenTrace is what a self-hosted endpoint returned for one call (see
// core.TokenTrace); it is only present when the endpoint provided it and the
// consistency check passed.
type TokenTrace = core.TokenTrace

// CacheInfo is the cache behaviour of one call.
type CacheInfo struct {
	HitRatio     float64 `json:"hit_ratio"`
	ExpectedRead int     `json:"expected_read,omitempty"`
	Anomaly      bool    `json:"anomaly,omitempty"`
}

// Observation is one tool result produced by a step's tool calls, as the agent
// saw it next.
type Observation struct {
	ToolID  string          `json:"tool_id"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output"` // what the model saw (possibly truncated with a recall handle)
	IsError bool            `json:"is_error,omitempty"`
	Full    core.Hash       `json:"full,omitempty"` // blob holding the untruncated output
	Ms      int64           `json:"ms,omitempty"`
}

// Edge is one causal link in the swarm DAG.
type Edge struct {
	Kind string `json:"kind"`
	From string `json:"from"` // step or agent id
	To   string `json:"to"`
	Ref  string `json:"ref,omitempty"` // task id, mail id, lease id, patch id
}

// Verdict is one verification result.
type Verdict struct {
	Kind    string    `json:"kind"` // verifier | review | human | protocol
	Pass    bool      `json:"pass"`
	Score   float64   `json:"score"` // 0..1
	Version string    `json:"version,omitempty"`
	Detail  core.Hash `json:"detail,omitempty"` // blob with logs
	Ms      int64     `json:"ms,omitempty"`
}

// Outcome gathers what happened at the end of the episode.
type Outcome struct {
	Verifier *Verdict  `json:"verifier,omitempty"`
	Reviews  []Verdict `json:"reviews,omitempty"`
	// Claimed is what the team said: "done" | "blocked" | "gave_up" | "budget" | "".
	Claimed string   `json:"claimed,omitempty"`
	Labels  []string `json:"labels,omitempty"`
	// Diff is the blob holding the final patch (minus protected paths).
	Diff core.Hash `json:"diff,omitempty"`
}

// Reward is a total and the named components it was built from.
type Reward struct {
	Total      float64            `json:"total"`
	Components map[string]float64 `json:"components,omitempty"`
	Notes      []string           `json:"notes,omitempty"`
}

// CostRef is what the episode consumed, actual and repriced.
type CostRef struct {
	Usage core.Usage `json:"usage"`
	USD   float64    `json:"usd,omitempty"`
	// ITE is the input-token-equivalent cost repriced under Target; it is what
	// the cost reward component uses.
	ITE      float64 `json:"ite,omitempty"`
	Target   string  `json:"target,omitempty"`
	Requests int     `json:"requests"`
	WallMs   int64   `json:"wall_ms,omitempty"`
}
