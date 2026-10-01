package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/anthropic"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
)

// ErrNoModel is what ResolveModel returns when nothing names a model: no flag, no configuration, no SLEIPNIR_MODEL. The chat answers it
// by asking the provider which models it has (the harness knows no model names: they change too often).
var ErrNoModel = errors.New("no model configured")

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
	"anthropic": {
		Dialect: config.DialectAnthropic, BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY",
	},
}

// hostedOpenWeights are hosts of open-weight models that speak chat completions: a base URL and a key
// variable each, nothing else to know. They are built in so `together/<model>` works with only the key set.
var hostedOpenWeights = map[string][2]string{
	"together":  {"https://api.together.xyz/v1", "TOGETHER_API_KEY"},
	"fireworks": {"https://api.fireworks.ai/inference/v1", "FIREWORKS_API_KEY"},
	"groq":      {"https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	"cerebras":  {"https://api.cerebras.ai/v1", "CEREBRAS_API_KEY"},
	"deepinfra": {"https://api.deepinfra.com/v1/openai", "DEEPINFRA_API_KEY"},
}

// localServers run on this machine and need no key: `ollama/qwen3:8b` is enough. They are never picked
// as the default provider for a bare model id (nothing says one is running); name them.
var localServers = map[string]string{
	"ollama":   "http://localhost:11434/v1",
	"lmstudio": "http://localhost:1234/v1",
	"llamacpp": "http://localhost:8080/v1",
	"vllm":     "http://localhost:8000/v1",
}

func init() {
	for n, v := range hostedOpenWeights {
		builtinProviders[n] = config.Provider{Dialect: config.DialectOpenAIChat, BaseURL: v[0], APIKeyEnv: v[1]}
	}
	for n, u := range localServers {
		builtinProviders[n] = config.Provider{Dialect: config.DialectOpenAIChat, BaseURL: u}
	}
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

// ProviderNames lists the configured and built-in providers, sorted.
func ProviderNames(cfg *config.Config) []string { return providerNames(cfg) }

func lookupProvider(cfg *config.Config, name string) (config.Provider, bool) {
	b, isBuiltin := builtinProviders[name]
	if cfg != nil {
		if p, ok := cfg.Providers[name]; ok {
			if isBuiltin && p.BaseURL == "" {
				// An entry that names no endpoint of its own only tunes the built-in
				// provider (options, a header, an allowance such as allow_hosts): it
				// extends it instead of replacing it with a provider that has nowhere
				// to send requests. An entry with its own base_url defines the
				// provider outright and inherits nothing, in particular not the
				// built-in key variable: a key issued for one host is not sent to
				// another because a configuration forgot to say otherwise.
				p = extendBuiltin(b, p)
			}
			return p, true
		}
	}
	return b, isBuiltin
}

// extendBuiltin lays a configured entry over the built-in provider it names.
func extendBuiltin(b, p config.Provider) config.Provider {
	p.BaseURL = b.BaseURL
	if p.Dialect == "" {
		p.Dialect = b.Dialect
	}
	if p.APIKeyEnv == "" {
		p.APIKeyEnv = b.APIKeyEnv
	}
	p.Headers = mergeMaps(b.Headers, p.Headers)
	p.Options = mergeMaps(b.Options, p.Options)
	return p
}

// mergeMaps returns base overlaid with over, as a new map (nil when both are empty).
func mergeMaps[V any](base, over map[string]V) map[string]V {
	if len(base)+len(over) == 0 {
		return nil
	}
	out := make(map[string]V, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
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
		return ModelRef{}, fmt.Errorf("%w. Recommended start: export HEIMDALL_API_KEY, then `sleipnir init --user` picks a model (`sleipnir models` lists what is available).\nOr pass --model provider/model, set SLEIPNIR_MODEL, or write a model into your config with `sleipnir init --user --model provider/model`", ErrNoModel)
	}
	if i := strings.IndexByte(ref, '/'); i > 0 {
		if p, ok := lookupProvider(cfg, ref[:i]); ok {
			// "anthropic/claude-x" is also a marketplace id: with no key for that provider and a
			// default provider that has one, the whole string is the marketplace's model id.
			if d, derr := defaultProvider(cfg); derr == nil && d != ref[:i] && (ref[:i] == "anthropic" || ref[:i] == "openai") && p.APIKey() == "" {
				return ModelRef{Provider: d, Model: ref}, nil
			}
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
// configuration or the built-ins, with the <NAME>_BASE_URL override applied where
// BuildProvider would honour it. An override that would carry the provider's key to
// a host it is not known to use (see provider.CheckEndpoint) is ignored here and
// refused, with an explanation, by BuildProvider: callers such as the RL policy
// setup turn this URL into a client that sends the key, so it must not be the
// attacker's.
func ProviderInfo(cfg *config.Config, name string) (baseURL, keyEnv string, ok bool) {
	p, ok := lookupProvider(cfg, name)
	if !ok {
		return "", "", false
	}
	base := p.BaseURL
	if env := envBaseURL(name); env != "" {
		if _, err := endpointOf(name, p, p.APIKey()); err == nil {
			base = env
		}
	}
	return base, p.APIKeyEnv, true
}

// envBaseURLVar is the environment variable that overrides a provider's base URL.
func envBaseURLVar(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_BASE_URL"
}

func envBaseURL(name string) string { return strings.TrimSpace(os.Getenv(envBaseURLVar(name))) }

// endpointOf resolves where provider name's requests go and checks that its API key
// (key, "" when none is sent) may be sent there. The base URL is the provider's
// configured one unless <NAME>_BASE_URL overrides it; an override from the
// environment, and a URL that a project file supplied, may not carry the key to a
// host the provider is not known to use unless the user's own configuration lists
// it (allow_hosts), and a key never travels over plain http to another machine
// unless the user allowed that too (allow_insecure_http). The returned error says
// what was refused and how to allow it deliberately.
func endpointOf(name string, p config.Provider, key string) (string, error) {
	base, src := p.BaseURL, provider.SourceConfigured
	if p.BaseURLFromProject {
		src = provider.SourceProject
	}
	if env := envBaseURL(name); env != "" {
		base, src = env, provider.SourceEnv
	}
	if base == "" {
		return "", fmt.Errorf("provider %q has no base_url", name)
	}
	ep := provider.Endpoint{
		Name: name, BaseURL: base, Source: src, EnvVar: envBaseURLVar(name),
		Anchors: trustedBaseURLs(name, p), AllowHosts: p.AllowHosts, AllowInsecureHTTP: p.AllowInsecureHTTP,
		// A header such as Authorization or X-Api-Key goes wherever the key goes.
		CredentialHeaders: provider.CredentialHeaders(p.Headers),
	}
	if key != "" {
		ep.KeyEnv = p.APIKeyEnv
	}
	if err := provider.CheckEndpoint(ep); err != nil {
		return "", err
	}
	return base, nil
}

// trustedBaseURLs are the base URLs a provider may always use: the compiled-in
// default of a built-in provider of that name, and the URL the user's own
// configuration (or the code that built the provider) gives it. A URL that came
// from a project file is not among them.
func trustedBaseURLs(name string, p config.Provider) []string {
	var out []string
	if b, ok := builtinProviders[name]; ok && b.BaseURL != "" {
		out = append(out, b.BaseURL)
	}
	if p.BaseURL != "" && !p.BaseURLFromProject {
		out = append(out, p.BaseURL)
	}
	return out
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
	for _, n := range []string{"heimdall", "openrouter", "openai", "anthropic", "together", "fireworks", "groq", "cerebras", "deepinfra"} {
		if p, ok := lookupProvider(cfg, n); ok && p.APIKey() != "" {
			return n, nil
		}
	}
	return "", fmt.Errorf("no provider configured: set HEIMDALL_API_KEY (recommended), or the key of another provider (OPENROUTER_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, ...), name a local server (ollama/<model>), or define a provider under \"providers\" in your config")
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

// optSeconds reads a whole number of seconds from provider options as a duration:
// 0 (unset) unless it is positive, and at most a day.
func optSeconds(m map[string]any, k string) time.Duration {
	f, _ := m[k].(float64)
	if i, ok := m[k].(int); ok {
		f = float64(i)
	}
	if !(f > 0) {
		return 0
	}
	return time.Duration(min(f, 86400)) * time.Second
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
		HTTPClient: o.HTTPClient, OnHeaders: o.OnHeaders, AllowInsecureHTTP: p.AllowInsecureHTTP,
		FirstByteTimeout:  optSeconds(p.Options, "first_byte_timeout_sec"),
		StreamIdleTimeout: optSeconds(p.Options, "stream_idle_timeout_sec"),
		RequestTimeout:    optSeconds(p.Options, "request_timeout_sec"),
		Limits:            provider.StreamLimits{MaxDuration: optSeconds(p.Options, "stream_timeout_sec")},
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
//
// Both dialects take first_byte_timeout_sec (how long a request may get no response
// at all; default 120, or stream_idle_timeout_sec when only that is set),
// stream_idle_timeout_sec (silence once a stream has started; default 60),
// stream_timeout_sec (the whole of one streamed response, however lively; default
// 1800: raise it for a slow self-hosted model that writes very long answers) and
// request_timeout_sec (a non-streaming call; default 600).
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
		Options: ao, HTTPClient: o.HTTPClient, OnHeaders: o.OnHeaders, AllowInsecureHTTP: p.AllowInsecureHTTP,
		FirstByteTimeout:  optSeconds(p.Options, "first_byte_timeout_sec"),
		StreamIdleTimeout: optSeconds(p.Options, "stream_idle_timeout_sec"),
		RequestTimeout:    optSeconds(p.Options, "request_timeout_sec"),
		Limits:            provider.StreamLimits{MaxDuration: optSeconds(p.Options, "stream_timeout_sec")},
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
	key := p.APIKey()
	// Where the requests go, and whether the key may go there: an override from the
	// environment or a project file cannot point a key at a host the provider is not
	// known to use, and no key crosses the network unencrypted, unless the user's own
	// configuration says so. A refusal explains how to allow it; it never downgrades.
	base, err := endpointOf(ref.Provider, p, key)
	if err != nil {
		return nil, cost.Model{}, err
	}
	if p.APIKeyEnv != "" && key == "" {
		msg := fmt.Sprintf("provider %q needs %s to be set", ref.Provider, p.APIKeyEnv)
		// A marketplace model id can start with a provider's name (openai/gpt-oss-20b on a marketplace is
		// not the OpenAI API): say how to reach it through the default provider.
		if d, err := defaultProvider(cfg); err == nil && d != ref.Provider {
			full := ref.Provider + "/" + ref.Model
			msg += fmt.Sprintf(" (if %q is a model id of your default provider %s, write it as %s/%s)", full, d, d, full)
		}
		return nil, cost.Model{}, errors.New(msg)
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
	out, _ := enrichModel(ctx, cacheDir, baseURL, m)
	return out
}

// enrichModel is EnrichModel that also says whether the catalogue had the model.
func enrichModel(ctx context.Context, cacheDir, baseURL string, m cost.Model) (cost.Model, bool) {
	if m.Provider != "unknown" || baseURL == "" {
		return m, false
	}
	// The catalogue decides the prices a budget is computed from: over plain http it
	// could be rewritten on the way, so it is only read over https or from this
	// machine.
	if provider.CheckKeyTransport(baseURL) != nil {
		return m, false
	}
	entries := loadCatalog(ctx, cacheDir, baseURL)
	for _, e := range entries {
		if e.Model.ID == m.ID || cost.Normalize(e.Model.ID) == cost.Normalize(m.ID) {
			out := e.Model // every entry has passed cost.Model.Validate (gateway.Parse, gateway.Vet)
			out.ID = m.ID
			return out, true
		}
	}
	return m, false
}

func loadCatalog(ctx context.Context, cacheDir, baseURL string) []gateway.Entry {
	var path string
	if cacheDir != "" {
		sum := sha256.Sum256([]byte(baseURL))
		path = filepath.Join(cacheDir, "catalog-"+hex.EncodeToString(sum[:6])+".json")
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < catalogTTL {
			if b, err := os.ReadFile(path); err == nil {
				var es []gateway.Entry
				// The copy on disk is not trusted more than the network: the same
				// checks apply, so an edited file cannot smuggle in a price that a
				// download could not.
				if json.Unmarshal(b, &es) == nil {
					if es = gateway.Vet(es); len(es) > 0 {
						return es
					}
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
