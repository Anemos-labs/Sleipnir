package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// Options configures a State. The zero value is what the harness uses.
type Options struct {
	// Prices is the list-price table that prices the cache reads of models session.start did not price itself. Nil means
	// cost.Defaults(), the repository's built-in table.
	Prices *cost.Table
	// DefaultTTL is the cache lifetime assumed, and flagged, where the events do not carry one. Zero means DefaultTTL (five minutes).
	DefaultTTL time.Duration
}

// State is the UI's view of a session, folded from its events. See the package documentation for what it holds and the rules it
// keeps. The zero value is not usable: make one with New.
type State struct {
	mu     sync.RWMutex
	prices *cost.Table
	ttl    time.Duration

	clock   time.Time // the latest event time seen
	first   time.Time // the first
	lastSeq uint64
	stats   Stats

	sess     Session
	totals   Totals
	agents   map[string]*agentState
	tasks    map[string]*taskState
	board    boardState
	mail     mailState
	merge    mergeState
	leases   leaseState
	gov      govState
	perms    permState
	sup      Supervision
	feed     ring[FeedLine]
	models   map[string]*modelState
	prefixes map[string]*prefixState
	anoms    ring[Anomaly]
	shared   []string // the shared layers (G1 hashes) that requests have carried, oldest first
	sup2     superviseState
	hooks    hookState
}

// New returns an empty State.
func New() *State { return NewWith(Options{}) }

// NewWith returns an empty State with the given options.
func NewWith(opt Options) *State {
	s := &State{prices: opt.Prices, ttl: opt.DefaultTTL}
	if s.prices == nil {
		s.prices = cost.Defaults()
	}
	if s.ttl <= 0 {
		s.ttl = DefaultTTL
	}
	s.init()
	return s
}

// init empties everything but the options.
func (s *State) init() {
	s.clock, s.first, s.lastSeq = time.Time{}, time.Time{}, 0
	s.stats = Stats{}
	s.sess = Session{}
	s.totals = Totals{Savings: Savings{Assumption: SavingsAssumption}}
	s.agents = map[string]*agentState{}
	s.tasks = map[string]*taskState{}
	s.board = newBoardState()
	s.mail = newMailState()
	s.merge = newMergeState()
	s.leases = newLeaseState()
	s.gov = govState{}
	s.perms = permState{recent: newRing[PermDecision](PermLog)}
	s.sup = Supervision{}
	s.feed = newRing[FeedLine](FeedCap)
	s.models = map[string]*modelState{}
	s.prefixes = map[string]*prefixState{}
	s.anoms = newRing[Anomaly](AnomalyLog)
	s.shared = nil
	s.sup2 = newSuperviseState()
	s.hooks.agents, s.hooks.tasks = nil, nil
	if s.hooks.notified != nil {
		s.hooks.notified = map[string]Status{} // the agents are gone; the callbacks stay registered
	}
}

// Reset forgets everything, as a log that was truncated or replaced needs. The options stay. Stats.Resets counts it.
func (s *State) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	resets := s.stats.Resets + 1
	s.init()
	s.stats.Resets = resets
}

// Apply folds one event into the State. It never panics and never blocks on anything but the State's own lock.
//
// Bad input is not an error: an event whose Seq is not greater than the highest applied is a duplicate and is ignored
// (Stats.Stale), one of an unknown type is ignored (Stats.Unknown), a payload that is not JSON, or is larger than the decode
// limit, is ignored (Stats.Bad, Stats.TooBig), a field of the wrong type or a missing one leaves that part of the event out
// and applies the rest, and a handler that panics is contained (Stats.Panics: a bug, and the tests require zero). An event
// without a Seq is applied as it comes; one without a timestamp takes the State's clock.
//
// The callbacks registered with OnStatus and OnChange are called after the lock is released, before Apply returns.
func (s *State) Apply(e events.Event) {
	s.mu.Lock()
	s.apply(e)
	n, ok := s.takeNote(s.clock)
	status, change := s.callbacks()
	s.mu.Unlock()
	if ok {
		s.deliver(n, status, change)
	}
}

// ApplyAll folds the events in order under one lock: the same as calling Apply for each. The callbacks of OnStatus and OnChange are
// called for each event, in order, once the lock is released.
func (s *State) ApplyAll(evs []events.Event) {
	s.mu.Lock()
	var notes []note
	for i := range evs {
		s.apply(evs[i])
		if n, ok := s.takeNote(s.clock); ok {
			notes = append(notes, n)
		}
	}
	status, change := s.callbacks()
	s.mu.Unlock()
	for _, n := range notes {
		s.deliver(n, status, change)
	}
}

// AddCorrupt records that a loader skipped n damaged lines of the log (events.CorruptError), for the footer.
func (s *State) AddCorrupt(n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	s.stats.Corrupt += n
	s.mu.Unlock()
}

