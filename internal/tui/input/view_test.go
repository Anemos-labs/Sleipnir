package input

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// glyphAt is the glyph drawn at cell col of a line ("" when the cell is empty), counted the way cell counts: a zero-width
// rune belongs to the glyph before it.
func glyphAt(l cell.Line, col int) string {
	var gs []string
	var ws []int
	for _, sp := range l {
		for _, r := range sp.Text {
			if w := cell.RuneWidth(r); w == 0 {
				if len(gs) > 0 {
					gs[len(gs)-1] += string(r)
				}
			} else {
				gs, ws = append(gs, string(r)), append(ws, w)
			}
		}
	}
	x := 0
	for i, g := range gs {
		if col >= x && col < x+ws[i] {
			return g
		}
		x += ws[i]
	}
	return ""
}

// glyphAtIndex is the glyph the buffer rune at i starts: the rune and the zero-width runes after it.
func glyphAtIndex(buf []rune, i int) string {
	s := string(buf[i])
	for j := i + 1; j < len(buf) && isMark(buf[j]); j++ {
		s += string(buf[j])
	}
	return s
}

func TestViewBasics(t *testing.T) {
	e := NewEditor(Options{Placeholder: "type a message"})
	v := e.View(30)
	if got := v.Plain(); !reflect.DeepEqual(got, []string{"❯ type a message"}) || v.CursorRow != 0 || v.CursorCol != 2 || v.InputRows != 1 {
		t.Errorf("placeholder: %q cursor %d,%d", got, v.CursorRow, v.CursorCol)
	}
	if v.Lines[0][0].Style != e.th.Prompt || v.Lines[0][1].Style != e.th.Placeholder {
		t.Errorf("placeholder styles: %+v", v.Lines[0])
	}
	// no placeholder configured: just the prompt
	if got := NewEditor(Options{}).View(30).Plain(); !reflect.DeepEqual(got, []string{"❯ "}) {
		t.Errorf("empty: %q", got)
	}
	r := &rig{t: t, ed: e}
	r.send("hello")
	v = e.View(30)
	if got := v.Plain(); !reflect.DeepEqual(got, []string{"❯ hello"}) || v.CursorCol != 7 {
		t.Errorf("typed: %q col %d", got, v.CursorCol)
	}
	if v.Lines[0][0].Style != e.th.Prompt || v.Lines[0][0].Text != "❯ " || v.Lines[0][1].Style != e.th.Text {
		t.Errorf("styles: %+v", v.Lines[0])
	}
	r.send(kAltEnter, "world", kLeft, kLeft)
	v = e.View(30)
	if got := v.Plain(); !reflect.DeepEqual(got, []string{"❯ hello", "  world"}) || v.CursorRow != 1 || v.CursorCol != 2+3 {
		t.Errorf("multi-line: %q cursor %d,%d", got, v.CursorRow, v.CursorCol)
	}
	if !strings.HasPrefix(v.Lines[1][0].Text, "  ") || v.Lines[1][0].Style != (cell.Style{}) {
		t.Errorf("the continuation prefix is unstyled: %+v", v.Lines[1])
	}
	r.send(kUp, kHome)
	if v = e.View(30); v.CursorRow != 0 || v.CursorCol != 2 {
		t.Errorf("home: %d,%d", v.CursorRow, v.CursorCol)
	}
	// a custom prompt
	e2 := NewEditor(Options{Prompt: ">>> "})
	(&rig{t: t, ed: e2}).send("a", kAltEnter, "b")
	if got := e2.View(30).Plain(); !reflect.DeepEqual(got, []string{">>> a", "    b"}) {
		t.Errorf("custom prompt: %q", got)
	}
	// an all-zero theme draws plain text
	e3 := NewEditor(Options{Theme: &Theme{}})
	(&rig{t: t, ed: e3}).send("x")
	for _, sp := range e3.View(30).Lines[0] {
		if sp.Style != (cell.Style{}) {
			t.Errorf("zero theme: %+v", sp)
		}
	}
	// View does not change the editor
	before := e.Text()
	e.View(5)
	e.View(80)
	if e.Text() != before {
		t.Error("View must not change the editor")
	}
}

