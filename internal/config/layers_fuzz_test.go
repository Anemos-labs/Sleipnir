package config

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// layerGen draws random configuration layers that stress the merge of lists: nulls and scalars over sections, empty lists, lists with
// duplicates, roles, hooks.
type layerGen struct{ r *rand.Rand }

// rules is the pool list elements come from (few, so that duplicates and overlaps are common).
var fuzzRules = []string{"Bash(rm:*)", "Read(./.env)", "Edit(docs/**)", "Bash(git push:*)"}

// list is a random list of rules, duplicates likely.
func (g layerGen) list() []any {
	out := []any{}
	for i := g.r.IntN(4); i > 0; i-- {
		out = append(out, fuzzRules[g.r.IntN(len(fuzzRules))])
	}
	return out
}

// value is what a random layer puts at a key: absent (ok false), null, a scalar (a type error, rarely), or what fill makes.
func (g layerGen) value(fill func() any) (any, bool) {
	switch n := g.r.IntN(20); {
	case n < 5:
		return nil, false
	case n < 8:
		return nil, true
	case n < 9:
		return "scalar", true
	default:
		return fill(), true
	}
}

// rulesSection is a random permissions-like section: deny, ask, allow.
func (g layerGen) rulesSection() any {
	m := map[string]any{}
	for _, k := range []string{"deny", "ask", "allow"} {
		if v, ok := g.value(func() any { return g.list() }); ok {
			m[k] = v
		}
	}
	return m
}

// doc is a random layer.
func (g layerGen) doc() map[string]any {
	d := map[string]any{}
	if v, ok := g.value(func() any {
		m := g.rulesSection().(map[string]any)
		if roles, ok := g.value(func() any {
			rs := map[string]any{}
			if r, ok := g.value(g.rulesSection); ok {
				rs["tester"] = r
			}
			return rs
		}); ok {
			m["roles"] = roles
		}
		return m
	}); ok {
		d["permissions"] = v
	}
	if v, ok := g.value(func() any {
		hs := map[string]any{}
		if l, ok := g.value(func() any {
			out := []any{}
			for i := g.r.IntN(3); i > 0; i-- {
				out = append(out, map[string]any{"matcher": fuzzRules[g.r.IntN(2)], "hooks": []any{map[string]any{"type": "command", "command": "x.sh"}}})
			}
			return out
		}); ok {
			hs["PreToolUse"] = l
		}
		return hs
	}); ok {
		d["hooks"] = v
	}
	return d
}

// TestRuleOriginsAgreeWithLoadOverRandomLayers draws thousands of user, project and local layers (and sometimes the environment and
// the flags), trusted or not, and checks that the traced lists are exactly Load's merged lists, element for element and in order,
// that every element is attributed to a layer that holds it, and that RuleOrigins fails exactly when Load does.
func TestRuleOriginsAgreeWithLoadOverRandomLayers(t *testing.T) {
	g := layerGen{rand.New(rand.NewPCG(20261010, 4))}
	home, root := t.TempDir(), t.TempDir()
	for _, d := range []string{filepath.Join(home, ".sleipnir"), filepath.Join(root, ".sleipnir")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	valid := 0
	for i := 0; i < 2500; i++ {
		docs := map[string]map[string]any{
			UserConfigPath(home): g.doc(), ProjectConfigPath(root): g.doc(), LocalConfigPath(root): g.doc(),
		}
		for path, d := range docs {
			b, _ := json.Marshal(d)
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		o := LoadOpts{Home: home, Root: root, UntrustedProject: g.r.IntN(2) == 0, Environ: func() []string { return nil }}
		if g.r.IntN(6) == 0 {
			o.Environ = func() []string { return []string{"SLEIPNIR_PERMISSIONS_DENY=Bash(make:*),Bash(make:*)"} }
		}
		if g.r.IntN(6) == 0 {
			o.Overrides = map[string]any{"permissions": map[string]any{"ask": []any{"Edit(x/**)"}}}
		}
		cfg, _, lerr := Load(o)
		origins, oerr := RuleOrigins(o)
		hooks, herr := ListOrigins(o, "hooks", "PreToolUse")
		if (lerr == nil) != (oerr == nil) || (lerr == nil) != (herr == nil) {
			t.Fatalf("case %d: Load %v, RuleOrigins %v, ListOrigins %v\nlayers %v", i, lerr, oerr, herr, docs)
		}
		if lerr != nil {
			continue
		}
		valid++
		layers, _, _ := LoadLayers(o)
		holds := func(src string, segs []string, v any) bool {
			for _, l := range layers {
				if l.Source != src {
					continue
				}
				lv, present, _ := lookupPath(l.Values, segs)
				if items, ok := lv.([]any); present && ok {
					for _, it := range items {
						if reflect.DeepEqual(it, v) {
							return true
						}
					}
				}
			}
			return false
		}
		check := func(effect, role string, want []string, segs []string) {
			var got []string
			for _, ro := range origins {
				if ro.Effect == effect && ro.Role == role {
					got = append(got, ro.Rule)
					if !holds(ro.File, segs, ro.Rule) {
						t.Errorf("case %d: %s %s is attributed to %s, which does not hold it\nlayers %v", i, fmtPath(segs), ro.Rule, ro.File, docs)
					}
				}
			}
			if !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
				t.Fatalf("case %d: %s traced %q, merged %q\nlayers %v", i, fmtPath(segs), got, want, docs)
			}
		}
		p := cfg.Permissions
		check("deny", "", p.Deny, []string{"permissions", "deny"})
		check("ask", "", p.Ask, []string{"permissions", "ask"})
		check("allow", "", p.Allow, []string{"permissions", "allow"})
		rp := p.Roles["tester"]
		check("deny", "tester", rp.Deny, []string{"permissions", "roles", "tester", "deny"})
		check("ask", "tester", rp.Ask, []string{"permissions", "roles", "tester", "ask"})
		check("allow", "tester", rp.Allow, []string{"permissions", "roles", "tester", "allow"})
		var merged any
		if raw := cfg.Hooks["PreToolUse"]; raw != nil {
			if err := json.Unmarshal(raw, &merged); err != nil {
				t.Fatal(err)
			}
		}
		var traced any
		if len(hooks) == 1 {
			if _, isList := merged.([]any); !isList {
				traced = hooks[0].Value // not a list: one value
			}
		}
		if traced == nil && len(hooks) > 0 {
			var items []any
			for _, h := range hooks {
				items = append(items, h.Value)
			}
			traced = items
		}
		if fmt.Sprint(traced) != fmt.Sprint(merged) && !(traced == nil && fmt.Sprint(merged) == "[]") {
			t.Fatalf("case %d: hooks traced %v, merged %v\nlayers %v", i, traced, merged, docs)
		}
	}
	if valid < 500 {
		t.Errorf("only %d of the random configurations load: the generator tests too little", valid)
	}
}
