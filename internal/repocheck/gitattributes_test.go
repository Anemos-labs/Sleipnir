package repocheck

import (
	"regexp"
	"strings"
	"testing"
)

// The golden files and the recorded gallery are compared byte for byte. A checkout that turns their line ends into CRLF (git's
// default on Windows: core.autocrlf) fails every one of those tests at its first line, with a diff that looks identical, so the
// repository says what its line ends are and does not leave it to each machine's git configuration.
func TestGitattributesPinLineEnds(t *testing.T) {
	text := read(t, ".gitattributes")
	if !regexp.MustCompile(`(?m)^\*\s+text=auto\s+eol=lf\s*$`).MatchString(text) {
		t.Error(".gitattributes has no line `* text=auto eol=lf`: a checkout on Windows writes CRLF into the golden files and every golden test fails")
	}
	// The terminal recordings carry CRLF on purpose; text handling would rewrite them.
	if !regexp.MustCompile(`(?m)^internal/tui/vt/testdata/\*\*\s+-text\s*$`).MatchString(text) {
		t.Error(".gitattributes does not mark internal/tui/vt/testdata/** as -text: the recordings there carry CRLF on purpose")
	}
	for _, ext := range []string{"png"} {
		if !strings.Contains(text, "*."+ext+" binary") {
			t.Errorf(".gitattributes does not mark *.%s as binary", ext)
		}
	}
}
