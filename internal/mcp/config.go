package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

// Transport types.
const (
	TypeStdio = "stdio" // a local child process speaking newline-delimited JSON-RPC
	TypeHTTP  = "http"  // streamable HTTP (2025-03-26 and later)
	TypeSSE   = "sse"   // the legacy HTTP+SSE transport (2024-11-05)
)

// Scope says where a server definition came from, which decides whether it
// may be started without asking.
type Scope string

const (
	// ScopeUser is the user's own configuration. Entries parsed with it are
	// trusted by default.
	ScopeUser Scope = "user"
	// ScopeProject is configuration that arrives with a repository (the
	// project's .sleipnir/config.json, a .mcp.json). The zero Scope is treated
	// the same way: not knowing where a definition came from is a reason to
	// distrust it, so the default fails closed.
	ScopeProject Scope = "project"
)

// ServerConfig configures one MCP server.
//
// JSON keys are snake_case ("allow_tools"); camelCase spellings and the usual
// aliases of other MCP clients are accepted when parsing (see ParseWith).
// Strings may contain ${VAR} references, expanded by Expand from an explicit
// map.
type ServerConfig struct {
	// Type is "stdio", "http" (streamable HTTP) or "sse" (legacy). When empty
	// it is inferred: a command means stdio, a url means http.
	Type string `json:"type,omitempty"`

	// stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"` // added to a minimal safe base; the harness environment is not inherited
	Cwd     string            `json:"cwd,omitempty"` // default: the manager's working directory

	// http and sse
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Timeout is the per-call timeout for tools/call (default: the manager's
	// CallTimeout). StartupTimeout bounds connect + initialize + first listing.
	// In JSON a number is seconds and a string is a Go duration ("90s").
	Timeout        time.Duration `json:"timeout,omitempty"`
	StartupTimeout time.Duration `json:"startup_timeout,omitempty"`

	// Disabled keeps the entry in the file but never starts it.
	Disabled bool `json:"disabled,omitempty"`

	// Trust lets the manager start this server without asking. Entries parsed
	// from user configuration get it by default; a project-scoped file cannot
	// vouch for itself, so ParseWith clears it there (with a warning). Code
	// that has asked the user may set it.
	Trust bool `json:"trust,omitempty"`

	// AllowPrivate lets an HTTP server live on a private, loopback or link-local
	// address, which the address guard otherwise refuses (SSRF). Like Trust it
	// is cleared when parsed from project scope.
	AllowPrivate bool `json:"allow_private,omitempty"`

	// AllowTools and DenyTools are globs ('*' and '?') on the server's own tool
	// names or on the exposed mcp__server__tool names. Deny beats allow; an
	// empty AllowTools allows everything not denied.
	AllowTools []string `json:"allow_tools,omitempty"`
	DenyTools  []string `json:"deny_tools,omitempty"`

	// MaxOutputChars lowers the model-visible size of this server's results
	// below the harness's global tool output limit (it can never raise it).
	MaxOutputChars int `json:"max_output_chars,omitempty"`

	// Scope is set by the parser from ParseOptions, never read from JSON.
	Scope Scope `json:"-"`
}

// Configuration limits: a hostile file must not be able to make the parser or
// the manager allocate without bound.
const (
	maxServers      = 256
	maxNameLen      = 128
	maxCommandLen   = 4096
	maxArgs         = 256
	maxArgLen       = 16 << 10
	maxEnvEntries   = 256
	maxHeaderCount  = 64
	maxValueLen     = 16 << 10
	maxURLLen       = 8 << 10
	maxGlobs        = 32
	maxGlobLen      = 96
	maxMaxOutput    = 16 << 20
	maxConfigTimout = 24 * time.Hour
)

// EffectiveType returns Type, inferring it when empty.
func (c ServerConfig) EffectiveType() string {
	if t := normalizeType(c.Type); t != "" {
		return t
	}
	switch {
	case c.Command != "" && c.URL == "":
		return TypeStdio
	case c.URL != "" && c.Command == "":
		return TypeHTTP
	}
	return ""
}

func normalizeType(t string) string {
	switch strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(t))) {
	case "stdio":
		return TypeStdio
	case "http", "streamablehttp", "streamable":
		return TypeHTTP
	case "sse":
		return TypeSSE
	}
	return ""
}

