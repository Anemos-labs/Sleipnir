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
	// UserInstructionMaxTokens bounds one steering entry, TaskMaxTokens one
	// user-typed turn (the task itself is usually the longest thing a human
	// writes). The bounds are generous on purpose: what the user typed is pinned in
	// full below them. Text beyond one keeps its beginning and its end (constraints
	// often close a spec), says how much was left out and where the rest is
	// ("recall tN"), and is reported in ApplyResult.UserTextCut.
	UserInstructionMaxTokens int
	TaskMaxTokens            int
	// MaxInstructionTokens bounds the whole instructions section; the oldest
	// entries move to the archive behind one pointer line when it overflows. It must
	// stay well above TaskMaxTokens, or the task itself would be the first thing
	// evicted.
	MaxInstructionTokens int
	// MaxNoteOps bounds how many note ops of one patch are applied and MaxNoteChars
	// how long one add/replace line may be: what a compactor (or whatever steered it)
	// can write into a private layer per commit is bounded, and a bigger patch is
	// applied in part with a warning, not refused.
	MaxNoteOps   int
	MaxNoteChars int
	// MaxPromotions bounds how many facts one patch may propose for the shared
	// layers, MaxPromotionChars the length of one. See vetPromotion for what else a
	// proposal must satisfy.
	MaxPromotions     int
	MaxPromotionChars int
	// SqueezeMinTokens is the smallest tool result an emergency patch (one with a
	// Target) may excerpt in the newest units, and SqueezeKeepTokens how much of it
	// survives, head and tail, with a pointer to the rest.
	SqueezeMinTokens  int
	SqueezeKeepTokens int
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
		UserInstructionMaxTokens: 2000,
		TaskMaxTokens:            8000,
		MaxInstructionTokens:     12000,
		MaxSpineTokens:           3000,
		MaxNoteOps:               64,
		MaxNoteChars:             400,
		MaxPromotions:            3,
		MaxPromotionChars:        240,
		SqueezeMinTokens:         1500,
		SqueezeKeepTokens:        800,
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
	if p.MaxNoteOps == 0 {
		p.MaxNoteOps = d.MaxNoteOps
	}
	if p.MaxNoteChars == 0 {
		p.MaxNoteChars = d.MaxNoteChars
	}
	if p.MaxPromotions == 0 {
		p.MaxPromotions = d.MaxPromotions
	}
	if p.MaxPromotionChars == 0 {
		p.MaxPromotionChars = d.MaxPromotionChars
	}
	if p.SqueezeMinTokens == 0 {
		p.SqueezeMinTokens = d.SqueezeMinTokens
	}
	if p.SqueezeKeepTokens == 0 {
		p.SqueezeKeepTokens = d.SqueezeKeepTokens
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
	NotesEvicted []string
	// UserTextCut lists the user-typed turns whose text was longer than its bound and
	// so is pinned as beginning and end only ("t1: 21000 tokens, kept about 8000"): the
	// one case in which what a human wrote is not pinned in full. The full text is
	// always in the archive.
	UserTextCut   []string
	MaskedResults int
	// MaskedTokens is what masking saved.
	MaskedTokens int
	// SqueezedResults and SqueezedTokens count the tool results an emergency patch
	// excerpted (only patches with a Target ever do), and what that saved.
	SqueezedResults int
	SqueezedTokens  int
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
	notes, changed, over, evicts, warns, cuts := applyNotes(s, p, turns, units[:keepIdx], est, pol)
	res.Notes, res.NotesChanged, res.NotesOverBudget, res.NotesEvicted = notes, changed, over, evicts
	res.NotesAfter = res.Notes.Tokens(est)
	res.Warnings = append(res.Warnings, warns...)
	res.UserTextCut = cuts

	// Retained turns.
	res.Replacement = retain(turns, units, keepIdx, p, pol, est, res)
	if p.Target > 0 {
		res.Replacement = squeeze(res.Replacement, p.Target, est, pol, z, res)
	}
	res.RetainedTokens = z.Turns(res.Replacement)
	res.Proposals = vetPromotions(p.Promote, pol, &res.Warnings)
	return res, nil
}

