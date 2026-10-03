package app

import (
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// Look says how the chat draws itself: the colours (a Theme for the text widgets, a Palette for the signature ones, which are not
// unified yet), whether the terminal can be trusted with more than ASCII, and whether anything moves.
type Look struct {
	Theme   widget.Theme
	Palette widget.Palette
	Unicode bool
	// Anim lets the spinner turn and the sweep, the fold and the flash play. Without it the screen changes only when something
	// happens (and the clocks that count seconds still count).
	Anim bool
}

// LookFor is the look a terminal gets. Colour comes from the depth it shows (none: the attributes bold, dim, italic, underline
// and reverse stand in for it, which is what NO_COLOR asks for), glyphs from its locale, and the motion from what the
// environment allows (term.Caps.Anim: NO_COLOR, REDUCE_MOTION, SLEIPNIR_ANIM) less what noAnim (--no-anim) takes away.
//
// A terminal's background cannot be read without asking it, so the colours are the dark ones.
func LookFor(caps term.Caps, noAnim bool) Look {
	mono := caps.Color == term.ColorNone
	l := Look{Theme: widget.ThemeFor(!mono, false), Palette: widget.DefaultPalette(), Unicode: caps.Unicode, Anim: caps.Anim && !noAnim}
	if mono {
		l.Palette = widget.MonoPalette()
	}
	l.Theme = l.Theme.WithASCII(!caps.Unicode)
	return l
}

// chatGlyphs are the pictures the chat draws besides what the widgets draw themselves, in both flavours.
type chatGlyphs struct {
	bullet   string // in front of the assistant's words and of a tool call
	result   string // in front of a tool's output
	prompt   string // in front of what the person typed
	ok, fail string
	warn     string
	compact  string // a compaction
	queued   string // a line typed ahead
	ask      string // a question waits
	ellipsis string
	up, down string // tokens sent and received
	approx   string
	arrow    string // from one size to the next
	dot      string // between the parts of a line
	rule     string // the start of a turn's summary
	spinner  func(frame int) string
	still    string // the spinner when nothing moves
}

var (
	uniChat = chatGlyphs{bullet: "●", result: "⎿", prompt: "❯", ok: "✓", fail: "✗", warn: "⚠", compact: "◆", queued: "⏎", ask: "?", ellipsis: "…",
		up: "↑", down: "↓", approx: "≈", arrow: "→", dot: "·", rule: "──", spinner: widget.Spinner, still: "●"}
	asciiChat = chatGlyphs{bullet: "*", result: "|_", prompt: ">", ok: "ok", fail: "x", warn: "!", compact: "<>", queued: ">>", ask: "?", ellipsis: "...",
		up: "^", down: "v", approx: "~", arrow: "->", dot: "-", rule: "--", spinner: widget.SpinnerASCII, still: "*"}
)

// chatLook is a Look with what is derived from it once: the styles of this package's own text and the glyph set.
type chatLook struct {
	Look
	st   styles
	g    *chatGlyphs
	mono bool
}

// newChatLook derives chat styles and selects glyphs from the configured palette, theme, and
// Unicode capability.
func newChatLook(l Look) *chatLook {
	k := &chatLook{Look: l, st: stylesOf(l.Palette), g: &asciiChat, mono: l.Theme.Mono}
	if l.Unicode {
		k.g = &uniChat
	}
	return k
}

// inputTheme is the editor's look: the harness's violet prompt, and attributes alone where there is no colour.
func (k *chatLook) inputTheme() input.Theme {
	if !k.mono {
		th := input.DefaultTheme()
		if !k.Unicode {
			th.Marker = ">"
		}
		return th
	}
	dim := cell.Style{Attr: cell.Dim}
	th := input.Theme{
		Prompt:      cell.Style{Attr: cell.Bold},
		Placeholder: cell.Style{Attr: cell.Dim | cell.Italic},
		Chip:        cell.Style{Attr: cell.Reverse},
		Selected:    cell.Style{Attr: cell.Reverse},
		Dim:         dim,
		Marker:      "▸",
	}
	if !k.Unicode {
		th.Marker = ">"
	}
	return th
}

// violet is the colour of the harness itself (docs/UX.md): the banner, the prompt and the fold.
func (k *chatLook) accent() cell.Style { return k.st.accent }
