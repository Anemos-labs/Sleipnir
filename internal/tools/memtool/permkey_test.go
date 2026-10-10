package memtool

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// The memory tool's permission request carries the call's input: two notes asked about
// at once are two questions, not one answer for both.
func TestPermissionRequestCarriesTheInput(t *testing.T) {
	tool := New(filepath.Join(t.TempDir(), "MEMORY.md"))
	rec := &recording{allow: true}
	for _, text := range []string{"note A", "note B"} {
		if r := run(t, tool, rec, map[string]any{"action": "add", "text": text}); r.IsError {
			t.Fatal(r.Text)
		}
	}
	if len(rec.seen) != 2 {
		t.Fatalf("requests: %+v", rec.seen)
	}
	for i, text := range []string{"note A", "note B"} {
		var in map[string]any
		if err := json.Unmarshal(rec.seen[i].Input, &in); err != nil || in["text"] != text {
			t.Fatalf("request %d input %q (%v)", i, rec.seen[i].Input, err)
		}
	}
}
