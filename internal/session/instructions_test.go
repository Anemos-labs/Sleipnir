package session

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/memory"
)

func TestInstructionTokenBudget(t *testing.T) {
	for _, tc := range []struct{ configured, window, want int }{
		{0, 200000, 16384}, {0, 32768, 8192}, {0, 8192, 2048},
		{0, 0, 16384}, {0, 1, 1}, {24000, 32768, 24000}, {3000, 200000, 3000},
	} {
		if got := instructionTokenBudget(tc.configured, tc.window); got != tc.want {
			t.Errorf("configured=%d window=%d: got %d, want %d", tc.configured, tc.window, got, tc.want)
		}
	}
}

func TestInstructionsBeyondOldCapRemainCompleteAndStable(t *testing.T) {
	est := core.NewBytesEstimator()
	srcs := []memory.Source{
		{Path: "AGENTS.md", Scope: memory.ScopeProject, Text: strings.Repeat("Follow the project conventions.\n", 1380) + "FINAL PROJECT RULE"},
		{Path: "SLEIPNIR.local.md", Scope: memory.ScopeLocal, Text: "LOCAL OVERRIDE"},
	}
	want := memory.Render(srcs)
	if n := est.Tokens(want); n < 12000 || n >= 16384 {
		t.Fatalf("fixture must reproduce ~12000 tokens, got %d", n)
	}
	for i := 0; i < 2; i++ {
		got, cut := fitInstructions(srcs, instructionTokenBudget(0, 200000), est)
		if got != want || len(cut) != 0 {
			t.Fatalf("instructions were changed or cut: %v", cut)
		}
	}
}

func TestInstructionOverflowPreservesPrecedenceAndReportsPaths(t *testing.T) {
	est := core.NewBytesEstimator()
	srcs := []memory.Source{
		{Path: "~/.sleipnir/SLEIPNIR.md", Scope: memory.ScopeUser, Text: strings.Repeat("Earlier rules.\n", 1000)},
		{Path: "AGENTS.md", Scope: memory.ScopeProject, Text: "PROJECT HEAD\n" + strings.Repeat("Project rules.\n", 1000) + "PROJECT TAIL"},
		{Path: "SLEIPNIR.local.md", Scope: memory.ScopeLocal, Text: "LOCAL RULE MUST SURVIVE"},
	}
	got, cut := fitInstructions(srcs, 3000, est)
	if !strings.Contains(got, "LOCAL RULE MUST SURVIVE") || !strings.Contains(got, "PROJECT HEAD") || strings.Contains(got, "PROJECT TAIL") || strings.Contains(got, "Earlier rules.") {
		t.Fatal("higher-precedence files were not retained before earlier files")
	}
	if !reflect.DeepEqual(cut, []string{"~/.sleipnir/SLEIPNIR.md", "AGENTS.md"}) || !strings.Contains(got, "instruction files truncated") {
		t.Fatalf("missing overflow details: %v", cut)
	}
	if est.Tokens(got) > 3000 || !strings.Contains(srcs[1].Text, "PROJECT TAIL") {
		t.Fatal("overflow exceeded the budget or mutated the loaded sources")
	}
}

func TestInstructionOverflowStaysBoundedAtLineBoundaries(t *testing.T) {
	est := core.NewBytesEstimator()
	srcs := []memory.Source{
		{Path: strings.Repeat("界", 100) + ".md", Scope: memory.ScopeProject, Text: strings.Repeat("é中😀", 5000)},
		{Path: "nested/AGENTS.md", Scope: memory.ScopeDir, Text: strings.Repeat("Always verify.\n", 60)},
		{Path: "SLEIPNIR.local.md", Scope: memory.ScopeLocal, Text: "KEEP WHOLE LINE é中😀\n"},
	}
	for budget := 0; budget < 1200; budget++ {
		got, cut := fitInstructions(srcs, budget, est)
		if n := est.Tokens(got); n > budget || !utf8.ValidString(got) {
			t.Fatalf("budget=%d got %d tokens or broken UTF-8", budget, n)
		}
		if len(cut) == 0 || strings.Contains(got, "é中😀é中😀") {
			t.Fatalf("budget=%d: oversized line was partially included or not reported", budget)
		}
		if budget >= 500 && !strings.Contains(got, "KEEP WHOLE LINE é中😀") {
			t.Fatalf("budget=%d dropped the highest-precedence source", budget)
		}
	}
}
