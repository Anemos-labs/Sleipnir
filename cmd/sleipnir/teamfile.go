package main

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/teamfile"
)

// findTeamFile resolves --team v: a path (it has a separator or ends in .json) as it is, otherwise NAME.json in the project of
// dir (the --cwd of the command, else the working directory), then in ~/.sleipnir/teams (teamfile.Find).
func findTeamFile(v, dir string) (string, error) {
	p, err := teamfile.Find(v, dir, "")
	if err != nil {
		return "", fmt.Errorf("--team %w", err)
	}
	return p, nil
}

// loadTeamFile reads and checks the team file at p.
func loadTeamFile(p string) (*teamfile.File, error) {
	t, err := teamfile.Load(p)
	if err != nil {
		return nil, fmt.Errorf("--team: %w", err)
	}
	return t, nil
}

// teamFlag is --team NAME or PATH: a saved team, whose slots and models join the --role-model values as if they had been
// given there, the slots always as the team. The file is read when the values are split, after the whole command line is
// parsed, so that a NAME is looked for in the project of --cwd wherever --cwd stands on the line.
type teamFlag struct {
	roles *roleModelFlag
	cwd   *string // the command's --cwd; nil or "" is the working directory
	name  string
}

// String renders the team as it was given.
func (t *teamFlag) String() string {
	if t == nil {
		return ""
	}
	return t.name
}

// Set takes the team; its values are added, at the place --team stands among the --role-model values, when they are split.
func (t *teamFlag) Set(v string) error {
	if t.name != "" {
		return errors.New("--team is given once")
	}
	t.name = v
	at := len(t.roles.entries)
	t.roles.load = func() error { return t.add(strings.TrimSpace(v), at) }
	return nil
}

// add reads the team v and puts its values among the --role-model values, before the at-th one.
func (t *teamFlag) add(v string, at int) error {
	dir := ""
	if t.cwd != nil {
		dir = *t.cwd
	}
	p, err := findTeamFile(v, dir)
	if err != nil {
		return err
	}
	tf, err := loadTeamFile(p)
	if err != nil {
		return err
	}
	team := &roleModelFlag{}
	roles := make([]string, 0, len(tf.Models))
	for r := range tf.Models {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		if err := team.Set(r + "=" + tf.Models[r]); err != nil {
			return fmt.Errorf("--team %s: models.%s: %v", v, r, err)
		}
	}
	for _, s := range tf.Slots {
		if name, _, _ := strings.Cut(s, "="); serviceRole(strings.TrimSpace(name)) {
			return fmt.Errorf("--team %s: slot %q: %s is not a worker; give its model under models", v, s, strings.TrimSpace(name))
		}
		if err := team.Set(s); err != nil {
			return fmt.Errorf("--team %s: slot %q: %v", v, s, err)
		}
		team.entries[len(team.entries)-1].slot = true
	}
	t.roles.entries = slices.Concat(t.roles.entries[:at:at], team.entries, t.roles.entries[at:])
	return nil
}

// teamHelp is the help of --team.
const teamHelp = "a saved team: `NAME` (.sleipnir/teams/NAME.json in the project of --cwd, else ~/.sleipnir/teams/NAME.json) or a path to such a file: " +
	`{"slots": ["backend=provider/model", "backend", "frontend", ...], "models": {"manager": "provider/model"}}; ` +
	"its slots are the team (at least 8), and --role-model values given with it add to them"
