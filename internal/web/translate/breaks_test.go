package translate

import (
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// The page is told of cache breaks by the rule of the terminal's chat (state.BreakNotices): a break of the prompt (a drift, a layer that
// changed) is never quiet; the endpoint's own miss is quiet when it cost less than a cent, and of the material ones the fourth and later
// are quiet, with one row that says so. Every break carries its layer and, at a known price, the cost of its miss; the info notice that
// says the same as a break is not a row.
func TestBreaksAreQuietByTheChatsRule(t *testing.T) {
	h := newHarness(t, Config{Root: "/work/shop", StartedAt: t0})
	b := newLog(t0)
	step := func(agent, typ string, data any) {
		b.at = h.now()
		h.feed(b.add(10*time.Millisecond, agent, typ, data))
	}
	step("", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work/shop",
		"models": map[string]any{"m1": map[string]any{"source": "session", "input_per_m": 3.0, "output_per_m": 15.0, "cache_read_per_m": 0.3, "ttl_s": 300}}})
	step("mgr", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "model": "m1"})
	step("be-1", "model.request", request("r1"))
	// cheap: 1,000 tokens at $2.70/M is $0.0027
	step("be-1", "cache.anomaly", map[string]any{"kind": "low_hit", "expected_read": 1000, "actual_read": 0, "req": "r1"})
	// a layer that changed, and a drift: never quiet
	step("be-1", "cache.anomaly", map[string]any{"kind": "low_hit", "expected_read": 1000, "actual_read": 0, "req": "r1", "diverged": "notes"})
	step("be-1", "cache.anomaly", map[string]any{"kind": "drift", "diverged": "role"})
	// material: 10,000 tokens is $0.027; the fourth of them is quiet
	for i := 0; i < 5; i++ {
		step("be-1", "cache.anomaly", map[string]any{"kind": "low_hit", "expected_read": 10000, "actual_read": 0, "req": "r1"})
	}
	snk := h.tr.Sink()
	h.set(h.now().Add(10 * time.Millisecond))
	snk.Notice("be-1", "info", "cache miss: expected ~10000 tokens read from cache, got 0 (internal prefix is stable)")
	h.drain()

	var quiet []bool
	var layers []string
	capRows, missRows := 0, 0
	for _, e := range h.decoded() {
		switch e["k"] {
		case "break":
			q, _ := e["quiet"].(bool)
			quiet = append(quiet, q)
			l, _ := e["layer"].(string)
			layers = append(layers, l)
			if e["kind"] == "low_hit" && l == "" {
				if usd, _ := e["missUsd"].(float64); usd <= 0 {
					t.Errorf("a priced miss without its cost: %v", e)
				}
			}
		case "sys":
			txt, _ := e["text"].(string)
			if strings.Contains(txt, state.EndpointBreaksCapped) {
				capRows++
			}
			if strings.Contains(txt, "cache miss: expected") {
				missRows++
			}
		}
	}
	want := []bool{true, false, false, false, false, false, true, true}
	if len(quiet) != len(want) {
		t.Fatalf("%d breaks: %v", len(quiet), quiet)
	}
	for i := range want {
		if quiet[i] != want[i] {
			t.Errorf("break %d quiet=%v, want %v (all: %v)", i, quiet[i], want[i], quiet)
		}
	}
	if layers[1] != "notes" || layers[2] != "role" {
		t.Errorf("layers %q", layers)
	}
	if capRows != 1 {
		t.Errorf("%d rows say that further misses are not said, want 1", capRows)
	}
	if missRows != 0 {
		t.Errorf("the info notice of a miss is a row %d times", missRows)
	}
}
