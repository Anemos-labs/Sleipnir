package render

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// hostile is text as an attacker would put it in a model reply, a file or a web page, and what the screen must show instead. The
// whole escape sequence goes, payload included, so a hyperlink or a clipboard write leaves nothing behind but the visible text.
var hostile = []struct {
	name string
	in   string
	want string
}{
	{"OSC 52 clipboard write, BEL", "a\x1b]52;c;aGVsbG8=\x07b", "ab"},
	{"OSC 52 clipboard write, ST", "a\x1b]52;c;aGVsbG8=\x1b\\b", "ab"},
	{"OSC 52 clipboard query", "a\x1b]52;c;?\x07b", "ab"},
	{"erase display", "a\x1b[2Jb", "ab"},
	{"erase display and home", "a\x1b[2J\x1b[Hb", "ab"},
	{"cursor up and erase line: overwrite what was printed above", "a\x1b[5A\x1b[2Kb", "ab"},
	{"SGR", "a\x1b[31mred\x1b[0mb", "aredb"},
	{"OSC 8 hyperlink, BEL", "a\x1b]8;;http://evil.test/\x07click\x1b]8;;\x07b", "aclickb"},
	{"OSC 8 hyperlink, ST", "a\x1b]8;id=1;http://evil.test/\x1b\\click\x1b]8;;\x1b\\b", "aclickb"},
	{"window title", "\x1b]0;rm -rf /\x07visible", "visible"},
	{"C1 CSI", "a\u009b2Jb", "a2Jb"},
	{"C1 OSC and ST", "a\u009d52;c;AAAA\u009cb", "a52;c;AAAAb"},
	{"C1 DCS", "a\u0090qdata\u009cb", "aqdatab"},
	{"C1 next line and index", "a\u0085b\u0084c", "abc"},
	{"every C1", "a\u0080\u0081\u0082\u0083\u0086\u0087\u0088\u0089\u008a\u008b\u008c\u008e\u008f\u0091\u0092\u0093\u0094\u0095\u0096\u0097\u0099\u009a\u009eb", "ab"},
	{"bare carriage return", "abc\rXYZ", "abcXYZ"},
	{"carriage return and line feed", "abc\r\nXYZ", "abcXYZ"},
	{"backspaces", "abc\b\b\bXYZ", "abcXYZ"},
	{"bell, nul, delete, vertical tab, form feed", "a\x00b\x07c\x7fd\x0be\x0cf", "abcdef"},
	{"shift out and in", "a\x0eb\x0fc", "abc"},
	{"line feed inside a span is dropped", "line1\nline2", "line1line2"},
	{"charset designation", "a\x1b(0qqq\x1b(Bb", "aqqqb"},
	{"two-byte escapes", "a\x1b7b\x1b8c\x1bMd\x1bDe\x1bcf", "abcdef"},
	{"DCS", "\x1bPq#0;2;0;0;0\x1b\\visible", "visible"},
	{"APC (kitty graphics)", "\x1b_Gf=24,s=1,v=1;AAAA\x1b\\visible", "visible"},
	{"PM", "\x1b^private\x1b\\visible", "visible"},
	{"SOS", "\x1bXsos\x1b\\visible", "visible"},
	{"a stray escape at the end", "a\x1b", "a"},
	{"only an escape", "\x1b", ""},
	{"an unterminated OSC loses only its introducer", "a\x1b]52;c;AAAA", "a52;c;AAAA"},
	{"an unterminated CSI loses only its introducer", "a\x1b[31", "a31"},
	{"an escape that does not start a sequence", "a\x1b\x01b", "ab"},
	{"an escape inside an OSC ends it", "a\x1b]52;c;AAA\x1b[2Jb", "ab"},
	{"line and paragraph separators", "a\u2028b\u2029c", "abc"},
	{"invalid UTF-8", "a\xffb\xc3c", "a\uFFFDb\uFFFDc"},
	{"a combining mark with nothing before it", "\u0301a", "a"},
	{"a tab", "a\tb", "a       b"},
	{"a tab at a stop", "12345678\tb", "12345678        b"},
}

