package provider

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The invisible characters are built at run time, so that no invisible character sits
// in this file where a reviewer could not see it.
var (
	bidiOverride = string(rune(0x202e))
	bidiIsolate  = string(rune(0x2066))
	zwsp         = string(rune(0x200b))
	zwj          = string(rune(0x200d))
	wordJoiner   = string(rune(0x2060))
	bom          = string(rune(0xfeff))
	softHyphen   = string(rune(0x00ad))
	tagA         = string(rune(0xe0041))
	tagCancel    = string(rune(0xe007f))
	varSel       = string(rune(0xfe0f))
	varSelSupp   = string(rune(0xe0100))
	hangulFill   = string(rune(0x3164))
	noncharacter = string(rune(0xfdd0))
	privateUse   = string(rune(0xe000))
	lineSep      = string(rune(0x2028))
	nbsp         = string(rune(0x00a0))
	c1CSI        = string(rune(0x9b))
	c1OSC        = string(rune(0x9d))
	c1ST         = string(rune(0x9c))
	nel          = string(rune(0x85))
)

func TestSanitizeText(t *testing.T) {
	const esc = "\x1b"
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain text is untouched", "context length exceeded: 200001 > 200000", "context length exceeded: 200001 > 200000"},
		{"non-ASCII text survives", "Zażółć gęślą jaźń — 日本語 — 🙂", "Zażółć gęślą jaźń — 日本語 — 🙂"},
		{"empty", "", ""},
		{"only whitespace", " \t\r\n ", ""},
		{"whitespace runs collapse", "a  \t b\r\n\r\nc", "a b c"},
		{"leading and trailing whitespace go", "\n  padded  \n", "padded"},
		{"a message cannot pose as several log lines", "boom\nsleipnir: session started\nERROR: all is well", "boom sleipnir: session started ERROR: all is well"},
		{"unicode line separators are line breaks", "a" + lineSep + "b" + nel + "c", "a b c"},
		{"no-break space is a space", "a" + nbsp + nbsp + "b", "a b"},

		// ANSI escape sequences go with their payload.
		{"SGR colour", "before " + esc + "[31;1mred" + esc + "[0m after", "before red after"},
		{"clear screen and home", "x" + esc + "[2J" + esc + "[Hy", "xy"},
		{"cursor and erase with intermediates", "a" + esc + "[?25l" + esc + "[1;2 qb", "ab"},
		{"OSC 52 clipboard write, BEL terminated", "a" + esc + "]52;c;ZXZpbA==\a" + "b", "ab"},
		{"OSC 52 clipboard write, ST terminated", "a" + esc + "]52;c;ZXZpbA==" + esc + "\\" + "b", "ab"},
		{"OSC title", esc + "]0;owned\atext", "text"},
		{"OSC hyperlink", esc + "]8;;http://evil.example\atext" + esc + "]8;;\a", "text"},
		{"DCS string", "a" + esc + "Pq#0;2;0;0;0" + esc + "\\b", "ab"},
		{"APC string", "a" + esc + "_secret payload" + esc + "\\b", "ab"},
		{"PM string", "a" + esc + "^private" + esc + "\\b", "ab"},
		{"SOS string", "a" + esc + "Xstring" + esc + "\\b", "ab"},
		{"charset designation", "a" + esc + "(Bb", "ab"},
		{"two byte escape: full reset", "a" + esc + "cb", "ab"},
		{"two byte escape: save cursor", "a" + esc + "7b" + esc + "8c", "abc"},
		{"lone ESC at the end", "abc" + esc, "abc"},
		{"ESC before a control character", "a" + esc + "\x01b", "ab"},
		{"unterminated OSC swallows the rest, as in a terminal", "keep " + esc + "]52;c;AAAA and this too", "keep"},
		{"ESC inside a string that is not ST ends it", "a" + esc + "]0;title" + esc + "[31mred", "ared"},
		{"8-bit CSI", "a" + c1CSI + "31mb", "ab"},
		{"8-bit OSC with 8-bit ST", "a" + c1OSC + "52;c;ZXZpbA==" + c1ST + "b", "ab"},
		{"other C1 controls", "a\u0080\u0081\u0084\u008fb", "ab"},
		{"C0 controls other than white space", "a\x00b\x01c\x07d\x08e\x7ff", "abcdef"},
		{"NUL bytes", strings.Repeat("\x00", 50) + "x", "x"},

		// Hidden and direction-changing characters are dropped, never shown.
		{"bidi override", "user" + bidiOverride + "gpj.exe", "usergpj.exe"},
		{"bidi isolate", "a" + bidiIsolate + "b", "ab"},
		{"zero width space and joiner", "pass" + zwsp + "word" + zwj + "!", "password!"},
		{"word joiner, BOM, soft hyphen", "a" + wordJoiner + bom + softHyphen + "b", "ab"},
		{"Unicode tag characters that spell text", "ok" + tagA + tagA + tagCancel, "ok"},
		{"variation selectors", "a" + varSel + varSelSupp + "b", "ab"},
		{"filler characters", "a" + hangulFill + "b", "ab"},
		{"noncharacters", "a" + noncharacter + "b", "ab"},
		{"private use", "a" + privateUse + "b", "ab"},
		{"invalid UTF-8 is replaced", "a\xff\xfeb", "a" + string(utf8.RuneError) + "b"},
		{"a truncated multi-byte character", "ok\xe2\x82", "ok" + string(utf8.RuneError)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeText(tc.in, 0); got != tc.want {
				t.Errorf("SanitizeText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeTextCapsLength(t *testing.T) {
	long := strings.Repeat("x", 900_000)
	got := SanitizeText(long, 0)
	if len(got) != MaxErrorText || !strings.HasSuffix(got, "…") {
		t.Fatalf("a 900 KB message became %d bytes ending %q", len(got), got[len(got)-6:])
	}
	for _, max := range []int{1, 2, 3, 4, 5, 6, 10, 100} {
		got := SanitizeText(strings.Repeat("ab", 500), max)
		if len(got) > max || !utf8.ValidString(got) {
			t.Errorf("max %d: got %d bytes %q", max, len(got), got)
		}
	}
	// A multi-byte character is never cut in half, at any budget.
	for max := 1; max <= 40; max++ {
		got := SanitizeText(strings.Repeat("日本語", 30), max)
		if len(got) > max || !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
			t.Fatalf("max %d: %q", max, got)
		}
	}
	// Text at the budget is not marked as cut.
	if got := SanitizeText(strings.Repeat("y", 50), 50); got != strings.Repeat("y", 50) {
		t.Errorf("text that fits was altered: %q", got)
	}
	// Text just over the budget is.
	if got := SanitizeText(strings.Repeat("y", 51), 50); len(got) != 50 || !strings.HasSuffix(got, "…") {
		t.Errorf("text over the budget: %q", got)
	}
	// The cap applies to what is left after the escapes are removed, not to the input:
	// a long escape sequence in front of a short message costs the message nothing.
	esc := "\x1b]52;c;" + strings.Repeat("A", 300) + "\a"
	if got := SanitizeText(esc+"short", 20); got != "short" {
		t.Errorf("payload counted against the budget: %q", got)
	}
}

// Hostile input costs about as much as the output budget, not as much as the input.
func TestSanitizeTextWorkIsBounded(t *testing.T) {
	for name, in := range map[string]string{
		"escape sequences": strings.Repeat("\x1b[31m", 2_000_000),
		"invisible":        strings.Repeat(zwsp, 3_000_000),
		"unterminated OSC": "\x1b]52;" + strings.Repeat("A", 8_000_000),
		"whitespace":       strings.Repeat(" \n", 4_000_000),
	} {
		got := SanitizeText(in, 0)
		if len(got) > MaxErrorText {
			t.Errorf("%s: %d bytes", name, len(got))
		}
	}
}

func TestSanitizeTextIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"plain", "  spaced\n\tout ", "\x1b[31mred\x1b[0m", "a" + zwsp + "b", strings.Repeat("日本語", 2000), "x\xffy",
		"\x1b]52;c;AAAA", strings.Repeat("z", 5000),
	} {
		once := SanitizeText(in, 0)
		if twice := SanitizeText(once, 0); twice != once {
			t.Errorf("not idempotent for %q:\n once  %q\n twice %q", in, once, twice)
		}
	}
}

