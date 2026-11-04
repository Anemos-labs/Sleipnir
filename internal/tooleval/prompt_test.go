package tooleval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

func testBase() *core.Prompt {
	return &core.Prompt{
		Model:       "m",
		Tools:       append([]core.ToolSpec(nil), testTools...),
		System:      []core.Block{core.Text("system")},
		Breakpoints: []core.Breakpoint{{}},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{
			core.Text("<role-context>manager</role-context>"), core.Text(baseGoal), core.Text("<live board=\"v1\">\n</live>"),
		}}},
	}
}

func TestBuildPlaysTheTurnsOnTheBase(t *testing.T) {
	base := testBase()
	c := Case{ID: "x", Goal: "Ship it.", Board: "<live board=\"v9\">T1 todo</live>", Turns: []Turn{
		{Assistant: "looking", Calls: []Call{{Tool: "task", Args: json.RawMessage(`{"action":"create"}`)}, {Tool: "wait"}}},
		{Results: []Result{{Text: "created T1"}, {Text: "timed out", Error: true}}},
		{User: "<notice>hi</notice>"},
	}, Expect: Expect{MinCalls: 1}}
	p, err := Build(base, c, Variant{Name: "short", Tools: map[string]string{"wait": "Sleep."}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 3 || p.Messages[1].Role != core.RoleAssistant || p.Messages[2].Role != core.RoleUser {
		t.Fatalf("messages: %+v", p.Messages)
	}
	first := p.Messages[0].Blocks
	if first[1].Text != "Ship it." || first[2].Text != c.Board {
		t.Fatalf("goal and board not in place: %+v", first)
	}
	calls := p.Messages[1].Blocks
	if calls[0].Text != "looking" || calls[1].ToolName != "task" || string(calls[2].Input) != `{}` {
		t.Fatalf("assistant turn: %+v", calls)
	}
	res := p.Messages[2].Blocks
	if len(res) != 3 || res[0].ToolID != calls[1].ToolID || res[1].ToolID != calls[2].ToolID || !res[1].IsError || res[2].Text != "<notice>hi</notice>" {
		t.Fatalf("results and the notice after them: %+v", res)
	}
	for _, tl := range p.Tools {
		if tl.Name == "wait" && tl.Description != "Sleep." {
			t.Fatalf("the variant's description is not used: %q", tl.Description)
		}
	}
	if p.Breakpoints != nil {
		t.Fatal("breakpoints kept")
	}
	if base.Messages[0].Blocks[1].Text != baseGoal || len(base.Messages) != 1 || base.Tools[1].Description != "sleep" {
		t.Fatal("the base was changed")
	}
}

func TestBuildRefusesAnUnfinishedHistory(t *testing.T) {
	for name, turns := range map[string][]Turn{
		"calls without results":        {{Calls: []Call{{Tool: "wait"}}}},
		"too few results":              {{Calls: []Call{{Tool: "wait"}, {Tool: "wait"}}}, {Results: []Result{{Text: "ok"}}}},
		"two assistant turns in a row": {{Calls: []Call{{Tool: "wait"}}}, {Assistant: "again"}},
		"the assistant speaks last":    {{Assistant: "done"}},
	} {
		if _, err := Build(testBase(), Case{ID: name, Turns: turns}, Variant{}); err == nil {
			t.Errorf("%s: built", name)
		}
	}
	if _, err := Build(nil, Case{ID: "x"}, Variant{}); err == nil {
		t.Error("no base: built")
	}
}

func TestBuildPlaysARecordedPromptAsItWas(t *testing.T) {
	rec := testBase()
	rec.Messages[0].Blocks[1].Text = "the recorded goal"
	p, err := Build(nil, Case{ID: "x", Goal: "ignored", Prompt: rec}, Variant{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Messages[0].Blocks[1].Text != "the recorded goal" || p.Breakpoints != nil {
		t.Fatalf("got %+v", p)
	}
}

func TestLoadVariantsPutsTheBuildFirstAndApplyRefusesATypo(t *testing.T) {
	vs, err := LoadVariants([]byte(`{"b":{"wait":"x"},"a":{"spawn":"y"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range vs {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != "current,a,b" {
		t.Fatalf("order %v", names)
	}
	if _, err := LoadVariants([]byte(`{"current":{}}`)); err == nil {
		t.Fatal("a variant named current was accepted")
	}
	if _, err := (Variant{Name: "v", Tools: map[string]string{"wiat": "x"}}).Apply(testTools); err == nil || !strings.Contains(err.Error(), "wiat") {
		t.Fatalf("typo: %v", err)
	}
}
