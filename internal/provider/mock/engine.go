// Package mock is a deterministic, protocol-strict fake model provider whose
// prompt cache behaves like the real thing.
//
// Cache behaviour is the whole point. A mock that always says "cache hit"
// would let the harness regress silently; this one hashes actual prompt bytes
// into block chains, evicts under memory pressure, only publishes a request's
// blocks once its first token is out, and routes by conversation affinity
// across several independent engines. Tests therefore fail when a layer edit
// invalidates a prefix, when a fan-out forgets to warm first, or when agents
// that should share an engine do not.
package mock

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"
)

// bytesPerToken is the mock tokenizer's fixed ratio. It deliberately differs
// from the harness's default estimator (3.6) so calibration is exercised.
const bytesPerToken = 4

// tokens returns the mock token count of n bytes.
func tokens(n int) int { return (n + bytesPerToken - 1) / bytesPerToken }

// EngineConfig sets one engine's prefix-cache behaviour.
type EngineConfig struct {
	// BlockTokens is the cache granularity: only whole blocks are cached, as in
	// paged-attention engines. Default 16.
	BlockTokens int
	// MinCacheTokens: prefixes shorter than this are never reported cached.
	MinCacheTokens int
	// CapacityBlocks bounds resident blocks (LRU eviction). 0 = unbounded.
	CapacityBlocks int
	// TTL evicts blocks idle longer than this (0 = never; pure LRU).
	TTL time.Duration
}

func (c *EngineConfig) defaults() {
	if c.BlockTokens == 0 {
		c.BlockTokens = 16
	}
}

// Engine is one inference engine with its own prefix cache.
type Engine struct {
	cfg EngineConfig
	now func() time.Time

	mu     sync.Mutex
	blocks map[uint64]*list.Element // chain hash -> LRU element
	lru    *list.List               // front = most recent
}

type blockEntry struct {
	key  uint64
	last time.Time
}

// NewEngine builds an engine.
func NewEngine(cfg EngineConfig, now func() time.Time) *Engine {
	cfg.defaults()
	if now == nil {
		now = time.Now
	}
	return &Engine{cfg: cfg, now: now, blocks: map[uint64]*list.Element{}, lru: list.New()}
}

// chain returns the block-chain hashes of data for every complete block.
func (e *Engine) chain(data []byte) []uint64 {
	bs := e.cfg.BlockTokens * bytesPerToken
	n := len(data) / bs
	out := make([]uint64, 0, n)
	var prev uint64
	buf := make([]byte, 8+bs)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint64(buf[:8], prev)
		copy(buf[8:], data[i*bs:(i+1)*bs])
		sum := sha256.Sum256(buf)
		prev = binary.LittleEndian.Uint64(sum[:8])
		out = append(out, prev)
	}
	return out
}

// Lookup returns how many tokens of data are served from cache right now.
func (e *Engine) Lookup(data []byte) int {
	chain := e.chain(data)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	hit := 0
	for _, h := range chain {
		el, ok := e.blocks[h]
		if !ok {
			break
		}
		be := el.Value.(*blockEntry)
		if e.cfg.TTL > 0 && now.Sub(be.last) > e.cfg.TTL {
			break
		}
		hit++
	}
	tok := hit * e.cfg.BlockTokens
	if tok < e.cfg.MinCacheTokens {
		return 0
	}
	// Touch on hit: a read refreshes the entry.
	for i := 0; i < hit; i++ {
		el := e.blocks[chain[i]]
		el.Value.(*blockEntry).last = now
		e.lru.MoveToFront(el)
	}
	return tok
}

// Insert publishes every complete block of data as cached.
func (e *Engine) Insert(data []byte) {
	chain := e.chain(data)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	for _, h := range chain {
		if el, ok := e.blocks[h]; ok {
			el.Value.(*blockEntry).last = now
			e.lru.MoveToFront(el)
			continue
		}
		e.blocks[h] = e.lru.PushFront(&blockEntry{key: h, last: now})
	}
	for e.cfg.CapacityBlocks > 0 && e.lru.Len() > e.cfg.CapacityBlocks {
		back := e.lru.Back()
		delete(e.blocks, back.Value.(*blockEntry).key)
		e.lru.Remove(back)
	}
}

// Resident reports the number of cached blocks.
func (e *Engine) Resident() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lru.Len()
}

// Router places requests on engines, honouring conversation affinity the way
// the marketplace does: a session id pins a conversation to the engine that
// last served it, for a while past the last request.
type Router struct {
	mu      sync.Mutex
	engines []*Engine
	pins    map[string]pin
	rr      int
	pinTTL  time.Duration
	now     func() time.Time
}

type pin struct {
	engine int
	until  time.Time
}

// NewRouter builds a router over engines.
func NewRouter(engines []*Engine, pinTTL time.Duration, now func() time.Time) *Router {
	if now == nil {
		now = time.Now
	}
	if pinTTL == 0 {
		pinTTL = time.Hour
	}
	return &Router{engines: engines, pins: map[string]pin{}, pinTTL: pinTTL, now: now}
}

// Route picks an engine index. Requests without a session id are spread
// round-robin, exactly the failure mode affinity exists to avoid.
func (r *Router) Route(model, session string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := model + "\x00" + session
	if session != "" {
		if p, ok := r.pins[key]; ok && r.now().Before(p.until) {
			r.pins[key] = pin{engine: p.engine, until: r.now().Add(r.pinTTL)}
			return p.engine
		}
	}
	idx := r.rr % len(r.engines)
	r.rr++
	if session != "" {
		r.pins[key] = pin{engine: idx, until: r.now().Add(r.pinTTL)}
	}
	return idx
}

// Engine returns engine i.
func (r *Router) Engine(i int) *Engine { return r.engines[i] }

// Len is the number of engines.
func (r *Router) Len() int { return len(r.engines) }
