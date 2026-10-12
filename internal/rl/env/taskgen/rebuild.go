package taskgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// Rebuild tasks.
//
// A rebuild task asks a team to write a library again from its specification.
// Rebuild takes a repository at a reference version and a scope of packages
// (Go package directories or Python packages), and makes the start state the
// repository with the scope's sources reduced to stubs (RebuildStubs) or
// removed and described in API.md (RebuildSpec), written as a repository of its
// own with one commit. The scope's tests are taken out of the start and become
// the hidden suite, scored by the whole-suite scorer: one unit per package (Go)
// or module (Python), weighted by its lines and listed in the order the
// packages import each other, holding the tests that pass with the reference
// and fail on the start every time. The reference sources are the gold.

// Tiers of a rebuild task.
const (
	// RebuildStubs keeps the scope's API (names, signatures, types, constants and documentation) with function
	// bodies that panic (Go) or raise NotImplementedError (Python).
	RebuildStubs = "stubs"
	// RebuildSpec removes the scope's sources and describes their API in API.md.
	RebuildSpec = "spec"
)

// Reasons Rebuild rejects a repository and scope for (RebuildRejection.Reason).
const (
	ReasonRebuildScope     = "the scope cannot be rebuilt"
	ReasonRebuildTooSmall  = "too few in-scope lines"
	ReasonRebuildTooLarge  = "too many in-scope lines"
	ReasonRebuildFewTests  = "too few tests to score"
	ReasonRebuildSlowSuite = "the reference test suite is too slow"
	ReasonRebuildLeak      = "the scope can be imported from outside the start tree"
	ReasonRebuildStubs     = "the stubs do not build"
	ReasonRebuildSelfCheck = "the self-check failed"
)

// RebuildRejection is the error Rebuild returns when a repository and scope make no sound task: Reason is one of the
// ReasonRebuild constants and Detail says what was found.
type RebuildRejection struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Error is the reason followed by the detail.
func (r *RebuildRejection) Error() string {
	if r.Detail == "" {
		return r.Reason
	}
	return r.Reason + ": " + r.Detail
}

