package state

import (
	"encoding/json"
	"sort"
	"time"
)

// The accessors of this file read one part of the State under its read lock and copy it out. They exist for a follower that mirrors
// the State elsewhere event by event (the web interface's translator): a whole Snapshot per event would cost the size of the State
// each time, these cost the size of what they return.

// AgentLite returns a copy of the agent with the id without its histories: Hits holds only First (the number of main requests
// answered), and Compacts, Anomalies and Scope are empty. The prompt stack's sections and breakpoints are copied.
func (s *State) AgentLite(id string) (Agent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := s.agents[id]
	if a == nil {
		return Agent{}, false
	}
	v := a.Agent
	v.Scope, v.Leases, v.Compacts, v.Anomalies = nil, nil, nil, nil
	v.Stack.Sections = copyOf(a.Stack.Sections)
	v.Stack.Breakpoints = copyOf(a.Stack.Breakpoints)
	v.Stack.SystemHashes = copyOf(a.Stack.SystemHashes)
	v.Hits = Hits{First: a.hitIdx}
	return v, true
}

// AgentIDs lists the ids of the agents in display order (see Snapshot.Agents).
func (s *State) AgentIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ags := s.sortedAgents()
	out := make([]string, len(ags))
	for i, a := range ags {
		out[i] = a.ID
	}
	return out
}

// Roster returns every agent in display order as AgentLite does, with Scope filled in (the paths of the tasks it holds).
func (s *State) Roster() []Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	scopes := s.scopes()
	ags := s.sortedAgents()
	out := make([]Agent, 0, len(ags))
	for _, a := range ags {
		v := a.Agent
		v.Leases, v.Compacts, v.Anomalies = nil, nil, nil
		v.Scope = copyOf(scopes[a.ID])
		v.Stack.Sections, v.Stack.Breakpoints, v.Stack.SystemHashes = nil, nil, nil
		v.Hits = Hits{First: a.hitIdx}
		out = append(out, v)
	}
	return out
}

// Hits returns the hit-ratio history of the agent with the id (see Hits), the zero Hits when there is no such agent.
func (s *State) Hits(id string) Hits {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := s.agents[id]
	if a == nil {
		return Hits{}
	}
	n := a.hits.len()
	return Hits{First: a.hitIdx - n, Ratios: a.hits.slice(), Marks: a.marks.slice()}
}

// FeedSince returns the lines the feed received after its first n lines, oldest first, and the number of lines it has received in all
// (the n for the next call). A follower that shows what the terminal's feed says calls it after each event with the total of the call
// before, and gets the lines that event wrote, in the words the terminal shows. Lines that the ring has already overwritten are not
// returned; an n above the total (the State was reset) counts from the start.
func (s *State) FeedSince(n int) (lines []FeedLine, total int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total = s.feed.total
	if n > total {
		n = 0
	}
	from := max(n, total-s.feed.len())
	if from >= total {
		return nil, total
	}
	lines = make([]FeedLine, 0, total-from)
	for abs := from; abs < total; abs++ {
		if l, ok := s.feed.byAbs(abs); ok {
			lines = append(lines, *l)
		}
	}
	return lines, total
}

// MarksAt returns the kinds of the markers of the agent's hit-ratio history that precede its main response number at, counted from its
// first as Mark.At is: an epoch (the agent took a new shared layer), a rebase (its thinking blocks were dropped), an anomaly or a
// compaction, in the order they were noted.
func (s *State) MarksAt(id string, at int) []MarkKind {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := s.agents[id]
	if a == nil {
		return nil
	}
	var out []MarkKind
	for i := 0; i < a.marks.len(); i++ {
		if m := a.marks.at(i); m.At == at {
			out = append(out, m.Kind)
		}
	}
	return out
}

// LastCompaction returns the newest compaction the agent with the id committed (a copy), false when it has committed none.
func (s *State) LastCompaction(id string) (Compaction, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := s.agents[id]
	if a == nil || a.compacts.len() == 0 {
		return Compaction{}, false
	}
	return *a.compacts.at(a.compacts.len() - 1), true
}

// Task returns a copy of the task with the id, with its derived column (Task.State).
func (s *State) Task(id string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ts := s.tasks[id]
	if ts == nil {
		return Task{}, false
	}
	t := ts.Task
	t.State = deriveTask(ts)
	t.Deps, t.Files = copyOf(t.Deps), copyOf(t.Files)
	return t, true
}

// TaskIDs lists the ids of the tasks, by number (T2 before T10).
func (s *State) TaskIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return taskLess(out[i], out[j]) })
	return out
}

