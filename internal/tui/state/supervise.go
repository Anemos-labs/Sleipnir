package state

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Log event types this package folds that internal/events does not name: the standing goal (internal/session/goal.go), the
// checkpoint store's records and the web host's audit trail (written through the session log by sleipnir web).
const (
	TypeGoalState         = "goal.state"
	TypeGoalJudge         = "goal.judge"
	TypeCheckpoint        = "checkpoint"
	TypeCheckpointRestore = "checkpoint.restore"
	TypeWebAction         = "web.action"
)

// The bounds of the supervision records: open stall findings, the handovers kept and the checkpoints kept.
const (
	// MaxStalls bounds the open stall findings (one per kind, agent and task while it is raised).
	MaxStalls = 64
	// HandoverLog is the number of handovers kept, the newest.
	HandoverLog = 32
	// CheckpointLog is the number of checkpoints kept, the newest.
	CheckpointLog = 64
)

// Stall is a supervision finding of the swarm (swarm.stall), open until cleared.
type Stall struct {
	// Kind is the finding: claimed_no_progress, manager_waiting_on_idle, orphaned_task, blocked_cycle or review_starved.
	Kind   string    `json:"kind"`
	Agent  string    `json:"agent,omitempty"`
	Task   string    `json:"task,omitempty"`
	Detail string    `json:"detail,omitempty"`
	Raised time.Time `json:"raised,omitzero"`
	Seq    uint64    `json:"seq,omitempty"`
}

