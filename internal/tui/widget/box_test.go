package widget

import (
	"reflect"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget/widgettest"
)

func plainLines(ss ...string) []cell.Line {
	out := make([]cell.Line, len(ss))
	for i, s := range ss {
		out[i] = cell.Text(s)
	}
	return out
}

func TestBoxLayout(t *testing.T) {
	th := MonoTheme()
	cases := []struct {
		name  string
		title string
		body  []string
		w     int
		opts  []BoxOpt
		want  string
	}{
		{"basic", "Title", []string{"hello"}, 20, nil,
			"╭─ Title ──────────╮\n│ hello            │\n╰──────────────────╯"},
		{"no title", "", []string{"hello"}, 12, nil,
			"╭──────────╮\n│ hello    │\n╰──────────╯"},
		{"empty body", "T", nil, 10, nil,
			"╭─ T ────╮\n╰────────╯"},
		{"title truncated", "A very long title indeed", []string{"x"}, 16, nil,
			"╭─ A very lo… ─╮\n│ x            │\n╰──────────────╯"},
		{"title that just fits", "abcd", []string{"x"}, 10, nil,
			"╭─ abcd ─╮\n│ x      │\n╰────────╯"},
		{"title with no room is dropped, and so is the padding", "abcd", []string{"x"}, 6, nil,
			"╭────╮\n│x   │\n╰────╯"},
		{"square", "", []string{"x"}, 7, []BoxOpt{BoxBorder(BorderSquare)}, "┌─────┐\n│ x   │\n└─────┘"},
		{"double", "", []string{"x"}, 7, []BoxOpt{BoxBorder(BorderDouble)}, "╔═════╗\n║ x   ║\n╚═════╝"},
		{"heavy", "", []string{"x"}, 7, []BoxOpt{BoxBorder(BorderHeavy)}, "┏━━━━━┓\n┃ x   ┃\n┗━━━━━┛"},
		{"ascii", "", []string{"x"}, 7, []BoxOpt{BoxBorder(BorderASCII)}, "+-----+\n| x   |\n+-----+"},
		{"unknown border falls back", "", []string{"x"}, 7, []BoxOpt{BoxBorder(99)}, "╭─────╮\n│ x   │\n╰─────╯"},
		{"padding", "", []string{"x"}, 11, []BoxOpt{BoxPadding(3, 1)},
			"╭─────────╮\n│         │\n│   x     │\n│         │\n╰─────────╯"},
		{"no padding", "", []string{"xyz"}, 7, []BoxOpt{BoxPadding(0, 0)}, "╭─────╮\n│xyz  │\n╰─────╯"},
		{"padding shrinks to keep a cell of body", "", []string{"xyz"}, 5, []BoxOpt{BoxPadding(9, 0)}, "╭───╮\n│xyz│\n╰───╯"},
		{"words wrap", "", []string{"the quick brown fox jumps"}, 14, nil,
			"╭────────────╮\n│ the quick  │\n│ brown fox  │\n│ jumps      │\n╰────────────╯"},
		{"a long word is broken", "", []string{"abcdefghijklmnop"}, 10, nil,
			"╭────────╮\n│ abcdef │\n│ ghijkl │\n│ mnop   │\n╰────────╯"},
		{"hard wrap keeps spaces and marks continuations", "", []string{"ab  cd  ef  gh"}, 10, []BoxOpt{BoxHardWrap()},
			"╭────────╮\n│ ab  cd │\n│ ↳  ef  │\n│ ↳ gh   │\n╰────────╯"},
		{"truncate", "", []string{"the quick brown fox"}, 12, []BoxOpt{BoxTruncate()},
			"╭──────────╮\n│ the qui… │\n╰──────────╯"},
		{"a line that fits is not disturbed", "", []string{"  indented", "a   b"}, 14, nil,
			"╭────────────╮\n│   indented │\n│ a   b      │\n╰────────────╯"},
		{"wide runes", "", []string{"日本語のテキスト"}, 12, nil,
			"╭──────────╮\n│ 日本語の │\n│ テキスト │\n╰──────────╯"},
		{"blank lines are kept", "", []string{"a", "", "b"}, 7, nil,
			"╭─────╮\n│ a   │\n│     │\n│ b   │\n╰─────╯"},
	}
	for _, c := range cases {
		got := Box(c.title, plainLines(c.body...), c.w, th, c.opts...)
		checkLines(t, c.name, got, c.w)
		if f := widgettest.Flatten(got); f != c.want {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", c.name, f, c.want)
		}
		for i, l := range got {
			if l.Width() != c.w {
				t.Errorf("%s: line %d is %d cells, a box is exactly as wide as asked: %q", c.name, i, l.Width(), l.Plain())
			}
		}
	}
}

