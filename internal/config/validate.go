package config

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/reee344/sleipnir/internal/perm"
)

var (
	envNameRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNameRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

var (
	dialects   = []string{DialectAnthropic, DialectOpenAIChat, DialectOpenAIResponses}
	permModes  = []perm.Mode{perm.ModeDefault, perm.ModeAcceptEdits, perm.ModePlan, perm.ModeBypass}
	cacheTTLs  = []string{"5m", "1h"}
	isolations = []string{"shared", "worktree"}
)

type validator struct{ issues []Issue }

func (v *validator) add(sev Severity, segs []string, format string, args ...any) {
	v.issues = append(v.issues, Issue{
		Severity: sev,
		Path:     fmtPath(segs),
		Message:  fmt.Sprintf(format, args...),
		segs:     cloneSegs(segs),
	})
}

func (v *validator) err(segs []string, format string, args ...any) {
	v.add(SeverityError, segs, format, args...)
}

func (v *validator) warn(segs []string, format string, args ...any) {
	v.add(SeverityWarning, segs, format, args...)
}

func seg(parts ...string) []string { return parts }

// Validate checks the values of a configuration and returns what it finds:
// errors for values that cannot work (an unknown dialect, a negative limit, a
// malformed URL), warnings for things that work but are probably not intended
// (unknown keys, bypass mode, a model on a provider that is not configured).
// It never modifies c. Issues carry the field path; Load adds file and line.
func (c *Config) Validate() []Issue {
	v := &validator{}
	for _, name := range sortedKeys(c.Providers) {
		v.provider(name, c.Providers[name])
	}
	v.models(c)
	v.permissions(c.Permissions)
	v.cache(c.Cache)
	v.swarm(c.Swarm)
	v.tools(c.Tools)
	for _, k := range sortedKeys(c.Extra) {
		if strings.HasPrefix(k, "$") { // "$schema" and friends
			continue
		}
		v.warn(seg(k), "unknown key %q; it is kept but has no effect", k)
		v.issues[len(v.issues)-1].code = codeUnknownKey
	}
	return v.issues
}

func (v *validator) provider(name string, p Provider) {
	base := seg("providers", name)
	switch {
	case name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, " \t\r\n"):
		v.err(base, "provider names cannot be empty or contain whitespace")
	case strings.Contains(name, "/"):
		v.err(base, "provider names cannot contain '/': model references are written provider/model")
	}
	if p.Dialect != "" && !slices.Contains(dialects, p.Dialect) {
		v.err(append(slices.Clone(base), "dialect"), "unknown dialect %q (valid: %s)", p.Dialect, strings.Join(dialects, ", "))
	}
	if p.BaseURL != "" {
		v.baseURL(append(slices.Clone(base), "base_url"), p.BaseURL)
	}
	if p.APIKeyEnv != "" && !envNameRE.MatchString(p.APIKeyEnv) {
		// The value is deliberately not echoed: a real key pasted here must not
		// end up in a log.
		v.err(append(slices.Clone(base), "api_key_env"), "must be the NAME of an environment variable (letters, digits and underscores), not the key itself")
	}
	for _, h := range sortedKeys(p.Headers) {
		hs := append(slices.Clone(base), "headers", h)
		if !headerNameRE.MatchString(h) {
			v.err(hs, "not a valid HTTP header name")
		}
		if strings.ContainsAny(p.Headers[h], "\r\n\x00") {
			v.err(hs, "header values cannot contain line breaks or NUL")
		}
	}
	seen := map[string]bool{}
	for i, m := range p.Models {
		ms := append(slices.Clone(base), "models", fmt.Sprintf("[%d]", i))
		switch {
		case strings.TrimSpace(m) == "" || strings.ContainsAny(m, " \t\r\n"):
			v.err(ms, "model ids cannot be empty or contain whitespace")
		case seen[m]:
			v.warn(ms, "duplicate model id %q", m)
		}
		seen[m] = true
	}
}

func (v *validator) baseURL(segs []string, raw string) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		v.err(segs, "must be an absolute http(s) URL such as https://api.example.com/v1")
		return
	}
	if u.User != nil {
		v.warn(segs, "the URL embeds credentials; keep them out of configuration files")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		v.warn(segs, "plain http sends prompts and the API key unencrypted; use https unless this is a local endpoint")
	}
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (v *validator) models(c *Config) {
	// The provider named by a reference is deliberately not looked up: built-in
	// providers need no entry in the file, so absence proves nothing.
	check := func(segs []string, ref string) {
		if ref == "" {
			return
		}
		if _, _, ok := SplitModelRef(ref); !ok {
			v.err(segs, "must look like \"provider/model\", got %q", ref)
		}
	}
	check(seg("models", "default"), c.Models.Default)
	check(seg("models", "compactor"), c.Models.Compactor)
	for _, role := range sortedKeys(c.Models.Roles) {
		rs := seg("models", "roles", role)
		if role == "" || strings.ContainsAny(role, " \t\r\n") {
			v.err(rs, "role names cannot be empty or contain whitespace")
		}
		if c.Models.Roles[role] == "" {
			v.err(rs, "must look like \"provider/model\"")
			continue
		}
		check(rs, c.Models.Roles[role])
	}
}

