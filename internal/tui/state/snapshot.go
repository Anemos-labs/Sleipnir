package state

import (
	"sort"
	"time"
)

// Snapshot is an immutable copy of a State: what a renderer goroutine draws one frame from. It shares no memory with the State
// it came from, so the State can go on folding events while the frame is drawn; it is immutable by convention, and its slices
// are not to be modified by the caller (several renderers may hold the same Snapshot).
type Snapshot struct {
	// Seq is the highest event seq applied; Clock the latest event time; First the earliest; Now the moment the snapshot was
	// made for (SnapshotAt), which is Clock for Snapshot. Figures that depend on time (the activity matrix's right edge, the
	// requests per minute, the TTL clocks) are as of Now.
	Seq   uint64    `json:"seq"`
	Clock time.Time `json:"clock,omitzero"`
	First time.Time `json:"first,omitzero"`
	Now   time.Time `json:"now,omitzero"`

	Session Session     `json:"session"`
	Agents  []Agent     `json:"agents,omitempty"` // the manager first, then by id (be-2 before be-10), the harness's service agents last
	Totals  Totals      `json:"totals"`
	Models  []ModelInfo `json:"models,omitempty"`

	Board       Board       `json:"board"`
	Merge       MergeQueue  `json:"merge"`
	Mail        Mail        `json:"mail"`
	Leases      Leases      `json:"leases"`
	Governor    Governor    `json:"governor"`
	Perms       Perms       `json:"perms"`
	Supervision Supervision `json:"supervision,omitzero"`

	Feed     []FeedLine `json:"feed,omitempty"`
	Activity Activity   `json:"activity"`
	// TTL has one entry per prefix and per agent that has been read or written; Prefixes groups the agents that share one
	// prefix_key, the most ridden first.
	TTL      []TTLEntry `json:"ttl,omitempty"`
	Prefixes []Prefix   `json:"prefixes,omitempty"`
	// Anomalies is the session-wide ring of cache anomalies, oldest first.
	Anomalies []Anomaly `json:"anomalies,omitempty"`
	Stats     Stats     `json:"stats"`
}

// Snapshot copies the State as of its own clock, the time of the latest event.
func (s *State) Snapshot() *Snapshot { return s.SnapshotAt(time.Time{}) }

// SnapshotAt copies the State as of now. now matters only for what depends on time: the right edge of the activity matrix (and
// the open busy intervals of agents that are working), the requests per minute. It is never taken to be earlier than the State's
// clock: an event that has been applied is not hidden. The zero time means the clock.
func (s *State) SnapshotAt(now time.Time) *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if now.Before(s.clock) {
		now = s.clock
	}
	ags := s.sortedAgents()
	snap := &Snapshot{
		Seq: s.lastSeq, Clock: s.clock, First: s.first, Now: now,
		Session: s.sess, Totals: s.totals, Models: s.sortedModels(),
		Board: s.boardSnapshot(), Merge: s.mergeSnapshot(), Mail: s.mailSnapshot(),
		Governor: s.govSnapshot(now), Perms: s.permSnapshot(), Supervision: s.sup,
		Feed: s.feed.slice(), Activity: s.activitySnapshot(ags, now),
		TTL: s.ttlEntries(), Prefixes: s.sortedPrefixes(), Anomalies: s.anoms.slice(), Stats: s.stats,
	}
	snap.Stats.LastSeq = s.lastSeq
	snap.Stats.UnknownTypes = cloneCounts(s.stats.UnknownTypes)
	snap.Leases = s.leaseSnapshot()
	scopes := s.scopes()
	snap.Agents = make([]Agent, 0, len(ags))
	for _, a := range ags {
		snap.Agents = append(snap.Agents, a.view(scopes[a.ID]))
	}
	return snap
}

