package widget

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

// The widgets clean text with their own copy of tools.SanitizeForTerminal (this package may not import tools: it is
// standard-library only). Text that went through the canonical function first must pass through the copy unchanged, and the two
// must never drift apart; this file is the alarm. (If tools changes on purpose, change markdown_text.go to match and keep this
// test.)

func TestCleanTextAgreesWithSanitizeForTerminal(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	g := widgettest.NewRand(7)
	corpus := append(widgettest.Hostile(), widgettest.Unicode()...)
	for i := 0; i < 400; i++ {
		corpus = append(corpus, g.Text(1+g.Intn(25)))
	}
	alphabet := []byte("ab \t\n\r\x1b[]()0123;?P_^\\\x07\x00\x7f\xc2\x85\x9c\x9b\xe2\x80\xae\xff")
	for i := 0; i < 3000; i++ {
		b := make([]byte, rng.Intn(40))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		corpus = append(corpus, string(b))
	}
	for _, in := range corpus {
		// Line by line: the two differ only for an OSC string that runs over a newline (see safeText), and a line has none.
		var got, want []string
		for _, line := range strings.Split(in, "\n") {
			got = append(got, safeText(line))
			want = append(want, tools.SanitizeForTerminal(line))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("safeText(%q) = %q, but tools.SanitizeForTerminal gives %q", in, got[i], want[i])
			}
		}
		// and as a whole, when no escape introducer is followed by a newline
		if !strings.Contains(in, "\x1b") {
			if g, w := safeText(in), tools.SanitizeForTerminal(in); g != w {
				t.Fatalf("safeText(%q) = %q, but tools.SanitizeForTerminal gives %q", in, g, w)
			}
		}
	}
}

// An escape string that is not finished on its line is not finished: what comes after the newline is text, and whatever
// arrives later cannot reach back over the newline and remove it.
func TestCleanTextStringSequencesEndAtTheLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\x1b]0;title\nb\x07c", "a0;title\nbc"},
		{"a\x1bPq\nb\x1b\\c", "aq\nbc"},
		{"a\x1b_G\nb\x07", "aG\nb"},
		{"a\x1b]0;title\x07b\nc", "ab\nc"},
	}
	for _, c := range cases {
		if got := safeText(c.in); got != c.want {
			t.Errorf("safeText(%q) = %q, want %q", c.in, got, c.want)
		}
		// the cleaning of the lines before the last one does not depend on what follows them
		for cut := 0; cut <= len(c.in); cut++ {
			prefix := safeText(c.in[:cut])
			whole := safeText(c.in)
			lines := strings.Split(prefix, "\n")
			done := strings.Join(lines[:len(lines)-1], "\n")
			if done != "" && !strings.HasPrefix(whole, done+"\n") {
				t.Errorf("cleaning %q[:%d] gives complete lines %q that %q does not start with", c.in, cut, done, whole)
			}
		}
	}
}

func TestInvisibleRunesAgreeWithTools(t *testing.T) {
	for r := rune(0); r <= 0x10FFFF; r++ {
		if invisibleRune(r) != tools.Invisible(r) {
			t.Fatalf("U+%04X: invisibleRune %v, tools.Invisible %v", r, invisibleRune(r), tools.Invisible(r))
		}
	}
}
