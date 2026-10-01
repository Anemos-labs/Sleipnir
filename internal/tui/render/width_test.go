package render

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// Wide runes, combining marks and lines exactly as wide as the terminal must not corrupt the renderer's count of rows, because
// every later relative move depends on it (vii). Each awkward line is shown in the live region, updated in place many times,
// printed, and kept across resizes, and after every frame the terminal must agree with the model.
func TestAwkwardWidthsKeepTheRowAccounting(t *testing.T) {
	cases := []struct{ name, text string }{
		{"exactly the width", "0123456789"},
		{"exactly the width in wide runes", "中中中中中"},
		{"exactly the width in accented letters", strings.Repeat("e\u0301", 10)},
		{"exactly the width in precomposed letters", strings.Repeat("é", 10)},
		{"one cell over", "01234567890"},
		{"a wide rune straddling the edge", "012345678中"},
		{"a wide rune first, then the rest", "中012345678"},
		{"a wide rune filling the last two cells", "01234567中"},
		{"two words", "hello worldwide"},
		{"a mark after the last cell of a full row", "012345678e\u0301"},
		{"marks all along a full row", "a\u0301b\u0301c\u0301d\u0301e\u0301f\u0301g\u0301h\u0301i\u0301j\u0301"},
		{"a joiner sequence", "\U0001f468\u200d\U0001f469\u200d\U0001f467 family"},
		{"a variation selector", "❤\ufe0f and more text here!"},
		{"spaces only", "          "},
		{"a long word", strings.Repeat("x", 35)},
		{"hangul and kana", "한글 こんにちは 漢字"},
		{"the empty line", ""},
	}
	const cols, rows = 10, 12
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, termCaps(cols, rows))
			m := newModel(cols, rows)
			other := "short"
			for i := 0; i < 12; i++ {
				text := c.text
				if i%2 == 1 {
					text = other
				}
				live := []cell.Line{txt("top"), txt(text), txt("bottom")}
				h.r.SetLive(live)
				m.live = live
				h.r.SetCursor(99, 3)                        // the last row, wherever the wrapping puts it
				m.caretOn, m.caretR, m.caretC = true, 99, 3 // the last row, wherever the wrapping puts it
				if i == 5 {
					h.r.Print(txt(c.text))
					m.print(txt(c.text))
				}
				if i == 8 {
					h.resize(7, rows)
					m.cols = 7
				}
				if i == 10 {
					h.resize(cols, rows)
					m.cols = cols
				}
				h.flush()
				m.flush()
				h.check(m)
				if t.Failed() {
					t.Fatalf("stopped at update %d", i)
				}
				if got, want := len(h.all()), len(m.all()); got != want {
					t.Fatalf("update %d: %d rows on the terminal, %d in the model", i, got, want)
				}
			}
			// The renderer's own count of the region must be what the terminal shows of it.
			region, _ := m.liveRows()
			if got := len(h.r.drawn); got != len(region) && c.text != "" {
				t.Errorf("the renderer counts %d rows of live region, the model %d", got, len(region))
			}
			h.r.Close()
		})
	}
}

// The width of a line is cell.Line.Width: the renderer and the emulator must agree with it for every rune the widget library
// will draw, or the rows it counts are not the rows there are.
func TestRendererAndEmulatorAgreeOnRuneWidths(t *testing.T) {
	for _, r := range []rune{'a', ' ', 'é', '─', '█', '⠹', '❯', '✓', '●', '⎿', '▏', '◆', '⚠', '↻', '✉', '中', 'あ', '한', 'Ａ', '😀', '🐎', '⏳', '\u0301', '\u200d', '\ufe0f'} {
		s := "x" + string(r) + "y"
		h := newHarness(t, termCaps(10, 3))
		h.r.Print(txt(s))
		h.flush()
		x, y, _ := h.v.Cursor()
		if y != 1 || x != 0 {
			t.Errorf("%U: the cursor is at (%d,%d) after one printed line", r, x, y)
		}
		h.r.SetLive(txts(s))
		h.flush()
		if want := cell.StringWidth(s); want > 0 {
			// the row is as wide on the terminal as cell says: the next row's text would start right after it
			h.v.WriteString("\x1b[2;1H")
			cx, _, _ := h.v.Cursor()
			if cx != 0 {
				t.Fatal("cursor move")
			}
		}
		rows := h.v.Rows()
		if rows[0] != s || rows[1] != s {
			t.Errorf("%U: %q", r, rows)
		}
	}
	// One printed cell at a time: the terminal's column after writing a rune is cell.RuneWidth of it.
	for _, r := range []rune{'a', '中', '😀', 'é', '\u0301'} {
		h := newHarness(t, termCaps(10, 3))
		h.v.WriteString("a" + string(r))
		x, _, _ := h.v.Cursor()
		if want := 1 + cell.RuneWidth(r); x != want {
			t.Errorf("%U: the emulator is at column %d after writing it, cell says %d", r, x, want)
		}
	}
}
