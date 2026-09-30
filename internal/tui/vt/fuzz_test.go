package vt

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// The corpus in testdata/corpus is real terminal output, captured from programs running in a pseudo-terminal of 80x24 with
// TERM=xterm-256color: ls with colours and with hyperlinks, git diff and log, vim (starting, quitting, editing a file of
// UTF-8 text), less, tput, and Python printing colours, UTF-8, operating system commands and cursor movement. Nothing in it
// comes from the machine it was captured on.

func corpus(tb testing.TB) (names []string, files map[string][]byte) {
	tb.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.vt"))
	if err != nil || len(paths) == 0 {
		tb.Fatalf("no corpus: %v", err)
	}
	sort.Strings(paths)
	files = map[string][]byte{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			tb.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(p), ".vt")
		names = append(names, name)
		files[name] = b
	}
	return names, files
}

func TestRealTerminalOutputNeverBreaksTheInvariants(t *testing.T) {
	names, files := corpus(t)
	sizes := [][2]int{{80, 24}, {40, 10}, {120, 50}, {7, 3}, {1, 1}, {200, 5}}
	for _, name := range names {
		for _, sz := range sizes {
			v := New(sz[0], sz[1])
			data := files[name]
			// Fed whole, byte by byte, and in uneven chunks: the state machine must not care where the reads end.
			v.Write(data)
			checkInvariants(t, v)
			whole := v.String()
			b := New(sz[0], sz[1])
			for i := range data {
				b.Write(data[i : i+1])
			}
			checkInvariants(t, b)
			if b.String() != whole || !reflect.DeepEqual(b.Scrollback(), v.Scrollback()) {
				t.Errorf("%s at %v: feeding byte by byte gives a different screen", name, sz)
			}
			v.Resize(sz[0]/2+1, sz[1]+3)
			checkInvariants(t, v)
			v.Resize(sz[0]*2, sz[1]/2+1)
			checkInvariants(t, v)
		}
	}
}

func TestRealTerminalOutputOnAnEighty24Screen(t *testing.T) {
	_, files := corpus(t)
	play := func(name string) *Term {
		v := New(80, 24)
		v.Write(files[name])
		checkInvariants(t, v)
		return v
	}

	ls := play("ls-color")
	rows := ls.All()
	joined := strings.Join(rows, "\n")
	for _, want := range []string{"archive.tar.gz", "main.go", "link -> main.go", "subdir", "script.sh"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ls: %q is missing from\n%s", want, joined)
		}
	}
	var sawBlueBold bool
	for y := 0; y < 24; y++ {
		for x := 0; x < 80; x++ {
			if r, st := ls.Cell(x, y); r == "s" && st.FG == cell.ANSI(4) && st.Has(cell.Bold) {
				sawBlueBold = true // the directory names are bold blue (01;34)
			}
		}
	}
	if !sawBlueBold {
		t.Error("ls: no bold blue cell")
	}

	links := play("ls-hyperlink")
	if len(links.Rejected) == 0 {
		t.Error("ls --hyperlink: the OSC 8 links were not seen")
	}
	for _, r := range links.Rejected {
		if !strings.HasPrefix(r, "8;") {
			t.Errorf("an OSC 8 record that is not one: %q", r)
		}
	}
	if got := strings.Fields(links.Rows()[0]); !reflect.DeepEqual(got, []string{"archive.tar.gz", "image.png", "link", "main.go", "readme.txt", "script.sh", "subdir"}) {
		t.Errorf("ls --hyperlink: the names were not drawn as they are: %q", got)
	}

	osc := play("python-osc")
	if got := strings.Fields(osc.Rows()[0]); !reflect.DeepEqual(got, []string{"text", "link", "prompt", "done"}) {
		t.Errorf("osc: row %q", osc.Rows()[0])
	}
	if want := []string{"8;;https://example.org/", "8;;", "52;c;aGVsbG8="}; !reflect.DeepEqual(osc.Rejected, want) {
		t.Errorf("osc: rejected %q, want %q", osc.Rejected, want)
	}

	uni := play("python-unicode")
	// The text has CJK, hiragana, hangul, an emoji, box drawing, and two letters with combining marks (e + U+0301, a + U+030A),
	// which must come out as they went in: a wrong width would shift the marks or cut a wide rune.
	if got, want := uni.Rows()[0], "café naïve 中文字 こんにちは 한글 🐎 ─│┌┐ e\u0301 a\u030a"; got != want {
		t.Errorf("unicode: row %q, want %q", got, want)
	}
	// The file ends with a newline and print adds one: the cursor is at the start of the third row.
	if x, y, _ := uni.Cursor(); y != 2 || x != 0 {
		t.Errorf("unicode: cursor (%d,%d)", x, y)
	}

	edit := play("vim-edit")
	if edit.AltScreen() {
		t.Error("vim left the terminal on the alternate screen")
	}
	if _, _, vis := edit.Cursor(); !vis {
		t.Error("vim left the cursor hidden")
	}
	if edit.BracketedPaste() {
		t.Error("vim left bracketed paste on")
	}
	if len(edit.Rejected) != 0 {
		t.Errorf("vim: %q", edit.Rejected)
	}

	tput := play("tput-mix")
	if tput.AltScreen() {
		t.Error("tput rmcup must leave the alternate screen")
	}

	dance := play("python-cursor-dance")
	if dance.AltScreen() {
		t.Error("the cursor dance ends on the main screen")
	}
	// The script asks for a cursor position report after "\x1b[5;5H", "part", up 3, down 2, right 10, left 4, next line twice
	// forward and previous line once: the cursor is at column 1 of row 5.
	if got := string(dance.Reply()); got != "\x1b[5;1R" {
		t.Errorf("the answer to the cursor position report: %q", got)
	}

	colors := play("python-colors")
	var sawIndexed, sawRGB, sawUnderline bool
	for y := 0; y < 24; y++ {
		for x := 0; x < 80; x++ {
			_, st := colors.Cell(x, y)
			sawIndexed = sawIndexed || st.BG.Kind == cell.KindIndexed
			sawRGB = sawRGB || st.FG.Kind == cell.KindRGB
			sawUnderline = sawUnderline || st.Has(cell.Underline)
		}
	}
	if !sawIndexed || !sawRGB || !sawUnderline {
		t.Errorf("colours: indexed %v rgb %v underline %v", sawIndexed, sawRGB, sawUnderline)
	}
}

