package env

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Harness runs one agent (or swarm) on a task inside a prepared workspace. It is
// implemented by internal/session; this package only depends on the interface.
//
// The contract, which Runner relies on to keep infrastructure noise out of the
// training data:
//
//   - Run must open its event log with events.Open(spec.RunDir, ...) and its
//     blobs with events.NewDirBlobs(spec.RunDir+"/blobs"), and must have
//     closed both before it returns: Runner appends the outcome events to the
//     same log afterwards.
//   - Run returns a non-nil error only for infrastructure faults: the provider
//     is down or rejects the credentials, the log cannot be written. Anything the
//     policy can cause (invalid tool calls, running out of steps or budget,
//     giving up, wrecking the repository) is a normal result: return
//     RunResult{Claimed: "gave_up" | "budget" | "blocked", Err: ...}. An error
//     here makes the episode an infra_error that exporters drop; if a policy
//     could trigger it on purpose it would learn to.
//   - Run must stop promptly when ctx ends (the wall-clock budget is enforced
//     through it) and may return the context error or a normal result.
type Harness interface {
	Run(ctx context.Context, spec RunSpec) (RunResult, error)
}

// RunSpec is everything the harness needs for one rollout.
type RunSpec struct {
	Task rl.Task
	// Workspace is the directory the agent works in (the repository root or its
	// task.Repo.Subdir).
	Workspace string
	// RunDir holds events.jsonl and blobs/.
	RunDir string
	Policy PolicySpec
	// Capture asks for token-level capture from the endpoint.
	Capture    bool
	Swarm      bool
	Agents     int
	RoleModels map[string]string
	// Single runs the task as one agent even when its team says swarm: the baseline a swarm is compared with.
	Single bool
	// TargetPrice names the price model episodes are repriced under.
	TargetPrice string
	Seed        int64
	Budget      rl.Budget

	// The fields below are additions to the minimal interface; a harness may
	// ignore them.

	Sample  int
	Group   string
	Attempt int // 1 for the first try, higher after infra retries
	// Env is the scrubbed environment for the agent's commands (see EnvSpec):
	// HOME and TMPDIR private to this rollout, no credentials.
	Env []string
	// NetPrefix is an argv prefix that runs a command without network access
	// (a fresh network namespace with loopback up). It is nil when the task may
	// use the network or the host cannot isolate; the reason is in the run
	// manifest's warnings. The harness owns the agent's shell, so applying it is
	// the harness's job.
	NetPrefix []string
}

// RunResult is what the harness reports.
type RunResult struct {
	// FinalMessage is the agent's last message (the answer of a recall task).
	FinalMessage string
	// Claimed is "done", "blocked", "gave_up" or "budget".
	Claimed string
	// Err is the agent-side reason a run ended abnormally (not an infra fault).
	Err error
}

// PolicySpec names the policy endpoint being trained or evaluated.
type PolicySpec struct {
	Model     string
	BaseURL   string
	APIKeyEnv string // the NAME of the variable holding the key; never the key
	Sampling  json.RawMessage
}

// Extractor turns a finished run directory into an Episode; it wraps traj.
type Extractor func(runDir string, task rl.Task, sample int, group string) (*rl.Episode, error)

// Scorer computes rewards on an episode; it wraps reward.Score.
type Scorer func(ep *rl.Episode, task *rl.Task, runDir string) error

// Runner runs G samples per task through a Harness, verifies each in a clean
// checkout and writes the run directory (see the package documentation).
type Runner struct {
	Harness    Harness
	Extract    Extractor // nil: MinimalExtractor
	Score      Scorer    // nil: no rewards are computed
	Workspaces *Workspaces
	// Out is the run directory: <Out>/manifest.json, <Out>/summary.json and
	// <Out>/<task>/<sample>/... The run id defaults to its base name.
	Out         string
	Concurrency int // rollouts in flight (default 1)

	// HiddenBlobs resolves "blob:<hash>" hidden files of tasks.
	HiddenBlobs events.Blobs
	// VerifyRepeats and VerifyPassPolicy control flake handling (see VerifyOptions).
	VerifyRepeats    int
	VerifyPassPolicy string
	MaxDiffBytes     int64

	// InfraRetries is how many times a rollout that failed for infrastructure
	// reasons is retried from scratch (default 2; negative disables).
	InfraRetries int
	RetryBackoff time.Duration // default 1 s, doubled per retry
	// MaxWall caps an agent run whose task sets no wall budget (default 1 h;
	// negative means unlimited).
	MaxWall time.Duration
	// StopGrace is how long a harness gets to return after its context ended
	// before the rollout is written off as an infra error (default 30 s).
	StopGrace time.Duration
	// MaxSpendUSD caps what a run may spend, failed attempts and earlier invocations into the same directory
	// included (see LedgerFile). When it is reached no further rollout starts; those are reported as capped.
	// Rollouts already running finish, so the cap is overshot by at most what they are still allowed to spend:
	// give each rollout a budget of its own (Budget.USD) as well. Zero means no cap.
	MaxSpendUSD float64

	// Progress, if set, receives events. Calls are serialised; keep the callback
	// quick, workers wait for it.
	Progress func(Progress)
	Logf     func(format string, args ...any)
	Now      func() time.Time
}

// RolloutOpts configures one Rollout call.
type RolloutOpts struct {
	RunID      string
	Policy     PolicySpec
	Capture    bool
	Swarm      bool
	Agents     int
	RoleModels map[string]string
	// Single runs every task as one agent, swarm tasks too (Swarm and Agents are then ignored).
	Single      bool
	TargetPrice string
	Seed        int64
	// Group labels GRPO groups: the group of a task is "<task>@<Group>". The
	// default is the run id, so groups never mix runs (and thus policy
	// snapshots) unless the caller says so.
	Group string
	// Force reruns rollouts that already have an episode, and lets a run start over in a directory
	// that holds the rollouts of another policy.
	Force bool
	// Budget fills what a task's own budget leaves open (steps, requests, USD, wall-clock seconds, context window).
	Budget rl.Budget
	// KeepFailed keeps the workspace of rollouts whose verifier failed or that
	// hit an infra error, for debugging.
	KeepFailed bool
	// KeepEpisodes retains every episode in memory on the results (Summary
	// consumers such as the evaluator and the server need them).
	KeepEpisodes bool
	// Extra is free-form configuration recorded in the manifest (a rewards path).
	Extra map[string]any
}

