// Package widget holds the terminal UI's widgets: pure functions from data to styled lines.
//
// A widget takes its data, a width and a Theme and returns []cell.Line (or one cell.Line). Nothing in here does I/O, reads a
// clock, starts a goroutine or keeps state between calls; an animated widget takes a frame number or a progress fraction,
// never the time. No widget emits an escape code: turning styled lines into bytes is the renderer's job, which knows what the
// terminal supports.
//
// Two rules hold for every widget and are what the tests check:
//
//   - No line is wider than the width it was asked for (width is in terminal cells, as cell.StringWidth counts them), for any
//     input, including wide runes, combining marks, control characters and widths too small to lay the widget out properly
//     (then the widget degrades: it cuts, it never overflows). A width below 1 yields no lines.
//   - No output contains a control character. Text that reaches a widget is data: escape sequences (CSI, OSC 52, ...), C0 and C1
//     controls, bidi overrides and invalid UTF-8 are removed or replaced before layout, so the renderer only ever writes the
//     sequences it generated itself.
//
// MonoTheme has no colours, and the widgets stay readable in it: whatever a colour says (added or removed, selected, changed,
// a heading's level) is also said by text (a marker, a rule, a glyph) or by an attribute (bold, underline, reverse).
//
// A widget added to this package keeps both rules by reusing what is here: safeText and safeOneLine (markdown_text.go) make
// any text safe to lay out, cutRows hard-wraps at the cell boundary, clipLines is the last pass that enforces the width, and
// Theme.glyphs picks the Unicode or the ASCII glyph set. The checks the tests use (widgettest.MaxWidth, widgettest.Control,
// widgettest.Hostile) and Flatten and Golden for golden files are in the widgettest package; the goldens live in
// testdata/golden and are rewritten by go test -update.
package widget

import "github.com/reee344/sleipnir/internal/tui/cell"

// Theme is the palette and the styles widgets draw with. It is a plain value without slices, maps or functions: it can be
// copied, compared with == and used as a cache key.
//
// Text is the terminal's own foreground and background on purpose (the zero Style), so body text follows the user's colour
// scheme; the roles below carry the harness's palette. The colour roles are fixed so a screenshot reads without a legend: green is
// good (hit, pass, added), red is bad (miss, stuck, error, removed), amber (Warn) is waiting, blue (Info) is information and
// violet (Accent) is the harness itself.
type Theme struct {
	Text   cell.Style // body text: the terminal's defaults
	Dim    cell.Style // secondary text: labels, line numbers, urls
	Faint  cell.Style // furniture: rules, gutters, empty gauge cells
	Strong cell.Style // strong emphasis (**x**)
	Emph   cell.Style // emphasis (*x*)

	Good   cell.Style
	Bad    cell.Style
	Warn   cell.Style
	Info   cell.Style
	Accent cell.Style

	Code      cell.Style // inline code
	CodeBlock cell.Style // the body of a code block: its background is a slab behind the code
	Link      cell.Style
	Quote     cell.Style    // block quotes: the gutter and the text
	Heading   [6]cell.Style // H1..H6; pairwise distinct, in every theme, by colour and attributes or by attributes alone

	DiffAdd     cell.Style // an added line
	DiffDel     cell.Style // a removed line
	DiffAddWord cell.Style // the words that changed, on an added line (applied over DiffAdd)
	DiffDelWord cell.Style // the words that changed, on a removed line (applied over DiffDel)

	Border      cell.Style // box and table furniture
	Title       cell.Style // the title of a box
	SelectedRow cell.Style // the selected row of a table, the selected option of a dialog
	Zebra       cell.Style // every second row of a table that asks for it

	// Layer holds the colours of the prompt layers G0..G6, cold (stable) to hot (volatile): indigo, blue, teal, green,
	// yellow, orange, red. In MonoTheme all seven are the default colour; showing the layers then takes glyphs.
	Layer [7]cell.Color

	// Mono says that colours are not available: widgets add textual cues where a colour would otherwise be the only signal
	// (inline code keeps its backticks).
	Mono bool
	// ASCII says that only ASCII is safe to print: widgets use ASCII borders, bullets and markers and "..." for an ellipsis.
	ASCII bool
}

