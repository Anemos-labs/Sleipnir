package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The longest thing a replacement puts in: ⟦redacted:private_key:abcdef⟧ is 33 bytes, and a
// replacement takes out at least one.
const longestReplacement = 36

// FuzzRedactKinds runs any subset of the rules over arbitrary text and checks what the
// package promises whatever the subset is: the same text in, the same text out (on a fresh
// redactor and on one that has seen it); a text that has been redacted is a fixed point;
// nothing grows by more than a token a replacement and there are no more replacements than
// bytes; the counts are of rules that are on, and Changed agrees with them; no rules, no
// change; a key on a line of its own never survives.
func FuzzRedactKinds(f *testing.F) {
	all := uint32(1)<<len(AllKinds()) - 1
	for i, kind := range AllKinds() {
		for _, c := range ruleTable[kind].pos {
			f.Add(c.in, uint32(1)<<i, "")
			f.Add(c.in, all, "anon")
		}
		for _, c := range ruleTable[kind].neg {
			f.Add(c.in, uint32(1)<<i, "")
			f.Add(c.in, all, "")
		}
	}
	f.Add("", all, "")
	f.Add("/home/alice/x /home/user/y /Users/anon/z", all, "anon")
	f.Add("⟦redacted:aws:abcdef⟧ ⟦redacted:nonsense⟧ ⟦redacted:aws:abcdef", all, "")
	f.Add("password="+strings.Repeat("a1B2", 40), all, "")
	f.Add(strings.Repeat("key=a1B2c3D4e5F6g7H8i9J0 ", 20), all, "")

	f.Fuzz(func(t *testing.T, in string, mask uint32, name string) {
		kinds := []string{}
		on := map[string]bool{}
		for i, k := range AllKinds() {
			if mask&(1<<i) != 0 {
				kinds = append(kinds, k)
				on[k] = true
			}
		}
		cfg := Config{Salt: "fz", Kinds: kinds, PathPlaceholder: placeholderName(name)}

		r := New(cfg)
		out := r.String(in)
		total := r.Total()

		if fresh := New(cfg).String(in); fresh != out {
			t.Fatalf("not deterministic (kinds %v):\n%q\n%q", kinds, out, fresh)
		}
		if again := r.String(out); again != out {
			t.Fatalf("not idempotent (kinds %v):\n in: %q\n1: %q\n2: %q", kinds, in, out, again)
		}
		if r.Total() != total {
			t.Fatalf("redacted text was redacted again: %d replacements became %d", total, r.Total())
		}
		if utf8.ValidString(in) && !utf8.ValidString(out) {
			t.Fatalf("valid UTF-8 became invalid (kinds %v): %q -> %q", kinds, in, out)
		}

		if total > len(in) || len(out) > len(in)+longestReplacement*total {
			t.Fatalf("%d replacements took %d bytes to %d (kinds %v): %q", total, len(in), len(out), kinds, in)
		}
		for k, n := range r.Stats() {
			if !on[k] || n <= 0 {
				t.Fatalf("stats %v list %q, which is off or empty (kinds %v)", r.Stats(), k, kinds)
			}
		}
		if len(kinds) == 0 && out != in {
			t.Fatalf("no rules on, and the text changed: %q -> %q", in, out)
		}

		c := New(cfg)
		got, changed := c.Changed(in)
		if got != out || changed != (out != in) || changed != (c.Total() > 0) {
			t.Fatalf("Changed = (%q, %v) for %q -> %q with %d replacements", got, changed, in, out, c.Total())
		}

		// The memo serves the text it has seen, and the counts come with it.
		if r.String(in) != out || r.Total() != 2*total {
			t.Fatalf("a second pass over the same text gave %q and %d replacements, want %q and %d", r.String(in), r.Total(), out, 2*total)
		}

		// (Text that holds the key inside a longer run of letters and digits is left alone on
		// purpose, and then the key is in the output for that reason.)
		if on[KindAWS] && !strings.Contains(in, awsKey) {
			wrapped := in + "\n" + awsKey + "\n" + in
			if strings.Contains(New(cfg).String(wrapped), awsKey) {
				t.Fatalf("the planted key survived (kinds %v) in %q", kinds, wrapped)
			}
		}
	})
}

// placeholderName is a home-directory placeholder the fuzzer proposes, cut to a name: a
// placeholder with a slash in it makes a new path for the rule to find.
func placeholderName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && (r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') && b.Len() < 12 {
			b.WriteRune(r)
		}
	}
	return b.String()
}
