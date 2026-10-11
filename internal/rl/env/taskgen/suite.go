package taskgen

import (
	"context"
	"crypto/sha256"
	_ "embed" // the scorer script
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// The whole-suite scorer.
//
// A long task needs a dense score: the share of a repository's real test suite
// that passes, weighted by milestones (units), that the code under test cannot
// print for itself. WriteSuite turns a task's verifier into suite_score.py (a
// hidden file, run with python3, standard library only) and a hidden spec of the
// test commands, units and guards. The script runs every command in its own
// session with stdin closed and its output in private files, parses the
// command's report (go test -json, a junit XML file, cargo's text), kills every
// process the command left before the next one starts, and only then prints the
// score as the last line of stdout, with its breakdown (env's json-score
// format). RecordSuite runs the same script in record mode on the start and on
// the reference solution, and BuildUnits turns those records into units (tests
// that fail at the start and pass with the reference, every time) and guards
// (tests that pass in both). RecordSuiteSteps and BuildStepUnits do the same
// over a series of states, a reference solution made of several changes: the
// unit of a step holds the tests that step makes pass for good.

//go:embed suite_score.py
var suiteScoreScript string

// Parsers of a SuiteCommand's report.
const (
	// SuiteParserGoTest reads `go test -json` from stdout; a test is <import path>.<TestName>, top-level tests only.
	SuiteParserGoTest = "gotest-json"
	// SuiteParserJUnit reads the junit XML file the command writes to $SUITE_REPORT; a pytest test is
	// <file>::<Class>::<name>, its node id (with junit_family=xunit1), any other testcase <classname>::<name>.
	SuiteParserJUnit = "junit"
	// SuiteParserCargo reads cargo test's output; a test is <target>::<module path>::<name>, doc-tests excluded.
	SuiteParserCargo = "cargo"
)

// DefaultSuiteDeadlineS is the deadline of a suite whose spec sets none, in seconds.
const DefaultSuiteDeadlineS = 1800

// suiteGraceS is how much longer than the suite's deadline the verifier may run:
// the scorer reports at its deadline, and the verifier's timeout only catches a
// scorer that hangs.
const suiteGraceS = 120

// suiteDir holds the scorer's hidden files, relative to the directory the
// verifier runs in.
const suiteDir = ".sleipnir/suite"

// suiteScriptRel is the scorer's path relative to the verifier's directory.
const suiteScriptRel = suiteDir + "/score.py"

// suiteRecordOutput bounds the output kept of a record-mode run: its one line
// lists every test of the suite.
const suiteRecordOutput = 64 << 20

// SuiteSpec is what the scorer runs and how it scores. WriteSuite writes it as
// the hidden file .sleipnir/suite/spec.json.
type SuiteSpec struct {
	// Commands run in order from the verifier's directory.
	Commands []SuiteCommand `json:"commands"`
	// Units are the milestones: score = (Σ weight × passed/tests ÷ Σ weight) × kept guards / guards.
	Units []SuiteUnit `json:"units,omitempty"`
	// Guards are tests that passed at the start and must keep passing; each one broken scales the score down.
	Guards []string `json:"guards,omitempty"`
	// Origin maps a test id to the command that reports it, so that the progress mode can give each command its own
	// share of the sample (SUITE_ARGS). Without it every command runs whole.
	Origin map[string]string `json:"origin,omitempty"`
	// DeadlineS bounds the whole run, in seconds (DefaultSuiteDeadlineS when 0). Commands not reached by then are not
	// run, their tests count as failed, and a score is still printed.
	DeadlineS int `json:"deadline_s,omitempty"`
	// PytestINI is the pytest configuration the junit commands run with, written as .sleipnir/suite/pytest.ini (see
	// PytestINIFromDir): a copy taken from the reference, so an agent that edits pyproject.toml, setup.cfg or tox.ini
	// cannot change how the tests run. "[pytest]" alone when empty. It is not part of spec.json.
	PytestINI string `json:"-"`
}

// SuiteCommand is one test command of a suite.
type SuiteCommand struct {
	// Name identifies the command in logs, records and Origin; unique within the spec.
	Name string `json:"name"`
	// Run is a shell command line. It gets SUITE_TMP (a private directory), SUITE_DIR (the scorer's directory),
	// SUITE_REPORT (where a junit command writes its report) and, in progress mode, SUITE_ARGS: shell words that
	// select the sampled tests in the parser's terms (the default commands apply them with eval "set -- $SUITE_ARGS").
	Run string `json:"run"`
	// Parser is SuiteParserGoTest, SuiteParserJUnit or SuiteParserCargo.
	Parser string `json:"parser"`
	// TimeoutS bounds the command, in seconds (0: up to the suite's deadline). A command that times out is killed
	// with everything it started; the tests it reported before count, the rest have failed.
	TimeoutS int `json:"timeout_s,omitempty"`
	// Cwd is the command's directory relative to the verifier's ("" for it).
	Cwd string `json:"cwd,omitempty"`
}

// SuiteUnit is one milestone of a suite: a weighted set of tests.
type SuiteUnit struct {
	Name   string   `json:"name"`
	Weight float64  `json:"weight"`
	Tests  []string `json:"tests"`
}

// suiteSelect applies the progress mode's selection (SUITE_ARGS) to the
// positional parameters of a default command.
const suiteSelect = `[ -z "${SUITE_ARGS-}" ] || eval "set -- $SUITE_ARGS"; `

// suiteCommandName names a default command after its tool and directory.
func suiteCommandName(tool, dir string) string {
	if dir == "" || dir == "." {
		return tool
	}
	return tool + ":" + dir
}

// GoSuiteCommand runs every package of the Go module in dir (relative to the
// verifier's directory, "" for it): go test -json -count=1 -p 2 ./...
func GoSuiteCommand(dir string) SuiteCommand {
	return SuiteCommand{
		Name: suiteCommandName("go", dir), Parser: SuiteParserGoTest, Cwd: dir,
		Run: `set -- ./...; ` + suiteSelect + `go test -json -count=1 -p 2 "$@"`,
	}
}

// PythonSuiteCommand runs pytest over paths (pytest's own default when none)
// from the project directory dir, without -x so every test runs, with the
// hidden copy of the reference configuration (-c .sleipnir/suite/pytest.ini
// --rootdir .), without the cache and pytest-randomly plugins, a junit report
// in $SUITE_REPORT, and a 120 s limit per test where pytest-timeout is
// installed.
func PythonSuiteCommand(dir string, paths ...string) SuiteCommand {
	args := make([]string, len(paths))
	for i, p := range paths {
		args[i] = shq(p)
	}
	return SuiteCommand{
		Name: suiteCommandName("pytest", dir), Parser: SuiteParserJUnit, Cwd: dir,
		Run: strings.TrimSpace(`set -- `+strings.Join(args, " ")) + `; ` + suiteSelect +
			`t=; python3 -c 'import pytest_timeout' >/dev/null 2>&1 && t='-o timeout=120'; ` +
			`python3 -m pytest -c "$SUITE_DIR/pytest.ini" --rootdir . -p no:cacheprovider -p no:randomly ` +
			`-o junit_family=xunit1 $t --junitxml="$SUITE_REPORT" -q "$@"`,
	}
}

// RustSuiteCommand runs the tests of the cargo package or workspace in dir:
// cargo test --offline --no-fail-fast --tests (doc-tests are not run).
func RustSuiteCommand(dir string) SuiteCommand {
	return SuiteCommand{
		Name: suiteCommandName("cargo", dir), Parser: SuiteParserCargo, Cwd: dir,
		Run: `set --; ` + suiteSelect + `cargo test --offline --no-fail-fast --tests "$@"`,
	}
}

// Validate reports what makes the spec unusable. With scoring, it must also be
// able to score: at least one unit, and weights that add up to more than 0; a
// spec without units only records.
func (s SuiteSpec) Validate(scoring bool) error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if len(s.Commands) == 0 {
		bad("a suite needs at least one command")
	}
	names := map[string]bool{}
	for i, c := range s.Commands {
		switch {
		case strings.TrimSpace(c.Name) == "":
			bad("commands[%d]: the name is empty", i)
		case names[c.Name]:
			bad("commands[%d]: the name %q is used twice", i, c.Name)
		}
		names[c.Name] = true
		if strings.TrimSpace(c.Run) == "" || strings.ContainsRune(c.Run, 0) {
			bad("commands[%d] (%s): the run line is empty or has a NUL byte", i, c.Name)
		}
		switch c.Parser {
		case SuiteParserGoTest, SuiteParserJUnit, SuiteParserCargo:
		default:
			bad("commands[%d] (%s): unknown parser %q (want %s, %s or %s)", i, c.Name, c.Parser, SuiteParserGoTest, SuiteParserJUnit, SuiteParserCargo)
		}
		if c.TimeoutS < 0 {
			bad("commands[%d] (%s): timeout_s is negative", i, c.Name)
		}
		if c.Cwd != "" && c.Cwd != "." && !validRel(c.Cwd) {
			bad("commands[%d] (%s): cwd %q is not a relative path inside the project", i, c.Name, c.Cwd)
		}
	}
	if s.DeadlineS < 0 {
		bad("deadline_s is negative")
	}
	if scoring && len(s.Units) == 0 {
		bad("a suite that scores needs at least one unit")
	}
	if len(s.Units) > env.MaxScoreUnits {
		bad("%d units, more than the %d a score line may carry: group the tests more coarsely", len(s.Units), env.MaxScoreUnits)
	}
	tests := map[string]bool{}
	units := map[string]bool{}
	sum := 0.0
	for i, u := range s.Units {
		switch {
		case u.Name == "" || len(u.Name) > env.MaxScoreUnitName:
			bad("units[%d]: the name must have 1 to %d bytes", i, env.MaxScoreUnitName)
		case units[u.Name]:
			bad("units[%d]: the name %q is used twice", i, u.Name)
		}
		units[u.Name] = true
		if math.IsNaN(u.Weight) || math.IsInf(u.Weight, 0) || u.Weight < 0 {
			bad("units[%d] (%s): the weight must be a finite number >= 0", i, u.Name)
		} else {
			sum += u.Weight
		}
		if len(u.Tests) == 0 {
			bad("units[%d] (%s): no tests", i, u.Name)
		}
		for _, t := range u.Tests {
			if t == "" || tests[t] {
				bad("units[%d] (%s): test %q is empty or listed twice", i, u.Name, t)
			}
			tests[t] = true
		}
	}
	if scoring && len(s.Units) > 0 && !(sum > 0) {
		bad("the units' weights add up to 0")
	}
	for _, g := range s.Guards {
		if g == "" || tests[g] {
			bad("guard %q is empty, listed twice or also a unit's test", g)
		}
		tests[g] = true
	}
	for t, c := range s.Origin {
		if !names[c] {
			bad("origin[%q] names %q, which is not a command", t, c)
		}
	}
	return errors.Join(errs...)
}

