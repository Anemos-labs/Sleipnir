package workspace

import (
	"github.com/anemos-labs/sleipnir/internal/events"
)

// Event type names emitted by the manager and the merge queue. They follow the
// dotted naming of internal/events; the session layer may register them next to
// the other type constants.
const (
	EventCreate   = "workspace.create"
	EventRemove   = "workspace.remove"
	EventPrune    = "workspace.prune"
	EventCommit   = "workspace.commit"
	EventReset    = "workspace.reset"
	EventQueued   = "merge.queued"
	EventMerged   = "merge.merged"
	EventConflict = "merge.conflict"
	// EventVerifyFailed is emitted when the verifier rejects a candidate, before
	// the roll back; EventRolledBack once the integration tip is restored.
	EventVerifyFailed = "merge.verify_failed"
	EventRolledBack   = "merge.rolled_back"
	EventRejected     = "merge.rejected"
	EventFastForward  = "merge.fast_forward"
)

// Event is one thing the workspace layer did, in a form independent of the
// session's log.
type Event struct {
	Type string
	// Agent is the agent the event is about (empty for events about the queue).
	Agent string
	Task  string
	// Data holds the payload; keys are stable and values are JSON-friendly.
	Data map[string]any
}

// EventFunc receives events. It is called synchronously from the goroutine doing
// the work, so it must be quick and must not call back into the workspace.
type EventFunc func(Event)

// EmitTo adapts an events.Emitter (the session's append-only log) to an
// EventFunc. Emission errors are dropped: losing a log line must never fail a
// merge.
func EmitTo(e events.Emitter) EventFunc {
	if e == nil {
		return nil
	}
	return func(ev Event) {
		data := ev.Data
		if data == nil {
			data = map[string]any{}
		}
		if ev.Task != "" {
			data["task"] = ev.Task
		}
		_, _ = e.Emit(ev.Agent, ev.Type, data)
	}
}

func (f EventFunc) emit(typ, agent, task string, data map[string]any) {
	if f == nil {
		return
	}
	f(Event{Type: typ, Agent: agent, Task: task, Data: data})
}
