package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
	"github.com/anemos-labs/sleipnir/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }

// isolate gives a test its own home, state directory and working directory, and no provider key of the machine it runs on.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLEIPNIR_HOME", "")
	t.Chdir(t.TempDir())
	cfg, _, _ := config.Load(config.LoadOpts{Home: home, UntrustedProject: true, Environ: func() []string { return nil }})
	for _, kp := range KeyProviders(cfg) {
		t.Setenv(kp.Env, "")
	}
	t.Setenv("SLEIPNIR_MODEL", "")
	return home
}

func entry(ref string, ctx int, in, out float64, modality string, supported ...string) Entry {
	return Entry{Ref: ref, Entry: gateway.Entry{Model: cost.Model{ID: ref, ContextTokens: ctx, Price: cost.Price{InputPerM: in, OutputPerM: out, CacheReadPerM: in}},
		Modality: modality, Supported: supported}}
}

// The filter of `sleipnir models` and the Models page, case by case.
func TestFilterGolden(t *testing.T) {
	plan := entry("chatgpt/gpt-x", 400_000, 0, 0, "text->text", "tools", "reasoning_effort")
	plan.Model.Provider = PlanProvider
	all := []Entry{
		entry("heimdall/qwen/qwen3.8-27b", 262_144, 0.1, 0.2, "text->text", "tools", "reasoning"),
		entry("heimdall/qwen/qwen3.8-flash-next", 262_144, 0.05, 0.1, "text->text"),
		entry("openrouter/moonshotai/kimi-k3", 300_000, 1, 3, "text->text", "tools", "reasoning_effort"),
		entry("ollama/llama3:8b", 0, 0, 0, ""),
		entry("heimdall/embedder", 8000, 0.01, 0, "text->embedding"),
		plan,
	}
	fav := map[string]bool{"ollama/llama3:8b": true}
	for _, tc := range []struct {
		name string
		f    Filter
		want []string
	}{
		{"chat models only", Filter{}, []string{"chatgpt/gpt-x", "heimdall/qwen/qwen3.8-27b", "heimdall/qwen/qwen3.8-flash-next", "ollama/llama3:8b", "openrouter/moonshotai/kimi-k3"}},
		{"all", Filter{All: true}, []string{"chatgpt/gpt-x", "heimdall/embedder", "heimdall/qwen/qwen3.8-27b", "heimdall/qwen/qwen3.8-flash-next", "ollama/llama3:8b", "openrouter/moonshotai/kimi-k3"}},
		{"words, any case", Filter{Words: []string{"QWEN", "flash"}}, []string{"heimdall/qwen/qwen3.8-flash-next"}},
		{"tools and reasoning", Filter{Tools: true, Reasoning: true}, []string{"chatgpt/gpt-x", "heimdall/qwen/qwen3.8-27b", "openrouter/moonshotai/kimi-k3"}},
		{"output price ceiling leaves the plan out", Filter{MaxOut: 0.5}, []string{"heimdall/qwen/qwen3.8-27b", "heimdall/qwen/qwen3.8-flash-next", "ollama/llama3:8b"}},
		{"context floor", Filter{MinContext: 280_000}, []string{"chatgpt/gpt-x", "openrouter/moonshotai/kimi-k3"}},
		{"favourites", Filter{OnlyFavorite: true}, []string{"ollama/llama3:8b"}},
	} {
		var got []string
		for _, e := range all {
			if tc.f.Keep(e, fav) {
				got = append(got, e.Ref)
			}
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestRowsSayWhenAPriceIsUnknown(t *testing.T) {
	plan := entry("chatgpt/gpt-x", 0, 0, 0, "text->text", "tools")
	plan.Model.Provider = PlanProvider
	for _, tc := range []struct {
		e     Entry
		known bool
	}{
		{entry("a/priced", 1000, 1, 2, "text->text"), true},
		{entry("a/output-only", 1000, 0, 2, "text->text"), true},
		{entry("ollama/ids-only", 0, 0, 0, ""), false},
		{plan, false},
	} {
		r := RowOf(tc.e)
		if r.PriceKnown() != tc.known || (r.In == nil) != (r.Out == nil) || (r.In == nil) != (r.Cached == nil) {
			t.Errorf("%s: known=%v in=%v out=%v", tc.e.Ref, r.PriceKnown(), r.In, r.Out)
		}
		if r.Provider != strings.SplitN(tc.e.Ref, "/", 2)[0] || r.Plan != tc.e.Plan() {
			t.Errorf("%s: provider %q plan %v", tc.e.Ref, r.Provider, r.Plan)
		}
	}
	if r := RowOf(entry("a/b", 1, 1.5, 2.5, "text->text")); *r.In != 1.5 || *r.Out != 2.5 || !r.Chat {
		t.Errorf("prices are carried: %+v", r)
	}
}

func TestParseTokens(t *testing.T) {
	for in, want := range map[string]int{"128k": 128_000, "1m": 1_000_000, "32768": 32768, " 1.5M ": 1_500_000} {
		if got, err := ParseTokens(in); err != nil || got != want {
			t.Errorf("ParseTokens(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "lots", "-5k"} {
		if _, err := ParseTokens(bad); err == nil {
			t.Errorf("ParseTokens(%q) accepted", bad)
		}
	}
}

// gatewayServer serves a catalogue and counts the requests it answered.
func gatewayServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts, &n
}

const catalogueBody = `{"data":[
 {"id":"model-a","context_length":128000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000002"},"supported_parameters":["tools"]},
 {"id":"model-b","context_length":0,"architecture":{"modality":"text->text"},"pricing":{}},
 {"id":"embed-c","context_length":8000,"architecture":{"modality":"text->embedding"},"pricing":{"prompt":"0.0000001"}}]}`

func TestCatalogueOffline(t *testing.T) {
	home := isolate(t)
	localSources = func(context.Context, *config.Config) []Source { return nil }
	usableSources = func(cfg *config.Config) []Source { return UsableSources(cfg, false) } // no public catalogue: no network
	t.Cleanup(func() {
		localSources = LocalSources
		usableSources = func(cfg *config.Config) []Source { return UsableSources(cfg, true) }
	})
	ts, hits := gatewayServer(t, catalogueBody)
	t.Setenv("ACME_API_KEY", "acme-test-key")
	cfg := &config.Config{Providers: map[string]config.Provider{"acme": {BaseURL: ts.URL + "/v1", APIKeyEnv: "ACME_API_KEY"}}}
	now := time.Now()
	o := Options{Home: home, Config: cfg, Now: func() time.Time { return now }}

	res := FetchResult(context.Background(), o)
	var acme []Row
	for _, r := range res.Rows {
		if r.Provider == "acme" {
			acme = append(acme, r)
		}
	}
	if len(acme) != 3 || hits.Load() != 1 {
		t.Fatalf("first read: %d acme rows, %d requests, errors %v", len(acme), hits.Load(), res.Errors)
	}
	byRef := map[string]Row{}
	for _, r := range acme {
		byRef[r.Ref] = r
	}
	if r := byRef["acme/model-a"]; r.In == nil || *r.In != 1 || *r.Out != 2 || !r.Tools || !r.Chat {
		t.Errorf("a priced model: %+v", r)
	}
	if r := byRef["acme/model-b"]; r.In != nil || r.Out != nil || r.Cached != nil {
		t.Errorf("a model without prices has unknown prices (null), not $0: %+v", r)
	}
	if r := byRef["acme/embed-c"]; r.Chat {
		t.Errorf("an embedding model is not a chat model: %+v", r)
	}
	cache := CachePath(filepath.Join(home, ".sleipnir", "cache"), ts.URL+"/v1")
	if fi, err := os.Stat(cache); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the catalogue is cached privately: %v %v", fi, err)
	}

	if FetchResult(context.Background(), o); hits.Load() != 1 {
		t.Errorf("a copy younger than the TTL is used: %d requests", hits.Load())
	}
	o.Refresh = true
	if FetchResult(context.Background(), o); hits.Load() != 2 {
		t.Errorf("refresh asks again: %d requests", hits.Load())
	}
	o.Refresh = false
	now = now.Add(CacheTTL + time.Minute)
	if FetchResult(context.Background(), o); hits.Load() != 3 {
		t.Errorf("an old copy is replaced: %d requests", hits.Load())
	}
	ts.Close()
	now = now.Add(48 * time.Hour)
	off := FetchResult(context.Background(), Options{Home: home, Config: cfg, Offline: true, Now: func() time.Time { return now }})
	if len(off.Rows) < 3 {
		t.Errorf("offline, the cached copy is used whatever its age: %d rows, %v", len(off.Rows), off.Errors)
	}
	// a cached file that is not a catalogue is not believed
	if err := os.WriteFile(cache, []byte(`{"not":"a catalogue"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if bad := FetchResult(context.Background(), Options{Home: home, Config: cfg, Offline: true, Now: func() time.Time { return now }}); len(bad.Errors) == 0 {
		t.Errorf("a cache that does not pass the checks is not used: %+v", bad.Rows)
	}
}

func TestFavoritesRoundTripThroughTheConfiguration(t *testing.T) {
	home := isolate(t)
	if _, err := SetFavorite(home, "not-a-ref", true); err == nil {
		t.Error("a reference without a provider is refused")
	}
	favs, err := SetFavorite(home, "acme/model-a", true)
	if err != nil || !slices.Equal(favs, []string{"acme/model-a"}) {
		t.Fatalf("star: %v %v", favs, err)
	}
	before, _ := os.ReadFile(config.UserConfigPath(home))
	if favs, err = SetFavorite(home, "acme/model-a", true); err != nil || len(favs) != 1 {
		t.Errorf("starring again: %v %v", favs, err)
	}
	if after, _ := os.ReadFile(config.UserConfigPath(home)); string(after) != string(before) {
		t.Error("starring a starred model rewrote the file")
	}
	cfg, _, _ := config.Load(config.LoadOpts{Home: home, UntrustedProject: true})
	if !Favorites(cfg)["acme/model-a"] {
		t.Errorf("the configuration holds the favourite: %v", cfg.Models.Favorites)
	}
	if on, err := ToggleFavorite(home, "acme/model-a"); err != nil || on {
		t.Errorf("toggle unstars: %v %v", on, err)
	}
	if favs, _ := EditFavorites(home, true, []string{"a/1", "b/2", "a/1"}); !slices.Equal(favs, []string{"a/1", "b/2"}) {
		t.Errorf("add: %v", favs)
	}
	if _, err := EditFavorites(home, true, []string{"ok/1", "bad"}); err == nil {
		t.Error("one bad reference refuses the whole edit")
	}
}

func TestConcurrentFavouriteChangesAreAllKept(t *testing.T) {
	home := isolate(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := SetFavorite(home, fmt.Sprintf("p/m%02d", i), true); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	cfg, _, err := config.Load(config.LoadOpts{Home: home, UntrustedProject: true})
	if err != nil || len(cfg.Models.Favorites) != 16 {
		t.Errorf("%d of 16 favourites kept (%v)", len(cfg.Models.Favorites), err)
	}
}

func TestProvidersKeySource(t *testing.T) {
	home := isolate(t)
	cfg := &config.Config{Providers: map[string]config.Provider{
		"envp": {BaseURL: "https://e.test/v1", APIKeyEnv: "CATTEST_ENVP_API_KEY"},
		"stop": {BaseURL: "https://s.test/v1", APIKeyEnv: "CATTEST_STOP_API_KEY"},
		"both": {BaseURL: "https://b.test/v1", APIKeyEnv: "CATTEST_BOTH_API_KEY"},
		"none": {BaseURL: "https://n.test/v1", APIKeyEnv: "CATTEST_NONE_API_KEY"},
	}}
	t.Setenv("CATTEST_ENVP_API_KEY", "env-value-1")
	t.Setenv("CATTEST_BOTH_API_KEY", "env-value-2")
	t.Setenv("CATTEST_STOP_API_KEY", "")
	t.Setenv("CATTEST_NONE_API_KEY", "")
	if err := StoreKey(home, "CATTEST_STOP_API_KEY", "stored-value-1"); err != nil {
		t.Fatal(err)
	}
	if err := StoreKey(home, "CATTEST_BOTH_API_KEY", "stored-value-2"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		harden.Provide("CATTEST_STOP_API_KEY", "")
		harden.Provide("CATTEST_BOTH_API_KEY", "")
	})
	got := map[string]KeyStatus{}
	for _, st := range Statuses(cfg, home) {
		got[st.Provider] = st
	}
	for name, want := range map[string]KeyStatus{
		"envp": {Provider: "envp", EnvName: "CATTEST_ENVP_API_KEY", Present: true, Source: SourceEnv},
		"stop": {Provider: "stop", EnvName: "CATTEST_STOP_API_KEY", Present: true, Source: SourceStored},
		"both": {Provider: "both", EnvName: "CATTEST_BOTH_API_KEY", Present: true, Source: SourceEnv}, // the environment wins
		"none": {Provider: "none", EnvName: "CATTEST_NONE_API_KEY", Present: false, Source: SourceNone},
	} {
		if got[name] != want {
			t.Errorf("%s: %+v, want %+v", name, got[name], want)
		}
	}
	// an auth.json that cannot be read: a key is there, where from cannot be told
	if err := os.WriteFile(config.AuthPath(home), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := StatusOf(home, KeyProvider{"stop", "CATTEST_STOP_API_KEY"}); st.Source != SourceUnknown || !st.Present {
		t.Errorf("unreadable store: %+v", st)
	}
	ks := KeyProviders(cfg)
	if ks[0].Name != "heimdall" {
		t.Errorf("Heimdall comes first: %v", ks[:2])
	}
}

func TestSignOutDropsHeldKey(t *testing.T) {
	home := isolate(t)
	t.Setenv("CATTEST_SIGNOUT_API_KEY", "")
	if err := StoreKey(home, "CATTEST_SIGNOUT_API_KEY", "  stored-key-value  "); err != nil {
		t.Fatal(err)
	}
	if harden.Secret("CATTEST_SIGNOUT_API_KEY") != "stored-key-value" {
		t.Fatal("a stored key is held by this process")
	}
	if err := ForgetKey(home, "CATTEST_SIGNOUT_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if v := harden.Secret("CATTEST_SIGNOUT_API_KEY"); v != "" {
		t.Error("after sign-out the process still holds the key")
	}
	m, _ := config.StoredKeys(home)
	if _, ok := m["CATTEST_SIGNOUT_API_KEY"]; ok {
		t.Error("after sign-out auth.json still holds the key")
	}
	if StoreKey(home, "", "x") == nil || StoreKey(home, "X_API_KEY", " ") == nil || ForgetKey(home, "") == nil {
		t.Error("an empty name or key is refused")
	}
}

func TestChatGPTStatusWithoutASignIn(t *testing.T) {
	home := isolate(t)
	if ok, who := ChatGPTStatus(home); ok || who != "" {
		t.Errorf("no sign-in: %v %q", ok, who)
	}
	if ChatGPTSignOut(context.Background(), home) == nil {
		t.Error("signing out without a sign-in says so")
	}
}

func TestCheckKeyWithAnUnknownModelSendsNothing(t *testing.T) {
	isolate(t)
	if kc := CheckKey(context.Background(), &config.Config{}, "nowhere/model"); kc.Checked || kc.Refused {
		t.Errorf("an unknown provider: %+v", kc)
	}
}