// The invariants that make the result safe to log and print, over arbitrary input.
func FuzzSanitizeText(f *testing.F) {
	for _, s := range []string{
		"", "plain", "\x1b[31m", "\x1b]52;c;AAAA\a", "a\u202eb", "\xff\xfe", "\U000e0041", "\x9b31m", "line1\nline2",
		"\x1b(B", "\x1bP1$r\x1b\\", strings.Repeat("\x1b", 100),
	} {
		f.Add(s, 64)
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 1 || max > 4096 {
			max = 64
		}
		got := SanitizeText(s, max)
		if len(got) > max {
			t.Fatalf("%d bytes > max %d", len(got), max)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8: %q", got)
		}
		if got != strings.TrimSpace(got) || strings.Contains(got, "  ") {
			t.Fatalf("untidy white space: %q", got)
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || hiddenRune(r) || r == '\u2028' || r == '\u2029' {
				t.Fatalf("U+%04X survived in %q", r, got)
			}
		}
		if again := SanitizeText(got, max); again != got {
			t.Fatalf("not idempotent: %q -> %q", got, again)
		}
	})
}

func TestErrorTextIsSanitisedEvenWhenBuiltByHand(t *testing.T) {
	e := &Error{Kind: ErrBadRequest, Status: 400, Message: "bad\n\x1b]52;c;AAAA\a\x1b[2J" + zwsp + strings.Repeat("y", 100_000)}
	s := e.Error()
	if len(s) > MaxErrorText+64 {
		t.Fatalf("Error() is %d bytes", len(s))
	}
	if strings.ContainsAny(s, "\x1b\a\n") || strings.Contains(s, zwsp) {
		t.Fatalf("Error() carries control or invisible characters: %q", s[:80])
	}
	if !strings.HasPrefix(s, "provider: bad_request (http 400): bad yyy") {
		t.Fatalf("Error() = %q", s[:80])
	}
	// The structured fields are left as they are: only the text is made safe.
	if e.Message == "" || !strings.Contains(e.Message, "\x1b") {
		t.Fatal("Error() must not modify the struct")
	}
	if got := (&Error{Kind: ErrTimeout, Message: "quiet"}).Error(); got != "provider: timeout: quiet" {
		t.Fatalf("ordinary errors changed: %q", got)
	}
}

func TestCapRaw(t *testing.T) {
	small := []byte(`{"error":"x"}`)
	if got := CapRaw(small); string(got) != string(small) {
		t.Fatalf("a small body must be kept verbatim: %q", got)
	}
	small[0] = 'X'
	if string(CapRaw([]byte("abc"))) != "abc" || CapRaw(nil) != nil && len(CapRaw(nil)) != 0 {
		t.Fatal("empty and tiny bodies")
	}
	big := []byte(strings.Repeat("z", 5<<20))
	if got := CapRaw(big); len(got) != MaxRawBytes {
		t.Fatalf("a 5 MiB body kept %d bytes", len(got))
	}
	// A copy: later changes to the source do not show through.
	src := []byte("hello")
	kept := CapRaw(src)
	src[0] = 'J'
	if string(kept) != "hello" {
		t.Fatalf("CapRaw aliased its input: %q", kept)
	}
}
