package widget

import (
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// DiffOptions tune Diff and UnifiedDiffWith. The zero value is what Diff's documentation describes.
type DiffOptions struct {
	// Context is the number of unchanged lines shown around each change. 0 means the default, 2; a negative number means
	// none. Hunks whose contexts touch are merged.
	Context int
	// MaxLines bounds the output: when there are more lines, the first MaxLines-1 are kept and the last line says "… N more
	// lines". 0 means no limit.
	MaxLines int
	// TabWidth is the width of a tab stop when tabs are expanded; 0 means 4.
	TabWidth int
	// NoLineNumbers leaves out the gutter of line numbers.
	NoLineNumbers bool
	// NoHeader leaves out the header line with the path and the counts.
	NoHeader bool
}

func (o DiffOptions) context() int {
	switch {
	case o.Context == 0:
		return 2
	case o.Context < 0:
		return 0
	}
	return min(o.Context, 1<<20)
}

func (o DiffOptions) tabs() int {
	if o.TabWidth < 1 {
		return 4
	}
	return min(o.TabWidth, 16)
}

// The model both entry points render: files with hunks of lines. Text is display text already: cleaned, tabs expanded.
type dfLine struct {
	kind     byte // ' ', '-', '+', or '\\' for a note such as "No newline at end of file"
	text     string
	old, new int        // line numbers, 0 where the line is not on that side
	marks    []diffMark // what changed inside the line, for lines that replace another
}

type dfHunk struct {
	lines   []dfLine
	section string // what the hunk header named, such as the function
	skipped int    // unchanged lines between the previous hunk and this one; -1 when not known
}

type dfFile struct {
	path       string
	note       string // "new file", "renamed from x", "binary files differ"
	adds, dels int
	hunks      []dfHunk
	text       []string // lines that belong to no hunk: commit messages, junk; drawn dim
}

// Diff renders an edit as a diff of the lines of before and after. The header has the path (shortened from the left when it
// is too long) and the counts, +N in green and −M in red. Then come the hunks: each change with Context lines (default 2) around
// it, merged when they touch, separated by a "⋯ N unchanged lines" row. Each row has a gutter with the old and the new line
// number, a marker (- removed, + added, blank unchanged), and the text; removed and added lines have a full-width red or green
// background, and where a removed line and an added line replace each other, the words that changed are highlighted on top of it
// (not when the two lines have too little in common: then the whole line already says it). MonoTheme has no colours: the
// markers carry the meaning, and the changed words are bold and underlined. A line too long for the width is wrapped at the cell
// boundary and its continuation rows start with ↳ in the marker column. Tabs are expanded, control characters are removed, and
// a line ending in a carriage return is compared without it. A narrow width drops the old line numbers, then all of them.
//
// The search is Myers' O(ND) over lines, with a cap on the edit distance and on the work: a pair of inputs that would take
// longer (for instance, two huge files that share nothing) are shown as one removed block and one added block instead of
// being aligned. Identical inputs give the header and "(no changes)"; inputs that differ only in line endings or in the final
// newline give "(line endings differ)". A width below 1 gives nil.
func Diff(path, before, after string, width int, th Theme, o DiffOptions) []cell.Line {
	if width < 1 {
		return nil
	}
	bl, al := splitDiffLines(before), splitDiffLines(after)
	runs := diffScript(bl, al)
	f := dfFile{path: strings.TrimSpace(safeOneLine(path))}
	for _, r := range runs {
		switch r.kind {
		case '-':
			f.dels += r.n
		case '+':
			f.adds += r.n
		}
	}
	switch {
	case before == after:
		f.note = "no changes"
	case f.adds == 0 && f.dels == 0:
		f.note = "line endings differ"
	case before == "":
		f.note = "new file"
	case after == "":
		f.note = "deleted"
	}
	f.hunks = diffBuildHunks(runs, bl, al, o.context(), o.tabs())
	return diffRenderFiles([]dfFile{f}, width, th, o)
}

func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	ls := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range ls {
		ls[i] = strings.TrimSuffix(l, "\r")
	}
	return ls
}

