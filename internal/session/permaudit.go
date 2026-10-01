package session

import (
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
)

// auditPermission writes the permission engine's questions and refusals to the event log (perm.ask, perm.decide). They are
// what `sleipnir friction` ranks: how often a run was asked, for what, and whether anyone could answer. The command and
// paths are clipped; the tool.call event next to them holds the full input.
func (s *Session) auditPermission(a perm.Audit) {
	if a.Kind == "decide" && a.By == "no one" && a.Request.Command != "" {
		s.noteNoOneRefused(a.Request.Command)
	}
	if s.Log == nil {
		return
	}
	r := a.Request
	typ := events.TypePermAsk
	d := map[string]any{"tool": r.Tool, "reason": clipRunes(a.Reason, 300)}
	if a.Kind == "decide" {
		typ = events.TypePermDecide
		d["allow"] = a.Decision.Allow
		d["by"] = a.By
		if a.Decision.Remember != "" {
			d["remember"] = string(a.Decision.Remember)
		}
	}
	if r.Command != "" {
		d["command"] = clipRunes(r.Command, 400)
	}
	if len(r.Paths) > 0 {
		n := min(len(r.Paths), 5)
		paths := make([]string, n)
		for i := range paths {
			paths[i] = clipRunes(r.Paths[i], 200)
		}
		d["paths"] = paths
	}
	if r.Role != "" {
		d["role"] = r.Role
	}
	s.Log.Emit(r.Agent, typ, d)
}

// clipRunes shortens s to at most n runes, marking the cut.
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// maxRefusedKept bounds the commands remembered for the end-of-run hint.
const maxRefusedKept = 8

// noteNoOneRefused remembers a command that was refused for want of anyone to ask, once each, for the hint a run prints at its end.
func (s *Session) noteNoOneRefused(command string) {
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()
	for i := range s.refused {
		if s.refused[i].command == command {
			s.refused[i].n++
			return
		}
	}
	if len(s.refused) < maxRefusedKept {
		s.refused = append(s.refused, refusedCommand{command: command, n: 1})
	}
}

type refusedCommand struct {
	command string
	n       int
}

// RefusedWithNoOneToAsk lists the commands that were refused because the run had nobody to ask (a run, a swarm), each with how
// many times, in the order they first came. It is what a person who did not mean to refuse them needs to see at the end.
func (s *Session) RefusedWithNoOneToAsk() []RefusedCommand {
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()
	out := make([]RefusedCommand, len(s.refused))
	for i, c := range s.refused {
		out[i] = RefusedCommand{Command: c.command, Times: c.n}
	}
	return out
}

// RefusedCommand is a command refused for want of someone to ask, and how often.
type RefusedCommand struct {
	Command string
	Times   int
}
