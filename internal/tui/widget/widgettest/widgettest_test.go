package widgettest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

func TestFlatten(t *testing.T) {
	lines := []cell.Line{
		cell.Text("one  "),
		nil,
		cell.Join(cell.Styled(cell.Style{Attr: cell.Bold}, "two"), cell.Text(" 中 ")),
	}
	if got := Flatten(lines); got != "one\n\ntwo 中" {
		t.Errorf("Flatten = %q", got)
	}
	if Flatten(nil) != "" || Flatten([]cell.Line{nil}) != "" {
		t.Error("no lines, and one empty line, are both the empty string")
	}
	if got := Flatten([]cell.Line{cell.Text("a"), cell.Text("")}); got != "a\n" {
		t.Errorf("a trailing empty row is kept, it is a row: %q", got)
	}
}

func TestFlattenStyled(t *testing.T) {
	bold, dim, it := cell.Style{Attr: cell.Bold}, cell.Style{Attr: cell.Dim}, cell.Style{Attr: cell.Italic}
	all := cell.Style{Attr: cell.Bold | cell.Dim | cell.Italic | cell.Underline | cell.Reverse | cell.Strike}
	cases := []struct {
		name string
		in   cell.Line
		want string
	}{
		{"plain text is written as it is", cell.Text("hello"), "hello"},
		{"bold", cell.Join(cell.Text("a "), cell.Styled(bold, "b"), cell.Text(" c")), "a {b}b{/} c"},
		{"attribute letters in a fixed order", cell.Styled(all, "x"), "{bdiurs}x{/}"},
		{"two styles side by side", cell.Join(cell.Styled(bold, "a"), cell.Styled(dim, "b"), cell.Styled(it, "c")), "{b}a{/}{d}b{/}{i}c{/}"},
		{"runs of one style are merged whatever the spans", cell.Join(cell.Styled(bold, "a"), cell.Styled(bold, "b"), cell.Text(""), cell.Styled(bold, "c")), "{b}abc{/}"},
		{"rgb colours", cell.Styled(cell.Style{FG: cell.RGB(1, 2, 255), BG: cell.Hex("#00ff80")}, "x"), "{fg=#0102ff,bg=#00ff80}x{/}"},
		{"ansi and indexed colours", cell.Styled(cell.Style{FG: cell.ANSI(3), BG: cell.Indexed(200)}, "x"), "{fg=a3,bg=x200}x{/}"},
		{"attributes then colours", cell.Styled(cell.Style{FG: cell.ANSI(1), Attr: cell.Underline | cell.Bold}, "x"), "{bu,fg=a1}x{/}"},
		{"trailing unstyled spaces are trimmed", cell.Join(cell.Styled(bold, "x"), cell.Text("   ")), "{b}x{/}"},
		{"trailing styled spaces are kept: they carry a background", cell.Styled(cell.Style{BG: cell.ANSI(4)}, "x  "), "{bg=a4}x  {/}"},
		{"the zero style is no style", cell.Styled(cell.Style{}, "plain"), "plain"},
	}
	for _, c := range cases {
		if got := FlattenStyled([]cell.Line{c.in}); got != c.want {
			t.Errorf("%s: FlattenStyled = %q, want %q", c.name, got, c.want)
		}
	}
	names := map[cell.Color]string{cell.Hex("#ff0000"): "red"}
	got := FlattenStyledWith([]cell.Line{cell.Styled(cell.Style{FG: cell.Hex("#ff0000"), BG: cell.Hex("#0000ff")}, "x")}, names)
	if got != "{fg=red,bg=#0000ff}x{/}" {
		t.Errorf("named colours: %q", got)
	}
}

func TestMaxWidth(t *testing.T) {
	if MaxWidth(nil) != 0 || MaxWidth([]cell.Line{cell.Text("ab"), cell.Text("中中"), nil}) != 4 {
		t.Error("MaxWidth counts cells")
	}
}

func TestControl(t *testing.T) {
	bad := []string{"a\x1bb", "a\x00", "\x7f", "x\u0085", "x\u009b", "\xff", "a\U0000202Eb", "\U0000200B", "\U0000FEFF", "a\tb", "a\nb", "\U000E0041"}
	for _, s := range bad {
		if where, found := Control([]cell.Line{cell.Text("ok"), cell.Text(s)}); !found || !strings.Contains(where, "line 1") {
			t.Errorf("%q: found=%v %q", s, found, where)
		}
	}
	good := []string{"", "plain", "日本語 😀 e\U00000301", "👨\U0000200D👩", "╭─╮ ❯ ↳ …"}
	for _, s := range good {
		if where, found := Control([]cell.Line{cell.Text(s)}); found {
			t.Errorf("%q is clean but Control says %s", s, where)
		}
	}
}

