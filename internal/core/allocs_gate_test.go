//go:build !race

package core

import (
	"strings"
	"testing"
)

// Canonical runs on every tool schema and every tool call on the way into a prompt; its allocations are held to what they are, with a
// fifth to spare (measured: 21, 79 and 41). Time is BenchmarkCanonical's; the count does not depend on the machine.
func TestCanonicalAllocationsAreHeld(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		max  float64
	}{
		{"small call", `{"command":"go test ./...","timeout":120}`, 26},
		{"schema", `{"type":"object","properties":{"path":{"type":"string","description":"File to read"},"offset":{"type":"integer","description":"First line to read (1-based)"},"limit":{"type":"integer"}},"required":["path"],"additionalProperties":false}`, 95},
		{"edit call", `{"path":"internal/kv/render.go","edits":[{"old":"` + strings.Repeat("a line of code\\n", 20) + `","new":"` + strings.Repeat("another line\\n", 22) + `"}]}`, 50},
	} {
		raw := []byte(c.raw)
		got := testing.AllocsPerRun(30, func() { _, _ = Canonical(raw) })
		if got > c.max {
			t.Errorf("Canonical of a %s allocates %.0f times, more than the %.0f it is held to", c.name, got, c.max)
		}
	}
}
