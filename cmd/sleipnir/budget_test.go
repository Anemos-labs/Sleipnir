package main

import (
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/session"
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
