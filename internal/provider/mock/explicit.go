package mock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// This file is a self-contained model of Anthropic-style explicit prompt
// caching, the behaviour the chat mock's automatic block-hash cache cannot
// reproduce. It knows nothing about HTTP or JSON: callers describe a request as
// an ordered list of blocks (tools, then system, then messages), mark the blocks
// that carry a cache_control breakpoint, and get back what the provider would
// bill as reads, writes and uncached input.
//
// The rules it implements, from the provider docs:
//
//   - A breakpoint writes one entry: the hash of the whole prefix ending at its
//     block. Nothing is written for earlier positions.
//   - To read, each breakpoint checks its own entry and then walks back at most
//     20 positions looking for an entry an earlier request wrote (the breakpoint
//     is position 1). A run of consecutive tool_use blocks is one position, and
//     so is a run of tool_result blocks. A turn that appends more than 20
//     positions therefore orphans the previous entry unless an intermediate
//     marker was placed.
//   - Reads bill up to the highest hit; writes bill from there to the last
//     breakpoint (1h-TTL markers first, then 5m); the rest is uncached input.
//   - Prefixes below the model's minimum are silently not cached.
//   - Entries become readable only when Publish is called, which the HTTP layer
//     does at the first response byte: N parallel requests over a cold prefix all
//     pay the write.
//   - A TTL is measured from the start of the request that wrote or read the
//     entry, and a read refreshes it.
//   - Parameters are cache-key inputs by tier: a model change rebuilds
//     everything; a tool change rebuilds tools, system and messages; a system
//     change rebuilds system and messages; tool_choice, images, thinking and
//     effort rebuild only the messages tier.
//   - Under memory pressure the least recently used LEAF is evicted first, so a
//     shared root outlives its private branches (vLLM frees tail blocks first
//     for the same reason).

// Tier is one of the three cache tiers, in render order.
type Tier int

const (
	TierTools Tier = iota
	TierSystem
	TierMessages
)

func (t Tier) String() string {
	switch t {
	case TierTools:
		return "tools"
	case TierSystem:
		return "system"
	case TierMessages:
		return "messages"
	}
	return "?"
}

// TierTokens counts tokens per tier.
type TierTokens struct{ Tools, System, Messages int }

// Total is the sum over all tiers.
func (t TierTokens) Total() int { return t.Tools + t.System + t.Messages }

func (t *TierTokens) add(tier Tier, n int) {
	switch tier {
	case TierTools:
		t.Tools += n
	case TierSystem:
		t.System += n
	default:
		t.Messages += n
	}
}

// ExplicitConfig configures an ExplicitEngine. The zero value is the documented
// Claude API behaviour with an unbounded cache.
type ExplicitConfig struct {
	// Now is the clock (tests inject a fake one to expire TTLs without sleeping).
	Now func() time.Time
	// Lookback is how many positions a breakpoint searches back. Default 20.
	Lookback int
	// MaxBreakpoints is the marker limit per request. Default 4.
	MaxBreakpoints int
	// TTL5m and TTL1h are the two lifetimes. Defaults 5 minutes and 1 hour.
	TTL5m, TTL1h time.Duration
	// MinPrefix returns the minimum cacheable prefix in tokens for a model.
	// Default: DefaultMinPrefix for every model.
	MinPrefix        func(model string) int
	DefaultMinPrefix int
	// CapacityTokens bounds the tokens resident in the cache and CapacityEntries
	// the number of entries; a prefix is stored once, so an entry only owns the
	// tokens beyond its parent. 0 means unbounded.
	CapacityTokens  int
	CapacityEntries int
	// NoCollapseRuns disables the rule that a run of tool_use blocks (or of
	// tool_result blocks) counts as one lookback position (Claude API only).
	NoCollapseRuns bool
	// ParamsAheadOfTools reports models that render the thinking configuration
	// ahead of tools and system, so changing it rebuilds every tier.
	ParamsAheadOfTools func(model string) bool
}

func (c *ExplicitConfig) defaults() {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Lookback <= 0 {
		c.Lookback = 20
	}
	if c.MaxBreakpoints <= 0 {
		c.MaxBreakpoints = 4
	}
	if c.TTL5m <= 0 {
		c.TTL5m = 5 * time.Minute
	}
	if c.TTL1h <= 0 {
		c.TTL1h = time.Hour
	}
	if c.DefaultMinPrefix <= 0 {
		c.DefaultMinPrefix = 1024
	}
}

