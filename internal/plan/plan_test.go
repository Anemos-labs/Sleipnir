package plan

import (
	"strings"
	"testing"
)

func TestNormalizeCleansAndRefuses(t *testing.T) {
	got, err := Normalize([]Item{{Step: "  read\n the   tests ", Status: "TODO"}, {Step: "fix it", Status: "in_progress"}, {Step: "run go test", Status: "completed"}, {Step: "ship"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{{"read the tests", Pending}, {"fix it", Doing}, {"run go test", Done}, {"ship", Pending}}
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
	items := []Item{{"a", Done}, {"b", Doing}, {"c", Pending}}
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