// Remote reports whether the server is reached over the network.
func (c ServerConfig) Remote() bool {
	t := c.EffectiveType()
	return t == TypeHTTP || t == TypeSSE
}

// Issue is one problem found while parsing or validating configuration. It
// names the server and the field; it never contains a value from a header or
// an environment variable, because configuration is where credentials live.
type Issue struct {
	Server  string
	Field   string
	Message string
	// Fatal issues exclude the server from the result; the rest are warnings
	// (ignored options, cleared privileges).
	Fatal bool
}

func (i Issue) Error() string {
	var b strings.Builder
	b.WriteString("mcp config: ")
	if i.Server != "" {
		b.WriteString("server " + strconv.Quote(i.Server))
		if i.Field != "" {
			b.WriteString(", field " + strconv.Quote(i.Field))
		}
		b.WriteString(": ")
	} else if i.Field != "" {
		b.WriteString("field " + strconv.Quote(i.Field) + ": ")
	}
	b.WriteString(i.Message)
	return b.String()
}

// ParseOptions tunes ParseWith.
type ParseOptions struct {
	// Scope is where the file came from. The zero value is treated as
	// ScopeProject (untrusted).
	Scope Scope
}

// Parse decodes server definitions in either of the two shapes in use:
//
//	Sleipnir:     {"github": {"command": "gh-mcp"}, "docs": {"url": "https://..."}}
//	Claude Code:  {"mcpServers": {"github": {"command": "gh-mcp"}}}   (.mcp.json)
//
// It treats its input as project-scoped and untrusted: use ParseWith with
// ScopeUser for the user's own configuration. Valid servers are returned even
// when others are broken; the error (a join of Issue values, so errors.As
// finds them) lists everything that made a server unusable. Warnings are
// dropped: use ParseWith to see them.
func Parse(raw map[string]json.RawMessage) (map[string]ServerConfig, error) {
	servers, issues := ParseWith(raw, ParseOptions{})
	var errs []error
	for _, is := range issues {
		if is.Fatal {
			errs = append(errs, is)
		}
	}
	return servers, errors.Join(errs...)
}