// ExplicitBlock is one block of a rendered request, in render order.
type ExplicitBlock struct {
	Tier Tier
	// Kind is "tool", "text", "image", "tool_use", "tool_result", "thinking", ...
	// Only tool_use and tool_result matter to the engine (run collapsing).
	Kind string
	// Hash identifies the block's content, excluding any cache_control marker.
	Hash string
	// Tokens is the block's size.
	Tokens int
	// HasImage marks a block that contains an image (a tool_result may).
	HasImage bool
	// Marker is set when a cache_control breakpoint sits on the block, and
	// MarkerTTL is its lifetime (the engine's TTL5m or TTL1h).
	Marker    bool
	MarkerTTL time.Duration
	// Label names the block in diagnostics ("messages[3].content[1]").
	Label string
}

// ExplicitParams are the request parameters that are part of a cache key. Empty
// strings mean "omitted".
type ExplicitParams struct {
	// ToolChoice is the canonical tool_choice (including disable_parallel_tool_use).
	ToolChoice string
	// Thinking is the canonical thinking configuration that affects rendering
	// (type and budget; not display or block_binding).
	Thinking string
	// Effort is top-level output_config.effort; the caller maps the model's
	// default level to "" because an explicit default equals omission.
	Effort string
	// SystemToggles are settings that change the system tier (speed, web search,
	// citations).
	SystemToggles string
}

// ExplicitRequest is one request as the cache sees it.
type ExplicitRequest struct {
	Model  string
	Params ExplicitParams
	// Blocks must be grouped by tier in render order: tools, system, messages.
	Blocks []ExplicitBlock
}

// ExplicitHit is one breakpoint's read.
type ExplicitHit struct {
	Marker string // label of the breakpoint's block
	Found  string // label of the block whose entry was found
	Tokens int    // prefix tokens covered by the entry
	Tier   Tier
}

// ExplicitPlan is what a request would be billed, computed at arrival. It does
// not touch the cache's entries until Publish.
type ExplicitPlan struct {
	Model string
	// Total is the prompt size in tokens. Total = Uncached + Read + Write5m + Write1h.
	Total    int
	Read     int
	Write5m  int
	Write1h  int
	Uncached int
	// ReadTiers and WriteTiers say which tiers the read and written tokens fall in.
	ReadTiers, WriteTiers TierTokens
	// Hits lists what each breakpoint found. Skipped lists breakpoints under the
	// model's minimum prefix, which are silently ignored.
	Hits    []ExplicitHit
	Skipped []string
	// Markers is the number of breakpoints in the request.
	Markers int

	start  time.Time
	writes []explicitWrite
}

type explicitWrite struct {
	key    string
	parent string
	tokens int
	ttl    time.Duration
	tier   Tier
	label  string
}

// ExplicitError is a request the API rejects with HTTP 400.
type ExplicitError struct{ Msg string }

func (e *ExplicitError) Error() string { return e.Msg }

// EntryInfo describes one resident entry (for tests).
type EntryInfo struct {
	Tokens  int
	Tier    Tier
	TTL     time.Duration
	Expires time.Time
	Leaf    bool
	Label   string
}

type explicitEntry struct {
	key      string
	tokens   int // prefix length
	own      int // tokens beyond the parent: what the entry adds to memory
	parent   string
	children int
	ttl      time.Duration
	expires  time.Time
	used     uint64
	tier     Tier
	label    string
}

// ExplicitEngine is one workspace's explicit cache.
type ExplicitEngine struct {
	cfg ExplicitConfig

	mu      sync.Mutex
	entries map[string]*explicitEntry
	tick    uint64
	tokens  int
}

// NewExplicitEngine builds an engine.
func NewExplicitEngine(cfg ExplicitConfig) *ExplicitEngine {
	cfg.defaults()
	return &ExplicitEngine{cfg: cfg, entries: map[string]*explicitEntry{}}
}

func (e *ExplicitEngine) minPrefix(model string) int {
	if e.cfg.MinPrefix != nil {
		if n := e.cfg.MinPrefix(model); n > 0 {
			return n
		}
	}
	return e.cfg.DefaultMinPrefix
}

func chainHash(prev string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(prev))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

type chainBlock struct {
	hash string
	cum  int
	pos  int
}