func TestViewWrapping(t *testing.T) {
	cases := []struct {
		name string
		text string
		cur  int // rune index; -1 = end
		w    int // total width (prompt 2 cells + text)
		want []string
		row  int
		col  int // in cells, prompt included
	}{
		{"fits", "hello world", -1, 30, []string{"❯ hello world"}, 0, 13},
		{"breaks at a space", "hello world foo bar", -1, 12, []string{"❯ hello", "  world foo", "  bar"}, 2, 5},
		{"cursor before the first letter of a wrapped word", "hello world foo bar", 6, 12, []string{"❯ hello", "  world foo", "  bar"}, 1, 2},
		{"cursor on the space that was dropped at the break", "hello world foo bar", 5, 12, []string{"❯ hello", "  world foo", "  bar"}, 0, 7},
		{"cursor after the space that was dropped", "hello world foo bar", 6, 12, []string{"❯ hello", "  world foo", "  bar"}, 1, 2},
		{"a long word is broken", "abcdefghijklmnop", -1, 12, []string{"❯ abcdefghij", "  klmnop"}, 1, 8},
		{"cursor at the break inside a long word", "abcdefghijklmnop", 10, 12, []string{"❯ abcdefghij", "  klmnop"}, 1, 2},
		{"a full last row gets a row for the cursor", "abcdefghij", -1, 12, []string{"❯ abcdefghij", "  "}, 1, 2},
		{"cursor on the last cell of a full row", "abcdefghij", 9, 12, []string{"❯ abcdefghij"}, 0, 11},
		{"leading spaces stay", "   x", -1, 12, []string{"❯    x"}, 0, 6},
		{"leading spaces, cursor among them", "   x", 2, 12, []string{"❯    x"}, 0, 4},
		{"only spaces", "   ", -1, 12, []string{"❯    "}, 0, 5},
		{"trailing spaces are not drawn, the cursor still moves past them", "ab  ", -1, 12, []string{"❯ ab"}, 0, 6},
		{"trailing spaces with the cursor elsewhere", "ab  ", 0, 12, []string{"❯ ab"}, 0, 2},
		{"cursor on a trailing space", "ab  ", 2, 12, []string{"❯ ab"}, 0, 4},
		{"cursor between trailing spaces", "ab  ", 3, 12, []string{"❯ ab"}, 0, 5},
		{"cursor after spaces at the end of a full row gets a row", "abcdefghij  ", -1, 12, []string{"❯ abcdefghij", "  "}, 1, 2},
		{"cursor on the trailing space of a full row", "abcdefghij ", 10, 12, []string{"❯ abcdefghij", "  "}, 1, 2},
		{"trailing spaces do not reflow the word", "ab defgh ", -1, 12, []string{"❯ ab defgh"}, 0, 11},
		{"a word that exactly fills the row stays, the cursor takes the next row", "ab defgh", -1, 10, []string{"❯ ab defgh", "  "}, 1, 2},
		{"wide runes wrap whole", "abcdefghi\U00004e2d", -1, 12, []string{"❯ abcdefghi", "  \U00004e2d"}, 1, 4},
		{"wide rune, cursor on it", "abcdefghi\U00004e2d", 9, 12, []string{"❯ abcdefghi", "  \U00004e2d"}, 1, 2},
		{"a combining mark is one cell", "e\U00000301x", -1, 12, []string{"❯ e\U00000301x"}, 0, 4},
		{"tab is spaces to the next stop", "a\tb", -1, 12, []string{"❯ a   b"}, 0, 7},
		{"tab, cursor on it", "a\tb", 1, 12, []string{"❯ a   b"}, 0, 3},
		{"empty lines", "a\n\nb", -1, 12, []string{"❯ a", "  ", "  b"}, 2, 3},
		{"cursor on an empty line", "a\n\nb", 2, 12, []string{"❯ a", "  ", "  b"}, 1, 2},
		{"cursor at the end of a line before a newline", "ab\ncd", 2, 12, []string{"❯ ab", "  cd"}, 0, 4},
		{"trailing newline is an empty last line", "ab\n", -1, 12, []string{"❯ ab", "  "}, 1, 2},
		{"each line wraps on its own", "aaa bbb ccc\nddd eee fff", -1, 8, []string{"❯ aaa", "  bbb", "  ccc", "  ddd", "  eee", "  fff"}, 5, 5},
		{"spaces kept between words", "a   b", -1, 12, []string{"❯ a   b"}, 0, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := NewEditor(Options{})
			e.SetText(c.text)
			if c.cur >= 0 {
				e.cur = c.cur
			}
			v := e.View(c.w)
			if got := v.Plain(); !reflect.DeepEqual(got, c.want) {
				t.Errorf("rows\n got  %q\n want %q", got, c.want)
			}
			if v.CursorRow != c.row || v.CursorCol != c.col {
				t.Errorf("cursor at row %d col %d, want row %d col %d", v.CursorRow, v.CursorCol, c.row, c.col)
			}
		})
	}
}

