package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/adv"
	"github.com/reee344/sleipnir/internal/rl/env"
	"github.com/reee344/sleipnir/internal/rl/export"
	"github.com/reee344/sleipnir/internal/rl/harness"
	"github.com/reee344/sleipnir/internal/rl/redact"
	"github.com/reee344/sleipnir/internal/rl/reward"
	"github.com/reee344/sleipnir/internal/rl/traj"
)

// TestRolloutsToTrainingData is the whole RL loop on one task, with only the
// policy scripted: real workspaces, the real session as the agent, a clean
// checkout verifier, extraction, scoring, group advantages and the exporters.
// Three samples: two fix the bug, one claims to be done without fixing it.
func TestRolloutsToTrainingData(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	repo := newRepo(t)
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip(err)
	}
	task := rl.Task{
		ID: "demo-fix", Kind: rl.TaskFix,
		Repo:     rl.RepoSpec{Path: repo, Commit: strings.TrimSpace(string(head))},
		Prompt:   "Add returns the wrong result. Fix it and check with the tests.",
		Verifier: rl.Verifier{Cmd: "go test ./...", TimeoutS: 120, Protected: []string{"*_test.go"}},
		Budget:   rl.Budget{Steps: 20, Requests: 60, WallS: 240},
		Tags:     []string{"go"},
	}
	if err := env.ValidateTask(task); err != nil {
		t.Fatal(err)
	}

	// The policy: one session per sample, run one after another. Sample 1 does not fix anything.
	var session atomic.Int64
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		n := turns(c)
		if n == 0 {
			session.Add(1) // a new conversation starts: with Concurrency 1 its number is the sample index
		}
		id := session.Load() - 1
		if id == 1 { // the sample that lies
			if n == 0 {
				return mock.Reply{Text: "looking", ToolCalls: []mock.ToolCall{call("r", "read", map[string]any{"path": "demo.go"})}}
			}
			return mock.Reply{Text: "Fixed it, all tests pass."}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "read", ToolCalls: []mock.ToolCall{call("c1", "read", map[string]any{"path": "demo.go"})}}
		case 1:
			return mock.Reply{Text: "fix", ToolCalls: []mock.ToolCall{call("c2", "edit", map[string]any{"path": "demo.go", "old_string": "a - b", "new_string": "a + b"})}}
		case 2:
			return mock.Reply{Text: "test", ToolCalls: []mock.ToolCall{call("c3", "bash", map[string]any{"command": "go test ./..."})}}
		}
		return mock.Reply{Text: "Fixed Add; go test passes."}
	})

	work := t.TempDir()
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{
		Root: work, RepoBase: "/", DisableNetIsolation: true,
		SetEnv: map[string]string{"GOCACHE": goCache(t), "GOFLAGS": "-mod=mod", "GOTOOLCHAIN": "local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	pipe := &harness.Pipeline{Harness: rl.HarnessRef{Version: "test"}, Reward: reward.DefaultConfig()}
	out := filepath.Join(t.TempDir(), "run1")
	rn := &env.Runner{
		Harness: &harness.Harness{NewProvider: injected(pol.url)}, Extract: pipe.Extract, Score: pipe.Score,
		Workspaces: ws, Out: out, Concurrency: 1, InfraRetries: -1, MaxWall: 5 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sum, err := rn.Rollout(ctx, []rl.Task{task}, 3, env.RolloutOpts{
		RunID: "run1", Policy: env.PolicySpec{Model: "mock-1", Sampling: json.RawMessage(`{"temperature":1}`)},
		Capture: true, Seed: 11, KeepEpisodes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Completed != 3 || sum.Infra != 0 {
		t.Fatalf("summary: %+v (infra: %+v)", sum, sum.InfraErrors)
	}
	if sum.PassRate < 0.66 || sum.PassRate > 0.67 {
		t.Fatalf("two of three samples fix the bug, pass rate = %v", sum.PassRate)
	}

	// Episodes: verified in a clean checkout, scored, and honest about lying.
	eps := map[int]*rl.Episode{}
	var passes, falseDone int
	for _, r := range sum.Results {
		ep := r.Episode
		if ep == nil {
			t.Fatalf("no episode kept for %s/%d", r.Task, r.Sample)
		}
		eps[r.Sample] = ep
		if ep.Outcome.Verifier == nil {
			t.Fatalf("%s has no verifier verdict", ep.ID)
		}
		if ep.Outcome.Verifier.Pass {
			passes++
		} else if ep.Outcome.Claimed == "done" {
			falseDone++
			if ep.Reward.Components[reward.CompHonestDone] >= 0 {
				t.Errorf("claiming done and failing the verifier must cost reward: %v", ep.Reward.Components)
			}
		}
		if ep.Policy.Model != "mock-1" || len(ep.Agents) != 1 {
			t.Errorf("episode policy/agents: %+v / %d", ep.Policy, len(ep.Agents))
		}
		for _, st := range ep.Agents[0].Steps {
			if !st.Trainable {
				t.Errorf("a step by the policy must be trainable: %+v", st.ID)
			}
			if st.Tokens == nil {
				t.Errorf("token capture was on, step %s has no token trace", st.ID)
			}
		}
	}
	if passes != 2 || falseDone != 1 {
		t.Fatalf("passes=%d falseDone=%d", passes, falseDone)
	}
	best, worst := 0.0, 1e9
	for _, ep := range eps {
		best, worst = max(best, ep.Reward.Total), min(worst, ep.Reward.Total)
	}
	if best <= worst {
		t.Errorf("rewards must separate the fixing samples from the lying one: best %v worst %v", best, worst)
	}

	// Every recorded prompt replays to the hash the endpoint was sent.
	var srcs []export.Source
	for s := 0; s < 3; s++ {
		dir := filepath.Join(out, "demo-fix", itoa(s))
		run, err := traj.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if mm := run.Verify(); len(mm) > 0 {
			t.Fatalf("sample %d does not replay: %+v", s, mm)
		}
		srcs = append(srcs, export.Source{Episode: eps[s], Prompts: traj.Resolver{Run: run}})
	}

	// Export: GRPO advantages separate the samples, token records pack a segment.
	o := export.DefaultOptions(export.FormatSteps)
	spec, _ := adv.SpecFor("grpo")
	o.Advantage = adv.GRPOFunc(spec)
	o.Redactor = redact.New(redact.Config{})
	var steps bytes.Buffer
	st, err := export.Export(&steps, srcs, o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Records == 0 || st.Episodes != 3 {
		t.Fatalf("steps export: %+v", st)
	}
	var sawPos, sawNeg bool
	for _, ln := range strings.Split(strings.TrimSpace(steps.String()), "\n") {
		var rec struct {
			Advantage *float64 `json:"advantage"`
			Prompt    []any    `json:"prompt"`
			Tools     []any    `json:"tools"`
		}
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Advantage == nil || len(rec.Prompt) == 0 || len(rec.Tools) == 0 {
			t.Fatalf("a step record lacks its advantage, prompt or tools: %s", ln[:min(len(ln), 300)])
		}
		sawPos = sawPos || *rec.Advantage > 0
		sawNeg = sawNeg || *rec.Advantage < 0
	}
	if !sawPos || !sawNeg {
		t.Errorf("GRPO advantages must be positive for the fixing samples and negative for the lying one (pos=%v neg=%v)", sawPos, sawNeg)
	}

	o = export.DefaultOptions(export.FormatTokens)
	o.Advantage = adv.GRPOFunc(spec)
	o.PackSegments = true
	var toks bytes.Buffer
	if st, err = export.Export(&toks, srcs, o); err != nil {
		t.Fatal(err)
	}
	if st.Records == 0 {
		t.Fatalf("tokens export produced nothing: %+v", st)
	}
	var tok struct {
		PromptIDs    []int     `json:"prompt_ids"`
		ResponseIDs  []int     `json:"response_ids"`
		ResponseMask []int     `json:"response_mask"`
		OldLogprobs  []float64 `json:"old_logprobs"`
	}
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(toks.String()), "\n")[0]), &tok); err != nil {
		t.Fatal(err)
	}
	if len(tok.PromptIDs) == 0 || len(tok.ResponseIDs) != len(tok.ResponseMask) {
		t.Fatalf("token record: %d prompt ids, %d response ids, %d mask", len(tok.PromptIDs), len(tok.ResponseIDs), len(tok.ResponseMask))
	}

	// The run directory is complete and the log is the harness's own.
	for _, f := range []string{"manifest.json", "summary.json", "demo-fix/0/episode.json", "demo-fix/0/events.jsonl", "demo-fix/0/task.json", "demo-fix/0/diff.patch"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("run directory lacks %s: %v", f, err)
		}
	}
	var sawOutcome bool
	_ = events.Scan(filepath.Join(out, "demo-fix", "0", "events.jsonl"), func(e events.Event) error {
		if e.Type == "outcome" {
			sawOutcome = true
		}
		return nil
	})
	if !sawOutcome {
		t.Error("the runner's outcome events are missing from the agent's log")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
