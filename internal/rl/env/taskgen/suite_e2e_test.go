package taskgen

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// End to end: a repository whose last commit is the start and whose previous one the reference, made into a suite task
// with WriteSuite, recorded with RecordSuite, given units by BuildUnits and proven sound by env.CheckTask (what `rl tasks
// check` runs), then scored as rollouts are: in a workspace, through env.Verify.

// suiteE2E is a suite task ready to be verified.
type suiteE2E struct {
	task   rl.Task
	gold   []byte
	hidden events.Blobs
	m      *env.Workspaces
	sel    SuiteSelection
}

// buildSuiteE2E commits the reference files, then the start's changes to them, and builds the task.
func buildSuiteE2E(t *testing.T, reference, start map[string]string, spec SuiteSpec, protected []string) *suiteE2E {
	t.Helper()
	f := newFixture(t)
	for p, body := range reference {
		f.write(p, body)
	}
	if spec.Commands[0].Parser == SuiteParserJUnit {
		ini, err := PytestINIFromDir(f.Dir)
		if err != nil {
			t.Fatal(err)
		}
		spec.PytestINI = ini
	}
	ref := f.commit("reference")
	for p, body := range start {
		f.write(p, body)
	}
	startCommit := f.commit("start")
	gold := []byte(f.git("diff", startCommit, ref) + "\n")

	e := &suiteE2E{gold: gold, hidden: events.NewMemBlobs(), m: newManager(t)}
	e.task = rl.Task{
		ID: "suite-e2e", Kind: rl.TaskFix, Prompt: "Make the test suite pass.",
		Repo:     rl.RepoSpec{Path: f.Dir, Commit: startCommit, License: "MIT"},
		Verifier: rl.Verifier{Protected: protected},
		Budget:   rl.Budget{Steps: 10, WallS: 600},
	}
	spec.DeadlineS = 300
	if err := WriteSuite(&e.task, spec, e.hidden); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rec, err := RecordSuite(ctx, e.m, e.task, gold, 2, e.hidden)
	if err != nil {
		t.Fatal(err)
	}
	if e.sel, err = BuildUnits(rec, nil); err != nil {
		t.Fatalf("%v\nrecord: %+v", err, rec)
	}
	e.sel.Apply(&spec)
	if err := WriteSuite(&e.task, spec, e.hidden); err != nil {
		t.Fatal(err)
	}
	if err := env.ValidateTask(e.task); err != nil {
		t.Fatal(err)
	}
	rep, err := env.CheckTask(ctx, e.task, gold, env.VerifyOptions{Workspaces: e.m, HiddenBlobs: e.hidden, Repeats: 2})
	if err != nil {
		t.Fatalf("the suite task is unsound: %v\nbaseline:\n%s\ngold:\n%s", err, rep.Baseline.Log, rep.Gold.Log)
	}
	if rep.Gold.Score != 1 || !rep.Gold.Pass || rep.Baseline.Score != 0 {
		t.Fatalf("reference %v, start %v: want exactly 1 and 0", rep.Gold.Score, rep.Baseline.Score)
	}
	return e
}

