package kv

import (
	"fmt"
	"time"

	"github.com/reee344/sleipnir/internal/cost"
)

// Pressure thresholds and horizons for the compaction planner. Zero values fall
// back to the defaults in DefaultPlanner.
type Planner struct {
	// SoftThreadTokens: start compacting once the verbatim thread exceeds this.
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
}

// State is a snapshot of one agent's cache situation.
type State struct {
	// PrefixTokens (S): the part of the prompt that survives a commit intact
	// (constitution, shared, role, notes and the existing spine).
	PrefixTokens int
	// ThreadTokens (T): the verbatim thread today.
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
}

// Outcome is what a compaction would do, from a computed or ready patch.
type Outcome struct {
	SpineAdded     int // tokens
	RetainedTokens int // thread tokens that remain
	// CompactorITE is the input-token-equivalent cost of the model call that
	// produced (or would produce) the patch.
	CompactorITE float64
}

// Decision explains a planner verdict.
type Decision struct {
	Yes    bool
	NetITE float64 // expected saving in input-token equivalents (may be negative)
	Reason string
}

// ShouldStart decides whether to spend a compactor call at all.
func (p Planner) ShouldStart(s State) Decision {
	p.fill()
	switch {
	case s.ThreadTokens >= p.HardThreadTokens:
		return Decision{Yes: true, Reason: fmt.Sprintf("thread %dk over hard limit %dk", s.ThreadTokens/1000, p.HardThreadTokens/1000)}
	case s.ContextWindow > 0 && float64(s.PromptTokens) >= p.ContextFraction*float64(s.ContextWindow):
		return Decision{Yes: true, Reason: "prompt near context window"}
	case s.ThreadTokens >= p.SoftThreadTokens:
		return Decision{Yes: true, Reason: fmt.Sprintf("thread %dk over soft limit %dk", s.ThreadTokens/1000, p.SoftThreadTokens/1000)}
	case !s.Warm && s.ThreadTokens >= p.MinThreadTokens:
		return Decision{Yes: true, Reason: "cache is cold: rewriting is free"}
	}
	return Decision{Reason: "no pressure"}
}

// ShouldCommit decides whether to apply a ready patch now or hold it for a
// cheaper moment.
//
// Cost model, in input-token equivalents, for a patch turning a thread of T
// tokens into spine A plus retained R, over a prefix S that survives:
//
//	warm:  extra cost of the next request = Write*(A+R) - Read*T
//	       saving on every later request  = Read*(T-A-R)
//	cold:  the next request rewrites everything anyway, so the commit only
//	       shrinks that write: saving Write*(T-A-R), never negative.
//
// The compactor call itself is sunk once a patch exists, so it is only counted
// when deciding whether to start.
func (p Planner) ShouldCommit(s State, o Outcome) Decision {
	p.fill()
	shrink := float64(s.ThreadTokens - o.SpineAdded - o.RetainedTokens)
	if shrink <= 0 {
		return Decision{Reason: "patch does not shrink the prompt"}
	}
	if s.ThreadTokens >= p.HardThreadTokens {
		return Decision{Yes: true, Reason: "hard limit: commit regardless of cost"}
	}
	if !s.Warm {
		return Decision{Yes: true, NetITE: s.Write * shrink, Reason: "cache is cold: commit is free"}
	}
	n := s.Remaining
	if n == 0 {
		n = p.HorizonTurns
	}
	penalty := s.Write*float64(o.SpineAdded+o.RetainedTokens) - s.W.Read*float64(s.ThreadTokens)
	saving := n * s.W.Read * shrink
	net := saving - penalty
	if net > 0 {
		return Decision{Yes: true, NetITE: net, Reason: fmt.Sprintf("pays back within %.0f turns", paybackTurns(penalty, s.W.Read*shrink))}
	}
	return Decision{NetITE: net, Reason: fmt.Sprintf("would not pay back within %.0f turns; waiting for a cold moment", n)}
}

func paybackTurns(penalty, perTurn float64) float64 {
	if perTurn <= 0 {
		return 1e9
	}
	if penalty <= 0 {
		return 0
	}
	return penalty / perTurn
}

// IsCold reports whether a cached prefix last used at lastStart is expected to
// have expired by now, given the provider's entry lifetime. A zero ttl (no
// modelled expiry) never reports cold on its own; measured hit ratios decide.
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
