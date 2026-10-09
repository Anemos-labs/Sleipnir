package rl

import "encoding/json"

// Task kinds.
const (
	TaskFix        = "fix"
	TaskFeature    = "feature"
	TaskRefactor   = "refactor"
	TaskSwarm      = "swarm"
	TaskRecall     = "recall"
	TaskCompaction = "compaction"
)

// Task is one environment instance: a repository state, a goal and a way to
// check the result. A tasks file holds one JSON Task per line.
type Task struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Repo     RepoSpec `json:"repo"`
	Setup    []string `json:"setup,omitempty"` // run once per task snapshot; the result is cached
	Prompt   string   `json:"prompt"`
	Team     Team     `json:"team"`
	Verifier Verifier `json:"verifier"`
	Budget   Budget   `json:"budget"`
	Tags     []string `json:"tags,omitempty"`
	// Network lets the agent reach the network; off by default.
	Network bool `json:"network,omitempty"`
	// Requires names the tools the task's setup and verifier run ("ruby",
	// "cargo"; see env.KnownTools). Where one is not on the PATH the commands
	// get, the task is skipped ("skipped: missing ruby"), not failed.
	Requires []string `json:"requires,omitempty"`
	// Meta is free-form provenance from the generator (source commit, seed).
	Meta json.RawMessage `json:"meta,omitempty"`
}

// RepoSpec locates the starting state.
type RepoSpec struct {
	Path    string `json:"path,omitempty"` // local clone
	URL     string `json:"url,omitempty"`
	Commit  string `json:"commit"`
	Subdir  string `json:"subdir,omitempty"`
	License string `json:"license,omitempty"` // SPDX id
}

// Team says who works on the task.
type Team struct {
	Mode   string   `json:"mode"`             // single | swarm
	Agents int      `json:"agents,omitempty"` // workers; the manager is not counted
	Roles  []string `json:"roles,omitempty"`
}

// Verifier defines how the result is judged. It always runs in a clean
// checkout: the agent's diff (minus Protected paths) is applied to the task's
// starting commit and Hidden files are written only then.
type Verifier struct {
	Cmd      string `json:"cmd"`
	TimeoutS int    `json:"timeout_s,omitempty"`
	// Pass: "exit0" (default), "regex:<re>" matched against stdout, or
	// "json-score" (last stdout line is {"score": 0..1}).
	Pass string `json:"pass,omitempty"`
	// Hidden maps a relative path to a blob ("blob:<hash>") or inline text
	// ("text:...") written into the verification checkout only.
	Hidden map[string]string `json:"hidden,omitempty"`
	// Protected are glob patterns the agent must not change; edits are discarded
	// at verification and flagged.
	Protected []string `json:"protected,omitempty"`
	// Expect checks the agent's final message: {"contains": [...], "regex":
	// "<RE2>", "fold": bool}; see env.Expect.
	Expect json.RawMessage `json:"expect,omitempty"`
	// BaselineScore is the json-score verifier's score on the untouched start,
	// in [0, 1), as `rl tasks check` measured it. The outcome reward is the
	// improvement over it, max(0, (s - b) / (1 - b)), so a verifier that gives
	// the start partial credit does not pay for doing nothing. 0: none.
	BaselineScore float64 `json:"baseline_score,omitempty"`
}

// Budget bounds an episode. Zero means the harness default.
type Budget struct {
	Steps int `json:"steps,omitempty"`
	// Requests bounds the model requests the endpoint answered; a request it refused (429, 5xx, a dropped
	// connection) is repeated and does not count.
	Requests int `json:"requests,omitempty"`
	// USD is a hard cap on what one rollout may spend: the agent stops as a budget outcome when it is reached.
	USD           float64 `json:"usd,omitempty"`
	ITE           float64 `json:"ite,omitempty"`
	WallS         int     `json:"wall_s,omitempty"`
	ContextWindow int     `json:"context_window,omitempty"` // set low to force frequent compaction
}
