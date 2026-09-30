package mock

// Adversarial review tests (lens: prompt-cache economics and correctness) for
// the "cache-faithful mock". See docs/reviews/cache-economics.md.
// Convention: tests PASS while the defect is present; REVIEW_STRICT=1 inverts.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
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

// prompt returns n complete 16-token blocks of deterministic, seed-specific bytes.
func cxBlocks(seed byte, n int) []byte {
	bs := 16 * bytesPerToken
	out := make([]byte, 0, n*bs)
	for i := 0; i < n; i++ {
		for j := 0; j < bs; j++ {
			out = append(out, seed+byte(i*7+j%13))
		}
	}
	return out
}

// Real paged-attention engines free the TAIL blocks of the least recently used
// sequence first (vLLM V1: "LRU free queue (tail blocks freed first)"), so an old
// conversation loses its newest suffix and keeps its reusable prefix. The mock
// evicts the ROOT first: the whole chain becomes unreachable while its remaining
// blocks squat in the capacity budget. Any test that runs the mock under memory
// pressure therefore reports misses a real engine would not have.
func TestCacheEcon_MockEvictsRootsBeforeLeaves(t *testing.T) {
	e := NewEngine(EngineConfig{BlockTokens: 16, CapacityBlocks: 8}, nil)
	c1 := cxBlocks(1, 6)
	c2 := cxBlocks(90, 4)
	e.Insert(c1)
	e.Insert(c2) // 10 blocks into 8: two must go
	hit := e.Lookup(c1)
	t.Logf("resident=%d blocks; old conversation (6 blocks) now hits %d tokens (a tail-first engine would keep 4 blocks = 64 tokens)", e.Resident(), hit)
	cxBug(t, hit == 0 && e.Resident() == 8,
		"capacity eviction removed the root of the older chain: lookup=%d tokens with %d of its blocks still resident but unreachable", hit, 4)
}

// The mock hashes only tools+message text. It has no notion of the request
// parameters that are cache-key inputs on real providers (tool_choice,
// reasoning effort / thinking config, cache_control markers, images), no write
// premium, no cache_write_tokens and no thinking binding. Passing tests therefore
// say nothing about explicit-breakpoint planning, the compactor fork's
// tool_choice, or preserved thinking.
func TestCacheEcon_MockIsBlindToCacheKeyParameters(t *testing.T) {
	srv := New(Config{Engine: EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, nil)
	ts := srv.Start()
	defer ts.Close()
	body := func(extra string) string {
		msg := strings.Repeat("shared prefix text that a real engine would cache. ", 60)
		return fmt.Sprintf(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":[{"type":"text","text":%q%s}]}]%s}`, msg, "", extra)
	}
	post := func(b string) {
		resp, err := http.Post(ts.URL+"/chat/completions", "application/json", bytes.NewReader([]byte(b)))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	post(body(`,"tool_choice":"auto","reasoning_effort":"high"`))
	post(body(`,"tool_choice":"none","reasoning_effort":"low"`)) // Anthropic: invalidates the messages tier
	st := srv.Stats()
	if len(st) != 2 {
		t.Fatalf("stats: %d", len(st))
	}
	t.Logf("second request (different tool_choice and effort): cached %d of %d tokens", st[1].Cached, st[1].PromptTokens)
	cxBug(t, st[1].Cached > 0 && st[1].Cached >= st[1].PromptTokens/2,
		"the mock serves the second request from cache although tool_choice and effort changed; usage never reports cache_write_tokens (cost has no write premium)")
}