// reject makes a RebuildRejection.
func reject(reason, format string, args ...any) error {
	return &RebuildRejection{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// RebuildGates are the sizes a rebuild task must have. A zero field takes its default (DefaultRebuildGates).
type RebuildGates struct {
	// MinLOC and MaxLOC bound the lines of the scope's sources that are neither blank nor only a comment.
	MinLOC int `json:"min_loc"`
	MaxLOC int `json:"max_loc"`
	// MinTests is the fewest tests the score may count.
	MinTests int `json:"min_tests"`
	// MaxSuite bounds one run of the whole suite with the reference sources.
	MaxSuite time.Duration `json:"max_suite_ns"`
}

// DefaultRebuildGates are 2,000 to 10,000 in-scope lines, at least 150 scored tests and a reference suite under 10
// minutes.
func DefaultRebuildGates() RebuildGates {
	return RebuildGates{MinLOC: 2000, MaxLOC: 10000, MinTests: 150, MaxSuite: 10 * time.Minute}
}

// withDefaults fills the zero fields of g from DefaultRebuildGates.
func (g RebuildGates) withDefaults() RebuildGates {
	d := DefaultRebuildGates()
	if g.MinLOC <= 0 {
		g.MinLOC = d.MinLOC
	}
	if g.MaxLOC <= 0 {
		g.MaxLOC = d.MaxLOC
	}
	if g.MinTests <= 0 {
		g.MinTests = d.MinTests
	}
	if g.MaxSuite <= 0 {
		g.MaxSuite = d.MaxSuite
	}
	return g
}

// RebuildOptions configures Rebuild.
type RebuildOptions struct {
	// Workspaces runs the checks, the recording and the self-check (required).
	Workspaces *env.Workspaces
	// Commit is the reference version (default HEAD).
	Commit string
	// Scope lists the packages to rebuild: Go package directories relative to the repository ("dir/..." with every
	// package below it, or a package's import path), or Python package names. All of one language.
	Scope []string
	// Tier is RebuildStubs (default) or RebuildSpec.
	Tier string
	// Seal is the directory the start state is written to, as <Seal>/<task id>.git (required). SealPath, when set,
	// is how the task names that directory (<SealPath>/<task id>.git, a path relative to where rollouts run);
	// otherwise the task names the absolute path.
	Seal     string
	SealPath string
	// HiddenBlobs stores the hidden files (tests and the scorer); nil inlines them as text. GoldBlobs receives the
	// reference solution (Meta.gold_blob), which `rl tasks check` needs.
	HiddenBlobs events.Blobs
	GoldBlobs   events.Blobs
	// Gates are the sizes the task must have.
	Gates RebuildGates
	// Repeats is how many times the suite is recorded on the start and on the reference (default 3): a test counts
	// only when every run agrees.
	Repeats int
	// DeadlineS bounds one run of the whole suite, in seconds (default DefaultSuiteDeadlineS).
	DeadlineS int
	// Setup overrides the detected setup commands (an empty non-nil slice means none).
	Setup []string
	// AllowLicenses lists acceptable SPDX identifiers; when set, a repository whose licence at Commit is not on the
	// list is refused (ErrLicense).
	AllowLicenses []string
	// IDPrefix prefixes the task id and is one of its tags (default: the repository directory name).
	IDPrefix string
	// Tags are added to the task; fields set in Budget override the derived budget.
	Tags   []string
	Budget rl.Budget
	// Python is the interpreter that analyses a Python scope (default python3).
	Python string
	// Progress, when set, receives a line per step.
	Progress func(string)
}

// RebuildReport is what Rebuild found and did.
type RebuildReport struct {
	Repo   string   `json:"repo,omitempty"`
	Commit string   `json:"commit,omitempty"`
	Lang   string   `json:"lang,omitempty"`
	Tier   string   `json:"tier"`
	Scope  []string `json:"scope"`
	// ID is the task's id, once it has one.
	ID    string       `json:"id,omitempty"`
	Gates RebuildGates `json:"gates"`
	// LOC is the scope's lines; Packages are its packages (Go) or modules (Python) in dependency order.
	LOC      int              `json:"loc"`
	Packages []RebuildPackage `json:"packages,omitempty"`
	// Hidden is the number of test files taken out of the start.
	Hidden int `json:"hidden,omitempty"`
	// TestSupport are the packages named by the scope that stay at the reference: each is used by the tests of
	// packages of the scope that do not depend on it otherwise (an assertion library), listed here with them.
	TestSupport map[string][]string `json:"test_support,omitempty"`
	// Units are the milestones in dependency order; Tests the tests they hold, Dropped the tests left out (passing on
	// the start, flaky, or failing with the reference).
	Units   []RebuildUnit `json:"units,omitempty"`
	Tests   int           `json:"tests"`
	Dropped int           `json:"dropped"`
	// StartScore and ReferenceScore are the self-check's scores; SuiteMs the reference suite's run time.
	StartScore     *float64 `json:"start_score,omitempty"`
	ReferenceScore *float64 `json:"reference_score,omitempty"`
	SuiteMs        int64    `json:"suite_ms,omitempty"`
	// Rejected says why no task was made.
	Rejected *RebuildRejection `json:"rejected,omitempty"`
	Duration time.Duration     `json:"duration_ns"`
}

// RebuildPackage is a package (Go) or module (Python) of the scope.
type RebuildPackage struct {
	Name    string   `json:"name"`
	Dir     string   `json:"dir"`
	Files   int      `json:"files"`
	LOC     int      `json:"loc"`
	Imports []string `json:"imports,omitempty"`
}

// RebuildUnit is a milestone of the score: a package or module, its weight (its lines) and its tests.
type RebuildUnit struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Tests  int     `json:"tests"`
}

// rebuildPkg is a package (Go) or module (Python) of the scope: a unit of the task.
type rebuildPkg struct {
	Name    string   // a Go import path or a Python module name
	Dir     string   // repository-relative
	Files   []string // its sources, repository-relative
	LOC     int
	Imports []string // the scope's packages it imports
}

// rebuildLib is a scope resolved at the reference, and what its task needs.
type rebuildLib struct {
	lang      string
	root      string            // Go: the module's directory; Python: the project's ("." for the repository root)
	module    string            // Go: the module path
	tops      []string          // Python: the top-level packages of the scope
	pkgs      []*rebuildPkg     // in dependency order
	sources   map[string][]byte // the scope's sources at the reference
	hidden    map[string][]byte // test files taken out of the start, written at verification
	stubs     map[string][]byte // tier stubs: each source's stub
	api       string            // tier spec: API.md
	protect   []string          // test files the agent must not add or change
	manifests []string          // the language's build manifests
	setup     []string
	command   SuiteCommand
	pytestINI string
	testUnit  map[string]string // Python: a test file and the module it exercises
	fallback  string            // Python: the unit of a test file that refers to no module of the scope
	// support are the packages named by the scope that stay at the reference (Go: test support, see rebuildGo), each
	// with the packages whose tests use it.
	support map[string][]string
}

// group names the unit of a test of the recorded suite (BuildUnits): its Go package, or the module its Python test
// file exercises.
func (l *rebuildLib) group(id, parser string) string {
	if l.lang == Python {
		file, _, _ := strings.Cut(id, "::")
		if u, ok := l.testUnit[path.Join(l.root, file)]; ok {
			return u
		}
		return l.fallback
	}
	return DefaultSuiteGroup(id, parser)
}

// Rebuild makes a rebuild task of the scope of the repository at repoPath (see RebuildOptions), or says why it
// cannot:
//
//   - the scope is resolved at the reference and its lines counted (Gates.MinLOC to Gates.MaxLOC);
//   - the start state is written to <Seal>/<id>.git: the reference with the scope's sources stubbed (RebuildStubs) or
//     removed and described in API.md (RebuildSpec), and the scope's tests removed; the reference sources' diff from
//     it is the gold;
//   - in the environment rollouts use, the scope must not be importable from outside the start tree (Go: its module
//     in neither the module cache, GOPATH nor vendor/; Python: no package by its name on the import path outside the
//     tree, the user site and PYTHONPATH included), and Go stubs must build with their tests;
//   - the suite is recorded Repeats times on the start and with the gold (RecordSuite), and the tests that fail on the
//     start and pass with the gold every time become the units, one per package or module, weighted by its lines and
//     in dependency order; there are no guards (Gates.MinTests);
//   - the self-check scores the task as rollouts will: the start must score 0 and the reference exactly 1, within
//     Gates.MaxSuite.
//
// A repository and scope that fail a gate or a check give a *RebuildRejection (also in the report); anything else that
// fails is an error. The start repository is removed unless a task is returned.
func Rebuild(ctx context.Context, repoPath string, opts RebuildOptions) (*rl.Task, *RebuildReport, error) {
	started := time.Now()
	if opts.Tier == "" {
		opts.Tier = RebuildStubs
	}
	opts.Gates = opts.Gates.withDefaults()
	if opts.Repeats <= 0 {
		opts.Repeats = 3
	}
	if opts.DeadlineS <= 0 {
		opts.DeadlineS = DefaultSuiteDeadlineS
	}
	rep := &RebuildReport{Tier: opts.Tier, Scope: opts.Scope, Gates: opts.Gates}
	defer func() { rep.Duration = time.Since(started) }()
	fail := func(err error) (*rl.Task, *RebuildReport, error) {
		var rj *RebuildRejection
		if errors.As(err, &rj) {
			rep.Rejected = rj
		}
		return nil, rep, err
	}
	switch {
	case opts.Workspaces == nil:
		return nil, rep, errors.New("RebuildOptions.Workspaces is required")
	case opts.Seal == "":
		return nil, rep, errors.New("RebuildOptions.Seal is required: the start state is a repository of its own")
	case len(opts.Scope) == 0:
		return nil, rep, errors.New("the scope is empty")
	case opts.Tier != RebuildStubs && opts.Tier != RebuildSpec:
		return nil, rep, fmt.Errorf("unknown tier %q (want %s or %s)", opts.Tier, RebuildStubs, RebuildSpec)
	}
	progress := func(format string, args ...any) {
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf(format, args...))
		}
	}
	g := opts.Workspaces.Git()
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, rep, err
	}
	gd, ok := g.GitDirOf(ctx, abs)
	if !ok {
		return nil, rep, fmt.Errorf("%s is not the root of a git repository", repoPath)
	}
	r := &repo{g: g, gitDir: gd}
	rev := opts.Commit
	if rev == "" {
		rev = "HEAD"
	}
	commit, err := r.resolve(ctx, rev)
	if err != nil {
		return nil, rep, fmt.Errorf("resolving %s: %w", rev, err)
	}
	rep.Commit, rep.Repo = commit, repoIdentity(ctx, r, abs)
	if opts.IDPrefix == "" {
		opts.IDPrefix = sanitizeID(filepath.Base(abs))
	}
	license := ""
	for _, f := range licenseFiles {
		if b, err := r.show(ctx, commit, f); err == nil {
			license = DetectLicense(string(b))
			break
		}
	}
	if len(opts.AllowLicenses) > 0 && !containsFold(opts.AllowLicenses, license) {
		return nil, rep, fmt.Errorf("%w: found %q, allowed %v", ErrLicense, license, opts.AllowLicenses)
	}
	files, err := r.listFiles(ctx, commit)
	if err != nil {
		return nil, rep, err
	}

	// The scope, its size, its stubs or API, its tests.
	gate := func(loc int) error {
		rep.LOC = loc
		switch {
		case loc < opts.Gates.MinLOC:
			return reject(ReasonRebuildTooSmall, "%d lines in scope, at least %d needed", loc, opts.Gates.MinLOC)
		case loc > opts.Gates.MaxLOC:
			return reject(ReasonRebuildTooLarge, "%d lines in scope, at most %d allowed", loc, opts.Gates.MaxLOC)
		}
		return nil
	}
	lib, err := planRebuild(ctx, r, commit, files, opts, gate)
	if err != nil {
		return fail(err)
	}
	rep.Lang, rep.Hidden, rep.TestSupport = lib.lang, len(lib.hidden), lib.support
	for _, p := range lib.pkgs {
		rep.Packages = append(rep.Packages, RebuildPackage{Name: p.Name, Dir: p.Dir, Files: len(p.Files), LOC: p.LOC, Imports: p.Imports})
	}
	progress("scope: %d %s, %d lines, %d hidden test files", len(lib.pkgs), unitNoun(lib.lang, len(lib.pkgs)), rep.LOC, len(lib.hidden))

	// The start state and the gold.
	names := make([]string, len(lib.pkgs))
	for i, p := range lib.pkgs {
		names[i] = p.Name
	}
	sum := sha256.Sum256([]byte(strings.Join(append([]string{commit, lib.lang, opts.Tier}, names...), "\x00")))
	id := fmt.Sprintf("%s-rebuild-%s-%s", opts.IDPrefix, opts.Tier, hex.EncodeToString(sum[:4]))
	rep.ID = id
	sources := sortedPaths(lib.sources)
	edits := map[string][]byte{}
	for p := range lib.hidden {
		edits[p] = nil
	}
	for _, p := range sources {
		edits[p] = nil
		if opts.Tier == RebuildStubs {
			if edits[p] = lib.stubs[p]; edits[p] == nil {
				return nil, rep, fmt.Errorf("internal error: no stub of %s", p)
			}
		}
	}
	apiPath := ""
	if opts.Tier == RebuildSpec {
		apiPath = apiFile(files)
		edits[apiPath] = []byte(lib.api)
	}
	// The start is written and checked under a staging name and takes its own name only once the task is accepted, so
	// a run that is rejected or fails never removes the start of an earlier accepted task (the same id, the same path).
	final := filepath.Join(opts.Seal, id+".git")
	dest := filepath.Join(opts.Seal, "."+id+".pending.git")
	startCommit, gold, err := writeStart(ctx, g, gd, commit, dest, edits, sources)
	if err != nil {
		return nil, rep, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dest)
		}
	}()
	if len(gold) == 0 {
		return fail(reject(ReasonRebuildScope, "the start state is the reference: nothing to rebuild"))
	}

	t, err := rebuildTask(ctx, r, lib, opts, rebuildTaskInput{
		id: id, commit: commit, start: startCommit, dest: dest, license: license, repoName: rep.Repo,
		gold: gold, apiPath: apiPath, loc: rep.LOC,
	})
	if err != nil {
		return nil, rep, err
	}
	// The suite is recorded with a deadline just over the gate on its time: a slower reference is rejected anyway, and
	// the self-check, with the task's own deadline, says so.
	spec := SuiteSpec{Commands: []SuiteCommand{lib.command}, PytestINI: lib.pytestINI}
	spec.DeadlineS = min(opts.DeadlineS, int(math.Ceil(opts.Gates.MaxSuite.Seconds()))+60)
	if err := WriteSuite(t, spec, opts.HiddenBlobs); err != nil {
		return nil, rep, err
	}

	// What must hold in the environment before anything is recorded.
	progress("checking the start in the environment")
	if err := rebuildChecks(ctx, t, lib, opts); err != nil {
		return fail(err)
	}

	// The units.
	progress("recording the suite: %d runs on the start, %d with the reference", opts.Repeats, opts.Repeats)
	rec, err := RecordSuite(ctx, opts.Workspaces, *t, gold, opts.Repeats, opts.HiddenBlobs)
	if err != nil {
		return nil, rep, err
	}
	sel, err := BuildUnits(rec, lib.group)
	if err != nil {
		return fail(reject(ReasonRebuildFewTests, "%v", err))
	}
	units, origin, dropped := lib.units(sel)
	rep.Dropped = len(dropped)
	for _, u := range units {
		rep.Units = append(rep.Units, RebuildUnit{Name: u.Name, Weight: u.Weight, Tests: len(u.Tests)})
		rep.Tests += len(u.Tests)
	}
	if rep.Tests < opts.Gates.MinTests {
		return fail(reject(ReasonRebuildFewTests, "%d tests fail on the start and pass with the reference every time, at least %d needed", rep.Tests, opts.Gates.MinTests))
	}
	spec.Units, spec.Guards, spec.Origin, spec.DeadlineS = units, nil, origin, opts.DeadlineS
	if err := WriteSuite(t, spec, opts.HiddenBlobs); err != nil {
		return nil, rep, err
	}

	// The self-check.
	progress("self-check: scoring the start and the reference")
	crep, err := env.CheckTask(ctx, *t, gold, env.VerifyOptions{Workspaces: opts.Workspaces, HiddenBlobs: opts.HiddenBlobs})
	if crep.Baseline.Version != "" {
		s := crep.Baseline.Score
		rep.StartScore = &s
	}
	if crep.Gold.Version != "" {
		s := crep.Gold.Score
		rep.ReferenceScore = &s
		if n := len(crep.Gold.Runs); n > 0 {
			rep.SuiteMs = crep.Gold.Runs[n-1].Ms
		}
	}
	suite := time.Duration(rep.SuiteMs) * time.Millisecond
	switch {
	case crep.Gold.Version != "" && suite >= opts.Gates.MaxSuite:
		return fail(reject(ReasonRebuildSlowSuite, "one run takes %s, the limit is %s", suite.Round(time.Second), opts.Gates.MaxSuite))
	case errors.Is(err, env.ErrBaselinePasses), errors.Is(err, env.ErrBaselineBroken), errors.Is(err, env.ErrGoldFails), errors.Is(err, env.ErrBaselineMismatch):
		return fail(reject(ReasonRebuildSelfCheck, "%s", firstLine(err.Error())))
	case err != nil:
		return nil, rep, err
	case crep.Gold.Score != 1 || crep.Baseline.Score != 0:
		return fail(reject(ReasonRebuildSelfCheck, "the start scores %g and the reference %g, want 0 and 1", crep.Baseline.Score, crep.Gold.Score))
	}
	if err := env.ValidateTask(*t); err != nil {
		return nil, rep, fmt.Errorf("internal error: generated an invalid task: %w", err)
	}
	if err := installDir(dest, final); err != nil {
		return nil, rep, err
	}
	keep = true
	if t.Repo.Path, err = filepath.Abs(final); err != nil {
		return nil, rep, err
	}
	if opts.SealPath != "" {
		t.Repo.Path = path.Join(filepath.ToSlash(opts.SealPath), id+".git")
	}
	progress("accepted %s: %d units, %d tests", id, len(units), rep.Tests)
	return t, rep, nil
}

