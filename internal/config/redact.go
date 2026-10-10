package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/mcp"
	"github.com/anemos-labs/sleipnir/internal/rl/redact"
)

// Showing a configuration (a settings page, a bug report) must not show a credential, and a configuration can hold one in more places
// than the provider key (which it never holds: it names the variable): a provider's headers and options, a hook's headers and command
// line, an MCP server's environment, headers, arguments and URL. Redact is an allow-list over those three sections (only the fields
// this package knows are copied, each by its own rule) and a check of every other string, by the shape of the value and the name of its
// key, plus the keys this process holds (harden.Held). Unknown top-level keys are not shown at all.

// Hidden is what Redact shows in place of a value it withholds.
const Hidden = "(set, not shown)"

// invalidMCP stands in for an MCP entry that does not parse.
const invalidMCP = "(not a valid entry: see `sleipnir mcp list`)"

// Redact returns cfg as a JSON-ready tree (the shape of the file, every field of the effective configuration) with what may be a
// credential replaced by Hidden: provider header values, hook header and environment values, MCP environment and header values, URL
// user information and query values, MCP arguments and other strings that look like secrets, tokens inside command lines, and the
// values of unknown top-level keys. It never returns nil.
func Redact(cfg *Config) map[string]any {
	if cfg == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return map[string]any{}
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return map[string]any{}
	}
	out, _ := RedactAt(nil, doc).(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// RedactAt redacts v, the value found at the path segs of a configuration document (nil segs: the whole document), with the rules of
// Redact. It is how a value of one layer (LayerValues.Values) is shown. The result shares nothing with v.
func RedactAt(segs []string, v any) any {
	if v == nil {
		return nil
	}
	if len(segs) == 0 {
		m, ok := v.(map[string]any)
		if !ok {
			return Hidden
		}
		out := make(map[string]any, len(m))
		for _, k := range sortedKeys(m) {
			out[k] = RedactAt([]string{k}, m[k])
		}
		return out
	}
	switch segs[0] {
	case "providers":
		return redactProviders(segs, v)
	case "mcp":
		return redactMCP(segs, v)
	case "hooks":
		return scrubTree(segs, v, true)
	}
	if !knownKey(segs[0]) {
		return Hidden
	}
	return scrubTree(segs, v, false)
}

// providerFields are the fields of a provider entry that are shown, each by the rule of RedactAt.
var providerFields = map[string]bool{
	"dialect": true, "base_url": true, "api_key_env": true, "auth": true, "headers": true, "options": true,
	"allow_hosts": true, "allow_insecure_http": true,
}

// redactProviders applies the provider allow-list at any depth of the providers section.
func redactProviders(segs []string, v any) any {
	switch len(segs) {
	case 1, 2: // the section, or one entry
		m, ok := v.(map[string]any)
		if !ok {
			return Hidden
		}
		out := make(map[string]any, len(m))
		for _, k := range sortedKeys(m) {
			if len(segs) == 2 && !providerFields[k] {
				out[k] = Hidden
				continue
			}
			out[k] = redactProviders(cloneSegs(segs, k), m[k])
		}
		return out
	}
	switch field := segs[2]; field {
	case "headers":
		return hideValues(v, len(segs) == 3)
	case "base_url":
		if s, ok := v.(string); ok && len(segs) == 3 {
			return RedactURL(s)
		}
		return Hidden
	case "options":
		return scrubTree(segs, v, false)
	case "allow_insecure_http":
		if b, ok := v.(bool); ok {
			return b
		}
		return Hidden
	}
	return scrubTree(segs, v, false) // dialect, api_key_env, auth: names; allow_hosts: host names
}

// hideValues replaces the values of a map (section true) or a single value by Hidden, keeping the names.
func hideValues(v any, section bool) any {
	if !section {
		return Hidden
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Hidden
	}
	out := make(map[string]any, len(m))
	for k := range m {
		out[k] = Hidden
	}
	return out
}

// redactMCP shows an MCP entry as `config --json` does (the entry parsed, then mcp.ServerConfig.Redacted), with each argument and
// the command checked for secrets.
func redactMCP(segs []string, v any) any {
	switch len(segs) {
	case 1:
		m, ok := v.(map[string]any)
		if !ok {
			return Hidden
		}
		out := make(map[string]any, len(m))
		for _, k := range sortedKeys(m) {
			out[k] = redactMCP(cloneSegs(segs, k), m[k])
		}
		return out
	case 2:
		raw, err := json.Marshal(v)
		if err != nil {
			return invalidMCP
		}
		servers, _ := mcp.ParseWith(map[string]json.RawMessage{segs[1]: raw}, mcp.ParseOptions{Scope: mcp.ScopeUser})
		c, ok := servers[segs[1]]
		if !ok {
			return invalidMCP
		}
		return MCPEntry(c)
	}
	switch v.(type) {
	case bool, float64:
		return v
	}
	return Hidden
}

// MCPEntry is an MCP server definition as a settings page shows it: its type, its command (tokens in it withheld) and its arguments
// (each one that looks like a secret withheld), its URL reduced to scheme and host, the names of its environment variables and
// headers with their values withheld, and its switches.
func MCPEntry(c mcp.ServerConfig) map[string]any {
	c = c.Redacted()
	e := map[string]any{"type": c.EffectiveType()}
	if c.Command != "" {
		e["command"] = ScrubText(c.Command)
	}
	if len(c.Args) > 0 {
		e["args"] = RedactArgs(c.Args)
	}
	if c.URL != "" {
		e["url"] = c.URL
	}
	if c.Cwd != "" {
		e["cwd"] = ScrubText(c.Cwd)
	}
	if len(c.Env) > 0 {
		e["env"] = hideNames(c.Env)
	}
	if len(c.Headers) > 0 {
		e["headers"] = hideNames(c.Headers)
	}
	if c.Disabled {
		e["disabled"] = true
	}
	if c.Trust {
		e["trust"] = true
	}
	if c.AllowPrivate {
		e["allow_private"] = true
	}
	if len(c.AllowTools) > 0 {
		e["allow_tools"] = scrubStrings(c.AllowTools)
	}
	if len(c.DenyTools) > 0 {
		e["deny_tools"] = scrubStrings(c.DenyTools)
	}
	if c.Timeout > 0 {
		e["timeout"] = c.Timeout.String()
	}
	if c.StartupTimeout > 0 {
		e["startup_timeout"] = c.StartupTimeout.String()
	}
	if c.MaxOutputChars > 0 {
		e["max_output_chars"] = c.MaxOutputChars
	}
	return e
}

// hideNames maps every name of m to Hidden.
func hideNames(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k := range m {
		out[k] = Hidden
	}
	return out
}

// scrubStrings applies ScrubText to each string.
func scrubStrings(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = ScrubText(s)
	}
	return out
}

