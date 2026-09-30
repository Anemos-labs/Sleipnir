package mock

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock for TTL tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const (
	m5 = 5 * time.Minute
	h1 = time.Hour
)

func toolB(name string, tokens int) ExplicitBlock {
	return ExplicitBlock{Tier: TierTools, Kind: "tool", Hash: "tool:" + name, Tokens: tokens, Label: "tool:" + name}
}

func sysB(text string, tokens int) ExplicitBlock {
	return ExplicitBlock{Tier: TierSystem, Kind: "text", Hash: "sys:" + text, Tokens: tokens, Label: "sys:" + text}
}

func msgB(kind, text string, tokens int) ExplicitBlock {
	return ExplicitBlock{Tier: TierMessages, Kind: kind, Hash: "m:" + kind + ":" + text, Tokens: tokens, Label: "m:" + text}
}

func marked(b ExplicitBlock, ttl time.Duration) ExplicitBlock {
	b.Marker, b.MarkerTTL = true, ttl
	return b
}

func reqOf(model string, p ExplicitParams, blocks ...ExplicitBlock) ExplicitRequest {
	return ExplicitRequest{Model: model, Params: p, Blocks: blocks}
}

// run does what the HTTP layer does for one request: look up at arrival, publish
// at the first byte.
func run(t *testing.T, e *ExplicitEngine, clk *fakeClock, r ExplicitRequest) *ExplicitPlan {
	t.Helper()
	pl, err := e.Lookup(r, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Publish(pl)
	return pl
}

func checkBilling(t *testing.T, pl *ExplicitPlan) {
	t.Helper()
	if got := pl.Read + pl.Write5m + pl.Write1h + pl.Uncached; got != pl.Total {
		t.Errorf("read %d + write5m %d + write1h %d + uncached %d = %d, want total %d", pl.Read, pl.Write5m, pl.Write1h, pl.Uncached, got, pl.Total)
	}
	if pl.ReadTiers.Total() != pl.Read {
		t.Errorf("read tiers %+v do not add up to %d", pl.ReadTiers, pl.Read)
	}
	if pl.WriteTiers.Total() != pl.Write5m+pl.Write1h {
		t.Errorf("write tiers %+v do not add up to %d", pl.WriteTiers, pl.Write5m+pl.Write1h)
	}
}

func newEngine(clk *fakeClock, cfg ExplicitConfig) *ExplicitEngine {
	cfg.Now = clk.Now
	if cfg.DefaultMinPrefix == 0 {
		cfg.DefaultMinPrefix = 100
	}
	return NewExplicitEngine(cfg)
}

func TestExplicitColdThenWarm(t *testing.T) {
	clk := newFakeClock()
	e := newEngine(clk, ExplicitConfig{})
	r := reqOf("m", ExplicitParams{},
		toolB("bash", 200), marked(sysB("const", 300), m5), marked(msgB("text", "task", 500), m5), msgB("text", "tail", 50))
	cold := run(t, e, clk, r)
	checkBilling(t, cold)
	if cold.Read != 0 || cold.Write5m != 1000 || cold.Uncached != 50 {
		t.Fatalf("cold: %+v", cold)
	}
	warm := run(t, e, clk, r)
	checkBilling(t, warm)
	if warm.Read != 1000 || warm.Write5m != 0 || warm.Uncached != 50 {
		t.Fatalf("warm: read %d write %d uncached %d", warm.Read, warm.Write5m, warm.Uncached)
	}
	if warm.ReadTiers != (TierTokens{Tools: 200, System: 300, Messages: 500}) {
		t.Fatalf("read tiers = %+v", warm.ReadTiers)
	}
	if len(warm.Hits) != 2 {
		t.Fatalf("each breakpoint reports its hit: %+v", warm.Hits)
	}
}

func TestExplicitGrowthReadsOldMarkerAndWritesTheTail(t *testing.T) {
	clk := newFakeClock()
	e := newEngine(clk, ExplicitConfig{})
	base := []ExplicitBlock{sysB("const", 400), msgB("text", "u1", 300)}
	run(t, e, clk, reqOf("m", ExplicitParams{}, append(append([]ExplicitBlock(nil), base...)[:1:1], marked(base[1], m5))...))
	grown := reqOf("m", ExplicitParams{}, base[0], base[1], msgB("tool_use", "a1", 100), marked(msgB("tool_result", "r1", 200), m5))
	pl := run(t, e, clk, grown)
	checkBilling(t, pl)
	if pl.Read != 700 || pl.Write5m != 300 || pl.Uncached != 0 {
		t.Fatalf("read %d write %d uncached %d, want 700/300/0", pl.Read, pl.Write5m, pl.Uncached)
	}
	if pl.ReadTiers.Messages != 300 || pl.WriteTiers.Messages != 300 {
		t.Fatalf("tiers: read %+v write %+v", pl.ReadTiers, pl.WriteTiers)
	}
}

func TestExplicitLookbackWindow(t *testing.T) {
	// A burst of appended blocks pushes the previous entry out of the 20-position
	// window. Rolling marker only: the whole prefix is rewritten. With an
	// intermediate marker inside the window of the old entry, it is read.
	filler := func(n int, kind string) []ExplicitBlock {
		var out []ExplicitBlock
		for i := 0; i < n; i++ {
			out = append(out, msgB(kind, fmt.Sprint("f", i), 100))
		}
		return out
	}
	prefix := []ExplicitBlock{sysB("const", 500), marked(msgB("text", "old-tail", 400), m5)}
	scenario := func(t *testing.T, appended []ExplicitBlock, intermediate int) *ExplicitPlan {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{})
		run(t, e, clk, reqOf("m", ExplicitParams{}, prefix...))
		blocks := append([]ExplicitBlock{sysB("const", 500), msgB("text", "old-tail", 400)}, appended...)
		if intermediate >= 0 {
			blocks[intermediate] = marked(blocks[intermediate], m5)
		}
		blocks[len(blocks)-1] = marked(blocks[len(blocks)-1], m5)
		pl := run(t, e, clk, reqOf("m", ExplicitParams{}, blocks...))
		checkBilling(t, pl)
		return pl
	}

	t.Run("25 sequential blocks orphan the old entry", func(t *testing.T) {
		pl := scenario(t, filler(25, "text"), -1)
		if pl.Read != 0 || pl.Write5m != 500+400+25*100 {
			t.Fatalf("read %d write %d: the rolling marker should have found nothing", pl.Read, pl.Write5m)
		}
	})
	t.Run("an intermediate marker within 20 positions saves it", func(t *testing.T) {
		pl := scenario(t, filler(25, "text"), 10) // block 10 is position 11, 9 after the old entry (position 2)
		if pl.Read != 900 || pl.Write5m != 2500 {
			t.Fatalf("read %d write %d, want 900/2500", pl.Read, pl.Write5m)
		}
	})
	t.Run("the window is exactly 20 positions", func(t *testing.T) {
		// system is position 1, the old entry position 2. With 19 appended blocks
		// the rolling marker is position 21: the old entry is the 20th position
		// back (the marker itself is position 1 of the window) and is found.
		if pl := scenario(t, filler(19, "text"), -1); pl.Read != 900 {
			t.Fatalf("19 appended: read %d, want 900", pl.Read)
		}
		// One more and the marker is position 22: the entry is 21 back, out of reach.
		if pl := scenario(t, filler(20, "text"), -1); pl.Read != 0 {
			t.Fatalf("20 appended: read %d, want 0", pl.Read)
		}
	})
	t.Run("a run of tool_use blocks is one position", func(t *testing.T) {
		pl := scenario(t, filler(25, "tool_use"), -1)
		if pl.Read != 900 {
			t.Fatalf("read %d, want 900: 25 consecutive tool_use blocks are one position", pl.Read)
		}
	})
	t.Run("alternating tool_use and tool_result do not collapse", func(t *testing.T) {
		var mixed []ExplicitBlock
		for i := 0; i < 13; i++ {
			mixed = append(mixed, msgB("tool_use", fmt.Sprint("u", i), 50), msgB("tool_result", fmt.Sprint("r", i), 50))
		}
		pl := scenario(t, mixed, -1)
		if pl.Read != 0 {
			t.Fatalf("read %d, want 0: 26 alternating blocks are 26 positions", pl.Read)
		}
	})
	t.Run("collapsing can be turned off", func(t *testing.T) {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{NoCollapseRuns: true})
		run(t, e, clk, reqOf("m", ExplicitParams{}, prefix...))
		blocks := append([]ExplicitBlock{sysB("const", 500), msgB("text", "old-tail", 400)}, filler(25, "tool_use")...)
		blocks[len(blocks)-1] = marked(blocks[len(blocks)-1], m5)
		if pl := run(t, e, clk, reqOf("m", ExplicitParams{}, blocks...)); pl.Read != 0 {
			t.Fatalf("read %d, want 0", pl.Read)
		}
	})
}