// Progress is one progress event.
type Progress struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"` // run.start, rollout.start, rollout.stage, rollout.retry, rollout.done, run.done
	RunID   string    `json:"run_id"`
	Task    string    `json:"task,omitempty"`
	Sample  int       `json:"sample"`
	Stage   string    `json:"stage,omitempty"` // prepare, agent, verify, extract, score
	Attempt int       `json:"attempt,omitempty"`
	Done    int       `json:"done"`
	Total   int       `json:"total"`
	Status  string    `json:"status,omitempty"` // for rollout.done: ok, infra, cancelled, capped, skipped, resumed
	Pass    *bool     `json:"pass,omitempty"`
	Score   *float64  `json:"score,omitempty"` // verifier score, when there is a verdict
	// Reward is the episode's scored reward (rollout.done of a completed rollout).
	Reward *float64 `json:"reward,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// MaxWireSeed is the largest sampling seed every endpoint accepts: a signed 32-bit integer's.
// A first run against a real marketplace failed every rollout twice over. The gateway in front
// refused a 63-bit seed ("seed: Too big: expected int to be <=9007199254740991": a JSON number
// must survive JavaScript), and once that was fixed the model's own upstream refused a 53-bit one
// ("seed ... is outside the 0 to 2147483647 this endpoint accepts"). Sampling seeds only need to
// differ between samples, and 31 bits are plenty for that.
const MaxWireSeed = 1<<31 - 1

// SampleSeed derives the seed of one rollout from the run seed: deterministic
// in (seed, task, sample) and independent of scheduling. It is at most MaxWireSeed, because it
// is sent to the policy endpoint as the request's sampling seed.
func SampleSeed(seed int64, taskID string, sample int) int64 {
	return int64(hash64(seed, "sample", taskID, strconv.Itoa(sample)) >> 33)
}

// Statuses of a rollout.
const (
	StatusOK        = "ok"
	StatusInfra     = "infra"
	StatusCancelled = "cancelled"
	// StatusCapped: not run, because the run's spend cap was reached (Runner.MaxSpendUSD).
	StatusCapped = "capped"
	// StatusSkipped: not run, because a tool the task requires (rl.Task.Requires) is not on the commands' PATH;
	// Error says which ("missing ruby"). Nothing is written, so a later run where the tool exists runs it.
	StatusSkipped = "skipped"
)

// RolloutResult is the per-rollout record kept in the summary.
type RolloutResult struct {
	Task    string   `json:"task"`
	Sample  int      `json:"sample"`
	Status  string   `json:"status"`
	Resumed bool     `json:"resumed,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	// Attempts is how many times the rollout ran (1 unless infra retries happened).
	Attempts int      `json:"attempts"`
	Verified bool     `json:"verified"` // a verifier verdict exists
	Pass     bool     `json:"pass"`
	Score    float64  `json:"score"`
	Claimed  string   `json:"claimed,omitempty"`
	Flags    []string `json:"flags,omitempty"`
	Reward   float64  `json:"reward"`
	CostUSD  float64  `json:"cost_usd"`
	ITE      float64  `json:"ite"`
	Requests int      `json:"requests"`
	Steps    int      `json:"steps"`
	WallMs   int64    `json:"wall_ms"`
	VerifyMs int64    `json:"verify_ms"`
	// What the rollout used and how it went, from its episode: billed tokens by kind, and the episode's own counts of
	// failed tool calls, malformed ones, retried and failed requests, and cache breaks (see the rl.Sig* signals).
	Tokens         Tokens `json:"tokens"`
	ToolErrors     int    `json:"tool_errors,omitempty"`
	InvalidCalls   int    `json:"invalid_calls,omitempty"`
	Retries        int    `json:"retries,omitempty"`
	RequestErrors  int    `json:"request_errors,omitempty"`
	CacheAnomalies int    `json:"cache_anomalies,omitempty"`
	// Efficiency signals of the episode (rl.SigToolCalls ...) and its best-of-n rank key.
	ToolCalls        int         `json:"tool_calls,omitempty"`
	StuckWarnings    int         `json:"stuck_warnings,omitempty"`
	StuckStops       int         `json:"stuck_stops,omitempty"`
	RepeatedReads    int         `json:"repeated_reads,omitempty"`
	FinalAnswerChars int         `json:"final_answer_chars,omitempty"`
	Rank             *rl.RankKey `json:"rank_key,omitempty"`
	// Closures counts the typed reasons the swarm board closed its tasks for (rl.Outcome.Closures), for a team run.
	Closures map[string]int `json:"closures,omitempty"`
	// ProtectedTouched are protected paths the agent's diff changed.
	ProtectedTouched []string             `json:"protected_touched,omitempty"`
	Error            string               `json:"error,omitempty"`
	Roles            map[string]RoleStats `json:"roles,omitempty"`
	Dir              string               `json:"dir,omitempty"` // sample directory relative to the run directory
	KeptWorkspace    string               `json:"kept_workspace,omitempty"`

	// Episode is the episode when Rollout was asked to keep them.
	Episode *rl.Episode `json:"-"`
}

// RoleStats summarises one role's part in an episode.
type RoleStats struct {
	Agents       int     `json:"agents"`
	Steps        int     `json:"steps"`
	Reward       float64 `json:"reward"` // mean agent reward
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
}

// Hacky reports whether the rollout carries a hack flag.
func (r RolloutResult) Hacky() bool {
	for _, f := range r.Flags {
		if strings.HasPrefix(f, "hack:") {
			return true
		}
	}
	return false
}

