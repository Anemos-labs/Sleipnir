package harness

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/reward"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
)

// Pipeline turns a finished rollout directory into a scored episode: the
// extractor and the scorer of env.Runner. The policy an episode names is what
// the run's own log says (the harness records it on session.start), so the
// server, which takes a different policy per request, needs no side channel.
type Pipeline struct {
	// Harness is recorded on every episode (version and commit of the build).
	Harness rl.HarnessRef
	// Reward configures scoring; the zero value scores with the documented
	// defaults, except that fidelity probes are off (see reward.DefaultConfig).
	Reward reward.Config

	mu    sync.Mutex
	runs  map[string]*traj.Run
	order []string
}

// maxKept bounds the runs held between the two stages; it is a leak guard, since
// a scorer that never runs would otherwise keep every log in memory.
const maxKept = 64

var (
	_ env.Extractor = (*Pipeline)(nil).Extract
	_ env.Scorer    = (*Pipeline)(nil).Score
)

// Extract implements env.Extractor.
func (p *Pipeline) Extract(runDir string, task rl.Task, sample int, group string) (*rl.Episode, error) {
	run, err := traj.Open(runDir)
	if err != nil {
		return nil, err
	}
	t := task
	ep, err := run.Episode(traj.Options{
		TaskID: task.ID, Sample: sample, Group: group,
		Policy: policyOf(run.Events()), Task: &t, Harness: p.Harness,
	})
	if err != nil {
		return nil, err
	}
	p.keep(runDir, run)
	return ep, nil
}

// Score implements env.Scorer.
func (p *Pipeline) Score(ep *rl.Episode, task *rl.Task, runDir string) error {
	run := p.take(runDir)
	blobs, err := events.NewDirBlobs(filepath.Join(runDir, "blobs"))
	if err != nil {
		return err
	}
	cfg := p.Reward
	if run == nil {
		if run, err = traj.Open(runDir); err != nil {
			return err
		}
	}
	// The hack detector needs to know where the workspace is: without it a workspace below a hidden directory of the home
	// (~/.sleipnir-bench/work/...) looks like the agent writing into the home's dotfiles. The log says where it ran.
	cfg.WorkspaceRoots = append(slices.Clone(cfg.WorkspaceRoots), run.WorkspaceRoots()...)
	res := traj.Resolver{Run: run}
	cfg.Prompts = func(e *rl.Episode, st *rl.Step) (string, error) {
		pr, err := res.Prompt(e, st)
		if err != nil {
			return "", err
		}
		s := *st
		s.Inline = pr
		return reward.InlinePromptText(e, &s)
	}
	return reward.Score(ep, task, cfg, reward.DiffsFromBlobs(blobs.Get))
}

// keep caches a trajectory run under lock and evicts oldest insertions beyond maxKept without
// refreshing order for replacements.
func (p *Pipeline) keep(dir string, run *traj.Run) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runs == nil {
		p.runs = map[string]*traj.Run{}
	}
	if _, ok := p.runs[dir]; !ok {
		p.order = append(p.order, dir)
	}
	p.runs[dir] = run
	for len(p.order) > maxKept {
		delete(p.runs, p.order[0])
		p.order = p.order[1:]
	}
}

// take removes and returns a cached trajectory run and its order entry under lock, returning nil
// when absent.
func (p *Pipeline) take(dir string) *traj.Run {
	p.mu.Lock()
	defer p.mu.Unlock()
	run := p.runs[dir]
	delete(p.runs, dir)
	for i, d := range p.order {
		if d == dir {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	return run
}

// policyOf reads the policy a run recorded about itself (session.start meta).
// Without it nothing in the episode is trainable: a policy must be named.
func policyOf(evs []events.Event) rl.PolicyRef {
	for _, e := range evs {
		if e.Type != events.TypeSessionStart {
			continue
		}
		var start struct {
			Meta struct {
				RL struct {
					Policy     string            `json:"policy"`
					Endpoint   string            `json:"endpoint"`
					Sampling   json.RawMessage   `json:"sampling"`
					RoleModels map[string]string `json:"role_models"`
				} `json:"rl"`
			} `json:"meta"`
		}
		if json.Unmarshal(e.Data, &start) != nil {
			return rl.PolicyRef{}
		}
		r := start.Meta.RL
		ref := rl.PolicyRef{Model: r.Policy, Endpoint: r.Endpoint, RoleModels: r.RoleModels}
		if len(r.Sampling) > 0 && string(r.Sampling) != "null" {
			ref.Sampling = r.Sampling
		}
		return ref
	}
	return rl.PolicyRef{}
}
