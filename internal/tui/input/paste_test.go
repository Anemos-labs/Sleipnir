package input

import (
	"reflect"
	"strings"
	"testing"
)

func lines(n int) string { return strings.Repeat("a line of text\n", n) }

func TestSmallPasteIsInsertedAsTyped(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"one line", "hello", "hello|"},
		{"three lines", "a\nb\nc", "a\nb\nc|"},
		{"three lines with a trailing newline", "a\nb\nc\n", "a\nb\nc\n|"},
		{"exactly 200 runes", strings.Repeat("x", 200), strings.Repeat("x", 200) + "|"},
		{"200 wide runes", strings.Repeat("中", 200), strings.Repeat("中", 200) + "|"},
		{"a single newline", "\n", "\n|"},
		{"spaces", "   ", "   |"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.send(paste(c.in))
			if got := r.state(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
			if len(r.ed.chips) != 0 {
				t.Error("no chip for a small paste")
			}
		})
	}
}

func TestLargePasteBecomesAChip(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		label string
	}{
		{"four lines", "a\nb\nc\nd", "[pasted text #1 +4 lines]"},
		{"four lines with a trailing newline", "a\nb\nc\nd\n", "[pasted text #1 +4 lines]"},
		{"312 lines", lines(312), "[pasted text #1 +312 lines]"},
		{"201 runes on one line", strings.Repeat("x", 201), "[pasted text #1 201 chars]"},
		{"a long line of wide runes", strings.Repeat("中", 300), "[pasted text #1 300 chars]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.send("x ", paste(c.in), " y")
			if got := r.state(); got != "x "+c.label+" y|" {
				t.Errorf("buffer shows %q", got)
			}
			if r.ed.Len() != len("x ")+1+len(" y") {
				t.Errorf("a chip is one rune in the buffer, not %d", r.ed.Len()-4)
			}
			evs := withoutChanged(r.send(kEnter))
			want := "x " + cleanText(c.in) + " y" // the chip expands to exactly what was pasted; only the end of the buffer is trimmed
			if !reflect.DeepEqual(evs, []Event{Submit{Text: want}}) {
				t.Errorf("Submit expands the chip: got %.60q", evs[0].(Submit).Text)
			}
		})
	}
}

func TestChipsAreNumberedAndReset(t *testing.T) {
	r := newRig(t, Options{})
	r.send(paste(lines(5)), " ", paste(lines(7)))
	if got := r.state(); got != "[pasted text #1 +5 lines] [pasted text #2 +7 lines]|" {
		t.Errorf("%q", got)
	}
	r.send(kEnter)
	r.send(paste(lines(9)))
	if got := r.state(); got != "[pasted text #1 +9 lines]|" {
		t.Errorf("numbering starts over after a submit: %q", got)
	}
}

