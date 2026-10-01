package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/input"
	"github.com/reee344/sleipnir/internal/tui/state"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

// The live region: the few rows under the scrollback that are drawn again at every frame. From top to bottom, when they apply:
//
//	● the words that are still arriving (markdown that is not final yet)
//	◆ compacting 31.2k ▓▓▓▓▓▓▓ → 12.4k          a thread being folded
//	⠙ Bash go test ./...  3.2s                   a tool that is running
//	⠹ Thinking…  14s  ↑48.3k ↓2.1k  $0.0021  saved ≈ $0.31  esc to interrupt
//	prompt 48.3k ████████▓░░░░▏░░  93% cached  ◕ warm 4:12 ▰▰▰▰▱▱▱▱
//	            G0      G1    G2  G3  G4       G5
//	cache ▁▃▇█▇▇█  hit ratio per request · 7 requests
//	╭────────────────────────────────────────╮
//	│ ❯ the prompt, and what is typed ahead  │
//	╰────────────────────────────────────────╯
//	  completion menu, the Esc hint
//	⏎ queued: "and run the race detector"
//	default · ctrl+t stack · / commands                    heimdall/model · session
//
// It is made by one function of a liveView, so that a golden file can hold it at 60, 80 and 120 columns.

// statusKind is what the status line says the agent is doing.
type statusKind uint8

const (
	statusNone       statusKind = iota // nothing: no status line
	statusStarting                     // the session is being made
	statusThinking                     // a model request is in flight
	statusTool                         // a tool runs
	statusAsking                       // a question waits for the person
	statusWaiting                      // a swarm's manager waits for its team
	statusCompacting                   // a compaction is being worked out
	statusStuck                        // the agent repeats one failing call
	statusCommand                      // a slash command runs
)

// statusView is the status line's data.
type statusView struct {
	kind    statusKind
	detail  string // "Bash", "/compact": what the kind does not say
	seed    int    // picks the verbs of the turn
	elapsed time.Duration
	tokIn   int64
	tokOut  int64
	cost    float64
	saved   float64
	flash   bool // a cache break: the line is in the alarm style for a moment
}

// toolView is a tool that is running.
type toolView struct {
	agent   string
	title   string
	summary string
	elapsed time.Duration
	asking  bool
	worker  bool
}

// foldView is a compaction that is being folded.
type foldView struct {
	who           string
	before, after int
	progress      float64
}

// dialogView is a question that is on the screen.
type dialogView struct {
	title   string
	body    []cell.Line // laid out for BodyWidth(cols)
	options []widget.DialogOption
	sel     int
	armed   bool
}

// liveView is everything the live region is made of, at one moment.
type liveView struct {
	cols, rows int
	frame      int

	status statusView
	tail   []cell.Line
	tools  []toolView
	fold   *foldView

	snap  *state.Snapshot
	agent string // whose prompt the stack bar shows
	mem   *Memory

	ed        input.View
	editorOn  bool // the editor takes keys (false: nothing can be typed)
	queue     []string
	dlg       *dialogView
	mode      string
	model     string
	session   string
	hint      string // a line of the program's own, in place of the keys (the mode was changed)
	inputBlur bool   // a dialog has the keys: the prompt is shown dim
}

// liveOut is the live region and where the cursor rests in it (row < 0: nowhere).
type liveOut struct {
	lines     []cell.Line
	curRow    int
	curCol    int
	boxHeight int
}

// The positions of the parts of the live region, top to bottom, and their priorities (the lower, the sooner it is kept when the
// terminal is too short for all of them).
const (
	posTail = iota
	posFold
	posTools
	posStatus
	posStackBar
	posStackLabels
	posSparkMarks
	posSparkBars
	posInput
	posBelow
	posInputUnder
	posQueue
	posFooter
)

type livePart struct {
	pos   int
	prio  int
	lines []cell.Line
	// trim, when set, lets the part give up rows: it returns the part cut to n rows.
	trim func(n int) []cell.Line
}