// Lookup computes what req is billed at time start, and refreshes the lifetime
// of every entry it reads. It writes nothing: call Publish at the first response
// byte, because that is when the provider makes entries readable.
func (e *ExplicitEngine) Lookup(req ExplicitRequest, start time.Time) (*ExplicitPlan, error) {
	req.Blocks = append([]ExplicitBlock(nil), req.Blocks...) // never mutate the caller's slice
	var markers []int
	for i := range req.Blocks {
		if req.Blocks[i].Marker {
			if req.Blocks[i].MarkerTTL <= 0 {
				req.Blocks[i].MarkerTTL = e.cfg.TTL5m // the default lifetime
			}
			markers = append(markers, i)
		}
	}
	if len(markers) > e.cfg.MaxBreakpoints {
		return nil, &ExplicitError{Msg: fmt.Sprintf("A maximum of %d blocks with cache_control may be provided. Found %d.", e.cfg.MaxBreakpoints, len(markers))}
	}
	for i := 1; i < len(markers); i++ {
		prev, cur := req.Blocks[markers[i-1]], req.Blocks[markers[i]]
		if cur.MarkerTTL > prev.MarkerTTL {
			return nil, &ExplicitError{Msg: fmt.Sprintf("a ttl='1h' cache_control block must not come after a ttl='5m' cache_control block (%s follows %s)", cur.Label, prev.Label)}
		}
	}

	chain := e.buildChain(req)
	total := 0
	for _, b := range req.Blocks {
		total += b.Tokens
	}
	pl := &ExplicitPlan{Model: req.Model, Total: total, Markers: len(markers), start: start}
	minTokens := e.minPrefix(req.Model)

	e.mu.Lock()
	defer e.mu.Unlock()

	type found struct {
		idx int
		key string
	}
	var hits []found
	var valid []int // markers that can produce an entry
	for _, m := range markers {
		if chain[m].cum < minTokens {
			pl.Skipped = append(pl.Skipped, req.Blocks[m].Label)
			continue
		}
		valid = append(valid, m)
		lo := chain[m].pos - e.cfg.Lookback + 1
		for j := m; j >= 0 && chain[j].pos >= lo; j-- {
			en := e.entries[chain[j].hash]
			if en == nil {
				continue
			}
			if !start.Before(en.expires) {
				e.removeLocked(en)
				continue
			}
			hits = append(hits, found{j, chain[j].hash})
			pl.Hits = append(pl.Hits, ExplicitHit{Marker: req.Blocks[m].Label, Found: req.Blocks[j].Label, Tokens: chain[j].cum, Tier: req.Blocks[j].Tier})
			break
		}
	}

	hit := -1
	for _, h := range hits {
		if hit < 0 || chain[h.idx].cum > chain[hit].cum {
			hit = h.idx
		}
		// A read refreshes the entry at no charge, measured from this request's start.
		en := e.entries[h.key]
		e.tick++
		en.used = e.tick
		en.expires = later(en.expires, start.Add(en.ttl))
	}
	readIdx := hit
	if readIdx >= 0 {
		pl.Read = chain[readIdx].cum
	}

	// Writes: every valid breakpoint without an entry, parented to the closest
	// earlier entry so that leaf-first eviction knows what depends on what.
	anc := map[int]string{}
	for _, h := range hits {
		anc[h.idx] = h.key
	}
	for _, m := range valid {
		if e.entries[chain[m].hash] != nil {
			anc[m] = chain[m].hash
		}
	}
	for _, m := range valid {
		key := chain[m].hash
		if e.entries[key] != nil {
			continue
		}
		parent, best := "", -1
		for idx, k := range anc {
			if idx < m && idx > best {
				parent, best = k, idx
			}
		}
		anc[m] = key
		pl.writes = append(pl.writes, explicitWrite{
			key: key, parent: parent, tokens: chain[m].cum, ttl: req.Blocks[m].MarkerTTL,
			tier: req.Blocks[m].Tier, label: req.Blocks[m].Label,
		})
	}

	// Billing: read up to A (the highest hit); write from A to C (the last
	// breakpoint), the part up to B (the highest 1h breakpoint past A) at the 1h
	// price and the rest at the 5m price.
	if len(valid) > 0 {
		c := chain[valid[len(valid)-1]].cum
		a := pl.Read
		if c > a {
			b := a
			for _, m := range valid {
				if req.Blocks[m].MarkerTTL >= e.cfg.TTL1h && chain[m].cum > b {
					b = chain[m].cum
				}
			}
			pl.Write1h, pl.Write5m = b-a, c-b
		}
	}
	pl.Uncached = total - pl.Read - pl.Write1h - pl.Write5m

	// Attribute reads and writes to tiers by walking the blocks.
	readEnd, writeEnd := pl.Read, pl.Read+pl.Write1h+pl.Write5m
	cum := 0
	for _, b := range req.Blocks {
		lo, hi := cum, cum+b.Tokens
		cum = hi
		pl.ReadTiers.add(b.Tier, overlap(lo, hi, 0, readEnd))
		pl.WriteTiers.add(b.Tier, overlap(lo, hi, readEnd, writeEnd))
	}
	return pl, nil
}

