package session

import (
	"github.com/anemos-labs/sleipnir/internal/lore"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// buildLore is the project memory by role of a team session (internal/lore), under the state directory, or nil for a
// session without a team or started with --no-memory.
func (s *Session) buildLore(team []TeamSlot) *lore.Store {
	if len(team) == 0 || s.opts.NoMemory {
		return nil
	}
	return lore.New(lore.ProjectDir(stateRoot(s.opts.Home), s.opts.Root))
}

// loreRoles appends each worker role's saved lore to its pin, read once at the start of the session so the role layer
// stays the same bytes for the whole session. The manager, the mailman and service roles have none. A file that cannot
// be read is reported and left out.
func (s *Session) loreRoles(st *lore.Store, roles swarm.Roles) swarm.Roles {
	for n, r := range roles {
		if n == "manager" || n == swarm.MailmanRoleName || ServiceRole(n) {
			continue
		}
		items, err := st.Load(n)
		if err != nil {
			s.Log.Emit("", "lore.load", map[string]any{"role": n, "error": err.Error()})
			continue
		}
		if block := lore.Render(n, items); block != "" {
			r.Pin += block
			roles[n] = r
			s.Log.Emit("", "lore.load", map[string]any{"role": n, "items": len(items)})
		}
	}
	return roles
}

// saveLore is the swarm's OnLearned: the role notes of an accepted task's workers become insights of their role.
func (s *Session) saveLore(st *lore.Store) func(role string, texts []string) {
	return func(role string, texts []string) {
		added, err := st.Save(role, texts)
		ev := map[string]any{"role": role, "notes": len(texts), "added": added}
		if err != nil {
			ev["error"] = err.Error()
		}
		s.Log.Emit("", "lore.save", ev)
	}
}