// Compile-time proof that Theme is comparable (the markdown stream compares themes to decide what it may reuse).
var _ = Theme{} == Theme{}

// The palette of docs/design/ux/sketchlib.py (PAL and LAYER), which the design sketches are drawn with.
var (
	palDim     = cell.Hex("#565f89")
	palFaint   = cell.Hex("#3b4261")
	palGreen   = cell.Hex("#9ece6a")
	palRed     = cell.Hex("#f7768e")
	palYellow  = cell.Hex("#e0af68")
	palBlue    = cell.Hex("#7aa2f7")
	palCyan    = cell.Hex("#7dcfff")
	palMagenta = cell.Hex("#bb9af7")
	palPanel   = cell.Hex("#16161e")
	palSel     = cell.Hex("#283457")
)

func themeFG(c cell.Color) cell.Style { return cell.Style{FG: c} }

// DefaultTheme is the theme for dark terminals, in the palette of the design sketches.
func DefaultTheme() Theme {
	bold := cell.Style{Attr: cell.Bold}
	return Theme{
		Dim:    themeFG(palDim),
		Faint:  themeFG(palFaint),
		Strong: bold,
		Emph:   cell.Style{Attr: cell.Italic},
		Good:   themeFG(palGreen),
		Bad:    themeFG(palRed),
		Warn:   themeFG(palYellow),
		Info:   themeFG(palBlue),
		Accent: themeFG(palMagenta),

		Code:      themeFG(palCyan),
		CodeBlock: cell.Style{BG: palPanel},
		Link:      cell.Style{FG: palBlue, Attr: cell.Underline},
		Quote:     cell.Style{FG: cell.Hex("#9aa5ce"), Attr: cell.Italic},
		Heading: [6]cell.Style{
			{FG: palMagenta, Attr: cell.Bold | cell.Underline},
			{FG: palBlue, Attr: cell.Bold},
			{FG: palCyan, Attr: cell.Bold},
			{Attr: cell.Bold},
			{Attr: cell.Bold | cell.Italic},
			{FG: palDim, Attr: cell.Italic},
		},

		DiffAdd:     cell.Style{FG: palGreen, BG: cell.Hex("#20352b")},
		DiffDel:     cell.Style{FG: palRed, BG: cell.Hex("#3b2030")},
		DiffAddWord: cell.Style{BG: cell.Hex("#2f5d43")},
		DiffDelWord: cell.Style{BG: cell.Hex("#6a2b45")},

		Border:      themeFG(palFaint),
		Title:       bold,
		SelectedRow: cell.Style{BG: palSel, Attr: cell.Bold},
		Zebra:       cell.Style{BG: cell.Hex("#1e2030")},

		Layer: [7]cell.Color{
			cell.Hex("#4c6fd0"), cell.Hex("#7aa2f7"), cell.Hex("#2ac3de"), cell.Hex("#9ece6a"),
			cell.Hex("#e0af68"), cell.Hex("#ff9e64"), cell.Hex("#f7768e"),
		},
	}
}

