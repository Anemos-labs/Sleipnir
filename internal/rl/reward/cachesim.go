package reward

import (
	"crypto/sha256"
	"sort"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

// This file is a small, exported, deterministic model of a provider's prefix
// cache. It replicates the semantics of internal/kv/sim's cacheSim (exact
// prefix chains, TTL or LRU eviction, entries readable only from the first
// response byte, a write premium, per-engine caches with optional routing
// affinity) on top of cost.Model instead of sim's private Provider type, so the
// reward package can reprice recorded episodes without importing kv or sim.
// cachesim_test.go replays sim's TestCacheSemantics cases to keep the two honest.

// Defaults of the time-to-first-byte model; they equal sim.Anthropic().
const (
	defaultTTFBBase       = 1500 * time.Millisecond
	defaultTTFBPerKTok    = 60 * time.Millisecond
	decodePerToken        = 20 * time.Millisecond // only used for providers that publish entries at completion
	DefaultLRUCapacity    = 3_000_000             // tokens per engine when the target has no TTL and no capacity is given
	scanWindow            = 128                   // nodes examined per request on long chains (see candidates)
	scanHead              = 8
	evictTo               = 0.9 // evict down to this share of capacity, as sim does
	maxEngines            = 4096
	defaultMaxBreakpoints = 4
)

// Seg is one segment of a prompt in prefix order. Two prompts share a cache
// entry exactly when they share the same sequence of segment ids up to it.
type Seg struct {
	ID     string
	Tokens int
	// Ephemeral segments (a per-request hot tail, a one-shot instruction) are
	// billed uncached and never stored, so they do not break the chain.
	Ephemeral bool
	// Long marks the segment as the end of a layer worth the longest TTL the
	// provider offers (shared pins).
	Long bool
}

// CacheResult is how one request was billed by the modelled cache.
type CacheResult struct {
	// Uncached, Read and the two write classes partition the prompt tokens.
	Uncached, Read, Write5m, Write1h int
	// Hit reports whether any prefix was read.
	Hit bool
	// TTFB is the modelled time to first byte.
	TTFB time.Duration
	// Ready is when the entries this request stored become readable.
	Ready  time.Duration
	Engine int
}

// Write is the total written to cache.
func (r CacheResult) Write() int { return r.Write5m + r.Write1h }

// node is one chain link: the hash of the segment sequence up to it.
type node struct {
	key  [16]byte
	cum  int // prompt tokens through this node, ephemeral segments excluded
	long bool
}

// chainKey hashes a fixed-width parent key followed by a segment ID into sixteen bytes.
func chainKey(parent [16]byte, id string) (k [16]byte) {
	h := sha256.New()
	h.Write(parent[:])
	h.Write([]byte(id))
	copy(k[:], h.Sum(nil))
	return k
}

type entry struct {
	ready   time.Duration // readable from here on
	touched time.Duration // start of the current TTL window
	last    time.Duration // recency for LRU eviction
	seq     uint64
	tokens  int // size of the node itself, for capacity accounting
	ttl     time.Duration
}

type engine struct {
	entries map[[16]byte]*entry
	tokens  int
}

// Cache is the modelled provider cache. It is not safe for concurrent use.
type Cache struct {
	m        cost.Model
	o        RepriceOptions
	engines  []*engine
	rr       int
	pins     map[string]int
	seq      uint64
	ttl      time.Duration // default entry lifetime (0: none, LRU only)
	longTTL  time.Duration // lifetime of Long segments
	capacity int
}

// NewCache builds an empty cache for a target model. Options that are zero take
// the documented defaults.
func NewCache(m cost.Model, o RepriceOptions) *Cache {
	c := &Cache{m: m, o: o, pins: map[string]int{}}
	n := o.Engines
	if n < 1 {
		n = 1
	}
	if n > maxEngines {
		n = maxEngines
	}
	for i := 0; i < n; i++ {
		c.engines = append(c.engines, &engine{entries: map[[16]byte]*entry{}})
	}
	c.ttl = m.Cache.DefaultTTL()
	c.longTTL = c.ttl
	if !o.NoSharedLongTTL {
		for _, t := range m.Cache.TTLs {
			if t > c.longTTL {
				c.longTTL = t
			}
		}
	}
	c.capacity = o.CapacityTokens
	if c.capacity == 0 && c.ttl == 0 {
		c.capacity = DefaultLRUCapacity
	}
	return c
}

// caching reports whether the target caches at all. A model with neither
// explicit nor automatic caching bills every prompt token uncached.
func (c *Cache) caching() bool { return c.m.Cache.Explicit || c.m.Cache.Auto }

// Route picks the engine for a request. With one engine it is always 0. A key
// pins a routing group to one engine (round-robin on first sight), which is what
// prompt_cache_key / X-Session-Id do; an empty key is spread round-robin, so
// requests may land on an engine that never saw their prefix.
func (c *Cache) Route(key string) int {
	if len(c.engines) == 1 {
		return 0
	}
	if key != "" {
		if e, ok := c.pins[key]; ok {
			return e
		}
		c.rr++
		c.pins[key] = c.rr % len(c.engines)
		return c.pins[key]
	}
	c.rr++
	return c.rr % len(c.engines)
}

// Request prices one prompt issued at time at (an offset from any fixed origin)
// and stores the prefixes it processed, the way sim's cacheSim.request does.
// bp lists indexes into segs after which an explicit-cache breakpoint sits; it
// is ignored by automatic caches, which store every prefix they process.
func (c *Cache) Request(eng int, at time.Duration, segs []Seg, bp []int) CacheResult {
	nodes := make([]node, 0, len(segs))
	segToNode := make([]int, len(segs))
	var parent [16]byte
	cum, tail := 0, 0
	for i, s := range segs {
		tok := s.Tokens
		if tok < 0 {
			tok = 0
		}
		if s.Ephemeral {
			tail += tok
			segToNode[i] = -1
			continue
		}
		parent = chainKey(parent, s.ID)
		cum += tok
		nodes = append(nodes, node{key: parent, cum: cum, long: s.Long})
		segToNode[i] = len(nodes) - 1
	}
	var nbp []int
	for _, i := range bp {
		if i >= 0 && i < len(segToNode) && segToNode[i] >= 0 {
			nbp = append(nbp, segToNode[i])
		}
	}
	return c.do(eng, at, nodes, tail, nbp, reqInfo{full: true})
}

// reqInfo carries what a caller knows about the request beyond its prompt.
type reqInfo struct {
	latency time.Duration // recorded total latency, 0 if unknown
	out     int           // output tokens, for providers that publish at completion
	full    bool          // scan every node (small chains, tests)
}

// candidates lists the node indexes worth probing for a hit, deepest first. On
// long chains only a head and a tail window are probed: entries are written at
// the end of each request and refreshed on every read, so the deepest fresh
// entry is always near the tail, and the low nodes (shared pins) are the ones
// other agents keep alive. This keeps a request O(1) in the chain length.
func candidates(n int, full bool) []int {
	if n == 0 {
		return nil
	}
	if full || n <= scanWindow+scanHead {
		out := make([]int, n)
		for i := range out {
			out[i] = n - 1 - i
		}
		return out
	}
	out := make([]int, 0, scanWindow+scanHead)
	for i := n - 1; i >= n-scanWindow; i-- {
		out = append(out, i)
	}
	for i := scanHead - 1; i >= 0; i-- {
		out = append(out, i)
	}
	return out
}

func (c *Cache) do(eng int, now time.Duration, nodes []node, tail int, bp []int, inf reqInfo) CacheResult {
	if eng < 0 || eng >= len(c.engines) {
		eng = 0
	}
	total := tail
	if n := len(nodes); n > 0 {
		total += nodes[n-1].cum
	}
	res := CacheResult{Engine: eng}
	if !c.caching() || len(nodes) == 0 {
		res.Uncached = total
		res.TTFB = c.ttfb(res.Uncached, inf)
		res.Ready = now + res.TTFB
		return res
	}
	e := c.engines[eng]
	minPrefix := c.m.Cache.MinPrefixTokens

	fresh := func(n *node) *entry {
		en, ok := e.entries[n.key]
		if !ok || n.cum < minPrefix || en.ready > now {
			return nil
		}
		if en.ttl > 0 && now-en.touched > en.ttl {
			return nil
		}
		return en
	}

	// The deepest fresh entry wins. An entry at a deeper node exists only if the
	// whole prefix before it matched, so no continuity check is needed.
	cand := candidates(len(nodes), inf.full)
	hitIdx, hitTok := -1, 0
	for _, i := range cand {
		if fresh(&nodes[i]) != nil {
			hitIdx, hitTok = i, nodes[i].cum
			break
		}
	}
	if hitIdx >= 0 {
		for _, i := range cand {
			if i > hitIdx {
				continue
			}
			if en := fresh(&nodes[i]); en != nil {
				en.last = now
				if c.m.Cache.ReadRefreshesTTL {
					en.touched = now
				}
			}
		}
	}

	var toStore []int
	if c.m.Cache.Auto {
		for i := hitIdx + 1; i < len(nodes); i++ {
			if nodes[i].cum >= minPrefix {
				toStore = append(toStore, i)
			}
		}
	} else {
		set := map[int]bool{}
		for _, i := range bp {
			set[i] = true
		}
		idx := make([]int, 0, len(set))
		for i := range set {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for _, i := range idx {
			if i > hitIdx && i < len(nodes) && nodes[i].cum >= minPrefix {
				toStore = append(toStore, i)
			}
		}
	}

	lastStore := hitTok
	if n := len(toStore); n > 0 {
		lastStore = nodes[toStore[n-1]].cum
	}
	res.Read = c.roundHit(hitTok)
	res.Hit = res.Read > 0
	write := lastStore - hitTok
	res.Uncached = total - res.Read - write
	if res.Uncached < 0 {
		res.Uncached = 0
	}
	res.TTFB = c.ttfb(res.Uncached+write, inf)
	switch c.m.Cache.ReadableAfter {
	case cost.ReadableImmediately:
		res.Ready = now
	case cost.ReadableAtComplete:
		if inf.latency > 0 {
			res.Ready = now + inf.latency
		} else {
			res.Ready = now + res.TTFB + time.Duration(inf.out)*decodePerToken
		}
	default:
		res.Ready = now + res.TTFB
	}

	prev := hitTok
	for _, i := range toStore {
		n := &nodes[i]
		span := n.cum - prev
		prev = n.cum
		ttl := c.ttl
		if n.long {
			ttl = c.longTTL
		}
		if n.long && c.longTTL > c.ttl {
			res.Write1h += span
		} else {
			res.Write5m += span
		}
		if en, ok := e.entries[n.key]; ok {
			// Two requests can write the same prefix concurrently; it becomes
			// readable at the earlier first byte. A stale entry is rewritten.
			if en.ready <= now || res.Ready < en.ready {
				e.tokens += span - en.tokens
				en.ready, en.touched, en.last, en.ttl, en.tokens = res.Ready, res.Ready, res.Ready, ttl, span
			}
		} else {
			c.seq++
			e.entries[n.key] = &entry{ready: res.Ready, touched: res.Ready, last: res.Ready, seq: c.seq, tokens: span, ttl: ttl}
			e.tokens += span
		}
	}
	c.evict(e)
	return res
}

// roundHit applies the provider's minimum and granularity to a cache hit:
// automatic caches serve hits in fixed increments above a floor.
func (c *Cache) roundHit(hit int) int {
	if hit <= 0 {
		return 0
	}
	cm := c.m.Cache
	if hit < cm.MinPrefixTokens {
		return 0
	}
	if g := cm.Granularity; g > 0 && cm.Auto {
		return cm.MinPrefixTokens + (hit-cm.MinPrefixTokens)/g*g
	}
	return hit
}

// ttfb estimates first-byte latency from billed tokens and caps it at a positive recorded response
// latency.
func (c *Cache) ttfb(billed int, inf reqInfo) time.Duration {
	base, per := defaultTTFBBase, defaultTTFBPerKTok
	if c.o.TTFBBaseMs > 0 {
		base = time.Duration(c.o.TTFBBaseMs) * time.Millisecond
	}
	if c.o.TTFBPerKTokMs > 0 {
		per = time.Duration(c.o.TTFBPerKTokMs * float64(time.Millisecond))
	}
	d := base + time.Duration(float64(billed)/1000*float64(per))
	// A recorded latency is an upper bound: the first byte cannot arrive after
	// the whole response, and fast local endpoints must not be modelled slower
	// than they were.
	if inf.latency > 0 && inf.latency < d {
		d = inf.latency
	}
	return d
}

// evict drops least-recently-used entries when an engine exceeds its capacity.
// Ties are broken by insertion order so the result never depends on map order.
func (c *Cache) evict(e *engine) {
	if c.capacity <= 0 || e.tokens <= c.capacity {
		return
	}
	type kv struct {
		k  [16]byte
		en *entry
	}
	list := make([]kv, 0, len(e.entries))
	for k, en := range e.entries {
		list = append(list, kv{k, en})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].en.last != list[j].en.last {
			return list[i].en.last < list[j].en.last
		}
		return list[i].en.seq < list[j].en.seq
	})
	target := int(float64(c.capacity) * evictTo)
	for _, x := range list {
		if e.tokens <= target {
			break
		}
		delete(e.entries, x.k)
		e.tokens -= x.en.tokens
	}
}
