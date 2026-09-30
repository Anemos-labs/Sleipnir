package kv

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
	// summarisation at a fraction of the cost, provided the edit is batched into a
	// commit that rewrites the cache anyway.
	AutoMaskAfterUnits int
	// MaskMinTokens is the size above which a result is worth masking.
	MaskMinTokens int
	// StripThinking removes thinking blocks from every retained turn, the carried
	// tail included. A rebase voids their provider-side bindings anyway; keeping
	// them would only bloat the prompt or trigger a rejection.
	StripThinking bool
	// MaxSectionTokens caps one compactor-written notes section and
	// MaxNotesTokens all of them together (the instructions and assignment sections
	// have their own bounds): over its cap a section is trimmed oldest line first
	// behind a marker. The compactor is asked to consolidate well before that.
	MaxSectionTokens int
	MaxNotesTokens   int
	// UserInstructionKey is the notes section that receives user-authored text
	// automatically, so human instructions survive any compaction. The harness
	// owns it: patch ops on it (and on "assignment") are ignored.
	UserInstructionKey string
	// UserInstructionMaxTokens caps one steering entry, TaskMaxTokens one
	// user-typed turn (the task itself is usually the longest thing a human
	// writes). Text beyond the cap is cut with a pointer to the archived turn.
	UserInstructionMaxTokens int
	TaskMaxTokens            int
	// MaxInstructionTokens bounds the whole instructions section; the oldest
	// entries move to the archive behind one pointer line when it overflows.
	MaxInstructionTokens int
	// MaxSpineTokens bounds the spine the same way: the oldest digests are
	// evicted behind a pointer line, never summarised again.
	MaxSpineTokens int
	// Caps describes what the target provider replays, so sizes count what is
	// sent (thinking that Render drops is free).
	Caps Caps
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
		TaskMaxTokens:            2400,
		MaxInstructionTokens:     4000,
		MaxSpineTokens:           3000,
	}
}

// WithDefaults fills what a partially specified policy left unset. A completely
// zero policy becomes DefaultApplyPolicy. Otherwise only the size bounds are
// filled, field by field: their zero value is never meaningful (an unbounded
// spine or a zero-length digest line), whereas zero for the masking and keep
// fields is a deliberate choice ("no auto-masking") and StripThinking cannot be
// told apart from unset, so those are left alone.
func (p ApplyPolicy) WithDefaults() ApplyPolicy {
	caps := p.Caps
	p.Caps = Caps{}
	if p == (ApplyPolicy{}) {
		p = DefaultApplyPolicy()
		p.Caps = caps
		return p
	}
	p.Caps = caps
	d := DefaultApplyPolicy()
	if p.MaxSpineLineChars == 0 {
		p.MaxSpineLineChars = d.MaxSpineLineChars
	}
	if p.MaskMinTokens == 0 {
		p.MaskMinTokens = d.MaskMinTokens
	}
	if p.MaxSectionTokens == 0 {
		p.MaxSectionTokens = d.MaxSectionTokens
	}
	if p.MaxNotesTokens == 0 {
		p.MaxNotesTokens = d.MaxNotesTokens
	}
	if p.UserInstructionKey == "" {
		p.UserInstructionKey = d.UserInstructionKey
	}
	if p.UserInstructionMaxTokens == 0 {
		p.UserInstructionMaxTokens = d.UserInstructionMaxTokens
	}
	if p.TaskMaxTokens == 0 {
		p.TaskMaxTokens = d.TaskMaxTokens
	}
	if p.MaxInstructionTokens == 0 {
		p.MaxInstructionTokens = d.MaxInstructionTokens
	}
	if p.MaxSpineTokens == 0 {
		p.MaxSpineTokens = d.MaxSpineTokens
	}
	return p
}

