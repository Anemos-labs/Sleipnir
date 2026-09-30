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
