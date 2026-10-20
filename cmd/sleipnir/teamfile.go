package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// teamFile is a named team, saved for reuse: the slots of its workers (role=model, or a bare role on the role's model) in
// team order, and the models of the roles that are not workers (manager, compactor, judge, mailman, embedder, reranker).
//
//	{"description": "Eight builders on two endpoints",
//	 "slots": ["architect=heimdallm/glm", "backend=heimdallm/qwen-next", "backend", "frontend=heimdallm/m3", ...],
//	 "models": {"manager": "heimdallm/dsv41", "judge": "heimdallm/glm"}}
type teamFile struct {
	Description string            `json:"description,omitempty"`
	Slots       []string          `json:"slots"`
	Models      map[string]string `json:"models,omitempty"`
}

// maxTeamFileBytes bounds what a team file may hold.
const maxTeamFileBytes = 64 << 10

var teamName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// teamDirs are where a team named NAME is looked for, in order: the project's .sleipnir/teams (the project found from the
// working directory), then the user's ~/.sleipnir/teams.
func teamDirs() []string {
	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		if root, ok := config.FindRoot(wd); ok {
			dirs = append(dirs, filepath.Join(root, ".sleipnir", "teams"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".sleipnir", "teams"))
	}
	return dirs
}

// findTeamFile resolves --team v: a path (it has a separator or ends in .json) as it is, otherwise NAME.json in teamDirs.
func findTeamFile(v string) (string, error) {
	if strings.ContainsRune(v, os.PathSeparator) || strings.Contains(v, "/") || strings.HasSuffix(v, ".json") {
		return v, nil
	}
	if !teamName.MatchString(v) {
		return "", fmt.Errorf("--team %q: a team is a name (letters, digits, '-', '_', '.') or a path to a .json file", v)
	}
	dirs := teamDirs()
	for _, d := range dirs {
		p := filepath.Join(d, v+".json")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, nil
		}
	}
	var have []string
	for _, d := range dirs {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			if n, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() {
				have = append(have, n)
			}
		}
	}
	sort.Strings(have)
	msg := fmt.Sprintf("--team %s: no %s.json in %s", v, v, strings.Join(dirs, " or "))
	if len(have) > 0 {
		msg += " (teams there: " + strings.Join(have, ", ") + ")"
	}
	return "", errors.New(msg)
}

// loadTeamFile reads and checks the team file at p.
func loadTeamFile(p string) (*teamFile, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("--team: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxTeamFileBytes {
		return nil, fmt.Errorf("--team: %s is not a regular file of at most %d bytes", p, maxTeamFileBytes)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("--team: %w", err)
	}
	var t teamFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("--team: %s: %v (want {\"slots\": [\"role=model\", \"role\", ...], \"models\": {\"manager\": \"model\"}})", p, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("--team: %s holds more than one JSON value", p)
	}
	if len(t.Slots) == 0 {
		return nil, fmt.Errorf("--team: %s has no slots", p)
	}
	for role := range t.Models {
		if !serviceRole(role) {
			return nil, fmt.Errorf("--team: %s: models.%s: a worker's model goes in its slot (%s=<model>); models is for the manager, compactor, judge, mailman, embedder and reranker", p, role, role)
		}
	}
	return &t, nil
}

// teamFlag is --team NAME or PATH: a saved team, whose slots and models join the --role-model values as if they had been
// given there, the slots always as the team.
type teamFlag struct {
	roles *roleModelFlag
	name  string
}

// String renders the team as it was given.
func (t *teamFlag) String() string {
	if t == nil {
		return ""
	}
	return t.name
}

// Set loads the team and adds its values.
func (t *teamFlag) Set(v string) error {
	if t.name != "" {
		return errors.New("--team is given once")
	}
	p, err := findTeamFile(strings.TrimSpace(v))
	if err != nil {
		return err
	}
	tf, err := loadTeamFile(p)
	if err != nil {
		return err
	}
	t.name = v
	roles := make([]string, 0, len(tf.Models))
	for r := range tf.Models {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		if err := t.roles.Set(r + "=" + tf.Models[r]); err != nil {
			return fmt.Errorf("--team %s: models.%s: %v", v, r, err)
		}
	}
	for _, s := range tf.Slots {
		if name, _, _ := strings.Cut(s, "="); serviceRole(strings.TrimSpace(name)) {
			return fmt.Errorf("--team %s: slot %q: %s is not a worker; give its model under models", v, s, strings.TrimSpace(name))
		}
		if err := t.roles.Set(s); err != nil {
			return fmt.Errorf("--team %s: slot %q: %v", v, s, err)
		}
		t.roles.entries[len(t.roles.entries)-1].slot = true
	}
	return nil
}

// teamHelp is the help of --team.
const teamHelp = "a saved team: `NAME` (.sleipnir/teams/NAME.json in the project, else ~/.sleipnir/teams/NAME.json) or a path to such a file: " +
	`{"slots": ["backend=provider/model", "backend", "frontend", ...], "models": {"manager": "provider/model"}}; ` +
	"its slots are the team (at least 8), and --role-model values given with it add to them"
