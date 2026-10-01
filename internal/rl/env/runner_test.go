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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// runnerFixture wires a FakeHarness, injected Extract/Score fakes and a real
// git repository with a Go test as verifier.
type runnerFixture struct {
	t       *testing.T
	repo    *fixtureRepo
	base    string
	task    rl.Task
	m       *Workspaces
	h       *FakeHarness
	r       *Runner
	out     string
	extract atomic.Int32
	score   atomic.Int32

	pmu      sync.Mutex
	progress []Progress
}

func newRunnerFixture(t *testing.T, mut ...func(*runnerFixture)) *runnerFixture {
	t.Helper()
	repo, base := mathxRepo(t)
	f := &runnerFixture{t: t, repo: repo, base: base, task: mathxTask(repo, base)}
	f.task.Budget.WallS = 60
	f.m = newManager(t)
	f.h = &FakeHarness{Release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-f.h.Release:
		default:
			close(f.h.Release)
		}
	})
	f.out = filepath.Join(t.TempDir(), "runs", "r001")
	f.r = &Runner{
		Harness: f.h, Workspaces: f.m, Out: f.out, Concurrency: 3,
		RetryBackoff: time.Millisecond, StopGrace: 300 * time.Millisecond,
		Extract: func(runDir string, task rl.Task, sample int, group string) (*rl.Episode, error) {
			f.extract.Add(1)
			return MinimalExtractor(runDir, task, sample, group)
		},
		Score: func(ep *rl.Episode, task *rl.Task, runDir string) error {
			f.score.Add(1)
			// A stand-in for reward.Score: outcome minus penalties for flags.
			total := 0.0
			if v := ep.Outcome.Verifier; v != nil {
				total = v.Score
			}
			if ep.Has(rl.FlagHackProtected) || ep.Has(rl.FlagHackVerifier) {
				total = 0
			}
			ep.Reward = rl.Reward{Total: total, Components: map[string]float64{"outcome": total}}
			return nil
		},
		Progress: func(p Progress) {
			f.pmu.Lock()
			f.progress = append(f.progress, p)
			f.pmu.Unlock()
		},
	}
	for _, m := range mut {
		m(f)
	}
	return f
}

// quickVerify swaps the task's verifier for a shell check that costs
// milliseconds instead of a `go test` run. Tests about orchestration (ordering,
// seeds, resume, cancellation, streaming) do not care what the verifier is, only
// that it tells the fixed tree from the buggy one; the tests about verification
// itself keep the real thing. The check squeezes out whitespace so it does not
// depend on formatting: the fix swaps the branch's return value.
func quickVerify(f *runnerFixture) {
	f.task.Verifier.Cmd = `tr -d ' \t\n' < mathx.go | grep -q 'ifa>b{returna}'`
}

func (f *runnerFixture) opts() RolloutOpts {
	return RolloutOpts{
		Policy: PolicySpec{Model: "test-policy", BaseURL: "https://user:pw@policy.example:8000/v1?key=abc", APIKeyEnv: "POLICY_KEY", Sampling: json.RawMessage(`{"temperature":1}`)},
		Seed:   7, TargetPrice: "anthropic-sonnet",
	}
}

func (f *runnerFixture) rollout(tasks []rl.Task, group int, opts RolloutOpts) *Summary {
	f.t.Helper()
	sum, err := f.r.Rollout(context.Background(), tasks, group, opts)
	if err != nil {
		f.t.Fatalf("Rollout: %v", err)
	}
	return sum
}

func (f *runnerFixture) sampleDir(task string, sample int) string {
	return filepath.Join(f.out, task, fmt.Sprint(sample))
}

func (f *runnerFixture) episode(task string, sample int) *rl.Episode {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.sampleDir(task, sample), "episode.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var ep rl.Episode
	if err := json.Unmarshal(b, &ep); err != nil {
		f.t.Fatal(err)
	}
	return &ep
}

