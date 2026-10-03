// Package svg draws a terminal screen as an SVG, and a sequence of screens as an animated one. It is how the interface is
// shown in the README and the docs without a screenshot: the recording is the UI itself, rendered from a replayed event log
// through the same widgets and renderer the terminal uses, into a VT emulator, and from there to vector graphics. Nothing
// here is drawn by hand, so a change to the interface regenerates the pictures.
//
// Text is laid out on the cell grid: every word and every symbol is placed at its column and pinned to its width
// (textLength), so the picture does not depend on the font the viewer has. Block elements, shades, box-drawing characters,
// braille dots and the parallelograms of the progress bars are drawn as geometry instead of glyphs, because fonts leave
// hairline gaps between rows of blocks and draw the others at widths of their own; that is what keeps the horse and the
// stack bars solid and the spinners round.
//
// An animation is one SVG: each row appears in as many versions as it had different contents, and a CSS animation shows one
// version at a time, so a frame that changes two rows costs two small groups, not a copy of the screen. The result plays in
// an <img> tag (GitHub renders it), needs no script, and is deterministic: the same frames give the same bytes.
package svg

import (
	"fmt"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/vt"
)

// A Cell is one position of a screen. Text is empty for a blank cell and for the second half of a wide rune.
type Cell struct {
	Text  string
	Style cell.Style
}

// A Frame is a screen at a moment.
type Frame struct {
	Rows [][]Cell
	// CursorX and CursorY are where the cursor is, drawn when CursorOn.
	CursorX, CursorY int
	CursorOn         bool
	// At is when the frame appears, from the start of the recording.
	At time.Duration
}

// Capture copies what a VT emulator shows now.
func Capture(t *vt.Term, at time.Duration) Frame {
	cols, rows := t.Size()
	f := Frame{At: at, Rows: make([][]Cell, rows)}
	for y := 0; y < rows; y++ {
		f.Rows[y] = make([]Cell, cols)
		for x := 0; x < cols; x++ {
			text, st := t.Cell(x, y)
			f.Rows[y][x] = Cell{Text: text, Style: st}
		}
	}
	f.CursorX, f.CursorY, f.CursorOn = t.Cursor()
	return f
}

// A Theme says what the terminal looked like: its colours, font and cell size.
type Theme struct {
	Background, Foreground string // "#rrggbb"
	ANSI                   [16]string
	Font                   string
	FontSize               float64
	CellW, CellH           float64
	Padding                float64
	// Title, when not empty, is drawn in a window bar above the screen.
	Title string
}

// DefaultTheme is a dark terminal whose colours are the ones the interface's palette was designed on.
func DefaultTheme() Theme {
	return Theme{
		Background: "#0d1117", Foreground: "#c9d1d9",
		ANSI: [16]string{
			"#484f58", "#ff7b72", "#3fb950", "#d29922", "#58a6ff", "#bc8cff", "#39c5cf", "#b1bac4",
			"#6e7681", "#ffa198", "#56d364", "#e3b341", "#79c0ff", "#d2a8ff", "#56d4dd", "#f0f6fc",
		},
		Font:     "ui-monospace, SFMono-Regular, Menlo, Consolas, 'DejaVu Sans Mono', 'Liberation Mono', monospace",
		FontSize: 14, CellW: 8.4, CellH: 18, Padding: 14,
	}
}

func (th Theme) fill() Theme {
	d := DefaultTheme()
	if th.Background == "" {
		th.Background = d.Background
	}
	if th.Foreground == "" {
		th.Foreground = d.Foreground
	}
	if th.ANSI == ([16]string{}) {
		th.ANSI = d.ANSI
	}
	if th.Font == "" {
		th.Font = d.Font
	}
	if th.FontSize == 0 {
		th.FontSize = d.FontSize
	}
	if th.CellW == 0 {
		th.CellW = d.CellW
	}
	if th.CellH == 0 {
		th.CellH = d.CellH
	}
	if th.Padding == 0 {
		th.Padding = d.Padding
	}
	return th
}