// RedactArgs is a command's argument list with every argument that looks like a secret (by its shape, by a token inside it, or by
// being the value of a flag named like one, as in --token X) replaced by Hidden.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		switch {
		case secretString(nil, a), ScrubText(a) != a:
			out[i] = Hidden
		case i > 0 && strings.HasPrefix(args[i-1], "-") && !strings.Contains(args[i-1], "=") && secretFlag(args[i-1]) && !strings.HasPrefix(a, "-"):
			out[i] = Hidden
		default:
			out[i] = a
		}
	}
	return out
}

// secretFlagName matches flag names whose value is a credential (--token, --api-key, --password, -p is not one).
var secretFlagName = regexp.MustCompile(`(?i)(token|secret|passw(or)?d|api[_-]?key|credential|auth)`)

// secretFlag reports whether a flag (with its dashes) names a credential.
func secretFlag(flag string) bool { return secretFlagName.MatchString(strings.TrimLeft(flag, "-")) }

// scrubTree copies a generic JSON value and withholds what looks secret: strings by their shape and their key's name, and in the hooks
// section (hooks true) every header and environment value and the user information and query of every URL.
func scrubTree(segs []string, v any, hooks bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, k := range sortedKeys(t) {
			p := cloneSegs(segs, k)
			if hooks && (k == "headers" || k == "env") {
				out[k] = hideValues(t[k], true)
				continue
			}
			if hooks && k == "url" {
				if s, ok := t[k].(string); ok {
					out[k] = RedactURL(s)
					continue
				}
			}
			out[k] = scrubTree(p, t[k], hooks)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = scrubTree(segs, e, hooks)
		}
		return out
	case string:
		if secretString(segs, t) {
			return Hidden
		}
		return ScrubText(t)
	}
	return v
}

