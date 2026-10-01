package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestBareSleipnirOpensTheChatOnATerminal(t *testing.T) {
	for _, tc := range []struct {
		argv    []string
		tty     bool
		cmd     string
		wantLen int
	}{
		{nil, true, "chat", 0},
		{nil, false, "", 0}, // a script gets the usage error, not a chat that waits on a pipe
		{[]string{"--model", "x/y"}, true, "chat", 2},
		{[]string{"--model", "x/y"}, false, "--model", 1},
		{[]string{"run", "do it"}, true, "run", 1},
		{[]string{"--help"}, true, "--help", 0},
		{[]string{"--version"}, true, "--version", 0},
		{[]string{"models", "qwen"}, true, "models", 1},
	} {
		cmd, args := defaultToChat(tc.argv, tc.tty)
		if cmd != tc.cmd || len(args) != tc.wantLen {
			t.Errorf("%v tty=%v: %q %v", tc.argv, tc.tty, cmd, args)
		}
	}
}

func TestPickModelSearchesAndChoosesFromWhatTheProviderLists(t *testing.T) {
	rows := []modelRow{
		row("heimdall/zeta/z-1", 8_000, 1),
		row("heimdall/alpha/a-1", 1_000_000, 0.02, "tools"),
		row("heimdall/alpha/a-2", 262_000, 0.25, "tools", "reasoning"),
		{Ref: "heimdall/embedder", Entry: gateway.Entry{Model: cost.Model{ID: "embedder"}, Modality: "text->embedding"}},
	}
	ask := func(input string, fav map[string]bool) (string, string, error) {
		var out bytes.Buffer
		ref, err := pickModel(bufio.NewReader(strings.NewReader(input)), &out, rows, fav)
		return ref, out.String(), err
	}
	// Alphabetical, numbered, no embedder; a number chooses.
	ref, out, err := ask("2\n", nil)
	if err != nil || ref != "heimdall/alpha/a-2" || strings.Contains(out, "embedder") || !strings.Contains(out, " 1. heimdall/alpha/a-1") {
		t.Fatalf("%q %v\n%s", ref, err, out)
	}
	// Words narrow the list first; the numbers then count the narrowed rows.
	if ref, _, err := ask("zeta\n1\n", nil); err != nil || ref != "heimdall/zeta/z-1" {
		t.Errorf("search then choose: %q %v", ref, err)
	}
	if _, out, _ := ask("nothing-like-this\nq\n", nil); !strings.Contains(out, "nothing matches") {
		t.Errorf("an empty search says so:\n%s", out)
	}
	// Favorites come first.
	if ref, _, err := ask("1\n", map[string]bool{"heimdall/zeta/z-1": true}); err != nil || ref != "heimdall/zeta/z-1" {
		t.Errorf("favorites lead: %q %v", ref, err)
	}
	// A number beyond the rows is a search, not a choice; q and end of input give up.
	if _, _, err := ask("q\n", nil); err == nil {
		t.Error("q must not choose")
	}
	if _, _, err := ask("", nil); err == nil {
		t.Error("end of input must not choose")
	}
	if _, err := pickModel(bufio.NewReader(strings.NewReader("1\n")), &bytes.Buffer{}, nil, nil); err == nil {
		t.Error("no models: an error, not a hang")
	}
}

// The first run on a terminal with a key and no model: the provider is asked, the person chooses, and the user's config file is written
// with the model and the permission mode. The second run is not asked again.
func TestFirstTimeSetupWritesTheConfigFromTheProvidersOwnList(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"data":[{"id":"zeta-1"},{"id":"alpha-1"}]}`)
	}))
	defer ts.Close()
	_, home := projectDir(t)
	cfgPath := filepath.Join(home, ".sleipnir", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_API_KEY", "k")
	for _, k := range []string{"SLEIPNIR_MODEL", "HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	provider := `{"providers":{"fake":{"base_url":"` + ts.URL + `/v1","api_key_env":"FAKE_API_KEY"}}}`
	// The config of providers is written by the person (not by setup): setup adds the model and the mode to it.
	if err := os.WriteFile(cfgPath, []byte(provider), 0o600); err != nil {
		t.Fatal(err)
	}
	var model string
	var out bytes.Buffer
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("1\n")), &out, true); err != nil {
		t.Fatal(err)
	}
	if model != "fake/alpha-1" {
		t.Fatalf("chose %q\n%s", model, out.String())
	}
	cfg := readJSON(t, cfgPath)
	if sub(cfg, "models", "default") != "fake/alpha-1" || sub(cfg, "providers", "fake", "base_url") == nil {
		t.Errorf("the config after setup: %v", cfg)
	}
	// Asked once: with a default in the config, nothing is asked and nothing changes.
	model, out = "", bytes.Buffer{}
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("")), &out, true); err != nil || model != "" || out.Len() != 0 {
		t.Errorf("second run: %q %v %q", model, err, out.String())
	}
	// Not on a terminal: never asked.
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("")), &out, false); err != nil || model != "" {
		t.Errorf("no terminal: %q %v", model, err)
	}
}

func TestWithNoKeyAtAllTheGuideNamesHeimdallFirst(t *testing.T) {
	_, _ = projectDir(t)
	for _, k := range []string{"SLEIPNIR_MODEL", "HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY", "GROQ_API_KEY", "CEREBRAS_API_KEY", "DEEPINFRA_API_KEY"} {
		t.Setenv(k, "")
	}
	var model string
	err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("")), &bytes.Buffer{}, true)
	if err == nil || !strings.Contains(err.Error(), "Recommended: Heimdall.   export HEIMDALL_API_KEY=") || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") || !strings.Contains(err.Error(), "ollama/") {
		t.Errorf("the guide: %v", err)
	}
}

// No config file at all: the setup says so, and the file it writes holds the model and the mode.
func TestFirstTimeSetupCreatesTheConfigFile(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"only-model"}]}`)
	}))
	defer ts.Close()
	_, home := projectDir(t)
	t.Setenv("SLEIPNIR_MODEL", "")
	t.Setenv("HEIMDALL_API_KEY", "k")
	t.Setenv("HEIMDALL_BASE_URL", ts.URL+"/v1") // a loopback address: the key may go there
	cfgPath := filepath.Join(home, ".sleipnir", "config.json")
	var model string
	var out bytes.Buffer
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("1\n")), &out, true); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, cfgPath)
	if model != "heimdall/only-model" || sub(cfg, "models", "default") != "heimdall/only-model" || sub(cfg, "permissions", "mode") != "default" {
		t.Errorf("%q %v", model, cfg)
	}
	if !strings.Contains(out.String(), "First-time setup") || !strings.Contains(out.String(), "Wrote "+cfgPath) {
		t.Errorf("the person is told what was written and where:\n%s", out.String())
	}
}
