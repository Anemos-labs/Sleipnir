package env

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/stats"
)

// ReportSchema identifies evaluation reports.
const ReportSchema = "sleipnir.rl.eval/2"

// ErrContaminated is returned (wrapped in a *ContaminationError) when an
// evaluation would run on tasks that appear in the training set.
var ErrContaminated = errors.New("evaluation tasks overlap the training set")

// ContaminationError lists the overlapping task ids.
type ContaminationError struct{ IDs []string }

func (e *ContaminationError) Error() string {
	ids := e.IDs
	more := ""
	if len(ids) > 10 {
		more = fmt.Sprintf(" (and %d more)", len(ids)-10)
		ids = ids[:10]
	}
	return fmt.Sprintf("%v: %d task(s) also appear in the training set: %s%s", ErrContaminated, len(e.IDs), strings.Join(ids, ", "), more)
}

func (e *ContaminationError) Is(target error) bool { return target == ErrContaminated }

// EvalOptions configures Eval.
type EvalOptions struct {
	// Samples is N, the number of samples per task (default 1).
	Samples int
	// ExcludeFile names a training-set list (task ids, one per line, or task
	// JSONL); Exclude is the same in memory. Eval refuses to run when any task
	// overlaps: measuring a model on what it trained on says nothing.
	ExcludeFile string
	Exclude     *ExcludeSet
	// Rollout carries the policy, run id, seed and so on.
	Rollout RolloutOpts
}

