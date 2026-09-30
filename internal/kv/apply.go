package kv

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
)

// ApplyPolicy bounds what a patch may do.
type ApplyPolicy struct {
	// MinKeepUnits is how many of the newest units always stay verbatim.
	MinKeepUnits int
	// MaxSpineLineChars truncates over-long digest lines.
	MaxSpineLineChars int
	// AutoMaskAfterUnits masks bulky tool results in retained units older than
	// this many units from the end (0 disables). Masking is deterministic and
	// reversible (recall), and is the cheapest compaction there is: the JetBrains
	// observation-masking result is that hiding old tool output rivals LLM
	// summarisation at a fraction of the cost.
	AutoMaskAfterUnits int
	// MaskMinTokens is the size above which a result is worth masking.
	MaskMinTokens int
	// StripThinking removes thinking blocks from retained turns. A rebase
	// voids their provider-side bindings anyway; keeping them would only bloat
	// the prompt or trigger a rejection.
	StripThinking bool
	// MaxSectionTokens / MaxNotesTokens cap the notes layer.
	MaxSectionTokens int
	MaxNotesTokens   int
	// UserInstructionKey is the notes section that receives user-authored text
	// automatically, so human instructions survive any compaction verbatim.
	UserInstructionKey       string
	UserInstructionMaxTokens int
}

// DefaultApplyPolicy is the baseline.
func DefaultApplyPolicy() ApplyPolicy {
	return ApplyPolicy{
		MinKeepUnits:             2,
		MaxSpineLineChars:        220,
		AutoMaskAfterUnits:       6,
		MaskMinTokens:            1200,
		StripThinking:            true,
		MaxSectionTokens:         1500,
		MaxNotesTokens:           6000,
		UserInstructionKey:       "instructions",
		UserInstructionMaxTokens: 600,
	}
}

// ApplyResult is the outcome of applying a patch to a snapshot. It carries new
// immutable values; nothing is mutated until the caller commits them.
type ApplyResult struct {
	Spine        *Layer
	Notes        *Layer
	NotesChanged bool
	// NotesOverBudget asks the planner to schedule a consolidation pass.
	NotesOverBudget bool
	// Replacement replaces the snapshot's turns (retained ones, masked and
	// stripped as configured).
	Replacement []core.Turn
	KeepFrom    core.TurnID

	RemovedTurns   int
	RemovedTokens  int
	RetainedTokens int
	SpineAdded     int // tokens
	MaskedResults  int
	Mechanical     int // spine lines the harness wrote itself
	Proposals      []Promotion
	Warnings       []string
}

