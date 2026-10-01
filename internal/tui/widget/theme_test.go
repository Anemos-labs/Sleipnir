package widget

import (
	"flag"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

// Shared test support: the -update flag, golden files, the themes every widget is tested in, and the checks every widget's
// output must pass. (The goldens of this package live in testdata/golden; read the diff of a golden before committing it.)

func init() {
	// A second agent adding goldens to this package must not define -update again: the flag is registered once, here,
	// unless somebody has registered it already.
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite golden files under testdata/golden")
	}
}

func goldenUpdate() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// checkGolden compares got with testdata/golden/<name>.txt, or rewrites it with -update.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	widgettest.Golden(t, goldenUpdate(), filepath.Join("testdata", "golden", name+".txt"), got)
}

// checkGoldenLines is golden for lines, written with the styles in them using the names of the theme's colours.
func checkGoldenLines(t *testing.T, name string, th Theme, ls []cell.Line) {
	t.Helper()
	var got string
	if th.Mono {
		got = widgettest.FlattenStyled(ls)
	} else {
		got = widgettest.FlattenStyledWith(ls, themeColourNames(th))
	}
	checkGolden(t, name, got)
}

// themeColourNames names the colours of a theme by the role that uses them, so a golden file says {fg=good} and not {fg=#9ece6a}.
func themeColourNames(th Theme) map[cell.Color]string {
	names := map[cell.Color]string{}
	put := func(c cell.Color, n string) {
		if c.Kind != cell.KindDefault {
			if _, ok := names[c]; !ok {
				names[c] = n
			}
		}
	}
	put(th.Good.FG, "good")
	put(th.Bad.FG, "bad")
	put(th.Warn.FG, "warn")
	put(th.Info.FG, "info")
	put(th.Accent.FG, "accent")
	put(th.Dim.FG, "dim")
	put(th.Faint.FG, "faint")
	put(th.Code.FG, "code")
	put(th.Quote.FG, "quote")
	put(th.DiffAdd.BG, "add-bg")
	put(th.DiffDel.BG, "del-bg")
	put(th.DiffAddWord.BG, "add-word-bg")
	put(th.DiffDelWord.BG, "del-word-bg")
	put(th.CodeBlock.BG, "slab")
	put(th.SelectedRow.BG, "selected-bg")
	put(th.Zebra.BG, "zebra-bg")
	for i, c := range th.Layer {
		put(c, fmt.Sprintf("G%d", i))
	}
	return names
}

type themeCase struct {
	name string
	th   Theme
}

// allTestThemes are the themes every widget is run in: the three palettes, and the dark one with ASCII glyphs.
func allTestThemes() []themeCase {
	return []themeCase{
		{"default", DefaultTheme()},
		{"light", LightTheme()},
		{"mono", MonoTheme()},
		{"ascii", DefaultTheme().WithASCII(true)},
		{"mono-ascii", MonoTheme().WithASCII(true)},
	}
}

// checkLines is what every widget's output must satisfy at every width: no line wider than the width, no control character or
// invalid UTF-8 anywhere, no newline or tab in a span.
func checkLines(t testing.TB, what string, ls []cell.Line, width int) {
	t.Helper()
	for i, l := range ls {
		if w := l.Width(); w > width {
			t.Fatalf("%s: width %d: line %d is %d cells wide: %q", what, width, i, w, l.Plain())
		}
		for _, sp := range l {
			if !utf8.ValidString(sp.Text) {
				t.Fatalf("%s: width %d: line %d has invalid UTF-8: %q", what, width, i, sp.Text)
			}
		}
	}
	if where, bad := widgettest.Control(ls); bad {
		t.Fatalf("%s: width %d: control character survived: %s", what, width, where)
	}
}

// widgetUnderRace says whether the race detector is on (it makes code several times slower, so sizes come down).
func widgetUnderRace() bool {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "-race" && s.Value == "true" {
				return true
			}
		}
	}
	return false
}

func TestThemesAreComparableValues(t *testing.T) {
	a, b := DefaultTheme(), DefaultTheme()
	if a != b {
		t.Fatal("two calls of DefaultTheme differ")
	}
	if a == LightTheme() || a == MonoTheme() || LightTheme() == MonoTheme() {
		t.Fatal("the themes are not distinct")
	}
	if a.WithASCII(true) == a || a.WithASCII(true).WithASCII(false) != a {
		t.Fatal("WithASCII must change only the ASCII flag")
	}
}

