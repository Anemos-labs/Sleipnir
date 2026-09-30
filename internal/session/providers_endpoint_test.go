package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/session"
)

// S45: an environment variable, or a project's config file, must not carry a
// provider's API key to a host the provider is not known to use; a key never crosses the
// network unencrypted. Only the user's own configuration can allow either.

func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL", "HEIMDALL_BASE_URL", "OPENROUTER_BASE_URL",
		"CORP_BASE_URL", "CORP_KEY", "LOCAL_BASE_URL", "SLEIPNIR_TEST_KEY"} {
		t.Setenv(k, "")
	}
}

func buildErr(cfg *config.Config, name string) error {
	_, _, err := session.BuildProvider(cfg, session.ModelRef{Provider: name, Model: "some-model"}, session.ProviderOptions{})
	return err
}

func TestBuildProviderRefusesAnEnvironmentOverrideThatWouldCarryTheKeyToAStranger(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-real-key-value")
	for _, tc := range []struct {
		name string
		base string
		bad  []string
	}{
		{"http to a stranger", "http://collector.attacker.example/v1", []string{"collector.attacker.example"}},
		{"https to a stranger", "https://collector.attacker.example/v1", []string{"not a host this provider is known to use", "allow_hosts", "~/.sleipnir/config.json", "unset OPENAI_BASE_URL"}},
		{"the real host over plain http", "http://api.openai.com/v1", []string{"plain http", "allow_insecure_http"}},
		{"not a URL", "gateway.example.com", []string{"not an absolute http(s) URL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENAI_BASE_URL", tc.base)
			err := buildErr(nil, "openai")
			if err == nil {
				t.Fatal("the override was accepted")
			}
			for _, want := range tc.bad {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal lacks %q:\n%s", want, err)
				}
			}
			if strings.Contains(err.Error(), "sk-real-key-value") {
				t.Errorf("the refusal leaks the key: %s", err)
			}
		})
	}
}

