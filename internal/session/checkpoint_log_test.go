package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// ckptRecord is what a test reads of a "checkpoint" event.
type ckptRecord struct {
	Count int      `json:"count"`
	File  string   `json:"file"`
	Files []string `json:"files"`
}

// writeTurns runs one turn of a session whose model writes per[i] new files in step i, sleeping delay before each reply, and
// returns the session's log.
func writeTurns(t *testing.T, per []int, delay time.Duration) []events.Event {
	t.Helper()
	repo := newRepo(t)
	next := 0
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		time.Sleep(delay)
		step := assistantTurns(c)
		if step >= len(per) {
			return mock.Reply{Text: "done"}
		}
		var calls []mock.ToolCall
		for range per[step] {
			calls = append(calls, call(fmt.Sprintf("w%d", next), "write", map[string]any{"path": fmt.Sprintf("gen/f%04d.txt", next), "content": "x\n"}))
			next++
		}
		return mock.Reply{Text: "writing", ToolCalls: calls}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "write files"); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return readEvents(t, dir)
}

// checkpoints are the counts of the log's "checkpoint" events and the size of the largest.
func checkpoints(t *testing.T, evs []events.Event) (counts []int, biggest int) {
	t.Helper()
	for _, e := range evs {
		if e.Type != "checkpoint" {
			continue
		}
		var p ckptRecord
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		counts = append(counts, max(p.Count, len(p.Files)))
		biggest = max(biggest, len(e.Data))
	}
	return counts, biggest
}

// wantCounts are the counts the log writes for a checkpoint that begins empty and records files one by one up to n: the
// beginning, every count that has grown by max(1, last/8) since the last one written, and n when the turn ends.
func wantCounts(n int) []int {
	out, last := []int{0}, 0
	for c := 1; c <= n; c++ {
		if c-last >= max(1, last/8) {
			out, last = append(out, c), c
		}
	}
	if last != n {
		out = append(out, n)
	}
	return out
}

// The log records a checkpoint's progress in a few small events, not one event per new file carrying every path so far: a turn
// that writes many files must not make the log grow with the square of their number. A change is written when the count has
// grown by an eighth since the last event (every count up to 16), and the last count when the turn ends.
func TestCheckpointEventsStaySmall(t *testing.T) {
	const files = 100
	counts, biggest := checkpoints(t, writeTurns(t, []int{files}, 0))
	if want := wantCounts(files); !reflect.DeepEqual(counts, want) {
		t.Fatalf("checkpoint counts %v, want %v", counts, want)
	}
	if biggest > 1024 {
		t.Fatalf("the largest checkpoint event is %d bytes", biggest)
	}
	if n := len(wantCounts(3000)); n > 70 {
		t.Fatalf("a 3000-file turn would write %d checkpoint events", n)
	}
}

// The same changes put the same checkpoint events in the same places of the log however fast the machine runs: a recording of
// a session tells the same story every time.
func TestCheckpointEventsDoNotDependOnSpeed(t *testing.T) {
	per := []int{3, 1, 4, 20}
	story := func(evs []events.Event) []string {
		var out []string
		for _, e := range evs {
			switch e.Type {
			case "checkpoint":
				var p ckptRecord
				if err := json.Unmarshal(e.Data, &p); err != nil {
					t.Fatal(err)
				}
				out = append(out, fmt.Sprintf("checkpoint %d %s", p.Count, p.File))
			case events.TypeToolCall, events.TypeToolResult, events.TypeModelRequest, events.TypeModelResponse:
				out = append(out, e.Type)
			}
		}
		return out
	}
	fast := story(writeTurns(t, per, 0))
	slow := story(writeTurns(t, per, 300*time.Millisecond))
	if !reflect.DeepEqual(fast, slow) {
		t.Fatalf("the log differs with the machine's speed:\nfast %s\nslow %s", strings.Join(fast, " | "), strings.Join(slow, " | "))
	}
	if len(fast) == 0 || !strings.Contains(strings.Join(fast, "|"), "checkpoint") {
		t.Fatalf("no checkpoint or tool events in %v", fast)
	}
}
