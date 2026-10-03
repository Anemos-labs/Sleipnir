package inspect

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// What a swarm does beyond the board and the mail, read from the events the swarm and
// the workspace manager write: worktree isolation (one git tree per writer and a
// serial merge queue that verifies every integration), the mailman (worker mail
// digested in bursts) and the supervision of the manager (held to its board in a batch
// run, woken in an interactive one). Each is shown only when the log has something of it.

const maxMerges = 300

// isoState accumulates the worktree side of an isolated swarm.
type isoState struct {
	seen        bool
	trees       TreeCounts
	queue       map[string]int // merge.* events by outcome
	submissions map[string]int // task.merge by outcome
	merges      []MergeView
	integration *IntegrationView
}

// mailmanState accumulates the mailman's part of the mail.
type mailmanState struct {
	seen                                        bool
	routed, batches, parcels, digests, digested int
	directMessages, outages                     int
	direct                                      map[string]int // messages delivered directly, by reason
	state, reason                               string
}

// superviseState accumulates how the manager was held and woken.
type superviseState struct {
	seen                                   bool
	holds, unfinished, wakes, paused, caps int
	lastHold, lastWake                     string
}

// iso initializes isolation indexes on first use, marks isolation data observed, and returns
// mutable state.
func (w *swarmState) iso() *isoState {
	if w.isolation.queue == nil {
		w.isolation.queue = map[string]int{}
		w.isolation.submissions = map[string]int{}
	}
	w.isolation.seen = true
	return &w.isolation
}

// mm initializes direct-mail counts on first use, marks mailman data observed, and returns mutable
// state.
func (w *swarmState) mm() *mailmanState {
	if w.mailman.direct == nil {
		w.mailman.direct = map[string]int{}
	}
	w.mailman.seen = true
	return &w.mailman
}

// sup marks supervision data as observed and returns the swarm's supervision state for updates.
func (w *swarmState) sup() *superviseState {
	w.supervise.seen = true
	return &w.supervise
}

// onSwarmExtra ingests the events of isolation, the mailman and the manager's
// supervision. It reports whether the event was one of them.
func (s *Session) onSwarmExtra(ev *events.Event, ts time.Time) bool {
	w := &s.swarm
	switch ev.Type {
	case events.TypeWorkspaceCreate:
		w.iso().trees.Created++
	case events.TypeWorkspaceRemove:
		w.iso().trees.Removed++
	case events.TypeWorkspacePrune:
		w.iso().trees.Pruned++
	case events.TypeWorkspaceCommit:
		w.iso().trees.Commits++
	case events.TypeWorkspaceReset:
		w.iso().trees.Resets++
	case events.TypeMergeQueued, events.TypeMergeMerged, events.TypeMergeConflict, events.TypeMergeVerifyFail,
		events.TypeMergeRolledBack, events.TypeMergeRejected, events.TypeMergeFastFwd:
		w.iso().queue[strings.TrimPrefix(ev.Type, "merge.")]++
	case events.TypeTaskMerge:
		s.onTaskMerge(ev, ts)
	case events.TypeSwarmIntegration:
		s.onIntegration(ev.Data, ts)

	case events.TypeMailRoute:
		w.mm().routed++
	case events.TypeMailBatch:
		var p struct{ Parcels int }
		_ = json.Unmarshal(ev.Data, &p)
		m := w.mm()
		m.batches++
		m.parcels += p.Parcels
	case events.TypeMailDigest:
		var p struct{ Parcels []string }
		_ = json.Unmarshal(ev.Data, &p)
		m := w.mm()
		m.digests++
		m.digested += len(p.Parcels)
	case events.TypeMailDirect:
		var p struct {
			Reason string
			N      int
		}
		_ = json.Unmarshal(ev.Data, &p)
		m := w.mm()
		m.directMessages += max(p.N, 1)
		reason := oneLine(p.Reason, 60)
		if reason == "" {
			reason = "unspecified"
		}
		if len(m.direct) < 32 || m.direct[reason] > 0 {
			m.direct[reason] += max(p.N, 1)
		}
	case events.TypeMailmanState:
		var p struct{ State, Reason string }
		_ = json.Unmarshal(ev.Data, &p)
		m := w.mm()
		if p.State == "down" {
			m.outages++
		}
		m.state, m.reason = oneLine(p.State, 20), oneLine(p.Reason, 120)

	case events.TypeSwarmHold:
		var p struct{ Reason string }
		_ = json.Unmarshal(ev.Data, &p)
		x := w.sup()
		x.holds++
		x.lastHold = oneLine(p.Reason, 200)
	case events.TypeSwarmUnfinished:
		w.sup().unfinished++
	case events.TypeSwarmWake:
		var p struct{ Note string }
		_ = json.Unmarshal(ev.Data, &p)
		x := w.sup()
		x.wakes++
		x.lastWake = oneLine(p.Note, 200)
	case events.TypeSwarmWakePaused:
		w.sup().paused++
	case events.TypeSwarmWakeLimit:
		w.sup().caps++
	default:
		return false
	}
	return true
}

