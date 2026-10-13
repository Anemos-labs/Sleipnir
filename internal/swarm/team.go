package swarm

import (
	"fmt"
	"sort"
	"strings"
)

// A team is the set of worker slots a session has: Config.Team names the role of
// each slot, one entry per slot, and a role may appear more than once (two backend
// slots, no docs slot). A worker occupies one slot of its role from the moment it is
// spawned until it is retired; the manager can start a worker of a role only while
// that role has a free slot, and reuses an idle worker (spawn agent=…) otherwise. An
// empty team limits roles by nothing but MaxWorkers (tests, embedders, training
// tasks whose team names roles without counts).

// DefaultTeam is the team of a session that names none: one slot per role in order
// when workers equals the number of roles, and otherwise slots cycle through the role order until the exact count is reached. order lists the
// spawnable roles; zero workers or no roles is no team.
func DefaultTeam(order []string, workers int) []string {
	if workers <= 0 || len(order) == 0 {
		return nil
	}
	out := make([]string, workers)
	for i := range out {
		out[i] = order[i%len(order)]
	}
	return out
}

// TeamCounts returns how many slots each role has in team, and the roles in the
// order they first appear.
func TeamCounts(team []string) (map[string]int, []string) {
	n := map[string]int{}
	var order []string
	for _, r := range team {
		if n[r] == 0 {
			order = append(order, r)
		}
		n[r]++
	}
	return n, order
}

// teamLimited reports whether the team limits which roles can be spawned.
func (s *Swarm) teamLimited() bool { return len(s.cfg.Team) > 0 }

// freeSlotErr says why no worker of role can be started now, or nil. It is called
// with s.mu held.
func (s *Swarm) freeSlotErr(role string) error {
	if !s.teamLimited() {
		return nil
	}
	slots, _ := TeamCounts(s.cfg.Team)
	if slots[role] == 0 {
		return fmt.Errorf("this team has no %s slot (%s); give the task to a role the team has", role, s.teamStateLocked())
	}
	used := 0
	for _, m := range s.members {
		if m.role == role && !m.manager && !m.service {
			used++
		}
	}
	if used >= slots[role] {
		return fmt.Errorf("no free %s slot (%s): reuse an idle %s with spawn agent=…, wait for one to finish, or use a role with a free slot", role, s.teamStateLocked(), role)
	}
	return nil
}

// slotFor is the slot a new member of role takes: the first slot of that role that
// no member holds, or -1 when the team does not limit roles or has none free. It is
// called with s.mu held.
func (s *Swarm) slotFor(role string) int {
	held := map[int]bool{}
	for _, m := range s.members {
		if m.slot >= 0 {
			held[m.slot] = true
		}
	}
	for i, r := range s.cfg.Team {
		if r == role && !held[i] {
			return i
		}
	}
	return -1
}

// teamStateLocked renders the team's occupancy, "backend 1/2 · tester 0/1", in team
// order. It is called with s.mu held.
func (s *Swarm) teamStateLocked() string {
	slots, order := TeamCounts(s.cfg.Team)
	used := map[string]int{}
	for _, m := range s.members {
		if !m.manager && !m.service {
			used[m.role]++
		}
	}
	parts := make([]string, 0, len(order))
	for _, r := range order {
		parts = append(parts, fmt.Sprintf("%s %d/%d", r, used[r], slots[r]))
	}
	return "team: " + strings.Join(parts, " · ")
}

// TeamState is the team's occupancy for the manager's live view, or "" when the
// team does not limit roles.
func (s *Swarm) TeamState() string {
	if !s.teamLimited() {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.teamStateLocked()
}

// teamRoles lists the roles of the team, sorted (nil when the team does not limit
// roles).
func (s *Swarm) teamRoles() []string {
	_, order := TeamCounts(s.cfg.Team)
	sort.Strings(order)
	return order
}

// Role returns the role an agent of the swarm runs under, with the pin the swarm
// gives it (the team card and the auto-docs notes included).
func (s *Swarm) Role(name string) (Role, bool) {
	r, ok := s.roles[name]
	return r, ok
}