// run is the state of one Rollout call. Nothing here is shared with other
// calls, so a Runner may be used from several goroutines at once.
type run struct {
	r      *Runner
	opts   RolloutOpts
	id     string
	out    string
	tasks  []rl.Task
	group  int
	total  int
	start  time.Time
	policy PolicySpec

	ledger *ledger // what every attempt spent

	mu       sync.Mutex // guards done, results, lastSave
	done     int
	results  []RolloutResult
	lastSave time.Time
}

// now uses the runner's injected clock when configured and the system clock otherwise.
func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// logf forwards rollout diagnostics when a logging callback is configured.
func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// retries disables infrastructure retries for negative values, defaults zero to two, and preserves
// positive values.
func (r *Runner) retries() int {
	switch {
	case r.InfraRetries < 0:
		return 0
	case r.InfraRetries == 0:
		return 2
	}
	return r.InfraRetries
}

// stopGrace uses a positive configured stop grace period or defaults to thirty seconds.
func (r *Runner) stopGrace() time.Duration {
	if r.StopGrace > 0 {
		return r.StopGrace
	}
	return 30 * time.Second
}

// wallFor prefers a positive task wall budget, then the runner limit; zero defaults to one hour
// and negative runner limits disable it.
func (r *Runner) wallFor(t rl.Task) time.Duration {
	if t.Budget.WallS > 0 {
		return time.Duration(t.Budget.WallS) * time.Second
	}
	switch {
	case r.MaxWall < 0:
		return 0
	case r.MaxWall == 0:
		return time.Hour
	}
	return r.MaxWall
}

type job struct {
	idx    int
	task   rl.Task
	sample int
}

// Rollout runs `group` independent samples of every task and returns the
// summary. It is resumable (a rollout whose episode.json exists is skipped
// unless opts.Force), bounded by r.Concurrency, and never stopped by one
// rollout failing: a crash in the harness, extractor or scorer becomes an
// infra_error episode. On cancellation the manifest and summary are still
// written and the summary is returned together with the context's error.
func (r *Runner) Rollout(ctx context.Context, tasks []rl.Task, group int, opts RolloutOpts) (*Summary, error) {
	if r.Harness == nil {
		return nil, errors.New("Runner.Harness is required")
	}
	if r.Workspaces == nil {
		return nil, errors.New("Runner.Workspaces is required")
	}
	if r.Out == "" {
		return nil, errors.New("Runner.Out is required")
	}
	if group < 1 {
		return nil, fmt.Errorf("group size must be at least 1, got %d", group)
	}
	if err := ValidateTasks(tasks); err != nil {
		return nil, err
	}
	out, err := filepath.Abs(r.Out)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	rn := &run{r: r, opts: opts, out: out, tasks: tasks, group: group, policy: opts.Policy}
	rn.id = opts.RunID
	if rn.id == "" {
		rn.id = filepath.Base(out)
	}
	rn.total = len(tasks) * group
	rn.results = make([]RolloutResult, rn.total)
	rn.start = r.now()
	if !opts.Force {
		if err := rn.checkResumable(); err != nil {
			return nil, err
		}
	}
	rn.ledger = openLedger(filepath.Join(out, LedgerFile))

	if err := rn.writeManifest("running"); err != nil {
		return nil, err
	}
	rn.emit(Progress{Type: "run.start"})

	conc := max(r.Concurrency, 1)
	jobs := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				rn.finishJob(j, rn.rollout(ctx, j))
			}
		}()
	}
feed:
	for ti, t := range tasks {
		for s := 0; s < group; s++ {
			select {
			case jobs <- job{idx: ti*group + s, task: t, sample: s}:
			case <-ctx.Done():
				break feed
			}
		}
	}
	close(jobs)
	wg.Wait()

	cancelled := ctx.Err() != nil
	sum := rn.summarise(cancelled)
	status := "done"
	if cancelled {
		status = "cancelled"
	}
	if err := writeJSONAtomic(filepath.Join(rn.out, "summary.json"), sum); err != nil {
		r.logf("env: writing summary: %v", err)
	}
	if err := rn.writeManifest(status); err != nil {
		r.logf("env: writing manifest: %v", err)
	}
	rn.emit(Progress{Type: "run.done", Status: status})
	if cancelled {
		return sum, ctx.Err()
	}
	return sum, nil
}

// emit serializes progress callbacks under the run lock and fills current time, run ID, and
// completion counts.
func (rn *run) emit(p Progress) {
	if rn.r.Progress == nil {
		return
	}
	rn.mu.Lock()
	defer rn.mu.Unlock()
	p.Time = rn.r.now().UTC()
	p.RunID = rn.id
	p.Total = rn.total
	p.Done = rn.done
	rn.r.Progress(p)
}

func (rn *run) finishJob(j job, res RolloutResult) {
	rn.mu.Lock()
	rn.results[j.idx] = res
	rn.done++
	save := time.Since(rn.lastSave) > time.Second
	if save {
		rn.lastSave = time.Now()
	}
	rn.mu.Unlock()
	p := Progress{Type: "rollout.done", Task: j.task.ID, Sample: j.sample, Status: res.Status, Error: res.Error}
	if res.Resumed {
		p.Status = "resumed"
	}
	if res.Verified {
		pass, score := res.Pass, res.Score
		p.Pass, p.Score = &pass, &score
	}
	if res.Status == StatusOK {
		reward := res.Reward
		p.Reward = &reward
	}
	rn.emit(p)
	// A live summary lets a watcher (the server's GET /v1/runs/{id}) see progress;
	// it is throttled so thousands of quick rollouts do not rewrite it each time.
	if save {
		sum := rn.summarise(false)
		sum.Partial = true
		_ = writeJSONAtomic(filepath.Join(rn.out, "summary.json"), sum)
	}
}

// sampleDir is <Out>/<task>/<sample>.
func (rn *run) sampleDir(task string, sample int) string {
	return filepath.Join(rn.out, task, strconv.Itoa(sample))
}

// groupID combines the task ID with an explicit grouping suffix or the current run ID.
func (rn *run) groupID(task string) string {
	suffix := rn.opts.Group
	if suffix == "" {
		suffix = rn.id
	}
	return task + "@" + suffix
}

