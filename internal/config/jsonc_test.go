package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *node {
	t.Helper()
	n, _, err := parseJSONC("t.json", []byte(src))
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return n
}

func TestParseAcceptsCommentsAndTrailingCommas(t *testing.T) {
	src := `// leading comment
{
  /* block
     comment */
  "url": "https://example.com/a//b", // slashes inside strings are not comments
  "note": "/* not a comment */",
  "list": [1, 2, 3,],   // trailing comma in a list
  "obj": {"a": true, "b": null,},
}
// trailing comment`
	n := mustParse(t, src)
	v := toValue(n).(map[string]any)
	if v["url"] != "https://example.com/a//b" || v["note"] != "/* not a comment */" {
		t.Fatalf("strings were mangled: %#v", v)
	}
	if got := v["list"].([]any); len(got) != 3 || got[2] != json.Number("3") {
		t.Fatalf("list = %#v", got)
	}
	obj := v["obj"].(map[string]any)
	if obj["a"] != true {
		t.Fatalf("obj = %#v", obj)
	}
	if b, present := obj["b"]; !present || b != nil {
		t.Fatalf("null must be present as nil: %#v", obj)
	}
}

func TestParseEmptyAndCommentOnlyDocumentsAreEmptyObjects(t *testing.T) {
	for _, src := range []string{"", "   \n\t", "// nothing here\n", "/* x */", "\xef\xbb\xbf", "\xef\xbb\xbf// bom then comment\n"} {
		n := mustParse(t, src)
		if n.kind != nObject || len(n.keys) != 0 {
			t.Errorf("%q: got %v", src, n.kind)
		}
	}
}

func TestParseHandlesBOM(t *testing.T) {
	n := mustParse(t, "\xef\xbb\xbf{\"a\": 1}")
	if v := toValue(n).(map[string]any); v["a"] != json.Number("1") {
		t.Fatalf("v = %#v", v)
	}
	// Columns on the first line do not count the invisible BOM.
	_, _, err := parseJSONC("t.json", []byte("\xef\xbb\xbf{x}"))
	var is Issue
	if !errors.As(err, &is) || is.Line != 1 || is.Col != 2 {
		t.Fatalf("err = %v (%+v)", err, is)
	}
}

func TestParseNumbersKeepTheirText(t *testing.T) {
	n := mustParse(t, `{"a": 12345678901234567890, "b": -0.5e+3, "c": 0, "d": 1E2}`)
	v := toValue(n).(map[string]any)
	want := map[string]string{"a": "12345678901234567890", "b": "-0.5e+3", "c": "0", "d": "1E2"}
	for k, w := range want {
		if got := v[k].(json.Number).String(); got != w {
			t.Errorf("%s = %s, want %s", k, got, w)
		}
	}
}

func TestParseStringEscapes(t *testing.T) {
	n := mustParse(t, `{"s": "tab\there \"quoted\" back\\slash \/ nl\n \u00e9 \ud83d\ude00 \u0041"}`)
	got := toValue(n).(map[string]any)["s"].(string)
	want := "tab\there \"quoted\" back\\slash / nl\n \u00e9 \U0001F600 A"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// A lone surrogate becomes the replacement character rather than an error or garbage.
	n = mustParse(t, `{"s": "x\ud83dy"}`)
	if s := toValue(n).(map[string]any)["s"].(string); s != "x\uFFFDy" {
		t.Fatalf("lone surrogate: %q", s)
	}
}

