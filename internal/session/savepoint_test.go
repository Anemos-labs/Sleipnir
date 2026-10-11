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

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A savepoint taken between two runs is a resume point: with the log cut back
// to it, a resumed session continues from the state the savepoint captured and
// knows nothing of what came after.
func TestSavepointIsAResumePoint(t *testing.T) {
	repo := newRepo(t)
	dir := filepath.Join(t.TempDir(), "sessions", "20260101-000000-savept")
	var prompts []string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		var all []string
		for _, m := range c.Messages {
			all = append(all, m.Content)
		}
		prompt := strings.Join(all, "\n")
		prompts = append(prompts, prompt)
		last := c.Messages[len(c.Messages)-1]
		if strings.Contains(last.Content, "write the marker") {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "w1", Name: "bash", Args: `{"command":"echo SAVED > marker.txt"}`}}}
		}
		return mock.Reply{Text: "ok"}
	})
	o := opts(t, repo, client, model)
	o.Dir, o.Savepoints = dir, true
	s1, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Run(context.Background(), "write the marker"); err != nil {
		t.Fatal(err)
	}
	captured := false
	sp, err := s1.Savepoint(context.Background(), 50*time.Millisecond, 50*time.Millisecond, func(context.Context) error {
		captured = true
		return nil
	})
	if err != nil || !sp.Consistent || !captured || sp.N != 1 || sp.Agents["main"] == "" || sp.LogBytes == 0 {
		t.Fatalf("savepoint: %+v captured=%v err=%v", sp, captured, err)
	}
	if _, err := s1.Run(context.Background(), "the codeword after the savepoint is TANGERINE"); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "marker.txt")); err != nil || strings.TrimSpace(string(b)) != "SAVED" {
		t.Fatalf("the tool did not run: %q %v", b, err)
	}

	// The savepoint event is in the log, after the agent's snapshot.
	var snapSeq, spSeq uint64
	for _, e := range readEvents(t, dir) {
		switch e.Type {
		case events.TypeAgentSnapshot:
			if strings.Contains(string(e.Data), `"savepoint":true`) {
				snapSeq = e.Seq
			}
		case events.TypeSessionSavepoint:
			var d struct {
				Consistent bool `json:"consistent"`
			}
			_ = json.Unmarshal(e.Data, &d)
			if d.Consistent {
				spSeq = e.Seq
			}
		}
	}
	if snapSeq == 0 || spSeq != sp.Seq || snapSeq > spSeq {
		t.Fatalf("snapshot seq %d, savepoint seq %d (result %d)", snapSeq, spSeq, sp.Seq)
	}

	// Cut the log back to the savepoint and resume.
	if err := os.Truncate(filepath.Join(dir, "events.jsonl"), sp.LogBytes); err != nil {
		t.Fatal(err)
	}
	o2 := opts(t, repo, client, model)
	o2.Resume, o2.Savepoints = dir, true
	s2, err := session.New(context.Background(), o2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.Run(context.Background(), "what happened so far?"); err != nil {
		t.Fatal(err)
	}
	last := prompts[len(prompts)-1]
	if !strings.Contains(last, "write the marker") || !strings.Contains(last, "what happened so far?") {
		t.Errorf("the resumed agent lost the state of the savepoint:\n%s", last)
	}
	if strings.Contains(last, "TANGERINE") {
		t.Errorf("the resumed agent knows what came after the savepoint:\n%s", last)
	}
}

// Without Options.Savepoints there is no barrier and no savepoint.
func TestSavepointNeedsTheOption(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Savepoint(context.Background(), time.Millisecond, time.Millisecond, nil); !errors.Is(err, session.ErrNoSavepoints) {
		t.Fatalf("err = %v", err)
	}
}