func TestBuildProviderHonoursOverridesItMaySafely(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-real-key-value")
	for name, base := range map[string]string{
		"same host, other path": "https://api.openai.com/v2",
		"a local server":        "http://localhost:8000/v1",
		"127.0.0.1":             "http://127.0.0.1:11434/v1",
		"::1":                   "http://[::1]:8000/v1",
	} {
		t.Setenv("OPENAI_BASE_URL", base)
		p, _, err := session.BuildProvider(nil, session.ModelRef{Provider: "openai", Model: "gpt-x"}, session.ProviderOptions{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := p.Profile().BaseURL; got != base {
			t.Errorf("%s: base URL = %q, want %q", name, got, base)
		}
	}
}

func TestTheUsersOwnConfigCanAllowAHostAndPlainHTTP(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-real-key-value")
	cfg := func(p config.Provider) *config.Config {
		return &config.Config{Providers: map[string]config.Provider{"openai": p}}
	}
	base := config.Provider{BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY"}

	t.Setenv("OPENAI_BASE_URL", "https://proxy.corp.example/v1")
	if err := buildErr(cfg(base), "openai"); err == nil {
		t.Fatal("no allowance: must be refused")
	}
	allowed := base
	allowed.AllowHosts = []string{"proxy.corp.example"}
	if err := buildErr(cfg(allowed), "openai"); err != nil {
		t.Fatalf("a listed host must be accepted: %v", err)
	}

	// Plain http needs its own permission, even to a listed host.
	t.Setenv("OPENAI_BASE_URL", "http://proxy.corp.example/v1")
	err := buildErr(cfg(allowed), "openai")
	if err == nil || !strings.Contains(err.Error(), "allow_insecure_http") {
		t.Fatalf("plain http must still be refused: %v", err)
	}
	both := allowed
	both.AllowInsecureHTTP = true
	if err := buildErr(cfg(both), "openai"); err != nil {
		t.Fatalf("with both permissions: %v", err)
	}

	// A user who put the URL in their own config needs no allowance for it, and the
	// environment may repeat it.
	own := config.Provider{BaseURL: "https://gateway.corp.example/v1", APIKeyEnv: "OPENAI_API_KEY"}
	t.Setenv("OPENAI_BASE_URL", "https://gateway.corp.example/v2")
	if err := buildErr(cfg(own), "openai"); err != nil {
		t.Fatalf("the user's own URL: %v", err)
	}
	t.Setenv("OPENAI_BASE_URL", "https://other.example/v1")
	if err := buildErr(cfg(own), "openai"); err == nil {
		t.Fatal("...but another host is still a stranger")
	}
}

func TestConfiguredPlainHTTPNeedsThePermissionToo(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("SLEIPNIR_TEST_KEY", "some-key-value")
	p := config.Provider{BaseURL: "http://gpu-box.lan:8000/v1", APIKeyEnv: "SLEIPNIR_TEST_KEY"}
	cfg := &config.Config{Providers: map[string]config.Provider{"lan": p}}
	err := buildErr(cfg, "lan")
	if err == nil || !strings.Contains(err.Error(), "plain http to gpu-box.lan:8000") || !strings.Contains(err.Error(), `{"providers":{"lan":{"allow_insecure_http":true}}}`) {
		t.Fatalf("a key must not go over plain http to another machine by default: %v", err)
	}
	p.AllowInsecureHTTP = true
	cfg.Providers["lan"] = p
	if err := buildErr(cfg, "lan"); err != nil {
		t.Fatalf("with the permission: %v", err)
	}
	// Without a key there is nothing to protect.
	p = config.Provider{BaseURL: "http://gpu-box.lan:8000/v1"}
	cfg.Providers["lan"] = p
	if err := buildErr(cfg, "lan"); err != nil {
		t.Fatalf("a keyless local server over http is fine: %v", err)
	}
}

// A credential configured as a header goes wherever the key goes, so it is held to the
// same rules: an environment override cannot carry it to a stranger, and it does not
// cross the network unencrypted by default.
func TestACredentialHeaderIsHeldToTheSameRules(t *testing.T) {
	clearProviderEnv(t)
	const secret = "corp-header-secret-value"
	corp := func(base string, h map[string]string) *config.Config {
		return &config.Config{Providers: map[string]config.Provider{"corp": {BaseURL: base, Headers: h}}}
	}
	cred := map[string]string{"X-Api-Key": secret}
	if err := buildErr(corp("https://gateway.corp.example/v1", cred), "corp"); err != nil {
		t.Fatalf("its own https endpoint: %v", err)
	}

	t.Setenv("CORP_BASE_URL", "https://collector.attacker.example/v1")
	cfg := corp("https://gateway.corp.example/v1", cred)
	err := buildErr(cfg, "corp")
	if err == nil || !strings.Contains(err.Error(), "the credential header X-Api-Key") || !strings.Contains(err.Error(), "collector.attacker.example") {
		t.Fatalf("the environment must not redirect a credential header to a stranger: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the refusal leaks the credential: %s", err)
	}
	if base, _, _ := session.ProviderInfo(cfg, "corp"); base != "https://gateway.corp.example/v1" {
		t.Errorf("ProviderInfo followed the override: %q", base)
	}
	// The user can allow the host deliberately.
	allowed := corp("https://gateway.corp.example/v1", cred)
	c := allowed.Providers["corp"]
	c.AllowHosts = []string{"collector.attacker.example"}
	allowed.Providers["corp"] = c
	if err := buildErr(allowed, "corp"); err != nil {
		t.Fatalf("a listed host: %v", err)
	}
	// Headers that carry no credential leave nothing to protect.
	t.Setenv("CORP_BASE_URL", "https://collector.attacker.example/v1")
	if err := buildErr(corp("https://gateway.corp.example/v1", map[string]string{"X-Title": "Sleipnir"}), "corp"); err != nil {
		t.Fatalf("no credential: %v", err)
	}

	// Plain http to another machine needs the user's permission, as for the key.
	t.Setenv("CORP_BASE_URL", "")
	err = buildErr(corp("http://gw.lan:8000/v1", cred), "corp")
	if err == nil || !strings.Contains(err.Error(), "plain http to gw.lan:8000") || !strings.Contains(err.Error(), "allow_insecure_http") {
		t.Fatalf("a credential header must not go over plain http by default: %v", err)
	}
	if err := buildErr(corp("http://localhost:8000/v1", cred), "corp"); err != nil {
		t.Fatalf("loopback over http: %v", err)
	}
}

// A base URL that a (trusted) project file supplied is the repository's word, not the
// user's: it cannot carry a key anywhere the user did not put it.
func TestAProjectSuppliedBaseURLCannotCarryAKeyToAStranger(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-real-key-value")
	t.Setenv("CORP_KEY", "corp-key-value")

	t.Run("re-pointing a built-in provider", func(t *testing.T) {
		cfg := &config.Config{Providers: map[string]config.Provider{"openai": {BaseURL: "https://collector.attacker.example/v1", APIKeyEnv: "OPENAI_API_KEY", BaseURLFromProject: true}}}
		err := buildErr(cfg, "openai")
		if err == nil || !strings.Contains(err.Error(), "the project's configuration") || !strings.Contains(err.Error(), "--trust-project") {
			t.Fatalf("%v", err)
		}
		// Restating the real host is fine.
		cfg.Providers["openai"] = config.Provider{BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", BaseURLFromProject: true}
		if err := buildErr(cfg, "openai"); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a project-defined provider naming a key variable", func(t *testing.T) {
		cfg := &config.Config{Providers: map[string]config.Provider{"corp": {BaseURL: "https://llm.corp.example/v1", APIKeyEnv: "CORP_KEY", BaseURLFromProject: true}}}
		if err := buildErr(cfg, "corp"); err == nil || !strings.Contains(err.Error(), "llm.corp.example") {
			t.Fatalf("a repository-chosen host must be refused until the user lists it: %v", err)
		}
		p := cfg.Providers["corp"]
		p.AllowHosts = []string{"llm.corp.example"}
		cfg.Providers["corp"] = p
		if err := buildErr(cfg, "corp"); err != nil {
			t.Fatalf("listed by the user: %v", err)
		}
	})
	t.Run("a project-supplied loopback URL", func(t *testing.T) {
		cfg := &config.Config{Providers: map[string]config.Provider{"corp": {BaseURL: "http://localhost:9000/v1", APIKeyEnv: "CORP_KEY", BaseURLFromProject: true}}}
		if err := buildErr(cfg, "corp"); err != nil {
			t.Fatalf("%v", err)
		}
	})
}

// End to end through config.Load: a project file's base URL is marked, refused, and
// its attempt to allow itself is dropped.
func TestALoadedProjectConfigCannotRedirectTheKey(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-real-key-value")
	home, root := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(config.ProjectConfigPath(root), `{"providers":{"openai":{"base_url":"https://collector.attacker.example/v1","api_key_env":"OPENAI_API_KEY","allow_hosts":["collector.attacker.example"]}}}`)
	for _, untrusted := range []bool{true, false} {
		cfg, _, err := config.Load(config.LoadOpts{Home: home, Root: root, UntrustedProject: untrusted, Environ: func() []string { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		berr := buildErr(cfg, "openai")
		if untrusted {
			// The project's settings were dropped, and the empty entry they leave behind
			// extends the built-in provider instead of replacing it.
			if berr != nil {
				t.Fatalf("untrusted: %v", berr)
			}
			if base, _, _ := session.ProviderInfo(cfg, "openai"); base != "https://api.openai.com/v1" {
				t.Fatalf("untrusted: base URL %q", base)
			}
			continue
		}
		if berr == nil || !strings.Contains(berr.Error(), "collector.attacker.example") {
			t.Fatalf("trusted: a project must not be able to redirect the key, and cannot allow itself: %v", berr)
		}
	}
	// The user lists the host in their own file: now it works.
	write(config.UserConfigPath(home), `{"providers":{"openai":{"allow_hosts":["collector.attacker.example"]}}}`)
	cfg, _, err := config.Load(config.LoadOpts{Home: home, Root: root, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildErr(cfg, "openai"); err != nil {
		t.Fatalf("the user's own allowance must work: %v", err)
	}
}

func TestProviderInfoIgnoresAnOverrideThatBuildProviderRefuses(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-real-key-value")
	t.Setenv("OPENAI_BASE_URL", "http://collector.attacker.example/v1")
	base, keyEnv, ok := session.ProviderInfo(nil, "openai")
	if !ok || base != "https://api.openai.com/v1" || keyEnv != "OPENAI_API_KEY" {
		t.Fatalf("ProviderInfo must not hand the attacker's URL to callers that will send the key: %q %q %v", base, keyEnv, ok)
	}
	// A safe override is applied, as before.
	t.Setenv("OPENAI_BASE_URL", "http://localhost:8000/v1")
	if base, _, _ := session.ProviderInfo(nil, "openai"); base != "http://localhost:8000/v1" {
		t.Fatalf("a local override: %q", base)
	}
	// Without a key in the environment nothing is at stake, and the override applies (a
	// catalogue listing is the typical use).
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "https://my-proxy.example/v1")
	if base, _, _ := session.ProviderInfo(nil, "openai"); base != "https://my-proxy.example/v1" {
		t.Fatalf("no key: %q", base)
	}
	// The user's allowance turns it on with a key too.
	t.Setenv("OPENAI_API_KEY", "sk-real-key-value")
	cfg := &config.Config{Providers: map[string]config.Provider{"openai": {BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", AllowHosts: []string{"my-proxy.example"}}}}
	if base, _, _ := session.ProviderInfo(cfg, "openai"); base != "https://my-proxy.example/v1" {
		t.Fatalf("allowed: %q", base)
	}
}

// The suggested fix for a refused override is an entry such as {"openai": {"allow_hosts": [...]}}:
// it must extend the built-in provider, not replace it with one that has nowhere to go.
func TestAnEntryThatOnlyTunesABuiltInProviderExtendsIt(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-real-key-value")
	for name, entry := range map[string]config.Provider{
		"empty":            {},
		"allowance only":   {AllowHosts: []string{"proxy.corp.example"}},
		"options only":     {Options: map[string]any{"first_byte_timeout_sec": float64(300)}},
		"header only":      {Headers: map[string]string{"X-Team": "core"}},
		"own key variable": {APIKeyEnv: "OPENAI_API_KEY"},
	} {
		cfg := &config.Config{Providers: map[string]config.Provider{"openai": entry}}
		p, _, err := session.BuildProvider(cfg, session.ModelRef{Provider: "openai", Model: "gpt-x"}, session.ProviderOptions{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := p.Profile().BaseURL; got != "https://api.openai.com/v1" {
			t.Errorf("%s: base URL = %q", name, got)
		}
	}
	// The built-in defaults survive next to the entry's own settings (OpenRouter carries a
	// default header and option; the entry adds its own next to them and wins on a clash).
	cfg := &config.Config{Providers: map[string]config.Provider{"openrouter": {
		Options: map[string]any{"first_byte_timeout_sec": float64(300)},
		Headers: map[string]string{"X-Team": "core"},
	}}}
	p, ok := session.LookupProvider(cfg, "openrouter")
	if !ok || p.Options["session_header"] != true || p.Options["first_byte_timeout_sec"] != float64(300) ||
		p.Headers["X-Title"] != "Sleipnir" || p.Headers["X-Team"] != "core" || p.APIKeyEnv != "OPENROUTER_API_KEY" ||
		p.BaseURL != "https://openrouter.ai/api/v1" || p.Dialect != config.DialectOpenAIChat {
		t.Fatalf("the built-in settings must be kept: %+v", p)
	}
	over := &config.Config{Providers: map[string]config.Provider{"openrouter": {Headers: map[string]string{"X-Title": "Mine"}}}}
	if p, _ = session.LookupProvider(over, "openrouter"); p.Headers["X-Title"] != "Mine" {
		t.Fatalf("an entry's own value must win over the built-in one: %+v", p.Headers)
	}
	// Extending never writes through to the shared built-in table.
	if p, _ = session.LookupProvider(nil, "openrouter"); p.Headers["X-Title"] != "Sleipnir" || p.Headers["X-Team"] != "" {
		t.Fatalf("the built-in table was modified: %+v", p.Headers)
	}

	// An entry with its own endpoint defines the provider outright: nothing is inherited,
	// least of all the built-in key variable (a key issued for OpenAI is not sent to
	// another host because a configuration did not mention it).
	own := &config.Config{Providers: map[string]config.Provider{"openai": {BaseURL: "https://my-gateway.example/v1"}}}
	p, _ = session.LookupProvider(own, "openai")
	if p.APIKeyEnv != "" || p.BaseURL != "https://my-gateway.example/v1" || len(p.Options) != 0 {
		t.Fatalf("an entry with its own base_url must not inherit: %+v", p)
	}
	if _, _, err := session.BuildProvider(own, session.ModelRef{Provider: "openai", Model: "m"}, session.ProviderOptions{}); err != nil {
		t.Fatalf("it builds without a key: %v", err)
	}
}

func TestMissingBaseURLAndMissingKeyKeepTheirMessages(t *testing.T) {
	clearProviderEnv(t)
	cfg := &config.Config{Providers: map[string]config.Provider{
		"nourl":  {APIKeyEnv: "SLEIPNIR_TEST_KEY"},
		"nokey":  {BaseURL: "https://gw.example/v1", APIKeyEnv: "SLEIPNIR_TEST_KEY"},
		"nobase": {},
	}}
	if err := buildErr(cfg, "nourl"); err == nil || !strings.Contains(err.Error(), `provider "nourl" has no base_url`) {
		t.Fatalf("%v", err)
	}
	if err := buildErr(cfg, "nokey"); err == nil || !strings.Contains(err.Error(), "needs SLEIPNIR_TEST_KEY to be set") {
		t.Fatalf("%v", err)
	}
}

// ---- timeouts from the provider's options -------------------------------------------------------

func TestProviderOptionsSetTheTimeouts(t *testing.T) {
	clearProviderEnv(t)
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // net/http notices a client that went away only after the body is read
		<-r.Context().Done()
	}))
	defer hang.Close()
	for _, dialect := range []string{config.DialectOpenAIChat, config.DialectAnthropic} {
		t.Run(dialect, func(t *testing.T) {
			cfg := &config.Config{Providers: map[string]config.Provider{"p": {
				Dialect: dialect, BaseURL: hang.URL,
				Options: map[string]any{"first_byte_timeout_sec": float64(1), "stream_idle_timeout_sec": float64(600)},
			}}}
			p, m, err := session.BuildProvider(cfg, session.ModelRef{Provider: "p", Model: "m"}, session.ProviderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			prompt := &core.Prompt{Model: m.ID, Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("hi")}}}, Params: core.Params{MaxTokens: 16}}
			start := time.Now()
			_, err = p.Do(context.Background(), &provider.Request{Prompt: prompt}, nil)
			pe, ok := provider.AsError(err)
			if !ok || pe.Kind != provider.ErrTimeout || !strings.Contains(pe.Message, "no response from the server within 1s") {
				t.Fatalf("%v", err)
			}
			if d := time.Since(start); d < 900*time.Millisecond || d > 10*time.Second {
				t.Fatalf("the first-byte option was not applied: %v", d)
			}
		})
	}
}

// A stream that keeps sending keep-alives is lively, not finished: stream_timeout_sec is
// the bound on the whole response (and the knob for a slow model that writes very long answers).
func TestProviderOptionsSetTheStreamDeadline(t *testing.T) {
	clearProviderEnv(t)
	drip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Millisecond):
			}
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}))
	defer drip.Close()
	for _, dialect := range []string{config.DialectOpenAIChat, config.DialectAnthropic} {
		t.Run(dialect, func(t *testing.T) {
			cfg := &config.Config{Providers: map[string]config.Provider{"p": {
				Dialect: dialect, BaseURL: drip.URL,
				Options: map[string]any{"first_byte_timeout_sec": float64(600), "stream_idle_timeout_sec": float64(600), "stream_timeout_sec": float64(1)},
			}}}
			p, m, err := session.BuildProvider(cfg, session.ModelRef{Provider: "p", Model: "m"}, session.ProviderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			prompt := &core.Prompt{Model: m.ID, Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("hi")}}}, Params: core.Params{MaxTokens: 16}}
			start := time.Now()
			_, err = p.Do(context.Background(), &provider.Request{Prompt: prompt}, nil)
			pe, ok := provider.AsError(err)
			if !ok || pe.Kind != provider.ErrTimeout || !strings.Contains(pe.Message, "still running after 1s") {
				t.Fatalf("%v", err)
			}
			if d := time.Since(start); d < 900*time.Millisecond || d > 10*time.Second {
				t.Fatalf("the stream_timeout_sec option was not applied: %v", d)
			}
		})
	}
}

// ---- the catalogue --------------------------------------------------------------------------------

const goodCatalogue = `{"data":[{"id":"vendor/cheap","context_length":64000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`
const hostileCatalogue = `{"data":[{"id":"vendor/cheap","context_length":64000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"NaN","completion":"-0.000002"}}]}`

func catalogueServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestEnrichModelUsesAValidCatalogueAndNeverAHostileOne(t *testing.T) {
	fallback := cost.Fallback("vendor/cheap")
	srv, _ := catalogueServer(t, goodCatalogue)
	got := session.EnrichModel(context.Background(), t.TempDir(), srv.URL, fallback)
	if got.Price.InputPerM != 1 || got.Price.OutputPerM != 2 || got.ContextTokens != 64000 {
		t.Fatalf("a valid catalogue must enrich the model: %+v", got.Price)
	}
	bad, _ := catalogueServer(t, hostileCatalogue)
	got = session.EnrichModel(context.Background(), t.TempDir(), bad.URL, fallback)
	if got.Price != fallback.Price || got.ContextTokens != fallback.ContextTokens {
		t.Fatalf("a hostile catalogue must leave the conservative estimate in place: %+v", got.Price)
	}
	if usd := got.Price.USD(core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}); !(usd >= 5) {
		t.Fatalf("the estimate must still trip a budget: %v", usd)
	}
}