// ParseWith is Parse with options, returning every issue (warnings included)
// in a deterministic order.
func ParseWith(raw map[string]json.RawMessage, o ParseOptions) (map[string]ServerConfig, []Issue) {
	servers := map[string]ServerConfig{}
	var issues []Issue
	entries := raw

	if inner, ok := raw["mcpServers"]; ok {
		// A .mcp.json. Other top-level keys (VS Code's "inputs", tool-specific
		// extras) are not servers here; say so instead of guessing.
		for _, k := range sortedKeys(raw) {
			if k != "mcpServers" {
				issues = append(issues, Issue{Field: k, Message: "ignored: next to \"mcpServers\" only its entries are servers"})
			}
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(inner, &m); err != nil {
			return servers, append(issues, Issue{Field: "mcpServers", Message: "must be an object mapping server names to definitions", Fatal: true})
		}
		entries = m
	}

	names := sortedKeys(entries)
	if len(names) > maxServers {
		issues = append(issues, Issue{Message: fmt.Sprintf("%d servers configured; only the first %d (by name) are read", len(names), maxServers), Fatal: true})
		names = names[:maxServers]
	}
	for _, name := range names {
		rawEntry := bytes.TrimSpace(entries[name])
		if string(rawEntry) == "null" {
			continue // an explicit null removes an entry; nothing to report
		}
		if msg := badServerName(name); msg != "" {
			issues = append(issues, Issue{Server: clipForError(name), Message: msg, Fatal: true})
			continue
		}
		cfg, is := parseEntry(name, rawEntry, o.Scope)
		issues = append(issues, is...)
		if !hasFatal(is) {
			servers[name] = cfg
		}
	}
	return servers, issues
}

func hasFatal(is []Issue) bool {
	for _, i := range is {
		if i.Fatal {
			return true
		}
	}
	return false
}

func badServerName(name string) string {
	switch {
	case name == "" || strings.TrimSpace(name) != name:
		return "server names must be non-empty and have no leading or trailing spaces"
	case len(name) > maxNameLen:
		return fmt.Sprintf("server name is longer than %d bytes", maxNameLen)
	case cleanStrict(name) != name || strings.ContainsAny(name, "\n\t"):
		return "server name contains control or invisible characters"
	}
	return ""
}

// fieldAliases maps normalised spellings (lower case, no '_' or '-') to the
// canonical field. Other clients' names for the same setting are accepted so
// an existing .mcp.json or Gemini settings block works unchanged.
var fieldAliases = map[string]string{
	"type": "type", "transport": "type", "transporttype": "type",
	"command": "command", "args": "args", "env": "env",
	"cwd": "cwd", "workingdirectory": "cwd",
	"url": "url", "serverurl": "url", "httpurl": "url",
	"headers":        "headers",
	"timeout":        "timeout",
	"startuptimeout": "startup_timeout",
	"disabled":       "disabled", "enabled": "enabled",
	"trust":        "trust",
	"allowprivate": "allow_private",
	"allowtools":   "allow_tools", "includetools": "allow_tools",
	"denytools": "deny_tools", "excludetools": "deny_tools", "disabledtools": "deny_tools",
	"maxoutputchars": "max_output_chars",
}

// ignoredFields are options of other clients that have no equivalent here.
// They are reported, not rejected, so a shared file still loads. The two
// auto-approve spellings deserve the explicit note: they do NOT skip
// permission prompts here.
var ignoredFields = map[string]string{
	"alwaysallow": "ignored: permissions are decided per call by permission rules, not by the server entry",
	"autoapprove": "ignored: permissions are decided per call by permission rules, not by the server entry",
	"description": "ignored",
	"oauth":       "ignored: OAuth flows are not supported; pass a token in headers",
	"icon":        "ignored",
	"envfile":     "ignored: list variables in \"env\"",
	"sandbox":     "ignored",
	"dev":         "ignored",
	"$schema":     "ignored",
}

func normKey(k string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(k))
}