// ---- one rollout ----

// rollout runs one (task, sample) including infra retries. It never panics and
// never returns without a result.
func (rn *run) rollout(ctx context.Context, j job) (res RolloutResult) {
	dir := rn.sampleDir(j.task.ID, j.sample)
	base := RolloutResult{Task: j.task.ID, Sample: j.sample, Tags: j.task.Tags, Dir: filepath.Join(j.task.ID, strconv.Itoa(j.sample)), Attempts: 0}
	defer func() {
		if p := recover(); p != nil {
			// Belt and braces: attempt() already recovers around each stage.
			res = base
			res.Status, res.Error = StatusInfra, fmt.Sprintf("panic: %v", p)
		}
	}()

	if !rn.opts.Force {
		if ep, ok := loadFinishedEpisode(dir); ok {
			res = rn.resultFromEpisode(j, ep)
			res.Resumed = true
			res.Dir = base.Dir
			return res
		}
	}
	if ctx.Err() != nil {
		base.Status = StatusCancelled
		return base
	}
	if rn.capReached() {
		base.Status = StatusCapped
		base.Error = fmt.Sprintf("the run's spend cap of $%.4g is reached", rn.r.MaxSpendUSD)
		return base
	}
	if missing := rn.r.Workspaces.MissingTools(j.task); len(missing) > 0 {
		base.Status = StatusSkipped
		base.Error = "missing " + strings.Join(missing, ", ")
		return base
	}
	rn.emit(Progress{Type: "rollout.start", Task: j.task.ID, Sample: j.sample})

	maxAttempts := rn.r.retries() + 1
	var last attemptOutcome
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		last = rn.attempt(ctx, j, dir, attempt)
		last.result.Attempts = attempt
		if last.cancelled || ctx.Err() != nil {
			out := base
			out.Attempts = attempt
			out.Status = StatusCancelled
			return out
		}
		if !last.retry || attempt == maxAttempts {
			break
		}
		if rn.capReached() { // another attempt would spend past the cap
			last.result.Error += " (not retried: the run's spend cap is reached)"
			break
		}
		rn.emit(Progress{Type: "rollout.retry", Task: j.task.ID, Sample: j.sample, Attempt: attempt, Error: last.result.Error})
		rn.r.logf("env: %s/%d: infra error (attempt %d/%d): %s", j.task.ID, j.sample, attempt, maxAttempts, last.result.Error)
		backoff := rn.r.RetryBackoff
		if backoff == 0 {
			backoff = time.Second
		}
		select {
		case <-time.After(backoff << (attempt - 1)):
		case <-ctx.Done():
			out := base
			out.Attempts = attempt
			out.Status = StatusCancelled
			return out
		}
	}
	res = last.result
	res.Dir = base.Dir
	res.Tags = j.task.Tags
	return res
}

// attemptOutcome is the result of one attempt at a rollout.
type attemptOutcome struct {
	result    RolloutResult
	retry     bool // infra failure worth another attempt
	cancelled bool
}

// stage marks progress and lets a panic name where it happened.
type stage struct {
	rn *run
	j  job
	n  string
}

// set changes the current rollout stage and emits progress with task, sample, and attempt
// identifiers.
func (s *stage) set(name string, attempt int) {
	s.n = name
	s.rn.emit(Progress{Type: "rollout.stage", Task: s.j.task.ID, Sample: s.j.sample, Stage: name, Attempt: attempt})
}