// LightTheme is the theme for light terminals: the same roles in darker colours that keep their contrast on white.
func LightTheme() Theme {
	var (
		dim     = cell.Hex("#6172b0")
		faint   = cell.Hex("#a8aecb")
		green   = cell.Hex("#3f7a2a")
		red     = cell.Hex("#c2304f")
		amber   = cell.Hex("#8c6c1e")
		blue    = cell.Hex("#2e5fd0")
		cyan    = cell.Hex("#007197")
		violet  = cell.Hex("#7c3fd0")
		bold    = cell.Style{Attr: cell.Bold}
		panel   = cell.Hex("#eceef4")
		sel     = cell.Hex("#c6d0f0")
		muted   = cell.Hex("#5a6690")
		addBG   = cell.Hex("#dcefd8")
		delBG   = cell.Hex("#f8dde3")
		addWord = cell.Hex("#b2dcaa")
		delWord = cell.Hex("#f0b4c2")
	)
	return Theme{
		Dim:    themeFG(dim),
		Faint:  themeFG(faint),
		Strong: bold,
		Emph:   cell.Style{Attr: cell.Italic},
		Good:   themeFG(green),
		Bad:    themeFG(red),
		Warn:   themeFG(amber),
		Info:   themeFG(blue),
		Accent: themeFG(violet),

		Code:      themeFG(cyan),
		CodeBlock: cell.Style{BG: panel},
		Link:      cell.Style{FG: blue, Attr: cell.Underline},
		Quote:     cell.Style{FG: muted, Attr: cell.Italic},
		Heading: [6]cell.Style{
			{FG: violet, Attr: cell.Bold | cell.Underline},
			{FG: blue, Attr: cell.Bold},
			{FG: cyan, Attr: cell.Bold},
			{Attr: cell.Bold},
			{Attr: cell.Bold | cell.Italic},
			{FG: dim, Attr: cell.Italic},
		},

		DiffAdd:     cell.Style{FG: green, BG: addBG},
		DiffDel:     cell.Style{FG: red, BG: delBG},
		DiffAddWord: cell.Style{BG: addWord},
		DiffDelWord: cell.Style{BG: delWord},

		Border:      themeFG(faint),
		Title:       bold,
		SelectedRow: cell.Style{BG: sel, Attr: cell.Bold},
		Zebra:       cell.Style{BG: cell.Hex("#f2f3f8")},

		Layer: [7]cell.Color{
			cell.Hex("#3a56b8"), cell.Hex("#2e7de9"), cell.Hex("#0f8ea8"), cell.Hex("#4c8a2e"),
			cell.Hex("#b28a1a"), cell.Hex("#c25a00"), cell.Hex("#d13a55"),
		},
	}
}

// MonoTheme has no colours at all, only the attributes bold, dim, italic, underline and reverse (strike for strikethrough
// text). Every widget is readable in it: what a colour would say is also in the text. Use it for NO_COLOR and for terminals
// with no colour.
func MonoTheme() Theme {
	bold, dim := cell.Style{Attr: cell.Bold}, cell.Style{Attr: cell.Dim}
	return Theme{
		Dim:    dim,
		Faint:  dim,
		Strong: bold,
		Emph:   cell.Style{Attr: cell.Italic},
		Good:   cell.Style{},
		Bad:    bold,
		Warn:   bold,
		Info:   cell.Style{},
		Accent: bold,

		Code:      cell.Style{}, // backticks carry it: see Theme.Mono
		CodeBlock: cell.Style{},
		Link:      cell.Style{Attr: cell.Underline},
		Quote:     cell.Style{Attr: cell.Italic},
		Heading: [6]cell.Style{
			{Attr: cell.Bold | cell.Underline},
			{Attr: cell.Bold},
			{Attr: cell.Bold | cell.Italic},
			{Attr: cell.Underline},
			{Attr: cell.Italic},
			{Attr: cell.Dim | cell.Italic},
		},

		// The markers (+ and -) say added and removed; the changed words are bold and underlined.
		DiffAdd:     cell.Style{},
		DiffDel:     cell.Style{},
		DiffAddWord: cell.Style{Attr: cell.Bold | cell.Underline},
		DiffDelWord: cell.Style{Attr: cell.Bold | cell.Underline},

		Border:      dim,
		Title:       bold,
		SelectedRow: cell.Style{Attr: cell.Reverse | cell.Bold},
		Zebra:       cell.Style{},

		Mono: true,
	}
}

