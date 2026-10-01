package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPassHatK(t *testing.T) {
	tests := []struct {
		n, c, k int
		want    float64
	}{
		{5, 3, 1, 0.6},
		{5, 3, 2, 3.0 / 10},
		{5, 3, 3, 1.0 / 10},
		{5, 3, 4, 0},
		{5, 5, 5, 1},
		{5, 0, 1, 0},
		{4, 2, 5, 0},
		{1, 1, 1, 1},
		{100, 90, 2, (90.0 * 89) / (100 * 99)},
	}
	for _, tc := range tests {
		if got := passHatK(tc.n, tc.c, tc.k); !near(got, tc.want) {
			t.Errorf("passHatK(%d,%d,%d) = %v, want %v", tc.n, tc.c, tc.k, got, tc.want)
		}
	}
}

func TestDist(t *testing.T) {
	d := dist([]float64{10, 1, 5, 3, 7})
	if !near(d.Mean, 5.2) || !near(d.Median, 5) || d.Min != 1 || d.Max != 10 || !near(d.P90, 8.8) {
		t.Fatalf("%+v", d)
	}
	if d := dist(nil); d != (Dist{}) {
		t.Fatalf("%+v", d)
	}
	if d := dist([]float64{4}); d.Median != 4 || d.P90 != 4 {
		t.Fatalf("%+v", d)
	}
}

func evalTasks() []rl.Task {
	mk := func(id string, tags ...string) rl.Task {
		t := goodTask(id)
		t.Tags = tags
		return t
	}
	return []rl.Task{mk("a", "go", "small"), mk("b", "go"), mk("c", "py"), mk("d", "py")}
}

func res(task string, sample int, pass bool, ite float64, flags ...string) RolloutResult {
	r := RolloutResult{Task: task, Sample: sample, Status: StatusOK, Verified: true, Pass: pass, ITE: ite, CostUSD: ite / 100, Requests: 10, Steps: 5, WallMs: 1000, Flags: flags}
	if pass {
		r.Score = 1
	}
	return r
}

