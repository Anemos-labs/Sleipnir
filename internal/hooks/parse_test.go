package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// cfg decodes a "hooks" object written as JSON.
func cfg(t testing.TB, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad test JSON: %v\n%s", err, s)
	}
	return m
}

func TestParseClaudeStyleConfiguration(t *testing.T) {
	m := cfg(t, `{
		"PreToolUse": [
			{"matcher": "Bash|Edit", "hooks": [
				{"type": "command", "command": "./check.sh", "timeout": 30},
				{"type": "command", "command": "./second.sh"}]},
			{"hooks": [{"type": "command", "command": "./all.sh"}]}],
		"SessionStart": [{"matcher": "startup|resume", "hooks": [{"type": "command", "command": "echo hi"}]}],
		"Stop": [{"hooks": [{"type": "command", "command": "./stop.sh", "failClosed": true}]}]
	}`)
	s, err := ParseAs(OriginUser, "~/.sleipnir/config.json", m)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Events(), ","); got != "SessionStart,PreToolUse,Stop" {
		t.Errorf("events = %s", got)
	}
	pre := s.Hooks("PreToolUse")
	if len(pre) != 3 {
		t.Fatalf("%d PreToolUse hooks", len(pre))
	}
	h := pre[0]
	if h.Event != PreToolUse || h.Matcher != "Bash|Edit" || h.Type != TypeCommand || h.Command != "./check.sh" || h.Timeout != 30*time.Second {
		t.Errorf("first hook = %+v", h)
	}
	if h.Origin != OriginUser || h.Source != "~/.sleipnir/config.json" || h.Group != 0 || h.Index != 0 {
		t.Errorf("provenance = %+v", h)
	}
	if pre[1].Index != 1 || pre[2].Group != 1 || pre[2].Matcher != "" {
		t.Errorf("positions: %+v %+v", pre[1], pre[2])
	}
	if got := pre[0].String(); got != "~/.sleipnir/config.json PreToolUse[0].hooks[0] (command: ./check.sh)" {
		t.Errorf("String() = %q", got)
	}
	if stop := s.Hooks("stop"); len(stop) != 1 || stop[0].FailClosed == nil || !*stop[0].FailClosed {
		t.Errorf("stop hooks: %+v", stop)
	}
	if len(s.Warnings()) != 0 {
		t.Errorf("warnings: %v", s.Warnings())
	}
}