func TestDefaultThemeUsesTheSketchPalette(t *testing.T) {
	th := DefaultTheme()
	// docs/design/ux/sketchlib.py: PAL and LAYER.
	checks := []struct {
		name string
		got  cell.Color
		want string
	}{
		{"Dim", th.Dim.FG, "#565f89"}, {"Faint", th.Faint.FG, "#3b4261"}, {"Good", th.Good.FG, "#9ece6a"},
		{"Bad", th.Bad.FG, "#f7768e"}, {"Warn", th.Warn.FG, "#e0af68"}, {"Info", th.Info.FG, "#7aa2f7"},
		{"Accent", th.Accent.FG, "#bb9af7"}, {"CodeBlock", th.CodeBlock.BG, "#16161e"}, {"SelectedRow", th.SelectedRow.BG, "#283457"},
		{"DiffDel bg", th.DiffDel.BG, "#3b2030"}, {"DiffAdd bg", th.DiffAdd.BG, "#20352b"},
		{"G0", th.Layer[0], "#4c6fd0"}, {"G1", th.Layer[1], "#7aa2f7"}, {"G2", th.Layer[2], "#2ac3de"}, {"G3", th.Layer[3], "#9ece6a"},
		{"G4", th.Layer[4], "#e0af68"}, {"G5", th.Layer[5], "#ff9e64"}, {"G6", th.Layer[6], "#f7768e"},
	}
	for _, c := range checks {
		if c.got != cell.Hex(c.want) {
			t.Errorf("%s = %+v, want %s", c.name, c.got, c.want)
		}
	}
	if th.Text != (cell.Style{}) {
		t.Errorf("body text must use the terminal's own colours, got %+v", th.Text)
	}
}

func TestMonoThemeHasNoColoursAtAll(t *testing.T) {
	th := MonoTheme()
	if !th.Mono {
		t.Error("Mono flag")
	}
	v := reflect.ValueOf(th)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		switch f := v.Field(i).Interface().(type) {
		case cell.Style:
			if f.FG.Kind != cell.KindDefault || f.BG.Kind != cell.KindDefault {
				t.Errorf("%s has a colour: %+v", name, f)
			}
		case [6]cell.Style:
			for j, s := range f {
				if s.FG.Kind != cell.KindDefault || s.BG.Kind != cell.KindDefault {
					t.Errorf("%s[%d] has a colour: %+v", name, j, s)
				}
			}
		case [7]cell.Color:
			for j, c := range f {
				if c.Kind != cell.KindDefault {
					t.Errorf("%s[%d] is a colour: %+v", name, j, c)
				}
			}
		}
	}
	const allowed = cell.Bold | cell.Dim | cell.Italic | cell.Underline | cell.Reverse | cell.Strike
	if th.Link.Attr&^allowed != 0 {
		t.Error("attributes outside the set")
	}
}

func TestHeadingLevelsAreDistinguishableInEveryTheme(t *testing.T) {
	for _, nt := range allTestThemes() {
		seen := map[cell.Style]int{}
		for i, st := range nt.th.Heading {
			if j, dup := seen[st]; dup {
				t.Errorf("%s: H%d and H%d have the same style %+v", nt.name, j+1, i+1, st)
			}
			seen[st] = i
		}
		if nt.th.Mono {
			for i, st := range nt.th.Heading {
				if st.Attr == 0 {
					t.Errorf("%s: H%d has no attribute: in mono only attributes can tell levels apart", nt.name, i+1)
				}
			}
		}
	}
}

func TestLayersRunColdToHot(t *testing.T) {
	for _, th := range []Theme{DefaultTheme(), LightTheme()} {
		seen := map[cell.Color]bool{}
		for i, c := range th.Layer {
			if c.Kind != cell.KindRGB || seen[c] {
				t.Errorf("layer %d: %+v is not a distinct RGB colour", i, c)
			}
			seen[c] = true
		}
		// cold to hot: G0 is bluer than G6, which is redder
		if th.Layer[0].B <= th.Layer[0].R || th.Layer[6].R <= th.Layer[6].B {
			t.Errorf("G0 %+v should be cold and G6 %+v hot", th.Layer[0], th.Layer[6])
		}
	}
}

func TestThemeFor(t *testing.T) {
	cases := []struct {
		colour, light bool
		want          Theme
	}{
		{true, false, DefaultTheme()}, {true, true, LightTheme()}, {false, false, MonoTheme()}, {false, true, MonoTheme()},
	}
	for _, c := range cases {
		if got := ThemeFor(c.colour, c.light); got != c.want {
			t.Errorf("ThemeFor(%v, %v) picked the wrong theme", c.colour, c.light)
		}
	}
}

func TestThemeAccessorsClamp(t *testing.T) {
	th := DefaultTheme()
	if th.LayerStyle(-5).FG != th.Layer[0] || th.LayerStyle(99).FG != th.Layer[6] || th.LayerStyle(3).FG != th.Layer[3] {
		t.Error("LayerStyle must clamp its index")
	}
	if th.HeadingStyle(0) != th.Heading[0] || th.HeadingStyle(9) != th.Heading[5] || th.HeadingStyle(2) != th.Heading[1] {
		t.Error("HeadingStyle must clamp its level")
	}
	if g := th.glyphs(); g.ellipsis != "…" {
		t.Error("unicode glyphs expected")
	}
	if g := th.WithASCII(true).glyphs(); strings.ContainsFunc(g.ellipsis+g.quote+g.codeBar+g.cont+g.rule+g.vbar+g.cross+g.sel+g.minus+g.gap+g.gaugeOn+g.gaugeOff+strings.Join(g.bullets[:], ""), func(r rune) bool { return r > 127 }) {
		t.Error("the ASCII glyph set has a non-ASCII rune")
	}
}

