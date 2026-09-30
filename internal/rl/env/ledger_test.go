package env

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl"
)

// spendAs writes the model responses of a fake run into its log: one per cost, the way a real session records them.
func spendAs(t *testing.T, dir string, costs ...float64) {
	t.Helper()
	log, err := events.Open(dir, "fake-session")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range costs {
		if _, err := log.Emit("worker", events.TypeModelResponse, map[string]any{"cost_usd": c}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := log.Emit("worker", events.TypeModelError, map[string]any{"kind": "rate_limit", "status": 429}); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
}

func readLedger(t *testing.T, out string) []LedgerEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(out, LedgerFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var es []LedgerEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e LedgerEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("ledger line %q: %v", sc.Text(), err)
		}
		es = append(es, e)
	}
	return es
}

// What a failed attempt spent was lost with its directory (every attempt starts from an empty one), so a run that retried
// reported only its last attempt and the total of a benchmark undercounted what the endpoint charged.
func TestTheLedgerKeepsWhatEveryAttemptSpent(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	var calls atomic.Int32
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		if calls.Add(1) == 1 {
			spendAs(t, spec.RunDir, 0.01, 0.01)
			return RunResult{}, Infra("provider", errors.New("502 from the endpoint"))
		}
		spendAs(t, spec.RunDir, 0.005)
		return RunResult{Claimed: "done"}, nil
	}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if sum.Completed != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	es := readLedger(t, f.out)
	if len(es) != 2 {
		t.Fatalf("ledger has %d entries, want one per attempt: %+v", len(es), es)
	}
	if es[0].Attempt != 1 || es[0].Status != StatusInfra || math.Abs(es[0].CostUSD-0.02) > 1e-9 || es[0].Requests != 2 || es[0].Retries != 1 {
		t.Errorf("first attempt: %+v", es[0])
	}
	if es[1].Attempt != 2 || es[1].Status != StatusOK || math.Abs(es[1].CostUSD-0.005) > 1e-9 {
		t.Errorf("second attempt: %+v", es[1])
	}
	if math.Abs(sum.SpentUSD-0.025) > 1e-9 {
		t.Errorf("the summary says %v was spent, the endpoint charged 0.025", sum.SpentUSD)
	}
}

// A run's spend has a cap of its own: the budget of one rollout bounds that rollout and says nothing about a thousand of them.
func TestARunsSpendCapStopsStartingRollouts(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.r.Concurrency = 1
	f.r.MaxSpendUSD = 0.03
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		spendAs(t, spec.RunDir, 0.02)
		return RunResult{Claimed: "done"}, nil
	}
	sum := f.rollout([]rl.Task{f.task}, 6, f.opts())
	if sum.Completed != 2 || sum.Capped != 4 {
		t.Fatalf("completed %d capped %d, want 2 and 4 (the cap was 0.03 and a rollout costs 0.02)", sum.Completed, sum.Capped)
	}
	if n := len(f.h.Calls()); n != 2 {
		t.Errorf("the harness ran %d times", n)
	}
	capped := 0
	for _, r := range sum.Results {
		if r.Status == StatusCapped {
			capped++
		}
	}
	if capped != 4 {
		t.Errorf("%d results say capped", capped)
	}
	if math.Abs(sum.SpentUSD-0.04) > 1e-9 {
		t.Errorf("spent %v", sum.SpentUSD)
	}
}

// Resuming counts what the earlier invocation spent: the cap is the run's, not the invocation's.
func TestTheSpendCapCountsWhatEarlierInvocationsSpent(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.r.Concurrency = 1
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		spendAs(t, spec.RunDir, 0.02)
		return RunResult{Claimed: "done"}, nil
	}
	f.rollout([]rl.Task{f.task}, 2, f.opts()) // 0.04 spent
	f.r.MaxSpendUSD = 0.05
	sum := f.rollout([]rl.Task{f.task}, 4, f.opts()) // two resumed, two to run: 0.04 + 0.02 reaches the cap after the third
	if sum.Resumed != 2 || sum.Completed+sum.Capped != 4 || sum.Capped < 1 {
		t.Fatalf("resumed %d completed %d capped %d", sum.Resumed, sum.Completed, sum.Capped)
	}
	if es := readLedger(t, f.out); len(es) < 3 {
		t.Errorf("ledger: %+v", es)
	}
}

// A rollout runs under the budget of its task; what the task leaves open comes from the run.
func TestTheRunsDefaultBudgetFillsWhatATaskLeavesOpen(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.task.Budget = rl.Budget{Requests: 10, WallS: 60}
	opts := f.opts()
	opts.Budget = rl.Budget{Steps: 40, Requests: 80, USD: 0.25, WallS: 999}
	f.rollout([]rl.Task{f.task}, 1, opts)
	calls := f.h.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls", len(calls))
	}
	want := rl.Budget{Steps: 40, Requests: 10, USD: 0.25, WallS: 60}
	if got := calls[0].Budget; got != want {
		t.Fatalf("the harness got the budget %+v, want %+v", got, want)
	}
}

// Finished episodes are skipped on a rerun into the same directory. They were produced by one policy: resuming under
// another would file its rollouts under the first one's results.
func TestResumingUnderAnotherPolicyIsRefused(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.rollout([]rl.Task{f.task}, 1, f.opts())
	other := f.opts()
	other.Policy.Model = "another-policy"
	_, err := f.r.Rollout(context.Background(), []rl.Task{f.task}, 1, other)
	if err == nil || !strings.Contains(err.Error(), "another-policy") || !strings.Contains(err.Error(), "test-policy") {
		t.Fatalf("want an error naming both policies, got %v", err)
	}
	if n := len(f.h.Calls()); n != 1 {
		t.Errorf("the refused run still ran %d rollouts", n)
	}
	// Force reruns everything under the new policy; the old one is not resumed into it.
	other.Force = true
	if _, err := f.r.Rollout(context.Background(), []rl.Task{f.task}, 1, other); err != nil {
		t.Fatalf("Force should rerun under the new policy: %v", err)
	}
	// The same policy resumes.
	f2 := newRunnerFixture(t, quickVerify)
	f2.rollout([]rl.Task{f2.task}, 1, f2.opts())
	f2.rollout([]rl.Task{f2.task}, 1, f2.opts())
}

// A verifier that changed since the run began produced verdicts no rerun can be compared with.
func TestResumingWithAChangedVerifierIsRefused(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.rollout([]rl.Task{f.task}, 1, f.opts())
	changed := f.task
	changed.Verifier.Cmd += " && true"
	_, err := f.r.Rollout(context.Background(), []rl.Task{changed}, 1, f.opts())
	if err == nil || !strings.Contains(err.Error(), f.task.ID) {
		t.Fatalf("want an error naming the task, got %v", err)
	}
}