func TestExplicitBreakpointLimits(t *testing.T) {
	clk := newFakeClock()
	e := newEngine(clk, ExplicitConfig{})
	var blocks []ExplicitBlock
	for i := 0; i < 5; i++ {
		blocks = append(blocks, marked(msgB("text", fmt.Sprint(i), 200), m5))
	}
	if _, err := e.Lookup(reqOf("m", ExplicitParams{}, blocks...), clk.Now()); err == nil || err.Error() != "A maximum of 4 blocks with cache_control may be provided. Found 5." {
		t.Fatalf("5 markers: %v", err)
	}
	blocks[0].Marker = false
	if _, err := e.Lookup(reqOf("m", ExplicitParams{}, blocks...), clk.Now()); err != nil {
		t.Fatalf("4 markers: %v", err)
	}
	// A 1h marker may not follow a 5m marker.
	bad := reqOf("m", ExplicitParams{}, marked(msgB("text", "a", 200), m5), marked(msgB("text", "b", 200), h1))
	if _, err := e.Lookup(bad, clk.Now()); err == nil || !strings.Contains(err.Error(), "ttl='1h'") {
		t.Fatalf("5m then 1h: %v", err)
	}
	good := reqOf("m", ExplicitParams{}, marked(msgB("text", "a", 200), h1), marked(msgB("text", "b", 200), m5))
	if _, err := e.Lookup(good, clk.Now()); err != nil {
		t.Fatalf("1h then 5m: %v", err)
	}
	// The caller's blocks are never modified.
	unset := reqOf("m", ExplicitParams{}, marked(msgB("text", "a", 200), 0))
	if _, err := e.Lookup(unset, clk.Now()); err != nil || unset.Blocks[0].MarkerTTL != 0 {
		t.Fatalf("Lookup must not mutate its input: %v %+v", err, unset.Blocks[0])
	}
}

