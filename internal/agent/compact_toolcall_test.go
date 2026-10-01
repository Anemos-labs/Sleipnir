package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider/mock"
)

// The fork that asks for a patch carries the agent's tools (a different tool list would change the prompt and lose the cache), and
// "do not call tools" is only text. Some models call one anyway, in the same reply as the patch: on a real benchmark that refused
// 20 of one model's 127 compactions that had a patch in them that was valid and was never used. The call is never run, and the patch
// is judged as any other.
func TestAValidPatchIsUsedEvenWhenTheCompactorAlsoCalledATool(t *testing.T) {
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 6500
	pl.MinThreadTokens = 2000
	var mu sync.Mutex
	step := 0
	var asked int
	r := newRig(t, rigOpts{planner: pl, mock: mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}},
		func(c *mock.Call) mock.Reply {
			if isCompactor(c) {
				n := 0
				for _, m := range c.Messages {
					if m.Role == "assistant" {
						n++
					}
				}
				keep := max(2*n-6, 3)
				mu.Lock()
				asked++
				mu.Unlock()
				return mock.Reply{Text: compactorPatch(keep)(c), ToolCalls: []mock.ToolCall{{ID: "stray", Name: "big", Args: `{"step":0}`}}}
			}
			mu.Lock()
			step++
			s := step
			mu.Unlock()
			if s <= 22 {
				return mock.Reply{Text: fmt.Sprintf("working on step %d", s), ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("call_%d", s), Name: "big", Args: fmt.Sprintf(`{"step":%d}`, s)}}}
			}
			return mock.Reply{Text: "all done"}
		})
	if _, err := r.agent.Run(context.Background(), "please build the whole thing"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(r.log.OfType(events.TypeCompactCommit)) == 0 {
		if _, err := r.agent.Run(context.Background(), "and once more"); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if asked == 0 {
		t.Fatal("the compactor was never asked")
	}
	for _, why := range rejections(r) {
		if strings.Contains(why, "tool call") {
			t.Errorf("a reply with a valid patch was refused for its tool call: %s", why)
		}
	}
	// the patch the model wrote became the ready patch (not the mechanical one), and says that its tool call was not run
	used := 0
	for _, e := range r.log.OfType(events.TypeCompactPatch) {
		var d struct {
			Stage    string   `json:"stage"`
			Fallback bool     `json:"fallback"`
			Warnings []string `json:"warnings"`
		}
		_ = json.Unmarshal(e.Data, &d)
		if d.Stage == "ready" && !d.Fallback && strings.Contains(strings.Join(d.Warnings, "|"), "called a tool") {
			used++
		}
	}
	if used == 0 {
		t.Errorf("no patch of the model's became the ready patch; rejections: %v", rejections(r))
	}
}

func rejections(r *rig) []string {
	var out []string
	for _, e := range r.log.OfType(events.TypeCompactReject) {
		var d struct{ Reason string }
		_ = json.Unmarshal(e.Data, &d)
		out = append(out, d.Reason)
	}
	return out
}

// A reply that only called a tool, with nothing in its text that is a patch, is still a failed compaction, with the reason that says so.
func TestACompactorThatOnlyCalledAToolIsStillAFailedCompaction(t *testing.T) {
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 2500
	pl.MinThreadTokens = 800
	var mu sync.Mutex
	step := 0
	r := newRig(t, rigOpts{planner: pl}, func(c *mock.Call) mock.Reply {
		if isCompactor(c) {
			return mock.Reply{Text: "Let me look at that first.", ToolCalls: []mock.ToolCall{{ID: "stray", Name: "big", Args: `{"step":0}`}}}
		}
		mu.Lock()
		step++
		s := step
		mu.Unlock()
		if s <= 18 {
			return mock.Reply{Text: fmt.Sprintf("working on step %d", s), ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("call_%d", s), Name: "big", Args: fmt.Sprintf(`{"step":%d}`, s)}}}
		}
		return mock.Reply{Text: "all done"}
	})
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	_, _ = r.agent.Run(context.Background(), "next")
	found := false
	for _, why := range rejections(r) {
		found = found || strings.Contains(why, "tool call instead of a patch")
	}
	if !found {
		t.Errorf("rejections: %v; want the one that says the compactor answered with a tool call instead of a patch", rejections(r))
	}
	if len(r.log.OfType(events.TypeCompactCommit)) == 0 {
		t.Error("a failed patch must still end in a mechanical compaction")
	}
}
