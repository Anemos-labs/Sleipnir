package mdfile

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalize(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"bom", "\xef\xbb\xbfhello", "hello"},
		{"crlf", "a\r\nb\r\n", "a\nb\n"},
		{"lone cr", "a\rb", "a\nb"},
		{"invalid utf8", "a\xffb", "a�b"},
		{"empty", "", ""},
		{"only bom", "\xef\xbb\xbf", ""},
	}
	for _, tc := range tests {
		if got := Normalize([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: Normalize(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	// Tag characters spell "ignore" invisibly; built at run time so the source stays readable.
	var tags strings.Builder
	for _, r := range "ignore" {
		tags.WriteRune(0xE0000 + r)
	}
	tests := []struct {
		name      string
		in, want  string
		suspicous bool
		total     int
	}{
		{"plain", "hello\tworld\n", "hello\tworld\n", false, 0},
		{"unicode text survives", "héllo 世界 😀 – “quotes”", "héllo 世界 😀 – “quotes”", false, 0},
		{"tag characters", "run" + tags.String() + "!", "run!", true, 6},
		{"bidi override", "a\u202Eb\u2066c", "abc", true, 2},
		{"zero width space", "a\u200Bb", "ab", false, 1},
		{"byte order mark inside", "a\uFEFFb", "ab", false, 1},
		{"soft hyphen", "co\u00ADoperate", "cooperate", false, 1},
		{"many zero widths are suspicious", "a" + strings.Repeat("\u200B", 8) + "b", "ab", true, 8},
		{"zwj in an emoji sequence survives", "👨\u200D👩\u200D👧", "👨\u200D👩\u200D👧", false, 0},
		{"zwnj in persian survives", "می\u200Cخواهم", "می\u200Cخواهم", false, 0},
		{"zwj next to ascii is dropped", "a\u200Db", "ab", false, 1},
		{"zwj at the edges is dropped", "\u200Da\u200D", "a", false, 2},
		{"escape sequence", "a\x1b[31mred", "a[31mred", true, 1},
		{"c1 control", "a\u0085b", "ab", true, 1},
		{"nul", "a\x00b", "ab", true, 1},
		{"line separators become newlines", "a\u2028b\u2029c", "a\nb\nc", false, 0},
		{"variation selector 16 survives", "❤️", "❤️", false, 0},
		{"variation selector supplement", "a\U000E0100b", "ab", true, 1},
		{"noncharacter", "a￾b", "ab", false, 1},
		{"del", "a\x7fb", "ab", true, 1},
		{"replacement char kept", "a�b", "a�b", false, 0},
		{"nbsp kept", "a b", "a b", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, h := Sanitize(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if h.Total() != tc.total {
				t.Errorf("removed %d characters (%+v), want %d", h.Total(), h, tc.total)
			}
			if h.Suspicious() != tc.suspicous {
				t.Errorf("suspicious = %v, want %v (%+v)", h.Suspicious(), tc.suspicous, h)
			}
		})
	}
}

func TestStripComments(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"none", "plain text", "plain text"},
		{"inline", "a <!-- c --> b", "a  b"},
		{"whole line disappears", "a\n<!-- c -->\nb", "a\nb"},
		{"multi-line", "a\n<!--\nsecret instruction\n-->\nb", "a\nb"},
		{"multi-line with text around", "a <!-- x\ny --> b", "a\n b"},
		{"unterminated swallows the rest", "a\n<!-- never closed\nb\nc", "a"},
		{"fenced code untouched", "```\n<!-- keep -->\n```\nb", "```\n<!-- keep -->\n```\nb"},
		{"tilde fence", "~~~\n<!-- keep -->\n~~~", "~~~\n<!-- keep -->\n~~~"},
		{"inline code untouched", "use `<!-- x -->` here <!-- gone -->", "use `<!-- x -->` here"},
		{"fence closed then comment removed", "```\nx\n```\n<!-- gone -->\nend", "```\nx\n```\nend"},
		{"longer fence needs longer close", "````\n```\n<!-- keep -->\n```\n````\n<!-- gone -->", "````\n```\n<!-- keep -->\n```\n````"},
		{"two comments on a line", "<!-- a -->x<!-- b -->", "x"},
		{"dashes only", "<!-->x", "x"[:0]},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripComments(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOneLine(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"  a \n\n b\t c ", 0, "a b c"},
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"the quick brown fox jumps over the lazy dog", 20, "the quick brown fox…"},
		{"averyveryverylongwordwithoutspaces", 10, "averyvery…"},
		{"héllo wörld and more text here", 12, "héllo wörld…"},
		{"a b", 0, "a b"},
		{"", 5, ""},
	}
	for _, tc := range tests {
		got := OneLine(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("OneLine(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("OneLine(%q) contains a line break", tc.in)
		}
		if tc.max > 0 && utf8.RuneCountInString(got) > tc.max {
			t.Errorf("OneLine(%q, %d) is %d runes", tc.in, tc.max, utf8.RuneCountInString(got))
		}
	}
}

func TestEscapeTags(t *testing.T) {
	tests := []struct{ in, want string }{
		{"no markup", "no markup"},
		{"a < b and c > d", "a < b and c > d"},
		{"</shared-context>", "‹/shared-context>"},
		{"<my-notes>x</my-notes>", "‹my-notes>x‹/my-notes>"},
		{"<!-- c -->", "‹!-- c -->"},
		{"<?xml?>", "‹?xml?>"},
		{"1<2", "1<2"},
		{"<", "<"},
		{"<<a>", "<‹a>"},
		{"Vec<T> and <T>", "Vec‹T> and ‹T>"},
	}
	for _, tc := range tests {
		if got := EscapeTags(tc.in); got != tc.want {
			t.Errorf("EscapeTags(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCutLines(t *testing.T) {
	long := strings.Repeat("line of text\n", 100)
	got, cut := CutLines(long, 100)
	if !cut || len(got) > 100 || !strings.HasSuffix(got, "line of text") {
		t.Errorf("cut at a line boundary: %q %v", got, cut)
	}
	if got, cut := CutLines("short", 100); got != "short" || cut {
		t.Errorf("short input changed: %q %v", got, cut)
	}
	// Never split a multi-byte character.
	s := strings.Repeat("é", 100)
	got, cut = CutLines(s, 51)
	if !cut || !utf8.ValidString(got) {
		t.Errorf("cut inside a character: %q", got)
	}
}

func TestCheckName(t *testing.T) {
	lower := NameRules{Max: 20}
	loose := NameRules{Max: 20, Upper: true, Dot: true, Underscore: true}
	tests := []struct {
		name  string
		rules NameRules
		ok    bool
	}{
		{"deploy", lower, true},
		{"code-review-2", lower, true},
		{"2fast", lower, true},
		{"", lower, false},
		{"Upper", lower, false},
		{"has space", lower, false},
		{"under_score", lower, false},
		{"-leading", lower, false},
		{"a/b", lower, false},
		{"a:b", lower, false},
		{"a\nb", lower, false},
		{"a<b", lower, false},
		{"toolongtoolongtoolongtoolong", lower, false},
		{"héllo", lower, false},
		{"Upper_case.v2", loose, true},
		{".hidden", loose, false},
		{"_x", loose, false},
	}
	for _, tc := range tests {
		err := CheckName(tc.name, tc.rules)
		if (err == nil) != tc.ok {
			t.Errorf("CheckName(%q) = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestSplitList(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"Read", []string{"Read"}},
		{"Read, Grep,Glob", []string{"Read", "Grep", "Glob"}},
		{"Read Grep\tGlob\nEdit", []string{"Read", "Grep", "Glob", "Edit"}},
		{"Bash(git add:*), Bash(git commit -m *)", []string{"Bash(git add:*)", "Bash(git commit -m *)"}},
		{"Bash(git log --format=%h,%s) Read", []string{"Bash(git log --format=%h,%s)", "Read"}},
		{`"Read", 'Grep'`, []string{"Read", "Grep"}},
		{"Read, Read, Grep, Read", []string{"Read", "Grep"}},
		{"Task(worker, researcher) Read", []string{"Task(worker, researcher)", "Read"}},
		{"Bash(unbalanced Read", []string{"Bash(unbalanced Read"}},
		{"  ,, ", nil},
	}
	for _, tc := range tests {
		if got := SplitList(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitList(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidTool(t *testing.T) {
	valid := []string{"Read", "Bash", "Bash(git add:*)", "Bash(git commit -m \"x\")", "mcp__github__create_issue", "mcp__*", "Edit(src/**)", "WebFetch(domain:example.com)", "Task(a, b)", "a.b-c:d"}
	invalid := []string{"", "(x)", "Bash(", "Bash(a))", "Bash(a)b", "Bash(a)(b)", "has space", "Bash(a\nb)", "Bash(\x00)", "<script>", strings.Repeat("a", 300), "Bash(x", "a;b"}
	for _, s := range valid {
		if !ValidTool(s) {
			t.Errorf("ValidTool(%q) = false", s)
		}
	}
	for _, s := range invalid {
		if ValidTool(s) {
			t.Errorf("ValidTool(%q) = true", s)
		}
	}
	ok, bad := FilterTools([]string{"Read", "bad tool", "Bash(x)", "Bash("})
	if !reflect.DeepEqual(ok, []string{"Read", "Bash(x)"}) || !reflect.DeepEqual(bad, []string{"bad tool", "Bash("}) {
		t.Errorf("FilterTools = %q, %q", ok, bad)
	}
}