func TestBuildReport(t *testing.T) {
	tasks := evalTasks()
	results := []RolloutResult{
		// a: 3/3 pass, cheap
		res("a", 0, true, 100), res("a", 1, true, 100), res("a", 2, true, 100),
		// b: 1/3 pass
		res("b", 0, true, 200), res("b", 1, false, 200), res("b", 2, false, 200, rl.FlagHackProtected),
		// c: 0/2 completed, one infra error
		res("c", 0, false, 300), res("c", 1, false, 300, rl.FlagBudgetExceeded),
		{Task: "c", Sample: 2, Status: StatusInfra, Error: "boom"},
		// d: only infra errors: dropped
		{Task: "d", Sample: 0, Status: StatusInfra}, {Task: "d", Sample: 1, Status: StatusInfra}, {Task: "d", Sample: 2, Status: StatusCancelled},
	}
	results[0].Roles = map[string]RoleStats{"worker": {Agents: 1, Steps: 4, Reward: 1, OutputTokens: 100}}
	results[1].Roles = map[string]RoleStats{"worker": {Agents: 1, Steps: 6, Reward: 0.5, OutputTokens: 300}, "compactor": {Agents: 1, Steps: 1}}
	rep := BuildReport(tasks, results, 3)

	if rep.Tasks != 4 || rep.Rollouts != 12 || rep.Completed != 8 || rep.Infra == 0 || !reflect.DeepEqual(rep.Dropped, []string{"d"}) {
		t.Fatalf("counts: %+v", rep)
	}
	if rep.Infra != 3 {
		t.Errorf("infra = %d", rep.Infra)
	}
	if want := 3.0 / 11; !near(rep.InfraRate, want) {
		t.Errorf("InfraRate = %v want %v (cancelled rollouts are not infra)", rep.InfraRate, want)
	}
	// pass@1 averages per-task rates over tasks that have data: (1 + 1/3 + 0)/3.
	if want := (1 + 1.0/3 + 0) / 3; !near(rep.PassAt1, want) {
		t.Errorf("PassAt1 = %v, want %v", rep.PassAt1, want)
	}
	// pass^2: a: 1; b: C(1,2)=0; c: n=2,c=0 -> 0: mean over 3 tasks.
	if !near(rep.PassHat[2], 1.0/3) || !near(rep.PassHat[3], 1.0/2) {
		t.Errorf("PassHat = %v", rep.PassHat)
	}
	// pass^3 only counts tasks with >= 3 completed samples (a and b): (1 + 0)/2.
	if !near(rep.PassAt[1], rep.PassAt1) {
		t.Errorf("pass@1 via PassAt = %v, want %v", rep.PassAt[1], rep.PassAt1)
	}
	// pass@2: a: 1; b: 1 - C(2,2)/C(3,2) = 2/3; c: 0 -> (1 + 2/3 + 0)/3
	if !near(rep.PassAt[2], (1+2.0/3)/3) {
		t.Errorf("PassAt[2] = %v", rep.PassAt[2])
	}
	if !near(rep.HackRate, 1.0/8) || !near(rep.BudgetRate, 1.0/8) {
		t.Errorf("hack %v budget %v", rep.HackRate, rep.BudgetRate)
	}
	// Cost distribution over the 8 completed rollouts: 100x3, 200x3, 300x2.
	if !near(rep.ITE.Mean, (300+600+600)/8.0) || !near(rep.ITE.Median, 200) || rep.ITE.Max != 300 || rep.ITE.Min != 100 {
		t.Errorf("ITE dist: %+v", rep.ITE)
	}
	if !near(rep.USD.Mean, rep.ITE.Mean/100) || !near(rep.Requests.Mean, 10) || !near(rep.WallMs.Median, 1000) {
		t.Errorf("dists: %+v %+v %+v", rep.USD, rep.Requests, rep.WallMs)
	}
	// Tags.
	goTag := rep.ByTag["go"]
	if goTag.Tasks != 2 || goTag.Samples != 6 || !near(goTag.PassAt1, (1+1.0/3)/2) || !near(goTag.MeanITE, (300+600)/6.0) || !near(goTag.Hack, 1.0/6) {
		t.Errorf("go slice: %+v", goTag)
	}
	if py := rep.ByTag["py"]; py.Tasks != 1 || py.PassAt1 != 0 {
		t.Errorf("py slice: %+v (a dropped task must not count)", py)
	}
	if rep.ByTag["small"].Tasks != 1 {
		t.Errorf("small slice: %+v", rep.ByTag["small"])
	}
	// Roles.
	w := rep.ByRole["worker"]
	if w.Episodes != 2 || !near(w.MeanSteps, 5) || !near(w.MeanReward, 0.75) || !near(w.MeanOutputTokens, 200) {
		t.Errorf("worker role: %+v", w)
	}
	if rep.ByRole["compactor"].Episodes != 1 {
		t.Errorf("compactor role: %+v", rep.ByRole["compactor"])
	}
	// Per-task rows survive JSON round trips.
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back.PassHat, rep.PassHat) || back.PerTask[1].Correct != 1 {
		t.Fatalf("round trip: %v", err)
	}
}

func TestBuildReportEmpty(t *testing.T) {
	rep := BuildReport(nil, nil, 1)
	if rep.PassAt1 != 0 || rep.Completed != 0 || rep.InfraRate != 0 {
		t.Fatalf("%+v", rep)
	}
	rep = BuildReport(evalTasks(), []RolloutResult{{Task: "a", Status: StatusInfra}}, 1)
	if rep.PassAt1 != 0 || len(rep.Dropped) != 4 || rep.InfraRate != 1 {
		t.Fatalf("%+v", rep)
	}
}

