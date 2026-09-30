package cell

import (
	"reflect"
	"strings"
	"testing"
)

func TestRuneWidth(t *testing.T) {
	cases := []struct {
		r    rune
		want int
		why  string
	}{
		{'a', 1, "ascii"}, {' ', 1, "space"}, {'é', 1, "latin-1"}, {'─', 1, "box drawing"}, {'█', 1, "block"}, {'⠹', 1, "braille"},
		{'❯', 1, "dingbat arrow"}, {'✓', 1, "check mark"}, {'中', 2, "CJK"}, {'あ', 2, "hiragana"}, {'한', 2, "hangul"},
		{'Ａ', 2, "fullwidth A"}, {'😀', 2, "emoji"}, {'🐎', 2, "horse emoji"}, {'⏳', 2, "hourglass emoji presentation"},
		{'́', 0, "combining acute"}, {'‍', 0, "zero width joiner"}, {'️', 0, "variation selector"},
		{'\n', 0, "control"}, {0, 0, "nul"}, {'\x7f', 0, "delete"},
	}
	for _, c := range cases {
		if got := RuneWidth(c.r); got != c.want {
			t.Errorf("RuneWidth(%q U+%04X) = %d, want %d (%s)", c.r, c.r, got, c.want, c.why)
		}
	}
	if got := StringWidth("héllo 中文 🐎"); got != 5+1+4+1+2 {
		t.Errorf("StringWidth = %d", got)
	}
	if got := StringWidth("é"); got != 1 {
		t.Errorf("a combining sequence is one cell, got %d", got)
	}
	if got := StringWidth("a\xffb"); got != 3 {
		t.Errorf("an invalid byte is drawn as one cell, got %d", got)
	}
}

func TestWidthTablesAreSortedAndDisjoint(t *testing.T) {
	for name, tab := range map[string][]span{"zeroWidth": zeroWidth, "wide": wide} {
		for i, s := range tab {
			if s.lo > s.hi {
				t.Errorf("%s[%d] = %x-%x is backwards", name, i, s.lo, s.hi)
			}
			if i > 0 && tab[i-1].hi >= s.lo {
				t.Errorf("%s[%d] = %x-%x overlaps or precedes the previous range (binary search needs them sorted)", name, i, s.lo, s.hi)
			}
		}
	}
}

func TestHex(t *testing.T) {
	if c := Hex("#7aa2f7"); c != RGB(0x7a, 0xa2, 0xf7) {
		t.Errorf("%+v", c)
	}
	if c := Hex("FFFFFF"); c != RGB(255, 255, 255) {
		t.Errorf("%+v", c)
	}
	for _, bad := range []string{"", "#fff", "#gggggg", "#12345"} {
		if c := Hex(bad); c != Default() {
			t.Errorf("Hex(%q) = %+v, want the default colour", bad, c)
		}
	}
}

func TestStyleIsAValue(t *testing.T) {
	base := Style{}.Fg(ANSI(2))
	bold := base.With(Bold | Dim)
	if base.Has(Bold) || !bold.Has(Bold) || !bold.Has(Dim) || bold.Without(Dim).Has(Dim) || !bold.Without(Dim).Has(Bold) {
		t.Errorf("attributes: %+v %+v", base, bold)
	}
}

func TestLineBasics(t *testing.T) {
	red, blue := Style{}.Fg(ANSI(1)), Style{}.Fg(ANSI(4))
	l := Join(Styled(red, "ab"), Text("中"), Styled(blue, "c"))
	if l.Width() != 5 || l.Plain() != "ab中c" {
		t.Errorf("width %d plain %q", l.Width(), l.Plain())
	}
	if got := l.Pad(8, Style{}); got.Width() != 8 || got.Plain() != "ab中c   " {
		t.Errorf("pad: %q", got.Plain())
	}
	if got := l.Pad(3, Style{}); !reflect.DeepEqual(got, l) {
		t.Error("a wide enough line is returned as it is")
	}
	if got := Spaces(3, red); got.Width() != 3 || Spaces(0, red) != nil {
		t.Errorf("spaces: %v", got)
	}
	if got := (Line{}).Append(Span{Text: ""}, Span{Text: "x"}); len(got) != 1 {
		t.Errorf("append dropped nothing: %v", got)
	}
}

