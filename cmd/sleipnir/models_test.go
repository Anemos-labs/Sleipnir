package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("1\n")), &out, nosecret, true); err != nil {
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
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("")), &out, nosecret, true); err != nil || model != "" || out.Len() != 0 {
		t.Errorf("second run: %q %v %q", model, err, out.String())
	}
	// Not on a terminal: never asked.
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("")), &out, nosecret, false); err != nil || model != "" {
		t.Errorf("no terminal: %q %v", model, err)
	}
}

func nosecret() (string, error) { return "", errors.New("no key should be asked for here") }

// Nothing at all: no key, no config. The person picks a provider (Heimdall is the first), pastes the key, picks a model; the key is kept in
// auth.json (mode 0600, never in the config file) and is in use for this very run; the config file is written.
func TestFirstRunWithNoKeyAsksForTheKeyAndKeepsIt(t *testing.T) {
	var gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		io.WriteString(w, `{"data":[{"id":"only-model"}]}`)
	}))
	defer ts.Close()
	_, home := projectDir(t)
	unsetProviderKeys(t)
	t.Setenv("HEIMDALL_BASE_URL", ts.URL+"/v1")
	t.Cleanup(func() { harden.Provide("HEIMDALL_API_KEY", "") })
	var model string
	var out bytes.Buffer
	secret := func() (string, error) { return "sk-pasted-key-123", nil }
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("1\n1\n")), &out, secret, true); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if model != "heimdall/only-model" || !strings.Contains(out.String(), "1. heimdall  (recommended)") {
		t.Fatalf("%q\n%s", model, out.String())
	}
	b, err := os.ReadFile(filepath.Join(home, ".sleipnir", "auth.json"))
	if err != nil || !strings.Contains(string(b), `"HEIMDALL_API_KEY": "sk-pasted-key-123"`) {
		t.Fatalf("auth.json: %v %s", err, b)
	}
	if fi, _ := os.Stat(filepath.Join(home, ".sleipnir", "auth.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode %v", fi.Mode().Perm())
	}
	if c, _ := os.ReadFile(filepath.Join(home, ".sleipnir", "config.json")); strings.Contains(string(c), "sk-pasted") {
		t.Error("a key must never be in the config file, which people share")
	}
	if harden.Secret("HEIMDALL_API_KEY") != "sk-pasted-key-123" {
		t.Error("the key is not in use for this run")
	}
	_ = gotKey
}

func TestLoginRefusesWhatTakesNoKeyAndLogoutForgets(t *testing.T) {
	_, home := projectDir(t)
	cfg, _, _ := config.Load(config.LoadOpts{UntrustedProject: true})
	in := bufio.NewReader(strings.NewReader(""))
	if _, err := login(context.Background(), in, &bytes.Buffer{}, nosecret, cfg, "ollama", nil); err == nil || !strings.Contains(err.Error(), "takes a key") {
		t.Errorf("a local server has no key to store: %v", err)
	}
	t.Setenv("GROQ_API_KEY", "")
	t.Cleanup(func() { harden.Provide("GROQ_API_KEY", "") })
	if name, err := login(context.Background(), in, &bytes.Buffer{}, func() (string, error) { return " sk-groq \n", nil }, cfg, "Groq", nil); err != nil || name != "groq" {
		t.Fatalf("%q %v", name, err)
	}
	keys, _ := config.StoredKeys(home)
	if keys["GROQ_API_KEY"] != "sk-groq" {
		t.Errorf("stored (trimmed): %v", keys)
	}
	if err := cmdLogout(context.Background(), []string{"groq"}); err != nil {
		t.Fatal(err)
	}
	if keys, _ := config.StoredKeys(home); len(keys) != 0 {
		t.Errorf("after logout: %v", keys)
	}
}

func TestARefusedKeyTellsThePersonWhatToDo(t *testing.T) {
	var out bytes.Buffer
	err := &provider.Error{Kind: provider.ErrAuth, Status: 401, Message: "Missing or invalid API key"}
	reportError(&out, err)
	if !strings.Contains(out.String(), "sleipnir login") || !strings.Contains(out.String(), "wins over the stored one") {
		t.Errorf("%s", out.String())
	}
	out.Reset()
	reportError(&out, errors.New("something else"))
	if strings.Contains(out.String(), "login") {
		t.Errorf("only a refused key gets the hint: %s", out.String())
	}
}

func TestInspectTakesASessionIdOrLatestLikeReplay(t *testing.T) {
	_, home := projectDir(t)
	t.Setenv("SLEIPNIR_HOME", home)
	for _, id := range []string{"20260101-000001-aaaaaa", "20260101-000002-bbbbbb"} {
		d := filepath.Join(home, "sessions", id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "events.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := inspectRoot("20260101-000001"); err != nil || filepath.Base(got) != "20260101-000001-aaaaaa" {
		t.Errorf("the start of an id: %q %v", got, err)
	}
	if got, err := inspectRoot("latest"); err != nil || filepath.Base(got) == "" {
		t.Errorf("latest: %q %v", got, err)
	}
	dir := t.TempDir()
	if got, err := inspectRoot(dir); err != nil || got != dir {
		t.Errorf("a directory stays as it is: %q %v", got, err)
	}
	if _, err := inspectRoot("nonsense-id"); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Errorf("nothing: %v", err)
	}
}

