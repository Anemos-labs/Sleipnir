package taskgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// The scorer's own tests: they run suite_score.py with python3 on commands that replay recorded test output, so they
// need no Go module, pytest or cargo of their own.

func havePython(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the suite scorer needs POSIX sessions and process groups")
	}
	have(t, "python3")
}

// layoutSuite makes a project directory with the scorer and spec in .sleipnir/suite and the given files.
func layoutSuite(t testing.TB, spec SuiteSpec, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	fs, err := suiteFiles(spec)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range fs {
		if err := writeFile(filepath.Join(dir, ".sleipnir", "suite", name), string(data)); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range files {
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(rel)), body); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// suiteFixture returns a recorded report from testdata/suite.
func suiteFixture(t testing.TB, name string) string {
	t.Helper()
	return mustRead(t, filepath.Join("testdata", "suite", name))
}

type scorerRun struct {
	stdout, stderr string
	took           time.Duration
}

// runScorer runs the scorer in dir as the verifier would, with extra environment variables.
func runScorer(t testing.TB, dir string, env ...string) scorerRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", ".sleipnir/suite/score.py")
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + t.TempDir(), "LC_ALL=C.UTF-8"}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("the scorer failed: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errb.String())
	}
	return scorerRun{stdout: out.String(), stderr: errb.String(), took: time.Since(start)}
}

type scoreLineT struct {
	Score  float64                 `json:"score"`
	Units  map[string]rl.UnitScore `json:"units"`
	Guards *rl.GuardScore          `json:"guards"`
}

// onlyLine returns the one line the scorer printed to stdout, failing when there is any other.
func onlyLine(t testing.TB, r scorerRun) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("stdout must be exactly one line, got %d:\n%s\nstderr:\n%s", len(lines), r.stdout, r.stderr)
	}
	return lines[0]
}

func scoreOf(t testing.TB, r scorerRun) scoreLineT {
	t.Helper()
	var s scoreLineT
	if err := json.Unmarshal([]byte(onlyLine(t, r)), &s); err != nil {
		t.Fatalf("not a score line: %v\n%s", err, r.stdout)
	}
	return s
}

func recordOf(t testing.TB, r scorerRun) map[string]suiteRecordCommand {
	t.Helper()
	cmds, err := parseSuiteRecord(onlyLine(t, r))
	if err != nil {
		t.Fatalf("%v\n%s", err, r.stderr)
	}
	out := map[string]suiteRecordCommand{}
	for _, c := range cmds {
		out[c.Name] = c
	}
	return out
}

// goEvents writes go test -json result events for package p: name -> pass, fail or skip.
func goEvents(p string, outcomes map[string]string) string {
	names := make([]string, 0, len(outcomes))
	for n := range outcomes {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, `{"Action":"run","Package":%q,"Test":%q}`+"\n", p, n)
		fmt.Fprintf(&b, `{"Action":%q,"Package":%q,"Test":%q,"Elapsed":0}`+"\n", outcomes[n], p, n)
	}
	return b.String()
}