func TestParseAcceptsEventAliases(t *testing.T) {
	m := cfg(t, `{
		"pre_tool_use": [{"hooks": [{"type": "command", "command": "a"}]}],
		"PRE-TOOL-USE": [{"hooks": [{"type": "command", "command": "b"}]}],
		"post_tool": [{"hooks": [{"type": "command", "command": "c"}]}],
		"prompt_submit": [{"hooks": [{"type": "command", "command": "d"}]}],
		"agent_stop": [{"hooks": [{"type": "command", "command": "e"}]}],
		"session_start": [{"hooks": [{"type": "command", "command": "f"}]}]
	}`)
	s, err := Parse(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Events(), ","); got != "SessionStart,UserPromptSubmit,PreToolUse,PostToolUse,SubagentStop" {
		t.Errorf("events = %s", got)
	}
	// Two spellings of one event merge, in sorted key order (deterministic).
	pre := s.Hooks(PreToolUse)
	if len(pre) != 2 || pre[0].Command != "b" || pre[1].Command != "a" {
		t.Errorf("merged spellings: %+v", pre)
	}
	for in, want := range map[string]string{"pretool": PreToolUse, "Permission": PermissionRequest, "tool_failure": PostToolUseFailure, "notify": Notification, "agent_start": SubagentStart} {
		if got, ok := Canonical(in); !ok || got != want {
			t.Errorf("Canonical(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := Canonical("Setup"); ok {
		t.Error("Setup is not an event Sleipnir fires")
	}
}

func TestParseIgnoresWhatSleipnirDoesNotDo(t *testing.T) {
	m := cfg(t, `{
		"Setup": [{"hooks": [{"type": "command", "command": "x"}]}],
		"FileChanged": [{"hooks": [{"type": "command", "command": "x"}]}],
		"PreToolUse": [{"matcher": "Bash", "extra": 1, "hooks": [
			{"type": "prompt", "prompt": "is this ok?"},
			{"type": "agent", "prompt": "verify"},
			{"type": "command", "command": "./keep.sh", "async": true, "statusMessage": "checking", "mystery": 1}]}],
		"Stop": null
	}`)
	s, err := Parse(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.Hooks(PreToolUse)); got != 1 {
		t.Fatalf("%d hooks kept", got)
	}
	text := strings.Join(s.Warnings(), "\n")
	for _, want := range []string{"Setup", "FileChanged", `hook type "prompt" is not supported`, `hook type "agent" is not supported`, `field "async" is not supported`, `unknown field "mystery"`, `unknown field "extra"`} {
		if !strings.Contains(text, want) {
			t.Errorf("warnings lack %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "statusMessage") || strings.Contains(text, "Stop") {
		t.Errorf("known or null entries must not warn:\n%s", text)
	}
}

func TestParseErrorsNameTheEventAndIndex(t *testing.T) {
	tests := []struct {
		name, json, want string
	}{
		{"unknown event", `{"PreToolUs": []}`, `hooks: PreToolUs: unknown event "PreToolUs" (did you mean "PreToolUse"?)`},
		{"nonsense event", `{"Whatever": []}`, `unknown event "Whatever"; the events are SessionStart`},
		{"groups not a list", `{"PreToolUse": {"matcher": "x"}}`, "PreToolUse: expected a list of matcher groups"},
		{"group not an object", `{"PreToolUse": ["x"]}`, "PreToolUse[0]: expected an object"},
		{"second group bad", `{"PreToolUse": [{"hooks": []}, 5]}`, "PreToolUse[1]: expected an object"},
		{"missing hooks", `{"PreToolUse": [{"matcher": "Bash"}]}`, `PreToolUse[0]: "hooks" is required`},
		{"hook where a group belongs", `{"PreToolUse": [{"type": "command", "command": "x"}]}`, "PreToolUse[0]: found a hook where a matcher group is expected"},
		{"hooks not a list", `{"Stop": [{"hooks": {"command": "x"}}]}`, "Stop[0].hooks: expected a list of hooks"},
		{"hook not an object", `{"Stop": [{"hooks": ["x"]}]}`, "Stop[0].hooks[0]: expected an object"},
		{"matcher not a string", `{"Stop": [{"matcher": 5, "hooks": []}]}`, "Stop[0].matcher: expected a string"},
		{"bad regex", `{"PreToolUse": [{"matcher": "(unclosed", "hooks": []}]}`, "PreToolUse[0].matcher: matcher \"(unclosed\" is not a valid regular expression"},
		{"lookahead is not RE2", `{"PreToolUse": [{"matcher": "^(?!Bash)", "hooks": []}]}`, "not a valid regular expression (RE2 syntax)"},
		{"no type", `{"Stop": [{"hooks": [{"timeout": 3}]}]}`, `Stop[0].hooks[0]: "type" is required`},
		{"no command", `{"Stop": [{"hooks": [{"type": "command"}]}]}`, "Stop[0].hooks[0]: command is required"},
		{"blank command", `{"Stop": [{"hooks": [{"type": "command", "command": "   "}]}]}`, "Stop[0].hooks[0]: command is required"},
		{"command wrong type", `{"Stop": [{"hooks": [{"type": "command", "command": 5}]}]}`, "Stop[0].hooks[0].command: expected a string"},
		{"nul in command", `{"Stop": [{"hooks": [{"type": "command", "command": "a\u0000b"}]}]}`, "command contains a NUL byte"},
		{"huge command", `{"Stop": [{"hooks": [{"type": "command", "command": "` + strings.Repeat("x", MaxCommandBytes+1) + `"}]}]}`, "the limit is 16384"},
		{"timeout string", `{"Stop": [{"hooks": [{"type": "command", "command": "x", "timeout": "30"}]}]}`, "Stop[0].hooks[0].timeout: expected a number of seconds"},
		{"timeout negative", `{"Stop": [{"hooks": [{"type": "command", "command": "x", "timeout": -1}]}]}`, "must be a positive number of seconds"},
		{"failClosed wrong type", `{"Stop": [{"hooks": [{"type": "command", "command": "x", "failClosed": "yes"}]}]}`, "failClosed: expected true or false"},
		{"http without url", `{"Stop": [{"hooks": [{"type": "http"}]}]}`, "url is required for an http hook"},
		{"http relative url", `{"Stop": [{"hooks": [{"type": "http", "url": "/x"}]}]}`, "absolute http or https URL"},
		{"http ftp", `{"Stop": [{"hooks": [{"type": "http", "url": "ftp://x/y"}]}]}`, "absolute http or https URL"},
		{"http credentials", `{"Stop": [{"hooks": [{"type": "http", "url": "https://user:pass@example.com/x"}]}]}`, "must not contain credentials"},
		{"http bad header", `{"Stop": [{"hooks": [{"type": "http", "url": "https://x.example/y", "headers": {"X-A": "a\r\nInjected: 1"}}]}]}`, `header "X-A" is not valid`},
		{"http headers wrong type", `{"Stop": [{"hooks": [{"type": "http", "url": "https://x.example/y", "headers": ["a"]}]}]}`, "headers: expected an object of strings"},
		{"bad if", `{"PreToolUse": [{"hooks": [{"type": "command", "command": "x", "if": "Bash("}]}]}`, "PreToolUse[0].hooks[0].if:"},
		{"if wrong type", `{"PreToolUse": [{"hooks": [{"type": "command", "command": "x", "if": 5}]}]}`, "if: expected a string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(cfg(t, tc.json))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v\nwant it to contain %q", err, tc.want)
			}
			if !strings.HasPrefix(err.Error(), "hooks: ") {
				t.Errorf("errors must be prefixed with the package: %q", err)
			}
		})
	}
}

func TestParseReportsEveryProblemSorted(t *testing.T) {
	m := cfg(t, `{
		"Stop": [{"hooks": [{"type": "command"}, {"type": "command", "command": "ok"}, {"type": "http"}]}],
		"PreToolUse": [{"matcher": "(", "hooks": []}, 5],
		"Nope": []
	}`)
	_, err := ParseAs(OriginUser, "settings.json", m)
	if err == nil {
		t.Fatal("expected errors")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d problems:\n%s", len(lines), err)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i-1] > lines[i] {
			t.Errorf("errors are not sorted:\n%s", err)
		}
	}
	if !strings.HasPrefix(lines[0], "hooks (settings.json): ") {
		t.Errorf("source missing: %q", lines[0])
	}
}