func TestExplicitMinimumPrefix(t *testing.T) {
	clk := newFakeClock()
	e := newEngine(clk, ExplicitConfig{MinPrefix: func(model string) int {
		if model == "small" {
			return 100
		}
		return 500
	}})
	blocks := []ExplicitBlock{marked(sysB("s", 200), m5), marked(msgB("text", "m", 200), m5)} // 200 and 400 cumulative
	pl := run(t, e, clk, reqOf("big", ExplicitParams{}, blocks...))
	if pl.Write5m != 0 || len(pl.Skipped) != 2 || pl.Uncached != 400 {
		t.Fatalf("under the minimum nothing is written: %+v", pl)
	}
	if again := run(t, e, clk, reqOf("big", ExplicitParams{}, blocks...)); again.Read != 0 {
		t.Fatalf("nothing was cached, nothing to read: %+v", again)
	}
	pl = run(t, e, clk, reqOf("small", ExplicitParams{}, blocks...))
	if pl.Write5m != 400 || len(pl.Skipped) != 0 {
		t.Fatalf("small model: %+v", pl)
	}
	// Only the breakpoint past the minimum caches.
	pl = run(t, e, clk, reqOf("big", ExplicitParams{}, marked(sysB("s2", 200), m5), marked(msgB("text", "m2", 400), m5)))
	if pl.Write5m != 600 || len(pl.Skipped) != 1 || pl.Skipped[0] != "sys:s2" {
		t.Fatalf("partial: %+v", pl)
	}
}

