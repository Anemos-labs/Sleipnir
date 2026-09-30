//go:build unix

package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func errorText(res Result) string {
	var out []string
	for _, e := range res.Errors {
		out = append(out, e.Error())
	}
	return strings.Join(out, "\n")
}

func TestExitStatusSemantics(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		command string
		blocked bool
		dec     Decision
		reason  string // substring
		context string
		errs    string // substring of the error text ("" = no errors)
	}{
		{name: "success", event: PreToolUse, command: "exit 0"},
		{name: "pre-tool exit 2 blocks with stderr", event: PreToolUse, command: "echo 'no rm here' >&2; exit 2", blocked: true, dec: Deny, reason: "no rm here"},
		{name: "exit 2 without a message", event: PreToolUse, command: "exit 2", blocked: true, dec: Deny, reason: "gave no reason"},
		{name: "permission request exit 2", event: PermissionRequest, command: "echo denied >&2; exit 2", blocked: true, dec: Deny, reason: "denied"},
		{name: "post-tool exit 2 feeds the model", event: PostToolUse, command: "echo 'lint failed' >&2; exit 2", blocked: true, reason: "lint failed"},
		{name: "post-tool failure exit 2", event: PostToolUseFailure, command: "echo again >&2; exit 2", blocked: true, reason: "again"},
		{name: "prompt exit 2 blocks", event: UserPromptSubmit, command: "echo 'not now' >&2; exit 2", blocked: true, reason: "not now"},
		{name: "stop exit 2 keeps going", event: Stop, command: "echo 'tests fail' >&2; exit 2", blocked: true, reason: "tests fail"},
		{name: "subagent stop exit 2", event: SubagentStop, command: "echo more >&2; exit 2", blocked: true, reason: "more"},
		{name: "pre-compact exit 2 blocks", event: PreCompact, command: "echo wait >&2; exit 2", blocked: true, reason: "wait"},
		{name: "session start exit 2 blocks nothing", event: SessionStart, command: "echo oops >&2; exit 2", errs: "exited with status 2, which blocks nothing on SessionStart: oops"},
		{name: "notification exit 2 blocks nothing", event: Notification, command: "exit 2", errs: "blocks nothing"},
		{name: "session end exit 2 blocks nothing", event: SessionEnd, command: "exit 2", errs: "blocks nothing"},
		{name: "exit 1 is a non-blocking error", event: PreToolUse, command: "echo 'script bug' >&2; exit 1", errs: "exited with status 1: script bug"},
		{name: "exit 3 is a non-blocking error", event: Stop, command: "exit 3", errs: "exited with status 3"},
		{name: "command not found fails open", event: PreToolUse, command: "definitely-not-a-command-xyz", errs: "exited with status 127"},
		{name: "killed by a signal", event: PreToolUse, command: "kill -9 $$", errs: "killed by signal 9"},
		{name: "session start stdout is context", event: SessionStart, command: "echo 'branch: main'", context: "branch: main"},
		{name: "prompt stdout is context", event: UserPromptSubmit, command: "printf 'line one\\nline two\\n'", context: "line one\nline two"},
		{name: "pre-tool stdout is ignored", event: PreToolUse, command: "echo ignored"},
		{name: "stop stdout is ignored", event: Stop, command: "echo ignored"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := one(t, tc.event, "", tc.command)
			res := run(t, r, Event{Name: tc.event, Tool: "bash"})
			if res.Blocked != tc.blocked || res.Decision != tc.dec {
				t.Errorf("blocked=%v decision=%q, want %v %q (errors: %s)", res.Blocked, res.Decision, tc.blocked, tc.dec, errorText(res))
			}
			if !strings.Contains(res.Reason, tc.reason) || (tc.reason == "" && res.Reason != "") {
				t.Errorf("reason = %q, want it to contain %q", res.Reason, tc.reason)
			}
			if res.AdditionalContext != tc.context {
				t.Errorf("context = %q, want %q", res.AdditionalContext, tc.context)
			}
			if tc.errs == "" && len(res.Errors) != 0 || tc.errs != "" && !strings.Contains(errorText(res), tc.errs) {
				t.Errorf("errors = %q, want %q", errorText(res), tc.errs)
			}
			if res.Ran() != 1 || len(res.Runs) != 1 {
				t.Errorf("runs = %+v", res.Runs)
			}
		})
	}
}

