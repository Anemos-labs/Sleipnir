package agent

import (
	"slices"
	"sync"
)

// What the session's compactor model (Config.Compactor) is trusted with. A model that keeps writing patches the harness cannot use
// (JSON that does not parse, a tool call in place of the patch, a patch that does not apply) leaves every agent with the mechanical
// fallback, which folds old turns into one line each and keeps no summary of them: in a 12-worker run on deepseek-v4-flash the
// compactor's patch was usable 211 times in 494, and across four sessions that model did no better than 43%. Once most of the
// compactor's recent patches have failed, the agents compact on their own models (whose fork reads their own cache) and the
// compactor is asked again every compactorProbeEvery-th compaction (a probe): it is trusted again once its last
// compactorRestoreAfter probes were all usable, so a model that answers well again writes the summaries again while one that only
// sometimes does stays benched.
const (
	compactorWindow       = 8  // the compactor patches the record keeps
	compactorBenchAt      = 5  // failed patches of the window that stop the agents from asking the compactor
	compactorProbeEvery   = 10 // while benched, the compactor writes every this-many-th patch (a probe)
	compactorRestoreAfter = 3  // usable probes in a row that make a benched compactor trusted again
)

// CompactorHealth is the record, shared by every agent of a session, of how the compactor's patches fared. It is safe for
// concurrent use, and a nil *CompactorHealth always trusts the compactor.
type CompactorHealth struct {
	mu      sync.Mutex
	recent  []bool // while trusted, the latest compactorWindow patches the compactor wrote; while benched, its probes (oldest first)
	benched bool
	skipped int // compactions written by the agents' own models since the compactor was last asked
	// gen counts the changes of standing. A compaction runs in the background while other agents' patches may bench or restore
	// the compactor: its result is recorded only under the standing it was asked under (use returns gen, record takes it back), so
	// a request asked while trusted that ends after the bench is no probe, and a probe that ends after the restore is no failure
	// of the trusted compactor.
	gen int
}

// NewCompactorHealth returns a record that trusts the compactor.
func NewCompactorHealth() *CompactorHealth { return &CompactorHealth{} }

// use reports whether the next compaction asks the compactor: always while it is trusted; while it is benched, every
// compactorProbeEvery-th compaction, the others going to the agent's own model. gen is the standing it was asked under, for record.
func (h *CompactorHealth) use() (ask bool, gen int) {
	if h == nil {
		return true, 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.benched {
		return true, h.gen
	}
	h.skipped++
	if h.skipped >= compactorProbeEvery {
		h.skipped = 0
		return true, h.gen
	}
	return false, h.gen
}

// record notes whether a patch the compactor wrote, asked under standing gen (use), could be used, and reports a change of
// standing (changed, with benched the new one) and the failures among the patches the record holds (failed of n). A patch asked
// under another standing than the present one is not recorded.
func (h *CompactorHealth) record(gen int, ok bool) (changed, benched bool, failed, n int) {
	if h == nil {
		return false, false, 0, 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if gen != h.gen {
		return false, h.benched, 0, len(h.recent)
	}
	h.recent = append(h.recent, ok)
	if len(h.recent) > compactorWindow {
		h.recent = h.recent[len(h.recent)-compactorWindow:]
	}
	for _, r := range h.recent {
		if !r {
			failed++
		}
	}
	n = len(h.recent)
	switch {
	case !h.benched && failed >= compactorBenchAt:
		h.benched, h.skipped, h.recent, changed = true, 0, nil, true
	case h.benched && n >= compactorRestoreAfter && !slices.Contains(h.recent[n-compactorRestoreAfter:], false):
		h.benched, h.recent, changed = false, nil, true
	}
	if changed {
		h.gen++
	}
	return changed, h.benched, failed, n
}

// Benched reports whether the agents compact on their own models because the compactor's recent patches failed.
func (h *CompactorHealth) Benched() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.benched
}
