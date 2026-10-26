package session

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/swarm"
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
		got, err := s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Query: query})
		if err != nil || !strings.Contains(got, "No matching evidence") {
			t.Fatalf("boundary %s: %s %v", query, got, err)
		}
	}
	got, err := s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Query: "UNIQUE PASS"})
	if err != nil || !strings.Contains(got, "UNIQUE PASS 37 tests") || !strings.Contains(got, "agent=ts-2") {
		t.Fatalf("lost long worker evidence: %s %v", got, err)
	}
	got, err = s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Record: record, Offset: 11000})
	if err != nil || !strings.Contains(got, "UNIQUE PASS 37 tests") {
		t.Fatalf("cannot page full output: %s %v", got, err)
	}
	got, err = s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Query: "wait", Offset: 140})
	if err != nil || !strings.Contains(got, "shown=10") {
		t.Fatalf("pagination: %s %v", got, err)
	}
	past, err := s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Records: []uint64{record, record + 1}, Offset: 1000000})
	if err != nil || !strings.Contains(past, fmt.Sprintf("record=%d offset past end", record)) || !strings.Contains(past, fmt.Sprintf("\nrecord=%d offset past end", record+1)) {
		t.Fatalf("past-end batch records: %s %v", past, err)
	}
	batch, err := s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Records: []uint64{record, record + 1}})
	if err != nil || !strings.Contains(batch, fmt.Sprintf("record=%d", record)) || !strings.Contains(batch, "wait") {
		t.Fatalf("batch read: %s %v", batch, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.goalEvidence(ctx, g, cutoff, evidenceRequest{}); err != context.Canceled {
		t.Fatalf("canceled scan continued: %v", err)
	}
	// The immutable log, not an agent's live/compacted thread, supplies the output.
	restored := &Session{Dir: dir, Log: log, Blobs: s.Blobs}
	got, err = restored.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Record: record, Offset: 11000})
	if err != nil || !strings.Contains(got, "UNIQUE PASS 37 tests") {
		t.Fatal("restore lost evidence")
	}
	if got := evidenceText(events.Event{Type: events.TypeTurnAppend, Data: []byte(`{"role":"assistant","blocks":[{"kind":"text","text":"everything passed"}]}`)}, nil); got != "" {
		t.Fatal("assistant claim became proof")
	}
}

func TestDeliveryCommandsBecomeDurableGoalEvidence(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "delivery-evidence")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	s := &Session{Dir: dir, Log: log, Blobs: events.NewMemBlobs()}
	g := goal.New("inspect delivery")
	s.SaveGoal(g)
	if _, err := s.deliveryExec(dir)(context.Background(), "git", "--version"); err != nil {
		t.Fatal(err)
	}
	cutoff, err := s.goalEvidenceCutoff()
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Query: "git version"})
	if err != nil || !strings.Contains(evidence, "delivery.evidence") || !strings.Contains(evidence, "git version") {
		t.Fatalf("missing controller proof: %s %v", evidence, err)
	}
}

func TestGoalRejectsMissingBoundaryAfterFailedSave(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "missing-boundary")
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{Dir: dir, Log: log, Blobs: events.NewMemBlobs()}
	log.Emit("old-worker", events.TypeToolCall, map[string]string{"name": "old objective passed"})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	g := goal.New("new objective")
	s.SaveGoal(g)
	if g.StartSeq != 0 {
		t.Fatal("failed save assigned a boundary")
	}
	reopened, err := events.Open(dir, "missing-boundary")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s.Log = reopened
	cutoff, err := s.goalEvidenceCutoff()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{}); err == nil {
		t.Fatal("unsaved goal searched prior objectives")
	}
	if _, _, err := s.JudgeGoal(context.Background(), g); err == nil || !strings.Contains(err.Error(), "evidence boundary") {
		t.Fatalf("judge accepted unsaved goal: %v", err)
	}
}

func TestGoalTaskGateDistinguishesDispositionFromUnfinishedWork(t *testing.T) {
	closeAs := func(kind swarm.ClosureKind, target string) swarm.Closure {
		c, err := swarm.NewClosure(kind, target)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, tc := range []struct {
		name   string
		task   swarm.Task
		others []swarm.Task
		open   bool
	}{
		{"done", swarm.Task{ID: "T1", Status: swarm.StatusDone}, nil, false},
		{"working", swarm.Task{ID: "T1", Status: swarm.StatusDoing}, nil, true},
		{"canceled", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseCanceled, "")}, nil, false},
		{"denied", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseDenied, "")}, nil, false},
		{"failed verification", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseVerifier, "")}, nil, true},
		{"exhausted", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseExhausted, "")}, nil, true},
		{"blocked", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseBlockedOn, "T2")}, []swarm.Task{{ID: "T2", Status: swarm.StatusDone}}, true},
		{"replaced", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseSuperseded, "T2")}, []swarm.Task{{ID: "T2", Status: swarm.StatusDone}}, false},
		{"replacement unfinished", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseSuperseded, "T2")}, []swarm.Task{{ID: "T2", Status: swarm.StatusDoing}}, true},
		{"replacement canceled", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseSuperseded, "T2")}, []swarm.Task{{ID: "T2", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseCanceled, "")}}, true},
		{"replacement absent", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseSuperseded, "T2")}, nil, true},
		{"replacement cycle", swarm.Task{ID: "T1", Status: swarm.StatusFailed, Closure: closeAs(swarm.CloseSuperseded, "T1")}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sn := &swarm.Snapshot{Tasks: append([]swarm.Task{tc.task}, tc.others...)}
			if got := goalTaskUnfinished(tc.task, sn); got != tc.open {
				t.Fatalf("unfinished=%v want %v", got, tc.open)
			}
		})
	}
}