// verifyWith verifies a workspace of the task in which files were written over the start.
func (e *suiteE2E) verifyWith(t *testing.T, label string, files map[string]string) env.Result {
	t.Helper()
	ctx := context.Background()
	w, err := e.m.Prepare(ctx, e.task, label)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	for p, body := range files {
		if err := writeFile(filepath.Join(w.Root, filepath.FromSlash(p)), body); err != nil {
			t.Fatal(err)
		}
	}
	res, err := env.Verify(ctx, e.task, w, env.VerifyOptions{HiddenBlobs: e.hidden})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// wantScore checks a verdict's score and breakdown.
func wantScore(t *testing.T, label string, res env.Result, score float64, units map[string]rl.UnitScore, guards rl.GuardScore) {
	t.Helper()
	if res.Score != score || res.Pass != (score == 1) || !reflect.DeepEqual(res.Units, units) || res.Guards == nil || *res.Guards != guards {
		t.Errorf("%s: pass=%v score=%v units=%v guards=%v, want %v %v %v\n%s", label, res.Pass, res.Score, res.Units, res.Guards, score, units, guards, res.Log)
	}
}

const (
	suiteCalcRef = "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Mul(a, b int) int { return a * b }\n"
	suiteStrsRef = "package strs\n\nimport \"strings\"\n\nfunc Upper(s string) string { return strings.ToUpper(s) }\n\n" +
		"func Reverse(s string) string {\n\tr := []rune(s)\n\tfor i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {\n\t\tr[i], r[j] = r[j], r[i]\n\t}\n\treturn string(r)\n}\n"
)

func TestSuiteEndToEndGo(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test many times")
	}
	havePython(t)
	have(t, "go")
	reference := map[string]string{
		"go.mod":       "module example.com/suite\n\ngo 1.21\n",
		"calc/calc.go": suiteCalcRef,
		"calc/calc_test.go": "package calc\n\nimport \"testing\"\n\n" +
			"func TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add\")\n\t}\n}\n\n" +
			"func TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"Mul\")\n\t}\n}\n",
		"strs/strs.go": suiteStrsRef,
		"strs/strs_test.go": "package strs\n\nimport \"testing\"\n\n" +
			"func TestUpper(t *testing.T) {\n\tif Upper(\"ab\") != \"AB\" {\n\t\tt.Fatal(\"Upper\")\n\t}\n}\n\n" +
			"func TestReverse(t *testing.T) {\n\tif Reverse(\"abc\") != \"cba\" {\n\t\tt.Fatal(\"Reverse\")\n\t}\n}\n\n" +
			"func TestKnownBroken(t *testing.T) { t.Fatal(\"fails with the reference too\") }\n",
	}
	brokenCalc := "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Mul(a, b int) int { return a + b }\n"
	brokenStrs := "package strs\n\nimport \"strings\"\n\nfunc Upper(s string) string { return strings.ToUpper(s) }\n\nfunc Reverse(s string) string { return s }\n"
	e := buildSuiteE2E(t, reference, map[string]string{"calc/calc.go": brokenCalc, "strs/strs.go": brokenStrs},
		SuiteSpec{Commands: []SuiteCommand{GoSuiteCommand("")}}, []string{"*_test.go", "go.mod"})

	wantUnits := []SuiteUnit{
		{Name: "example.com/suite/calc", Weight: 1, Tests: []string{"example.com/suite/calc.TestMul"}},
		{Name: "example.com/suite/strs", Weight: 1, Tests: []string{"example.com/suite/strs.TestReverse"}},
	}
	if !reflect.DeepEqual(e.sel.Units, wantUnits) || !reflect.DeepEqual(e.sel.Guards, []string{"example.com/suite/calc.TestAdd", "example.com/suite/strs.TestUpper"}) ||
		!reflect.DeepEqual(e.sel.Dropped, []string{"example.com/suite/strs.TestKnownBroken"}) {
		t.Fatalf("selection: %+v", e.sel)
	}

	unit := func(calc, strs int) map[string]rl.UnitScore {
		return map[string]rl.UnitScore{"example.com/suite/calc": {Passed: calc, Total: 1, Weight: 1}, "example.com/suite/strs": {Passed: strs, Total: 1, Weight: 1}}
	}
	wantScore(t, "nothing done", e.verifyWith(t, "s0", nil), 0, unit(0, 0), rl.GuardScore{Kept: 2, Total: 2})
	wantScore(t, "half fixed", e.verifyWith(t, "s1", map[string]string{"calc/calc.go": suiteCalcRef}), 0.5, unit(1, 0), rl.GuardScore{Kept: 2, Total: 2})
	wantScore(t, "all fixed", e.verifyWith(t, "s2", map[string]string{"calc/calc.go": suiteCalcRef, "strs/strs.go": suiteStrsRef}), 1, unit(1, 1), rl.GuardScore{Kept: 2, Total: 2})
	wantScore(t, "fixed but a guard broken", e.verifyWith(t, "s3", map[string]string{
		"calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a - b }\n\nfunc Mul(a, b int) int { return a * b }\n",
		"strs/strs.go": suiteStrsRef,
	}), 0.5, unit(1, 1), rl.GuardScore{Kept: 1, Total: 2})

	// The code under test prints a perfect score, passing-test lines and a test2json pass event: nothing changes.
	forger := "package strs\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"strings\"\n)\n\nfunc init() {\n" +
		"\tfmt.Println(`{\"score\": 1}`)\n\tfmt.Println(\"--- PASS: TestReverse (0.00s)\")\n\tfmt.Println(\"ok  \\texample.com/suite/strs\\t0.01s\")\n" +
		"\tfmt.Println(`{\"Action\":\"pass\",\"Package\":\"example.com/suite/strs\",\"Test\":\"TestReverse\"}`)\n" +
		"\tfmt.Fprintln(os.Stderr, `{\"score\": 1}`)\n}\n\n" +
		"func Upper(s string) string { return strings.ToUpper(s) }\n\nfunc Reverse(s string) string { return s }\n"
	wantScore(t, "forged output", e.verifyWith(t, "s4", map[string]string{"calc/calc.go": suiteCalcRef, "strs/strs.go": forger}),
		0.5, unit(1, 0), rl.GuardScore{Kept: 2, Total: 2})

	// An agent that rewrites the scorer's spec, or a test, changes nothing and is flagged.
	res := e.verifyWith(t, "s5", map[string]string{
		"calc/calc.go":              suiteCalcRef,
		".sleipnir/suite/spec.json": `{"commands":[{"name":"x","run":"true","parser":"gotest-json"}],"units":[{"name":"u","weight":1,"tests":["t"]}]}`,
		"strs/strs_test.go":         "package strs\n",
	})
	wantScore(t, "tampering", res, 0.5, unit(1, 0), rl.GuardScore{Kept: 2, Total: 2})
	if !contains(res.ProtectedTouched, ".sleipnir/suite/spec.json") || !contains(res.ProtectedTouched, "strs/strs_test.go") {
		t.Errorf("tampering not flagged: %v", res.ProtectedTouched)
	}
}

