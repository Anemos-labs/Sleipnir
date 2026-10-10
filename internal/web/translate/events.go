package translate

import (
	"reflect"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The events of this file extend the vocabulary of package wire additively: an existing kind with optional fields (a reader that does
// not know them ignores them), or a new kind (alert, mailstat, svc). Each embeds the wire type it extends, so it encodes as that type's
// fields plus its own, and it is a wire.Event through the embedded Base.

// StateX is a state event with reqSince: the session time at which the agent's oldest unanswered main request was sent (the page
// shows "waiting for the model" from it), absent when no request is in flight.
type StateX struct {
	wire.State
	ReqSince *float64 `json:"reqSince,omitempty"`
}

// UseX is a use event with SavedPartial (some of the agent's cache reads were at a price nobody knows, so Saved is a lower bound) and
// Unpriced, the number of those cache-read tokens.
type UseX struct {
	wire.Use
	SavedPartial bool  `json:"savedPartial,omitempty"`
	Unpriced     int64 `json:"unpriced,omitempty"`
}

// CompactX is a compact event with Moment: whether the shared prefix's cache was "warm" when the planner decided on the compaction (a
// declared, priced rebase) or "cold" (the rewrite costs nothing extra), as the compact.plan before the commit said; absent for a
// compaction with no plan before it (an emergency or a mask compaction).
type CompactX struct {
	wire.Compact
	Moment string `json:"moment,omitempty"`
}

// ReqX is a req event with Mark: "epoch" when the agent took a new shared prefix before the request (layer.commit shared-sync, the
// terminal's new epoch on the hit-ratio line) or "rebase" when its thinking blocks were dropped before it (layer.commit
// thinking-strip); absent otherwise. A request answered after the mark is the one it affects.
type ReqX struct {
	wire.Req
	Mark string `json:"mark,omitempty"`
}

// TaskX is a task event with Failed (the board failed the task: it stays in the todo column with its closure), Attempts (how many
// assignments it has had) and Blocked (the board's blocked status: its owner waits).
type TaskX struct {
	wire.Task
	Failed   bool `json:"failed,omitempty"`
	Attempts int  `json:"attempts,omitempty"`
	Blocked  bool `json:"blocked,omitempty"`
}

// MailX is a mail event with Tok, the estimated size of the text in tokens, and Digest, the number of messages a mailman's digest
// stands for.
type MailX struct {
	wire.Mail
	Tok    int `json:"tok,omitempty"`
	Digest int `json:"digest,omitempty"`
}

// ToolX is a tool event with Ms, the time the call ran, and Waited, the part of it spent waiting for a person's answer to a question
// (a tool's duration excludes that wait; Ms is already without it).
type ToolX struct {
	wire.Tool
	Ms     int64 `json:"ms,omitempty"`
	Waited int64 `json:"waited,omitempty"`
}

// Alert is a new kind: one of the board's short-lived warnings raised or cleared (board.op alert, alert-clear, alert-expire). Key
// tells two alerts of one kind apart; S is raise or clear.
type Alert struct {
	wire.Base
	S    string `json:"s"`
	Kind string `json:"kind"`
	Key  string `json:"key,omitempty"`
	Text string `json:"text,omitempty"`
}

// MailStat is a new kind: the session's mail counts and the mailman's state, as absolute values.
type MailStat struct {
	wire.Base
	Sent      int    `json:"sent"`
	Delivered int    `json:"delivered"`
	Dropped   int    `json:"dropped"`
	Routed    int    `json:"routed,omitempty"`
	Digests   int    `json:"digests,omitempty"`
	Batches   int    `json:"batches,omitempty"`
	Mailman   string `json:"mailman,omitempty"`
}

// SvcUse is a new kind: the token table, cost and estimated saving of the harness's own service agents (the mailman) taken together,
// as absolute values like a use event's, which replace the last. A service agent is in no roster and has no row of its own, but what
// it asks of the model is part of what the run used and cost, so the page adds this to the totals of its agents.
type SvcUse struct {
	wire.Base
	Rd           int64   `json:"rd"`
	Un           int64   `json:"un"`
	Out          int64   `json:"out"`
	Wr           int64   `json:"wr"`
	Cost         float64 `json:"cost"`
	Saved        float64 `json:"saved"`
	SavedPartial bool    `json:"savedPartial,omitempty"`
	Unpriced     int64   `json:"unpriced,omitempty"`
}

// extKinds names the kind of each extension type.
var extKinds = map[reflect.Type]string{
	reflect.TypeOf(&StateX{}): "state", reflect.TypeOf(&UseX{}): "use", reflect.TypeOf(&TaskX{}): "task", reflect.TypeOf(&MailX{}): "mail",
	reflect.TypeOf(&ToolX{}): "tool", reflect.TypeOf(&Alert{}): "alert", reflect.TypeOf(&MailStat{}): "mailstat", reflect.TypeOf(&SvcUse{}): "svc",
	reflect.TypeOf(&CompactX{}): "compact", reflect.TypeOf(&ReqX{}): "req",
}

// kindOf names the kind of any event of the vocabulary, the extensions included ("" for an unknown type).
func kindOf(e wire.Event) string {
	if k := wire.KindOf(e); k != "" {
		return k
	}
	return extKinds[reflect.TypeOf(e)]
}

// baseIndex caches, per event type, the field index of its Base.
var baseIndex sync.Map // reflect.Type -> []int

// baseOf returns a pointer to the head of an event, of any type of the vocabulary, so that the journal can stamp t, k, seq and at.
func baseOf(e wire.Event) *wire.Base {
	v := reflect.ValueOf(e)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil
	}
	el := v.Elem()
	t := el.Type()
	idx, ok := baseIndex.Load(t)
	if !ok {
		f, found := t.FieldByName("Base")
		if !found || f.Type != reflect.TypeOf(wire.Base{}) {
			return nil
		}
		idx = f.Index
		baseIndex.Store(t, idx)
	}
	return el.FieldByIndex(idx.([]int)).Addr().Interface().(*wire.Base)
}

// class is how the hub treats an event's frame for a slow page.
type class struct {
	critical    bool
	coalescable bool
	key         string
}

// criticalKinds are never coalesced and never dropped.
var criticalKinds = map[string]bool{
	"ask": true, "answer": true, "state": true, "task": true, "goal": true, "final": true, "turn": true, "merge": true, "mail": true,
	"tool": true, "say": true, "stream": true, "sys": true, "note": true, "refuse": true, "interrupt": true, "steer": true, "break": true,
	"compact": true, "req": true, "stall": true, "handover": true, "alert": true,
}

// classOf is the class of an event of the tab: critical, coalescable with its key, or ordinary (more). firstCkpt says whether a
// checkpoint event is the first of its id.
func classOf(tab, kind string, e wire.Event, firstCkpt bool) class {
	switch {
	case criticalKinds[kind]:
		return class{critical: true}
	case kind == "ckpt":
		if firstCkpt {
			return class{critical: true}
		}
		if c, ok := e.(*wire.Ckpt); ok {
			return class{coalescable: true, key: "ckpt/" + tab + "/" + c.CID}
		}
	case kind == "use":
		return class{coalescable: true, key: "use/" + tab + "/" + agentOf(e)}
	case kind == "layers":
		return class{coalescable: true, key: "layers/" + tab + "/" + agentOf(e)}
	case kind == "diff":
		if d, ok := e.(*wire.Diff); ok {
			return class{coalescable: true, key: "diff/" + tab + "/" + d.File}
		}
	case kind == "gov", kind == "warm", kind == "plan", kind == "verdict", kind == "queue", kind == "mailstat", kind == "svc":
		return class{coalescable: true, key: kind + "/" + tab}
	}
	return class{}
}

// agentOf is the agent an event of a per-agent kind is about.
func agentOf(e wire.Event) string {
	switch v := e.(type) {
	case *wire.Use:
		return v.ID
	case *UseX:
		return v.ID
	case *wire.Layers:
		return v.ID
	}
	return ""
}
