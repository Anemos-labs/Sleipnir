package input

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// truncatedMarker ends a paste that was cut at the size cap, so neither the user nor the model can mistake it for the whole.
const truncatedMarker = "[truncated]"

// chip is a paste that is stored aside and shown as a one-rune label in the buffer.
type chip struct {
	text  string // the pasted text, cleaned: what Submit expands the chip to
	label string // what the view draws
}

func (e *Editor) chipLabel(r rune) string {
	if id := chipID(r); id >= 0 && id < len(e.chips) {
		return e.chips[id].label
	}
	return "[pasted text]"
}

// paste inserts a bracketed paste. The text is cleaned first (control characters and escape sequences removed, line endings
// normalised, invisible characters dropped, see cleanText), and capped at MaxPasteBytes. A paste of up to PasteLines lines
// and PasteRunes runes goes in as if typed; a larger one is kept aside as a chip that moves and deletes as a single unit and
// is expanded to the real text on Submit.
func (e *Editor) paste(k Key) {
	text, trunc := k.Text, k.Truncated
	if len(text) > e.opt.MaxPasteBytes {
		text, trunc = cutUTF8(text, e.opt.MaxPasteBytes), true
	}
	text = cleanText(text)
	if len(text) > e.opt.MaxPasteBytes {
		text, trunc = cutUTF8(text, e.opt.MaxPasteBytes), true
	}
	if trunc {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += truncatedMarker
	}
	if text == "" {
		return
	}
	lines := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		lines++
	}
	runes := utf8.RuneCountInString(text)
	if !trunc && lines <= e.opt.PasteLines && runes <= e.opt.PasteRunes {
		rs := []rune(text)
		e.replace(e.cur, e.cur, rs, e.cur+len(rs), grpNone)
		return
	}
	if len(e.chips) >= maxChips || e.chipBytes+len(text) > maxChipBytes { // too much set aside already: keep the start, inline, rather than lose the paste
		rs := []rune(text)
		if len(rs) > e.opt.PasteRunes {
			rs = append(rs[:e.opt.PasteRunes:e.opt.PasteRunes], []rune("…"+truncatedMarker)...)
		}
		e.replace(e.cur, e.cur, rs, e.cur+len(rs), grpNone)
		return
	}
	id := len(e.chips)
	e.chips = append(e.chips, chip{text: text, label: chipLabel(id+1, lines, runes, trunc)})
	e.chipBytes += len(text)
	e.replace(e.cur, e.cur, []rune{chipRune(id)}, e.cur+1, grpNone)
}

// chipLabel is the text drawn for chip number n: "[pasted text #2 +312 lines]", or for a long single line
// "[pasted text #2 1250 chars]".
func chipLabel(n, lines, runes int, trunc bool) string {
	extra := ""
	if trunc {
		extra = ", truncated"
	}
	if lines > 1 {
		return fmt.Sprintf("[pasted text #%d +%d lines%s]", n, lines, extra)
	}
	return fmt.Sprintf("[pasted text #%d %d chars%s]", n, runes, extra)
}

// cutUTF8 cuts s to at most max bytes without splitting a rune.
func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