// planRebuild resolves the scope in the language its first entry names: Go when the entry is ".", has a slash, ends in
// "..." or names a directory that holds .go files; Python otherwise. gate is called with the scope's lines as soon as
// they are counted.
func planRebuild(ctx context.Context, r *repo, commit string, files []string, opts RebuildOptions, gate func(int) error) (*rebuildLib, error) {
	e := strings.TrimSpace(opts.Scope[0])
	isGo := e == "." || strings.Contains(e, "/") || strings.HasSuffix(e, "...")
	for _, f := range files {
		if !isGo && path.Dir(f) == e && strings.HasSuffix(f, ".go") {
			isGo = true
		}
	}
	if isGo {
		return rebuildGo(ctx, r, commit, files, opts, gate)
	}
	return rebuildPython(ctx, r, commit, files, opts, gate)
}

// rebuildTaskInput is what rebuildTask needs besides the scope.
type rebuildTaskInput struct {
	id, commit, start, dest, license, repoName, apiPath string
	gold                                                []byte
	loc                                                 int
}

// rebuildTask assembles the task (before WriteSuite): its start repository, hidden tests, protected paths, prompt,
// team, budget, tags and provenance.
func rebuildTask(ctx context.Context, r *repo, lib *rebuildLib, opts RebuildOptions, in rebuildTaskInput) (*rl.Task, error) {
	hidden := map[string]string{}
	hh := sha256.New()
	for _, p := range sortedPaths(lib.hidden) {
		data := lib.hidden[p]
		if opts.HiddenBlobs != nil {
			h, err := opts.HiddenBlobs.Put(data)
			if err != nil {
				return nil, err
			}
			hidden[p] = "blob:" + string(h)
		} else {
			if !utf8ValidText(data) {
				return nil, fmt.Errorf("hidden file %s is binary and there is no blob store to hold it", p)
			}
			hidden[p] = "text:" + string(data)
		}
		hh.Write([]byte(p))
		hh.Write([]byte{0})
		hh.Write(data)
		hh.Write([]byte{0})
	}
	// What the agent must leave as it is: the documentation, the examples, the build manifests, API.md and the test
	// files of the scope's directories (the hidden tests are written there).
	protected := []string{"README*", "/docs/**", "/doc/**", "/examples/**", "/example/**", "/_examples/**", "LICENSE*", "COPYING*"}
	protected = append(protected, lib.manifests...)
	if in.apiPath != "" {
		protected = append(protected, escapeGlob(in.apiPath))
	}
	protected = append(protected, lib.protect...)
	protected = append(protected, ".github/**", ".gitlab-ci.yml", ".circleci/**")

	setup := lib.setup
	if opts.Setup != nil {
		setup = opts.Setup
	}
	goldHash := sha256.Sum256(in.gold)
	date := ""
	if out, err := r.git(ctx, "show", "-s", "--format=%aI", "--end-of-options", in.commit); err == nil {
		if d, err := time.Parse(time.RFC3339, strings.TrimSpace(string(out))); err == nil {
			date = d.UTC().Format(time.RFC3339)
		}
	}
	scope := make([]string, len(lib.pkgs))
	for i, p := range lib.pkgs {
		scope[i] = p.Name
	}
	source := map[string]string{"repo": in.repoName, "commit": in.commit, "scope": strings.Join(opts.Scope, ","), "tier": opts.Tier}
	if in.license != "" {
		source["spdx"] = in.license
	}
	meta := Meta{
		Commit: in.commit, AuthorDate: date, Generator: "taskgen/rebuild", Lang: lib.lang, Files: sortedPaths(lib.sources),
		Lines: in.loc, GoldHash: hex.EncodeToString(goldHash[:]), HiddenHash: hex.EncodeToString(hh.Sum(nil)), Source: source,
	}
	if opts.GoldBlobs != nil {
		h, err := opts.GoldBlobs.Put(in.gold)
		if err != nil {
			return nil, err
		}
		meta.GoldBlob = string(h)
	}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	// The task names the start by its staging path while it is checked here; Rebuild names it by its own path (or by
	// SealPath) once it is accepted.
	repoPath, err := filepath.Abs(in.dest)
	if err != nil {
		return nil, err
	}
	tags := append([]string{"long", "rebuild", "harvested", lib.lang, "rebuild-" + opts.Tier, opts.IDPrefix}, opts.Tags...)
	return &rl.Task{
		ID:       in.id,
		Kind:     rl.TaskSwarm,
		Repo:     rl.RepoSpec{Path: repoPath, Commit: in.start, License: in.license},
		Setup:    setup,
		Prompt:   rebuildPrompt(lib, opts.Tier, in.apiPath),
		Team:     rl.Team{Mode: "swarm", Agents: rebuildAgents(in.loc)},
		Verifier: rl.Verifier{Hidden: hidden, Protected: dedupe(protected)},
		Budget:   rebuildBudget(in.loc, opts.Budget),
		Tags:     dedupe(tags),
		Meta:     metaRaw,
	}, nil
}

