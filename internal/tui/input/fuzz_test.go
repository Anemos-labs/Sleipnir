package input

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// fuzzRig is an editor with everything switched on (history, slash completion, a paste chip threshold low enough to be hit),
// fed by a decoder with a small paste cap, as the real input loop would drive them.
func fuzzRig() (*Editor, *Decoder) {
	h := NewHistory()
	for _, s := range []string{"first entry", "second\nentry with\nthree lines", "git status", "\x1b[31mnot red\x1b[0m"} {
		h.Add(s)
	}
	e := NewEditor(Options{History: h, Completer: SlashCommands(testCommands), PasteRunes: 40, MaxPasteBytes: 4096, Placeholder: "type here"})
	return e, &Decoder{MaxPaste: 8192}
}

func cleanOfControls(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r < 0xa0) || invisible(r) {
			return false
		}
	}
	return true
}

var viewChecks int // counts the calls of checkInvariants (tests here do not run in parallel)

// checkInvariants is everything that must be true of an editor after any key whatsoever.
func checkInvariants(t *testing.T, e *Editor, fed int) {
	t.Helper()
	if e.cur < 0 || e.cur > len(e.buf) {
		t.Fatalf("cursor %d outside a buffer of %d runes", e.cur, len(e.buf))
	}
	if !isBoundary(e.buf, e.cur) {
		t.Fatalf("cursor %d inside a cluster of %q", e.cur, string(e.buf))
	}
	if len(e.buf) > fed+8192 {
		t.Fatalf("the buffer holds %d runes after %d bytes of input", len(e.buf), fed)
	}
	for _, r := range e.buf {
		switch {
		case isChip(r):
			if chipID(r) >= len(e.chips) {
				t.Fatalf("chip rune %U has no chip (%d chips)", r, len(e.chips))
			}
		case r == utf8.RuneError && false:
		case (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r < 0xa0) || invisible(r) || !utf8.ValidRune(r):
			t.Fatalf("rune %U does not belong in the buffer %q", r, string(e.buf))
		}
	}
	if !cleanOfControls(e.Text()) || !cleanOfControls(e.Display()) {
		t.Fatalf("the text or its display holds a control character: %q", e.Text())
	}
	if m := e.menu; m != nil {
		if len(m.cands) == 0 || len(m.cands) > maxCands || m.sel < 0 || m.sel >= len(m.cands) || m.sel < m.top || m.sel >= m.top+menuRows || m.top < 0 {
			t.Fatalf("menu out of shape: %d candidates, selected %d, top %d", len(m.cands), m.sel, m.top)
		}
		if m.from < 0 || m.from > e.cur {
			t.Fatalf("menu starts at %d, cursor at %d", m.from, e.cur)
		}
		for _, c := range m.cands {
			if !cleanOfControls(c.Text) || !cleanOfControls(c.Display) || strings.ContainsAny(c.Text+c.Display+c.Detail, "\n\t") {
				t.Fatalf("a candidate is not clean: %+v", c)
			}
		}
	}
	if s := e.search; s != nil && (s.idx < -1 || s.idx >= e.hist.Len()) {
		t.Fatalf("search index %d of %d entries", s.idx, e.hist.Len())
	}
	if len(e.kills) > maxKills || len(e.undo) > maxUndo+maxUndo/8 || len(e.chips) > maxChips || e.hpos < 0 || e.hpos > e.hist.Len() {
		t.Fatalf("a bounded structure is out of bounds: kills %d undo %d chips %d hpos %d", len(e.kills), len(e.undo), len(e.chips), e.hpos)
	}
	viewChecks++
	widths := []int{4, 23}
	if viewChecks%4 == 0 {
		widths = []int{1, 9, 80} // the view is the expensive part: the other widths are checked on some of the keys
	}
	if len(e.buf) > 400 && viewChecks%32 != 0 {
		widths = nil // and on a long buffer, which costs a view O(n) per key, on only a few
	}
	for _, w := range widths {
		v := e.View(w)
		ww := max(w, 4)
		if len(v.Lines) == 0 || v.CursorRow < 0 || v.CursorRow >= len(v.Lines) || v.CursorCol < 0 || v.CursorCol > ww {
			t.Fatalf("view at width %d: %d lines, cursor %d,%d", w, len(v.Lines), v.CursorRow, v.CursorCol)
		}
		for _, l := range v.Lines {
			if !cleanOfControls(l.Plain()) {
				t.Fatalf("the view holds a control character: %q", l.Plain())
			}
		}
	}
}

