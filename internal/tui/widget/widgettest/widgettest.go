// Package widgettest holds the helpers the tests of the terminal widgets share: readable text forms of styled lines, for
// assertions and golden files; a check for control characters; hostile and Unicode corpora; a seeded generator of awkward
// input; and the golden-file comparison.
//
// It depends on internal/tui/cell only (not on internal/tui/widget), so the tests of widget, render and app can all use it, and
// it registers no flags: a test package owns its own -update flag and passes the value in.
package widgettest

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// Flatten is the plain text of the lines, one row per line, joined by "\n" with no trailing newline. Spaces at the end of a row
// are trimmed (they are invisible and editors strip them from golden files), so use Line.Width, not the length of this text,
// to measure a row.
func Flatten(lines []cell.Line) string {
	rows := make([]string, len(lines))
	for i, l := range lines {
		rows[i] = strings.TrimRight(l.Plain(), " ")
	}
	return strings.Join(rows, "\n")
}

// FlattenStyled is Flatten with the styles written into the text, so a golden file shows what is bold or red. A run of text in a
// non-default style is written {codes}text{/}, where codes is a list separated by commas: the attribute letters b (bold), d
// (dim), i (italic), u (underline), r (reverse), s (strike), then fg=COLOUR and bg=COLOUR. A colour is #rrggbb, aN (ANSI
// colour N) or xN (256-colour index N). Runs of the same style are merged first, so the notation does not depend on how the
// spans happen to be split. Unstyled text is written as it is; a style with no attribute and no colour is unstyled. The notation
// is for reading, not parsing (a literal "{b}" in the text looks like a tag).
func FlattenStyled(lines []cell.Line) string { return FlattenStyledWith(lines, nil) }

// FlattenStyledWith is FlattenStyled with names for colours: a colour found in names is written as its name (fg=good) instead of
// its value, which makes a golden file of a real theme readable.
func FlattenStyledWith(lines []cell.Line, names map[cell.Color]string) string {
	rows := make([]string, len(lines))
	for i, l := range lines {
		var sb strings.Builder
		var run cell.Span
		flush := func() {
			if run.Text == "" {
				return
			}
			if code := styleCode(run.Style, names); code != "" {
				sb.WriteString("{" + code + "}" + run.Text + "{/}")
			} else {
				sb.WriteString(run.Text)
			}
		}
		for _, sp := range l {
			if sp.Text == "" {
				continue
			}
			if sp.Style == run.Style && run.Text != "" {
				run.Text += sp.Text
				continue
			}
			flush()
			run = sp
		}
		flush()
		rows[i] = strings.TrimRight(sb.String(), " ")
	}
	return strings.Join(rows, "\n")
}

func styleCode(st cell.Style, names map[cell.Color]string) string {
	var parts []string
	var attrs strings.Builder
	for _, a := range []struct {
		bit cell.Attr
		ch  byte
	}{{cell.Bold, 'b'}, {cell.Dim, 'd'}, {cell.Italic, 'i'}, {cell.Underline, 'u'}, {cell.Reverse, 'r'}, {cell.Strike, 's'}} {
		if st.Attr&a.bit != 0 {
			attrs.WriteByte(a.ch)
		}
	}
	if attrs.Len() > 0 {
		parts = append(parts, attrs.String())
	}
	if st.FG.Kind != cell.KindDefault {
		parts = append(parts, "fg="+colourName(st.FG, names))
	}
	if st.BG.Kind != cell.KindDefault {
		parts = append(parts, "bg="+colourName(st.BG, names))
	}
	return strings.Join(parts, ",")
}

func colourName(c cell.Color, names map[cell.Color]string) string {
	if n, ok := names[c]; ok {
		return n
	}
	switch c.Kind {
	case cell.KindANSI:
		return "a" + strconv.Itoa(int(c.N))
	case cell.KindIndexed:
		return "x" + strconv.Itoa(int(c.N))
	case cell.KindRGB:
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return "default"
}

// MaxWidth is the width in cells of the widest line (0 for no lines).
func MaxWidth(lines []cell.Line) int {
	w := 0
	for _, l := range lines {
		w = max(w, l.Width())
	}
	return w
}

// Control looks for text a terminal would act on instead of showing: a C0 or C1 control (ESC included), DEL, the Unicode line and
// paragraph separators, invalid UTF-8, and the invisible runes that reorder or hide text (bidi overrides, zero-width space, BOM,
// tag characters). It reports where the first
// one is, for an error message, and whether there was one. Nothing a widget returns may contain any.
func Control(lines []cell.Line) (where string, found bool) {
	for i, l := range lines {
		col := 0
		for _, sp := range l {
			if !utf8.ValidString(sp.Text) {
				return fmt.Sprintf("line %d: invalid UTF-8 in %q", i, sp.Text), true
			}
			for _, r := range sp.Text {
				if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029 || invisible(r) {
					return fmt.Sprintf("line %d, cell %d: U+%04X in %q", i, col, r, l.Plain()), true
				}
				col += cell.RuneWidth(r)
			}
		}
	}
	return "", false
}

func invisible(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x180E, r == 0x200B, r == 0x2060, r == 0xFEFF:
	case r >= 0x2061 && r <= 0x2064, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
	case r >= 0xE0000 && r <= 0xE007F, r >= 0xE0100 && r <= 0xE01EF:
	default:
		return false
	}
	return true
}