func TestJSONOutputs(t *testing.T) {
	tests := []struct {
		name, event, out string
		blocked          bool
		dec              Decision
		reason           string
		updated          string
		context          string
		stop             bool
		errs             string
	}{
		{name: "deny", event: PreToolUse, out: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"protected file"}}`, blocked: true, dec: Deny, reason: "protected file"},
		{name: "allow", event: PreToolUse, out: `{"hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"trusted command"}}`, dec: Allow, reason: "trusted command"},
		{name: "ask", event: PreToolUse, out: `{"hookSpecificOutput":{"permissionDecision":"ask","permissionDecisionReason":"please confirm"}}`, dec: Ask, reason: "please confirm"},
		{name: "defer is no opinion", event: PreToolUse, out: `{"hookSpecificOutput":{"permissionDecision":"defer"}}`},
		{name: "legacy block on pre-tool is a denial", event: PreToolUse, out: `{"decision":"block","reason":"nope"}`, blocked: true, dec: Deny, reason: "nope"},
		{name: "legacy approve on pre-tool", event: PreToolUse, out: `{"decision":"approve","reason":"fine"}`, dec: Allow, reason: "fine"},
		{name: "legacy top-level permission fields", event: PreToolUse, out: `{"permissionDecision":"deny","permissionDecisionReason":"top"}`, blocked: true, dec: Deny, reason: "top"},
		{name: "block on post-tool feeds the model", event: PostToolUse, out: `{"decision":"block","reason":"tests broke"}`, blocked: true, reason: "tests broke"},
		{name: "block on stop keeps going", event: Stop, out: `{"decision":"block","reason":"finish the tests"}`, blocked: true, reason: "finish the tests"},
		{name: "block on prompt", event: UserPromptSubmit, out: `{"decision":"block","reason":"secrets in prompt"}`, blocked: true, reason: "secrets in prompt"},
		{name: "block on session start means nothing", event: SessionStart, out: `{"decision":"block","reason":"x"}`},
		{name: "decisions mean nothing on post-tool", event: PostToolUse, out: `{"hookSpecificOutput":{"permissionDecision":"allow"}}`},
		{name: "updated input", event: PreToolUse, out: `{"hookSpecificOutput":{"updatedInput":{"command":"ls -la"}}}`, updated: `{"command":"ls -la"}`},
		{name: "updated input at the top level", event: PreToolUse, out: `{"updatedInput":{"path":"a.txt"}}`, updated: `{"path":"a.txt"}`},
		{name: "updated input is dropped on a denial", event: PreToolUse, out: `{"hookSpecificOutput":{"permissionDecision":"deny","updatedInput":{"command":"x"}}}`, blocked: true, dec: Deny, reason: "gave no reason"},
		{name: "updated input must be an object", event: PreToolUse, out: `{"hookSpecificOutput":{"updatedInput":"rm -rf /"}}`, errs: "updatedInput is not a JSON object"},
		{name: "additional context", event: PostToolUse, out: `{"hookSpecificOutput":{"additionalContext":"the file has 3 TODOs"}}`, context: "the file has 3 TODOs"},
		{name: "additional context at the top level", event: UserPromptSubmit, out: `{"additionalContext":"today is Tuesday"}`, context: "today is Tuesday"},
		{name: "continue false stops the run", event: PostToolUse, out: `{"continue":false,"stopReason":"budget exhausted"}`, stop: true},
		{name: "continue true is nothing", event: PostToolUse, out: `{"continue":true}`},
		{name: "permission request allow", event: PermissionRequest, out: `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`, dec: Allow},
		{name: "permission request deny with message", event: PermissionRequest, out: `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"not on a Friday"}}}`, blocked: true, dec: Deny, reason: "not on a Friday"},
		{name: "invalid json", event: PreToolUse, out: `{"hookSpecificOutput": {oops}`, errs: "starts like JSON but does not parse"},
		{name: "wrong field types", event: PreToolUse, out: `{"reason": 5, "continue": "no", "additionalContext": ["x"]}`, errs: "reason is not a string"},
		{name: "empty object", event: PreToolUse, out: `{}`},
		{name: "unknown fields are ignored", event: PreToolUse, out: `{"suppressOutput":true,"terminalSequence":"x","whatever":[1,2]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := one(t, tc.event, "", heredoc(tc.out))
			res := run(t, r, Event{Name: tc.event, Tool: "bash"})
			if res.Blocked != tc.blocked || res.Decision != tc.dec {
				t.Errorf("blocked=%v decision=%q, want %v %q (errors: %s)", res.Blocked, res.Decision, tc.blocked, tc.dec, errorText(res))
			}
			if !strings.Contains(res.Reason, tc.reason) || (tc.reason == "" && res.Reason != "") {
				t.Errorf("reason = %q, want %q", res.Reason, tc.reason)
			}
			if string(res.UpdatedInput) != tc.updated {
				t.Errorf("updated input = %q, want %q", res.UpdatedInput, tc.updated)
			}
			if res.AdditionalContext != tc.context {
				t.Errorf("context = %q, want %q", res.AdditionalContext, tc.context)
			}
			if res.Stop != tc.stop {
				t.Errorf("stop = %v, want %v", res.Stop, tc.stop)
			}
			if tc.errs == "" && len(res.Errors) != 0 || tc.errs != "" && !strings.Contains(errorText(res), tc.errs) {
				t.Errorf("errors = %q, want %q", errorText(res), tc.errs)
			}
		})
	}
}

func TestSystemMessageAndStopReason(t *testing.T) {
	r := one(t, PostToolUse, "", heredoc(`{"continue": false, "stopReason": "enough for today", "systemMessage": "Hook says: wrapping up"}`))
	res := run(t, r, Event{Name: PostToolUse, Tool: "bash"})
	if !res.Stop || res.StopReason != "enough for today" || len(res.Messages) != 1 || res.Messages[0] != "Hook says: wrapping up" {
		t.Errorf("%+v", res)
	}
}

func TestPayloadReachesTheHook(t *testing.T) {
	r := one(t, PreToolUse, "", "cat > payload.json")
	res := run(t, r, Event{
		Name: PreToolUse, Tool: "bash", Input: json.RawMessage(`{"command": "ls <-la> & echo", "timeout": 5}`),
		Agent: "be-1", Role: "backend", SessionID: "abc123", Cwd: r.Dir,
		Extra: map[string]any{"prompt": "hello", "tool_name": "SPOOFED", "session_id": "SPOOFED", "n": 3},
	})
	if len(res.Errors) != 0 {
		t.Fatal(errorText(res))
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, "payload.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, b)
	}
	want := map[string]any{
		"session_id": "abc123", "cwd": r.Dir, "hook_event_name": "PreToolUse", "tool_name": "Bash", "sleipnir_tool_name": "bash",
		"agent": "be-1", "agent_id": "be-1", "role": "backend", "agent_type": "backend", "prompt": "hello", "n": float64(3),
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, p[k], v)
		}
	}
	in, _ := p["tool_input"].(map[string]any)
	if in["command"] != "ls <-la> & echo" || in["timeout"] != float64(5) {
		t.Errorf("tool_input = %v", in)
	}
	if strings.Contains(string(b), "\\"+"u003c") { // built at run time: the escape must not be written literally here
		t.Errorf("payload is HTML-escaped: %s", b)
	}
	// Deterministic bytes: a second run writes the same payload.
	run(t, r, Event{
		Name: PreToolUse, Tool: "bash", Input: json.RawMessage(`{"command": "ls <-la> & echo", "timeout": 5}`),
		Agent: "be-1", Role: "backend", SessionID: "abc123", Cwd: r.Dir,
		Extra: map[string]any{"prompt": "hello", "tool_name": "SPOOFED", "session_id": "SPOOFED", "n": 3},
	})
	if b2, _ := os.ReadFile(filepath.Join(r.Dir, "payload.json")); string(b2) != string(b) {
		t.Errorf("payload bytes are not deterministic:\n%s\n%s", b, b2)
	}
}

func TestPayloadFilePathAliasAndSessionDefaults(t *testing.T) {
	r := one(t, PreToolUse, "", "cat > payload.json")
	for _, tc := range []struct {
		tool, input string
		wantPath    bool
	}{
		{"write", `{"path": "src/a.go", "content": "x"}`, true},
		{"edit", `{"path": "src/a.go", "old_string": "a", "new_string": "b"}`, true},
		{"read", `{"path": "src/a.go"}`, true},
		{"read", `{"file_path": "src/a.go"}`, true},
		{"grep", `{"pattern": "x", "path": "src"}`, false},
		{"bash", `{"command": "ls"}`, false},
	} {
		run(t, r, Event{Name: PreToolUse, Tool: tc.tool, Input: json.RawMessage(tc.input)})
		b, _ := os.ReadFile(filepath.Join(r.Dir, "payload.json"))
		var p struct {
			SessionID string         `json:"session_id"`
			Cwd       string         `json:"cwd"`
			Input     map[string]any `json:"tool_input"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatalf("%s: %v\n%s", tc.tool, err, b)
		}
		_, has := p.Input["file_path"]
		if has != tc.wantPath {
			t.Errorf("%s: file_path present = %v, want %v (%v)", tc.tool, has, tc.wantPath, p.Input)
		}
		if tc.wantPath && p.Input["file_path"] != "src/a.go" {
			t.Errorf("%s: file_path = %v", tc.tool, p.Input["file_path"])
		}
		if p.SessionID != "sess-1" || p.Cwd != r.Dir {
			t.Errorf("defaults: session %q cwd %q", p.SessionID, p.Cwd)
		}
	}
}