// Apply validates p against the stack's thread snapshot and computes the
// resulting layers and turns. It is pure.
func Apply(s *Stack, p *Patch, est core.Estimator, pol ApplyPolicy) (*ApplyResult, error) {
	turns := s.Thread.Turns
	units := Units(turns)
	if len(units) == 0 {
		return nil, fmt.Errorf("nothing to compact: empty thread")
	}
	res := &ApplyResult{Warnings: append([]string(nil), p.Warnings...)}

	// Never compact an in-flight exchange, and always keep the newest units.
	protected := pol.MinKeepUnits
	if protected < 1 {
		protected = 1
	}
	maxKeep := len(units) - protected // index of first protected unit
	if maxKeep <= 0 {
		return nil, fmt.Errorf("nothing to compact: only %d units, %d protected", len(units), protected)
	}
	keepIdx := -1
	for i, u := range units {
		if u.From >= p.KeepFrom {
			keepIdx = i
			break
		}
	}
	if keepIdx < 0 || keepIdx > maxKeep {
		if keepIdx > maxKeep || keepIdx < 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("keep_from t%d clamped to keep the newest %d units", p.KeepFrom, protected))
		}
		keepIdx = maxKeep
	}
	if keepIdx == 0 {
		return nil, fmt.Errorf("nothing to compact: keep_from covers the whole thread")
	}
	res.KeepFrom = units[keepIdx].From

	// Coverage: which spine entry digests which unit.
	entries := append([]SpineEntry(nil), p.Spine...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].From < entries[j].From })
	cover := make([]int, keepIdx)
	for i := range cover {
		cover[i] = -1
	}
	for ei, e := range entries {
		claimed := false
		for ui := 0; ui < keepIdx; ui++ {
			u := units[ui]
			if u.To < e.From || u.From > e.To {
				continue
			}
			if cover[ui] != -1 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("spine entry t%d-t%d overlaps an earlier entry; ignored", e.From, e.To))
				claimed = false
				break
			}
			claimed = true
		}
		if !claimed {
			continue
		}
		for ui := 0; ui < keepIdx; ui++ {
			u := units[ui]
			if u.To >= e.From && u.From <= e.To && cover[ui] == -1 {
				cover[ui] = ei
			}
		}
	}

	// Spine lines in chronological order; uncovered runs get mechanical lines.
	var lines []string
	emitted := map[int]bool{}
	for ui := 0; ui < keepIdx; {
		if ei := cover[ui]; ei >= 0 {
			if !emitted[ei] {
				emitted[ei] = true
				// Range normalised to whole units.
				from, to := units[ui].From, units[ui].To
				for uj := ui; uj < keepIdx && cover[uj] == ei; uj++ {
					to = units[uj].To
				}
				lines = append(lines, spineLine(from, to, entries[ei].Line, pol.MaxSpineLineChars))
			}
			for ui < keepIdx && cover[ui] == ei {
				ui++
			}
			continue
		}
		uj := ui
		for uj < keepIdx && cover[uj] == -1 {
			uj++
		}
		lines = append(lines, mechanicalLine(turns, units[ui:uj]))
		res.Mechanical++
		ui = uj
	}

	// Removed accounting.
	for ui := 0; ui < keepIdx; ui++ {
		for _, tr := range turns[units[ui].Start:units[ui].End] {
			res.RemovedTokens += TurnTokens(tr, est)
			res.RemovedTurns++
		}
	}

	// Spine layer: append-only text.
	spineText := ""
	if !s.Spine.Empty() && len(s.Spine.Segments) > 0 {
		spineText = s.Spine.Segments[0].Text
	}
	addText := strings.Join(lines, "\n")
	if spineText != "" {
		spineText += "\n"
	}
	spineText += addText
	res.SpineAdded = est.Tokens(addText)
	spineID, ver := "spine:"+s.Agent, uint64(1)
	if s.Spine != nil {
		spineID, ver = s.Spine.ID, s.Spine.Version+1
	}
	res.Spine = NewLayer(spineID, KindSpine, ver, []Segment{{Text: spineText, Vol: VolFast}})

	// Notes: patch ops, then auto-preserved user instructions.
	notes, changed, over, warns := applyNotes(s, p, turns, units[:keepIdx], est, pol)
	res.Notes, res.NotesChanged, res.NotesOverBudget = notes, changed, over
	res.Warnings = append(res.Warnings, warns...)

	// Retained turns.
	res.Replacement = retain(turns, units, keepIdx, p, pol, est, res)
	for _, tr := range res.Replacement {
		res.RetainedTokens += TurnTokens(tr, est)
	}
	res.Proposals = p.Promote
	return res, nil
}

func spineLine(from, to core.TurnID, text string, max int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if max > 0 && utf8.RuneCountInString(text) > max {
		r := []rune(text)
		text = string(r[:max-1]) + "…"
	}
	if from == to {
		return fmt.Sprintf("t%d · %s", from, text)
	}
	return fmt.Sprintf("t%d-t%d · %s", from, to, text)
}

// mechanicalLine digests a run of units without a model: counts and tool names.
func mechanicalLine(turns []core.Turn, us []Unit) string {
	from, to := us[0].From, us[len(us)-1].To
	counts := map[string]int{}
	var order []string
	errs := 0
	for _, u := range us {
		for _, tr := range turns[u.Start:u.End] {
			for _, b := range tr.Blocks {
				switch b.Kind {
				case core.BlockToolUse:
					if counts[b.ToolName] == 0 {
						order = append(order, b.ToolName)
					}
					counts[b.ToolName]++
				case core.BlockToolResult:
					if b.IsError {
						errs++
					}
				}
			}
		}
	}
	if len(order) == 0 {
		return spineLine(from, to, fmt.Sprintf("%d turns of conversation (no summary written; use recall)", len(us)), 0)
	}
	parts := make([]string, 0, len(order))
	for _, n := range order {
		parts = append(parts, fmt.Sprintf("%s×%d", n, counts[n]))
	}
	msg := fmt.Sprintf("tool work: %s", strings.Join(parts, " "))
	if errs > 0 {
		msg += fmt.Sprintf(", %d errors", errs)
	}
	return spineLine(from, to, msg+" (no summary written; use recall)", 0)
}

