package widget

import "testing"

// A write's control characters are spelled out in its diff, and the text an escape sequence encloses stays: the person approves
// what the file will hold.
func TestDiffTextShowsControlCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"plain\ttext":                       "plain   text",
		"a\x1b]8;;http://x\x07hidden\x1b\\": `a\x1b]8;;http://x\x07hidden\x1b\`,
		"bell\x07":                          `bell\x07`,
		"c1\u009b31m":                       `c1\x9b31m`,
	} {
		if got := diffText(in, 4); got != want {
			t.Errorf("diffText(%q) = %q, want %q", in, got, want)
		}
	}
}