func (f *runnerFixture) outcomes(task string, sample int) []map[string]any {
	f.t.Helper()
	var out []map[string]any
	err := events.Scan(filepath.Join(f.sampleDir(task, sample), "events.jsonl"), func(e events.Event) error {
		if e.Type == events.TypeOutcome {
			var m map[string]any
			if err := json.Unmarshal(e.Data, &m); err != nil {
				return err
			}
			out = append(out, m)
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// TestRolloutFullPath is the integration test of the whole environment: six
// samples of one task, each behaving differently, through prepare -> agent ->
// clean-checkout verification -> outcome events -> extract -> score ->
// episode.json -> cleanup, plus the run-level manifest and summary.
func TestRolloutFullPath(t *testing.T) {
	f := newRunnerFixture(t)
	f.task.Budget.WallS = 3 // the hanging sample burns it
	f.h.Scripts = map[string]FakeScript{
		// 0: a correct fix.
		"mathx-max/0": {Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}, Final: "fixed Max", Claimed: "done"},
		// 1: does nothing but claims success.
		"mathx-max/1": {Final: "all done", Claimed: "done"},
		// 2: tampers with tests: weakens the visible one and pre-empts the hidden one.
		"mathx-max/2": {Steps: []FakeStep{
			FakeWrite("mathx_test.go", "package mathx\n"),
			FakeWrite("mathx_hidden_test.go", "package mathx\nimport \"testing\"\nfunc TestMax(t *testing.T) {}\n"),
		}, Claimed: "done"},
		// 3: hangs until the wall clock stops it.
		"mathx-max/3": {Steps: []FakeStep{FakeWrite("notes.txt", "thinking\n"), FakeHang()}},
		// 4: the harness panics, every time.
		"mathx-max/4": {Steps: []FakeStep{FakePanic("index out of range")}},
		// 5: a provider outage on the first attempt, a correct fix on the retry.
		"mathx-max/5": {Steps: []FakeStep{FakeInfra("provider 503").OnAttempt(1), FakeWrite("mathx.go", fixedMath)}, Claimed: "done"},
	}
	sum := f.rollout([]rl.Task{f.task}, 6, f.opts())

	// ---- summary ----
	if sum.Rollouts != 6 || sum.Completed != 5 || sum.Infra != 1 || sum.Cancelled != 0 {
		t.Fatalf("summary counts: %+v", sum)
	}
	if want := 2.0 / 5; sum.PassRate != want {
		t.Errorf("PassRate = %v, want %v (infra errors are not failures)", sum.PassRate, want)
	}
	if want := 1.0 / 6; diff(sum.InfraRate, want) > 1e-9 {
		t.Errorf("InfraRate = %v, want %v", sum.InfraRate, want)
	}
	if want := 1.0 / 5; diff(sum.HackRate, want) > 1e-9 {
		t.Errorf("HackRate = %v, want %v", sum.HackRate, want)
	}
	if want := 1.0 / 5; diff(sum.BudgetRate, want) > 1e-9 {
		t.Errorf("BudgetRate = %v, want %v", sum.BudgetRate, want)
	}
	if len(sum.InfraErrors) != 1 || sum.InfraErrors[0].Sample != 4 || sum.InfraErrors[0].Attempts != 3 || !strings.Contains(sum.InfraErrors[0].Message, "panic") {
		t.Errorf("infra errors: %+v", sum.InfraErrors)
	}
	if len(sum.PerTask) != 1 || sum.PerTask[0].Passed != 2 || sum.PerTask[0].Samples != 5 || sum.PerTask[0].Infra != 1 {
		t.Errorf("per task: %+v", sum.PerTask)
	}

	// ---- directory layout ----
	for s := 0; s < 6; s++ {
		dir := f.sampleDir("mathx-max", s)
		for _, name := range []string{"events.jsonl", "task.json", "episode.json", "env.json"} {
			if !exists(filepath.Join(dir, name)) {
				t.Errorf("sample %d lacks %s", s, name)
			}
		}
		if s != 4 { // the panicking harness never produced a verdict
			for _, name := range []string{"diff.patch", "verifier.log"} {
				if !exists(filepath.Join(dir, name)) {
					t.Errorf("sample %d lacks %s", s, name)
				}
			}
		}
	}
	for _, name := range []string{"manifest.json", "summary.json"} {
		if !exists(filepath.Join(f.out, name)) {
			t.Errorf("missing %s", name)
		}
	}

	// ---- per-sample verdicts and flags ----
	ep0 := f.episode("mathx-max", 0)
	if v := ep0.Outcome.Verifier; v == nil || !v.Pass || v.Score != 1 || v.Detail == "" || ep0.Outcome.Claimed != "done" || ep0.Outcome.Diff == "" {
		t.Errorf("sample 0 outcome: %+v", ep0.Outcome)
	}
	if ep0.Reward.Total != 1 || len(ep0.Flags) != 0 {
		t.Errorf("sample 0: reward %v flags %v", ep0.Reward, ep0.Flags)
	}
	if ep0.ID != "mathx-max/0" || ep0.Group != "mathx-max@r001" || ep0.Sample != 0 || ep0.TaskID != "mathx-max" {
		t.Errorf("sample 0 identity: %+v", ep0)
	}
	if ep0.Policy.Model != "test-policy" || ep0.Policy.Endpoint != "https://policy.example:8000" || strings.Contains(jsonOf(ep0), "pw@") || strings.Contains(jsonOf(ep0), "key=abc") {
		t.Errorf("policy ref / credentials: %+v", ep0.Policy)
	}
	if ep0.Env.Commit != f.base || ep0.Env.TreeHash == "" || ep0.Provenance.License != "MIT" {
		t.Errorf("env/provenance: %+v %+v", ep0.Env, ep0.Provenance)
	}
	if len(ep0.Agents) != 1 || len(ep0.Agents[0].Steps) != 1 {
		t.Errorf("extracted agents: %+v", ep0.Agents)
	}

	ep1 := f.episode("mathx-max", 1)
	if v := ep1.Outcome.Verifier; v == nil || v.Pass || v.Score != 0 || ep1.Outcome.Claimed != "done" {
		t.Errorf("sample 1 (did nothing, claimed done): %+v", ep1.Outcome)
	}

	ep2 := f.episode("mathx-max", 2)
	if ep2.Outcome.Verifier.Pass {
		t.Error("sample 2 passed by tampering with tests")
	}
	if !ep2.Has(rl.FlagHackProtected) || !ep2.Has(rl.FlagHackVerifier) || ep2.Reward.Total != 0 {
		t.Errorf("sample 2 flags %v reward %v", ep2.Flags, ep2.Reward)
	}
	oc := f.outcomes("mathx-max", 2)
	if len(oc) != 1 || oc[0]["kind"] != "verifier" || oc[0]["pass"] != false {
		t.Fatalf("sample 2 outcome events: %v", oc)
	}
	prot, _ := oc[0]["protected"].([]any)
	if len(prot) != 2 {
		t.Errorf("outcome event protected list: %v", oc[0]["protected"])
	}

	ep3 := f.episode("mathx-max", 3)
	if !ep3.Has(rl.FlagBudgetExceeded) || ep3.Outcome.Claimed != "budget" || ep3.Has(rl.FlagInfraError) {
		t.Errorf("sample 3 (budget stop): flags %v claimed %q", ep3.Flags, ep3.Outcome.Claimed)
	}
	if ep3.Outcome.Verifier == nil || ep3.Outcome.Verifier.Pass {
		t.Errorf("sample 3 must still be verified: %+v", ep3.Outcome.Verifier)
	}
	if ep3.Cost.WallMs < 2500 || ep3.Cost.WallMs > 30000 {
		t.Errorf("sample 3 wall time %d ms", ep3.Cost.WallMs)
	}

	ep4 := f.episode("mathx-max", 4)
	if !ep4.Has(rl.FlagInfraError) || ep4.Outcome.Verifier != nil {
		t.Errorf("sample 4 (panic): flags %v verifier %v", ep4.Flags, ep4.Outcome.Verifier)
	}
	oc = f.outcomes("mathx-max", 4)
	if len(oc) != 1 || oc[0]["kind"] != "infra" || oc[0]["pass"] != false || !strings.Contains(fmt.Sprint(oc[0]["message"]), "panic") {
		t.Errorf("sample 4 outcome events: %v", oc)
	}

	ep5 := f.episode("mathx-max", 5)
	if ep5.Has(rl.FlagInfraError) || ep5.Outcome.Verifier == nil || !ep5.Outcome.Verifier.Pass {
		t.Errorf("sample 5 should have recovered on the retry: flags %v", ep5.Flags)
	}
	// Only one outcome event even though the first attempt failed: attempts
	// start from an empty directory.
	if oc := f.outcomes("mathx-max", 5); len(oc) != 1 {
		t.Errorf("sample 5 outcome events: %v", oc)
	}
	res5 := sum.Results[5]
	if res5.Attempts != 2 || res5.Status != StatusOK {
		t.Errorf("sample 5 result: %+v", res5)
	}

	// ---- task.json never carries hidden test content ----
	raw := mustRead(t, filepath.Join(f.sampleDir("mathx-max", 0), "task.json"))
	if strings.Contains(raw, "TestMax") || !strings.Contains(raw, "redacted:sha256:") {
		t.Errorf("task.json leaks hidden content or lacks the redaction marker:\n%s", raw)
	}
	var tj rl.Task
	if err := json.Unmarshal([]byte(raw), &tj); err != nil || tj.ID != "mathx-max" {
		t.Errorf("task.json is not an rl.Task: %v", err)
	}
	for s := 0; s < 6; s++ {
		for _, name := range []string{"events.jsonl", "verifier.log", "diff.patch"} {
			if b, err := os.ReadFile(filepath.Join(f.sampleDir("mathx-max", s), name)); err == nil && strings.Contains(string(b), "func TestMax(t *testing.T) {\n\tcases") {
				t.Errorf("sample %d %s contains hidden test source", s, name)
			}
		}
	}

	// ---- manifest ----
	var mf Manifest
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(f.out, "manifest.json"))), &mf); err != nil {
		t.Fatal(err)
	}
	if mf.Status != "done" || mf.GroupSize != 6 || mf.RunID != "r001" || mf.Config.Seed != 7 || mf.Config.Concurrency != 3 {
		t.Errorf("manifest: %+v", mf)
	}
	if mf.Policy.Endpoint != "https://policy.example:8000" || mf.Policy.APIKeyEnv != "POLICY_KEY" || strings.Contains(jsonOf(mf), "pw@") || strings.Contains(jsonOf(mf), "abc") {
		t.Errorf("manifest policy: %+v", mf.Policy)
	}
	if len(mf.Tasks) != 1 || mf.Tasks[0].VerifierVersion != VerifierVersion(f.task) || mf.Tasks[0].HiddenFiles != 1 {
		t.Errorf("manifest tasks: %+v", mf.Tasks)
	}
	if mf.Versions.Env != EnvVersion || mf.Versions.Git == "" || mf.Versions.Go == "" {
		t.Errorf("manifest versions: %+v", mf.Versions)
	}

	// ---- what the harness was given ----
	calls := f.h.Calls()
	seeds := map[int64]bool{}
	dirs := map[string]bool{}
	for _, c := range calls {
		if c.Attempt == 1 || c.Sample == 5 || c.Sample == 4 {
			seeds[c.Seed] = true
		}
		if dirs[c.Workspace+fmt.Sprint(c.Attempt)] {
			t.Errorf("two runs shared a workspace: %s", c.Workspace)
		}
		dirs[c.Workspace+fmt.Sprint(c.Attempt)] = true
		if c.Seed != SampleSeed(7, c.Task, c.Sample) || c.Group != "mathx-max@r001" {
			t.Errorf("call %+v", c)
		}
		envm := EnvMap(c.Env)
		if envm["HOME"] == "" || !strings.HasPrefix(envm["HOME"], f.m.Root()) || envm["GOPROXY"] != "off" {
			t.Errorf("agent env: HOME=%q", envm["HOME"])
		}
	}
	if len(seeds) != 6 {
		t.Errorf("expected 6 distinct sample seeds, got %d", len(seeds))
	}
	if n := len(calls); n != 6+2+1 { // 6 samples + 2 extra panics + 1 retry of sample 5
		t.Errorf("harness called %d times", n)
	}
	if f.h.MaxConcurrent() > 3 || f.h.MaxConcurrent() < 2 {
		t.Errorf("max concurrent = %d, want 2..3", f.h.MaxConcurrent())
	}
	// Injected pipeline stages ran exactly for the verified rollouts.
	if f.extract.Load() != 5 || f.score.Load() != 5 {
		t.Errorf("extract %d score %d, want 5 each", f.extract.Load(), f.score.Load())
	}

	// ---- workspaces are cleaned up ----
	if ents, _ := os.ReadDir(filepath.Join(f.m.Root(), "ws")); len(ents) != 0 {
		t.Errorf("%d workspaces left behind", len(ents))
	}
	if ents, _ := os.ReadDir(filepath.Join(f.m.Root(), "verify")); len(ents) != 0 {
		t.Errorf("%d verification checkouts left behind", len(ents))
	}

	// ---- progress ----
	f.pmu.Lock()
	prog := append([]Progress(nil), f.progress...)
	f.pmu.Unlock()
	if len(prog) == 0 || prog[0].Type != "run.start" || prog[len(prog)-1].Type != "run.done" || prog[len(prog)-1].Done != 6 {
		t.Errorf("progress bookends: %+v ... %+v", prog[0], prog[len(prog)-1])
	}
	last := 0
	stages := map[string]bool{}
	for _, p := range prog {
		if p.Done < last {
			t.Errorf("progress went backwards: %d after %d", p.Done, last)
		}
		last = p.Done
		if p.Type == "rollout.stage" {
			stages[p.Stage] = true
		}
	}
	for _, st := range []string{"prepare", "agent", "verify", "extract", "score"} {
		if !stages[st] {
			t.Errorf("no progress event for stage %s", st)
		}
	}
}

