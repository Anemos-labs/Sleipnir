package taskgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// Rejection reasons reported by FromGit.
const (
	ReasonTooManyFiles    = "too many changed files"
	ReasonTooManyLines    = "too many changed lines"
	ReasonBinaryTest      = "binary or non-UTF-8 test file (no hidden blob store configured)"
	ReasonNoTestPlan      = "no usable test command"
	ReasonDuplicate       = "duplicate of an earlier task"
	ReasonBaselinePasses  = "verifier passes on the parent commit (invalid task)"
	ReasonBaselineBroken  = "verifier does not run cleanly on the parent commit"
	ReasonGoldFails       = "verifier fails on the commit itself (unfixable)"
	ReasonLanguageFilter  = "language not selected"
	ReasonSourceMinimum   = "too few source files"
	ReasonEmptyGold       = "the commit changes only tests"
	ReasonInfra           = "infrastructure error"
	ReasonUnsupportedName = "path needs quoting"
)

// Rejection records why a candidate commit did not become a task.
type Rejection struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject,omitempty"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail,omitempty"`
}

// Report summarises a generation run.
type Report struct {
	Examined    int            `json:"examined"`   // commits scanned
	Candidates  int            `json:"candidates"` // commits that change source and tests
	Accepted    int            `json:"accepted"`
	Rejected    map[string]int `json:"rejected,omitempty"`
	Rejections  []Rejection    `json:"rejections,omitempty"`
	InfraErrors []Rejection    `json:"infra_errors,omitempty"`
	License     string         `json:"license,omitempty"`
	Duration    time.Duration  `json:"duration_ns"`
}

func (r *Report) reject(commit, subject, reason, detail string) {
	if r.Rejected == nil {
		r.Rejected = map[string]int{}
	}
	r.Rejected[reason]++
	if len(r.Rejections) < 2000 {
		r.Rejections = append(r.Rejections, Rejection{Commit: commit, Subject: subject, Reason: reason, Detail: detail})
	}
}

// GitOptions configures FromGit.
type GitOptions struct {
	// Workspaces runs the validation (the same environment rollouts use). It is
	// required unless NoValidate is set.
	Workspaces *env.Workspaces
	// NoValidate skips the baseline and reference-solution runs. Tasks made this
	// way are unverified: some will reward doing nothing and some cannot be
	// solved. Meant for dry runs and tests of the miner itself.
	NoValidate bool

	// Rev is the tip to mine from (default HEAD); commits are visited newest
	// first. Since and Until bound the author date; MaxCommits bounds how many
	// commits are scanned (default 20000).
	Rev        string
	Since      time.Time
	Until      time.Time
	MaxCommits int
	// Max stops after that many accepted tasks (0 means all).
	Max int

	// Filters.
	MaxFiles       int      // changed files (default 12)
	MaxLines       int      // added plus deleted lines (default 800)
	MinSourceFiles int      // default 1
	Languages      []string // restrict to these languages (default: all supported)
	// AllowLicenses lists acceptable SPDX identifiers; when set, a repository whose
	// licence is not on the list yields no tasks (ErrLicense).
	AllowLicenses []string

	// TestCmd overrides the detected test command; {files} and {dirs} expand to
	// the hidden test files and their directories. Setup overrides the detected
	// setup commands (an empty non-nil slice means none).
	TestCmd string
	Setup   []string

	// Timeout is the verifier timeout written into tasks (default 5 minutes).
	Timeout time.Duration
	// Concurrency is how many candidates are validated at once (default 2).
	Concurrency int
	// Repeats runs the baseline that many times to catch flaky tests (default 1).
	Repeats int

	// HiddenBlobs stores the hidden test files; tasks then carry "blob:<hash>"
	// references (the store must travel with the tasks file). Nil embeds the
	// content inline as "text:", which limits hidden files to UTF-8 text and puts
	// verifier content in tasks.jsonl. GoldBlobs, when set, receives each
	// reference solution (Meta.gold_blob), which ValidateComposite needs.
	HiddenBlobs events.Blobs
	GoldBlobs   events.Blobs

	// IDPrefix prefixes task ids (default: the repository directory name).
	IDPrefix string
	// Tags are added to every task; Budget overrides the derived budget.
	Tags   []string
	Budget rl.Budget
	// Issue optionally resolves closing references in commit messages.
	Issue IssueResolver

	Progress func(string)
}

// ErrLicense is returned when the repository's licence is not allowed.
var ErrLicense = errors.New("repository licence is not on the allow list")

// Meta is what FromGit and Mutate record in Task.Meta. The first two fields are
// read by env.Holdout for date splits; Files is what Composite uses to check
// that tasks are independent.
type Meta struct {
	Commit     string   `json:"commit"`
	AuthorDate string   `json:"author_date,omitempty"`
	Parent     string   `json:"parent,omitempty"`
	Generator  string   `json:"generator"`
	Subject    string   `json:"subject,omitempty"`
	Lang       string   `json:"lang,omitempty"`
	Files      []string `json:"files,omitempty"`
	Lines      int      `json:"lines,omitempty"`
	GoldHash   string   `json:"gold_hash,omitempty"`
	GoldBlob   string   `json:"gold_blob,omitempty"`
	HiddenHash string   `json:"hidden_hash,omitempty"`
	// Mutation describes a Mutate task's injected bug.
	Mutation *MutationInfo `json:"mutation,omitempty"`
	// Components lists the tasks a composite was built from.
	Components []string `json:"components,omitempty"`
	GoldBlobs  []string `json:"gold_blobs,omitempty"`
	Base       string   `json:"base,omitempty"`
}

// commitInfo is a candidate commit before task assembly.
type commitInfo struct {
	Hash, Parent string
	AuthorDate   time.Time
	Subject      string
	Files        []nameStatus
}

type nameStatus struct {
	Status string
	Path   string
}

// FromGit mines repoPath for commits that change source and tests and turns
// each into a task: the workspace is the parent commit, the prompt is the commit
// message, the verifier is the commit's test files (hidden and protected) plus a
// test command restricted to them where the language allows.
//
// Unless NoValidate is set every candidate is validated by running the
// verifier twice in the real environment: it must FAIL on the parent (else the
// task rewards doing nothing) and PASS with the commit's non-test changes
// applied (else the task is unsolvable, or the verifier cannot run here).
// Infrastructure errors skip a candidate without rejecting it.
func FromGit(ctx context.Context, repoPath string, opts GitOptions) ([]rl.Task, *Report, error) {
	started := time.Now()
	rep := &Report{}
	if opts.Workspaces == nil && !opts.NoValidate {
		return nil, rep, errors.New("GitOptions.Workspaces is required for validation (or set NoValidate)")
	}
	var g *env.Git
	if opts.Workspaces != nil {
		g = opts.Workspaces.Git()
	} else {
		var err error
		if g, err = env.NewGit(env.GitOptions{}); err != nil {
			return nil, rep, err
		}
		defer g.Close() // the scratch HOME this call made for git
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, rep, err
	}
	gd, ok := g.GitDirOf(ctx, abs)
	if !ok {
		return nil, rep, fmt.Errorf("%s is not the root of a git repository", repoPath)
	}
	r := &repo{g: g, gitDir: gd}
	rev := opts.Rev
	if rev == "" {
		rev = "HEAD"
	}
	tip, err := r.resolve(ctx, rev)
	if err != nil {
		return nil, rep, fmt.Errorf("resolving %s: %w", rev, err)
	}
	opts = withDefaults(opts, abs)

	// Licence of the repository at the tip.
	license := ""
	for _, f := range licenseFiles {
		if b, err := r.show(ctx, tip, f); err == nil {
			license = DetectLicense(string(b))
			break
		}
	}
	rep.License = license
	if len(opts.AllowLicenses) > 0 && !containsFold(opts.AllowLicenses, license) {
		return nil, rep, fmt.Errorf("%w: found %q, allowed %v", ErrLicense, license, opts.AllowLicenses)
	}

	commits, err := listCommits(ctx, r, tip, opts, rep)
	if err != nil {
		return nil, rep, err
	}
	var tasks []rl.Task
	seenGold, seenHidden := map[string]bool{}, map[string]bool{}
	var mu sync.Mutex

	// Candidates are validated in chunks of Concurrency, in mining order, so the
	// result is deterministic: the first Max accepted candidates in that order.
	type outcome struct {
		task *rl.Task
		rej  *Rejection
		infr *Rejection
	}
	for i := 0; i < len(commits); i += opts.Concurrency {
		if ctx.Err() != nil {
			return tasks, rep, ctx.Err()
		}
		end := min(i+opts.Concurrency, len(commits))
		outs := make([]outcome, end-i)
		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				ci := commits[j]
				t, gold, rej, err := buildTask(ctx, r, ci, license, abs, opts)
				if rej != nil {
					outs[j-i].rej = rej
					return
				}
				if err != nil {
					outs[j-i].infr = &Rejection{Commit: ci.Hash, Subject: ci.Subject, Reason: ReasonInfra, Detail: err.Error()}
					return
				}
				// dedupe before spending validation time
				var m Meta
				_ = json.Unmarshal(t.Meta, &m)
				mu.Lock()
				dup := seenGold[m.GoldHash] || seenHidden[m.HiddenHash]
				if !dup {
					seenGold[m.GoldHash], seenHidden[m.HiddenHash] = true, true
				}
				mu.Unlock()
				if dup {
					outs[j-i].rej = &Rejection{Commit: ci.Hash, Subject: ci.Subject, Reason: ReasonDuplicate}
					return
				}
				if !opts.NoValidate {
					if rej, err := validate(ctx, t, gold, opts); rej != nil || err != nil {
						if rej != nil {
							rej.Commit, rej.Subject = ci.Hash, ci.Subject
							outs[j-i].rej = rej
						} else {
							outs[j-i].infr = &Rejection{Commit: ci.Hash, Subject: ci.Subject, Reason: ReasonInfra, Detail: err.Error()}
						}
						return
					}
				}
				outs[j-i].task = t
			}(j)
		}
		wg.Wait()
		for _, o := range outs {
			switch {
			case o.task != nil:
				if opts.Max > 0 && len(tasks) >= opts.Max {
					continue
				}
				tasks = append(tasks, *o.task)
				rep.Accepted++
				if opts.Progress != nil {
					opts.Progress(fmt.Sprintf("accepted %s (%d)", o.task.ID, rep.Accepted))
				}
			case o.rej != nil:
				rep.reject(o.rej.Commit, o.rej.Subject, o.rej.Reason, o.rej.Detail)
			case o.infr != nil:
				if len(rep.InfraErrors) < 500 {
					rep.InfraErrors = append(rep.InfraErrors, *o.infr)
				}
			}
		}
		if opts.Max > 0 && len(tasks) >= opts.Max {
			break
		}
	}
	rep.Duration = time.Since(started)
	if err := env.ValidateTasks(tasks); err != nil {
		return tasks, rep, fmt.Errorf("internal error: generated invalid tasks: %w", err)
	}
	return tasks, rep, nil
}

func withDefaults(o GitOptions, abs string) GitOptions {
	if o.MaxCommits <= 0 {
		o.MaxCommits = 20000
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 12
	}
	if o.MaxLines <= 0 {
		o.MaxLines = 800
	}
	if o.MinSourceFiles <= 0 {
		o.MinSourceFiles = 1
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.Repeats <= 0 {
		o.Repeats = 1
	}
	if o.IDPrefix == "" {
		o.IDPrefix = sanitizeID(filepath.Base(abs))
	}
	return o
}

var idBadRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeID(s string) string {
	s = strings.Trim(idBadRe.ReplaceAllString(s, "-"), "-._")
	if s == "" {
		s = "repo"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// listCommits scans history (newest first) and returns the commits that change
// both source and test files. Only cheap information is used here; everything
// else is fetched for the survivors.
func listCommits(ctx context.Context, r *repo, tip string, opts GitOptions, rep *Report) ([]commitInfo, error) {
	args := []string{"log", "--no-merges", "--no-renames", "--name-status", "--format=%x1e%H%x1f%P%x1f%aI%x1f%s",
		"--max-count=" + strconv.Itoa(opts.MaxCommits), "--end-of-options", tip}
	out, err := r.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	langOK := func(l string) bool {
		if len(opts.Languages) == 0 {
			return true
		}
		return containsFold(opts.Languages, l)
	}
	var res []commitInfo
	for _, block := range strings.Split(string(out), "\x1e") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		rep.Examined++
		head, rest, _ := strings.Cut(block, "\n")
		f := strings.SplitN(head, "\x1f", 4)
		if len(f) != 4 {
			continue
		}
		parents := strings.Fields(f[1])
		if len(parents) != 1 {
			continue // root commits have nothing to start from
		}
		date, err := time.Parse(time.RFC3339, f[2])
		if err != nil {
			continue
		}
		if !opts.Since.IsZero() && date.Before(opts.Since) || !opts.Until.IsZero() && date.After(opts.Until) {
			continue
		}
		ci := commitInfo{Hash: f[0], Parent: parents[0], AuthorDate: date, Subject: f[3]}
		quoted := false
		src, tests := 0, 0
		for _, line := range strings.Split(rest, "\n") {
			if line == "" {
				continue
			}
			st, p, ok := strings.Cut(line, "\t")
			if !ok {
				continue
			}
			if strings.HasPrefix(p, `"`) {
				quoted = true
				break
			}
			ci.Files = append(ci.Files, nameStatus{Status: st[:1], Path: p})
			lang, kind := Classify(p)
			switch {
			case kind == Source && langOK(lang):
				src++
			case kind == Test && st[:1] != "D" && langOK(lang):
				tests++
			}
		}
		if quoted {
			continue
		}
		if src < opts.MinSourceFiles || tests < 1 {
			continue
		}
		if strings.HasPrefix(ci.Subject, "Revert ") || strings.HasPrefix(ci.Subject, "revert:") {
			continue // a revert restores old behaviour: the "fix" is a removal
		}
		rep.Candidates++
		res = append(res, ci)
	}
	return res, nil
}

