package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// roleModelFlag is --role-model, repeatable, in the order given. It has two uses:
//
//   - role=model, each role at most once: the model that role runs on; the team is the
//     default one (one worker of each role).
//   - a role given more than once, or a bare role: the team itself. Every entry is one
//     worker slot (role=model on that model, a bare role on the role's model), and the
//     roles not listed have no slot: --role-model backend --role-model backend
//     --role-model frontend=x … is a team of two backends and a frontend on x.
//
// The manager, the compactor, the judge and the mailman are never workers: they only take a model, once, in either use.
type roleModelFlag struct {
	entries []roleEntry
	load    func() error // --team's values, added by split (teamFlag.Set)
}

// roleEntry is one --role-model value; model is "" for a bare role. slot marks a value from a team file (--team), which is
// always a slot of the team.
type roleEntry struct {
	role, model string
	slot        bool
}

// serviceRole reports whether role takes a model but is never a team slot: the manager, which leads the team, and the harness's
// compactor, judge and mailman.
func serviceRole(role string) bool {
	return role == "manager" || session.ServiceRole(role) || role == swarm.MailmanRoleName
}

// String renders the values as they were given.
func (r *roleModelFlag) String() string {
	if r == nil {
		return ""
	}
	parts := make([]string, len(r.entries))
	for i, e := range r.entries {
		parts[i] = e.role
		if e.model != "" {
			parts[i] += "=" + e.model
		}
	}
	return strings.Join(parts, " ")
}

// Set adds one value: role=model, or a bare role (one team slot on the role's model).
func (r *roleModelFlag) Set(v string) error {
	name, val, hasEq := strings.Cut(strings.TrimSpace(v), "=")
	name = strings.TrimSpace(name)
	if name == "" || (hasEq && strings.TrimSpace(val) == "") {
		return fmt.Errorf("want role=model, or a role alone for one more worker of it, got %q", v)
	}
	if serviceRole(name) && !hasEq {
		return fmt.Errorf("%s takes a model (%s=<model>); it is not a worker of the team", name, name)
	}
	r.entries = append(r.entries, roleEntry{role: name, model: strings.TrimSpace(val)})
	return nil
}

// split reads the values: the models of roles (the first use, and the service roles
// always) and the team (the second use; nil when the values define none).
func (r *roleModelFlag) split() (map[string]string, []session.TeamSlot, error) {
	if r != nil && r.load != nil {
		load := r.load
		r.load = nil
		if err := load(); err != nil {
			return nil, nil, err
		}
	}
	if r == nil || len(r.entries) == 0 {
		return nil, nil, nil
	}
	seen := map[string]int{}
	team := false
	for _, e := range r.entries {
		seen[e.role]++
		if serviceRole(e.role) {
			if seen[e.role] > 1 {
				return nil, nil, fmt.Errorf("--role-model %s is given more than once; it is not a worker of the team and takes one model", e.role)
			}
			continue
		}
		if e.model == "" || seen[e.role] > 1 || e.slot {
			team = true
		}
	}
	models := map[string]string{}
	var slots []session.TeamSlot
	for _, e := range r.entries {
		switch {
		case serviceRole(e.role) || !team:
			models[e.role] = e.model
		default:
			slots = append(slots, session.TeamSlot{Role: e.role, Model: e.model})
		}
	}
	if len(models) == 0 {
		models = nil
	}
	if team && len(slots) < session.MinTeamWorkers {
		return nil, nil, fmt.Errorf("--role-model: a team has at least %d workers (one of each role is the default); this one has %d", session.MinTeamWorkers, len(slots))
	}
	return models, slots, nil
}

// teamWorkers reconciles --swarm with a team named by --role-model: the team decides
// the number of workers, and an explicit --swarm must agree with it.
func teamWorkers(team []session.TeamSlot, n int, given bool) (int, error) {
	if len(team) == 0 {
		return n, nil
	}
	if given && n != len(team) {
		if n == 0 {
			return 0, errors.New("--swarm 0 is a single agent, but --role-model names a team; drop one of them")
		}
		return 0, fmt.Errorf("--swarm %d does not match the team --role-model names (%d workers); drop --swarm or make them agree", n, len(team))
	}
	return len(team), nil
}

// checkTeamSize refuses a team size a session would not take, before anything else is asked (a model, a key): --swarm is 0 (a
// single agent) or from session.MinTeamWorkers up to the ceiling (swarm.max_workers, else session.DefaultMaxTeamWorkers).
func checkTeamSize(n int) error {
	if n < 0 {
		return fmt.Errorf("--swarm %d: the number of workers is 0 (a single agent) or at least %d", n, session.MinTeamWorkers)
	}
	if n > session.DefaultMaxTeamWorkers {
		cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: true})
		if err != nil {
			cfg = nil
		}
		if ceil := session.TeamCeiling(cfg); n > ceil {
			return fmt.Errorf("--swarm %d: a session has at most %d workers; ask for fewer, or raise swarm.max_workers in your configuration", n, ceil)
		}
	}
	if n > 0 && n < session.MinTeamWorkers {
		return fmt.Errorf("--swarm %d: a team has at least %d workers, one of each role (--swarm 0 is a single agent); to leave a role out, name the team with --role-model", n, session.MinTeamWorkers)
	}
	return nil
}
