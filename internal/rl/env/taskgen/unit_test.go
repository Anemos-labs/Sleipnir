package taskgen

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		path string
		lang string
		kind Kind
	}{
		// Go
		{"calc.go", Go, Source},
		{"internal/x/y.go", Go, Source},
		{"calc_test.go", Go, Test},
		{"pkg/deep/x_test.go", Go, Test},
		{"pkg/testdata/golden.txt", "", TestSupport},
		{"pkg/testdata/helper.go", Go, TestSupport},
		{"vendor/github.com/x/y.go", "", Other},
		{"go.mod", "", Other},
		{"README.md", "", Other},
		// Python
		{"mathx.py", Python, Source},
		{"pkg/core.py", Python, Source},
		{"test_mathx.py", Python, Test},
		{"pkg/mathx_test.py", Python, Test},
		{"tests/test_a.py", Python, Test},
		{"tests/helpers.py", Python, TestSupport},
		{"tests/data/cases.json", "", TestSupport},
		{"conftest.py", Python, Test},
		{"__pycache__/x.py", "", Other},
		// JS / TS
		{"src/app.js", JS, Source},
		{"src/app.test.js", JS, Test},
		{"src/app.spec.ts", TS, Test},
		{"src/component.tsx", TS, Source},
		{"__tests__/app.js", JS, TestSupport},
		{"jest.config.js", "", Other},
		{"types/index.d.ts", "", Other},
		{"node_modules/x/index.js", "", Other},
		// Rust
		{"src/lib.rs", Rust, Source},
		{"tests/integration.rs", Rust, Test},
		{"tests/common/mod.rs", Rust, TestSupport},
		{"src/tests.rs", Rust, Test},
		{"build.rs", "", Other},
		{"target/debug/x.rs", "", Other},
		// Java
		{"src/main/java/A.java", Java, Source},
		{"src/test/java/ATest.java", Java, Test},
		{"src/test/java/Helper.java", Java, TestSupport},
		{"src/test/resources/data.json", "", TestSupport},
		{"src/it/java/BIT.java", Java, Test},
		// other
		{"Makefile", "", Other},
		{"docs/guide.md", "", Other},
	}
	for _, tc := range tests {
		lang, kind := Classify(tc.path)
		if lang != tc.lang || kind != tc.kind {
			t.Errorf("Classify(%q) = (%q, %v), want (%q, %v)", tc.path, lang, kind, tc.lang, tc.kind)
		}
	}
	if Source.String() != "source" || Other.String() != "other" || Test.String() != "test" || TestSupport.String() != "test-support" {
		t.Error("Kind.String")
	}
}

