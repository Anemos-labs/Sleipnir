package core_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

func TestBlockConstructors(t *testing.T) {
	in := json.RawMessage(`{"path":"a"}`)
	for _, c := range []struct {
		name string
		got  core.Block
		want core.Block
	}{
		{"Text", core.Text("hi"), core.Block{Kind: core.BlockText, Text: "hi"}},
		{"ToolUse", core.ToolUse("id1", "read", in), core.Block{Kind: core.BlockToolUse, ToolID: "id1", ToolName: "read", Input: in}},
		{"ToolResult ok", core.ToolResult("id1", false, core.Text("a"), core.Text("b")), core.Block{Kind: core.BlockToolResult, ToolID: "id1", Result: []core.Block{core.Text("a"), core.Text("b")}}},
		{"ToolResult error", core.ToolResult("id2", true, core.Text("x")), core.Block{Kind: core.BlockToolResult, ToolID: "id2", IsError: true, Result: []core.Block{core.Text("x")}}},
		{"ToolResult empty", core.ToolResult("id3", true), core.Block{Kind: core.BlockToolResult, ToolID: "id3", IsError: true}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}
}

func TestBlockPlainText(t *testing.T) {
	cases := []struct {
		name string
		b    core.Block
		want string
	}{
		{"text", core.Text("hello"), "hello"},
		{"thinking is its own text", core.Block{Kind: core.BlockThinking, Text: "hm"}, "hm"},
		{"redacted thinking has none", core.Block{Kind: core.BlockRedactedThinking, Text: "x", Wire: json.RawMessage(`{}`)}, ""},
		{"tool use has none", core.ToolUse("i", "n", json.RawMessage(`{"a":"b"}`)), ""},
		{"image has none", core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "ref"}, ""},
		{"compaction has none", core.Block{Kind: core.BlockCompaction, Text: "summary"}, ""},
		{"zero block", core.Block{}, ""},
		{"tool result joins its text", core.ToolResult("i", false, core.Text("a"), core.Text("b")), "ab"},
		{"tool result skips what is not text", core.ToolResult("i", false, core.Text("a"), core.Block{Kind: core.BlockImage}, core.Text("c")), "ac"},
		{"tool result without content", core.ToolResult("i", true), ""},
		{"nested tool results flatten", core.ToolResult("i", false, core.ToolResult("j", false, core.Text("x")), core.Text("y")), "xy"},
	}
	for _, c := range cases {
		if got := c.b.PlainText(); got != c.want {
			t.Errorf("%s: PlainText = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTurnPlainTextAndToolCalls(t *testing.T) {
	use1 := core.ToolUse("a", "read", json.RawMessage(`{}`))
	use2 := core.ToolUse("b", "bash", json.RawMessage(`{}`))
	cases := []struct {
		name      string
		turn      core.Turn
		wantText  string
		wantCalls []core.Block
	}{
		{"no blocks", core.Turn{}, "", nil},
		{"text only", core.Turn{Blocks: []core.Block{core.Text("a"), core.Text("b")}}, "ab", nil},
		{"reasoning is not part of the answer", core.Turn{Blocks: []core.Block{{Kind: core.BlockThinking, Text: "secret"}, core.Text("answer")}}, "answer", nil},
		{"calls in order", core.Turn{Blocks: []core.Block{core.Text("x"), use1, core.Text("y"), use2}}, "xy", []core.Block{use1, use2}},
		{"results count as text", core.Turn{Blocks: []core.Block{core.ToolResult("a", false, core.Text("out"))}}, "out", nil},
	}
	for _, c := range cases {
		if got := c.turn.PlainText(); got != c.wantText {
			t.Errorf("%s: PlainText = %q, want %q", c.name, got, c.wantText)
		}
		if got := c.turn.ToolCalls(); !reflect.DeepEqual(got, c.wantCalls) {
			t.Errorf("%s: ToolCalls = %+v, want %+v", c.name, got, c.wantCalls)
		}
	}
}
