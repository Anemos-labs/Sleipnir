package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// reservedKeys are the payload fields the runner owns; Event.Extra cannot
// override them, so a caller cannot make one field say something another
// (say the tool name) contradicts.
var reservedKeys = map[string]bool{
	"session_id": true, "cwd": true, "hook_event_name": true, "tool_name": true, "sleipnir_tool_name": true,
	"tool_input": true, "tool_response": true, "agent": true, "agent_id": true, "role": true, "agent_type": true,
	"payload_truncated": true,
}

// payload builds the JSON a hook reads on stdin: the fields Claude Code hooks
// expect (session_id, cwd, hook_event_name, tool_name, tool_input, tool_response,
// agent_id, agent_type) plus Sleipnir's own (agent, role) and Event.Extra. Keys
// are sorted, so equal events give equal bytes.
//
// tool_name is the Claude Code name of the tool ("Bash" for bash), because
// hooks written for Claude Code compare it; the tool's own name follows in
// sleipnir_tool_name when it differs. tool_input gets a file_path where the
// tool calls it path, for the same reason: a hook that reads
// .tool_input.file_path and finds nothing would silently allow everything.
func (r *Runner) payload(event string, ev Event) ([]byte, error) {
	cwd := ev.Cwd
	if cwd == "" {
		cwd = r.Dir
	}
	sid := ev.SessionID
	if sid == "" {
		sid = r.SessionID
	}
	m := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": event}
	if ev.Tool != "" {
		m["tool_name"] = claudeToolName(ev.Tool)
		if claudeToolName(ev.Tool) != ev.Tool {
			m["sleipnir_tool_name"] = ev.Tool
		}
	}
	if len(ev.Input) > 0 {
		m["tool_input"] = shapeInput(ev.Tool, ev.Input)
	}
	if ev.Output != "" {
		m["tool_response"] = ev.Output
	}
	if ev.Agent != "" {
		m["agent"], m["agent_id"] = ev.Agent, ev.Agent
	}
	if ev.Role != "" {
		m["role"], m["agent_type"] = ev.Role, ev.Role
	}
	for k, v := range ev.Extra {
		if !reservedKeys[k] {
			m[k] = v
		}
	}
	b, err := core.MarshalStable(m)
	if err != nil {
		return nil, fmt.Errorf("hooks: the event cannot be encoded as JSON: %w", err)
	}
	limit := r.maxPayload()
	if len(b) <= limit {
		return b, nil
	}

	// Too big for a hook to read comfortably. Shrink the parts that are bulk and
	// say so in the payload, so that a hook can tell it is not seeing everything.
	m["payload_truncated"] = true
	if resp, ok := m["tool_response"].(string); ok && len(resp) > 4<<10 {
		m["tool_response"] = capText(resp, 4<<10)
	}
	if b, err = core.MarshalStable(m); err == nil && len(b) <= limit {
		return b, nil
	}
	if in, ok := m["tool_input"]; ok {
		raw, _ := core.MarshalStable(in)
		m["tool_input"] = map[string]any{"truncated": true, "bytes": len(raw)}
	}
	if b, err = core.MarshalStable(m); err == nil && len(b) <= limit {
		return b, nil
	}
	for k := range m { // the bulk is in Extra: keep only what the runner owns
		if !reservedKeys[k] {
			delete(m, k)
		}
	}
	return core.MarshalStable(m)
}

// shapeInput returns the tool input as a value to embed: the JSON as it is, with
// file_path set, for file tools that call it path, to the file the tool acts on.
// Input that is not valid JSON is passed as a string rather than corrupting the
// payload.
func shapeInput(tool string, input json.RawMessage) any {
	if !json.Valid(input) {
		return string(input)
	}
	switch normTool(tool) {
	case "read", "write", "edit":
		if obj := inputObject(input); obj != nil {
			if p := effectivePath(obj); p != "" {
				obj["file_path"], _ = json.Marshal(p)
				return obj
			}
		}
	}
	return json.RawMessage(input)
}

// inputObject decodes a tool input as the tools do: an object, or a JSON string that holds one (some providers send arguments
// so). It is nil for anything else.
func inputObject(input json.RawMessage) map[string]json.RawMessage {
	raw := bytes.TrimSpace(input)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = bytes.TrimSpace([]byte(s))
		}
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	return obj
}

// inputText is the string member key of in, "" when it is absent or not a string.
func inputText(in map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := in[key]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// effectivePath is the file a file tool acts on, by the tools' own precedence: path when it is set, else file_path.
func effectivePath(in map[string]json.RawMessage) string {
	if p := inputText(in, "path"); p != "" {
		return p
	}
	return inputText(in, "file_path")
}
