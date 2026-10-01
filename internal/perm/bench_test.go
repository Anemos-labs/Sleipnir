package perm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Every tool call is judged before it runs, a shell command by parsing it and checking each simple command and every path in it
// against the workspace, the rules and the built-in protections (which look at the file system). Fifty agents make fifty of these a
// second in a busy swarm.
func benchEngine(b *testing.B) (*Engine, string) {
	b.Helper()
	root := b.TempDir()
	for _, f := range []string{"internal/kv/render.go", "cmd/sleipnir/main.go", "README.md", "go.mod"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	e, err := NewEngine(Config{Mode: ModeAcceptEdits, Root: root, Home: b.TempDir(), Allow: []string{"Bash(go test:*)", "Bash(go build:*)", "Bash(git status:*)"}})
	if err != nil {
		b.Fatal(err)
	}
	return e, root
}

func BenchmarkCheckBash(b *testing.B) {
	e, root := benchEngine(b)
	for name, cmd := range map[string]string{
		"read-only":     "ls -la internal/kv",
		"allowed rule":  "go test ./internal/kv -run TestGolden -count=1",
		"pipeline":      "go test ./... 2>&1 | grep -v '^ok' | head -50",
		"compound":      "cd internal/kv && go build ./... && go vet ./... && git status --short",
		"substitution":  `cd "$(git rev-parse --show-toplevel)" && ls`,
		"refused write": "cat > /tmp/x.go <<'EOF'\npackage main\nEOF",
	} {
		req := Request{Agent: "be-1", Tool: "bash", Command: cmd, Cwd: root}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = e.Check(context.Background(), req)
			}
		})
	}
}

func BenchmarkCheckFileTools(b *testing.B) {
	e, root := benchEngine(b)
	read, _ := json.Marshal(map[string]any{"path": "internal/kv/render.go"})
	edit, _ := json.Marshal(map[string]any{"path": "internal/kv/render.go", "old": "x", "new": "y"})
	for name, req := range map[string]Request{
		"read inside":  {Agent: "be-1", Tool: "read", Input: read, Paths: []string{filepath.Join(root, "internal/kv/render.go")}},
		"edit inside":  {Agent: "be-1", Tool: "edit", Input: edit, Paths: []string{filepath.Join(root, "internal/kv/render.go")}, Writes: true},
		"read outside": {Agent: "be-1", Tool: "read", Input: read, Paths: []string{"/usr/local/go/src/go/parser/parser.go"}},
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = e.Check(context.Background(), req)
			}
		})
	}
}
