package reward

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/cost"
)

// anthropicLike is cost.Fallback: read 0.1x, write 1.25x (5m) / 2x (1h), output
// 5x, 5 minute TTL, explicit breakpoints, entries readable at the first byte,
// 1024-token minimum. It is what sim.Anthropic() models.
func anthropicLike() cost.Model { return cost.Fallback("test-anthropic") }

// The first four tests replay internal/kv/sim's TestCacheSemantics against this
// package's model, case by case, so the two stay semantically identical.

func semanticsSegs() []Seg {
	return []Seg{{ID: "a", Tokens: 4000}, {ID: "b", Tokens: 4000}, {ID: "t1", Tokens: 1000}, {ID: "hot", Tokens: 500, Ephemeral: true}}
}

func TestCacheSemanticsColdWrite(t *testing.T) {
	c := NewCache(anthropicLike(), RepriceOptions{})
	r := c.Request(0, 0, semanticsSegs(), []int{1, 2})
	if r.Read != 0 || r.Hit || r.Write() != 9000 || r.Uncached != 500 {
		t.Fatalf("cold: unc=%d read=%d write=%d hit=%v", r.Uncached, r.Read, r.Write(), r.Hit)
	}
	// sim: ttfb = 1500ms + (500+9000)/1000 * 60ms
	if want := 1500*time.Millisecond + time.Duration(9.5*60*float64(time.Millisecond)); r.TTFB != want {
		t.Errorf("ttfb = %v, want %v", r.TTFB, want)
	}
}

func TestCacheSemanticsWaitForFirstByte(t *testing.T) {
	c := NewCache(anthropicLike(), RepriceOptions{})
	segs, bp := semanticsSegs(), []int{1, 2}
	first := c.Request(0, 0, segs, bp)
	// Not readable before the first byte, and a second cold request arriving in
	// the meantime does not push readiness back.
	if r := c.Request(0, first.TTFB/2, segs, bp); r.Read != 0 {
		t.Fatalf("entry readable before the first byte: read %d", r.Read)
	}
	r := c.Request(0, first.TTFB+time.Second, segs, bp)
	if r.Read != 9000 || r.Write() != 0 || !r.Hit {
		t.Fatalf("warm: read=%d write=%d hit=%v", r.Read, r.Write(), r.Hit)
	}
}

func TestCacheSemanticsGrowthAndTTL(t *testing.T) {
	c := NewCache(anthropicLike(), RepriceOptions{})
	segs, bp := semanticsSegs(), []int{1, 2}
	first := c.Request(0, 0, segs, bp)
	c.Request(0, first.TTFB+time.Second, segs, bp)
	// Growth: reads the old breakpoint, writes the new tail.
	grown := append([]Seg{}, segs[:3]...)
	grown = append(grown, Seg{ID: "t2", Tokens: 2000}, Seg{ID: "hot", Tokens: 500, Ephemeral: true})
	r := c.Request(0, 2*time.Minute, grown, []int{1, 3})
	if r.Read != 9000 || r.Write() != 2000 {
		t.Fatalf("growth: read=%d write=%d", r.Read, r.Write())
	}
	// TTL expiry: after 7 minutes idle everything is cold again.
	r = c.Request(0, 9*time.Minute, grown, []int{1, 3})
	if r.Read != 0 || r.Write() != 11000 {
		t.Fatalf("expired: read=%d write=%d", r.Read, r.Write())
	}
}

func TestReadsRefreshTheTTL(t *testing.T) {
	c := NewCache(anthropicLike(), RepriceOptions{})
	segs, bp := semanticsSegs(), []int{1, 2}
	c.Request(0, 0, segs, bp)
	// Touch every 4 minutes: alive at 12 minutes although written at 0.
	for m := 4; m <= 12; m += 4 {
		if r := c.Request(0, time.Duration(m)*time.Minute, segs, bp); r.Read != 9000 {
			t.Fatalf("at %dm: read %d, want a refreshed hit", m, r.Read)
		}
	}
	// Without refresh-on-read the entry dies TTL after it was written.
	m := anthropicLike()
	m.Cache.ReadRefreshesTTL = false
	c = NewCache(m, RepriceOptions{})
	c.Request(0, 0, segs, bp)
	if r := c.Request(0, 4*time.Minute, segs, bp); r.Read != 9000 {
		t.Fatalf("still within TTL: read %d", r.Read)
	}
	if r := c.Request(0, 6*time.Minute, segs, bp); r.Read != 0 {
		t.Fatalf("a read must not extend the life of the entry: read %d", r.Read)
	}
}

