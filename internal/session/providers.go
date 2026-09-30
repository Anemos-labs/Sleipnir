package session

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
)

// ModelRef names a model on a provider.
type ModelRef struct{ Provider, Model string }

func (m ModelRef) String() string { return m.Provider + "/" + m.Model }

// builtinProviders are the endpoints Sleipnir knows without configuration. Each
// is used only when its key variable is set.
var builtinProviders = map[string]config.Provider{
	"heimdall": {
		Dialect: config.DialectOpenAIChat, BaseURL: "https://api-staging.impossiblecarrot.cc/api/v1", APIKeyEnv: "HEIMDALL_API_KEY",
		// Heimdall pins a conversation to the engine that served it when it is told
		// the conversation id; prompt_cache_key carries the same id.
		Options: map[string]any{"session_header": true, "cache_key_body": true},
		Headers: map[string]string{"X-Title": "Sleipnir"},
	},
	"openrouter": {
		Dialect: config.DialectOpenAIChat, BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY",
		Options: map[string]any{"session_header": true},
		Headers: map[string]string{"X-Title": "Sleipnir"},
	},
	"openai": {
		Dialect: config.DialectOpenAIChat, BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY",
		Options: map[string]any{"cache_key_body": true},
	},
}

// providerNames lists configured and built-in providers, sorted.
func providerNames(cfg *config.Config) []string {
	set := map[string]bool{}
	for n := range builtinProviders {
		set[n] = true
	}
	if cfg != nil {
		for n := range cfg.Providers {
			set[n] = true
		}
	}
	var out []string
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func lookupProvider(cfg *config.Config, name string) (config.Provider, bool) {
	if cfg != nil {
		if p, ok := cfg.Providers[name]; ok {
			return p, true
		}
	}
	p, ok := builtinProviders[name]
	return p, ok
}

// ResolveModel turns a user-supplied model string into a provider and model id.
// The forms are "provider/model", a bare model id (routed to the default
// provider, which is how marketplace ids such as "deepseek/deepseek-v4.1-flash"
// work), or empty (the configured default).
func ResolveModel(cfg *config.Config, ref string) (ModelRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" && cfg != nil {
		ref = cfg.Models.Default
	}
	if ref == "" {
		return ModelRef{}, fmt.Errorf("no model configured: pass --model or set models.default in .sleipnir/config.json")
	}
	if i := strings.IndexByte(ref, '/'); i > 0 {
		if _, ok := lookupProvider(cfg, ref[:i]); ok {
			return ModelRef{Provider: ref[:i], Model: ref[i+1:]}, nil
		}
	}
	p, err := defaultProvider(cfg)
	if err != nil {
		return ModelRef{}, err
	}
	return ModelRef{Provider: p, Model: ref}, nil
}

// defaultProvider picks the provider a bare model id is sent to: the only
// configured one, else the first built-in whose key is set.
func defaultProvider(cfg *config.Config) (string, error) {
	if cfg != nil && len(cfg.Providers) == 1 {
		for n := range cfg.Providers {
			return n, nil
		}
	}
	for _, n := range []string{"heimdall", "openrouter", "openai"} {
		if p, ok := lookupProvider(cfg, n); ok && p.APIKey() != "" {
			return n, nil
		}
	}
	return "", fmt.Errorf("no provider configured: set HEIMDALL_API_KEY (or OPENROUTER_API_KEY / OPENAI_API_KEY) or define one under \"providers\" in .sleipnir/config.json")
}

// ProviderOptions are per-session knobs for building a client.
type ProviderOptions struct {
	// CaptureTokens marks the endpoint as able to return token ids and logprobs
	// (a self-hosted vLLM/SGLang-style policy server). RL rollouts set it.
	CaptureTokens bool
	HTTPClient    *http.Client
	OnHeaders     func(http.Header)
}

func optBool(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

func optString(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// BuildProvider constructs the client for ref together with the harness's model
// description (prices and cache behaviour, from the built-in table or a
// conservative fallback).
func BuildProvider(cfg *config.Config, ref ModelRef, o ProviderOptions) (provider.Provider, cost.Model, error) {
	p, ok := lookupProvider(cfg, ref.Provider)
	if !ok {
		return nil, cost.Model{}, fmt.Errorf("unknown provider %q (known: %s)", ref.Provider, strings.Join(providerNames(cfg), ", "))
	}
	base := p.BaseURL
	if env := os.Getenv(strings.ToUpper(strings.ReplaceAll(ref.Provider, "-", "_")) + "_BASE_URL"); env != "" {
		base = env
	}
	if base == "" {
		return nil, cost.Model{}, fmt.Errorf("provider %q has no base_url", ref.Provider)
	}
	key := p.APIKey()
	if p.APIKeyEnv != "" && key == "" {
		return nil, cost.Model{}, fmt.Errorf("provider %q needs %s to be set", ref.Provider, p.APIKeyEnv)
	}

	switch p.EffectiveDialect() {
	case config.DialectOpenAIChat:
	case config.DialectAnthropic, config.DialectOpenAIResponses:
		return nil, cost.Model{}, fmt.Errorf("provider %q uses the %s dialect, whose native adapter is not built yet; use the endpoint's chat-completions route (dialect %q)",
			ref.Provider, p.EffectiveDialect(), config.DialectOpenAIChat)
	default:
		return nil, cost.Model{}, fmt.Errorf("provider %q: unknown dialect %q", ref.Provider, p.Dialect)
	}

	oo := openaichat.Options{
		SessionHeader:        optBool(p.Options, "session_header"),
		CacheKeyBody:         optBool(p.Options, "cache_key_body"),
		SystemRole:           optString(p.Options, "system_role"),
		MaxTokensField:       optString(p.Options, "max_tokens_field"),
		ReasoningEffortField: optString(p.Options, "reasoning_effort_field"),
		CacheControlParts:    optBool(p.Options, "cache_control_parts"),
	}
	if extra, ok := p.Options["extra_body"].(map[string]any); ok {
		oo.ExtraBody = extra
	}
	client := openaichat.New(openaichat.Config{
		Name: ref.Provider, BaseURL: base, APIKey: key, Headers: p.Headers, Options: oo,
		HTTPClient: o.HTTPClient, OnHeaders: o.OnHeaders,
	})
	prof := client.Profile()
	if o.CaptureTokens || optBool(p.Options, "capture_tokens") {
		prof.CaptureTokens = true
		client.SetProfile(prof)
	}

	m, ok := cost.Defaults().Lookup(ref.Model)
	if !ok {
		m = cost.Fallback(ref.Model)
		// An unknown model on a chat endpoint gets the endpoint's own cache
		// behaviour; prices stay an estimate until the catalogue supplies them.
		m.Cache = prof.Cache
	}
	if m.ContextTokens == 0 {
		m.ContextTokens = 200_000
	}
	return client, m, nil
}
