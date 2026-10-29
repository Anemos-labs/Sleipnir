package translate

import (
	"testing"
	"time"
)

// A goal that has run continuation turns, or that the judge has given a reason for, sends both: the page reads each of them and
// every field of a goal event is omitted when empty.
func TestAGoalCarriesItsTurnsAndReason(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(b.add(time.Second, "", "goal.state", map[string]any{"goal": map[string]any{"Objective": "Build the shop", "Max": 20, "Turns": 3, "Reason": "T2 unfinished"}}))
	goals := ofKind(h.decoded(), "goal")
	if len(goals) != 1 {
		t.Fatalf("goals: %v", goals)
	}
	if goals[0]["turns"] != float64(3) || goals[0]["reason"] != "T2 unfinished" {
		t.Errorf("the goal lost its turns or its reason: %v", goals[0])
	}
}

// A goal's spec, rubric and milestones, with the judge's last ruling on each, reach the page from the log as the live tab sends them.
func TestAGoalCarriesItsMilestones(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	ms := []map[string]any{{"ID": "M1", "Text": "API answers", "Met": true, "Evidence": "record 4"}, {"ID": "M2", "Text": "site is up"}}
	h.feed(b.add(time.Second, "", "goal.state", map[string]any{"goal": map[string]any{"Objective": "Ship", "Max": 20, "Spec": "docs/SPEC.md", "SpecText": "the whole spec", "Rubric": ".sleipnir/rubric.md", "RubricText": "the whole rubric", "Milestones": ms}}))
	goals := ofKind(h.decoded(), "goal")
	if len(goals) != 1 {
		t.Fatalf("goals: %v", goals)
	}
	got, _ := goals[0]["milestones"].([]any)
	if goals[0]["spec"] != "docs/SPEC.md" || goals[0]["rubric"] != ".sleipnir/rubric.md" || len(got) != 2 {
		t.Fatalf("the goal lost its spec, rubric or milestones: %v", goals[0])
	}
	m1, _ := got[0].(map[string]any)
	m2, _ := got[1].(map[string]any)
	if m1["id"] != "M1" || m1["met"] != true || m1["evidence"] != "record 4" || m2["text"] != "site is up" || m2["met"] != nil {
		t.Errorf("milestones: %v", got)
	}
	if _, ok := goals[0]["specText"]; ok {
		t.Error("the spec's text was sent to the page")
	}
	if _, ok := goals[0]["rubricText"]; ok {
		t.Error("the rubric's text was sent to the page")
	}
}
