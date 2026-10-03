// Package config loads Sleipnir's layered configuration.
//
// Sources, lowest to highest precedence:
//
//  1. built-in defaults (Defaults)
//  2. user file            <home>/.sleipnir/config.json
//  3. project file         <root>/.sleipnir/config.json
//  4. project-local file   <root>/.sleipnir/config.local.json   (git-ignored)
//  5. environment          SLEIPNIR_* variables (see EnvVars)
//  6. explicit overrides   LoadOpts.Overrides (command-line flags)
//
// Files are JSONC: JSON with // and /* */ comments and trailing commas.
// Layers merge by presence, not by value: maps merge key by key (recursively
// for sections and provider entries), lists replace, and a scalar is overridden
// only if the higher layer actually contains the key, so an explicit false or 0
// wins over a true or 5 below it. A JSON null removes whatever lower layers set
// for that key. The values of "hooks" and "mcp" entries, and unknown top-level
// keys, are opaque to this package and replace as a whole.
//
// Errors are strict and located: syntax errors, wrong types and invalid values
// name the file, line, column and field path. Unknown keys are not errors: they
// are kept in Config.Extra and reported as warnings.
//
// # Environment
//
// Each SLEIPNIR_* variable that names a setting is one entry in the environment
// layer (see EnvVars for the machine-readable list):
//
//	SLEIPNIR_MODEL              models.default
//	SLEIPNIR_MODEL_<ROLE>       models.roles.<role>        (role lower-cased)
//	SLEIPNIR_PERMISSION_MODE    permissions.mode
//	SLEIPNIR_<SECTION>_<FIELD>  any scalar or list field of a section, named by
//	                            its JSON path in upper case, for example
//	                            SLEIPNIR_CACHE_SHARED_TTL, SLEIPNIR_SWARM_MAX_AGENTS,
//	                            SLEIPNIR_TOOLS_WEB_ALLOW_HOSTS
//
// Booleans accept 1/true/yes/on and 0/false/no/off, lists are comma-separated,
// and an empty value counts as unset. Other SLEIPNIR_* variables are ignored.
// Providers, hooks and MCP servers can only be configured in files.
//
// # Project files are untrusted input
//
// A project's .sleipnir/config.json arrives with the repository, which may be
// someone else's. Load lists every security-sensitive setting a project-level
// file makes (Report.ProjectRisks: provider URLs and headers, permission
// modes and allow rules, hooks, MCP servers, ...; see SensitivePaths) so the
// caller can ask the user before trusting them, and LoadOpts.UntrustedProject
// drops them instead of applying them.
//
// # Secrets
//
// Secrets do not belong in configuration. A provider names the environment
// variable holding its key (Provider.APIKeyEnv) and the key is only read when
// Provider.APIKey is called; Load warns about anything in a file that looks like
// a key.
package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/perm"
)

// Provider dialects.
const (
	DialectOpenAIChat      = "openai-chat"
	DialectAnthropic       = "anthropic"
	DialectOpenAIResponses = "openai-responses"
)

// AuthChatGPTPlan is Provider.Auth for a ChatGPT plan: "Sign in with ChatGPT" instead of an API key.
const AuthChatGPTPlan = "chatgpt-plan"

// Config is the whole configuration. The zero value is usable: every field's
// zero value means "use the built-in default" (Defaults spells the defaults out
// where the config itself owns the decision).
type Config struct {
	// Providers are the model endpoints, by name. A model reference is
	// "<provider>/<model>".
	Providers   map[string]Provider `json:"providers,omitempty"`
	Models      Models              `json:"models"`
	Permissions Permissions         `json:"permissions"`
	Cache       Cache               `json:"cache"`
	Swarm       Swarm               `json:"swarm"`
	Tools       Tools               `json:"tools"`

	// Hooks and MCP are placeholders the integrator will give real types; their
	// entries are kept as raw JSON.
	Hooks map[string]json.RawMessage `json:"hooks,omitempty"`
	MCP   map[string]json.RawMessage `json:"mcp,omitempty"`

	// Extra holds top-level keys this version does not know, verbatim.
	Extra map[string]json.RawMessage `json:"-"`
}

