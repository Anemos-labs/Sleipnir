package session_test

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// signedIn writes the file a ChatGPT sign-in leaves, under a home of its own: a token that is good for an hour.
func signedIn(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".sleipnir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	conn := map[string]any{"version": 1, "host_id": "urn:uuid:x", "client_id": "client_1", "subject": "u", "email": "me@example.com",
		"access_token": "at-live", "expires_at_ms": time.Now().Add(time.Hour).UnixMilli(), "refresh_token": "rt-1", "id_token": "x.y.z"}
	b, _ := json.Marshal(conn)
	if err := os.WriteFile(filepath.Join(dir, "chatgpt.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func planConfig(base string) *config.Config {
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"chatgpt": {Dialect: config.DialectOpenAIResponses, Auth: config.AuthChatGPTPlan, BaseURL: base}}
	return cfg
}

// A ChatGPT sign-in is a provider like another: it is ready when there is a sign-in, builds a client that sends the plan's token, charges
// nothing per token, and speaks the Responses dialect in the plan's way (tools in a namespace, no output limit).
func TestAChatGPTSignInIsAProviderThatSendsThePlansToken(t *testing.T) {
	var gotAuth, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotAuth, gotBody = r.Header.Get("Authorization"), string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"r1","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":10,"output_tokens":2}}}`+"\n\n")
	}))
	defer ts.Close()
	cfg := planConfig(ts.URL + "/v1")
	p, _ := session.LookupProvider(cfg, "chatgpt")

	t.Setenv("HOME", t.TempDir())
	if session.ProviderReady(p) {
		t.Error("with no sign-in the provider is not ready")
	}
	if _, _, err := session.BuildProvider(cfg, session.ModelRef{Provider: "chatgpt", Model: "gpt-test"}, session.ProviderOptions{}); err == nil || !strings.Contains(err.Error(), "sleipnir login chatgpt") {
		t.Errorf("not signed in: %v", err)
	}

	signedIn(t)
	if !session.ProviderReady(p) {
		t.Fatal("with a sign-in the provider is ready")
	}
	client, model, err := session.BuildProvider(cfg, session.ModelRef{Provider: "chatgpt", Model: "gpt-test"}, session.ProviderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if model.Price.InputPerM != 0 || model.Price.OutputPerM != 0 {
		t.Errorf("a plan is paid for by the month, not by the token: %+v", model.Price)
	}
	if client.Profile().Dialect != "openai-responses" {
		t.Errorf("dialect %q", client.Profile().Dialect)
	}
	req := &provider.Request{Prompt: &core.Prompt{Model: "gpt-test", System: []core.Block{core.Text("sys")}, Params: core.Params{MaxTokens: 100},
		Tools:    []core.ToolSpec{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("hello")}}}}}
	resp, err := client.Do(context.Background(), req, nil)
	if err != nil || resp.Turn.PlainText() != "hi" {
		t.Fatalf("%v %+v", err, resp)
	}
	if gotAuth != "Bearer at-live" {
		t.Errorf("authorization %q: the plan's token", gotAuth)
	}
	if !strings.Contains(gotBody, `"type":"namespace"`) || strings.Contains(gotBody, "max_output_tokens") || !strings.Contains(gotBody, `"store":false`) {
		t.Errorf("the plan's rules: %s", gotBody)
	}
}

// A ChatGPT plan bills nothing per token, so cost_usd stays 0 and no budget counts the plan's usage, however small the budget; what
// the usage would cost on the API, at the built-in table's list price, is recorded beside it (list_usd on every response, the list prices
// with plan: true in session.start, list_usd and plan on session.end and in the Result), for the interfaces to show.
func TestAPlanSessionShowsTheListPriceAndBudgetsCountTheBill(t *testing.T) {
	signedIn(t)
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		// 100k prompt tokens, 80k of them read from the cache, and 2k out: at gpt-6-luna's list price ($0.10 in, $0.01 cached, $0.50 out
		// per million) that is $0.0038 a request
		io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"r1","model":"gpt-6-luna","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":100000,"input_tokens_details":{"cached_tokens":80000},"output_tokens":2000}}}`+"\n\n")
	}))
	defer ts.Close()
	cfg := planConfig(ts.URL + "/v1")
	repo := newRepo(t)
	s, err := session.New(context.Background(), session.Options{
		Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Model: "chatgpt/gpt-6-luna", Cwd: repo, Root: repo,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: true,
		BudgetUSD: 0.001, // less than one request comes to at the list price
	})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Model.Plan || s.Model.Price != (cost.Price{}) || s.Model.ListPrice.InputPerM != 0.10 || s.Model.ID != "gpt-6-luna" {
		t.Fatalf("the plan's model: %+v", s.Model)
	}
	var res *session.Result
	for turn := 0; turn < 3; turn++ {
		if res, err = s.Run(context.Background(), "say done"); err != nil {
			t.Fatalf("turn %d: the plan's usage stopped the run against a $0.001 budget: %v", turn+1, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	const perRequest = 0.0038
	if requests != 3 || res.CostUSD != 0 || !res.Plan || math.Abs(res.ListUSD-3*perRequest) > 1e-9 {
		t.Fatalf("requests %d, result %+v", requests, res)
	}
	var responses int
	for _, e := range readEvents(t, res.Dir) {
		var p map[string]any
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		switch e.Type {
		case events.TypeModelResponse:
			responses++
			if p["cost_usd"] != 0.0 || math.Abs(p["list_usd"].(float64)-perRequest) > 1e-9 {
				t.Errorf("model.response: cost_usd %v, list_usd %v", p["cost_usd"], p["list_usd"])
			}
		case events.TypeSessionStart:
			rec, _ := p["models"].(map[string]any)["gpt-6-luna"].(map[string]any)
			if rec["plan"] != true || rec["source"] != "table" || rec["input_per_m"] != 0.1 || rec["cache_read_per_m"] != 0.01 || rec["output_per_m"] != 0.5 || rec["cache_write_5m_per_m"] != 0.125 {
				t.Errorf("session.start records %v", rec)
			}
		case events.TypeSessionEnd:
			if p["cost_usd"] != 0.0 || p["plan"] != true || math.Abs(p["list_usd"].(float64)-3*perRequest) > 1e-9 {
				t.Errorf("session.end: %v", p)
			}
		}
	}
	if responses != 3 {
		t.Errorf("%d model.response events", responses)
	}
	if sum := session.Summarize(filepath.Join(res.Dir, "events.jsonl")); !sum.Plan || sum.CostUSD != 0 || math.Abs(sum.ListUSD-3*perRequest) > 1e-9 {
		t.Errorf("listing summary: %+v", sum)
	}
}

// A team whose manager is billed per token and whose worker runs a ChatGPT plan's model the table does not know: the worker's usage
// has no list price (list_usd 0 on its responses), and the session still ended on a plan, so session.end says plan and list_usd 0.
func TestABilledTeamWithAWorkerOnAnUnknownPlanModelEndsOnAPlan(t *testing.T) {
	signedIn(t)
	repo := newRepo(t)
	var planCalls atomic.Int32
	plan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		planCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"w1","model":"gpt-next-x","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"surveyed"}]}],"usage":{"input_tokens":5000,"output_tokens":100}}}`+"\n\n")
	}))
	defer plan.Close()
	manager := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		if _, role, _ := whoIs(c); role != "manager" {
			return mock.Reply{Text: "nothing to digest"}
		}
		if assistantTurns(c) == 0 {
			return mock.Reply{Text: "dispatching", ToolCalls: []mock.ToolCall{
				call("t1", "task", map[string]any{"action": "create", "title": "Survey the code", "role": "scout"}),
				call("s1", "spawn", map[string]any{"role": "scout", "task": "T1"}),
			}}
		}
		for deadline := time.Now().Add(30 * time.Second); planCalls.Load() == 0 && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond) // the worker has asked its plan's model before the manager finishes
		}
		return mock.Reply{Text: "finished"}
	})
	mts := manager.Start()
	defer mts.Close()

	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{
		"gw":   {BaseURL: mts.URL},
		"plan": {Dialect: config.DialectOpenAIResponses, Auth: config.AuthChatGPTPlan, BaseURL: plan.URL + "/v1"},
	}
	s, err := session.New(context.Background(), session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Model: "gw/acme/main",
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: true,
		Swarm: true, Workers: 1, RoleModels: map[string]string{"scout": "plan/gpt-next-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Model.Plan {
		t.Fatal("the manager's model is billed per token, not a plan's")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := s.Run(ctx, "survey the repository")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if planCalls.Load() == 0 {
		t.Fatal("the worker never asked its plan's model")
	}
	if !res.Plan || res.ListUSD != 0 || res.CostUSD <= 0 {
		t.Errorf("result: plan %v, list %v, cost %v; want a plan's session with no list price and a billed manager", res.Plan, res.ListUSD, res.CostUSD)
	}
	var end map[string]any
	var planResponses int
	for _, e := range readEvents(t, res.Dir) {
		var p map[string]any
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		switch e.Type {
		case events.TypeModelResponse:
			if v, ok := p["list_usd"]; ok {
				planResponses++
				if v != 0.0 || p["cost_usd"] != 0.0 {
					t.Errorf("a response of the unknown plan model: list_usd %v, cost_usd %v", v, p["cost_usd"])
				}
			}
		case events.TypeSessionEnd:
			end = p
		}
	}
	if planResponses == 0 {
		t.Error("no response carried list_usd: the plan's answers are not marked")
	}
	if end == nil || end["plan"] != true || end["list_usd"] != 0.0 || !(end["cost_usd"].(float64) > 0) {
		t.Errorf("session.end: %v", end)
	}
	if sum := session.Summarize(filepath.Join(res.Dir, "events.jsonl")); !sum.Plan || sum.ListUSD != 0 {
		t.Errorf("listing summary: %+v", sum)
	}
}

// The same model on the API with a key is billed at the table's price, and a response carries no list_usd: cost_usd is the figure.
func TestAnAPIKeyModelIsBilledAtTheTablePriceWithoutAListFigure(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	_, m, err := session.BuildProvider(config.Defaults(), session.ModelRef{Provider: "openai", Model: "gpt-6.1-sol"}, session.ProviderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Plan || m.ListPrice != (cost.Price{}) || m.Price.InputPerM != 2 || m.Price.CacheReadPerM != 0.10 || m.Price.OutputPerM != 10 || m.ID != "gpt-6.1-sol" {
		t.Fatalf("openai/gpt-6.1-sol: %+v", m)
	}
	// A marketplace names the model with its author: the request keeps that name, the table still prices it.
	t.Setenv("OPENROUTER_API_KEY", "k")
	if _, m, err = session.BuildProvider(config.Defaults(), session.ModelRef{Provider: "openrouter", Model: "openai/gpt-6-luna"}, session.ProviderOptions{}); err != nil || m.ID != "openai/gpt-6-luna" || m.Price.OutputPerM != 0.5 {
		t.Fatalf("openrouter/openai/gpt-6-luna: %+v %v", m, err)
	}
}

// Where a ChatGPT sign-in is sent is the provider's own address: not an environment variable's, and not a project file's.
func TestAChatGPTSignInIsNeverSentWhereAProjectOrTheEnvironmentSays(t *testing.T) {
	signedIn(t)
	t.Setenv("CHATGPT_BASE_URL", "http://127.0.0.1:1/evil")
	cfg := planConfig("http://127.0.0.1:2/v1")
	if _, _, err := session.BuildProvider(cfg, session.ModelRef{Provider: "chatgpt", Model: "m"}, session.ProviderOptions{}); err != nil {
		t.Fatalf("the user's own address is used: %v", err)
	}
	p := cfg.Providers["chatgpt"]
	p.BaseURLFromProject = true
	cfg.Providers["chatgpt"] = p
	if _, _, err := session.BuildProvider(cfg, session.ModelRef{Provider: "chatgpt", Model: "m"}, session.ProviderOptions{}); err == nil {
		t.Error("a project file chose where the plan's token goes")
	}
}

// A bare model id goes to the first provider that is ready, and the ChatGPT plan is the last of them.
func TestTheChatGPTPlanIsTheLastDefaultProvider(t *testing.T) {
	signedIn(t)
	unsetProviderKeys(t)
	if got, err := session.DefaultProvider(config.Defaults()); err != nil || got != "chatgpt" {
		t.Errorf("the only provider that is ready: %q %v", got, err)
	}
	t.Setenv("NEBIUS_API_KEY", "k") // not one of the usual ones: any provider with a key comes before the plan
	if got, _ := session.DefaultProvider(config.Defaults()); got != "nebius" {
		t.Errorf("a key comes before the plan: %q", got)
	}
	t.Setenv("GROQ_API_KEY", "k")
	if got, _ := session.DefaultProvider(config.Defaults()); got != "groq" {
		t.Errorf("the usual providers come before the others: %q", got)
	}
}

func TestConfigRefusesAnUnknownAuthAndAPlanOnTheWrongDialect(t *testing.T) {
	for _, p := range []config.Provider{{Auth: "magic"}, {Auth: config.AuthChatGPTPlan, Dialect: config.DialectAnthropic}} {
		cfg := config.Defaults()
		cfg.Providers = map[string]config.Provider{"x": p}
		if issues := cfg.Validate(); len(issues) == 0 {
			t.Errorf("accepted %+v", p)
		}
	}
}

// unsetProviderKeys clears the key variable of every provider that takes one.
func unsetProviderKeys(t *testing.T) {
	t.Helper()
	for _, n := range session.ProviderNames(nil) {
		if _, env, ok := session.ProviderInfo(nil, n); ok && env != "" {
			t.Setenv(env, "")
		}
	}
}