// diffText is what a line shows: cleaned of control characters, tabs expanded.
func diffText(s string, tabw int) string {
	s = strings.ReplaceAll(safeText(s), "\n", " ")
	return expandTabStops(s, tabw)
}

// diffBuildHunks cuts the edit script into hunks with ctx lines of context and finds what changed inside replaced lines.
func diffBuildHunks(runs []dfRun, before, after []string, ctx, tabw int) []dfHunk {
	var hunks []dfHunk
	var cur *dfHunk
	var pending []dfLine
	skipped := 0
	context := func(r dfRun, lo, hi int) []dfLine {
		out := make([]dfLine, 0, hi-lo)
		for i := lo; i < hi; i++ {
			out = append(out, dfLine{kind: ' ', text: diffText(before[r.a+i], tabw), old: r.a + i + 1, new: r.b + i + 1})
		}
		return out
	}
	for ri, r := range runs {
		hasNext := ri+1 < len(runs)
		if r.kind == ' ' {
			switch {
			case cur != nil && hasNext && r.n <= 2*ctx: // the contexts touch: one hunk
				cur.lines = append(cur.lines, context(r, 0, r.n)...)
			case cur != nil:
				tc := min(ctx, r.n)
				cur.lines = append(cur.lines, context(r, 0, tc)...)
				hunks = append(hunks, *cur)
				cur = nil
				if hasNext {
					pending = context(r, r.n-ctx, r.n) // r.n > 2*ctx here, so the two contexts do not overlap
					skipped = r.n - tc - ctx
				}
			case hasNext:
				lc := min(ctx, r.n)
				pending, skipped = context(r, r.n-lc, r.n), r.n-lc
			}
			continue
		}
		if cur == nil {
			cur = &dfHunk{skipped: skipped, lines: pending}
			pending = nil
		}
		for i := 0; i < r.n; i++ {
			if r.kind == '-' {
				cur.lines = append(cur.lines, dfLine{kind: '-', text: diffText(before[r.a+i], tabw), old: r.a + i + 1})
			} else {
				cur.lines = append(cur.lines, dfLine{kind: '+', text: diffText(after[r.b+i], tabw), new: r.b + i + 1})
			}
		}
	}
	if cur != nil {
		hunks = append(hunks, *cur)
	}
	budget := diffWordBudget
	for i := range hunks {
		pairWords(hunks[i].lines, &budget)
	}
	return hunks
}

// diffWordBudget bounds the work spent on highlighting words in one diff, in tokens of the lines compared.
const diffWordBudget = 100_000

// pairWords finds what changed inside lines that replace each other: in a block of removed lines followed by added lines, the
// first removed is paired with the first added, and so on.
func pairWords(ls []dfLine, budget *int) {
	for i := 0; i < len(ls); {
		if ls[i].kind != '-' {
			i++
			continue
		}
		j := i
		for j < len(ls) && ls[j].kind == '-' {
			j++
		}
		k := j
		for k < len(ls) && ls[k].kind == '+' {
			k++
		}
		for p := 0; p < min(j-i, k-j); p++ {
			d, a := &ls[i+p], &ls[j+p]
			if ma, mb, ok := wordMarks(d.text, a.text, budget); ok {
				d.marks, a.marks = ma, mb
			}
		}
		i = k
	}
}

// ---- rendering ----

// dfLayout is how the rows of one file are laid out.
type dfLayout struct {
	mode    int // 2 old and new line numbers, 1 one number, 0 none
	ow, nw  int // width of the number columns
	prefixW int
	textW   int
}

func diffDigits(n int) int { return len(strconv.Itoa(max(n, 1))) }

func diffChooseLayout(width, maxOld, maxNew int, noNums bool) dfLayout {
	ow, nw := diffDigits(maxOld), diffDigits(maxNew)
	if !noNums {
		if p := ow + nw + 4; width-p >= 16 {
			return dfLayout{mode: 2, ow: ow, nw: nw, prefixW: p, textW: width - p}
		}
		w := max(ow, nw)
		if p := w + 3; width-p >= 12 {
			return dfLayout{mode: 1, ow: w, nw: w, prefixW: p, textW: width - p}
		}
	}
	return dfLayout{prefixW: 2, textW: max(1, width-2)}
}