// The judge's search lists the newest matches first, so the last run of a check is what it sees before the runs that came before
// it; offsets count back from the newest, and the rosters of earlier judgments are not search results. In a team trial the judge's
// searches surfaced the first test runs of a long session and it asked again and again for "the final test run", which existed.
func TestGoalEvidenceListsTheNewestFirst(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	s := &Session{Dir: dir, Log: log, Blobs: events.NewMemBlobs()}
	g := goal.New("ship")
	s.SaveGoal(g)
	var runs []uint64
	for _, n := range []int{3, 5, 9} {
		seq, _ := log.Emit("be-1", events.TypeToolCall, map[string]any{"name": "bash", "input": fmt.Sprintf("pytest: %d passed", n)})
		runs = append(runs, seq)
		if _, err := s.goalEvidenceCutoff(); err != nil { // a judgment in between
			t.Fatal(err)
		}
	}
	cutoff, err := s.goalEvidenceCutoff()
	if err != nil {
		t.Fatal(err)
	}
	first := func(got string) string { return strings.SplitN(got, "\n", 2)[0] }
	got, err := s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Query: "passed"})
	if err != nil || !strings.HasPrefix(first(got), fmt.Sprintf("record=%d ", runs[2])) || !strings.Contains(got, "matching records=3 shown=3") {
		t.Fatalf("search, newest first: %s %v", got, err)
	}
	got, err = s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Query: "passed", Offset: 1})
	if err != nil || !strings.HasPrefix(first(got), fmt.Sprintf("record=%d ", runs[1])) || !strings.Contains(got, "shown=2") {
		t.Fatalf("search from offset 1: %s %v", got, err)
	}
	got, err = s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{})
	if err != nil || !strings.HasPrefix(first(got), fmt.Sprintf("record=%d ", runs[2])) || strings.Contains(got, "goal.evidence") {
		t.Fatalf("the first page: %s %v", got, err)
	}
}

// A shell call and its output are separate records: the output names the command that produced it, so a search for the command
// finds it, and the call names the record of its output. A judge that searched for "make cross" found only the calls, and read one
// of them three times looking for the output.
func TestAToolCallAndItsResultPointAtEachOther(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	s := &Session{Dir: dir, Log: log, Blobs: events.NewMemBlobs()}
	g := goal.New("cross-build")
	s.SaveGoal(g)
	call, _ := log.Emit("be-1", events.TypeToolCall, map[string]any{"id": "c1", "name": "bash", "input": map[string]any{"command": "make cross", "description": "build"}})
	log.Emit("be-1", events.TypeToolCall, map[string]any{"id": "c2", "name": "bash", "input": map[string]any{"command": "make lint"}})
	result, _ := log.Emit("be-1", events.TypeTurnAppend, map[string]any{"role": "user", "blocks": []map[string]any{
		{"kind": "tool_result", "tool_id": "c1", "result": []map[string]any{{"kind": "text", "text": "built 16 of 16 targets"}}},
	}})
	cutoff, err := s.goalEvidenceCutoff()
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Query: "make cross"})
	if err != nil || !strings.Contains(got, fmt.Sprintf("record=%d agent=be-1 type=turn.append tool result c1 of bash make cross error=false built 16 of 16 targets", result)) {
		t.Fatalf("the output was not found by its command: %s %v", got, err)
	}
	if !strings.Contains(got, fmt.Sprintf("record=%d agent=be-1 type=tool.call (its result: record=%d)", call, result)) {
		t.Fatalf("the call did not name its result: %s", got)
	}
	got, err = s.goalEvidence(context.Background(), g, cutoff, evidenceRequest{Records: []uint64{call, call + 1}})
	if err != nil || !strings.Contains(got, fmt.Sprintf("type=tool.call (its result: record=%d) runes=", result)) || !strings.Contains(got, "type=tool.call (no result within the cutoff) runes=") {
		t.Fatalf("reading the calls: %s %v", got, err)
	}
	if strings.Contains(got, "\x00") {
		t.Fatalf("a mark was left in the evidence: %q", got)
	}
}