func (s *State) apply(e events.Event) {
	defer func() {
		if r := recover(); r != nil {
			s.stats.Panics++
			// What panicked, where, and from which event: the State goes on, and a report of the bug has what it needs.
			s.stats.LastPanic = clip(fmt.Sprintf("%v (event %d, %s)\n%s", r, e.Seq, clip(e.Type, textID), debug.Stack()), maxPanicText)
		}
	}()
	if e.Seq != 0 {
		if e.Seq <= s.lastSeq {
			s.stats.Stale++
			return
		}
		s.lastSeq = e.Seq
	}
	s.stats.Events++
	t := s.eventTime(e.TS)
	if s.sess.ID == "" && e.Session != "" {
		s.sess.ID = clip(e.Session, textID)
	}
	if s.sess.Started.IsZero() {
		s.sess.Started = t // session.start, when it comes, says it again
	}
	switch e.Type {
	case events.TypeLogOpen:
		s.onLogOpen(e)
	case events.TypeLogCorrupt:
		s.onLogCorrupt(e)
	case events.TypeSessionStart:
		s.onSessionStart(e, t)
	case events.TypeSessionEnd:
		s.onSessionEnd(e, t)
	case events.TypeUserInput:
		s.onUserInput(e, t)
	case events.TypeUserSteer:
		s.onUserSteer(e, t)
	case "notice":
		s.onNotice(e, t)

	case events.TypeAgentSpawn:
		s.onSpawn(e, t)
	case "agent.assign":
		s.onAssign(e, t)
	case events.TypeAgentState:
		s.onAgentState(e, t)
	case events.TypeAgentEnd:
		s.onAgentEnd(e, t)
	case events.TypeAgentStuck:
		s.onStuck(e, t)
	case events.TypeAgentCancel:
		s.onCancel(e, t)
	case "agent.panic", "agent.abandon", "supervisor.panic", "sink.panic", "tool.panic", "tool.timeout":
		s.onFault(e, t)

	case events.TypeModelRequest:
		s.onRequest(e, t)
	case events.TypeModelResponse:
		s.onResponse(e, t)
	case events.TypeModelError:
		s.onModelError(e, t)
	case events.TypeToolCall:
		s.onToolCall(e, t)
	case events.TypeToolResult:
		s.onToolResult(e, t)
	case events.TypeToolJob:
		s.onToolJob(e, t)

	case events.TypeCacheAnomaly:
		s.onAnomaly(e, t)
	case events.TypeCompactPlan:
		s.onCompactPlan(e, t)
	case events.TypeCompactPatch:
		s.onCompactPatch(e, t)
	case events.TypeCompactCommit:
		s.onCompactCommit(e, t)
	case events.TypeCompactReject:
		s.onCompactReject(e, t)
	case events.TypeLayerCommit:
		s.onLayerCommit(e, t)

	case events.TypeBoardOp:
		s.onBoardOp(e, t)
	case events.TypeMailSend:
		s.onMailSend(e, t)
	case events.TypeMailRoute:
		s.onMailRoute(e, t)
	case events.TypeMailDeliver:
		s.onMailDeliver(e, t)
	case "mail.drop":
		s.onMailDrop(e, t)
	case events.TypeMailAck:
		s.onMailAck(e, t)
	case events.TypeMailDigest:
		s.onMailDigest(e, t)
	case events.TypeMailDirect:
		s.onMailDirect(e, t)
	case events.TypeMailBatch:
		s.onMailBatch(e, t)
	case events.TypeMailmanState:
		s.onMailman(e, t)

	case events.TypeLease:
		s.onLease(e, t)
	case events.TypeGovernor:
		s.onGovernor(e, t)
	case "swarm.budget":
		s.onBudget(e, t)
	case events.TypePermAsk:
		s.onPermAsk(e, t)
	case events.TypePermDecide:
		s.onPermDecide(e, t)
	case events.TypeSwarmHold, events.TypeSwarmUnfinished, events.TypeSwarmWake, events.TypeSwarmWakePaused, events.TypeSwarmWakeLimit, "swarm.shutdown":
		s.onSupervision(e, t)
	case events.TypeSwarmStall:
		s.onStall(e, t)
	case events.TypeSwarmHandover:
		s.onHandover(e, t)
	case TypeGoalState:
		s.onGoalState(e, t)
	case TypeGoalJudge:
		s.onGoalJudge(e, t)
	case TypeCheckpoint, TypeCheckpointRestore:
		s.onCheckpoint(e, t)

	case events.TypeWorkspaceCreate, events.TypeWorkspaceRemove, events.TypeWorkspacePrune, events.TypeWorkspaceCommit, events.TypeWorkspaceReset,
		events.TypeMergeQueued, events.TypeMergeMerged, events.TypeMergeConflict, events.TypeMergeVerifyFail, events.TypeMergeRolledBack,
		events.TypeMergeRejected, events.TypeMergeFastFwd, events.TypeTaskMerge, events.TypeSwarmIntegration:
		s.onMerge(e, t)

	case events.TypeAgentRestore:
		s.onRestore(e, t)

	case events.TypeTurnAppend, events.TypeAgentSnapshot, events.TypeCachePlan, events.TypeRecall,
		events.TypeOutcome, "tool.spill", "tool.budget", "hook.run",
		events.TypePermState, events.TypeModelSwitch, "agent.prepare", "session.isolation", "swarm.integration_state", TypeWebAction, "verify.run":
		// Known, and nothing the UI shows: the transcript is the conversation's, not the state's.
	default:
		s.stats.Unknown++
		if _, ok := s.stats.UnknownTypes[e.Type]; ok || len(s.stats.UnknownTypes) < MaxUnknownTypes {
			if s.stats.UnknownTypes == nil {
				s.stats.UnknownTypes = map[string]int{}
			}
			s.stats.UnknownTypes[clip(e.Type, textID)]++
		}
	}
}

