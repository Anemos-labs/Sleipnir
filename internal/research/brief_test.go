package research

import (
	"strings"
	"testing"
)

// A research-first run does not hold the whole team: its goal and its brief put the free workers on the groundwork no finding
// can change, and its pages are written one writer each, by any free worker.
func TestKickoffKeepsTheFreeWorkersBuilding(t *testing.T) {
	r := &Run{Slug: "app", Question: "An app", Project: "Build an app.", Mode: ModeKickoff, Depth: "quick"}
	goal := Objective(r, "Build an app.", "")
	if !strings.Contains(goal, "The workers the research leaves free start at once") {
		t.Errorf("the goal holds the team for the research:\n%s", goal)
	}
	brief := Brief(r)
	for _, want := range []string{"**Builders**", "one writer per page: any free worker can write one"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q", want)
		}
	}
	r.Mode = ModeReport
	if b := Brief(r); strings.Contains(b, "**Builders**") {
		t.Error("a report run has builders")
	}
}

// Quick research is quick: one short round, one to three pages, and no fact-check round in its brief, its milestones or its goal;
// standard research keeps its fact-checkers.
func TestQuickResearchHasNoFactCheckRound(t *testing.T) {
	r := &Run{Slug: "app", Question: "An app", Project: "Build an app.", Mode: ModeKickoff, Depth: "quick"}
	brief, goal := Brief(r), Objective(r, "Build an app.", "")
	for _, no := range []string{"Fact-check", "Fact-checkers", "every page fact-checked"} {
		if strings.Contains(brief, no) || strings.Contains(goal, no) {
			t.Errorf("quick research mentions %q", no)
		}
	}
	if !strings.Contains(brief, "no fact-check round") || !strings.Contains(brief, "### 4. Publish") {
		t.Errorf("quick brief:\n%s", brief)
	}
	r.Depth = "standard"
	if b := Brief(r); !strings.Contains(b, "Fact-checkers") || !strings.Contains(b, "### 5. Fact-check") {
		t.Errorf("standard brief lost its fact-check:\n%s", b)
	}
}

// A report run's goal names its run, so the harness judges it from the ledger; a kickoff run's goal is also the project's and names
// none. Its brief says the bars end the run and that workers left without a listed problem stay idle.
func TestReportGoalNamesItsRunAndTheBriefEndsAtTheBars(t *testing.T) {
	r := &Run{Slug: "agent-memory", Question: "How do agents remember?", Mode: ModeReport, Depth: "standard"}
	for _, spec := range []string{"", "/work/app/.sleipnir/research/agent-memory/BRIEF.md"} {
		if got := ObjectiveSlug(Objective(r, "", spec)); got != "agent-memory" {
			t.Errorf("spec %q: the goal names run %q", spec, got)
		}
	}
	brief := Brief(r)
	for _, want := range []string{"## When the run ends", "the other workers stay idle", "The ledger decides"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q", want)
		}
	}
	k := &Run{Slug: "app", Question: "An app", Project: "Build an app.", Mode: ModeKickoff, Depth: "quick"}
	if got := ObjectiveSlug(Objective(k, "Build an app.", "")); got != "" {
		t.Errorf("a kickoff goal names run %q", got)
	}
	if strings.Contains(Brief(k), "The ledger decides") {
		t.Error("a kickoff brief says its ledger decides the goal")
	}
}
