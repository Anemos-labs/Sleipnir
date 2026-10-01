package taskgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// Rejection reasons specific to Mutate.
const (
	ReasonSurvives    = "the mutation survives: the project's tests still pass"
	ReasonBuildBroken = "the mutation breaks the build instead of a test"
	ReasonRestoreFail = "restoring the original code does not make the tests pass"
	ReasonNoTests     = "no tests next to the mutated file"
)

// MutateOptions configures Mutate.
type MutateOptions struct {
	// Workspaces runs the project's tests (required).
	Workspaces *env.Workspaces

	// Rev is the commit to mutate (default HEAD).
	Rev string
	// Max is the number of tasks wanted (default 10); MaxAttempts bounds the
	// candidates tried (default 20 * Max), because most mutations are not caught
	// by the tests and are discarded.
	Max         int
	MaxAttempts int
	// Seed selects which mutations are tried first; the result is deterministic.
	Seed int64
	// Languages restricts to "go" and/or "python" (default both).
	Languages []string
	// Files restricts mutated files to those matching any path.Match pattern.
	Files []string

	// TestCmd overrides the test command (Go: one package, Python: the suite);
	// Setup overrides the setup commands.
	TestCmd string
	Setup   []string

	Timeout     time.Duration // per verifier run, default 5 minutes
	Concurrency int           // candidates validated at once, default 2

	IDPrefix string
	Tags     []string
	Budget   rl.Budget
	// GoldBlobs, when set, receives each task's reference solution (the reverse
	// patch) and records its hash in Meta.gold_blob, which ValidateComposite needs.
	GoldBlobs events.Blobs
	Progress  func(string)
}