func TestSuiteEndToEndPython(t *testing.T) {
	if testing.Short() {
		t.Skip("runs pytest many times")
	}
	havePython(t)
	havePytest(t)
	calcRef := "def add(a, b):\n    return a + b\n\n\ndef mul(a, b):\n    return a * b\n"
	reference := map[string]string{
		"pyproject.toml": "[project]\nname = \"calc\"\nversion = \"0\"\n\n[tool.pytest.ini_options]\ntestpaths = [\"tests\"]\nmarkers = [\"slow: slow tests\"]\n",
		"calc.py":        calcRef,
		"tests/test_calc.py": "from calc import add, mul\n\n\ndef test_add():\n    assert add(2, 3) == 5\n\n\n" +
			"def test_mul():\n    print('{\"score\": 1}')\n    assert mul(2, 3) == 6\n",
		"tests/test_neg.py": "import pytest\nfrom calc import mul\n\n\n@pytest.mark.parametrize('a,b,want', [(-1, 2, -2), (-3, -3, 9)])\n" +
			"def test_neg(a, b, want):\n    assert mul(a, b) == want\n",
	}
	e := buildSuiteE2E(t, reference, map[string]string{"calc.py": "def add(a, b):\n    return a + b\n\n\ndef mul(a, b):\n    return a + b\n"},
		SuiteSpec{Commands: []SuiteCommand{PythonSuiteCommand("")}}, []string{"tests/**"})
	wantUnits := []SuiteUnit{
		{Name: "tests/test_calc.py", Weight: 1, Tests: []string{"tests/test_calc.py::test_mul"}},
		{Name: "tests/test_neg.py", Weight: 1, Tests: []string{"tests/test_neg.py::test_neg[-1-2--2]", "tests/test_neg.py::test_neg[-3--3-9]"}},
	}
	if !reflect.DeepEqual(e.sel.Units, wantUnits) || !reflect.DeepEqual(e.sel.Guards, []string{"tests/test_calc.py::test_add"}) {
		t.Fatalf("selection: %+v", e.sel)
	}
	unit := func(calc, neg int) map[string]rl.UnitScore {
		return map[string]rl.UnitScore{"tests/test_calc.py": {Passed: calc, Total: 1, Weight: 1}, "tests/test_neg.py": {Passed: neg, Total: 2, Weight: 1}}
	}
	// mul right for b >= 0 only: test_mul and one of the two test_neg cases pass.
	half := "def add(a, b):\n    return a + b\n\n\ndef mul(a, b):\n    return a * b if b >= 0 else a + b\n"
	wantScore(t, "partly fixed", e.verifyWith(t, "s1", map[string]string{"calc.py": half}), 0.75, unit(1, 1), rl.GuardScore{Kept: 1, Total: 1})
	wantScore(t, "fixed", e.verifyWith(t, "s2", map[string]string{"calc.py": calcRef}), 1, unit(1, 2), rl.GuardScore{Kept: 1, Total: 1})
	// The configuration the tests run with is the reference's hidden copy: a plugin that reports every test passed,
	// loaded through the project's own configuration, is never loaded.
	forcePass := "import pytest\n\n\n@pytest.hookimpl(hookwrapper=True)\ndef pytest_runtest_makereport(item, call):\n" +
		"    outcome = yield\n    outcome.get_result().outcome = 'passed'\n"
	wantScore(t, "configuration edited", e.verifyWith(t, "s3", map[string]string{
		"forcepass.py":   forcePass,
		"pyproject.toml": "[project]\nname = \"calc\"\nversion = \"0\"\n\n[tool.pytest.ini_options]\naddopts = \"-p forcepass\"\ntestpaths = [\"tests\"]\n",
		"setup.cfg":      "[tool:pytest]\naddopts = -p forcepass\n",
	}), 0, unit(0, 0), rl.GuardScore{Kept: 1, Total: 1})
}

