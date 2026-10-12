package taskgen

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// A suite that reports no test at all, on the start and with the reference (pytest stopped at startup by a plugin its
// addopts names, a conftest that does not import), is rejected with how the reference's first run ended and the end
// of what the command printed, so the rejection names the cause instead of only "nothing to score".

// missingPluginINI is a pytest configuration naming a plugin no environment has: pytest stops before collecting.
const missingPluginINI = "[tool.pytest.ini_options]\naddopts = \"-p nonexistent_plugin\"\ntestpaths = [\"tests\"]\n"

// missingPluginError is the last line pytest prints for missingPluginINI.
const missingPluginError = `ImportError: Error importing plugin "nonexistent_plugin": No module named 'nonexistent_plugin'`

func intp(n int) *int { return &n }

// TestDescribeNoTests: how a run that reported nothing ended, from its record and the scorer's log: each command's exit
// status or note, then the end of the first relayed output, without the scorer's timings, cut to whole lines and
// suiteNoTestsTail bytes.
func TestDescribeNoTests(t *testing.T) {
	log := "suite: go: exit 0 after 0.4s, 0 tests reported (0 passed, 0 failed, 0 skipped)\n" +
		"suite: pytest: exit 4 after 0.3s, 0 tests reported (0 passed, 0 failed, 0 skipped)\n" +
		"    | ERROR: usage: python -m pytest [options] [file_or_dir] [file_or_dir] [...]\n" +
		"    | python -m pytest: error: unrecognized arguments: --cov=calc\n" +
		"    | \n" +
		"suite: cargo: not run, the deadline passed\n"
	pytest := suiteRecordCommand{Name: "pytest", Parser: SuiteParserJUnit, Exit: intp(4)}
	if got, want := describeNoTests([]suiteRecordCommand{pytest}, log),
		"pytest: exit 4\n"+
			"    | ERROR: usage: python -m pytest [options] [file_or_dir] [file_or_dir] [...]\n"+
			"    | python -m pytest: error: unrecognized arguments: --cov=calc"; got != want {
		t.Errorf("one command:\n%s\nwant:\n%s", got, want)
	}
	cmds := []suiteRecordCommand{
		{Name: "go", Parser: SuiteParserGoTest, Exit: intp(0)},
		pytest,
		{Name: "cargo", Parser: SuiteParserCargo, Note: "not run: the deadline passed"},
		{Name: "cargo:rs", Parser: SuiteParserCargo, Exit: intp(-9), TimedOut: true},
		{Name: "odd", Parser: SuiteParserCargo},
	}
	got := describeNoTests(cmds, log)
	if want := "go: exit 0, pytest: exit 4, cargo: not run: the deadline passed, cargo:rs: timed out, odd: no exit status\n" +
		"  pytest printed:\n    | ERROR: usage:"; !strings.HasPrefix(got, want) || strings.Contains(got, "after 0.") {
		t.Errorf("several commands:\n%s\nwant it to start with:\n%s", got, want)
	}
	if got := describeNoTests(nil, log); got != "no command ran" {
		t.Errorf("no commands: %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := (suiteRecordCommand{Name: "c", Note: long}).ended(); got != long[:200]+"..." {
		t.Errorf("a long note: %q", got)
	}

	// A long output keeps its last whole lines, at most suiteNoTestsTail bytes of them.
	var b strings.Builder
	b.WriteString("suite: pytest: exit 1 after 2.0s, 0 tests reported (0 passed, 0 failed, 0 skipped)\n")
	for i := range 200 {
		fmt.Fprintf(&b, "    | line %03d %s\n", i, strings.Repeat("y", 40))
	}
	got = describeNoTests([]suiteRecordCommand{{Name: "pytest", Exit: intp(1)}}, b.String())
	lines := strings.Split(got, "\n")
	quoted := strings.Join(lines[2:], "\n")
	if lines[0] != "pytest: exit 1" || lines[1] != "    | ..." || !strings.HasSuffix(got, "line 199 "+strings.Repeat("y", 40)) ||
		len(strings.ReplaceAll(quoted, "    | ", "")) > suiteNoTestsTail {
		t.Errorf("a long output (%d bytes quoted):\n%s", len(quoted), got)
	}
	for _, l := range lines[2:] {
		if !strings.HasPrefix(l, "    | line ") || len(l) != len("    | line 000 ")+40 {
			t.Errorf("a line was cut: %q", l)
		}
	}
	// One line longer than that keeps its end, from a rune boundary.
	got = describeNoTests([]suiteRecordCommand{{Name: "pytest", Exit: intp(1)}},
		"suite: pytest: exit 1 after 1.0s, 0 tests reported\n    | "+strings.Repeat("é", 2000)+"END\n")
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "pytest: exit 1\n    | ...é") || !strings.HasSuffix(got, "END") ||
		len(got) > len("pytest: exit 1\n    | ...")+suiteNoTestsTail {
		t.Errorf("one long line: %d bytes, valid UTF-8 %v:\n%.80s...", len(got), utf8.ValidString(got), got)
	}
}

