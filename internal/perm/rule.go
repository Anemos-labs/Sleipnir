package perm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
	"github.com/anemos-labs/sleipnir/internal/weburl"
)

// Action is what a rule does when it matches.
type Action string

const (
	Allow Action = "allow"
	Ask   Action = "ask"
	Deny  Action = "deny"
)

// Rule is one permission rule: an Action for a Tool, optionally narrowed by a
// Pattern. The textual form is Tool or Tool(Pattern):
//
//	Bash(git status:*)          prefix match on the parsed command; ":*" = any args
//	Bash(npm run test)          exact match on the parsed command
//	Bash(*) or Bash             any shell command
//	Edit(src/**)                writes to paths matching a gitignore-like glob
//	Read(~/.ssh/**)             reads of such paths (by any tool, shell included)
//	WebFetch(domain:example.com) requests to a host (and its subdomains)
//	Read                        every read;  mcp__srv__tool  a tool by name
//
// Read and Edit (also Write, MultiEdit, ...) are rules about *access kinds*,
// not about a tool name: Read(p) covers a file read by the file tools and by
// "cat p" alike, and Edit(p) covers a write by the edit tools and by "> p".
type Rule struct {
	Action  Action
	Tool    string
	Pattern string
}

// String returns the textual form ParseRule accepts.
func (r Rule) String() string {
	if r.Pattern == "" {
		return r.Tool
	}
	return r.Tool + "(" + r.Pattern + ")"
}

// ParseRule parses "Tool" or "Tool(pattern)". The pattern is everything between
// the first "(" and the final ")", so it may itself contain parentheses.
func ParseRule(action Action, s string) (Rule, error) {
	switch action {
	case Allow, Ask, Deny:
	default:
		return Rule{}, fmt.Errorf("perm: unknown action %q", action)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return Rule{}, errors.New("perm: empty rule")
	}
	tool, pattern := s, ""
	if i := strings.IndexByte(s, '('); i >= 0 {
		if !strings.HasSuffix(s, ")") {
			return Rule{}, fmt.Errorf("perm: rule %q: missing closing parenthesis", s)
		}
		tool = strings.TrimSpace(s[:i])
		pattern = strings.TrimSpace(s[i+1 : len(s)-1])
	}
	if tool == "" {
		return Rule{}, fmt.Errorf("perm: rule %q: missing tool name", s)
	}
	for _, c := range tool {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-.*:", c)) {
			return Rule{}, fmt.Errorf("perm: rule %q: invalid character %q in tool name", s, c)
		}
	}
	if strings.ContainsAny(pattern, "\x00\n") {
		return Rule{}, fmt.Errorf("perm: rule %q: control character in pattern", s)
	}
	r := Rule{Action: action, Tool: tool, Pattern: pattern}
	if _, err := compileRule(r, nil); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// toolClass says what kind of thing a tool name refers to.
type toolClass int

const (
	classOther toolClass = iota // matched by name (MCP tools, Task, ...)
	classRead                   // Read, Glob, Grep, ...: rules about read access
	classWrite                  // Edit, Write, MultiEdit, ...: rules about write access
	classBash                   // Bash and friends: rules about shell commands
	classWeb                    // WebFetch, WebSearch: rules about URLs
)

var toolClasses = map[string]toolClass{
	"read": classRead, "readfile": classRead, "view": classRead, "glob": classRead,
	"grep": classRead, "ls": classRead, "list": classRead, "listdir": classRead,
	"edit": classWrite, "write": classWrite, "writefile": classWrite, "multiedit": classWrite,
	"notebookedit": classWrite, "patch": classWrite, "applypatch": classWrite,
	"create": classWrite, "createfile": classWrite, "delete": classWrite,
	"remove": classWrite, "move": classWrite, "rename": classWrite, "mkdir": classWrite,
	"bash": classBash, "shell": classBash, "sh": classBash, "exec": classBash,
	"run": classBash, "runcommand": classBash, "terminal": classBash, "cmd": classBash,
	"webfetch": classWeb, "fetch": classWeb, "websearch": classWeb, "httpget": classWeb,
	"http": classWeb, "urlfetch": classWeb,
}

// normTool lowercases a tool name and drops separators so that "web_fetch",
// "WebFetch" and "web-fetch" are one tool.
func normTool(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(s)
}

// classOf normalizes a tool name before looking up its permission class.
func classOf(tool string) toolClass { return toolClasses[normTool(tool)] }

// crule is a Rule ready to match.
type crule struct {
	Rule
	class   toolClass
	name    string // normalised tool name (classOther)
	blanket bool   // no pattern, or a pattern that matches everything

	globs []*pathGlob // classRead / classWrite: lexical and symlink-resolved forms

	argv    []string // classBash: prefix or exact tokens
	anyArgs bool

	domain string // classWeb: "domain:" rules
	urlPat string // classWeb: other patterns are wildcards over the URL
	wild   string // classOther: wildcard over Request.Summary

	origin  string // where the rule came from (Classification.Origin)
	runtime bool   // added while the session ran (AddRule): RemoveRule may remove it
}