// hex turns a colour into "#rrggbb"; def is what the terminal's own colour is.
func (th Theme) hex(c cell.Color, def string) string {
	switch c.Kind {
	case cell.KindANSI:
		return th.ANSI[c.N&15]
	case cell.KindIndexed:
		return indexed(th, c.N)
	case cell.KindRGB:
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return def
}

// indexed is the xterm 256-colour palette: the 16 of the theme, a 6x6x6 cube, and 24 greys.
func indexed(th Theme, n uint8) string {
	switch {
	case n < 16:
		return th.ANSI[n]
	case n < 232:
		i := int(n) - 16
		lv := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + 40*v
		}
		return fmt.Sprintf("#%02x%02x%02x", lv(i/36), lv(i/6%6), lv(i%6))
	}
	g := 8 + 10*(int(n)-232)
	return fmt.Sprintf("#%02x%02x%02x", g, g, g)
}

// size is the picture's width and height in user units.
func (th Theme) size(cols, rows int) (w, h, top float64) {
	th = th.fill()
	w = float64(cols)*th.CellW + 2*th.Padding
	h = float64(rows)*th.CellH + 2*th.Padding
	if th.Title != "" {
		top = 30
		h += top
	}
	return w, h, top
}

// Static draws one frame as a complete SVG document.
func Static(f Frame, th Theme) string {
	th = th.fill()
	cols := 0
	if len(f.Rows) > 0 {
		cols = len(f.Rows[0])
	}
	w, h, top := th.size(cols, len(f.Rows))
	var b strings.Builder
	header(&b, th, w, h, top)
	b.WriteString(`<style>` + commonCSS(th) + `</style>` + "\n")
	chrome(&b, th, w, h, top)
	b.WriteString(gridOpen(th, top))
	for y, row := range f.Rows {
		drawRow(&b, th, row, y, 0, "")
	}
	drawCursor(&b, th, f)
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}