// ApplyResult is the outcome of applying a patch to a snapshot. It carries new
// immutable values; nothing is mutated until the caller commits them.
type ApplyResult struct {
	Spine        *Layer
	Notes        *Layer
	NotesChanged bool
	// NotesOverBudget is true when a notes section or the notes as a whole had to
	// be trimmed to fit its budget. The agent reports it and the next compactor
	// instruction asks for a consolidation pass.
	NotesOverBudget bool
	// Replacement replaces the snapshot's turns (retained ones, masked and
	// stripped as configured).
	Replacement []core.Turn
	KeepFrom    core.TurnID

	RemovedTurns int
	// RemovedTokens is the size, as sent, of the folded turns; RetainedTokens the
	// size of what remains after masking and stripping; SnapTokens the size of the
	// whole snapshot thread before the patch. All three count what Render sends.
	RemovedTokens  int
	RetainedTokens int
	SnapTokens     int
	SpineAdded     int // tokens
	// SpineBefore/SpineAfter are the spine layer's sizes; SpineEvicted counts the
	// digest lines moved to the archive behind the pointer; NotesBefore/NotesAfter
	// are the notes layer's sizes.
	SpineBefore, SpineAfter int
	SpineEvicted            int
	NotesBefore, NotesAfter int
	// NotesEvicted names the sections that were trimmed ("instructions: 12").
	NotesEvicted  []string
	MaskedResults int
	// MaskedTokens is what masking saved.
	MaskedTokens int
	// NoticesDropped counts stale persisted hot notices removed from the
	// retained region (all but the newest).
	NoticesDropped int
	Mechanical     int // spine lines the harness wrote itself
	Proposals      []Promotion
	Warnings       []string
}

