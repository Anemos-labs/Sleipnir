package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// expandRefs resolves the local $defs references compactSchema makes, so a
// compacted schema can be compared with what it came from.
func expandRefs(v any, defs map[string]any) any {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok && len(x) == 1 && strings.HasPrefix(ref, "#/$defs/shared_") {
			return expandRefs(defs[strings.TrimPrefix(ref, "#/$defs/")], defs)
		}
		out := map[string]any{}
		for k, e := range x {
			if k != "$defs" {
				out[k] = expandRefs(e, defs)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = expandRefs(e, defs)
		}
		return out
	}
	return v
}

func decodeMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCompactSchemaSharesRepeatedSubschemas(t *testing.T) {
	capture := wideSchema(8)
	in := `{"type":"object","properties":{"left":` + capture + `,"right":` + capture + `,"examples":{"type":"string","examples":["x"]},` +
		`"modes":{"type":"array","items":{"anyOf":[` + capture + `,{"type":"null"}]}}},"examples":[{"left":{}}]}`
	orig := decodeMap(t, in)
	out, ok := compactSchema(decodeMap(t, in))
	if !ok {
		t.Fatal("nothing shared")
	}
	b, _ := json.Marshal(out)
	if len(b) >= len(in)/2 {
		t.Errorf("%d bytes from %d", len(b), len(in))
	}
	defs := out["$defs"].(map[string]any)
	if len(defs) != 1 || defs["shared_1"] == nil {
		t.Fatalf("defs = %v", defs)
	}
	// The property named "examples" stays; the keyword goes.
	props := out["properties"].(map[string]any)
	if _, kept := props["examples"]; !kept {
		t.Error("a property named examples was dropped")
	}
	if _, kept := out["examples"]; kept {
		t.Error("the examples keyword was kept")
	}
	// Resolving the references gives back the original, examples aside.
	want := dropExamples(orig, false)
	if got := expandRefs(out, defs); !reflect.DeepEqual(got, want) {
		t.Errorf("compaction changed the schema:\n got %v\nwant %v", got, want)
	}
	// Deterministic.
	again, _ := compactSchema(decodeMap(t, in))
	b2, _ := json.Marshal(again)
	if string(b) != string(b2) {
		t.Error("not deterministic")
	}
}

func TestCompactSchemaLeavesWhatItCannotSafelyRewrite(t *testing.T) {
	capture := wideSchema(8)
	pointer := `{"type":"object","properties":{"a":` + capture + `,"b":` + capture + `,"c":{"$ref":"#/properties/a"}}}`
	if _, ok := compactSchema(decodeMap(t, pointer)); ok {
		t.Error("a pointer outside $defs must stop compaction")
	}
	// A dangling reference never comes to point at a new definition.
	dangling := `{"type":"object","properties":{"a":` + capture + `,"b":` + capture + `,"c":{"$ref":"#/$defs/shared_1"}}}`
	if out, ok := compactSchema(decodeMap(t, dangling)); !ok {
		t.Error("nothing shared")
	} else if _, taken := out["$defs"].(map[string]any)["shared_1"]; taken {
		t.Error("a new definition took the name a dangling $ref points to")
	}
	// A "$ref" inside literal data is data, and a property named const is a schema.
	if _, ok := compactSchema(decodeMap(t, `{"type":"object","properties":{"a":`+capture+`,"b":`+capture+`,"c":{"default":{"$ref":"#/properties/a"}}}}`)); !ok {
		t.Error("a $ref-shaped default must not stop compaction")
	}
	if _, ok := compactSchema(decodeMap(t, `{"type":"object","properties":{"a":`+capture+`,"b":`+capture+`,"const":{"$ref":"#/properties/a"}}}`)); ok {
		t.Error("a pointer in a property named const must stop compaction")
	}
	if _, ok := compactSchema(decodeMap(t, `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`)); ok {
		t.Error("small repeats are not worth a definition")
	}
	// Existing definitions stay where they are and keep their names.
	withDefs := `{"type":"object","$defs":{"shared_1":` + capture + `,"cap":` + capture + `},"properties":{"a":{"$ref":"#/$defs/cap"},"b":` + capture + `}}`
	out, ok := compactSchema(decodeMap(t, withDefs))
	if !ok {
		t.Fatal("nothing shared")
	}
	defs := out["$defs"].(map[string]any)
	if _, ok := defs["cap"].(map[string]any)["properties"]; !ok {
		t.Errorf("a definition was replaced by a reference: %v", defs["cap"])
	}
	if _, ok := defs["shared_2"]; !ok {
		t.Errorf("the new definition must not take an existing name: %v", defs)
	}
}

func TestNormalizeCompactsOnlyWhenAllowedAndOverTheLimit(t *testing.T) {
	capture := wideSchema(40)
	in := json.RawMessage(`{"type":"object","properties":{"left":` + capture + `,"right":` + capture + `}}`)
	full, err := normalizeSchemaWithin(in, schemaLimits{bytes: 0, depth: 64, nodes: 20000, compact: true})
	if err != nil || full.compacted {
		t.Fatalf("under the limit nothing changes: %v %v", err, full.compacted)
	}
	limit := len(full.schema) * 2 / 3
	if _, err := normalizeSchemaWithin(in, schemaLimits{bytes: limit, depth: 64, nodes: 20000}); err == nil {
		t.Error("without compact the schema is refused")
	}
	n, err := normalizeSchemaWithin(in, schemaLimits{bytes: limit, depth: 64, nodes: 20000, compact: true})
	if err != nil || !n.compacted || len(n.schema) > limit || n.inline(0) {
		t.Errorf("err=%v compacted=%v %d bytes", err, n.compacted, len(n.schema))
	}
}

func TestCompactSchemaLeavesLiteralsAlone(t *testing.T) {
	capture := wideSchema(8)
	var big map[string]any
	_ = json.Unmarshal([]byte(capture), &big) // a large object used as data
	lit, _ := json.Marshal(big)
	in := `{"type":"object","properties":{"a":{"type":"object","description":"A","default":` + string(lit) + `,"examples":[1]},` +
		`"b":{"type":"object","default":` + string(lit) + `},"c":{"const":{"examples":[1],"x":` + string(lit) + `}},"d":` + capture + `,"e":` + capture + `}}`
	out, ok := compactSchema(decodeMap(t, in))
	if !ok {
		t.Fatal("nothing shared")
	}
	props := out["properties"].(map[string]any)
	for _, p := range []string{"a", "b"} {
		got, _ := json.Marshal(props[p].(map[string]any)["default"])
		if string(got) != string(lit) {
			t.Errorf("%s default changed: %s", p, got)
		}
	}
	c := props["c"].(map[string]any)["const"].(map[string]any)
	if _, kept := c["examples"]; !kept {
		t.Error("data inside const was edited")
	}
	if got, _ := json.Marshal(c["x"]); string(got) != string(lit) {
		t.Errorf("const changed: %s", got)
	}
	if _, ok := props["d"].(map[string]any)["$ref"]; !ok {
		t.Error("the repeated subschema was not shared")
	}
}
