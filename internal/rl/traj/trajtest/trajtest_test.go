package trajtest

import (
	"encoding/json"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// The wire form of a tool call is JSON whatever the tool's name and id hold: Go's %q quoting writes \a and \x7f, which JSON does not
// have, so the form is marshalled, not formatted.
func TestWireOfAToolCallIsJSONWhateverTheNameHolds(t *testing.T) {
	r := New()
	a := r.Agent("mgr", "manager", "m")
	a.User("go")
	const name, id = "to\aol\u2028\"x\x7f", "id\x00<&>"
	a.Step(Call{Text: "x", Tool: name, ToolID: id, Input: `{"a":1}`, Result: "ok"})
	var wire json.RawMessage
	for _, turn := range a.turns {
		for _, b := range turn.Blocks {
			if b.Kind == core.BlockToolUse {
				wire = b.Wire
			}
		}
	}
	if !json.Valid(wire) {
		t.Fatalf("the wire form is not JSON: %q", wire)
	}
	var got struct {
		Function struct {
			Arguments string `json:"arguments"`
			Name      string `json:"name"`
		} `json:"function"`
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if got.Function.Name != name || got.ID != id || got.Type != "function" || got.Function.Arguments != `{"a":1}` {
		t.Errorf("read back %+v", got)
	}
}
