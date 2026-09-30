package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/provider/mock"
)

// A model served through a gateway can leak its chat format into a tool name ("read<|channel|>commentary"). The call is clear;
// the dispatcher runs the tool it means, the thread keeps the name as the model wrote it, and the event says which tool ran.
func TestAToolNameWithChatFormatArtifactsStillRuns(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		if len(c.Messages) < 4 {
			return mock.Reply{ToolCalls: []mock.ToolCall{
				{ID: "h1", Name: "echo<|channel|>commentary", Args: `{"q":"one"}`},
				{ID: "h2", Name: "functions.echo", Args: `{"q":"two"}`},
			}}
		}
		return mock.Reply{Text: "done"}
	})
	res, err := r.agent.Run(context.Background(), "go")
	if err != nil || res.Text != "done" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	snap := r.agent.Thread().Snapshot()
	calls := snap.Turns[1].Blocks
	results := snap.Turns[2].Blocks
	for i, want := range []string{`echo:{"q":"one"}`, `echo:{"q":"two"}`} {
		if results[i].IsError || results[i].PlainText() != want {
			t.Errorf("result %d = %+v, want the echo tool's answer %q", i, results[i], want)
		}
	}
	if calls[0].ToolName != "echo<|channel|>commentary" || calls[1].ToolName != "functions.echo" {
		t.Errorf("the thread must keep the names as the model wrote them: %q, %q", calls[0].ToolName, calls[1].ToolName)
	}
	n := 0
	for _, e := range r.log.OfType("tool.call") {
		if strings.Contains(string(e.Data), `"as":"echo"`) {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d tool.call events say which tool ran, want 2", n)
	}
}

func TestAnUnknownToolNameGetsTheToolsAndASuggestion(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		if len(c.Messages) < 4 {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "u1", Name: "search", Args: `{}`}}}
		}
		return mock.Reply{Text: "ok"}
	})
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	b := r.agent.Thread().Snapshot().Turns[2].Blocks[0]
	txt := b.PlainText()
	if !b.IsError || !strings.Contains(txt, `unknown tool "search"`) || !strings.Contains(txt, "The tools are: big, echo.") {
		t.Fatalf("the message should list the tools: %+v", b)
	}
}