func diffRenderFiles(files []dfFile, width int, th Theme, o DiffOptions) []cell.Line {
	var out []cell.Line
	for _, f := range files {
		if len(out) > 0 {
			out = append(out, nil)
		}
		out = append(out, renderDiffFile(f, width, th, o)...)
	}
	if o.MaxLines > 0 && len(out) > o.MaxLines {
		more := len(out) - (o.MaxLines - 1)
		out = append(out[:o.MaxLines-1], cell.Styled(th.Dim, th.glyphs().ellipsis+" "+strconv.Itoa(more)+" more lines"))
	}
	return clipLines(out, width)
}

func renderDiffFile(f dfFile, width int, th Theme, o DiffOptions) []cell.Line {
	g := th.glyphs()
	var out []cell.Line
	if f.path == "" && len(f.hunks) == 0 && f.note == "" { // not a file: text that came with the patch
		for _, t := range f.text {
			out = append(out, cutRows(cell.Styled(th.Dim, diffText(t, 4)), width, cell.Span{})...) // indentation is kept
		}
		return out
	}
	if !o.NoHeader {
		out = append(out, diffHeader(f, width, th)...)
	}
	maxOld, maxNew := 0, 0
	for _, h := range f.hunks {
		for _, l := range h.lines {
			maxOld, maxNew = max(maxOld, l.old), max(maxNew, l.new)
		}
	}
	lay := diffChooseLayout(width, maxOld, maxNew, o.NoLineNumbers)
	for hi, h := range f.hunks {
		if hi > 0 || h.section != "" {
			if sep := diffSeparator(h, hi, lay, width, th, g); sep != nil {
				out = append(out, sep)
			}
		}
		for _, l := range h.lines {
			out = append(out, diffRows(l, lay, width, th, g)...)
		}
	}
	return out
}

// diffHeader is the path and the counts; a note (new file, renamed from x, binary) follows on the same row when it fits and
// on a row of its own when it does not, so that it is never lost. A file with no changed lines shows its note in place of
// the counts.
func diffHeader(f dfFile, width int, th Theme) []cell.Line {
	g := th.glyphs()
	var counts cell.Line
	if f.adds+f.dels > 0 || f.note == "" && f.path == "" {
		counts = cell.Line{
			{Text: "+" + strconv.Itoa(f.adds), Style: composeStyle(th.Good, cell.Style{Attr: cell.Bold})}, {Text: " "},
			{Text: g.minus + strconv.Itoa(f.dels), Style: composeStyle(th.Bad, cell.Style{Attr: cell.Bold})},
		}
	}
	var note cell.Line
	if f.note != "" {
		note = cell.Styled(th.Dim, "("+safeOneLine(f.note)+")")
	}
	gap := 0 // cells between what the row holds: path, counts, note
	if f.path != "" && counts != nil {
		gap += 2
	}
	rest := counts.Width() + gap // what must fit beside the path
	sameRow := note != nil && f.path != "" && cell.StringWidth(f.path)+rest+1+note.Width() <= width
	if sameRow {
		rest += 1 + note.Width()
	}
	var l cell.Line
	if f.path != "" {
		l = cell.Line{{Text: ellipsizeLeft(f.path, max(width-rest, 1), g.ellipsis), Style: th.Strong}}
		if counts != nil {
			l = append(l, cell.Span{Text: "  "})
		}
	}
	l = append(l, counts...)
	switch {
	case sameRow:
		return []cell.Line{append(append(l, cell.Span{Text: " "}), note...)}
	case note != nil && len(l) == 0:
		return diffWrapNote(note, width)
	case note != nil:
		return append([]cell.Line{l}, diffWrapNote(note, width)...)
	}
	return []cell.Line{l}
}

