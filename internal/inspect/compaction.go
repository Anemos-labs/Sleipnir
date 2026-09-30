package inspect

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

// compAgg accumulates compaction totals over the whole log.
type compAgg struct {
	n                              int
	commits, fork, mask, emergency int
	fallbacks, rejects, held       int
	folded, spineAdded, net        int64
	rebases, epochs                int
	list                           []*compaction
}

// newEpisode opens a compaction episode for an agent. An episode runs from the
// planner's decision to start (or the first sign of work) to the commit.
func (s *Session) newEpisode(a *agent, ts time.Time, seq uint64) *compaction {
	s.comp.n++
	ep := &compaction{startTS: ts}
	ep.N, ep.Agent, ep.Seq, ep.Start = s.comp.n, a.id, seq, ts
	ep.StartMs = ts.Sub(s.meta.first).Milliseconds()
	ep.Status, ep.Mode, ep.Patch = "running", "fork", "model"
	ep.PromptBefore = a.ctxLast
	ep.Steps = []CompStep{}
	a.ep = ep
	s.comp.list = append(s.comp.list, ep)
	if len(s.comp.list) > maxComps {
		s.comp.list = s.comp.list[len(s.comp.list)-maxComps:]
	}
	return ep
}

func (ep *compaction) step(ts time.Time, seq uint64, kind, text string) {
	if len(ep.Steps) >= 64 {
		return
	}
	ep.Steps = append(ep.Steps, CompStep{T: ts, Seq: seq, Kind: kind, Text: text})
}

