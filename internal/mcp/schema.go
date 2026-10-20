package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// A tool's input schema is sent to the model with every request of every
// agent, and it is written by the server. normalizeSchema turns whatever the
// server sent into something safe and stable to put in the shared prefix:
//
//   - every string is stripped of invisible and control characters (a property
//     description is as good an injection channel as the tool description);
//   - description-like strings are capped, other strings are never cut (a
//     truncated enum value or regex would make the model send something the
//     server rejects) and are rejected when absurdly long;
//   - keys are never rewritten: a property name is what the tool call must
//     echo, so one with invisible characters rejects the tool;
//   - remote $ref (anything but a "#" pointer) is rejected, since nothing
//     downstream should ever dereference a URL from a server;
//   - depth, node count and byte size are bounded;
//   - the result is canonical JSON (core.Canonical: sorted keys, no
//     whitespace), so equal schemas are equal bytes and the cache prefix does
//     not change when a server merely reorders keys.
//
// The top level must describe an object (that is what MCP requires and what
// providers accept for function parameters); missing "type" and "properties"
// are supplied.
//
// A tool that is loaded on demand (see deferred.go) reaches the model only in
// a tool result, never in the shared prefix, so its schema may be larger and
// deeper; every other rule is the same.
const (
	maxSchemaDepth  = 24
	maxSchemaNodes  = 4000
	maxSchemaDesc   = 300
	maxSchemaString = 500

	maxDeferredSchemaDepth = 64
	maxDeferredSchemaNodes = 20000
)

var emptyObjectSchema = json.RawMessage(`{"properties":{},"type":"object"}`)

type schemaWalker struct {
	nodes     int
	injection string
	maxDepth  int // the limits of this walk
	maxNodes  int
	deepest   int // the deepest level seen
	compact   bool
	compacted bool
}

// normalizeSchema returns the canonical, sanitised form of raw, and why it
// looks like an injection attempt if any of its strings do ("" otherwise).
func normalizeSchema(raw json.RawMessage, maxBytes int) (schema json.RawMessage, injection string, err error) {
	n, err := normalizeSchemaWithin(raw, schemaLimits{bytes: maxBytes, depth: maxSchemaDepth, nodes: maxSchemaNodes})
	return n.schema, n.injection, err
}

// schemaLimits bound one normalisation; a zero bytes means no byte limit.
// compact lets a schema over the byte limit be compacted (see compactSchema)
// before it is refused.
type schemaLimits struct {
	bytes, depth, nodes int
	compact             bool
}

// normalized is a schema normalizeSchemaWithin accepted, with what it measured.
type normalized struct {
	schema    json.RawMessage
	injection string
	depth     int // nesting depth, as maxSchemaDepth counts it
	nodes     int
	compacted bool // shared subschemas were moved to $defs
}

// inline reports whether the schema fits the limits of a tool sent in the
// shared prefix.
func (n normalized) inline(maxBytes int) bool {
	return !n.compacted && n.depth <= maxSchemaDepth && n.nodes <= maxSchemaNodes && (maxBytes <= 0 || len(n.schema) <= maxBytes)
}

// normalizeSchemaWithin is normalizeSchema under explicit limits.
func normalizeSchemaWithin(raw json.RawMessage, lim schemaLimits) (normalized, error) {
	w := &schemaWalker{maxDepth: lim.depth, maxNodes: lim.nodes, compact: lim.compact}
	schema, injection, err := w.normalize(raw, lim.bytes)
	if err != nil {
		return normalized{}, err
	}
	return normalized{schema: schema, injection: injection, depth: w.deepest, nodes: w.nodes, compacted: w.compacted}, nil
}

// normalize is the body of normalizeSchema for one walker.
func (w *schemaWalker) normalize(raw json.RawMessage, maxBytes int) (schema json.RawMessage, injection string, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return append(json.RawMessage(nil), emptyObjectSchema...), "", nil
	}
	if len(raw) > maxSchemaDecode {
		return nil, "", fmt.Errorf("input schema is larger than %d bytes", maxSchemaDecode)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, "", errors.New("input schema is not valid JSON")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, "", errors.New("input schema is not a JSON object")
	}
	cleaned, err := w.clean(obj, 0, "")
	if err != nil {
		return nil, "", err
	}
	obj = cleaned.(map[string]any)
	delete(obj, "$schema") // dialect noise: tokens for nothing, and a URL in the prefix
	switch t := obj["type"].(type) {
	case nil:
		obj["type"] = "object"
	case string:
		if t != "object" {
			return nil, "", fmt.Errorf("input schema describes %q, not an object", clipForError(t))
		}
	default:
		return nil, "", errors.New("input schema has a non-string type")
	}
	if _, ok := obj["properties"]; !ok {
		obj["properties"] = map[string]any{}
	}
	out, err := core.MarshalStable(obj)
	if err != nil {
		return nil, "", errors.New("input schema cannot be encoded")
	}
	canon, err := core.Canonical(out)
	if err != nil {
		return nil, "", errors.New("input schema cannot be canonicalised")
	}
	if maxBytes > 0 && len(canon) > maxBytes && w.compact {
		if small, ok := compactSchema(obj); ok {
			if out, err := core.MarshalStable(small); err == nil {
				if c, err := core.Canonical(out); err == nil && len(c) <= maxBytes {
					w.compacted = true
					return c, w.injection, nil
				}
			}
		}
	}
	if maxBytes > 0 && len(canon) > maxBytes {
		return nil, "", fmt.Errorf("input schema is %d bytes, over the %d byte limit", len(canon), maxBytes)
	}
	return canon, w.injection, nil
}

// descriptionKeys hold prose for the model; only these are length-capped.
var descriptionKeys = map[string]bool{"description": true, "title": true, "$comment": true, "markdownDescription": true}

func (w *schemaWalker) clean(v any, depth int, parentKey string) (any, error) {
	if w.nodes++; w.nodes > w.maxNodes {
		return nil, fmt.Errorf("input schema has more than %d nodes", w.maxNodes)
	}
	if depth > w.maxDepth {
		return nil, fmt.Errorf("input schema is nested deeper than %d levels", w.maxDepth)
	}
	w.deepest = max(w.deepest, depth)
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for _, k := range sortedKeys(x) { // sorted: a stable first error and first injection match
			val := x[k]
			if cleanStrict(k) != k || strings.ContainsAny(k, "\n\t") {
				return nil, errors.New("input schema has a key with control or invisible characters")
			}
			if k == "$ref" {
				if s, ok := val.(string); ok && !strings.HasPrefix(s, "#") {
					return nil, errors.New("input schema has a remote $ref")
				}
			}
			cv, err := w.clean(val, depth+1, k)
			if err != nil {
				return nil, err
			}
			out[k] = cv
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			cv, err := w.clean(val, depth+1, parentKey)
			if err != nil {
				return nil, err
			}
			out[i] = cv
		}
		return out, nil
	case string:
		s := cleanStrict(x)
		if descriptionKeys[parentKey] {
			s = tidy(s)
			if w.injection == "" {
				w.injection = detectInjection(s)
			}
			s, _ = truncateRunes(s, maxSchemaDesc)
			return s, nil
		}
		if len(s) > maxSchemaString {
			return nil, fmt.Errorf("input schema has a %q string longer than %d bytes", clipForError(parentKey), maxSchemaString)
		}
		if w.injection == "" {
			w.injection = detectInjection(s)
		}
		return s, nil
	}
	return v, nil // numbers, booleans, null
}
