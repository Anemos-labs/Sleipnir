package kv

import (
	"sort"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

// RunKind classifies a block for Anthropic's position counting: a run of
// consecutive tool_use blocks (or of consecutive tool_result blocks) is one
// position, everything else is one position per block.
type RunKind uint8

const (
	RunNone RunKind = iota
	RunToolUse
	RunToolResult
)

// PlanBlock is one persistent block of a prompt, in wire order, reduced to what
// marker placement needs. Render builds them from a prompt; the simulator builds
// them from its segments, so both apply exactly the same policy.
type PlanBlock struct {
	Tokens int
	Run    RunKind
	// End names the layer that ends with this block ("const", "shared", "role",
	// "notes"), or is empty.
	End string
	// LayerTokens is the size of the layer named by End.
	LayerTokens int
	// NoMark: a cache marker cannot sit here (tool definitions, thinking blocks,
	// empty text blocks, turn-scoped system messages).
	NoMark bool
}

// Mark is a planned cache marker: after PlanBlock Block.
type Mark struct {
	Block int
	Label string
	TTL   time.Duration
}

// PlanMarks chooses where to ask an explicit-cache provider to cache.
//
// Anthropic writes an entry at each marker (billed from the highest earlier hit
// to the marker), reads the longest entry it can find at or up to LookbackBlocks
// positions before a marker, and allows at most MaxBreakpoints markers. The
// planner therefore spends the slots, in priority order, where a later hit is
// worth the most:
//
//  1. thread  - the rolling marker on the newest markable block: every request
//     of every agent reads through it;
//  2. anchor  - intermediate markers, only when the blocks added since the
//     previous rolling marker (prev) span more than the lookback window. Without
//     them a burst of new blocks pushes the previous entry out of reach and the
//     whole thread is re-written at the write premium;
//  3. shared  - end of the shared pin, or of the constitution when there is none:
//     one entry serves the whole swarm. It only needs the cumulative prefix to
//     reach the provider minimum, however small the layer itself is;
//  4. role    - end of the role pin, when the pin itself reaches the provider
//     minimum (a smaller pin would spend a slot to save a few hundred tokens);
//  5. notes   - end of the agent's notes, when they reach MinLayerForBreakpoint:
//     a commit that only touches the spine and thread then re-writes nothing
//     above it;
//  6. const   - end of the constitution and tools, when a shared pin follows
//     and a slot is still free: it survives a shared-pin epoch.
//
// A marker is skipped when the prefix before it is under the provider minimum
// (it could not produce an entry anyway). prev is the index of the block that
// carried the previous request's rolling marker in this prompt, or -1.
func PlanMarks(blocks []PlanBlock, prev int, caps Caps, pol Policy) []Mark {
	max := caps.MaxBreakpoints
	if max <= 0 || len(blocks) == 0 {
		return nil
	}
	cum := make([]int, len(blocks))
	pos := make([]int, len(blocks))
	total, p := 0, 0
	last := RunNone
	for i, b := range blocks {
		total += b.Tokens
		cum[i] = total
		if !(b.Run != RunNone && b.Run == last) {
			p++
		}
		last = b.Run
		pos[i] = p
	}

	roll := -1
	for i := len(blocks) - 1; i >= 0; i-- {
		if !blocks[i].NoMark {
			roll = i
			break
		}
	}
	if roll < 0 || cum[roll] < caps.MinPrefixTokens {
		return nil // the whole prompt is too short to cache
	}

	var chosen []Mark
	used := map[int]bool{}
	add := func(idx int, label string, ttl time.Duration) {
		if idx < 0 || used[idx] || len(chosen) >= max {
			return
		}
		used[idx] = true
		chosen = append(chosen, Mark{Block: idx, Label: label, TTL: ttl})
	}
	add(roll, "thread", 0)

	// Anchors: every hop from the previous rolling entry to the new one must fit
	// in the lookback window, with one position to spare.
	if look := caps.LookbackBlocks; look > 2 && prev >= 0 && prev < roll {
		hop := look - 2
		at := prev
		for pos[roll]-pos[at] > look-1 {
			cand := -1
			for i := at + 1; i < roll; i++ {
				if pos[i]-pos[at] > hop {
					break
				}
				if !blocks[i].NoMark {
					cand = i
				}
			}
			if cand < 0 || len(chosen) >= max {
				break
			}
			add(cand, "anchor", 0)
			at = cand
		}
	}

	end := map[string]int{}
	size := map[string]int{}
	for i, b := range blocks {
		if b.End != "" {
			end[b.End] = i
			size[b.End] = b.LayerTokens
		}
	}
	eligible := func(label string) (int, bool) {
		i, ok := end[label]
		if !ok || blocks[i].NoMark || cum[i] < caps.MinPrefixTokens {
			return 0, false
		}
		return i, true
	}
	if i, ok := eligible("shared"); ok {
		add(i, "shared", pol.SharedTTL)
	} else if i, ok := eligible("const"); ok {
		add(i, "const", pol.SharedTTL)
	}
	if i, ok := eligible("role"); ok && size["role"] >= caps.MinPrefixTokens {
		add(i, "role", pol.SharedTTL)
	}
	if i, ok := eligible("notes"); ok && size["notes"] >= pol.MinLayerForBreakpoint {
		add(i, "notes", 0)
	}
	if _, has := end["shared"]; has {
		if i, ok := eligible("const"); ok {
			add(i, "const", pol.SharedTTL)
		}
	}
	// Providers require ascending positions and TTLs that do not increase along
	// the prompt; layer markers precede the thread and carry the longer TTL.
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].Block < chosen[j].Block })
	return chosen
}

