package tooleval

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
)

// Harvest turns the failed tool calls of a session kept with --training-logs into cases: each replays the exact prompt of
// the request whose answer made the call. A call to a tool the prompt did not have, or with arguments its schema refused,
// is expected to be made properly next time (every call is checked for that); any other failed call is expected not to be
// repeated as it was. The expectations are a draft: a case is worth more once its rules say what the right call was.
func Harvest(dir string) ([]Case, error) {
	r, err := traj.Open(dir)
	if err != nil {
		return nil, err
	}
	type call struct {
		name  string
		input json.RawMessage
		req   string
	}
	lastReq := map[string]string{}
	calls := map[string]call{}
	seen := map[string]bool{}
	var out []Case
	var skipped []string
	session := filepath.Base(filepath.Clean(dir))
	for _, e := range r.Events() {
		switch e.Type {
		case events.TypeModelResponse:
			var d struct{ Req string }
			if json.Unmarshal(e.Data, &d) == nil && d.Req != "" {
				lastReq[e.Agent] = d.Req
			}
		case events.TypeToolCall:
			var d struct {
				ID, Name string
				Input    json.RawMessage
			}
			if json.Unmarshal(e.Data, &d) == nil {
				calls[d.ID] = call{name: d.Name, input: d.Input, req: lastReq[e.Agent]}
			}
		case events.TypeToolResult:
			var d struct {
				ID    string
				Error bool
				Meta  map[string]any
			}
			if json.Unmarshal(e.Data, &d) != nil || !d.Error {
				continue
			}
			c, ok := calls[d.ID]
			if !ok || c.req == "" {
				continue
			}
			key := c.req + " " + c.name + " " + string(c.input)
			if seen[key] {
				continue
			}
			seen[key] = true
			p, err := r.Prompt(c.req)
			if err != nil {
				skipped = append(skipped, c.req)
				continue
			}
			kind, _ := d.Meta["error_kind"].(string)
			cs := Case{
				ID:     fmt.Sprintf("%s-%s-%d", session, e.Agent, e.Seq),
				Role:   roleOfAgent(e.Agent),
				Source: fmt.Sprintf("session %s, request %s", session, c.req),
				Prompt: p,
			}
			switch kind {
			case "unknown_tool", "invalid_input", "invalid_arguments", "invalid":
				cs.Failure = fmt.Sprintf("%s: a call the harness refused (%s): %s", c.name, kind, clip(string(c.input), 200))
				cs.Expect = Expect{MinCalls: 1}
			default:
				cs.Failure = fmt.Sprintf("%s failed%s: %s", c.name, paren(kind), clip(string(c.input), 200))
				cs.Expect = Expect{None: []Rule{{Tool: c.name, Args: exactArgs(c.input), Why: "the call failed in the session"}}}
			}
			out = append(out, cs)
		}
	}
	if len(out) == 0 && len(skipped) > 0 {
		return nil, fmt.Errorf("tooleval: %s: the prompts of %d requests with failed calls cannot be rebuilt (was the session kept with --training-logs?)", dir, len(skipped))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// paren is s in parentheses after a space, "" for "".
func paren(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

// roleOfAgent is the role a request label's agent id names: "mgr" the manager, "main" a solo session, else the id's role
// prefix (a worker "be-1" is "be").
func roleOfAgent(id string) string {
	switch id {
	case "mgr", "":
		return "manager"
	case "main":
		return "solo"
	}
	if i := regexp.MustCompile(`-\d+$`).FindStringIndex(id); i != nil {
		return id[:i[0]]
	}
	return id
}

// exactArgs is a rule's arguments that match input: each string as an anchored literal, an array as one holding each of its
// elements, anything else as JSON.
func exactArgs(input json.RawMessage) map[string]any {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil || len(in) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range in {
		switch w := v.(type) {
		case string:
			out[k] = "^" + regexp.QuoteMeta(w) + "$"
		case []any:
			// an array rule matches an array that holds a match for each element: strings as literals, the rest as JSON
			var a []any
			for _, x := range w {
				if s, ok := x.(string); ok {
					a = append(a, "^"+regexp.QuoteMeta(s)+"$")
				} else {
					a = append(a, x)
				}
			}
			out[k] = a
		default:
			out[k] = w
		}
	}
	return out
}