func overlap(lo, hi, a, b int) int {
	s, t := max(lo, a), min(hi, b)
	if t > s {
		return t - s
	}
	return 0
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// buildChain computes the cumulative prefix hash, token count and lookback
// position of every block. Parameters enter the chain at the tier they
// invalidate, which is what makes a tool_choice change miss the messages tier
// while tools and system still hit.
func (e *ExplicitEngine) buildChain(req ExplicitRequest) []chainBlock {
	ahead := ""
	if e.cfg.ParamsAheadOfTools != nil && e.cfg.ParamsAheadOfTools(req.Model) {
		ahead = req.Params.Thinking + "|" + req.Params.Effort
	}
	h := chainHash("", "model", req.Model, ahead)

	images := false
	for _, b := range req.Blocks {
		images = images || b.HasImage || b.Kind == "image"
	}
	msgSeed := req.Params.ToolChoice + "|" + req.Params.Thinking + "|" + req.Params.Effort
	if images {
		msgSeed += "|images"
	}

	out := make([]chainBlock, len(req.Blocks))
	cum, pos := 0, 0
	lastKind := ""
	lastTier := Tier(-1)
	for i, b := range req.Blocks {
		if b.Tier != lastTier {
			switch b.Tier {
			case TierSystem:
				h = chainHash(h, "system", req.Params.SystemToggles)
			case TierMessages:
				h = chainHash(h, "messages", msgSeed)
			}
			lastTier = b.Tier
		}
		h = chainHash(h, b.Hash)
		cum += b.Tokens
		collapse := !e.cfg.NoCollapseRuns && (b.Kind == "tool_use" || b.Kind == "tool_result") && b.Kind == lastKind && i > 0 && req.Blocks[i-1].Tier == b.Tier
		if !collapse {
			pos++
		}
		lastKind = b.Kind
		out[i] = chainBlock{hash: h, cum: cum, pos: pos}
	}
	return out
}

// Publish writes the entries a plan reserved. Call it at the first response
// byte. An entry another request published in the meantime is only refreshed.
func (e *ExplicitEngine) Publish(pl *ExplicitPlan) {
	if pl == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, w := range pl.writes {
		e.tick++
		if en := e.entries[w.key]; en != nil {
			en.used = e.tick
			en.expires = later(en.expires, pl.start.Add(w.ttl))
			continue
		}
		own := w.tokens
		if p := e.entries[w.parent]; p != nil {
			own = max(w.tokens-p.tokens, 0)
			p.children++
		} else {
			w.parent = ""
		}
		e.entries[w.key] = &explicitEntry{
			key: w.key, tokens: w.tokens, own: own, parent: w.parent, ttl: w.ttl,
			expires: pl.start.Add(w.ttl), used: e.tick, tier: w.tier, label: w.label,
		}
		e.tokens += own
	}
	e.evictLocked()
}

func (e *ExplicitEngine) removeLocked(en *explicitEntry) {
	delete(e.entries, en.key)
	e.tokens -= en.own
	if p := e.entries[en.parent]; p != nil && p.children > 0 {
		p.children--
	}
}

// evictLocked frees memory until the cache fits: expired entries first, then the
// least recently used leaf. A leaf goes before any root because a root's KV is
// still referenced by every descendant; only when no leaf is left (a cycle that
// cannot occur) does it fall back to the oldest entry.
func (e *ExplicitEngine) evictLocked() {
	over := func() bool {
		return (e.cfg.CapacityTokens > 0 && e.tokens > e.cfg.CapacityTokens) ||
			(e.cfg.CapacityEntries > 0 && len(e.entries) > e.cfg.CapacityEntries)
	}
	if !over() {
		return
	}
	now := e.cfg.Now()
	for _, en := range e.entries {
		if !now.Before(en.expires) {
			e.removeLocked(en)
		}
	}
	for over() && len(e.entries) > 0 {
		var victim *explicitEntry
		for _, en := range e.entries {
			if en.children == 0 && (victim == nil || en.used < victim.used) {
				victim = en
			}
		}
		if victim == nil {
			for _, en := range e.entries {
				if victim == nil || en.used < victim.used {
					victim = en
				}
			}
		}
		e.removeLocked(victim)
	}
}

// Resident reports the number of entries and the tokens they occupy.
func (e *ExplicitEngine) Resident() (entries, tokens int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.entries), e.tokens
}

// Entries lists the resident entries, oldest use first.
func (e *ExplicitEngine) Entries() []EntryInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := make([]*explicitEntry, 0, len(e.entries))
	for _, en := range e.entries {
		list = append(list, en)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].used < list[j].used })
	out := make([]EntryInfo, len(list))
	for i, en := range list {
		out[i] = EntryInfo{Tokens: en.tokens, Tier: en.tier, TTL: en.ttl, Expires: en.expires, Leaf: en.children == 0, Label: en.label}
	}
	return out
}

// Sweep removes entries that have expired at now.
func (e *ExplicitEngine) Sweep(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, en := range e.entries {
		if !now.Before(en.expires) {
			e.removeLocked(en)
		}
	}
}

// Clear drops every entry.
func (e *ExplicitEngine) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = map[string]*explicitEntry{}
	e.tokens = 0
}
