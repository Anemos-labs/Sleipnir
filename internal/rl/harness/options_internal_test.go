package harness

import (
	"testing"

	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/env"
)

func optionsOf(t *testing.T, sp env.RunSpec) (maxAgents int, budgetUSD float64) {
	t.Helper()
	h := &Harness{NewProvider: func(env.RunSpec) (provider.Provider, cost.Model, error) { return nil, cost.Model{}, nil }}
	cfg, err := h.config(sp)
	if err != nil {
		t.Fatal(err)
	}
	o, err := h.options(sp, cfg, nil, cost.Model{})
	if err != nil {
		t.Fatal(err)
	}
	return o.MaxAgents, o.BudgetUSD
}

// A team size counts workers: `run --swarm 3` is three workers and a manager, and a composite task of three parts
// has three workers. The session's MaxAgents counts the manager too.
func TestATeamOfNWorkersIsNPlusOneAgents(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec env.RunSpec
		want int
	}{
		{"from the task's team", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p", Team: rl.Team{Mode: "swarm", Agents: 3}}}, 4},
		{"from the command line", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p", Team: rl.Team{Mode: "swarm", Agents: 3}}, Swarm: true, Agents: 5}, 6},
		{"one worker", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p", Team: rl.Team{Mode: "swarm", Agents: 1}}}, 2},
		{"no size: the session's own limit", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p", Team: rl.Team{Mode: "swarm"}}}, 0},
		{"not a swarm", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p"}, Agents: 3}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := optionsOf(t, tc.spec); got != tc.want {
				t.Fatalf("MaxAgents = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTheBudgetInDollarsReachesTheSession(t *testing.T) {
	sp := env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p"}, Budget: rl.Budget{USD: 0.25}}
	if _, got := optionsOf(t, sp); got != 0.25 {
		t.Fatalf("BudgetUSD = %v, want 0.25", got)
	}
}
