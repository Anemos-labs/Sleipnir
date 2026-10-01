package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/widgettest"
)

// The scrollback of a conversation, as the terminal keeps it: a golden file, for the unicode and the ASCII look. Everything in it is
// final, so it is what a person scrolls back to; -update rewrites it (go test ./internal/tui/app -run Scrollback -update), and the
// diff of testdata/ is what to read.
func TestChatScrollbackGolden(t *testing.T) {
	for _, tc := range []struct {
		name    string
		unicode bool
	}{{"unicode", true}, {"ascii", false}} {
		t.Run(tc.name, func(t *testing.T) {
			look := Look{Theme: widget.MonoTheme().WithASCII(!tc.unicode), Palette: widget.MonoPalette(), Unicode: tc.unicode, Anim: false}
			r := startChat(t, rigOpts{cols: 80, rows: 40, look: look})
			var seq []string
			for i := 1; i <= 12; i++ {
				seq = append(seq, fmt.Sprintf("--- FAIL: TestList/page_%d (0.00s)", i))
			}
			bash := toolCall("c1", "bash", map[string]any{"command": "go test ./orders/... -run TestList"})
			edit := toolCall("c2", "edit", map[string]any{"path": "/work/proj/orders/list.go", "old_string": "a", "new_string": "b"})
			vet := toolCall("c3", "bash", map[string]any{"command": "go vet ./..."})
			read := toolCall("c4", "read", map[string]any{"path": "/work/proj/orders/list.go"})
			write := toolCall("c5", "write", map[string]any{"path": "/work/proj/orders/list_test.go", "content": "package orders\n\nfunc TestPage0(t *testing.T) {}\n"})
			r.host.turn = func(ctx context.Context, goal string) TurnResult {
				if goal == "slow" {
					<-ctx.Done()
					return TurnResult{Err: ctx.Err()}
				}
				r.sink.Text("main", "## Plan\n\nThe offset is computed from a zero-based page; pages are **one-based**, so page 1 skips the first `size` rows.\n\n")
				r.sink.Text("main", "1. run the test\n2. fix `List`\n\n```go\noffset := (page - 1) * size\n```\n")
				r.sink.Response("main", nil, 0)
				r.sink.ToolStart("main", bash)
				r.sink.ToolEnd("main", bash, okResult(strings.Join(seq, "\n")+"\nFAIL\n[exit code 1]", map[string]any{"exit_code": 1}), 1400*time.Millisecond)
				r.sink.ToolStart("main", read)
				r.sink.ToolEnd("main", read, okResult("package orders", map[string]any{"lines": 84}), 12*time.Millisecond)
				r.sink.ToolStart("main", edit)
				r.sink.ToolEnd("main", edit, okResult("Edited", map[string]any{"diff": "@@ -58,3 +58,3 @@\n func (s *Store) List() {\n-\toffset := page * size\n+\toffset := (page - 1) * size\n \treturn nil", "added": 1, "removed": 1}), 30*time.Millisecond)
				r.sink.ToolStart("main", write)
				r.sink.ToolEnd("main", write, okResult("Created", map[string]any{"created": true}), 8*time.Millisecond)
				r.sink.Notice("main", "warn", "server (http 429): slow down; retrying in 4s (attempt 2 of 6)")
				r.sink.ToolStart("main", vet)
				r.sink.ToolEnd("main", vet, okResult("vet: orders/list.go:61: unreachable code\n[exit code 2]", map[string]any{"exit_code": 2}), 2*time.Second)
				r.sink.Text("main", "Fixed: `go test` passes now, `go vet` has one finding left.")
				r.sink.Response("main", nil, 0)
				return TurnResult{Steps: 4, CostUSD: 0.0123, HitRatio: 0.5}
			}
			r.submit("fix the failing test in orders")
			r.shows("4 steps")
			r.submit("slow")
			r.shows("esc to interrupt")
			r.ctrl('c')
			r.shows("(cancelled)")
			r.submit("/cost")
			r.shows("ran /cost")
			r.ctrl('c')
			r.shows(QuitHint)
			widgettest.Golden(t, *update, "testdata/chat-scrollback-"+tc.name+"-80.txt", r.screen())
		})
	}
}
