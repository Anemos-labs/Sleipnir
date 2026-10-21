package main

// /team: the saved teams (loadouts) of this project and of the user, from the chat. The terminal and the page's chat run the same
// code; the page also has the team library (internal/web/ui/js/87c-ui-teams.js) over the /api/teams routes (web_teams.go).

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/teamfile"
)

// teamUsage is the usage line of /team.
const teamUsage = "usage: /team [list | save NAME [--project] [--force] | load NAME [--fresh] | show NAME | rm NAME [--project]]: the saved teams; save this team, start again as a saved one (--fresh: with an empty conversation), print one to share, or remove one"

// teamLoad is what /team load asks of the chat: to start again with typed (the saved team, slot by slot), empty when fresh.
type teamLoad struct {
	typed []string
	fresh bool
}

// teamSlash runs /team ARG for a session in dir (home: the user's home directory), writing what it says to out. loadout is the
// session's team as it would be saved (Session.Loadout, read under the caller's lock); working counts its workers at work by role
// (Session.WorkersAtWork), which are recovered into slots of their roles when the team starts again with its conversation. For
// load it returns the restart to make; nil otherwise.
func teamSlash(dir, home string, loadout func() ([]session.TeamSlot, map[string]string), working func() map[string]int, arg string, out io.Writer) *teamLoad {
	f := strings.Fields(arg)
	sub := "list"
	if len(f) > 0 {
		sub = strings.ToLower(f[0])
		f = f[1:]
	}
	var name, scope string
	force, fresh := false, false
	for _, a := range f {
		switch a {
		case "--fresh", "-fresh":
			fresh = true
		case "--project", "-project":
			scope = teamfile.ScopeProject
		case "--user", "-user":
			scope = teamfile.ScopeUser
		case "--force", "-force":
			force = true
		default:
			if name != "" || strings.HasPrefix(a, "-") {
				fmt.Fprintln(out, teamUsage)
				return nil
			}
			name = a
		}
	}
	if sub != "list" && sub != "ls" && name == "" {
		fmt.Fprintln(out, teamUsage)
		return nil
	}
	switch sub {
	case "list", "ls":
		printTeams(out, teamfile.List(dir, home))
	case "save":
		slots, models := loadout()
		if len(slots) == 0 {
			fmt.Fprintln(out, "team: a single agent has no team to save; start a team first (/swarm 8)")
			return nil
		}
		p, err := teamfile.Save(dir, home, scope, name, teamfile.FromTeam("", slots, models), force)
		switch {
		case errors.Is(err, teamfile.ErrExists):
			fmt.Fprintf(out, "team: %s is saved already (%s); /team save %s --force replaces it\n", name, tildePath(p), name)
		case err != nil:
			fmt.Fprintln(out, "team:", err)
		default:
			fmt.Fprintf(out, "saved the team (%d workers) as %s: %s\nstart with it: sleipnir chat --team %s, or /team load %s\n", len(slots), name, tildePath(p), name, name)
		}
	case "show", "export":
		tf, p, err := findTeam(name, dir, home)
		if err != nil {
			fmt.Fprintln(out, "team:", err)
			return nil
		}
		b, _ := tf.Marshal()
		fmt.Fprintf(out, "%s (%s): save it as .sleipnir/teams/NAME.json or ~/.sleipnir/teams/NAME.json to use it\n%s", name, tildePath(p), b)
	case "load", "use":
		tf, _, err := findTeam(name, dir, home)
		if err != nil {
			fmt.Fprintln(out, "team:", err)
			return nil
		}
		if !fresh {
			if why := keepsSlots(tf, working()); why != "" {
				fmt.Fprintf(out, "team: %s has %s; a worker at work keeps a slot of its role when the team starts again with its conversation. /team load %s --fresh starts again as %s with an empty conversation\n", name, why, name, name)
				return nil
			}
		}
		return &teamLoad{typed: loadoutFlags(tf), fresh: fresh}
	case "rm", "remove", "delete":
		if scope == "" {
			scope = scopeOf(name, dir, home)
		}
		if err := teamfile.Remove(dir, home, scope, name); err != nil {
			fmt.Fprintln(out, "team:", err)
			return nil
		}
		fmt.Fprintf(out, "removed the team %s (%s)\n", name, scope)
	default:
		fmt.Fprintln(out, teamUsage)
	}
	return nil
}

// keepsSlots says why the team tf cannot take over the workers at work (by role): a role with fewer slots in tf than workers at
// work ("2 backend slots for 4 at work"); "" when it can.
func keepsSlots(tf *teamfile.File, working map[string]int) string {
	slots, _ := tf.Team()
	have := map[string]int{}
	for _, t := range slots {
		have[t.Role]++
	}
	roles := make([]string, 0, len(working))
	for r := range working {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	var short []string
	for _, r := range roles {
		if working[r] > have[r] {
			short = append(short, fmt.Sprintf("%d %s slot%s for %d at work", have[r], r, map[bool]string{true: "", false: "s"}[have[r] == 1], working[r]))
		}
	}
	if len(short) == 0 {
		return ""
	}
	return strings.Join(short, ", ")
}

// findTeam finds and reads the saved team name (a name, or a path to a .json file).
func findTeam(name, dir, home string) (*teamfile.File, string, error) {
	p, err := teamfile.Find(name, dir, home)
	if err != nil {
		return nil, "", err
	}
	tf, err := teamfile.Load(p)
	return tf, p, err
}

// scopeOf is the scope --team NAME finds the team in: the project's when it has one of that name, else the user's.
func scopeOf(name, dir, home string) string {
	for _, e := range teamfile.List(dir, home) {
		if e.Name == name {
			return e.Scope
		}
	}
	return teamfile.ScopeUser
}

// printTeams lists the saved teams for /team.
func printTeams(out io.Writer, ts []teamfile.Entry) {
	if len(ts) == 0 {
		fmt.Fprintln(out, "no saved team: /team save NAME saves this one (in ~/.sleipnir/teams; --project: in this project's .sleipnir/teams)")
		return
	}
	fmt.Fprintln(out, "saved teams (/team load NAME starts again as one; /team show NAME prints it to share):")
	for _, e := range ts {
		what := ""
		switch {
		case e.Err != "":
			what = "unreadable: " + e.Err
		default:
			what = strconv.Itoa(len(e.File.Slots)) + " workers"
			if e.File.Description != "" {
				what += " · " + e.File.Description
			}
		}
		if e.Shadowed {
			what += " (hidden by the project's team of the same name)"
		}
		fmt.Fprintf(out, "  %-20s %-8s %s\n", e.Name, e.Scope, firstText(what, 90))
	}
}

// loadoutFlags are the flags that start a session as the team tf: its size, one --role-model per slot (role=model, or the role
// alone), and the models of its other roles.
func loadoutFlags(tf *teamfile.File) []string {
	slots, models := tf.Team()
	typed := []string{"--swarm", strconv.Itoa(len(slots))}
	roles := make([]string, 0, len(models))
	for r := range models {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		typed = append(typed, "--role-model", r+"="+models[r])
	}
	for _, t := range slots {
		v := t.Role
		if t.Model != "" {
			v += "=" + t.Model
		}
		typed = append(typed, "--role-model", v)
	}
	return typed
}
