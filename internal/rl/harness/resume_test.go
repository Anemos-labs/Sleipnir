package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/harness"
	"github.com/anemos-labs/sleipnir/internal/rl/reward"
)

// A rollout that is interrupted after a savepoint resumes from it on the next
// run into the same directory instead of starting over: the work tree comes
// back as the savepoint captured it, the agent continues its own conversation,
// the cut-off part of the log is kept aside, and the ledger counts every
// response exactly once.
func TestAnInterruptedRolloutResumesFromItsSavepoint(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	agent.RetryBase = time.Millisecond
	var (
		mu      sync.Mutex
		prompts []string
	)
	var stall atomic.Bool
	stall.Store(true)
	policy := func(c *mock.Call) mock.Reply {
		var all []string
		for _, m := range c.Messages {
			all = append(all, m.Content)
		}
		mu.Lock()
		prompts = append(prompts, strings.Join(all, "\n"))
		mu.Unlock()
		switch turns(c) {
		case 0:
			return mock.Reply{Text: "note", ToolCalls: []mock.ToolCall{call("c1", "bash", map[string]any{"command": "echo started > progress.txt"})}}
		case 1:
			// The first run is interrupted while it waits for this answer.
			for i := 0; i < 400 && stall.Load(); i++ {
				time.Sleep(10 * time.Millisecond)
			}
			return mock.Reply{Text: "read", ToolCalls: []mock.ToolCall{call("c2", "read", map[string]any{"path": "demo.go"})}}
		case 2:
			return mock.Reply{Text: "fix", ToolCalls: []mock.ToolCall{call("c3", "edit", map[string]any{"path": "demo.go", "old_string": "a - b", "new_string": "a + b"})}}
		}
		return mock.Reply{Text: "Fixed Add."}
	}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, policy)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	repo := newRepo(t)
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip(err)
	}
	task := rl.Task{
		ID: "resume-fix", Kind: rl.TaskFix,
		Repo:     rl.RepoSpec{Path: repo, Commit: strings.TrimSpace(string(head))},
		Prompt:   "Add returns the wrong result. Fix it.",
		Verifier: rl.Verifier{Cmd: "grep -q 'a + b' demo.go && grep -q started progress.txt", TimeoutS: 60},
		Budget:   rl.Budget{Steps: 20, Requests: 60},
	}
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{Root: t.TempDir(), RepoBase: "/", DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ws.Close)
	pipe := &harness.Pipeline{Harness: rl.HarnessRef{Version: "test"}, Reward: reward.DefaultConfig()}
	var resumed []env.Progress
	rn := &env.Runner{
		Harness: &harness.Harness{NewProvider: injected(ts.URL)}, Workspaces: ws, Out: filepath.Join(t.TempDir(), "run"),
		Extract: pipe.Extract, Score: pipe.Score, Concurrency: 1, InfraRetries: -1, SavepointEvery: 50 * time.Millisecond,
		Progress: func(p env.Progress) {
			if p.Type == "rollout.resume" {
				resumed = append(resumed, p)
			}
		},
	}
	opts := env.RolloutOpts{RunID: "resume", Policy: env.PolicySpec{Model: "mock-1"}}
	sample := filepath.Join(rn.Out, task.ID, "0")

	// The first run takes savepoints while the agent waits for its second answer, and is stopped there.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *env.Summary, 1)
	go func() {
		sum, err := rn.Rollout(ctx, []rl.Task{task}, 1, opts)
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		done <- sum
	}()
	// Savepoints are taken from the start, so on a slow machine the first ones come before the first answer. The
	// agent asking for its second answer means its first tool batch is in its thread; a savepoint that began before
	// that may still be finishing, so the run is stopped after two more.
	deadline := time.Now().Add(time.Minute)
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("the agent never asked for its second answer", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts) >= 2
	})
	after := savepoints(sample)
	waitFor("no savepoint was recorded while the agent waited", func() bool { return savepoints(sample) >= after+2 })
	cancel()
	if sum := <-done; sum == nil || sum.Results[0].Status != env.StatusCancelled {
		t.Fatalf("the first run was not interrupted: %+v", sum)
	}
	stall.Store(false)

	// The second run resumes.
	mu.Lock()
	before := len(prompts)
	mu.Unlock()
	sum, err := rn.Rollout(context.Background(), []rl.Task{task}, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	r := sum.Results[0]
	if r.Status != env.StatusOK || !r.Pass || r.Claimed != "done" {
		t.Fatalf("the resumed rollout did not finish: %+v (infra %+v)", r, sum.InfraErrors)
	}
	if len(resumed) != 1 || resumed[0].Savepoint < 2 {
		t.Errorf("rollout.resume events: %+v", resumed)
	}
	mu.Lock()
	first := prompts[before]
	mu.Unlock()
	if !strings.Contains(first, env.ResumeNote) || !strings.Contains(first, "[exit code 0]") || !strings.Contains(first, "Fix it.") {
		t.Errorf("the resumed agent did not continue its conversation:\n%s", first)
	}

	// The cut-off tail is kept aside, the log says the session was resumed, and the episode is whole.
	if _, err := os.Stat(filepath.Join(sample, "events.rewound.1.jsonl")); err != nil {
		t.Errorf("the cut-off part of the log was not kept: %v", err)
	}
	var starts, restores int
	for _, e := range readEvents(t, sample) {
		switch e.Type {
		case events.TypeSessionStart:
			starts++
		case events.TypeAgentRestore:
			restores++
		}
	}
	if starts != 2 || restores != 1 {
		t.Errorf("session.start %d, agent.restore %d: want 2 and 1", starts, restores)
	}
	var ep rl.Episode
	b, err := os.ReadFile(filepath.Join(sample, "episode.json"))
	if err != nil || json.Unmarshal(b, &ep) != nil {
		t.Fatalf("episode: %v", err)
	}
	if ep.Has(rl.FlagInfraError) || len(ep.Agents) != 1 || len(ep.Agents[0].Steps) < 4 {
		t.Fatalf("episode: flags %v, agents %+v", ep.Flags, ep.Agents)
	}
	// The interrupted request has no step, and the steps after the resume are a segment of their own.
	segs := ep.Agents[0].Segments
	if len(segs) != 2 || segs[1].Reason != "resume" || segs[1].From != 1 {
		t.Errorf("segments %+v: want the start and a resume from the second step", segs)
	}

	// Every response is in the ledger once: those in the final log and those in the cut-off tail.
	inLogs := responses(t, filepath.Join(sample, "events.jsonl")) + responses(t, filepath.Join(sample, "events.rewound.1.jsonl"))
	inLedger := 0
	lb, err := os.ReadFile(filepath.Join(rn.Out, env.LedgerFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range strings.Split(strings.TrimSpace(string(lb)), "\n") {
		var e env.LedgerEntry
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatal(err)
		}
		inLedger += e.Requests
	}
	if inLogs == 0 || inLedger != inLogs {
		t.Errorf("the ledger counts %d responses, the logs hold %d", inLedger, inLogs)
	}
}

// savepoints counts the records of a sample's savepoint index.
func savepoints(sample string) int {
	b, err := os.ReadFile(filepath.Join(sample, "savepoints", "index.jsonl"))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// responses counts the model responses in a log.
func responses(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), `"type":"`+events.TypeModelResponse+`"`)
}

