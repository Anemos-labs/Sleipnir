package reward

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// toolCall is one tool invocation of a step, normalised across how the episode
// recorded it: as an Observation (input and output as the agent saw them) or,
// for episodes built without observations, as a tool_use block of the
// completion.
type toolCall struct {
	ai, si  int
	step    *rl.Step
	name    string // normalised tool name (see normTool)
	raw     string // tool name as recorded
	input   json.RawMessage
	output  string
	isError bool
	result  bool // a result was recorded
	invalid bool // the model's arguments were malformed (core.Block.Invalid)
}

// normTool lower-cases a tool name and drops separators, so "apply_patch",
// "ApplyPatch" and "apply-patch" compare equal.
func normTool(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var (
	writeTools = setOf("write", "edit", "multiedit", "applypatch", "patch", "strreplaceeditor", "strreplacebasededittool",
		"strreplace", "createfile", "writefile", "editfile", "fileedit", "filewrite", "notebookedit", "insert", "replaceinfile")
	shellTools = setOf("bash", "shell", "sh", "runcommand", "execute", "exec", "terminal", "runshellcommand", "bashexec", "command")
	webTools   = setOf("webfetch", "fetch", "websearch", "browse", "httpget", "httprequest", "curl", "fetchurl", "openurl")
)

// setOf builds a membership map with duplicate names collapsed.
func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// isWrite reports whether the tool name belongs to the configured write-tool set.
func (c toolCall) isWrite() bool { return writeTools[c.name] }

// isShell reports whether the tool name belongs to the shell-tool set.
func (c toolCall) isShell() bool { return shellTools[c.name] }

// isWeb reports whether the tool name belongs to the web-tool set.
func (c toolCall) isWeb() bool { return webTools[c.name] }

// callsOf lists a step's tool calls in order.
func callsOf(ai, si int, st *rl.Step) []toolCall {
	var out []toolCall
	if len(st.Observations) > 0 {
		for _, o := range st.Observations {
			out = append(out, toolCall{ai: ai, si: si, step: st, name: normTool(o.Name), raw: o.Name,
				input: o.Input, output: o.Output, isError: o.IsError, result: true})
		}
		return out
	}
	for _, b := range st.Completion.Turn.Blocks {
		if b.Kind == core.BlockToolUse {
			out = append(out, toolCall{ai: ai, si: si, step: st, name: normTool(b.ToolName), raw: b.ToolName,
				input: b.Input, invalid: b.Invalid != ""})
		}
	}
	return out
}

// allCalls lists every tool call of the episode in agent, step order.
func allCalls(ep *rl.Episode) []toolCall {
	var out []toolCall
	for ai := range ep.Agents {
		for si := range ep.Agents[ai].Steps {
			out = append(out, callsOf(ai, si, &ep.Agents[ai].Steps[si])...)
		}
	}
	return out
}

// inputMap decodes a tool input object; nil when it is not one.
func inputMap(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// inputStrings returns the string (or array-of-string) values under the first of
// keys that is present, tolerating models that use another spelling.
func inputStrings(raw json.RawMessage, keys ...string) []string {
	m := inputMap(raw)
	if m == nil {
		return nil
	}
	var out []string
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			out = append(out, s)
			continue
		}
		var arr []string
		if json.Unmarshal(v, &arr) == nil {
			out = append(out, arr...)
		}
	}
	return out
}

// inputString returns the first extracted string from the supported input fields or empty when
// none exists.
func inputString(raw json.RawMessage, keys ...string) string {
	if s := inputStrings(raw, keys...); len(s) > 0 {
		return s[0]
	}
	return ""
}

var pathKeys = []string{"path", "file_path", "filepath", "file", "filename", "target_file", "notebook_path", "paths", "files", "new_path", "old_path"}

// writtenPaths lists the file paths a write-type call names: path arguments, and
// the file headers of an apply_patch or unified-diff body.
func writtenPaths(c toolCall) []string {
	paths := inputStrings(c.input, pathKeys...)
	for _, body := range inputStrings(c.input, "patch", "input", "diff", "patch_text", "content_patch") {
		paths = append(paths, patchPaths(body)...)
	}
	if len(paths) == 0 {
		// An apply_patch input may be the bare patch text rather than an object.
		var s string
		if json.Unmarshal(c.input, &s) == nil {
			paths = append(paths, patchPaths(s)...)
		}
	}
	return uniqueStrings(paths)
}

// patchPaths extracts the paths a patch body touches, understanding the
// "*** Update File:" envelope of the apply_patch tool and unified diff headers.
func patchPaths(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, pre := range []string{"*** Update File:", "*** Add File:", "*** Delete File:", "*** Move to:"} {
			if strings.HasPrefix(line, pre) {
				out = append(out, strings.TrimSpace(strings.TrimPrefix(line, pre)))
			}
		}
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			p := strings.TrimSpace(line[4:])
			if i := strings.IndexByte(p, '\t'); i >= 0 {
				p = p[:i]
			}
			if p != "" && p != "/dev/null" {
				out = append(out, p)
			}
		}
	}
	return out
}

// commandOf reads a command string from the supported command, cmd, script, or code input fields.
func commandOf(c toolCall) string { return inputString(c.input, "command", "cmd", "script", "code") }

// uniqueStrings returns distinct nonempty strings in input order without overwriting input
// elements.
func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
