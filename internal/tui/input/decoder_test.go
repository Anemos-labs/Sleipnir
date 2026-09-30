package input

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func kr(r rune, m Mod) Key      { return RuneKey(r, m) }
func ks(c Special, m Mod) Key   { return SpecialKey(c, m) }
func kp(s string, cut bool) Key { return Key{Kind: KindPaste, Text: s, Truncated: cut} }
func runes(s string, m Mod) []Key {
	var out []Key
	for _, r := range s {
		out = append(out, kr(r, m))
	}
	return out
}

func cat(parts ...[]Key) []Key {
	var out []Key
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func one(k Key) []Key { return []Key{k} }

// decodeWhole feeds s in one piece and then flushes.
func decodeWhole(s string) []Key {
	var d Decoder
	return append(d.Feed([]byte(s)), d.Flush()...)
}

// decodeSplit feeds s in the given chunks, flushing at the end only.
func decodeSplit(d *Decoder, s string, cuts ...int) []Key {
	var out []Key
	prev := 0
	for _, c := range append(cuts, len(s)) {
		out = append(out, d.Feed([]byte(s[prev:c]))...)
		prev = c
	}
	return append(out, d.Flush()...)
}

var decodeCases = []struct {
	name string
	in   string
	want []Key
}{
	{"ascii", "abc", runes("abc", 0)},
	{"upper case and symbols", "A~{}", runes("A~{}", 0)},
	{"space", " ", one(kr(' ', 0))},
	{"utf-8", "é中🐎", runes("é中🐎", 0)},
	{"ctrl letters", "\x01\x02\x03\x04\x05\x06\x07\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a",
		[]Key{kr('a', Ctrl), kr('b', Ctrl), kr('c', Ctrl), kr('d', Ctrl), kr('e', Ctrl), kr('f', Ctrl), kr('g', Ctrl),
			kr('k', Ctrl), kr('l', Ctrl), kr('n', Ctrl), kr('o', Ctrl), kr('p', Ctrl), kr('q', Ctrl), kr('r', Ctrl),
			kr('s', Ctrl), kr('t', Ctrl), kr('u', Ctrl), kr('v', Ctrl), kr('w', Ctrl), kr('x', Ctrl), kr('y', Ctrl), kr('z', Ctrl)}},
	{"ctrl space", "\x00", one(kr(' ', Ctrl))},
	{"ctrl backslash bracket caret underscore", "\x1c\x1d\x1e\x1f", []Key{kr('\\', Ctrl), kr(']', Ctrl), kr('^', Ctrl), kr('_', Ctrl)}},
	{"tab", "\t", one(ks(Tab, 0))},
	{"enter is CR", "\r", one(ks(Enter, 0))},
	{"LF is ctrl+enter, which is ctrl+j", "\n", one(ks(Enter, Ctrl))},
	{"backspace DEL", "\x7f", one(ks(Backspace, 0))},
	{"backspace BS", "\x08", one(ks(Backspace, 0))},
	{"lone esc needs a flush", "\x1b", one(ks(Esc, 0))},
	{"esc then text", "\x1b\x1b", []Key{ks(Esc, 0), ks(Esc, 0)}},
	{"esc esc x is a double tap, then alt+x", "\x1b\x1bx", []Key{ks(Esc, 0), kr('x', Alt)}},
	{"arrows CSI", "\x1b[A\x1b[B\x1b[C\x1b[D", []Key{ks(Up, 0), ks(Down, 0), ks(Right, 0), ks(Left, 0)}},
	{"arrows SS3", "\x1bOA\x1bOB\x1bOC\x1bOD", []Key{ks(Up, 0), ks(Down, 0), ks(Right, 0), ks(Left, 0)}},
	{"home and end, every variant", "\x1b[H\x1b[F\x1b[1~\x1b[4~\x1b[7~\x1b[8~\x1bOH\x1bOF",
		[]Key{ks(Home, 0), ks(End, 0), ks(Home, 0), ks(End, 0), ks(Home, 0), ks(End, 0), ks(Home, 0), ks(End, 0)}},
	{"insert delete pgup pgdn", "\x1b[2~\x1b[3~\x1b[5~\x1b[6~", []Key{ks(Insert, 0), ks(Delete, 0), ks(PgUp, 0), ks(PgDn, 0)}},
	{"F1 to F4 as SS3", "\x1bOP\x1bOQ\x1bOR\x1bOS", []Key{ks(F1, 0), ks(F2, 0), ks(F3, 0), ks(F4, 0)}},
	{"F1 to F4 as CSI", "\x1b[P\x1b[Q\x1b[R\x1b[S", []Key{ks(F1, 0), ks(F2, 0), ks(F3, 0), ks(F4, 0)}},
	{"F1 to F12 as CSI n ~", "\x1b[11~\x1b[12~\x1b[13~\x1b[14~\x1b[15~\x1b[17~\x1b[18~\x1b[19~\x1b[20~\x1b[21~\x1b[23~\x1b[24~",
		[]Key{ks(F1, 0), ks(F2, 0), ks(F3, 0), ks(F4, 0), ks(F5, 0), ks(F6, 0), ks(F7, 0), ks(F8, 0), ks(F9, 0), ks(F10, 0), ks(F11, 0), ks(F12, 0)}},
	{"linux console F1 to F5", "\x1b[[A\x1b[[B\x1b[[C\x1b[[D\x1b[[E", []Key{ks(F1, 0), ks(F2, 0), ks(F3, 0), ks(F4, 0), ks(F5, 0)}},
	{"shift+tab", "\x1b[Z", one(ks(Tab, Shift))},
	{"modifier 2 to 8 on an arrow", "\x1b[1;2A\x1b[1;3A\x1b[1;4A\x1b[1;5A\x1b[1;6A\x1b[1;7A\x1b[1;8A",
		[]Key{ks(Up, Shift), ks(Up, Alt), ks(Up, Shift|Alt), ks(Up, Ctrl), ks(Up, Ctrl|Shift), ks(Up, Ctrl|Alt), ks(Up, Ctrl|Alt|Shift)}},
	{"modifiers on the other cursor keys", "\x1b[1;5C\x1b[1;5D\x1b[1;3H\x1b[1;5F\x1b[1;2B",
		[]Key{ks(Right, Ctrl), ks(Left, Ctrl), ks(Home, Alt), ks(End, Ctrl), ks(Down, Shift)}},
	{"modifiers on tilde keys", "\x1b[3;5~\x1b[5;2~\x1b[2;3~\x1b[15;3~\x1b[24;6~",
		[]Key{ks(Delete, Ctrl), ks(PgUp, Shift), ks(Insert, Alt), ks(F5, Alt), ks(F12, Ctrl|Shift)}},
	{"modifiers on F1 to F4", "\x1b[1;5P\x1b[1;2Q\x1b[1;3R\x1b[1;4S", []Key{ks(F1, Ctrl), ks(F2, Shift), ks(F3, Alt), ks(F4, Shift|Alt)}},
	{"SS3 with a modifier", "\x1bO5A\x1bO1;2P\x1bO2D", []Key{ks(Up, Ctrl), ks(F1, Shift), ks(Left, Shift)}},
	{"alt+letter", "\x1ba\x1bB\x1b1", []Key{kr('a', Alt), kr('B', Alt), kr('1', Alt)}},
	{"alt+enter", "\x1b\r", one(ks(Enter, Alt))},
	{"alt+LF", "\x1b\n", one(ks(Enter, Alt|Ctrl))},
	{"alt+backspace", "\x1b\x7f", one(ks(Backspace, Alt))},
	{"alt+tab", "\x1b\t", one(ks(Tab, Alt))},
	{"alt+ctrl+letter", "\x1b\x01", one(kr('a', Alt|Ctrl))},
	{"alt+utf-8", "\x1bé\x1b中", []Key{kr('é', Alt), kr('中', Alt)}},
	{"alt+[ and alt+O need a flush", "\x1b[", one(kr('[', Alt))},
	{"alt+O", "\x1bO", one(kr('O', Alt))},
	{"alt+[ before an ordinary key", "\x1b[\rx", []Key{kr('[', Alt), ks(Enter, 0), kr('x', 0)}},
	{"alt+O before an ordinary key", "\x1bO\rx", []Key{kr('O', Alt), ks(Enter, 0), kr('x', 0)}},
	{"alt+arrow the old way", "\x1b\x1b[A\x1b\x1bOB", []Key{ks(Up, Alt), ks(Down, Alt)}},
	{"alt+shift+P, X, ^, _ and ] are keys when nothing follows", "\x1bP", one(kr('P', Alt))},
	{"alt+underscore", "\x1b_", one(kr('_', Alt))},
	{"bracketed paste", "\x1b[200~hello\x1b[201~", one(kp("hello", false))},
	{"paste keeps newlines and escapes as text", "\x1b[200~a\r\nb\x1b[31mred\x1b[0m\x1b[201~", one(kp("a\r\nb\x1b[31mred\x1b[0m", false))},
	{"paste between keys", "x\x1b[200~y\x1b[201~z", []Key{kr('x', 0), kp("y", false), kr('z', 0)}},
	{"two pastes", "\x1b[200~a\x1b[201~\x1b[200~b\x1b[201~", []Key{kp("a", false), kp("b", false)}},
	{"an empty paste is nothing", "\x1b[200~\x1b[201~q", one(kr('q', 0))},
	{"a nested paste start is text and the first end marker ends it", "\x1b[200~a\x1b[200~b\x1b[201~c\x1b[201~",
		[]Key{kp("a\x1b[200~b", false), kr('c', 0)}},
	{"an unterminated paste is closed by flush and marked", "\x1b[200~abc", one(kp("abc", true))},
	{"a cut end marker at the end of the stream is dropped", "\x1b[200~abc\x1b[20", one(kp("abc", true))},
	{"an ESC that is not the end marker is text", "\x1b[200~a\x1b[2Jb\x1b[201~", one(kp("a\x1b[2Jb", false))},
	{"utf-8 in a paste", "\x1b[200~héllo 中\x1b[201~", one(kp("héllo 中", false))},
	{"SGR mouse reports are dropped", "\x1b[<0;10;20Ma\x1b[<0;10;20m\x1b[<64;1;1Mb", runes("ab", 0)},
	{"legacy mouse report is dropped", "\x1b[M !!a", one(kr('a', 0))},
	{"legacy mouse report with high bytes", "\x1b[M\xff\xe0\x81a", one(kr('a', 0))},
	{"focus events are dropped", "\x1b[I\x1b[Oa", one(kr('a', 0))},
	{"unknown CSI is dropped", "\x1b[99;99Xa\x1b[?62;1;2ca\x1b[?2004;1$ya\x1b[0 qa", runes("aaaa", 0)},
	{"cursor position report is not F3", "\x1b[12;40Ra", one(kr('a', 0))},
	{"kitty keyboard query reply is dropped", "\x1b[?0ua", one(kr('a', 0))},
	{"unknown SS3 is dropped", "\x1bOxa", one(kr('a', 0))},
	{"F13 and up are dropped", "\x1b[25~\x1b[34~a", one(kr('a', 0))},
	{"OSC ended by BEL", "\x1b]0;a title\x07x", one(kr('x', 0))},
	{"OSC ended by ST", "\x1b]11;rgb:0000/0000/0000\x1b\\x", one(kr('x', 0))},
	{"DCS ended by ST", "\x1bP>|xterm(388)\x1b\\x", one(kr('x', 0))},
	{"APC and PM and SOS", "\x1b_Gi=1;OK\x1b\\\x1b^pm\x1b\\\x1bXsos\x1b\\x", one(kr('x', 0))},
	{"an ESC inside a string ends it and starts a sequence", "\x1b]0;t\x1b[Ax", []Key{ks(Up, 0), kr('x', 0)}},
	{"CAN cancels a string", "\x1b]0;t\x18x", one(kr('x', 0))},
	{"an unterminated string with a body is dropped at flush", "\x1b]0;title", nil},
	{"CSI u: shift+enter and ctrl+enter", "\x1b[13;2u\x1b[13;5u\x1b[13u", []Key{ks(Enter, Shift), ks(Enter, Ctrl), ks(Enter, 0)}},
	{"CSI u: letters", "\x1b[97;5u\x1b[97;2u\x1b[97;6u\x1b[97;3u\x1b[228u", []Key{kr('a', Ctrl), kr('A', 0), kr('a', Ctrl|Shift), kr('a', Alt), kr('ä', 0)}},
	{"CSI u: named keys", "\x1b[27u\x1b[9;2u\x1b[127u\x1b[8u\x1b[32;5u", []Key{ks(Esc, 0), ks(Tab, Shift), ks(Backspace, 0), ks(Backspace, 0), kr(' ', Ctrl)}},
	{"CSI u: kitty subfields and a release", "\x1b[97:65;2u\x1b[97;1:3u\x1b[97;1:1u", []Key{kr('A', 0), kr('a', 0)}},
	{"CSI u: private-use key codes and Super are dropped", "\x1b[57441u\x1b[97;9ua", one(kr('a', 0))},
	{"modifyOtherKeys", "\x1b[27;2;13~\x1b[27;5;9~\x1b[27;3;120~", []Key{ks(Enter, Shift), ks(Tab, Ctrl), kr('x', Alt)}},
	{"invalid UTF-8 is one U+FFFD per byte", "a\xffb\xc3(\xe4\xb8", []Key{kr('a', 0), kr('\U0000fffd', 0), kr('b', 0), kr('\U0000fffd', 0), kr('(', 0), kr('\U0000fffd', 0), kr('\U0000fffd', 0)}},
	{"C1 controls are dropped", "\xc2\x9ba\xc2\x85b", runes("ab", 0)},
	{"a damaged CSI is dropped and the byte after it is read again", "\x1b[1;\rx", []Key{ks(Enter, 0), kr('x', 0)}},
	{"a CSI cut by another ESC", "\x1b[1;\x1b[Ax", []Key{ks(Up, 0), kr('x', 0)}},
	{"NUL-terminated garbage does not loop", "\x1b[\x00\x00", []Key{kr('[', Alt), kr(' ', Ctrl), kr(' ', Ctrl)}},
}

func TestDecode(t *testing.T) {
	for _, c := range decodeCases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeWhole(c.in); !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Errorf("Feed+Flush(%q)\n got  %v\n want %v", c.in, got, c.want)
			}
		})
	}
}

