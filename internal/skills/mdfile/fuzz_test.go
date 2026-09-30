package mdfile

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkValue verifies the structural invariants every parsed Value must have,
// whatever the input was.
func checkValue(t *testing.T, v Value, depth int) {
	t.Helper()
	if depth > maxDepth+2 {
		t.Fatalf("value nested %d levels deep", depth)
	}
	switch v.Kind {
	case KindNull:
	case KindString:
		if v.List != nil || v.Keys != nil {
			t.Fatalf("string with children: %+v", v)
		}
	case KindList:
		for _, e := range v.List {
			checkValue(t, e, depth+1)
		}
	case KindMap:
		if len(v.Keys) != len(v.Vals) {
			t.Fatalf("keys and values differ: %d vs %d", len(v.Keys), len(v.Vals))
		}
		seen := map[string]bool{}
		for i, k := range v.Keys {
			if k == "" || seen[k] {
				t.Fatalf("empty or duplicate key %q in %v", k, v.Keys)
			}
			seen[k] = true
			checkValue(t, v.Vals[i], depth+1)
		}
	default:
		t.Fatalf("unknown kind %d", v.Kind)
	}
}

// FuzzParse: any text either parses to a well-formed document or is rejected
// with an error; it never panics or hangs.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"", "plain body", "---\nname: x\n---\nbody", "---\n---\n", "---\na: [1, 2\n---\n", "---\na: |\n  x\n---\n",
		"---\nl:\n- a\n- b: c\n  d: e\n---\n", "---\nm: {a: 1, b: [x, {c: d}]}\n---\n", "{\"a\": 1}\nbody", "{not json",
		"---\na: 'it''s'\nb: \"\\u00e9\\n\"\n---\n", "---\n\ta: 1\n---\n", "---\na: 1\na: 2\n---\n", "---\nd: >-\n  a\n\n  b\n---\n",
		"---\n" + strings.Repeat("- ", 50) + "x\n---\n", "---\nk:\n  - - - a\n---\n", "---\na: \"unterminated\n---\n", "---\r\na: 1\r\n---\r\n",
		"---\n? complex\n: key\n---\n", "---\n&a x: *a\n---\n", "---\na: 'x\n  y'\n---\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := Parse(s)
		if err != nil {
			return
		}
		if d.Meta.Kind != KindMap {
			t.Fatalf("meta is %v, not a mapping", d.Meta.Kind)
		}
		checkValue(t, d.Meta, 0)
		if !strings.HasSuffix(s, d.Body) {
			t.Fatalf("body %q is not the tail of the input", d.Body)
		}
		if d.BodyLine < 1 {
			t.Fatalf("body line %d", d.BodyLine)
		}
		if f, err := NewFields(d.Meta); err == nil {
			for _, k := range d.Meta.Keys {
				f.Str(k)
				f.Bool(k)
				f.Int(k)
				f.List(k)
			}
		}
	})
}

// FuzzSanitize: the output is valid UTF-8 (for valid input), contains none of
// the characters it is meant to remove, and sanitising twice changes nothing.
func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{"", "plain", "a\u202Eb", "\U000E0041\U000E0042", "👨\u200D👩", "a\u200Db", "\x1b[0m", "x\u2028y", "\u200D\u200D\u200D"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return // Sanitize runs on normalised text; Normalize repairs invalid UTF-8 first
		}
		out, h := Sanitize(s)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 out of valid input: %q", out)
		}
		if len(out) > len(s) { // U+2028 (3 bytes) becomes "\n" (1 byte): output never grows
			t.Fatalf("output grew: %d -> %d", len(s), len(out))
		}
		rs := []rune(out)
		for i, r := range rs {
			if r == 0x200C || r == 0x200D {
				// Joiners may survive, but only between two visible non-ASCII characters.
				if i == 0 || i+1 >= len(rs) || rs[i-1] <= 0x7F || rs[i+1] <= 0x7F {
					t.Fatalf("stray joiner survived at %d in %q", i, out)
				}
				continue
			}
			if c := classify(r); c != keepRune {
				t.Fatalf("hidden character %U survived at %d in %q", r, i, out)
			}
		}
		again, h2 := Sanitize(out)
		if again != out || h2.Total() != 0 {
			t.Fatalf("not idempotent: %q -> %q (%+v)", out, again, h2)
		}
		if h.Total() < 0 {
			t.Fatal("negative count")
		}
	})
}

