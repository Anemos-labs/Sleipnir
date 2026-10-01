package agent

import (
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// ttl is the cache entry lifetime the agent assumes: the model's modelled
// lifetime, else the endpoint's profile. Zero means no expiry is modelled (an
// engine that evicts under memory pressure).
func (a *Agent) ttl(prof provider.Profile) time.Duration {
	if t := a.cfg.Model.Cache.DefaultTTL(); t > 0 {
		return t
	}
	return prof.Cache.DefaultTTL()
}

// isWarmLocked estimates whether the agent's cached prefix is still resident.
// Callers hold a.mu.
//
// Warmth is a question about time and about evidence, never about how big the
// last tool result was:
//
//   - With a modelled entry lifetime the answer is the lifetime alone. Every
//     request re-writes or refreshes the prefix it sent, hit or miss, so the
//     agent is warm until that lifetime has run out since its last request.
//   - Without one (engines that evict under memory pressure) the evidence is
//     whether the previous request read what the guard said it could
//     (Check.ReadableTokens), not the hit ratio, which only says how much of the
//     prompt is old. A provider that never reports cache usage gives no evidence
//     either way and counts as warm: treating silence as a cold cache would take
//     the deterministic-masking path on every boundary, forever.
func (a *Agent) isWarmLocked(now time.Time) bool {
	if a.mainReqs == 0 || a.lastStart.IsZero() {
		return false
	}
	if ttl := a.ttl(a.cfg.Provider.Profile()); ttl > 0 {
		return !a.cfg.Planner.IsCold(a.lastStart, now, ttl)
	}
	if !a.reportsCache {
		return true
	}
	return !a.lastMiss
}

// tokenScaleLocked is how many provider tokens one estimated token was on the
// previous main request. The guard's expectations are in estimated tokens; the
// scale removes the estimator's error before they are compared with the tokens
// the provider reports. Callers hold a.mu.
func (a *Agent) tokenScaleLocked() float64 {
	if a.lastEstAll <= 0 || a.lastTotalIn <= 0 {
		return 1
	}
	s := float64(a.lastTotalIn) / float64(a.lastEstAll)
	switch {
	case s < 0.5:
		return 0.5
	case s > 2:
		return 2
	}
	return s
}