func TestDecodeSplitAtEveryBoundaryAndByteByByte(t *testing.T) {
	for _, c := range decodeCases {
		t.Run(c.name, func(t *testing.T) {
			whole := decodeWhole(c.in)
			for i := 0; i <= len(c.in); i++ {
				var d Decoder
				if got := decodeSplit(&d, c.in, i); !reflect.DeepEqual(got, whole) {
					t.Fatalf("split at %d of %q\n got  %v\n want %v", i, c.in, got, whole)
				}
			}
			for i := 0; i <= len(c.in); i++ {
				for j := i; j <= len(c.in); j++ {
					var d Decoder
					if got := decodeSplit(&d, c.in, i, j); !reflect.DeepEqual(got, whole) {
						t.Fatalf("split at %d,%d of %q\n got  %v\n want %v", i, j, c.in, got, whole)
					}
				}
			}
			var d Decoder
			var out []Key
			for i := 0; i < len(c.in); i++ {
				out = append(out, d.Feed([]byte{c.in[i]})...)
			}
			out = append(out, d.Flush()...)
			if !reflect.DeepEqual(out, whole) {
				t.Fatalf("byte by byte %q\n got  %v\n want %v", c.in, out, whole)
			}
		})
	}
}

// randomStream builds a byte stream that is mostly the pieces terminals send, with damage: the decoder must give the same
// keys however it is cut.
func randomStream(rng *rand.Rand, n int) string {
	frags := []string{"\x1b", "\x1b[", "\x1bO", "[", "O", ";", ":", "~", "1", "2", "5", "0", "<", "?", "M", "m", "A", "Z", "u", "I",
		"\x1b[200~", "\x1b[201~", "\x1b]", "\x07", "\x1b\\", "\x1bP", "\x1b_", "\r", "\n", "\t", "\x7f", "\x00", "\x01", "\x18",
		"a", "é", "中", "🐎", "\xff", "\xc3", "\xe4\xb8", "\xc2\x9b", "hello", " ", "\x1b[1;5A", "\x1b[13;2u", "\x1b[27;2;13~", "\x1b[M"}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if rng.Intn(6) == 0 {
			sb.WriteByte(byte(rng.Intn(256)))
		} else {
			sb.WriteString(frags[rng.Intn(len(frags))])
		}
	}
	return sb.String()
}

