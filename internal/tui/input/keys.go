package input

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind says which field of a Key carries the key.
type Kind uint8

const (
	KindRune    Kind = iota // a character: Key.R (Mod may hold Alt and Ctrl; Shift is implied by the rune)
	KindSpecial             // a named key: Key.Code
	KindPaste               // a bracketed paste: Key.Text
)

// Mod is a set of modifier keys held with a key.
type Mod uint8

const (
	Shift Mod = 1 << iota
	Alt
	Ctrl
)

// Special names a key that is not a character.
type Special uint8

const (
	NoSpecial Special = iota // the zero value: not a special key
	Enter                    // CR; LF (what Ctrl+J sends) is Enter with Ctrl held, so a caller can tell them apart
	Tab                      // Shift+Tab is Tab with Shift
	Backspace                // DEL and BS
	Esc
	Up
	Down
	Left
	Right
	Home
	End
	Insert
	Delete
	PgUp
	PgDn
	F1
	F2
	F3
	F4
	F5
	F6
	F7
	F8
	F9
	F10
	F11
	F12
)

var specialNames = [...]string{
	NoSpecial: "none", Enter: "enter", Tab: "tab", Backspace: "backspace", Esc: "esc", Up: "up", Down: "down", Left: "left",
	Right: "right", Home: "home", End: "end", Insert: "insert", Delete: "delete", PgUp: "pgup", PgDn: "pgdn",
	F1: "f1", F2: "f2", F3: "f3", F4: "f4", F5: "f5", F6: "f6", F7: "f7", F8: "f8", F9: "f9", F10: "f10", F11: "f11", F12: "f12",
}

// String is the key's name as used in the docs: "enter", "pgup", "f5".
func (s Special) String() string {
	if int(s) < len(specialNames) {
		return specialNames[s]
	}
	return "special(" + strconv.Itoa(int(s)) + ")"
}

// Key is one decoded key press or one paste.
type Key struct {
	Kind Kind
	R    rune    // KindRune: the character. Ctrl+letter keys carry the lower-case letter; Ctrl+Space carries ' '.
	Code Special // KindSpecial
	Mod  Mod
	// Text is the pasted text of a KindPaste key, exactly as the terminal sent it (it may still hold control characters
	// and invalid UTF-8: the Editor cleans it). Empty for every other kind.
	Text string
	// Truncated is set on a KindPaste key whose Text is not the whole paste: it went over the Decoder's size cap, or the
	// paste never ended and was closed by Flush.
	Truncated bool
}

// RuneKey is the key for a character with modifiers.
func RuneKey(r rune, m Mod) Key { return Key{Kind: KindRune, R: r, Mod: m} }

// SpecialKey is the key for a named key with modifiers.
func SpecialKey(c Special, m Mod) Key { return Key{Kind: KindSpecial, Code: c, Mod: m} }

// PasteKey is the key for a paste of text.
func PasteKey(text string) Key { return Key{Kind: KindPaste, Text: text} }

// Is reports whether k is the named key with exactly the modifiers m.
func (k Key) Is(c Special, m Mod) bool { return k.Kind == KindSpecial && k.Code == c && k.Mod == m }

// IsRune reports whether k is the character r with exactly the modifiers m.
func (k Key) IsRune(r rune, m Mod) bool { return k.Kind == KindRune && k.R == r && k.Mod == m }

// String is a short name for logs and test failures: "ctrl+a", "alt+enter", "shift+tab", "paste(12 bytes)".
func (k Key) String() string {
	if k.Kind == KindPaste {
		s := "paste(" + strconv.Itoa(len(k.Text)) + " bytes"
		if k.Truncated {
			s += ", truncated"
		}
		return s + ")"
	}
	var b strings.Builder
	if k.Mod&Ctrl != 0 {
		b.WriteString("ctrl+")
	}
	if k.Mod&Alt != 0 {
		b.WriteString("alt+")
	}
	if k.Mod&Shift != 0 {
		b.WriteString("shift+")
	}
	switch k.Kind {
	case KindSpecial:
		b.WriteString(k.Code.String())
	case KindRune:
		switch {
		case k.R == ' ':
			b.WriteString("space")
		case k.R < 0x20 || k.R == 0x7f || !utf8.ValidRune(k.R):
			b.WriteString("U+" + strconv.FormatInt(int64(k.R), 16))
		default:
			b.WriteRune(k.R)
		}
	default:
		b.WriteString("?")
	}
	return b.String()
}