func TestExplicitEntriesAreReadableOnlyAfterPublish(t *testing.T) {
	clk := newFakeClock()
	e := newEngine(clk, ExplicitConfig{})
	r := reqOf("m", ExplicitParams{}, marked(sysB("const", 400), m5), msgB("text", "q", 10))
	// Two requests arrive before either has produced a byte: both write.
	a, err := e.Lookup(r, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Lookup(r, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if a.Write5m != 400 || b.Write5m != 400 || a.Read+b.Read != 0 {
		t.Fatalf("stampede: a %+v b %+v", a, b)
	}
	e.Publish(a)
	// A request arriving after the first byte reads.
	c, _ := e.Lookup(r, clk.Now())
	if c.Read != 400 || c.Write5m != 0 {
		t.Fatalf("after publish: %+v", c)
	}
	// The second writer publishing later only refreshes the entry.
	e.Publish(b)
	if n, _ := e.Resident(); n != 1 {
		t.Fatalf("resident entries = %d, want 1", n)
	}
}

func TestExplicitTTLs(t *testing.T) {
	r5 := reqOf("m", ExplicitParams{}, marked(sysB("c5", 400), m5))
	r1 := reqOf("m", ExplicitParams{}, marked(sysB("c1", 400), h1))

	t.Run("a 5m entry expires after five minutes", func(t *testing.T) {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{})
		run(t, e, clk, r5)
		clk.Advance(m5 - time.Second)
		if pl := run(t, e, clk, r5); pl.Read != 400 {
			t.Fatalf("at 4m59: read %d", pl.Read)
		}
		// The read at 4m59 refreshed it: measured from that request's start.
		clk.Advance(m5 - time.Second)
		if pl := run(t, e, clk, r5); pl.Read != 400 {
			t.Fatalf("a read must refresh the lifetime: read %d", pl.Read)
		}
		clk.Advance(m5)
		if pl := run(t, e, clk, r5); pl.Read != 0 || pl.Write5m != 400 {
			t.Fatalf("after 5m of idleness: %+v", pl)
		}
	})
	t.Run("a 1h entry outlives it", func(t *testing.T) {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{})
		run(t, e, clk, r1)
		run(t, e, clk, r5)
		clk.Advance(30 * time.Minute)
		if pl := run(t, e, clk, r1); pl.Read != 400 {
			t.Fatalf("1h entry at 30m: %+v", pl)
		}
		if pl := run(t, e, clk, r5); pl.Read != 0 {
			t.Fatalf("5m entry at 30m must be gone: %+v", pl)
		}
		clk.Advance(h1 + time.Second)
		if pl := run(t, e, clk, r1); pl.Read != 0 || pl.Write1h != 400 {
			t.Fatalf("1h entry after an idle hour: %+v", pl)
		}
	})
	t.Run("the lifetime counts from the request that wrote it, not from its first byte", func(t *testing.T) {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{})
		pl, _ := e.Lookup(r5, clk.Now())
		clk.Advance(4 * time.Minute) // a slow generation
		e.Publish(pl)
		clk.Advance(90 * time.Second) // 5m30s after the request started
		if got := run(t, e, clk, r5); got.Read != 0 {
			t.Fatalf("entry should have expired 5m after the write request began: %+v", got)
		}
	})
	t.Run("mixed lifetimes bill 1h up to the highest 1h marker and 5m beyond", func(t *testing.T) {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{})
		pl := run(t, e, clk, reqOf("m", ExplicitParams{},
			marked(sysB("c", 300), h1), marked(msgB("text", "a", 200), h1), marked(msgB("text", "b", 100), m5), msgB("text", "tail", 40)))
		checkBilling(t, pl)
		if pl.Write1h != 500 || pl.Write5m != 100 || pl.Uncached != 40 {
			t.Fatalf("1h %d 5m %d uncached %d, want 500/100/40", pl.Write1h, pl.Write5m, pl.Uncached)
		}
		// Second request: hits at 500 (1h entry) and the 5m entry at 600; only the
		// growth beyond the highest hit is written, at 5m.
		pl = run(t, e, clk, reqOf("m", ExplicitParams{},
			marked(sysB("c", 300), h1), marked(msgB("text", "a", 200), h1), marked(msgB("text", "b", 100), m5), marked(msgB("text", "c", 80), m5)))
		checkBilling(t, pl)
		if pl.Read != 600 || pl.Write5m != 80 || pl.Write1h != 0 {
			t.Fatalf("second: %+v", pl)
		}
		// After the 5m entry expires only the 1h prefix is read.
		clk.Advance(10 * time.Minute)
		pl = run(t, e, clk, reqOf("m", ExplicitParams{},
			marked(sysB("c", 300), h1), marked(msgB("text", "a", 200), h1), marked(msgB("text", "b", 100), m5), marked(msgB("text", "c", 80), m5)))
		if pl.Read != 500 || pl.Write5m != 180 {
			t.Fatalf("after the 5m entries expired: %+v", pl)
		}
	})
}