func diffWrapNote(l cell.Line, width int) []cell.Line { return l.Wrap(width, 0) }

func diffSeparator(h dfHunk, hi int, lay dfLayout, width int, th Theme, g *glyphSet) cell.Line {
	var parts []string
	switch {
	case h.skipped == 1:
		parts = append(parts, "1 unchanged line")
	case h.skipped > 1:
		parts = append(parts, strconv.Itoa(h.skipped)+" unchanged lines")
	}
	if s := strings.TrimSpace(safeOneLine(h.section)); s != "" {
		parts = append(parts, s)
	}
	text := g.gap
	if len(parts) > 0 {
		text += " " + strings.Join(parts, "  ")
	}
	indent := max(0, lay.prefixW-2)
	if indent+cell.StringWidth(text) > width {
		indent = 0
	}
	l := cell.Line{{Text: repeatText(" ", indent)}, {Text: text, Style: th.Dim}}
	return l.Truncate(width, g.ellipsis)
}

// diffRows renders one line of a hunk as one or more rows.
func diffRows(l dfLine, lay dfLayout, width int, th Theme, g *glyphSet) []cell.Line {
	var line, word, num cell.Style
	switch l.kind {
	case '-':
		line, word = th.DiffDel, th.DiffDelWord
		num = line
	case '+':
		line, word = th.DiffAdd, th.DiffAddWord
		num = line
	default:
		line = th.Dim
		num = th.Dim
	}
	markStyle := line
	if l.kind == '-' || l.kind == '+' {
		markStyle = composeStyle(line, cell.Style{Attr: cell.Bold})
	}
	contStyle := composeStyle(line, th.Faint)
	if l.kind != '-' && l.kind != '+' {
		contStyle = th.Faint
	}
	numText := func(n int, w int) string {
		if n <= 0 {
			return repeatText(" ", w)
		}
		s := strconv.Itoa(n)
		return repeatText(" ", w-len(s)) + s
	}
	prefix := func(first bool) cell.Line {
		var nums string
		switch lay.mode {
		case 2:
			if first {
				nums = numText(l.old, lay.ow) + " " + numText(l.new, lay.nw) + " "
			} else {
				nums = repeatText(" ", lay.ow+lay.nw+2)
			}
		case 1:
			n := l.new
			if l.kind == '-' {
				n = l.old
			}
			if first {
				nums = numText(n, lay.ow) + " "
			} else {
				nums = repeatText(" ", lay.ow+1)
			}
		}
		p := cell.Line{}
		if nums != "" {
			p = append(p, cell.Span{Text: nums, Style: num})
		}
		if first {
			return append(p, cell.Span{Text: string(l.kind) + " ", Style: markStyle})
		}
		return append(p, cell.Span{Text: g.cont + " ", Style: contStyle})
	}
	text := diffTextLine(l.text, l.marks, line, word)
	var out []cell.Line
	for i, piece := range cutRows(text, lay.textW, cell.Span{}) {
		row := append(prefix(i == 0), piece...)
		if paintsBlank(line) {
			row = row.Pad(width, line)
		}
		out = append(out, row)
	}
	return out
}

// diffTextLine is the text of a line with its changed words in the word style laid over the line's.
func diffTextLine(text string, marks []diffMark, base, word cell.Style) cell.Line {
	if text == "" {
		return nil
	}
	if len(marks) == 0 {
		return cell.Styled(base, text)
	}
	var l cell.Line
	pos := 0
	for _, m := range marks {
		if m.lo < pos || m.hi > len(text) || m.lo >= m.hi { // marks come sorted and inside the text; be safe anyway
			continue
		}
		if m.lo > pos {
			l = append(l, cell.Span{Text: text[pos:m.lo], Style: base})
		}
		l = append(l, cell.Span{Text: text[m.lo:m.hi], Style: composeStyle(base, word)})
		pos = m.hi
	}
	if pos < len(text) {
		l = append(l, cell.Span{Text: text[pos:], Style: base})
	}
	return l
}
