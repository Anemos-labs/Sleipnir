package perm

import (
	"strings"
	"testing"
)

// Bash(python:venv-tests) is the run part of Bash(python:venv): the tests (and checks) run with a virtual environment of the
// workspace, never its creation or an install. A read-only role holds it, so a reviewer runs .venv/bin/python -m pytest and cannot
// change the environment.
func TestAVenvTestsRuleRunsTheTestsAndNothingMore(t *testing.T) {
	r := venvRig(t, ModePlan, nil)
	e := r.f.engine(t, Config{Mode: ModePlan, Tmp: r.tmp, TreeParents: []string{r.trees}, Allow: []string{"Bash(python:venv-tests)"}})
	for _, c := range []struct{ cmd, want string }{
		{".venv/bin/python -m pytest -q", "allow"},
		{".venv/bin/python3 -m unittest discover", "allow"},
		{".venv/bin/pytest src", "allow"},
		{".venv/bin/pip install pytest", "deny"},
		{".venv/bin/python -m pip install -r requirements.txt", "deny"},
		{"python3 -m venv .venv2", "deny"},
		{".venv/bin/python src/app.py", "deny"},
		{".venv/bin/pytest " + r.f.outside, "deny"}, // a test run is allowed while every file it names is inside the workspace
	} {
		if d := e.Check(bg, Request{Agent: "be-1", Tool: "Bash", Command: c.cmd, Cwd: r.wtA}); outcome(d) != c.want {
			t.Errorf("%q = %s (%s), want %s", c.cmd, outcome(d), d.Reason, c.want)
		}
	}

	// As a restriction it holds the test runs only; an install is judged as before.
	e = r.f.engine(t, Config{Mode: ModeAcceptEdits, Tmp: r.tmp, TreeParents: []string{r.trees}, Allow: TestsAllow, Deny: []string{"Bash(python:venv-tests)"}})
	if d := e.Check(bg, Request{Agent: "be-1", Tool: "Bash", Command: ".venv/bin/python -m pytest", Cwd: r.wtA}); outcome(d) != "deny" || !strings.Contains(d.Reason, "Bash(python:venv-tests)") {
		t.Errorf("a test run under the deny rule: %+v", d)
	}
	if d := e.Check(bg, Request{Agent: "be-1", Tool: "Bash", Command: ".venv/bin/pip install pytest", Cwd: r.wtA}); !d.Allow {
		t.Errorf("an install the tests preset allows, under a deny rule for test runs: %+v", d)
	}
	if _, err := ParseRule(Allow, "Bash(pip:venv-tests)"); err == nil {
		t.Error("a venv-tests rule for pip was accepted")
	}
}
