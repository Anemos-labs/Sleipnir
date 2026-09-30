package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/anthropic"
	"github.com/reee344/sleipnir/internal/provider/gateway"
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

// LookupProvider returns a configured or built-in provider by name.
func LookupProvider(cfg *config.Config, name string) (config.Provider, bool) {
	return lookupProvider(cfg, name)
}

// ProviderInfo returns a provider's base URL and API-key variable, from the
// configuration or the built-ins (with the <NAME>_BASE_URL override applied).
func ProviderInfo(cfg *config.Config, name string) (baseURL, keyEnv string, ok bool) {
	p, ok := lookupProvider(cfg, name)
	if !ok {
		return "", "", false
	}
	base := p.BaseURL
	if env := os.Getenv(strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_BASE_URL"); env != "" {
		base = env
	}
	return base, p.APIKeyEnv, true
}

// DefaultProvider is the provider a bare model id is sent to.
func DefaultProvider(cfg *config.Config) (string, error) { return defaultProvider(cfg) }

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

func optInt(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func optStrings(m map[string]any, k string) []string {
	var out []string
	switch v := m[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = v
	}
	return out
}

// buildChat constructs an OpenAI-style chat-completions client.
func buildChat(ref ModelRef, p config.Provider, base, key string, o ProviderOptions) provider.Provider {
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
	if o.CaptureTokens || optBool(p.Options, "capture_tokens") {
		prof := client.Profile()
		prof.CaptureTokens = true
		client.SetProfile(prof)
	}
	return client
}

// buildAnthropic constructs a Messages-API client: Anthropic itself, or a
// gateway that speaks its wire format (a marketplace's /messages route). What a
// gateway does not forward is declared in the provider's options:
//
//	auth_style             "x-api-key" (default) or "bearer"
//	cache_control          false when the gateway drops cache_control markers
//	no_turn_scoped_system  the gateway has no mid-conversation system messages
//	no_thinking_replay     the gateway cannot take thinking blocks back
//	no_zero_max_tokens     the gateway rejects max_tokens 0 (warm-ups use 1)
//	session_header_name    header that carries the routing key, if any
//	version, betas, default_max_tokens, thinking_display, thinking_budget,
//	max_breakpoints, extra_body
func buildAnthropic(ref ModelRef, p config.Provider, base, key string, o ProviderOptions) provider.Provider {
	ao := anthropic.Options{
		NoTurnScopedSystem: optBool(p.Options, "no_turn_scoped_system"),
		NoThinkingReplay:   optBool(p.Options, "no_thinking_replay"),
		NoZeroMaxTokens:    optBool(p.Options, "no_zero_max_tokens"),
		DefaultMaxTokens:   optInt(p.Options, "default_max_tokens"),
		ThinkingDisplay:    optString(p.Options, "thinking_display"),
		ThinkingBudget:     optInt(p.Options, "thinking_budget"),
		MaxBreakpoints:     optInt(p.Options, "max_breakpoints"),
	}
	if extra, ok := p.Options["extra_body"].(map[string]any); ok {
		ao.ExtraBody = extra
	}
	client := anthropic.New(anthropic.Config{
		Name: ref.Provider, BaseURL: base, APIKey: key, Headers: p.Headers, Model: ref.Model,
		AuthStyle: optString(p.Options, "auth_style"), Version: optString(p.Options, "version"),
		Betas: optStrings(p.Options, "betas"), SessionHeader: optString(p.Options, "session_header_name"),
		Options: ao, HTTPClient: o.HTTPClient, OnHeaders: o.OnHeaders,
	})
	prof := client.Profile()
	if v, ok := p.Options["cache_control"].(bool); ok && !v {
		// A gateway that drops cache_control has no explicit breakpoints to plan.
		prof.Cache.MaxBreakpoints = 0
	}
	if ao.NoTurnScopedSystem {
		prof.TurnScopedSystem = false
	}
	if ao.NoThinkingReplay {
		prof.ReplayThinking = false
	}
	client.SetProfile(prof)
	return client
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

	var client provider.Provider
	switch p.EffectiveDialect() {
	case config.DialectOpenAIChat:
		client = buildChat(ref, p, base, key, o)
	case config.DialectAnthropic:
		if o.CaptureTokens || optBool(p.Options, "capture_tokens") {
			return nil, cost.Model{}, fmt.Errorf("provider %q speaks the %s dialect, which returns no token ids or logprobs; RL capture needs a self-hosted chat-completions server (vLLM, SGLang)", ref.Provider, config.DialectAnthropic)
		}
		client = buildAnthropic(ref, p, base, key, o)
	case config.DialectOpenAIResponses:
		return nil, cost.Model{}, fmt.Errorf("provider %q uses the %s dialect, whose native adapter is not built yet; use the endpoint's chat-completions route (dialect %q) or its Messages route (dialect %q)",
			ref.Provider, p.EffectiveDialect(), config.DialectOpenAIChat, config.DialectAnthropic)
	default:
		return nil, cost.Model{}, fmt.Errorf("provider %q: unknown dialect %q", ref.Provider, p.Dialect)
	}
	prof := client.Profile()

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

// catalogTTL is how long a downloaded model catalogue is trusted.
const catalogTTL = 6 * time.Hour

// EnrichModel replaces the fallback description of a model with the endpoint's
// own catalogue entry (exact prices, context window, cache behaviour) when the
// endpoint publishes one. Marketplaces serve hundreds of models the built-in
// table cannot know, and a wrong context window would either waste the window or
// overflow it before compaction starts. Failures are silent: the fallback stands
// and gateway-reported costs still make the bill exact.
func EnrichModel(ctx context.Context, cacheDir, baseURL string, m cost.Model) cost.Model {
	if m.Provider != "unknown" || baseURL == "" {
		return m
	}
	entries := loadCatalog(ctx, cacheDir, baseURL)
	for _, e := range entries {
		if e.Model.ID == m.ID || cost.Normalize(e.Model.ID) == cost.Normalize(m.ID) {
			out := e.Model
			out.ID = m.ID
			return out
		}
	}
	return m
}

func loadCatalog(ctx context.Context, cacheDir, baseURL string) []gateway.Entry {
	var path string
	if cacheDir != "" {
		sum := sha256.Sum256([]byte(baseURL))
		path = filepath.Join(cacheDir, "catalog-"+hex.EncodeToString(sum[:6])+".json")
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < catalogTTL {
			if b, err := os.ReadFile(path); err == nil {
				var es []gateway.Entry
				if json.Unmarshal(b, &es) == nil && len(es) > 0 {
					return es
				}
			}
		}
	}
	fctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	es, err := gateway.Fetch(fctx, nil, baseURL)
	if err != nil || len(es) == 0 {
		return nil
	}
	if path != "" {
		if b, err := json.Marshal(es); err == nil {
			_ = os.MkdirAll(cacheDir, 0o700)
			_ = os.WriteFile(path, b, 0o600)
		}
	}
	return es
}