func TestExplicitInvalidationTiers(t *testing.T) {
	// tools(2 blocks) | system | messages, breakpoints at the end of each tier.
	build := func(mut func(*ExplicitRequest)) ExplicitRequest {
		r := reqOf("m", ExplicitParams{},
			toolB("a", 100), marked(toolB("b", 100), h1),
			marked(sysB("const", 300), h1),
			msgB("text", "shared", 400), marked(msgB("text", "task", 200), m5))
		if mut != nil {
			mut(&r)
		}
		return r
	}
	const tools, system, msgs = 200, 300, 600
	cases := []struct {
		name   string
		mut    func(*ExplicitRequest)
		ahead  bool
		reads  TierTokens // expected ReadTiers
		checkW bool
	}{
		{"unchanged", nil, false, TierTokens{tools, system, msgs}, false},
		{"tool_choice keeps tools and system, not messages", func(r *ExplicitRequest) { r.Params.ToolChoice = `{"type":"none"}` }, false, TierTokens{tools, system, 0}, true},
		{"images keep tools and system, not messages", func(r *ExplicitRequest) { r.Blocks[3].HasImage = true }, false, TierTokens{tools, system, 0}, true},
		{"thinking config invalidates messages only", func(r *ExplicitRequest) { r.Params.Thinking = "adaptive" }, false, TierTokens{tools, system, 0}, true},
		{"effort invalidates messages only", func(r *ExplicitRequest) { r.Params.Effort = "max" }, false, TierTokens{tools, system, 0}, true},
		{"thinking config invalidates everything on models that render it ahead of tools", func(r *ExplicitRequest) { r.Params.Thinking = "adaptive" }, true, TierTokens{}, true},
		{"speed keeps tools, not system or messages", func(r *ExplicitRequest) { r.Params.SystemToggles = "fast" }, false, TierTokens{tools, 0, 0}, true},
		{"system text keeps tools", func(r *ExplicitRequest) { r.Blocks[2].Hash = "sys:edited" }, false, TierTokens{tools, 0, 0}, true},
		{"a tool edit rebuilds everything", func(r *ExplicitRequest) { r.Blocks[0].Hash = "tool:a-edited" }, false, TierTokens{}, true},
		{"tool order rebuilds everything", func(r *ExplicitRequest) { r.Blocks[0], r.Blocks[1] = r.Blocks[1], r.Blocks[0] }, false, TierTokens{}, true},
		{"a model change rebuilds everything", func(r *ExplicitRequest) { r.Model = "other" }, false, TierTokens{}, true},
		{"message text keeps tools and system", func(r *ExplicitRequest) { r.Blocks[3].Hash = "m:text:edited" }, false, TierTokens{tools, system, 0}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := newFakeClock()
			cfg := ExplicitConfig{}
			if c.ahead {
				cfg.ParamsAheadOfTools = func(string) bool { return true }
			}
			e := newEngine(clk, cfg)
			run(t, e, clk, build(nil))
			pl := run(t, e, clk, build(c.mut))
			checkBilling(t, pl)
			if pl.ReadTiers != c.reads {
				t.Errorf("read tiers = %+v, want %+v (hits %+v)", pl.ReadTiers, c.reads, pl.Hits)
			}
			if c.checkW && pl.Write5m+pl.Write1h == 0 {
				t.Error("an invalidated tier must be rewritten")
			}
		})
	}
	// Setting a parameter back restores the hits: the invalidated entries were
	// never overwritten, only bypassed.
	clk := newFakeClock()
	e := newEngine(clk, ExplicitConfig{})
	run(t, e, clk, build(nil))
	run(t, e, clk, build(func(r *ExplicitRequest) { r.Params.ToolChoice = `{"type":"none"}` }))
	if pl := run(t, e, clk, build(nil)); pl.Read != tools+system+msgs {
		t.Fatalf("flipping tool_choice back should hit the original entries: %+v", pl)
	}
}