func TestMinPrefixIsNeverCached(t *testing.T) {
	c := NewCache(anthropicLike(), RepriceOptions{})
	segs := []Seg{{ID: "tiny", Tokens: 600}, {ID: "more", Tokens: 300}}
	r := c.Request(0, 0, segs, []int{0, 1})
	if r.Write() != 0 || r.Uncached != 900 {
		t.Fatalf("a prefix under the minimum must not be stored: %+v", r)
	}
	// A breakpoint below the minimum is skipped but a later one above it works.
	segs = []Seg{{ID: "tiny", Tokens: 600}, {ID: "big", Tokens: 900}}
	r = c.Request(0, time.Second, segs, []int{0, 1})
	if r.Write() != 1500 {
		t.Fatalf("write = %d, want 1500 (one entry through the second breakpoint)", r.Write())
	}
}

func TestAutomaticCacheStoresEveryPrefixAndRoundsHits(t *testing.T) {
	m := cost.Model{Price: cost.Price{InputPerM: 1, OutputPerM: 2, CacheReadPerM: 0.25, CacheWrite5mPerM: 1}, Cache: cost.OpenAICacheModel()}
	c := NewCache(m, RepriceOptions{})
	segs := []Seg{{ID: "s", Tokens: 3000}, {ID: "t", Tokens: 2000}}
	r := c.Request(0, 0, segs, nil) // no breakpoints: automatic caches ignore them
	if r.Write() != 5000 {
		t.Fatalf("automatic cache stores all processed tokens: write %d", r.Write())
	}
	// Hit of 5000 tokens is served in 128-token steps above the 1024 floor.
	r = c.Request(0, time.Minute, append(segs, Seg{ID: "u", Tokens: 700}), nil)
	want := 1024 + (5000-1024)/128*128
	if r.Read != want {
		t.Fatalf("read = %d, want %d (granularity)", r.Read, want)
	}
	if r.Uncached != 5700-want-700 && r.Uncached+r.Read+r.Write() != 5700 {
		t.Errorf("tokens do not add up: %+v", r)
	}
	if got := r.Uncached + r.Read + r.Write(); got != 5700 {
		t.Errorf("partition of the prompt: %d != 5700", got)
	}
}

func TestReadableImmediatelyAndAtComplete(t *testing.T) {
	segs := []Seg{{ID: "a", Tokens: 3000}}
	imm := anthropicLike()
	imm.Cache.ReadableAfter = cost.ReadableImmediately
	c := NewCache(imm, RepriceOptions{})
	c.Request(0, 0, segs, []int{0})
	if r := c.Request(0, 0, segs, []int{0}); r.Read != 3000 {
		t.Errorf("immediately readable: read %d", r.Read)
	}

	end := anthropicLike()
	end.Cache.ReadableAfter = cost.ReadableAtComplete
	c = NewCache(end, RepriceOptions{})
	r := c.do(0, 0, []node{{key: chainKey([16]byte{}, "a"), cum: 3000}}, 0, []int{0}, reqInfo{out: 500})
	if r.Ready <= r.TTFB {
		t.Errorf("entries should be readable only after the whole response: ready %v ttfb %v", r.Ready, r.TTFB)
	}
	if got := c.do(0, r.TTFB+time.Millisecond, []node{{key: chainKey([16]byte{}, "a"), cum: 3000}}, 0, []int{0}, reqInfo{}); got.Read != 0 {
		t.Errorf("entry readable before completion: %+v", got)
	}
	if got := c.do(0, r.Ready+time.Millisecond, []node{{key: chainKey([16]byte{}, "a"), cum: 3000}}, 0, []int{0}, reqInfo{}); got.Read != 3000 {
		t.Errorf("entry not readable after completion: %+v", got)
	}
}

func TestNoCachingModelBillsEverythingUncached(t *testing.T) {
	c := NewCache(cost.Model{Price: cost.Price{InputPerM: 1, OutputPerM: 1}}, RepriceOptions{})
	r := c.Request(0, 0, semanticsSegs(), []int{1, 2})
	if r.Uncached != 9500 || r.Read != 0 || r.Write() != 0 {
		t.Fatalf("%+v", r)
	}
	if again := c.Request(0, time.Minute, semanticsSegs(), []int{1, 2}); again.Read != 0 {
		t.Fatal("a provider without a cache never reads")
	}
}

