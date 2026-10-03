package session_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func TestWindowsWorkspaceToolsInAcceptEdits(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		switch assistantTurns(c) {
		case 0:
			return mock.Reply{ToolCalls: []mock.ToolCall{
				call("read-source", "read", map[string]any{"path": filepath.Join(repo, "server", "server.go")}),
				call("read-head", "read", map[string]any{"path": filepath.Join(repo, ".git", "HEAD")}),
				call("write-result", "write", map[string]any{"path": filepath.Join(repo, "result.txt"), "content": "verified workspace write\n"}),
			}}
		case 1:
			return mock.Reply{ToolCalls: []mock.ToolCall{
				call("read-result", "read", map[string]any{"path": "result.txt"}),
				call("protected-write", "write", map[string]any{"path": filepath.Join(repo, ".git", "HEAD"), "content": "must be refused"}),
			}}
		}
		return mock.Reply{Text: "finished"}
	})
	head, err := os.ReadFile(filepath.Join(repo, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	o := opts(t, repo, client, model)
	o.Mode, o.NoMCP = perm.ModeAcceptEdits, true
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := session.New(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Run(ctx, "read source and write a result"); err != nil {
		t.Fatal(err)
	}
	results := map[string]core.Block{}
	for _, e := range readEvents(t, s.Dir) {
		if e.Type != events.TypeTurnAppend {
			continue
		}
		var turn core.Turn
		if err := json.Unmarshal(e.Data, &turn); err != nil {
			t.Fatal(err)
		}
		for _, b := range turn.Blocks {
			if b.Kind == core.BlockToolResult {
				results[b.ToolID] = b
			}
		}
	}
	for id, marker := range map[string]string{"read-source": "func Serve()", "read-result": "verified workspace write", "write-result": ""} {
		b, ok := results[id]
		if !ok || b.IsError || !strings.Contains(b.PlainText(), marker) {
			t.Errorf("%s = %+v", id, b)
		}
	}
	if b, ok := results["protected-write"]; !ok || !b.IsError || !strings.Contains(b.PlainText(), "permission denied") {
		t.Errorf("protected write = %+v", b)
	}
	if got, err := os.ReadFile(filepath.Join(repo, ".git", "HEAD")); err != nil || string(got) != string(head) {
		t.Fatal("protected file changed", err)
	}
	if got, err := os.ReadFile(filepath.Join(repo, "result.txt")); err != nil || string(got) != "verified workspace write\n" {
		t.Fatalf("write result = %q, %v", got, err)
	}
}
