package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
)

func row(ref string, ctx int, out float64, supported ...string) modelRow {
	return modelRow{Ref: ref, Entry: gateway.Entry{
		Model: cost.Model{ID: ref, ContextTokens: ctx, Price: cost.Price{OutputPerM: out}}, Supported: supported, Modality: "text->text",
	}}
}

func TestModelSearchFiltersAndFavorites(t *testing.T) {
	all := []modelRow{
		row("heimdall/qwen/qwen3.8-27b", 262_000, 0.25, "tools", "reasoning"),
		row("heimdall/qwen/qwen3.8-flash-next", 1_000_000, 0.035, "tools"),
		row("openrouter/moonshotai/kimi-k3", 256_000, 2.5, "tools", "reasoning"),
		row("ollama/llama3:8b", 8_000, 0),
		{Ref: "heimdall/embedder", Entry: gateway.Entry{Model: cost.Model{ID: "embedder", ContextTokens: 8000}, Modality: "text->embedding"}},
	}
	list := func(f modelFilter, fav ...string) []string {
		set := map[string]bool{}
		for _, r := range fav {
			set[r] = true
		}
		var b bytes.Buffer
		if err := printModels(&b, all, f, set); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range strings.Split(strings.TrimSpace(b.String()), "\n")[1:] {
			out = append(out, strings.Fields(strings.TrimPrefix(l, "* "))[0]+map[bool]string{true: "*", false: ""}[strings.HasPrefix(l, "* ")])
		}
		return out
	}
	eq := func(what string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: got %v, want %v", what, got, want)
		}
	}
	eq("all chat models, sorted", list(modelFilter{}),
		"heimdall/qwen/qwen3.8-27b", "heimdall/qwen/qwen3.8-flash-next", "ollama/llama3:8b", "openrouter/moonshotai/kimi-k3")
	eq("every word must match, any case", list(modelFilter{Words: []string{"QWEN", "flash"}}), "heimdall/qwen/qwen3.8-flash-next")
	eq("tools and reasoning", list(modelFilter{Tools: true, Reasoning: true}), "heimdall/qwen/qwen3.8-27b", "openrouter/moonshotai/kimi-k3")
	eq("price ceiling", list(modelFilter{MaxOut: 0.3}), "heimdall/qwen/qwen3.8-27b", "heimdall/qwen/qwen3.8-flash-next", "ollama/llama3:8b")
	eq("context floor", list(modelFilter{MinContext: 256_000}), "heimdall/qwen/qwen3.8-27b", "heimdall/qwen/qwen3.8-flash-next", "openrouter/moonshotai/kimi-k3")
	eq("favorites first and marked", list(modelFilter{}, "openrouter/moonshotai/kimi-k3"),
		"openrouter/moonshotai/kimi-k3*", "heimdall/qwen/qwen3.8-27b", "heimdall/qwen/qwen3.8-flash-next", "ollama/llama3:8b")
	eq("only favorites", list(modelFilter{OnlyFavorite: true}, "ollama/llama3:8b"), "ollama/llama3:8b*")
	if n := len(list(modelFilter{All: true})); n != 5 {
		t.Errorf("--all keeps the embedder: %d rows", n)
	}
}

func TestParseTokens(t *testing.T) {
	for in, want := range map[string]int{"128k": 128_000, "1m": 1_000_000, "1.5M": 1_500_000, "32768": 32768} {
		if got, err := parseTokens(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	if _, err := parseTokens("lots"); err == nil {
		t.Error("nonsense must be refused")
	}
}
