package taskgen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// A small library of two Go packages, core importing util, with tests in both: util's own (package util, one of them
// using an unexported function) and core's from outside (package core_test, one of them reading testdata).
const (
	rbUtil = `// Package util has small helpers.
package util

import "strings"

// Version is the version of the package.
const Version = "1.2.0"

// Double returns twice n.
func Double(n int) int { return 2 * n }

// Clamp limits n to the range from lo to hi.
func Clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// Reverse reverses s.
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = swap(r[i], r[j])
	}
	return string(r)
}

// Shout upper-cases s and adds an exclamation mark.
func Shout(s string) string { return strings.ToUpper(s) + "!" }

func swap(a, b rune) (rune, rune) { return b, a }
`
	// a_test.go runs first, and passes on the stubs: not a test the score counts.
	rbUtilVersionTest = "package util\n\nimport \"testing\"\n\nfunc TestVersion(t *testing.T) {\n\tif Version != \"1.2.0\" {\n\t\tt.Fatal(Version)\n\t}\n}\n"
	rbUtilTest        = `package util

import "testing"

func TestDouble(t *testing.T) {
	if Double(4) != 8 {
		t.Fatal(Double(4))
	}
}

func TestClamp(t *testing.T) {
	if Clamp(-1, 0, 5) != 0 || Clamp(9, 0, 5) != 5 || Clamp(3, 0, 5) != 3 {
		t.Fatal("clamp")
	}
}

func TestReverse(t *testing.T) {
	if Reverse("abc") != "cba" {
		t.Fatal(Reverse("abc"))
	}
}

func TestShout(t *testing.T) {
	if Shout("hi") != "HI!" {
		t.Fatal(Shout("hi"))
	}
}

func TestSwap(t *testing.T) {
	if a, b := swap('a', 'b'); a != 'b' || b != 'a' {
		t.Fatal("swap")
	}
}
`
	rbCore = `// Package core scores words.
package core

import (
	"fmt"

	"example.com/rebuild/util"
)

// Score is the score of a word: twice its length, at most 10.
func Score(w string) int { return util.Clamp(util.Double(len(w)), 0, 10) }

// Label describes a word and its score.
func Label(w string) string { return fmt.Sprintf("%s=%d", util.Reverse(w), Score(w)) }

// Loud is Label shouted.
func Loud(w string) string { return util.Shout(Label(w)) }
`
	rbCoreTest = `package core_test

import (
	"os"
	"strings"
	"testing"

	"example.com/rebuild/core"
)

func TestScore(t *testing.T) {
	if core.Score("abc") != 6 || core.Score("abcdefgh") != 10 {
		t.Fatal("score")
	}
}

func TestLabel(t *testing.T) {
	if core.Label("ab") != "ba=4" {
		t.Fatal(core.Label("ab"))
	}
}

func TestLoud(t *testing.T) {
	if core.Loud("ab") != "BA=4!" {
		t.Fatal(core.Loud("ab"))
	}
}

func TestFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/words.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range strings.Fields(string(b)) {
		if s := core.Score(w); s < 2 || s > 10 {
			t.Fatal(w, s)
		}
	}
}
`
)

// rbFixture commits the library and returns its directory and commit.
func rbFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	for p, body := range map[string]string{
		"go.mod":                     "module example.com/rebuild\n\ngo 1.21\n",
		"README.md":                  "# rebuild\n\nScores words.\n",
		"LICENSE":                    mitLicense,
		"docs/guide.md":              "Use core.Score.\n",
		"util/util.go":               rbUtil,
		"util/a_test.go":             rbUtilVersionTest,
		"util/util_test.go":          rbUtilTest,
		"core/core.go":               rbCore,
		"core/core_test.go":          rbCoreTest,
		"core/testdata/words.txt":    "a bb ccc\n",
		"cmd/demo/main.go":           "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/rebuild/core\"\n)\n\nfunc main() { fmt.Println(core.Score(\"x\")) }\n",
		"cmd/demo/main_test.go":      "package main\n\nimport \"testing\"\n\nfunc TestNothing(t *testing.T) {}\n",
		"internal/note/note.go":      "// Package note is out of scope.\npackage note\n\n// Note is a note.\nconst Note = \"n\"\n",
		"internal/note/note_test.go": "package note\n\nimport \"testing\"\n\nfunc TestNote(t *testing.T) {\n\tif Note != \"n\" {\n\t\tt.Fatal()\n\t}\n}\n",
	} {
		f.write(p, body)
	}
	return f, f.commit("the library")
}

