package session_test

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A swarm has a spending cap unless the user removes it, --budget-usd wins over the
// configuration, and a budget that could not stop anything (NaN, negative) is refused
// instead of quietly meaning "none".
func TestSwarmBudgetDefaultsFlagsAndBadValues(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	build := func(mutate func(*session.Options, *config.Config)) (*session.Session, error) {
		cfg := config.Defaults()
		o := opts(t, repo, client, model)
		o.Config = cfg
		o.Swarm, o.Workers = true, 2
		mutate(&o, cfg)
		return session.New(context.Background(), o)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*session.Options, *config.Config)
		want   float64
	}{
		{"the built-in cap", func(*session.Options, *config.Config) {}, config.DefaultSwarmBudgetUSD},
		{"the configured cap", func(_ *session.Options, c *config.Config) { c.Swarm.BudgetUSD = 12.5 }, 12.5},
		{"removed by the user", func(_ *session.Options, c *config.Config) { c.Swarm.BudgetUSD = 0 }, 0},
		{"the flag wins", func(o *session.Options, c *config.Config) { o.BudgetUSD, c.Swarm.BudgetUSD = 3, 12.5 }, 3},
	} {
		s, err := build(tc.mutate)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := s.Budget(); got != tc.want {
			t.Errorf("%s: the session's budget is %v, want %v", tc.name, got, tc.want)
		}
		s.Close()
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1} {
		if _, err := build(func(o *session.Options, _ *config.Config) { o.BudgetUSD = bad }); err == nil || !strings.Contains(err.Error(), "--budget-usd must be zero") {
			t.Errorf("budget %v: %v", bad, err)
		}
	}
	// A single agent has only what the flag says.
	o := opts(t, repo, client, model)
	o.BudgetUSD = 2
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Budget() != 2 {
		t.Errorf("single agent budget %v", s.Budget())
	}
}
