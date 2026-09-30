package agentdefs

import (
	"fmt"
	"strings"

	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/skills/mdfile"
)

// toolClass says what a tool named in a definition can do.
type toolClass uint8

const (
	classOther toolClass = iota // not known to be harmless: treated as able to write
	classReadOnly
	classShell
	classWrite
	classWeb
	classMCP
)

// normTool is the spelling-insensitive form of a tool name: lower case without
// "_", "-" or spaces, so "web_fetch", "WebFetch" and "web-fetch" are one tool
// (the permission engine matches names the same way).
func normTool(s string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(s))
}

// toolName is a tool rule without its parenthesised pattern.
func toolName(entry string) string {
	name, _, _ := strings.Cut(entry, "(")
	return strings.TrimSpace(name)
}

var toolClasses = func() map[string]toolClass {
	m := map[string]toolClass{}
	for _, n := range []string{
		// Reading and searching.
		"read", "readfile", "view", "glob", "grep", "ls", "list", "listdir", "notebookread", "lsp", "recall",
		// Bookkeeping that touches no file: task lists, questions, plan mode, skills, coordination.
		"todowrite", "todoread", "taskcreate", "taskupdate", "taskget", "tasklist", "askuserquestion",
		"exitplanmode", "enterplanmode", "skill", "toolsearch", "mail", "note", "wait",
	} {
		m[n] = classReadOnly
	}
	for _, n := range []string{"bash", "shell", "sh", "exec", "run", "runcommand", "terminal", "cmd", "bashoutput", "bashkill", "killshell"} {
		m[n] = classShell
	}
	for _, n := range []string{
		"write", "writefile", "edit", "multiedit", "notebookedit", "applypatch", "patch", "create", "createfile",
		"delete", "remove", "move", "rename", "mkdir",
	} {
		m[n] = classWrite
	}
	for _, n := range []string{"webfetch", "websearch", "fetch", "httpget", "http", "urlfetch"} {
		m[n] = classWeb
	}
	return m
}()

func classOf(entry string) toolClass {
	n := normTool(toolName(entry))
	if strings.HasPrefix(n, "mcp") {
		return classMCP
	}
	if c, ok := toolClasses[n]; ok {
		return c
	}
	return classOther
}

// allReadOnly reports whether every tool of an allowlist is known to be
// harmless to the workspace. Web tools count: they read the network and write
// nothing. A tool this package has never heard of, an MCP tool and a delegation
// tool ("Task", "spawn") do not: their effects are unknown, and a role that may
// use one must count as a writer.
func allReadOnly(tools []string) bool {
	if len(tools) == 0 {
		return false
	}
	for _, t := range tools {
		switch classOf(t) {
		case classReadOnly, classWeb:
		default:
			return false
		}
	}
	return true
}

// Profile returns the runtime restriction the definition asks for, to register
// with the permission engine under Role.Name. It can only tighten: a profile
// adds denials and may choose a stricter mode, and the engine judges the role
// under both the session and the profile and keeps the more severe answer.
//
//   - A read-only role (or permissionMode: plan) gets plan mode.
//   - An explicit tool allowlist denies the classes it leaves out: shell
//     commands, file writes, web access and MCP tools.
//   - An entry that narrows a tool, either with a pattern ("Bash(git diff:*)",
//     "Edit(src/**)") or by naming one MCP tool, does not grant the whole class.
//     The role then runs in plan mode (nothing that writes or executes) with
//     exactly those entries, and the whole-tool entries that plan mode would
//     refuse, carved out as its own allow rules. The engine still never grants
//     beyond what the session permits, so "Bash(go vet:*)" means go vet under
//     the session's rules, and every other command is refused.
//   - disallowedTools are denied as written.
//
// Tools are never hidden: every agent sends the same tool list so that the
// provider caches it once for the swarm, and a restriction is a refusal at run
// time.
func (d Def) Profile() perm.RoleProfile {
	var p perm.RoleProfile
	if d.ReadOnly || d.PermissionMode == string(perm.ModePlan) {
		p.Mode = perm.ModePlan
	}
	if len(d.Tools) > 0 {
		var listed [classMCP + 1]bool // classes the allowlist mentions at all
		var carve []string            // entries that are exceptions to plan mode
		narrowed := false
		for _, t := range d.Tools {
			c := classOf(t)
			listed[c] = true
			narrow := strings.Contains(t, "(") || c == classMCP
			switch {
			case narrow:
				narrowed = true
				carve = append(carve, t)
			case c == classShell || c == classWrite || c == classOther:
				carve = append(carve, t) // a whole tool, granted where plan mode would refuse it
			}
		}
		if narrowed {
			p.Mode = perm.ModePlan
			p.Allow = append(p.Allow, carve...)
		}
		if !listed[classShell] {
			p.Deny = append(p.Deny, "Bash")
		}
		if !listed[classWrite] {
			p.Deny = append(p.Deny, "Edit")
		}
		if !listed[classWeb] {
			p.Deny = append(p.Deny, "WebFetch", "WebSearch")
		}
		if !listed[classMCP] {
			p.Deny = append(p.Deny, "mcp__*")
		}
	}
	p.Deny = append(p.Deny, d.DisallowedTools...)
	return p
}

// validShort reports whether s can be an agent-id prefix: 2 to 4 characters,
// lower-case letters and digits, starting with a letter.
func validShort(s string) bool {
	if len(s) < 2 || len(s) > maxShort || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// assignShorts gives every definition a unique agent-id prefix, in name order so
// that the result does not depend on discovery order. An explicit short is kept
// unless it collides; otherwise one is derived from the name: the initials of
// its words ("code-reviewer" gives "cr"), then its first two, three or four
// letters, then a digit appended.
func assignShorts(defs []Def, reserved []string) []Warning {
	used := map[string]bool{}
	for _, r := range reserved {
		used[strings.ToLower(r)] = true
	}
	var warns []Warning
	for i := range defs {
		s := defs[i].Short
		if s == "" {
			continue
		}
		if used[s] {
			warns = append(warns, mdfile.Warnf(defs[i].Path, defs[i].Name, "short %q is already in use; one is derived instead", s))
			defs[i].Short = ""
			continue
		}
		used[s] = true
	}
	for i := range defs {
		if defs[i].Short != "" {
			continue
		}
		defs[i].Short = deriveShort(defs[i].Name, used)
		used[defs[i].Short] = true
	}
	return warns
}

func deriveShort(name string, used map[string]bool) string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '-' || r == '_' || r == ':' }) {
		w = strings.TrimLeft(letters(w), "0123456789")
		if w != "" {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		words = []string{"ag"}
	}
	var cands []string
	if len(words) > 1 {
		var ini strings.Builder
		for _, w := range words {
			ini.WriteByte(w[0])
		}
		cands = append(cands, truncate(ini.String(), maxShort))
	}
	base := words[0]
	for n := 2; n <= maxShort; n++ {
		if len(base) >= n {
			cands = append(cands, base[:n])
		}
	}
	for _, c := range cands {
		if validShort(c) && !used[c] {
			return c
		}
	}
	stem := truncate(base, 2)
	if len(stem) < 2 {
		stem = "ag"
	}
	for i := 2; ; i++ {
		if c := fmt.Sprintf("%s%d", stem, i); validShort(c) && !used[c] {
			return c
		}
		if i > 99 {
			break
		}
	}
	for i := 0; ; i++ { // 26*26 two-letter prefixes are more than MaxDefs
		c := string([]byte{'a' + byte(i/26%26), 'a' + byte(i%26)})
		if !used[c] {
			return c
		}
	}
}

// letters keeps ASCII letters and digits.
func letters(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