// rbOptions are options for the fixture: gates lowered to its size.
func rbOptions(t *testing.T, m *env.Workspaces, store events.Blobs, tier string) RebuildOptions {
	return RebuildOptions{
		Workspaces: m, Scope: []string{"util", "core"}, Tier: tier, Seal: filepath.Join(t.TempDir(), "starts"),
		HiddenBlobs: store, GoldBlobs: store, Repeats: 2, DeadlineS: 300, IDPrefix: "rb",
		Gates: RebuildGates{MinLOC: 10, MinTests: 5},
	}
}

// sealedFiles lists the files of a start repository's one commit, and checks it has exactly one.
func sealedFiles(t *testing.T, task *rl.Task) map[string]string {
	t.Helper()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"--git-dir=" + task.Repo.Path}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	if all := git("rev-list", "--all"); all != task.Repo.Commit {
		t.Fatalf("the start repository's commits: %q, want only %s", all, task.Repo.Commit)
	}
	out := map[string]string{}
	for _, p := range strings.Split(git("ls-tree", "-r", "--name-only", "HEAD"), "\n") {
		out[p] = git("show", "HEAD:"+p)
	}
	return out
}

// checkRebuildTask checks what both tiers share.
func checkRebuildTask(t *testing.T, task *rl.Task, rep *RebuildReport, tier string) {
	t.Helper()
	if task.Kind != rl.TaskSwarm || task.Team.Mode != "swarm" || task.Team.Agents != 3 || task.Budget.WallS != 6*3600 {
		t.Errorf("kind %s, team %+v, budget %+v", task.Kind, task.Team, task.Budget)
	}
	for _, tag := range []string{"long", "rebuild", "harvested", "go", "rebuild-" + tier, "rb"} {
		if !contains(task.Tags, tag) {
			t.Errorf("tags %v lack %s", task.Tags, tag)
		}
	}
	var meta Meta
	if err := json.Unmarshal(task.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"repo": meta.Source["repo"], "commit": meta.Commit, "scope": "util,core", "tier": tier, "spdx": "MIT"}
	if meta.Generator != "taskgen/rebuild" || meta.GoldBlob == "" || meta.Source["repo"] == "" || !reflect.DeepEqual(meta.Source, want) ||
		!reflect.DeepEqual(meta.Files, []string{"core/core.go", "util/util.go"}) {
		t.Errorf("meta %+v", meta)
	}
	for _, p := range []string{"README*", "/docs/**", "go.mod", "util/*_test.go", "core/testdata/**", ".sleipnir/**"} {
		if !contains(task.Verifier.Protected, p) {
			t.Errorf("protected %v lacks %s", task.Verifier.Protected, p)
		}
	}
	for _, p := range []string{"util/a_test.go", "util/util_test.go", "core/core_test.go", "core/testdata/words.txt"} {
		if _, ok := task.Verifier.Hidden[p]; !ok {
			t.Errorf("%s is not hidden: %v", p, task.Verifier.Hidden)
		}
	}
	if _, ok := task.Verifier.Hidden["internal/note/note_test.go"]; ok {
		t.Error("a test out of scope is hidden")
	}
	// Units: util before core (core imports util; by name core would come first), weighted by their lines. TestVersion
	// passes on the stubs, which keep the constant, and is dropped; without the sources it fails like the rest.
	utilTests, dropped := 5, 1
	if tier == RebuildSpec {
		utilTests, dropped = 6, 0
	}
	wantUnits := []RebuildUnit{
		{Name: "example.com/rebuild/util", Weight: float64(countLOC([]byte(rbUtil), "//")), Tests: utilTests},
		{Name: "example.com/rebuild/core", Weight: float64(countLOC([]byte(rbCore), "//")), Tests: 4},
	}
	if !reflect.DeepEqual(rep.Units, wantUnits) || rep.Tests != utilTests+4 || rep.Dropped != dropped {
		t.Errorf("units %+v, %d tests, %d dropped; want %+v, %d and %d", rep.Units, rep.Tests, rep.Dropped, wantUnits, utilTests+4, dropped)
	}
	if rep.StartScore == nil || *rep.StartScore != 0 || rep.ReferenceScore == nil || *rep.ReferenceScore != 1 {
		t.Errorf("self-check: start %v, reference %v", rep.StartScore, rep.ReferenceScore)
	}
	if rep.LOC != countLOC([]byte(rbUtil), "//")+countLOC([]byte(rbCore), "//") || rep.Lang != Go {
		t.Errorf("report %+v", rep)
	}
	if err := env.ValidateTask(*task); err != nil {
		t.Fatal(err)
	}
}

