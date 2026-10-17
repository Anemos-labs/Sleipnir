package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/tools"
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

// Some models address an action of a tool as a tool of its own ("task.get"). Where the tool's schema has that action, the call
// runs as the tool with the action in its input; a call whose input names another action is not guessed at, nor is an action the
// tool does not have.
func TestAToolActionWrittenAsAToolNameRuns(t *testing.T) {
	board := fakeTool{name: "board", schema: `{"type":"object","properties":{"action":{"type":"string","enum":["get","list"]},"id":{"type":"string"}},"required":["action"]}`,
		run: func(in json.RawMessage) *tools.Result { return &tools.Result{Text: "board:" + string(in)} }}
	r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{board}}, func(c *mock.Call) mock.Reply {
		if len(c.Messages) < 4 {
			return mock.Reply{ToolCalls: []mock.ToolCall{
				{ID: "d1", Name: "board.get", Args: `{"id":"T1"}`},
				{ID: "d2", Name: "functions.board.list", Args: `{}`},
				{ID: "d3", Name: "board.get", Args: `{"action":"get","id":"T2"}`},
				{ID: "d4", Name: "board.get", Args: `{"action":"list"}`},
				{ID: "d5", Name: "board.close", Args: `{"id":"T1"}`},
			}}
		}
		return mock.Reply{Text: "done"}
	})
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	results := r.agent.Thread().Snapshot().Turns[2].Blocks
	for i, want := range []string{`board:{"action":"get","id":"T1"}`, `board:{"action":"list"}`, `board:{"action":"get","id":"T2"}`} {
		if results[i].IsError || results[i].PlainText() != want {
			t.Errorf("call %d = %+v, want %q", i+1, results[i], want)
		}
	}
	for i, name := range []string{"board.get", "board.close"} {
		if b := results[3+i]; !b.IsError || !strings.Contains(b.PlainText(), `unknown tool "`+name+`"`) {
			t.Errorf("call %d (%s) was guessed at: %+v", 4+i, name, b)
		}
	}
	n := 0
	for _, e := range r.log.OfType("tool.call") {
		if strings.Contains(string(e.Data), `"as":"board"`) {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d tool.call events say the board tool ran, want 3", n)
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

// toolSink records the names of the tools a person is shown running.
type toolSink struct {
	agent.NopSink
	mu    sync.Mutex
	names []string
}

func (s *toolSink) ToolStart(_ string, c core.Block) { s.add("start " + c.ToolName) }
func (s *toolSink) ToolEnd(_ string, c core.Block, _ *tools.Result, _ time.Duration) {
	s.add("end " + c.ToolName)
}
func (s *toolSink) add(n string) { s.mu.Lock(); s.names = append(s.names, n); s.mu.Unlock() }

// The person is shown the tool that ran, not the name with a piece of the model's chat format stuck to it ("task<|channel|>commentary").
func TestAPersonIsShownTheToolThatRanNotTheLeakedName(t *testing.T) {
	sink := &toolSink{}
	r := newRig(t, rigOpts{noCompact: true, sink: sink}, func(c *mock.Call) mock.Reply {
		if len(c.Messages) < 4 {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "h1", Name: "echo<|channel|>commentary", Args: `{"q":"one"}`}}}
		}
		return mock.Reply{Text: "done"}
	})
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if strings.Join(sink.names, ",") != "start echo,end echo" {
		t.Errorf("the sink was shown %q, want the tool that ran", sink.names)
	}
}