// liveLimit is how tall the live region may be on a terminal of rows lines: two thirds of it, so that the scrollback stays in view,
// and never the whole.
func liveLimit(rows int) int {
	return max(min(rows*2/3, rows-1), min(rows-1, 8))
}

// liveLines lays the live region out.
func (k *chatLook) liveLines(v *liveView) liveOut {
	w := max(v.cols, 1)
	limit := liveLimit(v.rows)
	var parts []livePart

	boxRow := 0
	var box []cell.Line
	var curRow, curCol int
	if v.dlg != nil {
		// A question has the keys (or is about to: see dialogView.armed). The prompt stays under it, dim, where there is room, so
		// that what is typed while the question waits can be seen going to the prompt.
		box, curRow = k.dialogLines(v.dlg, w), -1
		parts = append(parts, livePart{pos: posBelow, prio: 0, lines: []cell.Line{fit(k.dialogHint(v.dlg), w)}})
		dim := *v
		dim.inputBlur = true
		under, _, _ := k.inputBox(&dim, w)
		parts = append(parts, livePart{pos: posInputUnder, prio: 2, lines: under})
	} else {
		box, curRow, curCol = k.inputBox(v, w)
		if below := v.ed.Lines[min(v.ed.InputRows, len(v.ed.Lines)):]; len(below) > 0 {
			parts = append(parts, livePart{pos: posBelow, prio: 0, lines: fitAll(below, w)})
		}
	}
	parts = append(parts, livePart{pos: posInput, prio: 0, lines: box})
	parts = append(parts, livePart{pos: posFooter, prio: 0, lines: []cell.Line{k.footer(v, w)}})
	if q := k.queueLine(v, w); q != nil {
		parts = append(parts, livePart{pos: posQueue, prio: 2, lines: []cell.Line{q}})
	}
	if v.status.kind != statusNone {
		parts = append(parts, livePart{pos: posStatus, prio: 1, lines: []cell.Line{k.statusLine(v, w)}})
	}
	if a := stackAgent(v); a != nil && v.dlg == nil {
		if bar, labels := k.stackRows(v, a, w); bar != nil {
			parts = append(parts, livePart{pos: posStackBar, prio: 3, lines: []cell.Line{bar}})
			if labels != nil {
				parts = append(parts, livePart{pos: posStackLabels, prio: 6, lines: []cell.Line{labels}})
			}
		}
		if marks, bars := k.sparkRows(v, a, w); bars != nil {
			parts = append(parts, livePart{pos: posSparkMarks, prio: 5, lines: []cell.Line{marks}})
			parts = append(parts, livePart{pos: posSparkBars, prio: 4, lines: []cell.Line{bars}})
		}
	}
	if len(v.tools) > 0 {
		all := k.toolRows(v, w)
		parts = append(parts, livePart{pos: posTools, prio: 7, lines: all, trim: func(n int) []cell.Line { return cutRows(all, n, k.st.dim) }})
	}
	if v.fold != nil {
		parts = append(parts, livePart{pos: posFold, prio: 8, lines: []cell.Line{k.foldLine(v, w)}})
	}
	if len(v.tail) > 0 {
		tail := v.tail
		parts = append(parts, livePart{pos: posTail, prio: 9, lines: tail, trim: func(n int) []cell.Line { return tail[len(tail)-n:] }})
	}

	// Keep the parts by priority while they fit; a part that can give up rows does, down to one.
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].prio < parts[j].prio })
	used := 0
	var kept []livePart
	for _, p := range parts {
		n := len(p.lines)
		switch {
		case p.prio == 0 || used+n <= limit:
		case p.trim != nil && limit-used >= 1:
			p.lines = p.trim(limit - used)
			n = len(p.lines)
		default:
			continue
		}
		used += n
		kept = append(kept, p)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].pos < kept[j].pos })
	var out liveOut
	for _, p := range kept {
		if p.pos == posInput {
			boxRow = len(out.lines)
			out.boxHeight = len(p.lines)
		}
		out.lines = append(out.lines, p.lines...)
	}
	out.curRow, out.curCol = -1, 0
	if curRow >= 0 {
		out.curRow, out.curCol = boxRow+curRow, curCol
	}
	return out
}

