package harness

import (
	"context"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

func optionsOf(t *testing.T, sp env.RunSpec) (workers int, budgetUSD float64) {
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
	return o.Workers, o.BudgetUSD
}

// A team size counts workers: `--swarm 3` is three workers and a manager, and a composite task of three parts
// has three workers. The session is asked for that many workers, as it stands.
func TestATeamOfNWorkersIsNWorkers(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec env.RunSpec
		want int
	}{
		{"from the task's team", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p", Team: rl.Team{Mode: "swarm", Agents: 3}}}, 3},
		{"from the command line", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p", Team: rl.Team{Mode: "swarm", Agents: 3}}, Swarm: true, Agents: 5}, 5},
		{"one worker", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p", Team: rl.Team{Mode: "swarm", Agents: 1}}}, 1},
		{"no size: the session's own limit", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p", Team: rl.Team{Mode: "swarm"}}}, 0},
		{"not a swarm", env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p"}, Agents: 3}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := optionsOf(t, tc.spec); got != tc.want {
				t.Fatalf("Workers = %d, want %d", got, tc.want)
			}
		})
	}
}

// A rollout's agents wait an outage out for the runner's patience, silence included (the endpoint is known to exist); without one
// they give up after the usual attempts, as before.
func TestTheRunnersPatienceReachesTheAgents(t *testing.T) {
	h := &Harness{NewProvider: func(env.RunSpec) (provider.Provider, cost.Model, error) { return nil, cost.Model{}, nil }}
	for _, tc := range []struct {
		patience    time.Duration
		want        time.Duration
		unreachable bool
	}{
		{20 * time.Minute, 20 * time.Minute, true},
		{0, -1, false},
		{-1, -1, false},
	} {
		sp := env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p"}, OutagePatience: tc.patience}
		cfg, err := h.config(sp)
		if err != nil {
			t.Fatal(err)
		}
		o, err := h.options(sp, cfg, nil, cost.Model{})
		if err != nil {
			t.Fatal(err)
		}
		if o.OutagePatience != tc.want || o.WaitUnreachable != tc.unreachable {
			t.Errorf("patience %v: session patience %v unreachable %v, want %v %v", tc.patience, o.OutagePatience, o.WaitUnreachable, tc.want, tc.unreachable)
		}
	}
}

// The watch reads what came back from the endpoint: an outage begins at a failure that is one and ends at the next answer, a
// refusal with a status is an answer too, and a call the caller stopped says nothing.
func TestTheWatchTellsTheClockWhenTheEndpointIsDownAndBack(t *testing.T) {
	clock := env.NewWallClock(0, 0, nil)
	w := &outageWatch{clock: clock}
	var next error
	p := w.wrap(fakeProvider{do: func() error { return next }})
	call := func(ctx context.Context, err error) {
		next = err
		_, _ = p.Do(ctx, &provider.Request{}, nil)
	}
	call(context.Background(), &provider.Error{Kind: provider.ErrBadRequest, Status: 400})
	if clock.Outages() != 0 {
		t.Fatal("a 400 began an outage")
	}
	call(context.Background(), &provider.Error{Kind: provider.ErrNetwork, Message: "connection refused"})
	if clock.Outages() != 1 || !clock.IsDown() {
		t.Fatal("a refused connection did not begin an outage")
	}
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	call(stopped, nil)
	if !clock.IsDown() {
		t.Fatal("a call the caller stopped ended the outage")
	}
	call(context.Background(), &provider.Error{Kind: provider.ErrBadRequest, Status: 400})
	if clock.IsDown() {
		t.Fatal("an answer with a status did not end the outage")
	}
	call(context.Background(), &provider.Error{Kind: provider.ErrServer, Status: 503})
	call(context.Background(), nil)
	if clock.Outages() != 2 || clock.IsDown() {
		t.Fatalf("outages %d: a 503 and then an answer are one more outage, ended", clock.Outages())
	}
	if (&outageWatch{}).wrap(p) != p {
		t.Error("a watch without a clock wrapped the provider")
	}
}

// fakeProvider answers every call with what do returns.
type fakeProvider struct{ do func() error }

func (fakeProvider) Profile() provider.Profile { return provider.Profile{Name: "fake"} }
func (f fakeProvider) Do(context.Context, *provider.Request, func(provider.Event)) (*provider.Response, error) {
	if err := f.do(); err != nil {
		return nil, err
	}
	return &provider.Response{}, nil
}

func TestTheBudgetInDollarsReachesTheSession(t *testing.T) {
	sp := env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p"}, Budget: rl.Budget{USD: 0.25}}
	if _, got := optionsOf(t, sp); got != 0.25 {
		t.Fatalf("BudgetUSD = %v, want 0.25", got)
	}
}

// How soon a rollout's threads are compacted is a setting to compare, so it can be set from the command line; left alone, a rollout
// uses the default every session does.
func TestTheThreadSoftLimitReachesTheConfig(t *testing.T) {
	sp := env.RunSpec{Task: rl.Task{ID: "t", Prompt: "p"}}
	provide := func(env.RunSpec) (provider.Provider, cost.Model, error) { return nil, cost.Model{}, nil }
	def, err := (&Harness{NewProvider: provide}).config(sp)
	if err != nil {
		t.Fatal(err)
	}
	set, err := (&Harness{NewProvider: provide, ThreadSoftLimit: 80_000}).config(sp)
	if err != nil {
		t.Fatal(err)
	}
	if set.Cache.ThreadSoftLimitTokens != 80_000 {
		t.Errorf("soft limit %d, want the 80000 that was asked for", set.Cache.ThreadSoftLimitTokens)
	}
	if def.Cache.ThreadSoftLimitTokens == 80_000 || def.Cache.ThreadSoftLimitTokens < 0 {
		t.Errorf("without the setting the config keeps its own default, got %d", def.Cache.ThreadSoftLimitTokens)
	}
}
