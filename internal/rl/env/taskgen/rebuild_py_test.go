package taskgen

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const (
	pyInit = `"""The package."""
from .core import Stack, add
from . import util
`
	pyCore = `"""Core things."""
import os
from .util import helper

__all__ = ["Stack", "add", "LIMIT"]

LIMIT = 10
_cache = {}


def add(a: int, b: int = 2) -> int:
    """Add two numbers."""
    # a comment inside
    total = a + helper(b)
    return total  # trailing


def _internal(x): return x * 2


class Stack:
    """A stack."""
    size: int = 0

    def __init__(self):
        self.items = []

    @property
    def top(self):
        """The top item."""
        return self.items[-1]

    def abstract(self):
        raise NotImplementedError

    def proto(self): ...


if __name__ == "__main__":
    print(add(1, 2))
`
	pyUtil = `def helper(x):
    # first a comment
    @decorate
    def inner():
        return 1
    return x + 1
    # trailing note

# a module comment
def g(
    a,  # the a
    b,
):
    return a
`
	pyTestCore = `import pkg
from pkg import Stack
from pkg.core import _internal
from pkg import util as u


def test_stack():
    assert Stack().top is None
    assert pkg.add(1, 2) == 3
    assert u.helper(1) == 2
    assert _internal(2) == 4
`
	pyTestMisc = `from pkg.util import helper


def test_helper():
    assert helper(1) == 2
`
)

