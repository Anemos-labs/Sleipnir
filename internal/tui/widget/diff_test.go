package widget

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

// ---- rendering ----

const diffGoBefore = "package orders\n\nfunc (s *Store) List(ctx context.Context, page, size int) ([]Order, error) {\n\toffset := page * size\n\trows, err := s.db.QueryContext(ctx, listSQL, size, offset)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn scan(rows)\n}\n\nfunc other() {}\n\nfunc last() int { return 1 }\n"

var diffGoAfter = strings.NewReplacer("offset := page * size", "offset := (page - 1) * size", "return 1 }", "return 2 }").Replace(diffGoBefore)

func TestDiffGolden(t *testing.T) {
	for _, nt := range []themeCase{{"mono", MonoTheme()}, {"default", DefaultTheme()}, {"light", LightTheme()}} {
		checkGoldenLines(t, "diff_"+nt.name+"_80", nt.th, Diff("orders/list.go", diffGoBefore, diffGoAfter, 80, nt.th, DiffOptions{Context: 1}))
	}
	checkGoldenLines(t, "diff_default_44", DefaultTheme(), Diff("orders/list.go", diffGoBefore, diffGoAfter, 44, DefaultTheme(), DiffOptions{}))
	checkGoldenLines(t, "diff_mono_ascii_60", MonoTheme().WithASCII(true), Diff("orders/list.go", diffGoBefore, diffGoAfter, 60, MonoTheme().WithASCII(true), DiffOptions{}))
	checkGoldenLines(t, "diff_new_file_mono", MonoTheme(), Diff("docs/new.md", "", "# Title\n\nbody\n", 40, MonoTheme(), DiffOptions{}))
}

func TestDiffHunksAndContext(t *testing.T) {
	th := MonoTheme()
	before := numberedLines(20)
	change := func(ns ...int) string {
		out := strings.Split(strings.TrimSuffix(before, "\n"), "\n")
		for _, n := range ns {
			out[n-1] = fmt.Sprintf("CHANGED %d", n)
		}
		return strings.Join(out, "\n") + "\n"
	}
	rows := func(after string, o DiffOptions) []string {
		var out []string
		for _, l := range strings.Split(widgettest.Flatten(Diff("f", before, after, 60, th, o)), "\n")[1:] {
			out = append(out, l)
		}
		return out
	}
	// default context is two lines
	got := rows(change(10), DiffOptions{})
	want := []string{" 8  8   line 8", " 9  9   line 9", "10    - line 10", "   10 + CHANGED 10", "11 11   line 11", "12 12   line 12"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("one change:\n%s", strings.Join(got, "\n"))
	}
	// two changes whose contexts touch are one hunk; one line more and they are two
	got = rows(change(5, 9), DiffOptions{})
	for _, r := range got {
		if strings.Contains(r, "⋯") {
			t.Errorf("changes 3 lines apart are one hunk:\n%s", strings.Join(got, "\n"))
		}
	}
	got = rows(change(5, 10), DiffOptions{})
	for _, r := range got {
		if strings.Contains(r, "⋯") {
			t.Errorf("four unchanged lines between the changes: the two contexts touch, one hunk:\n%s", strings.Join(got, "\n"))
		}
	}
	got = rows(change(5, 11), DiffOptions{})
	if sep := strings.Join(got, "\n"); !strings.Contains(sep, "⋯ 1 unchanged line\n") {
		t.Errorf("five lines between the changes are two hunks with a note between them:\n%s", sep)
	}
	got = rows(change(3, 18), DiffOptions{})
	if sep := strings.Join(got, "\n"); !strings.Contains(sep, "⋯ 10 unchanged lines\n") {
		t.Errorf("a long gap is counted:\n%s", sep)
	}
	// no context, and more
	got = rows(change(10), DiffOptions{Context: -1})
	if len(got) != 2 || !strings.Contains(got[0], "- line 10") || !strings.Contains(got[1], "+ CHANGED 10") {
		t.Errorf("no context:\n%s", strings.Join(got, "\n"))
	}
	got = rows(change(10), DiffOptions{Context: 4})
	if len(got) != 4+2+4 {
		t.Errorf("four lines of context: %d rows", len(got))
	}
	got = rows(change(10), DiffOptions{Context: 1000})
	if len(got) != 20+1 {
		t.Errorf("all the context there is: %d rows", len(got))
	}
	got = rows(change(1), DiffOptions{})
	if !strings.Contains(got[0], "- line 1") || len(got) != 2+2 {
		t.Errorf("a change on the first line has no context above it:\n%s", strings.Join(got, "\n"))
	}
	got = rows(change(20), DiffOptions{})
	if len(got) != 2+2 {
		t.Errorf("a change on the last line has no context below it:\n%s", strings.Join(got, "\n"))
	}
}

