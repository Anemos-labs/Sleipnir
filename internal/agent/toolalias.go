package agent

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

// toolAliasNames are the names other harnesses give what Sleipnir's tools do (Claude Code, Codex, Gemini CLI and Qwen Code,
// OpenCode, Cursor, Cline, OpenHands, SWE-agent), which models trained on their transcripts call by habit. A call under one of
// these names runs as the tool when its arguments are the tool's own (aliasFits); a borrowed name whose arguments mean something
// else (Codex's shell takes its command as an array, Gemini's replace an expected_replacements) is only suggested, in the answer
// that lists the tools. Names are compared folded (foldToolName), so read_file also covers ReadFile and read-file.
var toolAliasNames = map[string][]string{
	"grep": {"search", "grep_search", "ripgrep", "rg", "find_in_files", "search_file_content", "grep_files", "search_files",
		"search_code", "code_search"},
	"read":  {"read_file", "open_file", "open", "cat", "view", "view_file", "read_text_file"},
	"write": {"write_file", "create_file", "create", "save_file", "write_to_file"},
	"edit": {"edit_file", "str_replace", "str_replace_editor", "str_replace_based_edit_tool", "replace", "replace_in_file",
		"multi_edit", "search_replace"},
	"bash": {"run", "shell", "sh", "run_command", "execute", "execute_bash", "execute_command", "terminal", "exec",
		"run_shell_command", "shell_command", "run_terminal_cmd", "local_shell", "command"},
	"ls":          {"list_files", "list_dir", "list_directory", "listdir", "dir", "list"},
	"glob":        {"find_files", "file_search", "find", "glob_files", "find_by_name"},
	"web_search":  {"search_web", "google_search", "google_web_search", "internet_search", "brave_search", "search_internet"},
	"web_fetch":   {"fetch_url", "fetch", "url_fetch", "fetch_webpage", "fetch_web_page", "read_url", "open_url", "browse", "http_get", "get_url", "read_webpage"},
	"bash_output": {"job_output", "get_job_output", "read_output"},
	"bash_kill":   {"kill_shell", "kill_bash", "kill_job", "kill_process", "stop_job"},
	"apply_patch": {"patch"},
	"plan":        {"todo_write", "write_todos", "update_plan"},
}

// toolAliases maps each folded alias to its tool.
var toolAliases = func() map[string]string {
	m := map[string]string{}
	for tool, names := range toolAliasNames {
		for _, n := range names {
			m[foldToolName(n)] = tool
		}
	}
	return m
}()

// foldToolName is a tool name with case and word separators taken out: WebFetch, web_fetch and web-fetch are one name.
func foldToolName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch r {
		case '_', '-', ' ':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// foldedTool is the one tool among names whose folded name is name's ("Read", "WebFetch", "BashOutput", "apply-patch"), or "" when
// none is or more than one is. It is the same name written to another convention, so nothing is guessed.
func foldedTool(names []string, name string) string {
	f := foldToolName(name)
	if f == "" {
		return ""
	}
	found := ""
	for _, n := range names {
		if foldToolName(n) == f {
			if found != "" {
				return ""
			}
			found = n
		}
	}
	return found
}

// resolveToolName is the tool a call's name means. The name itself when it is a tool; else the name with the pieces of a chat
// format cut off (repairToolName); else the same name in another case or separator convention (foldedTool); else the tool of a
// usual alias (toolAliases), when the call's arguments are that tool's (aliasFits). The name is returned unchanged otherwise, and
// the call is answered as an unknown tool, with a suggestion.
func resolveToolName(get func(string) (tools.Tool, bool), names func() []string, name string, input json.RawMessage) string {
	has := func(n string) bool { _, ok := get(n); return ok }
	if r := repairToolName(has, name); r != name || has(r) {
		return r
	}
	bare := BareToolName(name)
	if bare == "" {
		return name
	}
	all := names()
	if t := foldedTool(all, bare); t != "" {
		return t
	}
	if t, ok := toolAliases[foldToolName(bare)]; ok {
		if tool, has := get(t); has && aliasFits(tool, input) {
			return t
		}
	}
	return name
}

// aliasFits reports whether input is arguments tool understands: an object whose every key is a property of the tool's schema
// (or a synonym of one, tools.ArgSynonyms) holding a value of the property's JSON type, and which has every required property.
// Under a borrowed name, keys the tool does not know are what the other harness's tool means differently, so the call is not run.
func aliasFits(tool tools.Tool, input json.RawMessage) bool {
	var schema struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(tool.Spec().InputSchema, &schema) != nil {
		return false
	}
	raw := bytes.TrimSpace(input)
	if len(raw) > 0 && raw[0] == '"' { // arguments handed over as a JSON string holding the object
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return false
		}
		raw = bytes.TrimSpace([]byte(s))
	}
	args := map[string]json.RawMessage{}
	if len(raw) > 0 && json.Unmarshal(raw, &args) != nil {
		return false
	}
	var syn map[string]string
	if s, ok := tool.(tools.ArgSynonyms); ok {
		syn = s.ArgSynonyms()
	}
	present := map[string]bool{}
	for k, v := range args {
		prop := k
		if _, ok := schema.Properties[k]; !ok {
			prop = syn[k]
		}
		p, ok := schema.Properties[prop]
		if prop == "" || !ok || !jsonTypeIs(p.Type, v) {
			return false
		}
		present[prop] = true
	}
	for _, r := range schema.Required {
		if !present[r] {
			return false
		}
	}
	return true
}

// jsonTypeIs reports whether v is a value of the JSON schema type t (a name or a list of names); an absent type admits anything.
func jsonTypeIs(t any, v json.RawMessage) bool {
	var want []string
	switch t := t.(type) {
	case nil:
		return true
	case string:
		want = []string{t}
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				want = append(want, s)
			}
		}
	}
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return false
	}
	got := ""
	switch v[0] {
	case '"':
		got = "string"
	case '{':
		got = "object"
	case '[':
		got = "array"
	case 't', 'f':
		got = "boolean"
	case 'n':
		got = "null"
	default:
		got = "number"
	}
	for _, w := range want {
		switch {
		case w == got:
			return true
		case w == "integer" && got == "number":
			var f float64
			if json.Unmarshal(v, &f) == nil && f == float64(int64(f)) {
				return true
			}
		case w == "number" && got == "string", w == "integer" && got == "string", w == "boolean" && got == "string":
			// the tools read numbers and flags written as strings ("30", "true"); so does a call under a borrowed name
			return true
		}
	}
	return false
}
