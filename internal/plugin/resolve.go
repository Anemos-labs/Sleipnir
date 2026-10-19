package plugin

import (
	"fmt"
	"sort"
	"strings"
)

// Layers that decide a plugin's state, lowest precedence first.
const (
	LayerDefault = "default" // the manifest
	LayerProfile = "profile" // the chosen profile
	LayerUser    = "user"    // ~/.sleipnir/config.json
	LayerProject = "project" // <project>/.sleipnir/config.json
	LayerLocal   = "local"   // <project>/.sleipnir/config.local.json
	LayerEnv     = "env"     // SLEIPNIR_* variables
	LayerFlag    = "flag"    // command-line flags and the session's own options
)

var layerRank = map[string]int{LayerDefault: 0, LayerProfile: 1, LayerUser: 2, LayerProject: 3, LayerLocal: 4, LayerEnv: 5, LayerFlag: 6}

// LayerRank orders layers: a higher rank wins. Unknown layers rank as user.
func LayerRank(layer string) int {
	if r, ok := layerRank[layer]; ok {
		return r
	}
	return layerRank[LayerUser]
}

// Choice is one explicit on/off decision and the layer that made it.
type Choice struct {
	Enabled bool
	Layer   string
}

// Input is what Resolve composes: a profile and the explicit choices. Several
// choices may be given for one id (a plugin row and an older setting such as
// swarm.mailman); the one from the highest layer wins, a later one on a tie.
type Input struct {
	Profile  string
	Choices  map[string][]Choice
	Settings map[string]map[string]any
}

// Add appends a choice for id.
func (in *Input) Add(id string, enabled bool, layer string) {
	if in.Choices == nil {
		in.Choices = map[string][]Choice{}
	}
	in.Choices[id] = append(in.Choices[id], Choice{Enabled: enabled, Layer: layer})
}

// States of a plugin.
const (
	StateOn      = "on"
	StateOff     = "off"
	StatePending = "pending" // on, but a requirement is not
	StateError   = "error"   // on, but it cannot be activated (a cycle, a failed activation)
	StateRestart = "restart" // changed since the session started; applies when the team starts again
)

// Status is a plugin's resolved state.
type Status struct {
	Manifest
	Default bool           `json:"default"`
	Enabled bool           `json:"enabled"`
	State   string         `json:"state"`
	Reason  string         `json:"reason,omitempty"`
	Layer   string         `json:"layer"`
	Config  map[string]any `json:"config,omitempty"`
}

// Set is the resolved plugin set of a configuration. The zero Set and a nil
// *Set report every plugin as on (an older configuration knows no plugins).
type Set struct {
	Profile string
	list    []*Status
	byID    map[string]*Status
	order   []string // activation order: requirements first
}

// Resolve composes the catalogue with a profile and explicit choices. It
// returns the set and warnings: unknown ids (with a "did you mean"), an
// unknown profile, requirement cycles.
func Resolve(cat []Manifest, in Input) (*Set, []string) {
	var warns []string
	prof, ok := ProfileByID(in.Profile)
	if in.Profile == "" {
		prof, _ = ProfileByID(DefaultProfile)
	} else if !ok {
		warns = append(warns, fmt.Sprintf("plugins: unknown profile %q (known: %s); using %s", in.Profile, strings.Join(profileIDs(), ", "), DefaultProfile))
		prof, _ = ProfileByID(DefaultProfile)
	}
	s := &Set{Profile: prof.ID, byID: map[string]*Status{}}
	ids := make([]string, 0, len(cat))
	for _, m := range cat {
		ids = append(ids, m.ID)
	}
	for _, m := range cat {
		st := &Status{Manifest: m, Layer: LayerDefault}
		st.Default = m.DefaultOn()
		if !m.Core && profileDecides(prof, m.ID) {
			st.Default = profileOn(prof, m.ID, st.Default)
			st.Layer = LayerProfile
		}
		st.Enabled = st.Default
		if !m.Core {
			best := -1
			for _, c := range in.Choices[m.ID] {
				if r := LayerRank(c.Layer); r >= best {
					best, st.Enabled, st.Layer = r, c.Enabled, c.Layer
				}
			}
		}
		if m.Core {
			st.Enabled = true
		}
		if cfg := in.Settings[m.ID]; len(cfg) > 0 {
			st.Config = cfg
		}
		s.list = append(s.list, st)
		s.byID[m.ID] = st
	}
	for id := range in.Choices {
		if _, ok := s.byID[id]; !ok {
			warns = append(warns, unknownID(id, ids))
		}
	}
	for id := range in.Settings {
		if _, ok := s.byID[id]; !ok && len(in.Choices[id]) == 0 {
			warns = append(warns, unknownID(id, ids))
		}
	}
	sort.Strings(warns)
	warns = append(warns, s.order0()...)
	s.settle()
	return s, warns
}

// profileIDs lists the built-in profile ids.
func profileIDs() []string {
	var out []string
	for _, p := range Profiles() {
		out = append(out, p.ID)
	}
	return out
}

// profileDecides reports whether the profile says anything about id.
func profileDecides(p Profile, id string) bool {
	return len(p.Only) > 0 || contains(p.On, id) || contains(p.Off, id)
}