func parseEntry(name string, raw json.RawMessage, scope Scope) (ServerConfig, []Issue) {
	cfg := ServerConfig{Scope: scope}
	var issues []Issue
	fail := func(field, format string, args ...any) {
		issues = append(issues, Issue{Server: name, Field: field, Message: fmt.Sprintf(format, args...), Fatal: true})
	}
	warn := func(field, format string, args ...any) {
		issues = append(issues, Issue{Server: name, Field: field, Message: fmt.Sprintf(format, args...)})
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		fail("", "must be an object (for example {\"command\": \"...\"})")
		return cfg, issues
	}

	// Canonicalise keys so "allowTools", "allow_tools" and "includeTools" are one
	// field, and two spellings of the same one are an error rather than a race.
	canon := map[string]json.RawMessage{}
	spelled := map[string]string{}
	for _, k := range sortedKeys(fields) {
		nk := normKey(k)
		if note, ok := ignoredFields[nk]; ok {
			warn(clipForError(k), "%s", note)
			continue
		}
		c, ok := fieldAliases[nk]
		if !ok {
			fail(clipForError(k), "unknown field%s", didYouMean(nk))
			continue
		}
		if prev, dup := spelled[c]; dup {
			fail(clipForError(k), "sets the same option as %q", prev)
			continue
		}
		spelled[c] = clipForError(k)
		canon[c] = fields[k]
	}

	str := func(field string, dst *string, max int) {
		v, ok := canon[field]
		if !ok || isNull(v) {
			return
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			fail(field, "must be a string")
			return
		}
		if len(s) > max {
			fail(field, "is longer than %d bytes", max)
			return
		}
		*dst = s
	}
	flag := func(field string, dst *bool, set *bool) {
		v, ok := canon[field]
		if !ok || isNull(v) {
			return
		}
		if err := json.Unmarshal(v, dst); err != nil {
			fail(field, "must be true or false")
			return
		}
		if set != nil {
			*set = true
		}
	}

	str("type", &cfg.Type, 32)
	if cfg.Type != "" && normalizeType(cfg.Type) == "" {
		fail("type", "must be \"stdio\", \"http\" or \"sse\"")
	}
	str("command", &cfg.Command, maxCommandLen)
	str("cwd", &cfg.Cwd, maxCommandLen)
	str("url", &cfg.URL, maxURLLen)

	if v, ok := canon["args"]; ok && !isNull(v) {
		var args []string
		switch err := json.Unmarshal(v, &args); {
		case err != nil:
			fail("args", "must be an array of strings")
		case len(args) > maxArgs:
			fail("args", "has more than %d entries", maxArgs)
		default:
			for i, a := range args {
				if len(a) > maxArgLen {
					fail(fmt.Sprintf("args[%d]", i), "is longer than %d bytes", maxArgLen)
				}
			}
			cfg.Args = args
		}
	}
	cfg.Env = stringMap(canon, "env", maxEnvEntries, fail)
	cfg.Headers = stringMap(canon, "headers", maxHeaderCount, fail)

	for _, f := range []struct {
		field string
		dst   *time.Duration
	}{{"timeout", &cfg.Timeout}, {"startup_timeout", &cfg.StartupTimeout}} {
		if v, ok := canon[f.field]; ok && !isNull(v) {
			d, err := parseDuration(v)
			if err != nil {
				fail(f.field, "%v", err)
				continue
			}
			*f.dst = d
		}
	}

	var trustSet bool
	flag("disabled", &cfg.Disabled, nil)
	flag("trust", &cfg.Trust, &trustSet)
	flag("allow_private", &cfg.AllowPrivate, nil)
	if v, ok := canon["enabled"]; ok && !isNull(v) {
		var enabled bool
		if err := json.Unmarshal(v, &enabled); err != nil {
			fail("enabled", "must be true or false")
		} else if _, both := canon["disabled"]; both && cfg.Disabled == enabled {
			fail("enabled", "contradicts \"disabled\"")
		} else if !enabled {
			cfg.Disabled = true
		}
	}

	cfg.AllowTools = globList(canon, "allow_tools", fail)
	cfg.DenyTools = globList(canon, "deny_tools", fail)

	if v, ok := canon["max_output_chars"]; ok && !isNull(v) {
		var n int
		if err := json.Unmarshal(v, &n); err != nil || n < 0 || n > maxMaxOutput {
			fail("max_output_chars", "must be an integer between 0 and %d", maxMaxOutput)
		} else {
			cfg.MaxOutputChars = n
		}
	}

	// Structure: exactly one transport's fields.
	typ := normalizeType(cfg.Type)
	switch {
	case typ != "":
	case cfg.Command != "" && cfg.URL != "":
		fail("", "has both \"command\" and \"url\"; set \"type\" or remove one")
	case cfg.Command != "":
		typ = TypeStdio
	case cfg.URL != "":
		typ = TypeHTTP
	default:
		if !hasFatal(issues) {
			fail("", "needs \"command\" (a local server) or \"url\" (a remote one)")
		}
	}
	switch typ {
	case TypeStdio:
		if strings.TrimSpace(cfg.Command) == "" {
			fail("command", "is required for a stdio server")
		}
		if cfg.URL != "" {
			fail("url", "is not valid for a stdio server")
		}
		if len(cfg.Headers) > 0 {
			warn("headers", "ignored: only http and sse servers take headers")
		}
	case TypeHTTP, TypeSSE:
		if strings.TrimSpace(cfg.URL) == "" {
			fail("url", "is required for a %s server", typ)
		}
		if cfg.Command != "" {
			fail("command", "is not valid for a %s server", typ)
		}
		if len(cfg.Args) > 0 {
			fail("args", "is not valid for a %s server", typ)
		}
		if len(cfg.Env) > 0 {
			warn("env", "ignored: only stdio servers take environment variables")
		}
		if cfg.Cwd != "" {
			warn("cwd", "ignored: only stdio servers have a working directory")
		}
	}
	if typ != "" {
		cfg.Type = typ
	}

	// Privileges a definition must not grant itself.
	switch scope {
	case ScopeUser:
		if !trustSet {
			cfg.Trust = true
		}
	default:
		if cfg.Trust {
			warn("trust", "cleared: a project-scoped file cannot vouch for its own servers; approve the server instead")
		}
		cfg.Trust = false
		if cfg.AllowPrivate {
			warn("allow_private", "cleared: private addresses can only be enabled from user configuration or by the caller")
		}
		cfg.AllowPrivate = false
	}
	return cfg, issues
}

