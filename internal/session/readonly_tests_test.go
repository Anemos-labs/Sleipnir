package session_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A reviewer verifies what it reviews with the project's own test runners: python -m pytest, node --test, and the tests run with the
// project's virtual environment. In a trial a reviewer was refused ".venv/bin/python -m pytest -q" and "node --test tests/*.mjs" and
// reviewed without running anything. Creating or installing into the environment stays out of its reach.
func TestAReviewerRunsTheProjectsTests(t *testing.T) {
	repo := newRepo(t)
	python := ""
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			if real, err := filepath.EvalSymlinks(p); err == nil {
				python = real
				break
			}
		}
	}
	if python == "" {
		t.Skip("no installed Python interpreter")
	}
	venv := filepath.Join(repo, ".venv")
	for _, d := range []string{"bin", "lib/python3.12/site-packages"} {
		if err := os.MkdirAll(filepath.Join(venv, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"pyvenv.cfg": "home = /usr/bin\ninclude-system-site-packages = false\n",
		"bin/pip":    "#!" + filepath.Join(venv, "bin", "python3") + "\nimport sys\n",
		"bin/pytest": "#!" + filepath.Join(venv, "bin", "python3") + "\nimport sys\n",
	} {
		if err := os.WriteFile(filepath.Join(venv, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"bin/python3": python, "bin/python": "python3", "lib64": "lib"} {
		if err := os.Symlink(target, filepath.Join(venv, link)); err != nil {
			t.Fatal(err)
		}
	}
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Swarm = true
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range []struct {
		cmd   string
		allow bool
	}{
		{".venv/bin/python -m pytest -q", true},
		{".venv/bin/pytest", true},
		{"python3 -m pytest -q", true},
		{"node --test tests/*.mjs", true},
		{"go test ./...", true},
		{".venv/bin/pip install requests", false},
		{"python3 -m venv .venv2", false},
		{"touch x", false},
	} {
		d := s.Perm.Check(context.Background(), perm.Request{Agent: "rv-1", Role: "reviewer", Tool: "Bash", Command: c.cmd, Cwd: repo})
		if d.Allow != c.allow {
			t.Errorf("reviewer %q: allow=%v (%s), want %v", c.cmd, d.Allow, d.Reason, c.allow)
		}
	}
}