// A team rollout interrupted while its manager waits for a worker resumes with
// the manager's own conversation, its wait recorded as interrupted, the
// worker's task back on the board and the worker's changes in the workspace;
// the manager starts a worker again and the team finishes.
func TestAnInterruptedTeamRolloutResumes(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	agent.RetryBase = time.Millisecond
	var stall atomic.Bool
	stall.Store(true)
	var mu sync.Mutex
	var managerAfter []string // the manager's prompts after the resume
	policy := func(c *mock.Call) mock.Reply {
		var role, tid string
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].Role != "user" {
				continue
			}
			if m := teamWho.FindStringSubmatch(c.Messages[i].Content); m != nil && role == "" {
				role = m[2]
			}
			if m := teamTask.FindStringSubmatch(c.Messages[i].Content); m != nil && tid == "" {
				tid = m[1]
			}
		}
		n := turns(c)
		if role == "manager" {
			var all []string
			for _, m := range c.Messages {
				all = append(all, m.Content)
			}
			prompt := strings.Join(all, "\n")
			resumed := strings.Contains(prompt, env.ResumeNoteTeam)
			if resumed {
				mu.Lock()
				managerAfter = append(managerAfter, prompt)
				mu.Unlock()
			}
			switch {
			case n == 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("m1", "task", map[string]any{"action": "create", "title": "Fix Add", "role": "backend", "description": "Add returns a-b: make it return a+b", "files": []string{"demo.go"}}),
					call("m2", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("m3", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 120}),
				}}
			case n == 1 && resumed:
				return mock.Reply{Text: "again", ToolCalls: []mock.ToolCall{
					call("m4", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("m5", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 120}),
				}}
			case n == 2:
				return mock.Reply{Text: "review", ToolCalls: []mock.ToolCall{call("m6", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "Add is fixed by T1."}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "note", ToolCalls: []mock.ToolCall{call("w0", "bash", map[string]any{"command": "echo started >> progress.txt"})}}
		case 1:
			for i := 0; i < 400 && stall.Load(); i++ {
				time.Sleep(10 * time.Millisecond)
			}
			return mock.Reply{Text: "read", ToolCalls: []mock.ToolCall{call("w1", "read", map[string]any{"path": "demo.go"})}}
		case 2:
			return mock.Reply{Text: "fix", ToolCalls: []mock.ToolCall{call("w2", "edit", map[string]any{"path": "demo.go", "old_string": "a - b", "new_string": "a + b"})}}
		case 3:
			return mock.Reply{Text: "done", ToolCalls: []mock.ToolCall{call("w3", "task", map[string]any{"action": "done", "id": tid, "text": "Add returns a+b"})}}
		}
		return mock.Reply{Text: "T1 is done."}
	}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, policy)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	repo := newRepo(t)
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip(err)
	}
	task := rl.Task{
		ID: "team-resume", Kind: rl.TaskSwarm,
		Repo:     rl.RepoSpec{Path: repo, Commit: strings.TrimSpace(string(head))},
		Prompt:   "Add returns the wrong result. Fix it with a team.",
		Team:     rl.Team{Mode: "swarm", Agents: 2, Roles: []string{"backend"}},
		Verifier: rl.Verifier{Cmd: "grep -q 'a + b' demo.go && grep -q started progress.txt", TimeoutS: 60},
		Budget:   rl.Budget{Steps: 40, Requests: 120},
	}
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{Root: t.TempDir(), RepoBase: "/", DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ws.Close)
	pipe := &harness.Pipeline{Harness: rl.HarnessRef{Version: "test"}, Reward: reward.DefaultConfig()}
	rn := &env.Runner{
		Harness: &harness.Harness{NewProvider: injected(ts.URL)}, Workspaces: ws, Out: filepath.Join(t.TempDir(), "run"),
		Extract: pipe.Extract, Score: pipe.Score, Concurrency: 1, InfraRetries: -1, SavepointEvery: 50 * time.Millisecond,
	}
	opts := env.RolloutOpts{RunID: "team-resume", Policy: env.PolicySpec{Model: "mock-1"}}
	sample := filepath.Join(rn.Out, task.ID, "0")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := rn.Rollout(ctx, []rl.Task{task}, 1, opts); err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(time.Minute)
	for savepoints(sample) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no savepoint was recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	stall.Store(false)

	sum, err := rn.Rollout(context.Background(), []rl.Task{task}, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	if r := sum.Results[0]; r.Status != env.StatusOK || !r.Pass {
		t.Fatalf("the resumed team did not finish: %+v (infra %+v)", r, sum.InfraErrors)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(managerAfter) == 0 {
		t.Fatal("the manager was not told it was resumed")
	}
	if first := managerAfter[0]; !strings.Contains(first, agent.InterruptedCall) || !strings.Contains(first, "Fix it with a team.") {
		t.Errorf("the resumed manager lost its conversation or its interrupted wait:\n%s", first)
	}
	// The worker started after the resume has an id of its own: reusing one from before the interruption would give two
	// agents' requests the same ids in one log, and the episode would not replay.
	spawned := map[string]int{}
	for _, e := range readEvents(t, sample) {
		if e.Type == events.TypeAgentSpawn {
			var d struct{ ID string }
			_ = json.Unmarshal(e.Data, &d)
			if d.ID != "mgr" { // the manager is restored, not spawned anew: its spawn event repeats
				spawned[d.ID]++
			}
		}
	}
	for id, n := range spawned {
		if n > 1 {
			t.Errorf("agent id %s was spawned %d times", id, n)
		}
	}
	var ep rl.Episode
	if b, err := os.ReadFile(filepath.Join(sample, "episode.json")); err != nil || json.Unmarshal(b, &ep) != nil {
		t.Fatalf("episode: %v", err)
	}
	if ep.Has(rl.FlagReplayMismatch) {
		t.Errorf("the resumed team's episode does not replay: flags %v", ep.Flags)
	}
}

// Branches of a finished rollout continue its conversation from a savepoint:
// the steps before the fork are the trunk's (inherited, not trainable), and the
// siblings' first request after the fork is the same prompt, byte for byte,
// which is what pairs their decisions for DPO.
func TestBranchesShareTheirFirstPromptAndInheritTheTrunk(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	agent.RetryBase = time.Millisecond
	policy := func(c *mock.Call) mock.Reply {
		switch turns(c) {
		case 0:
			return mock.Reply{Text: "note", ToolCalls: []mock.ToolCall{call("c1", "bash", map[string]any{"command": "echo started > progress.txt"})}}
		case 1:
			return mock.Reply{Text: "look", ToolCalls: []mock.ToolCall{call("c2", "read", map[string]any{"path": "demo_test.go"})}}
		case 2:
			return mock.Reply{Text: "list", ToolCalls: []mock.ToolCall{call("c3", "bash", map[string]any{"command": "ls"})}}
		case 3:
			time.Sleep(300 * time.Millisecond) // the trunk's savepoints come while it waits here, with three batches to fold
			return mock.Reply{Text: "read", ToolCalls: []mock.ToolCall{call("c4", "read", map[string]any{"path": "demo.go"})}}
		case 4:
			return mock.Reply{Text: "fix", ToolCalls: []mock.ToolCall{call("c5", "edit", map[string]any{"path": "demo.go", "old_string": "a - b", "new_string": "a + b"})}}
		}
		return mock.Reply{Text: "Fixed Add."}
	}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, policy)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	repo := newRepo(t)
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip(err)
	}
	task := rl.Task{
		ID: "branch-fix", Kind: rl.TaskFix,
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
	pipe := &harness.Pipeline{Harness: rl.HarnessRef{Version: "test"}, Reward: reward.DefaultConfig()}
	rn := &env.Runner{
		Harness: &harness.Harness{NewProvider: injected(ts.URL)}, Workspaces: ws, Out: filepath.Join(t.TempDir(), "trunk"),
		Extract: pipe.Extract, Score: pipe.Score, Concurrency: 1, InfraRetries: -1, SavepointEvery: 50 * time.Millisecond,
	}
	opts := env.RolloutOpts{RunID: "trunk", Policy: env.PolicySpec{Model: "mock-1"}}
	if sum, err := rn.Rollout(context.Background(), []rl.Task{task}, 1, opts); err != nil || sum.Completed != 1 {
		t.Fatalf("trunk: %+v %v", sum, err)
	}
	trunk := filepath.Join(rn.Out, task.ID, "0")
	points, err := env.TrunkSavepoints(trunk)
	if err != nil || len(points) == 0 {
		t.Fatalf("trunk savepoints %v %v", points, err)
	}

	rn.Out = filepath.Join(t.TempDir(), "branches")
	opts.RunID = "branches"
	last := points[len(points)-1:]
	sum, err := rn.Branch(context.Background(), task, env.BranchRequest{Trunk: trunk, Points: last, Siblings: 2}, opts)
	if err != nil || sum.Completed != 2 {
		t.Fatalf("branches: %+v %v", sum, err)
	}
	var firsts []string
	for s := 0; s < 2; s++ {
		var ep rl.Episode
		b, err := os.ReadFile(filepath.Join(rn.Out, task.ID, strconv.Itoa(s), "episode.json"))
		if err != nil || json.Unmarshal(b, &ep) != nil {
			t.Fatalf("branch %d episode: %v", s, err)
		}
		if ep.Fork == nil || ep.Fork.Seq == 0 {
			t.Fatalf("branch %d: fork %+v", s, ep.Fork)
		}
		steps := ep.Agents[0].Steps
		inherited := 0
		for i, st := range steps {
			if st.Inherited {
				inherited++
				if st.Trainable {
					t.Errorf("branch %d step %s is inherited and trainable", s, st.ID)
				}
				continue
			}
			if i == inherited { // the first step after the fork
				firsts = append(firsts, string(st.Prompt.WireHash))
			}
		}
		if inherited == 0 || inherited == len(steps) {
			t.Errorf("branch %d: %d of %d steps inherited", s, inherited, len(steps))
		}
	}
	if len(firsts) != 2 || firsts[0] == "" || firsts[0] != firsts[1] {
		t.Errorf("the siblings' first prompts after the fork differ: %v", firsts)
	}

	// With --compact-first the siblings' first call after the fork is the compactor's, on the same prompt: competing
	// summaries of one state.
	rn.Out = filepath.Join(t.TempDir(), "summaries")
	opts.RunID = "summaries"
	if sum, err := rn.Branch(context.Background(), task, env.BranchRequest{Trunk: trunk, Points: last, Siblings: 2, CompactFirst: true}, opts); err != nil || sum.Completed != 2 {
		t.Fatalf("compact-first branches: %+v %v", sum, err)
	}
	var compactors []string
	for s := 0; s < 2; s++ {
		var ep rl.Episode
		b, err := os.ReadFile(filepath.Join(rn.Out, task.ID, strconv.Itoa(s), "episode.json"))
		if err != nil || json.Unmarshal(b, &ep) != nil {
			t.Fatalf("summary branch %d episode: %v", s, err)
		}
		if ep.Fork == nil || !ep.Fork.CompactFirst {
			t.Errorf("summary branch %d: fork %+v", s, ep.Fork)
		}
		for _, st := range ep.Agents[0].Steps {
			if !st.Inherited && st.Kind == rl.KindCompactor {
				compactors = append(compactors, string(st.Prompt.WireHash))
				break
			}
		}
	}
	if len(compactors) != 2 || compactors[0] != compactors[1] {
		t.Errorf("the siblings' compactor calls after the fork: %v, want two on one prompt", compactors)
	}
}