func TestLongTTLForSharedLayers(t *testing.T) {
	c := NewCache(anthropicLike(), RepriceOptions{})
	segs := []Seg{{ID: "shared", Tokens: 3000, Long: true}, {ID: "mine", Tokens: 1000}}
	r := c.Request(0, 0, segs, []int{0, 1})
	if r.Write1h != 3000 || r.Write5m != 1000 {
		t.Fatalf("shared layer should be written with the long TTL: 1h=%d 5m=%d", r.Write1h, r.Write5m)
	}
	// After ten minutes the private part is gone but the shared pin lives.
	r = c.Request(0, 10*time.Minute, segs, []int{0, 1})
	if r.Read != 3000 || r.Write5m != 1000 {
		t.Fatalf("after 10m: read %d write5m %d", r.Read, r.Write5m)
	}
	// Opting out prices everything at the default TTL.
	c = NewCache(anthropicLike(), RepriceOptions{NoSharedLongTTL: true})
	r = c.Request(0, 0, segs, []int{0, 1})
	if r.Write1h != 0 || r.Write5m != 4000 {
		t.Fatalf("no long ttl: 1h=%d 5m=%d", r.Write1h, r.Write5m)
	}
}

func TestEnginesAndRouting(t *testing.T) {
	m := cost.Model{Price: cost.Price{InputPerM: 1, OutputPerM: 2, CacheReadPerM: 0.25, CacheWrite5mPerM: 1},
		Cache: cost.CacheModel{Auto: true, MinPrefixTokens: 64, TTLs: []time.Duration{time.Hour}, ReadRefreshesTTL: true, KeyRouting: true}}
	segs := []Seg{{ID: "shared", Tokens: 5000}}
	warm := func(c *Cache, keys []string) (reads int) {
		c.Request(c.Route("seed"), 0, segs, nil)
		for i, k := range keys {
			reads += c.Request(c.Route(k), time.Duration(i+1)*time.Minute, segs, nil).Read
		}
		return reads
	}
	// One engine: everything hits.
	one := NewCache(m, RepriceOptions{Engines: 1})
	if got := warm(one, []string{"", "", "", ""}); got != 4*5000 {
		t.Errorf("single engine reads = %d", got)
	}
	// Four engines, unkeyed round-robin: requests land on engines that never saw
	// the prefix, so some are cold.
	rr := NewCache(m, RepriceOptions{Engines: 4})
	if got := warm(rr, []string{"", "", "", ""}); got >= 4*5000 {
		t.Errorf("round-robin over 4 engines cannot hit everywhere: reads = %d", got)
	}
	// A routing key pins requests to one engine, which holds the prefix.
	pinned := NewCache(m, RepriceOptions{Engines: 4})
	seedEngine := pinned.Route("k")
	pinned.Request(seedEngine, 0, segs, nil)
	for i := 1; i <= 4; i++ {
		if r := pinned.Request(pinned.Route("k"), time.Duration(i)*time.Minute, segs, nil); r.Read != 5000 {
			t.Errorf("keyed request %d read %d", i, r.Read)
		}
	}
	if e1, e2 := pinned.Route("k"), pinned.Route("k"); e1 != e2 {
		t.Error("a key must map to one engine")
	}
	// Out-of-range engine indexes are tolerated.
	pinned.Request(99, 0, segs, nil)
	pinned.Request(-3, 0, segs, nil)
}