// attempt runs the whole pipeline once: prepare, agent, verify, outcome events,
// extract, score, write, clean up.
func (rn *run) attempt(ctx context.Context, j job, dir string, attempt int) (out attemptOutcome) {
	r := rn.r
	task := j.task
	st := &stage{rn: rn, j: j, n: "prepare"}
	// Runs last (deferred first): what this attempt spent is recorded before the next one starts from an empty directory.
	defer func() { rn.recordAttempt(j, dir, attempt, out) }()
	var (
		ws      *Workspace
		wallMs  int64
		keep    bool // the workspace is worth keeping if the caller asked for that
		abandon bool // a harness may still be running in the workspace: leave it alone
	)

	// fail turns an error into an infra outcome. A rollout that ends in an
	// infrastructure failure still gets an outcome event and a stub episode
	// carrying the infra_error flag, so the run directory is complete and
	// exporters see, and drop, it.
	fail := func(err error, retry bool) attemptOutcome {
		keep = true
		res := RolloutResult{Task: task.ID, Sample: j.sample, Tags: task.Tags, Status: StatusInfra, Error: fmt.Sprintf("%s: %v", st.n, err)}
		if !retry || attempt >= r.retries()+1 {
			rn.writeInfraEpisode(j, dir, res.Error, wallMs)
		}
		return attemptOutcome{result: res, retry: retry}
	}
	defer func() {
		if p := recover(); p != nil {
			r.logf("env: %s/%d: panic in stage %s: %v\n%s", task.ID, j.sample, st.n, p, debug.Stack())
			out = fail(fmt.Errorf("panic: %v", p), st.n != "extract" && st.n != "score")
		}
		if ws != nil {
			switch {
			case abandon:
				// Removing a tree an unresponsive harness may still be writing to
				// would only make it fail in confusing ways; PruneStale collects it.
				out.result.KeptWorkspace = ws.Root
			case keep && rn.opts.KeepFailed:
				out.result.KeptWorkspace = ws.Root
			default:
				if err := ws.Cleanup(); err != nil {
					r.logf("env: %s/%d: cleaning up %s: %v", task.ID, j.sample, ws.Root, err)
				}
			}
		}
	}()

	// Every attempt starts from an empty sample directory: a half-written
	// events.jsonl from a crashed attempt would otherwise be resumed by the
	// harness and corrupt the trajectory.
	if err := removeAllNoFollow(dir); err != nil {
		return fail(err, true)
	}
	store, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		return fail(err, true)
	}

	// 1. workspace
	st.set("prepare", attempt)
	if ws, err = r.Workspaces.Prepare(ctx, task, fmt.Sprintf("s%d", j.sample)); err != nil {
		if ctx.Err() != nil {
			return attemptOutcome{cancelled: true}
		}
		return fail(err, IsInfra(err))
	}
	if err := rn.writeTaskFiles(dir, task, ws); err != nil {
		return fail(err, true)
	}

	// 2. the agent
	st.set("agent", attempt)
	// A swarm task runs as a swarm even when the caller did not ask for one, and
	// the team size defaults to the task's.
	swarm, agents := (rn.opts.Swarm || task.Team.Mode == "swarm") && !rn.opts.Single, rn.opts.Agents
	if agents == 0 {
		agents = task.Team.Agents
	}
	spec := RunSpec{
		Task: task, Workspace: ws.Dir, RunDir: dir, Policy: rn.policy, Capture: rn.opts.Capture, Swarm: swarm, Single: rn.opts.Single,
		Agents: agents, RoleModels: rn.opts.RoleModels, TargetPrice: rn.opts.TargetPrice,
		Seed: SampleSeed(rn.opts.Seed, task.ID, j.sample), Budget: mergeBudget(task.Budget, rn.opts.Budget),
		Sample: j.sample, Group: rn.groupID(task.ID), Attempt: attempt, Env: ws.Env(),
		NetPrefix: r.Workspaces.IsolationPrefix(task),
	}
	agentStart := r.now()
	walled := task
	walled.Budget = spec.Budget
	ho := rn.runHarness(ctx, spec, r.wallFor(walled))
	wallMs = r.now().Sub(agentStart).Milliseconds()
	if ctx.Err() != nil {
		return attemptOutcome{cancelled: true}
	}
	switch {
	case ho.panicked != nil:
		r.logf("env: %s/%d: harness panicked: %v\n%s", task.ID, j.sample, ho.panicked, ho.stack)
		return fail(fmt.Errorf("harness panicked: %v", ho.panicked), true)
	case ho.hung:
		// The harness may still be writing to the run directory; touching its log
		// now could corrupt it, so the outcome is recorded without a log entry.
		keep, abandon = true, true
		res := RolloutResult{Task: task.ID, Sample: j.sample, Tags: task.Tags, Status: StatusInfra,
			Error: "agent: the harness did not return after its context ended"}
		rn.writeInfraEpisodeNoEvents(j, dir, res.Error, wallMs)
		return attemptOutcome{result: res}
	case ho.err != nil && !ho.budget:
		return fail(ho.err, true)
	case ho.res.Err != nil && IsInfra(ho.res.Err):
		return fail(ho.res.Err, true)
	}
	claimed := ho.res.Claimed
	if ho.budget && claimed == "" {
		claimed = "budget"
	}

	// 3. verification, in a clean checkout and under the caller's context rather
	// than the agent's: a spent wall budget must not also cancel the check of what
	// the agent produced.
	st.set("verify", attempt)
	vr, err := Verify(ctx, task, ws, VerifyOptions{
		HiddenBlobs: r.HiddenBlobs, Store: store, OutDir: dir, Answer: ho.res.FinalMessage,
		Repeats: r.VerifyRepeats, PassPolicy: r.VerifyPassPolicy, MaxDiffBytes: r.MaxDiffBytes,
	})
	if err != nil {
		if ctx.Err() != nil {
			return attemptOutcome{cancelled: true}
		}
		return fail(err, true)
	}
	keep = !vr.Pass

	// 4. outcome events, appended to the harness's log
	if err := appendOutcome(dir, rn.id, task.ID, j.sample, verifierOutcome(vr, claimed)); err != nil {
		return fail(fmt.Errorf("appending outcome events: %w", err), true)
	}

	// 5. episode
	st.set("extract", attempt)
	extract := r.Extract
	if extract == nil {
		extract = MinimalExtractor
	}
	ep, err := extract(dir, task, j.sample, rn.groupID(task.ID))
	if err != nil {
		// Not retried: a failing extractor fails the same way on a rerun, and a
		// rerun costs a whole agent run.
		return fail(fmt.Errorf("extracting the episode: %w", err), false)
	}
	if ep == nil {
		return fail(errors.New("extractor returned no episode"), false)
	}
	rn.finalizeEpisode(ep, j, ws, &vr, claimed, ho.budget, wallMs)

	st.set("score", attempt)
	if r.Score != nil {
		t := task
		if err := r.Score(ep, &t, dir); err != nil {
			return fail(fmt.Errorf("scoring the episode: %w", err), false)
		}
	}
	if err := writeJSONAtomic(filepath.Join(dir, "episode.json"), ep); err != nil {
		return fail(fmt.Errorf("writing episode.json: %w", err), true)
	}

	res := rn.resultFromEpisode(j, ep)
	// The verifier's own verdict is authoritative for the record, whatever the
	// extractor put in the episode.
	res.Verified, res.Pass, res.Score, res.VerifyMs = true, vr.Pass, vr.Score, vr.Ms
	res.ProtectedTouched = vr.ProtectedTouched
	return attemptOutcome{result: res}
}

// harnessOutcome is what came back from the harness goroutine.
type harnessOutcome struct {
	res      RunResult
	err      error
	panicked any
	stack    string
	hung     bool
	budget   bool // the wall-clock budget ended the run
}

// runHarness runs the harness with the task's wall-clock budget as a context
// deadline. A harness that does not return within the grace period after its
// context ended is abandoned (and reported as hung) rather than allowed to
// stall the whole run.
func (rn *run) runHarness(ctx context.Context, spec RunSpec, wall time.Duration) harnessOutcome {
	rctx := ctx
	cancel := func() {}
	if wall > 0 {
		rctx, cancel = context.WithTimeout(ctx, wall)
	}
	defer cancel()
	done := make(chan harnessOutcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- harnessOutcome{panicked: p, stack: string(debug.Stack())}
			}
		}()
		res, err := rn.r.Harness.Run(rctx, spec)
		done <- harnessOutcome{res: res, err: err}
	}()
	var o harnessOutcome
	select {
	case o = <-done:
	case <-rctx.Done():
		t := time.NewTimer(rn.r.stopGrace())
		defer t.Stop()
		select {
		case o = <-done:
		case <-t.C:
			return harnessOutcome{hung: true}
		}
	}
	// The budget ended the run if our deadline passed while the parent is fine.
	if ctx.Err() == nil && rctx.Err() != nil {
		o.budget = true
		if o.err != nil && (errors.Is(o.err, context.DeadlineExceeded) || errors.Is(o.err, context.Canceled)) {
			o.err = nil
		}
	}
	return o
}

