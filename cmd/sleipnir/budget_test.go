package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// budgetStopped and budgetLabel need a session; a bare one carries only its budget
// and (for a swarm) its Swarm pointer, which is what they read.
func TestBudgetStopNamesTheCapAndHowToRaiseIt(t *testing.T) {
	var s session.Session
	if got := budgetLabel(&s); got != "" {
		t.Errorf("a session with no budget has no label: %q", got)
	}
	err := budgetStopped(&s, &session.Result{CostUSD: 1.2345})
	if err == nil || !strings.Contains(err.Error(), "--budget-usd") || !strings.Contains(err.Error(), "$1.23 spent") {
		t.Errorf("single agent: %v", err)
	}
	if strings.Contains(err.Error(), "swarm.budget_usd") {
		t.Errorf("a single agent has no swarm budget setting to raise: %v", err)
	}
}

// The reason a run's session ended, as the SessionEnd hooks see it.
func TestEndReasonOfARun(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"finished", context.Background(), nil, session.EndCompleted},
		{"out of budget", context.Background(), fmt.Errorf("run: %w", agent.ErrBudget), session.EndBudget},
		{"interrupted, as the error says", context.Background(), context.Canceled, session.EndInterrupted},
		{"interrupted, as the context says", cancelled, errors.New("provider: request aborted"), session.EndInterrupted},
		{"failed", context.Background(), errors.New("step limit reached"), session.EndError},
		{"budget wins over an interrupt that follows it", cancelled, agent.ErrBudget, session.EndBudget},
	} {
		if got := endReasonOf(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
