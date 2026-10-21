package session_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A resumed session does not warn again about what its earlier start warned about: the same skill problem is logged once, however
// many times the session restarts, so a page that replays the log shows it once.
func TestRestartsDoNotRepeatStartUpWarnings(t *testing.T) {
	repo := t.TempDir()
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Home = t.TempDir()
	skill := filepath.Join(o.Home, ".claude", "skills", "broken", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: other\ndescription: a skill whose name is not its folder's\n---\nDo it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	s.Close()
	for i := 0; i < 3; i++ {
		r := o
		r.Resume = dir
		s, err := session.New(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	count := map[string]int{}
	_ = events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		var n struct{ Msg string }
		if e.Type == "notice" && json.Unmarshal(e.Data, &n) == nil && strings.HasPrefix(n.Msg, "skills: ") {
			count[n.Msg]++
		}
		return nil
	})
	if len(count) == 0 {
		t.Fatal("the broken skill was never warned about")
	}
	for msg, n := range count {
		if n != 1 {
			t.Errorf("%q logged %d times over four starts", msg, n)
		}
	}
}