// retain builds the replacement turns: masked, stripped, verbatim otherwise.
func retain(turns []core.Turn, units []Unit, keepIdx int, p *Patch, pol ApplyPolicy, est core.Estimator, res *ApplyResult) []core.Turn {
	masked := map[[2]int64]bool{}
	for _, m := range p.Mask {
		masked[[2]int64{int64(m.Turn), int64(m.Index)}] = true
	}
	labels := toolLabels(turns)
	lastIdx := len(turns) - 1
	var out []core.Turn
	for ui := keepIdx; ui < len(units); ui++ {
		u := units[ui]
		fromEnd := len(units) - 1 - ui
		for ti := u.Start; ti < u.End; ti++ {
			tr := turns[ti]
			nb := make([]core.Block, 0, len(tr.Blocks))
			resultIdx := 0
			for _, b := range tr.Blocks {
				switch b.Kind {
				case core.BlockThinking, core.BlockRedactedThinking:
					inFlight := ti == lastIdx && tr.Role == core.RoleAssistant && len(tr.ToolCalls()) > 0
					if pol.StripThinking && !inFlight {
						continue
					}
				case core.BlockToolResult:
					tok := BlockTokens(b, est)
					explicit := masked[[2]int64{int64(tr.ID), int64(resultIdx)}]
					auto := pol.AutoMaskAfterUnits > 0 && fromEnd >= pol.AutoMaskAfterUnits && tok >= pol.MaskMinTokens
					if (explicit || auto) && !isMasked(b) {
						b = maskBlock(b, tr.ID, resultIdx, labels[b.ToolID], tok)
						res.MaskedResults++
					}
					resultIdx++
				}
				nb = append(nb, b)
			}
			tr.Blocks = nb
			out = append(out, tr)
		}
	}
	return out
}

const maskPrefix = "⟦masked"

func isMasked(b core.Block) bool {
	return len(b.Result) == 1 && strings.HasPrefix(b.Result[0].Text, maskPrefix)
}

func maskBlock(b core.Block, turn core.TurnID, idx int, label string, tokens int) core.Block {
	if label == "" {
		label = "tool result"
	}
	text := fmt.Sprintf("%s: %s · ~%d tokens · recall t%d.%d⟧", maskPrefix, label, tokens, turn, idx)
	return core.Block{Kind: core.BlockToolResult, ToolID: b.ToolID, IsError: b.IsError, Result: []core.Block{core.Text(text)}}
}

// toolLabels maps tool_use ids to short human labels: name(first argument).
func toolLabels(turns []core.Turn) map[string]string {
	out := map[string]string{}
	for _, tr := range turns {
		for _, b := range tr.Blocks {
			if b.Kind == core.BlockToolUse {
				out[b.ToolID] = b.ToolName + "(" + firstArg(b.Input) + ")"
			}
		}
	}
	return out
}

// firstArg extracts a short representative argument from tool input JSON.
func firstArg(in json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return ""
	}
	for _, k := range []string{"path", "file_path", "command", "pattern", "url", "query", "patch"} {
		if v, ok := m[k].(string); ok && v != "" {
			v = strings.Join(strings.Fields(v), " ")
			if utf8.RuneCountInString(v) > 60 {
				r := []rune(v)
				v = string(r[:59]) + "…"
			}
			return v
		}
	}
	return ""
}

// notesDefaults gives known sections their volatility class.
var notesDefaults = map[string]Volatility{
	"assignment":   VolFrozen,
	"instructions": VolEpoch,
	"constraints":  VolSlow,
	"decisions":    VolSlow,
	"facts":        VolSlow,
	"files":        VolSlow,
	"knowledge":    VolSlow,
	"working-set":  VolFast,
	"todo":         VolFast,
	"state":        VolFast,
}

