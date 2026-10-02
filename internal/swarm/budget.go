package swarm

import (
	"errors"
	"fmt"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/cost"
)

// The swarm budget (Config.BudgetUSD) is a ledger over every agent the swarm has
// had: the spend of retired agents is folded into it when they leave, so retiring
// (or the janitor) cannot reopen the budget. It is enforced where it cannot be
// evaded: the request governor asks before every request, and the moment the total
// is over the line every running worker is stopped.

// TotalCost sums spend across every agent, running or retired.
func (s *Swarm) TotalCost() float64 {
	s.mu.Lock()
	spent := s.spent
	ms := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		ms = append(ms, m)
	}
	s.mu.Unlock()
	for _, m := range ms {
		_, c := m.a.Usage()
		spent += c
	}
	return spent
}

// budgetErr returns a non-nil error once the swarm budget is spent. The error wraps
// agent.ErrBudget, so an agent whose request is refused ends its run as
// budget-exhausted.
func (s *Swarm) budgetErr() error {
	if s.cfg.BudgetUSD <= 0 {
		return nil
	}
	tc := s.TotalCost()
	if tc < s.cfg.BudgetUSD {
		return nil
	}
	s.budgetExceeded(tc)
	return fmt.Errorf("swarm budget of %s is exhausted (%s spent): %w: %w", cost.Dollars(s.cfg.BudgetUSD), cost.Dollars(tc), errSwarmBudget, agent.ErrBudget)
}

// errSwarmBudget marks a stop caused by the swarm-wide budget: no task or worker
// is at fault, so it does not count as a failed attempt.
var errSwarmBudget = errors.New("swarm budget exhausted")

// budgetExceeded stops the running workers, once, and tells the manager.
func (s *Swarm) budgetExceeded(spent float64) {
	if !s.budgetOnce.CompareAndSwap(false, true) {
		return
	}
	s.emit("swarm.budget", map[string]any{"budget_usd": s.cfg.BudgetUSD, "spent_usd": spent})
	s.goTracked(func() {
		n := s.stopWorkers("swarm budget exhausted", false)
		s.notifyManager(fmt.Sprintf("Swarm budget of %s is exhausted (%s spent): %d running worker(s) were stopped and their tasks returned to todo. Nothing more will run; report to the user.", cost.Dollars(s.cfg.BudgetUSD), cost.Dollars(spent), n))
	})
}

// goTracked runs fn on a goroutine the swarm waits for at Shutdown (unless it is
// already shutting down).
func (s *Swarm) goTracked(fn func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() { _ = recover() }()
		fn()
	}()
}
