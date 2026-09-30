package state

import (
	"sort"
	"time"
)

// prefixState is a group of agents that share one prefix_key, and when that prefix was last read or written.
type prefixState struct {
	key        string
	sharedHash string
	role       string
	tokens     int
	model      string
	riders     map[string]struct{}
	first      uint64
	last       uint64
	touch      touchInfo
}

// joinPrefix makes the agent a rider of the prefix group with the key, leaving the one it rode before (an epoch or a changed
// role pin gives it a new key), and keeps the group's description current. The groups are bounded by MaxPrefixes: a new one
// evicts the one that was used longest ago. A group with no riders is dropped.
func (s *State) joinPrefix(a *agentState, key string, seq uint64, sharedHash, role, model string, tokens int) {
	if key == "" {
		return
	}
	if a.prefix != key {
		if old := s.prefixes[a.prefix]; old != nil {
			delete(old.riders, a.ID)
			if len(old.riders) == 0 {
				delete(s.prefixes, a.prefix)
			}
		}
		a.prefix = key
	}
	g := s.prefixes[key]
	if g == nil {
		if len(s.prefixes) >= MaxPrefixes {
			s.evictPrefix()
		}
		g = &prefixState{key: key, riders: map[string]struct{}{}, first: seq}
		s.prefixes[key] = g
	}
	g.riders[a.ID] = struct{}{}
	g.last = seq
	g.sharedHash, g.role, g.model, g.tokens = firstOf(sharedHash, g.sharedHash), firstOf(role, g.role), firstOf(model, g.model), tokens
}

// evictPrefix drops the group used longest ago (ties by key) and unlinks its riders.
func (s *State) evictPrefix() {
	var victim *prefixState
	for _, g := range s.prefixes {
		if victim == nil || g.last < victim.last || (g.last == victim.last && g.key < victim.key) {
			victim = g
		}
	}
	if victim == nil {
		return
	}
	for id := range victim.riders {
		if a := s.agents[id]; a != nil && a.prefix == victim.key {
			a.prefix = ""
		}
	}
	delete(s.prefixes, victim.key)
}

// ttlFor says how long the provider keeps an entry for a request: the lifetime a cache breakpoint of one of the labels asked for
// (the longest), else the lifetime the provider profile recorded in session.start for the model, else DefaultTTL, flagged. Only
// the events count as saying: the built-in price table is a list of prices, not a record of what this endpoint did.
func (s *State) ttlFor(model string, bps []Breakpoint, labels ...string) (d time.Duration, dflt bool) {
	for _, b := range bps {
		for _, l := range labels {
			if b.Label == l && b.TTLSeconds > 0 {
				d = max(d, time.Duration(b.TTLSeconds)*time.Second)
			}
		}
	}
	if d > 0 {
		return d, false
	}
	if m := s.models[clip(model, textID)]; m != nil && m.recorded && m.info.TTLSeconds > 0 {
		return time.Duration(m.info.TTLSeconds) * time.Second, false
	}
	if m := s.recordedLike(model); m != nil && m.TTLSeconds > 0 {
		return time.Duration(m.TTLSeconds) * time.Second, false
	}
	return s.ttl, true
}

// sortedPrefixes lists the prefix groups, the most ridden first.
func (s *State) sortedPrefixes() []Prefix {
	out := make([]Prefix, 0, len(s.prefixes))
	for _, g := range s.prefixes {
		riders := make([]string, 0, len(g.riders))
		for id := range g.riders {
			riders = append(riders, id)
		}
		sort.Slice(riders, func(i, j int) bool { return idLess(riders[i], riders[j]) })
		out = append(out, Prefix{Key: g.key, SharedHash: g.sharedHash, Role: g.role, Tokens: g.tokens, Agents: riders, Riders: len(riders), FirstSeq: g.first, LastSeq: g.last})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Riders != out[j].Riders {
			return out[i].Riders > out[j].Riders
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ttlEntries lists the TTL entries: one per prefix group that has been read or written and one per agent that has, sorted by kind
// (prefixes first), then key.
func (s *State) ttlEntries() []TTLEntry {
	var out []TTLEntry
	for _, g := range s.prefixes {
		if g.touch.last.IsZero() {
			continue
		}
		riders := make([]string, 0, len(g.riders))
		for id := range g.riders {
			riders = append(riders, id)
		}
		sort.Slice(riders, func(i, j int) bool { return idLess(riders[i], riders[j]) })
		out = append(out, TTLEntry{Kind: "prefix", Key: g.key, Agents: riders, Model: g.model, Tokens: g.tokens, Last: g.touch.last, How: g.touch.how,
			TTLSeconds: int(g.touch.ttl / time.Second), Default: g.touch.dflt})
	}
	for _, a := range s.agents {
		if a.touch.last.IsZero() {
			continue
		}
		out = append(out, TTLEntry{Kind: "agent", Key: a.ID, Model: a.Stack.Model, Tokens: a.touch.size, Last: a.touch.last, How: a.touch.how,
			TTLSeconds: int(a.touch.ttl / time.Second), Default: a.touch.dflt})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "prefix"
		}
		if out[i].Kind == "agent" {
			return idLess(out[i].Key, out[j].Key)
		}
		return out[i].Key < out[j].Key
	})
	return out
}