// validRel reports whether p is a clean relative slash path that stays inside
// its root.
func validRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") || path.Clean(p) != p {
		return false
	}
	return p != ".." && !strings.HasPrefix(p, "../")
}

// suiteBase is the scorer's directory relative to the repository root: the
// hidden files are written there, and the verifier runs in Repo.Subdir.
func suiteBase(t *rl.Task) string {
	if t.Repo.Subdir == "" {
		return suiteDir
	}
	return path.Join(t.Repo.Subdir, suiteDir)
}

// suiteFiles is the content of the scorer's hidden files, by name within its
// directory.
func suiteFiles(spec SuiteSpec) (map[string][]byte, error) {
	if spec.DeadlineS == 0 {
		spec.DeadlineS = DefaultSuiteDeadlineS
	}
	raw, err := json.MarshalIndent(spec, "", " ")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{"spec.json": append(raw, '\n'), "score.py": []byte(suiteScoreScript)}
	for _, c := range spec.Commands {
		if c.Parser == SuiteParserJUnit {
			ini := spec.PytestINI
			if strings.TrimSpace(ini) == "" {
				ini = "[pytest]\n"
			}
			files["pytest.ini"] = []byte(ini)
			break
		}
	}
	return files, nil
}

// WriteSuite makes the task's verifier the whole-suite scorer of spec: it adds
// the hidden files .sleipnir/suite/{spec.json,score.py} (and pytest.ini when a
// command uses the junit parser) next to the verifier's directory, as blobs in
// hidden when it is not nil and inline otherwise, replacing those of an earlier
// WriteSuite; sets the command to `python3 .sleipnir/suite/score.py`, the pass
// mode to json-score with a start score of 0 and the timeout to the suite's
// deadline plus a grace; protects .sleipnir/** and the files that decide how a
// parser's tests are reported (conftest.py, pytest.ini, sitecustomize.py and
// usercustomize.py for pytest, .cargo directories for cargo); and adds python3
// and the commands' toolchains to Requires.
//
// A spec without units only records (RecordSuite); one that scores needs units
// (BuildUnits). The tests themselves are the caller's to protect or hide: the
// scorer measures them, it does not guard them.
func WriteSuite(task *rl.Task, spec SuiteSpec, hidden events.Blobs) error {
	if task == nil {
		return errors.New("WriteSuite: no task")
	}
	if err := spec.Validate(false); err != nil {
		return fmt.Errorf("suite spec: %w", err)
	}
	files, err := suiteFiles(spec)
	if err != nil {
		return err
	}
	base := suiteBase(task)
	h := map[string]string{}
	for p, ref := range task.Verifier.Hidden {
		if !strings.HasPrefix(p, base+"/") {
			h[p] = ref
		}
	}
	for name, data := range files {
		ref := "text:" + string(data)
		if hidden != nil {
			hh, err := hidden.Put(data)
			if err != nil {
				return fmt.Errorf("storing the suite's %s: %w", name, err)
			}
			ref = "blob:" + string(hh)
		}
		h[base+"/"+name] = ref
	}
	deadline := spec.DeadlineS
	if deadline == 0 {
		deadline = DefaultSuiteDeadlineS
	}
	prot := ".sleipnir/**"
	if task.Repo.Subdir != "" {
		prot = strings.TrimPrefix(escapeGlob(task.Repo.Subdir), "/") + "/.sleipnir/**"
	}
	protected := append(append([]string(nil), task.Verifier.Protected...), prot)
	requires := append([]string(nil), task.Requires...)
	requires = append(requires, "python3")
	for _, c := range spec.Commands {
		switch c.Parser {
		case SuiteParserGoTest:
			requires = append(requires, "go")
		case SuiteParserJUnit:
			protected = append(protected, "conftest.py", "pytest.ini", "sitecustomize.py", "usercustomize.py")
		case SuiteParserCargo:
			requires = append(requires, "cargo")
			protected = append(protected, "**/.cargo/**")
		}
	}
	task.Verifier.Hidden = h
	task.Verifier.Cmd = "python3 " + suiteScriptRel
	task.Verifier.Pass = "json-score"
	task.Verifier.TimeoutS = deadline + suiteGraceS
	task.Verifier.BaselineScore = 0
	task.Verifier.Protected = dedupe(protected)
	task.Requires = dedupe(requires)
	return nil
}

