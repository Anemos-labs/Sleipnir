package kv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/reee344/sleipnir/internal/core"
)

// Guard detects silent cache invalidation.
//
// The costliest caching failure is one that never errors: a stray timestamp, a
// reordered key, an edit nobody declared, and every request quietly misses.
// Guard remembers the previous request of one agent as a chain of block
// hashes and, on the next request, measures how much of it survives as an
// exact prefix. A prefix that shrinks without a declared rebase (a commit or
// shared-layer epoch) is a harness bug; the guard names the first block that
// diverged and the layer it belongs to.
type Guard struct {
	prev     []core.Hash // per persistent block, wire order
	prevTok  []int       // cumulative estimated tokens per block
	prevSecs []string    // layer name per block ("tools","const","shared",...)
	epoch    uint64
}

// Check is the result of comparing consecutive requests.
type Check struct {
	// SharedBlocks is the length of the common exact prefix, in blocks.
	SharedBlocks int
	// SharedTokens estimates the size of that prefix: what the provider should
	// be able to serve from cache.
	SharedTokens int
	// Drift is true when the prefix shrank although nothing declared a rebase.
	Drift bool
	// Diverged names the first differing block's layer when Drift is set.
	Diverged string
}

// Observe records prompt p as the agent's latest request and reports how it
// relates to the previous one. epoch is the agent's rebase counter (thread
// epoch plus shared-layer epoch); a changed epoch legitimizes a shorter prefix.
func (g *Guard) Observe(r *Rendered, epoch uint64, est core.Estimator) Check {
	hashes, toks, secs := digest(r, est)
	var c Check
	if g.prev != nil {
		n := 0
		for n < len(hashes) && n < len(g.prev) && hashes[n] == g.prev[n] {
			n++
		}
		c.SharedBlocks = n
		if n > 0 {
			c.SharedTokens = toks[n-1]
		}
		// Growth (new blocks appended) leaves n == len(prev). Anything less means
		// a previously sent block changed or vanished.
		if n < len(g.prev) && epoch == g.epoch {
			c.Drift = true
			if n < len(secs) {
				c.Diverged = secs[n]
			} else if n < len(g.prevSecs) {
				c.Diverged = g.prevSecs[n]
			}
		}
	}
	g.prev, g.prevTok, g.prevSecs, g.epoch = hashes, toks, secs, epoch
	return c
}

// digest hashes every persistent block of the prompt in wire order and labels
// each with the layer it came from.
func digest(r *Rendered, est core.Estimator) (hashes []core.Hash, cum []int, secs []string) {
	p := r.Prompt
	total := 0
	add := func(sec string, raw []byte, tokens int) {
		sum := sha256.Sum256(raw)
		hashes = append(hashes, core.Hash(hex.EncodeToString(sum[:8])))
		total += tokens
		cum = append(cum, total)
		secs = append(secs, sec)
	}
	for i := range p.Tools {
		t := p.Tools[i]
		raw, _ := json.Marshal(t)
		add("tools", raw, est.Tokens(t.Name)+est.Tokens(t.Description)+est.Tokens(string(t.InputSchema))+8)
	}
	for i := range p.System {
		add("const", []byte(p.System[i].Text), est.Tokens(p.System[i].Text))
	}
	names := map[int]string{}
	// Message 0 carries the pinned layers in order; label its blocks by section.
	for i, s := range r.Sections {
		names[i] = s.Name
	}
	for mi, m := range p.Messages {
		for bi, b := range m.Blocks {
			if b.Ephemeral {
				continue
			}
			label := "thread"
			if mi == 0 && bi < len(r.Sections) {
				label = names[bi]
			}
			raw, _ := json.Marshal(b)
			add(label, raw, BlockTokens(b, est))
		}
	}
	return hashes, cum, secs
}
