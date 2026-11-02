package session

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/research"
)

// A report run's goal is judged from its ledger: continue with the current phase's problems and the ledger's version, never a
// model's list of one more thing to do. A kickoff run's goal stays with the model judge.
func TestAResearchGoalIsJudgedFromItsLedger(t *testing.T) {
	root := t.TempDir()
	s := &Session{opts: Options{Root: root, Cwd: root}, research: research.NewStore(root)}
	r, err := s.research.Create("How do agents remember?", "quick", research.ModeReport, "")
	if err != nil {
		t.Fatal(err)
	}
	g := &goal.State{Objective: research.Objective(r, "", s.briefSpec(r))}
	v, ok := s.researchJudgment(g)
	if !ok || v.Kind != goal.Continue {
		t.Fatalf("a run with no plan: ok=%v verdict %+v", ok, v)
	}
	if !strings.Contains(v.Reason, "plan phase") || !strings.Contains(v.Reason, "ledger version") || len(v.Left) != 1 || !strings.Contains(v.Left[0], "no plan yet") {
		t.Fatalf("verdict %+v", v)
	}
	k, err := s.research.Create("An app", "quick", research.ModeKickoff, "Build an app.")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.researchJudgment(&goal.State{Objective: research.Objective(k, "Build an app.", s.briefSpec(k))}); ok {
		t.Fatal("a kickoff goal was judged from the research ledger")
	}
	if _, ok := s.researchJudgment(&goal.State{Objective: "Build an app."}); ok {
		t.Fatal("a goal that drives no run was judged from a research ledger")
	}
}

// What a verdict leaves counts its "and N more" entry as N items, so the free-capacity line asks for as many tasks as are missing.
func TestLeftCountReadsTheMoreEntry(t *testing.T) {
	if got := leftCount([]string{"a", "b", "and 5 more: research check lists them"}); got != 7 {
		t.Fatalf("left count %d, want 7", got)
	}
	if got := leftCount(nil); got != 0 {
		t.Fatalf("left count %d, want 0", got)
	}
}
