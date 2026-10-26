//go:build unix

package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

func TestACatThatShowsTheWholeFileCountsAsItsRead(t *testing.T) {
	h := newHarness(t)
	env := h.env("main")
	env.Files = tools.NewFileState()
	write := func(name, body string) string {
		p := filepath.Join(h.root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	long := strings.Repeat("line of the file\n", 40)
	a := write("src/a.go", "package src\n\nfunc A() int { return 1 }\n")
	b := write("README.md", "# readme\n\nwhat the project is\n")
	c := write("c.txt", long)
	d := write("d.txt", "first line\n\nthird line, after a blank one\n")
	tiny := write("t.txt", "x\n")

	h.bash(env, "ls -la; echo ---; cat src/a.go README.md && head -n 3 c.txt; cat -n d.txt; cat t.txt")
	for _, p := range []string{a, b, d} {
		if !env.Files.HasRead("main", p) {
			t.Errorf("%s was shown whole and is not counted as read", p)
		}
	}
	for p, why := range map[string]string{c: "head showed part of it", tiny: "it is too short to tell from chance"} {
		if env.Files.HasRead("main", p) {
			t.Errorf("%s counts as read, yet %s", p, why)
		}
	}
	if env.Files.HasRead("other", a) {
		t.Error("another agent's read")
	}

	g1 := write("pkg/one.py", "def one():\n    return 1\n")
	g2 := write("pkg/two.py", "def two():\n    return 2\n")
	h.bash(env, "for f in pkg/*.py; do :; done; cat pkg/*.py")
	for _, p := range []string{g1, g2} {
		if !env.Files.HasRead("main", p) {
			t.Errorf("%s, shown whole by a glob, is not counted as read", p)
		}
	}

	write("e.txt", long)
	h.bash(env, "cat e.txt; echo changed >> e.txt")
	if env.Files.HasRead("main", filepath.Join(h.root, "e.txt")) {
		t.Error("a file the command changed after showing it counts as read")
	}
}