// compileRule prepares r for matching. res may be nil (the pattern is then
// only validated and compiled in lexical form).
func compileRule(r Rule, res *resolver) (*crule, error) {
	c := &crule{Rule: r, class: classOf(r.Tool), name: normTool(r.Tool)}
	p := r.Pattern
	switch c.class {
	case classRead, classWrite:
		if p == "" || p == "*" || p == "**" || p == "**/*" {
			c.blanket = true
			return c, nil
		}
		gs, err := compilePathGlobs(p, r.Action, res)
		if err != nil {
			return nil, fmt.Errorf("perm: rule %s: %w", r, err)
		}
		c.globs = gs
	case classBash:
		if p == "" || p == "*" || p == ":*" {
			c.blanket = true
			return c, nil
		}
		switch {
		case strings.HasSuffix(p, ":*"):
			p, c.anyArgs = strings.TrimSpace(p[:len(p)-2]), true
		case strings.HasSuffix(p, " *"):
			p, c.anyArgs = strings.TrimSpace(p[:len(p)-2]), true
		}
		toks, ok := shellparse.Fields(p)
		if !ok || len(toks) == 0 {
			return nil, fmt.Errorf("perm: rule %s: cannot tokenise command pattern", r)
		}
		c.argv = toks
	case classWeb:
		switch {
		case p == "" || p == "*":
			c.blanket = true
		case strings.HasPrefix(p, "domain:"):
			c.domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(p, "domain:")), "."))
			if rest, wild := strings.CutPrefix(c.domain, "*."); wild {
				if a, ok := weburl.ASCIIName(rest); ok {
					c.domain = "*." + a
				}
			} else if a, ok := weburl.ASCIIName(c.domain); ok {
				c.domain = a
			}
			if c.domain == "" {
				return nil, fmt.Errorf("perm: rule %s: empty domain", r)
			}
		default:
			c.urlPat = p
		}
	default:
		if p == "" || p == "*" {
			c.blanket = true
		} else {
			c.wild = p
		}
	}
	return c, nil
}

