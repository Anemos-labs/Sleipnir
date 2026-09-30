package env

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/rl"
)

// Schema identifiers of the run-level files.
const (
	ManifestSchema = "sleipnir.rl.run/1"
	SummarySchema  = "sleipnir.rl.summary/1"
	// EnvVersion identifies this environment implementation in manifests.
	EnvVersion = "sleipnir.rl.env/1"
)

// Summary is what summary.json holds and Rollout returns.
type Summary struct {
	Schema     string    `json:"schema"`
	RunID      string    `json:"run_id"`
	Started    time.Time `json:"started"`
	Ended      time.Time `json:"ended,omitempty"`
	DurationMs int64     `json:"duration_ms"`

	Tasks    int `json:"tasks"`
	Group    int `json:"group"`
	Rollouts int `json:"rollouts"`
	// Completed rollouts finished with a verdict; Resumed of them came from an
	// earlier invocation; Infra failed for infrastructure reasons; Cancelled
	// never finished because the run was cancelled; Pending have not started
	// (only in live summaries).
	Completed int `json:"completed"`
	Resumed   int `json:"resumed"`
	Infra     int `json:"infra"`
	Cancelled int `json:"cancelled"`
	Pending   int `json:"pending,omitempty"`
	// Partial is true for the live summary written while the run is going.
	Partial     bool `json:"partial,omitempty"`
	Interrupted bool `json:"interrupted,omitempty"`

	// Rates and means are over completed rollouts: infrastructure failures say
	// nothing about the policy and are reported separately, never averaged in.
	PassRate     float64 `json:"pass_rate"`
	MeanScore    float64 `json:"mean_score"`
	MeanReward   float64 `json:"mean_reward"`
	MeanCostUSD  float64 `json:"mean_cost_usd"`
	MeanITE      float64 `json:"mean_ite"`
	MeanRequests float64 `json:"mean_requests"`
	MeanSteps    float64 `json:"mean_steps"`
	MeanWallMs   float64 `json:"mean_wall_ms"`
	HackRate     float64 `json:"hack_rate"`
	BudgetRate   float64 `json:"budget_rate"`
	// InfraRate is infra / (completed + infra).
	InfraRate float64 `json:"infra_rate"`

	PerTask     []TaskSummary   `json:"per_task"`
	InfraErrors []InfraRecord   `json:"infra_errors,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
	Results     []RolloutResult `json:"results"`
}

// TaskSummary aggregates one task's samples.
type TaskSummary struct {
	ID          string   `json:"id"`
	Repo        string   `json:"repo,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Samples     int      `json:"samples"` // completed
	Passed      int      `json:"passed"`
	Infra       int      `json:"infra"`
	PassRate    float64  `json:"pass_rate"`
	MeanScore   float64  `json:"mean_score"`
	MeanReward  float64  `json:"mean_reward"`
	MeanITE     float64  `json:"mean_ite"`
	MeanCostUSD float64  `json:"mean_cost_usd"`
}

// InfraRecord names one infrastructure failure.
type InfraRecord struct {
	Task     string `json:"task"`
	Sample   int    `json:"sample"`
	Attempts int    `json:"attempts"`
	Message  string `json:"message"`
}

func (rn *run) summarise(cancelled bool) *Summary {
	rn.mu.Lock()
	results := make([]RolloutResult, len(rn.results))
	copy(results, rn.results)
	rn.mu.Unlock()
	for i := range results {
		if results[i].Task == "" { // slot never filled
			results[i] = RolloutResult{Task: rn.tasks[i/rn.group].ID, Sample: i % rn.group, Status: "pending", Tags: rn.tasks[i/rn.group].Tags}
			if cancelled {
				results[i].Status = StatusCancelled
			}
		}
	}
	return buildSummary(rn.id, rn.start, rn.r.now(), rn.tasks, rn.group, results, rn.r.Workspaces.Warnings(), cancelled)
}

