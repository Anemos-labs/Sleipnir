package traj

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// view is the non-model part of the log, parsed once per Episode call: who was
// spawned by whom, what was committed, sent, decided and concluded. Every field
// is read tolerantly (missing keys, wrong types and unknown payloads are
// ignored) because these payloads are owned by other packages and have grown over
// time; the model traffic, which training data rests on, is parsed strictly in
// run.go.
type view struct {
	spawns    []spawnEv
	ends      []endEv
	commits   []commitEv // compact.commit
	layers    []layerEv  // layer.commit
	rejects   int        // compact.reject
	mails     []mailEv
	delivers  []deliverEv
	outcomes  []outcomeEv
	denials   int // perm.decide that refused
	steers    int // user.steer
	anomalies int // cache.anomaly
	budgetHit bool
	// closures is each task's latest closure ("status:kind"; "" once reopened) and
	// handoffs the assignments that were handed over, from board.op events.
	closures map[string]string
	handoffs int
	// stuckWarnings and stuckStops count agent.stuck events of the repetition guard: its
	// warnings (phase nudge, guard repeat or not named) and the runs it ended (phase stop).
	stuckWarnings, stuckStops int

	sessionStart, sessionEnd *time.Time
	turns                    map[string][]turnEv // agent -> turn.append in order
	toolCalls                map[string][]toolCallEv
	toolResults              map[string][]toolResultEv
}

type spawnEv struct {
	seq                     uint64
	idx                     int
	ts                      time.Time
	id, role, parent, model string
	task                    string
}

type endEv struct {
	seq   uint64
	idx   int
	ts    time.Time
	id    string
	state string
}

type commitEv struct {
	seq      uint64
	idx      int
	ts       time.Time
	agent    string
	keepFrom core.TurnID
	reason   string
	fallback bool
}

type layerEv struct {
	seq   uint64
	idx   int
	agent string
	scope string
	hash  string
}

type mailEv struct {
	seq          uint64
	idx          int
	id, from, to string
	kind, text   string
}

type deliverEv struct {
	seq  uint64
	idx  int
	id   string
	to   string // the agent the event is attributed to
	from string
	ts   time.Time
}

type outcomeEv struct {
	seq      uint64
	kind     string
	pass     bool
	hasPass  bool
	score    float64
	hasScore bool
	version  string
	detail   core.Hash
	ms       int64
}

type turnEv struct {
	seq  uint64
	idx  int
	turn core.Turn
}

type toolCallEv struct {
	seq   uint64
	idx   int
	id    string
	name  string
	input json.RawMessage
}

type toolResultEv struct {
	seq       uint64
	idx       int
	id        string
	name      string
	isErr     bool
	ms        int64
	truncated bool
	handle    string
	full      core.Hash
	meta      map[string]any
}

// buildView parses the auxiliary events of a run.
func (r *Run) buildView() *view {
	v := &view{
		turns:       map[string][]turnEv{},
		toolCalls:   map[string][]toolCallEv{},
		toolResults: map[string][]toolResultEv{},
	}
	for i, e := range r.evs {
		switch e.Type {
		case events.TypeSessionStart:
			t := e.TS
			if v.sessionStart == nil {
				v.sessionStart = &t
			}
		case events.TypeSessionEnd:
			t := e.TS
			v.sessionEnd = &t
		case events.TypeAgentSpawn:
			var p struct {
				ID, Role, Parent, By, Model, Task string
			}
			if json.Unmarshal(e.Data, &p) == nil && p.ID != "" {
				parent := p.Parent
				if parent == "" {
					parent = p.By
				}
				v.spawns = append(v.spawns, spawnEv{seq: e.Seq, idx: i, ts: e.TS, id: p.ID, role: p.Role, parent: parent, model: p.Model, task: p.Task})
			}
		case events.TypeAgentEnd:
			var p struct{ ID, State string }
			if json.Unmarshal(e.Data, &p) == nil {
				if p.ID == "" {
					p.ID = e.Agent
				}
				if p.ID != "" {
					v.ends = append(v.ends, endEv{seq: e.Seq, idx: i, ts: e.TS, id: p.ID, state: p.State})
				}
			}
		case events.TypeCompactCommit:
			var p struct {
				KeepFrom core.TurnID `json:"keep_from"`
				Reason   string      `json:"reason"`
				Fallback bool        `json:"fallback"`
			}
			_ = json.Unmarshal(e.Data, &p)
			v.commits = append(v.commits, commitEv{seq: e.Seq, idx: i, ts: e.TS, agent: e.Agent, keepFrom: p.KeepFrom, reason: p.Reason, fallback: p.Fallback})
		case events.TypeLayerCommit:
			var p struct{ Scope, Hash string }
			_ = json.Unmarshal(e.Data, &p)
			v.layers = append(v.layers, layerEv{seq: e.Seq, idx: i, agent: e.Agent, scope: p.Scope, hash: p.Hash})
		case events.TypeCompactReject:
			v.rejects++
		case events.TypeMailSend:
			var p struct{ ID, From, To, Kind, Text string }
			if json.Unmarshal(e.Data, &p) == nil {
				if p.From == "" {
					p.From = e.Agent
				}
				v.mails = append(v.mails, mailEv{seq: e.Seq, idx: i, id: p.ID, from: p.From, to: p.To, kind: p.Kind, text: p.Text})
			}
		case events.TypeMailDeliver:
			var p struct{ ID, From string }
			if json.Unmarshal(e.Data, &p) == nil {
				v.delivers = append(v.delivers, deliverEv{seq: e.Seq, idx: i, id: p.ID, to: e.Agent, from: p.From, ts: e.TS})
			}
		case events.TypeOutcome:
			v.outcomes = append(v.outcomes, parseOutcome(e))
		case events.TypeBoardOp:
			v.boardClosure(e)
		case events.TypePermDecide:
			if denied(e.Data) {
				v.denials++
			}
		case events.TypeUserSteer:
			v.steers++
		case events.TypeCacheAnomaly:
			v.anomalies++
		case events.TypeAgentStuck:
			var p struct{ Phase, Guard, Note string }
			if json.Unmarshal(e.Data, &p) == nil {
				switch {
				case p.Phase == "stop":
					v.stuckStops++
				case p.Phase == "nudge" && repeatNudge(p.Guard, p.Note):
					v.stuckWarnings++
				}
			}
		case events.TypeGovernor:
			if strings.Contains(strings.ToLower(string(e.Data)), "budget") {
				v.budgetHit = true
			}
		case events.TypeTurnAppend:
			var t core.Turn
			if json.Unmarshal(e.Data, &t) == nil {
				v.turns[e.Agent] = append(v.turns[e.Agent], turnEv{seq: e.Seq, idx: i, turn: t})
			}
		case events.TypeToolCall:
			var p struct {
				ID, Name string
				Input    json.RawMessage
			}
			if json.Unmarshal(e.Data, &p) == nil {
				v.toolCalls[e.Agent] = append(v.toolCalls[e.Agent], toolCallEv{seq: e.Seq, idx: i, id: p.ID, name: p.Name, input: p.Input})
			}
		case events.TypeToolResult:
			var p struct {
				ID        string         `json:"id"`
				Name      string         `json:"name"`
				Error     bool           `json:"error"`
				Ms        int64          `json:"ms"`
				Truncated bool           `json:"truncated"`
				Handle    string         `json:"handle"`
				Full      core.Hash      `json:"full"`
				FullRef   core.Hash      `json:"full_ref"`
				Meta      map[string]any `json:"meta"`
			}
			if json.Unmarshal(e.Data, &p) == nil {
				full := p.Full
				if full == "" {
					full = p.FullRef
				}
				v.toolResults[e.Agent] = append(v.toolResults[e.Agent], toolResultEv{seq: e.Seq, idx: i, id: p.ID, name: p.Name,
					isErr: p.Error, ms: p.Ms, truncated: p.Truncated, handle: p.Handle, full: full, meta: p.Meta})
			}
		}
	}
	return v
}

