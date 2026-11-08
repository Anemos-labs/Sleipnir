package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tools/fs"
	"github.com/anemos-labs/sleipnir/internal/tools/shell"
	"github.com/anemos-labs/sleipnir/internal/tools/web"
)

type noSearch struct{}

func (noSearch) Search(context.Context, string, int) ([]web.SearchResult, error) { return nil, nil }

// harnessTools is the file, shell and web tools as a session registers them.
func harnessTools(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	fs.Register(reg)
	mgr := shell.NewManager()
	t.Cleanup(mgr.Shutdown)
	shell.Register(reg, mgr)
	web.Register(reg, web.Config{Backend: noSearch{}})
	return reg
}

// Models trained on other harnesses call their tools by those harnesses' names. The same name in another case or separator
// convention runs as the tool; a usual alias runs when its arguments are the tool's own and is left as an unknown tool otherwise.
func TestResolveToolNameRunsOtherHarnessesNames(t *testing.T) {
	reg := harnessTools(t)
	cases := []struct{ name, input, want, why string }{
		{"read", `{"path":"a"}`, "read", "already a tool"},
		{"functions.read", `{}`, "read", "a namespace is cut off as before"},
		{"Read", `{"file_path":"a"}`, "read", "Claude Code's capitalised name"},
		{"Bash", `{"command":"ls","timeout":120000}`, "bash", "Claude Code"},
		{"WebFetch", `{"url":"https://x","prompt":"summarise"}`, "web_fetch", "a folded name is the same tool: its arguments are not judged"},
		{"WebSearch", `{"query":"go"}`, "web_search", "Claude Code"},
		{"BashOutput", `{"id":"job_1"}`, "bash_output", "Claude Code"},
		{"functions.Grep", `{"pattern":"x"}`, "grep", "namespace and case"},
		{"apply-patch", `{"patch":"x"}`, "apply_patch", "another separator"},
		{"read_file", `{"file_path":"a","offset":1,"limit":20}`, "read", "Gemini CLI and Qwen Code, with a synonym of path"},
		{"read_file", `{"path":"a"}`, "read", "Codex-style"},
		{"ReadFile", `{"path":"a"}`, "read", "an alias in another convention"},
		{"write_file", `{"file_path":"a","content":"x"}`, "write", "Gemini CLI"},
		{"edit_file", `{"path":"a","old_string":"x","new_string":"y"}`, "edit", "an edit"},
		{"MultiEdit", `{"file_path":"a","edits":[{"old_string":"x","new_string":"y"}]}`, "edit", "Claude Code's multi-edit is edit's edits"},
		{"run_shell_command", `{"command":"ls","description":"list"}`, "bash", "Gemini CLI"},
		{"fetch_url", `{"url":"https://x"}`, "web_fetch", "a usual name for fetching a page"},
		{"google_web_search", `{"query":"go"}`, "web_search", "Gemini CLI"},
		{"KillShell", `{"id":"job_1"}`, "bash_kill", "Claude Code's name for stopping a job"},
		{"list_directory", `{"path":"."}`, "ls", "Gemini CLI"},
		{"read_file", `"{\"path\":\"a\"}"`, "read", "arguments handed over as a JSON string"},
		{"fetch", `{"url":"https://x","max_length":5000}`, "fetch", "an argument web_fetch does not take: not run"},
		{"shell", `{"command":["bash","-lc","ls"]}`, "shell", "Codex's command is an array: not run"},
		{"replace", `{"file_path":"a","old_string":"x","new_string":"y","expected_replacements":2}`, "replace", "Gemini's replace counts replacements: not run"},
		{"read_file", `{"absolute_path":"a"}`, "read_file", "an argument read does not take"},
		{"read_file", `{}`, "read_file", "a required argument is missing"},
		{"read_file", `[1]`, "read_file", "arguments that are not an object"},
		{"search", `{"query":"go"}`, "search", "grep's arguments are not these"},
		{"todo_write", `{"todos":[]}`, "todo_write", "an alias of a tool this agent does not have"},
		{"nothing", `{}`, "nothing", "no tool"},
	}
	for _, c := range cases {
		if got := resolveToolName(reg.Get, reg.Names, c.name, json.RawMessage(c.input)); got != c.want {
			t.Errorf("resolveToolName(%q, %s) = %q, want %q (%s)", c.name, c.input, got, c.want, c.why)
		}
	}
}

// A folded name that is more than one tool's is not guessed between.
func TestFoldedToolIsNotGuessedBetweenTwo(t *testing.T) {
	if got := foldedTool([]string{"web_fetch", "webfetch"}, "WebFetch"); got != "" {
		t.Errorf("foldedTool = %q, want none", got)
	}
	if got := foldedTool([]string{"web_fetch", "read"}, "WebFetch"); got != "web_fetch" {
		t.Errorf("foldedTool = %q, want web_fetch", got)
	}
}

// Every alias names a tool the harness has, and no alias is a tool's own name (it would never be consulted).
func TestToolAliasesNameHarnessTools(t *testing.T) {
	reg := harnessTools(t)
	own := map[string]bool{}
	for _, n := range reg.Names() {
		own[foldToolName(n)] = true
	}
	for tool, names := range toolAliasNames {
		if _, ok := reg.Get(tool); !ok && tool != "plan" {
			t.Errorf("aliases of %q, which is not a tool", tool)
		}
		for _, n := range names {
			if own[foldToolName(n)] {
				t.Errorf("alias %q of %q is a tool's own name", n, tool)
			}
		}
	}
}