// TestSuiteRecordsEveryParser: recorded go test -json, pytest junit and cargo output are read into the right ids and
// outcomes. Subtests are not tests of their own, a package that does not build reports nothing, a pytest teardown
// error fails its test, and what a failing Rust test printed (a fake "test ... ok" line) is not a result.
func TestSuiteRecordsEveryParser(t *testing.T) {
	havePython(t)
	spec := SuiteSpec{Commands: []SuiteCommand{
		{Name: "go", Run: "cat gotest.json", Parser: SuiteParserGoTest},
		{Name: "py", Run: `cp junit.xml "$SUITE_REPORT"; exit 1`, Parser: SuiteParserJUnit},
		{Name: "rs", Run: "cat cargo.txt; exit 101", Parser: SuiteParserCargo},
	}}
	dir := layoutSuite(t, spec, map[string]string{
		"gotest.json": suiteFixture(t, "gotest.json"), "junit.xml": suiteFixture(t, "junit.xml"), "cargo.txt": suiteFixture(t, "cargo.txt"),
	})
	rec := recordOf(t, runScorer(t, dir, "SUITE_MODE=record"))
	want := map[string]map[string]string{
		"go": {
			"example.com/probe/alpha.TestPass": "pass", "example.com/probe/alpha.TestFail": "fail",
			"example.com/probe/alpha.TestSkip": "skip", "example.com/probe/alpha.TestSubs": "fail",
			"example.com/probe/alpha.TestSubsAllGood": "pass",
			"example.com/probe/gamma.TestGamma":       "pass", "example.com/probe/gamma.Example": "pass",
		},
		"py": {
			"tests/test_mod.py::test_plain": "pass", "tests/test_mod.py::test_fails": "fail",
			"tests/test_mod.py::test_param[1]": "pass", "tests/test_mod.py::test_param[2.5]": "pass",
			"tests/test_mod.py::test_param[a b]": "pass", "tests/test_mod.py::test_skipped": "skip",
			"tests/test_mod.py::TestThing::test_method":          "pass",
			"tests/test_mod.py::TestThing::TestInner::test_deep": "pass",
			"tests/test_mod.py::test_teardown_error":             "fail", "tests/test_mod.py::test_xfail": "skip",
		},
		"rs": {
			"demo_crate::tests::slow": "skip", "demo_crate::tests::adds": "pass", "demo_crate::tests::inner::deep": "pass",
			"demo_crate::tests::muls": "fail", "integ::smoke": "pass",
		},
	}
	for name, tests := range want {
		got := rec[name]
		if got.Parser == "" || !reflect.DeepEqual(got.Tests, tests) {
			t.Errorf("%s: got %v\nwant %v", name, got.Tests, tests)
		}
	}
}

// TestSuiteScoreArithmetic: the score is the weighted mean of the units' pass shares times the share of guards kept,
// computed exactly; a skipped or unreported test has failed; the breakdown is in the score line and the failures on
// stderr.
func TestSuiteScoreArithmetic(t *testing.T) {
	havePython(t)
	spec := SuiteSpec{
		Commands: []SuiteCommand{{Name: "go", Run: "cat events.json", Parser: SuiteParserGoTest}},
		Units: []SuiteUnit{
			{Name: "u1", Weight: 2, Tests: []string{"p.TestA", "p.TestB"}},
			{Name: "u2", Weight: 1, Tests: []string{"p.TestC", "p.TestD", "p.TestE"}},
		},
		Guards: []string{"p.TestF", "p.TestG"},
	}
	cases := []struct {
		name     string
		outcomes map[string]string
		score    float64
		units    map[string]rl.UnitScore
		guards   rl.GuardScore
		stderr   []string
	}{
		{"partial", map[string]string{"TestA": "pass", "TestB": "fail", "TestC": "pass", "TestD": "skip", "TestF": "pass", "TestG": "fail"},
			2.0 / 9, map[string]rl.UnitScore{"u1": {Passed: 1, Total: 2, Weight: 2}, "u2": {Passed: 1, Total: 3, Weight: 1}}, rl.GuardScore{Kept: 1, Total: 2},
			[]string{"FAIL u1: p.TestB (fail)", "FAIL u2: p.TestD (skip)", "FAIL u2: p.TestE (not reported)", "BROKEN GUARD p.TestG (fail)"}},
		{"everything", map[string]string{"TestA": "pass", "TestB": "pass", "TestC": "pass", "TestD": "pass", "TestE": "pass", "TestF": "pass", "TestG": "pass"},
			1, map[string]rl.UnitScore{"u1": {Passed: 2, Total: 2, Weight: 2}, "u2": {Passed: 3, Total: 3, Weight: 1}}, rl.GuardScore{Kept: 2, Total: 2}, nil},
		{"the start", map[string]string{"TestF": "pass", "TestG": "pass"},
			0, map[string]rl.UnitScore{"u1": {Passed: 0, Total: 2, Weight: 2}, "u2": {Passed: 0, Total: 3, Weight: 1}}, rl.GuardScore{Kept: 2, Total: 2}, nil},
		{"every guard broken", map[string]string{"TestA": "pass", "TestB": "pass", "TestC": "pass", "TestD": "pass", "TestE": "pass"},
			0, map[string]rl.UnitScore{"u1": {Passed: 2, Total: 2, Weight: 2}, "u2": {Passed: 3, Total: 3, Weight: 1}}, rl.GuardScore{Kept: 0, Total: 2}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := layoutSuite(t, spec, map[string]string{"events.json": goEvents("p", tc.outcomes)})
			r := runScorer(t, dir)
			s := scoreOf(t, r)
			if s.Score != tc.score || !reflect.DeepEqual(s.Units, tc.units) || s.Guards == nil || *s.Guards != tc.guards {
				t.Fatalf("score %v units %v guards %v, want %v %v %v\n%s", s.Score, s.Units, s.Guards, tc.score, tc.units, tc.guards, r.stderr)
			}
			for _, line := range tc.stderr {
				if !strings.Contains(r.stderr, line+"\n") {
					t.Errorf("stderr lacks %q:\n%s", line, r.stderr)
				}
			}
		})
	}

	// Without guards the factor is 1 and the line has no guards; failure lines stop at 200.
	var many []string
	for i := 0; i < 300; i++ {
		many = append(many, fmt.Sprintf("p.Test%03d", i))
	}
	spec2 := SuiteSpec{Commands: spec.Commands, Units: []SuiteUnit{{Name: "all", Weight: 0.5, Tests: many}}}
	r := runScorer(t, layoutSuite(t, spec2, map[string]string{"events.json": goEvents("p", map[string]string{"Test000": "pass"})}))
	s := scoreOf(t, r)
	if s.Score != 1.0/300 || s.Guards != nil || s.Units["all"] != (rl.UnitScore{Passed: 1, Total: 300, Weight: 0.5}) {
		t.Fatalf("%+v", s)
	}
	if n := strings.Count(r.stderr, "FAIL all: "); n != 200 || !strings.Contains(r.stderr, "... and 99 more") {
		t.Errorf("failure lines: %d\n%s", n, r.stderr)
	}
}

