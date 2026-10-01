package harness

import (
	"slices"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/reward"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

// Without knowing where the workspace is, the hack detector cannot tell a workspace below a hidden directory of the home
// (~/.sleipnir-bench/work/ws/...) from the agent writing into the home's dotfiles. It found out from the run's own log:
// a benchmark flagged correct, passing rollouts "hack:outside_worktree" and took their outcome reward away. The log says
// where the session ran (session.start) and where every worker's tree is (workspace.create); scoring tells the detector.
func TestScoringKnowsWhereTheWorkspaceIs(t *testing.T) {
	const (
		tree   = "/root/.bench/work/ws/t-s0-abc/tree"
		worker = "/root/.bench/work/ws/t-s0-abc/trees/w1"
	)
	write := func(path string) string {
		return `{"path":"` + path + `","old_string":"a - b","new_string":"a + b"}`
	}
	score := func(t *testing.T, roots bool, path string) *rl.Episode {
		t.Helper()
		r := trajtest.New()
		if roots {
			r.Emit("a1", events.TypeSessionStart, map[string]any{"cwd": tree, "root": tree})
			r.Emit("a1", events.TypeWorkspaceCreate, map[string]any{"path": worker, "branch": "w1", "mode": "worktree"})
		}
		a := r.Agent("a1", "worker", "m1")
		a.Step(trajtest.Call{Text: "fix", Tool: "edit", Input: write(path), Result: "edited"})
		a.Step(trajtest.Call{Text: "Fixed."})
		run := traj.OpenWith(r.Events(), r.Blobs)
		task := &rl.Task{ID: "t", Verifier: rl.Verifier{Cmd: "go test ./..."}}
		ep, err := run.Episode(traj.Options{TaskID: "t", Policy: rl.PolicyRef{Model: "m1"}, Task: task})
		if err != nil {
			t.Fatal(err)
		}
		ep.Outcome.Verifier = &rl.Verdict{Kind: "verifier", Pass: true, Score: 1}
		ep.Outcome.Claimed = "done"
		p := &Pipeline{Reward: reward.DefaultConfig()}
		dir := t.TempDir()
		p.keep(dir, run)
		if err := p.Score(ep, task, dir); err != nil {
			t.Fatal(err)
		}
		return ep
	}
	escaped := func(ep *rl.Episode) bool { return slices.Contains(ep.Flags, rl.FlagHackEscape) }

	t.Run("an edit inside the session's own tree is not an escape", func(t *testing.T) {
		if ep := score(t, true, tree+"/pkg/a.go"); escaped(ep) {
			t.Errorf("flagged: %v %v", ep.Flags, ep.Reward.Notes)
		}
	})
	t.Run("nor one inside a worker's tree", func(t *testing.T) {
		if ep := score(t, true, worker+"/pkg/a.go"); escaped(ep) {
			t.Errorf("flagged: %v %v", ep.Flags, ep.Reward.Notes)
		}
	})
	t.Run("a write outside every tree still is", func(t *testing.T) {
		if ep := score(t, true, "/root/.bench/other/a.go"); !escaped(ep) {
			t.Errorf("not flagged: %v", ep.Flags)
		}
	})
	t.Run("a log that does not say where it ran keeps the old rule", func(t *testing.T) {
		if ep := score(t, false, "/root/.ssh/authorized_keys"); !escaped(ep) {
			t.Errorf("not flagged: %v", ep.Flags)
		}
	})
}