func TestBoxTooNarrowForABorder(t *testing.T) {
	th := MonoTheme()
	body := plainLines("abcdef", "日本")
	for w := -1; w <= 3; w++ {
		got := Box("title", body, w, th)
		if w < 1 {
			if got != nil {
				t.Errorf("width %d: want nil, got %v", w, got)
			}
			continue
		}
		if len(got) != 2 {
			t.Errorf("width %d: the body comes back without a border: %v", w, got)
		}
		checkLines(t, "narrow box", got, w)
	}
	got := Box("", plainLines("abcdef"), 4, th)
	checkLines(t, "width 4", got, 4)
	if len(got) != 5 || got[0].Plain() != "╭──╮" || got[1].Plain() != "│ab│" {
		t.Errorf("width 4 is the narrowest box, two cells of body and no padding:\n%s", widgettest.Flatten(got))
	}
}

func TestBoxInnerWidth(t *testing.T) {
	cases := []struct {
		w    int
		opts []BoxOpt
		want int
	}{
		{20, nil, 16}, {20, []BoxOpt{BoxPadding(0, 0)}, 18}, {20, []BoxOpt{BoxPadding(4, 0)}, 10}, {4, nil, 2}, {5, nil, 3}, {3, nil, 0}, {0, nil, 0}, {-5, nil, 0},
		{6, []BoxOpt{BoxPadding(16, 0)}, 4},
	}
	for _, c := range cases {
		if got := BoxInnerWidth(c.w, c.opts...); got != c.want {
			t.Errorf("BoxInnerWidth(%d) = %d, want %d", c.w, got, c.want)
		}
		if c.want > 0 {
			// the promise: a body line of exactly that width is not wrapped
			line := cell.Text(strings.Repeat("x", c.want))
			got := Box("", []cell.Line{line}, c.w, MonoTheme(), c.opts...)
			if len(got) != 3 {
				t.Errorf("width %d: a line of the inner width (%d) was wrapped: %v", c.w, c.want, widgettest.Flatten(got))
			}
		}
	}
}

func TestBoxStyles(t *testing.T) {
	th := DefaultTheme()
	red := cell.Hex("#ff0000")
	got := Box("Title", []cell.Line{cell.Styled(th.Good, "ok")}, 20, th, BoxAccent(red))
	if got[0][0].Style.FG != red {
		t.Errorf("the accent colours the border: %+v", got[0][0].Style)
	}
	var title cell.Span
	for _, sp := range got[0] {
		if strings.Contains(sp.Text, "Title") {
			title = sp
		}
	}
	if title.Style.FG != red || !title.Style.Has(cell.Bold) {
		t.Errorf("the title is bold and in the accent: %+v", title.Style)
	}
	if got[1][0].Style.FG != red || got[2][0].Style.FG != red {
		t.Error("the side and the bottom edge take the accent")
	}
	for _, sp := range got[1] {
		if sp.Text == "ok" && sp.Style != th.Good {
			t.Errorf("the body keeps its own style: %+v", sp.Style)
		}
	}
	plain := Box("Title", plainLines("x"), 20, th)
	if plain[0][0].Style != th.Border {
		t.Errorf("without an accent the border is the theme's Border: %+v", plain[0][0].Style)
	}
	// Mono has no colours, so an accent is ignored
	mono := Box("Title", plainLines("x"), 20, MonoTheme(), BoxAccent(red))
	for _, l := range mono {
		for _, sp := range l {
			if sp.Style.FG.Kind != cell.KindDefault {
				t.Fatalf("an accent leaked into the mono theme: %+v", sp.Style)
			}
		}
	}
	// an ASCII theme draws ASCII borders, whatever border was asked for
	ascii := Box("T", plainLines("x"), 12, th.WithASCII(true), BoxBorder(BorderDouble))
	if f := widgettest.Flatten(ascii); f != "+- T ------+\n| x        |\n+----------+" {
		t.Errorf("ascii box:\n%s", f)
	}
}

