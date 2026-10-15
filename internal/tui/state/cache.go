package state

import (
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// mark adds a marker to the agent's hit-ratio history, at the request it precedes.
func (a *agentState) mark(kind MarkKind, seq uint64, text string) int {
	at := a.hitIdx
	a.marks.push(Mark{At: at, Kind: kind, Seq: seq, Text: clean(text, textShort)})
	return at
}

// MaterialMissUSD is what a cache break that is the endpoint's own must have cost, at list price, to be said to a person who did not ask
// for every notice: on a cheap endpoint it is a fraction of a cent, nothing they can act on.
const MaterialMissUSD = 0.01

// EndpointBreaksShown is how many of the endpoint's own cache breaks are said before one line says that the rest are not.
const EndpointBreaksShown = 3

// EndpointBreaksCapped is the line said once, after EndpointBreaksShown of the endpoint's own breaks, in place of the rest.
const EndpointBreaksCapped = "this endpoint's cache keeps missing the prompt prefix; further misses are not said here"

// EndpointOnly reports whether the anomaly is the endpoint's own cache missing: a low hit while no layer of the prompt changed. The
// person cannot mend it; a drift or a layer that changed is the prompt's, and always said.
func (a Anomaly) EndpointOnly() bool { return a.Layer == "" && a.Kind == "low_hit" }

// Material reports whether the miss is known to have cost at least MaterialMissUSD.
func (a Anomaly) Material() bool { return a.MissKnown && a.MissUSD >= MaterialMissUSD }

// BreakNotices is the rule by which a feed says cache breaks, the terminal's chat and the web page alike: a break of the prompt (a
// drift, a layer that changed) is always said; the endpoint's own is said when it was material or every notice was asked for (verbose),
// and of those at most EndpointBreaksShown, then the line EndpointBreaksCapped once. The zero value is ready; it counts as it goes.
type BreakNotices struct{ endpoint int }

// Say decides one break: show says whether it is said, capped that the line EndpointBreaksCapped is said now, in its place.
func (b *BreakNotices) Say(a Anomaly, verbose bool) (show, capped bool) {
	if !a.EndpointOnly() {
		return true, false
	}
	if !verbose && !a.Material() {
		return false, false
	}
	b.endpoint++
	if b.endpoint > EndpointBreaksShown {
		return false, b.endpoint == EndpointBreaksShown+1
	}
	return true, false
}

func (s *State) onAnomaly(e events.Event, t time.Time) {
	var p struct {
		Kind         string `json:"kind"`
		Diverged     string `json:"diverged"`
		Req          string `json:"req"`
		ExpectedRead int64  `json:"expected_read"`
		ActualRead   int64  `json:"actual_read"`
		Missed       int64  `json:"missed"`
		Action       string `json:"action"`
		Error        string `json:"error"`
		NotesTokens  int64  `json:"notes_tokens"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	s.totals.Anomalies++
	an := Anomaly{Seq: e.Seq, T: t, Agent: clip(e.Agent, textID), Kind: clip(p.Kind, textID), Layer: clip(p.Diverged, textID), Req: clip(p.Req, textID),
		Expected: clampTokens(p.ExpectedRead), Actual: clampTokens(p.ActualRead), Missed: clampTokens(p.Missed),
		Note: clean(firstOf(p.Action, p.Error), textLine)}
	if an.Kind == "" {
		an.Kind = "unknown"
	}
	if an.Kind == "low_hit" && an.Missed == 0 && an.Expected > an.Actual {
		an.Missed = an.Expected - an.Actual
	}
	a := s.agent(e.Agent, t)
	if a != nil {
		an.At = a.hitIdx
		if an.Kind == "low_hit" && an.Missed > 0 {
			// The cost of the miss: the tokens that should have been read, at the price of reading less the price of paying for them.
			if m := s.priceOf(a.Stack.Model, a.Model, s.sess.Model); m != nil && m.info.Known {
				an.MissUSD, an.MissKnown = float64(an.Missed)*(m.info.Price.InputPerM-m.info.Price.CacheReadPerM)/1e6, true
			}
		}
		a.anoms.push(an)
		a.mark(MarkAnomaly, e.Seq, an.Kind+" "+an.Layer)
		a.row.markAt(t, ActAnomaly)
		a.active(t)
	}
	s.anoms.push(an)
	text := "cache: " + an.Kind
	switch an.Kind {
	case "low_hit":
		text = "cache break: expected ~" + fmtTok(an.Expected) + " read, got " + fmtTok(an.Actual)
		if an.Layer != "" {
			text += " (layer " + an.Layer + ")"
		}
	case "drift":
		text = "the prompt prefix changed without a declared rebase"
		if an.Layer != "" {
			text += " (layer " + an.Layer + ")"
		}
	case "thinking_binding", "thinking_dropped":
		text = "cache: the provider rejected or dropped a thinking block"
	case "notes_over_budget":
		text = "the notes outgrew their budget: the oldest lines were evicted"
	}
	detail := an.Note
	if an.MissKnown {
		detail = "the miss cost " + fmtUSD(an.MissUSD)
	}
	s.line(e.Seq, t, e.Agent, FeedCache, GlyphWarn, text, detail)
}

func (s *State) onCompactPlan(e events.Event, t time.Time) {
	var p struct {
		Decision string `json:"decision"`
		Warm     *bool  `json:"warm"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	a := s.agent(e.Agent, t)
	if a == nil {
		return
	}
	a.plan = planInfo{}
	if p.Warm != nil {
		a.plan = planInfo{known: true, warm: *p.Warm}
	}
	if p.Decision == "start" {
		a.Compacting = true
		a.fallback, a.fallbackOf = false, "" // a new attempt: a mechanical compaction an earlier one fell back to is not its
	}
	a.active(t)
}

func (s *State) onCompactPatch(e events.Event, t time.Time) {
	var p struct {
		Stage string `json:"stage"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	if a := s.agent(e.Agent, t); a != nil {
		a.Compacting = true
		a.active(t)
	}
}

func (s *State) onCompactReject(e events.Event, t time.Time) {
	var p struct {
		Stage    string `json:"stage"`
		Reason   string `json:"reason"`
		Fallback string `json:"fallback"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	a := s.agent(e.Agent, t)
	fallback := p.Stage == "model_patch" && p.Fallback != ""
	if a != nil {
		if !fallback {
			a.Compacting = false // the attempt is over; with a fallback the harness goes on with a mechanical patch
			a.fallback, a.fallbackOf = false, ""
		}
		a.active(t)
		if fallback {
			// Routine, and harmless: the mechanical compaction that follows says so on its own line, with this reason, rather than
			// a warning of its own (a long team run filled the manager's rail with them).
			a.fallback, a.fallbackOf = true, clean(p.Reason, textShort)
			return
		}
	}
	text := "compaction rejected"
	if fallback {
		text = "the compactor's patch was unusable: folding mechanically instead"
	} else if p.Stage != "" {
		text += " (" + clip(p.Stage, textID) + ")"
	}
	s.line(e.Seq, t, e.Agent, FeedCompact, GlyphWarn, text, clean(p.Reason, textShort))
}

func (s *State) onCompactCommit(e events.Event, t time.Time) {
	var p struct {
		Reason         string `json:"reason"`
		RemovedTurns   int64  `json:"removed_turns"`
		RemovedTokens  int64  `json:"removed_tokens"`
		RetainedTokens int64  `json:"retained_tokens"`
		SnapTokens     int64  `json:"snap_tokens"`
		SpineAdded     int64  `json:"spine_added"`
		Masked         int64  `json:"masked"`
		MaskedTokens   int64  `json:"masked_tokens"`
		Squeezed       int64  `json:"squeezed"`
		SqueezedTokens int64  `json:"squeezed_tokens"`
		Fallback       bool   `json:"fallback"`
		HeldMs         int64  `json:"held_ms"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	s.totals.Compactions++
	mode, reason := "fork", p.Reason
	switch {
	case strings.HasPrefix(reason, "mask: "):
		mode, reason = "mask", strings.TrimPrefix(reason, "mask: ")
	case strings.HasPrefix(reason, "emergency: "):
		mode, reason = "emergency", strings.TrimPrefix(reason, "emergency: ")
	}
	c := Compaction{Seq: e.Seq, T: t, Agent: clip(e.Agent, textID), Mode: mode, Reason: clean(reason, textLine),
		Removed: clampTokens(p.RemovedTokens), Retained: clampTokens(p.RetainedTokens), SpineAdded: clampTokens(p.SpineAdded),
		RemovedTurn: clampTokens(p.RemovedTurns), Masked: clampTokens(p.Masked), MaskedTokens: clampTokens(p.MaskedTokens),
		Squeezed: clampTokens(p.Squeezed), SqueezedTokens: clampTokens(p.SqueezedTokens),
		HeldMs: max(p.HeldMs, 0), Fallback: p.Fallback, Moment: "unknown"}
	c.Before = clampTokens(p.SnapTokens)
	if c.Before == 0 {
		c.Before = min(c.Removed+c.Retained, smallCount)
	}
	c.After = min(c.SpineAdded+c.Retained, smallCount)
	a := s.agent(e.Agent, t)
	if a != nil {
		// An emergency compaction is the harness's safety net, taken in the agent's own loop with no plan and no choice of moment; a
		// plan that precedes it belongs to a model compaction that is still being worked out, and is left for that one.
		if mode != "emergency" {
			if a.plan.known {
				c.Moment = "warm"
				if !a.plan.warm {
					c.Moment = "cold"
				}
			}
			a.plan = planInfo{}
			a.Compacting = false
		}
		a.Compactions++
		c.At = a.mark(MarkCompaction, e.Seq, fmtTok(c.Before)+" → "+fmtTok(c.After))
		a.compacts.push(c)
		a.row.markAt(t, ActCompact)
		a.active(t)
	}
	text := "compacted " + fmtTok(c.Before) + " → " + fmtTok(c.After)
	detail := c.Mode + " compaction"
	switch c.Moment {
	case "cold":
		detail = "at a cold moment: the rewrite costs nothing extra"
	case "warm":
		detail = "at a warm moment: a declared, priced rebase"
	}
	if a != nil && a.fallback {
		if mode == "fork" { // the compaction a refused model patch fell back to (onCompactReject); a mask or an emergency one is not
			text = "compacted mechanically " + fmtTok(c.Before) + " → " + fmtTok(c.After)
			detail = "the compactor's patch was unusable"
			if a.fallbackOf != "" {
				detail += ": " + a.fallbackOf
			}
		}
		a.fallback, a.fallbackOf = false, ""
	}
	s.line(e.Seq, t, e.Agent, FeedCompact, GlyphCompact, text, detail)
}

func (s *State) onLayerCommit(e events.Event, t time.Time) {
	var p struct {
		Scope  string `json:"scope"`
		Reason string `json:"reason"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	scope := clip(p.Scope, textID)
	desc := clean(scope+": "+p.Reason, textShort)
	if scope == "shared-epoch" {
		s.sess.Epochs++
		s.line(e.Seq, t, e.Agent, FeedEpoch, GlyphEpoch, "the shared prefix was re-written: a new epoch", clean(p.Reason, textShort))
		return
	}
	a := s.agent(e.Agent, t)
	if a == nil {
		return
	}
	a.Stack.LastCommit = desc
	switch scope {
	case "shared-sync":
		a.Stack.Epochs++
		a.mark(MarkEpoch, e.Seq, p.Reason)
	case "thinking-strip":
		a.mark(MarkRebase, e.Seq, p.Reason)
	}
	a.active(t)
}