// goldOf reads a task's reference solution.
func goldOf(t *testing.T, task *rl.Task, store events.Blobs) []byte {
	t.Helper()
	var meta Meta
	_ = json.Unmarshal(task.Meta, &meta)
	b, err := store.Get(core.Hash(meta.GoldBlob))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// patchOf keeps the parts of a patch that change files under dir.
func patchOf(patch []byte, dir string) []byte {
	var out strings.Builder
	parts := strings.Split(string(patch), "diff --git ")
	for _, p := range parts[1:] {
		if strings.HasPrefix(p, "a/"+dir+"/") {
			out.WriteString("diff --git " + p)
		}
	}
	return []byte(out.String())
}

func TestRebuildGoStubs(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test many times")
	}
	havePython(t)
	have(t, "go")
	f, ref := rbFixture(t)
	store := events.NewMemBlobs()
	m := newManager(t)
	opts := rbOptions(t, m, store, RebuildStubs)
	opts.SealPath = ""
	task, rep, err := Rebuild(context.Background(), f.Dir, opts)
	if err != nil {
		t.Fatalf("%v\nreport: %+v", err, rep)
	}
	checkRebuildTask(t, task, rep, RebuildStubs)
	if task.Repo.Path != filepath.Join(opts.Seal, task.ID+".git") || !strings.HasPrefix(task.ID, "rb-rebuild-stubs-") {
		t.Errorf("repo %+v, id %s", task.Repo, task.ID)
	}
	// A later run of the same scope that is rejected leaves the accepted start alone: tasks files still name it.
	again := opts
	again.Gates.MinTests = 100000
	if _, _, err := Rebuild(context.Background(), f.Dir, again); err == nil {
		t.Fatal("a scope over the test gate was accepted")
	}
	if out, err := exec.Command("git", "--git-dir="+task.Repo.Path, "rev-parse", "HEAD").Output(); err != nil ||
		strings.TrimSpace(string(out)) != task.Repo.Commit {
		t.Errorf("after a rejected run the accepted start is %q (%v), want commit %s", out, err, task.Repo.Commit)
	}
	if entries, _ := os.ReadDir(opts.Seal); len(entries) != 1 {
		t.Errorf("the seal directory holds %v, want only the accepted start", entries)
	}
	if !strings.Contains(task.Prompt, "reduced to stubs") || !strings.Contains(task.Prompt, "- `example.com/rebuild/util` (util/") ||
		!strings.Contains(task.Prompt, "fetching the project's original sources from elsewhere") ||
		strings.Index(task.Prompt, "rebuild/util`") > strings.Index(task.Prompt, "rebuild/core`") {
		t.Errorf("prompt:\n%s", task.Prompt)
	}
	var meta Meta
	_ = json.Unmarshal(task.Meta, &meta)
	if meta.Commit != ref {
		t.Errorf("meta.commit %s, want the reference %s", meta.Commit, ref)
	}

	// The start: stubs in place of the sources, no tests of the scope, the rest as it was.
	files := sealedFiles(t, task)
	for _, gone := range []string{"util/a_test.go", "util/util_test.go", "core/core_test.go", "core/testdata/words.txt"} {
		if _, ok := files[gone]; ok {
			t.Errorf("the start has %s", gone)
		}
	}
	for _, kept := range []string{"README.md", "docs/guide.md", "go.mod", "LICENSE", "cmd/demo/main.go", "cmd/demo/main_test.go", "internal/note/note_test.go"} {
		if _, ok := files[kept]; !ok {
			t.Errorf("the start lacks %s", kept)
		}
	}
	util := files["util/util.go"]
	if !strings.Contains(util, "// Clamp limits n to the range from lo to hi.\nfunc Clamp(n, lo, hi int) int {\n\tpanic(\"not implemented\")\n}") ||
		!strings.Contains(util, "func swap(a, b rune) (rune, rune) {\n\tpanic(") || strings.Contains(util, "\"strings\"") || strings.Contains(util, "\treturn") {
		t.Errorf("util/util.go at the start:\n%s", util)
	}

	// The stubbed tree builds and vets clean.
	dir := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", task.Repo.Path, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	vet := exec.Command("go", "vet", "./...")
	vet.Dir = dir
	vet.Env = append(os.Environ(), "GOCACHE="+sharedGoCache, "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet of the start: %v\n%s", err, out)
	}

	// Scored as rollouts are: the reference is exactly 1, the start 0, util alone its weight's share.
	gold := goldOf(t, task, store)
	vo := env.VerifyOptions{Workspaces: m, HiddenBlobs: store}
	crep, err := env.CheckTask(context.Background(), *task, gold, vo)
	if err != nil || crep.Gold.Score != 1 || crep.Baseline.Score != 0 {
		t.Fatalf("check: %v, reference %v, start %v\n%s", err, crep.Gold.Score, crep.Baseline.Score, crep.Gold.Log)
	}
	half, err := env.VerifyPatch(context.Background(), *task, patchOf(gold, "util"), vo)
	if err != nil {
		t.Fatal(err)
	}
	wu, wc := rep.Units[0].Weight, rep.Units[1].Weight
	if want := wu / (wu + wc); half.Score < want-1e-9 || half.Score > want+1e-9 ||
		half.Units["example.com/rebuild/util"].Passed != 5 || half.Units["example.com/rebuild/core"].Passed != 0 {
		t.Errorf("util rebuilt alone: score %v (want %v), units %+v\n%s", half.Score, want, half.Units, half.Log)
	}
}