// Handover is one phase of a task changing hands (swarm.handover): begin, done or abort.
type Handover struct {
	Seq     uint64    `json:"seq"`
	T       time.Time `json:"t,omitzero"`
	Task    string    `json:"task"`
	Phase   string    `json:"phase"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	By      string    `json:"by,omitempty"`
	Closure string    `json:"closure,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Goal is the standing goal as the log last described it: the latest goal.state and the latest goal.judge after it.
type Goal struct {
	Objective string `json:"objective,omitempty"`
	Turns     int    `json:"turns,omitempty"`
	Max       int    `json:"max,omitempty"`
	// Paused says why the goal is paused ("" while it is active); Reason is what the judge said last.
	Paused  string `json:"paused,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Repeats int    `json:"repeats,omitempty"`
	Done    bool   `json:"done,omitempty"`
	// Cleared is true when the log recorded no goal after one: it was cleared, or met and dropped.
	Cleared bool      `json:"cleared,omitempty"`
	T       time.Time `json:"t,omitzero"`
	// Verdict is the kind of the judge's latest verdict (done, continue, blocked), VerdictReason its reason and Left what it found
	// still missing (at most 8 items).
	Verdict       string    `json:"verdict,omitempty"`
	VerdictReason string    `json:"verdict_reason,omitempty"`
	Left          []string  `json:"left,omitempty"`
	JudgedAt      time.Time `json:"judged_at,omitzero"`
}

// State is the goal's state word: met, paused, cleared or active.
func (g Goal) State() string {
	switch {
	case g.Cleared:
		return "cleared"
	case g.Done:
		return "met"
	case g.Paused != "":
		return "paused"
	}
	return "active"
}

// Checkpoint is a checkpoint of the project's files as the log recorded it (the "checkpoint" event of the session's checkpoint store).
type Checkpoint struct {
	Seq   uint64 `json:"seq"`
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	// Files counts the files the checkpoint holds; Agents names who wrote them (at most MaxFiles).
	Files  int       `json:"files"`
	Agents []string  `json:"agents,omitempty"`
	Safety bool      `json:"safety,omitempty"`
	T      time.Time `json:"t,omitzero"`
	// Restored counts the times the files were put back as the checkpoint holds them (checkpoint.restore).
	Restored int `json:"restored,omitempty"`
}

// superviseState is what the State keeps of supervision, handovers, the goal and the checkpoints.
type superviseState struct {
	stalls    []Stall
	handovers ring[Handover]
	goal      *Goal
	ckpts     ring[Checkpoint]
}

// newSuperviseState initializes the bounded rings of handovers and checkpoints.
func newSuperviseState() superviseState {
	return superviseState{handovers: newRing[Handover](HandoverLog), ckpts: newRing[Checkpoint](CheckpointLog)}
}

// onStall is swarm.stall (internal/swarm/stall.go): a finding raised or cleared. One finding is open per kind, agent and task.
func (s *State) onStall(e events.Event, t time.Time) {
	var p struct {
		Action string `json:"action"`
		Kind   string `json:"kind"`
		Task   string `json:"task"`
		Agent  string `json:"agent"`
		Detail string `json:"detail"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	st := Stall{Kind: clip(p.Kind, textID), Agent: clip(p.Agent, textID), Task: clip(p.Task, textID), Detail: clean(p.Detail, textLine), Raised: t, Seq: e.Seq}
	if st.Kind == "" {
		s.bad()
		return
	}
	sv := &s.sup2
	at := -1
	for i := range sv.stalls {
		if o := sv.stalls[i]; o.Kind == st.Kind && o.Agent == st.Agent && o.Task == st.Task {
			at = i
			break
		}
	}
	if a := s.agentIfAny(st.Agent); a != nil {
		a.active(t)
	}
	switch p.Action {
	case "clear":
		if at >= 0 {
			sv.stalls = append(sv.stalls[:at], sv.stalls[at+1:]...)
		}
		s.line(e.Seq, t, st.Agent, FeedNote, GlyphInfo, strings.ReplaceAll(st.Kind, "_", " ")+" cleared", st.Task)
	default:
		if at >= 0 {
			sv.stalls[at] = st
		} else {
			if len(sv.stalls) >= MaxStalls {
				sv.stalls = append(sv.stalls[:0], sv.stalls[1:]...)
				s.stats.Dropped++
			}
			sv.stalls = append(sv.stalls, st)
		}
		s.line(e.Seq, t, st.Agent, FeedNote, GlyphWarn, "stalled: "+firstOf(st.Detail, strings.ReplaceAll(st.Kind, "_", " ")), st.Task)
	}
}

// agentIfAny returns the tracked agent with the id, without creating one, and marks it touched.
func (s *State) agentIfAny(id string) *agentState {
	if id == "" {
		return nil
	}
	a := s.agents[clip(id, textID)]
	if a != nil {
		s.touchAgent(a.ID)
	}
	return a
}

// onHandover is swarm.handover (internal/swarm/handover.go): a task moving from one worker to another, begun, done or aborted.
func (s *State) onHandover(e events.Event, t time.Time) {
	var p struct {
		Phase   string          `json:"phase"`
		Task    string          `json:"task"`
		From    string          `json:"from"`
		To      string          `json:"to"`
		By      string          `json:"by"`
		Closure json.RawMessage `json:"closure"`
		Error   string          `json:"error"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	h := Handover{Seq: e.Seq, T: t, Task: clip(p.Task, textID), Phase: clip(p.Phase, textID), From: clip(p.From, textID), To: clip(p.To, textID),
		By: clip(p.By, textID), Closure: closureText(p.Closure), Error: clean(p.Error, textLine)}
	if h.Task == "" || h.Phase == "" {
		s.bad()
		return
	}
	s.sup2.handovers.push(h)
	if h.Task != "" {
		s.touchTask(h.Task)
	}
	switch h.Phase {
	case "done":
		s.line(e.Seq, t, h.By, FeedBoard, GlyphTask, h.Task+" handed from "+firstOf(h.From, "its worker")+" to "+firstOf(h.To, "another worker"), h.Closure)
	case "abort":
		s.line(e.Seq, t, h.By, FeedBoard, GlyphWarn, "the handover of "+h.Task+" from "+firstOf(h.From, "its worker")+" failed", h.Error)
	}
}

// closureText renders a typed closure as the board writes it ({"kind": ..., "target": ...}, or null) in the form kind or kind(target):
// "verified", "superseded(T5)". A closure that is not that shape is the empty string.
func closureText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var c struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Kind == "" {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			return clip(str, textID)
		}
		return ""
	}
	kind := clip(c.Kind, textID)
	if c.Target == "" {
		return kind
	}
	return kind + "(" + clip(c.Target, textID) + ")"
}

// goalWire is internal/goal.State as goal.state carries it: the struct has no JSON tags, so its keys are the Go field names.
type goalWire struct {
	Objective string
	Turns     int
	Max       int
	Paused    string
	Reason    string
	Repeats   int
	Done      bool
}

// onGoalState is goal.state (internal/session/goal.go SaveGoal): the standing goal after a change; a null goal says it was cleared or met
// and dropped.
func (s *State) onGoalState(e events.Event, t time.Time) {
	var p struct {
		Goal *goalWire `json:"goal"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	sv := &s.sup2
	if p.Goal == nil {
		if sv.goal != nil {
			g := *sv.goal
			g.Cleared, g.T = true, t
			sv.goal = &g
		}
		return
	}
	g := Goal{Objective: clean(p.Goal.Objective, textLong), Turns: clampInt(p.Goal.Turns), Max: clampInt(p.Goal.Max), Paused: clean(p.Goal.Paused, textLine),
		Reason: clean(p.Goal.Reason, textLong), Repeats: clampInt(p.Goal.Repeats), Done: p.Goal.Done, T: t}
	if old := sv.goal; old != nil && !old.Cleared && old.Objective == g.Objective {
		g.Verdict, g.VerdictReason, g.Left, g.JudgedAt = old.Verdict, old.VerdictReason, old.Left, old.JudgedAt
	}
	sv.goal = &g
}

// onGoalJudge is goal.judge (internal/session/goal.go JudgeGoal): the judge's verdict on the turn that just ended.
func (s *State) onGoalJudge(e events.Event, t time.Time) {
	var p struct {
		Verdict string   `json:"verdict"`
		Reason  string   `json:"reason"`
		Left    []string `json:"left"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	sv := &s.sup2
	g := Goal{}
	if sv.goal != nil {
		g = *sv.goal
	}
	g.Verdict, g.VerdictReason, g.JudgedAt = clip(p.Verdict, textID), clean(p.Reason, textLong), t
	g.Left = nil
	for i, l := range p.Left {
		if i >= 8 {
			break
		}
		g.Left = append(g.Left, clean(l, textLine))
	}
	sv.goal = &g
}

// onCheckpoint is checkpoint (a checkpoint began or its file set changed: the session's checkpoint store, through the log) and
// checkpoint.restore (the files were put back as a checkpoint holds them).
func (s *State) onCheckpoint(e events.Event, t time.Time) {
	var p struct {
		ID     string   `json:"id"`
		Label  string   `json:"label"`
		Files  []string `json:"files"`
		Agents []string `json:"agents"`
		Safety bool     `json:"safety"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	id := clip(p.ID, textID)
	if id == "" {
		s.bad()
		return
	}
	r := &s.sup2.ckpts
	var cur *Checkpoint
	for i := r.len() - 1; i >= 0; i-- {
		if c := r.at(i); c.ID == id {
			cur = c
			break
		}
	}
	if e.Type == "checkpoint.restore" {
		if cur != nil {
			cur.Restored++
		}
		s.line(e.Seq, t, e.Agent, FeedSession, GlyphEpoch, "files restored to checkpoint "+id, "")
		return
	}
	if cur == nil {
		r.push(Checkpoint{ID: id, T: t})
		cur = r.at(r.len() - 1)
	}
	cur.Seq, cur.Label, cur.Files, cur.Safety = e.Seq, clean(p.Label, textLine), min(len(p.Files), smallCount), p.Safety
	cur.Agents = clipList(p.Agents, MaxFiles, textID)
}

// stallsSnapshot copies the open stall findings.
func (s *State) stallsSnapshot() []Stall { return copyOf(s.sup2.stalls) }

// goalSnapshot copies the goal, nil when the log has none.
func (s *State) goalSnapshot() *Goal {
	if s.sup2.goal == nil {
		return nil
	}
	g := *s.sup2.goal
	g.Left = copyOf(g.Left)
	return &g
}

// checkpointsSnapshot copies the checkpoints, oldest first.
func (s *State) checkpointsSnapshot() []Checkpoint {
	out := s.sup2.ckpts.slice()
	for i := range out {
		out[i].Agents = copyOf(out[i].Agents)
	}
	return out
}
