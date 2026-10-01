package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/adv"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
	"github.com/anemos-labs/sleipnir/internal/rl/harness"
	"github.com/anemos-labs/sleipnir/internal/rl/recall"
	"github.com/anemos-labs/sleipnir/internal/rl/redact"
	"github.com/anemos-labs/sleipnir/internal/rl/reward"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
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

// A rollout that the wall clock ends is a budget episode. It did not say it was done: it is not counted among the false claims
// (the FALSEDONE column of a report) and its reward does not carry the penalty for them. On the first benchmark run the harness
// answered "done" for every run the clock cut off, 51 of 349, and both were wrong for all of them.
func TestARolloutThatTheClockEndsIsABudgetEpisodeAndNotAFalseDone(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	repo := newRepo(t)
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip(err)
	}
	task := rl.Task{
		ID: "demo-slow", Kind: rl.TaskFix,
		Repo:     rl.RepoSpec{Path: repo, Commit: strings.TrimSpace(string(head))},
		Prompt:   "Add returns the wrong result. Fix it and check with the tests.",
		Verifier: rl.Verifier{Cmd: "go test ./...", TimeoutS: 120, Protected: []string{"*_test.go"}},
		Budget:   rl.Budget{Steps: 10000, Requests: 10000, WallS: 4}, // the clock is the only limit that can end this run
	}
	if err := env.ValidateTask(task); err != nil {
		t.Fatal(err)
	}
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		time.Sleep(50 * time.Millisecond)
		return mock.Reply{Text: "still working", ToolCalls: []mock.ToolCall{call("c"+itoa(turns(c)), "bash", map[string]any{"command": "echo still working"})}}
	})
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{
		Root: t.TempDir(), RepoBase: "/", DisableNetIsolation: true,
		SetEnv: map[string]string{"GOCACHE": goCache(t), "GOFLAGS": "-mod=mod", "GOTOOLCHAIN": "local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	pipe := &harness.Pipeline{Harness: rl.HarnessRef{Version: "test"}, Reward: reward.DefaultConfig()}
	rn := &env.Runner{
		Harness: &harness.Harness{NewProvider: injected(pol.url)}, Extract: pipe.Extract, Score: pipe.Score,
		Workspaces: ws, Out: filepath.Join(t.TempDir(), "clock"), Concurrency: 1, InfraRetries: -1, MaxWall: 5 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
	defer cancel()
	sum, err := rn.Rollout(ctx, []rl.Task{task}, 1, env.RolloutOpts{
		RunID: "clock", Policy: env.PolicySpec{Model: "mock-1"}, Seed: 5, KeepEpisodes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Completed != 1 || len(sum.Results) != 1 || sum.Results[0].Episode == nil {
		t.Fatalf("the rollout did not complete: %+v (infra: %+v)", sum, sum.InfraErrors)
	}
	ep := sum.Results[0].Episode
	if ep.Outcome.Claimed != "budget" {
		t.Errorf("claimed %q: a run the clock cut off claimed nothing, and the episode says the budget ended it", ep.Outcome.Claimed)
	}
	if !slices.Contains(ep.Flags, rl.FlagBudgetExceeded) {
		t.Errorf("flags %v lack %s", ep.Flags, rl.FlagBudgetExceeded)
	}
	if c := ep.Reward.Components[reward.CompHonestDone]; c < 0 {
		t.Errorf("a run that never said it was done carries the false-claim penalty %v: %v", c, ep.Reward.Components)
	}
}

// A run whose every response the output limit cut off never said it was done: it gave up, or rather the model could not finish a
// sentence. The harness answered "done" for it (the run ended without an error), and the report counted a false claim. The first
// benchmark had one with a 16,000-token generation that went on for three minutes.
func TestARolloutWhoseResponsesAreCutOffAtTheOutputLimitGaveUpAndDidNotClaimDone(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	repo := newRepo(t)
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip(err)
	}
	task := rl.Task{
		ID: "demo-cutoff", Kind: rl.TaskFix,
		Repo:     rl.RepoSpec{Path: repo, Commit: strings.TrimSpace(string(head))},
		Prompt:   "Add returns the wrong result. Fix it and check with the tests.",
		Verifier: rl.Verifier{Cmd: "go test ./...", TimeoutS: 120, Protected: []string{"*_test.go"}},
		Budget:   rl.Budget{Steps: 40, Requests: 80, WallS: 120},
	}
	if err := env.ValidateTask(task); err != nil {
		t.Fatal(err)
	}
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		return mock.Reply{Text: "So the fix is, and then the fix is, and then the fix is", Finish: "length"}
	})
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{
		Root: t.TempDir(), RepoBase: "/", DisableNetIsolation: true,
		SetEnv: map[string]string{"GOCACHE": goCache(t), "GOFLAGS": "-mod=mod", "GOTOOLCHAIN": "local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	pipe := &harness.Pipeline{Harness: rl.HarnessRef{Version: "test"}, Reward: reward.DefaultConfig()}
	out := filepath.Join(t.TempDir(), "cutoff")
	rn := &env.Runner{
		Harness: &harness.Harness{NewProvider: injected(pol.url)}, Extract: pipe.Extract, Score: pipe.Score,
		Workspaces: ws, Out: out, Concurrency: 1, InfraRetries: -1, MaxWall: 5 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
	defer cancel()
	sum, err := rn.Rollout(ctx, []rl.Task{task}, 1, env.RolloutOpts{
		RunID: "cutoff", Policy: env.PolicySpec{Model: "mock-1"}, Seed: 6, KeepEpisodes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Completed != 1 || len(sum.Results) != 1 || sum.Results[0].Episode == nil {
		t.Fatalf("the rollout did not complete: %+v (infra: %+v)", sum, sum.InfraErrors)
	}
	ep := sum.Results[0].Episode
	if ep.Outcome.Claimed != "gave_up" {
		t.Errorf("claimed %q: the run ended because the model was cut off at the output limit again and again, which is not saying it was done", ep.Outcome.Claimed)
	}
	if c := ep.Reward.Components[reward.CompHonestDone]; c < 0 {
		t.Errorf("the false-claim penalty %v for a run that did not claim: %v", c, ep.Reward.Components)
	}
	// The episode's claim is read back from the trajectory, which knew the stop reason all along. The outcome event of the run is
	// what the harness itself said when the run ended, and it said "done" for every run that ended without an error.
	var claimed []string
	err = events.Scan(filepath.Join(out, "demo-cutoff", "0", "events.jsonl"), func(e events.Event) error {
		if e.Type == events.TypeOutcome {
			var m struct {
				Claimed string `json:"claimed"`
			}
			if err := json.Unmarshal(e.Data, &m); err != nil {
				return err
			}
			claimed = append(claimed, m.Claimed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0] != "gave_up" {
		t.Errorf("the outcome event of the run claims %q, want exactly one, gave_up", claimed)
	}
}

// TestRecallTaskIsJudgedOnTheFinalMessage runs a generated memory task: the
// agent reads a fact, reads other files, and must state the fact. The verdict
// comes from an exact match on the final message, with no command to run.
func TestRecallTaskIsJudgedOnTheFinalMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	dir := t.TempDir()
	for i := 1; i <= 8; i++ {
		p := filepath.Join(dir, fmt.Sprintf("pkg%02d", i), fmt.Sprintf("pkg%02d.go", i))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		body := fmt.Sprintf("// Package pkg%02d is one of many.\npackage pkg%02d\n\nconst Retries%02d = %d\n", i, i, i, 4000+i*37)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/many\n\ngo 1.24\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git: %v %s", err, out)
		}
	}
	g, err := env.NewGit(env.GitOptions{Scratch: t.TempDir()})
	if err != nil {
		t.Skip(err)
	}
	tasks, err := recall.Generate(context.Background(), dir, recall.Options{Git: g, Max: 1, Files: 3, Window: 6000})
	if err != nil {
		t.Fatal(err)
	}
	task := tasks[0]

	landmarkRe := regexp.MustCompile("Read `([^`]+)`")
	fillerRe := regexp.MustCompile(`(?m)^ {3}- (\S+)$`)
	valueRe := regexp.MustCompile(`= (\d+)`)

	// The scripted policy keeps its own notes, the way a model carries what it has
	// understood: compacting its thread does not make it forget which step it is on.
	type plan struct {
		landmark, value string
		fillers         []string
		step            int
	}
	var mu sync.Mutex
	var plans []*plan
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.LastUser(), "<compactor-task>") {
			return mock.Reply{Text: "I will not produce JSON today."} // the harness falls back to a mechanical compaction
		}
		mu.Lock()
		defer mu.Unlock()
		if turns(c) == 0 {
			plans = append(plans, &plan{})
		}
		pl := plans[len(plans)-1]
		wrong := len(plans) == 2 // the second sample misremembers
		for _, m := range c.Messages {
			switch {
			case m.Role == "user" && pl.landmark == "" && strings.Contains(m.Content, "memory exercise"):
				pl.landmark = landmarkRe.FindStringSubmatch(m.Content)[1]
				for _, f := range fillerRe.FindAllStringSubmatch(m.Content, -1) {
					pl.fillers = append(pl.fillers, f[1])
				}
			case m.Role == "tool" && pl.value == "" && pl.landmark != "" && strings.Contains(m.Content, strings.TrimSuffix(filepath.Base(pl.landmark), ".go")):
				if v := valueRe.FindStringSubmatch(m.Content); v != nil {
					pl.value = v[1]
				}
			}
		}
		step := pl.step
		pl.step++
		switch {
		case step == 0:
			return mock.Reply{Text: "reading the landmark", ToolCalls: []mock.ToolCall{call("r0", "read", map[string]any{"path": pl.landmark})}}
		case step <= len(pl.fillers):
			return mock.Reply{Text: "next file", ToolCalls: []mock.ToolCall{call(fmt.Sprintf("r%d", step), "read", map[string]any{"path": pl.fillers[step-1]})}}
		}
		answer := pl.value
		if wrong || answer == "" {
			answer = "1"
		}
		return mock.Reply{Text: "Covered every file.\nANSWER: " + answer}
	})

	ws, err := env.NewWorkspaces(env.WorkspaceOptions{Root: t.TempDir(), RepoBase: "/", DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	pipe := &harness.Pipeline{Harness: rl.HarnessRef{Version: "test"}, Reward: reward.DefaultConfig()}
	rn := &env.Runner{Harness: &harness.Harness{NewProvider: injected(pol.url)}, Extract: pipe.Extract, Score: pipe.Score,
		Workspaces: ws, Out: filepath.Join(t.TempDir(), "run"), Concurrency: 1, InfraRetries: -1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sum, err := rn.Rollout(ctx, []rl.Task{task}, 2, env.RolloutOpts{RunID: "r", Policy: env.PolicySpec{Model: "mock-1"}, KeepEpisodes: true})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Completed != 2 {
		t.Fatalf("%+v", sum)
	}
	var good, bad *rl.Episode
	for _, r := range sum.Results {
		if r.Episode.Outcome.Verifier != nil && r.Episode.Outcome.Verifier.Pass {
			good = r.Episode
		} else {
			bad = r.Episode
		}
	}
	if good == nil || bad == nil {
		t.Fatalf("one sample answers correctly and one does not: pass rate %v", sum.PassRate)
	}
	if bad.Reward.Total >= good.Reward.Total {
		t.Errorf("a wrong answer must earn less: %v vs %v", bad.Reward.Total, good.Reward.Total)
	}
	if bad.Outcome.Claimed != "done" || bad.Reward.Components[reward.CompHonestDone] >= 0 {
		t.Errorf("claiming done with a wrong answer is a false claim: %+v", bad.Reward)
	}
}