// buildSummary aggregates rollout results. It is a pure function so evaluation
// and tests can reuse it.
func buildSummary(runID string, started, ended time.Time, tasks []rl.Task, group int, results []RolloutResult, warnings []string, cancelled bool) *Summary {
	s := &Summary{
		Schema: SummarySchema, RunID: runID, Started: started.UTC(), Ended: ended.UTC(),
		DurationMs: ended.Sub(started).Milliseconds(), Tasks: len(tasks), Group: group, Rollouts: len(results),
		Interrupted: cancelled, Warnings: warnings, Results: results,
	}
	type acc struct {
		n, passed, infra        int
		score, reward, ite, usd float64
	}
	per := make([]acc, len(tasks))
	taskIdx := map[string]int{}
	for i, t := range tasks {
		taskIdx[t.ID] = i
	}
	var n, passed, hacks, budgets int
	var score, reward, usd, ite, req, steps, wall float64
	for _, r := range results {
		switch r.Status {
		case StatusOK:
			s.Completed++
			if r.Resumed {
				s.Resumed++
			}
			n++
			if r.Pass {
				passed++
			}
			if r.Hacky() {
				hacks++
			}
			for _, f := range r.Flags {
				if f == rl.FlagBudgetExceeded {
					budgets++
					break
				}
			}
			score += r.Score
			reward += r.Reward
			usd += r.CostUSD
			ite += r.ITE
			req += float64(r.Requests)
			steps += float64(r.Steps)
			wall += float64(r.WallMs)
			if i, ok := taskIdx[r.Task]; ok {
				a := &per[i]
				a.n++
				if r.Pass {
					a.passed++
				}
				a.score += r.Score
				a.reward += r.Reward
				a.ite += r.ITE
				a.usd += r.CostUSD
			}
		case StatusInfra:
			s.Infra++
			s.InfraErrors = append(s.InfraErrors, InfraRecord{Task: r.Task, Sample: r.Sample, Attempts: r.Attempts, Message: r.Error})
			if i, ok := taskIdx[r.Task]; ok {
				per[i].infra++
			}
		case StatusCancelled:
			s.Cancelled++
		default:
			s.Pending++
		}
	}
	if n > 0 {
		f := float64(n)
		s.PassRate = float64(passed) / f
		s.MeanScore, s.MeanReward = score/f, reward/f
		s.MeanCostUSD, s.MeanITE = usd/f, ite/f
		s.MeanRequests, s.MeanSteps, s.MeanWallMs = req/f, steps/f, wall/f
		s.HackRate = float64(hacks) / f
		s.BudgetRate = float64(budgets) / f
	}
	if d := s.Completed + s.Infra; d > 0 {
		s.InfraRate = float64(s.Infra) / float64(d)
	}
	for i, t := range tasks {
		a := per[i]
		ts := TaskSummary{ID: t.ID, Repo: RepoKey(t), Tags: t.Tags, Samples: a.n, Passed: a.passed, Infra: a.infra}
		if a.n > 0 {
			f := float64(a.n)
			ts.PassRate, ts.MeanScore, ts.MeanReward = float64(a.passed)/f, a.score/f, a.reward/f
			ts.MeanITE, ts.MeanCostUSD = a.ite/f, a.usd/f
		}
		s.PerTask = append(s.PerTask, ts)
	}
	sort.SliceStable(s.InfraErrors, func(a, b int) bool {
		if s.InfraErrors[a].Task != s.InfraErrors[b].Task {
			return s.InfraErrors[a].Task < s.InfraErrors[b].Task
		}
		return s.InfraErrors[a].Sample < s.InfraErrors[b].Sample
	})
	return s
}

// ---- manifest ----

// Manifest is manifest.json: what the run was configured with.
type Manifest struct {
	Schema    string    `json:"schema"`
	RunID     string    `json:"run_id"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Status    string    `json:"status"` // running, done, cancelled
	Resumes   int       `json:"resumes,omitempty"`
	GroupSize int       `json:"group_size"`

	Config   ManifestConfig `json:"config"`
	Policy   ManifestPolicy `json:"policy"`
	Tasks    []ManifestTask `json:"tasks"`
	Versions ManifestVer    `json:"versions"`
	Warnings []string       `json:"warnings,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`
}