func TestDecodeSplitInvarianceOnRandomStreams(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 400; iter++ {
		in := randomStream(rng, 1+rng.Intn(24))
		for _, max := range []int{0, 7} {
			mk := func() *Decoder { return &Decoder{MaxPaste: max} }
			whole := decodeSplit(mk(), in)
			for i := 0; i <= len(in); i++ {
				if got := decodeSplit(mk(), in, i); !reflect.DeepEqual(got, whole) {
					t.Fatalf("iteration %d (MaxPaste %d): split at %d of %q\n got  %v\n want %v", iter, max, i, in, got, whole)
				}
			}
			d := mk()
			var out []Key
			for i := 0; i < len(in); i++ {
				out = append(out, d.Feed([]byte{in[i]})...)
			}
			out = append(out, d.Flush()...)
			if !reflect.DeepEqual(out, whole) {
				t.Fatalf("iteration %d (MaxPaste %d): byte by byte %q\n got  %v\n want %v", iter, max, in, out, whole)
			}
			// a few random multi-way splits
			for k := 0; k < 4; k++ {
				var cuts []int
				for p := 0; p < len(in); p++ {
					if rng.Intn(3) == 0 {
						cuts = append(cuts, p)
					}
				}
				if got := decodeSplit(mk(), in, cuts...); !reflect.DeepEqual(got, whole) {
					t.Fatalf("iteration %d: cuts %v of %q\n got  %v\n want %v", iter, cuts, in, got, whole)
				}
			}
		}
	}
}