// profileOn is id's state under the profile, def when the profile is silent.
func profileOn(p Profile, id string, def bool) bool {
	switch {
	case len(p.Only) > 0:
		return contains(p.Only, id)
	case contains(p.Off, id):
		return false
	case contains(p.On, id):
		return true
	}
	return def
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// unknownID words a warning about an id no plugin has, with the closest known id.
func unknownID(id string, known []string) string {
	msg := fmt.Sprintf("plugins: no plugin %q", id)
	if near := closest(id, known); near != "" {
		msg += fmt.Sprintf(" (did you mean %q?)", near)
	}
	return msg + "; `sleipnir plugins` lists them"
}

// order0 computes the activation order (requirements first, ids in order
// within a level) and marks plugins in a requirement cycle as errors.
func (s *Set) order0() []string {
	provider := map[string]string{} // service -> plugin id
	for _, st := range s.list {
		provider[st.ID] = st.ID
		for _, p := range st.Provides {
			if _, taken := provider[p]; !taken {
				provider[p] = st.ID
			}
		}
	}
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var warns []string
	var visit func(id string, path []string) bool
	visit = func(id string, path []string) bool {
		switch color[id] {
		case black:
			return true
		case grey:
			cyc := append(append([]string(nil), path...), id)
			warns = append(warns, "plugins: requirement cycle "+strings.Join(cyc, " -> "))
			return false
		}
		color[id] = grey
		ok := true
		st := s.byID[id]
		reqs := append([]string(nil), st.Requires...)
		sort.Strings(reqs)
		for _, r := range reqs {
			pid, known := provider[r]
			if !known {
				continue
			}
			if !visit(pid, append(path, id)) {
				ok = false
			}
		}
		color[id] = black
		if !ok && !st.Core {
			st.State, st.Reason = StateError, "part of a requirement cycle"
		}
		s.order = append(s.order, id)
		return ok
	}
	ids := make([]string, 0, len(s.list))
	for _, st := range s.list {
		ids = append(ids, st.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		visit(id, nil)
	}
	return warns
}

// settle fills each plugin's state in activation order: a plugin that is on
// is pending while one of its requirements is not on.
func (s *Set) settle() {
	provided := func(name string) (*Status, bool) {
		if st, ok := s.byID[name]; ok {
			return st, true
		}
		for _, id := range s.order {
			if contains(s.byID[id].Provides, name) && s.byID[id].State == StateOn {
				return s.byID[id], true
			}
		}
		return nil, false
	}
	for _, id := range s.order {
		st := s.byID[id]
		if st.State == StateError {
			continue
		}
		if !st.Enabled {
			st.State = StateOff
			continue
		}
		st.State = StateOn
		for _, r := range st.Requires {
			dep, ok := provided(r)
			switch {
			case !ok:
				st.State, st.Reason = StatePending, "needs "+r+", which is not installed"
			case dep.State != StateOn:
				st.State, st.Reason = StatePending, "needs "+r+", which is "+dep.State
			}
			if st.State == StatePending {
				break
			}
		}
	}
}

// On reports whether the plugin is active: enabled, with every requirement
// met. A nil Set, and an id the catalogue does not know, report true, so code
// that asks about a plugin keeps working without a resolved set.
func (s *Set) On(id string) bool {
	if s == nil {
		return true
	}
	st, ok := s.byID[id]
	if !ok {
		return true
	}
	return st.State == StateOn
}

// Get returns the status of one plugin.
func (s *Set) Get(id string) (*Status, bool) {
	if s == nil {
		return nil, false
	}
	st, ok := s.byID[id]
	return st, ok
}

// List returns every plugin's status in catalogue order.
func (s *Set) List() []*Status {
	if s == nil {
		return nil
	}
	return append([]*Status(nil), s.list...)
}

// Order returns plugin ids in activation order: requirements first.
func (s *Set) Order() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.order...)
}

// Off lists the ids of plugins that are not on (off, pending or in error), sorted.
func (s *Set) Off() []string {
	var out []string
	for _, st := range s.List() {
		if st.State != StateOn {
			out = append(out, st.ID)
		}
	}
	sort.Strings(out)
	return out
}

// MarkRestart marks every plugin whose enabled state differs between the
// running set and next (what the configuration now says) as waiting for a
// restart, on a copy of next. It reports whether any did.
func MarkRestart(running, next *Set) (*Set, bool) {
	if running == nil || next == nil {
		return next, false
	}
	out := &Set{Profile: next.Profile, byID: map[string]*Status{}, order: next.Order()}
	any := false
	for _, st := range next.list {
		c := *st
		if r, ok := running.byID[st.ID]; ok && (r.State == StateOn) != (st.State == StateOn) && !st.Live {
			c.Reason = fmt.Sprintf("turns %s when the team starts again", map[bool]string{true: "on", false: "off"}[st.State == StateOn])
			c.State = StateRestart
			any = true
		}
		out.list = append(out.list, &c)
		out.byID[c.ID] = &c
	}
	return out, any
}

// closest returns the known id nearest to s by edit distance, if near enough.
func closest(s string, known []string) string {
	best, bestD := "", 1<<30
	for _, k := range known {
		if d := editDistance(s, k); d < bestD {
			best, bestD = k, d
		}
	}
	if bestD <= max(2, len(s)/3) {
		return best
	}
	return ""
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
