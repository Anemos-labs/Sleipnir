package session

import (
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
)

// auditPermission writes the permission engine's questions and refusals to the event log (perm.ask, perm.decide). They are
// what `sleipnir friction` ranks: how often a run was asked, for what, and whether anyone could answer. The command and
// paths are clipped; the tool.call event next to them holds the full input.
func (s *Session) auditPermission(a perm.Audit) {
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