// secretString reports whether a whole string value is a credential: a known token format or a credential URL (harden.LooksSecret's
// value check), or a long token-shaped value under a key named like a secret (the check Load warns with). Keys ending in _env name a
// variable and never hide their value.
func secretString(segs []string, s string) bool {
	if harden.LooksSecret("", strings.TrimSpace(s)) {
		return true
	}
	return looksSecret(segs, s)
}

// RedactURL shows a URL with its user information and the values of its query replaced by Hidden; the scheme, host and path stay
// (tokens inside the path are withheld as in any text). A string that is not a URL is scrubbed as text; a ${VAR} template is kept.
func RedactURL(raw string) string {
	s := strings.TrimSpace(raw)
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || scheme == "" {
		if strings.Contains(s, "${") {
			return s
		}
		return ScrubText(s)
	}
	frag := ""
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest, frag = rest[:i], Hidden
	}
	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query = rest[:i], rest[i+1:]
	}
	host, path := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, path = rest[:i], rest[i:]
	}
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = Hidden + "@" + host[i+1:]
	}
	out := scheme + "://" + host + ScrubText(path)
	if query != "" {
		var parts []string
		for _, kv := range strings.Split(query, "&") {
			k, _, _ := strings.Cut(kv, "=")
			parts = append(parts, ScrubText(k)+"="+Hidden)
		}
		out += "?" + strings.Join(parts, "&")
	}
	if frag != "" {
		out += "#" + frag
	}
	return out
}

// scrubber finds credentials inside free text (a command line, a path): the token families, private keys, JWTs, passwords in URLs,
// bearer values, key = value literals and high-entropy strings next to a key word. Its salt is random per process, so its tokens link
// nothing across runs; ScrubText replaces them by Hidden anyway.
var scrubber = redact.New(redact.Config{
	Salt: randomSalt(),
	Kinds: []string{redact.GroupTokens, redact.KindPrivateKey, redact.KindJWT, redact.KindURLCred, redact.KindBearer,
		redact.KindSecret, redact.KindEntropy},
})

// redactedToken matches a replacement token of package redact.
var redactedToken = regexp.MustCompile(`⟦redacted:[^⟧]*⟧`)

// Credentials a command line passes as flags: the value of a flag named like a secret (--token X, --password=X, --api-key X) and the
// password of a user:password pair (curl -u, --user).
var (
	secretFlagValue = regexp.MustCompile(`(?i)((?:^|\s)--?[a-z0-9_-]*(?:token|secret|passw(?:or)?d|api[_-]?key|credential|auth)[a-z0-9_-]*)(=|\s+)('[^']*'|"[^"]*"|[^\s'"]+)`)
	userPassword    = regexp.MustCompile(`((?:^|\s)(?:-u|--user)(?:=|\s+)['"]?[^\s:'"]+):[^\s'"]+`)
)

// randomSalt returns 16 random hex characters, or a fixed string when the system has no randomness (the tokens are replaced anyway).
func randomSalt() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "sleipnir-config"
	}
	return hex.EncodeToString(b[:])
}

// ScrubText withholds the credentials inside a free text: the values of the keys this process holds (harden.Held), and what the
// redaction rules of package redact find (token formats, private keys, JWTs, passwords in URLs, bearer values, secret assignments).
// Each becomes Hidden; the rest of the text is unchanged.
func ScrubText(s string) string {
	if s == "" {
		return s
	}
	held := harden.Held()
	vals := make([]string, 0, len(held))
	for _, name := range held {
		if v, ok := harden.LookupSecret(name); ok && len(v) >= 8 {
			vals = append(vals, v)
		}
	}
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		s = strings.ReplaceAll(s, v, Hidden)
	}
	s = scrubber.String(s)
	s = redactedToken.ReplaceAllString(s, Hidden)
	s = userPassword.ReplaceAllString(s, "${1}:"+Hidden)
	return secretFlagValue.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretFlagValue.FindStringSubmatch(m)
		if strings.HasPrefix(sub[3], "-") || sub[3] == Hidden || strings.HasPrefix(sub[3], "(set") {
			return m // the next flag, or already withheld
		}
		return sub[1] + sub[2] + Hidden
	})
}
