package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The log records a checkpoint's progress in a few small events, not one event per new
// file carrying every path so far: a turn that writes many files must not make the log
// grow with the square of their number.
func TestCheckpointEventsStaySmall(t *testing.T) {
	const files = 100
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if assistantTurns(c) == 0 {
			var calls []mock.ToolCall
			for i := range files {
				calls = append(calls, call(fmt.Sprintf("w%d", i), "write", map[string]any{"path": fmt.Sprintf("gen/f%03d.txt", i), "content": "x\n"}))
			}
			return mock.Reply{Text: "writing", ToolCalls: calls}
		}
		return mock.Reply{Text: "done"}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "write many files"); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	n, biggest, last := 0, 0, -1
	for _, e := range readEvents(t, dir) {
		if e.Type != "checkpoint" {
			continue
		}
		n++
		biggest = max(biggest, len(e.Data))
		var p struct {
			Count int      `json:"count"`
			Files []string `json:"files"`
		}
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		last = max(p.Count, len(p.Files))
	}
	if n == 0 || n > 20 || biggest > 1024 || last != files {
		t.Fatalf("%d checkpoint events, the largest %d bytes, the last counts %d files (want a few small events ending at %d)", n, biggest, last, files)
	}
}
