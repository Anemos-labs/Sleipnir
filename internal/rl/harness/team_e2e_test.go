package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
	"github.com/anemos-labs/sleipnir/internal/rl/harness"
	"github.com/anemos-labs/sleipnir/internal/rl/reward"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
)

var (
	teamWho  = regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	teamTask = regexp.MustCompile(`task (T\d+)`)
)

// teamPolicy scripts a manager and its workers, one rollout after another (Concurrency 1). Sample 0 fixes the bug through a
// worker, supersedes a second task, and wastes nothing. Sample 1 claims a fix and does nothing. Sample 2 fixes the bug
// as well but has its worker read the file again and again, and cancels the second task. Every manager first tries to
// edit a file itself, which the harness refuses.
func teamPolicy(sample *atomic.Int64) mock.Responder {
	return func(c *mock.Call) mock.Reply {
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
			if n == 0 {
				sample.Add(1)
			}
			s := sample.Load() - 1
			if s == 1 { // the sample that lies
				return mock.Reply{Text: "Fixed Add; the tests pass."}
			}
			switch n {
			case 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("m0", "edit", map[string]any{"path": "demo.go", "old_string": "a - b", "new_string": "a + b"}),
					call("m1", "task", map[string]any{"action": "create", "title": "Fix Add", "role": "backend", "description": "Add returns a-b: make it return a+b", "files": []string{"demo.go"}}),
					call("m2", "task", map[string]any{"action": "create", "title": "Rewrite Add differently", "role": "backend", "description": "an alternative approach"}),
					call("m3", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("m4", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 120}),
				}}
			case 1:
				fail := map[string]any{"action": "fail", "id": "T2", "reason": "superseded", "target": "T1", "text": "T1 fixed it"}
				if s == 2 {
					fail = map[string]any{"action": "fail", "id": "T2", "reason": "canceled", "text": "not needed"}
				}
				return mock.Reply{Text: "review", ToolCalls: []mock.ToolCall{
					call("m5", "task", map[string]any{"action": "accept", "id": "T1"}),
					call("m6", "task", fail),
				}}
			}
			return mock.Reply{Text: "Add is fixed by T1."}
		}
		// A worker. The sample is the one the manager started.
		s := sample.Load() - 1
		reads := 1
		if s == 2 {
			reads = 3
		}
		switch {
		case n < reads:
			return mock.Reply{Text: "read", ToolCalls: []mock.ToolCall{call("w-r"+string(rune('0'+n)), "read", map[string]any{"path": "demo.go"})}}
		case n == reads:
			return mock.Reply{Text: "fix", ToolCalls: []mock.ToolCall{call("w-e", "edit", map[string]any{"path": "demo.go", "old_string": "a - b", "new_string": "a + b"})}}
		case n == reads+1:
			return mock.Reply{Text: "done", ToolCalls: []mock.ToolCall{call("w-d", "task", map[string]any{"action": "done", "id": tid, "text": "Add returns a+b"})}}
		}
		return mock.Reply{Text: "T1 is done."}
	}
}

