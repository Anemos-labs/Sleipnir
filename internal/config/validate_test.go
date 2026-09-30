package config

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Config)
		wantErr  []string // paths of errors
		wantWarn []string // paths of warnings
		contains map[string]string
	}{
		{name: "defaults are valid", mutate: func(*Config) {}},
		{
			name:   "a full valid configuration",
			mutate: func(c *Config) { *c = *validFull() },
		},
		{
			name: "unknown dialect",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"p": {Dialect: "gpt"}}
			},
			wantErr:  []string{"providers.p.dialect"},
			contains: map[string]string{"providers.p.dialect": "valid: anthropic, openai-chat, openai-responses"},
		},
		{
			name: "all three dialects and the empty default are fine",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"a": {Dialect: "anthropic"}, "b": {Dialect: "openai-chat"}, "c": {Dialect: "openai-responses"}, "d": {}}
			},
		},
		{
			name: "base_url must be an absolute http(s) URL",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{
					"ok": {BaseURL: "https://api.example.com/v1"}, "rel": {BaseURL: "/v1"}, "ftp": {BaseURL: "ftp://x.example.com"},
					"nohost": {BaseURL: "https://"}, "junk": {BaseURL: "ht tp://x"},
				}
			},
			wantErr: []string{"providers.ftp.base_url", "providers.junk.base_url", "providers.nohost.base_url", "providers.rel.base_url"},
		},
		{
			name: "plain http to a remote host warns, to loopback does not",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"remote": {BaseURL: "http://models.example.com"}, "local": {BaseURL: "http://localhost:11434/v1"}, "lo4": {BaseURL: "http://127.0.0.1:8080"}, "lo6": {BaseURL: "http://[::1]:8080"}}
			},
			wantWarn: []string{"providers.remote.base_url"},
		},
		{
			name: "credentials embedded in a URL warn",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"p": {BaseURL: "https://user:hunter2@api.example.com"}}
			},
			wantWarn: []string{"providers.p.base_url"},
		},
		{
			name: "api_key_env must be a variable name and is never echoed",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"good": {APIKeyEnv: "OPENAI_API_KEY"}, "bad": {APIKeyEnv: "sk-live-abc123"}, "space": {APIKeyEnv: "MY KEY"}}
			},
			wantErr: []string{"providers.bad.api_key_env", "providers.space.api_key_env"},
		},
		{
			name: "header names and values",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"p": {Headers: map[string]string{"X-Ok": "v", "Bad Name": "v", "X-Inject": "a\r\nEvil: 1", "": "v"}}}
			},
			wantErr: []string{"providers.p.headers.X-Inject", `providers.p.headers[""]`, `providers.p.headers["Bad Name"]`},
		},
		{
			name: "provider names",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"has/slash": {}, "has space": {}, "fine": {}}
			},
			wantErr: []string{`providers["has space"]`, `providers["has/slash"]`},
		},
		{
			name: "provider model lists",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"p": {Models: []string{"a", "b", "a", "", "has space"}}}
			},
			wantErr:  []string{"providers.p.models[3]", "providers.p.models[4]"},
			wantWarn: []string{"providers.p.models[2]"},
		},
		{
			name: "model references",
			mutate: func(c *Config) {
				c.Models = Models{Default: "no-slash", Compactor: "ok/model", Roles: map[string]string{"planner": "a/b", "coder": "bad", "empty": "", "has space": "a/b"}}
			},
			wantErr: []string{"models.default", "models.roles.coder", "models.roles.empty", `models.roles["has space"]`},
		},
		{
			name: "references to unconfigured providers are not judged",
			mutate: func(c *Config) {
				c.Providers = map[string]Provider{"only": {}}
				c.Models.Default = "builtin/model"
			},
		},
		{
			name: "permission modes",
			mutate: func(c *Config) {
				c.Permissions = Permissions{Mode: "yolo", Roles: map[string]RolePermissions{"a": {Mode: "plan"}, "b": {Mode: "nope"}}}
			},
			wantErr:  []string{"permissions.mode", "permissions.roles.b.mode"},
			contains: map[string]string{"permissions.mode": "valid: default, accept-edits, plan, bypass"},
		},
		{
			name:     "bypass warns",
			mutate:   func(c *Config) { c.Permissions.Mode = "bypass" },
			wantWarn: []string{"permissions.mode"},
		},
		{
			name: "rule lists",
			mutate: func(c *Config) {
				c.Permissions = Permissions{
					Allow: []string{"Read", "Read", " ", "Edit"}, Deny: []string{"Edit"}, Ask: []string{""},
					Roles: map[string]RolePermissions{"r": {Allow: []string{"X"}, Deny: []string{"X"}}},
				}
			},
			wantErr:  []string{"permissions.allow[2]", "permissions.ask[0]"},
			wantWarn: []string{"permissions.allow[1]", "permissions.allow[3]", "permissions.roles.r.allow[0]"},
		},
		{
			name: "rule syntax is checked with the permission engine's parser",
			mutate: func(c *Config) {
				c.Permissions = Permissions{
					Allow: []string{"Bash(go test:*)", "Read(src/**)", "WebFetch(domain:example.com)", "mcp__srv__tool", "Bash(unclosed"},
					Deny:  []string{"Bash(rm:*)", "(nothing)", "Bad Tool Name(x)"},
				}
			},
			wantErr:  []string{"permissions.allow[4]", "permissions.deny[1]", "permissions.deny[2]"},
			contains: map[string]string{"permissions.allow[4]": "missing closing parenthesis"},
		},
		{
			name: "cache",
			mutate: func(c *Config) {
				c.Cache = Cache{SharedTTL: "2h", MinLayerForBreakpoint: -1, CompactThresholdTokens: -1, ThreadSoftLimitTokens: -5, HotMaxTokens: -1, AffinityShards: -2}
			},
			wantErr: []string{"cache.affinity_shards", "cache.compact_threshold_tokens", "cache.hot_max_tokens", "cache.min_layer_for_breakpoint", "cache.shared_ttl", "cache.thread_soft_limit_tokens"},
		},
		{
			name: "cache ttl values",
			mutate: func(c *Config) {
				c.Cache.SharedTTL = "1h"
			},
		},
		{
			name: "swarm",
			mutate: func(c *Config) {
				c.Swarm = Swarm{MaxAgents: -1, RequestsPerMinute: -1, MaxConcurrentRequests: -1, Isolation: "vm", BudgetUSD: -0.01}
			},
			wantErr: []string{"swarm.budget_usd", "swarm.isolation", "swarm.max_agents", "swarm.max_concurrent_requests", "swarm.requests_per_minute"},
		},
		{
			name:     "tools",
			mutate:   func(c *Config) { c.Tools = Tools{MaxOutputChars: -1, DefaultTimeoutSec: 900, MaxTimeoutSec: 60} },
			wantErr:  []string{"tools.default_timeout_sec", "tools.max_output_chars"},
			contains: map[string]string{"tools.default_timeout_sec": "exceeds the maximum"},
		},
		{
			name:   "a default timeout with no maximum is fine",
			mutate: func(c *Config) { c.Tools = Tools{DefaultTimeoutSec: 900} },
		},
		{
			name: "web hosts",
			mutate: func(c *Config) {
				c.Tools.WebAllowHosts = []string{"example.com", "https://docs.example.com/x", " ", "a.example.com/path"}
			},
			wantErr:  []string{"tools.web_allow_hosts[2]"},
			wantWarn: []string{"tools.web_allow_hosts[1]", "tools.web_allow_hosts[3]"},
		},
		{
			name: "unknown top-level keys warn",
			mutate: func(c *Config) {
				c.Extra = map[string]json.RawMessage{"future": []byte(`1`), "$schema": []byte(`"x"`)}
			},
			wantWarn: []string{"future"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Defaults()
			tc.mutate(c)
			issues := c.Validate()
			var errs, warns []string
			byPath := map[string]string{}
			for _, is := range issues {
				switch is.Severity {
				case SeverityError:
					errs = append(errs, is.Path)
				default:
					warns = append(warns, is.Path)
				}
				byPath[is.Path] += is.Message + "\n"
				if is.Path != "" && is.segs == nil {
					t.Errorf("issue %q has no segments; Load cannot attribute it to a file", is.Path)
				}
			}
			sort.Strings(errs)
			sort.Strings(warns)
			if !reflect.DeepEqual(nz(errs), nz(tc.wantErr)) {
				t.Errorf("error paths = %v, want %v\n%v", errs, tc.wantErr, issues)
			}
			if !reflect.DeepEqual(nz(warns), nz(tc.wantWarn)) {
				t.Errorf("warning paths = %v, want %v\n%v", warns, tc.wantWarn, issues)
			}
			for path, sub := range tc.contains {
				if !strings.Contains(byPath[path], sub) {
					t.Errorf("message for %s = %q, want it to contain %q", path, byPath[path], sub)
				}
			}
		})
	}
}