// Mutate injects small bugs (a flipped comparison, a dropped nil check, an
// off-by-one, a negated condition, an inverted boolean) into Go and Python
// sources and keeps those that the project's own tests catch. Each task's
// starting state is the repository at Rev with the mutation applied by a setup
// command; the verifier is the project's tests; the reference solution is the
// reverse patch. Only mutations that make the tests fail (without breaking the
// build) and whose reversal makes them pass are kept, both established by
// running the tests.
func Mutate(ctx context.Context, repoPath string, opts MutateOptions) ([]rl.Task, *Report, error) {
	started := time.Now()
	rep := &Report{}
	if opts.Workspaces == nil {
		return nil, rep, errors.New("MutateOptions.Workspaces is required")
	}
	if opts.Max <= 0 {
		opts.Max = 10
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 20 * opts.Max
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 2
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, rep, err
	}
	g := opts.Workspaces.Git()
	gd, ok := g.GitDirOf(ctx, abs)
	if !ok {
		return nil, rep, fmt.Errorf("%s is not the root of a git repository", repoPath)
	}
	r := &repo{g: g, gitDir: gd}
	rev := opts.Rev
	if rev == "" {
		rev = "HEAD"
	}
	commit, err := r.resolve(ctx, rev)
	if err != nil {
		return nil, rep, fmt.Errorf("resolving %s: %w", rev, err)
	}
	if opts.IDPrefix == "" {
		opts.IDPrefix = sanitizeID(filepath.Base(abs))
	}
	dateOut, err := r.git(ctx, "show", "-s", "--format=%aI", "--end-of-options", commit)
	if err != nil {
		return nil, rep, err
	}
	authorDate := strings.TrimSpace(string(dateOut))
	if d, err := time.Parse(time.RFC3339, authorDate); err == nil {
		authorDate = d.UTC().Format(time.RFC3339) // one canonical form for date splits
	}

	files, err := r.listFiles(ctx, commit)
	if err != nil {
		return nil, rep, err
	}
	hasTests := map[string]bool{} // language -> the repository has tests at all
	for _, f := range files {
		if l, k := Classify(f); k == Test {
			hasTests[l] = true
		}
	}
	langOK := func(l string) bool {
		if len(opts.Languages) == 0 {
			return l == Go || l == Python
		}
		return containsFold(opts.Languages, l)
	}

	// Enumerate candidate mutations.
	var cands []Mutation
	for _, f := range files {
		lang, kind := Classify(f)
		if kind != Source || (lang != Go && lang != Python) || !langOK(lang) || !hasTests[lang] {
			continue
		}
		if len(opts.Files) > 0 && !matchAny(opts.Files, f) {
			continue
		}
		if lang == Go && !dirHasSuffixFile(files, path.Dir(f), "_test.go") {
			continue // no test can fail
		}
		src, err := r.show(ctx, commit, f)
		if err != nil {
			return nil, rep, err
		}
		switch lang {
		case Go:
			ms, err := GoMutations(f, src)
			if err != nil {
				continue // a file that does not parse cannot be mutated
			}
			cands = append(cands, ms...)
		case Python:
			cands = append(cands, PythonMutations(f, src)...)
		}
	}
	rep.Candidates = len(cands)
	// Deterministic pseudo-random order.
	key := func(m Mutation) uint64 {
		h := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%d\x00%s", opts.Seed, m.File, m.Start, m.Op)))
		return uint64(h[0])<<56 | uint64(h[1])<<48 | uint64(h[2])<<40 | uint64(h[3])<<32 | uint64(h[4])<<24 | uint64(h[5])<<16 | uint64(h[6])<<8 | uint64(h[7])
	}
	sort.SliceStable(cands, func(a, b int) bool { return key(cands[a]) < key(cands[b]) })

	var tasks []rl.Task
	attempts := 0
	for i := 0; i < len(cands) && attempts < opts.MaxAttempts && len(tasks) < opts.Max; {
		if ctx.Err() != nil {
			return tasks, rep, ctx.Err()
		}
		n := min(opts.Concurrency, opts.MaxAttempts-attempts, len(cands)-i)
		chunk := cands[i : i+n]
		i += n
		attempts += n
		type outcome struct {
			task *rl.Task
			rej  *Rejection
			err  error
		}
		outs := make([]outcome, n)
		var wg sync.WaitGroup
		for j, m := range chunk {
			wg.Add(1)
			go func(j int, m Mutation) {
				defer wg.Done()
				t, rej, err := tryMutation(ctx, r, commit, authorDate, abs, m, opts)
				outs[j] = outcome{t, rej, err}
			}(j, m)
		}
		wg.Wait()
		rep.Examined += n
		for _, o := range outs {
			switch {
			case o.task != nil:
				if len(tasks) < opts.Max {
					tasks = append(tasks, *o.task)
					rep.Accepted++
					if opts.Progress != nil {
						opts.Progress(fmt.Sprintf("accepted %s (%d/%d)", o.task.ID, len(tasks), opts.Max))
					}
				}
			case o.rej != nil:
				rep.reject(commit, "", o.rej.Reason, o.rej.Detail)
			case o.err != nil:
				if ctx.Err() != nil {
					return tasks, rep, ctx.Err()
				}
				if len(rep.InfraErrors) < 500 {
					rep.InfraErrors = append(rep.InfraErrors, Rejection{Commit: commit, Reason: ReasonInfra, Detail: o.err.Error()})
				}
			}
		}
	}
	rep.Duration = time.Since(started)
	return tasks, rep, nil
}

func matchAny(patterns []string, p string) bool {
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
	}
	return false
}

// listFiles lists every file path at commit.
func (r *repo) listFiles(ctx context.Context, commit string) ([]string, error) {
	out, err := r.git(ctx, "ls-tree", "-r", "-z", "--name-only", "--full-tree", "--end-of-options", commit)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			files = append(files, string(p))
		}
	}
	return files, nil
}