// Golden compares got with the file at path, or rewrites the file when update is true (a test package defines its own -update
// flag, the convention of internal/kv: go test ./internal/tui/widget -update). The file holds got and a final newline. When the
// text differs the failure names the first line that differs and shows it as it was and as it is. A missing file fails with
// the command that creates it. Line endings of the file are compared as "\n", so a checkout that changed them still passes.
func Golden(t testing.TB, update bool, path, got string) {
	t.Helper()
	if !strings.HasSuffix(got, "\n") {
		got += "\n"
	}
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (create it with -update, and read it before committing): %v", path, err)
	}
	want := strings.ReplaceAll(string(data), "\r\n", "\n")
	if want == got {
		return
	}
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			t.Fatalf("output differs from %s at line %d (%d lines want, %d got):\n  want: %s\n  got:  %s\nIf the change is intended, rerun with -update and read the diff of testdata.",
				path, i+1, len(wl), len(gl), w, g)
		}
	}
}

// Hostile returns text that tries to make a terminal do something: escape sequences of every family (colours, cursor movement,
// clear screen, OSC 0 title, OSC 52 clipboard, OSC 8 hyperlink, DCS, APC), C1 controls, stray and unterminated introducers,
// carriage returns and backspaces that redraw a line, bidi overrides, zero-width characters and invalid UTF-8. Each entry is
// text a widget might be asked to show; none may survive into its output.
func Hostile() []string {
	return []string{
		"\x1b[31mred\x1b[0m",
		"\x1b[2J\x1b[H cleared",
		"before\x1b]52;c;SGVsbG8gd29ybGQ=\x07after",
		"before\x1b]52;c;SGVsbG8gd29ybGQ=\x1b\\after",
		"\x1b]0;window title\x07title",
		"\x1b]8;;http://evil.example\x1b\\click\x1b]8;;\x1b\\",
		"\x1bPq#0;2;0;0;0\x1b\\dcs",
		"\x1b_Gf=100,a=T;AAAA\x1b\\apc",
		"\U0000009B31mc1 csi\U0000009D0;title\x07",
		"a\U00000085b\U00000090c\U0000009Fd",
		"stray \x1b",
		"unterminated \x1b]52;c;AAAA",
		"unterminated \x1b[31",
		"\x1b(B\x1b)0\x1bc\x1b7\x1b8",
		"rm -rf /\rharmless",
		"back\x08\x08\x08space",
		"bell\x07 form\x0c vt\x0b nul\x00 del\x7f",
		"\U0000202Ehidden\U0000202C \U00002066isolate\U00002069 zero\U0000200Bwidth \U0000FEFFbom \U00002060joiner",
		"tag\U000E0041\U000E0042 selector\U000E0100",
		"bad \xff\xfe bytes \xc3",
		"line\U00002028separator\U00002029paragraph",
		"tab\there\tand\nnewline\r\ncrlf",
	}
}