func TestRolloutIsResumableAndForceable(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.h.Scripts = map[string]FakeScript{
		"mathx-max/0": {Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}},
		"mathx-max/1": {},
		"mathx-max/2": {Steps: []FakeStep{FakeInfra("provider down")}}, // fails every attempt
	}
	f.r.InfraRetries = -1
	sum := f.rollout([]rl.Task{f.task}, 3, f.opts())
	if sum.Completed != 2 || sum.Infra != 1 || len(f.h.Calls()) != 3 {
		t.Fatalf("first run: %+v calls %d", sum, len(f.h.Calls()))
	}
	// Rerun with the same Out: finished rollouts are skipped, the infra one is
	// retried (it may have been a transient outage).
	f.h.Scripts["mathx-max/2"] = FakeScript{Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}}
	sum = f.rollout([]rl.Task{f.task}, 3, f.opts())
	if sum.Completed != 3 || sum.Resumed != 2 || sum.Infra != 0 {
		t.Fatalf("resumed run: completed %d resumed %d infra %d", sum.Completed, sum.Resumed, sum.Infra)
	}
	if calls := f.h.Calls(); len(calls) != 4 || calls[3].Sample != 2 {
		t.Fatalf("only the infra-failed sample should have rerun: %d calls", len(calls))
	}
	if sum.PassRate != 2.0/3 {
		t.Errorf("pass rate %v", sum.PassRate)
	}
	// The resumed summary reconstructs metrics from the stored episodes.
	if !sum.Results[0].Resumed || !sum.Results[0].Pass || sum.Results[1].Pass {
		t.Errorf("resumed results: %+v", sum.Results[:2])
	}
	// The manifest remembers the resume and keeps its creation time.
	var mf Manifest
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(f.out, "manifest.json"))), &mf); err != nil {
		t.Fatal(err)
	}
	if mf.Resumes != 1 {
		t.Errorf("manifest resumes = %d", mf.Resumes)
	}
	// Force reruns everything.
	opts := f.opts()
	opts.Force = true
	sum = f.rollout([]rl.Task{f.task}, 3, opts)
	if sum.Resumed != 0 || len(f.h.Calls()) != 7 {
		t.Fatalf("force: resumed %d calls %d", sum.Resumed, len(f.h.Calls()))
	}
}