func TestParseBoundsTheProblemList(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"Stop": [`)
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`5`)
	}
	b.WriteString(`]}`)
	_, err := Parse(cfg(t, b.String()))
	if err == nil || strings.Count(err.Error(), "\n") != maxParseErrors || !strings.Contains(err.Error(), "and 80 more problems") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseBoundsAHostileConfiguration(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"Stop": [`)
	for i := 0; i <= maxGroups; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"hooks": []}`)
	}
	b.WriteString(`]}`)
	if _, err := Parse(cfg(t, b.String())); err == nil || !strings.Contains(err.Error(), "matcher groups; the limit is 256") {
		t.Fatalf("group limit: %v", err)
	}
	var hb strings.Builder
	hb.WriteString(`{"Stop": [{"hooks": [`)
	for i := 0; i <= maxHooksPerGroup; i++ {
		if i > 0 {
			hb.WriteByte(',')
		}
		hb.WriteString(`{"type": "command", "command": "x"}`)
	}
	hb.WriteString(`]}]}`)
	if _, err := Parse(cfg(t, hb.String())); err == nil || !strings.Contains(err.Error(), "hooks; the limit is 64") {
		t.Fatalf("hooks limit: %v", err)
	}
}

func TestParseEmptyAndNil(t *testing.T) {
	for _, m := range []map[string]json.RawMessage{nil, {}, cfg(t, `{"Stop": []}`), cfg(t, `{"Stop": [{"hooks": []}]}`), cfg(t, `{"Stop": null}`)} {
		s, err := Parse(m)
		if err != nil || !s.Empty() {
			t.Errorf("Parse(%v) = %v, %v", m, s, err)
		}
	}
	var s *Set
	if !s.Empty() || s.Events() != nil || s.Hooks(Stop) != nil || s.Warnings() != nil {
		t.Error("a nil Set must behave as an empty one")
	}
	if hs, err := s.Matching(Event{Name: Stop}); hs != nil || err != nil {
		t.Errorf("Matching on nil: %v %v", hs, err)
	}
}

func TestParseDefaultsAndOrigins(t *testing.T) {
	m := cfg(t, `{"Stop": [{"hooks": [
		{"command": "implied-command"},
		{"url": "https://example.com/hook"},
		{"type": "http", "url": "http://localhost:8080/x", "headers": {"X-Token": "abc"}, "timeout": 2.5}]}]}`)
	s, err := Parse(m)
	if err != nil {
		t.Fatal(err)
	}
	hs := s.Hooks(Stop)
	if hs[0].Type != TypeCommand || hs[1].Type != TypeHTTP || hs[2].Timeout != 2500*time.Millisecond || hs[2].Headers["X-Token"] != "abc" {
		t.Errorf("hooks: %+v", hs)
	}
	if hs[0].Origin != "" {
		t.Errorf("Parse must not vouch for a hook: origin %q", hs[0].Origin)
	}
}

func TestIfOnlyAppliesToToolEvents(t *testing.T) {
	m := cfg(t, `{"Stop": [{"hooks": [{"type": "command", "command": "x", "if": "Bash(ls:*)"}]}],
		"PreToolUse": [{"hooks": [{"type": "command", "command": "y", "if": "Bash(ls:*)"}]}]}`)
	s, err := Parse(m)
	if err != nil {
		t.Fatal(err)
	}
	if s.Hooks(Stop)[0].If != "" || s.Hooks(PreToolUse)[0].If != "Bash(ls:*)" {
		t.Errorf("if handling: %q %q", s.Hooks(Stop)[0].If, s.Hooks(PreToolUse)[0].If)
	}
	if w := strings.Join(s.Warnings(), "\n"); !strings.Contains(w, `"if" only applies to tool events`) {
		t.Errorf("warnings: %s", w)
	}
}

func TestHookKeyIdentifiesBehaviour(t *testing.T) {
	a := Hook{Event: Stop, Type: TypeCommand, Command: "x", Group: 0, Index: 0, Source: "one"}
	b := Hook{Event: Stop, Type: TypeCommand, Command: "x", Group: 3, Index: 2, Source: "two"}
	c := Hook{Event: Stop, Type: TypeCommand, Command: "y"}
	d := Hook{Event: PreToolUse, Type: TypeCommand, Command: "x"}
	e := Hook{Event: Stop, Matcher: "Bash", Type: TypeCommand, Command: "x"}
	if a.Key() != b.Key() {
		t.Error("position and source must not change a hook's identity")
	}
	for name, h := range map[string]Hook{"command": c, "event": d, "matcher": e} {
		if h.Key() == a.Key() {
			t.Errorf("a different %s must give a different key", name)
		}
	}
	h1 := Hook{Type: TypeHTTP, URL: "https://x", Headers: map[string]string{"A": "1", "B": "2"}}
	h2 := Hook{Type: TypeHTTP, URL: "https://x", Headers: map[string]string{"B": "2", "A": "1"}}
	if h1.Key() != h2.Key() {
		t.Error("header order must not matter")
	}
}

func TestMergeKeepsConfigurationOrder(t *testing.T) {
	user, _ := ParseAs(OriginUser, "user", cfg(t, `{"Stop": [{"hooks": [{"command": "u1"}, {"command": "u2"}]}]}`))
	proj, _ := ParseAs(OriginProject, "proj", cfg(t, `{"Stop": [{"hooks": [{"command": "p1"}]}], "SessionEnd": [{"hooks": [{"command": "p2"}]}]}`))
	m := Merge(nil, user, proj, nil)
	var got []string
	for _, h := range m.Hooks(Stop) {
		got = append(got, h.Command+"@"+h.Source)
	}
	if strings.Join(got, ",") != "u1@user,u2@user,p1@proj" {
		t.Errorf("merged = %v", got)
	}
	if strings.Join(m.Events(), ",") != "SessionEnd,Stop" {
		t.Errorf("events = %v", m.Events())
	}
	if !Merge().Empty() {
		t.Error("merging nothing must give an empty set")
	}
}