func TestLRUCapacityEvictsLeastRecentlyUsedDeterministically(t *testing.T) {
	m := cost.Model{Price: cost.Price{InputPerM: 1, OutputPerM: 2, CacheReadPerM: 0.25, CacheWrite5mPerM: 1},
		Cache: cost.CacheModel{Auto: true, MinPrefixTokens: 64, ReadRefreshesTTL: true}} // no TTL: pure LRU
	run := func() []int {
		c := NewCache(m, RepriceOptions{CapacityTokens: 10_000})
		var reads []int
		// Two 4000-token prompts fit in 10k tokens, three do not. "a" is reused, so
		// it stays; "b" is the least recently used when "c" arrives.
		for i, id := range []string{"a", "b", "a", "c", "a", "b"} {
			r := c.Request(0, time.Duration(i)*time.Minute, []Seg{{ID: id, Tokens: 4000}}, nil)
			reads = append(reads, r.Read)
		}
		return reads
	}
	got := run()
	if fmt.Sprint(got) != fmt.Sprint(run()) {
		t.Fatalf("eviction is not deterministic: %v vs %v", got, run())
	}
	if want := []int{0, 0, 4000, 0, 4000, 0}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reads = %v, want %v (a survives, b is evicted for c)", got, want)
	}
	// The pathological cyclic pattern misses every time under LRU: that is what
	// the model must say, not an artefact.
	c2 := NewCache(m, RepriceOptions{CapacityTokens: 10_000})
	for i, id := range []string{"a", "b", "c", "a", "b", "c"} {
		if r := c2.Request(0, time.Duration(i)*time.Minute, []Seg{{ID: id, Tokens: 4000}}, nil); r.Read != 0 {
			t.Errorf("cyclic access %d read %d, LRU should always miss", i, r.Read)
		}
	}
	// Without a capacity limit and without a TTL the model applies a large
	// default capacity, so small workloads always hit.
	c := NewCache(m, RepriceOptions{})
	c.Request(0, 0, []Seg{{ID: "a", Tokens: 4000}}, nil)
	if r := c.Request(0, 48*time.Hour, []Seg{{ID: "a", Tokens: 4000}}, nil); r.Read != 4000 {
		t.Errorf("entries of an LRU-only cache never expire by time: read %d", r.Read)
	}
}

// The windowed lookup used for long chains must agree with a full scan.
func TestWindowedLookupMatchesFullScan(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, m := range []cost.Model{anthropicLike(), {Price: anthropicLike().Price, Cache: cost.OpenAICacheModel()}} {
		full, win := NewCache(m, RepriceOptions{}), NewCache(m, RepriceOptions{})
		var nodes []node
		var parent [16]byte
		cum := 0
		now := time.Duration(0)
		for i := 0; i < 600; i++ {
			// grow the chain, occasionally jump far into the future
			tok := 200 + rng.Intn(800)
			parent = chainKey(parent, fmt.Sprintf("n%d", i))
			cum += tok
			nodes = append(nodes, node{key: parent, cum: cum, long: i == 0})
			now += time.Duration(5+rng.Intn(50)) * time.Second
			if rng.Intn(40) == 0 {
				now += 20 * time.Minute
			}
			bp := []int{len(nodes) - 1}
			if len(nodes) > 1 {
				bp = append(bp, 0)
			}
			a := full.do(0, now, nodes, 300, bp, reqInfo{full: true})
			b := win.do(0, now, nodes, 300, bp, reqInfo{})
			if a != b {
				t.Fatalf("step %d (%d nodes): full scan %+v, windowed %+v", i, len(nodes), a, b)
			}
		}
	}
}

func TestCacheIsDeterministic(t *testing.T) {
	run := func() string {
		c := NewCache(anthropicLike(), RepriceOptions{Engines: 3})
		var out []CacheResult
		for i := 0; i < 50; i++ {
			segs := []Seg{{ID: "g", Tokens: 3000, Long: true}, {ID: fmt.Sprintf("p%d", i%5), Tokens: 500 + i*10}, {ID: "hot", Tokens: 100, Ephemeral: true}}
			out = append(out, c.Request(c.Route(fmt.Sprintf("k%d", i%7)), time.Duration(i)*40*time.Second, segs, []int{0, 1}))
		}
		return fmt.Sprint(out)
	}
	if a, b := run(), run(); a != b {
		t.Fatal("same input, different bills")
	}
}

func TestRequestEdgeCases(t *testing.T) {
	c := NewCache(anthropicLike(), RepriceOptions{})
	// Empty prompt, negative sizes, breakpoints out of range: all tolerated.
	if r := c.Request(0, 0, nil, nil); r.Uncached != 0 || r.Write() != 0 {
		t.Errorf("empty: %+v", r)
	}
	r := c.Request(0, 0, []Seg{{ID: "a", Tokens: -5}, {ID: "b", Tokens: 2000}}, []int{-1, 0, 1, 99})
	if r.Write() != 2000 {
		t.Errorf("negative segment size should count as zero: %+v", r)
	}
	// Only ephemeral segments: nothing to cache.
	r = c.Request(0, 0, []Seg{{ID: "h", Tokens: 700, Ephemeral: true}}, []int{0})
	if r.Uncached != 700 || r.Write() != 0 {
		t.Errorf("ephemeral only: %+v", r)
	}
}
