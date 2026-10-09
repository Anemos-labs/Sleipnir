package swarm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ClosureKind names why a task, or one assignment of it, ended. The set is closed:
// the harness records nothing else, so a reward or a report can tell a superseded
// task from an abandoned one without reading text a model wrote.
type ClosureKind string

const (
	// CloseVerified: done after the harness's check (the verifier, or the merge
	// queue in an isolated run).
	CloseVerified ClosureKind = "verified"
	// CloseAgreed: done; a planning task's agreement was accepted.
	CloseAgreed ClosureKind = "agreed"
	// CloseBlockedOn: failed; it cannot proceed until another task does. Target: that task.
	CloseBlockedOn ClosureKind = "blocked_on"
	// CloseSuperseded: failed; another task replaces it. Target: that task.
	CloseSuperseded ClosureKind = "superseded"
	// CloseCanceled: failed; the work is no longer needed.
	CloseCanceled ClosureKind = "canceled"
	// CloseDenied: failed; the manager refused the work (wrong, unsafe, out of scope).
	CloseDenied ClosureKind = "denied"
	// CloseVerifier: failed; verification kept failing.
	CloseVerifier ClosureKind = "verifier"
	// CloseExhausted: failed by the harness; workers kept stopping until the attempt
	// limit was reached.
	CloseExhausted ClosureKind = "exhausted"
	// CloseHandedOff ends one assignment, not the task: the task moved to another
	// worker with a recap. Target: that worker.
	CloseHandedOff ClosureKind = "handed_off"
)

// closureTarget says what a kind's target names: a task, an agent, or nothing.
type closureTarget int

const (
	targetNone closureTarget = iota
	targetTask
	targetAgent
)

// closureSpec is the static description of one kind: the status it closes a task
// in ("" for an assignment closure), its target, and whether the manager may name
// it in task fail.
type closureSpec struct {
	status  TaskStatus
	target  closureTarget
	manager bool
}

var closureSpecs = map[ClosureKind]closureSpec{
	CloseVerified:   {status: StatusDone},
	CloseAgreed:     {status: StatusDone},
	CloseBlockedOn:  {status: StatusFailed, target: targetTask, manager: true},
	CloseSuperseded: {status: StatusFailed, target: targetTask, manager: true},
	CloseCanceled:   {status: StatusFailed, manager: true},
	CloseDenied:     {status: StatusFailed, manager: true},
	CloseVerifier:   {status: StatusFailed, manager: true},
	CloseExhausted:  {status: StatusFailed},
	CloseHandedOff:  {target: targetAgent},
}

// managerFailKinds lists, in a fixed order, the kinds the manager may give task fail.
var managerFailKinds = []ClosureKind{CloseBlockedOn, CloseSuperseded, CloseCanceled, CloseDenied, CloseVerifier}

// Closure is a typed closure reason: a kind and, for the kinds that need one, a
// target. Its fields are unexported so that the only ways to make one are
// NewClosure (and the JSON decoder, which calls it): a kind that needs a target
// always has one, and a kind that takes none never does. The zero value means
// "not closed".
type Closure struct {
	kind   ClosureKind
	target string
}

// NewClosure validates a kind and its target. Kinds that close on a task
// (blocked_on, superseded) need a task id; handed_off needs an agent id; every
// other kind takes no target.
func NewClosure(kind ClosureKind, target string) (Closure, error) {
	spec, ok := closureSpecs[kind]
	if !ok {
		return Closure{}, fmt.Errorf("unknown closure reason %q", cleanText(string(kind), 30))
	}
	target = strings.TrimSpace(target)
	switch spec.target {
	case targetNone:
		if target != "" {
			return Closure{}, fmt.Errorf("closure reason %s takes no target", kind)
		}
	case targetTask:
		if !taskID.MatchString(target) {
			return Closure{}, fmt.Errorf("closure reason %s needs target = the id of the task it refers to (like T3)", kind)
		}
	case targetAgent:
		if target == "" || len(target) > 40 || strings.ContainsAny(target, " \t\r\n()") {
			return Closure{}, fmt.Errorf("closure reason %s needs target = the id of the agent it refers to", kind)
		}
	}
	return Closure{kind: kind, target: target}, nil
}

// closeAs is NewClosure for the kinds that take no target, used by the harness with
// constant kinds; it panics on a kind that needs a target (a programming error).
func closeAs(kind ClosureKind) Closure {
	c, err := NewClosure(kind, "")
	if err != nil {
		panic(err)
	}
	return c
}

// Kind returns the closure's kind ("" for the zero value).
func (c Closure) Kind() ClosureKind { return c.kind }

// Target returns the task or agent the closure refers to, or "".
func (c Closure) Target() string { return c.target }

// IsZero reports whether the closure is unset (the task is not closed).
func (c Closure) IsZero() bool { return c.kind == "" }

// Status is the task status this closure ends a task in: done or failed, or "" for
// an assignment closure (handed_off) and for the zero value.
func (c Closure) Status() TaskStatus { return closureSpecs[c.kind].status }

// String renders the closure as kind or kind(target): "verified", "superseded(T5)".
func (c Closure) String() string {
	if c.target == "" {
		return string(c.kind)
	}
	return string(c.kind) + "(" + c.target + ")"
}

// closureJSON is the wire form of a Closure.
type closureJSON struct {
	Kind   ClosureKind `json:"kind"`
	Target string      `json:"target,omitempty"`
}

// MarshalJSON writes {"kind":...,"target":...}, or null for the zero value.
func (c Closure) MarshalJSON() ([]byte, error) {
	if c.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(closureJSON{Kind: c.kind, Target: c.target})
}

// UnmarshalJSON accepts null (the zero value) or the object MarshalJSON writes, and
// validates it with NewClosure, so a decoded closure is as well formed as a made one.
func (c *Closure) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*c = Closure{}
		return nil
	}
	var w closureJSON
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	if w.Kind == "" {
		*c = Closure{}
		return nil
	}
	v, err := NewClosure(w.Kind, w.Target)
	if err != nil {
		return err
	}
	*c = v
	return nil
}

// managerFailClosure parses the reason the manager gave task fail. Only the kinds a
// manager decides are accepted; the error lists them, with what each needs.
func managerFailClosure(kind, target string) (Closure, error) {
	k := ClosureKind(strings.TrimSpace(kind))
	if k == "" || !closureSpecs[k].manager {
		return Closure{}, fmt.Errorf("task fail needs reason = one of %s (blocked_on and superseded also need target = the task id they refer to), and text = a one-line explanation", managerFailKindList())
	}
	return NewClosure(k, target)
}

// managerFailKindList renders managerFailKinds for messages.
func managerFailKindList() string {
	parts := make([]string, len(managerFailKinds))
	for i, k := range managerFailKinds {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}