// Outcomes of a test in a SuiteOutcomes.
const (
	SuitePass = "pass"
	SuiteFail = "fail"
	SuiteSkip = "skip"
)

// SuiteOutcomes maps the id of every test a run reported to SuitePass,
// SuiteFail or SuiteSkip. A test it does not list was not reported: it did not
// run, or its command could not report it.
type SuiteOutcomes map[string]string

// SuiteRecord is what RecordSuite saw: the outcome of every test in each run on
// the start and with the reference solution applied.
type SuiteRecord struct {
	Start     []SuiteOutcomes `json:"start"`
	Reference []SuiteOutcomes `json:"reference"`
	// Origin and Parser name, for every test seen, the command that reported it
	// and that command's parser.
	Origin map[string]string `json:"origin"`
	Parser map[string]string `json:"parser"`
}

// RecordSuite runs the task's suite scorer (WriteSuite) in record mode repeats
// times on the starting state and repeats times with the reference solution gold
// (a patch) applied, each run in a fresh clean checkout with the hidden files in
// place, exactly as verification runs, and returns the outcome of every test.
// A failure of the scorer itself (no record printed, the reference not applying)
// is an error.
func RecordSuite(ctx context.Context, ws *env.Workspaces, task rl.Task, gold []byte, repeats int, hidden events.Blobs) (SuiteRecord, error) {
	steps, err := recordSuite(ctx, ws, task, []suiteStep{{"start", nil}, {"reference solution", gold}}, repeats, hidden)
	rec := SuiteRecord{Origin: steps.Origin, Parser: steps.Parser}
	if len(steps.Runs) > 0 {
		rec.Start = steps.Runs[0]
	}
	if len(steps.Runs) > 1 {
		rec.Reference = steps.Runs[1]
	}
	return rec, err
}

