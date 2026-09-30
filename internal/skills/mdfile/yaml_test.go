package mdfile

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// show renders a Value on one line so table cases can state what they expect.
func show(v Value) string {
	switch v.Kind {
	case KindNull:
		return "~"
	case KindString:
		return strconv.Quote(v.Str)
	case KindList:
		parts := make([]string, len(v.List))
		for i, e := range v.List {
			parts[i] = show(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KindMap:
		parts := make([]string, len(v.Keys))
		for i, k := range v.Keys {
			parts[i] = k + ": " + show(v.Vals[i])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return "?"
}

func TestParseYAMLTable(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"empty", "", "{}"},
		{"only comments", "# a\n\n# b\n", "{}"},
		{"scalars", "name: foo\ndescription: Does things", `{name: "foo", description: "Does things"}`},
		{"double quoted", `a: "x: y # not a comment"`, `{a: "x: y # not a comment"}`},
		{"single quoted with escape", `a: 'it''s'`, `{a: "it's"}`},
		{"double quote escapes", `a: "tab\there\nnew \u00e9 \x41 \" \\"`, "{a: \"tab\\there\\nnew \u00e9 A \\\" \\\\\"}"},
		{"numbers and bools stay text", "a: 12\nb: true\nc: 1.5", `{a: "12", b: "true", c: "1.5"}`},
		{"null forms", "a:\nb: ~\nc: null\nd: # only a comment", "{a: ~, b: ~, c: ~, d: ~}"},
		{"trailing comment", "a: value # note\nb: x#y", `{a: "value", b: "x#y"}`},
		{"url value", "url: http://example.com:8080/x", `{url: "http://example.com:8080/x"}`},
		{"colon inside plain value", "description: Use when: the user asks", `{description: "Use when: the user asks"}`},
		{"key with spaces and dashes", "allowed-tools: Read\nmy key: v", `{allowed-tools: "Read", my key: "v"}`},
		{"quoted key", `"a b": 1`, `{a b: "1"}`},

		{"inline list", `tools: [Read, Grep, "Bash(git diff:*)"]`, `{tools: ["Read", "Grep", "Bash(git diff:*)"]}`},
		{"empty flow list", "tools: []", "{tools: []}"},
		{"flow list trailing comma", "t: [a, b,]", `{t: ["a", "b"]}`},
		{"flow list multi-line", "t: [a,\n  b,\n  c]", `{t: ["a", "b", "c"]}`},
		{"flow map", "m: {a: 1, b: [x, y], c}", `{m: {a: "1", b: ["x", "y"], c: ~}}`},
		{"flow apostrophe inside plain", "t: [don't, stop]", `{t: ["don't", "stop"]}`},
		{"flow with comment", "t: [a, b] # c", `{t: ["a", "b"]}`},
		{"block list", "tools:\n  - Read\n  - Grep", `{tools: ["Read", "Grep"]}`},
		{"block list at key indent", "tools:\n- Read\n- Grep\nname: x", `{tools: ["Read", "Grep"], name: "x"}`},
		{"block list with quoted and pattern", "t:\n  - \"Bash(git commit:*)\"\n  - Bash(git add:*)", `{t: ["Bash(git commit:*)", "Bash(git add:*)"]}`},
		{"nested map one level", "metadata:\n  author: me\n  version: 2", `{metadata: {author: "me", version: "2"}}`},
		{"nested map deeper", "a:\n  b:\n    c: d", `{a: {b: {c: "d"}}}`},
		{"list of maps", "hooks:\n  - matcher: Bash\n    command: ./x.sh\n  - matcher: Edit\n    command: ./y.sh", `{hooks: [{matcher: "Bash", command: "./x.sh"}, {matcher: "Edit", command: "./y.sh"}]}`},
		{"claude hooks frontmatter", "hooks:\n  PreToolUse:\n    - matcher: \"Bash\"\n      hooks:\n        - type: command\n          command: \"./check.sh\"",
			`{hooks: {PreToolUse: [{matcher: "Bash", hooks: [{type: "command", command: "./check.sh"}]}]}}`},
		{"list of lists", "m:\n  - - a\n    - b\n  - - c", `{m: [["a", "b"], ["c"]]}`},
		{"dash in the middle of a scalar", "a: b - c\nd: -x", `{a: "b - c", d: "-x"}`},

		{"literal block", "d: |\n  line one\n  line two\nnext: x", `{d: "line one\nline two\n", next: "x"}`},
		{"literal strip", "d: |-\n  one\n  two\n", `{d: "one\ntwo"}`},
		{"literal keep", "d: |+\n  one\n\n\nnext: x", `{d: "one\n\n\n", next: "x"}`},
		{"folded", "d: >\n  one\n  two\n\n  three\nx: y", `{d: "one two\nthree\n", x: "y"}`},
		{"folded strip", "d: >-\n  a\n  b", `{d: "a b"}`},
		{"folded more indented", "d: >\n  a\n    b\n  c", `{d: "a\n  b\nc\n"}`},
		{"block scalar with comment", "d: | # note\n  x", `{d: "x\n"}`},
		{"block scalar explicit indent", "d: |2\n    x\n  y", `{d: "  x\ny\n"}`},
		{"block scalar tab content", "d: |\n  a\n  \tb", "{d: \"a\\n\\tb\\n\"}"},
		{"block scalar empty", "d: |\nnext: x", `{d: "", next: "x"}`},
		{"block scalar in list", "l:\n  - |\n    a\n    b\n  - c", `{l: ["a\nb\n", "c"]}`},
		{"gt that is not a block scalar", "a: > quote", `{a: "> quote"}`},
		{"pipe that is not a block scalar", "a: |x", `{a: "|x"}`},

		{"multi-line plain", "d: one\n  two\n  three\nx: y", `{d: "one two three", x: "y"}`},
		{"multi-line plain with blank", "d: one\n\n  two", `{d: "one\ntwo"}`},
		{"plain on next line", "d:\n  Long text\n  continues\nx: y", `{d: "Long text continues", x: "y"}`},
		{"multi-line double quoted", "d: \"one\n  two\n\n  three\"", `{d: "one two\nthree"}`},
		{"multi-line single quoted", "d: 'one\n  two'", `{d: "one two"}`},
		{"escaped line break", "d: \"one\\\n   two\"", `{d: "onetwo"}`},

		{"hint as flow list is text-able", "argument-hint: [message]", `{argument-hint: ["message"]}`},
		{"hint with two brackets is plain", "argument-hint: [issue-number] [priority]", `{argument-hint: "[issue-number] [priority]"}`},
		{"template braces plain", "n: {{name}}", `{n: "{{name}}"}`},
		{"description starting with bracket then text", "d: [WIP] Fix the thing", `{d: "[WIP] Fix the thing"}`},
		{"quoted then junk is plain", `d: "a" and b`, `{d: "\"a\" and b"}`},
		{"alias and anchor are plain text", "a: *.go\nb: &x y\nc: !tag z\nd: @mention\ne: `code`", "{a: \"*.go\", b: \"&x y\", c: \"!tag z\", d: \"@mention\", e: \"`code`\"}"},
		{"merge key is an ordinary key", "<<: x", `{<<: "x"}`},
		{"indented root", "  a: 1\n  b: 2", `{a: "1", b: "2"}`},
		{"blank lines and comments between", "a: 1\n\n# c\n\nb: 2", `{a: "1", b: "2"}`},
		{"list item keys with colon in value", "l:\n  - name: a\n    desc: x: y", `{l: [{name: "a", desc: "x: y"}]}`},
		{"unicode", "n: héllo 世界 😀", `{n: "héllo 世界 😀"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parseYAML(tc.src, 2)
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, tc.src)
			}
			if got := show(v); got != tc.want {
				t.Fatalf("got  %s\nwant %s\nsrc:\n%s", got, tc.want, tc.src)
			}
		})
	}
}

func TestParseYAMLErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		line int    // expected line number in the error (0 = any)
		want string // substring of the message
	}{
		{"tab indentation", "a:\n\t- b", 3, "tabs"},
		{"tab indentation in mapping", "a: 1\n\tb: 2", 3, "tabs"},
		{"duplicate key", "a: 1\nb: 2\na: 3", 4, "duplicate key \"a\""},
		{"duplicate key nested", "m:\n  x: 1\n  x: 2", 4, "duplicate key"},
		{"bad indentation", "a:\n    b: 1\n  c: 2", 4, "indentation"},
		{"bad indentation of a sibling", "a: 1\n  b: 2\n", 0, ""}, // folded into a's plain scalar, not an error
		{"unterminated double quote", "a: \"oops", 2, "unterminated"},
		{"unterminated single quote", "a: 'oops\nb: 1", 0, "unterminated"},
		{"unterminated flow", "a: [x, y\nb: 1", 0, "unterminated"},
		{"missing key colon", "just words", 2, "key: value"},
		{"list at root", "- a\n- b", 2, "not a list"},
		{"junk after mapping", "a: 1\n- x", 3, "unexpected content"},
		{"key too long", strings.Repeat("k", 200) + ": v", 2, "longer than"},
		{"bad escape", `a: "\q"`, 2, "unknown escape"},
		{"bad unicode escape", `a: "\uZZZZ"`, 2, "invalid escape"},
		{"nesting too deep", strings.Repeat("a:\n ", 12) + " b: 1", 0, "nested"},
		{"flow nesting too deep", "a: " + strings.Repeat("[", 30) + strings.Repeat("]", 30) + "\nb: [" + strings.Repeat("[", 30) + "\n", 0, ""},
		{"multi-line quoted junk after close", "a: \"x\n y\" junk", 2, "after the closing quote"},
		{"flow map duplicate keys on many lines", "a: {x: 1,\n x: 2}", 2, "duplicate key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseYAML(tc.src, 2)
			if tc.want == "" {
				return // documents behaviour that is deliberately not an error, or only must not panic
			}
			if err == nil {
				t.Fatalf("no error for:\n%s", tc.src)
			}
			var se *SyntaxError
			if !errors.As(err, &se) {
				t.Fatalf("error is %T, want *SyntaxError: %v", err, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if tc.line != 0 && se.Line != tc.line {
				t.Fatalf("error on line %d, want %d: %v", se.Line, tc.line, err)
			}
		})
	}
}

// Structural limits protect the parser from documents built to be expensive.
func TestParseYAMLLimits(t *testing.T) {
	t.Run("too many values", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < maxNodes+10; i++ {
			b.WriteString("k")
			b.WriteString(strconv.Itoa(i))
			b.WriteString(": v\n")
		}
		if _, err := parseYAML(b.String(), 2); err == nil || !strings.Contains(err.Error(), "too complex") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unclosed quote does not swallow the world", func(t *testing.T) {
		src := "a: \"" + strings.Repeat("x\n", 5000)
		if _, err := parseYAML(src, 2); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("long plain continuation is bounded", func(t *testing.T) {
		src := "a: x\n" + strings.Repeat("  more\n", maxContinue+50)
		if _, err := parseYAML(src, 2); err == nil || !strings.Contains(err.Error(), "continues over more than") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("deep list nesting", func(t *testing.T) {
		src := ""
		for i := 0; i < 20; i++ {
			src += strings.Repeat("  ", i) + "-\n"
		}
		src += strings.Repeat("  ", 20) + "x"
		if _, err := parseYAML("a:\n"+indentAll(src, 1), 2); err == nil {
			t.Fatal("expected a depth error")
		}
	})
}

func indentAll(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func TestParseYAMLLineNumbers(t *testing.T) {
	v, err := parseYAML("a: 1\nb:\n  - x\n  - y\nc: z", 2)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := map[string]int{"a": 2, "b": 3, "c": 6}
	for i, k := range v.Keys {
		if v.Vals[i].Line != wantLines[k] && k != "b" { // b's value is the list, which starts on line 4
			t.Errorf("value of %s starts on line %d, want %d", k, v.Vals[i].Line, wantLines[k])
		}
	}
	if got := v.Vals[1].Line; got != 4 {
		t.Errorf("list starts on line %d, want 4", got)
	}
}
