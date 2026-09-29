package kv

import (
	"github.com/reee344/sleipnir/internal/core"
)

// planBreakpoints chooses where to ask the provider to cache.
//
// With a hard limit on markers (Anthropic allows four) the layers compete for
// them. Priority reflects where a hit is worth the most:
//
//  1. thread  - the rolling marker: every turn of every agent reads through it
//  2. shared  - one entry serves the whole swarm
//  3. role    - one entry serves every agent of a role
//  4. notes   - shields an agent's tenured notes from thread-only rewrites
//
// A marker is skipped when it could not produce a cache entry anyway (prefix
// below the provider minimum) or when the layer it would close is too small to
// be worth a separate entry.
func planBreakpoints(p *core.Prompt, refs map[string]core.BlockRef, sections []Section, o RenderOpts) []core.Breakpoint {
	if o.Caps.MaxBreakpoints <= 0 {
		return nil
	}
	est := o.Est

	// Prefix size at each candidate, walking in wire order.
	sizeAt := map[core.BlockRef]int{}
	total := 0
	p.WalkBlocks(func(ref core.BlockRef, tool *core.ToolSpec, b *core.Block) {
		switch {
		case tool != nil:
			total += est.Tokens(tool.Name) + est.Tokens(tool.Description) + est.Tokens(string(tool.InputSchema)) + 8
		case b != nil && !b.Ephemeral:
			total += BlockTokens(*b, est)
		}
		sizeAt[ref] = total
	})

	layerTokens := map[string]int{}
	for _, s := range sections {
		layerTokens[s.Name] = s.Tokens
	}

	order := []string{"thread", "shared", "role", "notes"}
	var chosen []core.Breakpoint
	for _, label := range order {
		ref, ok := refs[label]
		if !ok {
			continue
		}
		if sizeAt[ref] < o.Caps.MinPrefixTokens {
			continue
		}
		if label != "thread" && layerTokens[label] < o.Policy.MinLayerForBreakpoint {
			continue
		}
		bp := core.Breakpoint{After: ref, Label: label}
		if label == "shared" || label == "role" {
			bp.TTL = o.Policy.SharedTTL
		}
		chosen = append(chosen, bp)
		if len(chosen) == o.Caps.MaxBreakpoints {
			break
		}
	}
	// Emit in wire order; providers require ascending positions and TTLs
	// that do not increase along the prompt.
	sortBreakpoints(chosen)
	return chosen
}

func sortBreakpoints(b []core.Breakpoint) {
	less := func(x, y core.BlockRef) bool {
		if x.Sys != y.Sys {
			return x.Sys
		}
		if x.Msg != y.Msg {
			return x.Msg < y.Msg
		}
		return x.Blk < y.Blk
	}
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && less(b[j].After, b[j-1].After); j-- {
			b[j], b[j-1] = b[j-1], b[j]
		}
	}
}