// mutationPatches returns the patch that applies m to the tree of commit, and
// the patch that reverts it, built with plumbing in a scratch repository so
// the source repository is not touched.
func mutationPatches(ctx context.Context, r *repo, commit string, m Mutation, mutated []byte) (fwd, rev []byte, err error) {
	scratch, err := os.MkdirTemp("", "sleipnir-mutate-")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	gd := filepath.Join(scratch, "g.git")
	if _, err := r.g.Run(ctx, "", nil, "init", "--quiet", "--bare", "--template=", gd); err != nil {
		return nil, nil, err
	}
	// Objects of the source repository are readable through alternates.
	srcObjects := filepath.Join(r.gitDir, "objects")
	if err := os.WriteFile(filepath.Join(gd, "objects", "info", "alternates"), []byte(srcObjects+"\n"), 0o644); err != nil {
		return nil, nil, err
	}
	run := func(stdin []byte, extra []string, args ...string) ([]byte, error) {
		var in *bytes.Reader
		if stdin != nil {
			in = bytes.NewReader(stdin)
			return r.g.RunEnv(ctx, "", extra, in, 1<<28, append([]string{"--git-dir=" + gd}, args...)...)
		}
		return r.g.RunEnv(ctx, "", extra, nil, 1<<28, append([]string{"--git-dir=" + gd}, args...)...)
	}
	sha, err := run(mutated, nil, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return nil, nil, err
	}
	// The original entry's mode, so that an executable script stays executable.
	entry, err := r.git(ctx, "ls-tree", "--end-of-options", commit, "--", m.File)
	if err != nil {
		return nil, nil, err
	}
	fields := strings.Fields(string(entry))
	if len(fields) < 3 {
		return nil, nil, fmt.Errorf("cannot find %s in %s", m.File, commit[:8])
	}
	idx := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "idx")}
	if _, err := run(nil, idx, "read-tree", commit); err != nil {
		return nil, nil, err
	}
	if _, err := run(nil, idx, "update-index", "--cacheinfo", fields[0]+","+strings.TrimSpace(string(sha))+","+m.File); err != nil {
		return nil, nil, err
	}
	tree, err := run(nil, idx, "write-tree")
	if err != nil {
		return nil, nil, err
	}
	t2 := strings.TrimSpace(string(tree))
	flags := []string{"diff-tree", "-r", "-p", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/", "--dst-prefix=b/"}
	if fwd, err = run(nil, nil, append(flags, commit, t2)...); err != nil {
		return nil, nil, err
	}
	if rev, err = run(nil, nil, append(flags, t2, commit)...); err != nil {
		return nil, nil, err
	}
	return fwd, rev, nil
}

// applyPatchCommand renders a patch as a setup command. The here-document is
// quoted, so nothing in the patch is expanded by the shell, and its delimiter is
// derived from the patch so it cannot occur inside it.
func applyPatchCommand(patch []byte) string {
	sum := sha256.Sum256(patch)
	delim := "SLEIPNIR_PATCH_" + strings.ToUpper(hex.EncodeToString(sum[:6]))
	for bytes.Contains(patch, []byte(delim)) {
		delim += "_X"
	}
	body := string(patch)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return "git apply --whitespace=nowarn <<'" + delim + "'\n" + body + delim
}

var (
	goFailRe     = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
	pytestFailRe = regexp.MustCompile(`(?m)^FAILED (\S+)`)
)

// failingTests extracts test names from a verifier log.
func failingTests(log string) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range goFailRe.FindAllStringSubmatch(log, -1) {
		n := strings.SplitN(m[1], "/", 2)[0]
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, m := range pytestFailRe.FindAllStringSubmatch(log, -1) {
		n := m[1]
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) > 5 {
		names = names[:5]
	}
	return names
}

var buildFailureMarkers = []string{
	"[build failed]", "[setup failed]", "SyntaxError", "IndentationError", "ImportError while importing", "ModuleNotFoundError",
	"collected 0 items", "no tests ran",
}

func looksLikeBuildFailure(log string) bool {
	for _, m := range buildFailureMarkers {
		if strings.Contains(log, m) {
			return true
		}
	}
	return false
}

// protectedTestGlobs protects the project's tests: the agent is told the tests
// fail because of a bug in the source, so editing them can only be cheating.
func protectedTestGlobs(lang string) []string {
	g := []string{".github/**", ".gitlab-ci.yml", ".circleci/**"}
	switch lang {
	case Go:
		return append(g, "*_test.go", "testdata/**", "go.mod", "go.sum")
	case Python:
		return append(g, "test_*.py", "*_test.py", "conftest.py", "tests/**", "test/**", "pytest.ini", "tox.ini", "setup.cfg", "pyproject.toml")
	}
	return g
}