// utf8ValidText reports whether data can travel inline as "text:": valid UTF-8 without NUL bytes.
func utf8ValidText(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0
}

// rebuildAgents is the team's size for a scope of loc lines: 3 workers at 2,000 lines and below, 6 at 10,000 and
// above, linear in between.
func rebuildAgents(loc int) int {
	return clamp(3+int(math.Round(3*float64(loc-2000)/8000)), 3, 6)
}

// rebuildBudget scales the budget with the scope's lines: 6 hours of wall clock at 2,000 lines and below, 12 at
// 10,000 and above, linear in between; steps in proportion, and requests for the team of rebuildAgents as
// teamRequests has them. Fields set in override win.
func rebuildBudget(loc int, override rl.Budget) rl.Budget {
	b := override
	if b.Steps == 0 {
		b.Steps = clamp(300+loc/8, 500, 1500)
	}
	if b.WallS == 0 {
		b.WallS = clamp(6*3600+int(6*3600*float64(loc-2000)/8000), 6*3600, 12*3600)
	}
	if b.Requests == 0 {
		b.Requests = teamRequests(b.Steps, rebuildAgents(loc), b.WallS)
	}
	return b
}

// unitNoun names the scope's units: packages (Go) or modules (Python).
func unitNoun(lang string, n int) string {
	noun := "package"
	if lang == Python {
		noun = "module"
	}
	if n != 1 {
		noun += "s"
	}
	return noun
}