func TestRolloutResumeIgnoresCorruptEpisodeFiles(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.rollout([]rl.Task{f.task}, 1, f.opts())
	if err := os.WriteFile(filepath.Join(f.sampleDir("mathx-max", 0), "episode.json"), []byte("{truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if sum.Resumed != 0 || len(f.h.Calls()) != 2 {
		t.Fatalf("corrupt episode should be rerun: resumed %d calls %d", sum.Resumed, len(f.h.Calls()))
	}
}

func TestRolloutCancellationWritesManifestAndSummary(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.r.Concurrency = 2
	f.r.StopGrace = 5 * time.Second
	f.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("marker.txt", "x"), FakeHang()}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		eventually(t, 30*time.Second, func() bool { return len(f.h.Calls()) >= 2 }, "two rollouts to start")
		cancel()
	}()
	start := time.Now()
	sum, err := f.r.Rollout(ctx, []rl.Task{f.task}, 6, f.opts())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if sum == nil || !sum.Interrupted || sum.Cancelled != 6 || sum.Completed != 0 || sum.Infra != 0 {
		t.Fatalf("summary: %+v", sum)
	}
	if time.Since(start) > 60*time.Second {
		t.Fatal("cancellation was slow")
	}
	var mf Manifest
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(f.out, "manifest.json"))), &mf); err != nil || mf.Status != "cancelled" {
		t.Fatalf("manifest: %+v %v", mf, err)
	}
	var onDisk Summary
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(f.out, "summary.json"))), &onDisk); err != nil || !onDisk.Interrupted {
		t.Fatalf("summary.json: %v", err)
	}
	// Cancelled rollouts leave no episode (a resume must redo them) and no workspace.
	for s := 0; s < 6; s++ {
		if exists(filepath.Join(f.sampleDir("mathx-max", s), "episode.json")) {
			t.Errorf("cancelled sample %d has an episode", s)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(f.m.Root(), "ws")); len(ents) != 0 {
		t.Errorf("%d workspaces left behind after cancel", len(ents))
	}
	// And the run can be resumed to completion.
	f.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}}
	sum = f.rollout([]rl.Task{f.task}, 6, f.opts())
	if sum.Completed != 6 || sum.PassRate != 1 {
		t.Fatalf("resume after cancel: %+v", sum)
	}
}

