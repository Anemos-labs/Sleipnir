package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
)

// providerSpec describes how to reach one family of endpoints.
type providerSpec struct {
	name    string
	baseURL string
	keyEnv  string
	opts    openaichat.Options
	headers map[string]string
	// allowInsecure is the user's deliberate permission (providers.<name>.
	// allow_insecure_http in their own configuration) to send the key over plain http
	// to a host that is not this machine.
	allowInsecure bool
}

var builtinProviders = map[string]providerSpec{
	"heimdall": {
		name: "heimdall", baseURL: "https://api-staging.impossiblecarrot.cc/api/v1", keyEnv: "HEIMDALL_API_KEY",
		// Heimdall pins a conversation to the engine that served it when it is
		// told the conversation id; prompt_cache_key carries the same id.
		opts:    openaichat.Options{SessionHeader: true, CacheKeyBody: true},
		headers: map[string]string{"X-Title": "Sleipnir"},
	},
	"openrouter": {
		name: "openrouter", baseURL: "https://openrouter.ai/api/v1", keyEnv: "OPENROUTER_API_KEY",
		opts:    openaichat.Options{SessionHeader: true},
		headers: map[string]string{"X-Title": "Sleipnir"},
	},
	"openai": {
		name: "openai", baseURL: "https://api.openai.com/v1", keyEnv: "OPENAI_API_KEY",
		opts: openaichat.Options{CacheKeyBody: true},
	},
}

// providerFlags are the flags every provider-using subcommand shares.
type providerFlags struct {
	provider string
	baseURL  string
	keyEnv   string
	// home is where the user's own configuration is looked for (empty: the current
	// user's home). Tests set it so they never read the real one.
	home string
}

// resolve picks a provider spec from flags and the environment.
//
// The base URL comes from --base-url (the user typed it), else from the
// <PROVIDER>_BASE_URL environment variable, else from the built-in default. The
// environment is not the user's word: a .envrc or a CI job of a repository being
// worked on can set it. So an API key is never sent to a host that the environment
// chose unless the provider is known to use it or the user's own configuration lists
// it (providers.<name>.allow_hosts), and a key is never sent over plain http to
// another machine unless providers.<name>.allow_insecure_http says so. A refusal is
// an error that says how to allow it deliberately.
func (pf providerFlags) resolve() (providerSpec, string, error) {
	name := strings.ToLower(pf.provider)
	if name == "" {
		switch {
		case pf.baseURL != "":
			name = "custom"
		case harden.Secret("HEIMDALL_API_KEY") != "":
			name = "heimdall"
		case harden.Secret("OPENROUTER_API_KEY") != "":
			name = "openrouter"
		case harden.Secret("OPENAI_API_KEY") != "":
			name = "openai"
		default:
			return providerSpec{}, "", fmt.Errorf("no provider has a key: run `sleipnir login` (Heimdall is recommended), or set HEIMDALL_API_KEY (or OPENROUTER_API_KEY / OPENAI_API_KEY), or pass --provider and --base-url")
		}
	}
	spec, ok := builtinProviders[name]
	if name == "custom" || !ok {
		spec = providerSpec{name: "custom", opts: openaichat.Options{SessionHeader: true}}
	}
	builtinBase := spec.baseURL // "" for a custom endpoint: there is nothing it is known to use
	src, envVar := provider.SourceConfigured, ""
	if pf.baseURL != "" {
		spec.baseURL, src = pf.baseURL, provider.SourceFlag
	} else if v := strings.ToUpper(spec.name) + "_BASE_URL"; strings.TrimSpace(os.Getenv(v)) != "" {
		spec.baseURL, src, envVar = strings.TrimSpace(os.Getenv(v)), provider.SourceEnv, v
	}
	if pf.keyEnv != "" {
		spec.keyEnv = pf.keyEnv
	}
	if spec.baseURL == "" {
		return spec, "", fmt.Errorf("provider %q needs --base-url", name)
	}
	key := ""
	if spec.keyEnv != "" {
		key = harden.Secret(spec.keyEnv)
	}

	// Judge the endpoint before the key is used for anything.
	ep := provider.Endpoint{Name: spec.name, BaseURL: spec.baseURL, Source: src, EnvVar: envVar}
	if builtinBase != "" {
		ep.Anchors = []string{builtinBase}
	}
	if key != "" {
		ep.KeyEnv = spec.keyEnv
		ep.AllowHosts, ep.AllowInsecureHTTP = pf.userAllowances(spec.name)
	}
	if err := provider.CheckEndpoint(ep); err != nil {
		return spec, "", err
	}
	spec.allowInsecure = ep.AllowInsecureHTTP
	return spec, key, nil
}

// userAllowances reads what the user's own configuration file allows for a
// provider: hosts that may receive its key when the environment names them, and
// whether its key may travel over plain http. Only the user-level file counts (a
// project's are never honoured, see config.UserOnlyPaths), so the project is loaded
// as untrusted; a configuration that cannot be read allows nothing.
func (pf providerFlags) userAllowances(name string) (hosts []string, insecure bool) {
	cfg, _, err := config.Load(config.LoadOpts{Home: pf.home, UntrustedProject: true, Environ: func() []string { return nil }})
	if err != nil || cfg == nil {
		return nil, false
	}
	p := cfg.Providers[name]
	return p.AllowHosts, p.AllowInsecureHTTP
}

// headerRecorder remembers the latest response headers (rate-limit info).
type headerRecorder struct {
	mu sync.Mutex
	h  http.Header
}

func (r *headerRecorder) set(h http.Header) {
	r.mu.Lock()
	r.h = h.Clone()
	r.mu.Unlock()
}

func (r *headerRecorder) get() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.h
}

// newClient builds the chat client for a resolved provider.
func newClient(spec providerSpec, key string, rec *headerRecorder) *openaichat.Client {
	cfg := openaichat.Config{
		Name: spec.name, BaseURL: spec.baseURL, APIKey: key,
		Headers: spec.headers, Options: spec.opts, AllowInsecureHTTP: spec.allowInsecure,
	}
	if rec != nil {
		cfg.OnHeaders = rec.set
	}
	return openaichat.New(cfg)
}

var _ provider.Provider = (*openaichat.Client)(nil)