// ---- artefacts ----

// writeTaskFiles atomically writes redacted task and environment metadata, including snapshot
// identity, file limits, and warnings.
func (rn *run) writeTaskFiles(dir string, task rl.Task, ws *Workspace) error {
	if err := writeJSONAtomic(filepath.Join(dir, "task.json"), redactTask(task)); err != nil {
		return err
	}
	env := rl.EnvRef{
		Repo: task.Repo.Path, Commit: ws.Commit, TreeHash: ws.BaseTree, Network: task.Network,
		Limits: map[string]string{"file_size": strconv.FormatInt(fileSizeLimit(rn.r.Workspaces.opts.Limits), 10)},
	}
	if env.Repo == "" {
		env.Repo = task.Repo.URL
	}
	env.Repo = stripURLCredentials(env.Repo)
	if env.Commit == "" {
		env.Commit = task.Repo.Commit
	}
	if len(ws.Warnings) > 0 {
		env.Limits["warnings"] = strings.Join(ws.Warnings, "; ")
	}
	return writeJSONAtomic(filepath.Join(dir, "env.json"), env)
}

// fileSizeLimit substitutes the default file-size limit only when the configured value is zero.
func fileSizeLimit(l Limits) int64 {
	if l.FileSize == 0 {
		return DefaultFileSizeLimit
	}
	return l.FileSize
}

// redactTask returns the task as written to <sample>/task.json. Hidden files
// are the one part of a task the policy must never see, and task.json sits next
// to the event log the agent's harness writes: inline text is therefore replaced
// by its content hash, blob references are kept (a hash reveals nothing).
func redactTask(t rl.Task) rl.Task {
	if len(t.Verifier.Hidden) == 0 {
		return t
	}
	hidden := make(map[string]string, len(t.Verifier.Hidden))
	for name, ref := range t.Verifier.Hidden {
		if strings.HasPrefix(ref, "text:") {
			ref = "redacted:sha256:" + string(core.HashString(strings.TrimPrefix(ref, "text:")))
		}
		hidden[name] = ref
	}
	t.Verifier.Hidden = hidden
	return t
}

// stripURLCredentials removes user info, query, and fragment from parseable URLs with hosts; other
// input is returned unchanged.
func stripURLCredentials(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		u.User = nil
		u.RawQuery, u.Fragment = "", ""
		return u.String()
	}
	return s
}

// outcomePayload is the data of the outcome events Runner appends. The first six
// fields are the contract with traj; the rest are additions it may ignore.
type outcomePayload struct {
	Kind    string    `json:"kind"`
	Pass    bool      `json:"pass"`
	Score   float64   `json:"score"`
	Version string    `json:"version,omitempty"`
	Detail  core.Hash `json:"detail,omitempty"`
	Ms      int64     `json:"ms"`

	Diff            core.Hash `json:"diff,omitempty"`
	Claimed         string    `json:"claimed,omitempty"`
	Protected       []string  `json:"protected,omitempty"`
	VerifierTouched []string  `json:"verifier_touched,omitempty"`
	TimedOut        bool      `json:"timed_out,omitempty"`
	Rejected        string    `json:"rejected,omitempty"`
	Flaky           bool      `json:"flaky,omitempty"`
	Repeats         int       `json:"repeats,omitempty"`
	Message         string    `json:"message,omitempty"`
}

// verifierOutcome projects verifier results and the agent's claim into the event payload,
// including rejection and repeat metadata.
func verifierOutcome(r Result, claimed string) outcomePayload {
	return outcomePayload{
		Kind: "verifier", Pass: r.Pass, Score: r.Score, Version: r.Version, Detail: r.LogBlob, Ms: r.Ms,
		Diff: r.DiffBlob, Claimed: claimed, Protected: r.ProtectedTouched, VerifierTouched: r.VerifierTouched,
		TimedOut: r.TimedOut, Rejected: r.Rejected, Flaky: r.Flaky, Repeats: len(r.Runs),
	}
}

// appendOutcome appends an outcome event to the sample's event log. The session
// id of the existing log is reused so all events of a rollout share one.
func appendOutcome(dir, runID, task string, sample int, p outcomePayload) error {
	session := sessionOf(filepath.Join(dir, "events.jsonl"))
	if session == "" {
		session = fmt.Sprintf("%s/%s/%d", runID, task, sample)
	}
	l, err := events.Open(dir, session)
	if err != nil {
		return err
	}
	_, err = l.Emit("", events.TypeOutcome, p)
	if cerr := l.Close(); err == nil {
		err = cerr
	}
	return err
}

// sessionOf returns the session id of the first event of a log, or "".
func sessionOf(path string) string {
	sess := ""
	stop := errors.New("stop")
	_ = events.Scan(path, func(e events.Event) error {
		sess = e.Session
		return stop
	})
	return sess
}

// writeInfraEpisode records a rollout that ended in an infrastructure failure:
// an outcome event of kind "infra" and a stub episode carrying the infra_error
// flag, which exporters drop by default.
func (rn *run) writeInfraEpisode(j job, dir, msg string, wallMs int64) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = appendOutcome(dir, rn.id, j.task.ID, j.sample, outcomePayload{Kind: "infra", Message: msg})
	rn.writeInfraEpisodeNoEvents(j, dir, msg, wallMs)
}

// writeInfraEpisodeNoEvents makes a best-effort standalone infrastructure-failure episode when
// normal event recording is unavailable.
func (rn *run) writeInfraEpisodeNoEvents(j job, dir, msg string, wallMs int64) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	ep := rn.stubEpisode(j)
	ep.AddFlag(rl.FlagInfraError)
	ep.Reward.Notes = append(ep.Reward.Notes, "infra: "+msg)
	ep.Cost.WallMs = wallMs
	_ = writeJSONAtomic(filepath.Join(dir, "episode.json"), ep)
}

