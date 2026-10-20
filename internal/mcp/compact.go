package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// compactSchema shrinks a schema without changing what it accepts, for a tool
// loaded on demand whose schema is over the limit. Generated schemas often
// repeat whole subschemas (the same capture shape for "left" and "right", the
// same options object under every mode); each subschema that occurs more than
// once and is large enough to be worth it moves to $defs and is replaced by a
// local $ref. "examples" are dropped too: they illustrate, they do not
// constrain.
//
// It only rewrites schemas whose $refs all point into $defs or definitions,
// because a JSON pointer into any other place could stop resolving once the
// tree around its target changes. The result is deterministic: keys are visited
// in sorted order and definitions are numbered in the order they are met.
// ok is false when nothing was shared.
func compactSchema(root map[string]any) (out map[string]any, ok bool) {
	refs := map[string]bool{}
	if !refsAreLocalDefs(root, false, refs) {
		return nil, false
	}
	root = dropExamples(root, false).(map[string]any)
	c := &compactor{counts: map[string]int{}, ids: map[string]string{}, refs: refs}
	c.count(root, false)
	c.done = true
	defs, _ := root["$defs"].(map[string]any)
	if defs == nil {
		defs = map[string]any{}
	}
	c.defs = defs
	out = c.rewrite(root, true, false).(map[string]any)
	if len(c.ids) == 0 {
		return nil, false
	}
	out["$defs"] = c.defs
	return out, true
}

// minSharedBytes is the smallest subschema worth a $defs entry: smaller ones
// cost about as much as the reference that replaces them.
const minSharedBytes = 200

// nameMaps are the keywords whose value maps names (not keywords) to schemas.
var nameMaps = map[string]bool{"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true}

// literals are the keywords whose value is JSON data, not a schema: it is never
// shared and never edited.
var literals = map[string]bool{"const": true, "enum": true, "default": true}

type compactor struct {
	counts map[string]int    // canonical text of a subschema -> occurrences
	ids    map[string]string // canonical text -> its $defs name
	defs   map[string]any
	next   int
	done   bool            // counting is over: count only computes the text
	refs   map[string]bool // $defs names the schema refers to, defined or not: never reused
}

// count records how often each subschema occurs (until done) and returns v's
// canonical text.
// names says v maps names to schemas, so v itself is not a schema.
func (c *compactor) count(v any, names bool) string {
	switch x := v.(type) {
	case map[string]any:
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range sortedKeys(x) {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			if !names && literals[k] {
				lb, _ := json.Marshal(x[k]) // sorted keys: deterministic
				b.Write(lb)
				continue
			}
			b.WriteString(c.count(x[k], !names && nameMaps[k]))
		}
		b.WriteByte('}')
		s := b.String()
		if !names && len(s) >= minSharedBytes && !c.done {
			c.counts[s]++
		}
		return s
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = c.count(e, false)
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(x)
		return strings.TrimSuffix(buf.String(), "\n")
	}
}

// rewrite replaces every shared subschema below v by a $ref to its definition.
// top marks a node that must stay where it is: the root, or a definition's own
// body; names as in count.
func (c *compactor) rewrite(v any, top, names bool) any {
	switch x := v.(type) {
	case map[string]any:
		if !top && !names {
			key := c.count(x, false)
			if c.counts[key] > 1 {
				id, seen := c.ids[key]
				if !seen {
					id = c.newID()
					c.ids[key] = id
					c.defs[id] = c.rewrite(x, true, false)
				}
				return map[string]any{"$ref": "#/$defs/" + id}
			}
		}
		out := make(map[string]any, len(x))
		for _, k := range sortedKeys(x) {
			if !names && literals[k] {
				out[k] = x[k]
				continue
			}
			// A definition is the target of references: it stays put, though what
			// is inside it may be shared.
			out[k] = c.rewrite(x[k], names && top, !names && nameMaps[k])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = c.rewrite(e, false, false)
		}
		return out
	}
	return v
}

// newID is the next free $defs name.
func (c *compactor) newID() string {
	for {
		c.next++
		id := fmt.Sprintf("shared_%d", c.next)
		if _, taken := c.defs[id]; !taken && !c.refs[id] {
			return id
		}
	}
}

// refsAreLocalDefs reports whether every $ref in v points into $defs or
// definitions (a "$ref" inside literal data is data), and records in refs the
// $defs names they point to; names as in count.
func refsAreLocalDefs(v any, names bool, refs map[string]bool) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if !names && literals[k] {
				continue
			}
			if s, ok := e.(string); ok && k == "$ref" {
				if name, ok := strings.CutPrefix(s, "#/$defs/"); ok {
					refs[strings.SplitN(name, "/", 2)[0]] = true
				} else if !strings.HasPrefix(s, "#/definitions/") {
					return false
				}
			}
			if !refsAreLocalDefs(e, !names && nameMaps[k], refs) {
				return false
			}
		}
	case []any:
		for _, e := range x {
			if !refsAreLocalDefs(e, false, refs) {
				return false
			}
		}
	}
	return true
}

// dropExamples removes the "examples" keyword everywhere (a property that is
// named "examples" stays); names as in count.
func dropExamples(v any, names bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if k == "examples" && !names {
				continue
			}
			if !names && literals[k] {
				out[k] = e
				continue
			}
			out[k] = dropExamples(e, !names && nameMaps[k])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = dropExamples(e, false)
		}
		return out
	}
	return v
}