func (v *validator) mode(segs []string, mode string) {
	if mode == "" {
		return
	}
	if !slices.Contains(permModes, perm.Mode(mode)) {
		names := make([]string, len(permModes))
		for i, m := range permModes {
			names[i] = string(m)
		}
		v.err(segs, "unknown permission mode %q (valid: %s)", mode, strings.Join(names, ", "))
		return
	}
	if perm.Mode(mode) == perm.ModeBypass {
		v.warn(segs, "bypass mode turns off permission prompts; use it only inside a sandbox")
	}
}

// rules checks a list of permission rules with the permission engine's own
// parser, so a rule this accepts is a rule the engine will accept.
func (v *validator) rules(segs []string, action perm.Action, rules []string) {
	seen := map[string]bool{}
	for i, r := range rules {
		rs := append(slices.Clone(segs), fmt.Sprintf("[%d]", i))
		if strings.TrimSpace(r) == "" {
			v.err(rs, "rules cannot be empty")
			continue
		}
		if _, err := perm.ParseRule(action, r); err != nil {
			v.err(rs, "%s", strings.TrimPrefix(err.Error(), "perm: "))
			continue
		}
		if seen[r] {
			v.warn(rs, "duplicate rule %q", r)
		}
		seen[r] = true
	}
}

func (v *validator) permissions(p Permissions) {
	v.mode(seg("permissions", "mode"), p.Mode)
	v.rules(seg("permissions", "allow"), perm.Allow, p.Allow)
	v.rules(seg("permissions", "ask"), perm.Ask, p.Ask)
	v.rules(seg("permissions", "deny"), perm.Deny, p.Deny)
	v.overlap(seg("permissions"), p.Allow, p.Deny)
	for _, role := range sortedKeys(p.Roles) {
		r := p.Roles[role]
		base := seg("permissions", "roles", role)
		if role == "" || strings.ContainsAny(role, " \t\r\n") {
			v.err(base, "role names cannot be empty or contain whitespace")
		}
		v.mode(append(slices.Clone(base), "mode"), r.Mode)
		v.rules(append(slices.Clone(base), "allow"), perm.Allow, r.Allow)
		v.rules(append(slices.Clone(base), "ask"), perm.Ask, r.Ask)
		v.rules(append(slices.Clone(base), "deny"), perm.Deny, r.Deny)
		v.overlap(base, r.Allow, r.Deny)
	}
}

// overlap warns about a rule that is both allowed and denied: whichever the
// engine prefers, the file is contradicting itself.
func (v *validator) overlap(base []string, allow, deny []string) {
	for i, r := range allow {
		if slices.Contains(deny, r) {
			v.warn(append(slices.Clone(base), "allow", fmt.Sprintf("[%d]", i)), "rule %q is in both allow and deny", r)
		}
	}
}

func (v *validator) nonNegative(segs []string, n int) {
	if n < 0 {
		v.err(segs, "must not be negative, got %d", n)
	}
}

func (v *validator) cache(c Cache) {
	if c.SharedTTL != "" && !slices.Contains(cacheTTLs, c.SharedTTL) {
		v.err(seg("cache", "shared_ttl"), "must be \"5m\" or \"1h\", got %q", c.SharedTTL)
	}
	v.nonNegative(seg("cache", "min_layer_for_breakpoint"), c.MinLayerForBreakpoint)
	v.nonNegative(seg("cache", "compact_threshold_tokens"), c.CompactThresholdTokens)
	v.nonNegative(seg("cache", "thread_soft_limit_tokens"), c.ThreadSoftLimitTokens)
	v.nonNegative(seg("cache", "hot_max_tokens"), c.HotMaxTokens)
	v.nonNegative(seg("cache", "affinity_shards"), c.AffinityShards)
}

func (v *validator) swarm(s Swarm) {
	v.nonNegative(seg("swarm", "max_agents"), s.MaxAgents)
	v.nonNegative(seg("swarm", "requests_per_minute"), s.RequestsPerMinute)
	v.nonNegative(seg("swarm", "max_concurrent_requests"), s.MaxConcurrentRequests)
	if s.Isolation != "" && !slices.Contains(isolations, s.Isolation) {
		v.err(seg("swarm", "isolation"), "must be \"shared\" or \"worktree\", got %q", s.Isolation)
	}
	if s.BudgetUSD < 0 || math.IsNaN(s.BudgetUSD) || math.IsInf(s.BudgetUSD, 0) {
		v.err(seg("swarm", "budget_usd"), "must be zero (no limit) or a positive amount")
	}
}

func (v *validator) tools(t Tools) {
	v.nonNegative(seg("tools", "max_output_chars"), t.MaxOutputChars)
	v.nonNegative(seg("tools", "default_timeout_sec"), t.DefaultTimeoutSec)
	v.nonNegative(seg("tools", "max_timeout_sec"), t.MaxTimeoutSec)
	if t.MaxTimeoutSec > 0 && t.DefaultTimeoutSec > t.MaxTimeoutSec {
		v.err(seg("tools", "default_timeout_sec"), "the default timeout (%ds) exceeds the maximum (%ds)", t.DefaultTimeoutSec, t.MaxTimeoutSec)
	}
	for i, h := range t.WebAllowHosts {
		hs := seg("tools", "web_allow_hosts", fmt.Sprintf("[%d]", i))
		switch {
		case strings.TrimSpace(h) == "":
			v.err(hs, "hosts cannot be empty")
		case strings.Contains(h, "://") || strings.Contains(h, "/"):
			v.warn(hs, "give the host name only (for example example.com), not a URL")
		}
	}
}