// rebuildPrompt is the task's prompt: what was taken away, the packages in the order they build on each other, and
// how the work is checked.
func rebuildPrompt(lib *rebuildLib, tier, apiPath string) string {
	noun := unitNoun(lib.lang, len(lib.pkgs))
	var b strings.Builder
	switch tier {
	case RebuildSpec:
		fmt.Fprintf(&b, "The source of %d %s %s of this repository was removed. %s documents their API: for each one its documentation and its declarations, and the unexported declarations its own tests use. Write them again so that they provide that API and behave as %s, the README, the documentation and the examples describe.",
			len(lib.pkgs), langName(lib.lang), noun, apiPath, apiPath)
	default:
		body := `panic("not implemented")`
		if lib.lang == Python {
			body = "raise NotImplementedError"
		}
		removed := ""
		if lib.lang == Go {
			removed = " (unexported functions nothing else refers to were removed; the few that run when the packages load, or before the tests start, keep their bodies)"
		}
		fmt.Fprintf(&b, "%d %s %s of this repository were reduced to stubs: their declarations are in place with their documentation, but the bodies of their functions and methods only %s%s. Implement them again so that they behave as their documentation, the README and the examples describe.",
			len(lib.pkgs), langName(lib.lang), noun, body, removed)
	}
	fmt.Fprintf(&b, "\n\nThe %s, in the order they build on each other:\n", noun)
	for _, p := range lib.pkgs {
		if lib.lang == Go {
			fmt.Fprintf(&b, "- `%s` (%s, %d lines)\n", p.Name, dirLabel(p.Dir), p.LOC)
		} else {
			fmt.Fprintf(&b, "- `%s` (%s, %d lines)\n", p.Name, p.Files[0], p.LOC)
		}
	}
	fmt.Fprintf(&b, "\nYour work is checked by the project's own tests, which you cannot see: each %s earns a share of the score, weighted by its size, as its tests pass. Keep the API as it is declared: the tests use these names, signatures and types. Do not edit the README, the documentation, the examples or the build files, and do not add or change test files next to the %s: the hidden tests are written there when the work is checked. Tests of your own belong in a directory of their own. Write the code yourself: fetching the project's original sources from elsewhere (its repository, a mirror, a module proxy or a package index) is not allowed.",
		strings.TrimSuffix(noun, "s"), noun)
	return b.String()
}

