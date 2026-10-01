package kv_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/kv"
)

// Render runs before every request of every agent, and Stack.PrefixKey and the layers' hashes with it, so what they cost is paid
// thousands of times in a session and by dozens of agents at once. These are the numbers to look at when a change touches them
// (go test ./internal/kv -run xxx -bench . -benchmem); allocs_gate_test.go holds the allocations of the same calls to what they are.

// BenchmarkRenderCanonical is Render on the nine-turn session of the render goldens, on every route and hot mode.
func BenchmarkRenderCanonical(b *testing.B) {
	for _, rt := range routes() {
		for _, m := range hotModes {
			stack, hot := goldenStack(b, m)
			caps := rt.caps
			caps.HotMode = m
			caps.TurnScopedSystem = m == kv.HotTurnScoped
			opts := kv.RenderOpts{Hot: hot, Caps: caps, Policy: rt.policy, Params: rt.params, CacheKey: rt.cacheKey, Est: goldenEstimator()}
			b.Run(rt.name+"/"+m.String(), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if r := kv.Render(stack, opts); len(r.Prompt.Messages) == 0 {
						b.Fatal("nothing rendered")
					}
				}
			})
		}
	}
}

// longStack is the canonical session with a thread of n tool exchanges instead of four: the render of the last request of a long
// session, which is what compaction exists to keep from growing.
func longStack(b testing.TB, n int) (*kv.Stack, kv.RenderOpts) {
	b.Helper()
	stack, hot := goldenStack(b, kv.HotInline)
	th := kv.NewThread()
	if err := th.Restore(nil, 7, 1); err != nil {
		b.Fatal(err)
	}
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTask, Blocks: []core.Block{core.Text("New assignment: T-1. Fix the pagination off-by-one.")}})
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("toolu_%04d", i)
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Model: goldenModel, Blocks: []core.Block{
			core.Text(fmt.Sprintf("Step %d: look at the handler.", i)),
			core.ToolUse(id, "read", json.RawMessage(fmt.Sprintf(`{"path":"api/file%d.go"}`, i))),
		}})
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{
			core.ToolResult(id, false, core.Text(strings.Repeat("package api\n\nfunc handler() {}\n", 40))),
		}})
	}
	stack.Thread = th.Snapshot()
	rt := routes()[1] // an automatic prefix cache: no markers to place
	caps := rt.caps
	caps.HotMode = kv.HotInline
	return stack, kv.RenderOpts{Hot: hot, Caps: caps, Policy: rt.policy, Params: rt.params, CacheKey: rt.cacheKey, Est: goldenEstimator()}
}

func BenchmarkRenderLongThread(b *testing.B) {
	for _, n := range []int{10, 50, 200} {
		stack, opts := longStack(b, n)
		b.Run(fmt.Sprintf("%d-exchanges", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if r := kv.Render(stack, opts); len(r.Prompt.Messages) < n {
					b.Fatal("the thread was cut")
				}
			}
		})
	}
}

// BenchmarkPrefixKeyAndHashes is what the planner asks of a stack on every request: its prefix key and the hash of each layer.
func BenchmarkPrefixKeyAndHashes(b *testing.B) {
	stack, _ := goldenStack(b, kv.HotInline)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = stack.PrefixKey()
		_ = stack.GlobalKey()
		_ = stack.Shared.Hash()
		_ = stack.Notes.Hash()
	}
}