func fitAll(ls []cell.Line, w int) []cell.Line {
	out := make([]cell.Line, len(ls))
	for i, l := range ls {
		out[i] = fit(l, w)
	}
	return out
}

// cutRows keeps the first n-1 rows and says how many were left out in the last.
func cutRows(ls []cell.Line, n int, st cell.Style) []cell.Line {
	if n >= len(ls) {
		return ls
	}
	if n <= 1 {
		return ls[:max(n, 0)]
	}
	out := append([]cell.Line(nil), ls[:n-1]...)
	return append(out, cell.Styled(st, fmt.Sprintf("+%d more", len(ls)-(n-1))))
}

// ---- the input ----

// inputBox is the prompt in its box. The cursor is where the editor says, moved by the border and the padding; the rows of the
// view that are not the input itself (the search line, the completion menu, the Esc hint) are not in the box (liveLines puts them
// under it), so a cursor that is on one of them is under the box.
func (k *chatLook) inputBox(v *liveView, w int) (lines []cell.Line, curRow, curCol int) {
	n := min(v.ed.InputRows, len(v.ed.Lines))
	body := v.ed.Lines[:n]
	if v.inputBlur {
		body = dimLines(body)
	}
	inner := widget.BoxInnerWidth(w)
	if inner < 10 { // no room for a border: the prompt alone
		return fitAll(body, w), v.ed.CursorRow, min(v.ed.CursorCol, w-1)
	}
	lines = widget.Box("", body, w, k.Theme, widget.BoxPadding(1, 0))
	if v.ed.CursorRow < n {
		return lines, 1 + v.ed.CursorRow, 2 + v.ed.CursorCol
	}
	return lines, 1 + n + 1 + (v.ed.CursorRow - n), v.ed.CursorCol
}

// dimLines draws lines dimmed, whatever else their spans say.
func dimLines(ls []cell.Line) []cell.Line {
	out := make([]cell.Line, len(ls))
	for i, l := range ls {
		o := make(cell.Line, len(l))
		for j, sp := range l {
			o[j] = cell.Span{Text: sp.Text, Style: sp.Style.With(cell.Dim)}
		}
		out[i] = o
	}
	return out
}

// queueLine shows the first line typed ahead, which is sent when the turn ends.
func (k *chatLook) queueLine(v *liveView, w int) cell.Line {
	if len(v.queue) == 0 {
		return nil
	}
	first := firstLine(v.queue[0], max(w-16, 8), k.g.ellipsis)
	var r row
	r.add(k.st.dim, k.g.queued+" queued: ").add(cell.Style{Attr: cell.Italic}, "“"+first+"”")
	if n := len(v.queue) - 1; n > 0 {
		r.add(k.st.dim, fmt.Sprintf(" (+%d more)", n))
	}
	return fit(r.line(), w)
}

// ---- the footer ----

// footer is the mode, the keys that are not obvious, and on the right the model and the session. When the width is short the keys
// give way first, then the session, then the model.
func (k *chatLook) footer(v *liveView, w int) cell.Line {
	hints := []string{
		k.g.dot + " ctrl+t stack " + k.g.dot + " ctrl+o expand " + k.g.dot + " / commands",
		k.g.dot + " ctrl+t stack " + k.g.dot + " / commands",
		"",
	}
	if v.hint != "" {
		hints = []string{k.g.dot + " " + clean(v.hint)}
	}
	model, session := clean(v.model), clean(v.session)
	rights := []string{}
	if model != "" && session != "" {
		rights = append(rights, model+" "+k.g.dot+" "+session)
	}
	if model != "" {
		rights = append(rights, model)
	}
	rights = append(rights, "")
	build := func(hint, right string) (cell.Line, int) {
		var left row
		left.add(k.modeStyle(v.mode), clean(v.mode))
		if hint != "" {
			left.add(k.st.dim, " "+hint)
		}
		return left.line(), left.w + 2 + cell.StringWidth(right)
	}
	// the keys give way before the session does
	for _, right := range rights {
		for _, hint := range hints {
			if l, n := build(hint, right); n <= w || (right == "" && hint == "") {
				return alignRight(l, cell.Styled(k.st.dim, right), w)
			}
		}
	}
	l, _ := build("", "")
	return fit(l, w)
}

