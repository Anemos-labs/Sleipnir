package taskgen

import (
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

func TestSuiteSpecValidate(t *testing.T) {
	good := SuiteSpec{
		Commands: []SuiteCommand{GoSuiteCommand(""), PythonSuiteCommand("py", "tests"), RustSuiteCommand("rs")},
		Units:    []SuiteUnit{{Name: "a", Weight: 1, Tests: []string{"p.TestA"}}, {Name: "b", Weight: 0, Tests: []string{"p.TestB"}}},
		Guards:   []string{"p.TestC"},
		Origin:   map[string]string{"p.TestA": "go"},
	}
	if err := good.Validate(true); err != nil {
		t.Fatalf("a good spec: %v", err)
	}
	if err := (SuiteSpec{Commands: good.Commands}).Validate(false); err != nil {
		t.Fatalf("a record-only spec: %v", err)
	}
	bad := map[string]func(s *SuiteSpec){
		"no command":           func(s *SuiteSpec) { s.Commands = nil },
		"duplicate command":    func(s *SuiteSpec) { s.Commands = append(s.Commands, s.Commands[0]) },
		"empty run":            func(s *SuiteSpec) { s.Commands[0].Run = " " },
		"unknown parser":       func(s *SuiteSpec) { s.Commands[0].Parser = "tap" },
		"negative timeout":     func(s *SuiteSpec) { s.Commands[0].TimeoutS = -1 },
		"absolute cwd":         func(s *SuiteSpec) { s.Commands[0].Cwd = "/etc" },
		"escaping cwd":         func(s *SuiteSpec) { s.Commands[0].Cwd = "../x" },
		"no units":             func(s *SuiteSpec) { s.Units = nil },
		"zero weights":         func(s *SuiteSpec) { s.Units[0].Weight = 0 },
		"NaN weight":           func(s *SuiteSpec) { s.Units[0].Weight = math.NaN() },
		"negative weight":      func(s *SuiteSpec) { s.Units[1].Weight = -1 },
		"empty unit":           func(s *SuiteSpec) { s.Units[0].Tests = nil },
		"duplicate unit":       func(s *SuiteSpec) { s.Units[1].Name = "a" },
		"long unit name":       func(s *SuiteSpec) { s.Units[0].Name = strings.Repeat("x", env.MaxScoreUnitName+1) },
		"test in two units":    func(s *SuiteSpec) { s.Units[1].Tests = []string{"p.TestA"} },
		"guard is a unit test": func(s *SuiteSpec) { s.Guards = []string{"p.TestA"} },
		"unknown origin":       func(s *SuiteSpec) { s.Origin = map[string]string{"p.TestA": "nope"} },
		"negative deadline":    func(s *SuiteSpec) { s.DeadlineS = -1 },
	}
	for name, mut := range bad {
		s := good
		s.Commands = append([]SuiteCommand(nil), good.Commands...)
		s.Units = append([]SuiteUnit(nil), good.Units...)
		mut(&s)
		if err := s.Validate(true); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDefaultSuiteGroup(t *testing.T) {
	for _, tc := range []struct{ id, parser, want string }{
		{"github.com/x/y/pkg.TestA", SuiteParserGoTest, "github.com/x/y/pkg"},
		{"tests/test_a.py::TestK::test_x[1.5]", SuiteParserJUnit, "tests/test_a.py"},
		{"com.example.FooTest::testBar", SuiteParserJUnit, "com.example.FooTest"},
		{"crate::tests::inner::deep", SuiteParserCargo, "crate::tests::inner"},
		{"integ::smoke", SuiteParserCargo, "integ"},
		{"odd", "", "odd"},
	} {
		if got := DefaultSuiteGroup(tc.id, tc.parser); got != tc.want {
			t.Errorf("DefaultSuiteGroup(%q, %q) = %q, want %q", tc.id, tc.parser, got, tc.want)
		}
	}
	long := strings.Repeat("é", env.MaxScoreUnitName)
	n1, n2 := unitName(long+"a"), unitName(long+"b")
	if len(n1) > env.MaxScoreUnitName || n1 == n2 || !strings.HasPrefix(n1, "é") || unitName("") != "(none)" || unitName("x") != "x" {
		t.Errorf("unitName: %q %q", n1, n2)
	}
}

// TestBuildUnits: units hold what fails at every start and passes with every reference run, guards what always passes,
// and everything else (flaky, failing on the reference, skipped) is dropped.
func TestBuildUnits(t *testing.T) {
	rec := SuiteRecord{
		Start: []SuiteOutcomes{
			{"p/a.TestNew": "fail", "p/a.TestOld": "pass", "p/a.TestFlakyStart": "pass", "p/b.TestSkipped": "skip", "p/a.TestBroken": "fail", "f.py::t": "fail"},
			{"p/a.TestNew": "fail", "p/a.TestOld": "pass", "p/a.TestFlakyStart": "fail", "p/b.TestSkipped": "skip", "p/a.TestBroken": "fail"},
		},
		Reference: []SuiteOutcomes{
			{"p/a.TestNew": "pass", "p/a.TestOld": "pass", "p/a.TestFlakyStart": "pass", "p/b.TestSkipped": "pass", "p/b.TestAdded": "pass", "p/a.TestBroken": "fail", "f.py::t": "pass", "p/a.TestFlakyRef": "pass"},
			{"p/a.TestNew": "pass", "p/a.TestOld": "pass", "p/a.TestFlakyStart": "pass", "p/b.TestSkipped": "pass", "p/b.TestAdded": "pass", "p/a.TestBroken": "fail", "f.py::t": "pass", "p/a.TestFlakyRef": "fail"},
		},
		Origin: map[string]string{"p/a.TestNew": "go", "p/a.TestOld": "go", "p/b.TestSkipped": "go", "p/b.TestAdded": "go", "f.py::t": "py"},
		Parser: map[string]string{"p/a.TestNew": SuiteParserGoTest, "p/a.TestOld": SuiteParserGoTest, "p/b.TestSkipped": SuiteParserGoTest,
			"p/b.TestAdded": SuiteParserGoTest, "f.py::t": SuiteParserJUnit},
	}
	sel, err := BuildUnits(rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantUnits := []SuiteUnit{
		{Name: "f.py", Weight: 1, Tests: []string{"f.py::t"}},
		{Name: "p/a", Weight: 1, Tests: []string{"p/a.TestNew"}},
		{Name: "p/b", Weight: 1, Tests: []string{"p/b.TestAdded", "p/b.TestSkipped"}},
	}
	if !reflect.DeepEqual(sel.Units, wantUnits) {
		t.Errorf("units: %+v", sel.Units)
	}
	if !reflect.DeepEqual(sel.Guards, []string{"p/a.TestOld"}) {
		t.Errorf("guards: %v", sel.Guards)
	}
	if !reflect.DeepEqual(sel.Dropped, []string{"p/a.TestBroken", "p/a.TestFlakyRef", "p/a.TestFlakyStart"}) {
		t.Errorf("dropped: %v", sel.Dropped)
	}
	if len(sel.Origin) != 5 || sel.Origin["f.py::t"] != "py" || sel.Origin["p/a.TestOld"] != "go" {
		t.Errorf("origin: %v", sel.Origin)
	}
	spec := SuiteSpec{Commands: []SuiteCommand{{Name: "go", Run: "x", Parser: SuiteParserGoTest}, {Name: "py", Run: "y", Parser: SuiteParserJUnit}}}
	sel.Apply(&spec)
	if err := spec.Validate(true); err != nil {
		t.Errorf("the applied selection does not validate: %v", err)
	}

	// A caller's grouping: one unit for everything.
	sel, err = BuildUnits(rec, func(id, parser string) string { return "all" })
	if err != nil || len(sel.Units) != 1 || len(sel.Units[0].Tests) != 4 {
		t.Errorf("custom grouping: %+v %v", sel.Units, err)
	}
	// Nothing to score, and no runs, are errors.
	if _, err := BuildUnits(SuiteRecord{Start: rec.Reference, Reference: rec.Reference}, nil); err == nil {
		t.Error("a record where nothing fails at the start was accepted")
	}
	if _, err := BuildUnits(SuiteRecord{Reference: rec.Reference}, nil); err == nil {
		t.Error("a record without start runs was accepted")
	}
}

func TestParseSuiteRecord(t *testing.T) {
	cmds, err := parseSuiteRecord("noise\n" + `{"suite_record":1,"commands":[{"name":"go","parser":"gotest-json","tests":{"p.TestA":"pass"}}]}` + "\n")
	if err != nil || len(cmds) != 1 || cmds[0].Tests["p.TestA"] != "pass" {
		t.Fatalf("%+v %v", cmds, err)
	}
	for _, bad := range []string{"", `{"score": 1}`, `{"suite_record":2,"commands":[]}`, `{"suite_record":1,"commands":[{"name":"go","tests":{"x":"maybe"}}]}`} {
		if _, err := parseSuiteRecord(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// TestWriteSuite: the verifier becomes the scorer, its files are hidden (as blobs when a store is given) and protected,
// a second write replaces the first's files, and a task in a subdirectory gets them beside its verifier.
func TestWriteSuite(t *testing.T) {
	task := rl.Task{
		ID: "suite-1", Kind: rl.TaskFeature, Prompt: "build it", Repo: rl.RepoSpec{Path: "/r", Commit: "abc"},
		Verifier: rl.Verifier{Cmd: "go test ./...", Hidden: map[string]string{"x_test.go": "text:package x"}, Protected: []string{"*_test.go"}},
		Requires: []string{"go"},
	}
	spec := SuiteSpec{
		Commands:  []SuiteCommand{GoSuiteCommand(""), PythonSuiteCommand("py")},
		Units:     []SuiteUnit{{Name: "u", Weight: 1, Tests: []string{"p.TestA"}}},
		DeadlineS: 600, PytestINI: "[pytest]\naddopts = -ra\n",
	}
	if err := WriteSuite(&task, spec, nil); err != nil {
		t.Fatal(err)
	}
	v := task.Verifier
	if v.Cmd != "python3 .sleipnir/suite/score.py" || v.Pass != "json-score" || v.TimeoutS != 720 || v.BaselineScore != 0 {
		t.Errorf("verifier: %+v", v)
	}
	if !reflect.DeepEqual(task.Requires, []string{"go", "python3"}) {
		t.Errorf("requires: %v", task.Requires)
	}
	for _, p := range []string{".sleipnir/**", "*_test.go", "conftest.py", "pytest.ini"} {
		if !contains(v.Protected, p) {
			t.Errorf("not protected: %s (%v)", p, v.Protected)
		}
	}
	if v.Hidden["x_test.go"] != "text:package x" || v.Hidden[".sleipnir/suite/pytest.ini"] != "text:[pytest]\naddopts = -ra\n" ||
		v.Hidden[".sleipnir/suite/score.py"] != "text:"+suiteScoreScript {
		t.Errorf("hidden: %v", keys(v.Hidden))
	}
	var onDisk SuiteSpec
	if err := json.Unmarshal([]byte(strings.TrimPrefix(v.Hidden[".sleipnir/suite/spec.json"], "text:")), &onDisk); err != nil ||
		onDisk.DeadlineS != 600 || len(onDisk.Units) != 1 || onDisk.PytestINI != "" {
		t.Errorf("spec.json: %+v %v", onDisk, err)
	}
	if err := env.ValidateTask(task); err != nil {
		t.Errorf("the task does not validate: %v", err)
	}

	// Again, Go only, into a blob store: the pytest config goes, the rest are blobs.
	blobs := events.NewMemBlobs()
	if err := WriteSuite(&task, SuiteSpec{Commands: []SuiteCommand{GoSuiteCommand("")}}, blobs); err != nil {
		t.Fatal(err)
	}
	if _, ok := task.Verifier.Hidden[".sleipnir/suite/pytest.ini"]; ok {
		t.Error("a stale pytest.ini survived")
	}
	ref := task.Verifier.Hidden[".sleipnir/suite/score.py"]
	if b, err := blobs.Get(core.Hash(strings.TrimPrefix(ref, "blob:"))); err != nil || string(b) != suiteScoreScript {
		t.Errorf("score.py blob %q: %v", ref, err)
	}
	if task.Verifier.TimeoutS != DefaultSuiteDeadlineS+suiteGraceS || strings.Count(strings.Join(task.Verifier.Protected, " "), ".sleipnir/**") != 1 {
		t.Errorf("rewrite: %+v", task.Verifier)
	}

	// A task in a subdirectory: the files go beside the verifier, which runs there.
	sub := task
	sub.Repo.Subdir = "svc/api"
	sub.Verifier = rl.Verifier{Cmd: "true"}
	if err := WriteSuite(&sub, SuiteSpec{Commands: []SuiteCommand{RustSuiteCommand("")}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := sub.Verifier.Hidden["svc/api/.sleipnir/suite/score.py"]; !ok || !contains(sub.Verifier.Protected, "svc/api/.sleipnir/**") ||
		!contains(sub.Verifier.Protected, "**/.cargo/**") || !contains(sub.Requires, "cargo") {
		t.Errorf("subdir: %v %v %v", keys(sub.Verifier.Hidden), sub.Verifier.Protected, sub.Requires)
	}
	if err := env.ValidateTask(sub); err != nil {
		t.Errorf("the subdir task does not validate: %v", err)
	}
	if err := WriteSuite(&sub, SuiteSpec{}, nil); err == nil {
		t.Error("an empty spec was accepted")
	}
}

// keys returns a map's keys.
func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPytestINIFromDir(t *testing.T) {
	write := func(files map[string]string) string {
		dir := t.TempDir()
		for name, body := range files {
			if err := writeFile(filepath.Join(dir, name), body); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"none", map[string]string{"README": "x"}, "[pytest]\n"},
		{"pytest.ini wins", map[string]string{"pytest.ini": "[pytest]\naddopts = -q\n", "pyproject.toml": "[tool.pytest.ini_options]\naddopts = \"-x\"\n"},
			"[pytest]\naddopts = -q\n"},
		{"empty pytest.ini", map[string]string{"pytest.ini": "", "tox.ini": "[pytest]\naddopts = -x\n"}, "[pytest]\n"},
		{"tox.ini", map[string]string{"tox.ini": "[tox]\nenvlist = py\n[pytest]\ntestpaths = tests\n[other]\nx = 1\n"}, "[pytest]\ntestpaths = tests\n"},
		{"setup.cfg", map[string]string{"setup.cfg": "[metadata]\nname = x\n[tool:pytest]\nmarkers =\n    slow: slow tests\n"},
			"[pytest]\nmarkers =\n    slow: slow tests\n"},
		{"pyproject without a pytest table falls through", map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "setup.cfg": "[tool:pytest]\naddopts = -q\n"},
			"[pytest]\naddopts = -q\n"},
		{"pyproject", map[string]string{"pyproject.toml": `[project]
name = "demo"

[tool.pytest.ini_options]
minversion = "6.0"   # comment
addopts = ["-ra", "-k", "not slow"]
testpaths = [
  "tests",  # the tests
  'integration',
]
markers = ["slow: marks tests as slow", "db"]
xfail_strict = true
timeout = 30
'quoted-key' = "v"

[tool.other]
x = 1
`}, "[pytest]\nminversion = 6.0\naddopts =\n    -ra\n    -k\n    'not slow'\ntestpaths =\n    tests\n    integration\nmarkers =\n    slow: marks tests as slow\n    db\n" +
			"xfail_strict = true\ntimeout = 30\nquoted-key = v\n"},
	}
	for _, tc := range cases {
		got, err := PytestINIFromDir(write(tc.files))
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q (%v)\nwant %q", tc.name, got, err, tc.want)
		}
	}
	for _, bad := range []string{
		"[tool.pytest.ini_options]\naddopts = \"\"\"-q\"\"\"\n",
		"[tool.pytest.ini_options]\nenv = {A = \"1\"}\n",
		"[tool.pytest.ini_options]\naddopts = [1, 2]\n",
		"[tool.pytest.ini_options]\naddopts = \"unterminated\n",
		"[tool.pytest.ini_options]\naddopts \"-q\"\n",
	} {
		if got, err := PytestINIFromDir(write(map[string]string{"pyproject.toml": bad})); err == nil {
			t.Errorf("accepted %q as %q", bad, got)
		}
	}
}
