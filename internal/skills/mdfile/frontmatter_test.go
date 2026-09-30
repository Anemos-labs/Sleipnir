package mdfile

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSplitsFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		format   string
		meta     string
		body     string
		bodyLine int
	}{
		{"yaml", "---\nname: x\n---\nBody here\n", "yaml", `{name: "x"}`, "Body here\n", 4},
		{"empty frontmatter", "---\n---\nBody", "yaml", "{}", "Body", 3},
		{"closing dots", "---\na: 1\n...\nBody", "yaml", `{a: "1"}`, "Body", 4},
		{"trailing spaces on fences", "---  \na: 1\n---\t\nBody", "yaml", `{a: "1"}`, "Body", 4},
		{"blank line before body is kept", "---\na: 1\n---\n\nBody", "yaml", `{a: "1"}`, "\nBody", 4},
		{"frontmatter only", "---\na: 1\n---", "yaml", `{a: "1"}`, "", 4},
		{"no frontmatter", "Just a body\n---\nnot: frontmatter\n---\n", "", "{}", "Just a body\n---\nnot: frontmatter\n---\n", 1},
		{"four dashes are not a fence", "----\na: 1\n----\n", "", "{}", "----\na: 1\n----\n", 1},
		{"dashes with text are not a fence", "--- x\na: 1\n---\n", "", "{}", "--- x\na: 1\n---\n", 1},
		{"json", "{\"name\": \"x\", \"n\": 3, \"ok\": true, \"tools\": [\"a\", \"b\"]}\nBody", "json", `{name: "x", n: "3", ok: "true", tools: ["a", "b"]}`, "Body", 2},
		{"json multi-line", "{\n  \"name\": \"x\"\n}\n\nBody", "json", `{name: "x"}`, "\nBody", 4},
		{"json null", `{"a": null}`, "json", "{a: ~}", "", 2},
		{"empty", "", "", "{}", "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Parse(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if d.Format != tc.format {
				t.Errorf("format = %q, want %q", d.Format, tc.format)
			}
			if got := show(d.Meta); got != tc.meta {
				t.Errorf("meta = %s, want %s", got, tc.meta)
			}
			if d.Body != tc.body {
				t.Errorf("body = %q, want %q", d.Body, tc.body)
			}
			if d.BodyLine != tc.bodyLine {
				t.Errorf("body line = %d, want %d", d.BodyLine, tc.bodyLine)
			}
		})
	}
}

func TestParseFrontmatterErrors(t *testing.T) {
	tests := []struct {
		name, src, want string
		line            int
	}{
		{"never closed", "---\nname: x\nBody with no fence\n", "never closed", 1},
		{"only the opener", "---", "never closed", 1},
		{"opener and newline", "---\n", "never closed", 1},
		{"yaml error carries the file line", "---\nname: x\nname: y\n---\nBody", "duplicate key", 3},
		{"too long", "---\n" + strings.Repeat("a: 1\n", maxFrontmatterLines+5) + "---\n", "longer than", 1},
		{"oversized", "---\n" + strings.Repeat("#"+strings.Repeat("x", 200)+"\n", 300) + "---\n", "longer than", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			var se *SyntaxError
			if !errors.As(err, &se) || se.Line != tc.line {
				t.Fatalf("err = %#v, want a SyntaxError on line %d", err, tc.line)
			}
		})
	}
}

// A body that merely starts with a brace is a body, not broken frontmatter, but
// the loader is told so the author can see what happened.
func TestParseBraceThatIsNotJSON(t *testing.T) {
	for _, src := range []string{"{not json} but a prompt", "{\"a\": 1,}", "{\"a\": 1, \"a\": 2}\nbody", "{\"a\": [1, 2}", "{}x"[:1]} {
		d, err := Parse(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if d.Format != "" || d.Body != src || len(d.Notes) != 1 {
			t.Errorf("%q: got format %q body %q notes %v", src, d.Format, d.Body, d.Notes)
		}
	}
}

func TestParseJSONFrontmatterIsStrict(t *testing.T) {
	for name, src := range map[string]string{
		"array root":     "[1, 2]",
		"duplicate keys": `{"a": 1, "a": 2}`,
		"deep":           strings.Repeat(`{"a":`, 12) + "1" + strings.Repeat("}", 12),
	} {
		d, err := Parse(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Format == "json" {
			t.Errorf("%s: accepted as JSON frontmatter", name)
		}
	}
}

func TestFlowDepthAcrossLines(t *testing.T) {
	src := "a: " + strings.Repeat("[", 30) + "\n" + strings.Repeat("]", 30)
	if _, err := parseYAML(src, 2); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("err = %v", err)
	}
}

func TestFieldsAccessors(t *testing.T) {
	src := "name: demo\nallowed_tools: Read, Grep, Bash(git diff:*)\ndisable-model-invocation: yes\nmax: 12\nhint: [message]\nempty:\nnum: not-a-number\nlist:\n  - a\n  - b\nwrongbool: maybe\nmapval:\n  x: 1\nunused-key: 1"
	meta, err := parseYAML(src, 2)
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFields(meta)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := f.Str("name"); !ok || s != "demo" {
		t.Errorf("name = %q %v", s, ok)
	}
	if l, ok := f.List("allowed-tools"); !ok || strings.Join(l, "|") != "Read|Grep|Bash(git diff:*)" {
		t.Errorf("allowed-tools = %q %v", l, ok)
	}
	if b, ok := f.Bool("disableModelInvocation"); !ok || !b {
		t.Errorf("bool = %v %v", b, ok)
	}
	if n, ok := f.Int("max"); !ok || n != 12 {
		t.Errorf("int = %d %v", n, ok)
	}
	if s, ok := f.Str("hint"); !ok || s != "[message]" {
		t.Errorf("hint = %q %v", s, ok)
	}
	if _, ok := f.Str("empty"); ok {
		t.Error("an empty value must count as absent")
	}
	if l, ok := f.List("list"); !ok || strings.Join(l, "|") != "a|b" {
		t.Errorf("list = %v %v", l, ok)
	}
	if err := f.Err(); err != nil {
		t.Fatalf("no type errors so far, got %v", err)
	}
	f.Int("num")
	f.Bool("wrongbool")
	f.Str("mapval")
	f.List("name") // a string is fine for a list
	f.Str("list")  // a block list is not text
	err = f.Err()
	if err == nil {
		t.Fatal("expected type errors")
	}
	for _, want := range []string{"num: expected a whole number", "wrongbool: expected true or false", "mapval: expected text, found a mapping", "list: expected text, found a list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if got := strings.Join(f.Unused(), ","); got != "unused-key" {
		t.Errorf("unused = %q", got)
	}
}

func TestFieldsSpellingVariantsCollide(t *testing.T) {
	meta, err := parseYAML("allowed-tools: Read\nallowedTools: Write", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFields(meta); err == nil || !strings.Contains(err.Error(), "same field") {
		t.Fatalf("err = %v", err)
	}
}

func TestNormKey(t *testing.T) {
	for in, want := range map[string]string{
		"allowed-tools": "allowedtools", "allowed_tools": "allowedtools", "allowedTools": "allowedtools",
		"Disable-Model-Invocation": "disablemodelinvocation", "a b": "ab", "": "",
	} {
		if got := NormKey(in); got != want {
			t.Errorf("NormKey(%q) = %q, want %q", in, got, want)
		}
	}
}