func TestDiffHeader(t *testing.T) {
	th := MonoTheme()
	head := func(path, before, after string, w int) string {
		first, _, _ := strings.Cut(widgettest.Flatten(Diff(path, before, after, w, th, DiffOptions{})), "\n")
		return first
	}
	cases := []struct {
		path, before, after string
		w                   int
		want                string
	}{
		{"a.go", "x\n", "y\n", 40, "a.go  +1 −1"},
		{"a.go", "x\n", "x\ny\nz\n", 40, "a.go  +2 −0"},
		{"a.go", "x\ny\nz\n", "x\n", 40, "a.go  +0 −2"},
		{"a.go", "x\n", "x\n", 40, "a.go (no changes)"},
		{"a.go", "", "x\ny\n", 40, "a.go  +2 −0 (new file)"},
		{"a.go", "x\ny\n", "", 40, "a.go  +0 −2 (deleted)"},
		{"a.go", "x\r\n", "x\n", 50, "a.go (line endings differ)"},
		{"a.go", "x", "x\n", 50, "a.go (line endings differ)"},
		{"", "x\n", "y\n", 40, "+1 −1"},
		{"internal/tui/widget/very/long/path/to/file.go", "x\n", "y\n", 30, "…y/long/path/to/file.go  +1 −1"},
		{"a\nb\x1b[31mc", "x\n", "y\n", 40, "a bc  +1 −1"},
	}
	for _, c := range cases {
		if got := head(c.path, c.before, c.after, c.w); got != c.want {
			t.Errorf("header for %q (%d cells): %q, want %q", c.path, c.w, got, c.want)
		}
	}
	// the counts are coloured: + green, − red
	d := Diff("a.go", "x\n", "y\n", 40, DefaultTheme(), DiffOptions{})[0]
	var plus, minus cell.Span
	for _, sp := range d {
		switch {
		case strings.HasPrefix(sp.Text, "+"):
			plus = sp
		case strings.HasPrefix(sp.Text, "−"):
			minus = sp
		}
	}
	if plus.Style.FG != DefaultTheme().Good.FG || minus.Style.FG != DefaultTheme().Bad.FG || !plus.Style.Has(cell.Bold) {
		t.Errorf("counts: %+v %+v", plus, minus)
	}
	ascii := widgettest.Flatten(Diff("a", "x\n", "y\n", 40, DefaultTheme().WithASCII(true), DiffOptions{}))
	if !strings.HasPrefix(ascii, "a  +1 -1\n") {
		t.Errorf("an ASCII minus: %q", ascii)
	}
	if got := widgettest.Flatten(Diff("a", "x\n", "y\n", 40, th, DiffOptions{NoHeader: true})); strings.Contains(got, "+1") {
		t.Errorf("NoHeader: %q", got)
	}
}