// buildTask assembles the task for one candidate and its reference solution.
// A non-nil *Rejection means the candidate is unusable; a non-nil error is an
// infrastructure problem.
func buildTask(ctx context.Context, r *repo, ci commitInfo, license, repoDir string, opts GitOptions) (*rl.Task, []byte, *Rejection, error) {
	rej := func(reason, detail string) (*rl.Task, []byte, *Rejection, error) {
		return nil, nil, &Rejection{Commit: ci.Hash, Subject: ci.Subject, Reason: reason, Detail: detail}, nil
	}
	changes, err := r.changes(ctx, ci.Parent, ci.Hash)
	if err != nil {
		return nil, nil, nil, err
	}
	lines := 0
	for _, c := range changes {
		lines += c.Added + c.Deleted
	}
	if len(changes) > opts.MaxFiles {
		return rej(ReasonTooManyFiles, strconv.Itoa(len(changes)))
	}
	if lines > opts.MaxLines {
		return rej(ReasonTooManyLines, strconv.Itoa(lines))
	}

	var hidden []hiddenFile
	var deletedTests, goldPaths, allPaths, sourcePaths []string
	langs := map[string]int{}
	for _, c := range changes {
		allPaths = append(allPaths, c.Path)
		lang, kind := Classify(c.Path)
		switch kind {
		case Test, TestSupport:
			if c.Status == "D" {
				if kind == Test {
					deletedTests = append(deletedTests, c.Path)
				}
				continue
			}
			data, err := r.show(ctx, ci.Hash, c.Path)
			if err != nil {
				return nil, nil, nil, err
			}
			if opts.HiddenBlobs == nil && (c.Binary || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)) {
				return rej(ReasonBinaryTest, c.Path)
			}
			hidden = append(hidden, hiddenFile{Path: c.Path, Kind: kind, Lang: lang, Data: data})
			if kind == Test {
				langs[lang]++
			}
		default:
			goldPaths = append(goldPaths, c.Path)
			if kind == Source {
				sourcePaths = append(sourcePaths, c.Path)
			}
		}
	}
	if len(goldPaths) == 0 {
		return rej(ReasonEmptyGold, "")
	}
	sort.Strings(allPaths)
	sort.Slice(hidden, func(a, b int) bool { return hidden[a].Path < hidden[b].Path })

	pl, err := planTests(ctx, r, ci.Parent, ci.Hash, hidden, deletedTests, opts.TestCmd, opts.Setup, opts.Setup != nil)
	if err != nil {
		return rej(ReasonNoTestPlan, err.Error())
	}
	if len(opts.Languages) > 0 && !containsFold(opts.Languages, pl.Lang) {
		return rej(ReasonLanguageFilter, pl.Lang)
	}

	// The reference solution: every non-test change of the commit.
	sort.Strings(goldPaths)
	// GIT_LITERAL_PATHSPECS makes every path mean itself (no globs, no magic); the
	// number of paths is bounded by GitOptions.MaxFiles.
	diffArgs := append([]string{"diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv",
		ci.Parent, ci.Hash, "--"}, goldPaths...)
	gold, err := r.gitIn(ctx, nil, []string{"GIT_LITERAL_PATHSPECS=1"}, diffArgs...)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(gold) == 0 {
		return rej(ReasonEmptyGold, "")
	}

	// Hidden files and their protection.
	hiddenRefs := map[string]string{}
	h := sha256.New()
	var protected []string
	for _, hf := range hidden {
		if opts.HiddenBlobs != nil {
			hash, err := opts.HiddenBlobs.Put(hf.Data)
			if err != nil {
				return nil, nil, nil, err
			}
			hiddenRefs[hf.Path] = "blob:" + string(hash)
		} else {
			hiddenRefs[hf.Path] = "text:" + string(hf.Data)
		}
		protected = append(protected, escapeGlob(hf.Path))
		h.Write([]byte(hf.Path))
		h.Write([]byte{0})
		h.Write(hf.Data)
		h.Write([]byte{0})
	}
	for _, d := range deletedTests {
		protected = append(protected, escapeGlob(d))
	}
	protected = append(protected, protectedInfra(pl.Lang)...)
	protected = dedupe(protected)

	goldHash := sha256.Sum256(gold)
	meta := Meta{
		Commit: ci.Hash, Parent: ci.Parent, AuthorDate: ci.AuthorDate.UTC().Format(time.RFC3339), Generator: "taskgen/git",
		Subject: ci.Subject, Lang: pl.Lang, Files: allPaths, Lines: lines,
		GoldHash: hex.EncodeToString(goldHash[:]), HiddenHash: hex.EncodeToString(h.Sum(nil)),
	}
	if opts.GoldBlobs != nil {
		hash, err := opts.GoldBlobs.Put(gold)
		if err != nil {
			return nil, nil, nil, err
		}
		meta.GoldBlob = string(hash)
	}
	metaRaw, _ := json.Marshal(meta)

	msgRaw, err := r.git(ctx, "show", "-s", "--format=%B", "--end-of-options", ci.Hash)
	if err != nil {
		return nil, nil, nil, err
	}
	issues := map[string]string{}
	if opts.Issue != nil {
		for _, ref := range IssueRefs(string(msgRaw)) {
			if txt, err := opts.Issue(ctx, ref); err == nil {
				issues[ref] = txt
			}
		}
	}

	t := &rl.Task{
		ID:     fmt.Sprintf("%s-%s", opts.IDPrefix, ci.Hash[:8]),
		Kind:   InferKind(ci.Subject),
		Repo:   rl.RepoSpec{Path: repoDir, Commit: ci.Parent, License: license},
		Setup:  pl.Setup,
		Prompt: BuildPrompt(string(msgRaw), issues),
		Team:   rl.Team{Mode: "single"},
		Verifier: rl.Verifier{
			Cmd: pl.Cmd, TimeoutS: int(opts.Timeout / time.Second), Pass: "exit0", Hidden: hiddenRefs, Protected: protected,
		},
		Budget: deriveBudget(len(sourcePaths), lines, opts.Budget),
		Tags:   dedupe(append([]string{pl.Lang, "mined", sizeTag(len(changes), lines), opts.IDPrefix}, opts.Tags...)),
		Meta:   metaRaw,
	}
	if t.Prompt == "" || strings.TrimSpace(CleanMessage(string(msgRaw))) == "" {
		return rej(ReasonNoTestPlan, "empty commit message")
	}
	return t, gold, nil, nil
}