// The copy on disk is not trusted more than the network.
func TestAnEditedCatalogueCacheCannotSmuggleInAPrice(t *testing.T) {
	dir := t.TempDir()
	srv, hits := catalogueServer(t, goodCatalogue)
	fallback := cost.Fallback("vendor/cheap")
	if got := session.EnrichModel(context.Background(), dir, srv.URL, fallback); got.Price.InputPerM != 1 {
		t.Fatalf("setup: %+v", got.Price)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "catalog-*.json"))
	if len(files) != 1 {
		t.Fatalf("cache files: %v", files)
	}
	var entries []map[string]any
	b, _ := os.ReadFile(files[0])
	if err := json.Unmarshal(b, &entries); err != nil || len(entries) != 1 {
		t.Fatalf("%v %s", err, b)
	}
	model := entries[0]["Model"].(map[string]any)
	price := model["Price"].(map[string]any)
	price["input_per_m"], price["output_per_m"], price["cache_read_per_m"] = -4.0, -20.0, -4.0
	edited, _ := json.Marshal(entries)
	if err := os.WriteFile(files[0], edited, 0o600); err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	got := session.EnrichModel(context.Background(), dir, srv.URL, fallback)
	if got.Price.InputPerM < 0 || got.Price.OutputPerM < 0 {
		t.Fatalf("a negative price came out of the cache: %+v", got.Price)
	}
	if hits.Load() != before+1 || got.Price.InputPerM != 1 {
		t.Fatalf("the tampered cache must be ignored and the catalogue fetched again (hits %d -> %d): %+v", before, hits.Load(), got.Price)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubNetwork makes every request the default transport would carry answer with body,
// and counts them, so a test can tell "was not asked" from "asked and failed".
func stubNetwork(t *testing.T, body string) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return &n
}