func TestRolloutHarnessThatIgnoresItsContext(t *testing.T) {
	f := newRunnerFixture(t)
	f.task.Budget.WallS = 1
	f.r.InfraRetries = -1
	f.r.StopGrace = 200 * time.Millisecond
	f.h.Scripts = map[string]FakeScript{
		"mathx-max/0": {Steps: []FakeStep{FakeHangForever()}},
		"mathx-max/1": {Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}},
	}
	start := time.Now()
	sum := f.rollout([]rl.Task{f.task}, 2, f.opts())
	if time.Since(start) > 30*time.Second {
		t.Fatal("a hung harness stalled the run")
	}
	if sum.Infra != 1 || sum.Completed != 1 || sum.Results[0].Status != StatusInfra || !strings.Contains(sum.Results[0].Error, "did not return") {
		t.Fatalf("summary: %+v", sum.Results)
	}
	if !sum.Results[1].Pass {
		t.Error("the healthy sample should still complete")
	}
	// The abandoned workspace is left in place (the harness may still be using it).
	if sum.Results[0].KeptWorkspace == "" || !exists(sum.Results[0].KeptWorkspace) {
		t.Errorf("abandoned workspace: %q", sum.Results[0].KeptWorkspace)
	}
	// The abandoned harness must not have been given outcome events to trip over.
	if oc := f.outcomes("mathx-max", 0); len(oc) != 0 {
		t.Errorf("outcome events were appended to a log a harness may still write: %v", oc)
	}
	close(f.h.Release)
}