func TestViewTinyWidthsDoNotPanic(t *testing.T) {
	e := NewEditor(Options{})
	(&rig{t: t, ed: e}).send(paste(strings.Repeat("word \U00004e2d\n", 20)), "abc  d", kAltEnter, "\t\tx")
	for w := -3; w < 12; w++ {
		v := e.View(w)
		if len(v.Lines) == 0 || v.CursorRow >= len(v.Lines) || v.CursorRow < 0 || v.CursorCol < 0 {
			t.Fatalf("width %d: %d lines, cursor %d,%d", w, len(v.Lines), v.CursorRow, v.CursorCol)
		}
	}
	if got := e.View(0).Plain(); len(got) == 0 {
		t.Error("width 0")
	}
	e.View(10000)
}

func randomBuffer(rng *rand.Rand) string {
	parts := []string{"a", "b", "c", "d", "e", "f", "g", "hello", "wor", "x", " ", " ", " ", "  ", "\n", "\t", "\U00004e2d", "\U00006587",
		"\U0001f600", "\U0001f44d\U0001f3fd", "e\U00000301", "\U0000200d", "\U0000fe0f", "\U00000301", "-", ".", "abcdefghijklmnopqrstuvwxyz"}
	var sb strings.Builder
	for n := rng.Intn(40); n > 0; n-- {
		sb.WriteString(parts[rng.Intn(len(parts))])
	}
	return sb.String()
}

// removeRow returns rows without row k.
func removeRow(rows []string, k int) []string {
	return append(append([]string(nil), rows[:k]...), rows[k+1:]...)
}

