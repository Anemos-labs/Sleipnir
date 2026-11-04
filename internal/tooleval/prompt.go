package tooleval

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Variant is a set of tool descriptions tried in place of the build's: tool name to description. The empty variant is the
// build as it is.
type Variant struct {
	Name  string            `json:"name"`
	Tools map[string]string `json:"tools,omitempty"`
}

// Current is the variant that changes nothing.
const Current = "current"

// LoadVariants reads a variants file: a JSON object of variant name to {tool: description}. The current descriptions come
// first, then the file's variants in name order.
func LoadVariants(b []byte) ([]Variant, error) {
	var m map[string]map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("variants: %w (want {\"name\": {\"tool\": \"description\"}})", err)
	}
	out := []Variant{{Name: Current}}
	names := make([]string, 0, len(m))
	for n := range m {
		if n == Current {
			return nil, fmt.Errorf("variants: %q names the build's own descriptions", Current)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, Variant{Name: n, Tools: m[n]})
	}
	return out, nil
}

// Apply is tools with the variant's descriptions. A tool the variant names that is not among tools is an error: a typo would
// otherwise test the build's own description under the variant's name.
func (v Variant) Apply(tools []core.ToolSpec) ([]core.ToolSpec, error) {
	out := v.apply(tools)
	have := map[string]bool{}
	for _, t := range tools {
		have[t.Name] = true
	}
	for n := range v.Tools {
		if !have[n] {
			return nil, fmt.Errorf("variant %s: no tool is named %q", v.Name, n)
		}
	}
	return out, nil
}

// apply is tools with the variant's descriptions, whichever of its tools they have.
func (v Variant) apply(tools []core.ToolSpec) []core.ToolSpec {
	out := append([]core.ToolSpec(nil), tools...)
	for i, t := range out {
		if d, ok := v.Tools[t.Name]; ok {
			out[i].Description = d
		}
	}
	return out
}

// Build is the prompt case c sends: its recorded prompt, or base with its goal, board and turns, under variant v's
// descriptions of the tools it has. base is the prompt of c's role (Base); it is not changed.
func Build(base *core.Prompt, c Case, v Variant) (*core.Prompt, error) {
	var p core.Prompt
	switch {
	case c.Prompt != nil:
		p = *c.Prompt
		p.Messages = append([]core.Message(nil), c.Prompt.Messages...)
	case base == nil:
		return nil, fmt.Errorf("case %s: no base prompt for role %s", c.ID, roleOf(c))
	default:
		p = *base
		msgs, err := play(base.Messages, c)
		if err != nil {
			return nil, err
		}
		p.Messages = msgs
	}
	p.Tools = v.apply(p.Tools) // Run checks the variant's names against every prompt it asks
	p.Breakpoints = nil        // the eval's prompts are not the session's: nothing is planned for its cache
	return &p, nil
}

// roleOf is the role c is played as.
func roleOf(c Case) string {
	if c.Role == "" {
		return "manager"
	}
	return c.Role
}

// baseGoal is the objective Base starts its session with; a case's Goal takes its place.
const baseGoal = "Build the project the README describes."

// play is the base messages with c's goal and board in place, then c's turns.
func play(base []core.Message, c Case) ([]core.Message, error) {
	var msgs []core.Message
	for _, m := range base {
		m.Blocks = append([]core.Block(nil), m.Blocks...)
		for i, b := range m.Blocks {
			if b.Kind != core.BlockText {
				continue
			}
			switch {
			case c.Goal != "" && b.Text == baseGoal:
				m.Blocks[i].Text = c.Goal
			case c.Board != "" && strings.HasPrefix(b.Text, "<live "):
				m.Blocks[i].Text = c.Board
			}
		}
		msgs = append(msgs, m)
	}
	var pending []core.Block // the calls of the last assistant turn, waiting for their results
	for i, t := range c.Turns {
		switch {
		case t.User != "":
			msgs = appendUser(msgs, core.Text(t.User))
		case t.Assistant != "" || len(t.Calls) > 0:
			if len(pending) > 0 {
				return nil, fmt.Errorf("case %s: turn %d: the calls of the last assistant turn have no results", c.ID, i+1)
			}
			m := core.Message{Role: core.RoleAssistant}
			if t.Assistant != "" {
				m.Blocks = append(m.Blocks, core.Text(t.Assistant))
			}
			for j, call := range t.Calls {
				args := call.Args
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				b := core.ToolUse(fmt.Sprintf("call_%d_%d", i+1, j+1), call.Tool, args)
				m.Blocks = append(m.Blocks, b)
				pending = append(pending, b)
			}
			msgs = append(msgs, m)
		default:
			if len(t.Results) != len(pending) {
				return nil, fmt.Errorf("case %s: turn %d: %d results for %d calls", c.ID, i+1, len(t.Results), len(pending))
			}
			for j, r := range t.Results {
				msgs = appendUser(msgs, core.ToolResult(pending[j].ToolID, r.Error, core.Text(r.Text)))
			}
			pending = nil
		}
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("case %s: the calls of the last assistant turn have no results", c.ID)
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != core.RoleUser {
		return nil, fmt.Errorf("case %s: the last turn is the assistant's: the model has nothing to answer", c.ID)
	}
	return msgs, nil
}

// appendUser adds b to the last message when it is the user's (tool results and the harness text after them travel
// together), else in a new user message.
func appendUser(msgs []core.Message, b core.Block) []core.Message {
	if n := len(msgs); n > 0 && msgs[n-1].Role == core.RoleUser {
		msgs[n-1].Blocks = append(msgs[n-1].Blocks, b)
		return msgs
	}
	return append(msgs, core.Message{Role: core.RoleUser, Blocks: []core.Block{b}})
}

// Size is how much of every prompt a tool takes: its name, description and schema, in bytes.
func Size(t core.ToolSpec) int {
	return len(t.Name) + len(t.Description) + len(t.InputSchema)
}
