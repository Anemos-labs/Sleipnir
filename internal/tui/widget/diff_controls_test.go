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

// A CRLF line ending is a line ending, but a bare carriage return that ends the text is content the file keeps, and is drawn.
func TestSplitDiffLinesKeepsAFinalBareCarriageReturn(t *testing.T) {
	for in, want := range map[string][]string{
		"a\r\nb\r\n": {"a", "b"},
		"a\r\nb":     {"a", "b"},
		"text\r":     {"text\r"},
		"a\nb\r":     {"a", "b\r"},
	} {
		got := splitDiffLines(in)
		if len(got) != len(want) {
			t.Errorf("splitDiffLines(%q) = %q, want %q", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("splitDiffLines(%q) = %q, want %q", in, got, want)
				break
			}
		}
	}
	if diffText(splitDiffLines("text\r")[0], 4) != "text␍" {
		t.Errorf("a final bare carriage return is not drawn: %q", diffText(splitDiffLines("text\r")[0], 4))
	}
}