func nz(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func validFull() *Config {
	c := Defaults()
	c.Providers = map[string]Provider{
		"anthropic": {Dialect: "anthropic", BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY", Models: []string{"claude-sonnet-5-5"}},
		"openrouter": {Dialect: "openai-chat", BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY",
			Headers: map[string]string{"HTTP-Referer": "https://example.com"}, Options: map[string]any{"route": "fallback"}},
	}
	c.Models = Models{Default: "anthropic/claude-sonnet-5-5", Compactor: "openrouter/vendor/small:free", Roles: map[string]string{"planner": "anthropic/claude-opus-5"}}
	c.Permissions = Permissions{Mode: "accept-edits", Allow: []string{"Read", "Bash(go test:*)"}, Ask: []string{"Bash"}, Deny: []string{"Bash(rm:*)"},
		Roles: map[string]RolePermissions{"reviewer": {Mode: "plan", Allow: []string{"Read"}}}}
	c.Cache = Cache{SharedTTL: "1h", MinLayerForBreakpoint: 1500, CompactThresholdTokens: 60000, ThreadSoftLimitTokens: 20000, HotMaxTokens: 2000, AffinityShards: 4, Prewarm: true, Keepalive: true}
	c.Swarm = Swarm{MaxAgents: 8, RequestsPerMinute: 120, MaxConcurrentRequests: 16, Isolation: "worktree", BudgetUSD: 25}
	c.Tools = Tools{MaxOutputChars: 24000, DefaultTimeoutSec: 120, MaxTimeoutSec: 600, WebAllowHosts: []string{"docs.example.com"}}
	c.UI = UI{Theme: "dark", Editor: "vim"}
	c.Training = Training{Enabled: true, RedactSecrets: true, Dir: ".sleipnir/training"}
	return c
}