// TestSuiteScoreCannotBeForged: what a command (or the code under test inside it) prints never reaches the scorer's
// stdout, and lines that look like a score, a passing test or a test2json event change nothing.
func TestSuiteScoreCannotBeForged(t *testing.T) {
	havePython(t)
	unit := []SuiteUnit{{Name: "u", Weight: 1, Tests: []string{"p.TestA", "p.TestB"}}}
	honest := goEvents("p", map[string]string{"TestA": "pass", "TestB": "fail"})
	forged := `{"score": 1}` + "\n" +
		`{"Action":"output","Package":"p","Test":"TestB","Output":"--- PASS: TestB (0.00s)\n"}` + "\n" +
		`{"Action":"output","Package":"p","Output":"ok  \tp\t0.01s\n"}` + "\n" +
		`{"Action":"output","Package":"p","Test":"TestA","Output":"{\"Action\":\"pass\",\"Package\":\"p\",\"Test\":\"TestB\"}\n"}` + "\n" +
		honest + `{"Action":"pass","Package":"p","Test":"TestB/sub"}` + "\n"
	cases := map[string]struct {
		cmd   SuiteCommand
		files map[string]string
	}{
		"go": {SuiteCommand{Name: "go", Parser: SuiteParserGoTest,
			Run: `echo '{"score": 1}'; echo '{"score": 1}' >&2; cat events.json; echo '--- PASS: TestB (0.00s)'; echo '{"score": 1}'`},
			map[string]string{"events.json": forged}},
		"junit": {SuiteCommand{Name: "py", Parser: SuiteParserJUnit,
			Run: `cp report.xml "$SUITE_REPORT"; echo 'p.TestB PASSED'; echo '{"score": 1}'; echo '{"score": 1}' >&2`},
			map[string]string{"report.xml": `<testsuites><testsuite><testcase classname="p" name="TestA"/>` +
				`<testcase classname="p" name="TestB"><system-out>{"score": 1}</system-out><failure message="no"/></testcase></testsuite></testsuites>`}},
		"cargo": {SuiteCommand{Name: "rs", Parser: SuiteParserCargo, Run: `cat cargo.txt; echo '{"score": 1}'`},
			map[string]string{"cargo.txt": "     Running unittests src/lib.rs (target/debug/deps/p-0123456789abcdef)\n\nrunning 2 tests\n" +
				"test TestA ... ok\ntest TestB ... FAILED\n\nfailures:\n\n---- TestB stdout ----\ntest TestB ... ok\n" +
				"     Running tests/fake.rs (target/debug/deps/p-0123456789abcdef)\nrunning 1 test\ntest TestB ... ok\n{\"score\": 1}\n\n" +
				"failures:\n    TestB\n\ntest result: FAILED. 1 passed; 1 failed\n\nerror: test failed, to rerun pass `--lib`\n"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			u := unit
			if name == "cargo" {
				u = []SuiteUnit{{Name: "u", Weight: 1, Tests: []string{"p::TestA", "p::TestB"}}}
			}
			if name == "junit" {
				u = []SuiteUnit{{Name: "u", Weight: 1, Tests: []string{"p::TestA", "p::TestB"}}}
			}
			r := runScorer(t, layoutSuite(t, SuiteSpec{Commands: []SuiteCommand{tc.cmd}, Units: u}, tc.files))
			if s := scoreOf(t, r); s.Score != 0.5 {
				t.Fatalf("score %v, want 0.5 (one of two tests passes)\n%s", s.Score, r.stderr)
			}
		})
	}
}