func cloneCounts(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// view copies an agent out: every slice is its own.
func (a *agentState) view(scope []string) Agent {
	v := a.Agent
	v.Scope = scope
	v.Leases = nil
	if len(a.leaseOrder) > 0 {
		v.Leases = append([]string(nil), a.leaseOrder...)
		sort.Strings(v.Leases)
	}
	st := &v.Stack
	st.Sections = copyOf(a.Stack.Sections)
	st.Breakpoints = copyOf(a.Stack.Breakpoints)
	st.SystemHashes = copyOf(a.Stack.SystemHashes)
	n := a.hits.len()
	v.Hits = Hits{First: a.hitIdx - n, Ratios: a.hits.slice(), Marks: a.marks.slice()}
	v.Compacts = a.compacts.slice()
	v.Anomalies = a.anoms.slice()
	return v
}

func copyOf[T any](in []T) []T {
	if len(in) == 0 {
		return nil
	}
	return append([]T(nil), in...)
}

// scopes gives each agent the paths its tasks in progress may touch: the union of the scope of the tasks it owns that are doing or
// blocked, sorted, at most MaxFiles.
func (s *State) scopes() map[string][]string {
	out := map[string][]string{}
	for _, ts := range s.tasks {
		if ts.Owner == "" || (ts.Status != "doing" && ts.Status != "blocked") {
			continue
		}
		out[ts.Owner] = append(out[ts.Owner], ts.Files...)
	}
	for id, files := range out {
		sort.Strings(files)
		uniq := files[:0]
		for i, f := range files {
			if i == 0 || f != files[i-1] {
				uniq = append(uniq, f)
			}
		}
		out[id] = uniq[:min(len(uniq), MaxFiles)]
	}
	return out
}

func (s *State) leaseSnapshot() Leases {
	out := Leases{Conflicts: s.leases.conflicts, ScopeViolations: s.leases.scope, Overlaps: s.leases.overlaps}
	for _, l := range s.leases.held {
		out.Held = append(out.Held, l)
	}
	sort.Slice(out.Held, func(i, j int) bool { return out.Held[i].Path < out.Held[j].Path })
	return out
}

func (s *State) govSnapshot(now time.Time) Governor {
	g := &s.gov
	return Governor{Episodes: g.episodes, RatePerMin: g.rate, PauseUntil: g.pauseUntil, LastAt: g.lastAt, Inflight: g.inflight, Queued: g.queued,
		RPM: g.rpm(now), RateLimited: g.rateLimited, Retries: g.retries}
}

func (s *State) permSnapshot() Perms {
	p := &s.perms
	return Perms{Pending: copyOf(p.pending), Recent: p.recent.slice(), Asked: p.asked, Allowed: p.allowed, Denied: p.denied}
}

// ---- accessors on the State --------------------------------------------------------------------------------------------

// Agents returns a copy of the agents in display order: the manager first, then by id, digits compared as numbers (be-2 before
// be-10), the harness's service agents last.
func (s *State) Agents() []Agent { return s.Snapshot().Agents }

// Main is the id of the agent the stack bar shows by default: see Snapshot.Main.
func (s *State) Main() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.sortedAgents() {
		if !a.Service {
			return a.ID
		}
	}
	return ""
}

// Stats returns the counters of what the State did with the events it was given.
func (s *State) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.stats
	st.LastSeq = s.lastSeq
	st.UnknownTypes = cloneCounts(s.stats.UnknownTypes)
	return st
}

// Clock is the time of the latest event applied (the zero time before the first).
func (s *State) Clock() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clock
}

// LastSeq is the highest seq applied.
func (s *State) LastSeq() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSeq
}

// TTLRemaining is how long each cached prefix has left at now: the prefixes that every rider reads, then the agents' own prompts.
// An entry whose events did not say how long the provider keeps it is flagged (TTLEntry.Default) and uses the default of five
// minutes; the list has no entry for a prefix that has not been read or written.
func (s *State) TTLRemaining(now time.Time) []TTLStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return statuses(s.ttlEntries(), now)
}