func (s *Session) onCompactPlan(ev *events.Event, ts time.Time) {
	var p struct {
		Decision     string  `json:"decision"`
		Mode         string  `json:"mode"`
		Reason       string  `json:"reason"`
		Warm         *bool   `json:"warm"`
		ThreadTokens int     `json:"thread_tokens"`
		Yes          bool    `json:"yes"`
		NetITE       float64 `json:"net_ite"`
		AgeMs        int64   `json:"age_ms"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	a := s.agentFor(ev.Agent, ts)
	if a == nil {
		return
	}
	switch p.Decision {
	case "start":
		ep := s.newEpisode(a, ts, ev.Seq)
		if p.Mode == "mask" {
			ep.Mode = "mask"
			ep.Patch = "mechanical"
		}
		ep.Trigger = oneLine(p.Reason, 200)
		ep.Warm = p.Warm
		ep.ThreadTokens = p.ThreadTokens
		ep.step(ts, ev.Seq, "start", fmt.Sprintf("planner starts a %s compaction: %s%s", ep.Mode, ep.Trigger, warmText(p.Warm)))
	default: // "commit?": is now the moment to install a ready patch?
		ep := a.ep
		if ep == nil {
			ep = s.newEpisode(a, ts, ev.Seq)
			ep.Status = "ready"
		}
		ep.NetITE = p.NetITE
		if p.Warm != nil {
			ep.Warm = p.Warm
		}
		if p.ThreadTokens > 0 && ep.ThreadTokens == 0 {
			ep.ThreadTokens = p.ThreadTokens
		}
		verdict := "yes"
		if !p.Yes {
			verdict = "not yet"
			ep.Holds++
			s.comp.held++
		} else {
			ep.Reason = oneLine(p.Reason, 200)
		}
		ep.step(ts, ev.Seq, "plan", fmt.Sprintf("commit? %s: %s (net %+.0f input-token equivalents%s)", verdict, oneLine(p.Reason, 120), p.NetITE, warmText(p.Warm)))
	}
}

func warmText(w *bool) string {
	switch {
	case w == nil:
		return ""
	case *w:
		return ", warm cache"
	}
	return ", cold cache"
}

func (s *Session) onCompactPatch(ev *events.Event, ts time.Time) {
	var p struct {
		Stage          string   `json:"stage"`
		Reason         string   `json:"reason"`
		RemovedTokens  int      `json:"removed_tokens"`
		RetainedTokens int      `json:"retained_tokens"`
		SpineAdded     int      `json:"spine_added"`
		Masked         int      `json:"masked"`
		Mechanical     int      `json:"mechanical_lines"`
		Fallback       bool     `json:"fallback"`
		Warnings       []string `json:"warnings"`
		CostITE        float64  `json:"cost_ite"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	a := s.agentFor(ev.Agent, ts)
	if a == nil {
		return
	}
	ep := a.ep
	if ep == nil {
		ep = s.newEpisode(a, ts, ev.Seq)
	}
	switch p.Stage {
	case "ready":
		ep.Status = "ready"
		ep.Removed, ep.Retained, ep.Spine = p.RemovedTokens, p.RetainedTokens, p.SpineAdded
		ep.Net = p.RemovedTokens - p.SpineAdded
		ep.Masked, ep.MechLines, ep.CompactorITE = p.Masked, p.Mechanical, p.CostITE
		ep.ProposeMs = ts.Sub(ep.Start).Milliseconds()
		if p.Fallback {
			ep.Patch = "mechanical"
		}
		ep.Warnings = capStrings(p.Warnings, 8)
		ep.step(ts, ev.Seq, "ready", fmt.Sprintf("patch ready (%s): folds %s tokens, keeps %s verbatim, adds %s spine tokens",
			ep.Patch, fmtK(p.RemovedTokens), fmtK(p.RetainedTokens), fmtK(p.SpineAdded)))
	default: // "request": the compactor fork was sent
		ep.step(ts, ev.Seq, "request", "compactor fork requested: same prefix as the agent, one instruction appended")
	}
}

func (s *Session) onCompactReject(ev *events.Event, ts time.Time) {
	var p struct {
		Stage    string `json:"stage"`
		Reason   string `json:"reason"`
		Fallback string `json:"fallback"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	a := s.agentFor(ev.Agent, ts)
	if a == nil {
		return
	}
	s.comp.rejects++
	ep := a.ep
	if ep == nil {
		ep = s.newEpisode(a, ts, ev.Seq)
	}
	reason := oneLine(p.Reason, 240)
	if p.Stage == "model_patch" && p.Fallback != "" {
		// The model's patch was unusable; the harness substitutes a mechanical one
		// so a triggered compaction always yields something.
		ep.FallbackReason, ep.Patch = reason, "mechanical"
		ep.step(ts, ev.Seq, "reject", "model patch rejected ("+reason+"): falling back to a "+p.Fallback+" patch")
		return
	}
	ep.Rejects = append(ep.Rejects, strings.TrimSpace(p.Stage+": "+reason))
	if len(ep.Rejects) > 8 {
		ep.Rejects = ep.Rejects[:8]
	}
	ep.step(ts, ev.Seq, "reject", "rejected ("+firstNonEmpty(p.Stage, "commit")+"): "+reason)
	// Anything but a fallback ends the attempt.
	ep.Status = "failed"
	a.ep = nil
}

func (s *Session) onCompactCommit(ev *events.Event, ts time.Time) {
	var p struct {
		Reason          string   `json:"reason"`
		RemovedTurns    int      `json:"removed_turns"`
		RemovedTokens   int      `json:"removed_tokens"`
		RetainedTokens  int      `json:"retained_tokens"`
		SpineAdded      int      `json:"spine_added"`
		Masked          int      `json:"masked"`
		Mechanical      int      `json:"mechanical_lines"`
		NotesChanged    bool     `json:"notes_changed"`
		NotesOverBudget bool     `json:"notes_over_budget"`
		Fallback        bool     `json:"fallback"`
		Warnings        []string `json:"warnings"`
		HeldMs          int64    `json:"held_ms"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	a := s.agentFor(ev.Agent, ts)
	if a == nil {
		return
	}
	ep := a.ep
	if ep == nil {
		ep = s.newEpisode(a, ts, ev.Seq) // an emergency compaction has no plan
	}
	mode, reason := "fork", p.Reason
	switch {
	case strings.HasPrefix(reason, "mask: "):
		mode, reason = "mask", strings.TrimPrefix(reason, "mask: ")
	case strings.HasPrefix(reason, "emergency: "):
		mode, reason = "emergency", strings.TrimPrefix(reason, "emergency: ")
	}
	ep.Mode = mode
	ep.Status = "committed"
	ep.Commit, ep.CommitMs = ts, ts.Sub(s.meta.first).Milliseconds()
	if ep.Reason == "" || mode != "fork" {
		ep.Reason = oneLine(reason, 200)
	}
	if ep.Trigger == "" {
		ep.Trigger = ep.Reason
	}
	ep.Removed, ep.Retained, ep.Spine = p.RemovedTokens, p.RetainedTokens, p.SpineAdded
	ep.Net = p.RemovedTokens - p.SpineAdded
	ep.Masked, ep.MechLines = p.Masked, p.Mechanical
	ep.NotesChanged, ep.NotesOver, ep.HeldMs = p.NotesChanged, p.NotesOverBudget, p.HeldMs
	if len(p.Warnings) > 0 {
		ep.Warnings = capStrings(p.Warnings, 8)
	}
	if ep.ThreadTokens == 0 {
		ep.ThreadTokens = p.RemovedTokens + p.RetainedTokens
	}
	// Model forks yield a model patch unless the harness substituted its own; the
	// other modes are deterministic by design and never count as fallbacks.
	if mode == "fork" && !p.Fallback {
		ep.Patch = "model"
	} else {
		ep.Patch = "mechanical"
	}
	if ep.PromptBefore == 0 {
		ep.PromptBefore = a.ctxLast
	}
	ep.step(ts, ev.Seq, "commit", fmt.Sprintf("committed: %s tokens folded into %s of spine (%s verbatim kept); the next request re-prefills from the spine on",
		fmtK(p.RemovedTokens), fmtK(p.SpineAdded), fmtK(p.RetainedTokens)))

	c := &s.comp
	c.commits++
	switch mode {
	case "mask":
		c.mask++
	case "emergency":
		c.emergency++
	default:
		c.fork++
		if p.Fallback {
			c.fallbacks++
		}
	}
	c.folded += int64(p.RemovedTokens)
	c.spineAdded += int64(p.SpineAdded)
	c.net += int64(ep.Net)
	c.rebases++
	a.commits++
	a.netFolded += int64(ep.Net)
	a.foldedCum += int64(ep.Net)
	a.rebasePending = "commit"
	a.lastCommit = ep
	a.ep = nil
}