func TestSanitizeLine(t *testing.T) {
	for _, c := range hostile {
		got := sanitizeLine(txt(c.in)).Plain()
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSanitizeTabsCountFromTheStartOfTheLineAcrossSpans(t *testing.T) {
	l := sanitizeLine(cell.Join(txt("ab"), cell.Styled(cell.Style{}.With(cell.Bold), "cd\te"), txt("\tf")))
	if got := l.Plain(); got != "abcd    e       f" {
		t.Errorf("%q", got)
	}
}

func TestSanitizeKeepsStylesAndDropsEmptySpans(t *testing.T) {
	bold := cell.Style{}.With(cell.Bold)
	l := sanitizeLine(cell.Line{{Text: "\x1b[2J", Style: bold}, {Text: "a", Style: bold}, {Text: "\x07"}, {Text: "b"}})
	want := cell.Line{{Text: "a", Style: bold}, {Text: "b"}}
	if !reflect.DeepEqual(l, want) {
		t.Errorf("%+v, want %+v", l, want)
	}
}

func TestSanitizeLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{"", "plain ascii", "café", "中文 日本語 한글", "emoji 😀 and e\u0301 and \u200d joiner", "box ─│┌┐ braille ⠋ arrows ❯ ✓", "  leading and trailing  "} {
		if got := sanitizeLine(txt(s)).Plain(); got != s {
			t.Errorf("%q came back as %q", s, got)
		}
	}
	// Not removed, and documented as such: Unicode format characters are text.
	if got := sanitizeLine(txt("a\u202eb\u200bc")).Plain(); got != "a\u202eb\u200bc" {
		t.Errorf("format characters: %q", got)
	}
}

func TestSanitizeDoesNotModifyItsInput(t *testing.T) {
	in := cell.Line{{Text: "a\x1b[31mb"}, {Text: "c\td"}}
	orig := append(cell.Line(nil), in...)
	sanitizeLine(in)
	if !reflect.DeepEqual(in, orig) {
		t.Errorf("the input changed: %+v", in)
	}
}

// Hostile text through Print and SetLive: nothing it says reaches the terminal (the emulator rejects nothing and the bridge checks
// the byte stream), the screen shows exactly the visible text, and what was on the screen before is untouched.
func TestHostileTextLeavesTheScreenAloneAndRejectsNothing(t *testing.T) {
	for _, c := range hostile {
		for _, where := range []string{"print", "live"} {
			h := newHarness(t, termCaps(60, 12))
			h.r.Print(txts("sentinel one", "sentinel two")...)
			h.flush()
			if where == "print" {
				h.r.Print(txt(c.in))
				h.r.SetLive(txts("the live region"))
			} else {
				h.r.SetLive([]cell.Line{txt(c.in), txt("the live region")})
			}
			h.flush()
			want := []string{"sentinel one", "sentinel two", c.want, "the live region"}
			if where == "print" && c.want == "" {
				want = []string{"sentinel one", "sentinel two", "", "the live region"}
			}
			if where == "live" && c.want == "" {
				want = []string{"sentinel one", "sentinel two", "", "the live region"}
			}
			if got := h.all(); !reflect.DeepEqual(got, want) {
				t.Errorf("%s via %s:\n got  %q\n want %q", c.name, where, got, want)
			}
			if len(h.v.Rejected) != 0 {
				t.Errorf("%s via %s: rejected %q", c.name, where, h.v.Rejected)
			}
			// The same through a redraw, where rows are compared and partly rewritten.
			h.r.SetLive([]cell.Line{txt(c.in + "!"), txt("the live region")})
			h.flush()
			if got := h.all(); got[len(got)-1] != "the live region" {
				t.Errorf("%s via %s after a redraw: %q", c.name, where, got)
			}
			h.r.Close()
		}
	}
}

func TestHostileTextOnTheFullScreenView(t *testing.T) {
	for _, c := range hostile {
		sh := newScreenHarness(t, termCaps(60, 6))
		if err := sh.s.Enter(); err != nil {
			t.Fatal(err)
		}
		if err := sh.s.DrawLines([]cell.Line{txt("first row"), txt(c.in), txt("third row")}); err != nil {
			t.Fatal(err)
		}
		rows := sh.v.Rows()
		if rows[0] != "first row" || rows[1] != c.want || rows[2] != "third row" {
			t.Errorf("%s: %q, want row 1 %q", c.name, rows[:3], c.want)
		}
		// And as grid cells: one character each.
		grid := [][]Cell{{{Text: c.in}, {Text: "\x1b[2J"}, {Text: "x\x1b[31my"}, {Text: "\x07"}, {Text: "ok"}}}
		if err := sh.s.Draw(grid); err != nil {
			t.Fatal(err)
		}
		if len(sh.v.Rejected) != 0 {
			t.Errorf("%s: rejected %q", c.name, sh.v.Rejected)
		}
		sh.s.Leave()
	}
}

// Every byte string, as text: the screen never shows a control, the terminal never sees an escape that did not come from the
// renderer, the cursor stays on the screen, and sanitizing twice is sanitizing once.
func FuzzHostileText(f *testing.F) {
	for _, c := range hostile {
		f.Add(c.in)
	}
	f.Add("\x1b[?1049h\x1b[?25l\x1bc\x1b[6n\x1b]0;x\x07\x9b31m")
	f.Add(strings.Repeat("\x1b[", 100) + strings.Repeat("\x1b]8;;", 100))
	f.Fuzz(func(t *testing.T, s string) {
		once := sanitizeLine(txt(s))
		for _, r := range once.Plain() {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
				t.Fatalf("%q sanitized to %q, which has the control %U", s, once.Plain(), r)
			}
		}
		if !utf8.ValidString(once.Plain()) {
			t.Fatalf("%q sanitized to invalid UTF-8 %q", s, once.Plain())
		}
		if twice := sanitizeLine(once); !reflect.DeepEqual(once, twice) && once.Plain() != twice.Plain() {
			t.Fatalf("sanitizing is not idempotent: %q then %q", once.Plain(), twice.Plain())
		}

		c := termCaps(17, 6)
		c.SyncOutput = true
		h := newHarness(t, c)
		h.r.Print(txt("history"), cell.Styled(cell.Style{}.Fg(cell.RGB(1, 2, 3)).With(cell.Bold), s))
		h.r.SetLive([]cell.Line{txt(s), txt("> " + s), txt("fixed")})
		h.r.SetCursor(1, 5)
		h.flush()
		h.r.SetLive([]cell.Line{txt("fixed"), txt(s)})
		h.resize(9, 7)
		h.flush()
		if len(h.v.Rejected) != 0 {
			t.Fatalf("rejected %q for %q", h.v.Rejected, s)
		}
		h.r.Close()
		if !h.v.Idle() {
			t.Fatalf("output ended inside a sequence for %q", s)
		}

		sh := newScreenHarness(t, termCaps(17, 6))
		if err := sh.s.Enter(); err != nil {
			t.Fatal(err)
		}
		sh.s.DrawLines([]cell.Line{txt(s), txt("fixed")})
		sh.s.Draw([][]Cell{{{Text: s}, {Text: s}}})
		sh.s.Leave()
		if len(sh.v.Rejected) != 0 {
			t.Fatalf("rejected %q for %q on the full screen view", sh.v.Rejected, s)
		}
	})
}