// stubEpisode constructs minimal rollout provenance and timing for a job without trajectory data.
func (rn *run) stubEpisode(j job) *rl.Episode {
	t := j.task
	ep := &rl.Episode{
		Schema: rl.SchemaEpisode, ID: fmt.Sprintf("%s/%d", t.ID, j.sample), TaskID: t.ID, Group: rn.groupID(t.ID), Sample: j.sample,
		Policy:     rn.policyRef(),
		Env:        rl.EnvRef{Repo: stripURLCredentials(firstNonEmpty(t.Repo.Path, t.Repo.URL)), Commit: t.Repo.Commit, Network: t.Network},
		Provenance: rl.Provenance{License: t.Repo.License},
		StartedAt:  rn.start, EndedAt: rn.r.now().UTC(),
	}
	return ep
}

// firstNonEmpty returns the earliest nonempty argument or empty when none exists.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// policyRef captures model, endpoint host, sampling, and optional per-role models for episode
// provenance.
func (rn *run) policyRef() rl.PolicyRef {
	p := rl.PolicyRef{Model: rn.policy.Model, Endpoint: endpointHost(rn.policy.BaseURL), Sampling: rn.policy.Sampling}
	if len(rn.opts.RoleModels) > 0 {
		p.RoleModels = rn.opts.RoleModels
	}
	return p
}

// endpointHost keeps only scheme and host: the episode records which endpoint,
// never credentials embedded in the URL or its query.
func endpointHost(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// finalizeEpisode fills what the extractor left blank from what Runner knows
// first hand, and adds the flags only Runner can know.
func (rn *run) finalizeEpisode(ep *rl.Episode, j job, ws *Workspace, vres *Result, claimed string, budget bool, wallMs int64) {
	t := j.task
	if ep.Schema == "" {
		ep.Schema = rl.SchemaEpisode
	}
	if ep.ID == "" {
		ep.ID = fmt.Sprintf("%s/%d", t.ID, j.sample)
	}
	if ep.TaskID == "" {
		ep.TaskID = t.ID
	}
	if ep.Group == "" {
		ep.Group = rn.groupID(t.ID)
	}
	if ep.Policy.Model == "" {
		ep.Policy = rn.policyRef()
	}
	if ep.Env.Repo == "" {
		ep.Env.Repo = stripURLCredentials(firstNonEmpty(t.Repo.Path, t.Repo.URL))
	}
	if ep.Env.Commit == "" {
		ep.Env.Commit = firstNonEmpty(ws.Commit, t.Repo.Commit)
	}
	if ep.Env.TreeHash == "" {
		ep.Env.TreeHash = ws.BaseTree
	}
	ep.Env.Network = t.Network
	if ep.Provenance.License == "" {
		ep.Provenance.License = t.Repo.License
	}
	if ep.Outcome.Claimed == "" {
		ep.Outcome.Claimed = claimed
	}
	if ep.Outcome.Diff == "" {
		ep.Outcome.Diff = vres.DiffBlob
	}
	if ep.Outcome.Verifier == nil {
		v := vres.Verdict()
		ep.Outcome.Verifier = &v
	}
	if ep.Cost.WallMs == 0 {
		ep.Cost.WallMs = wallMs
	}
	if ep.StartedAt.IsZero() {
		ep.StartedAt = rn.start
	}
	if ep.EndedAt.IsZero() {
		ep.EndedAt = rn.r.now().UTC()
	}
	if budget || claimed == "budget" {
		ep.AddFlag(rl.FlagBudgetExceeded)
	}
	if len(vres.ProtectedTouched) > 0 {
		ep.AddFlag(rl.FlagHackProtected)
	}
	if len(vres.VerifierTouched) > 0 {
		ep.AddFlag(rl.FlagHackVerifier)
	}
}

// loadFinishedEpisode returns the episode of a completed rollout: episode.json
// exists, parses and is not an infra stub (those are worth retrying).
func loadFinishedEpisode(dir string) (*rl.Episode, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "episode.json"))
	if err != nil {
		return nil, false
	}
	var ep rl.Episode
	if err := json.Unmarshal(b, &ep); err != nil || ep.Has(rl.FlagInfraError) {
		return nil, false
	}
	return &ep, true
}

// resultFromEpisode derives the per-rollout record from an episode.
func (rn *run) resultFromEpisode(j job, ep *rl.Episode) RolloutResult {
	res := ResultFromEpisode(ep, j.task.Tags)
	res.Task, res.Sample = j.task.ID, j.sample
	if rn.opts.KeepEpisodes {
		res.Episode = ep
	}
	return res
}

