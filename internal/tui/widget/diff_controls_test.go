package widget

import "testing"

// A write's control characters are drawn in its diff, and the text an escape sequence encloses stays: the person approves what
// the file will hold. A drawn control never looks like text a line holds as written.
func TestDiffTextShowsControlCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"plain\ttext":                       "plain   text",
		"a\x1b]8;;http://x\x07hidden\x1b\\": "a␛]8;;http://x␇hidden␛\\",
		"bell\x07":                          "bell␇",
		"c1\u009b31m":                       "c1⟨9b⟩31m",
		`literal \x1b[31m`:                  `literal \x1b[31m`,
	} {
		if got := diffText(in, 4); got != want {
			t.Errorf("diffText(%q) = %q, want %q", in, got, want)
		}
	}
	if diffText("\x1b[31m", 4) == diffText(`\x1b[31m`, 4) {
		t.Error("an ESC and the text \\x1b look the same")
	}
}