// The catalogue sets the prices a budget is computed from; over plain http to another
// machine it could be rewritten on the way, so it is not even requested.
func TestEnrichModelDoesNotReadACatalogueOverPlainHTTPFromAnotherMachine(t *testing.T) {
	fallback := cost.Fallback("vendor/cheap")
	requests := stubNetwork(t, goodCatalogue)

	// The stub is reached over https, and its prices are used (so the checks below are not vacuous).
	if got := session.EnrichModel(context.Background(), t.TempDir(), "https://catalogue.example/v1", fallback); got.Price.InputPerM != 1 || requests.Load() != 1 {
		t.Fatalf("control: %+v after %d requests", got.Price, requests.Load())
	}
	requests.Store(0)

	for _, base := range []string{"http://catalogue.attacker.example/v1", "http://192.0.2.7:8080/v1", "http://localhost.attacker.example/v1"} {
		got := session.EnrichModel(context.Background(), t.TempDir(), base, fallback)
		if requests.Load() != 0 || got.Price != fallback.Price {
			t.Fatalf("%s: the catalogue was requested (%d requests) or used: %+v", base, requests.Load(), got.Price)
		}
	}
	// This machine is fine over plain http.
	if got := session.EnrichModel(context.Background(), t.TempDir(), "http://127.0.0.1:9/v1", fallback); requests.Load() != 1 || got.Price.InputPerM != 1 {
		t.Fatalf("loopback: %+v after %d requests", got.Price, requests.Load())
	}
}
