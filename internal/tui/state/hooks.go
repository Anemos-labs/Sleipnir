package state

import (
	"time"
)

// StatusFunc is told that an agent's Status changed from one value to another by an event that happened at at.
type StatusFunc func(agent string, from, to Status, at time.Time)

// ChangeFunc is told, after each applied event, which agents and which tasks the event may have changed (ids, in the order the event
// touched them). A follower that mirrors the State elsewhere looks at those and nothing else, so that it does constant work per event.
type ChangeFunc func(agents, tasks []string)

// hookState is the registered callbacks and what the event being applied touched. Tracking is off until a callback is registered,
// so that a State nobody watches pays nothing for it.
type hookState struct {
	status   []StatusFunc
	change   []ChangeFunc
	on       bool
	agents   []string          // touched by the event being applied
	tasks    []string          // likewise
	notified map[string]Status // the status last reported per agent
}

// note is what one applied event changed, delivered to the callbacks after the State's lock is released.
type note struct {
	at       time.Time
	agents   []string
	tasks    []string
	statuses []statusChange
}

// statusChange is one agent's status change, for the StatusFuncs.
type statusChange struct {
	agent    string
	from, to Status
}

// OnStatus registers fn, called after each applied event for every agent whose Status changed (the gantt segments of the page). It is
// called from the goroutine that called Apply, after the State's lock was released, in the order of the events, and once per change.
func (s *State) OnStatus(fn func(agent string, from, to Status, at time.Time)) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks.status = append(s.hooks.status, fn)
	s.hooks.on = true
	if s.hooks.notified == nil {
		s.hooks.notified = map[string]Status{}
		for id, a := range s.agents {
			s.hooks.notified[id] = a.Status
		}
	}
}

// OnChange registers fn, called after each applied event with the agents and tasks it may have changed (see ChangeFunc), from the
// goroutine that called Apply, after the State's lock was released.
func (s *State) OnChange(fn func(agents, tasks []string)) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks.change = append(s.hooks.change, fn)
	s.hooks.on = true
	if s.hooks.notified == nil {
		s.hooks.notified = map[string]Status{}
		for id, a := range s.agents {
			s.hooks.notified[id] = a.Status
		}
	}
}

// touchAgent records that the event being applied may have changed the agent.
func (s *State) touchAgent(id string) {
	if !s.hooks.on || id == "" {
		return
	}
	for _, v := range s.hooks.agents {
		if v == id {
			return
		}
	}
	s.hooks.agents = append(s.hooks.agents, id)
}

// touchTask records that the event being applied may have changed the task.
func (s *State) touchTask(id string) {
	if !s.hooks.on || id == "" {
		return
	}
	for _, v := range s.hooks.tasks {
		if v == id {
			return
		}
	}
	s.hooks.tasks = append(s.hooks.tasks, id)
}

// takeNote collects what the event just applied changed and resets the per-event tracking; ok is false when nobody listens or nothing
// was touched. Called with the lock held.
func (s *State) takeNote(at time.Time) (note, bool) {
	h := &s.hooks
	if !h.on || (len(h.agents) == 0 && len(h.tasks) == 0) {
		h.agents, h.tasks = h.agents[:0], h.tasks[:0]
		return note{}, false
	}
	n := note{at: at, agents: append([]string(nil), h.agents...), tasks: append([]string(nil), h.tasks...)}
	for _, id := range h.agents {
		a := s.agents[id]
		var now Status
		if a != nil {
			now = a.Status
		}
		if was, seen := h.notified[id]; !seen || was != now {
			if a == nil && !seen {
				continue
			}
			n.statuses = append(n.statuses, statusChange{agent: id, from: was, to: now})
			if a == nil {
				delete(h.notified, id)
			} else {
				h.notified[id] = now
			}
		}
	}
	h.agents, h.tasks = h.agents[:0], h.tasks[:0]
	return n, true
}

// deliver calls the callbacks with what one event changed. Called without the lock.
func (s *State) deliver(n note, status []StatusFunc, change []ChangeFunc) {
	for _, c := range n.statuses {
		for _, fn := range status {
			fn(c.agent, c.from, c.to, n.at)
		}
	}
	if len(change) > 0 {
		for _, fn := range change {
			fn(n.agents, n.tasks)
		}
	}
}

// callbacks returns the registered callbacks (the slices are only ever appended to under the lock). Called with the lock held.
func (s *State) callbacks() ([]StatusFunc, []ChangeFunc) { return s.hooks.status, s.hooks.change }
