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
		{"node --test --import ./hook.mjs .", "not allow"},
		{"node script.mjs .", "not allow"},
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