// langName is the language's name in prose.
func langName(lang string) string {
	if lang == Python {
		return "Python"
	}
	return "Go"
}

// apiPreamble opens API.md.
func apiPreamble(lang string, n int) string {
	noun := unitNoun(lang, n)
	return fmt.Sprintf("# API\n\nThe source of the %s %s below was removed from this repository. This file documents what each one provides: its documentation, its declarations and, where its own tests use them, unexported ones. The %s are listed in the order they build on each other.\n\n",
		langName(lang), noun, noun)
}

// apiFile is where API.md is written: API.md, or REBUILD-API.md when the repository already has an API.md.
func apiFile(files []string) string {
	for _, f := range files {
		if strings.EqualFold(f, "API.md") {
			return "REBUILD-API.md"
		}
	}
	return "API.md"
}

// units turns BuildUnits' selection into the task's units: one per package or module, weighted by its lines (at
// least 1), in dependency order; guards and the tests of no package are dropped. It returns the units, the origin of
// their tests and the dropped tests.
func (l *rebuildLib) units(sel SuiteSelection) ([]SuiteUnit, map[string]string, []string) {
	index := map[string]int{}
	for i, p := range l.pkgs {
		index[unitName(p.Name)] = i
	}
	dropped := append(append([]string(nil), sel.Dropped...), sel.Guards...)
	var units []SuiteUnit
	for _, u := range sel.Units {
		i, ok := index[u.Name]
		if !ok {
			dropped = append(dropped, u.Tests...)
			continue
		}
		units = append(units, SuiteUnit{Name: u.Name, Weight: float64(max(l.pkgs[i].LOC, 1)), Tests: u.Tests})
	}
	sort.SliceStable(units, func(a, b int) bool { return index[units[a].Name] < index[units[b].Name] })
	origin := map[string]string{}
	for _, u := range units {
		for _, t := range u.Tests {
			if c, ok := sel.Origin[t]; ok {
				origin[t] = c
			}
		}
	}
	sort.Strings(dropped)
	return units, origin, dropped
}