// eventTime is the time an event counts as: its own when it is usable (a year from 1970 to 2999), else the State's clock. It
// advances the clock, which never goes back.
func (s *State) eventTime(ts time.Time) time.Time {
	if ts.IsZero() || ts.Year() < 1970 || ts.Year() > 2999 {
		return s.clock
	}
	if ts.After(s.clock) {
		s.clock = ts
	}
	if s.first.IsZero() {
		s.first = ts
	}
	return ts
}

// decode reads an event's data into v. No data is an empty v (true). Data that is not JSON, or is larger than limit, is left
// out and counted (false). A field of the wrong type leaves that field at its zero value and the rest decoded (true): a
// payload is never trusted to have the shape its producer documents.
func (s *State) decode(data json.RawMessage, limit int, v any) bool {
	if len(data) == 0 {
		return true
	}
	if len(data) > limit {
		s.stats.TooBig++
		return false
	}
	err := json.Unmarshal(data, v)
	if err == nil {
		return true
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return true
	}
	s.stats.Bad++
	return false
}

// bad counts a payload that was decodable but lacked what the event is for.
func (s *State) bad() { s.stats.Bad++ }

// firstOf returns the first argument that is not empty.
func firstOf(ss ...string) string {
	for _, v := range ss {
		if v != "" {
			return v
		}
	}
	return ""
}

// line adds a line to the feed. The text is made one line and cut; glyph and kind are the caller's.
func (s *State) line(seq uint64, t time.Time, agent string, kind FeedKind, glyph, text, detail string) {
	s.feed.push(FeedLine{Seq: seq, T: t, Agent: clip(agent, textID), Kind: kind, Glyph: glyph, Text: clean(text, textLine), Detail: clean(detail, textShort)})
}

// ---- agents -----------------------------------------------------------------------------------------------------------

// nonAgents are the names the harness uses for events that are nobody's: the swarm runtime, the harness's own mail and board
// writes, the notes curator.
var nonAgents = map[string]bool{"": true, "swarm": true, "harness": true, "curator": true}

// agent returns the agent with the id, creating it on first sight (the log writes an agent's first board and state events before
// its agent.spawn). It returns nil for an id that is no agent, and for a new agent that does not fit under MaxAgents: the agent
// that has been idle, done or failed the longest makes room; if all of them are active the new one is dropped.
func (s *State) agent(id string, t time.Time) *agentState {
	id = clip(id, textID)
	if nonAgents[id] {
		return nil
	}
	if a := s.agents[id]; a != nil {
		s.touchAgent(id)
		return a
	}
	if len(s.agents) >= MaxAgents && !s.evictAgent() {
		s.stats.Dropped++
		return nil
	}
	a := newAgentState(id, t)
	s.agents[id] = a
	s.touchAgent(id)
	return a
}

// evictAgent removes the finished agent that has been quiet the longest (ties by id) and reports whether there was one.
func (s *State) evictAgent() bool {
	var victim *agentState
	for _, a := range s.agents {
		if a.Status.Active() {
			continue
		}
		if victim == nil || a.LastActive.Before(victim.LastActive) || (a.LastActive.Equal(victim.LastActive) && idLess(a.ID, victim.ID)) {
			victim = a
		}
	}
	if victim == nil {
		return false
	}
	s.forget(victim)
	return true
}

// forget removes an agent and everything that refers to it by pointer or by membership.
func (s *State) forget(a *agentState) {
	delete(s.agents, a.ID)
	s.touchAgent(a.ID)
	if g := s.prefixes[a.prefix]; g != nil {
		delete(g.riders, a.ID)
	}
	for _, path := range a.leaseOrder {
		if l, ok := s.leases.held[path]; ok && l.Agent == a.ID {
			delete(s.leases.held, path)
		}
	}
	s.stats.Dropped++
}

// sortedAgents returns the agents in display order: the manager first, then the agents of the session by id (digits compare as
// numbers: be-2 before be-10), then the harness's own service agents.
func (s *State) sortedAgents() []*agentState {
	out := make([]*agentState, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return agentLess(&out[i].Agent, &out[j].Agent) })
	return out
}

// agentLess is the display order of agents.
func agentLess(a, b *Agent) bool {
	ra, rb := agentRank(a), agentRank(b)
	if ra != rb {
		return ra < rb
	}
	return idLess(a.ID, b.ID)
}

// agentRank orders managers before ordinary workers and service agents after them.
func agentRank(a *Agent) int {
	switch {
	case a.Role == "manager":
		return 0
	case a.Service:
		return 2
	}
	return 1
}
