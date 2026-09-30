package tools

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// u builds a string from code points. The invisible and control characters this file is about are
// spelled as numbers, never written into the source, so that the file itself reads the same in an
// editor as it does to the compiler.
func u(cps ...rune) string { return string(cps) }

const rep = "�" // written by the sanitiser for invalid UTF-8; visible on purpose

func TestSanitizeForTerminal(t *testing.T) {
	long := strings.Repeat("x", 5000)
	tests := []struct {
		name, in, want string
	}{
		// text that must come through untouched
		{"empty", "", ""},
		{"ascii", "hello, world 123 ~!@#", "hello, world 123 ~!@#"},
		{"newlines and tabs", "a\tb\nc\n\n", "a\tb\nc\n\n"},
		{"unicode text", "héllo wörld ✓ 日本語 Ελληνικά 🎉", "héllo wörld ✓ 日本語 Ελληνικά 🎉"},
		{"emoji zwj sequence and marks kept", "👨" + u(0x200d) + "👩" + u(0x200d) + "👧 a" + u(0x200e) + "b" + u(0x200f) + "c " + u(0x200c), "👨" + u(0x200d) + "👩" + u(0x200d) + "👧 a" + u(0x200e) + "b" + u(0x200f) + "c " + u(0x200c)},
		{"nbsp and other spaces", "a" + u(0xa0) + "b" + u(0x2003) + "c", "a" + u(0xa0) + "b" + u(0x2003) + "c"},
		{"latin-1 supplement above C1", u(0xa1, 0xff), u(0xa1, 0xff)},

		// escape sequences, complete
		{"sgr", "\x1b[31mred\x1b[0m plain", "red plain"},
		{"sgr with many params", "\x1b[1;38;2;255;0;0;48;5;17mx\x1b[m", "x"},
		{"cursor and erase", "a\x1b[2J\x1b[Hb\x1b[10;20Hc\x1b[K", "abc"},
		{"private mode", "\x1b[?1049h\x1b[?25lx\x1b[?25h", "x"},
		{"csi with intermediate", "\x1b[ qx", "x"},
		{"osc title, bel", "x\x1b]0;evil title\x07y", "xy"},
		{"osc 52 clipboard, st", "\x1b]52;c;ZXZpbA==\x1b\\ok", "ok"},
		{"osc 8 hyperlink", "\x1b]8;;http://evil.example\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"osc terminated by c1 st", "a\x1b]0;t" + u(0x9c) + "b", "ab"},
		{"dcs", "\x1bPq#0;2;0;0;0\x1b\\text", "text"},
		{"apc", "\x1b_Gi=1,a=q\x1b\\text", "text"},
		{"pm", "\x1b^private\x1b\\text", "text"},
		{"sos", "\x1bXstring\x1b\\text", "text"},
		{"charset selection", "\x1b(Bplain\x1b)0x\x1b*Ay", "plainxy"},
		{"two byte forms", "\x1bcreset\x1b7save\x1b8back\x1bMup\x1b=\x1b>", "resetsavebackup"},

		// escape sequences, broken or hostile: only what cannot be part of a sequence is kept as text
		{"csi without final loses only its introducer", "abc\x1b[31", "abc31"},
		{"csi with a control byte inside", "a\x1b[3\x01m", "a3m"},
		{"osc never ends", "abc\x1b]0;never ends", "abc0;never ends"},
		{"osc aborted by a new escape drops its payload, the new sequence is handled on its own", "a\x1b]0;title\x1b[31mred", "ared"},
		{"stray esc at the end", "abc\x1b", "abc"},
		{"esc esc", "\x1b\x1b[31mx", "x"},
		{"esc then a control", "a\x1b\x01b", "ab"},
		{"esc then a c1 char", "a\x1b" + u(0x85) + "b", "ab"},
		{"esc then a letter that is not a sequence", "a\x1bzb", "ab"},
		{"long unterminated osc keeps the text after it", "\x1b]" + long, long},

		// C1 and C0 controls
		{"c1 as utf-8: csi", u(0x9b) + "31mred", "31mred"},
		{"c1 as utf-8: osc", u(0x9d) + "0;title\x07x", "0;titlex"},
		{"c1 as utf-8: dcs, nel, ss3", u(0x90) + "a" + u(0x85) + "b" + u(0x8f) + "c", "abc"},
		{"c0 controls", "a\x00b\x07c\x08d\x0be\x0cf\x0eg\x0fh\x7fi\x01\x02\x03\x04\x05\x06\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1c\x1d\x1e\x1fz", "abcdefghiz"},
		{"nul bytes", "a\x00\x00b", "ab"},

		// carriage returns
		{"crlf", "a\r\nb\r\n", "a\nb\n"},
		{"lone cr", "loading 10%\rloading 90%\rdone", "loading 10%\nloading 90%\ndone"},
		{"cr cr lf", "a\r\r\nb", "a\n\nb"},
		{"cr at the end", "a\r", "a\n"},
		{"the classic overwrite", "rm -rf /\rharmless", "rm -rf /\nharmless"},
		{"line and paragraph separators", "a" + u(0x2028) + "b" + u(0x2029) + "c", "a\nb\nc"},

		// invisible and reordering characters
		{"bidi override", "safe" + u(0x202e) + "txt.exe", "safetxt.exe"},
		{"bidi embeddings and isolates", "a" + u(0x202a, 0x202b, 0x202c, 0x202d, 0x2066, 0x2067, 0x2068, 0x2069) + "b", "ab"},
		{"tag characters", "a" + u(0xE0041, 0xE0069, 0xE007F) + "b", "ab"},
		{"variation selectors supplement", "a" + u(0xE0100) + "b", "ab"},
		{"zero width space, word joiner, bom, soft hyphen", "a" + u(0x200b) + "b" + u(0x2060) + "c" + u(0xfeff) + "d" + u(0xad) + "e" + u(0x2062) + "f" + u(0x180e) + "g", "abcdefg"},

		// invalid utf-8
		{"invalid byte", "a\xffb", "a" + rep + "b"},
		{"truncated multibyte", "a\xe2\x82", "a" + rep + rep},
		{"a lone 0x9b byte is not a csi", "\x9b31mred", rep + "31mred"},
		{"overlong nul", "a\xc0\x80b", "a" + rep + rep + "b"},
		{"utf-8 encoded surrogate", "a\xed\xa0\x80b", "a" + rep + rep + rep + "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeForTerminal(tt.in)
			if got != tt.want {
				t.Fatalf("SanitizeForTerminal(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
			if again := SanitizeForTerminal(got); again != got {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
			checkTerminalSafe(t, got)
		})
	}
}

// checkTerminalSafe asserts the properties every output must have, whatever the input.
func checkTerminalSafe(t testing.TB, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("invalid UTF-8 in %q", s)
	}
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
		case r < 0x20 || r == 0x7f:
			t.Fatalf("control character %U survived in %q", r, s)
		case r >= 0x80 && r < 0xA0:
			t.Fatalf("C1 control %U survived in %q", r, s)
		case r == 0x2028 || r == 0x2029:
			t.Fatalf("line separator %U survived in %q", r, s)
		case Invisible(r):
			t.Fatalf("invisible character %U survived in %q", r, s)
		}
	}
}

