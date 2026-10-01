package hooks

import (
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
// file_path added for file tools that call it path. Input that is not valid JSON
// is passed as a string rather than corrupting the payload.
func shapeInput(tool string, input json.RawMessage) any {
	if !json.Valid(input) {
		return string(input)
	}
	switch normTool(tool) {
	case "read", "write", "edit":
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(input, &obj); err == nil && obj != nil {
			if p, ok := obj["path"]; ok {
				if _, has := obj["file_path"]; !has {
					obj["file_path"] = p
					return obj
				}
			}
		}
	}
	return json.RawMessage(input)
}