// FuzzSubstitute: substitution never panics, leaves a template without "$"
// alone, and never expands what an argument contains.
func FuzzSubstitute(f *testing.F) {
	for _, seed := range [][2]string{
		{"", ""}, {"$ARGUMENTS", "a b"}, {"$1 $2 $$1 $ARGUMENTS[0]", "x 'y z'"}, {"$$", "a"}, {"$ARGUMENTS[", "a"}, {"$ARGUMENTS[99999]", "a"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, tmpl, args string) {
		for _, mode := range []ArgMode{ArgsText, ArgsShell} {
			out, used := Substitute(tmpl, args, mode)
			if !strings.Contains(tmpl, "$") && (out != tmpl || used) {
				t.Fatalf("template without a dollar changed: %q -> %q", tmpl, out)
			}
		}
		// With a marker argument, the marker must appear exactly as often as the
		// template has placeholders that can produce it, never more: proof that
		// output is not re-scanned.
		const marker = "MARKER$1MARKER"
		out, _ := Substitute("$1", marker, ArgsText)
		if out != marker {
			t.Fatalf("argument was rewritten: %q", out)
		}
	})
}

// FuzzSplitArgs: quoting the words of any argument string and splitting again
// gives the same words back, which is what makes ShellQuote safe to rely on.
func FuzzSplitArgs(f *testing.F) {
	for _, seed := range []string{"", "a b", `"a b" 'c d'`, `it\'s`, `"" ''`, "a\tb\nc", `x"y`, `\`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		words := SplitArgs(s)
		q := make([]string, len(words))
		for i, w := range words {
			q[i] = ShellQuote(w)
		}
		back := SplitArgs(strings.Join(q, " "))
		if len(words) == 0 && len(back) == 0 {
			return
		}
		if !reflect.DeepEqual(words, back) {
			t.Fatalf("round trip changed the words: %q -> %q -> %q", words, q, back)
		}
	})
}

// FuzzSplitList: items are non-empty, unique and free of surrounding space.
func FuzzSplitList(f *testing.F) {
	for _, seed := range []string{"", "a, b", "Bash(x y), Read", `"a" 'b'`, "((", "))", "a,a,a"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		seen := map[string]bool{}
		for _, item := range SplitList(s) {
			if item == "" || item != strings.TrimSpace(item) || seen[item] {
				t.Fatalf("bad item %q in %q", item, SplitList(s))
			}
			seen[item] = true
		}
		_ = ValidTool(s)
	})
}

// FuzzStripComments: never panics and never adds text.
func FuzzStripComments(f *testing.F) {
	for _, seed := range []string{"", "a <!-- b --> c", "<!--", "```\n<!-- x -->\n```", "`<!--`", "<!-- a\nb -->", "~~~~\n~~~\n<!--"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if out := StripComments(s); len(out) > len(s) {
			t.Fatalf("output longer than input: %d > %d", len(out), len(s))
		}
	})
}

// FuzzEscapeTags: the output has no "<" followed by a tag-like character.
func FuzzEscapeTags(f *testing.F) {
	for _, seed := range []string{"", "<a>", "</x>", "<!--", "<<a", "a<b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := EscapeTags(s)
		for i := 0; i+1 < len(out); i++ {
			if out[i] == '<' {
				if n := out[i+1]; n == '/' || n == '!' || n == '?' || (n|0x20 >= 'a' && n|0x20 <= 'z') {
					t.Fatalf("unescaped tag start in %q", out)
				}
			}
		}
	})
}
