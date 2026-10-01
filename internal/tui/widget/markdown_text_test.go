package widget

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

func TestCleanText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain text is untouched", "hello, wörld 日本 😀", "hello, wörld 日本 😀"},
		{"newline and tab stay", "a\tb\nc", "a\tb\nc"},
		{"CSI colours", "\x1b[31mred\x1b[0m", "red"},
		{"CSI with parameters and intermediates", "x\x1b[38;2;1;2;3m\x1b[?25l\x1b[2 qy", "xy"},
		{"clear screen and home", "\x1b[2J\x1b[H cleared", " cleared"},
		{"OSC 52 ended by BEL", "before\x1b]52;c;SGVsbG8=\x07after", "beforeafter"},
		{"OSC 52 ended by ST", "before\x1b]52;c;SGVsbG8=\x1b\\after", "beforeafter"},
		{"OSC 8 hyperlink", "\x1b]8;;http://evil.example\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"OSC ended by the C1 ST", "a\x1b]0;title\u009cb", "ab"},
		{"DCS", "\x1bPq#0;2;0;0;0\x1b\\dcs", "dcs"},
		{"APC", "\x1b_Gf=100;AAAA\x1b\\apc", "apc"},
		{"charset selection and two-byte forms", "\x1b(B\x1b)0\x1bc\x1b7\x1b8z", "z"},
		{"a stray ESC loses only itself", "stray \x1b", "stray "},
		{"an ESC before a newline", "a\x1b\nb", "a\nb"},
		{"unterminated OSC loses its introducer only", "a\x1b]52;c;AAAA", "a52;c;AAAA"},
		{"unterminated CSI loses its introducer only", "a\x1b[31", "a31"},
		{"C1 controls", "a\u0085b\u0090c\u009bd\u009de", "abcde"},
		{"C0 controls", "bell\x07 bs\x08 ff\x0c vt\x0b nul\x00 del\x7f", "bell bs ff vt nul del"},
		{"CRLF", "a\r\nb", "a\nb"},
		{"lone CR cannot redraw the line", "rm -rf /\rharmless", "rm -rf /\nharmless"},
		{"line and paragraph separators", "a\U00002028b\U00002029c", "a\nb\nc"},
		{"bidi overrides and isolates", "a\U0000202Eb\U0000202Cc\U00002066d\U00002069e", "abcde"},
		{"zero width and BOM", "a\U0000200Bb\U0000FEFFc\U00002060d\U000000ADe", "abcde"},
		{"tag characters", "a\U000E0041b", "ab"},
		{"joiners stay for emoji", "👨\U0000200D👩", "👨\U0000200D👩"},
		{"invalid UTF-8 becomes U+FFFD", "a\xffb\xc3", "a�b�"},
	}
	for _, c := range cases {
		got := safeText(c.in)
		if got != c.want {
			t.Errorf("%s: safeText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if again := safeText(got); again != got {
			t.Errorf("%s: not idempotent: %q then %q", c.name, got, again)
		}
	}
}

func TestCleanTextLeavesNothingActive(t *testing.T) {
	g := widgettest.NewRand(1)
	var corpus []string
	corpus = append(corpus, widgettest.Hostile()...)
	for i := 0; i < 300; i++ {
		corpus = append(corpus, g.Text(1+g.Intn(30)))
	}
	for _, in := range corpus {
		out := safeText(in)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 from %q", in)
		}
		for _, r := range out {
			if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) || invisibleRune(r) || r == 0x2028 || r == 0x2029 {
				t.Fatalf("U+%04X survived in %q (from %q)", r, out, in)
			}
		}
		if strings.Contains(out, "\r") || strings.Contains(out, "\x1b") {
			t.Fatalf("CR or ESC survived in %q", out)
		}
	}
}

func TestOneLine(t *testing.T) {
	if got := safeOneLine("a\tb\nc\x1b[31md"); got != "a b cd" {
		t.Errorf("oneLine = %q", got)
	}
}

func TestExpandTabs(t *testing.T) {
	cases := []struct {
		in   string
		tw   int
		want string
	}{
		{"\tx", 4, "    x"}, {"ab\tx", 4, "ab  x"}, {"abcd\tx", 4, "abcd    x"}, {"\t\tx", 2, "    x"},
		{"中\tx", 4, "中  x"}, {"no tabs", 4, "no tabs"}, {"a\tb", 0, "a b"},
	}
	for _, c := range cases {
		if got := expandTabStops(c.in, c.tw); got != c.want {
			t.Errorf("expandTabStops(%q, %d) = %q, want %q", c.in, c.tw, got, c.want)
		}
	}
}

func TestCleanLine(t *testing.T) {
	clean := cell.Line{{Text: "fine", Style: cell.Style{Attr: cell.Bold}}}
	if got := safeLine(clean); &got[0] != &clean[0] {
		t.Error("a clean line is returned as it is, without copying")
	}
	dirty := cell.Line{{Text: "a\tb", Style: cell.Style{Attr: cell.Bold}}, {Text: "\x1b[31m\x1b]52;c;QQ==\x07red\nnext"}}
	got := safeLine(dirty)
	if got.Plain() != "a   bred next" {
		t.Errorf("cleanLine = %q", got.Plain())
	}
	if got[0].Style != dirty[0].Style {
		t.Error("styles must survive cleaning")
	}
	if dirty[0].Text != "a\tb" {
		t.Error("the input line must not be modified")
	}
}