// Apply validates p against the stack's thread snapshot and computes the
// resulting layers and turns. It is pure.
func Apply(s *Stack, p *Patch, est core.Estimator, pol ApplyPolicy) (*ApplyResult, error) {
	pol = pol.WithDefaults()
	z := Sizer{Est: est, Caps: pol.Caps}
	turns := s.Thread.Turns
	units := Units(turns)
	if len(units) == 0 {
		return nil, fmt.Errorf("%w: empty thread", ErrNothingToCompact)
	}
	res := &ApplyResult{Warnings: append([]string(nil), p.Warnings...), SnapTokens: z.Turns(turns)}

	// Never compact an in-flight exchange, and always keep the newest units.
	protected := pol.MinKeepUnits
	if protected < 1 {
		protected = 1
	}
	maxKeep := len(units) - protected // index of first protected unit
	if maxKeep <= 0 {
		return nil, fmt.Errorf("%w: only %d units, %d protected", ErrNothingToCompact, len(units), protected)
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
		return nil, fmt.Errorf("%w: keep_from covers the whole thread", ErrNothingToCompact)
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
	res.RemovedTurns = 0
	for ui := 0; ui < keepIdx; ui++ {
		for _, tr := range turns[units[ui].Start:units[ui].End] {
			res.RemovedTokens += z.Turn(tr)
			res.RemovedTurns++
		}
	}

	// Spine layer: append-only text, bounded by evicting the oldest digests
	// behind a pointer line.
	var oldLines []string
	if !s.Spine.Empty() && len(s.Spine.Segments) > 0 {
		oldLines = splitLines(s.Spine.Segments[0].Text)
	}
	res.SpineBefore = s.Spine.Tokens(est)
	res.SpineAdded = est.Tokens(strings.Join(lines, "\n"))
	all, evicted := evictOldest(append(oldLines, lines...), est, pol.MaxSpineTokens, spinePointer)
	res.SpineEvicted = evicted
	spineID, ver := "spine:"+s.Agent, uint64(1)
	if s.Spine != nil {
		spineID, ver = s.Spine.ID, s.Spine.Version+1
	}
	res.Spine = NewLayer(spineID, KindSpine, ver, []Segment{{Text: strings.Join(all, "\n"), Vol: VolFast}})
	res.SpineAfter = res.Spine.Tokens(est)

	// Notes: patch ops, then auto-preserved user instructions.
	res.NotesBefore = s.Notes.Tokens(est)
	notes, changed, over, evicts, warns := applyNotes(s, p, turns, units[:keepIdx], est, pol)
	res.Notes, res.NotesChanged, res.NotesOverBudget, res.NotesEvicted = notes, changed, over, evicts
	res.NotesAfter = res.Notes.Tokens(est)
	res.Warnings = append(res.Warnings, warns...)

	// Retained turns.
	res.Replacement = retain(turns, units, keepIdx, p, pol, est, res)
	res.RetainedTokens = z.Turns(res.Replacement)
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
//
// Everything after a commit's first changed byte is re-written by the provider
// anyway, so this is also where the thread is tidied at no extra cache cost:
// thinking is stripped (its bindings are void after a rebase), stale persisted
// hot notices are dropped (only the newest is kept), and turn-scoped system
// messages that a later user message has already cleared are removed.
func retain(turns []core.Turn, units []Unit, keepIdx int, p *Patch, pol ApplyPolicy, est core.Estimator, res *ApplyResult) []core.Turn {
	masked := map[[2]int64]bool{}
	for _, m := range p.Mask {
		masked[[2]int64{int64(m.Turn), int64(m.Index)}] = true
	}
	labels := toolLabels(turns)

	// The newest persisted notice and the last user turn are the only ones that
	// still matter.
	lastNoticeTurn, lastNoticeBlk := -1, -1
	lastUser := -1
	first := 0
	if keepIdx < len(units) {
		first = units[keepIdx].Start
	}
	for ti := first; ti < len(turns); ti++ {
		if turns[ti].Role == core.RoleUser {
			lastUser = ti
		}
		for bi, b := range turns[ti].Blocks {
			if IsNotice(b) {
				lastNoticeTurn, lastNoticeBlk = ti, bi
			}
		}
	}

	var out []core.Turn
	for ui := keepIdx; ui < len(units); ui++ {
		u := units[ui]
		fromEnd := len(units) - 1 - ui
		for ti := u.Start; ti < u.End; ti++ {
			tr := turns[ti]
			if tr.Role == core.RoleSystem && ti < lastUser {
				res.NoticesDropped++
				continue // cleared turn-scoped message: renders nothing
			}
			nb := make([]core.Block, 0, len(tr.Blocks))
			resultIdx := 0
			for bi, b := range tr.Blocks {
				switch b.Kind {
				case core.BlockThinking, core.BlockRedactedThinking:
					if pol.StripThinking {
						continue
					}
				case core.BlockText:
					if IsNotice(b) && !(ti == lastNoticeTurn && bi == lastNoticeBlk) {
						res.NoticesDropped++
						continue
					}
				case core.BlockToolResult:
					tok := BlockTokens(b, est)
					explicit := masked[[2]int64{int64(tr.ID), int64(resultIdx)}]
					auto := pol.AutoMaskAfterUnits > 0 && fromEnd >= pol.AutoMaskAfterUnits && tok >= pol.MaskMinTokens
					if (explicit || auto) && !isMasked(b) {
						mb := maskBlock(b, tr.ID, resultIdx, labels[b.ToolID], tok)
						if saved := tok - BlockTokens(mb, est); saved > 0 {
							res.MaskedTokens += saved
						}
						b = mb
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

// harnessKeys are the notes sections only the harness writes: the user's words
// and the assignment. A model-written patch (steerable by whatever the agent
// read) must not be able to replace them.
func harnessKeys(pol ApplyPolicy) map[string]bool {
	m := map[string]bool{"assignment": true}
	if k := strings.ToLower(strings.TrimSpace(pol.UserInstructionKey)); k != "" {
		m[k] = true
	}
	return m
}

func applyNotes(s *Stack, p *Patch, turns []core.Turn, old []Unit, est core.Estimator, pol ApplyPolicy) (layer *Layer, changed, over bool, evicts []string, warns []string) {
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
	owned := harnessKeys(pol)
	for _, op := range p.Notes {
		key := strings.TrimSpace(strings.ToLower(op.Key))
		if owned[key] {
			warns = append(warns, fmt.Sprintf("notes %s on %q ignored: the harness owns that section", op.Op, key))
			continue
		}
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
	// User-authored text is preserved by the harness, never left to a summariser:
	// the turns a human typed and any steering delivered with tool results or
	// mail. An entry cut for length says which archived turn holds the rest.
	if key := strings.TrimSpace(strings.ToLower(pol.UserInstructionKey)); key != "" {
		for _, u := range old {
			for _, tr := range turns[u.Start:u.End] {
				for _, e := range userEntries(tr, est, pol) {
					sg := get(key)
					if strings.Contains(sg.Text, e) {
						continue
					}
					if sg.Text != "" {
						sg.Text += "\n"
					}
					sg.Text += e
					changed = true
				}
			}
		}
	}
	if !changed {
		return s.Notes, false, false, nil, warns
	}

	// Bounds. The instructions section overflows into the archive behind a
	// pointer to the turn range. Other sections (written by the compactor, whose
	// lines carry no turn provenance) are trimmed oldest line first behind a
	// marker once they exceed their cap, and the notes as a whole are held to
	// MaxNotesTokens by trimming the largest of them: the compactor is asked to
	// consolidate well before either limit bites (see Instruction).
	instrKey := strings.ToLower(strings.TrimSpace(pol.UserInstructionKey))
	trim := func(k string, bound int) {
		sg := segs[k]
		grouped := k == instrKey && instrKey != ""
		ptr := trimPointer
		if grouped {
			ptr = instructionPointer
		}
		kept, dropped := evictOldest(splitEntries(sg.Text, grouped), est, bound, ptr)
		if dropped > 0 {
			sg.Text = strings.Join(kept, "\n")
			over = true
			evicts = append(evicts, fmt.Sprintf("%s: %d", k, dropped))
		}
	}
	for _, k := range order {
		sg := segs[k]
		switch {
		case strings.TrimSpace(sg.Text) == "" || k == "assignment":
		case k == instrKey && instrKey != "":
			if est.Tokens(sg.Text) > pol.MaxInstructionTokens {
				trim(k, pol.MaxInstructionTokens)
			}
		case est.Tokens(sg.Text) > pol.MaxSectionTokens:
			trim(k, pol.MaxSectionTokens)
			if est.Tokens(sg.Text) > pol.MaxSectionTokens {
				over = true // a single entry larger than the cap cannot be trimmed
			}
		}
	}
	// The instructions and the assignment have their own bounds; MaxNotesTokens
	// caps what the compactor writes.
	notesTotal := func() (total int, largest string) {
		big := 0
		for _, k := range order {
			sg := segs[k]
			if strings.TrimSpace(sg.Text) == "" || k == instrKey || k == "assignment" {
				continue
			}
			t := est.Tokens(sg.Text)
			total += t
			if t > big {
				big, largest = t, k
			}
		}
		return
	}
	for guard := 0; guard < 16; guard++ {
		total, largest := notesTotal()
		if total <= pol.MaxNotesTokens {
			break
		}
		over = true
		if largest == "" {
			break
		}
		before := est.Tokens(segs[largest].Text)
		want := before - (total - pol.MaxNotesTokens*85/100)
		if want < 200 {
			want = 200
		}
		bound := want * 10 / 7 // evictOldest trims down to 70% of its bound
		if bound >= before {
			bound = before - 1
		}
		trim(largest, bound)
		if est.Tokens(segs[largest].Text) >= before {
			break
		}
	}
	var out []Segment
	for _, k := range order {
		if strings.TrimSpace(segs[k].Text) == "" {
			continue
		}
		out = append(out, *segs[k])
	}
	id, ver := "notes:"+s.Agent, uint64(1)
	if s.Notes != nil {
		id, ver = s.Notes.ID, s.Notes.Version+1
	}
	return NewLayer(id, KindNotes, ver, out), true, over, evicts, warns
}

// userEntries renders the human-authored text of a folded turn as instruction
// entries: one per user-typed turn, one per steering block. Notices, model text,
// tool results and mail from other agents are never user text.
func userEntries(tr core.Turn, est core.Estimator, pol ApplyPolicy) []string {
	var out []string
	switch {
	case tr.Origin == core.OriginUser:
		if txt := strings.TrimSpace(userText(tr)); txt != "" {
			out = append(out, instructionEntry(tr.ID, txt, pol.TaskMaxTokens, est))
		}
	default:
		for _, b := range tr.Blocks {
			if IsSteer(b) {
				if txt := strings.TrimSpace(b.Text); txt != "" {
					out = append(out, instructionEntry(tr.ID, txt, pol.UserInstructionMaxTokens, est))
				}
			}
		}
	}
	return out
}

// instructionEntry formats one preserved user text. Continuation lines are
// indented so a bullet inside the text cannot be mistaken for another entry; the
// turn id lets a pointer line and recall find the original.
func instructionEntry(id core.TurnID, text string, max int, est core.Estimator) string {
	body, cut := capTokens(text, max, est)
	body = strings.ReplaceAll(body, "\n", "\n  ")
	if cut {
		return fmt.Sprintf("- %s …[truncated; full text: recall t%d]", body, id)
	}
	return fmt.Sprintf("- %s [t%d]", body, id)
}

// capTokens truncates text to roughly max tokens and reports whether it did.
func capTokens(s string, max int, est core.Estimator) (string, bool) {
	if max <= 0 || est.Tokens(s) <= max {
		return s, false
	}
	r := []rune(s)
	for len(r) > 8 && est.Tokens(string(r)) > max {
		r = r[:len(r)*9/10]
	}
	return strings.TrimRight(string(r), " \n"), true
}

// ---- bounded, append-ordered sections --------------------------------------------

var (
	spineRange   = regexp.MustCompile(`^t(\d+)(?:-t(\d+))? · `)
	turnTag      = regexp.MustCompile(`\bt(\d+)\]\s*$`)
	pointerCount = regexp.MustCompile(`\((\d+) `)
)

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// splitEntries groups a section's lines into entries. Instruction entries start
// with "- " and continue on indented lines; every other section is line based.
func splitEntries(text string, grouped bool) []string {
	lines := splitLines(text)
	if !grouped {
		return lines
	}
	var out []string
	for _, l := range lines {
		if len(out) == 0 || strings.HasPrefix(l, "- ") {
			out = append(out, l)
			continue
		}
		out[len(out)-1] += "\n" + l
	}
	return out
}

// evictOldest drops entries from the front of an append-ordered section once
// it exceeds max tokens, down to 70% of max (so it does not evict on every
// commit), and puts one pointer entry in their place. An existing pointer at the
// front is merged into the new one. The newest entry is never dropped.
func evictOldest(entries []string, est core.Estimator, max int, ptr func(old string, dropped []string) string) ([]string, int) {
	if max <= 0 || len(entries) < 2 {
		return entries, 0
	}
	total := 0
	tok := make([]int, len(entries))
	for i, e := range entries {
		tok[i] = est.Tokens(e) + 1
		total += tok[i]
	}
	if total <= max {
		return entries, 0
	}
	target := max * 7 / 10
	head, oldPtr := 0, ""
	if isPointer(entries[0]) {
		head, oldPtr = 1, entries[0]
		total -= tok[0]
	}
	keep := head
	total += 40 // room for the pointer entry
	for keep < len(entries)-1 && total > target {
		total -= tok[keep]
		keep++
	}
	dropped := entries[head:keep]
	if len(dropped) == 0 {
		return entries, 0
	}
	out := append([]string{ptr(oldPtr, dropped)}, entries[keep:]...)
	return out, len(dropped)
}

const pointerMark = "archived"

// pointerRe matches exactly the pointer entries this file writes, so a user line
// that merely looks similar is never mistaken for one and replaced.
var pointerRe = regexp.MustCompile(`^(t\d+-t\d+ · \(\d+ earlier digests archived; recall turns="t\d+-t\d+"\)|- \(\d+ older instructions archived; recall[^)]*\)|- \(\d+ older lines archived to fit the notes budget\))$`)

func isPointer(e string) bool { return pointerRe.MatchString(e) }

var recallRange = regexp.MustCompile(`recall turns="t(\d+)-t(\d+)"`)

// spinePointer replaces evicted digest lines: "t1-t120 · (57 earlier digests
// archived; recall turns="t1-t120")".
func spinePointer(old string, dropped []string) string {
	from, to, n := core.TurnID(0), core.TurnID(0), 0
	note := func(e string) {
		if m := spineRange.FindStringSubmatch(e); m != nil {
			a, _ := strconv.ParseInt(m[1], 10, 64)
			b := a
			if m[2] != "" {
				b, _ = strconv.ParseInt(m[2], 10, 64)
			}
			if from == 0 || core.TurnID(a) < from {
				from = core.TurnID(a)
			}
			if core.TurnID(b) > to {
				to = core.TurnID(b)
			}
		}
	}
	if old != "" {
		note(old)
		if m := pointerCount.FindStringSubmatch(old); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
	}
	for _, e := range dropped {
		note(e)
		n++
	}
	return fmt.Sprintf("t%d-t%d · (%d earlier digests %s; recall turns=\"t%d-t%d\")", from, to, n, pointerMark, from, to)
}

// instructionPointer replaces evicted instruction entries.
func instructionPointer(old string, dropped []string) string {
	from, to := core.TurnID(0), core.TurnID(0)
	note := func(e string) {
		for _, m := range turnTag.FindAllStringSubmatch(e, -1) {
			a, _ := strconv.ParseInt(m[1], 10, 64)
			id := core.TurnID(a)
			if from == 0 || id < from {
				from = id
			}
			if id > to {
				to = id
			}
		}
	}
	n := 0
	if old != "" {
		if m := recallRange.FindStringSubmatch(old); m != nil {
			a, _ := strconv.ParseInt(m[1], 10, 64)
			b, _ := strconv.ParseInt(m[2], 10, 64)
			from, to = core.TurnID(a), core.TurnID(b)
		}
		if m := pointerCount.FindStringSubmatch(old); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
	}
	for _, e := range dropped {
		note(e)
		n++
	}
	if from == 0 {
		return fmt.Sprintf("- (%d older instructions %s; recall the earliest turns)", n, pointerMark)
	}
	return fmt.Sprintf("- (%d older instructions %s; recall turns=\"t%d-t%d\")", n, pointerMark, from, to)
}

// trimPointer marks a trimmed compactor-written section.
func trimPointer(old string, dropped []string) string {
	n := 0
	if old != "" {
		if m := pointerCount.FindStringSubmatch(old); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
	}
	return fmt.Sprintf("- (%d older lines %s to fit the notes budget)", n+len(dropped), pointerMark)
}

// ErrNothingToCompact is returned by Apply when the patch would fold nothing: the
// thread is empty, too short, or the patch keeps all of it.
var ErrNothingToCompact = errors.New("nothing to compact")

// ErrNothingToMask is returned by MaskOnly when no tool result is worth hiding.
var ErrNothingToMask = errors.New("nothing worth masking")

// maskOnlyPolicy is the masking policy of MaskOnly: every result older than the
// protected units, at half the usual size threshold.
func maskOnlyPolicy(pol ApplyPolicy) ApplyPolicy {
	protect := pol.MinKeepUnits
	if protect < 1 {
		protect = 1
	}
	mp := pol
	mp.AutoMaskAfterUnits = protect
	if mp.MaskMinTokens > 600 {
		mp.MaskMinTokens /= 2
	}
	return mp
}

// MaskableTokens is what MaskOnly would save right now: the tokens of bulky old
// tool results that masking would replace by a placeholder. The planner needs it
// to decide whether the deterministic path has anything to do.
func MaskableTokens(s *Stack, est core.Estimator, pol ApplyPolicy) int {
	pol = pol.WithDefaults()
	turns := s.Thread.Turns
	units := Units(turns)
	protect := pol.MinKeepUnits
	if protect < 1 {
		protect = 1
	}
	if len(units) <= protect {
		return 0
	}
	res := &ApplyResult{}
	pol.StripThinking = false
	retain(turns, units, 0, &Patch{}, maskOnlyPolicy(pol), est, res)
	return res.MaskedTokens
}

// MaskOnly is the deterministic compaction: no turn is folded, the spine and
// notes are untouched, and no model is involved. Bulky tool results older than
// the newest units are replaced by recallable placeholders, and stale thinking
// and notices are dropped along the way. It is what the planner chooses when the
// cache is cold (the next request re-prefills everything anyway, so a smaller
// prompt is free). It only counts as a compaction when something was masked:
// stripping thinking alone rewrites the thread and discards the model's
// reasoning for no saving, and repeated every step it would be a commit storm.
func MaskOnly(s *Stack, est core.Estimator, pol ApplyPolicy) (*ApplyResult, error) {
	pol = pol.WithDefaults()
	z := Sizer{Est: est, Caps: pol.Caps}
	turns := s.Thread.Turns
	units := Units(turns)
	protect := pol.MinKeepUnits
	if protect < 1 {
		protect = 1
	}
	if len(units) <= protect {
		return nil, fmt.Errorf("nothing to mask: only %d units", len(units))
	}
	res := &ApplyResult{Spine: s.Spine, Notes: s.Notes, KeepFrom: turns[0].ID,
		SnapTokens: z.Turns(turns), SpineBefore: s.Spine.Tokens(est), SpineAfter: s.Spine.Tokens(est),
		NotesBefore: s.Notes.Tokens(est), NotesAfter: s.Notes.Tokens(est)}
	res.Replacement = retain(turns, units, 0, &Patch{}, maskOnlyPolicy(pol), est, res)
	res.RetainedTokens = z.Turns(res.Replacement)
	res.RemovedTokens = res.SnapTokens - res.RetainedTokens
	if res.MaskedResults == 0 {
		return nil, ErrNothingToMask
	}
	return res, nil
}
