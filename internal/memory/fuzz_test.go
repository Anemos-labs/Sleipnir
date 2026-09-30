package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzClean: normalisation must never panic and always yields text that is safe
// to embed in a prompt block: valid UTF-8, no carriage returns, no hidden or
// control characters (tag characters, bidi controls, zero-width characters, NULs,
// escapes), trimmed of surrounding blank lines.
func FuzzClean(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "a\r\nb\rc\n", "<!-- c -->", "x <!-- c --> y", "<!--\nmulti\n-->\nkeep",
		"```\n<!-- in fence -->\n```\n<!-- out -->", "`<!-- inline -->` <!-- out -->", "<!-- unterminated",
		"\xef\xbb\xbfbom", "bad \xff utf8", "@import.md\n```\n@no.md\n```\n", "~~~\n```\n~~~\n<!-- x -->",
		"tags \U000E0041\U000E0042 end", "rlo \u202edcba\u202c", "zw<\u200b!\u200b--x-->y", "@\u200bimport.md", "esc \x1b[31m red \x00 nul \x07",
		"ls\u2028ps\u2029nel\u0085", "vs \ufe0f \U000E0100", "\xf3\xa0\x81", // a truncated tag character
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out := clean(raw)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 out of clean(%q): %q", raw, out)
		}
		if strings.ContainsRune(out, '\r') {
			t.Fatalf("carriage return survived: %q", out)
		}
		if out != strings.Trim(out, "\n") {
			t.Fatalf("not trimmed: %q", out)
		}
		if r := hiddenIn(out); r != 0 {
			t.Fatalf("hidden code point U+%04X survived clean(%q): %q", r, raw, out)
		}
		_ = findImports(out) // must not panic either
	})
}