func TestHostileCorpusIsHostile(t *testing.T) {
	for _, h := range Hostile() {
		if _, found := Control([]cell.Line{cell.Text(h)}); !found {
			t.Errorf("%q has nothing a terminal would act on: it does not belong in Hostile", h)
		}
	}
	for _, u := range Unicode() {
		if !utf8.ValidString(u) {
			t.Errorf("%q is not valid UTF-8", u)
		}
		if _, found := Control([]cell.Line{cell.Text(u)}); found {
			t.Errorf("%q holds a control character: it belongs in Hostile", u)
		}
	}
}

// fakeTB records a failure instead of failing the test that is testing Golden.
type fakeTB struct {
	testing.TB
	msg string
}

type stop struct{}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.msg = format
	panic(stop{})
}
func (f *fakeTB) Fatal(args ...any) { f.msg = "fatal"; panic(stop{}) }

func fails(f *fakeTB, fn func()) (failed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stop); !ok {
				panic(r)
			}
			failed = true
		}
	}()
	fn()
	return false
}

func TestGolden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.txt")
	f := &fakeTB{TB: t}
	if !fails(f, func() { Golden(f, false, path, "hello") }) || !strings.Contains(f.msg, "missing golden file") {
		t.Fatalf("a missing file fails and says how to create it: %q", f.msg)
	}
	Golden(t, true, path, "hello")
	if data, err := os.ReadFile(path); err != nil || string(data) != "hello\n" {
		t.Fatalf("update writes the text and a final newline, creating directories: %q %v", data, err)
	}
	Golden(t, false, path, "hello")
	Golden(t, false, path, "hello\n")
	if err := os.WriteFile(path, []byte("hello\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Golden(t, false, path, "hello") // a checkout that changed the line endings still matches
	if !fails(f, func() { Golden(f, false, path, "hellp") }) || !strings.Contains(f.msg, "differs from") {
		t.Errorf("a difference fails: %q", f.msg)
	}
	Golden(t, true, path, "line1\nline2\n")
	if !fails(f, func() { Golden(f, false, path, "line1\nLINE2\n") }) {
		t.Error("a difference on a later line fails")
	}
	if !fails(f, func() { Golden(f, false, path, "line1\nline2\nline3") }) {
		t.Error("an extra line fails")
	}
}

func TestRandIsDeterministicAndAwkward(t *testing.T) {
	a, b := NewRand(42), NewRand(42)
	for i := 0; i < 20; i++ {
		if a.Text(10) != b.Text(10) || a.Markdown(10) != b.Markdown(10) || a.Intn(1000) != b.Intn(1000) {
			t.Fatal("the same seed must give the same text")
		}
	}
	if NewRand(1).Text(30) == NewRand(2).Text(30) {
		t.Error("different seeds give different text")
	}
	if NewRand(1).Intn(0) != 0 || NewRand(1).Intn(-5) != 0 {
		t.Error("Intn of a non-positive number is 0")
	}
	g := NewRand(3)
	sawControl := false
	for i := 0; i < 50; i++ {
		if _, found := Control([]cell.Line{cell.Text(g.Text(20))}); found {
			sawControl = true
		}
	}
	if !sawControl {
		t.Error("Text is meant to include hostile fragments")
	}
	if g.Pick("only") != "only" {
		t.Error("Pick")
	}
}

func TestChunksConcatenateToTheOriginal(t *testing.T) {
	g := NewRand(5)
	for i := 0; i < 500; i++ {
		s := g.Markdown(g.Intn(30))
		for _, max := range []int{-1, 0, 1, 2, 7, 100} {
			chunks := g.Chunks(s, max)
			if strings.Join(chunks, "") != s {
				t.Fatalf("Chunks(%q, %d) = %q", s, max, chunks)
			}
			if len(chunks) > max && !(max < 1 && len(chunks) == 1) {
				t.Fatalf("Chunks(%q, %d) gave %d pieces", s, max, len(chunks))
			}
		}
	}
	if got := g.Chunks("", 4); strings.Join(got, "") != "" || len(got) == 0 {
		t.Errorf("an empty string gives at least one (empty) piece: %q", got)
	}
	// chunks may split a rune: that is the point
	split := false
	for i := 0; i < 200 && !split; i++ {
		for _, c := range g.Chunks("日本語日本語日本語", 6) {
			if !utf8.ValidString(c) {
				split = true
			}
		}
	}
	if !split {
		t.Error("Chunks should sometimes cut inside a rune")
	}
}
