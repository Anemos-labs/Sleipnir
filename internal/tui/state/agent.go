package state

import (
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// runState is the coarse state of an agent's run; Status is derived from it, the tools and the stuck flag.
type runState uint8

const (
	runStarting runState = iota // spawned or given work, no request sent yet
	runThinking                 // a request is in flight, or its results are being turned into the next one
	runIdle
	runDone
	runError
)

// openTool is a tool call that has started and not returned.
type openTool struct {
	id, name, summary string
	t                 time.Time
	seq               uint64
	status            Status // what the agent shows while it runs
}

// openReq is a model request that has been sent and not answered.
type openReq struct {
	req   string
	t     time.Time
	seq   uint64
	side  bool
	model string
}

// planInfo is what the last compact.plan of an agent said about the moment.
type planInfo struct {
	known bool
	warm  bool
}

// touchInfo is the last time a prompt of the agent's was read from or written to the provider's cache.
type touchInfo struct {
	last time.Time
	how  string
	ttl  time.Duration
	dflt bool
	size int
}

// agentState is an Agent and the bookkeeping behind it.
type agentState struct {
	Agent
	run        runState
	tools      []openTool
	reqs       []openReq
	leaseOrder []string
	busySince  time.Time
	row        actRow
	hits       ring[float64]
	marks      ring[Mark]
	compacts   ring[Compaction]
	anoms      ring[Anomaly]
	hitIdx     int // the main responses folded in: the index of the next sample
	mainReqs   int // the main requests sent
	plan       planInfo
	prefix     string // the prefix group it rides
	touch      touchInfo
	lastTask   string  // the task it last held, for settling "done" whichever of the accept and the end comes first
	asks       int     // permission questions of this agent that are waiting for an answer (State.perms.pending holds them)
	failed     failure // its newest failed model request, until something else of its own happens
}

func newAgentState(id string, t time.Time) *agentState {
	a := &agentState{
		hits: newRing[float64](HistCap), marks: newRing[Mark](MarkCap),
		compacts: newRing[Compaction](CompactCap), anoms: newRing[Anomaly](AnomalyCap),
	}
	a.ID, a.Status, a.LastActive = id, StatusStarting, t
	return a
}

// setTask records the assignment the agent holds; the last non-empty one is remembered.
func (a *agentState) setTask(id string) {
	a.Task = id
	if id != "" {
		a.lastTask = id
	}
}

// settleDone turns an idle worker into a done one when the task it last held was accepted: its job is finished. It is called when
// the worker goes idle and when the task is accepted, so the order of the two does not matter.
func (s *State) settleDone(a *agentState) {
	if a.run != runIdle || a.lastTask == "" {
		return
	}
	if ts := s.tasks[a.lastTask]; ts != nil && ts.Status == "done" && (ts.Owner == "" || ts.Owner == a.ID) {
		a.run = runDone
		a.refresh()
	}
}

// active marks the agent as having done something at t.
func (a *agentState) active(t time.Time) {
	if t.After(a.LastActive) {
		a.LastActive = t
	}
}

// refresh derives Status and the current tool from the run state, the open tools, the questions it waits on and the stuck flag. A
// run that ended (idle, done, error) shows that whatever else is recorded; a working agent shows asking while a permission
// question of its is unanswered (it is held, whatever its tool call looks like), else stuck while it has not got out of a
// repetition, else its newest tool, else thinking once it has sent a request, else starting.
func (a *agentState) refresh() {
	switch a.run {
	case runIdle:
		a.Status = StatusIdle
	case runDone:
		a.Status = StatusDone
	case runError:
		a.Status = StatusError
	default:
		switch {
		case a.asks > 0:
			a.Status = StatusAsking
		case a.Stuck.Active:
			a.Status = StatusStuck
		case len(a.tools) > 0:
			a.Status = a.tools[len(a.tools)-1].status
		case a.run == runThinking:
			a.Status = StatusThinking
		default:
			a.Status = StatusStarting
		}
	}
	if n := len(a.tools); n > 0 {
		top := a.tools[n-1]
		a.Tool, a.ToolSummary, a.ToolSince = top.name, top.summary, top.t
	} else {
		a.Tool, a.ToolSummary, a.ToolSince = "", "", time.Time{}
	}
	a.OpenTools, a.InFlight, a.Asking = len(a.tools), len(a.reqs), a.asks
	a.ReqSince = time.Time{}
	for _, r := range a.reqs {
		if !r.side {
			a.ReqSince = r.t
			break
		}
	}
}

// syncBusy opens or closes the busy interval of the activity lane: the agent is busy while a model request or a tool call is
// in flight. A closed interval is credited to the lane; an open one is credited by the snapshot, up to its own now.
func (a *agentState) syncBusy(t time.Time) {
	busy := len(a.tools)+len(a.reqs) > 0
	switch {
	case busy && a.busySince.IsZero():
		a.busySince = t
	case !busy && !a.busySince.IsZero():
		a.row.addBusy(a.busySince, t)
		a.busySince = time.Time{}
	}
}

// endRun closes every open span and sets the run state: the agent has stopped.
func (a *agentState) endRun(r runState, t time.Time) {
	a.run = r
	a.tools, a.reqs = a.tools[:0], a.reqs[:0]
	a.Stuck.Active = false
	a.Compacting = false
	a.syncBusy(t)
	a.refresh()
}

// endRun is agentState.endRun for an agent that has stopped for good or for now: what it waited for is over as well, so the
// permission questions it had put are no longer pending (the engine answers them "canceled" before a run ends, but a log may be
// cut short, and a question must not outlive its asker).
func (s *State) endRun(a *agentState, r runState, t time.Time) {
	s.dropAsks(a)
	a.endRun(r, t)
}

// halt sets the run state of an agent that has stopped working by a way that closes its spans itself (a final answer, a request
// that failed or was cancelled): what it waited for is over as well, so its questions are not pending any more.
func (s *State) halt(a *agentState, r runState) {
	s.dropAsks(a)
	a.run = r
}

// stopped is the run state of an agent whose run was cut short: idle, unless it had ended for good already (a cancel that arrives
// after the manager's answer, or after a failure, does not bring the agent back).
func stopped(r runState) runState {
	if r == runDone || r == runError {
		return r
	}
	return runIdle
}

// removeTool takes the open tool call with the id off the list.
func (a *agentState) removeTool(id string) (openTool, bool) {
	for i := len(a.tools) - 1; i >= 0; i-- {
		if a.tools[i].id == id {
			t := a.tools[i]
			a.tools = append(a.tools[:i], a.tools[i+1:]...)
			return t, true
		}
	}
	return openTool{}, false
}

// takeReq takes the open request with the id off the list. A response that names no request, or one that is not open, takes
// nothing: it must not close the span of another request.
func (a *agentState) takeReq(id string) (openReq, bool) {
	if id == "" {
		return openReq{}, false
	}
	for i := range a.reqs {
		if a.reqs[i].req == id {
			r := a.reqs[i]
			a.reqs = append(a.reqs[:i], a.reqs[i+1:]...)
			return r, true
		}
	}
	return openReq{}, false
}

// ---- lifecycle events -------------------------------------------------------------------------------------------------

func (s *State) onSpawn(e events.Event, t time.Time) {
	var p struct {
		ID      string `json:"id"`
		Role    string `json:"role"`
		Task    string `json:"task"`
		By      string `json:"by"`
		Parent  string `json:"parent"`
		Model   string `json:"model"`
		Service bool   `json:"service"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	id := firstOf(p.ID, e.Agent)
	a := s.agent(id, t)
	if a == nil {
		if nonAgents[clip(id, textID)] {
			s.bad()
		}
		return
	}
	s.sess.Swarm = true
	a.Role = firstOf(clip(p.Role, textID), a.Role)
	a.Parent = firstOf(clip(firstOf(p.Parent, p.By), textID), a.Parent)
	a.Model = firstOf(clip(p.Model, textID), a.Model)
	a.setTask(firstOf(clip(p.Task, textID), a.Task))
	a.Service = a.Service || p.Service
	a.Spawned, a.SpawnSeq = t, e.Seq
	a.EndState = ""
	if a.run != runThinking {
		a.run = runStarting
	}
	a.active(t)
	a.refresh()
	what := a.Role
	if what == "" {
		what = "agent"
	}
	text := "spawned " + a.ID + " (" + what + ")"
	if a.Service {
		text = "started " + a.ID + " (" + what + ", harness service)"
	} else if a.Task != "" {
		text += " for " + a.Task
	}
	s.line(e.Seq, t, a.ID, FeedSpawn, GlyphSpawn, text, "")
}

func (s *State) onAssign(e events.Event, t time.Time) {
	var p struct {
		ID   string `json:"id"`
		Role string `json:"role"`
		Task string `json:"task"`
		By   string `json:"by"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	a := s.agent(firstOf(p.ID, e.Agent), t)
	if a == nil {
		return
	}
	a.Role = firstOf(a.Role, clip(p.Role, textID))
	a.setTask(firstOf(clip(p.Task, textID), a.Task))
	a.EndState = ""
	if a.run != runThinking {
		a.run = runStarting
	}
	a.active(t)
	a.refresh()
	s.line(e.Seq, t, a.ID, FeedSpawn, GlyphSpawn, "gave "+firstOf(a.Task, "new work")+" to "+a.ID, "")
}

func (s *State) onAgentState(e events.Event, t time.Time) {
	var p struct {
		ID    string  `json:"id"`
		State string  `json:"state"`
		Line  string  `json:"line"`
		Task  *string `json:"task"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	a := s.agent(firstOf(p.ID, e.Agent), t)
	if a == nil {
		return
	}
	s.applyInfo(a, p.State, p.Line, p.Task, t)
}

// applyInfo is what agent.state and the board's "agent" op both say: the harness-derived state, line and task of an agent.
func (s *State) applyInfo(a *agentState, state, line string, task *string, t time.Time) {
	a.Line = clean(line, textShort)
	if task != nil {
		a.setTask(clip(*task, textID))
	}
	switch state {
	case "running", "waiting":
		if a.run == runIdle || a.run == runDone || a.run == runError {
			a.run = runStarting
		}
		a.refresh()
	case "idle":
		s.endRun(a, runIdle, t)
		s.settleDone(a)
	case "done":
		s.endRun(a, runDone, t)
	case "failed":
		s.endRun(a, runError, t)
	}
	a.active(t)
}

func (s *State) onAgentEnd(e events.Event, t time.Time) {
	var p struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Evidence string `json:"evidence"`
		Service  bool   `json:"service"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	a := s.agent(firstOf(p.ID, e.Agent), t)
	if a == nil {
		return
	}
	a.Service = a.Service || p.Service
	a.EndState = clip(p.State, textID)
	a.Evidence = clean(p.Evidence, textLong)
	switch p.State {
	case "failed":
		s.endRun(a, runError, t)
	case "done":
		s.endRun(a, runDone, t)
	default:
		s.endRun(a, runIdle, t)
		s.settleDone(a)
	}
	a.active(t)
	glyph, text := GlyphEnd, a.ID+" finished"
	if a.run == runError {
		glyph, text = GlyphFail, a.ID+" stopped with a failure"
	}
	s.line(e.Seq, t, a.ID, FeedEnd, glyph, text, a.Evidence)
}

// sentBack says why an agent's answer was sent back, by the phase of the agent.stuck that records it.
var sentBack = map[string]string{
	"plan":   "the answer was sent back: the plan still has open steps",
	"verify": "the answer was sent back: code changed and no tests ran",
	"leak":   "the message was sent back: it was a tool call written as text",
}

func (s *State) onStuck(e events.Event, t time.Time) {
	var p struct {
		Phase string `json:"phase"`
		Note  string `json:"note"`
		Error string `json:"error"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	note := clean(firstOf(p.Note, p.Error), textLine)
	if text, ok := sentBack[p.Phase]; ok {
		// The loop sent an answer back; no call failed, so the agent is not stuck.
		s.line(e.Seq, t, e.Agent, FeedNote, GlyphInfo, text, note)
		return
	}
	a := s.agent(e.Agent, t)
	if a != nil {
		a.Stuck.Phase = clip(p.Phase, textID)
		a.Stuck.Note, a.Stuck.At = note, t
		if p.Phase == "stop" {
			a.Stuck.Stops++
		} else {
			a.Stuck.Nudges++
		}
		if a.run != runIdle && a.run != runDone && a.run != runError {
			a.Stuck.Active = true
		}
		a.row.markAt(t, ActStuck)
		a.active(t)
		a.refresh()
	}
	text := "stuck: the same call failed again and again"
	if p.Phase == "stop" {
		text = "stuck: the run was ended"
	}
	s.line(e.Seq, t, e.Agent, FeedStuck, GlyphWarn, text, note)
}

// onCancel is agent.cancel (internal/agent agent.go run): the run ended because its context was cancelled, and the event says what
// the run was doing (phase) and how far it had got (steps). The producer writes it as the last thing the run does, after the
// results of the tools that were interrupted and after the model.error that reports a request cancelled, so the agent has stopped:
// it is idle, unless it had ended for good already. A swarm worker's agent.end follows and says what it is now.
func (s *State) onCancel(e events.Event, t time.Time) {
	var p struct {
		Phase string `json:"phase"`
		Cause string `json:"cause"`
		Steps int    `json:"steps"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	phase, cause := clip(p.Phase, textID), clip(p.Cause, textID)
	s.totals.RunsCancelled++
	if a := s.agent(e.Agent, t); a != nil {
		if a.failed.ok && !a.failed.side && phase == CancelModel {
			// The run ended inside a request, and the request is what failed, just before: it failed because the run was cancelled.
			s.reclassify(a)
		}
		a.failed = failure{}
		a.Cancel = Cancel{Count: a.Cancel.Count + 1, Phase: phase, Cause: cause, Steps: clampInt(p.Steps), At: t}
		s.endRun(a, stopped(a.run), t)
		a.active(t)
	}
	who := "a run"
	if e.Agent != "" {
		who = clip(e.Agent, textID) + "'s run"
	}
	text := who + " was cancelled"
	switch phase {
	case CancelModel:
		text += " while it waited for the model"
	case CancelTools:
		text += " while its tools ran"
	}
	detail := "before its first answer"
	switch {
	case p.Steps == 1:
		detail = "after 1 step"
	case p.Steps > 1:
		detail = "after " + strconv.Itoa(clampInt(p.Steps)) + " steps"
	}
	if cause == CauseDeadline {
		detail += ", a time limit passed"
	}
	s.line(e.Seq, t, e.Agent, FeedCancel, GlyphEnd, text, detail)
}

// onFault is a panic, a timeout or an abandoned run: a line for the feed, and for an abandoned run the agent's status.
func (s *State) onFault(e events.Event, t time.Time) {
	var p struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Panic   string `json:"panic"`
		Where   string `json:"where"`
		In      string `json:"in"`
		Agent   string `json:"agent"`
		LimitMs int64  `json:"limit_ms"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	id := firstOf(p.ID, p.Agent, e.Agent)
	var text, detail string
	switch e.Type {
	case "agent.abandon":
		text = id + " did not stop when asked: its run was abandoned"
		if a := s.agent(id, t); a != nil {
			s.endRun(a, runError, t)
			a.active(t)
		}
	case "tool.timeout":
		text = "tool " + clip(p.Name, textID) + " ran too long and was stopped"
		detail = "limit " + fmtDur(p.LimitMs)
	case "tool.panic":
		text = "tool " + clip(p.Name, textID) + " crashed"
	case "agent.panic":
		text = id + " crashed"
		detail = clean(strings.TrimSpace(p.Panic+" "+p.Where+" "+p.In), textShort)
	default:
		text = e.Type
		detail = clean(p.Panic, textShort)
	}
	s.line(e.Seq, t, id, FeedError, GlyphFail, text, detail)
}
