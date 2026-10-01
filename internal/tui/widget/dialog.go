package widget

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// DialogOption is one choice of a Dialog.
type DialogOption struct {
	Label string   // the text after the number: "Yes"
	Hint  string   // dim text after the label: "(esc)"
	Keys  []string // keys that choose this option, besides its number: "y", "esc" (see DialogKeys)
}

// PermissionOptions are the three choices of the permission prompt: 1 yes (key y), 2 yes and do not ask again for scope (key
// a), 3 no, and tell Sleipnir what to do instead (keys n and esc). scope is what the rule would cover, such as the command
// prefix "go test" or a tool name; empty leaves the sentence without it.
func PermissionOptions(scope string) []DialogOption {
	again := "Yes, and don't ask again"
	if scope = strings.TrimSpace(safeOneLine(scope)); scope != "" {
		again += " for " + scope
	}
	return []DialogOption{
		{Label: "Yes", Keys: []string{"y"}},
		{Label: again, Keys: []string{"a"}},
		{Label: "No, and tell Sleipnir what to do instead", Hint: "(esc)", Keys: []string{"n", "esc"}},
	}
}

// Dialog is the permission prompt: a box (amber when the theme has colours) with title, the body (the command, or a diff,
// shown in full and never truncated: the person must see what they are approving; long lines are wrapped exactly at the
// cell boundary, keeping every space, so callers bound the body themselves, for instance with DiffOptions.MaxLines), a blank
// row, and the options numbered 1., 2., ... The selected option (an index into options) is marked with ❯ (> in ASCII) and drawn
// bold on the theme's SelectedRow style, which is reverse video in MonoTheme, so it is obvious with or without colour.
// A selected index outside the options marks none. Option labels wrap with a hanging indent under the label. Control
// characters in title, labels and body are removed. Render a body that is already laid out (a diff) at BoxInnerWidth(width,
// BoxHardWrap()) so the box does not have to wrap it. A width below 1 gives nil; a very narrow one loses the border (see Box).
func Dialog(title string, body []cell.Line, options []DialogOption, selected int, width int, th Theme) []cell.Line {
	if width < 1 {
		return nil
	}
	opts := []BoxOpt{BoxHardWrap(), BoxAccent(th.Warn.FG)}
	inner := BoxInnerWidth(width, opts...)
	content := make([]cell.Line, 0, len(body)+len(options)+1)
	content = append(content, body...)
	if len(body) > 0 && len(options) > 0 {
		content = append(content, nil)
	}
	if inner < 1 {
		inner = width // too narrow for a border: the options are drawn bare
	}
	content = append(content, dialogOptionLines(options, selected, inner, th)...)
	return Box(title, content, width, th, opts...)
}

func dialogOptionLines(options []DialogOption, selected, width int, th Theme) []cell.Line {
	g := th.glyphs()
	numW := len(strconv.Itoa(len(options)))
	var out []cell.Line
	for i, o := range options {
		sel := i == selected
		marker, markStyle, numStyle, labelStyle := "  ", th.Text, th.Dim, th.Text
		if sel {
			marker = g.sel + " "
			markStyle = composeStyle(th.Accent, cell.Style{Attr: cell.Bold})
			numStyle, labelStyle = th.Strong, th.Strong
		}
		num := fmt.Sprintf("%*d. ", numW, i+1)
		prefixW := cell.StringWidth(marker) + len(num)
		text := cell.Styled(labelStyle, strings.TrimSpace(safeOneLine(o.Label)))
		if hint := strings.TrimSpace(safeOneLine(o.Hint)); hint != "" {
			text = append(text, cell.Span{Text: " " + hint, Style: th.Dim})
		}
		for j, w := range text.Wrap(max(1, width-prefixW), 0) {
			var row cell.Line
			if j == 0 {
				row = cell.Line{{Text: marker, Style: markStyle}, {Text: num, Style: numStyle}}
			} else {
				row = cell.Spaces(prefixW, cell.Style{})
			}
			row = append(row, w...)
			if sel {
				row = overlayStyle(row.Pad(width, cell.Style{}), th.SelectedRow)
			}
			out = append(out, row)
		}
	}
	return out
}

// DialogKeys maps a pressed key to the option it chooses. key is the key's name as the input layer reports it: a character
// ("y", "1", "N") or a name ("esc", "enter"); names compare case-insensitively, and "escape" is "esc", "return" is "enter".
// An option is chosen by any of its Keys; failing that, a digit 1..9 chooses the option in that position (so the numbers
// drawn by Dialog always work, unless a key list claims the digit for another option). When several options list the same
// key the first wins. ok is false for an empty or unknown key, a digit with no such option, or no options; index is then 0.
// Enter and the arrow keys are not handled here: they act on the selected option, which the caller tracks.
func DialogKeys(key string, options []DialogOption) (index int, ok bool) {
	k := dialogKey(key)
	if k == "" {
		return 0, false
	}
	for i, o := range options {
		for _, name := range o.Keys {
			if dialogKey(name) == k {
				return i, true
			}
		}
	}
	if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
		if n := int(k[0] - '0'); n <= len(options) {
			return n - 1, true
		}
	}
	return 0, false
}

func dialogKey(s string) string {
	if s == " " {
		return "space"
	}
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "escape":
		return "esc"
	case "return":
		return "enter"
	case "spacebar", "space bar":
		return "space"
	}
	return s
}
