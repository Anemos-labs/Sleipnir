package mdfile

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"one", []string{"one"}},
		{"one two  three", []string{"one", "two", "three"}},
		{`"two words" x`, []string{"two words", "x"}},
		{`'single quoted $1' x`, []string{"single quoted $1", "x"}},
		{`a\ b c`, []string{"a b", "c"}},
		{`"" x`, []string{"", "x"}},
		{`x ""`, []string{"x", ""}},
		{`"unterminated quote`, []string{"unterminated quote"}},
		{`'unterminated`, []string{"unterminated"}},
		{`say "he said \"hi\""`, []string{"say", `he said "hi"`}},
		{`"a\nb"`, []string{`a\nb`}},
		{"tab\tseparated\nand newline", []string{"tab", "separated", "and", "newline"}},
		{`trailing\`, []string{`trailing\`}},
		{`a"b c"d`, []string{"ab cd"}},
	}
	for _, tc := range tests {
		if got := SplitArgs(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":           "''",
		"plain":      "'plain'",
		"a b":        "'a b'",
		"it's":       `'it'\''s'`,
		"; rm -rf ~": "'; rm -rf ~'",
		"$(id)":      "'$(id)'",
		"`id`":       "'`id`'",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubstitute(t *testing.T) {
	tests := []struct {
		name, tmpl, args string
		mode             ArgMode
		want             string
		used             bool
	}{
		{"no dollar", "plain text", "x y", ArgsText, "plain text", false},
		{"all arguments", "Fix $ARGUMENTS now", "issue 42 in db", ArgsText, "Fix issue 42 in db now", true},
		{"arguments are trimmed", "[$ARGUMENTS]", "  x y  ", ArgsText, "[x y]", true},
		{"positional", "$1 then $2", "first second third", ArgsText, "first then second", true},
		{"quoted positional", "$1|$2", `"a b" c`, ArgsText, "a b|c", true},
		{"missing positional is empty", "[$1][$2]", "only", ArgsText, "[only][]", true},
		{"indexed", "$ARGUMENTS[0] $ARGUMENTS[1] $ARGUMENTS[2]", "a b", ArgsText, "a b ", true},
		{"tenth is not supported", "$10", "a b c d e f g h i j", ArgsText, "$10", false},
		{"dollar zero is literal", "$0", "a", ArgsText, "$0", false},
		{"arguments boundary", "$ARGUMENTSX and $ARGUMENTS_Y", "a", ArgsText, "$ARGUMENTSX and $ARGUMENTS_Y", false},
		{"followed by punctuation", "$ARGUMENTS.", "a", ArgsText, "a.", true},
		{"followed by a bracket that is not an index", "$ARGUMENTS[x]", "a", ArgsText, "a[x]", true},
		{"escaped digit", "cost $$1 and $1", "5", ArgsText, "cost $1 and 5", true},
		{"escaped arguments", "$$ARGUMENTS and $ARGUMENTS", "x", ArgsText, "$ARGUMENTS and x", true},
		{"double dollar elsewhere is untouched", "pid $$ and $$x", "a", ArgsText, "pid $$ and $$x", false},
		{"lone dollar", "5$ and $", "a", ArgsText, "5$ and $", false},
		{"dollar at end", "x$", "a", ArgsText, "x$", false},
		{"arguments are not re-expanded", "$1 $2", "$2 $ARGUMENTS", ArgsText, "$2 $ARGUMENTS", true},
		{"argument with a placeholder", "run $ARGUMENTS", "$1 and $ARGUMENTS", ArgsText, "run $1 and $ARGUMENTS", true},
		{"unicode", "héllo $1 世界", "wörld", ArgsText, "héllo wörld 世界", true},
		{"empty args", "a $ARGUMENTS b", "", ArgsText, "a  b", true},

		{"shell all", "git log $ARGUMENTS", "--oneline -5", ArgsShell, "git log '--oneline' '-5'", true},
		{"shell operator neutralised", "git log $1", "main; rm -rf ~", ArgsShell, "git log 'main;'", true},
		{"shell whole argument quoted", "echo $1", `"a; b"`, ArgsShell, "echo 'a; b'", true},
		{"shell substitution neutralised", "echo $ARGUMENTS", "$(id) `id`", ArgsShell, "echo '$(id)' '`id`'", true},
		{"shell quote in argument", "echo $1", `"it's"`, ArgsShell, `echo 'it'\''s'`, true},
		{"shell missing is empty", "echo $1|", "", ArgsShell, "echo |", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, used := Substitute(tc.tmpl, tc.args, tc.mode)
			if got != tc.want || used != tc.used {
				t.Fatalf("Substitute(%q, %q) = %q, %v; want %q, %v", tc.tmpl, tc.args, got, used, tc.want, tc.used)
			}
		})
	}
}

func TestCleanArgs(t *testing.T) {
	got, err := CleanArgs("  hello\u200B world\x00\r\nline2  ")
	if err != nil || got != "hello world\nline2" {
		t.Fatalf("CleanArgs = %q, %v", got, err)
	}
	if _, err := CleanArgs(strings.Repeat("x", MaxArgsBytes+1)); err == nil {
		t.Fatal("over-long arguments must be rejected")
	}
	if got, err := CleanArgs(strings.Repeat("x", MaxArgsBytes)); err != nil || len(got) != MaxArgsBytes {
		t.Fatalf("arguments at the limit: %d, %v", len(got), err)
	}
}