// Provider is one model endpoint.
type Provider struct {
	// Dialect is the wire protocol: "openai-chat" (also the default when empty),
	// "anthropic" or "openai-responses".
	Dialect string `json:"dialect,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	// APIKeyEnv names the environment variable holding the API key. The key
	// itself is never stored in configuration.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Auth says how requests are authorised when it is not an API key: "chatgpt-plan" is a ChatGPT plan, signed in with
	// `sleipnir login chatgpt` (the openai-responses dialect). Empty: the API key.
	Auth    string            `json:"auth,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Options are provider-specific request options, passed through untouched.
	Options map[string]any `json:"options,omitempty"`

	// AllowHosts lists hosts, besides the provider's own, that may receive its API
	// key when the base URL is changed by the environment (<NAME>_BASE_URL) or by a
	// project file: "gateway.example.com" (any port) or "gateway.example.com:8443".
	// Without an entry such an override is refused, because a key issued for one
	// provider must not follow a URL that a repository (a .envrc, a project
	// config) chose. Loopback hosts never need one.
	//
	// Only the user's own configuration file can set it: a project file that
	// carries it has it ignored, trusted or not.
	AllowHosts []string `json:"allow_hosts,omitempty"`
	// AllowInsecureHTTP lets the API key travel over plain http to a host that is
	// not this machine (a trusted proxy on a LAN). By default a key is only sent
	// over https, or over http to a loopback address. User configuration only, like
	// AllowHosts.
	AllowInsecureHTTP bool `json:"allow_insecure_http,omitempty"`

	// BaseURLFromProject records that a project-level file (trusted, or Load would
	// have dropped it) supplied BaseURL. Load fills it in; it is not part of the file
	// format. Callers that send the provider's key to BaseURL use it to refuse a
	// repository-chosen host that the user has not allowed (AllowHosts).
	BaseURLFromProject bool `json:"-"`
}

// APIKey reads the provider's API key, at call time: from the environment or, when
// harden.MoveKeys took it out of there, from the harness's own memory. It returns ""
// when APIKeyEnv is empty or the variable is unset.
func (p Provider) APIKey() string {
	if p.APIKeyEnv == "" {
		return ""
	}
	return strings.TrimSpace(harden.Secret(p.APIKeyEnv))
}

// EffectiveDialect is Dialect, defaulting to "openai-chat".
func (p Provider) EffectiveDialect() string {
	if p.Dialect == "" {
		return DialectOpenAIChat
	}
	return p.Dialect
}

// Models says which model each role uses.
type Models struct {
	// Default is the "provider/model" used when a role has no entry.
	Default string `json:"default,omitempty"`
	// Roles maps a role name to "provider/model".
	Roles map[string]string `json:"roles,omitempty"`
	// Favorites are "provider/model" references the model listing (`sleipnir models`) puts first.
	Favorites []string `json:"favorites,omitempty"`
}

// Permissions configures the permission engine.
type Permissions struct {
	// Mode is "default", "accept-edits", "plan", "bypass" or "yolo" (see perm.Mode).
	Mode  string   `json:"mode,omitempty"`
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	// Roles overrides the above per swarm role.
	Roles map[string]RolePermissions `json:"roles,omitempty"`
}