// The cursor the view reports is where the cursor rune is drawn, for random buffers at every width from 8 to 40 and every
// cursor position: the heart of "soft-wrapped so the cursor maps exactly". And the text does not reflow when the cursor
// moves: the rows are the same for every cursor position, but for one extra row for the cursor to stand on.
func TestViewCursorMapsToTheCursorRune(t *testing.T) {
	rng := rand.New(rand.NewSource(20240607))
	type job struct {
		text   string
		widths []int
	}
	var every []int
	for w := 8; w <= 40; w++ {
		every = append(every, w)
	}
	var jobs []job
	full, sampled := 24, 60 // buffers checked at every width, and at five of them
	if testing.Short() {
		full, sampled = 6, 10
	}
	for b := 0; b < full; b++ {
		jobs = append(jobs, job{randomBuffer(rng), every})
	}
	for b := 0; b < sampled; b++ {
		jobs = append(jobs, job{randomBuffer(rng), []int{8, 11, 17, 26, 40}})
	}
	for _, jb := range jobs {
		text := jb.text
		e := NewEditor(Options{})
		e.SetText(text)
		buf := e.buf
		for _, w := range jb.widths {
			// the glyph model and Wrap must agree for every line, so that the fallback never has to run
			for ls := 0; ; {
				le := lineEnd(buf, ls)
				if _, ok := alignLine(e.lineGlyphs(ls, le), max(w-2, 2)); !ok {
					t.Fatalf("buffer %q width %d: Wrap's rows could not be matched to the glyphs of line %q", text, w, string(buf[ls:le]))
				}
				if le >= len(buf) {
					break
				}
				ls = le + 1
			}

			e.cur = 0
			base := e.View(w).Plain()
			prevRow, prevCol := -1, -1
			for i := 0; i <= len(buf); i = nextBoundary(buf, i) {
				e.cur = i
				v := e.View(w)
				ctx := func() string {
					return "buffer " + quote(text) + " width " + itoa(w) + " cursor " + itoa(i)
				}
				if v.CursorRow < 0 || v.CursorRow >= len(v.Lines) || v.CursorCol < 0 || v.CursorCol >= w {
					t.Fatalf("%s: cursor %d,%d outside %d lines of width %d", ctx(), v.CursorRow, v.CursorCol, len(v.Lines), w)
				}
				for _, l := range v.Lines {
					if l.Width() > w {
						t.Fatalf("%s: a row is %d cells wide: %q", ctx(), l.Width(), l.Plain())
					}
				}

				// no reflow: the same rows as with the cursor at the start, plus at most one row for the cursor
				rows := v.Plain()
				switch {
				case len(rows) == len(base):
					if !reflect.DeepEqual(rows, base) {
						t.Fatalf("%s: the text reflowed when the cursor moved\n got  %q\n base %q", ctx(), rows, base)
					}
				case len(rows) == len(base)+1:
					ok := false
					for k := range rows {
						if strings.TrimRight(rows[k], " ") == "" && len(rows[k]) == 2 && reflect.DeepEqual(removeRow(rows, k), base) {
							ok = true
						}
					}
					if !ok {
						t.Fatalf("%s: one extra row is allowed, and only an empty one\n got  %q\n base %q", ctx(), rows, base)
					}
				default:
					t.Fatalf("%s: %d rows with the cursor here, %d with it at the start", ctx(), len(rows), len(base))
				}

				row := v.Lines[v.CursorRow]
				got := glyphAt(row, v.CursorCol)
				switch {
				case i == len(buf) || buf[i] == '\n':
					if v.CursorCol < row.Width() {
						t.Fatalf("%s: the cursor at the end of a line must be past the text, but the cell holds %q (%q)", ctx(), got, row.Plain())
					}
				case buf[i] == ' ' || buf[i] == '\t':
					// A space is drawn where it is, or was dropped at a break: the cursor is then where it would have been
					// drawn, past the end of its row, or at the start of the next one when that one is full.
					if got != "" && !strings.HasPrefix(got, " ") && v.CursorCol != 2 {
						t.Fatalf("%s: the cursor before a space is on %q, not past the end of the row or at the start of the next\n%q", ctx(), got, v.Plain())
					}
				case isMark(buf[i]):
					// a zero-width rune with nothing to attach to draws nothing; the cursor is where the next glyph is
				default:
					if want := glyphAtIndex(buf, i); got != want {
						t.Fatalf("%s: the cursor is on %q but the rune under it is %q\n%q", ctx(), got, want, v.Plain())
					}
				}
				if v.CursorRow < prevRow || (v.CursorRow == prevRow && v.CursorCol < prevCol) {
					t.Fatalf("%s: the cursor moved backwards: %d,%d after %d,%d", ctx(), v.CursorRow, v.CursorCol, prevRow, prevCol)
				}
				prevRow, prevCol = v.CursorRow, v.CursorCol
				if i == len(buf) {
					break
				}
			}

			// nothing is lost or duplicated: the non-space text comes back in order
			var drawn strings.Builder
			for _, l := range base {
				drawn.WriteString(strings.Join(strings.Fields(strings.ReplaceAll(string([]rune(l)[2:]), "\U000000a0", " ")), ""))
			}
			if want := strings.Join(strings.Fields(strings.ReplaceAll(withoutOrphanMarks(buf), "\U000000a0", " ")), ""); drawn.String() != want {
				t.Fatalf("buffer %q width %d: drawn text %q lost or changed something", text, w, drawn.String())
			}
		}
	}
}

