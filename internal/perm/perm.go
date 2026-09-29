// Package perm decides whether an agent may perform an action.
//
// Tools describe what they are about to do as a Request (paths touched, shell
// command, whether it writes); the Engine applies the permission mode and
// rules, and asks a human (or a delegate) only when rules do not settle it.
package perm

import (
	"context"
	"encoding/json"
)

// Mode is the coarse posture of a session or agent.
type Mode string

const (
	// ModeDefault allows reads inside the workspace and asks for the rest.
	ModeDefault Mode = "default"
	// ModeAcceptEdits also allows file edits inside the workspace.
	ModeAcceptEdits Mode = "accept-edits"
	// ModePlan is read-only: anything that writes or executes is denied.
	ModePlan Mode = "plan"
	// ModeBypass allows everything except hard denies. For sandboxes only.
	ModeBypass Mode = "bypass"
)

// Risk is a tool's own estimate of how dangerous an action is.
type Risk int

const (
	RiskLow Risk = iota
	RiskMedium
	RiskHigh
)

// Request describes one action awaiting a decision.
type Request struct {
	Agent string          `json:"agent"`
	Role  string          `json:"role,omitempty"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input,omitempty"`

	// Filled in by the tool so matching never re-parses Input.
	Summary string   `json:"summary"`           // one line for humans
	Paths   []string `json:"paths,omitempty"`   // absolute paths touched
	Command string   `json:"command,omitempty"` // shell command line
	Writes  bool     `json:"writes,omitempty"`  // mutates workspace or system
	Network bool     `json:"network,omitempty"` // reaches the network
	Risk    Risk     `json:"risk,omitempty"`
}

// Scope says how long a remembered decision lasts.
type Scope string

const (
	ScopeOnce    Scope = ""
	ScopeSession Scope = "session"
	ScopeProject Scope = "project"
)

// Decision is the outcome of a check.
type Decision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
	// Remember asks the engine to persist this answer as a rule.
	Remember Scope `json:"remember,omitempty"`
}

// Requester is what tools call before acting.
type Requester interface {
	Check(ctx context.Context, r Request) Decision
}

// Prompter asks a human (or a delegating agent) to decide.
type Prompter func(ctx context.Context, r Request) Decision

// AllowAll is a Requester that permits everything (tests, bypass sandboxes).
type AllowAll struct{}

// Check implements Requester.
func (AllowAll) Check(context.Context, Request) Decision {
	return Decision{Allow: true, Reason: "allow-all"}
}