// header writes the opening SVG element with explicit dimensions, viewBox, and image role.
func header(b *strings.Builder, th Theme, w, h, top float64) {
	fmt.Fprintf(b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %s %s" width="%s" height="%s" role="img">`+"\n", num(w), num(h), num(w), num(h))
}

// chrome writes the SVG background and optional terminal title bar with escaped title and font
// text.
func chrome(b *strings.Builder, th Theme, w, h, top float64) {
	fmt.Fprintf(b, `<rect width="%s" height="%s" rx="10" fill="%s"/>`+"\n", num(w), num(h), th.Background)
	if th.Title != "" {
		fmt.Fprintf(b, `<rect width="%s" height="%s" rx="10" fill="#161b22"/><rect y="20" width="%s" height="10" fill="#161b22"/>`+"\n", num(w), num(top), num(w))
		for i, c := range []string{"#ff5f56", "#ffbd2e", "#27c93f"} {
			fmt.Fprintf(b, `<circle cx="%d" cy="15" r="5.5" fill="%s"/>`, 18+i*18, c)
		}
		fmt.Fprintf(b, `<text x="%s" y="19.5" text-anchor="middle" fill="#8b949e" font-family="%s" font-size="12">%s</text>`+"\n", num(w/2), esc(th.Font), esc(th.Title))
	}
}

// commonCSS is the type of the grid. It sets no colour: a CSS rule beats the fill attribute of a text element, so a colour set here
// would turn every cell's own colour (and the dark text on a reversed cell) into the default one. The default colour is the
// attribute of the grid's group (gridOpen), which a text's own fill attribute overrides.
func commonCSS(th Theme) string {
	return fmt.Sprintf(`g text{font-family:%s;font-size:%spx;font-variant-ligatures:none;font-variant-emoji:text}.b{font-weight:700}.i{font-style:italic}`,
		th.Font, num(th.FontSize))
}

// gridOpen opens the group that holds the grid: shifted by the padding and the title bar, with the terminal's own foreground as the
// colour of every text that has none of its own.
func gridOpen(th Theme, top float64) string {
	return fmt.Sprintf(`<g fill="%s" transform="translate(%s %s)">`+"\n", th.Foreground, num(th.Padding), num(th.Padding+top))
}

// drawRow draws a row of cells, or a stretch of one that starts at column x0: backgrounds, geometry for blocks and lines, then the
// text. class, when not empty, is put on the group (an animation version).
func drawRow(b *strings.Builder, th Theme, row []Cell, y, x0 int, class string) {
	if !rowVisible(row) {
		return
	}
	open := `<g>`
	if class != "" {
		open = fmt.Sprintf(`<g class="%s">`, class)
	}
	b.WriteString(open)
	cw, ch := th.CellW, th.CellH
	py := float64(y) * ch
	// backgrounds: runs of the same colour
	for x := 0; x < len(row); {
		bg, ok := cellBG(th, row[x])
		if !ok {
			x++
			continue
		}
		e := x + 1
		for e < len(row) {
			if b2, ok2 := cellBG(th, row[e]); !ok2 || b2 != bg {
				break
			}
			e++
		}
		fmt.Fprintf(b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`, num(float64(x0+x)*cw), num(py), num(float64(e-x)*cw+0.3), num(ch+0.3), bg)
		x = e
	}
	// lines under and through text are drawn as shapes too: a text decoration stops at the gap between two words
	for x := 0; x < len(row); {
		st := row[x].Style
		if !st.Has(cell.Underline) && !st.Has(cell.Strike) {
			x++
			continue
		}
		e := x + 1
		for e < len(row) && row[e].Style == st {
			e++
		}
		fg := cellFG(th, row[x])
		if st.Has(cell.Underline) {
			fmt.Fprintf(b, `<rect x="%s" y="%s" width="%s" height="1.1" fill="%s"%s/>`, num(float64(x0+x)*cw), num(py+ch*0.9), num(float64(e-x)*cw), fg, opacity(st))
		}
		if st.Has(cell.Strike) {
			fmt.Fprintf(b, `<rect x="%s" y="%s" width="%s" height="1.1" fill="%s"%s/>`, num(float64(x0+x)*cw), num(py+ch*0.52), num(float64(e-x)*cw), fg, opacity(st))
		}
		x = e
	}
	// geometry and text. A word is a run of plain (printable ASCII) cells of one style; anything else (a symbol, a wide rune, a
	// rune with a mark) stands alone in its cells. Each is placed at its own column and pinned to its own width, so a font
	// that is wider or narrower than the grid, or that draws a symbol from another font, moves nothing but that word.
	for x := 0; x < len(row); {
		c := row[x]
		if c.Text == "" || c.Text == " " {
			x++
			continue
		}
		fg := cellFG(th, c)
		if r := []rune(c.Text); len(r) == 1 {
			if g, ok := glyph(r[0]); ok {
				// a run of the same block or line is one shape, not one per cell: a stack bar is one rectangle
				n := 1
				if mergeable(g) {
					for x+n < len(row) && row[x+n].Text == c.Text && sameInk(row[x+n].Style, c.Style) {
						n++
					}
				}
				g.draw(b, float64(x0+x)*cw, py, cw*float64(n), ch, fg, c.Style.Has(cell.Dim))
				x += n
				continue
			}
		}
		if plain(c.Text) {
			e := x + 1
			for e < len(row) && plain(row[e].Text) && row[e].Style == c.Style {
				e++
			}
			var word strings.Builder
			for i := x; i < e; i++ {
				word.WriteString(row[i].Text)
			}
			drawText(b, th, x0+x, e-x, py, word.String(), fg, c.Style, "spacing")
			x = e
			continue
		}
		w := 1
		if cell.StringWidth(c.Text) > 1 && x+1 < len(row) && row[x+1].Text == "" {
			w = 2
		}
		drawText(b, th, x0+x, w, py, c.Text, fg, c.Style, "spacingAndGlyphs")
		x += w
	}
	b.WriteString("</g>\n")
}

// plain says a cell holds a character that every monospaced font draws on the grid, in one cell: printable ASCII other than the
// space, the Latin-1 letters and signs (the middle dot of a track, the degree sign, accented letters) and the typographic
// punctuation of running text. Symbols, arrows, box pieces and emoji are not plain: fonts disagree about their width, so each stands
// alone in its cell.
func plain(s string) bool {
	if len(s) == 1 {
		return s[0] > ' ' && s[0] < 0x7f
	}
	r := []rune(s)
	if len(r) != 1 {
		return false
	}
	switch c := r[0]; {
	case c >= 0xa1 && c <= 0xff && c != 0xad:
		return true
	case c == '…' || c == '–' || c == '—' || c == '‘' || c == '’' || c == '“' || c == '”' || c == '•':
		return true
	}
	return false
}

// drawText places text at a column and pins it to cells columns. A word is spaced out (or drawn closer) to fit; a symbol is
// scaled to fit, since the font a viewer has may draw it wider or narrower than the cell.
func drawText(b *strings.Builder, th Theme, col, cells int, py float64, text, fg string, st cell.Style, adjust string) {
	fmt.Fprintf(b, `<text x="%s" y="%s"`, num(float64(col)*th.CellW), num(py+th.CellH*0.77))
	if cells > 1 || adjust == "spacingAndGlyphs" {
		fmt.Fprintf(b, ` textLength="%s" lengthAdjust="%s"`, num(float64(cells)*th.CellW), adjust)
	}
	fmt.Fprintf(b, ` fill="%s"%s>%s</text>`, fg, classes(st)+opacity(st), esc(text))
}

func classes(s cell.Style) string {
	var c []string
	if s.Has(cell.Bold) {
		c = append(c, "b")
	}
	if s.Has(cell.Italic) {
		c = append(c, "i")
	}
	if len(c) == 0 {
		return ""
	}
	return ` class="` + strings.Join(c, " ") + `"`
}

// opacity returns the SVG opacity attribute used for dim text and otherwise no attribute.
func opacity(s cell.Style) string {
	if s.Has(cell.Dim) {
		return ` opacity="0.62"`
	}
	return ""
}

// rowVisible recognizes rows with text or painted/decorated cells, including visually significant
// blank cells.
func rowVisible(row []Cell) bool {
	for _, c := range row {
		if (c.Text != "" && c.Text != " ") || c.Style.BG.Kind != cell.KindDefault || c.Style.Has(cell.Reverse) || c.Style.Has(cell.Underline) || c.Style.Has(cell.Strike) {
			return true
		}
	}
	return false
}

// sameInk says two cells are drawn with the same colour and dimness (the background is drawn separately).
func sameInk(a, b cell.Style) bool {
	return a.FG == b.FG && a.BG == b.BG && a.Attr&(cell.Dim|cell.Reverse) == b.Attr&(cell.Dim|cell.Reverse)
}

// cellFG resolves a cell's theme foreground, substituting its background when reverse video is
// active.
func cellFG(th Theme, c Cell) string {
	fg, bg := th.hex(c.Style.FG, th.Foreground), th.hex(c.Style.BG, th.Background)
	if c.Style.Has(cell.Reverse) {
		return bg
	}
	return fg
}

// cellBG is the colour behind a cell, and whether there is one to draw (the default background is the picture's).
func cellBG(th Theme, c Cell) (string, bool) {
	if c.Style.Has(cell.Reverse) {
		return th.hex(c.Style.FG, th.Foreground), true
	}
	if c.Style.BG.Kind == cell.KindDefault {
		return "", false
	}
	return th.hex(c.Style.BG, th.Background), true
}

// drawCursor emits a translucent cursor rectangle only when enabled and within the frame's row
// range.
func drawCursor(b *strings.Builder, th Theme, f Frame) {
	if !f.CursorOn || f.CursorY < 0 || f.CursorY >= len(f.Rows) {
		return
	}
	fmt.Fprintf(b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s" opacity="0.7"/>`+"\n",
		num(float64(f.CursorX)*th.CellW), num(float64(f.CursorY)*th.CellH+th.CellH*0.12), num(th.CellW), num(th.CellH*0.76), th.Foreground)
}

// num formats a coordinate compactly and deterministically.
func num(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

// esc escapes text for an XML text node or attribute. Control characters are dropped: SVG cannot carry them.
func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&apos;")
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0xfffe || r == 0xffff:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FromLines lays styled lines out on a grid of cols columns, one row per line: what a widget returns, as a frame, without a
// terminal in between. A line longer than cols is cut at the edge; a shorter one is padded with blank cells.
func FromLines(lines []cell.Line, cols int, at time.Duration) Frame {
	f := Frame{At: at, Rows: make([][]Cell, len(lines))}
	for y, l := range lines {
		row := make([]Cell, 0, cols)
		for _, sp := range l {
			for _, r := range sp.Text {
				w := cell.RuneWidth(r)
				switch {
				case w == 0:
					if n := len(row); n > 0 && row[n-1].Text != "" {
						row[n-1].Text += string(r) // a mark joins the cell before it
					}
				case len(row)+w > cols:
				case w == 1:
					row = append(row, Cell{Text: string(r), Style: sp.Style})
				default:
					row = append(row, Cell{Text: string(r), Style: sp.Style}, Cell{Style: sp.Style})
				}
			}
		}
		for len(row) < cols {
			row = append(row, Cell{})
		}
		f.Rows[y] = row
	}
	return f
}
