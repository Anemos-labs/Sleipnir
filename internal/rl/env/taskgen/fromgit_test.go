package taskgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/env"
)

const calcGoMod = "module example.com/calc\n\ngo 1.24\n"

const calcV1 = `package calc

// Add returns a+b.
func Add(a, b int) int { return a + b }

// Clamp limits v to [lo, hi].
func Clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return lo // BUG: should return hi
	}
	return v
}
`

const calcTestV1 = `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add")
	}
}
`

// buildCalcRepo creates the history used by the FromGit tests and returns the
// commit ids by name.
func buildCalcRepo(t *testing.T) (*fixture, map[string]string) {
	t.Helper()
	f := newFixture(t)
	ids := map[string]string{}
	f.write("go.mod", calcGoMod)
	f.write("calc.go", calcV1)
	f.write("calc_test.go", calcTestV1)
	f.write("LICENSE", mitLicense)
	f.write("README.md", "# calc\n")
	ids["initial"] = f.commit("initial commit")

	// c2: a feature with its test (candidate: parent fails to compile the new test).
	calcV2 := calcV1 + "\n// Max returns the larger of a and b.\nfunc Max(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n"
	f.write("calc.go", calcV2)
	calcTestV2 := calcTestV1 + "\nfunc TestMax(t *testing.T) {\n\tif Max(2, 5) != 5 || Max(5, 2) != 5 {\n\t\tt.Fatal(\"Max\")\n\t}\n}\n"
	f.write("calc_test.go", calcTestV2)
	ids["max"] = f.commit("Add Max\n\nMax returns the larger of two ints.\n\nSigned-off-by: A Dev <dev@example.com>")

	// c3: documentation only (not a candidate).
	f.write("README.md", "# calc\n\nA calculator.\n")
	ids["docs"] = f.commit("docs: describe the package")

	// c4: a bug fix with a regression test.
	calcV3 := strings.Replace(calcV2, "return lo // BUG: should return hi", "return hi", 1)
	f.write("calc.go", calcV3)
	calcTestV3 := calcTestV2 + "\nfunc TestClampUpper(t *testing.T) {\n\tif got := Clamp(10, 0, 5); got != 5 {\n\t\tt.Fatalf(\"Clamp(10,0,5) = %d, want 5\", got)\n\t}\n}\n"
	f.write("calc_test.go", calcTestV3)
	ids["clamp"] = f.commit("Fix Clamp returning the lower bound above the range\n\nFixes #7")

	// c5: test-only change (not a candidate).
	f.write("calc_test.go", calcTestV3+"\nfunc TestAddNegative(t *testing.T) {\n\tif Add(-1, -2) != -3 {\n\t\tt.Fatal()\n\t}\n}\n")
	ids["testonly"] = f.commit("test: cover negative Add")

	// c6: a new source file with a new test file.
	f.write("min.go", "package calc\n\n// Min returns the smaller of a and b.\nfunc Min(a, b int) int {\n\tif a < b {\n\t\treturn a\n\t}\n\treturn b\n}\n")
	f.write("min_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestMin(t *testing.T) {\n\tif Min(2, 5) != 2 {\n\t\tt.Fatal(\"Min\")\n\t}\n}\n")
	ids["min"] = f.commit("Add Min")

	// c7: the test added does not exercise the change: it passes on the parent.
	f.write("abs.go", "package calc\n\n// Abs returns |v|.\nfunc Abs(v int) int {\n\tif v < 0 {\n\t\treturn -v\n\t}\n\treturn v\n}\n")
	f.write("abs_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestAddAgain(t *testing.T) {\n\tif Add(2, 2) != 4 {\n\t\tt.Fatal()\n\t}\n}\n")
	ids["abs"] = f.commit("Add Abs")

	// c8: the commit's own test fails with the commit's own source.
	f.write("sub.go", "package calc\n\n// Sub returns a-b.\nfunc Sub(a, b int) int { return a - b }\n")
	f.write("sub_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestSubWrongExpectation(t *testing.T) {\n\tif Sub(5, 3) != 3 {\n\t\tt.Fatal(\"expected 3\")\n\t}\n}\n")
	ids["sub"] = f.commit("Add Sub")

	// c9: a revert (skipped).
	f.remove("min.go")
	f.remove("min_test.go")
	ids["revert"] = f.commit("Revert \"Add Min\"\n\nThis reverts commit " + ids["min"] + ".")

	// c10: too many files.
	for i := 0; i < 14; i++ {
		f.write(fmt.Sprintf("gen/g%02d.go", i), fmt.Sprintf("package gen\n\nconst C%d = %d\n", i, i))
	}
	f.write("gen/gen_test.go", "package gen\n\nimport \"testing\"\n\nfunc TestC0(t *testing.T) {\n\tif C0 != 0 {\n\t\tt.Fatal()\n\t}\n}\n")
	ids["big"] = f.commit("Add generated constants")
	return f, ids
}

