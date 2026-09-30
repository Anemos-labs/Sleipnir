package hooks

import (
	"fmt"
	"regexp"
	"strings"
)

// matcher decides whether a hook applies to an event. It follows Claude Code:
//
//   - empty or "*" matches everything;
//   - text made only of letters, digits, underscores and "|" is a list of exact
//     names ("Bash|Edit");
//   - anything else is a regular expression that may match anywhere in the name
//     ("^Notebook", "mcp__memory__.*"). The syntax is Go's RE2, which has no
//     lookaround or backreferences.
//
// Exact names ignore case and the separators "_" and "-", so the Claude Code
// spelling "WebFetch" matches Sleipnir's web_fetch tool.
type matcher struct {
	all   bool
	exact map[string]bool
	re    *regexp.Regexp
}

func compileMatcher(s string) (*matcher, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return &matcher{all: true}, nil
	}
	if simpleMatcher(s) {
		m := &matcher{exact: map[string]bool{}}
		for _, part := range strings.Split(s, "|") {
			if part != "" {
				m.exact[normTool(part)] = true
			}
		}
		if len(m.exact) == 0 {
			return &matcher{all: true}, nil
		}
		return m, nil
	}
	if len(s) > 512 {
		return nil, fmt.Errorf("matcher is %d characters long; the limit is 512", len(s))
	}
	re, err := regexp.Compile(s)
	if err != nil {
		return nil, fmt.Errorf("matcher %q is not a valid regular expression (RE2 syntax): %v", clip(s, 60), err)
	}
	return &matcher{re: re}, nil
}

func simpleMatcher(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '|') {
			return false
		}
	}
	return true
}

// matches reports whether any of the candidate names matches.
func (m *matcher) matches(candidates []string) bool {
	if m.all {
		return true
	}
	for _, c := range candidates {
		switch {
		case m.exact != nil:
			if m.exact[normTool(c)] {
				return true
			}
		case m.re != nil:
			if m.re.MatchString(c) {
				return true
			}
		}
	}
	return false
}

// normTool is the spelling-insensitive form of a tool name: lower case without
// "_" and "-" (the permission engine matches names the same way).
func normTool(s string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(s))
}

// toolNames maps Sleipnir's tool names to the names Claude Code hooks are
// written against. The first name is what a hook sees as tool_name; all of them
// are matched, so a hook for "Edit|Write" also fires for the patch tool, which
// edits and creates files, and a guard written for Claude Code does not fall
// silent because the tool has another name here.
var toolNames = map[string][]string{
	"bash":       {"Bash"},
	"bashoutput": {"BashOutput"},
	"bashkill":   {"KillShell", "KillBash"},
	"read":       {"Read"},
	"write":      {"Write"},
	"edit":       {"Edit", "MultiEdit"},
	"applypatch": {"apply_patch", "Edit", "Write", "MultiEdit"},
	"glob":       {"Glob"},
	"grep":       {"Grep"},
	"ls":         {"LS"},
	"webfetch":   {"WebFetch"},
	"websearch":  {"WebSearch"},
	"spawn":      {"spawn", "Task", "Agent"},
}

// claudeToolName is the tool_name a hook sees for a Sleipnir tool.
func claudeToolName(tool string) string {
	if names, ok := toolNames[normTool(tool)]; ok {
		return names[0]
	}
	return tool
}

// toolCandidates are the names a matcher is tried against for a tool: the tool's
// own name, plus its Claude Code spellings.
func toolCandidates(tool string) []string {
	out := []string{tool}
	if names, ok := toolNames[normTool(tool)]; ok {
		out = append(out, names...)
	}
	return out
}

// matchTarget returns the names a hook matcher is tried against on an event, and
// whether the event supports matchers at all. Events without a matcher field
// (UserPromptSubmit, Stop) run every hook, as in Claude Code.
func matchTarget(ev Event, name string) (candidates []string, supported bool) {
	str := func(key string) []string {
		if v, ok := ev.Extra[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return []string{s}
			}
		}
		return nil
	}
	switch name {
	case PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest:
		if ev.Tool == "" {
			return nil, true
		}
		return toolCandidates(ev.Tool), true
	case SubagentStart, SubagentStop:
		if ev.Role == "" {
			return nil, true
		}
		return []string{ev.Role}, true
	case SessionStart:
		return str("source"), true
	case SessionEnd:
		return str("reason"), true
	case Notification:
		return str("notification_type"), true
	case PreCompact, PostCompact:
		return str("trigger"), true
	}
	return nil, false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
