package input

import "github.com/anemos-labs/sleipnir/internal/tui/cell"

// Theme is how the editor's view is styled. The zero Style is the terminal's own look, so a Theme of zero values draws plain
// text. The renderer decides what a colour becomes on a terminal with fewer colours.
type Theme struct {
	Prompt      cell.Style // the prompt prefix ("❯ ")
	Text        cell.Style // what the user typed
	Placeholder cell.Style // the placeholder shown while the buffer is empty
	Chip        cell.Style // a paste chip
	Menu        cell.Style // a completion row
	Selected    cell.Style // the selected completion row
	Dim         cell.Style // hints, details, the search label
	Marker      string     // marks the selected menu row; default "▸"
}

// DefaultTheme uses the harness's violet for the prompt (docs/UX.md: violet is the harness itself), and attributes that
// survive any colour depth for everything else.
func DefaultTheme() Theme {
	dim := cell.Style{}.Fg(cell.Indexed(244))
	return Theme{
		Prompt:      cell.Style{}.Fg(cell.RGB(0xa7, 0x8b, 0xfa)).With(cell.Bold),
		Placeholder: dim.With(cell.Italic),
		Chip:        cell.Style{}.Fg(cell.Indexed(252)).Bg(cell.Indexed(238)),
		Menu:        cell.Style{}.Fg(cell.Indexed(250)),
		Selected:    cell.Style{}.With(cell.Reverse),
		Dim:         dim,
		Marker:      "▸",
	}
}