// The whole combination on one task run three times by a team (--swarm 3: a manager and up to three workers): the manager's
// own edit is refused; the board's typed closures, the efficiency signals and the best-of-n ranking land on the same episodes;
// and the summary, the report and the exporters show them together.
func TestATeamRolloutCarriesClosuresSignalsAndRanksTogether(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	repo := newRepo(t)
	headOut, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip(err)
	}
	head := strings.TrimSpace(string(headOut))
	task := rl.Task{
		ID: "team-fix", Kind: rl.TaskSwarm,
		Repo:     rl.RepoSpec{Path: repo, Commit: head},
		Prompt:   "Add returns the wrong result. Fix it with a team and check with the tests.",
		Team:     rl.Team{Mode: "swarm", Agents: 3, Roles: []string{"backend"}},
		Verifier: rl.Verifier{Cmd: "go test ./...", TimeoutS: 120, Protected: []string{"*_test.go"}},
		Budget:   rl.Budget{Steps: 60, Requests: 200, WallS: 300},
		Tags:     []string{"go", "swarm"},
	}
	if err := env.ValidateTask(task); err != nil {
		t.Fatal(err)
	}

	var sample atomic.Int64
	pol := startPolicy(t, teamPolicy(&sample))
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{
		Root: t.TempDir(), RepoBase: "/", DisableNetIsolation: true,
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
		Workspaces: ws, Out: out, Concurrency: 1, InfraRetries: -1, MaxWall: 8 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	sum, err := rn.Rollout(ctx, []rl.Task{task}, 3, env.RolloutOpts{
		RunID: "run1", Policy: env.PolicySpec{Model: "mock-1"}, Swarm: true, Agents: 3, Seed: 5, KeepEpisodes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Completed != 3 || sum.Infra != 0 {
		t.Fatalf("summary: %+v (infra: %+v)", sum, sum.InfraErrors)
	}

	eps := map[int]*rl.Episode{}
	for _, r := range sum.Results {
		if r.Episode == nil {
			t.Fatalf("no episode kept for %s/%d", r.Task, r.Sample)
		}
		eps[r.Sample] = r.Episode
	}
	for s, pass := range map[int]bool{0: true, 1: false, 2: true} {
		if v := eps[s].Outcome.Verifier; v == nil || v.Pass != pass {
			t.Fatalf("sample %d verdict = %+v, want pass=%v", s, v, pass)
		}
	}

	// Closures: counted from the board's events, by how each task finally closed; the sample that never
	// used the board has none.
	wantClosures := map[int]map[string]int{
		0: {"done:verified": 1, "failed:superseded": 1},
		1: nil,
		2: {"done:verified": 1, "failed:canceled": 1},
	}
	for s, want := range wantClosures {
		if got := eps[s].Outcome.Closures; !reflect.DeepEqual(got, want) {
			t.Errorf("sample %d closures = %v, want %v", s, got, want)
		}
	}
	total := map[string]int{"done:verified": 2, "failed:superseded": 1, "failed:canceled": 1}
	if !reflect.DeepEqual(sum.Closures, total) {
		t.Errorf("summary closures = %v, want %v", sum.Closures, total)
	}

	// Signals: the manager's refused edit is a tool error; the wasteful team re-read the file; the first
	// team and the liar did not.
	for _, s := range []int{0, 2} {
		if got := eps[s].Signals[rl.SigToolErrors]; got < 1 {
			t.Errorf("sample %d: the manager's refused edit is not a tool error (tool_errors = %v)", s, got)
		}
		if got := eps[s].Signals[rl.SigToolCalls]; got < 8 {
			t.Errorf("sample %d: tool_calls = %v, want the calls of the manager and its worker", s, got)
		}
	}
	if r0, r2 := eps[0].Signals[rl.SigRepeatedReads], eps[2].Signals[rl.SigRepeatedReads]; r0 != 0 || r2 != 2 {
		t.Errorf("repeated reads: sample 0 = %v (want 0), sample 2 = %v (want 2)", r0, r2)
	}
	if len(eps[0].Agents) != 2 {
		t.Errorf("sample 0 has %d agents, want the manager and one worker", len(eps[0].Agents))
	}

	// Ranking: the clean pass first, the wasteful pass next, the false claim last, in the summary as in the exports.
	if best := sum.PerTask[0].Best; best == nil || best.Sample != 0 || !best.Key.Verified {
		t.Fatalf("best of the group = %+v, want sample 0", best)
	}
	if sum.Efficiency.ToolCalls <= 0 || sum.Efficiency.RepeatedReads <= 0 {
		t.Errorf("efficiency means = %+v", sum.Efficiency)
	}
	var srcs []export.Source
	for s := 0; s < 3; s++ {
		run, err := traj.Open(filepath.Join(out, "team-fix", itoa(s)))
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, export.Source{Episode: eps[s], Prompts: traj.Resolver{Run: run}})
	}
	best := export.DefaultOptions(export.FormatSFT)
	best.Select = export.SelectBest
	var buf bytes.Buffer
	if _, err := export.Export(&buf, srcs, best); err != nil {
		t.Fatal(err)
	}
	kept := map[float64]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec struct {
			Sample float64 `json:"sample"`
		}
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("sft record: %v\n%.200s", err, ln)
		}
		kept[rec.Sample] = true
	}
	if len(kept) != 1 || !kept[0] {
		t.Errorf("--select best kept samples %v, want only sample 0", kept)
	}
	pair := export.DefaultOptions(export.FormatDPO)
	pair.Pair = export.PairBestWorst
	buf.Reset()
	if _, err := export.Export(&buf, srcs, pair); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"chosen_episode":"team-fix/0"`) || !strings.Contains(buf.String(), `"rejected_episode":"team-fix/1"`) {
		t.Errorf("--pair best-worst did not pair sample 0 with the false claim:\n%.600s", buf.String())
	}
	buf.Reset()
	if _, err := export.Export(&buf, srcs, export.Options{Format: export.FormatCanonical, Inline: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"closures":{"done:verified":1,"failed:superseded":1}`) {
		t.Errorf("the lossless export lacks sample 0's closures")
	}
}

// A team of N workers is N workers and a manager: the manager does not take a place under the cap, and the (N+1)th worker is
// refused. The task's team size and the run's --swarm size are the same number.
func TestATeamOfNWorkersAllowsExactlyNWorkers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		task, run    int
		wantRefusals int
	}{
		{"one worker: the second spawn is refused", 1, 0, 1},
		{"two workers: both spawns succeed", 2, 0, 0},
		{"the run's size overrides the task's", 1, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			var refused atomic.Int32
			pol := startPolicy(t, func(c *mock.Call) mock.Reply {
				manager, n := false, 0
				for _, m := range c.Messages {
					manager = manager || strings.Contains(m.Content, "you: mgr (manager)")
					if m.Role == "tool" && strings.Contains(m.Content, "worker limit reached") {
						n++
					}
				}
				if !manager {
					return mock.Reply{Text: "ok"}
				}
				if turns(c) == 0 {
					return mock.Reply{Text: "two workers", ToolCalls: []mock.ToolCall{
						call("s1", "spawn", map[string]any{"role": "scout", "task": "look at demo.go"}),
						call("s2", "spawn", map[string]any{"role": "scout", "task": "look at demo_test.go"}),
					}}
				}
				refused.Store(int32(n)) // what the spawn results said, as the manager saw them
				return mock.Reply{Text: "done"}
			})
			h := &harness.Harness{NewProvider: injected(pol.url)}
			sp := spec(t, repo, "coordinate")
			sp.Task.Kind = rl.TaskSwarm
			sp.Task.Team = rl.Team{Mode: "swarm", Agents: tc.task, Roles: []string{"scout"}}
			sp.Swarm, sp.Agents = true, tc.run
			if _, err := h.Run(context.Background(), sp); err != nil {
				t.Fatal(err)
			}
			if n := int(refused.Load()); n != tc.wantRefusals {
				t.Errorf("%d spawns refused at the worker limit, want %d", n, tc.wantRefusals)
			}
		})
	}
}