// SuiteSteps is what RecordSuiteSteps saw: the outcome of every test in each
// run at each step.
type SuiteSteps struct {
	// Runs[k] are the runs at step k: the start for k = 0, the start with the
	// k-th patch applied after.
	Runs [][]SuiteOutcomes `json:"runs"`
	// Origin and Parser name, for every test seen, the command that reported it
	// and that command's parser.
	Origin map[string]string `json:"origin"`
	Parser map[string]string `json:"parser"`
}

// RecordSuiteSteps is RecordSuite over a sequence of states: it runs the task's
// suite scorer in record mode repeats times on each, step k being the starting
// state with patches[k] applied (nil: the start itself), each run in a fresh
// clean checkout with the hidden files in place, and returns the outcome of
// every test at every step. A generator whose reference solution is a series of
// changes (taskgen's chains) records the state after each one this way and
// chooses its milestones with BuildStepUnits.
func RecordSuiteSteps(ctx context.Context, ws *env.Workspaces, task rl.Task, patches [][]byte, repeats int, hidden events.Blobs) (SuiteSteps, error) {
	steps := make([]suiteStep, len(patches))
	for k, p := range patches {
		steps[k] = suiteStep{fmt.Sprintf("step %d", k), p}
	}
	return recordSuite(ctx, ws, task, steps, repeats, hidden)
}