func TestComposeAndOverlay(t *testing.T) {
	base := cell.Style{FG: cell.ANSI(1), Attr: cell.Bold}
	over := cell.Style{BG: cell.ANSI(2), Attr: cell.Italic}
	got := composeStyle(base, over)
	if got.FG != cell.ANSI(1) || got.BG != cell.ANSI(2) || got.Attr != cell.Bold|cell.Italic {
		t.Errorf("compose: %+v", got)
	}
	if composeStyle(base, cell.Style{FG: cell.ANSI(5)}).FG != cell.ANSI(5) {
		t.Error("the overlay's colour wins")
	}
	l := cell.Line{{Text: "a", Style: cell.Style{FG: cell.ANSI(3)}}, {Text: "b"}}
	o := overlayStyle(l, cell.Style{FG: cell.ANSI(4), BG: cell.ANSI(6), Attr: cell.Reverse})
	if o[0].Style.FG != cell.ANSI(3) || o[0].Style.BG != cell.ANSI(6) || o[1].Style.FG != cell.ANSI(4) || !o[1].Style.Has(cell.Reverse) {
		t.Errorf("overlay: %+v", o)
	}
	if l[1].Style != (cell.Style{}) {
		t.Error("overlay must not modify its input")
	}
}

// asciiDoc is one document for every widget in which everything is ASCII, so that any non-ASCII rune in the output of an ASCII
// theme was drawn by the widget.
const asciiDoc = "# Title\n\nSome *text* with `code`, a [link](https://x.y/z) and ~~strike~~.\n\n- a\n  - b\n- [x] done\n\n> quote\n\n" +
	"```go\nx := 1\n```\n\n| a | b |\n|---|--:|\n| 1 | 22 |\n\n---\n\n1. one\n2. two\n"

func TestASCIIThemesDrawOnlyASCII(t *testing.T) {
	ascii := func(ls []cell.Line) bool {
		return !strings.ContainsFunc(widgettest.Flatten(ls), func(r rune) bool { return r > 127 })
	}
	for _, base := range []Theme{DefaultTheme(), LightTheme(), MonoTheme()} {
		th := base.WithASCII(true)
		checks := map[string][]cell.Line{
			"markdown": Markdown(asciiDoc, 40, th),
			"diff":     Diff("a/b.go", "a\nb\nc\n"+strings.Repeat("long ", 30)+"\n", "a\nB\nc\n"+strings.Repeat("long ", 30)+"x\n", 40, th, DiffOptions{}),
			"unified":  UnifiedDiff("--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@ main\n a\n-b\n+B\n", 40, th),
			"box":      Box("Title", plainLines("body line", strings.Repeat("wrap ", 20)), 30, th, BoxHardWrap()),
			"dialog":   Dialog("Bash", plainLines("ls -la"), PermissionOptions("ls"), 1, 40, th),
			"table":    Table{Columns: []Column{{Title: "Name"}, {Title: "Qty", Align: AlignRight}}, Rows: [][]cell.Line{tblRow("apple", "3"), tblRow("kiwi-very-long-name-indeed", "12")}, Grid: true}.RenderSelected(20, th, 1),
			"gauge":    {Gauge(0.4, 10, th)},
		}
		for name, ls := range checks {
			if !ascii(ls) {
				t.Errorf("%s in an ASCII theme draws non-ASCII:\n%s", name, widgettest.Flatten(ls))
			}
		}
	}
}

func TestMonoThemeOutputHasNoColour(t *testing.T) {
	th := MonoTheme()
	g := widgettest.NewRand(23)
	plain := func(name string, ls []cell.Line) {
		t.Helper()
		for i, l := range ls {
			for _, sp := range l {
				if sp.Style.FG.Kind != cell.KindDefault || sp.Style.BG.Kind != cell.KindDefault {
					t.Fatalf("%s: line %d has a colour: %+v", name, i, sp)
				}
			}
		}
	}
	for i := 0; i < 30; i++ {
		src := g.Markdown(30)
		plain("markdown", Markdown(src, 40, th))
		plain("diff", Diff("p", g.Text(10), g.Text(10), 50, th, DiffOptions{}))
		plain("unified", UnifiedDiff(udMakePatch("f", g.Text(10), g.Text(10), 2), 50, th))
		plain("box", Box(g.Text(2), plainLines(g.Text(8)), 40, th, BoxAccent(cell.Hex("#ff0000"))))
		plain("dialog", Dialog(g.Text(2), plainLines(g.Text(8)), PermissionOptions(g.Text(1)), g.Intn(3), 50, th))
		plain("table", Table{Columns: []Column{{Title: g.Text(1)}, {Title: g.Text(1)}}, Rows: [][]cell.Line{tblRow(g.Text(2), g.Text(2))}, Zebra: true}.RenderSelected(40, th, 0))
		plain("gauge", []cell.Line{Gauge(float64(g.Intn(101))/100, 12, th)})
	}
	for _, b := range strings.Split(asciiDoc, "\n\n") {
		plain("markdown sample", Markdown(b, 30, th))
	}
}