func (k *chatLook) modeStyle(mode string) cell.Style {
	switch mode {
	case "accept-edits":
		return k.st.good
	case "plan":
		return k.st.info
	case "bypass":
		return k.st.bad
	}
	return k.st.dim
}

// ---- the status line ----

// statusLine is what the agent is doing, for how long, what it has used and what the cache has saved, and how to stop it.
func (k *chatLook) statusLine(v *liveView, w int) cell.Line {
	s := v.status
	glyph, st := k.g.still, k.st.accent
	if k.Anim {
		glyph = k.g.spinner(v.frame)
	}
	var word string
	switch s.kind {
	case statusStarting:
		word = "Starting"
	case statusThinking:
		word = widget.Verb(s.seed, v.frame)
	case statusTool:
		word = "Running"
		if s.detail != "" {
			word += " " + s.detail
		}
	case statusAsking:
		glyph, st = k.g.ask, k.st.warn
		word = "Waiting for your answer"
	case statusWaiting:
		word = "Waiting for the team"
	case statusCompacting:
		word = "Compacting"
	case statusStuck:
		glyph, st = k.g.warn, k.st.bad
		word = "Stuck on a failing call"
	case statusCommand:
		word = "Running"
		if s.detail != "" {
			word += " " + s.detail
		}
	}
	if s.flash {
		st = k.st.bad
	}
	word += k.g.ellipsis

	type seg struct {
		text string
		st   cell.Style
		rank int // 0 never goes; the higher, the sooner it is given up
	}
	segs := []seg{{glyph + " " + word, st.Without(cell.Bold), 0}}
	if s.elapsed > 0 {
		segs = append(segs, seg{duration(s.elapsed.Round(time.Second)), k.st.dim, 1})
	}
	if s.tokIn > 0 || s.tokOut > 0 {
		segs = append(segs, seg{fmt.Sprintf("%s%s %s%s", k.g.up, widget.Tokens(int(s.tokIn)), k.g.down, widget.Tokens(int(s.tokOut))), k.st.dim, 4})
	}
	if s.cost > 0 {
		segs = append(segs, seg{widget.USD(s.cost), cell.Style{}, 2})
	}
	savedText, savedShort := "", ""
	if s.saved > 0 {
		savedShort = fmt.Sprintf("saved %s %s", k.g.approx, widget.USD(s.saved))
		savedText = savedShort + " at list price"
		segs = append(segs, seg{savedText, k.st.good, 3})
	}
	hint := "esc to interrupt"
	segs = append(segs, seg{hint, k.st.dim, 5})

	build := func() (left row, right cell.Line) {
		for _, sg := range segs {
			if sg.text == hint {
				right = cell.Styled(sg.st, sg.text)
				continue
			}
			if left.w > 0 {
				left.add(cell.Style{}, "  ")
			}
			left.add(sg.st, sg.text)
		}
		return left, right
	}
	fits := func() bool {
		l, r := build()
		return l.w+2+r.Width() <= w || (r.Width() == 0 && l.w <= w)
	}
	if !fits() { // "at list price" is the first to go: the ≈ and the word saved say what it is
		for i := range segs {
			if segs[i].text == savedText && savedShort != "" {
				segs[i].text = savedShort
			}
		}
	}
	for rank := 5; rank >= 1 && !fits(); rank-- {
		kept := segs[:0:0]
		for _, sg := range segs {
			if sg.rank != rank {
				kept = append(kept, sg)
			}
		}
		segs = kept
	}
	l, r := build()
	return alignRight(l.line(), r, w)
}

// ---- running tools and the fold ----

// maxToolRows is how many running tools the live region lists.
const maxToolRows = 4

