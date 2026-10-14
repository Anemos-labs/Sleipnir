package session

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/goal"
)

func TestGoalEvidenceSurvivesNoiseAndKeepsObjectiveBoundaries(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	s := &Session{Dir: dir, Log: log, Blobs: events.NewMemBlobs()}
	log.Emit("worker", events.TypeToolCall, map[string]any{"name": "bash", "input": "old goal passed"})
	g := goal.New("ship current work")
	s.SaveGoal(g)
	body := strings.Repeat("prefix noise ", 900) + "UNIQUE PASS 37 tests" + strings.Repeat(" trailing noise", 900)
	ref, err := s.Blobs.Put([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	record, _ := log.Emit("ts-2", events.TypeToolResult, map[string]any{"id": "test-call", "name": "bash", "ref": ref, "error": false})
	for i := 0; i < 150; i++ {
		log.Emit("mgr", events.TypeToolCall, map[string]any{"name": "wait", "input": fmt.Sprint(i)})
	}
	cutoff, err := s.goalEvidenceCutoff()
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("worker", events.TypeToolCall, map[string]any{"name": "bash", "input": "future goal passed"})
	log.Flush()
	for _, query := range []string{"old goal", "future goal"} {
		got, err := s.goalEvidence(g, cutoff, evidenceRequest{Query: query})
		if err != nil || !strings.Contains(got, "No matching evidence") {
			t.Fatalf("boundary %s: %s %v", query, got, err)
		}
	}
	got, err := s.goalEvidence(g, cutoff, evidenceRequest{Query: "UNIQUE PASS"})
	if err != nil || !strings.Contains(got, "UNIQUE PASS 37 tests") || !strings.Contains(got, "agent=ts-2") {
		t.Fatalf("lost long worker evidence: %s %v", got, err)
	}
	got, err = s.goalEvidence(g, cutoff, evidenceRequest{Record: record, Offset: 11000})
	if err != nil || !strings.Contains(got, "UNIQUE PASS 37 tests") {
		t.Fatalf("cannot page full output: %s %v", got, err)
	}
	got, err = s.goalEvidence(g, cutoff, evidenceRequest{Query: "wait", Offset: 140})
	if err != nil || !strings.Contains(got, "shown=10") {
		t.Fatalf("pagination: %s %v", got, err)
	}
	// The immutable log, not an agent's live/compacted thread, supplies the output.
	restored := &Session{Dir: dir, Log: log, Blobs: s.Blobs}
	got, err = restored.goalEvidence(g, cutoff, evidenceRequest{Record: record, Offset: 11000})
	if err != nil || !strings.Contains(got, "UNIQUE PASS 37 tests") {
		t.Fatal("restore lost evidence")
	}
	if got := evidenceText(events.Event{Type: events.TypeTurnAppend, Data: []byte(`{"role":"assistant","blocks":[{"kind":"text","text":"everything passed"}]}`)}); got != "" {
		t.Fatal("assistant claim became proof")
	}
}
