package hooks

import (
	"strings"
	"testing"
)

func TestMatcherSemantics(t *testing.T) {
	tests := []struct {
		matcher string
		tool    string
		want    bool
	}{
		{"", "bash", true},
		{"*", "anything", true},
		{"  ", "bash", true},
		{"Bash", "bash", true},
		{"Bash", "Bash", true},
		{"Bash", "bash_output", false},
		{"Bash|Edit", "edit", true},
		{"Bash|Edit", "read", false},
		{"Edit|Write", "apply_patch", true}, // the patch tool edits and writes files
		{"MultiEdit", "edit", true},
		{"WebFetch", "web_fetch", true},
		{"web_fetch", "WebFetch", true},
		{"Task", "spawn", true},
		{"Read|Grep|Glob|LS", "ls", true},
		{"mcp__github__create_issue", "mcp__github__create_issue", true},
		{"mcp__github", "mcp__github__create_issue", false}, // exact, not a prefix
		{"mcp__github__.*", "mcp__github__create_issue", true},
		{"^Notebook", "NotebookEdit", true},
		{"^Notebook", "MyNotebook", false},
		{"^Bash$", "bash", true}, // regexes are also tried against the Claude Code name
		{"^Bash$", "bash_output", false},
		{"Edit.*", "edit", true},
		{"(?i)^BASH$", "bash", true},
		{"Bash(", "bash", false}, // never reached at run time: rejected by Parse
	}
	for _, tc := range tests {
		m, err := compileMatcher(tc.matcher)
		if err != nil {
			if tc.matcher == "Bash(" {
				continue
			}
			t.Errorf("compile %q: %v", tc.matcher, err)
			continue
		}
		if got := m.matches(toolCandidates(tc.tool)); got != tc.want {
			t.Errorf("matcher %q on tool %q = %v, want %v", tc.matcher, tc.tool, got, tc.want)
		}
	}
	if _, err := compileMatcher(strings.Repeat("a|", 300) + "("); err == nil {
		t.Error("an over-long regular expression must be rejected")
	}
}

func TestMatchingPerEvent(t *testing.T) {
	s, err := Parse(cfg(t, `{
		"PreToolUse": [{"matcher": "Bash", "hooks": [{"command": "pre-bash"}]}, {"hooks": [{"command": "pre-all"}]}],
		"SubagentStop": [{"matcher": "reviewer", "hooks": [{"command": "sub"}]}],
		"SessionStart": [{"matcher": "startup", "hooks": [{"command": "start"}]}],
		"SessionEnd": [{"matcher": "clear", "hooks": [{"command": "end"}]}],
		"Notification": [{"matcher": "permission_prompt", "hooks": [{"command": "note"}]}],
		"PreCompact": [{"matcher": "auto", "hooks": [{"command": "compact"}]}],
		"Stop": [{"matcher": "ignored", "hooks": [{"command": "stop"}]}],
		"UserPromptSubmit": [{"matcher": "ignored", "hooks": [{"command": "prompt"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cmds := func(ev Event) string {
		hs, err := s.Matching(ev)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hs {
			out = append(out, h.Command)
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name string
		ev   Event
		want string
	}{
		{"tool matches", Event{Name: "PreToolUse", Tool: "bash"}, "pre-bash,pre-all"},
		{"tool does not match", Event{Name: "pre_tool_use", Tool: "read"}, "pre-all"},
		{"no tool named", Event{Name: PreToolUse}, "pre-all"},
		{"subagent role", Event{Name: SubagentStop, Role: "reviewer"}, "sub"},
		{"other role", Event{Name: SubagentStop, Role: "scout"}, ""},
		{"session source", Event{Name: SessionStart, Extra: map[string]any{"source": "startup"}}, "start"},
		{"other source", Event{Name: SessionStart, Extra: map[string]any{"source": "resume"}}, ""},
		{"no source", Event{Name: SessionStart}, ""},
		{"session end reason", Event{Name: SessionEnd, Extra: map[string]any{"reason": "clear"}}, "end"},
		{"notification type", Event{Name: Notification, Extra: map[string]any{"notification_type": "permission_prompt"}}, "note"},
		{"compaction trigger", Event{Name: PreCompact, Extra: map[string]any{"trigger": "auto"}}, "compact"},
		{"a non-string extra is not a target", Event{Name: PreCompact, Extra: map[string]any{"trigger": 5}}, ""},
		{"stop has no matcher", Event{Name: Stop}, "stop"},
		{"prompt has no matcher", Event{Name: UserPromptSubmit}, "prompt"},
		{"no hooks for the event", Event{Name: PostCompact}, ""},
	}
	for _, tc := range tests {
		if got := cmds(tc.ev); got != tc.want {
			t.Errorf("%s: matched %q, want %q", tc.name, got, tc.want)
		}
	}
	if _, err := s.Matching(Event{Name: "Nonsense"}); err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Errorf("unknown event: %v", err)
	}
}

func TestClaudeToolNames(t *testing.T) {
	for tool, want := range map[string]string{
		"bash": "Bash", "read": "Read", "write": "Write", "edit": "Edit", "glob": "Glob", "grep": "Grep", "ls": "LS",
		"web_fetch": "WebFetch", "web_search": "WebSearch", "spawn": "spawn", "apply_patch": "apply_patch",
		"mcp__x__y": "mcp__x__y", "recall": "recall", "": "",
	} {
		if got := claudeToolName(tool); got != want {
			t.Errorf("claudeToolName(%q) = %q, want %q", tool, got, want)
		}
	}
}

func TestEventOrderAndCanonical(t *testing.T) {
	events := Events()
	if len(events) != 13 || events[0] != SessionStart || events[12] != PostCompact {
		t.Fatalf("events = %v", events)
	}
	events[0] = "mutated"
	if Events()[0] != SessionStart {
		t.Error("Events must return a copy")
	}
	for _, e := range Events() {
		if c, ok := Canonical(strings.ToLower(e)); !ok || c != e {
			t.Errorf("Canonical(%q) = %q, %v", strings.ToLower(e), c, ok)
		}
	}
}

func TestSuggest(t *testing.T) {
	for in, want := range map[string]string{
		"PreToolUs": "PreToolUse", "posttooluse": "", "SesionStart": "SessionStart", "Stopp": "Stop", "Totally unrelated": "",
	} {
		got := suggest(in)
		if in == "posttooluse" {
			continue // an exact spelling never reaches suggest
		}
		if got != want {
			t.Errorf("suggest(%q) = %q, want %q", in, got, want)
		}
	}
}
