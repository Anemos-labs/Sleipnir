package env

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// waitFor polls cond for up to a minute.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// indexLen counts the records of a sample's savepoint index.
func indexLen(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, savepointDir, savepointIndex))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// respond appends a model response that cost usd to a log.
func respond(t *testing.T, log *events.Log, usd float64) {
	t.Helper()
	if _, err := log.Emit("main", events.TypeModelResponse, map[string]any{"cost_usd": usd}); err != nil {
		t.Fatal(err)
	}
}

// An attempt that ends in an infra failure after a savepoint does not start
// over: the next attempt gets the workspace as the savepoint captured it, the
// log cut back to it (the cut-off tail kept aside) and what is left of the
// budgets, and it does not use up one of the retries. The ledger counts each
// response once.
func TestAnInfraFailureAfterASavepointResumes(t *testing.T) {
	f := newRunnerFixture(t, quickVerify, func(f *runnerFixture) {
		f.r.InfraRetries = -1
		f.r.SavepointEvery = 20 * time.Millisecond
		f.r.Concurrency = 1
		f.task.Budget.Requests = 10
	})
	var resumedOK atomic.Bool
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		if !spec.Resume {
			log, err := events.Open(spec.RunDir, "fake")
			if err != nil {
				return RunResult{}, err
			}
			defer log.Close()
			_, _ = log.Emit("", events.TypeSessionStart, map[string]any{})
			mustWrite(t, filepath.Join(spec.Workspace, "mathx.go"), fixedMath)
			respond(t, log, 0.5)
			n := 0
			spec.Savepoints.Bind(func(ctx context.Context, capture func(context.Context) error) (SavepointInfo, error) {
				size, err := log.FlushedSize()
				if err != nil {
					return SavepointInfo{}, err
				}
				n++
				return SavepointInfo{N: n, Consistent: true, LogBytes: size}, capture(ctx)
			})
			waitFor(t, "a savepoint", func() bool { return indexLen(spec.RunDir) > 0 })
			spec.Savepoints.Bind(nil)
			respond(t, log, 0.25) // after the savepoint: cut off by the resume
			mustWrite(t, filepath.Join(spec.Workspace, "late.txt"), "after the savepoint\n")
			return RunResult{}, Infraf("provider", "the endpoint went away")
		}
		if spec.Budget.Requests != 9 {
			t.Errorf("the resumed attempt's request budget is %d, want 10 less the 1 response kept", spec.Budget.Requests)
		}
		if b, err := os.ReadFile(filepath.Join(spec.Workspace, "mathx.go")); err != nil || string(b) != fixedMath {
			t.Errorf("the workspace lacks the work before the savepoint: %v", err)
		}
		if _, err := os.Stat(filepath.Join(spec.Workspace, "late.txt")); err == nil {
			t.Error("the workspace has what was written after the savepoint")
		}
		log, err := events.Open(spec.RunDir, "fake")
		if err != nil {
			return RunResult{}, err
		}
		defer log.Close()
		respond(t, log, 0.125)
		resumedOK.Store(true)
		return RunResult{Claimed: "done"}, nil
	}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	r := sum.Results[0]
	if r.Status != StatusOK || !r.Pass || r.Attempts != 2 || !resumedOK.Load() {
		t.Fatalf("result %+v (infra %+v)", r, sum.InfraErrors)
	}
	dir := f.sampleDir(f.task.ID, 0)
	if got := spendOfRange(filepath.Join(dir, "events.rewound.1.jsonl"), 0, 1<<30); got.requests != 1 || got.usd != 0.25 {
		t.Errorf("the cut-off tail holds %+v, want the one response after the savepoint", got)
	}
	var requests int
	var usd float64
	for _, e := range readLedger(t, f.out) {
		requests += e.Requests
		usd += e.CostUSD
		if e.Status == StatusRewound {
			t.Errorf("a rewound entry for spend the infra attempt had recorded: %+v", e)
		}
	}
	if requests != 3 || usd != 0.875 {
		t.Errorf("the ledger counts %d responses for $%v, want 3 for $0.875", requests, usd)
	}
}

