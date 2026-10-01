package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// countingBlobs counts what a recall pulls out of the blob store.
type countingBlobs struct {
	events.Blobs
	reads, bytes int
}

func (c *countingBlobs) Get(h core.Hash) ([]byte, error) {
	b, err := c.Blobs.Get(h)
	c.reads++
	c.bytes += len(b)
	return b, err
}

func newTool(t *testing.T, turns int, body func(i int) []core.Block) (*Tool, *tools.Env, *countingBlobs) {
	t.Helper()
	cb := &countingBlobs{Blobs: events.NewMemBlobs()}
	a := kv.NewArchive(cb)
	for i := 1; i <= turns; i++ {
		role := core.RoleUser
		if i%2 == 0 {
			role = core.RoleAssistant
		}
		if err := a.Put("be-1", core.Turn{ID: core.TurnID(i), Role: role, Blocks: body(i)}); err != nil {
			t.Fatal(err)
		}
	}
	env := (&tools.Env{Agent: "be-1", Blobs: cb, Handles: tools.NewHandles()}).Defaults()
	return New(a), env, cb
}

func call(t *testing.T, tool *Tool, env *tools.Env, in map[string]any) *tools.Result {
	t.Helper()
	raw, _ := json.Marshal(in)
	res, err := tool.Run(context.Background(), &tools.Call{ID: "r1", Name: "recall", Input: raw, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func text(i int) []core.Block { return []core.Block{core.Text(fmt.Sprintf("turn %d says hello", i))} }

func TestRecallTurnsRange(t *testing.T) {
	tool, env, _ := newTool(t, 10, text)
	res := call(t, tool, env, map[string]any{"turns": "t3-t5"})
	if res.IsError || !strings.Contains(res.Text, "turn 3 says hello") || !strings.Contains(res.Text, "turn 5 says hello") || strings.Contains(res.Text, "turn 6") {
		t.Fatalf("t3-t5: %v %q", res.IsError, res.Text)
	}
	for _, spec := range []string{"t7", "7", "t7-t7", "t7:t7"} {
		if res := call(t, tool, env, map[string]any{"turns": spec}); res.IsError || !strings.Contains(res.Text, "turn 7 says hello") {
			t.Errorf("%q: %v %q", spec, res.IsError, res.Text)
		}
	}
	for _, spec := range []string{"t9-t3", "soon", "t-1", "t1.x"} {
		if res := call(t, tool, env, map[string]any{"turns": spec}); !res.IsError {
			t.Errorf("%q should be refused: %q", spec, res.Text)
		}
	}
	if res := call(t, tool, env, map[string]any{"turns": "t50-t60"}); !res.IsError || !strings.Contains(res.Text, "no archived turns") {
		t.Errorf("an empty range: %v %q", res.IsError, res.Text)
	}
}

// A request for an enormous range is answered with the first page and a way to continue,
// and reads a page's worth of the archive, not all of it (S47).
func TestRecallHugeRangeIsPagedNotLoaded(t *testing.T) {
	body := strings.Repeat("output line of a test run\n", 400) // ~10 KB
	tool, env, cb := newTool(t, 1500, func(i int) []core.Block {
		return []core.Block{core.ToolResult("x", false, core.Text(fmt.Sprintf("t%d ", i)+body))}
	})
	res := call(t, tool, env, map[string]any{"turns": "t1-t99999999"})
	if res.IsError {
		t.Fatalf("error: %s", res.Text)
	}
	if cb.bytes > 4*env.Limits.MaxOutputChars+16_000 || cb.reads > maxRangeTurns {
		t.Fatalf("one recall read %d bytes in %d blobs of a %d MB archive", cb.bytes, cb.reads, 1500*len(body)>>20)
	}
	if len(res.Text) > env.Limits.MaxOutputChars+200 {
		t.Fatalf("the answer is %d chars against a limit of %d", len(res.Text), env.Limits.MaxOutputChars)
	}
	if !strings.Contains(res.Text, "── t1 user ──") {
		t.Fatalf("the first turn is missing: %.100s", res.Text)
	}
	// The truncated page says so; paging on works.
	if !strings.Contains(res.Text, "truncated") && !strings.Contains(res.Text, "call again") {
		t.Fatalf("a cut page must say how to continue: ...%s", res.Text[len(res.Text)-200:])
	}
	cb.reads, cb.bytes = 0, 0
	res = call(t, tool, env, map[string]any{"turns": "t1-t99999999", "limit": 500})
	if len(res.Text) > 500+200 || cb.bytes > 4*500+16_000 {
		t.Fatalf("a small limit must bound the read too: %d chars, %d bytes read", len(res.Text), cb.bytes)
	}

	// Many small turns: the turn cap applies and the reply names where to go on.
	small, env2, _ := newTool(t, 400, text)
	res = call(t, small, env2, map[string]any{"turns": "t1-t400"})
	if !strings.Contains(res.Text, fmt.Sprintf("turns=\"t%d-t400\"", maxRangeTurns+1)) {
		t.Fatalf("no continuation hint after %d turns:\n...%s", maxRangeTurns, res.Text[len(res.Text)-150:])
	}
	if strings.Count(res.Text, "says hello") != maxRangeTurns {
		t.Fatalf("%d turns returned, want %d", strings.Count(res.Text, "says hello"), maxRangeTurns)
	}
}

// "recall t13.7" is what mask and excerpt placeholders say: it returns that one result.
func TestRecallOneToolResult(t *testing.T) {
	tool, env, _ := newTool(t, 6, func(i int) []core.Block {
		return []core.Block{
			core.ToolResult("a", false, core.Text(fmt.Sprintf("turn %d first result", i))),
			core.ToolResult("b", false, core.Text(fmt.Sprintf("turn %d second result %s", i, strings.Repeat("z", 500)))),
		}
	})
	for _, spec := range []string{"t3.1", "3.1", "T3.1", "t3#1", " t3 . 1 "} {
		res := call(t, tool, env, map[string]any{"turns": spec})
		if res.IsError || !strings.Contains(res.Text, "turn 3 second result") || strings.Contains(res.Text, "first result") {
			t.Errorf("%q: %v %.120s", spec, res.IsError, res.Text)
		}
	}
	res := call(t, tool, env, map[string]any{"turns": "t3.0"})
	if res.IsError || !strings.Contains(res.Text, "turn 3 first result") {
		t.Errorf("t3.0: %v %q", res.IsError, res.Text)
	}
	// The output limit applies.
	res = call(t, tool, env, map[string]any{"turns": "t3.1", "limit": 60})
	if !strings.Contains(res.Text, "truncated") || len(res.Text) > 200 {
		t.Errorf("limit: %q", res.Text)
	}
	for _, spec := range []string{"t3.2", "t3.99", "t99.0", "t3.99999999999999999999"} {
		if res := call(t, tool, env, map[string]any{"turns": spec}); !res.IsError {
			t.Errorf("%q should be an error: %q", spec, res.Text)
		}
	}
	// A range with a dot is not a result reference.
	if res := call(t, tool, env, map[string]any{"turns": "t2-t4"}); res.IsError || !strings.Contains(res.Text, "── t4") {
		t.Errorf("t2-t4: %q", res.Text)
	}
}

func TestRecallSearchAndHandleStillWork(t *testing.T) {
	tool, env, _ := newTool(t, 6, func(i int) []core.Block {
		return []core.Block{core.Text(fmt.Sprintf("turn %d mentions token%d", i, i))}
	})
	res := call(t, tool, env, map[string]any{"query": "token4"})
	if res.IsError || !strings.Contains(res.Text, "t4 ") {
		t.Fatalf("search: %q", res.Text)
	}
	if res := call(t, tool, env, map[string]any{"query": "nothing-like-this"}); !strings.Contains(res.Text, "no matches") {
		t.Fatalf("search miss: %q", res.Text)
	}
	h, err := env.Blobs.Put([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	handle := env.Handles.Add(h, 16)
	res = call(t, tool, env, map[string]any{"handle": handle, "offset": 4, "limit": 6})
	if res.IsError || !strings.HasPrefix(res.Text, "456789") {
		t.Fatalf("handle page: %q", res.Text)
	}
	if res := call(t, tool, env, map[string]any{}); !res.IsError {
		t.Fatal("no argument should be an error")
	}
	if res := call(t, tool, env, map[string]any{"handle": "out_00000000"}); !res.IsError {
		t.Fatal("an unknown handle should be an error")
	}
}

// The tool reads the calling agent's archive only.
func TestRecallIsPerAgent(t *testing.T) {
	tool, env, _ := newTool(t, 4, text)
	env.Agent = "someone-else"
	if res := call(t, tool, env, map[string]any{"turns": "t1-t4"}); !res.IsError {
		t.Fatalf("another agent's turns were readable: %q", res.Text)
	}
	if res := call(t, tool, env, map[string]any{"turns": "t1.0"}); !res.IsError {
		t.Fatalf("another agent's result was readable: %q", res.Text)
	}
}
