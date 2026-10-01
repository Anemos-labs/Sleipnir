package widget

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// BorderStyle is the set of characters a box is drawn with.
type BorderStyle uint8

const (
	BorderRounded BorderStyle = iota // ╭─╮ │ ╰─╯ (the default)
	BorderSquare                     // ┌─┐ │ └─┘
	BorderDouble                     // ╔═╗ ║ ╚═╝
	BorderHeavy                      // ┏━┓ ┃ ┗━┛
	BorderASCII                      // +-+ | +-+
)

type borderRunes struct{ tl, tr, bl, br, h, v string }

var boxBorderSets = [...]borderRunes{
	BorderRounded: {"╭", "╮", "╰", "╯", "─", "│"},
	BorderSquare:  {"┌", "┐", "└", "┘", "─", "│"},
	BorderDouble:  {"╔", "╗", "╚", "╝", "═", "║"},
	BorderHeavy:   {"┏", "┓", "┗", "┛", "━", "┃"},
	BorderASCII:   {"+", "+", "+", "+", "-", "|"},
}

type boxWrap uint8

const (
	wrapWords boxWrap = iota
	wrapCells
	wrapCut
)

type boxCfg struct {
	border     BorderStyle
	padX, padY int
	accent     cell.Color
	hasAccent  bool
	wrap       boxWrap
}

// BoxOpt changes how Box draws.
type BoxOpt func(*boxCfg)

// BoxBorder chooses the border characters. BorderRounded is the default; a theme with ASCII set always draws BorderASCII.
func BoxBorder(b BorderStyle) BoxOpt { return func(c *boxCfg) { c.border = b } }

// BoxPadding sets the blank cells between the border and the body: x on each side (default 1), y rows above and below
// (default 0). Values are clamped to 0..16. When the width is too small for the padding, the horizontal padding shrinks
// until the body has 3 cells (or as many as the border leaves).
func BoxPadding(x, y int) BoxOpt {
	return func(c *boxCfg) { c.padX, c.padY = max(0, min(x, 16)), max(0, min(y, 16)) }
}

// BoxAccent colours the border and the title with c (a permission box is amber, an error red). It is ignored by MonoTheme,
// which has no colours.
func BoxAccent(c cell.Color) BoxOpt { return func(cfg *boxCfg) { cfg.accent, cfg.hasAccent = c, true } }

// BoxTruncate cuts a body line that is too wide and marks the cut with an ellipsis, instead of wrapping it: for one-line
// summaries. Never use it for text the reader has to see in full before deciding (a command to approve).
func BoxTruncate() BoxOpt { return func(c *boxCfg) { c.wrap = wrapCut } }

// BoxHardWrap wraps a body line that is too wide exactly at the cell boundary, keeping every space and marking each continuation
// with ↳ (\ in ASCII), instead of breaking at spaces: for commands and code, which must be shown exactly as they are.
func BoxHardWrap() BoxOpt { return func(c *boxCfg) { c.wrap = wrapCells } }

func newBoxCfg(opts []BoxOpt) boxCfg {
	cfg := boxCfg{padX: 1, wrap: wrapWords}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	return cfg
}

// geometry is the horizontal padding and the width of the body inside the border for a box of the given total width. It is
// the same for Box and BoxInnerWidth, so a caller can render a diff at exactly the width the body will have.
func (c boxCfg) geometry(width int) (padX, inner int) {
	padX = c.padX
	for padX > 0 && width-2-2*padX < 3 { // padding is the first thing to give up when the body would get cramped
		padX--
	}
	return padX, max(0, width-2-2*padX)
}

// BoxInnerWidth is the width of the body lines of a Box of the given total width and options (0 when the box is too narrow to
// have a border). Render a body (a diff, a table) at this width and Box will not have to wrap it.
func BoxInnerWidth(width int, opts ...BoxOpt) int {
	if width < 4 {
		return 0
	}
	_, inner := newBoxCfg(opts).geometry(width)
	return inner
}

// Box draws a bordered box exactly width cells wide: a border (rounded by default) with title in the top edge, and the body
// inside, padded. A body line that is wider than the box is wrapped at spaces (a longer word is broken), or see BoxHardWrap
// and BoxTruncate; a line that fits is kept exactly as it is, so a pre-laid-out body (a diff, a table) is not disturbed. The
// body's own styles survive. Control characters in the title or the body are removed. A title too long for the top edge is cut
// with an ellipsis and an empty title leaves a plain edge. Below a width of 4 there is no room for a border: the body comes back
// without one, cut to the width. A width below 1 gives nil.
func Box(title string, body []cell.Line, width int, th Theme, opts ...BoxOpt) []cell.Line {
	if width < 1 {
		return nil
	}
	cfg := newBoxCfg(opts)
	if width < 4 {
		out := make([]cell.Line, 0, len(body))
		for _, l := range body {
			out = append(out, clipLine(safeLine(l), width))
		}
		return out
	}
	g := th.glyphs()
	border := cfg.border
	if th.ASCII {
		border = BorderASCII
	}
	if int(border) >= len(boxBorderSets) {
		border = BorderRounded
	}
	br := boxBorderSets[border]
	padX, inner := cfg.geometry(width)

	edge, ttl := th.Border, th.Title
	if cfg.hasAccent && !th.Mono {
		edge.FG, ttl.FG = cfg.accent, cfg.accent
	}
	dash := func(n int) cell.Span { return cell.Span{Text: repeatText(br.h, n), Style: edge} }
	side := cell.Span{Text: br.v, Style: edge}

	var out []cell.Line
	top := cell.Line{{Text: br.tl, Style: edge}}
	space := cell.Span{Text: " ", Style: edge}
	title = strings.TrimSpace(safeOneLine(title))
	if tw := width - 6; tw >= 1 && title != "" {
		t := cell.Styled(ttl, title).Truncate(tw, g.ellipsis)
		top = append(top, dash(1), space)
		top = append(top, t...)
		top = append(top, space, dash(width-5-t.Width()), cell.Span{Text: br.tr, Style: edge})
	} else {
		top = append(top, dash(width-2), cell.Span{Text: br.tr, Style: edge})
	}
	out = append(out, top)

	row := func(l cell.Line) cell.Line {
		r := make(cell.Line, 0, len(l)+5)
		r = append(r, side)
		if padX > 0 {
			r = append(r, cell.Span{Text: repeatText(" ", padX)})
		}
		r = append(r, l.Pad(inner, cell.Style{})...)
		if padX > 0 {
			r = append(r, cell.Span{Text: repeatText(" ", padX)})
		}
		return append(r, side)
	}
	for i := 0; i < cfg.padY; i++ {
		out = append(out, row(nil))
	}
	cont := cell.Span{Text: g.cont, Style: th.Faint}
	for _, l := range body {
		l = safeLine(l)
		switch {
		case l.Width() <= inner:
			out = append(out, row(l))
		case cfg.wrap == wrapCut:
			out = append(out, row(l.Truncate(inner, g.ellipsis)))
		case cfg.wrap == wrapCells:
			for _, p := range cutRows(l, inner, cont) {
				out = append(out, row(p))
			}
		default:
			for _, p := range l.Wrap(inner, 0) {
				out = append(out, row(p))
			}
		}
	}
	for i := 0; i < cfg.padY; i++ {
		out = append(out, row(nil))
	}
	out = append(out, cell.Line{{Text: br.bl, Style: edge}, dash(width - 2), {Text: br.br, Style: edge}})
	return clipLines(out, width)
}