func checkEvents(t *testing.T, evs []Event) {
	t.Helper()
	for _, ev := range evs {
		if s, ok := ev.(Submit); ok {
			if !cleanOfControls(s.Text) || strings.TrimSpace(s.Text) == "" || s.Text != strings.TrimRight(s.Text, " \t\r\n") {
				t.Fatalf("a bad Submit: %q", s.Text)
			}
		}
	}
}

// run feeds data to the decoder in chunks whose sizes come from the data itself, with a Flush now and then, and checks the
// invariants after every key.
func runFuzz(t *testing.T, data []byte) {
	t.Helper()
	if len(data) > 2048 {
		data = data[:2048] // what the keys do is local: long inputs only make the checks below, which are O(n) a key, slow
	}
	e, d := fuzzRig()
	rng := rand.New(rand.NewSource(int64(len(data))*7919 + int64(sumBytes(data))))
	fed := 0
	for i := 0; i < len(data); {
		n := 1 + rng.Intn(7)
		if i+n > len(data) {
			n = len(data) - i
		}
		keys := d.Feed(data[i : i+n])
		i += n
		fed = i
		if rng.Intn(9) == 0 {
			keys = append(keys, d.Flush()...)
		}
		for _, k := range keys {
			evs := e.Handle(k)
			checkEvents(t, evs)
			checkInvariants(t, e, fed)
		}
		if d.buffered() > DefaultMaxPaste+maxCSI+16 {
			t.Fatalf("the decoder holds %d bytes", d.buffered())
		}
	}
	for _, k := range d.Flush() {
		checkEvents(t, e.Handle(k))
		checkInvariants(t, e, fed)
	}
}

func sumBytes(b []byte) int {
	s := 0
	for _, c := range b {
		s = s*31 + int(c)
	}
	return s
}

func FuzzDecoderAndEditor(f *testing.F) {
	for _, c := range decodeCases {
		f.Add([]byte(c.in))
	}
	for _, s := range []string{
		"hello\x1b[D\x1b[D\x7fX\r", "/he\t\r", "@src\t", "\x12git\r", "\x1b[200~" + strings.Repeat("line\n", 30) + "\x1b[201~\x7f",
		"a\x1b\rb\x0b\x19\x14\x1f\x1f\x1bz", "e\U00000301\x1b[D\x7f", "\x1b[A\x1b[A\x1b[B\x03\x04", "🐎👨\U0000200d👩\x1b[D\x1b[D\x1b[3~",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(runFuzz)
}

// The same, over many structured random streams, so that an ordinary test run exercises it and not only the seeds.
func TestRandomKeyStreamsKeepTheEditorValid(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	n := 250
	if testing.Short() {
		n = 60
	}
	for i := 0; i < n; i++ {
		data := []byte(randomStream(rng, 10+rng.Intn(50)))
		if rng.Intn(4) == 0 { // and a real paste in the middle
			data = append(data, []byte("\x1b[200~"+strings.Repeat("pasted line\n", rng.Intn(12))+"\x1b[201~")...)
			data = append(data, []byte(randomStream(rng, 10))...)
		}
		runFuzz(t, data)
	}
}

func TestRandomBytesKeepTheEditorValid(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	n := 120
	if testing.Short() {
		n = 30
	}
	for i := 0; i < n; i++ {
		data := make([]byte, rng.Intn(200))
		rng.Read(data)
		runFuzz(t, data)
	}
}