func TestDiffNumbersAndMarkersInPlainText(t *testing.T) {
	// the information a colour carries is in the text: which lines went, which came, and where they were
	th := MonoTheme()
	got := strings.Split(widgettest.Flatten(Diff("f.go", "a\nb\nc\nd\n", "a\nB\nc\nd\ne\n", 50, th, DiffOptions{})), "\n")
	want := []string{
		"f.go  +2 −1",
		"1 1   a",
		"2   - b",
		"  2 + B",
		"3 3   c",
		"4 4   d",
		"  5 + e",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// the added and removed words are marked by attributes
	ls := Diff("f.go", "let x = 1\n", "let y = 1\n", 50, th, DiffOptions{})
	for _, l := range ls[1:] {
		marked := ""
		for _, sp := range l {
			if sp.Style.Has(cell.Bold | cell.Underline) {
				marked += sp.Text
			}
		}
		if marked != "x" && marked != "y" {
			t.Errorf("row %q: bold+underlined text is %q, want just the changed word", l.Plain(), marked)
		}
	}
}

func TestDiffColoursTheWholeLine(t *testing.T) {
	th := DefaultTheme()
	ls := Diff("f.go", "keep\nold line here\nkeep2\n", "keep\nnew line here\nkeep2\n", 40, th, DiffOptions{Context: 1})
	for _, l := range ls[1:] {
		txt := l.Plain()
		switch {
		case strings.Contains(txt, "old line"):
			if l.Width() != 40 {
				t.Errorf("a removed row is padded to the width: %d", l.Width())
			}
			for _, sp := range l {
				if sp.Style.BG != th.DiffDel.BG && sp.Style.BG != th.DiffDelWord.BG {
					t.Errorf("a removed row is red all the way: %+v", sp)
				}
			}
		case strings.Contains(txt, "new line"):
			if l.Width() != 40 {
				t.Errorf("an added row is padded to the width: %d", l.Width())
			}
			for _, sp := range l {
				if sp.Style.BG != th.DiffAdd.BG && sp.Style.BG != th.DiffAddWord.BG {
					t.Errorf("an added row is green all the way: %+v", sp)
				}
			}
		default:
			for _, sp := range l {
				if sp.Style.BG.Kind != cell.KindDefault {
					t.Errorf("a context row has no background: %+v", sp)
				}
			}
		}
	}
	// the changed word is brighter than the rest of its line, and only the word
	var word []string
	for _, sp := range ls[2] {
		if sp.Style.BG == th.DiffDelWord.BG {
			word = append(word, sp.Text)
		}
	}
	if !reflect.DeepEqual(word, []string{"old"}) {
		t.Errorf("highlighted words of the removed line: %q", word)
	}
	// repeatLines with nothing in common are not word-highlighted: the line colour already says it
	ls = Diff("f.go", "alpha beta\n", "gamma delta\n", 40, th, DiffOptions{})
	for _, l := range ls[1:] {
		for _, sp := range l {
			if sp.Style.BG == th.DiffDelWord.BG || sp.Style.BG == th.DiffAddWord.BG {
				t.Errorf("dissimilar lines get no word highlight: %+v", sp)
			}
		}
	}
}

func TestDiffWrapsLongLinesWithAContinuationMarker(t *testing.T) {
	th := MonoTheme()
	long := "x := " + strings.Repeat("abcdefghij", 10)
	got := Diff("f", "a\n", "a\n"+long+"\n", 40, th, DiffOptions{})
	checkLines(t, "wrap", got, 40)
	var rows []string
	for _, l := range got[2:] {
		rows = append(rows, l.Plain())
	}
	if len(rows) < 4 || !strings.HasPrefix(rows[0], "  2 + x := ") {
		t.Fatalf("%q", rows)
	}
	var text strings.Builder
	for i, r := range rows {
		if i == 0 {
			text.WriteString(strings.TrimPrefix(r, "  2 + "))
			continue
		}
		if !strings.HasPrefix(r, "    ↳ ") {
			t.Errorf("a continuation row starts with blank numbers and ↳ in the marker column: %q", r)
		}
		text.WriteString(strings.TrimPrefix(r, "    ↳ "))
	}
	if text.String() != long {
		t.Errorf("wrapping lost text:\n%q\n%q", text.String(), long)
	}
	// continuation rows of an added line keep the green background
	dt := DefaultTheme()
	for _, l := range Diff("f", "a\n", "a\n"+long+"\n", 40, dt, DiffOptions{})[2:] {
		if l.Width() != 40 || l[len(l)-1].Style.BG != dt.DiffAdd.BG {
			t.Errorf("a wrapped row of an added line is full width and green: %d %+v", l.Width(), l[len(l)-1].Style)
		}
	}
	ascii := widgettest.Flatten(Diff("f", "a\n", "a\n"+long+"\n", 40, th.WithASCII(true), DiffOptions{}))
	if !strings.Contains(ascii, "    \\ ") || strings.ContainsFunc(ascii, func(r rune) bool { return r > 127 }) {
		t.Errorf("ASCII continuation marker:\n%s", ascii)
	}
}

func TestDiffTabsCRLFAndHostileText(t *testing.T) {
	th := MonoTheme()
	got := widgettest.Flatten(Diff("f", "a\n", "a\n\tif x {\n\t\ty\n", 40, th, DiffOptions{}))
	if !strings.Contains(got, "+     if x {") || !strings.Contains(got, "+         y") {
		t.Errorf("tabs expand to four columns:\n%s", got)
	}
	got = widgettest.Flatten(Diff("f", "a\n", "a\n\tx\n", 40, th, DiffOptions{TabWidth: 2}))
	if !strings.Contains(got, "+   x") || strings.Contains(got, "+     x") {
		t.Errorf("TabWidth:\n%s", got)
	}
	// CRLF files: the line ending is not a change
	crlf := Diff("f", "a\r\nb\r\nc\r\n", "a\r\nB\r\nc\r\n", 40, th, DiffOptions{})
	lf := Diff("f", "a\nb\nc\n", "a\nB\nc\n", 40, th, DiffOptions{})
	if !reflect.DeepEqual(crlf, lf) {
		t.Errorf("CRLF and LF give different diffs:\n%s\n%s", widgettest.Flatten(crlf), widgettest.Flatten(lf))
	}
	mixed := widgettest.Flatten(Diff("f", "a\r\nb\n", "a\nb\r\n", 40, th, DiffOptions{}))
	if !strings.Contains(mixed, "no visible") && !strings.Contains(mixed, "line endings") {
		t.Errorf("a pure line-ending change is named as one:\n%s", mixed)
	}
	for _, h := range widgettest.Hostile() {
		for _, nt := range allTestThemes() {
			for _, w := range []int{10, 33, 100} {
				checkLines(t, "hostile diff", Diff(h, h+"\nold\n"+h, h+"\nnew\n"+h+"x", w, nt.th, DiffOptions{}), w)
			}
		}
	}
}

func TestDiffMaxLines(t *testing.T) {
	th := MonoTheme()
	after := repeatLines(100, func(i int) string { return fmt.Sprintf("added %d", i) })
	full := Diff("f", "", after, 40, th, DiffOptions{})
	if len(full) != 101 {
		t.Fatalf("%d rows", len(full))
	}
	for _, max := range []int{1, 2, 10, 100, 101, 102, 1000} {
		got := Diff("f", "", after, 40, th, DiffOptions{MaxLines: max})
		checkLines(t, "max", got, 40)
		if max >= 101 {
			if len(got) != 101 {
				t.Errorf("MaxLines %d: %d rows, nothing to cut", max, len(got))
			}
			continue
		}
		if len(got) != max {
			t.Errorf("MaxLines %d: %d rows", max, len(got))
			continue
		}
		last := got[len(got)-1].Plain()
		if want := fmt.Sprintf("… %d more lines", 101-(max-1)); last != want {
			t.Errorf("MaxLines %d: footer %q, want %q", max, last, want)
		}
		if !reflect.DeepEqual(got[:max-1], full[:max-1]) {
			t.Errorf("MaxLines %d: the lines before the footer are the first lines of the diff", max)
		}
	}
	if got := widgettest.Flatten(Diff("f", "", after, 40, th.WithASCII(true), DiffOptions{MaxLines: 3})); !strings.HasSuffix(got, "... 99 more lines") {
		t.Errorf("ASCII footer: %q", got)
	}
}

func TestDiffWidthsAndLayoutModes(t *testing.T) {
	th := MonoTheme()
	modes := map[int]int{80: 2, 40: 2, 30: 2, 24: 2, 23: 1, 17: 1, 16: 0, 10: 0}
	for w, mode := range modes {
		lay := diffChooseLayout(w, 12, 12, false)
		if lay.mode != mode {
			t.Errorf("width %d: layout mode %d, want %d", w, lay.mode, mode)
		}
		if lay.textW < 1 || lay.prefixW+lay.textW > max(w, 3) {
			t.Errorf("width %d: prefix %d + text %d", w, lay.prefixW, lay.textW)
		}
	}
	if lay := diffChooseLayout(200, 5, 5, true); lay.mode != 0 {
		t.Error("NoLineNumbers")
	}
	got := widgettest.Flatten(Diff("f", "a\n", "b\n", 40, th, DiffOptions{NoLineNumbers: true}))
	if got != "f  +1 −1\n- a\n+ b" {
		t.Errorf("NoLineNumbers:\n%s", got)
	}
	for w := 1; w <= 130; w++ {
		for _, nt := range allTestThemes() {
			checkLines(t, "widths/"+nt.name, Diff("orders/list.go", diffGoBefore, diffGoAfter, w, nt.th, DiffOptions{}), w)
		}
	}
	if Diff("f", "a", "b", 0, th, DiffOptions{}) != nil || Diff("f", "a", "b", -1, th, DiffOptions{}) != nil {
		t.Error("a width below 1 gives nil")
	}
}

func TestDiffPropertyRandomText(t *testing.T) {
	g := widgettest.NewRand(71)
	for i := 0; i < 80; i++ {
		before, after := g.Text(g.Intn(40)), g.Text(g.Intn(40))
		if g.Intn(3) == 0 {
			after = before + g.Text(g.Intn(10))
		}
		path := g.Text(g.Intn(6))
		for _, nt := range allTestThemes() {
			for w := 10; w <= 120; w += 11 {
				o := DiffOptions{Context: g.Intn(5) - 1, MaxLines: g.Intn(3) * 7, NoLineNumbers: g.Intn(4) == 0}
				got := Diff(path, before, after, w, nt.th, o)
				checkLines(t, "random diff/"+nt.name, got, w)
				if len(got) > 2*(len(before)+len(after)+len(path))+64 {
					t.Fatalf("%d rows for %d and %d bytes", len(got), len(before), len(after))
				}
				if !reflect.DeepEqual(got, Diff(path, before, after, w, nt.th, o)) {
					t.Fatal("not deterministic")
				}
			}
		}
	}
}