// Ollama (or another local server) is running and nothing has a key: the first run offers it beside the hosted providers, needs no key for it,
// lists what it serves and keeps the choice; `sleipnir models` lists it too.
func TestFirstRunFindsALocalServerAndNeedsNoKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"qwen3:8b"},{"id":"llama3:8b"}]}`)
	}))
	defer ts.Close()
	_, home := projectDir(t)
	unsetProviderKeys(t)
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, ".sleipnir", "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"providers":{"ollama":{"base_url":"`+ts.URL+`/v1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var model string
	var out bytes.Buffer
	// The hosted providers take a key; the running local server is the choice after them.
	cfg, _, _ := config.Load(config.LoadOpts{UntrustedProject: true})
	n := len(loginChoices(cfg)) + 2 // after the providers that take a key and the ChatGPT plan
	if err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader(fmt.Sprintf("%d\n1\n", n))), &out, nosecret, true); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if model != "ollama/llama3:8b" || !strings.Contains(out.String(), fmt.Sprintf("%d. ollama  (running on this machine, no key)", n)) {
		t.Fatalf("%q\n%s", model, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".sleipnir", "auth.json")); err == nil {
		t.Error("a local server has no key to keep")
	}
	if cfg := readJSON(t, cfgPath); sub(cfg, "models", "default") != "ollama/llama3:8b" || sub(cfg, "providers", "ollama", "base_url") == nil {
		t.Errorf("the config keeps the model beside the provider the person wrote: %v", cfg)
	}
}

// A catalogue that lists models for anyone says nothing about the key, so the key just typed is tried with one small request: a refusal
// ends the setup and the key is not kept, instead of the first goal failing later.
func TestFirstRunRefusesAKeyTheProviderRejects(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"Missing or invalid API key"}}`)
			return
		}
		io.WriteString(w, `{"data":[{"id":"only-model"}]}`)
	}))
	defer ts.Close()
	_, home := projectDir(t)
	unsetProviderKeys(t)
	t.Setenv("HEIMDALL_BASE_URL", ts.URL+"/v1")
	t.Cleanup(func() { harden.Provide("HEIMDALL_API_KEY", "") })
	var model string
	var out bytes.Buffer
	err := ensureModel(context.Background(), &model, bufio.NewReader(strings.NewReader("1\n1\n")), &out, func() (string, error) { return "sk-typo", nil }, true)
	if err == nil || !strings.Contains(err.Error(), "did not accept that key") || model != "" {
		t.Fatalf("%q %v\n%s", model, err, out.String())
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".sleipnir", "auth.json")); strings.Contains(string(b), "sk-typo") {
		t.Errorf("the rejected key was kept: %s", b)
	}
	if harden.Secret("HEIMDALL_API_KEY") != "" {
		t.Error("the rejected key is still in use")
	}
}

// A ChatGPT plan has no key: the sign-in is what makes it usable. It is offered by the login menu (after the providers that take a key and
// before a local server), asked for its models with the sign-in's token, and its models are marked as the plan's, with no price.
func TestTheChatGPTPlanIsUsableOnlyWhenSignedInAndItsModelsSayPlan(t *testing.T) {
	_, home := projectDir(t)
	for _, k := range []string{"SLEIPNIR_MODEL", "HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY", "GROQ_API_KEY"} {
		t.Setenv(k, "")
	}
	cfg, _, _ := config.Load(config.LoadOpts{UntrustedProject: true})
	if slices.Contains(usableProviders(cfg, false), "chatgpt") {
		t.Error("not signed in, the plan is not usable")
	}
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o700); err != nil {
		t.Fatal(err)
	}
	conn := `{"version":1,"host_id":"urn:uuid:x","client_id":"c","access_token":"at","expires_at_ms":` + strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `,"refresh_token":"rt"}`
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "chatgpt.json"), []byte(conn), 0o600); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(usableProviders(cfg, false), "chatgpt") {
		t.Fatal("signed in, the plan is usable")
	}
	var plan modelSource
	for _, s := range usableSources(cfg, false) {
		if s.name == "chatgpt" {
			plan = s
		}
	}
	if !plan.plan {
		t.Errorf("the source says it is a plan's: %+v", plan)
	}
	row := modelRow{Ref: "chatgpt/gpt-x", Entry: gateway.Entry{Model: cost.Model{ID: "gpt-x", Provider: planProvider}, Modality: "text->text", Supported: []string{"tools"}}}
	if got := modelLine(row, nil); !strings.Contains(got, "plan") || strings.Contains(got, "$") {
		t.Errorf("a plan's model has no price: %q", got)
	}
	if got := priceOut(modelRow{Entry: gateway.Entry{Model: cost.Model{Price: cost.Price{OutputPerM: 2.5}}}}); got != "$2.5/M out" {
		t.Errorf("a priced model keeps its price: %q", got)
	}
}

// unsetProviderKeys clears the key variable of every provider that takes one, and SLEIPNIR_MODEL, so that a test starts like a first run
// whatever the machine it runs on has set.
func unsetProviderKeys(t *testing.T) {
	t.Helper()
	t.Setenv("SLEIPNIR_MODEL", "")
	for _, n := range session.ProviderNames(nil) {
		if _, env, ok := session.ProviderInfo(nil, n); ok && env != "" {
			t.Setenv(env, "")
		}
	}
}
