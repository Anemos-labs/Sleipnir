package harness_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/chaos"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/harness"
)

// downForAWhile is an endpoint that answers nothing (it resets every connection) for d from its first request, and serves every
// request after that: a gateway that went away and came back.
func downForAWhile(d time.Duration) chaos.Plan {
	var mu sync.Mutex
	var until time.Time
	return chaos.PlanFunc(func(int, *http.Request) chaos.Fault {
		mu.Lock()
		defer mu.Unlock()
		if until.IsZero() {
			until = time.Now().Add(d)
		}
		if time.Now().Before(until) {
			return chaos.Fault{Kind: chaos.Reset}
		}
		return chaos.Fault{}
	})
}

// fixer is a policy that reads demo.go, fixes Add and says so.
func fixer(c *mock.Call) mock.Reply {
	switch turns(c) {
	case 0:
		return mock.Reply{Text: "read", ToolCalls: []mock.ToolCall{call("c1", "read", map[string]any{"path": "demo.go"})}}
	case 1:
		return mock.Reply{Text: "fix", ToolCalls: []mock.ToolCall{call("c2", "edit", map[string]any{"path": "demo.go", "old_string": "a - b", "new_string": "a + b"})}}
	}
	return mock.Reply{Text: "Fixed Add."}
}

// outageRun is a runner over the real harness, with a policy endpoint that plan breaks, and the task it runs: fix Add, checked
// by a grep so that only the agent's part takes time.
func outageRun(t *testing.T, plan chaos.Plan, mut func(*env.Runner)) (*env.Runner, rl.Task) {
	t.Helper()
	agent.RetryBase = time.Millisecond
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, fixer)
	ts := httptest.NewServer(chaos.New(srv.Handler(), plan))
	t.Cleanup(ts.Close)

	repo := newRepo(t)
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip(err)
	}
	task := rl.Task{
		ID: "outage-fix", Kind: rl.TaskFix,
		Repo:     rl.RepoSpec{Path: repo, Commit: strings.TrimSpace(string(head))},
		Prompt:   "Add returns the wrong result. Fix it.",
		Verifier: rl.Verifier{Cmd: "grep -q 'a + b' demo.go", TimeoutS: 60},
		Budget:   rl.Budget{Steps: 20, Requests: 60},
	}
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{Root: t.TempDir(), RepoBase: "/", DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ws.Close)
	rn := &env.Runner{
		Harness: &harness.Harness{NewProvider: injected(ts.URL)}, Workspaces: ws, Out: filepath.Join(t.TempDir(), "run"),
		Concurrency: 1, InfraRetries: -1,
	}
	mut(rn)
	return rn, task
}

// On 2026-10-11 the gateway timed out and refused connections for about twenty minutes and then eighty, and every rollout that
// was running then ended as an infra failure. Now the agent waits the outage out and the rollout's wall clock stops meanwhile: an
// outage longer than the whole wall budget leaves the rollout to finish its work, and the record says which part was the outage.
//
// The outage is long on the runner's clock, not in real time: the clock jumps past the budget while the endpoint is down. Waiting a
// real outage longer than a real budget made the budget the test's margin for its own work, and a CI runner starved by the
// packages beside it spent 4.3 s of a 2 s budget on two requests and an edit.
func TestARolloutSitsOutAnOutageLongerThanItsWallBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	const budget = 30 * time.Second
	var skew atomic.Int64 // how far the runner's clock is ahead of real time
	// Four requests go unanswered. The third comes after a failure has stopped the clock (the agent retries once the harness has
	// seen one), and moves the clock past the whole budget: were it counted as active time, the run would end on its budget.
	down := chaos.PlanFunc(func(n int, _ *http.Request) chaos.Fault {
		if n == 3 {
			skew.Add(int64(budget + 3*time.Second))
		}
		if n <= 4 {
			return chaos.Fault{Kind: chaos.Reset}
		}
		return chaos.Fault{}
	})
	rn, task := outageRun(t, down, func(r *env.Runner) {
		r.MaxWall = budget
		r.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sum, err := rn.Rollout(ctx, []rl.Task{task}, 1, env.RolloutOpts{RunID: "outage", Policy: env.PolicySpec{Model: "mock-1"}})
	if err != nil {
		t.Fatal(err)
	}
	r := sum.Results[0]
	if sum.Completed != 1 || sum.Infra != 0 || r.Status != env.StatusOK || !r.Pass || r.Claimed != "done" {
		t.Fatalf("the rollout did not survive the outage: %+v (infra: %+v)", r, sum.InfraErrors)
	}
	if b := budget.Milliseconds(); r.WallMs < b || r.PausedMs < b || r.ActiveWallMs >= b {
		t.Errorf("wall %dms active %dms paused %dms: the run outlasted its %s budget and the outage is what was paused", r.WallMs, r.ActiveWallMs, r.PausedMs, budget)
	}

	// The log says when the endpoint went down, how (nothing answered), and when it came back.
	var phases []string
	for _, e := range readEvents(t, filepath.Join(rn.Out, task.ID, "0")) {
		if e.Type != events.TypeRLOutage {
			continue
		}
		var d struct {
			Phase, Class, Kind string
			OutageMs           int64 `json:"outage_ms"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		phases = append(phases, d.Phase+"/"+d.Class+"/"+d.Kind)
		if d.Phase == "end" && d.OutageMs < budget.Milliseconds() {
			t.Errorf("the outage lasted %dms by the log", d.OutageMs)
		}
	}
	if strings.Join(phases, " ") != "start/unanswered/network end//" {
		t.Errorf("rl.outage events: %q", phases)
	}
}

// An endpoint that stays down past --max-outage-pause ends the rollout as an infrastructure failure, without a retry: the agent is
// patient for far longer, but the rollout is not.
func TestARolloutWhoseEndpointStaysDownEndsAsInfra(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	rn, task := outageRun(t, downForAWhile(time.Hour), func(r *env.Runner) { r.MaxOutagePause = 300 * time.Millisecond })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	sum, err := rn.Rollout(ctx, []rl.Task{task}, 1, env.RolloutOpts{RunID: "down", Policy: env.PolicySpec{Model: "mock-1"}})
	if err != nil {
		t.Fatal(err)
	}
	r := sum.Results[0]
	if sum.Infra != 1 || r.Status != env.StatusInfra || r.Attempts != 1 || !strings.Contains(r.Error, "--max-outage-pause") {
		t.Fatalf("%+v", r)
	}
	if r.PausedMs < 300 {
		t.Errorf("paused %dms, want at least the limit", r.PausedMs)
	}
	if d := time.Since(start); d > time.Minute {
		t.Errorf("the rollout took %v to give up on a 300ms limit", d)
	}
}