func applyNotes(s *Stack, p *Patch, turns []core.Turn, old []Unit, est core.Estimator, pol ApplyPolicy) (layer *Layer, changed, over bool, warns []string) {
	segs := map[string]*Segment{}
	var order []string
	if s.Notes != nil {
		for _, sg := range s.Notes.Segments {
			cp := sg
			segs[sg.Key] = &cp
			order = append(order, sg.Key)
		}
	}
	get := func(key string) *Segment {
		if sg, ok := segs[key]; ok {
			return sg
		}
		vol, ok := notesDefaults[key]
		if !ok {
			vol = VolSlow
		}
		sg := &Segment{Key: key, Vol: vol}
		segs[key] = sg
		order = append(order, key)
		return sg
	}
	for _, op := range p.Notes {
		key := strings.TrimSpace(strings.ToLower(op.Key))
		switch op.Op {
		case "add":
			sg := get(key)
			t := strings.TrimSpace(op.Text)
			if t == "" || strings.Contains(sg.Text, t) {
				continue
			}
			if sg.Text != "" {
				sg.Text += "\n"
			}
			sg.Text += t
			changed = true
		case "set":
			sg := get(key)
			if sg.Text != strings.TrimSpace(op.Text) {
				sg.Text = strings.TrimSpace(op.Text)
				changed = true
			}
		case "replace":
			sg := get(key)
			ls := strings.Split(sg.Text, "\n")
			hit := false
			for i, l := range ls {
				if op.Match != "" && strings.Contains(l, op.Match) {
					ls[i] = strings.TrimSpace(op.Text)
					hit, changed = true, true
					break
				}
			}
			if !hit {
				if sg.Text != "" {
					sg.Text += "\n"
				}
				sg.Text += strings.TrimSpace(op.Text)
				changed = true
				warns = append(warns, fmt.Sprintf("notes replace in %q: match not found, appended instead", key))
			} else {
				sg.Text = strings.Join(ls, "\n")
			}
		case "remove":
			sg, ok := segs[key]
			if !ok || op.Match == "" {
				continue
			}
			var keep []string
			for _, l := range strings.Split(sg.Text, "\n") {
				if strings.Contains(l, op.Match) {
					changed = true
					continue
				}
				keep = append(keep, l)
			}
			sg.Text = strings.Join(keep, "\n")
		}
	}
	// User-authored text is preserved verbatim, never left to a summariser.
	if pol.UserInstructionKey != "" {
		for _, u := range old {
			for _, tr := range turns[u.Start:u.End] {
				if tr.Origin != core.OriginUser {
					continue
				}
				txt := strings.TrimSpace(tr.PlainText())
				if txt == "" {
					continue
				}
				sg := get(pol.UserInstructionKey)
				line := "- " + capTokens(txt, pol.UserInstructionMaxTokens, est)
				if strings.Contains(sg.Text, line) {
					continue
				}
				if sg.Text != "" {
					sg.Text += "\n"
				}
				sg.Text += line
				changed = true
			}
		}
	}
	if !changed {
		return s.Notes, false, false, warns
	}
	var out []Segment
	total := 0
	for _, k := range order {
		sg := segs[k]
		if strings.TrimSpace(sg.Text) == "" {
			continue
		}
		t := est.Tokens(sg.Text)
		if t > pol.MaxSectionTokens {
			over = true
		}
		total += t
		out = append(out, *sg)
	}
	if total > pol.MaxNotesTokens {
		over = true
	}
	id, ver := "notes:"+s.Agent, uint64(1)
	if s.Notes != nil {
		id, ver = s.Notes.ID, s.Notes.Version+1
	}
	return NewLayer(id, KindNotes, ver, out), true, over, warns
}

// capTokens truncates text to roughly max tokens.
func capTokens(s string, max int, est core.Estimator) string {
	if max <= 0 || est.Tokens(s) <= max {
		return s
	}
	r := []rune(s)
	for len(r) > 8 && est.Tokens(string(r)) > max {
		r = r[:len(r)*9/10]
	}
	return string(r) + " …[truncated]"
}

// MaskOnly is the deterministic compaction: no turn is folded, the spine and
// notes are untouched, and no model is involved. Bulky tool results older than
// the newest units are replaced by recallable placeholders and stale thinking is
// stripped. It is what the planner chooses when the cache is cold (the next
// request re-prefills everything anyway, so a smaller prompt is free) and what
// keeps a hard limit from ever needing a model call to be respected.
func MaskOnly(s *Stack, est core.Estimator, pol ApplyPolicy) (*ApplyResult, error) {
	turns := s.Thread.Turns
	units := Units(turns)
	protect := pol.MinKeepUnits
	if protect < 1 {
		protect = 1
	}
	if len(units) <= protect {
		return nil, fmt.Errorf("nothing to mask: only %d units", len(units))
	}
	mp := pol
	mp.AutoMaskAfterUnits = protect
	if mp.MaskMinTokens > 600 {
		mp.MaskMinTokens /= 2
	}
	res := &ApplyResult{Spine: s.Spine, Notes: s.Notes, KeepFrom: turns[0].ID}
	before := 0
	for _, tr := range turns {
		before += TurnTokens(tr, est)
	}
	res.Replacement = retain(turns, units, 0, &Patch{}, mp, est, res)
	for _, tr := range res.Replacement {
		res.RetainedTokens += TurnTokens(tr, est)
	}
	res.RemovedTokens = before - res.RetainedTokens
	if res.MaskedResults == 0 && res.RemovedTokens <= 0 {
		return nil, fmt.Errorf("nothing worth masking")
	}
	return res, nil
}
