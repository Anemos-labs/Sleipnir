package shellparse

import (
	"strings"
	"testing"
	"time"
)

// tricky is a pile of hostile or merely odd one-liners. The plain test below
// feeds each through Parse and checks the invariants that must hold for every
// input (no panic, deterministic, Raw is a real substring, ...); several are
// also shape-checked in parseCases. The same strings seed the fuzzer.
var tricky = []string{
	// unterminated everything
	`"`, `'`, "`", `$(`, `${`, `$((`, `$'`, `$"`, `<(`, `>(`, `(`, `)`, `{`, `}`, `((`, `))`,
	`[[`, `]]`, `<<`, `<<-`, `<<<`, `>`, `>>`, `<`, `&>`, `2>`, `2>&`, `|`, `||`, `&`, `&&`, `;`, `;;`,
	`echo "`, `echo '`, "echo `", `echo $(`, `echo ${`, `echo $((1`, `echo $((1)`, `echo $'\`,
	`echo "$(`, `echo "${`, "echo \"`", `echo <(`, `cat <<EOF`, "cat <<EOF\n", "cat <<'EOF", `cat <<"EOF`,
	`$(()`, `$(())`, `$((()))`, `$(( ))`, `$( )`, "` `", `${}`, `${ }`, `$(echo)`, `$(;)`, `$(|)`, `$(&)`,
	// stray operators
	`;;;`, `&&&`, `|||`, `<<<<`, `>>>>`, `&>>&>>`, `|&|&`, `;&;&`, ` ; ; ; `, `&`, ` & `, "\n\n\n;\n",
	`a &&& b`, `a ||| b`, `a ; & b`, `a & ; b`, `a | | b`, `( ; )`, `( & )`, `{ ; }`, `{ }`, `{ } }`,
	// heredoc oddities
	"cat <<EOF\n$(\nEOF", "cat <<EOF\n`\nEOF", "cat <<EOF\n${\nEOF", "cat <<EOF\n\\\nEOF",
	"cat <<EOF\nEOF\nEOF", "cat <<E\\\nOF\nx\nEOF", "cat << EOF\nx\nEOF", "cat <<EOF;ls\nx\nEOF",
	"cat <<EOF && cat <<EOF2\na\nEOF\nb\nEOF2", "cat <<`echo EOF`\nx\nEOF", "cat <<$X\nx\n$X",
	"$(cat <<EOF\nhi\nEOF\n)", "`cat <<EOF\nhi\nEOF\n`", "cat <<EOF\n$(cat <<EOF2\nx\nEOF2\n)\nEOF",
	// nesting
	strings.Repeat("$(", 30) + "x" + strings.Repeat(")", 30),
	strings.Repeat("(", 60) + "x" + strings.Repeat(")", 60),
	strings.Repeat("{ ", 40) + "x;" + strings.Repeat(" }", 40),
	strings.Repeat("`", 41),
	strings.Repeat("\\`", 41),
	strings.Repeat("bash -c '", 1) + strings.Repeat("a; ", 200) + "'",
	`bash -c 'bash -c '\''bash -c x'\'''`,
	strings.Repeat("eval ", 40) + "x",
	strings.Repeat("sudo ", 40) + "x",
	strings.Repeat("env ", 40) + "x",
	strings.Repeat("nohup ", 40) + "x",
	strings.Repeat("a | ", 300) + "b",
	strings.Repeat("a; ", 1000),
	strings.Repeat("a && ", 500) + "b",
	strings.Repeat("echo {a,b}", 12),
	`echo {1..100}{1..100}`,
	`echo {a..z}{a..z}{a..z}`,
	`echo {{{{{{{{{{a}}}}}}}}}}`,
	`echo {,,,,,,,,,,,,,,,,,,,,,,,}`,
	`echo {-9223372036854775808..9223372036854775807}`,
	`echo {1..9223372036854775807..1}`,
	`echo {1..2..0}`, `echo {a..}`, `echo {..a}`, `echo {a..b..c}`, `echo {1..3..-1}`,
	// escapes
	`echo $'\x'`, `echo $'\u'`, `echo $'\U'`, `echo $'\c'`, `echo $'\777'`, `echo $'\xZZ'`, `echo $'é\U0001F600'`,
	`echo $'\U7fffffff'`, `echo $'\ud800'`, `echo $'\0'`, `echo $'a\0b\0c'`, `echo $'\`, `echo $'\'`,
	"echo \\\n\\\n\\\n", "echo a\\\n", "\\\n\\\n", "\\", "\\\\", `echo \\\\`,
	// expansions
	`echo $`, `echo $$`, `echo $$$`, `echo $$(`, `echo $1a`, `echo ${1}${2}`, `echo $@$*$#$?$-$!`,
	`echo ${x:-${y:-${z:-$(w)}}}`, `echo ${x/$(a)/$(b)}`, `echo ${!x}`, `echo ${#}`, `echo ${x[@]}`,
	`echo "${x:-"a"}"`, `echo "${x:-'a'}"`, `echo "$(echo "$(echo "$(echo hi)")")"`,
	`echo $((1+$(echo 2)))`, `echo $(($(echo $((1)))))`, `echo $((a)) $((b`, `echo $[1+2]`,
	`echo ~+ ~- ~0 ~root/x ~"root"`, `echo *?[a-z]`, `echo [[:alpha:]]*`, `echo !!`, `echo a!b`,
	// assignments
	`=`, `=a`, `a=`, `a==`, `a=b=c cmd`, `a+=b`, `a[1]=b cmd`, `a=(b)`, `a=(`, `a=$(b) c=$(d)`, `a='b'c"d" e`,
	`export a=b c=d`, `declare -a x=(1 2)`, `local x=$(y)`, `readonly x`,
	// wrappers with weird options
	`env`, `env -`, `env --`, `env -i`, `env -u`, `env -u x`, `env -S`, `env -C`, `env x=y`, `env x=y --`,
	`command`, `command -`, `command --`, `command -p`, `builtin`, `nohup`, `nohup --`, `time`, `time -`, `time --`,
	`time -o`, `time -f`, `exec`, `exec -a`, `nice`, `nice -n`, `nice --`, `ionice`, `ionice -c`, `timeout`,
	`timeout 1`, `timeout -s`, `timeout --`, `stdbuf`, `stdbuf -o`, `setsid`, `sudo`, `sudo -`, `sudo --`, `sudo -u`,
	`sudo -uu`, `sudo --user`, `sudo --user=`, `sudo a=b`, `doas`, `doas -u`, `su`, `su -c`, `su -c x`,
	`bash -c`, `bash -c ''`, `bash -c '' x`, `bash -o`, `bash -lc`, `bash --`, `bash -- -c x`, `sh -c "$1"`, `eval ''`,
	// unicode and control characters
	"\x00", "a\x00b", "echo \x00", "\x01\x02\x03", "\x7f", "\xff", "\xc3", "\xc3\x28", "echo \xf0\x9f",
	"echo é\u0301", "\u200b", "\ufeffls", "ls\ufeff", "ls\u2028rm", "ls\u2029rm", "\u0085", "\v\f", "ls\vrm", "ls\frm",
	"echo \u202e", "\u3000ls", "ls\u3000-la", "ｌｓ", "ｅｃｈｏ ｈｉ", "ls ；rm", "ls ｜ rm", "ls ＆ rm", "ls ＜ f",
	// long
	strings.Repeat("a", 100_000),
	strings.Repeat("a ", 50_000),
	strings.Repeat("'", 100_001),
	strings.Repeat("\"a\"", 30_000),
	strings.Repeat("$a", 30_000),
	strings.Repeat("\\ ", 30_000),
	strings.Repeat("(a)", 3000),
	strings.Repeat("a>b;", 10_000),
	strings.Repeat("<<E\n", 2000),
}

func TestTrickyNoPanicAndInvariants(t *testing.T) {
	for i, in := range tricky {
		in := in
		t.Run(name(in)+"_"+string(rune('a'+i%26)), func(t *testing.T) {
			done := make(chan Analysis, 1)
			go func() { done <- Parse(in) }()
			select {
			case a := <-done:
				checkInvariants(t, in, a)
			case <-time.After(20 * time.Second):
				t.Fatal("Parse did not finish in 20s")
			}
		})
	}
	if len(tricky)+len(parseCases) < 200 {
		t.Fatalf("expected at least 200 one-liners, have %d", len(tricky)+len(parseCases))
	}
}

func TestDeepNestingIsRefusedNotFollowed(t *testing.T) {
	in := strings.Repeat("$(", 100) + "rm -rf /" + strings.Repeat(")", 100)
	a := Parse("echo " + in)
	if a.Parsed {
		t.Fatal("100 levels of nesting should not be trusted")
	}
	if !a.HasCommandSubstitution {
		t.Error("HasCommandSubstitution should be set")
	}
	// Sanity: a modest depth is followed all the way down.
	a = Parse("echo $(echo $(echo $(echo $(rm x))))")
	last := a.Commands[len(a.Commands)-1]
	if !a.Parsed || last.Program != "rm" {
		t.Errorf("modest nesting: parsed=%v last=%q", a.Parsed, last.Program)
	}
}

func TestBraceBombIsBounded(t *testing.T) {
	start := time.Now()
	a := Parse("echo " + strings.Repeat("{a,b}", 40))
	if a.Parsed {
		t.Error("2^40 words must be refused")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("brace bomb took %v", time.Since(start))
	}
}

func TestInputLimits(t *testing.T) {
	if a := Parse(strings.Repeat("a", maxInput+1)); a.Parsed || len(a.Commands) != 0 {
		t.Error("oversized input must be refused outright")
	}
	if a := Parse("echo a\x00rm -rf /"); a.Parsed {
		t.Error("NUL must be refused")
	}
}

func TestNulInAnsiCTruncatesLikeBash(t *testing.T) {
	a := Parse(`cat $'/etc/passwd\x00/../shadow'`)
	if got := a.Commands[0].Args[0]; got != "/etc/passwd" {
		t.Errorf("arg = %q", got)
	}
}

func FuzzParse(f *testing.F) {
	for _, tc := range parseCases {
		f.Add(tc.in)
	}
	for _, in := range tricky {
		if len(in) <= 4096 {
			f.Add(in)
		}
	}
	f.Fuzz(func(t *testing.T, in string) {
		a := Parse(in)
		checkInvariants(t, in, a)
		// Any command that parsed cleanly must survive being re-quoted and
		// re-split: Fields(Join(words)) is the identity for words Parse produced.
		for _, c := range a.Commands {
			words := append([]string{c.Program}, c.Args...)
			if strings.IndexByte(strings.Join(words, ""), 0) >= 0 {
				continue
			}
			got, ok := Fields(Join(words))
			if !ok || len(got) != len(words) {
				t.Fatalf("Fields(Join(%q)) = %q, %v", words, got, ok)
			}
			for i := range words {
				if got[i] != words[i] {
					t.Fatalf("round trip changed word %d: %q -> %q", i, words[i], got[i])
				}
			}
		}
	})
}

func FuzzQuoteRoundTrip(f *testing.F) {
	for _, s := range []string{"", "a", "a b", "it's", `"`, `\`, "$(x)", "`x`", "\n", "é", "~", "*", "{a,b}", "a;b", "#", "!"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if strings.IndexByte(s, 0) >= 0 {
			t.Skip()
		}
		got, ok := Fields(Quote(s))
		if !ok || len(got) != 1 || got[0] != s {
			t.Fatalf("Fields(Quote(%q)) = %q, %v", s, got, ok)
		}
	})
}

// Quadratic behaviour on hostile input shows up as a hang, not a wrong answer:
// each of these shapes once took seconds to minutes at 100 KB. The bounds are hang
// guards, not timings: the deepest of them (nested arithmetic) takes four seconds under the race
// detector on a machine that is busy with other suites, and a bound of five failed it there.
func TestAdversarialSizeIsBounded(t *testing.T) {
	n := 50_000
	rep := strings.Repeat
	inputs := map[string]string{
		"open braces":            "echo " + rep("{", n),
		"open brace comma":       "echo " + rep("{,", n/2),
		"open brace a comma":     "echo " + rep("{a,", n/3),
		"closing braces":         "echo " + rep("}{", n/2),
		"balanced braces":        "echo " + rep("{x}", n/3),
		"nested brace groups":    "echo " + rep("{{a,b}", n/6),
		"sequence groups":        "echo " + rep("{1..9}", 100),
		"open param expansions":  "echo " + rep("${", n/2),
		"open command subst":     "echo " + rep("$(", n/2),
		"open arithmetic":        "echo " + rep("$((", n/3),
		"nested arithmetic":      "echo " + rep("$((1+", n/5) + "1" + rep("))", n/5),
		"nested param balanced":  "echo " + rep("${x:-", n/5) + "y" + rep("}", n/5),
		"backticks":              "echo " + rep("`", n),
		"escaped backticks":      "echo " + rep("\\`", n),
		"quotes":                 "echo " + rep("'", n) + rep("\"", n),
		"ansi-c":                 "echo " + rep("$'", n/2),
		"heredocs":               rep("cat <<E\n", n/8),
		"heredoc with subst":     "cat <<EOF\n" + rep("$(", n/2) + "\nEOF",
		"open parens":            rep("(", n),
		"pipes":                  rep("a|", n/2),
		"and-ands":               rep("a&&", n/3),
		"newlines":               rep("\n", n),
		"keywords":               rep("if ", n/3) + rep("for a in b; ", n/12) + rep("case ", n/5) + rep("[[ ", n/3),
		"wrappers":               rep("sudo env nohup time ", n/20) + "x",
		"shell -c chain":         rep("bash -c ", n/8) + "x",
		"eval chain":             rep("eval ", n/5) + "x",
		"assignments":            rep("a=b ", n/4) + "x",
		"redirections":           rep("2>a ", n/4),
		"process substitutions":  rep("<(", n/2),
		"one long word":          "echo " + rep("a", n*10),
		"many words":             "echo " + rep("a ", n),
		"many env words":         "echo " + rep("A=1 ", n),
		"many nested cmd substs": "echo " + rep("$(echo x) ", n/10),
	}
	for name, in := range inputs {
		in := in
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			done := make(chan Analysis, 1)
			go func() { done <- Parse(in) }()
			select {
			case a := <-done:
				if el := time.Since(start); el > 45*time.Second {
					t.Errorf("took %v", el)
				}
				checkInvariants(t, in, a)
			case <-time.After(90 * time.Second):
				t.Fatal("did not finish in 90s")
			}
		})
	}
}