func TestRebuildGoSpec(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test many times")
	}
	havePython(t)
	have(t, "go")
	f, _ := rbFixture(t)
	store := events.NewMemBlobs()
	opts := rbOptions(t, newManager(t), store, RebuildSpec)
	opts.SealPath = "starts"
	task, rep, err := Rebuild(context.Background(), f.Dir, opts)
	if err != nil {
		t.Fatalf("%v\nreport: %+v", err, rep)
	}
	checkRebuildTask(t, task, rep, RebuildSpec)
	if task.Repo.Path != "starts/"+task.ID+".git" {
		t.Errorf("repo %+v", task.Repo)
	}
	if !contains(task.Verifier.Protected, "/API.md") || !strings.Contains(task.Prompt, "API.md documents their API") {
		t.Errorf("protected %v, prompt:\n%s", task.Verifier.Protected, task.Prompt)
	}
	task.Repo.Path = filepath.Join(opts.Seal, task.ID+".git")
	files := sealedFiles(t, task)
	for _, gone := range []string{"util/util.go", "core/core.go", "util/util_test.go", "core/testdata/words.txt"} {
		if _, ok := files[gone]; ok {
			t.Errorf("the start has %s", gone)
		}
	}
	api := files["API.md"]
	for _, want := range []string{
		"# API\n", "## Package `example.com/rebuild/util`", "Package util has small helpers.", "const Version = \"1.2.0\"",
		"```go\nfunc Clamp(n, lo, hi int) int\n```\n\nClamp limits n to the range from lo to hi.",
		"### Unexported declarations the package's tests use\n\n```go\nfunc swap(a, b rune) (rune, rune)\n```",
		"## Package `example.com/rebuild/core`", "func Loud(w string) string",
	} {
		if !strings.Contains(api, want) {
			t.Errorf("API.md lacks %q:\n%s", want, api)
		}
	}
	if strings.Index(api, "rebuild/util`") > strings.Index(api, "rebuild/core`") {
		t.Errorf("API.md lists core before util:\n%s", api)
	}
}

