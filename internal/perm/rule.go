package perm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
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

// urlHost extracts the host a web request is aimed at from its Input.
func urlHost(r Request) string {
	if len(r.Input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(r.Input, &m) != nil {
		return ""
	}
	for _, k := range []string{"url", "uri", "href", "endpoint"} {
		s, _ := m[k].(string)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "://") {
			s = "//" + s
		}
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		}
	}
	return ""
}

func requestURL(r Request) string {
	var m map[string]any
	if len(r.Input) == 0 || json.Unmarshal(r.Input, &m) != nil {
		return ""
	}
	for _, k := range []string{"url", "uri", "href", "endpoint"} {
		if s, _ := m[k].(string); s != "" {
			return s
		}
	}
	return ""
}

// matchWeb reports whether a classWeb rule covers the request.
func (c *crule) matchWeb(r Request) bool {
	if c.class != classWeb || !c.nameMatches(r.Tool) {
		return false
	}
	switch {
	case c.blanket:
		return true
	case c.domain != "":
		host := urlHost(r)
		if host == "" {
			return false
		}
		if strings.HasPrefix(c.domain, "*.") {
			return strings.HasSuffix(host, c.domain[1:])
		}
		return host == c.domain || strings.HasSuffix(host, "."+c.domain)
	default:
		u := requestURL(r)
		return u != "" && wildMatch(c.urlPat, u)
	}
}

// matchOther reports whether a classOther rule covers the request by name.
func (c *crule) matchOther(r Request) bool {
	if c.class != classOther || !c.nameMatches(r.Tool) {
		return false
	}
	return c.blanket || wildMatch(c.wild, r.Summary)
}