// ManifestConfig records how rollouts were run.
type ManifestConfig struct {
	Concurrency   int               `json:"concurrency"`
	Seed          int64             `json:"seed"`
	Capture       bool              `json:"capture,omitempty"`
	Swarm         bool              `json:"swarm,omitempty"`
	Agents        int               `json:"agents,omitempty"`
	RoleModels    map[string]string `json:"role_models,omitempty"`
	TargetPrice   string            `json:"target_price,omitempty"`
	InfraRetries  int               `json:"infra_retries"`
	VerifyRepeats int               `json:"verify_repeats"`
	PassPolicy    string            `json:"pass_policy,omitempty"`
	WorkspaceMode string            `json:"workspace_mode"`
	Group         string            `json:"group,omitempty"`
	KeepFailed    bool              `json:"keep_failed,omitempty"`
	Force         bool              `json:"force,omitempty"`
}

// ManifestPolicy identifies the policy. The endpoint is scheme and host only and
// the API key is named, never recorded.
type ManifestPolicy struct {
	Model     string          `json:"model"`
	Endpoint  string          `json:"endpoint,omitempty"`
	APIKeyEnv string          `json:"api_key_env,omitempty"`
	Sampling  json.RawMessage `json:"sampling,omitempty"`
}

// ManifestTask is the per-task part of the manifest.
type ManifestTask struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Repo            string    `json:"repo"`
	Commit          string    `json:"commit"`
	License         string    `json:"license,omitempty"`
	Tags            []string  `json:"tags,omitempty"`
	Team            rl.Team   `json:"team"`
	Budget          rl.Budget `json:"budget"`
	Network         bool      `json:"network,omitempty"`
	VerifierVersion string    `json:"verifier_version"`
	HiddenFiles     int       `json:"hidden_files,omitempty"`
}

// ManifestVer pins the software that produced the run.
type ManifestVer struct {
	Env    string `json:"env"`
	Schema string `json:"episode_schema"`
	Go     string `json:"go"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Git    string `json:"git,omitempty"`
}

func (rn *run) writeManifest(status string) error {
	path := filepath.Join(rn.out, "manifest.json")
	m := Manifest{Schema: ManifestSchema, RunID: rn.id, Created: rn.start.UTC(), Status: status, GroupSize: rn.group}
	// Resuming keeps the original creation time and counts the resume.
	if b, err := os.ReadFile(path); err == nil {
		var old Manifest
		if json.Unmarshal(b, &old) == nil {
			if !old.Created.IsZero() {
				m.Created = old.Created
			}
			m.Resumes = old.Resumes
			if status == "running" && old.Status != "" {
				m.Resumes++
			}
		}
	}
	m.Updated = rn.r.now().UTC()
	r := rn.r
	m.Config = ManifestConfig{
		Concurrency: max(r.Concurrency, 1), Seed: rn.opts.Seed, Capture: rn.opts.Capture, Swarm: rn.opts.Swarm,
		Agents: rn.opts.Agents, RoleModels: rn.opts.RoleModels, TargetPrice: rn.opts.TargetPrice,
		InfraRetries: r.retries(), VerifyRepeats: max(r.VerifyRepeats, 1), PassPolicy: r.VerifyPassPolicy,
		WorkspaceMode: r.Workspaces.opts.Mode, Group: rn.opts.Group, KeepFailed: rn.opts.KeepFailed, Force: rn.opts.Force,
	}
	m.Policy = ManifestPolicy{
		Model: rn.policy.Model, Endpoint: endpointHost(rn.policy.BaseURL), APIKeyEnv: rn.policy.APIKeyEnv, Sampling: rn.policy.Sampling,
	}
	for _, t := range rn.tasks {
		m.Tasks = append(m.Tasks, ManifestTask{
			ID: t.ID, Kind: t.Kind, Repo: stripURLCredentials(firstNonEmpty(t.Repo.URL, t.Repo.Path)), Commit: t.Repo.Commit,
			License: t.Repo.License, Tags: t.Tags, Team: t.Team, Budget: t.Budget, Network: t.Network,
			VerifierVersion: VerifierVersion(t), HiddenFiles: len(t.Verifier.Hidden),
		})
	}
	m.Versions = ManifestVer{
		Env: EnvVersion, Schema: rl.SchemaEpisode, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Git: strings.TrimPrefix(r.Workspaces.git.Version(context.Background()), "git version "),
	}
	m.Warnings = r.Workspaces.Warnings()
	m.Extra = rn.opts.Extra
	return writeJSONAtomic(path, m)
}