// withoutOrphanMarks is the buffer as text, minus the zero-width runes that have nothing to attach to (cell draws those as
// nothing): the ones at the start of a line, or after a tab or a chip, however many in a row.
func withoutOrphanMarks(buf []rune) string {
	var sb strings.Builder
	orphan := true
	for _, r := range buf {
		switch {
		case isSpecial(r):
			orphan = true
			sb.WriteRune(r)
		case isMark(r):
			if !orphan {
				sb.WriteRune(r)
			}
		default:
			orphan = false
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func quote(s string) string { return strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(s) }

func TestViewHasNoControlCharacters(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	e := NewEditor(Options{Completer: SlashCommands(testCommands)})
	var d Decoder
	for n := 0; n < 300; n++ {
		data := []byte(randomStream(rng, 30))
		for _, k := range append(d.Feed(data), d.Flush()...) {
			e.Handle(k)
			for _, l := range e.View(20 + n%30).Lines {
				for _, r := range l.Plain() {
					if r < 0x20 || (r >= 0x7f && r < 0xa0) || r == 0xfeff || (r >= 0x202a && r <= 0x202e) {
						t.Fatalf("control or invisible rune %U in the view: %q", r, l.Plain())
					}
				}
			}
		}
	}
}

func TestNaiveWrapFallbackIsConsistent(t *testing.T) {
	e := NewEditor(Options{})
	e.SetText("ab \U00004e2d\U00004e2d\U00004e2d c\U00000301d")
	gs := e.lineGlyphs(0, len(e.buf))
	for w := 2; w < 12; w++ {
		ll := naiveWrap(gs, w)
		rows, pos := ll.rows, ll.pos
		if len(pos) != len(gs) {
			t.Fatal("a position for every glyph")
		}
		if last := rows[len(rows)-1]; ll.end.row < len(rows)-1 || ll.end.col > w || (ll.end.row == len(rows)-1 && ll.end.col != pos[last[1]-1].col+gs[last[1]-1].w) {
			t.Errorf("w=%d: the cell after the line is %+v", w, ll.end)
		}
		covered := 0
		for ri, r := range rows {
			width := 0
			for g := r[0]; g < r[1]; g++ {
				if pos[g].row != ri || pos[g].col != width {
					t.Fatalf("w=%d glyph %d at %+v, expected row %d col %d", w, g, pos[g], ri, width)
				}
				width += gs[g].w
				covered++
			}
			if width > w && r[1]-r[0] > 1 {
				t.Fatalf("w=%d: row %d is %d cells", w, ri, width)
			}
		}
		if covered != len(gs) {
			t.Fatalf("w=%d: %d of %d glyphs on rows", w, covered, len(gs))
		}
	}
}

func TestLayoutIndexMap(t *testing.T) {
	e := NewEditor(Options{})
	e.SetText("ab\n\U00004e2d\tx")
	l := e.layout(10)
	for i := 0; i <= len(e.buf); i = nextBoundary(e.buf, i) {
		e.cur = i
		l = e.layout(10)
		if i < len(e.buf) && e.buf[i] != '\n' && l.at[i] < 0 {
			t.Errorf("no glyph starts at index %d", i)
		}
		if i == len(e.buf) {
			break
		}
	}
	if !utf8.ValidString(string(e.buf)) {
		t.Fatal("buffer")
	}
}
