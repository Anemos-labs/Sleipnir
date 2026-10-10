package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// The effective configuration says what a setting is; the report (Report.Origins) says which layer supplied it last. Neither says
// where each element of a merged list came from: a deny list holds the user's rules and the rules a project added to them, and a page
// that lists the rules in force has to name the file of each. The per-layer view below replays the merge of one list over the layers
// Load read, element by element, with Load's own rules (lists replace, a repository's deny and ask lists and hooks only add, null
// removes).

// LayerValues is one configuration layer as Load applied it.
type LayerValues struct {
	// Kind is user, project, local, env or overrides (the built-in defaults are not a layer: Defaults has them).
	Kind string
	// Source is the file path, "env:NAME" for one environment variable, or "overrides".
	Source string
	// Values are the layer's settings after Load's checks: values of the wrong type dropped, a project's user-only settings removed,
	// and, when the project is untrusted, its security-sensitive settings removed too. Shaped like the file; a copy.
	Values map[string]any
}

// LoadLayers reads the layers Load would merge for opts, lowest precedence first, with Load's checks applied, and the report Load
// gives. It fails as Load fails, so a caller never shows layers of a configuration that would not start.
func LoadLayers(opts LoadOpts) ([]LayerValues, *Report, error) {
	_, rep, layers, err := load(opts)
	if err != nil {
		return nil, rep, err
	}
	out := make([]LayerValues, 0, len(layers))
	for _, l := range layers {
		v, _ := deepCopy(l.tree).(map[string]any)
		out = append(out, LayerValues{Kind: l.kind, Source: l.source, Values: v})
	}
	return out, rep, nil
}

// ListValue is one element of a merged list and the layer that put it there.
type ListValue struct {
	Value any
	// Kind and Source name the layer as LayerValues does.
	Kind, Source string
}

// listOrigins replays the merge of the list at segs over layers and returns its elements with their layers, in the order the merge
// leaves them. It applies mergeObject's rules: an absent key changes nothing, null removes, a list replaces, and a list that a
// repository may only add to (additive) gains the elements it does not already hold.
func listOrigins(layers []*layer, segs []string) []ListValue {
	var cur []ListValue
	for _, l := range layers {
		v, present, cleared := lookupPath(l.tree, segs)
		if cleared {
			cur = nil
			continue
		}
		if !present {
			continue
		}
		repo := l.kind == "project" || l.kind == "local"
		if repo && additive(segs) {
			add, _ := v.([]any)
			for _, a := range add {
				dup := false
				for _, c := range cur {
					if reflect.DeepEqual(c.Value, a) {
						dup = true
						break
					}
				}
				if !dup {
					cur = append(cur, ListValue{Value: deepCopy(a), Kind: l.kind, Source: l.source})
				}
			}
			continue
		}
		if v == nil {
			cur = nil
			continue
		}
		items, ok := v.([]any)
		if !ok {
			cur = []ListValue{{Value: deepCopy(v), Kind: l.kind, Source: l.source}} // not a list: it replaces as a whole
			continue
		}
		cur = cur[:0:0]
		for _, it := range items {
			cur = append(cur, ListValue{Value: deepCopy(it), Kind: l.kind, Source: l.source})
		}
	}
	return cur
}

// lookupPath finds the value at segs in a layer's tree. present says the layer has the key; cleared says that a parent of it is null
// in this layer, which removes everything below it (a null on the key itself is present with a nil value).
func lookupPath(tree map[string]any, segs []string) (v any, present, cleared bool) {
	cur := tree
	for i, s := range segs {
		val, ok := cur[s]
		if !ok {
			return nil, false, false
		}
		if i == len(segs)-1 {
			return val, true, false
		}
		if val == nil {
			return nil, false, true
		}
		next, ok := val.(map[string]any)
		if !ok {
			return nil, false, true // a scalar where a section was: the section is replaced
		}
		cur = next
	}
	return nil, false, false
}

// ListOrigins loads the configuration for opts and returns the elements of the list at the path segs (for example "permissions",
// "deny", or "hooks", "PreToolUse") with the layer that supplied each. It fails as Load fails.
func ListOrigins(opts LoadOpts, segs ...string) ([]ListValue, error) {
	_, _, layers, err := load(opts)
	if err != nil {
		return nil, err
	}
	return listOrigins(layers, segs), nil
}

// RuleOrigin is one permission rule of the effective configuration with the layer and file that supplied it.
type RuleOrigin struct {
	// Effect is allow, deny or ask.
	Effect string
	// Rule is the rule as written ("Bash(go test:*)").
	Rule string
	// Role is empty for a rule of every agent, or the swarm role it is for (permissions.roles.<role>).
	Role string
	// Layer is the kind of layer: user, project, local, env or overrides.
	Layer string
	// File is the layer's source: a file path, "env:NAME" or "overrides".
	File string
}

// RuleOrigins lists every permission rule of the effective configuration for opts with its origin: deny, then ask, then allow (the
// order the engine judges them in), each in the order of the merged list, then the rules of each role (roles by name). A rule that
// several layers name keeps the first layer that supplied it. It fails as Load fails.
func RuleOrigins(opts LoadOpts) ([]RuleOrigin, error) {
	cfg, _, layers, err := load(opts)
	if err != nil {
		return nil, err
	}
	var out []RuleOrigin
	add := func(effect, role string, want []string, segs []string) error {
		got := listOrigins(layers, segs)
		if len(got) != len(want) {
			return fmt.Errorf("config: the origins of %s do not match the merged list (%d rules, %d traced)", fmtPath(segs), len(want), len(got))
		}
		for i, g := range got {
			s, _ := g.Value.(string)
			if s != want[i] {
				return fmt.Errorf("config: the origins of %s do not match the merged list at %d", fmtPath(segs), i)
			}
			out = append(out, RuleOrigin{Effect: effect, Rule: s, Role: role, Layer: g.Kind, File: g.Source})
		}
		return nil
	}
	p := cfg.Permissions
	for _, e := range []struct {
		effect string
		rules  []string
	}{{"deny", p.Deny}, {"ask", p.Ask}, {"allow", p.Allow}} {
		if err := add(e.effect, "", e.rules, []string{"permissions", e.effect}); err != nil {
			return nil, err
		}
	}
	roles := make([]string, 0, len(p.Roles))
	for r := range p.Roles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		rp := p.Roles[r]
		for _, e := range []struct {
			effect string
			rules  []string
		}{{"deny", rp.Deny}, {"ask", rp.Ask}, {"allow", rp.Allow}} {
			if err := add(e.effect, r, e.rules, []string{"permissions", "roles", r, e.effect}); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// LayerKind says which kind of layer a source names: "defaults", "user", "project", "local", "env" or "overrides", or "" for a
// source the report does not list. Environment sources are "env:NAME".
func (r *Report) LayerKind(source string) string {
	if r == nil || source == "" {
		return ""
	}
	if strings.HasPrefix(source, "env:") {
		return "env"
	}
	for i := len(r.Layers) - 1; i >= 0; i-- {
		if r.Layers[i].Source == source {
			return r.Layers[i].Kind
		}
	}
	return ""
}
