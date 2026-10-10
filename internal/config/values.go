package config

import (
	"encoding/json"
	"sort"
)

// Value is one effective setting as a page that shows the configuration lists it.
type Value struct {
	// Key is the setting's path as Report.Origins spells it ("swarm.isolation", "providers.heimdall.base_url", "mcp.github").
	Key string
	// Value is the effective value, redacted by Redact's rules.
	Value any
	// Layer is the kind of layer that supplied it: defaults, user, project, local, env or overrides.
	Layer string
	// File is that layer's source: a file path, "env:NAME" or "overrides"; "" for the built-in defaults.
	File string
	// Below are the values that lower layers set for the same key and that the winning layer replaced, redacted, by layer kind; nil
	// when there are none.
	Below map[string]any
}

// Values lists the effective configuration for opts, one row per setting, sorted by key: every leaf of the configuration (a list,
// an hook event and an MCP server are one value each), redacted, with the layer that supplied it and what lower layers said. It
// fails as Load fails and returns the report either way.
func Values(opts LoadOpts) ([]Value, *Report, error) {
	cfg, rep, layers, err := load(opts)
	if err != nil {
		return nil, rep, err
	}
	var out []Value
	flatten(nil, Redact(cfg), func(segs []string, v any) {
		row := Value{Key: fmtPath(segs), Value: v, Layer: "defaults"}
		if src := originOf(rep.Origins, segs); src != "" {
			row.File = src
			if k := rep.LayerKind(src); k != "" {
				row.Layer = k
			}
			for _, l := range layers {
				if l.source == src {
					break
				}
				if lv, present, _ := lookupPath(l.tree, segs); present && lv != nil {
					if row.Below == nil {
						row.Below = map[string]any{}
					}
					row.Below[l.kind] = plainJSON(RedactAt(segs, lv))
				}
			}
		}
		out = append(out, row)
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, rep, nil
}

// flatten calls fn for every leaf of a configuration tree: a scalar, a list, an empty section, or a value the merge treats as one
// unit (an unknown top-level key, a hook event, an MCP server).
func flatten(segs []string, v any, fn func([]string, any)) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 || (len(segs) > 0 && opaque(segs)) {
		if len(segs) > 0 {
			fn(segs, v)
		}
		return
	}
	for _, k := range sortedKeys(m) {
		flatten(cloneSegs(segs, k), m[k], fn)
	}
}

// originOf finds the source the report names for the value at segs, or for the nearest enclosing value that has one.
func originOf(origins map[string]string, segs []string) string {
	for n := len(segs); n > 0; n-- {
		if s, ok := origins[fmtPath(segs[:n])]; ok {
			return s
		}
	}
	return ""
}

// plainJSON gives a value the types encoding/json decodes to (float64 numbers), so that values of a layer and of the merged
// configuration compare and print alike.
func plainJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}
