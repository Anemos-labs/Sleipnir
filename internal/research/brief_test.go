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