// dependencyOrder orders packages so that each comes after the packages of the scope it imports, in name order among
// those that are ready at the same time. An import cycle (Python allows them) is broken at the package with the
// fewest imports left, the first in name order on a tie.
func dependencyOrder(pkgs []*rebuildPkg) []*rebuildPkg {
	byName := map[string]*rebuildPkg{}
	names := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		byName[p.Name] = p
		names = append(names, p.Name)
	}
	sort.Strings(names)
	done := map[string]bool{}
	left := func(p *rebuildPkg) int {
		n := 0
		for _, d := range p.Imports {
			if _, ok := byName[d]; ok && !done[d] && d != p.Name {
				n++
			}
		}
		return n
	}
	out := make([]*rebuildPkg, 0, len(pkgs))
	for len(out) < len(pkgs) {
		pick := ""
		for _, n := range names {
			if !done[n] && left(byName[n]) == 0 {
				pick = n
				break
			}
		}
		if pick == "" {
			for _, n := range names {
				if !done[n] && (pick == "" || left(byName[n]) < left(byName[pick])) {
					pick = n
				}
			}
		}
		done[pick] = true
		out = append(out, byName[pick])
	}
	return out
}

// rebuildChecks runs, on the start state in the environment rollouts use, the check that the scope cannot be
// imported from outside the start tree and, for Go stubs, that the stubs build with their tests.
func rebuildChecks(ctx context.Context, t *rl.Task, lib *rebuildLib, opts RebuildOptions) error {
	script := "(\n" + lib.leakScript() + "\n)"
	if lib.lang == Go && opts.Tier == RebuildStubs {
		script += "\ns=$?; [ \"$s\" -eq 0 ] || exit \"$s\"\n(\n" + lib.goBuildScript() + "\n)"
	}
	ct := *t
	ct.Verifier.Cmd = script
	ct.Verifier.Pass = "exit0"
	ct.Verifier.BaselineScore = 0
	ct.Verifier.TimeoutS = 900
	res, err := env.VerifyPatch(ctx, ct, nil, env.VerifyOptions{Workspaces: opts.Workspaces, HiddenBlobs: opts.HiddenBlobs})
	if err != nil {
		return err
	}
	if res.Rejected != "" {
		return fmt.Errorf("checking the start: %s", res.Rejected)
	}
	if res.Pass {
		return nil
	}
	detail := checkOutput(res.Log)
	switch res.ExitCode {
	case 10:
		return reject(ReasonRebuildLeak, "%s", detail)
	case 11:
		return reject(ReasonRebuildStubs, "%s", detail)
	}
	return fmt.Errorf("checking the start failed (exit status %d): %s", res.ExitCode, detail)
}

