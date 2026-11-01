package main

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A tool row shows a line of its command and of its output; GET .../tools/{agent}/{tid} gives the whole of both, read from the
// session's log, and 404 for a call the log does not hold.
func TestWebToolDetailGivesTheWholeCommandAndOutput(t *testing.T) {
	project := webWorld(t)
	long := "echo " + strings.Repeat("abcdefghij", 40) + " && for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do echo line-$i-" + strings.Repeat("z", 60) + "; done && echo THE-END"
	_, base := newWebModel(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether") {
			return mock.Reply{Text: `{"verdict":"done","reason":"it is done","left":[]}`}
		}
		if c.Messages[len(c.Messages)-1].Role == "tool" {
			return mock.Reply{Text: "ran it"}
		}
		return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "b1", Name: "bash", Args: string(mustJSON(t, map[string]any{"command": long}))}}}
	})
	d := soloDefaults(project)
	r := newWebRig(t, d, base)
	tab := r.ready("")
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "run it"}, 200, nil)
	ev := r.waitEv(tab.ID, "tool", map[string]any{"tid": "b1"})
	if arg, _ := ev["arg"].(string); len(arg) >= len(long) {
		t.Fatalf("the row's arg is not clipped (%d characters): the test does not test what it says", len(arg))
	}
	r.waitEv(tab.ID, "final", nil)

	var d1 wire.ToolDetail
	r.json("GET", "/api/sessions/"+tab.ID+"/tools/mgr/b1", nil, 200, &d1)
	if d1.Tool != "bash" || d1.Name != "Bash" || d1.Arg != long || d1.Agent != "mgr" || d1.TID != "b1" || d1.Running || d1.Error {
		t.Fatalf("detail %+v", d1)
	}
	if !d1.HasOutput || !strings.Contains(d1.Output, "line-1-") || !strings.Contains(d1.Output, "line-20-") || !strings.Contains(d1.Output, "THE-END") {
		t.Fatalf("the output is not whole: %q", d1.Output)
	}
	if !strings.Contains(d1.Input, `"command":`) {
		t.Errorf("input %q", d1.Input)
	}
	if code, body := r.do("GET", "/api/sessions/"+tab.ID+"/tools/mgr/nope", nil); code != 404 || !strings.Contains(string(body), "no_call") {
		t.Errorf("an unknown call = %d %s", code, body)
	}
	if code, _ := r.do("GET", "/api/sessions/"+tab.ID+"/tools/be-1/b1", nil); code != 404 {
		t.Errorf("another agent's call = %d", code)
	}
}