func TestChipIsAtomic(t *testing.T) {
	label := "[pasted text #1 +5 lines]"
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{"left moves over the whole chip", []string{"a", paste(lines(5)), "b", kLeft, kLeft}, "a|" + label + "b"},
		{"left again goes past it", []string{"a", paste(lines(5)), "b", kLeft, kLeft, kLeft}, "|a" + label + "b"},
		{"right moves over it", []string{"a", paste(lines(5)), "b", kHome, kRight, kRight}, "a" + label + "|b"},
		{"backspace removes the whole chip", []string{"a", paste(lines(5)), kBS}, "a|"},
		{"delete removes the whole chip", []string{"a", paste(lines(5)), "b", kLeft, kLeft, kDel}, "a|b"},
		{"ctrl+w removes the chip as a word", []string{"a ", paste(lines(5)), ctrl('w')}, "a |"},
		{"alt+b stops before the chip", []string{"a ", paste(lines(5)), " b", alt("b"), alt("b")}, "a |" + label + " b"},
		{"alt+f stops after the chip", []string{paste(lines(5)), " b", kHome, alt("f")}, label + "| b"},
		{"word movement treats a chip glued to text as two words", []string{"ab", paste(lines(5)), "cd", alt("b")}, "ab" + label + "|cd"},
		{"ctrl+k kills it with the rest of the line", []string{"a", paste(lines(5)), "b", kHome, kRight, ctrl('k')}, "a|"},
		{"ctrl+t transposes around it as a unit", []string{"a", paste(lines(5)), ctrl('t')}, label + "a|"},
		{"home and end", []string{"a", paste(lines(5)), kHome, kEnd}, "a" + label + "|"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.send(c.keys...)
			if got := r.state(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestChipUndoAndYank(t *testing.T) {
	r := newRig(t, Options{})
	big := lines(5)
	r.send("a", paste(big), kBS)
	if r.state() != "a|" {
		t.Fatal(r.state())
	}
	r.send(kUndo)
	if r.state() != "a[pasted text #1 +5 lines]|" {
		t.Fatalf("undo brings the chip back: %q", r.state())
	}
	r.send(kEnter)
	if got := r.events[len(r.events)-2]; got != (Submit{Text: "a" + strings.TrimRight(big, "\n")}) {
		t.Errorf("and it still expands to the pasted text: %v", got)
	}
	// kill and yank a chip within one prompt
	r.send(paste(big), ctrl('a'), ctrl('k'), "x", ctrl('y'))
	if r.state() != "x[pasted text #1 +5 lines]|" || r.ed.Text() != "x"+strings.TrimRight(big, "\n")+"" && r.ed.Text() != "x"+big {
		t.Errorf("yank restores a chip: %q / %q", r.state(), r.ed.Text())
	}
}

func TestChipsDieWithTheirPrompt(t *testing.T) {
	r := newRig(t, Options{})
	r.send(paste(lines(5)), ctrl('a'), ctrl('k'), "x", kEnter) // the chip went to the kill ring, then the prompt was sent
	r.send(ctrl('y'))
	if !r.ed.Empty() {
		t.Errorf("yanking must not bring back a chip that belongs to the old prompt: %q", r.state())
	}
	r.send("z", kUndo, kUndo)
	if r.state() != "|" {
		t.Errorf("undo does not reach into the previous prompt: %q", r.state())
	}
	r.send(kUndo)
	if !r.ed.Empty() {
		t.Error("nothing left to undo")
	}
}

func TestPasteIsCleaned(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"escape sequences are removed whole", "a\x1b[31mred\x1b[0mb", "aredb"},
		{"OSC with BEL", "x\x1b]0;evil title\x07y", "xy"},
		{"OSC 52 (clipboard) with ST", "x\x1b]52;c;ZXZpbA==\x1b\\y", "xy"},
		{"DCS", "x\x1bP1$rtext\x1b\\y", "xy"},
		{"control characters", "a\x00b\x01c\x07d\x08e\x0bf\x0cg\x7fh", "abcdefgh"},
		{"C1 controls", "a\u0085b\u009bc\u009dd", "abcd"},
		{"CRLF", "a\r\nb\r\nc", "a\nb\nc"},
		{"lone CR", "a\rb", "a\nb"},
		{"tabs are kept", "a\tb", "a\tb"},
		{"line and paragraph separators", "a\U00002028b\U00002029c", "a\nb\nc"},
		{"bidi overrides and isolates", "a\U0000202eb\U00002066c\U00002069d", "abcd"},
		{"zero-width space, word joiner, BOM, soft hyphen", "a\U0000200bb\U00002060c\U0000feffd\U000000ade", "abcde"},
		{"tag characters (hidden text)", "a\U000e0041\U000e0042b", "ab"},
		{"the zero-width joiner stays: emoji need it", "👨\U0000200d👩", "👨\U0000200d👩"},
		{"invalid UTF-8", "a\xffb\xc3c", "a\U0000fffdb\U0000fffdc"},
		{"a forged chip", "a\U0010f000b\U0010f7ffc", "abc"},
		{"a bare ESC at the end", "abc\x1b", "abc"},
		{"ESC and the byte after it are a two-byte sequence", "a\x1bbc", "ac"},
		{"nested paste markers", "a\x1b[200~b\x1b[201~c", "abc"},
		{"only controls", "\x01\x02\x03", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.ed.Handle(PasteKey(c.in))
			r.check()
			if got := r.ed.Text(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
			for _, l := range r.ed.View(40).Plain() {
				if strings.ContainsAny(l, "\x1b\x07\x00") {
					t.Errorf("a control character reached the view: %q", l)
				}
			}
		})
	}
}

func TestPasteCleaningIsIdempotentAndCheap(t *testing.T) {
	in := "plain text é 中 😀\nsecond line\twith tab"
	if got := cleanText(in); got != in {
		t.Errorf("ordinary text is returned unchanged: %q", got)
	}
	dirty := "a\x1b[31mb\r\nc\U0000202ed\xff"
	once := cleanText(dirty)
	if twice := cleanText(once); twice != once {
		t.Errorf("not idempotent: %q then %q", once, twice)
	}
}

func TestTypedTextIsCleanedToo(t *testing.T) {
	r := newRig(t, Options{})
	for _, k := range []Key{kr('\U0000202e', 0), kr('\U0010f000', 0), kr('\U0000200b', 0), kr(0x85, 0), kr('a', 0)} {
		r.ed.Handle(k)
		r.check()
	}
	if got := r.ed.Text(); got != "a" {
		t.Errorf("hostile typed runes are dropped: %q", got)
	}
	r.ed.SetText("x\x1b[2Jy\U0010f001")
	if got := r.ed.Text(); got != "xy" {
		t.Errorf("SetText cleans: %q", got)
	}
}

func TestPasteHardCapAndTruncationMarker(t *testing.T) {
	r := newRig(t, Options{MaxPasteBytes: 1000})
	r.ed.Handle(PasteKey(strings.Repeat("0123456789\n", 500)))
	if len(r.ed.chips) != 1 {
		t.Fatalf("%d chips", len(r.ed.chips))
	}
	txt := r.ed.chips[0].text
	if !strings.HasSuffix(txt, "\n"+truncatedMarker) || len(txt) > 1000+len(truncatedMarker)+2 {
		t.Errorf("capped text is %d bytes and ends %q", len(txt), txt[len(txt)-20:])
	}
	if !strings.Contains(r.ed.chips[0].label, "truncated") {
		t.Errorf("the label says so too: %q", r.ed.chips[0].label)
	}
	if got := r.ed.Text(); !strings.HasSuffix(got, truncatedMarker) {
		t.Errorf("Submit text carries the marker: %q", got[len(got)-30:])
	}

	// the cap never splits a rune
	r = newRig(t, Options{MaxPasteBytes: 1001})
	r.ed.Handle(PasteKey(strings.Repeat("中", 600)))
	r.check()
	body := strings.TrimSuffix(r.ed.chips[0].text, "\n"+truncatedMarker)
	if len(body) > 1001 || len(body)%3 != 0 || strings.ContainsRune(body, '\U0000fffd') {
		t.Errorf("%d bytes: a rune was split", len(body))
	}

	// the decoder's own cap marks a paste the same way, even when the editor's would not
	r = newRig(t, Options{})
	r.ed.Handle(Key{Kind: KindPaste, Text: "short", Truncated: true})
	if got := r.ed.Text(); got != "short\n"+truncatedMarker {
		t.Errorf("a paste the decoder cut: %q", got)
	}
	if len(r.ed.chips) != 1 {
		t.Error("a truncated paste is always a chip")
	}
}

func TestPasteOfTwoMiBThroughTheDecoder(t *testing.T) {
	r := newRig(t, Options{})
	big := strings.Repeat("0123456789abcdef", 1<<17) // 2 MiB
	r.send(paste(big))
	if len(r.ed.chips) != 1 {
		t.Fatalf("%d chips", len(r.ed.chips))
	}
	text := r.ed.chips[0].text
	if len(text) > 1<<20+len(truncatedMarker)+2 || !strings.HasSuffix(text, truncatedMarker) {
		t.Errorf("hard cap of 1 MiB with a visible marker: %d bytes, ends %q", len(text), text[len(text)-16:])
	}
	if r.d.Pending() {
		t.Error("the decoder is back to normal")
	}
	r.send("x") // typing still works afterwards
	if !strings.HasSuffix(r.state(), "x|") {
		t.Errorf("%q", r.state()[len(r.state())-10:])
	}
}

func TestTooManyChipsFallBackToInlineText(t *testing.T) {
	r := newRig(t, Options{})
	for i := 0; i < maxChips+5; i++ {
		r.ed.Handle(PasteKey(lines(50)))
	}
	r.check()
	if len(r.ed.chips) != maxChips {
		t.Errorf("%d chips", len(r.ed.chips))
	}
	// the last five pastes were kept, inline and cut short, not lost
	if !strings.Contains(r.ed.Text(), "a line of text") {
		t.Error("the paste beyond the last chip is still there")
	}
	if !strings.Contains(r.ed.Display(), truncatedMarker) {
		t.Error("and says it was cut")
	}
}

func TestPasteInTheMiddleAndWithMenuOrSelection(t *testing.T) {
	r := menuRig(t)
	r.send("/he")
	if !r.ed.Completion().Open {
		t.Fatal("menu")
	}
	r.send(paste("XY"))
	if r.ed.Completion().Open || r.state() != "/heXY|" {
		t.Errorf("a paste closes the menu: %q open=%v", r.state(), r.ed.Completion().Open)
	}
	r = newRig(t, Options{})
	r.send("abcd", kLeft, kLeft, paste("--"))
	if r.state() != "ab--|cd" {
		t.Errorf("%q", r.state())
	}
	r.send(kUndo)
	if r.state() != "ab|cd" {
		t.Errorf("a paste is one undo step: %q", r.state())
	}
}

func TestChipInView(t *testing.T) {
	r := newRig(t, Options{})
	r.send("see ", paste(lines(312)), " ok")
	v := r.ed.View(60)
	if got := v.Plain(); len(got) != 1 || got[0] != "❯ see [pasted text #1 +312 lines] ok" {
		t.Fatalf("%q", got)
	}
	var chipText string
	for _, sp := range v.Lines[0] {
		if sp.Style == r.ed.th.Chip {
			chipText += sp.Text
		}
	}
	if chipText != "[pasted text #1 +312 lines]" {
		t.Errorf("the chip is drawn in its own style: %q", chipText)
	}
	if v.CursorCol != v.Lines[0].Width() {
		t.Errorf("cursor after the text: %d vs %d", v.CursorCol, v.Lines[0].Width())
	}
	// a narrow view keeps the label together as long as it fits
	v = r.ed.View(34)
	for _, l := range v.Plain() {
		if strings.Contains(l, "[pasted") && !strings.Contains(l, "[pasted text #1 +312 lines]") {
			t.Errorf("the chip label was broken across rows: %q", v.Plain())
		}
	}
}
