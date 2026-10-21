// Package teamfile reads, lists, writes and removes saved teams ("loadouts"): a team's worker slots in order, and the models of
// the roles that are not workers, kept as a small JSON file under a project's .sleipnir/teams or the user's ~/.sleipnir/teams.
//
// The file is also the form a team is shared in: its bytes are what an export prints, and what an import reads.
//
//	{"description": "Eight builders on two endpoints",
//	 "slots": ["architect=heimdallm/glm", "backend=heimdallm/qwen-next", "backend", "frontend=heimdallm/m3", ...],
//	 "models": {"manager": "heimdallm/dsv41", "judge": "heimdallm/glm"}}
//
// A slot is role=model, or a bare role that runs on the role's model. models holds the manager, compactor, judge, mailman,
// embedder and reranker, which are never slots.
package teamfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// File is a saved team.
type File struct {
	Description string            `json:"description,omitempty"`
	Slots       []string          `json:"slots"`
	Models      map[string]string `json:"models,omitempty"`
}

// MaxBytes bounds what a team file may hold.
const MaxBytes = 64 << 10

// MaxSlots bounds the slots a team file may name; the session's own ceiling (swarm.max_workers) is checked when the team starts.
const MaxSlots = 256

// maxDescription bounds a team's description, in bytes.
const maxDescription = 500

// Scopes of a saved team: the project's .sleipnir/teams, or the user's ~/.sleipnir/teams.
const (
	ScopeProject = "project"
	ScopeUser    = "user"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidName reports whether n can name a saved team: letters, digits, '-', '_' and '.', starting with a letter or a digit, at
// most 64 characters.
func ValidName(n string) bool { return nameRE.MatchString(n) && !strings.HasSuffix(n, ".json") }

// ServiceRole reports whether role takes a model but is never a slot of a team: the manager, which leads the team, and the
// harness's compactor, judge, mailman, embedder and reranker.
func ServiceRole(role string) bool {
	return role == "manager" || session.ServiceRole(role) || role == swarm.MailmanRoleName
}

// Dirs are the directories saved teams are kept in: the project's .sleipnir/teams (the project found from dir, else from the
// working directory; "" outside a project) and the user's (home/.sleipnir/teams; home "" is the user's home directory, and
// "" when there is none).
func Dirs(dir, home string) (project, user string) {
	wd, err := filepath.Abs(dir)
	if dir == "" {
		wd, err = os.Getwd()
	}
	if err == nil {
		if root, ok := config.FindRoot(wd); ok {
			project = filepath.Join(root, ".sleipnir", "teams")
		}
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		user = filepath.Join(home, ".sleipnir", "teams")
	}
	return project, user
}

// dirOf is the directory of scope, or an error when the scope is unknown or has none here.
func dirOf(dir, home, scope string) (string, error) {
	project, user := Dirs(dir, home)
	switch scope {
	case ScopeProject:
		if project == "" {
			return "", errors.New("the folder is not in a project (no .git or .sleipnir above it): save the team for yourself instead")
		}
		return project, nil
	case ScopeUser, "":
		if user == "" {
			return "", errors.New("no home directory to save the team in")
		}
		return user, nil
	}
	return "", fmt.Errorf("a team is saved for the project or for yourself (%s or %s), not %q", ScopeProject, ScopeUser, scope)
}

// Parse reads and checks a team file's bytes: one JSON object with no unknown fields, at least one slot, every slot a worker
// role (role=model or a bare role) and every key of models a role that is not a worker.
func Parse(b []byte) (*File, error) {
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("a team file holds at most %d bytes", MaxBytes)
	}
	var f File
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%v (want {\"slots\": [\"role=model\", \"role\", ...], \"models\": {\"manager\": \"model\"}})", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("it holds more than one JSON value")
	}
	if err := f.Check(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Check reports what makes f no team: no slots, too many, a slot that is not role=model or a bare worker role, a model under
// models for a worker role, or an overlong description.
func (f *File) Check() error {
	if len(f.Slots) == 0 {
		return errors.New("it has no slots")
	}
	if len(f.Slots) > MaxSlots {
		return fmt.Errorf("it has %d slots; a team file names at most %d", len(f.Slots), MaxSlots)
	}
	if len(f.Description) > maxDescription {
		return fmt.Errorf("its description is longer than %d bytes", maxDescription)
	}
	for _, s := range f.Slots {
		role, model, hasEq := strings.Cut(strings.TrimSpace(s), "=")
		role, model = strings.TrimSpace(role), strings.TrimSpace(model)
		if role == "" || hasEq && model == "" {
			return fmt.Errorf("slot %q: want role=model, or a role alone", s)
		}
		if ServiceRole(role) {
			return fmt.Errorf("slot %q: %s is not a worker; give its model under models", s, role)
		}
	}
	for role, model := range f.Models {
		if !ServiceRole(role) {
			return fmt.Errorf("models.%s: a worker's model goes in its slot (%s=<model>); models is for the manager, compactor, judge, mailman, embedder and reranker", role, role)
		}
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("models.%s: no model", role)
		}
	}
	return nil
}

// Load reads and checks the team file at p.
func Load(p string) (*File, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxBytes {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", p, MaxBytes)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	f, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return f, nil
}

// Find resolves a team named v: a path (it has a separator or ends in .json) as it is, otherwise NAME.json in the project's
// directory, then the user's (Dirs). A name that is not found lists the teams there are.
func Find(v, dir, home string) (string, error) {
	if strings.ContainsRune(v, os.PathSeparator) || strings.Contains(v, "/") || strings.HasSuffix(v, ".json") {
		return v, nil
	}
	if !ValidName(v) {
		return "", fmt.Errorf("%q: a team is a name (letters, digits, '-', '_', '.') or a path to a .json file", v)
	}
	project, user := Dirs(dir, home)
	var dirs []string
	for _, d := range []string{project, user} {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	for _, d := range dirs {
		p := filepath.Join(d, v+".json")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, nil
		}
	}
	var have []string
	for _, e := range List(dir, home) {
		have = append(have, e.Name)
	}
	have = uniq(have)
	msg := fmt.Sprintf("%s: no %s.json in %s", v, v, strings.Join(dirs, " or "))
	if len(have) > 0 {
		msg += " (teams there: " + strings.Join(have, ", ") + ")"
	}
	return "", errors.New(msg)
}

// uniq sorts s and drops repeats.
func uniq(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// Entry is one saved team as List finds it.
type Entry struct {
	Name  string `json:"name"`
	Scope string `json:"scope"` // ScopeProject or ScopeUser
	Path  string `json:"path"`
	// Shadowed is true for a team of the user's that a project team of the same name hides from --team NAME.
	Shadowed bool  `json:"shadowed,omitempty"`
	File     *File `json:"team,omitempty"`
	// Err says why the file is no team (it is listed so that it can be removed); File is then nil.
	Err string `json:"error,omitempty"`
}

// List is every saved team of the project of dir and of the user, the project's first, each scope by name.
func List(dir, home string) []Entry {
	project, user := Dirs(dir, home)
	var out []Entry
	seen := map[string]bool{}
	for _, sd := range []struct{ scope, dir string }{{ScopeProject, project}, {ScopeUser, user}} {
		if sd.dir == "" {
			continue
		}
		ents, _ := os.ReadDir(sd.dir)
		for _, e := range ents {
			name, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || e.IsDir() || !ValidName(name) {
				continue
			}
			en := Entry{Name: name, Scope: sd.scope, Path: filepath.Join(sd.dir, e.Name()), Shadowed: seen[name]}
			seen[name] = true
			if f, err := Load(en.Path); err != nil {
				en.Err = strings.TrimPrefix(err.Error(), en.Path+": ")
			} else {
				en.File = f
			}
			out = append(out, en)
		}
		if project == user { // a project at the home directory: one directory, listed once
			break
		}
	}
	return out
}

// ErrExists is returned by Save when a team of that name is saved there already and overwrite is false.
var ErrExists = errors.New("a team of that name is saved there already")

// Save writes f as the team name of scope (Dirs) and returns its path. A team of that name is replaced only when overwrite is
// set. The file is written whole, then renamed into place.
func Save(dir, home, scope, name string, f *File, overwrite bool) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("%q: a team's name is letters, digits, '-', '_' and '.', at most 64, starting with a letter or a digit", name)
	}
	if err := f.Check(); err != nil {
		return "", err
	}
	d, err := dirOf(dir, home, scope)
	if err != nil {
		return "", err
	}
	b, err := f.Marshal()
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, name+".json")
	if fi, err := os.Lstat(p); err == nil {
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", p)
		}
		if !overwrite {
			return p, ErrExists
		}
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(d, "."+name+".*.tmp")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return p, nil
}

// Remove deletes the team name of scope.
func Remove(dir, home, scope, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%q is not a team's name", name)
	}
	d, err := dirOf(dir, home, scope)
	if err != nil {
		return err
	}
	p := filepath.Join(d, name+".json")
	fi, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no team %s is saved for the %s", name, map[string]string{ScopeProject: "project", ScopeUser: "user"}[orUser(scope)])
		}
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", p)
	}
	return os.Remove(p)
}