func TestBoxHostileText(t *testing.T) {
	for _, h := range widgettest.Hostile() {
		for _, w := range []int{10, 24, 60} {
			body := []cell.Line{cell.Text(h), {{Text: h, Style: cell.Style{Attr: cell.Bold}}}}
			for _, opts := range [][]BoxOpt{nil, {BoxHardWrap()}, {BoxTruncate()}} {
				got := Box(h, body, w, DefaultTheme(), opts...)
				checkLines(t, "hostile box", got, w)
			}
		}
	}
}

func TestBoxUnicode(t *testing.T) {
	for _, u := range widgettest.Unicode() {
		for w := 4; w <= 50; w += 3 {
			for _, opts := range [][]BoxOpt{nil, {BoxHardWrap()}, {BoxTruncate()}, {BoxPadding(0, 1)}} {
				got := Box(u, plainLines(u, u+" "+u), w, MonoTheme(), opts...)
				checkLines(t, "unicode box", got, w)
				for i, l := range got {
					if l.Width() != w {
						t.Fatalf("w=%d opts=%v: line %d is %d cells: %q", w, len(opts), i, l.Width(), l.Plain())
					}
				}
			}
		}
	}
}

func TestBoxDeterministicAndDoesNotModifyItsInput(t *testing.T) {
	body := []cell.Line{{{Text: "a\tb\x1b[31m", Style: cell.Style{Attr: cell.Bold}}}, cell.Text("the quick brown fox jumps over")}
	saved := []cell.Line{append(cell.Line(nil), body[0]...), append(cell.Line(nil), body[1]...)}
	a := Box("t", body, 20, DefaultTheme())
	b := Box("t", body, 20, DefaultTheme())
	if !reflect.DeepEqual(a, b) {
		t.Error("not deterministic")
	}
	if !reflect.DeepEqual(body, saved) {
		t.Error("the body was modified")
	}
}

func TestBoxPropertyRandomTextAtEveryWidth(t *testing.T) {
	g := widgettest.NewRand(11)
	for i := 0; i < 60; i++ {
		body := plainLines(g.Text(1+g.Intn(12)), g.Text(g.Intn(6)))
		title := g.Text(g.Intn(5))
		for w := 10; w <= 120; w += 5 {
			for _, opts := range [][]BoxOpt{nil, {BoxHardWrap()}, {BoxTruncate(), BoxPadding(2, 1)}} {
				got := Box(title, body, w, DefaultTheme(), opts...)
				checkLines(t, "random box", got, w)
				for _, l := range got {
					if l.Width() != w {
						t.Fatalf("w=%d: a box line is %d cells", w, l.Width())
					}
				}
			}
		}
	}
}

func TestBoxGolden(t *testing.T) {
	for _, nt := range []themeCase{{"default", DefaultTheme()}, {"mono", MonoTheme()}} {
		th := nt.th
		body := []cell.Line{
			cell.Join(cell.Text("status: "), cell.Styled(th.Good, "ok"), cell.Text("  "), cell.Styled(th.Bad, "3 failed")),
			nil,
			cell.Styled(th.Dim, "a second line that is long enough to wrap around the edge"),
		}
		var ls []cell.Line
		ls = append(ls, Box("Tests", body, 34, th)...)
		ls = append(ls, Box("Warning", plainLines("careful"), 34, th, BoxAccent(th.Warn.FG), BoxBorder(BorderDouble), BoxPadding(2, 1))...)
		ls = append(ls, Box("", plainLines("no title, truncated: "+strings.Repeat("x", 40)), 34, th, BoxTruncate())...)
		checkGoldenLines(t, "box_"+nt.name, th, ls)
	}
}