func isNull(v json.RawMessage) bool { return string(bytes.TrimSpace(v)) == "null" }

// stringMap decodes an object of scalar values. Numbers and booleans are
// accepted and rendered as text (environment values are strings anyway and
// hand-written files often write PORT: 8080).
func stringMap(canon map[string]json.RawMessage, field string, max int, fail func(field, format string, args ...any)) map[string]string {
	v, ok := canon[field]
	if !ok || isNull(v) {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(v, &m); err != nil {
		fail(field, "must be an object of string values")
		return nil
	}
	if len(m) > max {
		fail(field, "has more than %d entries", max)
		return nil
	}
	out := make(map[string]string, len(m))
	for _, k := range sortedKeys(m) {
		if k == "" || len(k) > 256 {
			fail(field, "has an empty or oversized key")
			continue
		}
		fk := field + "." + clipForError(k)
		var s string
		switch raw := bytes.TrimSpace(m[k]); {
		case string(raw) == "null":
			continue
		case len(raw) > 0 && raw[0] == '"':
			if err := json.Unmarshal(raw, &s); err != nil {
				fail(fk, "must be a string")
				continue
			}
		default:
			var scalar any
			if err := json.Unmarshal(raw, &scalar); err != nil {
				fail(fk, "must be a string")
				continue
			}
			switch scalar.(type) {
			case bool, float64:
				s = string(raw)
			default:
				fail(fk, "must be a string")
				continue
			}
		}
		if len(s) > maxValueLen {
			fail(fk, "is longer than %d bytes", maxValueLen)
			continue
		}
		out[k] = s
	}
	return out
}

func globList(canon map[string]json.RawMessage, field string, fail func(field, format string, args ...any)) []string {
	v, ok := canon[field]
	if !ok || isNull(v) {
		return nil
	}
	var list []string
	if err := json.Unmarshal(v, &list); err != nil {
		fail(field, "must be an array of glob strings")
		return nil
	}
	if len(list) > maxGlobs {
		fail(field, "has more than %d patterns", maxGlobs)
		return nil
	}
	for i, g := range list {
		if g == "" || len(g) > maxGlobLen {
			fail(fmt.Sprintf("%s[%d]", field, i), "must be 1 to %d bytes", maxGlobLen)
		}
	}
	return list
}

// parseDuration accepts a JSON number (seconds) or a string: a Go duration
// ("1500ms", "2m") or plain seconds ("30").
func parseDuration(raw json.RawMessage) (time.Duration, error) {
	raw = bytes.TrimSpace(raw)
	var d time.Duration
	switch {
	case len(raw) > 0 && raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, errors.New("must be a number of seconds or a duration such as \"90s\"")
		}
		s = strings.TrimSpace(s)
		if secs, err := strconv.ParseFloat(s, 64); err == nil {
			d = secondsToDuration(secs)
			break
		}
		pd, err := time.ParseDuration(s)
		if err != nil {
			return 0, errors.New("must be a number of seconds or a duration such as \"90s\"")
		}
		d = pd
	default:
		var secs float64
		if err := json.Unmarshal(raw, &secs); err != nil {
			return 0, errors.New("must be a number of seconds or a duration such as \"90s\"")
		}
		d = secondsToDuration(secs)
	}
	if d < 0 || d > maxConfigTimout {
		return 0, fmt.Errorf("must be between 0 and %s", maxConfigTimout)
	}
	return d, nil
}