// ResultFromEpisode derives the per-rollout record of a finished episode: what the runner writes to the summary, and what
// LoadRun recomputes from the episodes on disk. Task and Sample come from the episode; tags from the task.
func ResultFromEpisode(ep *rl.Episode, tags []string) RolloutResult {
	res := RolloutResult{
		Task: ep.TaskID, Sample: ep.Sample, Status: StatusOK, Tags: tags, Attempts: 1,
		Claimed: ep.Outcome.Claimed, Flags: append([]string(nil), ep.Flags...),
		Reward: ep.Reward.Total, CostUSD: ep.Cost.USD, ITE: ep.Cost.ITE, WallMs: ep.Cost.WallMs,
		Tokens:         tokensOf(ep.Cost.Usage),
		ToolErrors:     int(ep.Signals[rl.SigToolErrors]),
		InvalidCalls:   int(ep.Signals[rl.SigInvalidToolCalls]),
		Retries:        int(ep.Signals[sigRequestRetries]),
		RequestErrors:  int(ep.Signals[sigRequestErrors]),
		CacheAnomalies: int(ep.Signals[rl.SigCacheAnomalies]),
	}
	if v := ep.Outcome.Verifier; v != nil {
		res.Verified, res.Pass, res.Score, res.VerifyMs = true, v.Pass, v.Score, v.Ms
	}
	efficiencyFields(&res, ep)
	res.Closures = cloneClosures(ep.Outcome.Closures)
	roles := map[string]*RoleStats{}
	rewardSum := map[string]float64{}
	mainSteps, allSteps := 0, 0
	for _, a := range ep.Agents {
		rs := roles[a.Role]
		if rs == nil {
			rs = &RoleStats{}
			roles[a.Role] = rs
		}
		rs.Agents++
		rewardSum[a.Role] += a.Reward.Total
		for _, s := range a.Steps {
			allSteps++
			if s.Kind == rl.KindMain {
				mainSteps++
			}
			role := s.Role
			if role == "" {
				role = a.Role
			}
			sr := roles[role]
			if sr == nil {
				sr = &RoleStats{}
				roles[role] = sr
			}
			sr.Steps++
			sr.InputTokens += s.Usage.TotalInput()
			sr.OutputTokens += s.Usage.OutputTokens
		}
	}
	res.Steps = mainSteps
	if v, ok := ep.Signals[rl.SigSteps]; ok {
		res.Steps = int(v)
	}
	res.Requests = ep.Cost.Requests
	if res.Requests == 0 {
		if v, ok := ep.Signals[rl.SigRequests]; ok {
			res.Requests = int(v)
		} else {
			res.Requests = allSteps
		}
	}
	if len(roles) > 0 {
		res.Roles = map[string]RoleStats{}
		for role, rs := range roles {
			if n := rewardSumCount(ep, role); n > 0 {
				rs.Reward = rewardSum[role] / float64(n)
			}
			res.Roles[role] = *rs
		}
	}
	return res
}

// rewardSumCount counts the agents of a role.
func rewardSumCount(ep *rl.Episode, role string) int {
	n := 0
	for _, a := range ep.Agents {
		if a.Role == role {
			n++
		}
	}
	return n
}

// writeJSONAtomic writes indented JSON plus a newline atomically with mode 0644.
func writeJSONAtomic(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(p, append(b, '\n'), 0o644)
}

// mergeBudget is the budget of a rollout: the task's own, with what it leaves open taken from the run's defaults.
// (ITE is a reward normaliser of the task, not a limit, and is never defaulted.)
func mergeBudget(task, run rl.Budget) rl.Budget {
	b := task
	if b.Steps == 0 {
		b.Steps = run.Steps
	}
	if b.Requests == 0 {
		b.Requests = run.Requests
	}
	if b.USD == 0 {
		b.USD = run.USD
	}
	if b.WallS == 0 {
		b.WallS = run.WallS
	}
	if b.ContextWindow == 0 {
		b.ContextWindow = run.ContextWindow
	}
	return b
}

// checkResumable refuses to continue a run directory under another policy, seed or team, or with a verifier that
// changed. Finished episodes are skipped on a rerun, and they were produced under what the manifest says: resuming
// under something else would file new rollouts among the old results and report the mixture as one experiment.
func (rn *run) checkResumable() error {
	b, err := os.ReadFile(filepath.Join(rn.out, "manifest.json"))
	if err != nil {
		return nil // a new run
	}
	var old Manifest
	if json.Unmarshal(b, &old) != nil {
		return nil // nothing trustworthy to compare with
	}
	here := ManifestPolicy{Model: rn.policy.Model, Endpoint: endpointHost(rn.policy.BaseURL), Sampling: rn.policy.Sampling}
	var diffs []string
	if old.Policy.Model != "" && old.Policy.Model != here.Model {
		diffs = append(diffs, fmt.Sprintf("policy %s, this run uses %s", old.Policy.Model, here.Model))
	}
	if old.Policy.Endpoint != "" && old.Policy.Endpoint != here.Endpoint {
		diffs = append(diffs, fmt.Sprintf("endpoint %s, this run uses %s", old.Policy.Endpoint, here.Endpoint))
	}
	if !sameJSON(old.Policy.Sampling, here.Sampling) {
		diffs = append(diffs, fmt.Sprintf("sampling %s, this run uses %s", compactJSON(old.Policy.Sampling), compactJSON(here.Sampling)))
	}
	if old.Config.Seed != rn.opts.Seed {
		diffs = append(diffs, fmt.Sprintf("seed %d, this run uses %d", old.Config.Seed, rn.opts.Seed))
	}
	if old.Config.Swarm != rn.opts.Swarm || old.Config.Agents != rn.opts.Agents || old.Config.Single != rn.opts.Single {
		diffs = append(diffs, fmt.Sprintf("team swarm=%v agents=%d single=%v, this run uses swarm=%v agents=%d single=%v",
			old.Config.Swarm, old.Config.Agents, old.Config.Single, rn.opts.Swarm, rn.opts.Agents, rn.opts.Single))
	}
	if !sameRoleModels(old.Config.RoleModels, rn.opts.RoleModels) {
		diffs = append(diffs, "role models differ")
	}
	oldVer := map[string]string{}
	for _, t := range old.Tasks {
		oldVer[t.ID] = t.VerifierVersion
	}
	for _, t := range rn.tasks {
		if v, ok := oldVer[t.ID]; ok && v != VerifierVersion(t) {
			diffs = append(diffs, fmt.Sprintf("task %s was verified by another verifier", t.ID))
		}
	}
	if len(diffs) == 0 {
		return nil
	}
	return fmt.Errorf("env: %s holds a run with %s: rollouts finished there would be resumed into this one and the results mixed; use another directory, or Force to start over",
		rn.out, strings.Join(diffs, "; "))
}

// compactJSON removes insignificant JSON whitespace, labels absent JSON as none, and preserves
// malformed input verbatim.
func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "none"
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}

// sameJSON compares compacted JSON text; it ignores formatting whitespace but does not normalize
// object-key order or numeric spelling.
func sameJSON(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return compactJSON(a) == compactJSON(b)
}

// sameRoleModels compares map lengths and model values, assuming stored role-model values are
// nonempty.
func sameRoleModels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