// Clean text is returned as it is, without copying it.
func TestSanitizeForTerminalDoesNotAllocateForCleanText(t *testing.T) {
	for _, s := range []string{"", "plain ascii text\nwith lines\tand tabs", "héllo wörld ✓ 日本語 🎉", strings.Repeat("line of output\n", 1000)} {
		if n := testing.AllocsPerRun(20, func() { _ = SanitizeForTerminal(s) }); n != 0 {
			t.Errorf("%q...: %v allocations for clean text", s[:min(len(s), 20)], n)
		}
	}
}

// Hostile input costs time linear in its size: every escape sequence is bounded and none can make the
// scanner look further than the next escape.
func TestSanitizeForTerminalIsLinearOnHostileInput(t *testing.T) {
	const size = 4 << 20
	hostile := map[string]string{
		"csi introducers":           strings.Repeat("\x1b[", size/2),
		"osc introducers":           strings.Repeat("\x1b]", size/2),
		"osc with payload, no end":  strings.Repeat("\x1b]0;"+strings.Repeat("a", 200), size/205),
		"csi with long parameters":  strings.Repeat("\x1b["+strings.Repeat("1", 60), size/62),
		"intermediates":             strings.Repeat("\x1b("+strings.Repeat(" ", 14), size/16),
		"only escape":               strings.Repeat("\x1b", size),
		"one huge unterminated osc": "\x1b]" + strings.Repeat("a", size),
		"carriage returns":          strings.Repeat("\r", size),
		"invalid utf-8":             strings.Repeat("\xff", size),
		"alternating text and bidi": strings.Repeat("ab"+u(0x202e), size/5),
		"c1 and c0":                 strings.Repeat(u(0x9b)+"\x01", size/3),
	}
	for name, in := range hostile {
		start := time.Now()
		out := SanitizeForTerminal(in)
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%s: %v for %d bytes", name, d, len(in))
		}
		head := out[:min(len(out), 1<<16)]
		for !utf8.ValidString(head) && len(head) > 0 { // the cut may fall inside a character
			head = head[:len(head)-1]
		}
		checkTerminalSafe(t, head)
		if strings.IndexByte(out, 0x1b) >= 0 {
			t.Errorf("%s: an ESC survived", name)
		}
	}
}

func TestInvisible(t *testing.T) {
	for _, r := range []rune{0xAD, 0x180E, 0x200B, 0x2060, 0xFEFF, 0x2061, 0x2064, 0x202A, 0x202E, 0x2066, 0x2069, 0xE0000, 0xE0041, 0xE007F, 0xE0100, 0xE01EF} {
		if !Invisible(r) {
			t.Errorf("%U should be invisible", r)
		}
	}
	for _, r := range []rune{'a', ' ', '\n', 0x200C, 0x200D, 0x200E, 0x200F, 0xA0, 0x2028, 0x3000, 0x1F600, 0xE0080, 0xE00FF, 0xE01F0, 0xFFFD} {
		if Invisible(r) {
			t.Errorf("%U should be visible or deliberately kept", r)
		}
	}
}

func FuzzSanitizeForTerminal(f *testing.F) {
	for _, s := range []string{
		"", "plain", "\x1b[31mred\x1b[0m", "\x1b]0;t\x07", "\x1b]52;c;abc\x1b\\", "\x1bPq\x1b\\", "a\rb", u(0x9b) + "31m", "\xff\xfe", "\x1b[", "\x1b", "\x1b(",
		"safe" + u(0x202e) + "txt", "a" + u(0x2028) + "b", "\x1b]0;x\x1b[31m", u(0xE0041),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := SanitizeForTerminal(s)
		checkTerminalSafe(t, out)
		if strings.IndexByte(out, 0x1b) >= 0 {
			t.Fatalf("ESC in output %q for input %q", out, s)
		}
		if again := SanitizeForTerminal(out); again != out {
			t.Fatalf("not idempotent: %q -> %q (input %q)", out, again, s)
		}
		if len(out) > 3*len(s) {
			t.Fatalf("output grew from %d to %d bytes", len(s), len(out))
		}
		// Text that needs no cleaning is not touched.
		if !needsTerminalCleaning(s) && out != s {
			t.Fatalf("clean text was changed: %q -> %q", s, out)
		}
	})
}