// TestNoTestsReasons: "nothing to score" says which runs reported no test and how the first of them ended, for the
// reference solution (BuildUnits) and for a step after the start (BuildStepUnits), and nothing more when only the start
// is silent.
func TestNoTestsReasons(t *testing.T) {
	none := []SuiteOutcomes{{}, {}}
	const nothing = "no test fails on the start and passes with the reference solution every time: nothing to score"
	for _, c := range []struct {
		start, ref, want string
	}{
		{"", "", nothing},
		{"pytest: exit 1", "", nothing},
		{"pytest: exit 1", "pytest: exit 4\n    | boom", nothing + "; no run with the reference solution reported a test (nor on the start); the first ended with pytest: exit 4\n    | boom"},
		{"", "pytest: exit 4", nothing + "; no run with the reference solution reported a test; the first ended with pytest: exit 4"},
	} {
		_, err := BuildUnits(SuiteRecord{Start: none, Reference: none, StartNoTests: c.start, ReferenceNoTests: c.ref}, nil)
		if err == nil || err.Error() != c.want {
			t.Errorf("start %q, reference %q:\n%v\nwant:\n%s", c.start, c.ref, err, c.want)
		}
	}

	pass := []SuiteOutcomes{{"g": SuitePass}, {"g": SuitePass}}
	ms := []SuiteMilestone{{"m1", 1}, {"m2", 1}}
	const nothingSteps = "no test fails at the start and passes from some step on, every time: nothing to score"
	for _, c := range []struct {
		runs    [][]SuiteOutcomes
		noTests []string
		want    string
	}{
		{[][]SuiteOutcomes{none, none, none}, []string{"s0", "s1", "s2"}, nothingSteps + "; no run at any step reported a test; the first at step 2 ended with s2"},
		{[][]SuiteOutcomes{pass, none, none}, []string{"", "s1", "s2"}, nothingSteps + "; no run at step 2 reported a test; the first ended with s2"},
		{[][]SuiteOutcomes{pass, none, pass}, []string{"", "s1", ""}, nothingSteps + "; no run at step 1 reported a test; the first ended with s1"},
		{[][]SuiteOutcomes{none, none, none}, []string{"s0", "", ""}, nothingSteps},
		{[][]SuiteOutcomes{none, none, none}, nil, nothingSteps},
	} {
		_, err := BuildStepUnits(SuiteSteps{Runs: c.runs, NoTests: c.noTests}, ms)
		if err == nil || err.Error() != c.want {
			t.Errorf("no tests at %q:\n%v\nwant:\n%s", c.noTests, err, c.want)
		}
	}
}

// TestRecordSuiteNoTests: a command that fails at startup without reporting a test, as pytest does when its addopts
// name a plugin the environment lacks, is recorded with no outcome on either side and how the first run of each ended,
// and BuildUnits' rejection quotes the reference's. Only python3 and git are needed.
func TestRecordSuiteNoTests(t *testing.T) {
	if testing.Short() {
		t.Skip("records the suite in workspaces")
	}
	havePython(t)
	f := newFixture(t)
	f.write("calc.py", "def add(a, b):\n    return a + b\n")
	ref := f.commit("reference")
	f.write("calc.py", "def add(a, b):\n    return 0\n")
	start := f.commit("start")
	gold := []byte(f.git("diff", start, ref) + "\n")

	hidden := events.NewMemBlobs()
	task := rl.Task{
		ID: "suite-no-tests", Kind: rl.TaskFix, Prompt: "Make the test suite pass.",
		Repo:   rl.RepoSpec{Path: f.Dir, Commit: start, License: "MIT"},
		Budget: rl.Budget{Steps: 10, WallS: 600},
	}
	spec := SuiteSpec{Commands: []SuiteCommand{{Name: "pytest", Parser: SuiteParserJUnit, Run: `python3 -c 'import nonexistent_plugin'`}}, DeadlineS: 120}
	if err := WriteSuite(&task, spec, hidden); err != nil {
		t.Fatal(err)
	}
	rec, err := RecordSuite(context.Background(), newManager(t), task, gold, 2, hidden)
	if err != nil {
		t.Fatal(err)
	}
	const cause = "ModuleNotFoundError: No module named 'nonexistent_plugin'"
	for side, d := range map[string]string{"start": rec.StartNoTests, "reference": rec.ReferenceNoTests} {
		if !strings.HasPrefix(d, "pytest: exit 1\n    | ") || !strings.HasSuffix(d, "    | "+cause) {
			t.Errorf("%s: %q", side, d)
		}
	}
	if len(rec.Start) != 2 || len(rec.Reference) != 2 || len(rec.Start[0])+len(rec.Reference[1]) != 0 {
		t.Errorf("record: %+v", rec)
	}
	_, err = BuildUnits(rec, nil)
	if err == nil || !strings.Contains(err.Error(), "nothing to score; no run with the reference solution reported a test (nor on the start); the first ended with pytest: exit 1\n") ||
		!strings.HasSuffix(err.Error(), cause) {
		t.Errorf("BuildUnits: %v", err)
	}
}