func secondsToDuration(secs float64) time.Duration {
	if secs != secs || secs < 0 || secs > maxConfigTimout.Seconds() { // NaN, negative, absurd: caller range-checks
		return -1
	}
	return time.Duration(secs * float64(time.Second))
}

var knownFields = func() []string {
	seen := map[string]bool{}
	for _, c := range fieldAliases {
		seen[c] = true
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}()

// didYouMean suggests the closest known field for a typo, to make "unknown
// field" errors actionable: a mistyped "deny_tool" that is silently ignored
// would leave a server more exposed than its author intended.
func didYouMean(normalized string) string {
	best, bestD := "", 3
	for _, f := range knownFields {
		if d := editDistance(normalized, normKey(f)); d < bestD {
			best, bestD = f, d
		}
	}
	if best == "" {
		return " (known fields: " + strings.Join(knownFields, ", ") + ")"
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) > 64 || len(rb) > 64 {
		return 99
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Expand returns a copy with ${VAR} references resolved from env, and only
// from env. Keys are never expanded. Errors name the field and the variable,
// never a value.
func (c ServerConfig) Expand(env map[string]string) (ServerConfig, error) {
	out := c
	var err error
	ex := func(field, s string) string {
		if err != nil || s == "" {
			return s
		}
		v, e := expandString(s, env)
		if e != nil {
			err = fmt.Errorf("%s: %w", field, e)
			return s
		}
		return v
	}
	out.Command = ex("command", c.Command)
	out.Cwd = ex("cwd", c.Cwd)
	out.URL = ex("url", c.URL)
	if c.Args != nil {
		out.Args = make([]string, len(c.Args))
		for i, a := range c.Args {
			out.Args[i] = ex(fmt.Sprintf("args[%d]", i), a)
		}
	}
	if c.Env != nil {
		out.Env = make(map[string]string, len(c.Env))
		for _, k := range sortedKeys(c.Env) {
			out.Env[k] = ex("env."+k, c.Env[k])
		}
	}
	if c.Headers != nil {
		out.Headers = make(map[string]string, len(c.Headers))
		for _, k := range sortedKeys(c.Headers) {
			out.Headers[k] = ex("headers."+k, c.Headers[k])
		}
	}
	if err != nil {
		return c, err
	}
	return out, nil
}

// EnvRefs lists, sorted, the variables the entry's strings reference, so an
// approval prompt can say "this server wants $GITHUB_TOKEN".
func (c ServerConfig) EnvRefs() []string {
	var refs []string
	add := func(s string) { refs = append(refs, refsIn(s)...) }
	add(c.Command)
	add(c.Cwd)
	add(c.URL)
	for _, a := range c.Args {
		add(a)
	}
	for _, v := range c.Env {
		add(v)
	}
	for _, v := range c.Headers {
		add(v)
	}
	return sortedUnique(refs)
}

// Validate checks an expanded configuration. It is separate from parsing
// because ${VAR} may supply pieces (a host, a port) that only make sense
// once expanded.
func (c ServerConfig) Validate() error {
	typ := c.EffectiveType()
	switch typ {
	case TypeStdio:
		if strings.TrimSpace(c.Command) == "" {
			return errors.New("command: is empty")
		}
		if strings.ContainsRune(c.Command, 0) {
			return errors.New("command: contains a NUL byte")
		}
		for i, a := range c.Args {
			if strings.ContainsRune(a, 0) {
				return fmt.Errorf("args[%d]: contains a NUL byte", i)
			}
		}
		for _, k := range sortedKeys(c.Env) {
			if !validEnvName(k) {
				return fmt.Errorf("env.%s: not a valid variable name", clipForError(k))
			}
			if strings.ContainsRune(c.Env[k], 0) {
				return fmt.Errorf("env.%s: value contains a NUL byte", k)
			}
		}
		if strings.ContainsRune(c.Cwd, 0) {
			return errors.New("cwd: contains a NUL byte")
		}
	case TypeHTTP, TypeSSE:
		if _, err := parseServerURL(c.URL); err != nil {
			return fmt.Errorf("url: %w", err)
		}
		for _, k := range sortedKeys(c.Headers) {
			if err := validHeader(k, c.Headers[k]); err != nil {
				return fmt.Errorf("headers.%s: %w", clipForError(k), err)
			}
		}
	default:
		return errors.New("type: cannot tell whether this is a stdio, http or sse server")
	}
	if c.Timeout < 0 || c.StartupTimeout < 0 {
		return errors.New("timeout: must not be negative")
	}
	if c.MaxOutputChars < 0 {
		return errors.New("max_output_chars: must not be negative")
	}
	return nil
}

func validEnvName(k string) bool {
	return k != "" && len(k) <= 256 && !strings.ContainsAny(k, "=\x00")
}

// parseServerURL parses and vets the URL of a remote server without
// including any part of it in the error text (it may carry a token in its
// path or query).
func parseServerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil:
		return nil, errors.New("is not a valid URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, errors.New("must start with http:// or https://")
	case u.Hostname() == "":
		return nil, errors.New("has no host")
	case u.User != nil:
		return nil, errors.New("must not contain credentials; put them in headers")
	case u.Opaque != "":
		return nil, errors.New("is not a valid URL")
	}
	return u, nil
}

// reservedHeaders are set by the transport itself or are hop-by-hop; letting
// configuration override them would break framing or let a definition steer
// session handling.
var reservedHeaders = map[string]bool{
	"host": true, "content-length": true, "content-type": true, "accept": true,
	"transfer-encoding": true, "connection": true, "keep-alive": true, "upgrade": true,
	"te": true, "trailer": true, "proxy-authorization": true, "proxy-authenticate": true,
	"mcp-session-id": true, "mcp-protocol-version": true, "last-event-id": true,
}

func validHeader(name, value string) error {
	if name == "" || len(name) > 256 {
		return errors.New("invalid header name")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !ok {
			return errors.New("invalid header name")
		}
	}
	if reservedHeaders[strings.ToLower(name)] {
		return errors.New("this header is managed by the transport and cannot be configured")
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\r' || c == '\n' || c == 0 || c < 0x20 && c != '\t' || c == 0x7f {
			return errors.New("value contains a control character")
		}
	}
	return nil
}

// Redacted returns a copy that is safe to log or show: environment and header
// values are masked, a remote URL is reduced to scheme and host. Args are kept
// as written, because an approval prompt needs to show what will run; do not
// put secrets in args.
func (c ServerConfig) Redacted() ServerConfig {
	out := c
	if c.Env != nil {
		out.Env = make(map[string]string, len(c.Env))
		for k := range c.Env {
			out.Env[k] = "***"
		}
	}
	if c.Headers != nil {
		out.Headers = make(map[string]string, len(c.Headers))
		for k := range c.Headers {
			out.Headers[k] = "***"
		}
	}
	if c.URL != "" {
		out.URL = redactURL(c.URL)
	}
	return out
}

// redactURL keeps scheme and host and drops everything a token could hide in:
// userinfo, path, query, fragment. Servers such as hosted MCP gateways embed
// secrets in the path.
func redactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		if strings.Contains(raw, "${") { // an unexpanded template is safe to show
			return raw
		}
		return "(invalid url)"
	}
	out := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		out += "/..."
	}
	return out
}

