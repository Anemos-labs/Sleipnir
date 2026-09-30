package mock

// Regression tests for the prompt-cache economics review (R16). The mock is an
// automatic-prefix-cache engine; explicit-breakpoint (Anthropic-style) semantics
// live in a separate engine.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// cxBlocks returns n complete 16-token blocks of deterministic, seed-specific bytes.
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
// used to evict the ROOT first: the whole chain became unreachable while its
// remaining blocks squatted in the capacity budget.
func TestCacheEcon_MockEvictsTailsBeforeRoots(t *testing.T) {
	e := NewEngine(EngineConfig{BlockTokens: 16, CapacityBlocks: 8}, nil)
	c1 := cxBlocks(1, 6)
	c2 := cxBlocks(90, 4)
	e.Insert(c1)
	e.Insert(c2) // 10 blocks into 8: two must go
	if e.Resident() != 8 {
		t.Fatalf("resident = %d, want the full budget of 8", e.Resident())
	}
	if hit := e.Lookup(c1); hit != 4*16 {
		t.Fatalf("the older conversation keeps its 4-block prefix (64 tokens), hit %d", hit)
	}
	if hit := e.Lookup(c2); hit != 4*16 {
		t.Fatalf("the newer conversation is intact, hit %d", hit)
	}
	// Touching a conversation refreshes it as a unit: after reading c1 (root last),
	// inserting a third chain evicts the tail of the now-older c2, not c1's root.
	e.Lookup(c1)
	e.Insert(cxBlocks(200, 4))
	if hit := e.Lookup(c1); hit == 0 {
		t.Fatal("a recently read conversation must not lose its root")
	}
	if e.Resident() != 8 {
		t.Fatalf("resident = %d", e.Resident())
	}
}

// The automatic-cache engine keys on the prefix text alone, which is right for
// OpenAI-style caching and deliberately says nothing about explicit caches:
// markers are flattened and sampling / routing parameters are not part of a key.
func TestCacheEcon_AutomaticModeKeysOnPrefixBytesOnly(t *testing.T) {
	srv := New(Config{Engine: EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, nil)
	ts := srv.Start()
	defer ts.Close()
	body := func(extra string, marker string) string {
		msg := strings.Repeat("shared prefix text that a real engine would cache. ", 60)
		return fmt.Sprintf(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":[{"type":"text","text":%q%s}]}]%s}`, msg, marker, extra)
	}
	post := func(b string) {
		resp, err := http.Post(ts.URL+"/chat/completions", "application/json", bytes.NewReader([]byte(b)))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	post(body(`,"tool_choice":"auto","reasoning_effort":"high"`, ""))
	post(body(`,"tool_choice":"none","reasoning_effort":"low"`, `,"cache_control":{"type":"ephemeral"}`))
	st := srv.Stats()
	if len(st) != 2 {
		t.Fatalf("stats: %d", len(st))
	}
	if st[0].Cached != 0 || st[1].Cached < st[1].PromptTokens*9/10 {
		t.Fatalf("automatic prefix caching: the second request reads the first's prefix whatever else changed: %+v %+v", st[0], st[1])
	}
}