func TestExplicitEvictsLeavesBeforeRoots(t *testing.T) {
	// chain: root(200) -> mid(200) -> leaf(200).
	chain := func(tag string) ExplicitRequest {
		return reqOf("m", ExplicitParams{},
			marked(sysB("root-"+tag, 200), m5), marked(msgB("text", "mid-"+tag, 200), m5), marked(msgB("text", "leaf-"+tag, 200), m5))
	}
	probe := func(t *testing.T, e *ExplicitEngine, clk *fakeClock, tag string) int {
		t.Helper()
		// Same content, marker only on the tail: reads whatever prefix survived.
		pl, err := e.Lookup(reqOf("m", ExplicitParams{}, marked(sysB("root-"+tag, 200), m5), marked(msgB("text", "mid-"+tag, 200), m5),
			marked(msgB("text", "leaf-"+tag, 200), m5)), clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		return pl.Read
	}

	t.Run("one entry too many costs the oldest leaf, not its root", func(t *testing.T) {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{CapacityEntries: 5})
		run(t, e, clk, chain("a"))
		run(t, e, clk, chain("b")) // 6 entries into 5
		if got := probe(t, e, clk, "a"); got != 400 {
			t.Fatalf("a keeps root and middle, loses its leaf: read %d, want 400", got)
		}
		if got := probe(t, e, clk, "b"); got != 600 {
			t.Fatalf("b is untouched: read %d, want 600", got)
		}
	})
	t.Run("under more pressure the oldest chain shrinks from the tail toward its root", func(t *testing.T) {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{CapacityEntries: 4})
		run(t, e, clk, chain("a"))
		run(t, e, clk, chain("b")) // 6 into 4: a's leaf, then a's middle
		if got := probe(t, e, clk, "a"); got != 200 {
			t.Fatalf("a keeps only its root: read %d, want 200", got)
		}
		roots := 0
		for _, en := range e.Entries() {
			if en.Tokens == 200 {
				roots++
			}
		}
		if roots != 2 {
			t.Fatalf("both roots must outlive every descendant: %+v", e.Entries())
		}
	})
	t.Run("a root is evicted only when nothing else is left", func(t *testing.T) {
		clk := newFakeClock()
		e := newEngine(clk, ExplicitConfig{CapacityEntries: 1})
		run(t, e, clk, chain("a"))
		if n, _ := e.Resident(); n != 1 {
			t.Fatalf("resident = %d", n)
		}
		if got := probe(t, e, clk, "a"); got != 200 {
			t.Fatalf("read %d, want the root 200", got)
		}
	})
}