func TestParseErrorsAreLocatedAndHelpful(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		line, col int
		contains  string
	}{
		{"missing comma", "{\n  \"a\": 1\n  \"b\": 2\n}", 3, 3, "missing ',' between members"},
		{"missing comma in list", "{\"a\": [1 2]}", 1, 10, "missing ',' between list items"},
		{"unquoted key", "{\n  model: \"x\"\n}", 2, 3, "keys must be double-quoted"},
		{"single quotes", "{\"a\": 'x'}", 1, 7, "double quotes"},
		{"unclosed object", "{\n  \"a\": {\n    \"b\": 1\n", 4, 1, "the object ({) opened on line 2 is never closed"},
		{"unclosed list", "{\"a\": [1, 2", 1, 12, "the list ([) opened on line 1 is never closed"},
		{"unterminated string", "{\"a\": \"abc\n}", 1, 7, "never closed"},
		{"unterminated block comment", "{\n/* oops\n\"a\": 1}", 2, 1, "/* comment is never closed"},
		{"python literal", "{\"a\": True}", 1, 7, "lower case"},
		{"None", "{\"a\": None}", 1, 7, "use null"},
		{"NaN", "{\"a\": NaN}", 1, 7, "finite"},
		{"bare word", "{\"a\": hello}", 1, 7, "double quotes"},
		{"leading zero", "{\"a\": 012}", 1, 7, "leading zeros"},
		{"bad number", "{\"a\": 1.}", 1, 7, "decimal point"},
		{"bad exponent", "{\"a\": 1e}", 1, 7, "exponent"},
		{"number then word", "{\"a\": 12abc}", 1, 7, "invalid number"},
		{"trailing garbage", "{\"a\": 1} x", 1, 10, "after the end"},
		{"second document", "{} {}", 1, 4, "after the end"},
		{"missing colon", "{\"a\" 1}", 1, 6, "expected ':' after the key \"a\""},
		{"non-breaking space", "{\"a\":\u00a01}", 1, 6, "non-breaking space"},
		{"curly quotes", "{\u201ca\u201d: 1}", 1, 2, "curly quote"},
		{"invalid escape", "{\"a\": \"\\q\"}", 1, 8, "invalid escape sequence"},
		{"bad unicode escape", "{\"a\": \"\\u12\"}", 1, 8, "four hexadecimal digits"},
		{"raw control char", "{\"a\": \"x\ty\"}", 1, 9, "control character"},
		{"hash comment", "{\n# comment\n}", 2, 1, "expected a double-quoted key or '}'"},
		{"eof after key", "{\"a\":", 1, 6, "value was expected"},
		{"multibyte column", "{\"caf\u00e9\": x}", 1, 10, "double quotes"},
		{"second line multibyte", "{\n\"\u00e9\u00e9\": 1 2}", 2, 9, "expected ',' or '}'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseJSONC("cfg.json", []byte(tc.src))
			var is Issue
			if !errors.As(err, &is) {
				t.Fatalf("want an Issue error, got %v", err)
			}
			if is.Source != "cfg.json" || is.Line != tc.line || is.Col != tc.col {
				t.Errorf("position = %s:%d:%d, want cfg.json:%d:%d (%s)", is.Source, is.Line, is.Col, tc.line, tc.col, is.Message)
			}
			if !strings.Contains(is.Message, tc.contains) {
				t.Errorf("message %q does not contain %q", is.Message, tc.contains)
			}
			if !strings.HasPrefix(err.Error(), "cfg.json:") {
				t.Errorf("Error() = %q should start with the file", err.Error())
			}
		})
	}
}

func TestParseInvalidUTF8(t *testing.T) {
	_, _, err := parseJSONC("t.json", []byte("{\"a\": \"x\xffy\"}"))
	if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseDuplicateKeysWarnAndLastWins(t *testing.T) {
	n, warns, err := parseJSONC("t.json", []byte("{\n  \"a\": 1,\n  \"a\": 2\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if v := toValue(n).(map[string]any)["a"]; v != json.Number("2") {
		t.Fatalf("a = %v", v)
	}
	if len(warns) != 1 || warns[0].Severity != SeverityWarning || warns[0].Line != 3 || !strings.Contains(warns[0].Message, "duplicate key") {
		t.Fatalf("warnings = %+v", warns)
	}
}

func TestParseNestingIsBounded(t *testing.T) {
	deep := strings.Repeat("[", maxNesting+5) + strings.Repeat("]", maxNesting+5)
	_, _, err := parseJSONC("t.json", []byte(deep))
	if err == nil || !strings.Contains(err.Error(), "nesting is deeper") {
		t.Fatalf("err = %v", err)
	}
	ok := strings.Repeat("[", maxNesting) + strings.Repeat("]", maxNesting)
	if _, _, err := parseJSONC("t.json", []byte(ok)); err != nil {
		t.Fatalf("depth %d must be accepted: %v", maxNesting, err)
	}
}

func TestParseTrailingCommaOnlyWhereAllowed(t *testing.T) {
	for _, src := range []string{`{,}`, `[,]`, `{"a": 1,,}`, `[1,,2]`} {
		if _, _, err := parseJSONC("t.json", []byte(src)); err == nil {
			t.Errorf("%q should be rejected", src)
		}
	}
}

func TestLocateFindsNodesByPath(t *testing.T) {
	src := "{\n  \"a\": {\n    \"list\": [10, {\"x\": 5}]\n  }\n}"
	n := mustParse(t, src)
	val, keyOff, ok := n.locate([]string{"a", "list", "[1]", "x"})
	if !ok || val.kind != nNumber {
		t.Fatalf("locate = %v %v", val, ok)
	}
	if line, col := lineCol([]byte(src), keyOff); line != 3 || col != 19 {
		t.Fatalf("key of x at %d:%d", line, col)
	}
	if _, _, ok := n.locate([]string{"a", "nope"}); ok {
		t.Fatal("missing path must not locate")
	}
	if _, _, ok := n.locate([]string{"a", "list", "[9]"}); ok {
		t.Fatal("index out of range must not locate")
	}
}

func TestNodeFromValueRoundTrips(t *testing.T) {
	n, err := nodeFromValue(map[string]any{"a": []string{"x", "y"}, "b": map[string]any{"c": 1.5, "d": nil}, "e": true})
	if err != nil {
		t.Fatal(err)
	}
	v := toValue(n).(map[string]any)
	if v["e"] != true || v["a"].([]any)[1] != "y" || v["b"].(map[string]any)["c"] != json.Number("1.5") {
		t.Fatalf("v = %#v", v)
	}
	if _, err := nodeFromValue(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("unencodable values must error")
	}
}