func tryMutation(ctx context.Context, r *repo, commit, authorDate, repoDir string, m Mutation, opts MutateOptions) (*rl.Task, *Rejection, error) {
	src, err := r.show(ctx, commit, m.File)
	if err != nil {
		return nil, nil, err
	}
	mutated, err := m.Apply(src)
	if err != nil {
		return nil, nil, err
	}
	fwd, rev, err := mutationPatches(ctx, r, commit, m, mutated)
	if err != nil {
		return nil, nil, err
	}
	if len(fwd) == 0 {
		return nil, nil, errors.New("mutation produced an empty patch")
	}
	lang, _ := Classify(m.File)

	// Test command and setup.
	var cmd string
	var setup []string
	switch lang {
	case Go:
		dir := path.Dir(m.File)
		mod, ok := r.nearestDirWith(ctx, commit, dir, "go.mod")
		if !ok {
			return nil, &Rejection{Reason: ReasonNoTests, Detail: "no go.mod above " + dir}, nil
		}
		rel := "."
		switch {
		case mod == "." && dir != ".":
			rel = "./" + dir
		case mod != "." && dir != mod:
			rel = "./" + strings.TrimPrefix(dir, mod+"/")
		}
		cmd = "go test -count=1 " + rel
		setup = []string{"go mod download"}
		if mod != "." {
			cmd = "cd " + shq(mod) + " && " + cmd
			setup = []string{"cd " + shq(mod) + " && go mod download"}
		}
	case Python:
		py := "python3"
		cmd = py + " -m pytest -q -x -p no:cacheprovider"
	}
	if opts.TestCmd != "" {
		cmd = opts.TestCmd
	}
	if opts.Setup != nil {
		setup = opts.Setup
	}
	setup = append(append([]string(nil), setup...), applyPatchCommand(fwd))

	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", commit, m.File, m.Start, m.Op)))
	meta := Meta{
		Commit: commit, AuthorDate: authorDate, Generator: "taskgen/mutate", Lang: lang, Files: []string{m.File},
		Mutation: m.info(), Base: commit,
	}
	if opts.GoldBlobs != nil {
		h, err := opts.GoldBlobs.Put(rev)
		if err != nil {
			return nil, nil, err
		}
		meta.GoldBlob = string(h)
	}
	metaRaw, _ := json.Marshal(meta)
	t := rl.Task{
		ID:     fmt.Sprintf("mut-%s-%s", opts.IDPrefix, hex.EncodeToString(sum[:5])),
		Kind:   rl.TaskFix,
		Repo:   rl.RepoSpec{Path: repoDir, Commit: commit},
		Setup:  setup,
		Prompt: "Some of this project's tests fail because of a bug in the source code. Find the bug and fix it. Do not modify the tests.",
		Team:   rl.Team{Mode: "single"},
		Verifier: rl.Verifier{
			Cmd: cmd, TimeoutS: int(opts.Timeout / time.Second), Pass: "exit0", Protected: protectedTestGlobs(lang),
		},
		Budget: deriveBudget(1, 20, opts.Budget),
		Tags:   dedupe(append([]string{lang, "mutation", m.Op, opts.IDPrefix}, opts.Tags...)),
		Meta:   metaRaw,
	}
	// Stage 1: the tests must fail on the mutated code, and fail for the right
	// reason. A survivor (tests still pass) is the common case and is cheap to
	// discard; a build failure is a bug the compiler reports, not a task.
	vo := env.VerifyOptions{Workspaces: opts.Workspaces}
	base, err := env.VerifyBaseline(ctx, t, vo)
	switch {
	case errors.Is(err, env.ErrBaselinePasses):
		return nil, &Rejection{Reason: ReasonSurvives, Detail: m.Op}, nil
	case errors.Is(err, env.ErrBaselineBroken):
		return nil, &Rejection{Reason: env.ErrBaselineBroken.Error(), Detail: firstLine(err.Error())}, nil
	case err != nil:
		return nil, nil, err
	}
	if looksLikeBuildFailure(base.Log) {
		return nil, &Rejection{Reason: ReasonBuildBroken, Detail: m.Op}, nil
	}
	// Stage 2: the reverse patch must restore green.
	gold, err := env.VerifyPatch(ctx, t, rev, vo)
	if err != nil {
		return nil, nil, err
	}
	if !gold.Pass {
		return nil, &Rejection{Reason: ReasonRestoreFail, Detail: m.Op}, nil
	}
	if names := failingTests(base.Log); len(names) > 0 {
		t.Prompt = "These tests fail: " + strings.Join(names, ", ") + ". A bug in the source code is the cause. Find it and fix it. Do not modify the tests."
	}
	return &t, nil, nil
}