func TestExplicitEvictionUnderTokenPressure(t *testing.T) {
	clk := newFakeClock()
	// Entries own only the tokens beyond their parent: a chain of three 200-token
	// blocks occupies 600 tokens, not 200+400+600.
	e := newEngine(clk, ExplicitConfig{CapacityTokens: 1000})
	mk := func(tag string) ExplicitRequest {
		return reqOf("m", ExplicitParams{}, marked(sysB("root-"+tag, 200), m5), marked(msgB("text", "mid-"+tag, 200), m5), marked(msgB("text", "leaf-"+tag, 200), m5))
	}
	run(t, e, clk, mk("a"))
	if _, tokens := e.Resident(); tokens != 600 {
		t.Fatalf("tokens = %d, want 600", tokens)
	}
	run(t, e, clk, mk("b")) // 1200 > 1000: one leaf goes, the older one
	n, tokens := e.Resident()
	if tokens > 1000 || n != 5 {
		t.Fatalf("entries %d tokens %d", n, tokens)
	}
	// The least recently used leaf was a's; b is intact.
	if pl := run(t, e, clk, mk("b")); pl.Read != 600 {
		t.Fatalf("b evicted? read %d", pl.Read)
	}
	// A read refreshes recency: touch a's remaining chain, then add c; a's leaf
	// (if present) survives and b's leaf is the victim.
	run(t, e, clk, mk("a"))
	run(t, e, clk, mk("c"))
	if pl := run(t, e, clk, mk("a")); pl.Read < 400 {
		t.Fatalf("recently used chain lost its prefix: read %d", pl.Read)
	}
}

func TestExplicitExpiredEntriesLeaveBeforeLiveOnes(t *testing.T) {
	clk := newFakeClock()
	e := newEngine(clk, ExplicitConfig{CapacityEntries: 2})
	old := reqOf("m", ExplicitParams{}, marked(sysB("old", 300), m5))
	live := reqOf("m", ExplicitParams{}, marked(sysB("live", 300), h1))
	run(t, e, clk, old)
	run(t, e, clk, live)
	clk.Advance(10 * time.Minute)
	run(t, e, clk, reqOf("m", ExplicitParams{}, marked(sysB("new", 300), m5)))
	if pl := run(t, e, clk, live); pl.Read != 300 {
		t.Fatalf("the live 1h entry must survive: %+v", pl)
	}
}

func TestExplicitBillingInvariantsOnRandomTraffic(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	clk := newFakeClock()
	e := newEngine(clk, ExplicitConfig{CapacityTokens: 5000})
	conv := func(n int) []ExplicitBlock {
		blocks := []ExplicitBlock{toolB("t", 150), sysB("const", 250)}
		for i := 0; i < n; i++ {
			kind := []string{"text", "tool_use", "tool_result"}[i%3]
			blocks = append(blocks, msgB(kind, fmt.Sprint("b", i), 30+i%7*10))
		}
		return blocks
	}
	for i := 0; i < 400; i++ {
		blocks := conv(rng.Intn(40))
		var idx []int
		for j := range blocks {
			if rng.Intn(5) == 0 {
				idx = append(idx, j)
			}
		}
		if len(idx) > 4 {
			idx = idx[:4]
		}
		ttl := h1
		for _, j := range idx {
			if rng.Intn(3) == 0 {
				ttl = m5 // 1h markers may only come first
			}
			blocks[j] = marked(blocks[j], ttl)
		}
		pl, err := e.Lookup(reqOf("m", ExplicitParams{}, blocks...), clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		checkBilling(t, pl)
		if pl.Read < 0 || pl.Write5m < 0 || pl.Write1h < 0 || pl.Uncached < 0 {
			t.Fatalf("negative counter: %+v", pl)
		}
		e.Publish(pl)
		clk.Advance(time.Duration(rng.Intn(120)) * time.Second)
	}
}

func TestExplicitIsRaceFree(t *testing.T) {
	e := NewExplicitEngine(ExplicitConfig{DefaultMinPrefix: 50, CapacityEntries: 64})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				r := reqOf("m", ExplicitParams{}, marked(sysB(fmt.Sprint("s", i%5), 100), m5), marked(msgB("text", fmt.Sprint(g, i%3), 100), m5))
				pl, err := e.Lookup(r, time.Now())
				if err != nil {
					t.Error(err)
					return
				}
				e.Publish(pl)
				e.Entries()
			}
		}(g)
	}
	wg.Wait()
}