// Unicode returns text that is awkward to measure and to cut: East Asian wide characters, emoji (single and ZWJ sequences, with
// variation selectors and skin tones), combining marks, Hangul, Thai, right-to-left scripts, full-width forms, box-drawing and
// the glyphs the widgets draw themselves.
func Unicode() []string {
	return []string{
		"日本語のテキストです。長い文章は折り返されます。",
		"中文，全角标点；还有「引号」。",
		"한국어 텍스트와 English が混在",
		"emoji 😀 🐎 🚀 👨\U0000200D👩\U0000200D👧\U0000200D👦 🏳\U0000FE0F\U0000200D🌈 👍🏽 ❤\U0000FE0F ⚠\U0000FE0F ✓ ✗",
		"e\U00000301 a\U00000308 n\U00000303 combining: ǝ\U00000338\U00000327\U0000031B\U0000035A\U00000349\U0000032A\U00000347 z\U00000334\U00000327\U0000031B\U0000031B\U00000349\U0000035A\U00000330a\U0000034Fl\U0000034Fg\U0000034Fo",
		"ก\U00000E47ไม\U00000E48ร\U00000E39\U00000E49 ภาษาไทย ไม\U00000E48ม\U00000E35ช\U00000E48องว\U00000E48าง",
		"שלום עולם مرحبا بالعالم",
		"ＡＢＣ　ｆｕｌｌｗｉｄｔｈ １２３",
		"╭──╮ │▏▎▍▌▋▊▉█ ▰▱ ⠋⠙⠹ ❯ ↳ ⋯ … • ◦ ▪ −",
		"\U0000200D\U0000200D\U0000200D lone joiners \U0000FE0F\U0000FE0F variation selectors",
		strings.Repeat("𝕬", 40),
		strings.Repeat("語", 61) + "x",
	}
}

// Rand is a deterministic generator of awkward text for property tests. The same seed gives the same text on every machine,
// so a failure names a seed and is replayed by it.
type Rand struct{ r *rand.Rand }

// NewRand returns a generator for seed.
func NewRand(seed int64) *Rand { return &Rand{r: rand.New(rand.NewSource(seed))} }

// Intn is a number in [0, n); n < 1 gives 0.
func (g *Rand) Intn(n int) int {
	if n < 1 {
		return 0
	}
	return g.r.Intn(n)
}

// Pick returns one of the items.
func (g *Rand) Pick(items ...string) string { return items[g.Intn(len(items))] }

var words = []string{
	"the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog", "func", "return", "error", "nil", "x", "y", "offset", "page",
	"size", "a", "b", "ok", "true", "go", "test", "./orders/...", "https://example.com/a_b_c?q=1&r=2", "naïve", "café", "日本語", "中文字",
	"😀", "🐎", "e\U00000301", "a\U00000308b", "👨\U0000200D👩\U0000200D👧", "—", "…", "«q»", "\x1b[31m", "\x1b]52;c;QQ==\x07", "\U0000009B", "\x00", "\x7f", "\U0000202E", "\xff",
}

// Text is n words and separators mixed from plain words, wide and combining runes, emoji, and hostile fragments.
func (g *Rand) Text(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(words[g.Intn(len(words))])
		switch g.Intn(12) {
		case 0:
			sb.WriteString("\n")
		case 1:
			sb.WriteString("  ")
		case 2:
			sb.WriteString("\t")
		default:
			sb.WriteString(" ")
		}
	}
	return sb.String()
}

var mdPieces = []string{
	"# ", "## ", "### ", "#### ", "- ", "* ", "+ ", "1. ", "2) ", "- [ ] ", "- [x] ", "> ", ">> ", "```", "```go", "~~~", "    ", "  ", "\t",
	"---", "***", "===", "|", "| a | b |", "|---|:-:|", "| --- | ---: |", "**", "*", "__", "_", "~~", "`", "``", "[", "]", "(", ")", "![", "](",
	"<", ">", "<br>", "<https://x.y>", "&amp;", "&#27;", "\\", "\\*", "\n", "\n", "\n\n", " ", " ", "  \n", "word", "words", "日本", "😀", "e\U00000301",
	"\x1b[31m", "\x1b]52;c;QQ==\x07", "\r\n", "https://example.com/path", "[link](https://example.com)", "[a](b \"t\")", "`code`",
	"**bold**", "*em*", "~~gone~~", "a_b_c", "2*3*4", "\U0000009D", "\x00",
}

// Markdown is n fragments of markdown syntax and text (markers, fences, tables, lists, links, escapes, hostile bytes) in
// random order: mostly broken markdown, which is what a model emits when it is cut off or confused.
func (g *Rand) Markdown(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(mdPieces[g.Intn(len(mdPieces))])
	}
	return sb.String()
}

// Chunks cuts s at random byte offsets (inside a rune or an escape sequence included) into between 1 and max pieces that
// concatenate to s. Empty pieces can occur.
func (g *Rand) Chunks(s string, maxPieces int) []string {
	if maxPieces < 1 {
		maxPieces = 1
	}
	n := 1 + g.Intn(maxPieces)
	cuts := make([]int, 0, n)
	for i := 1; i < n && len(s) > 0; i++ {
		cuts = append(cuts, g.Intn(len(s)+1))
	}
	slices.Sort(cuts)
	var out []string
	prev := 0
	for _, c := range cuts {
		out = append(out, s[prev:c])
		prev = c
	}
	return append(out, s[prev:])
}