func TestPayloadWithInvalidInputStaysValidJSON(t *testing.T) {
	r := one(t, PreToolUse, "", "cat > payload.json")
	run(t, r, Event{Name: PreToolUse, Tool: "bash", Input: json.RawMessage(`{"command": "unterminated`)})
	b, _ := os.ReadFile(filepath.Join(r.Dir, "payload.json"))
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, b)
	}
	if p["tool_input"] != `{"command": "unterminated` {
		t.Errorf("tool_input = %v", p["tool_input"])
	}
}

func TestPayloadTruncation(t *testing.T) {
	r := one(t, PostToolUse, "", "cat > payload.json")
	r.MaxPayload = 20 << 10
	big := strings.Repeat("x", 100<<10)
	res := run(t, r, Event{Name: PostToolUse, Tool: "bash", Input: json.RawMessage(`{"command": "ls"}`), Output: big})
	if len(res.Errors) != 0 {
		t.Fatal(errorText(res))
	}
	b, _ := os.ReadFile(filepath.Join(r.Dir, "payload.json"))
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("truncated payload is not JSON: %v", err)
	}
	if p["payload_truncated"] != true || len(b) > 20<<10 {
		t.Errorf("payload_truncated=%v, %d bytes", p["payload_truncated"], len(b))
	}
	if !strings.Contains(p["tool_response"].(string), "truncated") {
		t.Error("the response should say it was cut")
	}

	// A huge tool_input is replaced by a marker, not passed half.
	huge := `{"content": "` + strings.Repeat("y", 100<<10) + `"}`
	run(t, r, Event{Name: PostToolUse, Tool: "write", Input: json.RawMessage(huge)})
	b, _ = os.ReadFile(filepath.Join(r.Dir, "payload.json"))
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	in, _ := p["tool_input"].(map[string]any)
	if p["payload_truncated"] != true || in["truncated"] != true {
		t.Errorf("tool_input = %v", p["tool_input"])
	}
}