// String describes the entry without secrets: what kind, what to run or where
// to connect (scheme and host only), and the names (not values) of env
// variables and headers.
func (c ServerConfig) String() string {
	var b strings.Builder
	typ := c.EffectiveType()
	if typ == "" {
		typ = "invalid"
	}
	b.WriteString(typ + "(")
	if typ == TypeStdio {
		b.WriteString("command=" + strconv.Quote(clipForError(c.Command)))
		if len(c.Args) > 0 {
			b.WriteString(" args=" + strconv.Itoa(len(c.Args)))
		}
		if len(c.Env) > 0 {
			b.WriteString(" env=[" + strings.Join(sortedKeys(c.Env), ",") + "]")
		}
	} else {
		b.WriteString("url=" + strconv.Quote(redactURL(c.URL)))
		if len(c.Headers) > 0 {
			b.WriteString(" headers=[" + strings.Join(sortedKeys(c.Headers), ",") + "]")
		}
	}
	if c.Scope != "" {
		b.WriteString(" scope=" + string(c.Scope))
	}
	if c.Trust {
		b.WriteString(" trusted")
	}
	b.WriteString(")")
	return b.String()
}

// GoString keeps %#v from printing secrets: a struct dump is exactly what
// ends up in a bug report.
func (c ServerConfig) GoString() string { return "mcp.ServerConfig" + c.String() }