// ThemeFor picks a theme from what the terminal offers: MonoTheme when colour is not available, else LightTheme when the
// background is light, else DefaultTheme. Detecting the terminal is not this package's job, so the answers come in as plain
// booleans.
func ThemeFor(colour, lightBackground bool) Theme {
	switch {
	case !colour:
		return MonoTheme()
	case lightBackground:
		return LightTheme()
	}
	return DefaultTheme()
}

// WithASCII returns the theme with ASCII-only output switched on or off. It changes the glyphs widgets draw (borders, bullets,
// markers, the ellipsis), not the styles.
func (th Theme) WithASCII(on bool) Theme { th.ASCII = on; return th }

// LayerStyle is the foreground style of prompt layer i (0 is G0, the coldest); an index out of range is clamped. Mono themes
// give the default colour.
func (th Theme) LayerStyle(i int) cell.Style {
	i = max(0, min(i, len(th.Layer)-1))
	return cell.Style{FG: th.Layer[i]}
}

// HeadingStyle is the style of a heading of the given level; a level outside 1..6 is clamped.
func (th Theme) HeadingStyle(level int) cell.Style {
	return th.Heading[max(1, min(level, 6))-1]
}

// glyphSet is every non-ASCII glyph the widgets draw, in both flavours.
type glyphSet struct {
	ellipsis string
	bullets  [3]string
	quote    string // gutter of a block quote
	codeBar  string // gutter of a code block
	cont     string // first cell of a wrapped continuation line of code
	rule     string // one cell of a horizontal rule
	vbar     string // a vertical separator
	cross    string // where a rule meets a vertical separator
	sel      string // the selection marker
	minus    string // the minus of "−3"
	gap      string // the mark of skipped, unchanged lines
	gaugeOn  string
	gaugeOff string
}

var (
	uniGlyphs = glyphSet{
		ellipsis: "…", bullets: [3]string{"•", "◦", "▪"}, quote: "▎", codeBar: "│", cont: "↳", rule: "─", vbar: "│", cross: "┼", sel: "❯",
		minus: "−", gap: "⋯", gaugeOn: "▰", gaugeOff: "▱",
	}
	asciiGlyphs = glyphSet{
		ellipsis: "...", bullets: [3]string{"*", "-", "+"}, quote: ">", codeBar: "|", cont: "\\", rule: "-", vbar: "|", cross: "+", sel: ">",
		minus: "-", gap: "...", gaugeOn: "#", gaugeOff: "-",
	}
)

// glyphs returns the glyph set the theme asks for. The sets are read-only tables.
func (th Theme) glyphs() *glyphSet {
	if th.ASCII {
		return &asciiGlyphs
	}
	return &uniGlyphs
}

// composeStyle returns base with over laid on it: over's colours win where it has any, attributes add up.
func composeStyle(base, over cell.Style) cell.Style {
	if over.FG.Kind != cell.KindDefault {
		base.FG = over.FG
	}
	if over.BG.Kind != cell.KindDefault {
		base.BG = over.BG
	}
	base.Attr |= over.Attr
	return base
}

// overlayStyle returns l with style st laid under every span: the spans keep what they set, and get the colours and attributes of
// st where they set none. A row style (selected, zebra) is applied this way.
func overlayStyle(l cell.Line, st cell.Style) cell.Line {
	if st == (cell.Style{}) {
		return l
	}
	out := make(cell.Line, len(l))
	for i, sp := range l {
		s := sp.Style
		if s.FG.Kind == cell.KindDefault {
			s.FG = st.FG
		}
		if s.BG.Kind == cell.KindDefault {
			s.BG = st.BG
		}
		s.Attr |= st.Attr
		out[i] = cell.Span{Text: sp.Text, Style: s}
	}
	return out
}

// paintsBlank reports whether a style is visible on blank cells, that is whether padding a line with spaces in it shows.
func paintsBlank(st cell.Style) bool {
	return st.BG.Kind != cell.KindDefault || st.Attr&(cell.Reverse|cell.Underline|cell.Strike) != 0
}
