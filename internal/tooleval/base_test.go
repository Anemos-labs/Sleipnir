package tooleval

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBaseIsTheManagersFirstPromptWithItsTeamCard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p, err := Base(ctx, "manager", []string{"backend", "backend", "tester"})
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range p.Tools {
		have[tl.Name] = true
		if tl.Description == "" || len(tl.InputSchema) == 0 {
			t.Errorf("tool %s has no description or schema", tl.Name)
		}
	}
	for _, n := range []string{"spawn", "task", "wait", "mail", "bash", "read"} {
		if !have[n] {
			t.Errorf("the manager's prompt has no %s tool (it has %v)", n, have)
		}
	}
	if len(p.System) == 0 || len(p.System[0].Text) < 1000 {
		t.Fatalf("no system text: %+v", p.System)
	}
	var text strings.Builder
	for _, m := range p.Messages {
		for _, b := range m.Blocks {
			text.WriteString(b.Text + "\n")
		}
	}
	for _, want := range []string{"backend ×2", "tester ×1", baseGoal, "<live "} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("the opening messages have no %q", want)
		}
	}
}

func TestBaseOfASoloSession(t *testing.T) {
	p, err := Base(context.Background(), "solo", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range p.Tools {
		if tl.Name == "spawn" {
			t.Fatal("a solo session's agent has the spawn tool")
		}
	}
}