func TestHookRunsInTheEventsDirectory(t *testing.T) {
	r := one(t, Stop, "", "pwd > where.txt")
	other := realTemp(t)
	run(t, r, Event{Name: Stop, Cwd: other})
	if b, _ := os.ReadFile(filepath.Join(other, "where.txt")); strings.TrimSpace(string(b)) != other {
		t.Errorf("ran in %q, want %q", b, other)
	}
	// A directory that does not exist falls back to the project directory.
	run(t, r, Event{Name: Stop, Cwd: filepath.Join(other, "missing")})
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "where.txt")); strings.TrimSpace(string(b)) != r.Dir {
		t.Errorf("fallback ran in %q, want %q", b, r.Dir)
	}
}

func TestRunOnNothing(t *testing.T) {
	var nilRunner *Runner
	if res, err := nilRunner.Run(t.Context(), Event{Name: Stop}); err != nil || res.Ran() != 0 || res.Blocked {
		t.Errorf("nil runner: %+v %v", res, err)
	}
	if res, err := (&Runner{}).Run(t.Context(), Event{Name: Stop}); err != nil || len(res.Runs) != 0 {
		t.Errorf("runner without a set: %+v %v", res, err)
	}
	r := one(t, Stop, "", "exit 0")
	if _, err := r.Run(t.Context(), Event{Name: "Bogus"}); err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Errorf("unknown event: %v", err)
	}
	if _, err := (&Runner{}).Run(t.Context(), Event{Name: "Bogus"}); err == nil {
		t.Error("an unknown event is a caller error even when there is nothing to run")
	}
	if res := run(t, r, Event{Name: PostCompact}); len(res.Runs) != 0 {
		t.Errorf("an event without hooks ran %d", len(res.Runs))
	}
}

func TestFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		runner  bool // Runner.FailClosed
		hook    *bool
		event   string
		command string
		blocked bool
	}{
		{"open by default", false, nil, PreToolUse, "exit 1", false},
		{"runner closed, exit 1", true, nil, PreToolUse, "exit 1", true},
		{"hook closed, exit 1", false, ptr(true), PreToolUse, "exit 1", true},
		{"hook open overrides runner closed", true, ptr(false), PreToolUse, "exit 1", false},
		{"closed on command not found", true, nil, PreToolUse, "no-such-command-xyz", true},
		{"closed on invalid json", true, nil, PreToolUse, "echo '{broken'", true},
		{"closed on a stop hook", true, nil, Stop, "exit 1", true},
		{"closed applies to prompts", true, nil, UserPromptSubmit, "exit 5", true},
		{"nothing to block on session start", true, nil, SessionStart, "exit 1", false},
		{"nothing to block on notification", true, nil, Notification, "exit 1", false},
		{"success is not a failure", true, nil, PreToolUse, "exit 0", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := cmdHook(tc.command)
			if tc.hook != nil {
				h.failClosed(*tc.hook)
			}
			r := newRunner(t, settings(t, tc.event, group{hooks: []hookSpec{h}}))
			r.FailClosed = tc.runner
			res := run(t, r, Event{Name: tc.event})
			if res.Blocked != tc.blocked {
				t.Fatalf("blocked = %v, want %v (%s)", res.Blocked, tc.blocked, errorText(res))
			}
			if tc.blocked {
				if !strings.Contains(res.Reason, "fail closed") {
					t.Errorf("reason = %q", res.Reason)
				}
				if hasDecision(tc.event) && res.Decision != Deny {
					t.Errorf("decision = %q", res.Decision)
				}
				if len(res.Errors) == 0 {
					t.Error("the failure must still be reported")
				}
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
