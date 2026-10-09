package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The manager plans, delegates and reviews; it edits no file itself, in a shared checkout as much as in an isolated run, and
// whatever the permission mode says. Its write tools and a writing shell command are refused at run time with what to do
// instead, while it keeps the same tool list as every other agent.
func TestTheManagerEditsNoFileInASharedCheckout(t *testing.T) {
	repo := newRepo(t)
	var refused atomic.Int32
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		manager := false
		for _, m := range c.Messages {
			manager = manager || strings.Contains(m.Content, "you: mgr (manager)")
			if m.Role == "tool" && strings.Contains(m.Content, "the manager does not edit files") {
				refused.Add(1)
			}
		}
		if !manager {
			return mock.Reply{Text: "ok"}
		}
		switch assistantTurns(c) {
		case 0:
			return mock.Reply{Text: "writing", ToolCalls: []mock.ToolCall{call("c1", "write", map[string]any{"path": "hello.txt", "content": "hi\n"})}}
		case 1:
			return mock.Reply{Text: "shell", ToolCalls: []mock.ToolCall{call("c2", "bash", map[string]any{"command": "echo hi > shell.txt"})}}
		}
		return mock.Reply{Text: "done"}
	})
	o := opts(t, repo, client, model)
	o.Swarm, o.Workers = true, 2
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if names := toolNames(s); !names["write"] || !names["bash"] || !names["spawn"] {
		t.Fatalf("every agent sends the same tools, the manager's write and bash included: %v", names)
	}
	if _, err := s.Run(context.Background(), "create hello.txt"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"hello.txt", "shell.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err == nil {
			t.Errorf("the manager wrote %s", f)
		}
	}
	if refused.Load() < 2 {
		t.Errorf("the manager was told %d times that it does not edit files, want once for the write and once for the shell", refused.Load())
	}
}

func toolNames(s *session.Session) map[string]bool {
	out := map[string]bool{}
	for _, sp := range s.Specs {
		out[sp.Name] = true
	}
	return out
}