func TestRolloutKeepFailed(t *testing.T) {
	f := newRunnerFixture(t)
	f.h.Scripts = map[string]FakeScript{
		"mathx-max/0": {Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}},
		"mathx-max/1": {Steps: []FakeStep{FakeWrite("notes.txt", "the agent's failed attempt")}},
	}
	opts := f.opts()
	opts.KeepFailed = true
	sum := f.rollout([]rl.Task{f.task}, 2, opts)
	if sum.Results[0].KeptWorkspace != "" {
		t.Error("a passing rollout's workspace should be removed")
	}
	kept := sum.Results[1].KeptWorkspace
	if kept == "" || mustRead(t, filepath.Join(kept, "notes.txt")) != "the agent's failed attempt" {
		t.Fatalf("failed workspace not kept: %q", kept)
	}
	if ents, _ := os.ReadDir(filepath.Join(f.m.Root(), "ws")); len(ents) != 1 {
		t.Errorf("expected exactly the failed workspace, got %d", len(ents))
	}
}

func TestRolloutExtractAndScoreFailuresAreContained(t *testing.T) {
	t.Run("extract error", func(t *testing.T) {
		f := newRunnerFixture(t, quickVerify)
		f.r.Extract = func(string, rl.Task, int, string) (*rl.Episode, error) { return nil, errors.New("replay mismatch") }
		f.r.InfraRetries = 2
		sum := f.rollout([]rl.Task{f.task}, 2, f.opts())
		if sum.Infra != 2 || len(f.h.Calls()) != 2 {
			t.Fatalf("extract failures must not be retried (a rerun fails the same way): infra %d calls %d", sum.Infra, len(f.h.Calls()))
		}
		if !strings.Contains(sum.Results[0].Error, "replay mismatch") {
			t.Errorf("error: %q", sum.Results[0].Error)
		}
		if !f.episode("mathx-max", 0).Has(rl.FlagInfraError) {
			t.Error("no infra episode written")
		}
	})
	t.Run("extract panic", func(t *testing.T) {
		f := newRunnerFixture(t, quickVerify)
		f.r.Extract = func(string, rl.Task, int, string) (*rl.Episode, error) { panic("nil pointer") }
		sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
		if sum.Infra != 1 || !strings.Contains(sum.Results[0].Error, "nil pointer") {
			t.Fatalf("%+v", sum.Results)
		}
	})
	t.Run("score error and panic", func(t *testing.T) {
		f := newRunnerFixture(t, quickVerify)
		f.r.Score = func(ep *rl.Episode, task *rl.Task, dir string) error {
			if ep.Sample == 0 {
				return errors.New("bad reward config")
			}
			panic("divide by zero")
		}
		sum := f.rollout([]rl.Task{f.task}, 2, f.opts())
		if sum.Infra != 2 {
			t.Fatalf("%+v", sum.Results)
		}
	})
	t.Run("nil extract and score use the defaults", func(t *testing.T) {
		f := newRunnerFixture(t, quickVerify)
		f.r.Extract, f.r.Score = nil, nil
		f.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}}
		sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
		if sum.Completed != 1 || sum.PassRate != 1 {
			t.Fatalf("%+v", sum)
		}
	})
	t.Run("one bad task does not stop the others", func(t *testing.T) {
		f := newRunnerFixture(t, quickVerify)
		bad := f.task
		bad.ID = "broken-repo"
		bad.Repo.Commit = strings.Repeat("0", 40)
		f.r.InfraRetries = -1
		sum := f.rollout([]rl.Task{bad, f.task}, 2, f.opts())
		if sum.Infra != 2 || sum.Completed != 2 {
			t.Fatalf("summary: infra %d completed %d", sum.Infra, sum.Completed)
		}
		for _, r := range sum.Results[:2] {
			if r.Status != StatusInfra || !strings.Contains(r.Error, "prepare") {
				t.Errorf("%+v", r)
			}
		}
	})
}