func taskBySubject(tasks []rl.Task, subject string) *rl.Task {
	for i := range tasks {
		var m Meta
		if json.Unmarshal(tasks[i].Meta, &m) != nil {
			continue
		}
		if strings.HasPrefix(m.Subject, subject) {
			return &tasks[i]
		}
	}
	return nil
}

func TestFromGitMinesValidatesAndRejects(t *testing.T) {
	f, ids := buildCalcRepo(t)
	m := newManager(t)
	hidden := events.NewMemBlobs()
	gold := events.NewMemBlobs()
	tasks, rep, err := FromGit(context.Background(), f.Dir, GitOptions{
		Workspaces: m, HiddenBlobs: hidden, GoldBlobs: gold, Concurrency: 3, Timeout: time.Minute, IDPrefix: "calc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.ValidateTasks(tasks); err != nil {
		t.Fatalf("generated tasks do not validate: %v", err)
	}

	// ---- which candidates became tasks ----
	var subjects []string
	for _, tk := range tasks {
		var meta Meta
		if err := json.Unmarshal(tk.Meta, &meta); err != nil {
			t.Fatal(err)
		}
		subjects = append(subjects, meta.Subject)
	}
	sort.Strings(subjects)
	wantSubjects := []string{"Add Max", "Add Min", "Fix Clamp returning the lower bound above the range"}
	if !reflect.DeepEqual(subjects, wantSubjects) {
		t.Fatalf("tasks for %q, want %q\nreport: %+v", subjects, wantSubjects, rep)
	}
	if len(rep.InfraErrors) != 0 {
		t.Fatalf("infra errors: %+v", rep.InfraErrors)
	}
	if rep.Accepted != 3 || rep.License != "MIT" {
		t.Errorf("report: %+v", rep)
	}
	wantRejected := map[string]int{
		ReasonBaselinePasses: 1, // Add Abs
		ReasonGoldFails:      1, // Add Sub
		ReasonTooManyFiles:   1, // generated constants
	}
	if !reflect.DeepEqual(rep.Rejected, wantRejected) {
		t.Errorf("rejected = %v, want %v\n%+v", rep.Rejected, wantRejected, rep.Rejections)
	}
	// Reverts, docs-only and test-only commits are not even candidates.
	if rep.Candidates != 6 || rep.Examined < 9 {
		t.Errorf("candidates %d examined %d", rep.Candidates, rep.Examined)
	}

	// ---- the fix task in detail ----
	fix := taskBySubject(tasks, "Fix Clamp")
	if fix == nil {
		t.Fatal("no fix task")
	}
	if fix.ID != "calc-"+ids["clamp"][:8] || fix.Kind != rl.TaskFix || fix.Team.Mode != "single" {
		t.Errorf("identity: %s %s %+v", fix.ID, fix.Kind, fix.Team)
	}
	parent := f.git("rev-parse", ids["clamp"]+"^")
	if fix.Repo.Path != f.Dir || fix.Repo.Commit != parent || fix.Repo.License != "MIT" {
		t.Errorf("repo: %+v (want parent %s)", fix.Repo, parent)
	}
	if !strings.Contains(fix.Prompt, "Fix Clamp returning the lower bound") || !strings.Contains(fix.Prompt, "Fixes #7") ||
		strings.Contains(fix.Prompt, ids["clamp"]) || !strings.Contains(fix.Prompt, "cannot see") {
		t.Errorf("prompt:\n%s", fix.Prompt)
	}
	v := fix.Verifier
	if v.Cmd != "go test -count=1 -run '^(TestClampUpper)$' ." || v.Pass != "exit0" || v.TimeoutS != 60 {
		t.Errorf("verifier: %+v", v)
	}
	if !reflect.DeepEqual(fix.Setup, []string{"go mod download"}) {
		t.Errorf("setup: %v", fix.Setup)
	}
	if len(v.Hidden) != 1 {
		t.Fatalf("hidden: %v", v.Hidden)
	}
	ref, ok := v.Hidden["calc_test.go"]
	if !ok || !strings.HasPrefix(ref, "blob:") {
		t.Fatalf("hidden ref: %v", v.Hidden)
	}
	data, err := hidden.Get(core.Hash(strings.TrimPrefix(ref, "blob:")))
	if err != nil || !strings.Contains(string(data), "TestClampUpper") {
		t.Fatalf("hidden blob: %v", err)
	}
	if !contains(v.Protected, "/calc_test.go") || !contains(v.Protected, ".github/**") {
		t.Errorf("protected: %v", v.Protected)
	}
	var meta Meta
	if err := json.Unmarshal(fix.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Commit != ids["clamp"] || meta.AuthorDate != "2024-01-04T12:00:00Z" || meta.Generator != "taskgen/git" || meta.Lang != "go" ||
		!reflect.DeepEqual(meta.Files, []string{"calc.go", "calc_test.go"}) || meta.GoldBlob == "" || meta.GoldHash == "" || meta.Lines == 0 {
		t.Errorf("meta: %+v", meta)
	}
	if g, err := gold.Get(core.Hash(meta.GoldBlob)); err != nil || !strings.Contains(string(g), "calc.go") || strings.Contains(string(g), "calc_test.go") {
		t.Errorf("gold blob should hold the source change only: %v", err)
	}
	tags := strings.Join(fix.Tags, ",")
	for _, want := range []string{"go", "mined", "small", "calc"} {
		if !strings.Contains(tags, want) {
			t.Errorf("tags %v lack %q", fix.Tags, want)
		}
	}
	if fix.Budget.Steps < 40 || fix.Budget.Requests <= fix.Budget.Steps || fix.Budget.WallS < 900 {
		t.Errorf("budget: %+v", fix.Budget)
	}

	// The feature task starts from a parent where the new test does not even compile.
	max := taskBySubject(tasks, "Add Max")
	if max == nil || max.Kind != rl.TaskFeature || !strings.Contains(max.Verifier.Cmd, "TestMax") {
		t.Fatalf("feature task: %+v", max)
	}
	// The task with a new test file hides that file; the new source file is not hidden.
	minT := taskBySubject(tasks, "Add Min")
	if minT == nil || len(minT.Verifier.Hidden) != 1 || minT.Verifier.Hidden["min_test.go"] == "" {
		t.Fatalf("min task: %+v", minT)
	}

	// ---- the tasks really are runnable end to end ----
	// A fresh agent workspace lacks the fix; applying the stored reference solution
	// (which is all of the commit's source change) passes.
	var mm Meta
	if err := json.Unmarshal(fix.Meta, &mm); err != nil {
		t.Fatal(err)
	}
	g, _ := gold.Get(core.Hash(mm.GoldBlob))
	res, err := env.VerifyPatch(context.Background(), *fix, g, env.VerifyOptions{Workspaces: m, HiddenBlobs: hidden})
	if err != nil || !res.Pass {
		t.Fatalf("gold: %v %+v", err, res.Log)
	}
	res, err = env.VerifyBaseline(context.Background(), *fix, env.VerifyOptions{Workspaces: m, HiddenBlobs: hidden})
	if err != nil || res.Pass {
		t.Fatalf("baseline: %v", err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestFromGitDedupesIdenticalCommits(t *testing.T) {
	f := newFixture(t)
	f.write("go.mod", calcGoMod)
	f.write("calc.go", calcV1)
	f.write("calc_test.go", calcTestV1)
	f.commit("initial")
	base := f.head()
	fixSrc := strings.Replace(calcV1, "return lo // BUG: should return hi", "return hi", 1)
	testSrc := calcTestV1 + "\nfunc TestClampUpper(t *testing.T) {\n\tif Clamp(10, 0, 5) != 5 {\n\t\tt.Fatal()\n\t}\n}\n"
	f.write("calc.go", fixSrc)
	f.write("calc_test.go", testSrc)
	f.commit("Fix Clamp")
	// The same change made on a branch from the same parent (a cherry-pick),
	// merged back: two distinct non-merge commits with identical content.
	f.git("checkout", "-q", "-b", "dup", base)
	f.write("calc.go", fixSrc)
	f.write("calc_test.go", testSrc)
	f.commit("Fix Clamp (cherry-picked)")
	f.git("checkout", "-q", "main")
	f.git("merge", "-q", "--no-edit", "dup")

	tasks, rep, err := FromGit(context.Background(), f.Dir, GitOptions{NoValidate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || rep.Rejected[ReasonDuplicate] != 1 {
		t.Fatalf("%d tasks, report %+v", len(tasks), rep)
	}
}

func TestFromGitFilters(t *testing.T) {
	f, ids := buildCalcRepo(t)
	run := func(o GitOptions) ([]rl.Task, *Report, error) {
		o.NoValidate = true
		return FromGit(context.Background(), f.Dir, o)
	}
	subjects := func(ts []rl.Task) []string {
		var out []string
		for _, tk := range ts {
			var m Meta
			if err := json.Unmarshal(tk.Meta, &m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m.Subject)
		}
		sort.Strings(out)
		return out
	}

	t.Run("max stops early, newest first", func(t *testing.T) {
		tasks, rep, err := run(GitOptions{Max: 2})
		if err != nil || len(tasks) != 2 {
			t.Fatal(err, len(tasks))
		}
		// Newest candidates: the generated-constants commit is rejected (too many
		// files), then Sub, Abs, Min...
		var m Meta
		if err := json.Unmarshal(tasks[0].Meta, &m); err != nil {
			t.Fatal(err)
		}
		if m.Commit != ids["sub"] {
			t.Errorf("first task is %s, want the newest acceptable commit %s", m.Commit, ids["sub"])
		}
		if rep.Accepted != 2 {
			t.Errorf("accepted %d", rep.Accepted)
		}
	})
	t.Run("file and line limits", func(t *testing.T) {
		tasks, rep, _ := run(GitOptions{MaxFiles: 2})
		for _, tk := range tasks {
			var m Meta
			if err := json.Unmarshal(tk.Meta, &m); err != nil {
				t.Fatal(err)
			}
			if len(m.Files) > 2 {
				t.Errorf("task with %d files", len(m.Files))
			}
		}
		if rep.Rejected[ReasonTooManyFiles] == 0 {
			t.Errorf("nothing rejected for size: %+v", rep.Rejected)
		}
		_, rep, _ = run(GitOptions{MaxLines: 5})
		if rep.Rejected[ReasonTooManyLines] == 0 {
			t.Errorf("nothing rejected for lines: %+v", rep.Rejected)
		}
	})
	t.Run("date range", func(t *testing.T) {
		since := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC) // commit 4 (clamp) and later
		until := time.Date(2024, 1, 4, 23, 0, 0, 0, time.UTC)
		tasks, _, err := run(GitOptions{Since: since, Until: until})
		if err != nil || !reflect.DeepEqual(subjects(tasks), []string{"Fix Clamp returning the lower bound above the range"}) {
			t.Fatalf("%v %v", subjects(tasks), err)
		}
	})
	t.Run("licence allow list", func(t *testing.T) {
		if _, _, err := run(GitOptions{AllowLicenses: []string{"Apache-2.0", "BSD-3-Clause"}}); !errors.Is(err, ErrLicense) {
			t.Fatalf("MIT repo under an Apache-only policy: %v", err)
		}
		if tasks, _, err := run(GitOptions{AllowLicenses: []string{"mit"}}); err != nil || len(tasks) == 0 {
			t.Fatalf("%v", err)
		}
		// A repository without a recognisable licence is not allowed when a list is set.
		g := newFixture(t)
		g.write("x.go", "package x\n")
		g.commit("init")
		if _, _, err := FromGit(context.Background(), g.Dir, GitOptions{NoValidate: true, AllowLicenses: []string{"MIT"}}); !errors.Is(err, ErrLicense) {
			t.Fatalf("unlicensed repo: %v", err)
		}
	})
	t.Run("language filter", func(t *testing.T) {
		tasks, _, err := run(GitOptions{Languages: []string{"python"}})
		if err != nil || len(tasks) != 0 {
			t.Fatalf("%d tasks for python in a Go repo (%v)", len(tasks), err)
		}
		tasks, _, _ = run(GitOptions{Languages: []string{"go"}})
		if len(tasks) == 0 {
			t.Fatal("go filter removed everything")
		}
	})
	t.Run("rev", func(t *testing.T) {
		tasks, _, err := run(GitOptions{Rev: ids["clamp"]})
		if err != nil || !reflect.DeepEqual(subjects(tasks), []string{"Add Max", "Fix Clamp returning the lower bound above the range"}) {
			t.Fatalf("%v %v", subjects(tasks), err)
		}
	})
	t.Run("custom test command and setup", func(t *testing.T) {
		tasks, _, err := run(GitOptions{Rev: ids["clamp"], Max: 1, TestCmd: "go test -count=1 -v {dirs}", Setup: []string{}})
		if err != nil || len(tasks) != 1 || tasks[0].Verifier.Cmd != "go test -count=1 -v '.'" || len(tasks[0].Setup) != 0 {
			t.Fatalf("%+v %v", tasks, err)
		}
	})
	t.Run("errors", func(t *testing.T) {
		if _, _, err := FromGit(context.Background(), f.Dir, GitOptions{}); err == nil {
			t.Error("validation without a manager accepted")
		}
		if _, _, err := run(GitOptions{Rev: "no-such-rev"}); err == nil {
			t.Error("bad rev accepted")
		}
		if _, _, err := FromGit(context.Background(), t.TempDir(), GitOptions{NoValidate: true}); err == nil {
			t.Error("non-repository accepted")
		}
		sub := filepath.Join(f.Dir, "gen")
		if _, _, err := FromGit(context.Background(), sub, GitOptions{NoValidate: true}); err == nil {
			t.Error("a subdirectory of a repository is not a repository root")
		}
	})
}

func TestFromGitInlineHiddenWithoutBlobStore(t *testing.T) {
	f, _ := buildCalcRepo(t)
	tasks, _, err := FromGit(context.Background(), f.Dir, GitOptions{NoValidate: true, Max: 1, Rev: "HEAD~4"})
	if err != nil || len(tasks) != 1 {
		t.Fatal(err, len(tasks))
	}
	for name, ref := range tasks[0].Verifier.Hidden {
		if !strings.HasPrefix(ref, "text:") || !strings.Contains(ref, "package calc") {
			t.Errorf("%s: %q", name, ref[:min(40, len(ref))])
		}
	}
	// A binary fixture cannot be inlined: the candidate is rejected with a reason.
	g := newFixture(t)
	g.write("go.mod", calcGoMod)
	g.write("calc.go", calcV1)
	g.write("calc_test.go", calcTestV1)
	g.commit("initial")
	g.write("calc.go", calcV1+"\nfunc Z() {}\n")
	g.write("calc_test.go", calcTestV1+"\nfunc TestZ(t *testing.T) { Z() }\n")
	if err := os.MkdirAll(filepath.Join(g.Dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.Dir, "testdata", "blob.bin"), []byte{0, 1, 2, 0xff, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	g.commit("Add Z with a binary fixture")
	_, rep, _ := FromGit(context.Background(), g.Dir, GitOptions{NoValidate: true})
	if rep.Rejected[ReasonBinaryTest] != 1 {
		t.Fatalf("%+v", rep.Rejected)
	}
	// With a blob store the same commit is fine.
	tasks, _, err = FromGit(context.Background(), g.Dir, GitOptions{NoValidate: true, HiddenBlobs: events.NewMemBlobs()})
	if err != nil || len(tasks) != 1 || len(tasks[0].Verifier.Hidden) != 2 {
		t.Fatalf("%v %+v", err, tasks)
	}
}

func TestFromGitDeletedTestsAreRemovedByTheVerifier(t *testing.T) {
	f := newFixture(t)
	f.write("go.mod", calcGoMod)
	f.write("calc.go", calcV1)
	f.write("old_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) { _ = Add(1, 1) }\n")
	f.commit("initial")
	// The commit moves the test to a new file (same function name!) and adds
	// behaviour: keeping the old file would make the package fail to compile.
	f.remove("old_test.go")
	f.write("calc.go", calcV1+"\nfunc Twice(v int) int { return v * 2 }\n")
	f.write("new_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) { _ = Add(1, 1) }\n\nfunc TestTwice(t *testing.T) {\n\tif Twice(2) != 4 {\n\t\tt.Fatal()\n\t}\n}\n")
	f.commit("Add Twice and move the tests")
	m := newManager(t)
	tasks, rep, err := FromGit(context.Background(), f.Dir, GitOptions{Workspaces: m, Timeout: time.Minute})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("%v %d %+v", err, len(tasks), rep)
	}
	cmd := tasks[0].Verifier.Cmd
	if !strings.HasPrefix(cmd, "rm -f -- 'old_test.go' && go test") {
		t.Errorf("cmd: %s", cmd)
	}
	if !contains(tasks[0].Verifier.Protected, "/old_test.go") {
		t.Errorf("protected: %v", tasks[0].Verifier.Protected)
	}
}

func TestFromGitPython(t *testing.T) {
	havePytest(t)
	f := newFixture(t)
	f.write("mathx.py", "def clamp(v, lo, hi):\n    if v < lo:\n        return lo\n    return v\n")
	f.write("test_mathx.py", "from mathx import clamp\n\ndef test_low():\n    assert clamp(-1, 0, 5) == 0\n")
	f.write("LICENSE", mitLicense)
	f.commit("initial")
	f.write("mathx.py", "def clamp(v, lo, hi):\n    if v < lo:\n        return lo\n    if v > hi:\n        return hi\n    return v\n")
	f.write("test_mathx.py", "from mathx import clamp\n\ndef test_low():\n    assert clamp(-1, 0, 5) == 0\n\ndef test_high():\n    assert clamp(9, 0, 5) == 5\n")
	f.commit("Fix clamp above the range")
	m := newManager(t)
	tasks, rep, err := FromGit(context.Background(), f.Dir, GitOptions{Workspaces: m, Timeout: time.Minute})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("%v %d %+v", err, len(tasks), rep)
	}
	tk := tasks[0]
	if !strings.Contains(tk.Verifier.Cmd, "-m pytest") || !strings.Contains(tk.Verifier.Cmd, "'test_mathx.py'") || len(tk.Setup) != 0 {
		t.Errorf("cmd %q setup %v", tk.Verifier.Cmd, tk.Setup)
	}
	if !contains(tk.Verifier.Protected, "conftest.py") || !contains(tk.Verifier.Protected, "/test_mathx.py") {
		t.Errorf("protected: %v", tk.Verifier.Protected)
	}
}

func TestPlanners(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write("package.json", `{"name":"x","scripts":{"test":"jest"}}`)
	f.write("package-lock.json", "{}")
	f.write("web/package.json", `{"name":"y"}`)
	f.write("Cargo.toml", "[package]\nname=\"x\"\n")
	f.write("crates/inner/Cargo.toml", "[package]\nname=\"inner\"\n")
	f.write("pom.xml", "<project/>")
	f.write("svc/go.mod", "module example.com/svc\n\ngo 1.24\n")
	f.write("svc/api/api.go", "package api\n")
	f.commit("layout")
	m := newManager(t)
	r := &repo{g: m.Git(), gitDir: filepath.Join(f.Dir, ".git")}
	hf := func(p string, k Kind, l string, data string) hiddenFile {
		return hiddenFile{Path: p, Kind: k, Lang: l, Data: []byte(data)}
	}

	t.Run("js", func(t *testing.T) {
		p, err := planJS(ctx, r, "HEAD", []hiddenFile{hf("src/a.test.js", Test, JS, "")})
		if err != nil || p.Cmd != "npm test --silent -- 'src/a.test.js'" || !reflect.DeepEqual(p.Setup, []string{"npm ci --no-audit --no-fund"}) {
			t.Fatalf("%+v %v", p, err)
		}
	})
	t.Run("rust integration", func(t *testing.T) {
		p, err := planRust(ctx, r, "HEAD", []hiddenFile{hf("tests/b.rs", Test, Rust, ""), hf("tests/a.rs", Test, Rust, "")})
		if err != nil || p.Cmd != "cargo test --offline --test 'a' --test 'b'" {
			t.Fatalf("%+v %v", p, err)
		}
		p, err = planRust(ctx, r, "HEAD", []hiddenFile{hf("crates/inner/tests/x.rs", Test, Rust, "")})
		if err != nil || !strings.HasPrefix(p.Cmd, "cd 'crates/inner' && cargo test --offline --test 'x'") {
			t.Fatalf("%+v %v", p, err)
		}
		p, err = planRust(ctx, r, "HEAD", []hiddenFile{hf("src/tests.rs", Test, Rust, "")})
		if err != nil || p.Cmd != "cargo test --offline" {
			t.Fatalf("inline unit tests run the whole suite: %+v %v", p, err)
		}
	})
	t.Run("java", func(t *testing.T) {
		p, err := planJava(ctx, r, "HEAD", []hiddenFile{hf("src/test/java/BTest.java", Test, Java, ""), hf("src/test/java/ATest.java", Test, Java, "")})
		if err != nil || !strings.Contains(p.Cmd, "-Dtest='ATest,BTest'") || !strings.HasPrefix(p.Cmd, "mvn -q -B -o test") {
			t.Fatalf("%+v %v", p, err)
		}
	})
	t.Run("go nested module", func(t *testing.T) {
		p, err := planGo(ctx, r, "HEAD", "HEAD", []hiddenFile{hf("svc/api/api_test.go", Test, Go, "package api\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")})
		if err != nil || p.Cmd != "cd 'svc' && go test -count=1 -run '^(TestX)$' ./api" || p.Setup[0] != "cd 'svc' && go mod download" {
			t.Fatalf("%+v %v", p, err)
		}
		if _, err := planGo(ctx, r, "HEAD", "HEAD", []hiddenFile{
			hf("svc/api/a_test.go", Test, Go, "package api"), hf("other/b_test.go", Test, Go, "package other"),
		}); err == nil {
			t.Error("tests in two modules accepted")
		}
	})
	t.Run("errors", func(t *testing.T) {
		g := newFixture(t)
		g.write("x.txt", "x")
		g.commit("i")
		rr := &repo{g: m.Git(), gitDir: filepath.Join(g.Dir, ".git")}
		if _, err := planJS(ctx, rr, "HEAD", []hiddenFile{hf("a.test.js", Test, JS, "")}); err == nil {
			t.Error("no package.json")
		}
		if _, err := planJava(ctx, rr, "HEAD", []hiddenFile{hf("ATest.java", Test, Java, "")}); err == nil {
			t.Error("no build file")
		}
		if _, err := planPython([]hiddenFile{hf("conftest.py", Test, Python, "")}); err == nil {
			t.Error("conftest only")
		}
		if _, err := planTests(ctx, rr, "HEAD", "HEAD", []hiddenFile{hf("a_test.go", Test, Go, ""), hf("test_b.py", Test, Python, "")}, nil, "", nil, false); err == nil {
			t.Error("mixed languages accepted")
		}
		if _, err := planTests(ctx, rr, "HEAD", "HEAD", []hiddenFile{hf("fixture.json", TestSupport, "", "")}, nil, "", nil, false); err == nil {
			t.Error("no test files accepted")
		}
	})
}