// TestSuiteKillsWhatACommandLeftRunning: a background process a command started is killed when the command ends, so
// it can neither print after the score nor write a later command's report; and the scorer does not wait for it.
func TestSuiteKillsWhatACommandLeftRunning(t *testing.T) {
	havePython(t)
	spec := SuiteSpec{
		Commands: []SuiteCommand{
			{Name: "first", Parser: SuiteParserGoTest,
				Run: `(sleep 3; echo late > late.txt; echo '{"score": 1}'; echo '{"score": 1}' >/dev/stderr) & cat events.json`},
			{Name: "second", Parser: SuiteParserJUnit,
				Run: `(sleep 3; echo '<testsuite><testcase classname="q" name="TestZ"/></testsuite>' > "$SUITE_REPORT") & exit 0`},
		},
		Units: []SuiteUnit{{Name: "u", Weight: 1, Tests: []string{"p.TestA", "q::TestZ"}}},
	}
	dir := layoutSuite(t, spec, map[string]string{"events.json": goEvents("p", map[string]string{"TestA": "pass"})})
	r := runScorer(t, dir)
	if r.took > 2500*time.Millisecond {
		t.Errorf("the scorer waited %v for a background process", r.took)
	}
	if s := scoreOf(t, r); s.Score != 0.5 {
		t.Fatalf("score %v, want 0.5\n%s", s.Score, r.stderr)
	}
	time.Sleep(4 * time.Second)
	if _, err := os.Stat(filepath.Join(dir, "late.txt")); err == nil {
		t.Fatal("a background process of the first command outlived it")
	}
}

// TestSuiteTimeoutsStillScore: a command over its own timeout is killed and keeps the results it reported; one the
// suite's deadline cuts off, and those it never reached, count their tests as failed; a score is printed either way.
func TestSuiteTimeoutsStillScore(t *testing.T) {
	havePython(t)
	files := map[string]string{
		"a.json": goEvents("p", map[string]string{"TestA": "pass"}),
		"c.json": goEvents("q", map[string]string{"TestC": "pass"}),
	}
	units := []SuiteUnit{
		{Name: "p", Weight: 1, Tests: []string{"p.TestA", "p.TestB"}},
		{Name: "q", Weight: 1, Tests: []string{"q.TestC"}},
	}
	t.Run("command timeout", func(t *testing.T) {
		spec := SuiteSpec{Units: units, Commands: []SuiteCommand{
			{Name: "slow", Parser: SuiteParserGoTest, Run: "cat a.json; sleep 60", TimeoutS: 1},
			{Name: "next", Parser: SuiteParserGoTest, Run: "cat c.json"},
		}}
		r := runScorer(t, layoutSuite(t, spec, files))
		s := scoreOf(t, r)
		if s.Score != 0.75 || r.took > 20*time.Second || !strings.Contains(r.stderr, "(timed out)") {
			t.Fatalf("score %v after %v, want 0.75 (TestA reported before the timeout, TestC after it)\n%s", s.Score, r.took, r.stderr)
		}
	})
	t.Run("suite deadline", func(t *testing.T) {
		spec := SuiteSpec{Units: units, DeadlineS: 2, Commands: []SuiteCommand{
			{Name: "slow", Parser: SuiteParserGoTest, Run: "cat a.json; sleep 60"},
			{Name: "never", Parser: SuiteParserGoTest, Run: "cat c.json"},
		}}
		r := runScorer(t, layoutSuite(t, spec, files))
		s := scoreOf(t, r)
		if s.Score != 0.25 || r.took > 20*time.Second || !strings.Contains(r.stderr, "never: not run, the deadline passed") {
			t.Fatalf("score %v after %v, want 0.25 (only TestA)\n%s", s.Score, r.took, r.stderr)
		}
	})
}