// suiteStep is one state a record is taken of.
type suiteStep struct {
	label string
	patch []byte
}

// recordSuite runs the record mode on each step in order and stops at the first
// failure, returning what it recorded until then.
func recordSuite(ctx context.Context, ws *env.Workspaces, task rl.Task, steps []suiteStep, repeats int, hidden events.Blobs) (SuiteSteps, error) {
	if ws == nil {
		return SuiteSteps{}, errors.New("RecordSuite: no workspaces")
	}
	if task.Verifier.Cmd != "python3 "+suiteScriptRel {
		return SuiteSteps{}, errors.New("RecordSuite: the task's verifier is not the suite scorer (WriteSuite first)")
	}
	repeats = max(repeats, 1)
	rt := task
	rt.Verifier.Cmd = "SUITE_MODE=record " + task.Verifier.Cmd
	rt.Verifier.Pass = "exit0"
	rt.Verifier.BaselineScore = 0
	rec := SuiteSteps{Origin: map[string]string{}, Parser: map[string]string{}}
	run := func(label string, patch []byte) ([]SuiteOutcomes, error) {
		outs := make([]string, repeats)
		opts := env.VerifyOptions{
			Workspaces: ws, HiddenBlobs: hidden, Repeats: repeats, PassPolicy: env.PassAll, MaxOutput: suiteRecordOutput,
			Stdout: func(i int, s string) {
				if i >= 0 && i < len(outs) {
					outs[i] = s
				}
			},
		}
		res, err := env.VerifyPatch(ctx, rt, patch, opts)
		if err != nil {
			return nil, fmt.Errorf("recording the suite on the %s: %w", label, err)
		}
		if res.Rejected != "" {
			return nil, fmt.Errorf("recording the suite on the %s: %s", label, res.Rejected)
		}
		runs := make([]SuiteOutcomes, 0, repeats)
		for i, out := range outs {
			cmds, err := parseSuiteRecord(out)
			if err != nil {
				return nil, fmt.Errorf("recording the suite on the %s, run %d: %v\n%s", label, i+1, err, tail(res.Log, 2000))
			}
			o := SuiteOutcomes{}
			for _, c := range cmds {
				for id, oc := range c.Tests {
					mergeOutcome(o, id, oc)
					if _, ok := rec.Origin[id]; !ok {
						rec.Origin[id], rec.Parser[id] = c.Name, c.Parser
					}
				}
			}
			runs = append(runs, o)
		}
		return runs, nil
	}
	for _, s := range steps {
		runs, err := run(s.label, s.patch)
		if err != nil {
			return rec, err
		}
		rec.Runs = append(rec.Runs, runs)
	}
	return rec, nil
}