// validate runs the two checks that make a task sound.
func validate(ctx context.Context, t *rl.Task, gold []byte, opts GitOptions) (*Rejection, error) {
	vo := env.VerifyOptions{Workspaces: opts.Workspaces, HiddenBlobs: opts.HiddenBlobs, Repeats: opts.Repeats}
	_, err := env.CheckTask(ctx, *t, gold, vo)
	switch {
	case err == nil:
		return nil, nil
	case errors.Is(err, env.ErrBaselinePasses):
		return &Rejection{Reason: ReasonBaselinePasses}, nil
	case errors.Is(err, env.ErrBaselineBroken):
		return &Rejection{Reason: ReasonBaselineBroken, Detail: firstLine(err.Error())}, nil
	case errors.Is(err, env.ErrGoldFails):
		return &Rejection{Reason: ReasonGoldFails, Detail: tail(err.Error(), 600)}, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func tail(s string, n int) string {
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// escapeGlob turns a path into a glob that matches exactly that path.
func escapeGlob(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	s := b.String()
	// A leading "!" would read as negation and a slash-free pattern would match
	// at any depth: anchoring with "/" makes it exactly this path.
	if !strings.Contains(p, "/") {
		s = "/" + s
	}
	return s
}

// protectedInfra lists the files that decide how tests run in a language and so
// must not be changed by the agent: an agent-added conftest.py can rewrite
// results, a changed jest config can skip files.
func protectedInfra(lang string) []string {
	ci := []string{".github/**", ".gitlab-ci.yml", ".circleci/**"}
	switch lang {
	case Python:
		return append(ci, "conftest.py", "pytest.ini", "tox.ini")
	case JS, TS:
		return append(ci, "jest.config.*", "vitest.config.*", ".mocharc*")
	}
	return ci
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sizeTag(files, lines int) string {
	switch {
	case files <= 3 && lines <= 60:
		return "small"
	case files <= 8 && lines <= 300:
		return "medium"
	}
	return "large"
}

// deriveBudget scales the budget with the size of the change.
func deriveBudget(sourceFiles, lines int, override rl.Budget) rl.Budget {
	b := override
	if b.Steps == 0 {
		b.Steps = clamp(30+8*sourceFiles+lines/10, 40, 150)
	}
	if b.Requests == 0 {
		b.Requests = b.Steps * 5 / 2
	}
	if b.WallS == 0 {
		b.WallS = clamp(600+180*sourceFiles+lines, 900, 3600)
	}
	return b
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }
