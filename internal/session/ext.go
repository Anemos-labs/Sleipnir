package session

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/agentdefs"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/skills"
	"github.com/reee344/sleipnir/internal/swarm"
)

// Extensions are the user-written parts of a harness: skills and role
// definitions (markdown files), discovered once when the session starts. They are
// snapshots: what an agent writes into .sleipnir/ mid-session changes nothing
// until the next session, and the permission engine asks before it writes there
// at all (see protectedConfigDirs).
type extensions struct {
	skills   *skills.Catalog
	defs     []agentdefs.Def
	roles    swarm.Roles
	profiles map[string]perm.RoleProfile
	warnings []string
}

// skillListingTokens bounds the always-present listing of skills in the shared
// layer: every request of every agent reads it.
const skillListingTokens = skills.DefaultListingTokens

// loadExtensions discovers skills and role definitions. Repository-supplied ones
// are read only when the project is trusted.
func (s *Session) loadExtensions() {
	o := s.opts
	ext := &extensions{profiles: map[string]perm.RoleProfile{}}
	s.ext = ext

	cat, warns := skills.Discover(skills.Opts{Root: o.Root, Home: o.Home, TrustProject: o.TrustProject})
	ext.skills = cat
	for _, w := range warns {
		ext.warnings = append(ext.warnings, "skills: "+w.String())
	}

	roles := swarm.Roles{}
	for n, r := range o.Roles {
		roles[n] = r
	}
	if o.Roles == nil {
		roles = swarm.BuiltinRoles()
	}
	if !o.Swarm {
		ext.roles = roles
		return
	}
	var shorts []string
	for _, r := range roles {
		shorts = append(shorts, r.Short)
	}
	sort.Strings(shorts)
	defs, dwarns := agentdefs.Load(agentdefs.Opts{
		Root: o.Root, Home: o.Home, TrustProject: o.TrustProject,
		Reserved: roles.Names(), ReservedShorts: shorts,
	})
	for _, w := range dwarns {
		ext.warnings = append(ext.warnings, "agents: "+w.String())
	}
	ext.defs = defs
	for _, d := range defs {
		roles[d.Name] = swarm.Role(d.ToRole())
		if p := d.Profile(); p.Mode != "" || len(p.Deny) > 0 || len(p.Allow) > 0 || len(p.Ask) > 0 {
			ext.profiles[d.Name] = p
		}
	}
	if m, ok := roles["manager"]; ok && len(defs) > 0 {
		m.Pin += managerRoleList(defs)
		roles["manager"] = m
	}
	ext.roles = roles
}

// managerRoleList tells the manager which project-defined roles it can spawn. It
// goes into the manager's own role pin, so the shared layers stay identical for
// everyone else.
func managerRoleList(defs []agentdefs.Def) string {
	var b strings.Builder
	b.WriteString("\n\nRoles this project defines (spawn them like the built-in ones):")
	for _, d := range defs {
		fmt.Fprintf(&b, "\n- %s: %s", d.Name, d.Description)
		if d.ReadOnly {
			b.WriteString(" (read-only)")
		}
	}
	return b.String()
}

// skillsSegment is the skills listing as a shared-layer segment; empty when
// there is nothing the model may load.
func (e *extensions) skillsSegment() string {
	if e == nil {
		return ""
	}
	listing := e.skills.Listing(skillListingTokens, core.NewBytesEstimator())
	if strings.TrimSpace(listing) == "" {
		return ""
	}
	return "<skills>\nLoad one with the skill tool when its description matches the task.\n" + listing + "\n</skills>"
}

// protectedConfigDirs are the places whose contents run as code or steer
// every later session: hooks, skills, commands and agent definitions live in
// them, and so does the project's configuration. An agent that can write there
// can make its next session do anything, so writes ask (in every mode, bypass
// included) and an unattended session refuses.
var protectedConfigDirs = []string{
	"Edit(./.sleipnir/**)", "Edit(./.claude/**)",
	"Edit(./.git/hooks/**)", "Edit(./.git/config)",
	"Edit(~/.sleipnir/**)", "Edit(~/.claude/**)",
}
