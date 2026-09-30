package openaichat

// Adversarial review tests (lens: prompt-cache economics and correctness).
// See docs/reviews/cache-economics.md. Convention: tests PASS while the defect
// is present; REVIEW_STRICT=1 inverts.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/kv"
)

func cxBug(t *testing.T, present bool, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if os.Getenv("REVIEW_STRICT") != "" {
		if present {
			t.Fatalf("DEFECT PRESENT: %s", msg)
		}
		return
	}
	if !present {
		t.Fatalf("defect no longer reproduces (%s): invert or delete this review test", msg)
	}
	t.Logf("DEFECT CONFIRMED: %s", msg)
}

func cxRenderPrompt(t *testing.T, lastIsToolResult bool) *core.Prompt {
	t.Helper()
	est := core.NewBytesEstimator().WithRatio(4)
	tools, err := kv.SortTools([]core.ToolSpec{{Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	s := &kv.Stack{Agent: "a", Role: "r", Model: "anthropic/claude-opus-5-5", Tools: tools,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("abcd", 3000)}}),
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "p", Text: strings.Repeat("abcd", 2000), Vol: kv.VolEpoch}}),
		RoleL:  kv.NewLayer("role", kv.KindRole, 1, []kv.Segment{{Key: "r", Text: strings.Repeat("abcd", 1800), Vol: kv.VolEpoch}}),
		Notes:  kv.NewLayer("notes", kv.KindNotes, 1, []kv.Segment{{Key: "facts", Text: strings.Repeat("abcd", 1700), Vol: kv.VolSlow}}),
	}
	th := kv.NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("build it")}})
	th.Append(core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("ok"), core.ToolUse("c1", "bash", json.RawMessage(`{}`))}})
	if lastIsToolResult {
		th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("done"))}})
	} else {
		th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("done")), core.Text("[mail] fyi")}})
	}
	s.Thread = th.Snapshot()
	caps := kv.Caps{Dialect: Dialect, MaxBreakpoints: 4, LookbackBlocks: 20, MinPrefixTokens: 512}
	return kv.Render(s, kv.RenderOpts{Caps: caps, Policy: kv.DefaultPolicy(), Est: est}).Prompt
}

// With CacheControlParts (the documented way to reach Anthropic models through a
// chat-completions gateway) the marker for the rolling "thread" breakpoint lands
// on the last persistent block. In every tool loop that block is a tool_result,
// which this adapter renders as a role=tool message whose content is a plain
// string; markers are looked up only for text parts. The thread marker is dropped
// silently, so the conversation itself is never cached explicitly.
func TestCacheEcon_RollingBreakpointOnToolResultIsDropped(t *testing.T) {
	p := cxRenderPrompt(t, true)
	var labels []string
	for _, b := range p.Breakpoints {
		labels = append(labels, b.Label)
	}
	body, err := Build(p, Options{CacheControlParts: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Count(string(body), `"cache_control"`)
	t.Logf("planner asked for %d markers %v; the request body carries %d", len(labels), labels, got)

	// Control: when the last block is text the same marker is rendered.
	p2 := cxRenderPrompt(t, false)
	body2, _ := Build(p2, Options{CacheControlParts: true}, false)
	got2 := strings.Count(string(body2), `"cache_control"`)
	t.Logf("control (last block is a text part): %d markers requested, %d rendered", len(p2.Breakpoints), got2)
	if got2 != len(p2.Breakpoints) {
		t.Fatalf("control failed: %d != %d", got2, len(p2.Breakpoints))
	}
	cxBug(t, len(labels) == 4 && got == 3, "the thread marker on a tool_result is silently dropped (%d of %d markers rendered)", got, len(labels))
}