// TestRebuildPython: a Python package of three modules, the package re-exporting from core, which imports util;
// tests outside the package, by file. Units are modules, a test file belongs to the module it exercises (through the
// re-exports), and they come in import order.
func TestRebuildPython(t *testing.T) {
	if testing.Short() {
		t.Skip("runs pytest many times")
	}
	havePytest(t)
	f := newFixture(t)
	lib := map[string]string{
		"pyproject.toml":    "[project]\nname = \"mylib\"\nversion = \"0\"\n\n[tool.pytest.ini_options]\ntestpaths = [\"tests\"]\n",
		"README.md":         "# mylib\n",
		"mylib/__init__.py": "\"\"\"A small library.\"\"\"\nfrom .core import Stack, total\n\n__all__ = [\"Stack\", \"total\"]\n",
		"mylib/util.py":     "\"\"\"Helpers.\"\"\"\n\n\ndef double(x):\n    \"\"\"Twice x.\"\"\"\n    return 2 * x\n\n\ndef _clip(x, hi):\n    return min(x, hi)\n",
		"mylib/core.py": "\"\"\"The core.\"\"\"\nfrom .util import double, _clip\n\nLIMIT = 10\n\n\ndef total(xs):\n    \"\"\"The clipped sum of the doubles.\"\"\"\n    return _clip(sum(double(x) for x in xs), LIMIT)\n\n\n" +
			"class Stack:\n    \"\"\"A stack.\"\"\"\n\n    def __init__(self):\n        self.items = []\n\n    def push(self, x):\n        self.items.append(x)\n        return self\n\n    def size(self):\n        return len(self.items)\n",
		"tests/test_util.py":  "from mylib.util import double, _clip\n\n\ndef test_double():\n    assert double(3) == 6\n\n\ndef test_clip():\n    assert _clip(9, 5) == 5\n",
		"tests/test_core.py":  "import mylib\nfrom mylib import Stack\n\n\ndef test_total():\n    assert mylib.total([1, 2]) == 6\n    assert mylib.total([9]) == 10\n\n\ndef test_stack():\n    assert Stack().push(1).push(2).size() == 2\n",
		"tests/test_const.py": "from mylib.core import LIMIT\n\n\ndef test_limit():\n    assert LIMIT == 10\n",
	}
	for p, body := range lib {
		f.write(p, body)
	}
	f.commit("the library")
	for _, tier := range []string{RebuildStubs, RebuildSpec} {
		t.Run(tier, func(t *testing.T) {
			store := events.NewMemBlobs()
			opts := rbOptions(t, newManager(t), store, tier)
			opts.Scope = []string{"mylib"}
			opts.Gates.MinTests = 4
			task, rep, err := Rebuild(context.Background(), f.Dir, opts)
			if err != nil {
				t.Fatalf("%v\nreport: %+v", err, rep)
			}
			// test_const.py passes on the stubs (the constant stays) and is dropped there.
			constTests := 0
			if tier == RebuildSpec {
				constTests = 1
			}
			want := []RebuildUnit{
				{Name: "mylib.util", Weight: float64(countLOC([]byte(f.git("show", "HEAD:mylib/util.py")), "#")), Tests: 2},
				{Name: "mylib.core", Weight: float64(countLOC([]byte(f.git("show", "HEAD:mylib/core.py")), "#")), Tests: 2 + constTests},
			}
			if !reflect.DeepEqual(rep.Units, want) || rep.Lang != Python {
				t.Errorf("units %+v, want %+v", rep.Units, want)
			}
			if rep.StartScore == nil || *rep.StartScore != 0 || rep.ReferenceScore == nil || *rep.ReferenceScore != 1 {
				t.Errorf("self-check: start %v, reference %v", rep.StartScore, rep.ReferenceScore)
			}
			files := sealedFiles(t, task)
			for _, gone := range []string{"tests/test_util.py", "tests/test_core.py", "tests/test_const.py"} {
				if _, ok := files[gone]; ok {
					t.Errorf("the start has %s", gone)
				}
			}
			switch tier {
			case RebuildStubs:
				if core := files["mylib/core.py"]; !strings.Contains(core, "    def push(self, x):\n        raise NotImplementedError\n") || !strings.Contains(core, "LIMIT = 10") {
					t.Errorf("mylib/core.py at the start:\n%s", core)
				}
			case RebuildSpec:
				if _, ok := files["mylib/core.py"]; ok {
					t.Error("the start has mylib/core.py")
				}
				if api := files["API.md"]; !strings.Contains(api, "## Module `mylib.util`") || !strings.Contains(api, "def _clip(x, hi): ...") ||
					strings.Index(api, "`mylib.util`") > strings.Index(api, "`mylib.core`") || strings.Index(api, "`mylib.core`") > strings.Index(api, "## Module `mylib`") {
					t.Errorf("API.md:\n%s", api)
				}
			}
		})
	}

	// The same library in a src layout: the tests import it only because the suite puts src/ on the import path.
	srcf := newFixture(t)
	for p, body := range lib {
		if strings.HasPrefix(p, "mylib/") {
			p = "src/" + p
		}
		srcf.write(p, body)
	}
	srcf.commit("the library, in src/")
	srcOpts := rbOptions(t, newManager(t), events.NewMemBlobs(), RebuildStubs)
	srcOpts.Scope = []string{"mylib"}
	srcOpts.Gates.MinTests = 4
	if _, rep, err := Rebuild(context.Background(), srcf.Dir, srcOpts); err != nil || rep.ReferenceScore == nil || *rep.ReferenceScore != 1 ||
		rep.StartScore == nil || *rep.StartScore != 0 {
		t.Errorf("src layout: %v %+v", err, rep)
	}

	// A copy of the package where the tests would import it from outside the checkout: the user site (where
	// pip install --user puts it) or a PYTHONPATH entry. Either one leaks the reference.
	userBase := t.TempDir()
	where := exec.Command("python3", "-c", "import site; print(site.ENABLE_USER_SITE, site.getusersitepackages())")
	where.Env = append(os.Environ(), "PYTHONUSERBASE="+userBase)
	out, err := where.Output()
	if err != nil {
		t.Fatal(err)
	}
	enabled, userSite, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	pyPath := t.TempDir()
	for _, c := range []struct {
		name, dir string
		env       map[string]string
	}{
		{"user site", userSite, map[string]string{"PYTHONUSERBASE": userBase}},
		{"PYTHONPATH", pyPath, map[string]string{"PYTHONPATH": pyPath}},
	} {
		if c.name == "user site" && enabled != "True" {
			t.Logf("python3 has no user site (%s), so nothing installed there is importable", enabled)
			continue
		}
		if err := os.MkdirAll(filepath.Join(c.dir, "mylib"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(c.dir, "mylib", "__init__.py"), []byte("from .core import Stack, total\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		c.env["GOCACHE"] = sharedGoCache
		leaky, err := env.NewWorkspaces(env.WorkspaceOptions{
			Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true, SetupTimeout: 2 * time.Minute, FailureTTL: -1, SetEnv: c.env,
		})
		if err != nil {
			t.Fatal(err)
		}
		opts := rbOptions(t, leaky, events.NewMemBlobs(), RebuildStubs)
		opts.Scope = []string{"mylib"}
		opts.Gates.MinTests = 4
		_, rep, err := Rebuild(context.Background(), f.Dir, opts)
		leaky.Close()
		// The check prints resolved paths (a temporary directory may lie behind a symlink, as /var does on macOS).
		real, rerr := filepath.EvalSymlinks(c.dir)
		if rerr != nil {
			real = c.dir
		}
		var rj *RebuildRejection
		if !errors.As(err, &rj) || rj.Reason != ReasonRebuildLeak || !strings.Contains(err.Error(), "mylib is installed at "+real) {
			t.Errorf("mylib in the %s: %v %+v", c.name, err, rep)
		}
	}
}

func TestRebuildRejections(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go in the environment")
	}
	havePython(t)
	have(t, "go")
	f, _ := rbFixture(t)
	store := events.NewMemBlobs()
	m := newManager(t)
	reason := func(err error) string {
		var rj *RebuildRejection
		if errors.As(err, &rj) {
			return rj.Reason
		}
		return ""
	}

	opts := rbOptions(t, m, store, RebuildStubs)
	opts.Gates.MinLOC = 100000
	_, rep, err := Rebuild(context.Background(), f.Dir, opts)
	if reason(err) != ReasonRebuildTooSmall || rep.Rejected == nil || rep.LOC == 0 {
		t.Errorf("too small: %v %+v", err, rep)
	}
	if entries, _ := os.ReadDir(opts.Seal); len(entries) > 0 {
		t.Errorf("a rejected task left %v", entries)
	}
	// A scope entry may be an import path, and "dir/..." takes every package below dir (testdata and the like aside).
	lib := countLOC([]byte(rbUtil), "//") + countLOC([]byte(rbCore), "//")
	for _, c := range []struct {
		scope []string
		loc   int
	}{
		{[]string{"example.com/rebuild/util", "./core"}, lib},
		{[]string{"./..."}, lib + 6 + 2}, // and cmd/demo (6 lines) and internal/note (2)
	} {
		opts := rbOptions(t, m, store, RebuildStubs)
		opts.Scope, opts.Gates.MinLOC = c.scope, 100000
		_, rep, err := Rebuild(context.Background(), f.Dir, opts)
		if reason(err) != ReasonRebuildTooSmall || rep.LOC != c.loc {
			t.Errorf("scope %v: %v, %d lines, want %d", c.scope, err, rep.LOC, c.loc)
		}
	}

	opts = rbOptions(t, m, store, RebuildStubs)
	opts.Scope = []string{"nowhere/pkg"}
	if _, _, err := Rebuild(context.Background(), f.Dir, opts); reason(err) != ReasonRebuildScope {
		t.Errorf("unknown scope: %v", err)
	}

	opts = rbOptions(t, m, store, RebuildStubs)
	opts.Gates.MinTests = 50
	if _, _, err := Rebuild(context.Background(), f.Dir, opts); reason(err) != ReasonRebuildFewTests {
		t.Errorf("too few tests: %v", err)
	}

	opts = rbOptions(t, m, store, RebuildStubs)
	opts.Gates.MaxSuite = time.Millisecond
	if _, rep, err := Rebuild(context.Background(), f.Dir, opts); reason(err) != ReasonRebuildSlowSuite || rep.SuiteMs == 0 {
		t.Errorf("a slow suite: %v %+v", err, rep)
	}

	// The module in the environment's module cache: an agent could copy the reference from there.
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "example.com", "rebuild@v1.0.0", "util"), 0o755); err != nil {
		t.Fatal(err)
	}
	leaky, err := env.NewWorkspaces(env.WorkspaceOptions{
		Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true, SetupTimeout: 2 * time.Minute, FailureTTL: -1,
		SetEnv: map[string]string{"GOCACHE": sharedGoCache, "GOMODCACHE": cache},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer leaky.Close()
	opts = rbOptions(t, leaky, store, RebuildStubs)
	_, rep, err = Rebuild(context.Background(), f.Dir, opts)
	if reason(err) != ReasonRebuildLeak || !strings.Contains(err.Error(), "rebuild@v1.0.0") {
		t.Errorf("leak: %v %+v", err, rep)
	}
}