// Dist summarises a distribution over rollouts.
type Dist struct {
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// Slice aggregates a subset of tasks (all of them, or those with a tag).
type Slice struct {
	Tasks   int     `json:"tasks"`
	Samples int     `json:"samples"`
	PassAt1 float64 `json:"pass_at_1"`
	MeanITE float64 `json:"mean_ite"`
	MeanUSD float64 `json:"mean_usd"`
	Hack    float64 `json:"hack_rate"`
}

// RoleReport aggregates what one role did across episodes.
type RoleReport struct {
	Episodes         int     `json:"episodes"`
	MeanSteps        float64 `json:"mean_steps"`
	MeanReward       float64 `json:"mean_reward"`
	MeanOutputTokens float64 `json:"mean_output_tokens"`
}

// TaskResult is one task's row in a report.
type TaskResult struct {
	ID           string   `json:"id"`
	Repo         string   `json:"repo,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	N            int      `json:"n"` // completed samples
	Correct      int      `json:"correct"`
	Infra        int      `json:"infra"`
	Hacks        int      `json:"hacks"`
	PassAt1      float64  `json:"pass_at_1"`
	MeanScore    float64  `json:"mean_score"`
	MeanReward   float64  `json:"mean_reward"`
	MeanITE      float64  `json:"mean_ite"`
	MeanUSD      float64  `json:"mean_usd"`
	MeanRequests float64  `json:"mean_requests"`
	MeanSteps    float64  `json:"mean_steps"`
	MeanWallMs   float64  `json:"mean_wall_ms"`
}

// Report is the result of an evaluation.
type Report struct {
	Schema  string    `json:"schema"`
	RunID   string    `json:"run_id,omitempty"`
	Model   string    `json:"model,omitempty"`
	Created time.Time `json:"created"`

	Tasks     int `json:"tasks"`
	Samples   int `json:"samples"` // requested per task
	Rollouts  int `json:"rollouts"`
	Completed int `json:"completed"`
	Infra     int `json:"infra"`
	// Dropped lists tasks with no completed rollout (all infra errors); they are
	// left out of every average.
	Dropped []string `json:"dropped,omitempty"`

	// PassAt1 is the mean over tasks of the fraction of samples that passed.
	PassAt1 float64 `json:"pass_at_1"`
	// PassHat[k] is pass^k: the probability that k samples of a task ALL pass,
	// estimated without bias from n >= k samples as C(c,k)/C(n,k), averaged over
	// the tasks that have at least k samples. PassAt[k] is pass@k, the chance
	// that at least one of k passes. pass^k is what a user who needs the agent to
	// work every time experiences; pass@1 hides its variance.
	PassHat map[int]float64 `json:"pass_hat_k,omitempty"`
	PassAt  map[int]float64 `json:"pass_at_k,omitempty"`

	MeanScore  float64 `json:"mean_score"`
	MeanReward float64 `json:"mean_reward"`
	ITE        Dist    `json:"ite"`
	USD        Dist    `json:"usd"`
	Requests   Dist    `json:"requests"`
	Steps      Dist    `json:"steps"`
	WallMs     Dist    `json:"wall_ms"`

	HackRate   float64 `json:"hack_rate"`
	InfraRate  float64 `json:"infra_rate"`
	BudgetRate float64 `json:"budget_rate"`

	// Report v2. Passed counts episodes; PassLow and PassHigh are the 95% Wilson interval of Passed/Completed (episodes are
	// taken as independent, which they are not quite: several samples of one task share its difficulty, so read it as the
	// interval for a workload like this suite, and compare runs with Compare, which resamples tasks).
	Passed   int     `json:"passed"`
	PassLow  float64 `json:"pass_low"`
	PassHigh float64 `json:"pass_high"`
	// Solved is the share of tasks that at least one sample passed.
	Solved float64 `json:"solved"`
	// Pending is how many rollouts of a run that has not finished have no outcome yet.
	Pending int `json:"pending,omitempty"`
	// What was spent: the sum over completed episodes, and per passing one.
	USDTotal   float64 `json:"usd_total"`
	USDPerPass float64 `json:"usd_per_pass,omitempty"`
	// Tokens are billed tokens by kind over completed episodes; HitRatio is the token-weighted share served from the cache.
	Tokens   Tokens  `json:"tokens"`
	HitRatio float64 `json:"hit_ratio"`
	// Friction, per completed episode: failed and malformed tool calls, retried and failed requests, cache breaks.
	ToolErrors     float64 `json:"tool_errors_per_ep"`
	InvalidCalls   float64 `json:"invalid_calls_per_ep"`
	Retries        float64 `json:"retries_per_ep"`
	RequestErrors  float64 `json:"request_errors_per_ep"`
	CacheAnomalies float64 `json:"cache_anomalies_per_ep"`
	// FalseDone is the share of episodes that claimed to be done and failed the verifier.
	FalseDone float64 `json:"false_done_rate"`
	// Attempts and Identity come from the run directory (LoadRun); a report built from results alone leaves them empty.
	Attempts *AttemptStats `json:"attempts,omitempty"`
	Identity *Identity     `json:"identity,omitempty"`

	ByTag  map[string]Slice      `json:"by_tag,omitempty"`
	ByRole map[string]RoleReport `json:"by_role,omitempty"`

	PerTask []TaskResult `json:"per_task"`
}

// Eval runs N samples of every task through the runner and reports pass@1,
// pass^k, cost, protocol and infrastructure statistics. It refuses to run when
// a task also appears in the training set (see EvalOptions.Exclude).
func Eval(ctx context.Context, r *Runner, tasks []rl.Task, opts EvalOptions) (Report, error) {
	ex := opts.Exclude
	if opts.ExcludeFile != "" {
		loaded, err := LoadExcludeList(opts.ExcludeFile)
		if err != nil {
			return Report{}, err
		}
		if ex == nil {
			ex = loaded
		} else {
			for id := range loaded.IDs {
				ex.IDs[id] = true
			}
			for k := range loaded.Pairs {
				ex.Pairs[k] = true
			}
		}
	}
	if overlap := ex.Overlap(tasks); len(overlap) > 0 {
		return Report{}, &ContaminationError{IDs: overlap}
	}
	n := max(opts.Samples, 1)
	sum, err := r.Rollout(ctx, tasks, n, opts.Rollout)
	if sum == nil {
		return Report{}, err
	}
	rep := BuildReport(tasks, sum.Results, n)
	rep.RunID = sum.RunID
	rep.Model = opts.Rollout.Policy.Model
	if perr := writeJSONAtomic(filepath.Join(r.Out, "report.json"), rep); perr != nil && err == nil {
		err = perr
	}
	return rep, err
}

// BuildReport aggregates rollout results into a Report. Infrastructure failures
// and cancelled rollouts are excluded from every statistic except InfraRate.
func BuildReport(tasks []rl.Task, results []RolloutResult, samples int) Report {
	rep := Report{Schema: ReportSchema, Created: time.Now().UTC(), Tasks: len(tasks), Samples: samples, Rollouts: len(results)}
	byTask := map[string][]RolloutResult{}
	infra := map[string]int{}
	for _, r := range results {
		switch r.Status {
		case StatusOK:
			rep.Completed++
			byTask[r.Task] = append(byTask[r.Task], r)
		case StatusInfra:
			rep.Infra++
			infra[r.Task]++
		}
	}
	if d := rep.Completed + rep.Infra; d > 0 {
		rep.InfraRate = float64(rep.Infra) / float64(d)
	}

	var ite, usd, req, steps, wall []float64
	var scoreSum, rewardSum float64
	hacks, budgets, falseDone := 0, 0, 0
	var tool, invalid, retries, reqErrs, anomalies float64
	rows := make([]TaskResult, 0, len(tasks))
	roleAcc := map[string]*roleAccum{}
	for _, t := range tasks {
		rs := byTask[t.ID]
		row := TaskResult{ID: t.ID, Repo: RepoKey(t), Tags: t.Tags, N: len(rs), Infra: infra[t.ID]}
		if len(rs) == 0 {
			rep.Dropped = append(rep.Dropped, t.ID)
			rows = append(rows, row)
			continue
		}
		for _, r := range rs {
			budgeted := slices.Contains(r.Flags, rl.FlagBudgetExceeded)
			if r.Pass {
				row.Correct++
				rep.Passed++
			} else if r.Claimed == "done" && !budgeted {
				// A run the budget ended did not say it was done: episodes written before the harness stopped defaulting the
				// claim to "done" for a run the wall clock cut off carry both, and the flag is what the runner saw.
				falseDone++
			}
			rep.Tokens = rep.Tokens.plus(r.Tokens)
			rep.USDTotal += r.CostUSD
			tool += float64(r.ToolErrors)
			invalid += float64(r.InvalidCalls)
			retries += float64(r.Retries)
			reqErrs += float64(r.RequestErrors)
			anomalies += float64(r.CacheAnomalies)
			if r.Hacky() {
				row.Hacks++
				hacks++
			}
			if budgeted {
				budgets++
			}
			row.MeanScore += r.Score
			row.MeanReward += r.Reward
			row.MeanITE += r.ITE
			row.MeanUSD += r.CostUSD
			row.MeanRequests += float64(r.Requests)
			row.MeanSteps += float64(r.Steps)
			row.MeanWallMs += float64(r.WallMs)
			ite = append(ite, r.ITE)
			usd = append(usd, r.CostUSD)
			req = append(req, float64(r.Requests))
			steps = append(steps, float64(r.Steps))
			wall = append(wall, float64(r.WallMs))
			scoreSum += r.Score
			rewardSum += r.Reward
			for role, st := range r.Roles {
				a := roleAcc[role]
				if a == nil {
					a = &roleAccum{}
					roleAcc[role] = a
				}
				a.episodes++
				a.steps += float64(st.Steps)
				a.reward += st.Reward
				a.out += float64(st.OutputTokens)
			}
		}
		f := float64(len(rs))
		row.PassAt1 = float64(row.Correct) / f
		row.MeanScore /= f
		row.MeanReward /= f
		row.MeanITE /= f
		row.MeanUSD /= f
		row.MeanRequests /= f
		row.MeanSteps /= f
		row.MeanWallMs /= f
		rows = append(rows, row)
	}
	rep.PerTask = rows

	var valid []TaskResult
	for _, r := range rows {
		if r.N > 0 {
			valid = append(valid, r)
		}
	}
	if len(valid) > 0 {
		for _, r := range valid {
			rep.PassAt1 += r.PassAt1
		}
		rep.PassAt1 /= float64(len(valid))
	}
	if rep.Completed > 0 {
		n := float64(rep.Completed)
		rep.MeanScore = scoreSum / n
		rep.MeanReward = rewardSum / n
		rep.HackRate = float64(hacks) / n
		rep.BudgetRate = float64(budgets) / n
		rep.FalseDone = float64(falseDone) / n
		rep.ToolErrors, rep.InvalidCalls, rep.Retries = tool/n, invalid/n, retries/n
		rep.RequestErrors, rep.CacheAnomalies = reqErrs/n, anomalies/n
		rep.PassLow, rep.PassHigh = stats.Wilson(rep.Passed, rep.Completed, stats.Z95)
		rep.HitRatio = rep.Tokens.HitRatio()
		if rep.Passed > 0 {
			rep.USDPerPass = rep.USDTotal / float64(rep.Passed)
		}
		if len(valid) > 0 {
			solved := 0
			for _, r := range valid {
				if r.Correct > 0 {
					solved++
				}
			}
			rep.Solved = float64(solved) / float64(len(valid))
		}
	}
	rep.ITE, rep.USD, rep.Requests, rep.Steps, rep.WallMs = dist(ite), dist(usd), dist(req), dist(steps), dist(wall)

	rep.PassHat, rep.PassAt = map[int]float64{}, map[int]float64{}
	for k := 1; k <= max(samples, 1); k++ {
		var hat, at float64
		cnt := 0
		for _, r := range valid {
			if r.N < k {
				continue
			}
			hat += passHatK(r.N, r.Correct, k)
			at += 1 - passHatK(r.N, r.N-r.Correct, k)
			cnt++
		}
		if cnt > 0 {
			rep.PassHat[k], rep.PassAt[k] = hat/float64(cnt), at/float64(cnt)
		}
	}

	// per-tag slices
	rep.ByTag = map[string]Slice{}
	tagTasks := map[string][]TaskResult{}
	for _, r := range valid {
		for _, tag := range r.Tags {
			tagTasks[tag] = append(tagTasks[tag], r)
		}
	}
	for tag, rs := range tagTasks {
		var s Slice
		var iteSum, usdSum float64
		hack := 0
		for _, r := range rs {
			s.Tasks++
			s.Samples += r.N
			s.PassAt1 += r.PassAt1
			iteSum += r.MeanITE * float64(r.N)
			usdSum += r.MeanUSD * float64(r.N)
			hack += r.Hacks
		}
		s.PassAt1 /= float64(s.Tasks)
		if s.Samples > 0 {
			s.MeanITE, s.MeanUSD = iteSum/float64(s.Samples), usdSum/float64(s.Samples)
			s.Hack = float64(hack) / float64(s.Samples)
		}
		rep.ByTag[tag] = s
	}
	if len(rep.ByTag) == 0 {
		rep.ByTag = nil
	}
	if len(roleAcc) > 0 {
		rep.ByRole = map[string]RoleReport{}
		for role, a := range roleAcc {
			f := float64(a.episodes)
			rep.ByRole[role] = RoleReport{Episodes: a.episodes, MeanSteps: a.steps / f, MeanReward: a.reward / f, MeanOutputTokens: a.out / f}
		}
	}
	sort.Strings(rep.Dropped)
	return rep
}

type roleAccum struct {
	episodes           int
	steps, reward, out float64
}

// passHatK is C(c,k)/C(n,k): the chance that k samples drawn without
// replacement from n, c of them correct, are all correct. Computed as a
// product to stay exact for large n.
func passHatK(n, c, k int) float64 {
	if k > n || c < k {
		return 0
	}
	p := 1.0
	for i := 0; i < k; i++ {
		p *= float64(c-i) / float64(n-i)
	}
	return p
}

func dist(xs []float64) Dist {
	if len(xs) == 0 {
		return Dist{}
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	var sum float64
	for _, x := range s {
		sum += x
	}
	q := func(p float64) float64 {
		if len(s) == 1 {
			return s[0]
		}
		pos := p * float64(len(s)-1)
		lo := int(math.Floor(pos))
		hi := int(math.Ceil(pos))
		return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
	}
	return Dist{Mean: sum / float64(len(s)), Median: q(0.5), P90: q(0.9), Min: s[0], Max: s[len(s)-1]}
}

// ---- comparison ----

// MetricDelta is one paired comparison.
type MetricDelta struct {
	Name  string  `json:"name"`
	A     float64 `json:"a"` // mean over the paired tasks
	B     float64 `json:"b"`
	Delta float64 `json:"delta"` // B - A
	Low   float64 `json:"ci_low"`
	High  float64 `json:"ci_high"`
	// P is the two-sided bootstrap p-value for "the true difference is zero".
	P float64 `json:"p"`
	// Significant is true when the confidence interval excludes zero.
	Significant bool `json:"significant"`
	N           int  `json:"n"` // paired tasks
}

// Comparison is the result of Compare.
type Comparison struct {
	A          string        `json:"a"`
	B          string        `json:"b"`
	Paired     int           `json:"paired_tasks"`
	OnlyA      []string      `json:"only_a,omitempty"`
	OnlyB      []string      `json:"only_b,omitempty"`
	Confidence float64       `json:"confidence"`
	Resamples  int           `json:"resamples"`
	Metrics    []MetricDelta `json:"metrics"`
}

// Metric returns the named metric, if present.
func (c Comparison) Metric(name string) (MetricDelta, bool) {
	for _, m := range c.Metrics {
		if m.Name == name {
			return m, true
		}
	}
	return MetricDelta{}, false
}

// CompareOptions tunes CompareWith.
type CompareOptions struct {
	Confidence float64 // default 0.95
	Resamples  int     // default 2000
	Seed       int64   // default 1
}

// Compare compares two reports over the tasks they share, with paired
// bootstrap confidence intervals: tasks (not samples) are resampled with
// replacement, because tasks are the unit that varies between an evaluation
// set and the real workload, and the same tasks were run under both policies so
// the difference per task is the quantity with the least noise. The result is
// deterministic.
func Compare(a, b Report) Comparison { return CompareWith(a, b, CompareOptions{}) }

// CompareWith is Compare with explicit options.
func CompareWith(a, b Report, o CompareOptions) Comparison {
	if o.Confidence <= 0 || o.Confidence >= 1 {
		o.Confidence = 0.95
	}
	if o.Resamples <= 0 {
		o.Resamples = 2000
	}
	if o.Seed == 0 {
		o.Seed = 1
	}
	c := Comparison{A: label(a), B: label(b), Confidence: o.Confidence, Resamples: o.Resamples}
	ra, rb := map[string]TaskResult{}, map[string]TaskResult{}
	for _, t := range a.PerTask {
		ra[t.ID] = t
	}
	for _, t := range b.PerTask {
		rb[t.ID] = t
	}
	var ids []string
	for id, ta := range ra {
		if tb, ok := rb[id]; ok && ta.N > 0 && tb.N > 0 {
			ids = append(ids, id)
		} else if !ok {
			c.OnlyA = append(c.OnlyA, id)
		}
	}
	for id := range rb {
		if _, ok := ra[id]; !ok {
			c.OnlyB = append(c.OnlyB, id)
		}
	}
	sort.Strings(ids)
	sort.Strings(c.OnlyA)
	sort.Strings(c.OnlyB)
	c.Paired = len(ids)
	if len(ids) == 0 {
		return c
	}
	type metric struct {
		name string
		get  func(a, b TaskResult) (va, vb float64)
	}
	both := func(f func(TaskResult) float64) func(a, b TaskResult) (float64, float64) {
		return func(a, b TaskResult) (float64, float64) { return f(a), f(b) }
	}
	hackRate := func(t TaskResult) float64 {
		if t.N == 0 {
			return 0
		}
		return float64(t.Hacks) / float64(t.N)
	}
	metrics := []metric{
		{"pass_at_1", both(func(t TaskResult) float64 { return t.PassAt1 })},
		{"mean_score", both(func(t TaskResult) float64 { return t.MeanScore })},
		{"mean_reward", both(func(t TaskResult) float64 { return t.MeanReward })},
		{"mean_ite", both(func(t TaskResult) float64 { return t.MeanITE })},
		{"mean_usd", both(func(t TaskResult) float64 { return t.MeanUSD })},
		{"mean_requests", both(func(t TaskResult) float64 { return t.MeanRequests })},
		{"mean_steps", both(func(t TaskResult) float64 { return t.MeanSteps })},
		{"mean_wall_ms", both(func(t TaskResult) float64 { return t.MeanWallMs })},
		{"hack_rate", both(hackRate)},
	}
	// pass^k for the largest k <= 4 that every paired task can support in both runs.
	minN := math.MaxInt
	for _, id := range ids {
		minN = min(minN, ra[id].N, rb[id].N)
	}
	if k := min(minN, 4); k >= 2 {
		metrics = append(metrics, metric{fmt.Sprintf("pass_hat_%d", k), func(a, b TaskResult) (float64, float64) {
			return passHatK(a.N, a.Correct, k), passHatK(b.N, b.Correct, k)
		}})
	}
	for _, m := range metrics {
		d := make([]float64, 0, len(ids))
		var sa, sb float64
		for _, id := range ids {
			va, vb := m.get(ra[id], rb[id])
			sa += va
			sb += vb
			d = append(d, vb-va)
		}
		n := float64(len(d))
		md := MetricDelta{Name: m.name, A: sa / n, B: sb / n, Delta: (sb - sa) / n, N: len(d)}
		md.Low, md.High, md.P = bootstrapMean(d, o)
		md.Significant = md.Low > 0 || md.High < 0
		c.Metrics = append(c.Metrics, md)
	}
	return c
}

func label(r Report) string {
	switch {
	case r.Model != "" && r.RunID != "":
		return r.Model + " (" + r.RunID + ")"
	case r.Model != "":
		return r.Model
	}
	return r.RunID
}

// bootstrapMean returns the percentile confidence interval of the mean of d and
// a two-sided p-value for "mean is zero", from paired-difference resampling.
func bootstrapMean(d []float64, o CompareOptions) (low, high, p float64) {
	n := len(d)
	rng := newSplitMix(uint64(o.Seed))
	means := make([]float64, o.Resamples)
	le, ge := 0, 0
	for i := range means {
		var s float64
		for j := 0; j < n; j++ {
			s += d[rng.intn(n)]
		}
		m := s / float64(n)
		means[i] = m
		if m <= 0 {
			le++
		}
		if m >= 0 {
			ge++
		}
	}
	sort.Float64s(means)
	alpha := (1 - o.Confidence) / 2
	at := func(q float64) float64 {
		pos := q * float64(len(means)-1)
		lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
		return means[lo] + (means[hi]-means[lo])*(pos-float64(lo))
	}
	low, high = at(alpha), at(1-alpha)
	p = 2 * math.Min(float64(le), float64(ge)) / float64(o.Resamples)
	if p > 1 {
		p = 1
	}
	return low, high, p
}

// splitMix is a tiny deterministic PRNG (splitmix64). The standard library's
// generators are not guaranteed stable across Go versions, and a comparison
// that changes when the toolchain does could not be reproduced.
type splitMix struct{ s uint64 }

func newSplitMix(seed uint64) *splitMix { return &splitMix{s: seed} }

func (r *splitMix) next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// intn returns a value in [0, n) without modulo bias worth caring about at these sizes.
func (r *splitMix) intn(n int) int { return int(r.next() % uint64(n)) }
