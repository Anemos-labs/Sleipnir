package perm

import "testing"

// "Don't ask again" for an interpreter fed a program on its standard input remembers nothing: a rule would bind the words and allow
// any program the next heredoc brings, and a long option's value (node --require ./hook.js) must not pass for the script.
func TestRememberSkipsAnInterpreterFedItsProgram(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	for _, cmd := range []string{
		"node --require ./hook.js <<'EOF'\nconsole.log(1)\nEOF",
		"cat prog.js | node --require ./hook.js",
		"node --inspect-brk app.js < prog.js",
	} {
		if d := e.Check(bg, f.request(bash(cmd))); !d.Allow {
			t.Fatalf("%q: %+v", cmd, d)
		}
		if got := e.Rules(Allow); len(got) != 0 {
			t.Fatalf("%q was remembered as %q", cmd, got)
		}
	}
}