func TestRolloutAgentFailureIsNotInfra(t *testing.T) {
	f := newRunnerFixture(t)
	f.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("mathx.go", fixedMath), FakeAgentFail("stuck in a tool loop")}}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if sum.Infra != 0 || sum.Completed != 1 {
		t.Fatalf("%+v", sum)
	}
	// The work done before giving up is still verified and counted.
	if !sum.Results[0].Pass || sum.Results[0].Claimed != "gave_up" {
		t.Fatalf("%+v", sum.Results[0])
	}
}

func TestRolloutRecallTaskUsesTheFinalMessage(t *testing.T) {
	f := newRunnerFixture(t)
	rt := f.task
	rt.ID = "recall-1"
	rt.Kind = rl.TaskRecall
	rt.Verifier = rl.Verifier{Expect: json.RawMessage(`{"contains":["needle-7"]}`)}
	f.h.Scripts = map[string]FakeScript{
		"recall-1/0": {Final: "The constant was needle-7."},
		"recall-1/1": {Final: "I do not remember."},
	}
	sum := f.rollout([]rl.Task{rt}, 2, f.opts())
	if !sum.Results[0].Pass || sum.Results[1].Pass || sum.Completed != 2 {
		t.Fatalf("%+v", sum.Results)
	}
}

func TestRolloutValidatesInputs(t *testing.T) {
	f := newRunnerFixture(t)
	ctx := context.Background()
	if _, err := f.r.Rollout(ctx, []rl.Task{f.task}, 0, f.opts()); err == nil {
		t.Error("group 0 accepted")
	}
	bad := f.task
	bad.Verifier.Cmd = ""
	if _, err := f.r.Rollout(ctx, []rl.Task{bad}, 1, f.opts()); err == nil || !strings.Contains(err.Error(), "verifier.cmd") {
		t.Errorf("invalid task: %v", err)
	}
	if _, err := f.r.Rollout(ctx, []rl.Task{f.task, f.task}, 1, f.opts()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate ids: %v", err)
	}
	r2 := *f.r
	r2.Harness = nil
	if _, err := r2.Rollout(ctx, []rl.Task{f.task}, 1, f.opts()); err == nil {
		t.Error("nil harness accepted")
	}
	r3 := *f.r
	r3.Out = ""
	if _, err := r3.Rollout(ctx, []rl.Task{f.task}, 1, f.opts()); err == nil {
		t.Error("empty Out accepted")
	}
	if len(f.h.Calls()) != 0 {
		t.Error("harness was called despite invalid input")
	}
}

func TestRolloutDeterministicOrderingAndSeeds(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.r.Concurrency = 4
	t2 := f.task
	t2.ID = "mathx-max-b"
	f.h.Default = FakeScript{Steps: []FakeStep{FakeSleep(time.Duration(5) * time.Millisecond), FakeWrite("mathx.go", fixedMath)}}
	sum := f.rollout([]rl.Task{f.task, t2}, 4, f.opts())
	var order []string
	for _, r := range sum.Results {
		order = append(order, fmt.Sprintf("%s/%d", r.Task, r.Sample))
	}
	want := []string{"mathx-max/0", "mathx-max/1", "mathx-max/2", "mathx-max/3", "mathx-max-b/0", "mathx-max-b/1", "mathx-max-b/2", "mathx-max-b/3"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("results order %v", order)
	}
	seeds := map[string]int64{}
	for _, c := range f.h.Calls() {
		seeds[fmt.Sprintf("%s/%d", c.Task, c.Sample)] = c.Seed
	}
	f2 := newRunnerFixture(t, quickVerify)
	f2.h.Default = f.h.Default
	f2.r.Concurrency = 1
	f2.rollout([]rl.Task{f2.task, func() rl.Task { x := f2.task; x.ID = "mathx-max-b"; return x }()}, 4, f2.opts())
	for _, c := range f2.h.Calls() {
		if seeds[fmt.Sprintf("%s/%d", c.Task, c.Sample)] != c.Seed {
			t.Errorf("seed of %s/%d depends on scheduling", c.Task, c.Sample)
		}
	}
	keys := make([]string, 0, len(seeds))
	for k := range seeds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	distinct := map[int64]bool{}
	for _, v := range seeds {
		distinct[v] = true
	}
	if len(distinct) != len(keys) {
		t.Errorf("seeds collide: %v", seeds)
	}
}