// markable reports whether a marker may sit on block b of a message.
func markable(m *core.Message, b *core.Block) bool {
	if m != nil && m.ClearAt != "" {
		return false // turn-scoped system messages take no cache_control
	}
	switch b.Kind {
	case core.BlockThinking, core.BlockRedactedThinking:
		return false
	case core.BlockText:
		return b.Text != ""
	}
	return true
}

// planBreakpoints builds the plan blocks of a rendered prompt and turns the
// chosen marks back into block references.
func planBreakpoints(p *core.Prompt, refs map[string]core.BlockRef, sections []Section, o RenderOpts) []core.Breakpoint {
	if o.Caps.MaxBreakpoints <= 0 {
		return nil
	}
	est := o.Est
	layerTokens := map[string]int{}
	for _, s := range sections {
		layerTokens[s.Name] = s.Tokens
	}
	endOf := map[core.BlockRef]string{}
	for _, name := range []string{"shared", "role", "notes"} {
		if r, ok := refs[name]; ok {
			endOf[r] = name
		}
	}

	var blocks []PlanBlock
	var at []core.BlockRef
	prev := -1
	sysLast := core.BlockRef{}
	if n := len(p.System); n > 0 {
		sysLast = core.BlockRef{Sys: true, Msg: 0, Blk: n - 1}
	}
	constTokens := 0
	p.WalkBlocks(func(ref core.BlockRef, tool *core.ToolSpec, b *core.Block) {
		var pb PlanBlock
		switch {
		case tool != nil:
			pb = PlanBlock{Tokens: est.Tokens(tool.Name) + est.Tokens(tool.Description) + est.Tokens(string(tool.InputSchema)) + 8, NoMark: true}
			constTokens += pb.Tokens
		case b != nil && ref.Sys:
			pb = PlanBlock{Tokens: BlockTokens(*b, est), NoMark: !markable(nil, b)}
			constTokens += pb.Tokens
			if ref == sysLast {
				pb.End, pb.LayerTokens = "const", constTokens
			}
		case b != nil:
			if b.Ephemeral {
				return
			}
			m := &p.Messages[ref.Msg]
			pb = PlanBlock{Tokens: SentBlockTokens(*b, est), NoMark: !markable(m, b)}
			switch b.Kind {
			case core.BlockToolUse:
				pb.Run = RunToolUse
			case core.BlockToolResult:
				pb.Run = RunToolResult
			}
			if name, ok := endOf[ref]; ok {
				pb.End, pb.LayerTokens = name, layerTokens[name]
			}
		}
		if o.PrevRolling != nil && ref == *o.PrevRolling {
			prev = len(blocks)
		}
		blocks = append(blocks, pb)
		at = append(at, ref)
	})

	marks := PlanMarks(blocks, prev, o.Caps, o.Policy)
	out := make([]core.Breakpoint, 0, len(marks))
	for _, m := range marks {
		out = append(out, core.Breakpoint{After: at[m.Block], Label: m.Label, TTL: m.TTL})
	}
	return out
}
