package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// The live region: the few rows under the scrollback that are drawn again at every frame. From top to bottom, when they apply:
//
//	● the words that are still arriving (markdown that is not final yet)
//	◆ compacting 31.2k ▓▓▓▓▓▓▓ → 12.4k          a thread being folded
//	⠙ Bash go test ./...  3.2s                   a tool that is running
//	⠹ Thinking…  14s  ↑48.3k ↓2.1k  $0.0021  esc to interrupt
//	╭────────────────────────────────────────╮
//	│ ❯ the prompt, and what is typed ahead  │
//	╰────────────────────────────────────────╯
//	  completion menu, the Esc hint
//	⏎ queued: "and run the race detector"
//	default · ctrl+t stats · ctrl+g agents · / commands    heimdall/model · session
//
// The page is kept as clean as it can be: what the cache saved, the prompt stack and the hit ratio are statistics, and they are on the
// stats page (ctrl+t, /stats), one key away, not in every frame.
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
	flash   bool // a cache break: the line is in the alarm style for a moment
	// waiting is how long the request in flight has gone unanswered, once that is long (slowRequest): 0 before.
	waiting time.Duration
}

// slowRequest is how long a model request goes unanswered before the status line says it is waiting for the model.
const slowRequest = 45 * time.Second

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
	more    int // questions that wait behind this one
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
	agent string // whose prompt the stats page shows
	team  bool   // the session is a team: the footer names the key of the agents page

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
		parts = append(parts, livePart{pos: posBelow, prio: 0, lines: []cell.Line{k.dialogHint(v.dlg, w)}})
		dim := *v
		dim.inputBlur = true
		under, _, _ := k.inputBox(&dim, w)
		parts = append(parts, livePart{pos: posInputUnder, prio: 2, lines: under})
	} else {
		box, curRow, curCol = k.inputBox(v, w)
		if below := v.ed.Lines[min(v.ed.InputRows, len(v.ed.Lines)):]; len(below) > 0 {
			parts = append(parts, livePart{pos: posBelow, prio: 0, lines: k.fitAll(below, w)})
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

func (k *chatLook) fitAll(ls []cell.Line, w int) []cell.Line {
	out := make([]cell.Line, len(ls))
	for i, l := range ls {
		out[i] = k.fit(l, w)
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
		return k.fitAll(body, w), v.ed.CursorRow, min(v.ed.CursorCol, w-1)
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
	open, shut := "“", "”"
	if !k.Unicode {
		open, shut = `"`, `"`
	}
	var r row
	r.add(k.st.dim, k.g.queued+" queued: ").add(cell.Style{Attr: cell.Italic}, open+first+shut)
	if n := len(v.queue) - 1; n > 0 {
		r.add(k.st.dim, fmt.Sprintf(" (+%d more)", n))
	}
	return k.fit(r.line(), w)
}

// ---- the footer ----

// footer is the mode, the keys that are not obvious, and on the right the model and the session. When the width is short the session
// gives way first (it is on /status, and a person does not read it), then the keys one at a time, the least needed first, then the model.
func (k *chatLook) footer(v *liveView, w int) cell.Line {
	// the keys of the screens that matter: the stats page, the team's agents (only a team has them), the commands
	hints := []string{
		k.g.dot + " ctrl+t stats " + k.g.dot + " / commands",
		k.g.dot + " ctrl+t stats",
		"",
	}
	if v.team {
		hints = []string{
			k.g.dot + " ctrl+t stats " + k.g.dot + " ctrl+g agents " + k.g.dot + " / commands",
			k.g.dot + " ctrl+t stats " + k.g.dot + " ctrl+g agents",
			k.g.dot + " ctrl+t stats",
			"",
		}
	}
	if v.hint != "" {
		hints = []string{k.g.dot + " " + clean(v.hint)}
	}
	model, session := clean(v.model), clean(v.session)
	var rights []string // with the model, the longest first; the line with none is the last resort
	if model != "" && session != "" {
		rights = append(rights, model+" "+k.g.dot+" "+session)
	}
	if model != "" {
		rights = append(rights, model)
	}
	build := func(hint, right string) (cell.Line, int) {
		var left row
		left.add(k.modeStyle(v.mode), clean(v.mode))
		if hint != "" {
			if left.w == 0 { // no mode yet (the session is being made): the keys start the line, without the dot that would join them to one
				hint = strings.TrimPrefix(hint, k.g.dot+" ")
			} else {
				hint = " " + hint
			}
			left.add(k.st.dim, hint)
		}
		return left.line(), left.w + 2 + cell.StringWidth(right)
	}
	for _, hint := range hints {
		for _, right := range rights {
			if l, n := build(hint, right); n <= w {
				return alignRight(l, cell.Styled(k.st.dim, right), w)
			}
		}
	}
	for _, hint := range hints { // not even the shortest keys fit beside the model: the keys without it
		if l, n := build(hint, ""); n <= w || hint == "" {
			return alignRight(l, cell.Styled(k.st.dim, ""), w)
		}
	}
	l, _ := build("", "")
	return k.fit(l, w)
}

func (k *chatLook) modeStyle(mode string) cell.Style {
	switch mode {
	case "accept-edits":
		return k.st.good
	case "plan":
		return k.st.info
	case "bypass", "yolo":
		return k.st.bad
	}
	return k.st.dim
}

// ---- the status line ----

// statusLine is what the agent is doing, for how long, what it has used, and how to stop it.
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
		if s.waiting > 0 { // an endpoint that takes long: the playful word would say the model is busy thinking
			word = "Waiting for the model (" + duration(s.waiting.Round(time.Second)) + ")"
		}
	case statusTool:
		word = "Running"
		if s.detail != "" {
			word += " " + clean(s.detail)
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
			word += " " + clean(s.detail)
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
		segs = append(segs, seg{fmt.Sprintf("%s%s %s%s", k.g.up, widget.Tokens(int(s.tokIn)), k.g.down, widget.Tokens(int(s.tokOut))), k.st.dim, 5})
	}
	if s.cost > 0 {
		segs = append(segs, seg{widget.USD(s.cost), cell.Style{}, 2})
	}
	hint := "esc to interrupt"
	segs = append(segs, seg{hint, k.st.dim, 4})

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
			r.add(k.st.dim, "["+k.agentTag(t.agent)+"] ")
		}
		r.add(gst, glyph+" ").add(k.st.info.With(cell.Bold), clean(t.title))
		tail := ""
		if t.elapsed >= time.Second {
			tail = "  " + duration(t.elapsed.Round(time.Second))
		}
		if t.asking {
			tail += "  waiting for your answer"
		}
		if room := w - r.w - 1 - cell.StringWidth(tail); t.summary != "" && room > 3 {
			r.add(cell.Style{}, " ").add(cell.Style{}, cutCells(clean(t.summary), room, k.g.ellipsis))
		}
		r.add(k.st.dim, tail)
		out = append(out, k.fit(r.line(), w))
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
		r.add(k.st.dim, "["+k.agentTag(f.who)+"] ")
	}
	r.add(k.st.accent.Without(cell.Bold), strings.Repeat(full, cells)+strings.Repeat(" ", foldWidth-cells)).
		add(cell.Style{}, " "+widget.Tokens(foldTokens(f.before, f.after, f.progress))).
		add(k.st.dim, " "+k.g.arrow+" "+widget.Tokens(f.after))
	return k.fit(r.line(), w)
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

// ttlLine is how long the provider will keep the prompt: the clock of widget.TTL, or in words where the glyphs cannot be trusted.
func (k *chatLook) ttlLine(left, total time.Duration, w int) cell.Line {
	if k.Unicode {
		return widget.TTL(left, total, w, k.Palette)
	}
	if left <= 0 {
		return cell.Styled(k.st.bad, "cold")
	}
	st := k.st.good
	if left < time.Minute {
		st = k.st.warn
	}
	return cell.Styled(st, "warm "+widget.Duration(left.Round(time.Second)))
}

// ---- the permission dialog ----

// dialogHint is the line under a question: the keys, or why they are not taken yet. It is said as fully as w allows.
func (k *chatLook) dialogHint(d *dialogView, w int) cell.Line {
	var variants []cell.Line
	if !d.armed {
		for _, s := range []string{
			"  your typing goes to the prompt until you pause, so that it cannot answer this",
			"  typing goes to the prompt until you pause",
			"  typing goes to the prompt for now",
		} {
			variants = append(variants, cell.Styled(k.st.dim, s))
		}
	} else {
		sep := "  " + k.g.dot + "  "
		keys := func(or string, enter string, rest ...string) cell.Line {
			var r row
			r.add(k.st.dim, "  ").add(k.st.accent, "1 2 3").add(k.st.dim, or).add(k.st.accent, k.upDown()).
				add(k.st.dim, enter).add(k.st.accent, "enter")
			for _, t := range rest {
				r.add(k.st.dim, sep+t)
			}
			return r.line()
		}
		variants = []cell.Line{
			keys(" answers, or ", " and ", "esc says no", "ctrl+c cancels the turn"),
			keys(" or ", " ", "esc says no", "ctrl+c cancels the turn"),
			keys(" or ", " ", "esc says no", "ctrl+c cancels"),
			keys(" or ", " ", "esc says no"),
			keys(" or ", " "),
		}
	}
	if d.more > 0 { // other questions wait behind this one: the person is told, as long as there is room
		tag := cell.Styled(k.st.warn, fmt.Sprintf("  %s  %d more waiting", k.g.dot, d.more))
		for _, v := range variants {
			if w2 := cell.Join(v, tag); w2.Width() <= w {
				return w2
			}
		}
	}
	for _, v := range variants {
		if v.Width() <= w {
			return v
		}
	}
	return k.fit(variants[len(variants)-1], w)
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
