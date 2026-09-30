package inspect

import (
	"fmt"
	"strings"
	"time"
)

// layerMeaning says what a change in each layer means for the design.
var layerMeaning = map[string]string{
	"tools":  "The tool list changed. Every agent, in every role, must send the same tools array byte for byte; a per-role, reordered or re-serialised list forks the prefix at byte 0 for everyone.",
	"const":  "The constitution (system prompt) changed. It is frozen for the session, so something is interpolating per-request or per-agent text into it.",
	"shared": "The shared pin changed outside a declared epoch. Pins change only when the swarm installs a new shared layer (SyncShared), which every agent then reports as a shared-sync.",
	"role":   "The role pin changed outside a declared epoch.",
	"notes":  "The agent's notes changed outside a major compaction commit.",
	"spine":  "The spine changed outside a compaction commit. Spine lines are append-only and only commits add them.",
	"thread": "An earlier turn of the thread was rewritten or removed outside a commit: thinking blocks stripped, a tool result masked, or two turns merged.",
}

// explainLocked adds the evidence the log holds for an anomaly and writes the
// explanation. It only states facts it can support: where the log cannot say
// (a provider-side eviction, say) it lists what to rule out.
func (s *Session) explainLocked(an *anomaly) Anomaly {
	out := an.Anomaly
	out.TMs = max(an.T.Sub(s.meta.first).Milliseconds(), 0)
	out.Explain = []string{}
	var r, prev *req
	if an.Req != "" {
		r = s.byID[an.Req]
	}
	if r != nil {
		prev = s.prevMainLocked(r)
		out.Rebase = r.rebase
		out.Changed = changedNames(r.changed)
		if prev != nil {
			out.GapMs = r.t.Sub(prev.t).Milliseconds()
			out.KeyChanged = r.cacheKey != prev.cacheKey
		}
		out.TTLMs = s.priceFor(firstNonEmpty(r.model, r.agent.model, s.meta.model)).ttl.Milliseconds()
		if out.Expected == 0 {
			out.Expected = r.expected
		}
		if out.Actual == 0 && r.done {
			out.Actual = r.usage.CacheReadTokens
		}
	}

	switch out.Kind {
	case "drift":
		layer := firstNonEmpty(out.Diverged, "the prompt")
		out.Title = "Prefix drift in " + layer
		out.Explain = append(out.Explain,
			fmt.Sprintf("The exact prefix this agent shares with its previous request shrank to %d block(s) although no compaction commit or shared-layer sync was declared between the two requests. The first block that differs belongs to %s.", out.SharedBlocks, layer))
		if m, ok := layerMeaning[out.Diverged]; ok {
			out.Explain = append(out.Explain, m)
		}
		out.Explain = append(out.Explain, "Expect the provider to miss from that layer onward on this request and to write it again, at the write premium where there is one.")
		if r != nil && r.done {
			out.Explain = append(out.Explain, fmt.Sprintf("Observed: %.0f%% of the prompt read from cache (%s of %s tokens).", r.hit*100, fmtK(r.usage.CacheReadTokens), fmtK(r.prompt)))
		}
	case "low_hit":
		out.Title = fmt.Sprintf("Cache read %s of an expected %s tokens", fmtK(out.Actual), fmtK(out.Expected))
		out.Explain = append(out.Explain,
			fmt.Sprintf("The guard saw the prefix unchanged, so the provider should have served about %s tokens from cache, but it reported reading %s (below 70%% of the expectation).", fmtK(out.Expected), fmtK(out.Actual)))
		out.Causes = s.lowHitCauses(out, r, prev)
	case "undeclared":
		layer := firstNonEmpty(out.Diverged, "a layer")
		out.Title = "Layer " + layer + " changed without a declared rebase"
		out.Explain = append(out.Explain,
			fmt.Sprintf("Comparing consecutive requests of %s, the bytes of %s differ from the previous request, but no compaction commit or shared-layer sync was logged in between. The guard did not flag it.", out.Agent, strings.Join(out.Changed, ", ")))
		if m, ok := layerMeaning[out.Diverged]; ok {
			out.Explain = append(out.Explain, m)
		}
	default:
		out.Title = "Cache anomaly (" + firstNonEmpty(out.Kind, "unknown") + ")"
	}
	if r != nil && len(out.Changed) > 0 && out.Kind != "undeclared" {
		what := "no declared rebase"
		if out.Rebase != "" {
			what = "a declared " + out.Rebase
		}
		out.Explain = append(out.Explain, fmt.Sprintf("Layers whose bytes changed since the previous request: %s (%s).", strings.Join(out.Changed, ", "), what))
	}
	return out
}

// lowHitCauses lists likely causes, most likely first, from what the log shows.
func (s *Session) lowHitCauses(a Anomaly, r, prev *req) []string {
	var c []string
	if a.GapMs > 0 && a.TTLMs > 0 && a.GapMs > a.TTLMs {
		c = append(c, fmt.Sprintf("The agent was idle for %s before this request, longer than the cache lifetime (%s). The entry has very likely expired; this is a cold restart, not a harness fault.",
			fmtDur(time.Duration(a.GapMs)*time.Millisecond), fmtDur(time.Duration(a.TTLMs)*time.Millisecond)))
	}
	if a.KeyChanged && r != nil && prev != nil {
		c = append(c, fmt.Sprintf("The routing key changed since the previous request (%s to %s). On engines that cache per replica the request may have been served by one that never saw this prefix.", shortHash(prev.cacheKey), shortHash(r.cacheKey)))
	}
	if r != nil && r.rebase != "" {
		c = append(c, fmt.Sprintf("A declared %s preceded this request, so a partial rewrite was expected; the guard's estimate of the surviving prefix was optimistic (layer sizes are estimates).", r.rebase))
	} else if len(a.Changed) > 0 {
		c = append(c, "Layers "+strings.Join(a.Changed, ", ")+" changed since the previous request with no declared rebase; see the layer view for the first differing byte.")
	}
	if r != nil && prev != nil && r.model != prev.model && r.model != "" && prev.model != "" {
		c = append(c, fmt.Sprintf("The model differs from the previous request (%s to %s); caches are per model.", prev.model, r.model))
	}
	if r != nil && a.Actual == 0 && !s.cache.anyRead {
		c = append(c, "No request in this log reported a cache read: the endpoint may not report cache usage, or caching is disabled.")
	}
	if len(c) == 0 {
		c = append(c,
			"No local cause found: the prefix was identical and the agent's cache should have been warm.",
			"Rule out provider-side eviction under memory pressure, a concurrent request racing the write (an entry becomes readable only after the first response byte), and a routing change between replicas.")
	}
	return c
}