// Fingerprint is a stable digest of everything that decides what running or
// contacting this entry does: transport, command, args, env, cwd, url and
// headers (as written, before expansion), and AllowPrivate. A caller that
// remembers approvals should key them on it, so an edit to a previously
// approved entry (a new commit changing the command) asks again.
func (c ServerConfig) Fingerprint() string {
	b, _ := core.MarshalStable(struct {
		Type         string            `json:"type"`
		Command      string            `json:"command"`
		Args         []string          `json:"args"`
		Env          map[string]string `json:"env"`
		Cwd          string            `json:"cwd"`
		URL          string            `json:"url"`
		Headers      map[string]string `json:"headers"`
		AllowPrivate bool              `json:"allow_private"`
	}{c.EffectiveType(), c.Command, c.Args, c.Env, c.Cwd, c.URL, c.Headers, c.AllowPrivate})
	return string(core.HashBytes(b))
}

// MarshalJSON writes durations as strings ("30s") so Parse reads back what
// was written; encoding/json would emit nanoseconds.
func (c ServerConfig) MarshalJSON() ([]byte, error) {
	type plain ServerConfig
	dur := func(d time.Duration) string {
		if d == 0 {
			return ""
		}
		return d.String()
	}
	return json.Marshal(struct {
		plain
		Timeout        string `json:"timeout,omitempty"`
		StartupTimeout string `json:"startup_timeout,omitempty"`
	}{plain(c), dur(c.Timeout), dur(c.StartupTimeout)})
}

// secrets lists the values of this (expanded) entry that must never appear in
// text leaving the package.
func (c ServerConfig) secrets() []string {
	var out []string
	for _, v := range c.Env {
		out = append(out, v)
	}
	for _, v := range c.Headers {
		out = append(out, v)
		// "Bearer <token>": the token alone is what leaks when a server echoes it.
		if i := strings.LastIndexByte(v, ' '); i >= 0 {
			out = append(out, v[i+1:])
		}
	}
	if c.URL != "" {
		out = append(out, c.URL)
		if u, err := url.Parse(c.URL); err == nil {
			if u.RawQuery != "" {
				out = append(out, u.RawQuery)
			}
			if u.User != nil {
				if p, ok := u.User.Password(); ok {
					out = append(out, p)
				}
			}
			for _, seg := range strings.Split(u.Path, "/") {
				if len(seg) >= 16 { // long path segments look like embedded tokens
					out = append(out, seg)
				}
			}
		}
	}
	return out
}
