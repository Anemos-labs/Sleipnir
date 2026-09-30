package redact

import (
	"strings"
	"testing"
	"time"
)

// Redaction runs over every message of every trajectory that is exported, and tool
// output is cut at 8 MiB, not at a line: one line can be a megabyte of minified code
// or a log record. The work has to grow with the size of the text, not with the
// square of the length of a line.
//
// The limit below is a guard against a complexity bomb, not a timing. The work in the
// test that uses it is a few milliseconds with the window the rule reads, and many
// minutes with a search back to the start of the line.
const redactGuard = 3 * time.Minute

func withinGuard(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(redactGuard):
		t.Fatalf("%s did not finish within %v (a complexity bomb, or a loop that never ends)", what, redactGuard)
	}
}

// The entropy rule asks, for each candidate, what stands in front of it on its line. On a
// line of many candidates (minified code, a log record, a base64 dump) that has to cost a
// few bytes, not the length of the line up to there: a line of 4 MB made of candidates
// took the rule more than three minutes when it looked back to the start of the line.
//
// The text is a line of 16 MB with no line break, and the question is asked for places
// near its end, 64,000 times: a search to the start of the line reads a terabyte for that.
func TestEntropyKeyWindowCostDoesNotGrowWithTheLine(t *testing.T) {
	line := strings.Repeat("a", 16<<20)
	withinGuard(t, "64000 questions about the end of a line of 16 MB", func() {
		for i := 0; i < 64_000; i++ {
			at := len(line) - i%1000
			if w := entropyKeyWindow(line, at); len(w) != 64 {
				t.Errorf("window at %d has %d bytes, want 64", at, len(w))
				return
			}
		}
	})
}

// A long line is redacted like a short one: text with no key word in front of a candidate is
// left as it is, and a secret is found wherever it is on the line, at its start or four
// kilobytes along it.
func TestEntropyRuleOnALongLine(t *testing.T) {
	const cand = "a1B2c3D4e5F6g7H8i9J0 " // twenty characters of mixed case and digits, which pass every test but the key word
	const secret = "Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"
	const n = 200
	r := New(Config{Salt: "s", Kinds: []string{KindEntropy}})

	line := strings.Repeat(cand, n)
	if out := r.String(line); out != line {
		t.Errorf("a line of candidates with no key word in front of them was changed")
	}
	for _, before := range []int{0, 1, 3, 10, n} {
		planted := strings.Repeat(cand, before) + "secret " + secret + " " + strings.Repeat(cand, n-before)
		out := New(Config{Salt: "s", Kinds: []string{KindEntropy}}).String(planted)
		if strings.Contains(out, secret) || !strings.Contains(out, "secret ⟦redacted:entropy:") {
			t.Errorf("the secret after %d candidates survived: %.120q", before, out)
		}
		if strings.Count(out, cand) != n {
			t.Errorf("the candidates around the secret were changed (%d of %d left)", strings.Count(out, cand), n)
		}
	}
}

// The window the entropy rule reads in front of a candidate is the start of its line
// or the 64 bytes before it, whichever is shorter.
func TestEntropyKeyWindow(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		at         int
		want       string
	}{
		{"start of the text", "token abc", 6, "token "},
		{"after a newline", "x\ntoken abc", 8, "token "},
		{"a long line is cut at 64 bytes", strings.Repeat("k", 100) + "abc", 100, strings.Repeat("k", 64)},
		{"exactly 64 bytes from the start", strings.Repeat("k", 64) + "abc", 64, strings.Repeat("k", 64)},
		{"a newline inside the window", strings.Repeat("k", 40) + "\n" + strings.Repeat("j", 10) + "abc", 51, strings.Repeat("j", 10)},
		{"a newline just outside the window", "\n" + strings.Repeat("k", 70) + "abc", 71, strings.Repeat("k", 64)},
		{"a newline right in front", "abc\nxyz", 4, ""},
		{"nothing in front", "abc", 0, ""},
	} {
		if got := entropyKeyWindow(tc.text, tc.at); got != tc.want {
			t.Errorf("%s: window = %q, want %q", tc.name, got, tc.want)
		}
		// The definition the window replaces: the line start found by searching back over the whole
		// text, cut to 64 bytes.
		ls := strings.LastIndexByte(tc.text[:tc.at], '\n') + 1
		if tc.at-ls > 64 {
			ls = tc.at - 64
		}
		if old := tc.text[ls:tc.at]; entropyKeyWindow(tc.text, tc.at) != old {
			t.Errorf("%s: %q is not what a search to the start of the line gives (%q)", tc.name, entropyKeyWindow(tc.text, tc.at), old)
		}
	}
}