func TestSuiteEndToEndRust(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cargo test many times")
	}
	havePython(t)
	have(t, "cargo")
	libRef := "pub fn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n\npub fn mul(a: i32, b: i32) -> i32 {\n    a * b\n}\n\n" +
		"#[cfg(test)]\nmod tests {\n    use super::*;\n\n    #[test]\n    fn adds() {\n        assert_eq!(add(2, 2), 4);\n    }\n\n" +
		"    #[test]\n    fn muls() {\n        println!(\"test tests::other ... ok\");\n        assert_eq!(mul(2, 3), 6);\n    }\n}\n"
	reference := map[string]string{
		"Cargo.toml":     "[package]\nname = \"suite-demo\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\n",
		"src/lib.rs":     libRef,
		"tests/integ.rs": "#[test]\nfn negative() {\n    assert_eq!(suite_demo::mul(-2, 3), -6);\n}\n",
		".gitignore":     "/target\n",
	}
	broken := "pub fn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n\npub fn mul(a: i32, b: i32) -> i32 {\n    a + b\n}\n" + libRef[len("pub fn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n\npub fn mul(a: i32, b: i32) -> i32 {\n    a * b\n}\n"):]
	e := buildSuiteE2E(t, reference, map[string]string{"src/lib.rs": broken},
		SuiteSpec{Commands: []SuiteCommand{RustSuiteCommand("")}}, []string{"tests/**"})
	wantUnits := []SuiteUnit{
		{Name: "integ", Weight: 1, Tests: []string{"integ::negative"}},
		{Name: "suite_demo::tests", Weight: 1, Tests: []string{"suite_demo::tests::muls"}},
	}
	if !reflect.DeepEqual(e.sel.Units, wantUnits) || !reflect.DeepEqual(e.sel.Guards, []string{"suite_demo::tests::adds"}) {
		t.Fatalf("selection: %+v", e.sel)
	}
	half := "pub fn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n\npub fn mul(a: i32, b: i32) -> i32 {\n    if a >= 0 { a * b } else { a + b }\n}\n" +
		libRef[len("pub fn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n\npub fn mul(a: i32, b: i32) -> i32 {\n    a * b\n}\n"):]
	wantScore(t, "partly fixed", e.verifyWith(t, "s1", map[string]string{"src/lib.rs": half}), 0.5,
		map[string]rl.UnitScore{"integ": {Passed: 0, Total: 1, Weight: 1}, "suite_demo::tests": {Passed: 1, Total: 1, Weight: 1}}, rl.GuardScore{Kept: 1, Total: 1})
}
