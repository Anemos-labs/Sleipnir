package hooks

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "hello\nworld", "hello\nworld"},
		{"invalid utf8", "a\xff\xfeb", "a\uFFFDb"},
		{"colours", "\x1b[1;31mred\x1b[0m", "red"},
		{"osc with bel", "a\x1b]0;title\x07b", "ab"},
		{"osc with st", "a\x1b]52;c;ZGF0YQ==\x1b\\b", "ab"},
		{"dcs", "a\x1bPdata\x1b\\b", "ab"},
		{"two byte escape", "a\x1bcb", "ab"},
		{"lone escape at the end", "abc\x1b", "abc"},
		{"unterminated csi", "abc\x1b[12", "abc"},
		{"unterminated osc", "abc\x1b]0;never ends", "abc"},
		{"controls", "a\x00b\x01c\x7fd", "abcd"},
		{"tabs and newlines", "a\tb\nc", "a\tb\nc"},
		{"crlf", "a\r\nb\rc", "a\nb\nc"},
		{"hidden unicode", "a\u202Eb\u200Bc", "abc"},
		{"unicode text", "héllo 世界", "héllo 世界"},
	}
	for _, tc := range tests {
		if got := cleanText([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: cleanText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	// An escape sequence that never ends must not swallow the rest of the output.
	long := "start\x1b]0;" + strings.Repeat("x", 10000) + "\nafter"
	if got := cleanText([]byte(long)); !strings.HasSuffix(got, "after") {
		t.Errorf("an unterminated OSC ate the rest: %d bytes", len(got))
	}
}

func TestCapText(t *testing.T) {
	if got := capText("short", 100); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := capText("anything", 0); got != "anything" {
		t.Errorf("no cap: %q", got)
	}
	got := capText(strings.Repeat("a", 100), 10)
	if !strings.HasPrefix(got, "aaaaaaaaaa") || !strings.HasSuffix(got, truncMark) {
		t.Errorf("got %q", got)
	}
	// Never inside a multi-byte character.
	for n := 1; n < 20; n++ {
		if out := capText(strings.Repeat("世", 20), n); !utf8.ValidString(out) {
			t.Fatalf("cap %d gave invalid UTF-8: %q", n, out)
		}
	}
	// Trailing space before the marker is trimmed.
	if got := capText("word   more", 7); strings.Contains(got, " \n") {
		t.Errorf("got %q", got)
	}
}

func TestParseOutputPlainTextAndErrors(t *testing.T) {
	for _, in := range []string{"", "   ", "plain text", "[1,2,3]", "\"string\"", "42"} {
		if _, ok, err := parseOutput(PreToolUse, []byte(in)); ok || err != nil {
			t.Errorf("%q: ok=%v err=%v; text must be left to the caller", in, ok, err)
		}
	}
	for _, in := range []string{"{", `{"a":`, `{"a": 1,}`, "{'a': 1}", "{} trailing"} {
		if _, ok, err := parseOutput(PreToolUse, []byte(in)); ok || err == nil || !strings.Contains(err.Error(), "starts like JSON") {
			t.Errorf("%q: ok=%v err=%v", in, ok, err)
		}
	}
	// Leading white space is fine.
	if p, ok, err := parseOutput(PreToolUse, []byte("\n  {\"hookSpecificOutput\":{\"permissionDecision\":\"deny\"}}\n")); !ok || err != nil || p.decision != Deny {
		t.Errorf("padded JSON: %+v %v %v", p, ok, err)
	}
}

func TestParseOutputPrecedence(t *testing.T) {
	// hookSpecificOutput wins over the top level.
	p, _, err := parseOutput(PreToolUse, []byte(`{"decision":"block","reason":"top","hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"specific"}}`))
	if err != nil || p.decision != Allow || p.reason != "specific" {
		t.Errorf("%+v %v", p, err)
	}
	// A decision the event does not support is ignored, not misapplied.
	p, _, _ = parseOutput(Notification, []byte(`{"decision":"block","hookSpecificOutput":{"permissionDecision":"deny"}}`))
	if p.decision != "" || p.block {
		t.Errorf("Notification cannot block: %+v", p)
	}
	// The reason is cleaned like all hook text.
	p, _, _ = parseOutput(PreToolUse, []byte(`{"reason":"a\u001b[31mb","decision":"block"}`))
	if p.reason != "a[31mb" && p.reason != "ab" {
		t.Logf("reason = %q", p.reason)
	}
	if strings.Contains(p.reason, "\x1b") {
		t.Errorf("escape survived: %q", p.reason)
	}
}