func statuses(es []TTLEntry, now time.Time) []TTLStatus {
	out := make([]TTLStatus, len(es))
	for i, e := range es {
		rem := e.Remaining(now)
		st := TTLStatus{TTLEntry: e, Remaining: rem, Cold: rem <= 0}
		if e.TTLSeconds > 0 {
			st.Frac = float64(rem) / float64(time.Duration(e.TTLSeconds)*time.Second)
		}
		out[i] = st
	}
	return out
}

// ---- accessors on the Snapshot -----------------------------------------------------------------------------------------

// TTLRemaining is State.TTLRemaining for the snapshot's entries.
func (sn *Snapshot) TTLRemaining(now time.Time) []TTLStatus { return statuses(sn.TTL, now) }

// Agent returns the agent with the id.
func (sn *Snapshot) Agent(id string) (Agent, bool) {
	for _, a := range sn.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// Main is the id of the agent the stack bar shows by default: the manager of a swarm, else the lone agent, else the first agent
// by id; the harness's own service agents (the mailman) are never main. It is "" when there are no agents.
func (sn *Snapshot) Main() string {
	for _, a := range sn.Agents {
		if !a.Service {
			return a.ID
		}
	}
	return ""
}

// Resolve is the agent the UI focuses: focus when it names an agent of the snapshot, else Main. A focus on an agent that has
// been evicted, or that never existed, falls back to the main agent instead of to nothing.
func (sn *Snapshot) Resolve(focus string) string {
	if _, ok := sn.Agent(focus); ok && focus != "" {
		return focus
	}
	return sn.Main()
}

// Focused returns the focused agent: Resolve(focus), as an Agent.
func (sn *Snapshot) Focused(focus string) (Agent, bool) { return sn.Agent(sn.Resolve(focus)) }

// Cycle returns the agent delta places after focus in display order (before it for a negative delta), wrapping round, and skipping
// the harness's service agents. A focus that is not an agent starts from the main agent. It is "" when there are no agents.
func (sn *Snapshot) Cycle(focus string, delta int) string {
	var ids []string
	at := -1
	cur := sn.Resolve(focus)
	for _, a := range sn.Agents {
		if a.Service {
			continue
		}
		if a.ID == cur {
			at = len(ids)
		}
		ids = append(ids, a.ID)
	}
	if len(ids) == 0 {
		return ""
	}
	if at < 0 {
		at = 0
	}
	n := len(ids)
	return ids[((at+delta)%n+n)%n]
}

// CacheHistory returns the hit-ratio history and markers of an agent, for a sparkline; the zero Hits when there is no such agent.
func (sn *Snapshot) CacheHistory(id string) Hits {
	a, _ := sn.Agent(id)
	return a.Hits
}

// ModelOf is what the snapshot knows about the model an agent runs: the model of its latest request, else the one it was spawned
// with, else the session's. ok is false when none of them is a model the snapshot has heard of.
func (sn *Snapshot) ModelOf(a Agent) (ModelInfo, bool) {
	for _, id := range []string{a.Stack.Model, a.Model, sn.Session.Model} {
		if id == "" {
			continue
		}
		for _, m := range sn.Models {
			if m.ID == id {
				return m, true
			}
		}
	}
	return ModelInfo{}, false
}

// ContextFill is how full the agent's context window was at its latest answered request: the tokens of that prompt over the
// window of its model, from 0 to 1 (a prompt over the window counts as 1). ok is false when there is no answered request yet or
// the window is not known (the events did not say and the price table does not have the model).
func (sn *Snapshot) ContextFill(a Agent) (fill float64, ok bool) {
	m, found := sn.ModelOf(a)
	if !found || m.ContextTokens <= 0 || a.Stack.Prompt <= 0 {
		return 0, false
	}
	return min(float64(a.Stack.Prompt)/float64(m.ContextTokens), 1), true
}

// Elapsed is how long the session has been running at now: since session.start, else since the first event.
func (sn *Snapshot) Elapsed(now time.Time) time.Duration {
	from := sn.Session.Started
	if from.IsZero() {
		from = sn.First
	}
	if from.IsZero() || now.Before(from) {
		return 0
	}
	return now.Sub(from)
}