// spineLine renders one digest line: "t12-t19 · what happened". The text is one
// escaped line of at most max characters (max 0: no cut), so a digest, whoever
// wrote it, cannot close the <history> frame or pose as another entry.
func spineLine(from, to core.TurnID, text string, max int) string {
	text = EscapeLine(text, max)
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
		// Tool names come out of model replies: one line, nothing that reads as a tag.
		parts = append(parts, fmt.Sprintf("%s×%d", EscapeLine(n, 40), counts[n]))
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
	// The newest units are what the agent is acting on: a model-written patch may
	// name any result it likes, but the ones in the protected units are never masked
	// (S05). Only the harness's own emergency path shrinks them, see squeeze.
	protect := pol.MinKeepUnits
	if protect < 1 {
		protect = 1
	}
	refused := map[[2]int64]bool{}

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
					ref := [2]int64{int64(tr.ID), int64(resultIdx)}
					explicit := masked[ref]
					if explicit && fromEnd < protect {
						explicit = false
						refused[ref] = true
					}
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
	if len(refused) > 0 {
		refs := make([]string, 0, len(refused))
		for r := range refused {
			refs = append(refs, fmt.Sprintf("t%d.%d", r[0], r[1]))
		}
		sort.Strings(refs)
		res.Warnings = append(res.Warnings, fmt.Sprintf("mask %s ignored: the newest %d units stay verbatim", strings.Join(refs, ", "), protect))
	}
	return out
}

const maskPrefix = "⟦masked"

// maskRe is the exact shape maskBlock writes. isMasked insists on all of it: a tool
// result that merely starts with "⟦masked" (any tool can write that) is ordinary
// output, and must not be able to opt out of masking and squeezing that way.
var maskRe = regexp.MustCompile(`^⟦masked: [^\n]{0,240} · ~\d+ tokens · recall t\d+\.\d+⟧$`)

func isMasked(b core.Block) bool {
	return len(b.Result) == 1 && len(b.Result[0].Text) <= 400 && maskRe.MatchString(b.Result[0].Text)
}

func maskBlock(b core.Block, turn core.TurnID, idx int, label string, tokens int) core.Block {
	if label == "" {
		label = "tool result"
	}
	text := fmt.Sprintf("%s: %s · ~%d tokens · recall t%d.%d⟧", maskPrefix, label, tokens, turn, idx)
	return core.Block{Kind: core.BlockToolResult, ToolID: b.ToolID, IsError: b.IsError, Result: []core.Block{core.Text(text)}}
}