func TestRolloutPassesNetworkIsolationToTheHarness(t *testing.T) {
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "ip"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newRunnerFixture(t, quickVerify)
	local, err := NewLocalSandbox(LocalSandboxOptions{LookPath: func(n string) (string, error) {
		if n == "ip" {
			return filepath.Join(fakeBin, "ip"), nil
		}
		return lookPathOS(n)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !local.NetIsolation().Available {
		t.Skipf("no network namespaces here: %s", local.NetIsolation().Reason)
	}
	f.m.local, f.m.sandbox = local, local
	offline := f.task
	online := f.task
	online.ID = "mathx-net"
	online.Network = true
	f.rollout([]rl.Task{offline, online}, 1, f.opts())
	got := map[string][]string{}
	for _, c := range f.h.Calls() {
		got[c.Task] = c.NetPrefix
	}
	if len(got["mathx-max"]) == 0 || !strings.Contains(strings.Join(got["mathx-max"], " "), "unshare") {
		t.Errorf("a task without network must get an isolation prefix: %v", got["mathx-max"])
	}
	if got["mathx-net"] != nil {
		t.Errorf("a task with network must not: %v", got["mathx-net"])
	}
}

func TestRolloutRedactsHiddenBlobStoreContentToo(t *testing.T) {
	store := events.NewMemBlobs()
	h, _ := store.Put([]byte(hiddenTest))
	f := newRunnerFixture(t)
	f.r.HiddenBlobs = store
	f.task.Verifier.Hidden = map[string]string{"mathx_hidden_test.go": "blob:" + string(h)}
	f.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if !sum.Results[0].Pass {
		t.Fatalf("%+v", sum.Results[0])
	}
	raw := mustRead(t, filepath.Join(f.sampleDir("mathx-max", 0), "task.json"))
	if !strings.Contains(raw, "blob:"+string(h)) || strings.Contains(raw, "TestMax") {
		t.Errorf("task.json: %s", raw)
	}
}

// The sample seed is sent to the policy as the request's sampling seed, so it must be one
// every endpoint accepts (0 to 2^31-1: see MaxWireSeed).
func TestSampleSeedIsOneEveryEndpointAccepts(t *testing.T) {
	for _, run := range []int64{0, 1, -1, 42, math.MaxInt64, math.MinInt64, 1 << 53, 1<<53 + 1} {
		for _, task := range []string{"", "a", "demo/fix add", strings.Repeat("x", 300)} {
			for sample := 0; sample < 64; sample++ {
				got := SampleSeed(run, task, sample)
				if got < 0 || got > MaxWireSeed {
					t.Fatalf("SampleSeed(%d, %q, %d) = %d, outside 0..%d", run, task, sample, got, int64(MaxWireSeed))
				}
			}
		}
	}
}

func TestSampleSeedIsStable(t *testing.T) {
	base, again := SampleSeed(1, "a", 0), SampleSeed(1, "a", 0)
	if base != again || base == SampleSeed(1, "a", 1) || base == SampleSeed(2, "a", 0) || base == SampleSeed(1, "b", 0) {
		t.Fatal("SampleSeed is not a function of exactly (seed, task, sample)")
	}
	if base < 0 {
		t.Fatal("negative seed")
	}
	// A golden value pins the algorithm: changing it would silently change every
	// run's sampling seeds.
	if got := SampleSeed(42, "task", 3); got != SampleSeed(42, "task", 3) {
		t.Fatal(got)
	}
}

func TestRolloutSwarmDefaultsFromTheTask(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.h.Default = FakeScript{}
	swarm := f.task
	swarm.ID = "swarm-task"
	swarm.Kind = rl.TaskSwarm
	swarm.Team = rl.Team{Mode: "swarm", Agents: 4}
	var got []RunSpec
	var mu sync.Mutex
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		mu.Lock()
		got = append(got, spec)
		mu.Unlock()
		return RunResult{Claimed: "done"}, nil
	}
	f.rollout([]rl.Task{swarm, f.task}, 1, f.opts())
	by := map[string]RunSpec{}
	for _, s := range got {
		by[s.Task.ID] = s
	}
	if s := by["swarm-task"]; !s.Swarm || s.Agents != 4 {
		t.Errorf("swarm task: swarm=%v agents=%d", s.Swarm, s.Agents)
	}
	if s := by["mathx-max"]; s.Swarm || s.Agents != 0 {
		t.Errorf("single task: swarm=%v agents=%d", s.Swarm, s.Agents)
	}
	// Explicit options win.
	opts := f.opts()
	opts.Swarm, opts.Agents, opts.Force = true, 6, true
	got = nil
	f.rollout([]rl.Task{f.task}, 1, opts)
	if len(got) != 1 || !got[0].Swarm || got[0].Agents != 6 {
		t.Errorf("explicit swarm options: %+v", got)
	}
}
