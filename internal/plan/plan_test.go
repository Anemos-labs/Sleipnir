package plan

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeCleansAndRefuses(t *testing.T) {
	got, err := Normalize([]Item{{Step: "  read\n the   tests ", Status: "TODO"}, {Step: "fix it", Status: "in_progress"}, {Step: "run go test", Status: "completed"}, {Step: "ship"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{{Step: "read the tests", Status: Pending}, {Step: "fix it", Status: Doing}, {Step: "run go test", Status: Done}, {Step: "ship", Status: Pending}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d: %+v, want %+v", i+1, got[i], want[i])
		}
	}
	for name, bad := range map[string][]Item{
		"empty":      nil,
		"empty step": {{Step: " \n "}},
		"two doing":  {{Step: "a", Status: "doing"}, {Step: "b", Status: "doing"}},
		"odd status": {{Step: "a", Status: "maybe"}},
		"too long":   {{Step: strings.Repeat("x", MaxStepRune+1)}},
		"too many":   make([]Item, MaxSteps+1),
	} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestLinesFrameAndStore(t *testing.T) {
	items := []Item{{Step: "a", Status: Done}, {Step: "b", Status: Doing}, {Step: "c", Status: Pending}}
	if got := Lines(items); got != "[x] 1. a\n[>] 2. b\n[ ] 3. c\n" {
		t.Errorf("%q", got)
	}
	f := Frame(items)
	if !strings.HasPrefix(f, "<live plan>\n") || !strings.HasSuffix(f, "</live>") || Frame(nil) != "" {
		t.Errorf("frame: %q", f)
	}
	if Open(items) != 2 || Open(nil) != 0 {
		t.Error("open")
	}
	s := NewStore()
	s.Set("a1", items)
	items[0].Status = Pending // the store keeps its own copy
	if s.Get("a1")[0].Status != Done || s.Open("a1") != 2 || s.Open("other") != 0 {
		t.Errorf("store: %+v", s.Get("a1"))
	}
}

// A model's estimate is read as hours whatever form it takes, and its milestone as the goal numbers milestones; neither refuses a
// plan.
func TestNormalizeReadsEffortAndMilestone(t *testing.T) {
	var in struct {
		Items []Item `json:"items"`
	}
	raw := `{"items":[{"step":"a","effort":3,"milestone":"m2"},{"step":"b","effort":"90m","milestone":"Milestone 3"},` +
		`{"step":"c","effort":"2d","milestone":"**M10**"},{"step":"d","effort":"soon"},{"step":"e","effort":-4},{"step":"f","effort":9999,"milestone":"rubric-long"}]}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	got, err := Normalize(in.Items)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		e  Hours
		ms string
	}{{3, "M2"}, {1.5, "M3"}, {16, "M10"}, {0, ""}, {0, ""}, {MaxEffort, "RUBRIC-L"}}
	for i, w := range want {
		if got[i].Effort != w.e || got[i].Milestone != w.ms {
			t.Errorf("step %d: effort %v milestone %q, want %v %q", i+1, got[i].Effort, got[i].Milestone, w.e, w.ms)
		}
	}
	if l := Lines(got[:2]); l != "[ ] 1. a (M2, 3h)\n[ ] 2. b (M3, 1.5h)\n" {
		t.Errorf("lines: %q", l)
	}
}

// A plan's progress is weighed by its estimates, holds a share for each milestone it leaves out, and does not count a step done
// whose milestone the judge has ruled unmet.
func TestProgressWeighsTheWork(t *testing.T) {
	items := []Item{
		{Step: "scaffold", Status: Done, Effort: 1, Milestone: "M1"},
		{Step: "api", Status: Done, Effort: 1, Milestone: "M2"},
		{Step: "ui", Status: Pending, Effort: 8, Milestone: "M3"},
		{Step: "docs", Status: Pending, Milestone: "M4"}, // no estimate: weighs the mean, 10/3
	}
	// Two of four steps done looks like half, but they are 2 of the 13.3 estimated hours.
	w := Progress(items, nil)
	if !w.Hours || w.Done != 2 || w.Pct() != 15 {
		t.Errorf("unruled: %+v pct %d", w, w.Pct())
	}
	// The judge has ruled: M1 met, M2 not met. M2's step is claimed, not done. M5 has no step and adds a mean share.
	ms := []Mark{{ID: "M1", Met: true, Ruled: true}, {ID: "M2", Ruled: true}, {ID: "M3", Ruled: true}, {ID: "M4", Ruled: true}, {ID: "M5", Ruled: true}}
	w = Progress(items, ms)
	if w.Done != 1 || w.Claimed != 1 || len(w.Missing) != 1 || w.Missing[0] != "M5" {
		t.Errorf("ruled: %+v", w)
	}
	if p := w.Pct(); p != 6 { // 1 of 10+10/3+10/3 hours
		t.Errorf("pct %d", p)
	}
	// Before the judge has ruled, the agent's marks stand.
	ms[1].Ruled = false
	if w := Progress(items, ms[:2]); w.Done != 2 || w.Claimed != 0 {
		t.Errorf("unruled mark: %+v", w)
	}
	// Without estimates, steps weigh one each.
	if w := Progress([]Item{{Step: "a", Status: Done}, {Step: "b"}}, nil); w.Hours || w.Pct() != 50 {
		t.Errorf("counted: %+v", w)
	}
	if s := Summary(items, nil); s != "2 of 4 steps done, about 15% of 13.3h estimated" {
		t.Errorf("summary: %q", s)
	}
}
