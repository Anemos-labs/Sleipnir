//go:build !race

package perm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Every tool call is judged before it runs; the allocations of the judgement are held to what they are, with a fifth to spare
// (measured: 107 for a read-only command, 269 for a pipeline of three, 71 for a read inside the workspace). Time is
// BenchmarkCheckBash's, and does not fail a build.
func TestCheckAllocationsAreHeld(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal/kv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/kv/render.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(Config{Mode: ModeAcceptEdits, Root: root, Home: t.TempDir(), Allow: []string{"Bash(go test:*)"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		req  Request
		max  float64
	}{
		{"read-only command", Request{Agent: "be-1", Tool: "bash", Command: "ls -la internal/kv", Cwd: root}, 130},
		{"pipeline", Request{Agent: "be-1", Tool: "bash", Command: "go test ./... 2>&1 | grep -v '^ok' | head -50", Cwd: root}, 330},
		{"read inside", Request{Agent: "be-1", Tool: "read", Paths: []string{filepath.Join(root, "internal/kv/render.go")}}, 90},
	} {
		got := testing.AllocsPerRun(30, func() { _ = e.Check(context.Background(), c.req) })
		if got > c.max {
			t.Errorf("%s: Check allocates %.0f times, more than the %.0f it is held to", c.name, got, c.max)
		}
	}
}