func TestDecoderReusableAfterFlush(t *testing.T) {
	var d Decoder
	if got := d.Feed([]byte("\x1b")); len(got) != 0 || !d.Pending() {
		t.Fatalf("a lone ESC waits: %v pending=%v", got, d.Pending())
	}
	if got := d.Flush(); !reflect.DeepEqual(got, one(ks(Esc, 0))) || d.Pending() {
		t.Fatalf("flush resolves it: %v pending=%v", got, d.Pending())
	}
	if got := d.Feed([]byte("a")); !reflect.DeepEqual(got, one(kr('a', 0))) {
		t.Fatalf("after flush: %v", got)
	}
	if got := d.Flush(); len(got) != 0 {
		t.Fatalf("flush with nothing pending: %v", got)
	}
}

func TestGraceAndPending(t *testing.T) {
	var d Decoder
	if d.Grace() != 0 || d.Pending() || d.InPaste() {
		t.Fatal("a fresh decoder is idle")
	}
	d.Feed([]byte("\x1b["))
	if d.Grace() != EscGrace || !d.Pending() {
		t.Errorf("half a sequence: grace %v", d.Grace())
	}
	d.Flush()
	d.Feed([]byte("\x1b[200~abc"))
	if !d.InPaste() || d.Grace() != PasteGrace || !d.Pending() {
		t.Errorf("in a paste: inPaste=%v grace=%v", d.InPaste(), d.Grace())
	}
	if PasteGrace <= EscGrace || EscGrace < 10*time.Millisecond {
		t.Errorf("the graces are a policy: esc %v paste %v", EscGrace, PasteGrace)
	}
	d.Reset()
	if d.Pending() || d.InPaste() || d.Grace() != 0 {
		t.Error("Reset forgets everything")
	}
	if got := d.Feed([]byte("a")); !reflect.DeepEqual(got, one(kr('a', 0))) {
		t.Errorf("after reset: %v", got)
	}
}