// suiteRecordCommand is one command of the record-mode line.
type suiteRecordCommand struct {
	Name   string            `json:"name"`
	Parser string            `json:"parser"`
	Tests  map[string]string `json:"tests"`
}

// parseSuiteRecord reads the record-mode line, the last line of stdout.
func parseSuiteRecord(stdout string) ([]suiteRecordCommand, error) {
	lines := strings.Split(strings.TrimRight(stdout, "\r\n\t "), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	var line struct {
		Record   int                  `json:"suite_record"`
		Commands []suiteRecordCommand `json:"commands"`
	}
	if last == "" || json.Unmarshal([]byte(last), &line) != nil || line.Record != 1 {
		return nil, errors.New("the suite scorer printed no record")
	}
	for _, c := range line.Commands {
		for id, o := range c.Tests {
			if o != SuitePass && o != SuiteFail && o != SuiteSkip {
				return nil, fmt.Errorf("the record has outcome %q for %s", o, id)
			}
		}
	}
	return line.Commands, nil
}

// outcomeRank orders outcomes for mergeOutcome: a test passes only when every
// report of it says so.
var outcomeRank = map[string]int{SuitePass: 0, SuiteSkip: 1, SuiteFail: 2}

// mergeOutcome records one report of a test: fail beats skip beats pass.
func mergeOutcome(o SuiteOutcomes, id, outcome string) {
	if prev, ok := o[id]; !ok || outcomeRank[outcome] > outcomeRank[prev] {
		o[id] = outcome
	}
}

// SuiteGroup names the unit a test belongs to, from its id and the parser that
// reported it.
type SuiteGroup func(id, parser string) string

// DefaultSuiteGroup groups tests by Go package (the import path of
// pkg/path.TestName), by Python file (the file of file::Class::name, or the
// classname of another junit test) and by Rust module (target::module of
// target::module::test).
func DefaultSuiteGroup(id, parser string) string {
	switch parser {
	case SuiteParserGoTest:
		if i := strings.LastIndex(id, "."); i > 0 {
			return id[:i]
		}
	case SuiteParserJUnit:
		if i := strings.Index(id, "::"); i > 0 {
			return id[:i]
		}
	case SuiteParserCargo:
		if i := strings.LastIndex(id, "::"); i > 0 {
			return id[:i]
		}
	}
	return id
}

// SuiteSelection is what BuildUnits chose from a record.
type SuiteSelection struct {
	// Units hold the tests that failed in every start run and passed in every reference run, grouped, each unit
	// with weight 1, in name order.
	Units []SuiteUnit `json:"units"`
	// Guards are the tests that passed in every run, start and reference.
	Guards []string `json:"guards,omitempty"`
	// Origin maps every test of Units and Guards to the command that reported it.
	Origin map[string]string `json:"origin,omitempty"`
	// Dropped are the other tests: flaky ones, and those that do not pass with the reference every time.
	Dropped []string `json:"dropped,omitempty"`
}

// Apply puts the selection's units, guards and origins into spec.
func (s SuiteSelection) Apply(spec *SuiteSpec) {
	spec.Units = append([]SuiteUnit(nil), s.Units...)
	spec.Guards = append([]string(nil), s.Guards...)
	spec.Origin = make(map[string]string, len(s.Origin))
	for k, v := range s.Origin {
		spec.Origin[k] = v
	}
}

// BuildUnits chooses the tests a suite scores from rec. A test that failed (or
// was skipped, or was not reported) in every start run and passed in every
// reference run goes to the unit group names (DefaultSuiteGroup when nil); one
// that passed in every run, start and reference, is a guard; every other test is
// dropped. A unit name longer than env.MaxScoreUnitName bytes is shortened with
// a hash. It is an error when no test qualifies, or when there are more units
// than a score line may carry.
func BuildUnits(rec SuiteRecord, group SuiteGroup) (SuiteSelection, error) {
	if len(rec.Start) == 0 || len(rec.Reference) == 0 {
		return SuiteSelection{}, errors.New("the record needs runs on the start and on the reference")
	}
	if group == nil {
		group = DefaultSuiteGroup
	}
	seen := map[string]bool{}
	var ids []string
	for _, runs := range [][]SuiteOutcomes{rec.Start, rec.Reference} {
		for _, r := range runs {
			for id := range r {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	sort.Strings(ids)
	all := func(runs []SuiteOutcomes, id string, want bool) bool {
		for _, r := range runs {
			if (r[id] == SuitePass) != want {
				return false
			}
		}
		return true
	}
	sel := SuiteSelection{Origin: map[string]string{}}
	byUnit := map[string][]string{}
	for _, id := range ids {
		switch {
		case all(rec.Reference, id, true) && all(rec.Start, id, false):
			name := unitName(group(id, rec.Parser[id]))
			byUnit[name] = append(byUnit[name], id)
		case all(rec.Reference, id, true) && all(rec.Start, id, true):
			sel.Guards = append(sel.Guards, id)
		default:
			sel.Dropped = append(sel.Dropped, id)
			continue
		}
		if c, ok := rec.Origin[id]; ok {
			sel.Origin[id] = c
		}
	}
	if len(byUnit) == 0 {
		return sel, errors.New("no test fails on the start and passes with the reference solution every time: nothing to score")
	}
	if len(byUnit) > env.MaxScoreUnits {
		return sel, fmt.Errorf("%d units, more than the %d a score line may carry: group the tests more coarsely", len(byUnit), env.MaxScoreUnits)
	}
	names := make([]string, 0, len(byUnit))
	for n := range byUnit {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sel.Units = append(sel.Units, SuiteUnit{Name: n, Weight: 1, Tests: byUnit[n]})
	}
	return sel, nil
}

// SuiteMilestone names and weighs the unit of one step of a SuiteSteps record.
type SuiteMilestone struct {
	Name   string
	Weight float64
}

// BuildStepUnits chooses the tests a suite scores from a record of steps
// (RecordSuiteSteps): milestones[k-1] is the unit of step k, for k = 1 ..
// len(steps.Runs)-1. A test belongs to the unit of step k when it fails (or is
// skipped, or is not reported) in every run of every step before k and passes
// in every run of step k and of every step after it: the step makes it pass and
// no later step breaks it. A test that passes in every run of every step is a
// guard; every other test (flaky at some step, failing at the last, or passing
// and failing again) is dropped. A step whose unit would hold no test has no
// unit. Units keep the order of the steps, their names cut to
// env.MaxScoreUnitName bytes as BuildUnits does.
//
// Scored with these units, the state at step k reaches exactly the weight of
// the units up to k over the weight of all units, with every guard kept, so a
// generator can check each step against that value. It is an error when the
// record has fewer than two steps, the milestones do not match the steps, or no
// test makes a unit.
func BuildStepUnits(steps SuiteSteps, milestones []SuiteMilestone) (SuiteSelection, error) {
	n := len(steps.Runs)
	if n < 2 {
		return SuiteSelection{}, errors.New("the record needs runs on the start and on at least one step")
	}
	if len(milestones) != n-1 {
		return SuiteSelection{}, fmt.Errorf("%d milestones for %d steps after the start", len(milestones), n-1)
	}
	for k, runs := range steps.Runs {
		if len(runs) == 0 {
			return SuiteSelection{}, fmt.Errorf("step %d has no runs", k)
		}
	}
	seen := map[string]bool{}
	var ids []string
	for _, runs := range steps.Runs {
		for _, r := range runs {
			for id := range r {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	sort.Strings(ids)
	// state is 1 when a test passed in every run of a step, 0 when it passed in
	// none, and -1 when the runs disagree.
	state := func(k int, id string) int {
		passed := 0
		for _, r := range steps.Runs[k] {
			if r[id] == SuitePass {
				passed++
			}
		}
		switch passed {
		case 0:
			return 0
		case len(steps.Runs[k]):
			return 1
		}
		return -1
	}
	sel := SuiteSelection{Origin: map[string]string{}}
	byStep := make([][]string, n)
	for _, id := range ids {
		first := -1 // the step from which the test passes for good
		ok := true
		for k := 0; k < n && ok; k++ {
			switch s := state(k, id); {
			case s < 0:
				ok = false
			case s == 1 && first < 0:
				first = k
			case s == 0 && first >= 0:
				ok = false
			}
		}
		switch {
		case !ok || first < 0:
			sel.Dropped = append(sel.Dropped, id)
			continue
		case first == 0:
			sel.Guards = append(sel.Guards, id)
		default:
			byStep[first] = append(byStep[first], id)
		}
		if c, ok := steps.Origin[id]; ok {
			sel.Origin[id] = c
		}
	}
	names := map[string]bool{}
	for k := 1; k < n; k++ {
		if len(byStep[k]) == 0 {
			continue
		}
		m := milestones[k-1]
		name := unitName(m.Name)
		if names[name] {
			return sel, fmt.Errorf("milestone %d: the name %q is used twice", k, name)
		}
		names[name] = true
		sel.Units = append(sel.Units, SuiteUnit{Name: name, Weight: m.Weight, Tests: byStep[k]})
	}
	if len(sel.Units) == 0 {
		return sel, errors.New("no test fails at the start and passes from some step on, every time: nothing to score")
	}
	return sel, nil
}

// unitName keeps a unit name within env.MaxScoreUnitName bytes: a longer one is
// cut (at a rune boundary) and given a hash of the whole, so distinct long names
// stay distinct. An empty name becomes "(none)".
func unitName(n string) string {
	if n == "" {
		return "(none)"
	}
	if len(n) <= env.MaxScoreUnitName {
		return n
	}
	sum := sha256.Sum256([]byte(n))
	suffix := "~" + hex.EncodeToString(sum[:6])
	cut := env.MaxScoreUnitName - len(suffix)
	for cut > 0 && (n[cut]&0xC0) == 0x80 {
		cut--
	}
	return n[:cut] + suffix
}