// Board returns the board's version, counts and alerts without its tasks.
func (s *State) Board() Board {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b := Board{Version: s.board.version, Notes: len(s.board.notes), Dropped: s.board.dropped, Alerts: copyOf(s.board.alerts)}
	for _, ts := range s.tasks {
		switch deriveTask(ts) {
		case TaskTodo:
			b.Counts.Todo++
		case TaskRunning:
			b.Counts.Running++
			if ts.Status == "blocked" {
				b.Counts.Blocked++
			}
		case TaskVerifying:
			b.Counts.Verifying++
		case TaskMerged:
			b.Counts.Merged++
		case TaskFailed:
			b.Counts.Failed++
		}
	}
	return b
}

// GovernorAt returns the governor as of now (the requests per minute counted over the 60 seconds before now).
func (s *State) GovernorAt(now time.Time) Governor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if now.Before(s.clock) {
		now = s.clock
	}
	return s.govSnapshot(now)
}

// PrefixTouch is the TTL entry of the cached prefix the agent rides (kind "prefix"), else the agent's own entry (kind "agent"): when
// it was last read or written and how long it lives. ok is false when neither has been read or written yet.
func (s *State) PrefixTouch(id string) (TTLEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := s.agents[id]
	if a == nil {
		return TTLEntry{}, false
	}
	if g := s.prefixes[a.prefix]; g != nil && !g.touch.last.IsZero() {
		return TTLEntry{Kind: "prefix", Key: g.key, Model: g.model, Tokens: g.tokens, Last: g.touch.last, How: g.touch.how,
			TTLSeconds: int(g.touch.ttl / time.Second), Default: g.touch.dflt}, true
	}
	if !a.touch.last.IsZero() {
		return TTLEntry{Kind: "agent", Key: a.ID, Model: a.Stack.Model, Tokens: a.touch.size, Last: a.touch.last, How: a.touch.how,
			TTLSeconds: int(a.touch.ttl / time.Second), Default: a.touch.dflt}, true
	}
	return TTLEntry{}, false
}

// PendingAsk is the oldest permission question of the agent that waits for an answer.
func (s *State) PendingAsk(agent string) (PermAsk, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, q := range s.perms.pending {
		if q.Agent == agent {
			return q.detach(), true
		}
	}
	return PermAsk{}, false
}

// Session returns what the log says about the session.
func (s *State) Session() Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sess
}

// Totals returns the session's counters.
func (s *State) Totals() Totals {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totals
}

// Mail returns the mail counts and the mailman's state, without the messages.
func (s *State) Mail() Mail {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Mail{Counts: s.mail.counts, Mailman: s.mail.mailman}
}

// Goal returns the standing goal as the log last described it, nil when none was recorded.
func (s *State) Goal() *Goal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.goalSnapshot()
}

// Stalls returns the open supervision findings, oldest first.
func (s *State) Stalls() []Stall {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stallsSnapshot()
}

// ToolSummary says what a tool call was asked to do, in a short line, exactly as Agent.ToolSummary shows it for a call the State
// folded: the command of bash, the path of the file tools, the pattern of grep, and so on. input is the call's arguments as JSON.
func ToolSummary(name string, input json.RawMessage) string {
	var in toolInput
	if len(input) > 0 && len(input) <= maxToolPayload {
		_ = json.Unmarshal(input, &in) // a field of the wrong type leaves that field out
	}
	return toolSummary(name, &in)
}

// ToolStatus is the Status an agent shows while the tool runs: editing for edit, write and apply_patch, waiting for wait, else tool.
func ToolStatus(name string) Status { return toolStatus(name) }

// LayerSplit splits an agent's latest prompt into G0..G5 tokens as the terminal's stack bar does (G6 folded into G5); g0 is the
// constitution estimate. The request sizes G1 to G4 (the sections shared, role, notes and spine); what it does not size, the
// constitution and tools, the thread and the hot tail, is the agent's Stack.Unsectioned, of which G0 takes min(g0, Unsectioned) and
// G5 the rest. A section of another name is counted in G5, so that the six add up to the prompt. A layer the request did not carry
// is zero.
func LayerSplit(a Agent, g0 int) [6]int {
	var out [6]int
	stk := a.Stack
	if len(stk.Sections) == 0 && stk.Unsectioned == 0 {
		return out
	}
	g0 = min(max(g0, 0), stk.Unsectioned)
	out[0] = g0
	out[5] = stk.Unsectioned - g0
	for _, sec := range stk.Sections {
		switch sec.Name {
		case "shared":
			out[1] += sec.Tokens
		case "role":
			out[2] += sec.Tokens
		case "notes":
			out[3] += sec.Tokens
		case "spine":
			out[4] += sec.Tokens
		default:
			out[5] += sec.Tokens
		}
	}
	return out
}
