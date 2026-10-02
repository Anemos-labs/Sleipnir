package session_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
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
