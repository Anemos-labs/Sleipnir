package kv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/anemos-labs/sleipnir/internal/core"
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
//
// The chain covers everything a provider keys its cache on, not only block
// text: the model and the request parameters that sit in front of the messages
// tier (thinking, effort, tool_choice), and each block's message role and
// position in its message, so re-splitting the same blocks into other messages
// is a change too. Ephemeral blocks (the inline hot tail) are skipped: they sit
// after the last marker and are not cached.
type Guard struct {
	prev         []core.Hash // per persistent block, wire order
	prevTok      []int       // cumulative estimated tokens per block
	prevSecs     []string    // layer name per block ("params","tools","const","shared",...)
	prevMarks    []int       // chain index of each cache marker of the previous request
	prevAuto     bool        // the previous request had no markers because the provider caches automatically
	prevBoundary bool        // automatic entries end at complete eligible messages
	epoch        uint64
}

// Check is the result of comparing consecutive requests.
type Check struct {
	// SharedBlocks is the length of the common exact prefix, in blocks.
	SharedBlocks int
	// SharedTokens estimates the size of that prefix.
	SharedTokens int
	// ReadableTokens estimates a reusable prefix under the route's cache model:
	// arbitrary prefixes, explicit markers, or complete message endings. This
	// does not verify server entries, routing, retention, or API serialization.
	ReadableTokens int
	// TotalTokens estimates the size of the whole prompt (persistent blocks).
	TotalTokens int
	// Drift is true when the prefix shrank although nothing declared a rebase.
	Drift bool
	// Diverged names the first differing block's layer when Drift is set.
	Diverged string
}

// Observe records prompt p as the agent's latest request and reports how it
// relates to the previous one. epoch is the agent's rebase counter (thread
// epoch plus shared-layer epoch); a changed epoch legitimizes a shorter prefix.
func (g *Guard) Observe(r *Rendered, epoch uint64, est core.Estimator) Check {
	hashes, toks, secs, marks, endings := digest(r, est)
	var c Check
	if len(toks) > 0 {
		c.TotalTokens = toks[len(toks)-1]
	}
	if g.prev != nil {
		n := 0
		for n < len(hashes) && n < len(g.prev) && hashes[n] == g.prev[n] {
			n++
		}
		c.SharedBlocks = n
		if n > 0 {
			c.SharedTokens = toks[n-1]
		}
		if g.prevAuto && !g.prevBoundary {
			c.ReadableTokens = c.SharedTokens
		} else {
			for _, m := range g.prevMarks {
				// Extending a message preserves its text prefix but loses its
				// previous ending. That ending cannot find the old entry.
				if g.prevBoundary && !slices.Contains(endings, m) {
					continue
				}
				if m < n && toks[m] > c.ReadableTokens {
					c.ReadableTokens = toks[m]
				}
			}
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
	g.prevMarks, g.prevAuto = marks, r.Caps.MaxBreakpoints == 0
	g.prevBoundary = g.prevAuto && r.Caps.MessageBoundaries
	if g.prevBoundary && len(endings) > 0 {
		// Implicit mode writes the latest eligible ending, not every stable
		// layer. Older server entries are unknown to this two-request guard.
		g.prevMarks = endings[len(endings)-1:]
	}
	return c
}

// digest hashes every persistent block of the prompt in wire order and labels
// each with the layer it came from.
func digest(r *Rendered, est core.Estimator) (hashes []core.Hash, cum []int, secs []string, marks, endings []int) {
	p := r.Prompt
	total := 0
	at := map[core.BlockRef]int{}
	add := func(sec string, raw []byte, tokens int) {
		sum := sha256.Sum256(raw)
		hashes = append(hashes, core.Hash(hex.EncodeToString(sum[:8])))
		total += tokens
		cum = append(cum, total)
		secs = append(secs, sec)
	}
	// Model and the parameters that key the messages tier come first, so a change
	// names "params" and invalidates everything behind it, as it does at the provider.
	add("params", []byte("model|"+p.Model+"|thinking|"+p.Params.Thinking+"|effort|"+p.Params.Effort+"|tool_choice|"+p.Params.ToolChoice), 0)
	for i := range p.Tools {
		t := p.Tools[i]
		raw, _ := json.Marshal(t)
		add("tools", raw, est.Tokens(t.Name)+est.Tokens(t.Description)+est.Tokens(string(t.InputSchema))+8)
		at[core.BlockRef{Sys: true, Msg: -1, Blk: i}] = len(hashes) - 1
	}
	for i := range p.System {
		add("const", []byte(p.System[i].Text), est.Tokens(p.System[i].Text))
		at[core.BlockRef{Sys: true, Msg: 0, Blk: i}] = len(hashes) - 1
	}
	names := map[int]string{}
	// Message 0 carries the pinned layers in order; label its blocks by section.
	for i, s := range r.Sections {
		names[i] = s.Name
	}
	for mi, m := range p.Messages {
		start, ephemeral := len(hashes), false
		for bi, b := range m.Blocks {
			if b.Ephemeral {
				ephemeral = true
				continue
			}
			label := "thread"
			if mi == 0 && bi < len(r.Sections) {
				label = names[bi]
			}
			raw, _ := json.Marshal(b)
			// Role, position in the message and turn-scoped marking are part of
			// what the provider hashed.
			head := []byte(string(m.Role) + "|" + m.ClearAt + "|")
			if bi == 0 {
				head = append(head, '^')
			}
			add(label, append(head, raw...), SentBlockTokens(b, est))
			at[core.BlockRef{Msg: mi, Blk: bi}] = len(hashes) - 1
		}
		if m.Role == core.RoleUser && len(hashes) > start && !ephemeral {
			endings = append(endings, len(hashes)-1)
		}
	}
	for _, b := range p.Breakpoints {
		if i, ok := at[b.After]; ok {
			marks = append(marks, i)
		}
	}
	return hashes, cum, secs, marks, endings
}
