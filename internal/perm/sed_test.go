package perm

import "testing"

// sed is allowed as a reader of lines and as nothing else: a script that can write a file, run a program or read another
// file is asked about like any command that could.
func TestSedIsAllowedOnlyAsLineSelection(t *testing.T) {
	f := newFixture(t)
	R := "{root}/"
	var cases []tc
	for _, c := range []string{
		`sed -n 1,10p main.go`, `sed -n '120,160p' src/a.go`, `sed -n '$p' main.go`, `sed -n '5,$p' main.go`, `sed -n '1~2p' main.go`,
		`sed -n '/func/p' main.go`, `sed -n '/start/,/end/p' main.go`, `sed -n '/foo/I,+3p' main.go`, `sed -n '/a\/b/p' main.go`,
		`sed -n -e 1p -e '5,7p' main.go`, `sed -ne 3p main.go`, `sed --quiet --expression=3p main.go`, `sed -n '1p;3p' main.go`,
		"sed -n '1p\n3p' main.go", `sed -n '2,4!p' main.go`, `sed -n '2,4 ! p' main.go`, `sed 10q main.go`, `sed -n 5q5 main.go`,
		`sed -n p main.go`, `sed -n '=' main.go`, `sed '3d' main.go`, `sed -s -n 1p main.go src/a.go`, `sed -nE '1p' main.go`,
		`sed -n 1,3p < main.go`, `grep -n foo main.go | sed -n '1,3p'`, `cat main.go | sed -n 2p`, `sed -n 1p -- main.go`,
	} {
		cases = append(cases, tc{name: "allow " + c, req: bash(c), want: "allow"})
	}
	for _, c := range []string{
		// what sed can do besides select lines
		`sed -i s/a/b/ main.go`, `sed -n -i 1p main.go`, `sed -ni 1p main.go`, `sed --in-place -n 1p main.go`, `sed -i.bak 1p main.go`,
		`sed -n '1w out.txt' main.go`, `sed -n 'w out.txt' main.go`, `sed -n '1W out.txt' main.go`, `sed -n '1e echo hi' main.go`,
		`sed 's/a/b/' main.go`, `sed -n 's/a/b/p' main.go`, `sed 's/a/b/e' main.go`, `sed 's/a/b/w out' main.go`, `sed 'y/ab/cd/' main.go`,
		`sed -n '1r /etc/passwd' main.go`, `sed -n '1R /etc/passwd' main.go`, `sed '1a hello' main.go`, `sed '1i hello' main.go`,
		`sed '1c hello' main.go`, `sed -n '/a/{p}' main.go`, `sed -n '$!N;p' main.go`, `sed -n 'b end;p;:end' main.go`, `sed -n '1l' main.go`,
		`sed -n '#n' main.go`, `sed -f script.sed main.go`, `sed --file=script.sed main.go`, `sed -n -f script.sed main.go`,
		`sed -n 1p main.go -i`, `sed -n --debug 1p main.go`, `sed -n -l5 1p main.go`,
		// not valid line selection
		`sed`, `sed -n`, `sed -e`, `sed -n ',5p' main.go`, `sed -n '5,p' main.go`, `sed -n '/unterminated' main.go`, `sed -n '1' main.go`,
		`sed -n '1,+p' main.go`, `sed -n '1~p' main.go`, `sed -n 'p;' -e`, `sed -n 1pp main.go`, `sed -n '1p x' main.go`,
		// the file operands are read like any other
		`sed -n 1p /etc/hosts`, `sed -n 1p ../../outside/secret.txt`, `sed -n 1p link-out`, `sed -n 1p main.go /etc/hosts`,
		// and a redirect writes
		`sed -n 1,5p main.go > out.txt`,
	} {
		cases = append(cases, tc{name: "ask " + c, req: bash(c), want: "ask"})
	}
	cases = append(cases,
		tc{name: "sed on credentials is denied whatever the script", req: bash(`sed -n 1p {home}/.ssh/id_rsa`), want: "deny"},
		tc{name: "sed in a project that allows it by rule is unchanged", allow: []string{"Bash(sed:*)"}, req: bash(`sed -i s/a/b/ main.go`), want: "allow"},
		tc{name: "the reason says what is wrong with the script", req: bash(`sed -n '1w out.txt' main.go`), want: "ask", why: "is not plain line selection"},
		tc{name: "the reason names the option", req: bash(`sed -i s/a/b/ main.go`), want: "ask", why: "sed option -i can write files or run programs"},
	)
	_ = R
	runCases(t, f, cases)
}

func TestSedScriptGrammar(t *testing.T) {
	ok := []string{"p", "1p", "1,2p", "1,$p", "$p", "/x/p", "/x/,/y/p", "/x/I,+2p", "2,~4p", "1~3p", "1!p", "1p;2p", "1p\n2p", " 1p ; 2p ", "1d", "=", "q", "3q", "3q1", "3Q", ""}
	bad := []string{"s/a/b/", "w f", "1w f", "r f", "e", "{p}", "1,p", ",2p", "/x", "1~p", "1,+p", "1pp", "p p", "y/a/b/", "1a x", "b", ":a", "n", "N", "x", "G", "#n", "1,2,3p", "/x\ny/p"}
	for _, s := range ok {
		if why := sedScriptProblem(s); why != "" {
			t.Errorf("%q should be line selection, got: %s", s, why)
		}
	}
	for _, s := range bad {
		if why := sedScriptProblem(s); why == "" {
			t.Errorf("%q is more than line selection, but was accepted", s)
		}
	}
}

// The parser is fed what a model wrote: it must end on anything.
func FuzzSedScript(f *testing.F) {
	for _, s := range []string{"1,2p", "/x/,+3p", "s/a/b/w f", "\\", "/\\", "1~", "$!{p}", "q5;Q", "\x00", "///", "1,2,3"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _ = sedScriptProblem(s) })
}