func TestPasteCap(t *testing.T) {
	d := Decoder{MaxPaste: 10}
	in := "\x1b[200~" + strings.Repeat("x", 25) + "\x1b[201~y"
	got := append(d.Feed([]byte(in)), d.Flush()...)
	want := []Key{kp(strings.Repeat("x", 10), true), kr('y', 0)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	// the cap never leaves half a rune at the end
	d = Decoder{MaxPaste: 4}
	got = append(d.Feed([]byte("\x1b[200~ab中c\x1b[201~")), d.Flush()...)
	if len(got) != 1 || got[0].Text != "ab" || !got[0].Truncated {
		t.Errorf("cap inside a rune: %v %q", got, got[0].Text)
	}
	// a paste that fits exactly is not truncated
	d = Decoder{MaxPaste: 5}
	got = append(d.Feed([]byte("\x1b[200~12345\x1b[201~")), d.Flush()...)
	if len(got) != 1 || got[0].Truncated || got[0].Text != "12345" {
		t.Errorf("exact fit: %v", got)
	}
}

func TestBufferingIsBounded(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	var d Decoder
	d.MaxPaste = 1000
	for i := 0; i < 2000; i++ {
		d.Feed([]byte(randomStream(rng, 50)))
		if n := d.buffered(); n > d.MaxPaste+maxCSI+16 {
			t.Fatalf("after %d feeds the decoder holds %d bytes", i, n)
		}
	}
	// A CSI that never ends, a string that never ends and a paste that never ends hold nothing more than the cap.
	for _, in := range []string{"\x1b[" + strings.Repeat("1;", 1<<16), "\x1b]" + strings.Repeat("x", 1<<20), "\x1b[200~" + strings.Repeat("y", 1<<20)} {
		d = Decoder{MaxPaste: 1000}
		for i := 0; i < len(in); i += 4096 {
			end := i + 4096
			if end > len(in) {
				end = len(in)
			}
			d.Feed([]byte(in[i:end]))
			if n := d.buffered(); n > 1000+maxCSI+16 {
				t.Fatalf("holding %d bytes of a never-ending sequence", n)
			}
		}
		d.Flush()
		if d.buffered() != 0 {
			t.Errorf("flush left %d bytes", d.buffered())
		}
	}
}

func TestOverlongCSIIsSkippedNotTyped(t *testing.T) {
	in := "\x1b[" + strings.Repeat("9;", 100) + "Hab"
	got := decodeWhole(in)
	if !reflect.DeepEqual(got, runes("ab", 0)) {
		t.Errorf("an over-long sequence must vanish, up to its final byte: %v", got)
	}
}

func TestKeyString(t *testing.T) {
	cases := []struct {
		k    Key
		want string
	}{
		{kr('a', Ctrl), "ctrl+a"}, {kr(' ', Ctrl), "ctrl+space"}, {ks(Enter, Alt), "alt+enter"}, {ks(Tab, Shift), "shift+tab"},
		{ks(Up, Ctrl|Alt|Shift), "ctrl+alt+shift+up"}, {kr('é', 0), "é"}, {ks(F12, 0), "f12"}, {kr(0x7f, 0), "U+7f"},
		{kp("abc", false), "paste(3 bytes)"}, {kp("abc", true), "paste(3 bytes, truncated)"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("%#v.String() = %q, want %q", c.k, got, c.want)
		}
	}
	if !ks(Enter, Alt).Is(Enter, Alt) || ks(Enter, Alt).Is(Enter, 0) || !kr('x', Ctrl).IsRune('x', Ctrl) || kr('x', Ctrl).IsRune('y', Ctrl) {
		t.Error("Is and IsRune")
	}
	if Special(200).String() != "special(200)" {
		t.Error("an unknown special has a name")
	}
}

func FuzzDecoder(f *testing.F) {
	for _, c := range decodeCases {
		f.Add([]byte(c.in))
	}
	f.Add([]byte("\x1b[200~\x1b[200~\x1b[201~\x1b[201~"))
	f.Add([]byte("\x1b\x1b\x1b\x1b[\x1b]\x1bP"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var whole Decoder
		want := append(whole.Feed(data), whole.Flush()...)
		var d Decoder
		var got []Key
		for i := range data {
			got = append(got, d.Feed(data[i:i+1])...)
			if d.buffered() > maxCSI+16+DefaultMaxPaste {
				t.Fatalf("holding %d bytes", d.buffered())
			}
		}
		got = append(got, d.Flush()...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("byte by byte differs from whole for %q\n got  %v\n want %v", data, got, want)
		}
		if d.Pending() || d.buffered() != 0 {
			t.Fatal("flush leaves nothing behind")
		}
	})
}