// checkOutput is the output of the last verifier run of a log, trimmed to its last 1,500 bytes.
func checkOutput(log string) string {
	if i := strings.LastIndex(log, "===== verifier run"); i >= 0 {
		if j := strings.IndexByte(log[i:], '\n'); j >= 0 {
			log = log[i+j+1:]
		}
	}
	return tail(strings.TrimSpace(log), 1500)
}

// leakScript is the shell check that the scope cannot be imported from outside the start tree; it exits 10 with a
// line per finding when it can.
func (l *rebuildLib) leakScript() string {
	var b strings.Builder
	if l.lang == Python {
		// The check sees what the tests see, the user site and PYTHONPATH included (python3 -I would skip both), and
		// runs from / so the start tree's own package, found through the working directory, hides none elsewhere.
		code := `import importlib.util, os, sys
root = os.path.realpath(sys.argv[1])
sys.path[:] = [p for p in sys.path if p not in ("", ".")]
bad = []
for n in sys.argv[2:]:
    try:
        s = importlib.util.find_spec(n)
    except Exception:
        s = None
    if s is None:
        continue
    locs = list(s.submodule_search_locations or [])
    if s.origin and os.path.isabs(s.origin):
        locs.append(s.origin)
    for loc in locs:
        p = os.path.realpath(loc)
        if p != root and not p.startswith(root + os.sep):
            bad.append("%s is installed at %s" % (n, p))
for m in bad:
    print(m)
sys.exit(10 if bad else 0)
`
		b.WriteString("r=$(pwd -P) && cd / && python3 -c " + shq(code) + " \"$r\"")
		for _, top := range l.tops {
			b.WriteString(" " + shq(top))
		}
		return b.String()
	}
	if l.root != "." {
		b.WriteString("cd " + shq(l.root) + " || exit 2\n")
	}
	b.WriteString("leak=0\nc=$(go env GOMODCACHE 2>/dev/null) || c=\np=$(go env GOPATH 2>/dev/null) || p=\n")
	mods := []string{l.module}
	if base := goMajorRe.ReplaceAllString(l.module, ""); base != l.module {
		mods = append(mods, base)
	}
	for _, m := range mods {
		e := shq(escapeModulePath(m))
		fmt.Fprintf(&b, "if [ -n \"$c\" ]; then for d in \"$c\"/%s@* \"$c\"/cache/download/%s/@v; do if [ -e \"$d\" ]; then echo \"in the module cache: $d\"; leak=1; fi; done; fi\n", e, e)
		fmt.Fprintf(&b, "for gp in $(printf '%%s' \"$p\" | tr ':' ' '); do if [ -e \"$gp\"/src/%s ]; then echo \"in GOPATH: $gp/src/%s\"; leak=1; fi; done\n", shq(m), m)
		fmt.Fprintf(&b, "if [ -e vendor/%s ]; then echo 'vendored: vendor/%s'; leak=1; fi\n", shq(m), m)
		fmt.Fprintf(&b, "if [ -f vendor/modules.txt ] && grep -Fq -e %s vendor/modules.txt; then echo 'listed in vendor/modules.txt: %s'; leak=1; fi\n", shq("# "+m+" "), m)
	}
	b.WriteString("[ \"$leak\" = 0 ] || exit 10")
	return b.String()
}

// goBuildScript is the shell check that Go stubs build and their tests compile against them; it exits 11 when they do
// not.
func (l *rebuildLib) goBuildScript() string {
	var pkgs []string
	for _, p := range l.pkgs {
		rel := relDir(l.root, p.Dir)
		if rel != "." {
			rel = "./" + rel
		}
		pkgs = append(pkgs, shq(rel))
	}
	list := strings.Join(pkgs, " ")
	cd := ""
	if l.root != "." {
		cd = "cd " + shq(l.root) + " || exit 2\n"
	}
	return cd + "out=$(go build " + list + " 2>&1) || { printf '%s\\n' \"$out\" | tail -n 40; exit 11; }\n" +
		"out=$(go test -count=1 -run '^$' -exec true " + list + " 2>&1) || { printf '%s\\n' \"$out\" | tail -n 40; exit 11; }"
}

// FormatRebuildUnits describes units for a person: each unit's name, weight and tests, in order.
func FormatRebuildUnits(units []RebuildUnit) string {
	parts := make([]string, len(units))
	for i, u := range units {
		parts[i] = u.Name + " (weight " + strconv.FormatFloat(u.Weight, 'f', -1, 64) + ", " + strconv.Itoa(u.Tests) + " tests)"
	}
	return strings.Join(parts, ", ")
}