// toolRows lists the tools that are running, each with how long it has been.
func (k *chatLook) toolRows(v *liveView, w int) []cell.Line {
	var out []cell.Line
	for i, t := range v.tools {
		if i == maxToolRows {
			out = append(out, cell.Styled(k.st.dim, fmt.Sprintf("  +%d more running", len(v.tools)-maxToolRows)))
			break
		}
		glyph, gst := k.g.still, k.st.accent
		if k.Anim {
			glyph = k.g.spinner(v.frame + i*3)
		}
		if t.asking {
			glyph, gst = k.g.ask, k.st.warn
		}
		var r row
		if t.worker {
			r.add(k.st.dim, "["+clean(t.agent)+"] ")
		}
		r.add(gst, glyph+" ").add(k.st.info.With(cell.Bold), t.title)
		tail := ""
		if t.elapsed >= time.Second {
			tail = "  " + duration(t.elapsed.Round(time.Second))
		}
		if t.asking {
			tail += "  waiting for your answer"
		}
		if room := w - r.w - 1 - cell.StringWidth(tail); t.summary != "" && room > 3 {
			r.add(cell.Style{}, " ").add(cell.Style{}, cutCells(t.summary, room, k.g.ellipsis))
		}
		r.add(k.st.dim, tail)
		out = append(out, fit(r.line(), w))
	}
	return out
}

// foldLine is a compaction in progress: the thread, a block that shrinks into the resume, and the tokens counting down.
func (k *chatLook) foldLine(v *liveView, w int) cell.Line {
	f := v.fold
	full, _ := k.foldGlyphs()
	cells := foldCells(f.before, f.after, f.progress, foldWidth)
	var r row
	r.add(k.st.accent, k.g.compact+" compacting ")
	if f.who != "" {
		r.add(k.st.dim, "["+clean(f.who)+"] ")
	}
	r.add(k.st.accent.Without(cell.Bold), strings.Repeat(full, cells)+strings.Repeat(" ", foldWidth-cells)).
		add(cell.Style{}, " "+widget.Tokens(foldTokens(f.before, f.after, f.progress))).
		add(k.st.dim, " "+k.g.arrow+" "+widget.Tokens(f.after))
	return fit(r.line(), w)
}

// ---- the cache: the prompt stack and the hit ratio ----

// stackAgent is the agent whose prompt the stack bar shows: the one the chat is with, once it has sent something.
func stackAgent(v *liveView) *state.Agent {
	if v.snap == nil {
		return nil
	}
	id := v.snap.Resolve(v.agent)
	a, ok := v.snap.Agent(id)
	if !ok || (len(a.Stack.Sections) == 0 && a.Stack.Unsectioned == 0) {
		return nil
	}
	return &a
}

// stackRows is the prompt as a bar of the layers G0 to G6, sized by tokens: bright where the provider served it from its cache, dim
// where it was paid in full, ▏ after a cache breakpoint, a light that sweeps along it when an answer says how much matched, the layer that
// broke in red, and on the right how much was cached and how long the cache will last. Under it, the names of the layers.
func (k *chatLook) stackRows(v *liveView, a *state.Agent, w int) (bar, labels cell.Line) {
	stk := a.Stack
	layers := promptLayers(a, v.mem.g0est())
	if len(layers) == 0 {
		return nil, nil
	}
	total := 0
	for _, l := range layers {
		total += l.Tokens
	}
	if stk.Answered && stk.Prompt > 0 {
		total = stk.Prompt
	}
	left := "prompt " + widget.Tokens(total) + " "
	var right row
	if stk.Answered && stk.Prompt > 0 {
		right.add(hitStyle(k.st, float64(stk.Read)/float64(stk.Prompt)), " "+widget.Percent(float64(stk.Read)/float64(stk.Prompt))+" cached")
	}
	var ttl cell.Line
	if lft, tot, ok := ttlOf(v.snap, "agent", a.ID); ok {
		ttl = widget.TTL(lft, tot, 26, k.Palette)
	}
	if ttl != nil {
		right.add(cell.Style{}, "  ").addLine(ttl)
	}
	lead := cell.StringWidth(left)
	barW := w - lead - right.w
	if !k.Unicode || barW < 10 { // a bar that narrow says nothing: the words do
		var r row
		r.add(k.st.dim, left).addLine(right.line())
		return fit(r.line(), w), nil
	}
	o := widget.NewStackOpts(barW, stk.Read)
	if !stk.Answered {
		o.CachedTokens = 0
	}
	if stk.Answered && k.Anim && stk.RespSeq != 0 && stk.Prompt > 0 {
		if age := v.mem.born().Age(stk.RespSeq, v.frame); age < sweepFrames {
			o.Sweep = float64(stk.Read) / float64(stk.Prompt) * float64(age+1) / sweepFrames
		}
	}
	o.BreakAt = breakLayer(a, layers)
	if lft, tot, ok := ttlOf(v.snap, "agent", a.ID); ok && tot > 0 {
		o.Warm = float64(lft) / float64(tot)
	}
	b, lb := widget.StackBar(layers, o, k.Palette)
	var r row
	r.add(k.st.dim, left).addLine(b).addLine(right.line())
	bar = fit(r.line(), w)
	if lb != nil {
		labels = cell.Join(cell.Spaces(lead, cell.Style{}), lb)
		labels = fit(labels, w)
	}
	return bar, labels
}

