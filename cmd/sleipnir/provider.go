package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
)

// providerSpec describes how to reach one family of endpoints.
type providerSpec struct {
	name    string
	baseURL string
	keyEnv  string
	opts    openaichat.Options
	headers map[string]string
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
}

// resolve picks a provider spec from flags and the environment.
func (pf providerFlags) resolve() (providerSpec, string, error) {
	name := strings.ToLower(pf.provider)
	if name == "" {
		switch {
		case pf.baseURL != "":
			name = "custom"
		case os.Getenv("HEIMDALL_API_KEY") != "":
			name = "heimdall"
		case os.Getenv("OPENROUTER_API_KEY") != "":
			name = "openrouter"
		case os.Getenv("OPENAI_API_KEY") != "":
			name = "openai"
		default:
			return providerSpec{}, "", fmt.Errorf("no provider configured: set HEIMDALL_API_KEY (or OPENROUTER_API_KEY / OPENAI_API_KEY), or pass --provider and --base-url")
		}
	}
	spec, ok := builtinProviders[name]
	if name == "custom" || !ok {
		spec = providerSpec{name: "custom", opts: openaichat.Options{SessionHeader: true}}
	}
	if pf.baseURL != "" {
		spec.baseURL = pf.baseURL
	} else if env := os.Getenv(strings.ToUpper(spec.name) + "_BASE_URL"); env != "" {
		spec.baseURL = env
	}
	if pf.keyEnv != "" {
		spec.keyEnv = pf.keyEnv
	}
	if spec.baseURL == "" {
		return spec, "", fmt.Errorf("provider %q needs --base-url", name)
	}
	key := ""
	if spec.keyEnv != "" {
		key = os.Getenv(spec.keyEnv)
	}
	return spec, key, nil
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
		Headers: spec.headers, Options: spec.opts,
	}
	if rec != nil {
		cfg.OnHeaders = rec.set
	}
	return openaichat.New(cfg)
}

var _ provider.Provider = (*openaichat.Client)(nil)
