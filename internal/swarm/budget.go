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

// closeAgent closes a retired member's agent, and agentSpend reads what an agent has cost, what its plan-covered usage comes to at the
// API list price and whether it had any (agent.Agent.PlanUsage). They are variables so that a test can stand between the two (detach
// reads the spend after the close).
var (
	closeAgent = func(a *agent.Agent) error { return a.Close() }
	agentSpend = func(a *agent.Agent) (cost, list float64, plan bool) {
		_, c := a.Usage()
		l, p := a.PlanUsage()
		return c, l, p
	}
)

// counted are the agents whose spend is not yet in the ledger: the members, and the members being retired (detach), with the ledger
// (the spend, the list-price value and whether a plan was used) read in the same critical section, so that an agent is counted exactly
// once whatever detach does meanwhile.
func (s *Swarm) counted() (spent, list float64, plan bool, ms []*member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms = make([]*member, 0, len(s.members)+len(s.retiring))
	for _, m := range s.members {
		ms = append(ms, m)
	}
	for m := range s.retiring {
		ms = append(ms, m)
	}
	return s.spent, s.spentList, s.spentPlan, ms
}

// TotalCost sums spend across every agent, running or retired.
func (s *Swarm) TotalCost() float64 {
	spent, _, _, ms := s.counted()
	for _, m := range ms {
		_, c := m.a.Usage()
		spent += c
	}
	return spent
}

// TotalListCost sums, across every agent running or retired, the API list-price value of the usage a subscription plan covered
// (agent.Agent.ListUSD): what the interfaces show for that usage. plan says any agent's request was accounted on a plan's model, which
// a plan's model whose list price is not known does without adding to list. No budget reads them; TotalCost is the spend.
func (s *Swarm) TotalListCost() (list float64, plan bool) {
	_, list, plan, ms := s.counted()
	for _, m := range ms {
		l, p := m.a.PlanUsage()
		list, plan = list+l, plan || p
	}
	return list, plan
}

// AddRetiredListCost folds into TotalListCost the list-price value, and whether a plan was used, of agents of an earlier run that a
// resume does not bring back (the counterpart of RestoreTeam's retiredCost for the budget ledger).
func (s *Swarm) AddRetiredListCost(list float64, plan bool) {
	s.mu.Lock()
	s.spentList += list
	s.spentPlan = s.spentPlan || plan
	s.mu.Unlock()
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

// BudgetError reports and enforces the session-wide swarm budget for host workflows.
func (s *Swarm) BudgetError() error { return s.budgetErr() }
