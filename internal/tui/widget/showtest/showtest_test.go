package showtest

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

func TestFlatten(t *testing.T) {
	red := cell.Style{}.Fg(cell.ANSI(1))
	ls := []cell.Line{cell.Join(cell.Styled(red, "ab"), cell.Text("c")), nil, cell.Text("  d  ")}
	if got := Flatten(ls); got != "abc\n\n  d  " {
		t.Errorf("Flatten = %q: styles dropped, blank lines kept, spaces kept, no trailing newline", got)
	}
	if Flatten(nil) != "" {
		t.Error("no lines is the empty string")
	}
	if got := MaxWidth([]cell.Line{cell.Text("ab"), cell.Text("中文z"), nil}); got != 5 {
		t.Errorf("MaxWidth counts cells, not runes: %d", got)
	}
	if MaxWidth(nil) != 0 {
		t.Error("no lines is width 0")
	}
}

func TestFlattenStyled(t *testing.T) {
	g0 := cell.Hex("#4c6fd0")
	names := map[cell.Color]string{g0: "G0"}
	bold := cell.Style{Attr: cell.Bold}
	ls := []cell.Line{
		{
			{Text: "plain"},
			{Text: "██", Style: cell.Style{FG: g0}},
			{Text: "░", Style: cell.Style{FG: g0, Attr: cell.Dim}},
			{Text: "x", Style: cell.Style{FG: g0, Attr: cell.Dim}}, // merged with the run before it
			{Text: "!", Style: bold},
			{Text: "", Style: cell.Style{FG: cell.ANSI(2)}}, // empty: nothing
			{Text: "▀", Style: cell.Style{FG: cell.RGB(1, 2, 255), BG: cell.Indexed(200), Attr: cell.Bold | cell.Reverse}},
		},
	}
	want := "plain{G0|██}{G0+d|░x}{-+b|!}{#0102ff/x200+br|▀}"
	if got := FlattenStyled(ls, names); got != want {
		t.Errorf("FlattenStyled =\n%s\nwant\n%s", got, want)
	}
	// without names a colour is written out; the 16 colours are aN
	if got := FlattenStyled([]cell.Line{cell.Join(cell.Styled(cell.Style{FG: g0}, "q"), cell.Styled(cell.Style{FG: cell.ANSI(9)}, "r"))}, nil); got != "{#4c6fd0|q}{a9|r}" {
		t.Errorf("unnamed colours: %q", got)
	}
	if got := StyleName(cell.Style{Attr: cell.Dim | cell.Italic | cell.Underline | cell.Strike}, nil); got != "-+diu"+"s" {
		t.Errorf("attribute letters: %q", got)
	}
	// plain spaces at the end of a row are left out; styled ones (a background) and the ones in the middle are kept
	trail := []cell.Line{
		cell.Join(cell.Styled(bold, "a"), cell.Text("  b  ")),
		cell.Join(cell.Text("c"), cell.Styled(cell.Style{BG: g0}, "  ")),
		cell.Text("     "),
	}
	if got, want := FlattenStyled(trail, names), "{-+b|a}  b\nc{-/G0|  }\n"; got != want {
		t.Errorf("trailing spaces: %q, want %q", got, want)
	}
}

func TestPictureAndDoc(t *testing.T) {
	ls := []cell.Line{cell.Text("ab"), cell.Text("中"), nil, cell.Text("toolong")}
	want := "ab   |\n中   |\n     |\ntoolong|"
	if got := Picture(ls, 5); got != want {
		t.Errorf("Picture:\n%s\nwant\n%s", got, want)
	}
	var d Doc
	d.Note("what %d", 3)
	d.Add("a title", ls[:2], 4)
	d.Add("nothing", nil, 4)
	d.AddText("styled", "{x|y}")
	d.AddText("blank rows at the end", "{x|y}\n\n\n")
	got := d.String()
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("a document does not end in a blank line: %q", got)
	}
	for _, frag := range []string{"# what 3\n", "\n== a title (width 4, 2 lines)\nab  |\n中  |\n", "\n== nothing (width 4, 0 lines)\n", "\n== styled\n{x|y}\n"} {
		if !strings.Contains(got, frag) {
			t.Errorf("the document lacks %q:\n%s", frag, got)
		}
	}
}

func TestFirstDiff(t *testing.T) {
	if got := FirstDiff("a\nb\nc", "a\nB\nC"); !strings.Contains(got, "line 2") || !strings.Contains(got, "want: b") || !strings.Contains(got, "got:  B") || !strings.Contains(got, "2 line(s)") {
		t.Errorf("%s", got)
	}
	if got := FirstDiff("a\nb", "a\nb\nc"); !strings.Contains(got, "line 3") || !strings.Contains(got, "(end of file)") {
		t.Errorf("a longer output: %s", got)
	}
	if got := FirstDiff("same", "same"); !strings.Contains(got, "the ends differ") {
		t.Errorf("no difference: %s", got)
	}
}

// fakeT records what a golden check would have told the test runner, so that a failing comparison can be tested.
type fakeT struct {
	testing.TB
	failed string
}

func (f *fakeT) Helper()      {}
func (f *fakeT) Name() string { return "TestFake" }
func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed = format
	panic(f) // stops the goroutine the way a real Fatalf does
}
func (f *fakeT) Fatal(args ...any) { f.failed = "fatal"; panic(f) }

func check(t *testing.T, f func(tb testing.TB)) (failed string) {
	t.Helper()
	ft := &fakeT{TB: t}
	defer func() {
		if r := recover(); r != nil {
			if r != any(ft) {
				panic(r)
			}
			failed = ft.failed
		}
	}()
	f(ft)
	return ""
}

func TestGolden(t *testing.T) {
	RegisterUpdateFlag()
	RegisterUpdateFlag() // twice is fine: the second finds the flag
	if f := flag.Lookup("update"); f == nil {
		t.Fatal("the -update flag is registered")
	}
	if Updating() {
		t.Fatal("-update was not given")
	}
	path := filepath.Join(t.TempDir(), "sub", "g.txt")

	if failed := check(t, func(tb testing.TB) { Golden(tb, path, "hello") }); !strings.Contains(failed, "missing golden file") {
		t.Errorf("a missing file fails: %q", failed)
	}

	// -update writes it (and the directory), ending in a newline
	if err := flag.Set("update", "true"); err != nil {
		t.Fatal(err)
	}
	if !Updating() {
		t.Error("Updating after -update")
	}
	Golden(t, path, "hello\nworld")
	if err := flag.Set("update", "false"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "hello\nworld\n" {
		t.Fatalf("the file: %q %v", b, err)
	}

	// without it a match passes and a difference fails with the first differing line
	Golden(t, path, "hello\nworld")
	Golden(t, path, "hello\nworld\n")
	if failed := check(t, func(tb testing.TB) { Golden(tb, path, "hello\nWORLD") }); !strings.Contains(failed, "output differs") {
		t.Errorf("a difference fails: %q", failed)
	}
}
