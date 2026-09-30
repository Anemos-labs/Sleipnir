package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse: any JSON becomes a Set or an error, never a panic, and a Set
// contains only well-formed hooks.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"PreToolUse": []}`, `{"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "x", "timeout": 3}]}]}`,
		`{"Stop": [{"hooks": [{"type": "http", "url": "https://x.example/y", "headers": {"A": "b"}}]}]}`,
		`{"Nope": 1}`, `{"PreToolUse": [{"matcher": "(", "hooks": []}]}`, `{"pre_tool_use": null}`, `{"PreToolUse": [5, "x", null, []]}`,
		`{"PreToolUse": [{"hooks": [{"command": "x", "if": "Bash(ls:*)", "failClosed": true}]}]}`, `{"Setup": []}`,
		`{"PreToolUse": [{"hooks": [{"type": "command", "command": "x", "timeout": 1e999}]}]}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(s), &m) != nil {
			return
		}
		set, err := Parse(m)
		if err != nil {
			if !strings.HasPrefix(err.Error(), "hooks: ") {
				t.Fatalf("error without the package prefix: %q", err)
			}
			return
		}
		for _, e := range set.Events() {
			for _, h := range set.Hooks(e) {
				if h.Event != e || (h.Type != TypeCommand && h.Type != TypeHTTP) {
					t.Fatalf("malformed hook %+v", h)
				}
				if h.Type == TypeCommand && strings.TrimSpace(h.Command) == "" {
					t.Fatal("a command hook without a command")
				}
				if h.Timeout < 0 {
					t.Fatalf("negative timeout %v", h.Timeout)
				}
				if h.Key() == "" || h.String() == "" {
					t.Fatal("empty key or description")
				}
			}
		}
		if _, err := set.Matching(Event{Name: PreToolUse, Tool: "bash"}); err != nil {
			t.Fatal(err)
		}
	})
}

// FuzzMatcher: compiling and matching never panic, and "*" matches everything.
func FuzzMatcher(f *testing.F) {
	for _, seed := range [][2]string{{"Bash", "bash"}, {"Bash|Edit", "edit"}, {"^Note", "NotebookEdit"}, {"(", "x"}, {"*", ""}, {"a{1000}{1000}", "aaa"}, {"", ""}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, matcher, tool string) {
		m, err := compileMatcher(matcher)
		if err != nil {
			return
		}
		_ = m.matches(toolCandidates(tool))
		if all, _ := compileMatcher("*"); !all.matches(toolCandidates(tool)) {
			t.Fatal("* must match everything")
		}
	})
}

// FuzzParseOutput: hook output is hostile input. Whatever it says, parsing
// yields text that is clean and bounded.
func FuzzParseOutput(f *testing.F) {
	for _, seed := range []string{
		``, `plain`, `{}`, `{"decision":"block","reason":"x"}`, `{"hookSpecificOutput":{"permissionDecision":"deny","updatedInput":{"a":1}}}`,
		`{"hookSpecificOutput":5}`, `{"continue":false,"stopReason":"\u001b[31m"}`, `{"additionalContext":"` + strings.Repeat("a", 100) + `"}`,
		`{"hookSpecificOutput":{"decision":{"behavior":"allow","updatedInput":[1]}}}`, "{\x00}",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, out string) {
		for _, event := range Events() {
			p, ok, err := parseOutput(event, []byte(out))
			if ok && err != nil {
				t.Fatal("ok with an error")
			}
			for _, s := range []string{p.reason, p.context, p.stopReason, p.message} {
				if !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\x1b") {
					t.Fatalf("unclean text %q", s)
				}
			}
			if len(p.reason) > maxReason+len(truncMark) {
				t.Fatalf("reason of %d bytes", len(p.reason))
			}
			if p.updated != nil && !json.Valid(p.updated) {
				t.Fatalf("updated input is not JSON: %s", p.updated)
			}
			if !hasDecision(event) && p.decision != "" {
				t.Fatalf("a decision on %s", event)
			}
		}
	})
}

// FuzzCleanText: output is always valid UTF-8 without escapes or controls.
func FuzzCleanText(f *testing.F) {
	for _, seed := range []string{"", "plain", "\x1b[31mred", "\x1b]0;t\x07", "a\xffb", "\x1b", "\x1bP\x1b", strings.Repeat("\x1b[", 100)} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		out := cleanText(b)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8: %q", out)
		}
		for _, r := range out {
			if r == 0x1b || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
				t.Fatalf("control character %U survived in %q", r, out)
			}
		}
		if len(out) > len(b)*3+8 { // U+FFFD is three bytes for one invalid byte
			t.Fatalf("output grew from %d to %d bytes", len(b), len(out))
		}
	})
}

// FuzzScrubEnv: a scrubbed environment never contains a name the pattern
// forbids, and scrubbing is idempotent.
func FuzzScrubEnv(f *testing.F) {
	for _, seed := range []string{"A=1", "API_KEY=x", "PATH=/bin\nHOME=/h", "X_TOKEN=1\nY=postgres://u:p@h/d", "=x", "noequals"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		base := strings.Split(s, "\n")
		out := scrubEnv(base, nil, nil)
		for _, kv := range out {
			name, value, ok := strings.Cut(kv, "=")
			if !ok || name == "" || secretName.MatchString(name) || urlCredentials.MatchString(value) {
				t.Fatalf("%q survived scrubbing", kv)
			}
		}
		again := scrubEnv(out, nil, nil)
		if strings.Join(again, "\n") != strings.Join(out, "\n") {
			t.Fatal("scrubbing is not idempotent")
		}
	})
}
