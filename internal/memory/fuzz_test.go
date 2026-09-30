package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzClean: normalisation must never panic and always yields text that is safe
// to embed in a prompt block: valid UTF-8, no carriage returns, no NULs kept as
// raw bytes beyond what the input had, trimmed of surrounding blank lines.
func FuzzClean(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "a\r\nb\rc\n", "<!-- c -->", "x <!-- c --> y", "<!--\nmulti\n-->\nkeep",
		"```\n<!-- in fence -->\n```\n<!-- out -->", "`<!-- inline -->` <!-- out -->", "<!-- unterminated",
		"\xef\xbb\xbfbom", "bad \xff utf8", "@import.md\n```\n@no.md\n```\n", "~~~\n```\n~~~\n<!-- x -->",
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
		_ = findImports(out) // must not panic either
	})
}