func (s *Session) onTaskMerge(ev *events.Event, ts time.Time) {
	var p struct {
		Task, Outcome, Commit, Reason string
		Files                         []string
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	iso := s.swarm.iso()
	outcome := oneLine(p.Outcome, 24)
	if len(iso.submissions) < 16 || iso.submissions[outcome] > 0 {
		iso.submissions[outcome]++
	}
	v := MergeView{T: ts, Agent: ev.Agent, Task: oneLine(p.Task, 24), Outcome: outcome, Commit: commitID(p.Commit), Reason: oneLine(p.Reason, 240)}
	for i, f := range p.Files {
		if i >= 30 {
			break
		}
		v.Files = append(v.Files, oneLine(f, 200))
	}
	iso.merges = append(iso.merges, v)
	if len(iso.merges) > maxMerges {
		iso.merges = iso.merges[len(iso.merges)-maxMerges:]
	}
}

// onIntegration decodes a workspace integration event and stores its sanitized latest summary.
func (s *Session) onIntegration(raw json.RawMessage, ts time.Time) {
	var p struct {
		Branch, Tip, Reason string
		Applied, Committed  bool
		Files               []string
	}
	if json.Unmarshal(raw, &p) != nil {
		s.badPay++
		return
	}
	v := &IntegrationView{T: ts, Branch: oneLine(p.Branch, 120), Tip: commitID(p.Tip), Applied: p.Applied, Committed: p.Committed, Files: len(p.Files), Reason: oneLine(p.Reason, 240)}
	s.swarm.iso().integration = v
}

// isolationLocked is the isolation section of the swarm report: nil when the log has
// no worktree or merge event and the session did not start isolated.
func (s *Session) isolationLocked() *IsolationView {
	w := &s.swarm
	if !w.isolation.seen && s.meta.isolation != "worktree" {
		return nil
	}
	iso := w.isolation
	v := &IsolationView{Trees: iso.trees, Queue: map[string]int{}, Submissions: map[string]int{}, Merges: []MergeView{}, Integration: iso.integration}
	for k, n := range iso.queue {
		v.Queue[k] = n
	}
	for k, n := range iso.submissions {
		v.Submissions[k] = n
	}
	for i := len(iso.merges) - 1; i >= 0 && len(v.Merges) < 100; i-- {
		v.Merges = append(v.Merges, iso.merges[i])
	}
	return v
}

// mailmanLocked is the mailman section: nil when the session did not start with the
// mailman on and the log has no event of it.
func (s *Session) mailmanLocked() *MailmanView {
	m := &s.swarm.mailman
	if !m.seen && !s.meta.mailman {
		return nil
	}
	v := &MailmanView{Routed: m.routed, Batches: m.batches, Parcels: m.parcels, Digests: m.digests, Digested: m.digested,
		DirectMessages: m.directMessages, Outages: m.outages, State: m.state, Reason: m.reason, Direct: map[string]int{}}
	for k, n := range m.direct {
		v.Direct[k] = n
	}
	if v.State == "" {
		v.State = "up"
	}
	return v
}

// supervisionLocked is the section on how the manager was held and woken: nil when
// neither happened.
func (s *Session) supervisionLocked() *SupervisionView {
	x := &s.swarm.supervise
	if !x.seen {
		return nil
	}
	return &SupervisionView{Holds: x.holds, Unfinished: x.unfinished, Wakes: x.wakes, WakePaused: x.paused, WakeLimits: x.caps, LastHold: x.lastHold, LastWake: x.lastWake}
}

// commitID is the first 12 characters of a commit id, kept only if it is one (hex).
func commitID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 12 {
		s = s[:12]
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return ""
		}
	}
	return s
}
