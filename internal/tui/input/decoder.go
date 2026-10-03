package input

import (
	"bytes"
	"time"
	"unicode/utf8"
)

const (
	// DefaultMaxPaste is the most bytes of one bracketed paste a Decoder keeps (1 MiB); a Decoder's MaxPaste overrides it.
	DefaultMaxPaste = 1 << 20

	// EscGrace is how long a caller should wait for more bytes after an incomplete escape sequence (a lone ESC, for
	// instance) before it calls Flush. A terminal writes the whole sequence of one key press at once, so a gap this long
	// means the user pressed Esc.
	EscGrace = 40 * time.Millisecond

	// PasteGrace is the wait before Flush while a bracketed paste is open. A paste that stalls for a moment (a slow ssh link)
	// and is cut in two would type its second half as keystrokes, newlines included, so this is long.
	PasteGrace = 3 * time.Second

	// maxCSI is the longest parameter and intermediate run of a CSI or SS3 sequence that is interpreted. A longer one is
	// skipped up to its final byte without being buffered, so a hostile or broken stream cannot make the decoder hold memory.
	maxCSI = 64
)

// pasteEnd ends a bracketed paste (CSI 201 ~); the start marker, CSI 200 ~, is recognised with the other CSI sequences.
var pasteEnd = []byte("\x1b[201~")

type mode uint8

const (
	modeNormal     mode = iota
	modePaste           // inside a bracketed paste: everything up to the end marker is text
	modeSkipCSI         // inside an over-long CSI sequence: drop bytes up to its final byte
	modeSkipString      // inside an OSC, DCS, SOS, PM or APC string: drop bytes up to BEL or ST
)

// Decoder turns the bytes a terminal in raw mode sends into Keys. It is a pure state machine: no goroutines, no clock, no
// I/O. The caller reads bytes, passes them to Feed, and when Grace says so and nothing more has arrived, calls Flush.
//
// Feeding a byte stream in pieces, split at any byte boundary, yields the same keys as feeding it whole (an incomplete
// sequence waits in the decoder until the rest arrives), and Flush resolves whatever is still waiting. The zero value is
// ready to use. A Decoder is not safe for concurrent use.
//
// What it decodes: UTF-8 text (a rune split across reads is reassembled); C0 controls as Ctrl+letter (Ctrl+Space, Ctrl+\
// Ctrl+] Ctrl+^ Ctrl+_), with Tab, Enter (CR), Ctrl+Enter (LF) and Backspace (BS and DEL) named; ESC followed by a key is Alt
// plus that key; CSI and SS3 sequences for arrows, Home, End, Insert, Delete, PgUp, PgDn and F1 to F12 with xterm modifier
// parameters ("1;5A" is Ctrl+Up), Shift+Tab (CSI Z); CSI u and xterm modifyOtherKeys reports, which is how a terminal says
// Shift+Enter; and bracketed paste as one KindPaste key. Mouse reports, focus events, cursor position and device replies
// and every other sequence it does not know are consumed and dropped, never turned into text.
//
// Malformed input never fails and never panics: an invalid UTF-8 byte is reported as the rune U+FFFD, a C1 control
// character and an unknown or damaged escape sequence are dropped. Bytes held back are bounded (a few dozen, plus the
// paste being collected, at most MaxPaste).
type Decoder struct {
	// MaxPaste is the most bytes of one paste that are kept; 0 means DefaultMaxPaste. Bytes beyond it are read and thrown
	// away, and the paste key has Truncated set.
	MaxPaste int

	buf        []byte // bytes of one incomplete item, waiting for the rest
	mode       mode
	paste      []byte
	pasteTrunc bool
	strIntro   byte // the byte after ESC that opened the string being skipped
	strBytes   int  // payload bytes of that string seen so far
}

// maxPaste uses a positive configured paste-byte limit or the default limit.
func (d *Decoder) maxPaste() int {
	if d.MaxPaste > 0 {
		return d.MaxPaste
	}
	return DefaultMaxPaste
}

// Feed adds bytes read from the terminal and returns the keys that are now complete, in order. Bytes that might start a
// longer sequence (a lone ESC, half a UTF-8 rune, half a CSI) are kept until more arrive or Flush is called.
func (d *Decoder) Feed(p []byte) []Key {
	if len(p) == 0 {
		return nil
	}
	src := p
	if len(d.buf) > 0 {
		d.buf = append(d.buf, p...)
		src = d.buf
	}
	out, rest := d.run(nil, src, false)
	d.buf = append(d.buf[:0], rest...)
	if cap(d.buf) > 1<<16 { // do not pin the memory of one huge read
		d.buf = append([]byte(nil), d.buf...)
	}
	return out
}