func TestEvalRunsSamplesAndRefusesTrainingTasks(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.h.Scripts = map[string]FakeScript{
		"mathx-max/0": {Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}},
		"mathx-max/1": {Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}},
		"mathx-max/2": {},
	}
	opts := EvalOptions{Samples: 3, Rollout: f.opts()}
	rep, err := Eval(context.Background(), f.r, []rl.Task{f.task}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Model != "test-policy" || rep.RunID != "r001" || rep.Completed != 3 {
		t.Fatalf("%+v", rep)
	}
	if !near(rep.PassAt1, 2.0/3) || !near(rep.PassHat[2], 1.0/3) || !near(rep.PassHat[3], 0) {
		t.Fatalf("pass@1 %v pass^k %v", rep.PassAt1, rep.PassHat)
	}
	if !exists(filepath.Join(f.out, "report.json")) {
		t.Error("report.json not written")
	}
	if rep.ByTag["go"].Tasks != 1 {
		t.Errorf("by tag: %+v", rep.ByTag)
	}
	calls := len(f.h.Calls())

	// A training list containing the task id: refused before anything runs.
	list := filepath.Join(t.TempDir(), "train.txt")
	if err := os.WriteFile(list, []byte("other-task\nmathx-max\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Eval(context.Background(), f.r, []rl.Task{f.task}, EvalOptions{Samples: 1, ExcludeFile: list, Rollout: f.opts()})
	var ce *ContaminationError
	if !errors.Is(err, ErrContaminated) || !errors.As(err, &ce) || !reflect.DeepEqual(ce.IDs, []string{"mathx-max"}) {
		t.Fatalf("got %v", err)
	}
	if len(f.h.Calls()) != calls {
		t.Fatal("the harness ran despite contamination")
	}
	// Same repo and commit under another id is contamination too.
	renamed := f.task
	renamed.ID = "renamed"
	renamedJSON, _ := json.Marshal(f.task)
	if err := os.WriteFile(list, renamedJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Eval(context.Background(), f.r, []rl.Task{renamed}, EvalOptions{Samples: 1, ExcludeFile: list, Rollout: f.opts()}); !errors.Is(err, ErrContaminated) {
		t.Fatalf("renamed copy of a training task accepted: %v", err)
	}
	// In-memory exclusion.
	if _, err := Eval(context.Background(), f.r, []rl.Task{f.task}, EvalOptions{Exclude: ExcludeFromTasks([]rl.Task{f.task})}); !errors.Is(err, ErrContaminated) {
		t.Fatalf("got %v", err)
	}
	// A missing exclude file is an error, not a silent pass.
	if _, err := Eval(context.Background(), f.r, []rl.Task{f.task}, EvalOptions{ExcludeFile: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("missing exclude file ignored")
	}
	if !strings.Contains((&ContaminationError{IDs: []string{"a"}}).Error(), "a") {
		t.Error("error text")
	}
}

// synthetic report with one row per task.
func reportOf(model string, rows ...TaskResult) Report {
	return Report{Model: model, PerTask: rows}
}

func row(id string, n, correct int, ite float64) TaskResult {
	return TaskResult{ID: id, N: n, Correct: correct, PassAt1: float64(correct) / float64(n), MeanScore: float64(correct) / float64(n), MeanITE: ite, MeanUSD: ite / 100, MeanRequests: 10, MeanSteps: 5, MeanWallMs: 1000}
}

func TestCompareDetectsARealImprovement(t *testing.T) {
	var ra, rb []TaskResult
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("t%02d", i)
		// B solves every task at least as often as A, and 10 more outright.
		ca := 2
		cb := 2
		if i < 10 {
			cb = 4
		}
		ra = append(ra, row(id, 4, ca, 1000))
		rb = append(rb, row(id, 4, cb, 900))
	}
	c := Compare(reportOf("a", ra...), reportOf("b", rb...))
	if c.Paired != 30 || len(c.OnlyA) != 0 || len(c.OnlyB) != 0 || c.Confidence != 0.95 || c.Resamples != 2000 {
		t.Fatalf("%+v", c)
	}
	m, ok := c.Metric("pass_at_1")
	if !ok || !near(m.A, 0.5) || !near(m.B, 0.5+10.0/30*0.5) || !near(m.Delta, m.B-m.A) {
		t.Fatalf("pass_at_1: %+v", m)
	}
	if !m.Significant || m.Low <= 0 || m.High < m.Delta || m.Low > m.Delta || m.P > 0.01 {
		t.Fatalf("a consistent improvement must be significant: %+v", m)
	}
	cost, _ := c.Metric("mean_ite")
	if !near(cost.Delta, -100) || !cost.Significant || cost.High >= 0 {
		t.Fatalf("cost: %+v", cost)
	}
	if hat, ok := c.Metric("pass_hat_4"); !ok || hat.Delta <= 0 {
		t.Fatalf("pass^k: %+v", hat)
	}
	// Deterministic.
	c2 := Compare(reportOf("a", ra...), reportOf("b", rb...))
	if !reflect.DeepEqual(c, c2) {
		t.Fatal("Compare is not deterministic")
	}
	// A different seed moves the interval slightly but not the verdict.
	c3 := CompareWith(reportOf("a", ra...), reportOf("b", rb...), CompareOptions{Seed: 99, Resamples: 500, Confidence: 0.9})
	m3, _ := c3.Metric("pass_at_1")
	if m3.Delta != m.Delta || !m3.Significant || c3.Confidence != 0.9 {
		t.Fatalf("%+v", m3)
	}
}

func TestCompareIsCautiousWithNoise(t *testing.T) {
	var ra, rb []TaskResult
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("t%02d", i)
		// Half the tasks get better, half worse, by the same amount.
		if i%2 == 0 {
			ra = append(ra, row(id, 4, 1, 1000))
			rb = append(rb, row(id, 4, 3, 1000))
		} else {
			ra = append(ra, row(id, 4, 3, 1000))
			rb = append(rb, row(id, 4, 1, 1000))
		}
	}
	c := Compare(reportOf("a", ra...), reportOf("b", rb...))
	m, _ := c.Metric("pass_at_1")
	if m.Significant || m.Low >= 0 || m.High <= 0 || m.P < 0.5 {
		t.Fatalf("no real difference: %+v", m)
	}
	// Identical reports: zero delta, zero-width interval, p = 1.
	same := Compare(reportOf("a", ra...), reportOf("a", ra...))
	for _, m := range same.Metrics {
		if m.Delta != 0 || m.Low != 0 || m.High != 0 || m.P != 1 || m.Significant {
			t.Fatalf("identical reports: %+v", m)
		}
	}
}

func TestComparePairsOnSharedTasksOnly(t *testing.T) {
	a := reportOf("a", row("shared1", 2, 1, 100), row("shared2", 2, 2, 100), row("only-a", 2, 0, 100), TaskResult{ID: "dropped", N: 0})
	b := reportOf("b", row("shared1", 2, 2, 100), row("shared2", 2, 2, 100), row("only-b", 2, 2, 100), row("dropped", 2, 1, 100))
	c := Compare(a, b)
	if c.Paired != 2 || !reflect.DeepEqual(c.OnlyA, []string{"only-a"}) || !reflect.DeepEqual(c.OnlyB, []string{"only-b"}) {
		t.Fatalf("%+v", c)
	}
	m, _ := c.Metric("pass_at_1")
	if m.N != 2 || !near(m.A, 0.75) || !near(m.B, 1) {
		t.Fatalf("%+v", m)
	}
	// Nothing in common: an empty comparison, not a crash.
	empty := Compare(reportOf("x", row("p", 1, 1, 1)), reportOf("y", row("q", 1, 1, 1)))
	if empty.Paired != 0 || len(empty.Metrics) != 0 {
		t.Fatalf("%+v", empty)
	}
	if _, ok := empty.Metric("pass_at_1"); ok {
		t.Fatal("metric found in an empty comparison")
	}
	if got := label(Report{Model: "m", RunID: "r"}); got != "m (r)" {
		t.Fatal(got)
	}
}

func TestSplitMixIsStable(t *testing.T) {
	// Golden values: the bootstrap must not change with the Go version.
	r := newSplitMix(1)
	got := []uint64{r.next(), r.next(), r.next()}
	want := []uint64{0x910a2dec89025cc1, 0xbeeb8da1658eec67, 0xf893a2eefb32555e}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitmix64 output changed: %#x", got)
	}
}