// Without a savepoint an infra failure still starts over, and uses up a retry.
func TestAnInfraFailureWithoutASavepointStartsOver(t *testing.T) {
	f := newRunnerFixture(t, quickVerify, func(f *runnerFixture) {
		f.r.InfraRetries = -1
		f.r.SavepointEvery = time.Hour
	})
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		return RunResult{}, Infraf("provider", "the endpoint went away")
	}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if r := sum.Results[0]; r.Status != StatusInfra || r.Attempts != 1 {
		t.Fatalf("result %+v", r)
	}
}

// A resume after the process died records the spend that no ledger entry has
// counted yet, keeps each cut-off tail under its own name, and accepts only a
// consistent savepoint of the same policy and seed whose log prefix exists.
func TestRewindAndTheSavepointsAResumeAccepts(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	respond(t, log, 1)
	kept, err := log.FlushedSize()
	if err != nil {
		t.Fatal(err)
	}
	respond(t, log, 2)
	respond(t, log, 4)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "episode.json"), `{"flags":["infra_error"]}`)
	good := savepointRecord{SavepointInfo: SavepointInfo{N: 1, Consistent: true, LogBytes: kept}, Tree: "t1", Policy: "p", Seed: 3}
	for _, rec := range []savepointRecord{
		good,
		{SavepointInfo: SavepointInfo{N: 2, Consistent: false, LogBytes: kept}, Tree: "t2", Policy: "p", Seed: 3},
		{SavepointInfo: SavepointInfo{N: 3, Consistent: true, LogBytes: kept}, Tree: "t3", Policy: "other", Seed: 3},
		{SavepointInfo: SavepointInfo{N: 4, Consistent: true, LogBytes: kept}, Tree: "t4", Policy: "p", Seed: 4},
		{SavepointInfo: SavepointInfo{N: 5, Consistent: true, LogBytes: 1 << 30}, Tree: "t5", Policy: "p", Seed: 3},
	} {
		if err := appendSavepoint(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	rec := lastSavepoint(dir, "p", 3)
	if rec == nil || rec.N != 1 {
		t.Fatalf("lastSavepoint = %+v, want savepoint 1", rec)
	}
	lost, err := rewindTo(dir, rec)
	if err != nil {
		t.Fatal(err)
	}
	if lost.requests != 3 || lost.usd != 7 {
		t.Errorf("unrecorded spend %+v, want all three responses", lost)
	}
	if fi, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil || fi.Size() != kept {
		t.Errorf("the log was not cut back to %d bytes: %v %v", kept, fi, err)
	}
	if got := spendOfRange(filepath.Join(dir, "events.rewound.1.jsonl"), 0, 1<<30); got.requests != 2 || got.usd != 6 {
		t.Errorf("the cut-off tail holds %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "episode.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the earlier attempt's episode is still there: %v", err)
	}
	if recordedBytes(dir) != kept {
		t.Errorf("recorded offset %d, want %d", recordedBytes(dir), kept)
	}
	if n := indexLen(dir); n != 1 {
		t.Errorf("the index keeps %d records after the rewind, want only savepoint 1 (the later ones belong to the cut log)", n)
	}

	// A second resume to the same point: nothing new is unrecorded, and its tail gets the next name.
	log, err = events.Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	respond(t, log, 8)
	_ = log.Close()
	if err := setRecordedBytes(dir, 1<<30); err != nil {
		t.Fatal(err)
	}
	if lost, err := rewindTo(dir, rec); err != nil || !lost.zero() {
		t.Errorf("second rewind: lost %+v, err %v", lost, err)
	}
	if got := spendOfRange(filepath.Join(dir, "events.rewound.2.jsonl"), 0, 1<<30); got.usd != 8 {
		t.Errorf("the second tail holds %+v", got)
	}
}

// What is left of a budget after a resume: never below one, never turned off.
func TestRemainingBudget(t *testing.T) {
	b := remainingBudget(rl.Budget{Steps: 10, Requests: 20, WallS: 60}, spend{requests: 25, main: 4}, true)
	if b.Requests != 1 || b.Steps != 6 || b.WallS != 60 {
		t.Errorf("single agent: %+v", b)
	}
	if b := remainingBudget(rl.Budget{Steps: 10}, spend{main: 4}, false); b.Steps != 10 {
		t.Errorf("a team's members count steps per run: %+v", b)
	}
	if b := remainingBudget(rl.Budget{}, spend{requests: 5, main: 5}, true); b.Requests != 0 || b.Steps != 0 {
		t.Errorf("an open budget stays open: %+v", b)
	}
}

// Unbinding the hook ends a savepoint in progress and waits for it.
func TestUnbindingTheHookEndsASavepointInProgress(t *testing.T) {
	h := newSavepointHook(time.Minute)
	if h.Polite != 30*time.Second || h.Firm != time.Minute {
		t.Errorf("polite %v firm %v", h.Polite, h.Firm)
	}
	if _, bound, _ := h.Take(context.Background(), nil); bound {
		t.Fatal("an unbound hook took a savepoint")
	}
	started, ended := make(chan struct{}), make(chan struct{})
	h.Bind(func(ctx context.Context, _ func(context.Context) error) (SavepointInfo, error) {
		close(started)
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		close(ended)
		return SavepointInfo{}, ctx.Err()
	})
	go func() { _, _, _ = h.Take(context.Background(), nil) }()
	<-started
	h.Bind(nil)
	select {
	case <-ended:
	default:
		t.Fatal("Bind(nil) returned while the savepoint still ran")
	}
}

// With ScoreSavepoints every savepoint's tree is scored by the task's verifier
// in progress mode (its environment carries SUITE_SAMPLE and SUITE_BUDGET_S),
// in a clean checkout, and the scores reach progress.jsonl and the episode; the
// final verification runs without them.
func TestSavepointsAreScoredInProgressMode(t *testing.T) {
	f := newRunnerFixture(t, func(f *runnerFixture) {
		f.r.InfraRetries = -1
		f.r.SavepointEvery = 20 * time.Millisecond
		f.r.ScoreSavepoints, f.r.ProgressSample, f.r.ProgressBudget = true, 7, 90*time.Second
		f.r.Concurrency = 1
		// 0.25 for a fixed tree in progress mode with the expected settings, 1 for it at the end, 0 for the start.
		f.task.Verifier = rl.Verifier{Pass: "json-score", TimeoutS: 60, Cmd: `if ! tr -d ' \t\n' < mathx.go | grep -q 'ifa>b{returna}'; then echo '{"score": 0}'; ` +
			`elif [ "$SUITE_SAMPLE/$SUITE_BUDGET_S" = 7/90 ]; then echo '{"score": 0.25}'; else echo '{"score": 1}'; fi`}
	})
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		log, err := events.Open(spec.RunDir, "fake")
		if err != nil {
			return RunResult{}, err
		}
		defer log.Close()
		mustWrite(t, filepath.Join(spec.Workspace, "mathx.go"), fixedMath)
		n := 0
		spec.Savepoints.Bind(func(ctx context.Context, capture func(context.Context) error) (SavepointInfo, error) {
			size, err := log.FlushedSize()
			if err != nil {
				return SavepointInfo{}, err
			}
			n++
			return SavepointInfo{N: n, Consistent: true, LogBytes: size}, capture(ctx)
		})
		waitFor(t, "a scored savepoint", func() bool { return len(readProgress(spec.RunDir)) > 0 })
		spec.Savepoints.Bind(nil)
		return RunResult{Claimed: "done"}, nil
	}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if r := sum.Results[0]; r.Status != StatusOK || !r.Pass || r.Score != 1 {
		t.Fatalf("result %+v", r)
	}
	ep := f.episode(f.task.ID, 0)
	if len(ep.Progress) == 0 || ep.Progress[0].Score != 0.25 || ep.Progress[0].Savepoint < 1 {
		t.Fatalf("progress %+v: want the savepoint scored 0.25 in progress mode", ep.Progress)
	}
}

// A finished rollout's savepoint is continued K times: each branch gets the
// trunk's work tree as the savepoint captured it, a log that is the trunk's up
// to the savepoint plus an rl.fork event, and its own seed; the branches of
// one point are one group, and the run's ledger has only their own spend.
func TestBranchContinuesTheTrunkFromASavepoint(t *testing.T) {
	f := newRunnerFixture(t, quickVerify, func(f *runnerFixture) {
		f.r.InfraRetries = -1
		f.r.SavepointEvery = 20 * time.Millisecond
		f.r.Concurrency = 1
	})
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		log, err := events.Open(spec.RunDir, "trunk")
		if err != nil {
			return RunResult{}, err
		}
		defer log.Close()
		_, _ = log.Emit("", events.TypeSessionStart, map[string]any{})
		mustWrite(t, filepath.Join(spec.Workspace, "mathx.go"), fixedMath)
		respond(t, log, 1)
		n := 0
		spec.Savepoints.Bind(func(ctx context.Context, capture func(context.Context) error) (SavepointInfo, error) {
			size, err := log.FlushedSize()
			if err != nil {
				return SavepointInfo{}, err
			}
			n++
			return SavepointInfo{N: n, Consistent: true, LogBytes: size}, capture(ctx)
		})
		waitFor(t, "a savepoint", func() bool { return indexLen(spec.RunDir) > 0 })
		spec.Savepoints.Bind(nil)
		mustWrite(t, filepath.Join(spec.Workspace, "after.txt"), "after the savepoint\n")
		respond(t, log, 2)
		return RunResult{Claimed: "done"}, nil
	}
	f.rollout([]rl.Task{f.task}, 1, f.opts())
	trunk := f.sampleDir(f.task.ID, 0)
	points, err := TrunkSavepoints(trunk)
	if err != nil || len(points) == 0 {
		t.Fatalf("trunk savepoints %v %v", points, err)
	}

	var seeds []int64
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		if !spec.Resume {
			t.Error("a branch did not resume")
		}
		seeds = append(seeds, spec.Seed)
		if b, err := os.ReadFile(filepath.Join(spec.Workspace, "mathx.go")); err != nil || string(b) != fixedMath {
			t.Errorf("the branch lacks the trunk's work before the savepoint: %v", err)
		}
		if _, err := os.Stat(filepath.Join(spec.Workspace, "after.txt")); err == nil {
			t.Error("the branch has the trunk's work after the savepoint")
		}
		log, err := events.Open(spec.RunDir, "trunk")
		if err != nil {
			return RunResult{}, err
		}
		defer log.Close()
		respond(t, log, 0.5)
		return RunResult{Claimed: "done"}, nil
	}
	out := filepath.Join(t.TempDir(), "branches")
	f.r.Out = out
	sum, err := f.r.Branch(context.Background(), f.task, BranchRequest{Trunk: trunk, Points: points[:1], Siblings: 2}, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Completed != 2 || len(seeds) != 2 || seeds[0] == seeds[1] {
		t.Fatalf("completed %d, seeds %v", sum.Completed, seeds)
	}
	var groups []string
	for s := 0; s < 2; s++ {
		ep := loadEpisode(t, filepath.Join(out, f.task.ID, strconv.Itoa(s)))
		if ep.Fork == nil || ep.Fork.Savepoint != points[0] || ep.Fork.Sibling != s+1 || ep.Fork.Siblings != 2 {
			t.Errorf("sample %d: fork %+v", s, ep.Fork)
			continue
		}
		groups = append(groups, ep.Group)
	}
	if len(groups) != 2 || groups[0] != groups[1] || !strings.Contains(groups[0], ":sp") {
		t.Errorf("the branches of one point are not one group: %v", groups)
	}
	var requests int
	for _, e := range readLedger(t, out) {
		requests += e.Requests
	}
	if requests != 2 {
		t.Errorf("the branch run's ledger counts %d responses, want the 2 the branches made", requests)
	}
	if _, err := f.r.Branch(context.Background(), f.task, BranchRequest{Trunk: trunk, Points: []int{99}, Siblings: 1}, f.opts()); err == nil {
		t.Error("a branch from a savepoint the trunk does not have was accepted")
	}
}

// loadEpisode reads a sample's episode.json.
func loadEpisode(t *testing.T, dir string) *rl.Episode {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "episode.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ep rl.Episode
	if err := json.Unmarshal(b, &ep); err != nil {
		t.Fatal(err)
	}
	return &ep
}