// sampleOf is the scorer's progress-mode sample: the n tests with the smallest sha256 of their id.
func sampleOf(tests []string, n int) []string {
	key := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	out := append([]string(nil), tests...)
	sort.Slice(out, func(i, j int) bool { return key(out[i]) < key(out[j]) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// TestSuiteProgressMode: SUITE_SAMPLE scores a stable sample of each unit and of the guards, and hands each command its
// own share of the sample in SUITE_ARGS; SUITE_BUDGET_S shortens the deadline. A command none of whose tests is in the
// sample is not run.
func TestSuiteProgressMode(t *testing.T) {
	havePython(t)
	goTests := []string{"p/one.TestA", "p/one.TestB", "p/one.TestC", "p/two.TestD", "p/two.TestE"}
	rsTests := []string{"crate::m::a", "crate::m::b", "crate::m::c"}
	origin := map[string]string{}
	outcomes := map[string]map[string]string{"p/one": {}, "p/two": {}}
	for _, id := range goTests {
		origin[id] = "go"
		i := strings.LastIndex(id, ".")
		outcomes[id[:i]][id[i+1:]] = "pass"
	}
	for _, id := range rsTests {
		origin[id] = "rs"
	}
	origin["other.TestX"] = "other"
	spec := SuiteSpec{
		Commands: []SuiteCommand{
			{Name: "go", Parser: SuiteParserGoTest, Run: `printf '%s' "${SUITE_ARGS-unset}" > go-args.txt; cat one.json two.json`},
			{Name: "rs", Parser: SuiteParserCargo, Run: `printf '%s' "${SUITE_ARGS-unset}" > rs-args.txt; cat cargo.txt`},
			{Name: "other", Parser: SuiteParserGoTest, Run: `touch other-ran.txt; sleep 60`},
			{Name: "idle", Parser: SuiteParserGoTest, Run: `touch idle-ran.txt`},
		},
		Units:  []SuiteUnit{{Name: "go", Weight: 1, Tests: goTests}, {Name: "rs", Weight: 1, Tests: rsTests}},
		Guards: []string{"other.TestX"},
		Origin: origin,
	}
	cargo := "     Running unittests src/lib.rs (target/debug/deps/crate-0123456789abcdef)\n\nrunning 3 tests\n" +
		"test m::a ... ok\ntest m::b ... FAILED\ntest m::c ... ok\n\ntest result: FAILED. 2 passed; 1 failed\n"
	files := map[string]string{"one.json": goEvents("p/one", outcomes["p/one"]), "two.json": goEvents("p/two", outcomes["p/two"]), "cargo.txt": cargo}

	// The whole suite: no selection, and every command runs. The budget ends the third command, which would sleep a
	// minute, and so the fourth is not reached.
	dir := layoutSuite(t, spec, files)
	r := runScorer(t, dir, "SUITE_BUDGET_S=2", "SUITE_SAMPLE=0")
	if got := mustRead(t, filepath.Join(dir, "go-args.txt")); got != "unset" {
		t.Errorf("SUITE_ARGS outside progress mode: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "other-ran.txt")); err != nil || r.took > 20*time.Second {
		t.Errorf("the full suite did not run every command within its budget (%v)", r.took)
	}

	// Two of each unit, one guard; the command with no test in the sample is not run.
	dir = layoutSuite(t, spec, files)
	r = runScorer(t, dir, "SUITE_SAMPLE=2", "SUITE_BUDGET_S=2")
	if _, err := os.Stat(filepath.Join(dir, "idle-ran.txt")); err == nil || !strings.Contains(r.stderr, "idle: not run, none of its tests is in the sample") {
		t.Errorf("a command without sampled tests ran\n%s", r.stderr)
	}
	s := scoreOf(t, r)
	goPick, rsPick := sampleOf(goTests, 2), sampleOf(rsTests, 2)
	rsPassed := 0
	for _, id := range rsPick {
		if id != "crate::m::b" {
			rsPassed++
		}
	}
	if s.Units["go"] != (rl.UnitScore{Passed: 2, Total: 2, Weight: 1}) || s.Units["rs"] != (rl.UnitScore{Passed: rsPassed, Total: 2, Weight: 1}) ||
		s.Guards == nil || *s.Guards != (rl.GuardScore{Kept: 0, Total: 1}) || s.Score != 0 {
		t.Fatalf("sampled score: %+v (go sample %v, rs sample %v)\n%s", s, goPick, rsPick, r.stderr)
	}
	var names, pkgs []string
	for _, id := range goPick {
		i := strings.LastIndex(id, ".")
		pkgs, names = append(pkgs, id[:i]), append(names, id[i+1:])
	}
	sort.Strings(names)
	sort.Strings(pkgs)
	pkgs = dedupe(pkgs)
	if got, want := mustRead(t, filepath.Join(dir, "go-args.txt")), "-run '^("+strings.Join(names, "|")+")$' "+strings.Join(pkgs, " "); got != want {
		t.Errorf("go SUITE_ARGS = %q, want %q", got, want)
	}
	sort.Strings(rsPick)
	if got, want := mustRead(t, filepath.Join(dir, "rs-args.txt")), "-- --exact "+strings.TrimPrefix(rsPick[0], "crate::")+" "+strings.TrimPrefix(rsPick[1], "crate::"); got != want {
		t.Errorf("cargo SUITE_ARGS = %q, want %q", got, want)
	}
	// The guard's command ran (it is in the sample), and a second run picks the same sample.
	if again := scoreOf(t, runScorer(t, layoutSuite(t, spec, files), "SUITE_SAMPLE=2", "SUITE_BUDGET_S=1")); !reflect.DeepEqual(again.Units, s.Units) {
		t.Errorf("the sample is not stable: %v then %v", s.Units, again.Units)
	}
}

// TestSuiteScorerRefusesABadSpec: a spec the scorer cannot use prints no score line (exit 2), so the verdict is a
// failure, never a score.
func TestSuiteScorerRefusesABadSpec(t *testing.T) {
	havePython(t)
	cmds := `"commands": [{"name": "go", "run": "true", "parser": "gotest-json"%s}]`
	unit := `"units": [{"name": "u", "weight": 1, "tests": ["p.TestA"]}]`
	for name, spec := range map[string]string{
		"no units":          "{" + fmt.Sprintf(cmds, "") + "}",
		"a bad deadline":    "{" + fmt.Sprintf(cmds, "") + ", " + unit + `, "deadline_s": "soon"}`,
		"a bad timeout":     "{" + fmt.Sprintf(cmds, `, "timeout_s": -1`) + ", " + unit + "}",
		"an escaping cwd":   "{" + fmt.Sprintf(cmds, `, "cwd": "../x"`) + ", " + unit + "}",
		"an unknown parser": `{"commands": [{"name": "go", "run": "true", "parser": "tap"}], ` + unit + "}",
		"a duplicate test":  "{" + fmt.Sprintf(cmds, "") + `, "units": [{"name": "u", "tests": ["a"]}, {"name": "v", "tests": ["a"]}]}`,
		"not json":          "{",
	} {
		dir := layoutSuite(t, SuiteSpec{Commands: []SuiteCommand{{Name: "go", Run: "true", Parser: SuiteParserGoTest}}}, nil)
		if err := os.WriteFile(filepath.Join(dir, ".sleipnir", "suite", "spec.json"), []byte(spec), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("python3", ".sleipnir/suite/score.py")
		cmd.Dir = dir
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 || out.Len() != 0 || !strings.Contains(errb.String(), "cannot use spec.json") {
			t.Errorf("%s: err=%v stdout=%q stderr=%q", name, err, out.String(), errb.String())
		}
	}
}

// TestSuiteScriptIsPython38: the scorer parses as Python 3.8, the oldest it supports.
func TestSuiteScriptIsPython38(t *testing.T) {
	havePython(t)
	src := filepath.Join(t.TempDir(), "score.py")
	if err := os.WriteFile(src, []byte(suiteScoreScript), 0o644); err != nil {
		t.Fatal(err)
	}
	check := `import ast, sys; ast.parse(open(sys.argv[1]).read(), feature_version=(3, 8))`
	if out, err := exec.Command("python3", "-c", check, src).CombinedOutput(); err != nil {
		t.Fatalf("not Python 3.8: %v\n%s", err, out)
	}
}

// TestSuiteRerunsAGoPackageThatCrashed: go test stops a package at the test that crashes its binary, so the scorer runs
// the package again without the tests it reported, as often as it keeps crashing and reporting more; the test that
// crashed stays failed, and only the crashed package is run again.
func TestSuiteRerunsAGoPackageThatCrashed(t *testing.T) {
	havePython(t)
	crash := func(p, name string) string {
		return fmt.Sprintf(`{"Action":"run","Package":%q,"Test":%q}`+"\n"+`{"Action":"output","Package":%q,"Test":%q,"Output":"panic: runtime error: invalid memory address or nil pointer dereference\n"}`+"\n"+
			`{"Action":"fail","Package":%q,"Test":%q,"Elapsed":0}`+"\n"+`{"Action":"fail","Package":%q,"Elapsed":0}`+"\n", p, name, p, name, p, name, p)
	}
	files := map[string]string{
		// The first run: TestA passes, TestB crashes the binary; p/other is unaffected.
		"run1.json": goEvents("p/one", map[string]string{"TestA": "pass"}) + crash("p/one", "TestB") + goEvents("p/other", map[string]string{"TestZ": "pass"}),
		// Run again without TestA and TestB: TestC passes, TestD crashes too.
		"run2.json": goEvents("p/one", map[string]string{"TestC": "pass"}) + crash("p/one", "TestD"),
		// And again without those: TestE fails normally, TestF passes.
		"run3.json": goEvents("p/one", map[string]string{"TestE": "fail", "TestF": "pass"}),
	}
	spec := SuiteSpec{Commands: []SuiteCommand{{Name: "go", Parser: SuiteParserGoTest,
		Run: `n=$(cat n 2>/dev/null || echo 0); n=$((n+1)); echo $n > n; printf '%s\n' "${SUITE_ARGS-}" >> args.txt; cat run$n.json`}}}
	dir := layoutSuite(t, spec, files)
	got := recordOf(t, runScorer(t, dir, "SUITE_MODE=record"))["go"].Tests
	want := map[string]string{"p/one.TestA": "pass", "p/one.TestB": "fail", "p/one.TestC": "pass", "p/one.TestD": "fail",
		"p/one.TestE": "fail", "p/one.TestF": "pass", "p/other.TestZ": "pass"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests %v\nwant %v", got, want)
	}
	args := strings.Split(strings.TrimRight(mustRead(t, filepath.Join(dir, "args.txt")), "\n"), "\n")
	if wantArgs := []string{"", "-skip '^(TestA|TestB)$' p/one", "-skip '^(TestA|TestB|TestC|TestD)$' p/one"}; !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("SUITE_ARGS of the runs: %q, want %q", args, wantArgs)
	}
}

// TestSuiteGoCrashCostsOnlyItsTest: with real go test, a test that panics fails alone, and the tests after it in the same
// package are still reported.
func TestSuiteGoCrashCostsOnlyItsTest(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	havePython(t)
	have(t, "go")
	files := map[string]string{
		"go.mod": "module example.com/crash\n\ngo 1.21\n",
		"p/p.go": "package p\n\nfunc Double(x int) int { return 2 * x }\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\n" +
			"func TestA(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal(\"A\")\n\t}\n}\n\n" +
			"func TestB(t *testing.T) {\n\tvar m map[string]int\n\tm[\"x\"] = 1\n}\n\n" +
			"func TestC(t *testing.T) {\n\tif Double(3) != 6 {\n\t\tt.Fatal(\"C\")\n\t}\n}\n\n" +
			"func TestD(t *testing.T) {\n\tif Double(1) != 3 {\n\t\tt.Fatal(\"D\")\n\t}\n}\n",
	}
	dir := layoutSuite(t, SuiteSpec{Commands: []SuiteCommand{GoSuiteCommand("")}}, files)
	r := runScorer(t, dir, "SUITE_MODE=record", "GOFLAGS=-mod=mod", "GOCACHE="+sharedGoCache)
	got := recordOf(t, r)["go"].Tests
	want := map[string]string{"example.com/crash/p.TestA": "pass", "example.com/crash/p.TestB": "fail",
		"example.com/crash/p.TestC": "pass", "example.com/crash/p.TestD": "fail"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests %v\nwant %v\n%s", got, want, r.stderr)
	}
}