func TestTruncate(t *testing.T) {
	red := Style{}.Fg(ANSI(1))
	l := Join(Styled(red, "hello "), Text("world"))
	if got := l.Truncate(20, "…"); !reflect.DeepEqual(got, l) {
		t.Error("a line that fits is unchanged")
	}
	got := l.Truncate(8, "…")
	if got.Plain() != "hello w…" || got.Width() != 8 {
		t.Errorf("truncate: %q (%d)", got.Plain(), got.Width())
	}
	if got[0].Style != red {
		t.Error("styles must survive a cut")
	}
	// A wide rune is never split: the cut lands before it.
	w := Text("ab中中cd").Truncate(5, "")
	if w.Plain() != "ab中" || w.Width() != 4 {
		t.Errorf("wide: %q", w.Plain())
	}
	if got := Text("abcdef").Truncate(0, "…"); got != nil {
		t.Errorf("zero width: %v", got)
	}
	if got := Text("abcdef").Truncate(1, "…"); got.Plain() != "a" {
		t.Errorf("an ellipsis as wide as the line is dropped: %q", got.Plain())
	}
}

func plain(ls []Line) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Plain())
	}
	return out
}

func TestWrap(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		w, ind int
		want   []string
	}{
		{"fits", "hello world", 20, 0, []string{"hello world"}},
		{"at a space", "the quick brown fox", 10, 0, []string{"the quick", "brown fox"}},
		{"exactly full", "abcde fghij", 5, 0, []string{"abcde", "fghij"}},
		{"long word is broken", "abcdefghij", 4, 0, []string{"abcd", "efgh", "ij"}},
		{"word after a long one", "abcdefghij k", 4, 0, []string{"abcd", "efgh", "ij k"}},
		{"indent on continuation", "one two three four", 9, 2, []string{"one two", "  three", "  four"}},
		{"spaces at a break vanish", "aaa    bbb", 3, 0, []string{"aaa", "bbb"}},
		{"leading spaces on the first line stay out of the way", "  hi", 10, 0, []string{"hi"}},
		{"wide runes", "中中中中", 5, 0, []string{"中中", "中中"}},
		{"a wide rune wider than the line", "中", 1, 0, []string{"中"}},
		{"empty", "", 10, 0, []string{""}},
	}
	for _, c := range cases {
		got := plain(Text(c.in).Wrap(c.w, c.ind))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Wrap(%q, %d, %d) = %q, want %q", c.name, c.in, c.w, c.ind, got, c.want)
		}
	}
}

func TestWrapKeepsStylesAndNeverExceedsTheWidth(t *testing.T) {
	red, blue := Style{}.Fg(ANSI(1)), Style{}.Fg(ANSI(4))
	l := Join(Styled(red, "error: "), Styled(blue, "the quick brown fox jumps over the lazy dog"))
	for w := 4; w <= 30; w++ {
		for _, ln := range l.Wrap(w, 2) {
			if ln.Width() > w {
				t.Fatalf("w=%d: %q is %d cells", w, ln.Plain(), ln.Width())
			}
		}
	}
	lines := l.Wrap(16, 0)
	if lines[0][0].Style != red || !strings.HasPrefix(lines[0][0].Text, "error:") {
		t.Errorf("first span lost its style: %+v", lines[0])
	}
	for _, ln := range lines[1:] {
		for _, sp := range ln {
			if sp.Style != blue && strings.TrimSpace(sp.Text) != "" {
				t.Errorf("continuation span lost its style: %+v", sp)
			}
		}
	}
	// Nothing is lost: the words come back in order.
	if got := strings.Join(strings.Fields(strings.Join(plain(lines), " ")), " "); got != "error: the quick brown fox jumps over the lazy dog" {
		t.Errorf("text changed: %q", got)
	}
}

func TestCombiningMarksStayWithTheirBase(t *testing.T) {
	got := plain(Text("ééé").Wrap(2, 0))
	if !reflect.DeepEqual(got, []string{"éé", "é"}) {
		t.Errorf("%q", got)
	}
}
