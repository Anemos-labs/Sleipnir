package swarm

import (
	"time"
)

// outageSteer is how long after a worker's run ended on an outage of its model's endpoint the manager's assignments go to a
// free member of its role on another model instead. In a long team run one provider had no endpoints for hours; the manager,
// told to give the work to another worker, gave it back to the workers on that model, and each new run failed minutes later
// while the members on another model sat idle.
const outageSteer = 10 * time.Minute

// inOutage says whether m's last run ended on an outage of its model's endpoint less than outageSteer ago, and when.
func (s *Swarm) inOutage(m *member) (time.Time, bool) {
	m.mu.Lock()
	at := m.outage
	m.mu.Unlock()
	return at, !at.IsZero() && s.deps.Now().Sub(at) < outageSteer
}

// freeElsewhere lists the idle members of m's role on another model whose last run did not end on an outage.
func (s *Swarm) freeElsewhere(m *member) []string {
	var out []string
	for _, o := range s.poolMembers(m.role) {
		if o == m || o.model.ID == m.model.ID {
			continue
		}
		o.mu.Lock()
		free := o.life == lifeIdle && o.restart == nil && o.outage.IsZero()
		o.mu.Unlock()
		if free {
			out = append(out, o.id)
		}
	}
	return out
}