// Flush says that no more bytes are coming for now and resolves what the decoder holds: a lone ESC is the Esc key, ESC [
// alone is Alt+[, a string introducer (ESC ] and the like) with nothing after it is the Alt key it looks like, half a
// UTF-8 rune is U+FFFD, a half-received escape sequence is dropped, and a paste still open is closed and returned with
// Truncated set. The caller calls it after Grace has passed with no new bytes; tests call it directly.
func (d *Decoder) Flush() []Key {
	out, _ := d.run(nil, d.buf, true)
	d.buf = d.buf[:0]
	switch d.mode {
	case modePaste:
		out = d.endPaste(out, true)
	case modeSkipString:
		if d.strBytes == 0 { // ESC ] (or P, X, ^, _) and nothing more: it was Alt plus that character
			out = append(out, RuneKey(rune(d.strIntro), Alt))
		}
	}
	d.mode = modeNormal
	return out
}

// Reset forgets everything the decoder holds, including an open paste, without producing keys.
func (d *Decoder) Reset() {
	d.buf = d.buf[:0]
	d.paste = d.paste[:0]
	d.mode, d.pasteTrunc, d.strBytes = modeNormal, false, 0
}

// Pending reports whether the decoder holds input that only more bytes, or Flush, can resolve.
func (d *Decoder) Pending() bool { return len(d.buf) > 0 || d.mode != modeNormal }

// InPaste reports whether a bracketed paste is open.
func (d *Decoder) InPaste() bool { return d.mode == modePaste }

// Grace is how long the caller should wait for more bytes before calling Flush: zero when nothing is pending, PasteGrace
// inside a paste, EscGrace otherwise. The wait belongs to the caller (a timer it restarts after every read), so the
// decoder itself never looks at a clock.
func (d *Decoder) Grace() time.Duration {
	switch {
	case !d.Pending():
		return 0
	case d.mode == modePaste:
		return PasteGrace
	}
	return EscGrace
}

// buffered is how many bytes the decoder is holding (tests check that it stays small).
func (d *Decoder) buffered() int { return len(d.buf) + len(d.paste) }

// run decodes b, appending keys to out, and returns the undecoded tail (an incomplete item; empty when final).
func (d *Decoder) run(out []Key, b []byte, final bool) ([]Key, []byte) {
	i := 0
	for i < len(b) {
		before := d.mode
		var n int
		switch before {
		case modePaste:
			n, out = d.stepPaste(b[i:], out, final)
		case modeSkipString:
			n = d.stepSkipString(b[i:])
		case modeSkipCSI:
			n = d.stepSkipCSI(b[i:])
		default:
			n, out = d.stepNormal(b[i:], out, final)
		}
		if n == 0 && d.mode == before {
			break // an incomplete item: wait for more bytes
		}
		i += n
	}
	return out, b[i:]
}

// stepNormal decodes one item at the start of b (not inside a paste or a skipped sequence) and returns the bytes it used;
// 0 with the mode unchanged means b is an incomplete item.
func (d *Decoder) stepNormal(b []byte, out []Key, final bool) (int, []Key) {
	if b[0] == 0x1b {
		return d.stepEsc(b, out, final)
	}
	k, n, ok := plainKey(b, final)
	if ok {
		out = append(out, k)
	}
	return n, out
}

// plainKey decodes the one key at the start of b, which does not begin with ESC. n == 0 means b is half a UTF-8 rune;
// ok == false with n > 0 means the bytes are consumed and mean nothing.
func plainKey(b []byte, final bool) (k Key, n int, ok bool) {
	c := b[0]
	switch {
	case c < 0x20 || c == 0x7f:
		return ctrlKey(c), 1, true
	case c < 0x80:
		return RuneKey(rune(c), 0), 1, true
	}
	if !final && !utf8.FullRune(b) {
		return Key{}, 0, false
	}
	r, n := utf8.DecodeRune(b)
	if r >= 0x80 && r <= 0x9f {
		return Key{}, n, false // a C1 control (8-bit CSI and friends): never text
	}
	return RuneKey(r, 0), n, true // an invalid byte is U+FFFD, one key per byte
}

// ctrlKey is the key for a C0 control byte or DEL.
func ctrlKey(c byte) Key {
	switch {
	case c == 0x00:
		return RuneKey(' ', Ctrl)
	case c == 0x08 || c == 0x7f:
		return SpecialKey(Backspace, 0)
	case c == 0x09:
		return SpecialKey(Tab, 0)
	case c == 0x0a:
		return SpecialKey(Enter, Ctrl)
	case c == 0x0d:
		return SpecialKey(Enter, 0)
	case c == 0x1b:
		return SpecialKey(Esc, 0)
	case c >= 0x01 && c <= 0x1a:
		return RuneKey(rune('a'+c-1), Ctrl)
	}
	return RuneKey(rune(c)+0x40, Ctrl) // 0x1c..0x1f: Ctrl+\ Ctrl+] Ctrl+^ Ctrl+_
}

