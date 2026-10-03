package kv

import (
	"fmt"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

// Pressure thresholds and horizons for the compaction planner. Zero values fall
// back to the defaults in DefaultPlanner.
type Planner struct {
	// SoftThreadTokens: start compacting once the verbatim thread exceeds this
	// (and the economics allow it).
	SoftThreadTokens int
	// HardThreadTokens: commit as soon as a patch is ready, whatever it costs.
	HardThreadTokens int
	// ContextFraction: also treat the prompt as under pressure above this share
	// of the model's window.
	ContextFraction float64
	// MinThreadTokens: below this, compaction never pays even when cold.
	MinThreadTokens int
	// HorizonTurns is the default estimate of remaining requests for an agent.
	HorizonTurns float64
	// ColdMargin: a request this close to (or past) the cache lifetime is treated
	// as cold.
	ColdMargin time.Duration

	// MaskMinTokens is the least a mask-only commit must save to be worth
	// rewriting the thread; MaskMinInterval the fewest requests between two of
	// them. Together they keep the deterministic path from turning into a commit
	// storm.
	MaskMinTokens   int
	MaskMinInterval int
	// ForkInstrTokens / ForkOutputTokens size the compactor call (its
	// instruction and its JSON answer) for the start decision.
	ForkInstrTokens  int
	ForkOutputTokens int
	// EstRetainedFrac / EstSpineFrac are what a typical model patch achieves
	// (share of the thread retained, share added to the spine); the start
	// decision prices a compaction with them before a patch exists.
	EstRetainedFrac float64
	EstSpineFrac    float64
	// A ready patch is discarded and re-proposed when it has waited more than
	// HoldMaxRequests requests, or the thread has grown by more than
	// StaleGrowthFrac of what it covered (at least StaleGrowthTokens).
	HoldMaxRequests   int
	StaleGrowthFrac   float64
	StaleGrowthTokens int
}

// DefaultPlanner returns conservative defaults tuned for agent loops whose
// verbatim thread should stay well under a few tens of thousands of tokens.
func DefaultPlanner() Planner {
	return Planner{
		SoftThreadTokens: 20_000,
		HardThreadTokens: 60_000,
		ContextFraction:  0.6,
		MinThreadTokens:  4_000,
		HorizonTurns:     25,
		ColdMargin:       20 * time.Second,

		MaskMinTokens:     2_000,
		MaskMinInterval:   6,
		ForkInstrTokens:   1_500,
		ForkOutputTokens:  1_000,
		EstRetainedFrac:   0.22,
		EstSpineFrac:      0.03,
		HoldMaxRequests:   8,
		StaleGrowthFrac:   0.25,
		StaleGrowthTokens: 3_000,
	}
}

func (p *Planner) fill() {
	d := DefaultPlanner()
	if p.SoftThreadTokens == 0 {
		p.SoftThreadTokens = d.SoftThreadTokens
	}
	if p.HardThreadTokens == 0 {
		p.HardThreadTokens = d.HardThreadTokens
	}
	if p.ContextFraction == 0 {
		p.ContextFraction = d.ContextFraction
	}
	if p.MinThreadTokens == 0 {
		p.MinThreadTokens = d.MinThreadTokens
	}
	if p.HorizonTurns == 0 {
		p.HorizonTurns = d.HorizonTurns
	}
	if p.ColdMargin == 0 {
		p.ColdMargin = d.ColdMargin
	}
	if p.MaskMinTokens == 0 {
		p.MaskMinTokens = d.MaskMinTokens
	}
	if p.MaskMinInterval == 0 {
		p.MaskMinInterval = d.MaskMinInterval
	}
	if p.ForkInstrTokens == 0 {
		p.ForkInstrTokens = d.ForkInstrTokens
	}
	if p.ForkOutputTokens == 0 {
		p.ForkOutputTokens = d.ForkOutputTokens
	}
	if p.EstRetainedFrac == 0 {
		p.EstRetainedFrac = d.EstRetainedFrac
	}
	if p.EstSpineFrac == 0 {
		p.EstSpineFrac = d.EstSpineFrac
	}
	if p.HoldMaxRequests == 0 {
		p.HoldMaxRequests = d.HoldMaxRequests
	}
	if p.StaleGrowthFrac == 0 {
		p.StaleGrowthFrac = d.StaleGrowthFrac
	}
	if p.StaleGrowthTokens == 0 {
		p.StaleGrowthTokens = d.StaleGrowthTokens
	}
}

// State is a snapshot of one agent's cache situation. Every token count is what
// the provider is sent (thinking that is not replayed does not count).
type State struct {
	// PrefixTokens: tools, constitution, shared and role pins. It survives every
	// commit intact.
	PrefixTokens int
	// NotesTokens and SpineTokens are the agent's private layers.
	NotesTokens int
	SpineTokens int
	// ThreadTokens (T): the LIVE verbatim thread today, tail included. Hard and
	// window pressure are judged on it, never on a stale snapshot.
	ThreadTokens int
	// PromptTokens: whole prompt size.
	PromptTokens int
	// ContextWindow of the model, in tokens.
	ContextWindow int
	// Warm reports whether the agent's cached prefix is expected to still be
	// resident for its next request.
	Warm bool
	// Remaining is the expected number of further requests (0: use HorizonTurns).
	Remaining float64
	// W are the provider's relative prices; Write is the write weight that
	// applies to a rebuilt prefix (5m or 1h premium, or 1.0 without one).
	W     cost.Weights
	Write float64
	// Explicit: the provider caches only at markers (Anthropic style). The spine is
	// one block, so appending to it re-writes all of it; on automatic providers the
	// old spine survives byte for byte and only the added lines are new.
	Explicit bool
	// MaskableTokens is what deterministic masking would save right now.
	MaskableTokens int
	// SinceMask is the number of requests since the last mask-only commit.
	SinceMask int
}

// Outcome is what a compaction would do, from a computed or ready patch.
type Outcome struct {
	// SnapTokens (T) is the thread the patch replaces, measured before masking.
	// Zero means State.ThreadTokens.
	SnapTokens int
	// SpineAdded (A) and RetainedTokens (R) are what the patch adds to the spine
	// and keeps of the snapshot thread. SpineAfter is the spine layer's size after
	// the patch (0: SpineTokens+SpineAdded); SpineRewritten says the front of the
	// spine changed (eviction), so even an append-only cache re-writes all of it.
	SpineAdded, RetainedTokens int
	SpineAfter                 int
	SpineRewritten             bool
	// NotesChanged: the notes layer is re-written too (everything after it is
	// then), NotesAfter is its new size (0: unchanged size).
	NotesChanged bool
	NotesAfter   int
	// TailTokens (D') is the turns appended since the snapshot that the provider
	// has already cached: they sit behind the changed prefix and are re-written.
	TailTokens int
	// CompactorITE is the input-token-equivalent cost of the model call that
	// produced (or would produce) the patch. It is sunk once a patch exists.
	CompactorITE float64
}

// Mode says how a compaction should be carried out.
type Mode int

const (
	// ModeFork asks a model (a fork of the agent's own request) for a patch. It is
	// the high-quality path and only worth its call while the cache is warm: the
	// fork reads the agent's cached prefix at the read rate.
	ModeFork Mode = iota
	// ModeMask applies deterministic masking of bulky old tool results right now,
	// with no model call. It is the right move when the cache is cold: the next
	// request re-prefills the whole prompt anyway, so shrinking it first is free,
	// whereas a fork would have to pay a cold write for the prefix itself. It is
	// only chosen when there is something to mask.
	ModeMask
)

// Decision explains a planner verdict.
type Decision struct {
	Yes    bool
	Mode   Mode
	NetITE float64 // expected saving in input-token equivalents (may be negative)
	Reason string
}

// write selects an explicit positive write multiplier, then the five-minute multiplier, and
// otherwise one.
func (s State) write() float64 {
	switch {
	case s.Write > 0:
		return s.Write
	case s.W.Write5m > 0:
		return s.W.Write5m
	}
	return 1
}

// priced reports whether the state carries prices; without them decisions rest
// on thresholds alone.
func (s State) priced() bool { return s.W.Read > 0 }

// pressure reports hard thread-size or context-window pressure, prioritizing the thread limit when
// both apply.
func (s State) pressure(p Planner) (string, bool) {
	switch {
	case s.ThreadTokens >= p.HardThreadTokens:
		return fmt.Sprintf("thread %dk over hard limit %dk", s.ThreadTokens/1000, p.HardThreadTokens/1000), true
	case s.ContextWindow > 0 && float64(s.PromptTokens) >= p.ContextFraction*float64(s.ContextWindow):
		return "prompt near context window", true
	}
	return "", false
}

// ShouldStart decides whether to spend a compaction at all, and how.
//
//   - Under hard or window pressure something must be folded. A cold agent with
//     enough to mask masks first (free, deterministic); otherwise a model fork
//     runs even though the cache is cold, because unbounded growth is worse.
//   - Cold, below the hard limit: mask when there is enough to mask and the
//     previous mask commit is not too recent. Never fork: a fork over a cold
//     prefix pays a full write just to read the thread.
//   - Warm, over the soft limit: fork, but only if the compaction is expected to
//     pay for its own compactor call within the remaining turns.
func (p Planner) ShouldStart(s State) Decision {
	p.fill()
	maskOK := s.MaskableTokens >= p.MaskMinTokens && s.SinceMask >= p.MaskMinInterval
	if why, forced := s.pressure(p); forced {
		if !s.Warm && maskOK {
			return Decision{Yes: true, Mode: ModeMask, Reason: why + " (cold: masking first)"}
		}
		return Decision{Yes: true, Mode: ModeFork, Reason: why}
	}
	if !s.Warm {
		if s.ThreadTokens >= p.MinThreadTokens && maskOK {
			return Decision{Yes: true, Mode: ModeMask, Reason: "cache is cold: shrink for free by masking, no model call"}
		}
		if s.ThreadTokens >= p.SoftThreadTokens {
			return Decision{Reason: "cache is cold and there is nothing worth masking: a fork would pay a cold prefix write; waiting for the hard limit or a warm moment"}
		}
		return Decision{Reason: "no pressure"}
	}
	if s.ThreadTokens < p.SoftThreadTokens {
		return Decision{Reason: "no pressure"}
	}
	if s.priced() {
		net := p.estimatedNet(s)
		if net <= 0 {
			return Decision{NetITE: net, Reason: fmt.Sprintf("thread %dk over soft limit but a compaction would not pay for its own call within the remaining turns (net %+.0f ITE)", s.ThreadTokens/1000, net)}
		}
		return Decision{Yes: true, NetITE: net, Reason: fmt.Sprintf("thread %dk over soft limit %dk (expected net %+.0f ITE)", s.ThreadTokens/1000, p.SoftThreadTokens/1000, net)}
	}
	return Decision{Yes: true, Reason: fmt.Sprintf("thread %dk over soft limit %dk", s.ThreadTokens/1000, p.SoftThreadTokens/1000)}
}

// estimatedNet prices a typical model compaction of the live thread before a
// patch exists: the saving over the remaining turns, less the commit's rewrite
// and the compactor call itself (a fork that reads the whole prompt).
func (p Planner) estimatedNet(s State) float64 {
	T := s.ThreadTokens
	o := Outcome{
		SnapTokens:     T,
		SpineAdded:     int(p.EstSpineFrac * float64(T)),
		RetainedTokens: int(p.EstRetainedFrac * float64(T)),
	}
	penalty, shrink := commitEconomics(s, o)
	n := s.Remaining
	if n == 0 {
		n = p.HorizonTurns
	}
	out := s.W.Output
	if out == 0 {
		out = 5
	}
	fork := s.W.Read*float64(s.PrefixTokens+s.NotesTokens+s.SpineTokens+T) + float64(p.ForkInstrTokens) + out*float64(p.ForkOutputTokens)
	return n*s.W.Read*shrink - penalty - fork
}

// commitEconomics prices a commit in input-token equivalents.
//
// The commit changes the prompt from the first affected block onward, and the
// provider re-writes everything after that point at the write weight w instead
// of reading the (soon obsolete) old bytes at the read weight r. What is
// affected depends on the cache:
//
//   - always: the retained thread R and the tail D' appended since the snapshot;
//     the old thread T and the same tail would have been read.
//   - explicit-breakpoint caches (Anthropic): the spine is one block, so the
//     whole spine is re-written, not only the added lines; the old spine would
//     have been read.
//   - append-only caches (OpenAI-style): the old spine survives byte for byte,
//     so only the added lines A are new (unless eviction changed its front).
//   - any cache, when the notes change: the notes and everything after them,
//     the whole spine included.
//
// The extra cost of the first request after the commit is the penalty; every
// later request then reads shrink fewer tokens.
func commitEconomics(s State, o Outcome) (penalty, shrink float64) {
	T := o.SnapTokens
	if T == 0 {
		T = s.ThreadTokens
	}
	A, R, D := o.SpineAdded, o.RetainedTokens, o.TailTokens
	spineAfter := o.SpineAfter
	if spineAfter == 0 {
		spineAfter = s.SpineTokens + A
	}
	notesAfter := s.NotesTokens
	if o.NotesAfter > 0 {
		notesAfter = o.NotesAfter
	}
	rewrite := float64(R + D)
	read := float64(T + D)
	switch {
	case o.NotesChanged:
		rewrite += float64(notesAfter + spineAfter)
		read += float64(s.NotesTokens + s.SpineTokens)
	case s.Explicit || o.SpineRewritten:
		rewrite += float64(spineAfter)
		read += float64(s.SpineTokens)
	default:
		rewrite += float64(A)
	}
	penalty = s.write()*rewrite - s.W.Read*read
	shrink = float64(T + s.SpineTokens - spineAfter - R)
	if o.NotesChanged {
		shrink += float64(s.NotesTokens - notesAfter)
	}
	return penalty, shrink
}

// ShouldCommit decides whether to apply a ready patch now or hold it for a
// cheaper moment. s describes the agent NOW (live thread, tail included); o the
// patch as computed against its snapshot.
//
//	warm:  the next request costs `penalty` more than without the commit
//	       (commitEconomics); every later request saves r*shrink.
//	cold:  the next request rewrites everything anyway, so the commit only
//	       shrinks that write: saving w*shrink, never negative.
//
// Hard and window pressure on the live thread override economics. The compactor
// call itself is sunk once a patch exists, so it is not counted here (ShouldStart
// counts it).
func (p Planner) ShouldCommit(s State, o Outcome) Decision {
	p.fill()
	penalty, shrink := commitEconomics(s, o)
	if shrink <= 0 {
		return Decision{Reason: "patch does not shrink the prompt"}
	}
	if why, forced := s.pressure(p); forced {
		return Decision{Yes: true, Reason: why + ": commit regardless of cost"}
	}
	if !s.Warm {
		return Decision{Yes: true, NetITE: s.write() * shrink, Reason: "cache is cold: commit is free"}
	}
	n := s.Remaining
	if n == 0 {
		n = p.HorizonTurns
	}
	saving := n * s.W.Read * shrink
	net := saving - penalty
	if net > 0 {
		return Decision{Yes: true, NetITE: net, Reason: fmt.Sprintf("pays back within %.0f turns", paybackTurns(penalty, s.W.Read*shrink))}
	}
	return Decision{NetITE: net, Reason: fmt.Sprintf("would not pay back within %.0f turns; waiting for a cold moment", n)}
}

// paybackTurns estimates turns needed to repay a positive penalty, using 1e9 when per-turn savings
// are nonpositive.
func paybackTurns(penalty, perTurn float64) float64 {
	if perTurn <= 0 {
		return 1e9
	}
	if penalty <= 0 {
		return 0
	}
	return penalty / perTurn
}

// Stale reports whether a ready patch should be dropped and proposed again: it
// has waited too many requests for its commit moment, or the thread has grown so
// much since its snapshot that it no longer addresses the pressure. A held patch
// must not be able to block compaction forever.
func (p Planner) Stale(snapTokens, liveTokens, heldRequests int) (bool, string) {
	p.fill()
	if heldRequests > p.HoldMaxRequests {
		return true, fmt.Sprintf("held for %d requests without a commit moment", heldRequests)
	}
	limit := int(p.StaleGrowthFrac * float64(snapTokens))
	if limit < p.StaleGrowthTokens {
		limit = p.StaleGrowthTokens
	}
	if grown := liveTokens - snapTokens; grown > limit {
		return true, fmt.Sprintf("thread grew by %d tokens since the patch was computed", grown)
	}
	return false, ""
}

// IsCold reports whether a cached prefix last used at lastStart is expected to
// have expired by now, given the provider's entry lifetime. A zero ttl (no
// modelled expiry) never reports cold on its own; measured cache reads decide.
func (p Planner) IsCold(lastStart, now time.Time, ttl time.Duration) bool {
	p.fill()
	if lastStart.IsZero() {
		return true
	}
	if ttl <= 0 {
		return false
	}
	return now.Sub(lastStart) >= ttl-p.ColdMargin
}
