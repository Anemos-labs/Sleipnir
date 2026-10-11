package taskgen

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// chainRepo is a repository whose history after "initial" is three merged pull requests, each adding a function and
// its test in a package of its own: a merge commit, a squash commit that also moves the root package's test to a new
// file (deleting the old one), and a merge commit that also edits the README. Non-test lines: 4, 8 and 6.
type chainRepo struct {
	f                      *fixture
	initial, pr1, pr2, pr3 string
}

// buildChainRepo makes the repository of the chain tests.
func buildChainRepo(t *testing.T) *chainRepo {
	t.Helper()
	f := newFixture(t)
	addTest := "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add\")\n\t}\n}\n"
	f.write("go.mod", "module example.com/chain\n\ngo 1.21\n")
	f.write("LICENSE", mitLicense)
	f.write("calc.go", "package calc\n\n// Add returns a+b.\nfunc Add(a, b int) int { return a + b }\n")
	f.write("add_old_test.go", addTest)
	c := &chainRepo{f: f, initial: f.commit("initial")}

	f.git("checkout", "-q", "-b", "sub")
	f.write("sub/sub.go", "package sub\n\n// Sub returns a-b.\nfunc Sub(a, b int) int { return a - b }\n")
	f.write("sub/sub_test.go", "package sub\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n\tif Sub(5, 3) != 2 {\n\t\tt.Fatal(\"Sub\")\n\t}\n}\n")
	f.commit("sub: add Sub")
	f.git("checkout", "-q", "main")
	f.git("merge", "-q", "--no-ff", "-m", "Merge pull request #1 from dev/sub\n\nAdd Sub, the difference of two numbers", "sub")
	c.pr1 = f.head()

	f.write("mul/mul.go", "package mul\n\n// Mul returns a*b.\nfunc Mul(a, b int) int {\n\tp := a\n\tp *= b\n\treturn p\n}\n")
	f.write("mul/mul_test.go", "package mul\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"Mul\")\n\t}\n}\n")
	f.remove("add_old_test.go")
	f.write("add_test.go", addTest) // the same test function, moved: both files together do not compile
	c.pr2 = f.commit("Add Mul (#2)\n\nMul multiplies two numbers.")

	f.git("checkout", "-q", "-b", "div")
	f.write("div/div.go", "package div\n\n// Div returns a/b.\nfunc Div(a, b int) int { return a / b }\n")
	f.write("div/div_test.go", "package div\n\nimport \"testing\"\n\nfunc TestDiv(t *testing.T) {\n\tif Div(6, 3) != 2 {\n\t\tt.Fatal(\"Div\")\n\t}\n}\n")
	f.write("README.md", "# chain\nAdd, Sub, Mul and Div.\n")
	f.commit("div: add Div")
	f.git("checkout", "-q", "main")
	f.git("merge", "-q", "--no-ff", "-m", "Merge pull request #3 from dev/div\n\nAdd Div", "div")
	c.pr3 = f.head()
	return c
}

func TestFromChainsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("records go test at four states")
	}
	havePython(t)
	have(t, "go")
	c := buildChainRepo(t)
	ctx := context.Background()
	m := newManager(t)
	hidden := events.NewMemBlobs()
	seal := filepath.Join(t.TempDir(), "repos")
	var progress []string
	chain := Chain{ID: "arith", Commits: []string{c.pr1, c.pr2, c.pr3}, Issues: []string{"", "Multiplication is missing\n\nWe need Mul(a, b).<!-- template -->"},
		Source: map[string]string{"prs": "1,2,3"}}
	tasks, rep, err := FromChains(ctx, c.f.Dir, []Chain{chain}, ChainOptions{
		Workspaces: m, Repeats: 2, DeadlineS: 300, HiddenBlobs: hidden, GoldBlobs: hidden, IDPrefix: "calc", Seal: seal,
		Progress: func(s string) { progress = append(progress, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || rep.Accepted != 1 || len(rep.Chains) != 1 || !rep.Chains[0].OK {
		t.Fatalf("%d tasks\nreport: %+v\nprogress: %v", len(tasks), rep, progress)
	}
	task := tasks[0]

	// The task.
	if task.ID != "calc-arith" || task.Kind != rl.TaskSwarm || !reflect.DeepEqual(task.Team, rl.Team{Mode: "swarm", Agents: 2}) {
		t.Errorf("identity: %s %s %+v", task.ID, task.Kind, task.Team)
	}
	if task.Budget.WallS != 6*3600+18*36/10 || task.Budget.Steps != 300 || task.Budget.Requests != 750 {
		t.Errorf("budget: %+v", task.Budget)
	}
	for _, tag := range []string{"long", "chain", "harvested", "go", "calc"} {
		if !contains(task.Tags, tag) {
			t.Errorf("tags %v lack %s", task.Tags, tag)
		}
	}
	wantPrompt := []string{
		"This task is a series of 3 milestones",
		"Milestone 1:\nAdd Sub, the difference of two numbers\n\nMilestone 2:\nMultiplication is missing\n\nWe need Mul(a, b).\n\nMilestone 3:\nAdd Div\n\n",
		"Do not edit existing test files.",
	}
	for _, w := range wantPrompt {
		if !strings.Contains(task.Prompt, w) {
			t.Errorf("the prompt lacks %q:\n%s", w, task.Prompt)
		}
	}
	if strings.Contains(task.Prompt, "Merge pull request") || strings.Contains(task.Prompt, "Mul multiplies") {
		t.Errorf("prompt:\n%s", task.Prompt)
	}

	// Provenance.
	var meta Meta
	if err := json.Unmarshal(task.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Generator != "taskgen/chain" || meta.Parent != c.initial || meta.Commit != c.pr3 || !reflect.DeepEqual(meta.Commits, []string{c.pr1, c.pr2, c.pr3}) {
		t.Errorf("meta: %+v", meta)
	}
	if want := map[string]string{"repo": filepath.Base(c.f.Dir), "range": c.initial + ".." + c.pr3, "license": "MIT", "prs": "1,2,3"}; !reflect.DeepEqual(meta.Source, want) {
		t.Errorf("meta.source: %v, want %v", meta.Source, want)
	}

	// The sealed start: one commit, the initial tree without the test file the chain deletes.
	if task.Repo.Path != filepath.Join(seal, "calc-arith.git") || task.Repo.License != "MIT" {
		t.Fatalf("repo: %+v", task.Repo)
	}
	in := func(args ...string) string {
		return c.f.git(append([]string{"--git-dir=" + task.Repo.Path}, args...)...)
	}
	if in("rev-list", "--all") != task.Repo.Commit || in("ls-tree", "--name-only", task.Repo.Commit) != "LICENSE\ncalc.go\ngo.mod" {
		t.Errorf("sealed start: %q", in("ls-tree", "--name-only", task.Repo.Commit))
	}

	// The verifier: the end's tests hidden, the deleted one removed before the suite runs, every test file protected.
	for _, p := range []string{"add_test.go", "sub/sub_test.go", "mul/mul_test.go", "div/div_test.go"} {
		if _, ok := task.Verifier.Hidden[p]; !ok {
			t.Errorf("%s is not hidden: %v", p, keysOfStr(task.Verifier.Hidden))
		}
	}
	for _, p := range []string{"*_test.go", "/add_old_test.go", ".sleipnir/**"} {
		if !contains(task.Verifier.Protected, p) {
			t.Errorf("%s is not protected: %v", p, task.Verifier.Protected)
		}
	}
	if task.Verifier.Pass != "json-score" || task.Verifier.Cmd != "python3 .sleipnir/suite/score.py" || !contains(task.Requires, "go") {
		t.Errorf("verifier: %+v requires %v", task.Verifier, task.Requires)
	}
	raw, err := hidden.Get(core.Hash(strings.TrimPrefix(task.Verifier.Hidden[".sleipnir/suite/spec.json"], "blob:")))
	if err != nil {
		t.Fatal(err)
	}
	var spec SuiteSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	wantUnits := []SuiteUnit{
		{Name: "milestone-1", Weight: 4, Tests: []string{"example.com/chain/sub.TestSub"}},
		{Name: "milestone-2", Weight: 8, Tests: []string{"example.com/chain/mul.TestMul"}},
		{Name: "milestone-3", Weight: 6, Tests: []string{"example.com/chain/div.TestDiv"}},
	}
	if !reflect.DeepEqual(spec.Units, wantUnits) || !reflect.DeepEqual(spec.Guards, []string{"example.com/chain.TestAdd"}) {
		t.Errorf("units %+v\nguards %v", spec.Units, spec.Guards)
	}
	if len(spec.Commands) != 1 || !strings.HasPrefix(spec.Commands[0].Run, "rm -f -- 'add_old_test.go'; ") {
		t.Errorf("commands: %+v", spec.Commands)
	}

	// The self-check: 0, then 4/18, 12/18 and 1.
	check := rep.Chains[0]
	want := []float64{0, 4.0 / 18, 12.0 / 18, 1}
	for k := range want {
		if math.Abs(check.Want[k]-want[k]) > 1e-9 || math.Abs(check.Got[k]-want[k]) > 1e-6 {
			t.Errorf("state %d: want %v, got %v (expected %v)", k, check.Want[k], check.Got[k], want[k])
		}
	}
	if check.Units != 3 || check.Guards != 1 || !strings.Contains(check.Summary(), "scores 0.000 0.222 0.667 1.000") {
		t.Errorf("check: %+v %s", check, check.Summary())
	}

	// What `rl tasks check` runs: the start fails with score 0, the reference solution passes.
	gold, err := hidden.Get(core.Hash(meta.GoldBlob))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gold), "_test.go") {
		t.Errorf("the reference solution touches tests:\n%s", gold)
	}
	if r, err := env.CheckTask(ctx, task, gold, env.VerifyOptions{Workspaces: m, HiddenBlobs: hidden, Repeats: 2}); err != nil || r.Gold.Score != 1 || r.Baseline.Score != 0 {
		t.Fatalf("CheckTask: %v (start %v, reference %v)", err, r.Baseline.Score, r.Gold.Score)
	}
}

// TestFromChainsRejects: chains that are not consecutive, name unknown commits or have no runnable test are reported
// with their reasons, and nothing is recorded for them.
func TestFromChainsRejects(t *testing.T) {
	c := buildChainRepo(t)
	f := c.f
	// A chain on a Rust-free, Go-free test: only a JavaScript test changes.
	f.write("web/app.js", "module.exports = 1\n")
	f.write("web/app.test.js", "test('x', () => {})\n")
	js1 := f.commit("web: app")
	f.write("web/app.js", "module.exports = 2\n")
	f.write("web/app.test.js", "test('y', () => {})\n")
	js2 := f.commit("web: app 2")
	f.write("web/app.js", "module.exports = 3\n")
	f.write("web/app.test.js", "test('z', () => {})\n")
	js3 := f.commit("web: app 3")
	chains := []Chain{
		{ID: "gap", Commits: []string{c.pr1, c.pr3, js1}},
		{ID: "unknown", Commits: []string{c.pr1, "no-such-commit", c.pr3}},
		{ID: "js", Commits: []string{js1, js2, js3}},
		{ID: "short", Commits: []string{c.pr1, c.pr2}},
		{ID: "gap", Commits: []string{c.pr1, c.pr2, c.pr3}},
	}
	tasks, rep, err := FromChains(context.Background(), f.Dir, chains, ChainOptions{Workspaces: newManager(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 || rep.Examined != 5 || len(rep.Chains) != 0 || len(rep.InfraErrors) != 0 {
		t.Fatalf("%d tasks, report %+v", len(tasks), rep)
	}
	want := map[string]string{"gap": ReasonChainShape, "unknown": ReasonUnknownCommit, "js": ReasonNoTestPlan, "short": ReasonChainShape}
	got := map[string]string{}
	for _, r := range rep.Rejections {
		if _, seen := got[r.Chain]; !seen {
			got[r.Chain] = r.Reason
		}
	}
	if !reflect.DeepEqual(got, want) || rep.Rejected[ReasonDuplicate] != 1 {
		t.Errorf("rejections %v, want %v\n%+v", got, want, rep.Rejections)
	}
	if _, _, err := FromChains(context.Background(), f.Dir, chains, ChainOptions{}); err == nil {
		t.Error("chains without workspaces were accepted")
	}
}

func TestReadChains(t *testing.T) {
	good := "# chains\n\n" + `{"id": "a", "commits": ["c1", "c2", "c3"], "issues": ["first"], "source": {"pr": "x"}}` + "\n" +
		`{"id": "b-2.x", "commits": ["d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"]}` + "\n"
	chains, err := ReadChains(strings.NewReader(good))
	if err != nil || len(chains) != 2 || chains[0].Issues[0] != "first" || chains[0].Source["pr"] != "x" || len(chains[1].Commits) != 8 {
		t.Fatalf("%v %+v", err, chains)
	}
	for name, line := range map[string]string{
		"unknown field":   `{"id": "a", "commits": ["c1", "c2", "c3"], "parent": "x"}`,
		"no id":           `{"commits": ["c1", "c2", "c3"]}`,
		"bad id":          `{"id": "a/b", "commits": ["c1", "c2", "c3"]}`,
		"too few":         `{"id": "a", "commits": ["c1", "c2"]}`,
		"too many":        `{"id": "a", "commits": ["1", "2", "3", "4", "5", "6", "7", "8", "9"]}`,
		"option commit":   `{"id": "a", "commits": ["c1", "--all", "c3"]}`,
		"too many issues": `{"id": "a", "commits": ["c1", "c2", "c3"], "issues": ["", "", "", ""]}`,
		"duplicate id":    `{"id": "a", "commits": ["c1", "c2", "c3"]}` + "\n" + `{"id": "a", "commits": ["d1", "d2", "d3"]}`,
		"not json":        `c1 c2 c3`,
		"empty":           "# nothing\n",
	} {
		if _, err := ReadChains(strings.NewReader(line)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestBuildStepUnits: a test belongs to the step from which it passes in every run, for good; one that passes
// everywhere is a guard; flaky, regressing and never-passing tests are dropped; a step that makes nothing pass has no
// unit.
func TestBuildStepUnits(t *testing.T) {
	both := func(o SuiteOutcomes) []SuiteOutcomes { return []SuiteOutcomes{o, o} }
	steps := SuiteSteps{
		Runs: [][]SuiteOutcomes{
			both(SuiteOutcomes{"g": "pass", "a": "fail", "regress": "fail", "never": "fail", "skipped": "skip"}),
			{{"g": "pass", "a": "pass", "regress": "pass", "flaky": "pass", "never": "fail"}, {"g": "pass", "a": "pass", "regress": "pass", "flaky": "fail", "never": "fail"}},
			both(SuiteOutcomes{"g": "pass", "a": "pass", "regress": "fail", "flaky": "pass", "never": "fail"}),
			both(SuiteOutcomes{"g": "pass", "a": "pass", "regress": "pass", "flaky": "pass", "late": "pass", "skipped": "pass", "never": "fail"}),
		},
		Origin: map[string]string{"g": "go", "a": "go", "late": "go"},
	}
	ms := []SuiteMilestone{{"m1", 3}, {"m2", 5}, {"m3", 2}}
	sel, err := BuildStepUnits(steps, ms)
	if err != nil {
		t.Fatal(err)
	}
	want := []SuiteUnit{{Name: "m1", Weight: 3, Tests: []string{"a"}}, {Name: "m3", Weight: 2, Tests: []string{"late", "skipped"}}}
	if !reflect.DeepEqual(sel.Units, want) || !reflect.DeepEqual(sel.Guards, []string{"g"}) ||
		!reflect.DeepEqual(sel.Dropped, []string{"flaky", "never", "regress"}) || len(sel.Origin) != 3 {
		t.Errorf("selection: %+v", sel)
	}
	for name, bad := range map[string]func() (SuiteSelection, error){
		"one step":        func() (SuiteSelection, error) { return BuildStepUnits(SuiteSteps{Runs: steps.Runs[:1]}, nil) },
		"milestone count": func() (SuiteSelection, error) { return BuildStepUnits(steps, ms[:2]) },
		"nothing to score": func() (SuiteSelection, error) {
			return BuildStepUnits(SuiteSteps{Runs: [][]SuiteOutcomes{both(SuiteOutcomes{"g": "pass"}), both(SuiteOutcomes{"g": "pass"})}}, ms[:1])
		},
		"a step without runs": func() (SuiteSelection, error) {
			return BuildStepUnits(SuiteSteps{Runs: [][]SuiteOutcomes{steps.Runs[0], nil}}, ms[:1])
		},
		"duplicate names": func() (SuiteSelection, error) {
			return BuildStepUnits(steps, []SuiteMilestone{{"m", 1}, {"x", 1}, {"m", 1}})
		},
	} {
		if _, err := bad(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestChainPromptAndHelpers(t *testing.T) {
	if got := chainMessage("Merge pull request #12 from dev/x\n\nAdd the parser\n\nSigned-off-by: A <a@example.com>\n"); got != "Add the parser" {
		t.Errorf("merge message: %q", got)
	}
	if got := chainMessage("Merge branch 'x'\n"); got != "Merge branch 'x'" {
		t.Errorf("a bare merge message: %q", got)
	}
	if got := chainMessage("Add the parser (#12)\n\n* wip\n"); got != "Add the parser (#12)\n\n* wip" {
		t.Errorf("squash message: %q", got)
	}
	p := BuildChainPrompt([]string{"one", " two\n"})
	if !strings.HasPrefix(p, "This task is a series of 2 milestones") || !strings.Contains(p, "Milestone 1:\none\n\nMilestone 2:\ntwo\n\nYour work") {
		t.Errorf("prompt:\n%s", p)
	}
	if long := capText(strings.Repeat("line\n", 10000), maxIssueBytes, "[cut]"); len(long) > maxIssueBytes+10 || !strings.HasSuffix(long, "line\n\n[cut]") {
		t.Errorf("capText: %d bytes, ends %q", len(long), long[len(long)-20:])
	}
	for _, tc := range []struct {
		prs, lines, wall, agents int
	}{{3, 0, 6 * 3600, 2}, {5, 1000, 6*3600 + 2*2700 + 3600, 4}, {8, 100000, 12 * 3600, 6}} {
		b := chainBudget(tc.prs, tc.lines, rl.Budget{})
		if b.WallS != tc.wall || b.Steps < 300 || b.Steps > 1500 || b.Requests != b.Steps*5/2 || clamp(tc.prs-1, 2, 6) != tc.agents {
			t.Errorf("chainBudget(%d, %d) = %+v", tc.prs, tc.lines, b)
		}
	}
	if b := chainBudget(3, 0, rl.Budget{WallS: 60, Steps: 7}); b.WallS != 60 || b.Steps != 7 || b.Requests != 17 {
		t.Errorf("override: %+v", b)
	}
}

// TestChainSuiteCommands: a command per Go module and cargo package, one Python project, and the deleted test files
// removed relative to each command's directory.
func TestChainSuiteCommands(t *testing.T) {
	f := newFixture(t)
	f.write("go.mod", "module example.com/a\n")
	f.write("svc/go.mod", "module example.com/svc\n")
	f.write("py/pyproject.toml", "[tool.pytest.ini_options]\naddopts = \"-q\"\n")
	f.write("rs/Cargo.toml", "[package]\nname = \"rs\"\n")
	rev := f.commit("layout")
	r := &repo{g: newGit(t), gitDir: filepath.Join(f.Dir, ".git")}
	hidden := []hiddenFile{
		{Path: "a_test.go", Kind: Test, Lang: Go}, {Path: "svc/deep/x_test.go", Kind: Test, Lang: Go},
		{Path: "py/tests/test_a.py", Kind: Test, Lang: Python}, {Path: "py/tests/conftest.py", Kind: Test, Lang: Python},
		{Path: "rs/tests/it.rs", Kind: Test, Lang: Rust}, {Path: "web/a.test.js", Kind: Test, Lang: JS},
		{Path: "testdata/x.txt", Kind: TestSupport},
	}
	spec, setup, langs, err := chainSuite(context.Background(), r, rev, hidden, []string{"old_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	var names, runs []string
	for _, c := range spec.Commands {
		names = append(names, c.Name)
		runs = append(runs, c.Run[:strings.Index(c.Run, ";")+1])
	}
	if !reflect.DeepEqual(names, []string{"go", "go:svc", "pytest:py", "cargo:rs"}) ||
		!reflect.DeepEqual(runs, []string{"rm -f -- 'old_test.go';", "rm -f -- '../old_test.go';", "rm -f -- '../old_test.go';", "rm -f -- '../old_test.go';"}) {
		t.Errorf("commands %v\nruns %v", names, runs)
	}
	if !reflect.DeepEqual(setup, []string{"go mod download", "cd 'svc' && go mod download", "cd 'rs' && cargo fetch"}) ||
		!reflect.DeepEqual(langs, []string{"go", "python", "rust"}) || spec.PytestINI != "[pytest]\naddopts = -q\n" {
		t.Errorf("setup %v langs %v ini %q", setup, langs, spec.PytestINI)
	}
	if err := spec.Validate(false); err != nil {
		t.Error(err)
	}
	f.write("py2/setup.py", "")
	rev = f.commit("a second Python project")
	if _, _, _, err := chainSuite(context.Background(), r, rev, append(hidden, hiddenFile{Path: "py2/test_b.py", Kind: Test, Lang: Python}), nil); err == nil {
		t.Error("tests in two Python projects were accepted")
	}
}

// keysOfStr returns a map's keys in order.
func keysOfStr(m map[string]string) []string {
	set := map[string]bool{}
	for k := range m {
		set[k] = true
	}
	return keysOf(set)
}