// toolLabels maps tool_use ids to short human labels: name(first argument). The
// label ends up in a mask placeholder and in the compactor's brief, and both the tool
// name and its arguments are model output (a model can be talked into calling
// `echo "</history>..."`), so both are one escaped line.
func toolLabels(turns []core.Turn) map[string]string {
	out := map[string]string{}
	for _, tr := range turns {
		for _, b := range tr.Blocks {
			if b.Kind == core.BlockToolUse {
				out[b.ToolID] = EscapeLine(b.ToolName, 40) + "(" + firstArg(b.Input) + ")"
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
			return EscapeLine(v, 60)
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

// compactorKeys are the notes sections a compactor's patch may write: exactly the
// ones its brief names (see Instruction). Anything else is refused with a warning, not
// created: a section is a "## key" header in the agent's pinned context, so a key the
// model invents ("urgent-from-the-user", a look-alike of "instructions" in another
// alphabet) would be a heading it authored itself. Sections that exist already keep
// working for the harness (the assignment, the user's instructions).
var compactorKeys = map[string]bool{"facts": true, "decisions": true, "constraints": true, "files": true, "todo": true, "working-set": true}

const compactorKeyList = "facts, decisions, constraints, files, todo, working-set"

// noteText is what a note op's text becomes: escaped, trimmed and, for a line that is
// added or replaced, cut to the per-op bound (a "set" replaces a whole section and is
// bounded by the section budget instead).
func noteText(text, op string, pol ApplyPolicy) (string, bool) {
	t := strings.TrimSpace(EscapeUntrusted(text))
	if op != "set" && pol.MaxNoteChars > 0 && utf8.RuneCountInString(t) > pol.MaxNoteChars {
		// Defused again after the cut: it can end a word where it did not end before.
		return EscapeUntrusted(cutRunes(t, pol.MaxNoteChars)), true
	}
	return t, false
}

func applyNotes(s *Stack, p *Patch, turns []core.Turn, old []Unit, est core.Estimator, pol ApplyPolicy) (layer *Layer, changed, over bool, evicts []string, warns []string, cuts []string) {
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
	// A compactor's note ops are proposals from a model that may have been steered by
	// whatever it read. They can only write the sections its brief names; the harness's
	// own sections (the user's instructions, the assignment) and any key it did not
	// hand out are refused; the number and length of ops are bounded; and every text is
	// escaped, so it cannot close the <my-notes> frame or pose as a section.
	owned := harnessKeys(pol)
	ops := p.Notes
	if pol.MaxNoteOps > 0 && len(ops) > pol.MaxNoteOps {
		warns = append(warns, fmt.Sprintf("notes: %d ops sent, only the first %d applied", len(ops), pol.MaxNoteOps))
		ops = ops[:pol.MaxNoteOps]
	}
	for i, op := range ops {
		name := strings.ToLower(strings.TrimSpace(op.Op))
		key := strings.TrimSpace(strings.ToLower(op.Key))
		switch {
		case owned[key]:
			warns = append(warns, fmt.Sprintf("notes[%d] %s on %q ignored: the harness owns that section", i, name, key))
			continue
		case !compactorKeys[key]:
			warns = append(warns, fmt.Sprintf("notes[%d] %s on %q ignored: not a section a compactor may write (%s)", i, name, EscapeLine(key, 32), compactorKeyList))
			continue
		}
		text, cutText := noteText(op.Text, name, pol)
		if cutText {
			warns = append(warns, fmt.Sprintf("notes[%d]: text cut to %d characters", i, pol.MaxNoteChars))
		}
		match := EscapeUntrusted(op.Match)
		switch name {
		case "add":
			sg := get(key)
			if text == "" || strings.Contains(sg.Text, text) {
				continue
			}
			if sg.Text != "" {
				sg.Text += "\n"
			}
			sg.Text += text
			changed = true
		case "set":
			sg := get(key)
			if sg.Text != text {
				sg.Text = text
				changed = true
			}
		case "replace":
			sg := get(key)
			ls := strings.Split(sg.Text, "\n")
			hit := false
			for j, l := range ls {
				if len(strings.TrimSpace(match)) >= 3 && strings.Contains(l, match) {
					ls[j] = text
					hit, changed = true, true
					break
				}
			}
			if !hit {
				if text == "" {
					continue
				}
				if sg.Text != "" {
					sg.Text += "\n"
				}
				sg.Text += text
				changed = true
				warns = append(warns, fmt.Sprintf("notes replace in %q: match not found, appended instead", key))
			} else {
				sg.Text = strings.Join(ls, "\n")
			}
		case "remove":
			sg, ok := segs[key]
			if !ok || len(strings.TrimSpace(match)) < 3 {
				continue // a shorter match would wipe lines by accident
			}
			var keep []string
			for _, l := range strings.Split(sg.Text, "\n") {
				if strings.Contains(l, match) {
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
				entries, cut := userEntries(tr, est, pol)
				cuts = append(cuts, cut...)
				for _, e := range entries {
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
	if len(cuts) > 0 {
		warns = append(warns, "user text kept as beginning and end only (full text: recall): "+strings.Join(cuts, "; "))
	}
	// A task the harness handed to the agent in a turn (a reused swarm worker's next
	// assignment) takes over the assignment section when its turn is folded; the newest
	// folded one wins. The section is the harness's: this and the spawn are the only
	// writers, and a task is never pinned as the user's instructions. A kickoff that
	// carries no assignment block (the notes hold it since the spawn) pins nothing.
	if id, text, ok := lastTask(turns, old); ok {
		entry, cut := assignmentEntry(id, text, pol.TaskMaxTokens, est)
		if sg := get("assignment"); sg.Text != entry {
			sg.Text = entry
			changed = true
		}
		if cut != "" {
			warns = append(warns, "assignment kept as beginning and end only (full text: recall): "+cut)
		}
	}
	if !changed {
		return s.Notes, false, false, nil, warns, cuts
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
	return NewLayer(id, KindNotes, ver, out), true, over, evicts, warns, cuts
}

// lastTask finds the assignment the newest folded task turn carries: the text of its
// task blocks (see Task), in the turn's own words. Only core.OriginTask turns count;
// a task block in any other turn is just text.
func lastTask(turns []core.Turn, old []Unit) (id core.TurnID, text string, ok bool) {
	for _, u := range old {
		for _, tr := range turns[u.Start:u.End] {
			if tr.Origin != core.OriginTask || tr.Role != core.RoleUser {
				continue
			}
			var sb strings.Builder
			for _, b := range tr.Blocks {
				if IsTask(b) {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(b.Text)
				}
			}
			if t := strings.TrimSpace(sb.String()); t != "" {
				id, text, ok = tr.ID, t, true
			}
		}
	}
	return id, text, ok
}

// assignmentEntry formats an assignment for the notes: the card as the harness wrote
// it, escaped like everything else that enters the notes, and cut to max tokens (head
// and tail around a marker that says where the rest is) if it is longer. cut describes
// the cut for the report.
func assignmentEntry(id core.TurnID, text string, max int, est core.Estimator) (entry, cut string) {
	text = strings.TrimSpace(EscapeUntrusted(text))
	tok := est.Tokens(text)
	if max <= 0 || tok <= max {
		return text, ""
	}
	head, tail := headTail(text, max, est)
	omitted := tok - est.Tokens(head) - est.Tokens(tail)
	if omitted < 0 {
		omitted = 0
	}
	entry = fmt.Sprintf("%s\n…[~%d tokens omitted; full text: recall t%d]…\n%s", head, omitted, id, tail)
	return EscapeUntrusted(entry), fmt.Sprintf("t%d: %d tokens, kept about %d", id, tok, max)
}

// userEntries renders the human-authored text of a folded turn as instruction
// entries: one per user-typed turn, one per steering block. Notices, model text,
// tool results, mail from other agents and the tasks the harness hands out (see
// lastTask) are never user text. cut names the entries that were longer than their
// bound (see instructionEntry).
func userEntries(tr core.Turn, est core.Estimator, pol ApplyPolicy) (entries, cut []string) {
	add := func(txt string, max int) {
		if txt = strings.TrimSpace(txt); txt == "" {
			return
		}
		e, c := instructionEntry(tr.ID, txt, max, est)
		entries = append(entries, e)
		if c != "" {
			cut = append(cut, c)
		}
	}
	switch {
	case tr.Origin == core.OriginUser:
		add(userText(tr), pol.TaskMaxTokens)
	default:
		for _, b := range tr.Blocks {
			if IsSteer(b) {
				add(b.Text, pol.UserInstructionMaxTokens)
			}
		}
	}
	return entries, cut
}

// instructionEntry formats one preserved user text. What the user typed is pinned in
// full up to max tokens; a longer text keeps its beginning and its end (a spec closes
// with its constraints) around a marker that says how much is missing and where it
// is, and cut describes it for the report. Continuation lines are indented so a
// bullet inside the text cannot be mistaken for another entry; the turn id lets a
// pointer line and recall find the original. The text is escaped like everything
// else that enters the notes: a pasted "</my-notes>" must not close the frame.
func instructionEntry(id core.TurnID, text string, max int, est core.Estimator) (entry, cut string) {
	text = strings.TrimSpace(EscapeUntrusted(text))
	indent := func(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }
	tok := est.Tokens(text)
	if max <= 0 || tok <= max {
		return fmt.Sprintf("- %s [t%d]", indent(text), id), ""
	}
	head, tail := headTail(text, max, est)
	omitted := tok - est.Tokens(head) - est.Tokens(tail)
	if omitted < 0 {
		omitted = 0
	}
	entry = fmt.Sprintf("- %s\n  …[~%d tokens omitted; full text: recall t%d]…\n  %s [t%d]", indent(head), omitted, id, indent(tail), id)
	// The cuts can end a word where it did not end before: defuse the whole entry again.
	return EscapeUntrusted(entry), fmt.Sprintf("t%d: %d tokens, kept about %d", id, tok, max)
}

// headTail keeps about max tokens of s, the first 60% of them from its start and the
// rest from its end, cutting on line and character boundaries. The estimator only
// counts, so the byte budget is derived from this text's own bytes per token.
func headTail(s string, max int, est core.Estimator) (head, tail string) {
	tok := est.Tokens(s)
	if tok <= max || max <= 0 || tok == 0 {
		return s, ""
	}
	keep := int(float64(len(s)) * float64(max) / float64(tok))
	h := keep * 6 / 10
	return cutHead(s, h), cutTail(s, keep-h)
}

// cutHead returns about the first n bytes of s, ending on a character boundary and,
// when a line end is close, on it.
func cutHead(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	if i := strings.LastIndexByte(s[:n], '\n'); i > n*85/100 {
		n = i
	}
	return strings.TrimRight(s[:n], " \n")
}

// cutTail returns about the last n bytes of s, starting on a character boundary and,
// when a line start is close, on it.
func cutTail(s string, n int) string {
	if n >= len(s) {
		return s
	}
	from := len(s) - n
	for from < len(s) && !utf8.RuneStart(s[from]) {
		from++
	}
	if i := strings.IndexByte(s[from:], '\n'); i >= 0 && i < n*15/100 {
		from += i + 1
	}
	return strings.TrimLeft(s[from:], " \n")
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