// TestPythonRebuildScript: rebuild_py.py stubs each module (signatures, docstrings, module-level code and class
// attributes kept; bodies and the comments in them replaced), extracts the API with what the tests use, lists the
// imports between modules and follows the tests' references through a package's re-exports. Only python3 is needed.
func TestPythonRebuildScript(t *testing.T) {
	have(t, "python3")
	mods := []pyModuleIn{
		{Name: "pkg", Path: "pkg/__init__.py", Package: true, Source: pyInit},
		{Name: "pkg.core", Path: "pkg/core.py", Source: pyCore},
		{Name: "pkg.util", Path: "pkg/util.py", Source: pyUtil},
	}
	tests := []pyTestIn{{Path: "tests/test_core.py", Source: pyTestCore}, {Path: "tests/test_misc.py", Source: pyTestMisc}}
	a, err := runPyRebuild(context.Background(), "python3", mods, tests)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Errors) > 0 {
		t.Fatalf("errors: %v", a.Errors)
	}

	core := a.Modules["pkg.core"].Stub
	for _, want := range []string{
		`"""Core things."""`, "import os\nfrom .util import helper", `__all__ = ["Stack", "add", "LIMIT"]`, "LIMIT = 10", "_cache = {}",
		"def add(a: int, b: int = 2) -> int:\n    \"\"\"Add two numbers.\"\"\"\n    raise NotImplementedError\n",
		"def _internal(x): raise NotImplementedError\n",
		"class Stack:\n    \"\"\"A stack.\"\"\"\n    size: int = 0\n",
		"    def __init__(self):\n        raise NotImplementedError\n",
		"    @property\n    def top(self):\n        \"\"\"The top item.\"\"\"\n        raise NotImplementedError\n",
		"    def abstract(self):\n        raise NotImplementedError\n", // already a stub: kept
		"    def proto(self): ...\n",
		"if __name__ == \"__main__\":\n    raise NotImplementedError\n",
	} {
		if !strings.Contains(core, want) {
			t.Errorf("the stub of pkg.core lacks %q:\n%s", want, core)
		}
	}
	for _, gone := range []string{"a comment inside", "total", "trailing", "self.items", "print("} {
		if strings.Contains(core, gone) {
			t.Errorf("the stub of pkg.core still has %q:\n%s", gone, core)
		}
	}
	util := a.Modules["pkg.util"].Stub
	if want := "def helper(x):\n    raise NotImplementedError\n\n# a module comment\ndef g(\n    a,  # the a\n    b,\n):\n    raise NotImplementedError\n"; util != want {
		t.Errorf("the stub of pkg.util:\n%s\nwant:\n%s", util, want)
	}
	if a.Modules["pkg"].Stub != pyInit {
		t.Errorf("the package's __init__ changed:\n%s", a.Modules["pkg"].Stub)
	}

	// Imports between the modules, and the order they give.
	imports := map[string][]string{}
	var pkgs []*rebuildPkg
	for _, m := range mods {
		imports[m.Name] = a.Modules[m.Name].Imports
		pkgs = append(pkgs, &rebuildPkg{Name: m.Name, Imports: a.Modules[m.Name].Imports})
	}
	if want := map[string][]string{"pkg": {"pkg.core", "pkg.util"}, "pkg.core": {"pkg.util"}, "pkg.util": {}}; !reflect.DeepEqual(imports, want) {
		t.Errorf("imports %v, want %v", imports, want)
	}
	var order []string
	idx := map[string]int{}
	for i, p := range dependencyOrder(pkgs) {
		order = append(order, p.Name)
		idx[p.Name] = i
	}
	if want := []string{"pkg.util", "pkg.core", "pkg"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}

	// References, through the package's re-exports to the module that defines a name.
	tc := a.Tests["tests/test_core.py"]
	if want := map[string]int{"pkg": 1, "pkg.core": 3, "pkg.util": 2}; !reflect.DeepEqual(tc.Refs, want) {
		t.Errorf("test_core.py refs %v, want %v", tc.Refs, want)
	}
	if want := map[string][]string{"pkg.core": {"Stack", "_internal", "add"}, "pkg.util": {"helper"}}; !reflect.DeepEqual(tc.Names, want) {
		t.Errorf("test_core.py names %v, want %v", tc.Names, want)
	}
	// test_core.py is after core; test_misc.py refers to util only.
	if u := pyTestUnit("tests/test_core.py", tc.Refs, idx, "pkg"); u != "pkg.core" {
		t.Errorf("test_core.py belongs to %s", u)
	}
	if u := pyTestUnit("tests/test_misc.py", a.Tests["tests/test_misc.py"].Refs, idx, "pkg"); u != "pkg.util" {
		t.Errorf("test_misc.py belongs to %s", u)
	}
	if u := pyTestUnit("tests/test_cli.py", nil, idx, "pkg"); u != "pkg" {
		t.Errorf("a test that refers to nothing belongs to %s", u)
	}
	// Without a name to go by, the module referred to most, the later in order on a tie.
	if u := pyTestUnit("tests/test_x.py", map[string]int{"pkg.util": 2, "pkg.core": 2, "pkg": 1}, idx, "pkg"); u != "pkg.core" {
		t.Errorf("a tie went to %s", u)
	}

	// The API: __all__ decides what is public; _internal is shown because a test uses it.
	sec := pyAPISection(&rebuildPkg{Name: "pkg.core", Files: []string{"pkg/core.py"}}, a.Modules["pkg.core"])
	for _, want := range []string{
		"## Module `pkg.core`", "File `pkg/core.py`.", "Core things.", "LIMIT = 10",
		"def add(a: int, b: int=2) -> int:\n    \"\"\"Add two numbers.\"\"\"",
		"class Stack:\n    \"\"\"A stack.\"\"\"\n\n    size: int = 0\n\n    def __init__(self): ...\n\n    @property\n    def top(self):\n        \"\"\"The top item.\"\"\"",
		"### Not public; the module's tests use it\n\n```python\ndef _internal(x): ...\n```",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("the API of pkg.core lacks %q:\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "_cache") || strings.Contains(sec, "total") {
		t.Errorf("the API of pkg.core shows its internals:\n%s", sec)
	}
	if sec := pyAPISection(&rebuildPkg{Name: "pkg", Files: []string{"pkg/__init__.py"}}, a.Modules["pkg"]); !strings.Contains(sec, "from .core import Stack, add") {
		t.Errorf("the API of pkg lacks its re-exports:\n%s", sec)
	}
}