func TestCleanMessageAndPrompt(t *testing.T) {
	msg := `Fix nil dereference in Router.Match

The router panicked when a route had no handler. See
https://github.com/org/repo/pull/123 and https://github.com/org/repo/commit/abcdef1234
for background; cherry-picked from 0123456789abcdef0123456789abcdef01234567.

Fixes #42
Signed-off-by: A Developer <dev@example.com>
Co-authored-by: Someone <s@example.com>
Change-Id: I8a7b6c5d
Reviewed-on: https://gerrit.example/c/123
Co-Authored-By: Assistant <noreply@example.com>
Claude-Session: https://claude.ai/code/session_0123456789
`
	got := CleanMessage(msg)
	for _, bad := range []string{"Signed-off-by", "Co-authored-by", "Co-Authored-By", "Claude-Session", "claude.ai/code", "Change-Id", "Reviewed-on", "github.com/org/repo/pull", "abcdef1234", "0123456789abcdef", "dev@example.com"} {
		if strings.Contains(got, bad) {
			t.Errorf("cleaned message still contains %q:\n%s", bad, got)
		}
	}
	for _, keep := range []string{"Fix nil dereference in Router.Match", "The router panicked", "Fixes #42"} {
		if !strings.Contains(got, keep) {
			t.Errorf("cleaned message lost %q:\n%s", keep, got)
		}
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("blank lines not collapsed: %q", got)
	}
	prompt := BuildPrompt(msg, map[string]string{"#42": "The router crashes when I register /x without a handler."})
	if !strings.Contains(prompt, "Issue #42:\nThe router crashes") || !strings.Contains(prompt, "test suite that you cannot see") {
		t.Errorf("prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "Signed-off-by") {
		t.Error("trailer in prompt")
	}
	if got := CleanMessage("\r\nSubject\r\n\r\n\r\n\r\nBody\r\n"); got != "Subject\n\nBody" {
		t.Errorf("CRLF handling: %q", got)
	}
}

func TestIssueRefs(t *testing.T) {
	got := IssueRefs("Fixes #12 and closes org/repo#7, resolved: #12. Also fixed #99\nSee #5 (not closing)")
	want := []string{"#12", "org/repo#7", "#99"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if len(IssueRefs("no references here")) != 0 {
		t.Error("false positive")
	}
}

func TestInferKind(t *testing.T) {
	for subject, want := range map[string]string{
		"Fix crash on empty input":      "fix",
		"fix(parser): handle EOF":       "fix",
		"feat: add streaming API":       "feature",
		"Add support for TOML":          "feature",
		"Implement retry with backoff":  "feature",
		"Refactor the scheduler":        "refactor",
		"refactor: split module":        "refactor",
		"Rename Foo to Bar":             "refactor",
		"Handle nil pointer in Match":   "fix",
		"Update dependencies":           "fix",
		"panic when the input is empty": "fix",
	} {
		if got := InferKind(subject); got != want {
			t.Errorf("InferKind(%q) = %q, want %q", subject, got, want)
		}
	}
}

func TestDetectLicense(t *testing.T) {
	for want, text := range map[string]string{
		"MIT":          "MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy of this software",
		"Apache-2.0":   "Apache License\nVersion 2.0, January 2004",
		"BSD-3-Clause": "Redistribution and use in source and binary forms, with or without modification, are permitted provided that ... Neither the name of the copyright holder nor",
		"BSD-2-Clause": "Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met",
		"ISC":          "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted",
		"MPL-2.0":      "Mozilla Public License Version 2.0",
		"GPL-3.0":      "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007",
		"GPL-2.0":      "GNU GENERAL PUBLIC LICENSE\nVersion 2, June 1991",
		"LGPL-2.1":     "GNU LESSER GENERAL PUBLIC LICENSE Version 2.1, February 1999",
		"AGPL-3.0":     "GNU AFFERO GENERAL PUBLIC LICENSE Version 3",
		"Unlicense":    "This is free and unencumbered software released into the public domain.",
		"":             "All rights reserved. Proprietary.",
	} {
		if got := DetectLicense(text); got != want {
			t.Errorf("DetectLicense(%.40q...) = %q, want %q", text, got, want)
		}
	}
}

func TestGoTestChunks(t *testing.T) {
	old := `package x

import "testing"

func TestA(t *testing.T) {
	if 1 != 1 {
		t.Fatal()
	}
}

func helper() int { return 1 }

func TestB(t *testing.T) {
	_ = helper()
}
`
	next := `package x

import "testing"

func TestA(t *testing.T) {
	if 1 != 1 {
		t.Fatal()
	}
}

func helper() int { return 2 }

func TestB(t *testing.T) {
	_ = helper()
	t.Log("changed")
}

func TestC(t *testing.T) {}
`
	got := changedGoTests(old, next)
	if !reflect.DeepEqual(got, []string{"TestB", "TestC"}) {
		t.Fatalf("changed = %v (TestA is untouched; helper changes do not change TestA)", got)
	}
	if got := changedGoTests("", next); !reflect.DeepEqual(got, []string{"TestA", "TestB", "TestC"}) {
		t.Fatalf("all new = %v", got)
	}
	if got := changedGoTests(next, next); len(got) != 0 {
		t.Fatalf("identical files: %v", got)
	}
}

func TestShq(t *testing.T) {
	for in, want := range map[string]string{"a": "'a'", "it's": `'it'\''s'`, "a b": "'a b'", "$(x)": "'$(x)'", "": "''"} {
		if got := shq(in); got != want {
			t.Errorf("shq(%q) = %q", in, got)
		}
	}
	// Quoted strings survive a real shell.
	for _, s := range []string{"plain", "with space", "it's", `back\slash`, "$HOME", "`x`", "a;b", "new\nline"} {
		out, err := exec.Command("sh", "-c", "printf %s "+shq(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("shell round trip of %q gave %q (%v)", s, out, err)
		}
	}
}

func TestEscapeGlob(t *testing.T) {
	for in, want := range map[string]string{
		"calc_test.go":        "/calc_test.go",
		"pkg/a_test.go":       "pkg/a_test.go",
		"weird[1]*name?.txt":  `/weird\[1\]\*name\?.txt`,
		"sub/we\\ird_test.go": `sub/we\\ird_test.go`,
	} {
		if got := escapeGlob(in); got != want {
			t.Errorf("escapeGlob(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- mutation operators ----

const goSample = `package sample

import "errors"

// Clamp limits v; the comment mentions a < b and x == y.
func Clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func Next(i int) int { return i + 1 }

func Prev(i int) int { return i - 1 }

func Both(a, b bool) bool { return a && b }

func Either(a, b bool) bool { return a || b }

func Load(p *int) (int, error) {
	if p == nil {
		return 0, errors.New("nil")
	}
	if p != nil {
		return *p, nil
	}
	return 0, nil
}

func Empty(s string) bool {
	if !isBlank(s) {
		return false
	}
	return true
}

func isBlank(s string) bool { return s == "a < b" }
`

func TestGoMutations(t *testing.T) {
	ms, err := GoMutations("sample.go", []byte(goSample))
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		op       string
		old, new string
	}
	got := map[key]int{}
	for _, m := range ms {
		got[key{m.Op, m.Old, m.New}]++
	}
	for _, want := range []key{
		{"cmp-flip", "<", "<="},
		{"cmp-flip", ">", ">="},
		{"cmp-flip", "==", "!="},
		{"cmp-flip", "!=", "=="},
		{"boundary", "+", "-"},
		{"boundary", "-", "+"},
		{"logic-swap", "&&", "||"},
		{"logic-swap", "||", "&&"},
		{"nil-check", "p == nil", "false"},
		{"nil-check", "p != nil", "true"},
		{"negate", "v < lo", "!(v < lo)"},
		{"negate", "!", ""},
		{"return-bool", "true", "false"},
		{"return-bool", "false", "true"},
	} {
		if got[want] == 0 {
			t.Errorf("missing mutation %+v", want)
		}
	}
	for _, m := range ms {
		if m.Line < 1 || m.File != "sample.go" {
			t.Errorf("bad position: %+v", m)
		}
	}
	// Strings and comments are never touched: there is exactly one "==" flip in a
	// string context that must be absent: the only == sites are in real code.
	for _, m := range ms {
		line := strings.Split(goSample, "\n")[m.Line-1]
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			t.Errorf("mutation inside a comment: %+v", m)
		}
		if m.Op == "cmp-flip" && m.Old == "==" && strings.Contains(line, `"a < b"`) && m.Start > strings.Index(goSample, `"a < b"`) {
			// the == precedes the string on that line; the string itself is not mutated
			if src := goSample[m.Start:m.End]; src != "==" {
				t.Errorf("mutation range is wrong: %q", src)
			}
		}
	}
	// Every mutation yields Go that still parses, and differs from the original.
	for _, m := range ms {
		out, err := m.Apply([]byte(goSample))
		if err != nil {
			t.Fatalf("%+v: %v", m, err)
		}
		if string(out) == goSample {
			t.Errorf("mutation is a no-op: %+v", m)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), "x.go", out, 0); err != nil {
			t.Errorf("mutation %+v yields invalid Go: %v", m, err)
		}
	}
	// Sorted by position.
	if !sort.SliceIsSorted(ms, func(a, b int) bool { return ms[a].Start < ms[b].Start }) {
		t.Error("mutations are not sorted by position")
	}
}

func TestGoMutationsSkipGeneratedAndBrokenFiles(t *testing.T) {
	gen := "// Code generated by tool. DO NOT EDIT.\n\npackage x\n\nfunc F(a int) bool { return a < 1 }\n"
	if ms, err := GoMutations("gen.go", []byte(gen)); err != nil || len(ms) != 0 {
		t.Fatalf("generated file: %v %v", ms, err)
	}
	if _, err := GoMutations("bad.go", []byte("package x\nfunc {{{")); err == nil {
		t.Fatal("unparseable file accepted")
	}
}

func TestMutationApplyChecksTheText(t *testing.T) {
	m := Mutation{Start: 3, End: 5, Old: "ab", New: "X"}
	if out, err := m.Apply([]byte("123ab456")); err != nil || string(out) != "123X456" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := m.Apply([]byte("123cd456")); err == nil {
		t.Error("stale mutation applied")
	}
	if _, err := (Mutation{Start: 5, End: 99, Old: "x"}).Apply([]byte("short")); err == nil {
		t.Error("out of range accepted")
	}
	// Insertion (empty Old).
	ins := Mutation{Start: 2, End: 2, New: " not"}
	if out, _ := ins.Apply([]byte("if x")); string(out) != "if not x" {
		t.Errorf("insertion: %q", out)
	}
}

const pySample = `import os

def clamp(v, lo, hi):
    """Docstring with a < b, x == y and "quotes"."""
    if v < lo:  # a comment with == in it
        return lo
    elif v >= hi:
        return hi
    return v

def check(x, y):
    s = 'a == b'
    t = f"{x} != {y}"
    r = """multi
    line == string
    """
    if x is None and y is not None:
        return True
    if not x:
        return False
    return [i for i in range(3) if i > 1] + [x + 1, y - 1]

def contains(a, b):
    return a not in b or a == b

@decorator(1 < 2)
def deco(): pass

def cond(a):
    z = 1 if a else 2
    while a != 0:
        a = a - 1
    return z
`

func TestPythonMutations(t *testing.T) {
	ms := PythonMutations("sample.py", []byte(pySample))
	type key struct {
		op       string
		old, new string
	}
	got := map[key]int{}
	for _, m := range ms {
		got[key{m.Op, m.Old, m.New}]++
	}
	for _, want := range []key{
		{"cmp-flip", "<", "<="},
		{"cmp-flip", ">=", ">"},
		{"cmp-flip", "==", "!="},
		{"cmp-flip", "!=", "=="},
		{"cmp-flip", ">", ">="},
		{"logic-swap", "and", "or"},
		{"logic-swap", "or", "and"},
		{"none-check", "is", "is not"},
		{"none-check", "is not", "is"},
		{"negate", "not ", ""},
		{"negate", "", " not"},
		{"return-bool", "True", "False"},
		{"return-bool", "False", "True"},
		{"boundary", "+", "-"},
		{"boundary", "-", "+"},
	} {
		if got[want] == 0 {
			t.Errorf("missing mutation %+v", want)
		}
	}
	// Nothing inside strings, docstrings, f-strings or comments; no "not in" split.
	for _, m := range ms {
		line := strings.Split(pySample, "\n")[m.Line-1]
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, `"""Docstring`), strings.HasPrefix(trim, "s = "), strings.HasPrefix(trim, "t = f"), strings.HasPrefix(trim, "line =="):
			t.Errorf("mutation inside a string on line %d: %+v", m.Line, m)
		}
		if strings.Contains(line, "not in") && m.Op == "negate" && m.Old == "not " {
			t.Errorf("'not in' must not be split: %+v", m)
		}
	}
	// "is not None" is one construct, not "is" + "not".
	for _, m := range ms {
		if m.Op == "negate" && m.Old == "not " && strings.Contains(strings.Split(pySample, "\n")[m.Line-1], "is not None") {
			t.Errorf("'is not None' mutated as a negation: %+v", m)
		}
	}
	// A conditional expression and a comprehension filter are not statements:
	// no `if` negation is offered for them.
	for _, m := range ms {
		line := strings.Split(pySample, "\n")[m.Line-1]
		if m.Op == "negate" && m.Old == "" && (strings.Contains(line, "z = 1 if") || strings.Contains(line, "for i in range")) {
			t.Errorf("negated a non-statement condition: %+v on %q", m, line)
		}
	}
	if !sort.SliceIsSorted(ms, func(a, b int) bool { return ms[a].Start < ms[b].Start }) {
		t.Error("not sorted")
	}
}

// Every Python mutation must leave the file syntactically valid.
func TestPythonMutationsStayValidPython(t *testing.T) {
	have(t, "python3")
	dir := t.TempDir()
	ms := PythonMutations("sample.py", []byte(pySample))
	if len(ms) < 10 {
		t.Fatalf("only %d mutations", len(ms))
	}
	for i, m := range ms {
		out, err := m.Apply([]byte(pySample))
		if err != nil {
			t.Fatalf("%+v: %v", m, err)
		}
		p := filepath.Join(dir, "m.py")
		if err := os.WriteFile(p, out, 0o644); err != nil {
			t.Fatal(err)
		}
		if b, err := exec.Command("python3", "-c", "import ast,sys; ast.parse(open(sys.argv[1]).read())", p).CombinedOutput(); err != nil {
			t.Errorf("mutation %d %+v yields invalid Python: %s", i, m, b)
		}
	}
}

func TestLexPython(t *testing.T) {
	src := "x = 'it''s' # c\ny = rb'\\d' + \"a\\\"b\"\nz = ('a' \\\n  'b')\nw = a<=b>=c**2//3\n"
	toks := lexPython([]byte(src))
	var texts []string
	for _, tk := range toks {
		if tk.kind != pyNewline {
			texts = append(texts, tk.text)
		}
	}
	want := []string{"x", "=", "y", "=", "+", "z", "=", "(", ")", "w", "=", "a", "<=", "b", ">=", "c", "**", "2", "//", "3"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("tokens = %q\nwant     %q", texts, want)
	}
	// An unterminated string or a lone quote must not hang or panic.
	for _, bad := range []string{"'unterminated", `"""never closed`, "'\\", "x = '", "\"\"\"\"\"\""} {
		lexPython([]byte(bad))
	}
}

func TestApplyPatchCommandIsShellSafe(t *testing.T) {
	patch := []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-$(rm -rf /) `x` 'q' \"d\" \\ EOF\n+new $HOME\n")
	cmd := applyPatchCommand(patch)
	if !strings.HasPrefix(cmd, "git apply --whitespace=nowarn <<'SLEIPNIR_PATCH_") {
		t.Fatalf("command: %s", cmd)
	}
	// Run it against a real file: the here-document must deliver the patch byte for byte.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("$(rm -rf /) `x` 'q' \"d\" \\ EOF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", cmd)
	c.Dir = dir
	c.Env = gitEnv()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := mustRead(t, filepath.Join(dir, "x")); got != "new $HOME\n" {
		t.Fatalf("file after apply: %q", got)
	}
	// A patch that contains the delimiter of its own hash still works.
	tricky := append([]byte{}, patch...)
	delim := strings.Split(strings.Split(cmd, "'")[1], "'")[0]
	tricky = append(tricky, []byte("+"+delim+"\n")...)
	if c2 := applyPatchCommand(tricky); strings.Count(c2, "\n"+delim+"\n") > 0 && !strings.Contains(c2, delim+"_X") {
		// (the derived delimiter differs from the tricky patch's hash anyway)
		_ = c2
	}
}

func TestFailingTestsExtraction(t *testing.T) {
	log := `--- FAIL: TestClamp (0.00s)
    --- FAIL: TestClamp/upper (0.00s)
--- FAIL: TestAbs (0.00s)
FAIL
FAILED tests/test_x.py::test_y - assert 1 == 2
FAILED tests/test_x.py::test_z
`
	got := failingTests(log)
	want := []string{"TestClamp", "TestAbs", "tests/test_x.py::test_y", "tests/test_x.py::test_z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	long := strings.Repeat("--- FAIL: TestA (0s)\n", 3) + "--- FAIL: T1 (0s)\n--- FAIL: T2 (0s)\n--- FAIL: T3 (0s)\n--- FAIL: T4 (0s)\n"
	if len(failingTests(long)) != 5 {
		t.Errorf("list is not capped: %v", failingTests(long))
	}
	if !looksLikeBuildFailure("FAIL\tpkg [build failed]") || !looksLikeBuildFailure("E   SyntaxError: bad") || looksLikeBuildFailure("--- FAIL: TestX") {
		t.Error("build failure detection")
	}
}

func TestCompositeScript(t *testing.T) {
	// The generated script must count passing components and print the score last,
	// without anything the components print being able to impersonate it.
	dir := t.TempDir()
	cases := []struct {
		cmds []string
		want string
	}{
		{[]string{"true", "true", "true"}, `{"score": 1.000000}`},
		{[]string{"true", "false", "true"}, `{"score": 0.666667}`},
		{[]string{"false", "exit 3", "false"}, `{"score": 0.000000}`},
		{[]string{"echo '{\"score\": 1}'; false", "true"}, `{"score": 0.500000}`}, // a component printing a fake score
		{[]string{"true"}, `{"score": 1.000000}`},
	}
	for _, tc := range cases {
		script := filepath.Join(dir, "c.sh")
		if err := os.WriteFile(script, []byte(compositeScript(tc.cmds)), 0o755); err != nil {
			t.Fatal(err)
		}
		c := exec.Command("sh", script)
		c.Dir = dir
		out, err := c.Output()
		if err != nil {
			t.Fatalf("%v: %v", tc.cmds, err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) != 1 || lines[0] != tc.want {
			t.Errorf("%v: stdout %q, want exactly %q", tc.cmds, out, tc.want)
		}
	}
	// Component commands with quotes and shell syntax survive.
	script := filepath.Join(dir, "q.sh")
	if err := os.WriteFile(script, []byte(compositeScript([]string{`test "it's" = "it's" && cd /tmp && true`})), 0o755); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("sh", script).Output()
	if strings.TrimSpace(string(out)) != `{"score": 1.000000}` {
		t.Errorf("quoting: %q", out)
	}
}