// boardClosure records the closure reasons a board.op carries: the task's own
// closure (set on done and failed, absent otherwise, so a reopened task loses it)
// and an assignment closure (a handover). Events without task state are ignored.
func (v *view) boardClosure(e events.Event) {
	var p struct {
		Task    string `json:"task"`
		Status  string `json:"status"`
		Closure *struct {
			Kind string `json:"kind"`
		} `json:"closure"`
		Assignment *struct {
			Kind string `json:"kind"`
		} `json:"assignment_closure"`
	}
	if json.Unmarshal(e.Data, &p) != nil || p.Task == "" || p.Status == "" {
		return
	}
	if v.closures == nil {
		v.closures = map[string]string{}
	}
	v.closures[p.Task] = ""
	if p.Closure != nil && p.Closure.Kind != "" {
		v.closures[p.Task] = p.Status + ":" + p.Closure.Kind
	}
	if p.Assignment != nil && p.Assignment.Kind != "" {
		v.handoffs++
	}
}

// repeatNudge reports whether an agent.stuck nudge came from the repetition guard. A log
// written before nudges named their guard is read by the wording of the test-weakening
// guard's note, the only other source of nudges (h).
func repeatNudge(guard, note string) bool {
	if guard != "" {
		return guard == "repeat"
	}
	return !strings.Contains(note, "you changed only test files")
}

func parseOutcome(e events.Event) outcomeEv {
	var p struct {
		Kind    string    `json:"kind"`
		Pass    *bool     `json:"pass"`
		Score   *float64  `json:"score"`
		Version string    `json:"version"`
		VerVer  string    `json:"verifier_version"`
		Detail  core.Hash `json:"detail"`
		Ms      int64     `json:"ms"`
	}
	_ = json.Unmarshal(e.Data, &p)
	o := outcomeEv{seq: e.Seq, kind: strings.ToLower(p.Kind), version: p.Version, detail: p.Detail, ms: p.Ms}
	if o.version == "" {
		o.version = p.VerVer
	}
	if p.Pass != nil {
		o.pass, o.hasPass = *p.Pass, true
	}
	if p.Score != nil {
		o.score, o.hasScore = *p.Score, true
	}
	return o
}

// denied reports whether a perm.decide payload refused the action. The producer
// is not fixed yet, so the common spellings are accepted: allow:false,
// allowed:false, or a decision/behavior/outcome of deny/denied/reject/block.
func denied(data json.RawMessage) bool {
	var p map[string]json.RawMessage
	if json.Unmarshal(data, &p) != nil {
		return false
	}
	for _, k := range []string{"allow", "allowed"} {
		if raw, ok := p[k]; ok {
			var b bool
			if json.Unmarshal(raw, &b) == nil {
				return !b
			}
		}
	}
	for _, k := range []string{"decision", "behavior", "outcome", "result", "verdict"} {
		if raw, ok := p[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				switch strings.ToLower(s) {
				case "deny", "denied", "reject", "rejected", "block", "blocked", "refuse", "refused":
					return true
				}
				return false
			}
		}
	}
	return false
}