// fragments are the pieces a stream of terminal output is made of: introducers, parameters, finals, controls, printable
// text of every width, and broken UTF-8. Random streams of them reach states a corpus never does.
var fragments = []string{
	"\x1b", "\x1b[", "\x1b]", "\x1bP", "\x1b_", "\x1b^", "\x1bX", "\x1b(", "\x1b\\", "\x9b", "\x9d", "\x90", "\x9c", "\u009b", "\u009d",
	"\u0085", "[", "]", ";", ":", "?", "<", ">", "=", "!", " ", "$", "0", "1", "2", "3", "5", "6", "7", "8", "9", "10", "25", "38", "48",
	"99999999999", "1049", "2004", "2026", "A", "B", "C", "D", "E", "F", "G", "H", "J", "K", "L", "M", "S", "T", "X", "d", "f", "h", "l", "m", "n", "r", "s", "t", "u",
	"\x07", "\x08", "\x09", "\x0a", "\x0b", "\x0c", "\x0d", "\x18", "\x1a", "\x7f", "\x00",
	"a", "Z", "~", "é", "\u0301", "\u200d", "\ufe0f", "中", "あ", "😀", "\xe4", "\xb8", "\xad", "\xff", "\xc0\x80", "\xed\xa0\x80",
	"\x1b[?1049h", "\x1b[?1049l", "\x1b[?25l", "\x1b[?25h", "\x1b[2J", "\x1b[H", "\x1b[K", "\x1b[1;31m", "\x1b[0m", "\x1b[6n", "\x1b]52;c;QQ==\x07",
	"\x1b]8;;http://x\x1b\\", "\x1b[3;3H", "\x1b[5L", "\x1b[5M", "\x1b[9S", "\x1b[9T", "\x1b7", "\x1b8", "\x1bM", "\x1bD", "\x1bE", "\x1bc",
}

func TestRandomStreamsKeepTheInvariants(t *testing.T) {
	for seed := int64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		v := New(rng.Intn(30)+1, rng.Intn(12)+1)
		v.SetScrollbackLimit(rng.Intn(40))
		v.SetReflow(rng.Intn(4) != 0)
		for step := 0; step < 40; step++ {
			var sb strings.Builder
			for n := rng.Intn(25); n > 0; n-- {
				sb.WriteString(fragments[rng.Intn(len(fragments))])
			}
			v.WriteString(sb.String())
			if rng.Intn(6) == 0 {
				v.Resize(rng.Intn(30)+1, rng.Intn(12)+1)
			}
			checkInvariants(t, v)
		}
	}
}

func FuzzWrite(f *testing.F) {
	names, files := corpus(f)
	for _, name := range names {
		f.Add(files[name], uint8(80), uint8(24), uint8(40), uint8(10))
		f.Add(files[name], uint8(9), uint8(3), uint8(200), uint8(60))
	}
	for _, s := range []string{
		"", "\x1b", "\x1b[", "\x1b]52;c;AAAA", "\x1b[?1049h\x1b[2;3Hx\x1b[?1049l", "a\x1b[1;2;3;4;5;6;7;8;9;10;11;12m", "\xe4\xb8",
		"abc\x1b[99999999999999999999A", "\x1b[38;2;1;2m\x1b[38:2::1:2:3m\x1b[4:3m", "中中中中\x1b[2G\x1b[3X", "\x1bP\x1b\\\x1b_\x1b\\",
	} {
		f.Add([]byte(s), uint8(10), uint8(4), uint8(3), uint8(2))
	}
	f.Fuzz(func(t *testing.T, data []byte, cols, rows, rcols, rrows uint8) {
		v := New(int(cols%200)+1, int(rows%60)+1)
		mid := len(data) / 2
		v.Write(data[:mid])
		checkInvariants(t, v)
		v.Write(data[mid:])
		checkInvariants(t, v)
		v.Resize(int(rcols%200)+1, int(rrows%60)+1)
		checkInvariants(t, v)
		v.Write(data)
		checkInvariants(t, v)
		v.Reply()
	})
}
