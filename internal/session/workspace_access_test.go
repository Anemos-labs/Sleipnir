package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func TestWorkspaceAccessOfASingleAgent(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if assistantTurns(c) == 0 {
			return mock.Reply{Text: "writing", ToolCalls: []mock.ToolCall{call("w", "write", map[string]any{"path": "notes.txt", "content": "one\ntwo\n"})}}
		}
		return mock.Reply{Text: "done"}
	})
	o := opts(t, repo, client, model)
	o.Allow = []string{"Bash(make lint)"}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "write notes"); err != nil {
		t.Fatal(err)
	}

	// who wrote it: the checkpoint journal, by a relative or an absolute path
	for _, p := range []string{"notes.txt", filepath.Join(repo, "notes.txt")} {
		if a, ok := s.LastWriter(p); !ok || a != "main" {
			t.Fatalf("LastWriter(%s) = %q %v", p, a, ok)
		}
	}
	if _, ok := s.LastWriter("main.go"); ok {
		t.Fatal("main.go was not written in the session")
	}

	// the change a pending call asks for, against the file as it is
	in, _ := json.Marshal(map[string]any{"path": "notes.txt", "content": "one\nTWO\n"})
	c, ok := s.PendingChange("write", in)
	if !ok || c.Path != "notes.txt" || c.Added != 1 || c.Removed != 1 || !strings.Contains(c.Change, "-two\n+TWO\n") {
		t.Fatalf("pending write = %+v %v", c, ok)
	}
	in, _ = json.Marshal(map[string]any{"path": filepath.Join(repo, "server", "server.go"), "old_string": "func Serve() {}", "new_string": "func Serve() { listen() }"})
	if c, ok := s.PendingChange("edit", in); !ok || c.Path != "server/server.go" || !strings.Contains(c.Change, "+func Serve() { listen() }") || !strings.Contains(c.Change, " // Serve starts the demo server.") {
		t.Fatalf("pending edit = %+v %v", c, ok)
	}
	// a file outside the project is never read to show a change
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("do not show\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ = json.Marshal(map[string]any{"path": outside, "content": "x\n"})
	if c, ok := s.PendingChange("write", in); !ok || strings.Contains(c.Change, "do not show") || !strings.Contains(c.Change, "--- /dev/null") {
		t.Fatalf("pending write outside = %+v", c)
	}
	if _, ok := s.PendingChange("bash", json.RawMessage(`{"command":"ls"}`)); ok {
		t.Fatal("a command has no file change")
	}

	// what the engine would do, and where the deciding rule came from
	cases := []struct {
		tool, arg string
		verdict   perm.Action
		origin    string
	}{
		{"Bash", "make lint", perm.Allow, "--allow flag"},
		{"Edit", ".sleipnir/config.json", perm.Ask, perm.OriginBuiltIn},
		{"Read", "main.go", perm.Allow, ""},
	}
	for _, k := range cases {
		got, err := s.Classify(k.tool, k.arg)
		// the session of this test has no prompter: a question is a refusal
		want := k.verdict
		if want == perm.Ask {
			want = perm.Deny
		}
		if err != nil || got.Verdict != want || got.Origin != k.origin {
			t.Errorf("Classify(%s %s) = %+v %v", k.tool, k.arg, got, err)
		}
	}
	if _, err := s.Classify("Fetch", "x"); !session.IsBadTool(err) {
		t.Fatalf("bad tool: %v", err)
	}
	if got := s.RuleOrigin("Bash(make lint)"); got != "--allow flag" {
		t.Fatalf("RuleOrigin = %q", got)
	}

	// the isolated team's parts are not there
	if s.MergeQueue() != nil || s.Worktrees() != nil {
		t.Fatal("no merge queue or trees without isolation")
	}
	if _, _, err := s.AcceptVerified(context.Background(), "x"); !errors.Is(err, session.ErrNotIsolated) {
		t.Fatalf("AcceptVerified: %v", err)
	}
	if _, err := s.ApplyVerified(context.Background(), swarm.AcceptOptions{DryRun: true}); !errors.Is(err, session.ErrNotIsolated) {
		t.Fatalf("ApplyVerified: %v", err)
	}
}

func TestFileStateLastWriter(t *testing.T) {
	f := tools.NewFileState()
	if _, ok := f.LastWriter("/p/a.go"); ok {
		t.Fatal("nothing written yet")
	}
	f.RecordRead("be-1", "/p/a.go", []byte("x"))
	if _, ok := f.LastWriter("/p/a.go"); ok {
		t.Fatal("a read is not a write")
	}
	f.RecordWrite("be-1", "/p/a.go", []byte("y"), time.Now())
	f.RecordWrite("be-2", "/p/a.go", []byte("z"), time.Now())
	if a, ok := f.LastWriter("/p/a.go"); !ok || a != "be-2" {
		t.Fatalf("LastWriter = %q %v", a, ok)
	}
	if _, ok := f.LastWriter("/p/b.go"); ok {
		t.Fatal("another path")
	}
}