// stepEsc decodes an item that starts with ESC: the Esc key, Alt plus a key, or a CSI, SS3 or string introducer.
func (d *Decoder) stepEsc(b []byte, out []Key, final bool) (int, []Key) {
	if len(b) == 1 {
		if !final {
			return 0, out
		}
		return 1, append(out, SpecialKey(Esc, 0))
	}
	switch x := b[1]; x {
	case '[':
		return d.stepCSI(b, out, final, 0)
	case 'O':
		return d.stepSS3(b, out, final, 0)
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: a string we do not use
		d.mode, d.strIntro, d.strBytes = modeSkipString, x, 0
		return 2, out
	case 0x1b:
		// ESC ESC [ A is Alt+Up on terminals that prefix the sequence with ESC; ESC ESC followed by anything else is Esc, Esc
		// (a double tap), and is never Alt+Esc: the editor's "press Esc again" must not depend on read boundaries.
		if len(b) < 3 {
			if !final {
				return 0, out
			}
			return 1, append(out, SpecialKey(Esc, 0))
		}
		var n int
		var o []Key
		switch b[2] {
		case '[':
			n, o = d.stepCSI(b[1:], out, false, Alt)
		case 'O':
			n, o = d.stepSS3(b[1:], out, false, Alt)
		default:
			return 1, append(out, SpecialKey(Esc, 0))
		}
		if n == 0 && d.mode == modeNormal { // the inner sequence is incomplete
			if !final {
				return 0, out
			}
			return 1, append(out, SpecialKey(Esc, 0))
		}
		return n + 1, o
	}
	k, n, ok := plainKey(b[1:], final)
	if n == 0 {
		return 0, out
	}
	if ok {
		k.Mod |= Alt
		out = append(out, k)
	}
	return n + 1, out
}

// stepSkipString drops the body of an OSC/DCS/SOS/PM/APC string. BEL or ESC \ ends it; CAN and SUB cancel it; any other ESC
// cancels it and starts a sequence of its own, so a string that never ends cannot swallow the keys typed after it for long.
func (d *Decoder) stepSkipString(b []byte) int {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case 0x07, 0x18, 0x1a:
			d.mode = modeNormal
			return i + 1
		case 0x1b:
			if i+1 >= len(b) {
				d.strBytes += i
				return i // the byte after ESC decides: keep the ESC
			}
			d.mode = modeNormal
			if b[i+1] == '\\' {
				return i + 2
			}
			return i
		}
	}
	d.strBytes += len(b)
	return len(b)
}

// stepSkipCSI drops the rest of an over-long CSI sequence.
func (d *Decoder) stepSkipCSI(b []byte) int {
	for i, c := range b {
		switch {
		case c >= 0x40 && c <= 0x7e:
			d.mode = modeNormal
			return i + 1
		case c >= 0x20 && c <= 0x3f:
		default:
			d.mode = modeNormal
			return i
		}
	}
	return len(b)
}

// stepPaste collects the text of a bracketed paste up to the end marker. Everything else, escape sequences included, is
// text: pastes do not nest, and a second start marker inside one is just more of it.
func (d *Decoder) stepPaste(b []byte, out []Key, final bool) (int, []Key) {
	i := 0
	for i < len(b) {
		e := bytes.IndexByte(b[i:], 0x1b)
		if e < 0 {
			d.collect(b[i:])
			return len(b), out
		}
		d.collect(b[i : i+e])
		i += e
		rest := b[i:]
		switch {
		case len(rest) >= len(pasteEnd):
			if bytes.HasPrefix(rest, pasteEnd) {
				return i + len(pasteEnd), d.endPaste(out, false)
			}
			d.collect(rest[:1]) // an ESC that is not the end marker is pasted text
			i++
		case bytes.HasPrefix(pasteEnd, rest): // the start of the marker, cut by the end of the read
			if final {
				return len(b), out // the stream ended inside the marker: drop its beginning
			}
			return i, out
		default:
			d.collect(rest[:1])
			i++
		}
	}
	return i, out
}

// collect adds pasted bytes, up to the size cap.
func (d *Decoder) collect(p []byte) {
	if len(p) == 0 {
		return
	}
	room := d.maxPaste() - len(d.paste)
	if room < len(p) {
		d.pasteTrunc = true
		if room < 0 {
			room = 0
		}
		p = p[:room]
	}
	d.paste = append(d.paste, p...)
}

// endPaste closes the paste in progress and appends its key (nothing for an empty paste).
func (d *Decoder) endPaste(out []Key, cut bool) []Key {
	text := d.paste
	trunc := d.pasteTrunc || cut
	if d.pasteTrunc {
		text = trimPartialRune(text) // the cap may have cut a rune in two
	}
	if len(text) > 0 {
		out = append(out, Key{Kind: KindPaste, Text: string(text), Truncated: trunc})
	}
	d.paste = d.paste[:0]
	if cap(d.paste) > 1<<16 {
		d.paste = nil
	}
	d.pasteTrunc = false
	d.mode = modeNormal
	return out
}

// trimPartialRune drops an incomplete UTF-8 sequence from the end of b.
func trimPartialRune(b []byte) []byte {
	for n := 1; n <= utf8.UTFMax && n <= len(b); n++ {
		if c := b[len(b)-n]; c >= 0xc0 { // a lead byte: is its sequence complete?
			if utf8.FullRune(b[len(b)-n:]) {
				return b
			}
			return b[:len(b)-n]
		} else if c < 0x80 {
			return b
		}
	}
	return b
}