// TestRebuildPythonNoTests: a Python project whose pytest configuration names a plugin the environment lacks is
// rejected as having too few tests, and the rejection, in the error and the report, names the missing plugin.
func TestRebuildPythonNoTests(t *testing.T) {
	if testing.Short() {
		t.Skip("runs pytest in workspaces")
	}
	havePytest(t)
	f := newFixture(t)
	for p, body := range map[string]string{
		"pyproject.toml":    "[project]\nname = \"mylib\"\nversion = \"0\"\n\n" + missingPluginINI,
		"mylib/__init__.py": "\"\"\"A small library.\"\"\"\nfrom .core import total\n",
		"mylib/core.py":     "\"\"\"The core.\"\"\"\n\n\ndef total(xs):\n    \"\"\"The sum.\"\"\"\n    return sum(xs)\n",
		"tests/test_core.py": "from mylib import total\n\n\ndef test_total():\n    assert total([1, 2]) == 3\n\n\n" +
			"def test_empty():\n    assert total([]) == 0\n",
	} {
		f.write(p, body)
	}
	f.commit("the library")
	opts := rbOptions(t, newManager(t), events.NewMemBlobs(), RebuildStubs)
	opts.Scope, opts.Repeats, opts.Gates.MinLOC, opts.Gates.MinTests = []string{"mylib"}, 1, 1, 1
	_, rep, err := Rebuild(context.Background(), f.Dir, opts)
	var rj *RebuildRejection
	if !errors.As(err, &rj) || rj.Reason != ReasonRebuildFewTests || rep.Rejected != rj || rep.Tests != 0 {
		t.Fatalf("%v\nreport: %+v", err, rep)
	}
	if !strings.Contains(rj.Detail, "no run with the reference solution reported a test (nor on the start); the first ended with pytest: exit 1\n    | ") ||
		!strings.HasSuffix(rj.Detail, "    | "+missingPluginError) || len(rj.Detail) > 2000 {
		t.Errorf("the rejection does not name the missing plugin (%d bytes):\n%s", len(rj.Detail), rj.Detail)
	}
}

// TestFromChainsPythonNoTests: a chain of a Python project whose pytest configuration names a plugin the environment
// lacks records no test at any step, and its rejection names the missing plugin.
func TestFromChainsPythonNoTests(t *testing.T) {
	if testing.Short() {
		t.Skip("runs pytest in workspaces")
	}
	havePytest(t)
	f := newFixture(t)
	f.write("pyproject.toml", "[project]\nname = \"calc\"\nversion = \"0\"\n\n"+missingPluginINI)
	f.write("LICENSE", mitLicense)
	f.write("calc/__init__.py", "")
	f.commit("initial")
	var commits []string
	for _, op := range []struct{ name, expr string }{{"add", "a + b"}, {"sub", "a - b"}, {"mul", "a * b"}} {
		f.write("calc/"+op.name+".py", fmt.Sprintf("def %s(a, b):\n    return %s\n", op.name, op.expr))
		f.write("tests/test_"+op.name+".py", fmt.Sprintf("from calc.%s import %s\n\n\ndef test_%s():\n    assert %s(6, 3) == %d\n",
			op.name, op.name, op.name, op.name, map[string]int{"add": 9, "sub": 3, "mul": 18}[op.name]))
		commits = append(commits, f.commit("Add "+op.name))
	}
	tasks, rep, err := FromChains(context.Background(), f.Dir, []Chain{{ID: "ops", Commits: commits}},
		ChainOptions{Workspaces: newManager(t), Repeats: 1, DeadlineS: 120, HiddenBlobs: events.NewMemBlobs()})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 || len(rep.Rejections) != 1 || rep.Rejections[0].Reason != ReasonNoMilestones {
		t.Fatalf("%d tasks, report %+v", len(tasks), rep)
	}
	d := rep.Rejections[0].Detail
	if first := firstLine(d); !strings.HasSuffix(first, "; no run at any step reported a test; the first at step 3 ended with pytest: exit 1") ||
		!strings.HasSuffix(d, "    | "+missingPluginError) || len(d) > 2000 {
		t.Errorf("the rejection does not name the missing plugin (%d bytes):\n%s", len(d), d)
	}
}