func TestHardWrap(t *testing.T) {
	red := cell.Style{FG: cell.ANSI(1)}
	plain := func(rows []cell.Line) string {
		var got []string
		for _, r := range rows {
			got = append(got, r.Plain())
		}
		return strings.Join(got, "|")
	}
	l := cell.Join(cell.Styled(red, "ab"), cell.Text("中c"), cell.Text("e\u0301f"))
	if got := plain(cutRows(l, 3, cell.Span{})); got != "ab|中c|e\u0301f" {
		t.Errorf("a wide rune is not split: %q", got)
	}
	rows := cutRows(l, 4, cell.Span{})
	if plain(rows) != "ab中|ce\u0301f" || rows[0][0].Style != red {
		t.Errorf("width 4: %q", plain(rows))
	}
	if got := plain(cutRows(l, 6, cell.Span{})); got != "ab中ce\u0301|f" {
		t.Errorf("a combining mark stays with its base: %q", got)
	}
	if got := plain(cutRows(l, 100, cell.Span{})); got != l.Plain() {
		t.Errorf("a line that fits is one row: %q", got)
	}
	if got := plain(cutRows(cell.Text("中"), 1, cell.Span{})); got != "中" {
		t.Errorf("a wide rune on a one-cell width is taken whole, so that wrapping always ends: %q", got)
	}
	if got := plain(cutRows(cell.Text("中中"), 1, cell.Span{})); got != "中|中" {
		t.Errorf("%q", got)
	}
	if got := plain(cutRows(cell.Text("abcdefghij"), 4, cell.Span{Text: "↳"})); got != "abcd|↳efg|↳hij" {
		t.Errorf("continuations are marked: %q", got)
	}
	if got := plain(cutRows(cell.Text("abcdef"), 2, cell.Span{Text: "↳↳"})); got != "ab|cd|ef" {
		t.Errorf("a marker as wide as the row is dropped: %q", got)
	}
	if rows := cutRows(nil, 4, cell.Span{}); len(rows) != 1 || rows[0].Width() != 0 {
		t.Errorf("an empty line is one empty row: %v", rows)
	}
	if got := plain(cutRows(cell.Text("a  b   "), 3, cell.Span{})); got != "a  |b  | " {
		t.Errorf("spaces are kept: %q", got)
	}
	// styles survive a cut in the middle of a span
	rows = cutRows(cell.Join(cell.Text("x"), cell.Styled(red, "abcdef")), 3, cell.Span{})
	if plain(rows) != "xab|cde|f" || rows[1][0].Style != red || rows[0][1].Style != red {
		t.Errorf("styles across rows: %+v", rows)
	}
	// linear: a line of a million cells in rows of ten
	big := cutRows(cell.Text(strings.Repeat("x", 1_000_000)), 10, cell.Span{Text: "↳"})
	if len(big) != 1+(1_000_000-10+8)/9 {
		t.Errorf("%d rows", len(big))
	}
}

func TestHardWrapKeepsEveryRuneAndNeverOverflows(t *testing.T) {
	g := widgettest.NewRand(2)
	texts := append(widgettest.Unicode(), widgettest.Hostile()...)
	for i := 0; i < 100; i++ {
		texts = append(texts, g.Text(20))
	}
	for _, raw := range texts {
		src := safeOneLine(raw)
		for w := 2; w <= 40; w += 3 {
			rows := cutRows(cell.Text(src), w, cell.Span{})
			var sb strings.Builder
			for _, r := range rows {
				if r.Width() > w {
					t.Fatalf("w=%d: row %q is %d cells wide (from %q)", w, r.Plain(), r.Width(), src)
				}
				sb.WriteString(r.Plain())
			}
			if sb.String() != src {
				t.Fatalf("w=%d: text changed: %q != %q", w, sb.String(), src)
			}
		}
	}
}

func TestTruncateLeftAndClip(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"short.go", 20, "short.go"}, {"internal/tui/widget/diff.go", 14, "…idget/diff.go"},
		{"abc", 1, "c"}, {"abc", 0, ""}, {"日本語のパス/ファイル.go", 9, "…イル.go"},
	}
	for _, c := range cases {
		got := ellipsizeLeft(c.in, c.w, "…")
		if cell.StringWidth(got) > c.w || (c.w >= 4 && got != c.want) {
			t.Errorf("ellipsizeLeft(%q, %d) = %q (%d cells), want %q", c.in, c.w, got, cell.StringWidth(got), c.want)
		}
	}
	if got := ellipsizeLeft("a/very/long/path/name.go", 10, "..."); cell.StringWidth(got) > 10 || !strings.HasPrefix(got, "...") {
		t.Errorf("ASCII ellipsis: %q", got)
	}
	if clipLine(cell.Text("abcdef"), 3).Plain() != "abc" || clipLine(cell.Text("abc"), 0) != nil || clipLine(cell.Text("中中"), 3).Plain() != "中" {
		t.Error("clip")
	}
}
