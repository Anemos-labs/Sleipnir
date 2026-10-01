package core

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkBuildManifest measures the per-request cost of logging an exact prompt:
// hashing every message of a long thread (the delta encoding avoids storing them
// again, but the hash of each is still computed).
func BenchmarkBuildManifest(b *testing.B) {
	p := &Prompt{Model: "m", Tools: []ToolSpec{{Name: "t", InputSchema: []byte(`{"type":"object"}`)}}}
	p.System = []Block{Text(strings.Repeat("constitution ", 300))}
	p.Messages = []Message{{Role: RoleUser, Blocks: []Block{Text(strings.Repeat("pins ", 3000))}}}
	for i := 0; i < 100; i++ {
		p.Messages = append(p.Messages,
			Message{Role: RoleAssistant, Blocks: []Block{Text("step"), ToolUse(fmt.Sprintf("c%d", i), "bash", []byte(`{"command":"go test ./..."}`))}},
			Message{Role: RoleUser, Blocks: []Block{ToolResult(fmt.Sprintf("c%d", i), false, Text(strings.Repeat("output line\n", 150)))}})
	}
	store := memBlobs{}
	var st ManifestState
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, next, err := BuildManifest(p, st, fmt.Sprintf("a.%d", i), store.put)
		if err != nil {
			b.Fatal(err)
		}
		st = next
	}
}

// Canonical is what a tool's input schema and the arguments of every tool call pass through on their way into the prompt, and it must
// give the same bytes for the same meaning: it runs on every request, for every tool.
func BenchmarkCanonical(b *testing.B) {
	for name, raw := range map[string]string{
		"small call":  `{"command":"go test ./...","timeout":120}`,
		"schema":      `{"type":"object","properties":{"path":{"type":"string","description":"File to read"},"offset":{"type":"integer","description":"First line to read (1-based)"},"limit":{"type":"integer"}},"required":["path"],"additionalProperties":false}`,
		"edit call":   `{"path":"internal/kv/render.go","edits":[{"old":"` + strings.Repeat("a line of code\\n", 20) + `","new":"` + strings.Repeat("another line\\n", 22) + `"}]}`,
		"unsorted":    `{"z":1,"y":{"b":[3,2,1],"a":"x"},"x":[{"d":1,"c":2},{"f":3,"e":4}],"w":null,"v":true}`,
		"large array": `[` + strings.Repeat(`{"id":1,"name":"item","tags":["a","b"]},`, 200) + `{"id":2}]`,
	} {
		raw := []byte(raw)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := Canonical(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMarshalStable(b *testing.B) {
	v := map[string]any{"id": "toolu_01", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"go test ./..."}`}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := MarshalStable(v); err != nil {
			b.Fatal(err)
		}
	}
}