// sparkRows is the hit ratio of every request as a row of bars, with a cache break (⚠), a compaction (◆) and a new epoch (↻) marked
// above the request they came before.
func (k *chatLook) sparkRows(v *liveView, a *state.Agent, w int) (marks, bars cell.Line) {
	ratios := a.Hits.Ratios
	if len(ratios) == 0 || !k.Unicode {
		return nil, nil
	}
	const label = "cache "
	var mk []widget.Mark
	for _, m := range a.Hits.Marks {
		i := m.At - a.Hits.First
		if i < 0 || i >= len(ratios) {
			continue
		}
		kind := widget.MarkEpoch
		switch m.Kind {
		case state.MarkAnomaly:
			kind = widget.MarkBreak
		case state.MarkCompaction:
			kind = widget.MarkCompact
		}
		mk = append(mk, widget.Mark{At: i, Kind: kind})
	}
	room := min(len(ratios), 32, max(w-len(label)-24, 8))
	vals := ratios
	lines := widget.Spark(vals, room, mk, k.Palette)
	if len(lines) != 2 {
		return nil, nil
	}
	n := a.Hits.Len()
	var note row
	note.add(k.st.dim, fmt.Sprintf("  hit ratio per request %s %s", k.g.dot, count(n, "request", "requests")))
	if s := v.snap.Totals.Savings; s.Known() && s.SavedUSD > 0 {
		note.add(k.st.dim, " "+k.g.dot+" saved "+k.g.approx+" ").add(k.st.good, widget.USD(s.SavedUSD)).add(k.st.dim, " at list price")
	}
	var bl row
	bl.add(k.st.dim, label).addLine(lines[1]).addLine(note.line())
	return cell.Join(cell.Spaces(len(label), cell.Style{}), lines[0]), fit(bl.line(), w)
}

// ---- the permission dialog ----

// dialogHint is the line under a question: the keys, or why they are not taken yet.
func (k *chatLook) dialogHint(d *dialogView) cell.Line {
	if !d.armed {
		return cell.Styled(k.st.dim, "  your typing goes to the prompt until you pause, so that it cannot answer this")
	}
	var r row
	r.add(k.st.dim, "  ").add(k.st.accent, "1 2 3").add(k.st.dim, " or ").add(k.st.accent, "y a n").add(k.st.dim, ", ").add(k.st.accent, k.upDown()).
		add(k.st.dim, " and ").add(k.st.accent, "enter").add(k.st.dim, "  "+k.g.dot+"  esc says no  "+k.g.dot+"  ctrl+c cancels the turn")
	return r.line()
}

func (k *chatLook) upDown() string {
	if k.Unicode {
		return "↑↓"
	}
	return "up/down"
}

// dialogLines is the question in its box. The body is what was asked, laid out for the box's inner width by the program, which has
// cut it to fit (and printed the whole of it into the scrollback) when it did not.
func (k *chatLook) dialogLines(d *dialogView, w int) []cell.Line {
	return widget.Dialog(d.title, d.body, d.options, d.sel, w, k.Theme)
}
