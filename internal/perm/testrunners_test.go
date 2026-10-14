package perm

import (
	"context"
	"testing"
)

// A test runner given paths reads the tests below them. Judged as an unknown command, "." was a tree it might write, which covered
// the protected configuration directory, so in a team demo's bypass session every "node --test ." a frontend worker ran stopped for
// the person. An option that is not on the runner's list keeps the command unknown, so a path it names may still be written.
func TestATestRunnerReadsThePathsItIsGiven(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Ask: []string{"Edit(./.sleipnir/**)", "Edit(~/.sleipnir/**)"}})
	for _, c := range []struct{ cmd, want string }{
		{"pytest .", "allow"},
		{"pytest -q -x tests/test_api.py::test_create", "allow"},
		{"pytest -xq -k 'csv and not slow' .", "allow"},
		{"python3 -m pytest . --tb=short", "allow"},
		{"python3 -m unittest discover -s . -p 'test_*.py'", "allow"},
		{"node --test .", "allow"},
		{"node --test --test-reporter=spec tests/", "allow"},
		{"node --test-name-pattern=format --test .", "allow"},
		// an option that writes, or one not on the list: the operands are judged as before
		{"pytest --basetemp=.sleipnir/tmp .", "not allow"},
		{"pytest --junitxml .sleipnir/report.xml tests", "not allow"},
		{"node --test --test-reporter-destination=.sleipnir/out.txt .", "not allow"},
		// an option not on the list, or a program that is not a runner: "." is a word that only might be a file, which bypass does not
		// ask about (default mode does, below)
		{"node --test --import ./hook.mjs .", "allow"},
		{"node script.mjs .", "allow"},
	} {
		d := e.Check(context.Background(), Request{Agent: "fe-1", Tool: "Bash", Command: f.expand(c.cmd)})
		got := outcome(d)
		if c.want == "not allow" && got != "allow" {
			continue
		}
		if got != c.want {
			t.Errorf("%s = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// Outside bypass, a program that is not a test runner (or a runner given an option it does not know) handed "." may write the tree,
// which covers the protected configuration directory: it is not allowed without a question.
func TestAnUnknownProgramHandedTheProjectAsksOutsideBypass(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeAcceptEdits, Ask: []string{"Edit(./.sleipnir/**)", "Edit(~/.sleipnir/**)"}})
	for _, cmd := range []string{"node --test --import ./hook.mjs .", "node script.mjs ."} {
		if d := e.Check(context.Background(), Request{Agent: "fe-1", Tool: "Bash", Command: f.expand(cmd)}); outcome(d) == "allow" {
			t.Errorf("%s = allow (%s)", cmd, d.Reason)
		}
	}
}