// wildMatch reports whether s matches pattern, where "*" matches any run of
// characters (including none and including "/").
func wildMatch(pattern, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		switch {
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, sx
			px++
		case px < len(pattern) && pattern[px] == s[sx]:
			px++
			sx++
		case star >= 0:
			px = star + 1
			mark++
			sx = mark
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// canonTool folds tool-name aliases that mean the same thing.
func canonTool(n string) string {
	switch n {
	case "fetch", "httpget", "http", "urlfetch":
		return "webfetch"
	}
	return n
}

// nameMatches reports whether the rule's tool name covers the request's tool
// (classOther and classWeb rules; the name may hold "*" wildcards).
func (c *crule) nameMatches(tool string) bool {
	n := normTool(tool)
	return wildMatch(c.name, n) || canonTool(c.name) == canonTool(n)
}

// matchArgv reports whether argv satisfies a bash rule's tokens.
func (c *crule) matchArgv(argv []string) bool {
	if c.blanket {
		return true
	}
	if len(argv) < len(c.argv) || (!c.anyArgs && len(argv) != len(c.argv)) {
		return false
	}
	for i, t := range c.argv {
		if !wildMatch(t, argv[i]) {
			return false
		}
	}
	return true
}

// dynamicWord reports whether a word still holds shell syntax that the parser
// did not resolve (variables, substitutions, globs, tildes, braces).
func dynamicWord(w string) bool {
	return strings.ContainsAny(w, "$`*?[{")
}

// dynamicProgram is dynamicWord for a command name; the test builtins "[" and
// "[[" are literal names, not glob characters.
func dynamicProgram(w string) bool {
	return w != "[" && w != "[[" && dynamicWord(w)
}

// commandForms returns the argument vectors a bash rule is tried against. An
// allow rule sees only the command as written (Program must match exactly: a
// workspace script called ./git must not pass for git). Deny and ask rules are
// deliberately greedy: they also see through sudo and wrappers and ignore the
// directory part of the program, so obfuscation cannot dodge them.
func commandForms(s shellparse.Simple, restrict bool) [][]string {
	base := append([]string{s.Program}, s.Args...)
	if !restrict {
		if dynamicProgram(s.Program) {
			return nil
		}
		return [][]string{base}
	}
	forms := [][]string{base}
	withBase := func(argv []string) []string {
		out := append([]string(nil), argv...)
		if i := strings.LastIndexByte(out[0], '/'); i >= 0 && i+1 < len(out[0]) {
			out[0] = out[0][i+1:]
		}
		return out
	}
	forms = append(forms, withBase(base))
	if len(s.Wrappers) > 0 {
		forms = append(forms, append(append([]string(nil), s.Wrappers...), base...))
	}
	if s.Elevated() {
		if p, a := s.EffectiveCommand(); p != "" {
			eff := append([]string{p}, a...)
			forms = append(forms, eff, withBase(eff))
		}
	}
	return forms
}

// matchCommand reports whether the bash rule covers a simple command.
func (c *crule) matchCommand(s shellparse.Simple, restrict bool) bool {
	if c.class != classBash {
		return false
	}
	if c.blanket {
		return true
	}
	for _, f := range commandForms(s, restrict) {
		if c.matchArgv(f) {
			return true
		}
	}
	return false
}

// webTarget is the URL a web request goes to, read as the tool that makes it reads it.
// For a fetch tool (web_fetch and the names rules use for it) that is the "url" field
// alone, normalised by weburl.Parse, the tool's own parser: a rule about a host is then
// judged against exactly what is fetched. raw is that field as given, without the white
// space around it. takesURL is true for such a tool; u is nil when its "url" is missing
// or is not one the tool would fetch. A search tool takes no URL. For any other tool (an
// MCP tool that reaches the network) the URL-like fields of its input are read, and only
// a URL they all agree on counts: two fields that name different hosts name none.
func webTarget(r Request) (u *url.URL, raw string, takesURL bool) {
	var m map[string]json.RawMessage
	if len(r.Input) == 0 || json.Unmarshal(r.Input, &m) != nil {
		m = nil
	}
	str := func(k string) (string, bool) {
		var s string
		field, ok := m[k]
		if !ok {
			return "", false
		}
		err := json.Unmarshal(field, &s)
		return s, err == nil
	}
	switch canonTool(normTool(r.Tool)) {
	case "webfetch":
		s, ok := str("url")
		if !ok {
			return nil, "", true
		}
		parsed, err := weburl.Parse(s)
		if err != nil {
			return nil, "", true
		}
		return parsed, strings.TrimSpace(s), true
	case "websearch":
		return nil, "", false
	}
	var found *url.URL
	for _, k := range []string{"url", "uri", "href", "endpoint"} {
		s, ok := str(k)
		if !ok || s == "" {
			continue
		}
		parsed, err := weburl.Parse(s)
		if err != nil {
			return nil, "", false
		}
		if found != nil && weburl.Canonical(found) != weburl.Canonical(parsed) {
			return nil, "", false
		}
		found, raw = parsed, strings.TrimSpace(s)
	}
	return found, raw, false
}

// urlHost is the host a web request goes to (webTarget), as rules name hosts; "" when
// there is none.
func urlHost(r Request) string {
	if u, _, _ := webTarget(r); u != nil {
		return weburl.Host(u)
	}
	return ""
}

// urlForms are the spellings of a URL a rule about URLs is matched against: the value
// as the model gave it, the URL as the tool fetches it (weburl.Canonical), that without
// the "/" of an empty path, and each of these without its scheme. A pattern written in
// any of the forms rules have been written in ("https://docs.example",
// "docs.example/*") matches the URL the tool fetches.
func urlForms(u *url.URL, raw string) []string {
	canon := weburl.Canonical(u)
	forms := []string{raw, canon}
	if u.RawQuery == "" && (u.Path == "" || u.Path == "/") {
		forms = append(forms, strings.TrimSuffix(canon, "/"))
	}
	for _, f := range forms[1:] {
		if _, rest, ok := strings.Cut(f, "://"); ok {
			forms = append(forms, rest)
		}
	}
	return forms
}

// matchWeb reports whether a classWeb rule covers the request. A request of a fetch tool
// whose URL the tool would not fetch matches no allow rule (it is asked about) and every
// deny and ask rule that is about hosts or URLs (restrict). A domain is compared in its
// ASCII (punycode) form.
func (c *crule) matchWeb(r Request, restrict bool) bool {
	if c.class != classWeb || !c.nameMatches(r.Tool) {
		return false
	}
	if c.blanket {
		return true
	}
	u, raw, takesURL := webTarget(r)
	if u == nil {
		return restrict && takesURL
	}
	return c.matchURL(u, raw, restrict)
}

// matchURL is matchWeb for one URL the tool would fetch (raw is the field it came from).
// A host whose ASCII form cannot be made matches a domain only for a deny or ask rule.
func (c *crule) matchURL(u *url.URL, raw string, restrict bool) bool {
	if c.domain != "" {
		host, valid := weburl.HostChecked(u)
		return (valid || restrict) && c.domainHas(host)
	}
	for _, f := range urlForms(u, raw) {
		if f != "" && wildMatch(c.urlPat, f) {
			return true
		}
	}
	return false
}

// domainHas reports whether host (lower case, without a trailing dot) is the rule's
// domain or under it.
func (c *crule) domainHas(host string) bool {
	if strings.HasPrefix(c.domain, "*.") {
		return strings.HasSuffix(host, c.domain[1:])
	}
	return host == c.domain || strings.HasSuffix(host, "."+c.domain)
}

// matchOther reports whether a classOther rule covers the request by name.
func (c *crule) matchOther(r Request) bool {
	if c.class != classOther || !c.nameMatches(r.Tool) {
		return false
	}
	return c.blanket || wildMatch(c.wild, r.Summary)
}