// orUser is scope, with "" read as ScopeUser.
func orUser(scope string) string {
	if scope == "" {
		return ScopeUser
	}
	return scope
}

// Marshal is f as it is saved and shared: indented JSON with a final newline (the keys of models in order).
func (f *File) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// FromTeam is the team file of a team: its slots in order (role=model, or the role alone when the slot names no model) and the
// models of the roles that are not workers (worker roles in models are left out: a slot carries its model).
func FromTeam(description string, slots []session.TeamSlot, models map[string]string) *File {
	f := &File{Description: strings.TrimSpace(description)}
	for _, s := range slots {
		v := s.Role
		if s.Model != "" {
			v += "=" + s.Model
		}
		f.Slots = append(f.Slots, v)
	}
	for role, m := range models {
		if ServiceRole(role) && strings.TrimSpace(m) != "" {
			if f.Models == nil {
				f.Models = map[string]string{}
			}
			f.Models[role] = strings.TrimSpace(m)
		}
	}
	return f
}

// Team is f as a team: its slots ({role, model}, model "" for a bare role) and the models of its other roles.
func (f *File) Team() ([]session.TeamSlot, map[string]string) {
	slots := make([]session.TeamSlot, 0, len(f.Slots))
	for _, s := range f.Slots {
		role, model, _ := strings.Cut(strings.TrimSpace(s), "=")
		slots = append(slots, session.TeamSlot{Role: strings.TrimSpace(role), Model: strings.TrimSpace(model)})
	}
	var models map[string]string
	for role, m := range f.Models {
		if models == nil {
			models = map[string]string{}
		}
		models[role] = m
	}
	return slots, models
}