// RolePermissions is the per-role permission override.
type RolePermissions struct {
	Mode  string   `json:"mode,omitempty"`
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// Cache tunes the prompt-cache engine. Token values of 0 mean "use the
// engine's default"; the comments name the kv setting each maps to.
type Cache struct {
	// InstructionMaxTokens caps instruction files in the shared layer. Zero uses
	// up to 16384 tokens, limited to a quarter of the initial model's context.
	InstructionMaxTokens int `json:"instruction_max_tokens"`
	// SharedTTL is the lifetime requested for cache entries on the shared and
	// role layers: "5m" or "1h".
	SharedTTL string `json:"shared_ttl,omitempty"`
	// MinLayerForBreakpoint skips a cache breakpoint after a layer smaller than
	// this many tokens (kv.Policy.MinLayerForBreakpoint; 0 places them always).
	MinLayerForBreakpoint int `json:"min_layer_for_breakpoint"`
	// CompactThresholdTokens is the thread size at which compaction is forced
	// whatever it costs (kv.Planner.HardThreadTokens).
	CompactThresholdTokens int `json:"compact_threshold_tokens"`
	// ThreadSoftLimitTokens is the thread size at which compaction is considered
	// (kv.Planner.SoftThreadTokens).
	ThreadSoftLimitTokens int `json:"thread_soft_limit_tokens"`
	// HotMaxTokens caps the always-fresh "hot" layer.
	HotMaxTokens int `json:"hot_max_tokens"`
	// AffinityShards spreads a large swarm over several provider cache routing
	// keys; 0 or 1 uses one.
	AffinityShards int `json:"affinity_shards"`
}

// SharedTTLDuration is SharedTTL as a duration (5 minutes when unset).
func (c Cache) SharedTTLDuration() time.Duration {
	if c.SharedTTL == "1h" {
		return time.Hour
	}
	return 5 * time.Minute
}

// Swarm bounds multi-agent runs. A 0 means "no limit" (or the swarm's own
// default) for every number.
type Swarm struct {
	MaxAgents             int `json:"max_agents"`
	RequestsPerMinute     int `json:"requests_per_minute"`
	MaxConcurrentRequests int `json:"max_concurrent_requests"`
	// Isolation is "none" (the default: every agent edits the one working tree,
	// guarded by write leases; "shared" is the older spelling of the same thing) or
	// "worktree": each writer gets a git worktree of its own and finished work is
	// integrated through a verifying merge queue (docs/SWARM-PROTOCOL.md section 14).
	// The trees always live in the user's cache directory, never in the repository
	// and never anywhere a configuration value names: a project's file may switch
	// isolation on (it only reduces risk), but it cannot choose where anything is
	// written.
	Isolation string `json:"isolation,omitempty"`
	// Mailman routes worker mail through a mailman agent that turns bursts into short
	// digests (default false: mail is delivered at once). The router's checks and rate
	// limits are unchanged, the manager's and the harness's own mail never goes through
	// the mailman, and every digest names its original senders; a mailman that is absent
	// or slow costs delay, never mail (docs/SWARM-PROTOCOL.md section 5). It runs on the
	// session's model unless one is given for the role (--role-model mailman=<model>).
	// Like isolation, it changes nothing about what a project's files can reach, so a
	// project's file may set it.
	Mailman bool `json:"mailman,omitempty"`
	// BudgetUSD is the most a swarm run may spend, retired agents included; 0 is no
	// limit. It defaults to DefaultSwarmBudgetUSD, a safety net: a swarm can spend many
	// times what one agent does, and a budget that is off by default is one nobody
	// remembers to set. Raising or removing it is the user's decision, so a project's
	// file cannot (trust.go); --budget-usd overrides it for one run.
	BudgetUSD float64 `json:"budget_usd"`
}

// DefaultSwarmBudgetUSD is the built-in cap on what a swarm run may spend, in US dollars.
const DefaultSwarmBudgetUSD float64 = 50

// Isolation modes as IsolationMode reports them.
const (
	IsolationNone     = "none"
	IsolationWorktree = "worktree"
)

// IsolationMode is the effective isolation: "worktree", or "none" for the default,
// an empty value and the older spelling "shared".
func (s Swarm) IsolationMode() string {
	if s.Isolation == IsolationWorktree {
		return IsolationWorktree
	}
	return IsolationNone
}

// Tools bounds tool output and work (mirrors tools.Limits).
type Tools struct {
	MaxOutputChars    int `json:"max_output_chars"`
	DefaultTimeoutSec int `json:"default_timeout_sec"`
	MaxTimeoutSec     int `json:"max_timeout_sec"`
	// WebAllowPrivate lets web tools reach private and loopback addresses.
	WebAllowPrivate bool `json:"web_allow_private"`
	// WebAllowHosts are hosts the web tools may always reach.
	WebAllowHosts []string `json:"web_allow_hosts,omitempty"`
}

// DefaultTimeout is DefaultTimeoutSec as a duration.
func (t Tools) DefaultTimeout() time.Duration {
	return time.Duration(t.DefaultTimeoutSec) * time.Second
}

// MaxTimeout is MaxTimeoutSec as a duration.
func (t Tools) MaxTimeout() time.Duration { return time.Duration(t.MaxTimeoutSec) * time.Second }

// Defaults returns the built-in configuration: the lowest-precedence layer.
// Numbers the engines own (cache and swarm tuning) are left at 0, meaning "the
// engine's default"; the rest mirror the defaults of the packages they feed.
func Defaults() *Config {
	return &Config{
		Permissions: Permissions{Mode: string(perm.ModeDefault)},
		Cache: Cache{
			SharedTTL:             "5m",
			MinLayerForBreakpoint: 1500, // kv.DefaultPolicy
		},
		Swarm: Swarm{Isolation: IsolationNone, BudgetUSD: DefaultSwarmBudgetUSD},
		Tools: Tools{
			MaxOutputChars:    24_000, // tools.DefaultLimits
			DefaultTimeoutSec: 120,
			MaxTimeoutSec:     600,
		},
	}
}

// SplitModelRef splits "provider/model" at the first slash; the model part may
// itself contain slashes (marketplace ids such as "openrouter/vendor/model").
func SplitModelRef(ref string) (provider, model string, ok bool) {
	provider, model, ok = strings.Cut(ref, "/")
	if !ok || provider == "" || model == "" || strings.TrimSpace(ref) != ref {
		return "", "", false
	}
	return provider, model, true
}

// ModelFor returns the "provider/model" reference for a role: its entry in
// Models.Roles, else Models.Default ("" when neither is set).
func (c *Config) ModelFor(role string) string {
	if ref := c.Models.Roles[role]; ref != "" {
		return ref
	}
	return c.Models.Default
}

// ModeFor returns the permission mode for a role: the role's override, else the
// global mode, else "default".
func (p Permissions) ModeFor(role string) perm.Mode {
	if m := p.Roles[role].Mode; m != "" {
		return perm.Mode(m)
	}
	if p.Mode != "" {
		return perm.Mode(p.Mode)
	}
	return perm.ModeDefault
}

// knownKey reports whether name is exactly a top-level key of Config. Matching
// is case-sensitive on purpose: "Models" is a typo to be reported, not a second
// spelling of "models" that silently takes effect.
func knownKey(name string) bool {
	for _, f := range structFields(reflect.TypeOf(Config{})) {
		if f.name == name {
			return true
		}
	}
	return false
}

// UnmarshalJSON decodes into c, leaving fields that are absent from data
// untouched (so decoding over Defaults() overlays them), and collects unknown
// top-level keys into Extra.
func (c *Config) UnmarshalJSON(data []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	known := map[string]json.RawMessage{}
	extra := map[string]json.RawMessage{}
	for k, v := range all {
		if knownKey(k) {
			known[k] = v
		} else {
			extra[k] = v
		}
	}
	b, err := json.Marshal(known)
	if err != nil {
		return err
	}
	type plain Config // no methods: avoids recursing into this function
	if err := json.Unmarshal(b, (*plain)(c)); err != nil {
		return err
	}
	if len(extra) > 0 {
		if c.Extra == nil {
			c.Extra = map[string]json.RawMessage{}
		}
		for k, v := range extra {
			c.Extra[k] = v
		}
	}
	return nil
}

// MarshalJSON encodes c with Extra's keys included, so a decode/encode round
// trip loses nothing.
func (c Config) MarshalJSON() ([]byte, error) {
	type plain Config
	b, err := json.Marshal(plain(c))
	if err != nil || len(c.Extra) == 0 {
		return b, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for k, v := range c.Extra {
		if _, taken := all[k]; !taken {
			all[k] = v
		}
	}
	return marshalSorted(all)
}

// marshalSorted encodes a value with sorted keys, no HTML escaping and no
// trailing newline.
func marshalSorted(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
