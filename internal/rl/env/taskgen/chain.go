package taskgen

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path"
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

// Chains of consecutive merged changes.
//
// A chain is a run of pull requests merged one after another on a branch's
// first-parent history. Its task starts before the first and asks for all of
// them, their texts given in order as milestones; a team of agents works on it
// for hours. The whole-suite scorer gives it a dense score: the unit of
// milestone k holds the tests that pass for good once pull request k is in,
// weighted by the lines that pull request changed outside the tests.

// The number of pull requests a chain holds.
const (
	MinChainPRs = 3
	MaxChainPRs = 8
)

// Rejection reasons that only FromChains produces.
const (
	ReasonChainShape      = "not consecutive changes on first-parent history"
	ReasonLicense         = "licence not on the allow list"
	ReasonEmptyMilestone  = "a milestone has no text"
	ReasonSuiteUnrecorded = "the test suite could not be recorded"
	ReasonNoMilestones    = "no test passes for good at any milestone"
	ReasonSelfCheck       = "a milestone does not score its weight"
)

// chainTolerance is how far a state's self-check score may be from its
// milestone's cumulative weight.
const chainTolerance = 0.02

// Chain is one line of a chains file: consecutive merged changes made into one
// task.
type Chain struct {
	// ID names the chain within the file (letters, digits, '.', '_' and '-');
	// the task is <IDPrefix>-<ID>.
	ID string `json:"id"`
	// Commits are the merge or squash commits of the pull requests, oldest
	// first; each one's first parent is the one before it.
	Commits []string `json:"commits"`
	// Issues optionally holds, per commit, the text of the issue its pull request
	// resolved: a non-empty one is that milestone's text in place of the commit
	// message. It may be shorter than Commits.
	Issues []string `json:"issues,omitempty"`
	// Source is provenance added to Meta.Source (its keys win), never shown to
	// the agent.
	Source map[string]string `json:"source,omitempty"`
}

// ReadChains reads a chains file: one JSON object per line with the fields of
// Chain (unknown fields are an error). Blank lines and lines starting with #
// are skipped. Each chain needs an id that no other line has and
// MinChainPRs to MaxChainPRs commits, and at most one issue text per commit.
// Line numbers in errors count from 1.
func ReadChains(r io.Reader) ([]Chain, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20) // eight issue bodies can be long
	var out []Chain
	ids := map[string]int{}
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c Chain
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if err := c.validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if prev, ok := ids[c.ID]; ok {
			return nil, fmt.Errorf("line %d: the id %q is also on line %d", n, c.ID, prev)
		}
		ids[c.ID] = n
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no chains listed")
	}
	return out, nil
}

// validate reports what makes a chain unusable before any git work.
func (c Chain) validate() error {
	switch {
	case c.ID == "" || len(c.ID) > 60 || !safeID(c.ID):
		return fmt.Errorf("id %q must be 1 to 60 letters, digits, '.', '_' and '-'", c.ID)
	case len(c.Commits) < MinChainPRs || len(c.Commits) > MaxChainPRs:
		return fmt.Errorf("chain %s: %d commits, want %d to %d", c.ID, len(c.Commits), MinChainPRs, MaxChainPRs)
	case len(c.Issues) > len(c.Commits):
		return fmt.Errorf("chain %s: %d issue texts for %d commits", c.ID, len(c.Issues), len(c.Commits))
	}
	for _, rev := range c.Commits {
		if rev = strings.TrimSpace(rev); rev == "" || strings.HasPrefix(rev, "-") {
			return fmt.Errorf("chain %s: commit %q is empty or starts with '-'", c.ID, rev)
		}
	}
	return nil
}

// ChainOptions configures FromChains.
type ChainOptions struct {
	// Workspaces records the suite and runs the self-check, in the environment
	// rollouts use. Required: a chain is never emitted unchecked.
	Workspaces *env.Workspaces
	// Repeats is how many times the suite is recorded at each milestone (default
	// 3): a test counts only where every run agrees.
	Repeats int
	// Concurrency is how many chains are recorded at once (default 1).
	Concurrency int
	// DeadlineS bounds one run of the whole suite, in seconds (default
	// DefaultSuiteDeadlineS).
	DeadlineS int
	// Setup overrides the detected setup commands (an empty non-nil slice means
	// none).
	Setup []string
	// AllowLicenses lists acceptable SPDX identifiers; when set, a chain whose
	// last commit's licence is not on the list is rejected.
	AllowLicenses []string
	// HiddenBlobs stores the hidden test files and the scorer's files; nil
	// inlines them as text. GoldBlobs receives each reference solution
	// (Meta.gold_blob), which `rl tasks check` needs.
	HiddenBlobs events.Blobs
	GoldBlobs   events.Blobs
	// IDPrefix prefixes task ids and is a tag of every task (default: the
	// repository directory name).
	IDPrefix string
	// Tags are added to every task; Budget overrides the derived budget field by
	// field.
	Tags   []string
	Budget rl.Budget
	// Seal and SealPath are GitOptions' (see Seal): the start of every chain is
	// sealed before it is recorded, without the test files the chain deletes.
	Seal     string
	SealPath string
	// Progress receives one line per step of the work.
	Progress func(string)
}

// ChainCheck is the self-check of one chain, run at generation time: the score
// of the start and of the state after each pull request, against the
// cumulative weight of the milestones reached.
type ChainCheck struct {
	ID string `json:"id"`
	// Want and Got hold, at index k, the score the state after pull request k
	// must reach and the one it did; index 0 is the start.
	Want []float64 `json:"want"`
	Got  []float64 `json:"got"`
	OK   bool      `json:"ok"`
	// Units, Guards and Dropped count the milestones that have tests, the
	// guards and the tests left out.
	Units   int `json:"units"`
	Guards  int `json:"guards"`
	Dropped int `json:"dropped"`
}

// FromChains turns each chain of repoPath into a long swarm task:
//
//   - the start is the first commit's first parent, and every commit's first
//     parent must be the commit before it;
//   - the hidden tests are the last commit's version of every test file any
//     of the pull requests touched (test files the chain deletes are removed
//     before the suite runs);
//   - the reference solution is the start-to-end diff of every other file;
//   - the prompt lists the pull requests' texts (or their issues) in order as
//     milestones (BuildChainPrompt);
//   - the verifier is the whole-suite scorer (WriteSuite) over the Go modules,
//     the Python project and the cargo packages the tests belong to, with the
//     units BuildStepUnits chooses from the suite recorded Repeats times at the
//     start and after each pull request (RecordSuiteSteps): unit k holds the
//     tests that pull request k makes pass for good, weighted by the lines it
//     changed outside the tests, and the tests that pass at every step are
//     guards;
//   - the team is a swarm of two to six workers and the wall-clock budget six
//     to twelve hours, both growing with the chain.
//
// Each task is then checked as it will be scored: the start must score 0, the
// state after pull request k the cumulative weight of the milestones up to k
// (within 0.02), and the end exactly 1. A chain that fails the check is
// rejected and the check is reported (Report.Chains). Infrastructure errors
// skip a chain without rejecting it.
func FromChains(ctx context.Context, repoPath string, chains []Chain, opts ChainOptions) ([]rl.Task, *Report, error) {
	started := time.Now()
	rep := &Report{}
	if opts.Workspaces == nil {
		return nil, rep, errors.New("ChainOptions.Workspaces is required: chains are recorded and checked in the environment rollouts use")
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
	if opts.Repeats <= 0 {
		opts.Repeats = 3
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	if opts.DeadlineS <= 0 {
		opts.DeadlineS = DefaultSuiteDeadlineS
	}
	if opts.IDPrefix == "" {
		opts.IDPrefix = sanitizeID(filepath.Base(abs))
	}
	if opts.IDPrefix != sanitizeID(opts.IDPrefix) {
		return nil, rep, fmt.Errorf("id prefix %q: use at most 60 letters, digits, '.', '_' and '-', starting and ending with a letter or digit", opts.IDPrefix)
	}
	if head, err := r.resolve(ctx, "HEAD"); err == nil {
		rep.License = licenseAt(ctx, r, head)
	}
	name := repoIdentity(ctx, r, abs)
	if p := opts.Progress; p != nil {
		var mu sync.Mutex // chains recorded at once report one line at a time
		opts.Progress = func(s string) {
			mu.Lock()
			defer mu.Unlock()
			p(s)
		}
	}

	type outcome struct {
		task  *rl.Task
		check *ChainCheck
		rej   *Rejection
		infr  *Rejection
	}
	outs := make([]outcome, len(chains))
	seenID := map[string]bool{}
	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	for i, ch := range chains {
		rep.Examined++
		if err := ch.validate(); err != nil {
			outs[i].rej = &Rejection{Chain: ch.ID, Reason: ReasonChainShape, Detail: err.Error()}
			continue
		}
		if seenID[ch.ID] {
			outs[i].rej = &Rejection{Chain: ch.ID, Reason: ReasonDuplicate, Detail: "the id is used twice"}
			continue
		}
		seenID[ch.ID] = true
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, ch Chain) {
			defer wg.Done()
			defer func() { <-sem }()
			t, check, rej, err := buildChain(ctx, r, abs, name, ch, opts)
			switch {
			case err != nil:
				outs[i].infr = &Rejection{Chain: ch.ID, Reason: ReasonInfra, Detail: err.Error()}
			case rej != nil:
				rej.Chain = ch.ID
				outs[i].rej = rej
			default:
				outs[i].task = t
			}
			outs[i].check = check
		}(i, ch)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, rep, ctx.Err()
	}
	// Results in the order of the chains; a chain with the range of an earlier
	// one is a duplicate.
	var tasks []rl.Task
	seenRange := map[string]string{}
	for i, o := range outs {
		if o.check != nil {
			rep.Chains = append(rep.Chains, *o.check)
		}
		switch {
		case o.task != nil:
			rep.Candidates++
			var m Meta
			_ = json.Unmarshal(o.task.Meta, &m)
			key := m.Parent + ".." + m.Commit
			if prev, dup := seenRange[key]; dup {
				rep.add(Rejection{Chain: chains[i].ID, Commit: m.Commit, Reason: ReasonDuplicate, Detail: "the range of chain " + prev})
				if opts.Seal != "" {
					_ = os.RemoveAll(filepath.Join(opts.Seal, o.task.ID+".git"))
				}
				continue
			}
			seenRange[key] = chains[i].ID
			tasks = append(tasks, *o.task)
			rep.Accepted++
			if opts.Progress != nil {
				opts.Progress(fmt.Sprintf("accepted %s (%d)", o.task.ID, rep.Accepted))
			}
		case o.rej != nil:
			switch o.rej.Reason {
			case ReasonChainShape, ReasonUnknownCommit, ReasonDuplicate:
			default:
				rep.Candidates++ // a chain of consecutive changes that did not make a task
			}
			rep.add(*o.rej)
		case o.infr != nil:
			rep.Candidates++
			if len(rep.InfraErrors) < 500 {
				rep.InfraErrors = append(rep.InfraErrors, *o.infr)
			}
		}
	}
	rep.Duration = time.Since(started)
	if err := env.ValidateTasks(tasks); err != nil {
		return tasks, rep, fmt.Errorf("internal error: generated invalid tasks: %w", err)
	}
	return tasks, rep, nil
}

// chainPR is one pull request of a chain.
type chainPR struct {
	Hash, Parent string
	AuthorDate   time.Time
	Message      string
	Lines        int // added plus deleted lines outside the tests
}

// isTestKind reports whether a kind belongs to the tests (hidden, protected,
// never part of the reference solution).
func isTestKind(k Kind) bool { return k == Test || k == TestSupport }

// buildChain makes, records and checks the task of one chain. A non-nil
// *Rejection means the chain is unusable; a non-nil error is an infrastructure
// problem. The check is returned whenever it ran.
func buildChain(ctx context.Context, r *repo, repoDir, repoName string, ch Chain, opts ChainOptions) (task *rl.Task, check *ChainCheck, rej *Rejection, err error) {
	reject := func(commit, reason, detail string) (*rl.Task, *ChainCheck, *Rejection, error) {
		return nil, check, &Rejection{Commit: commit, Reason: reason, Detail: detail}, nil
	}
	progress := func(format string, args ...any) {
		if opts.Progress != nil {
			opts.Progress("chain " + ch.ID + ": " + fmt.Sprintf(format, args...))
		}
	}

	// The pull requests, each on the one before.
	prs := make([]chainPR, len(ch.Commits))
	for k, rev := range ch.Commits {
		hash, err := r.resolve(ctx, strings.TrimSpace(rev))
		if err != nil {
			return reject(rev, ReasonUnknownCommit, firstLine(err.Error()))
		}
		parent, err := r.resolve(ctx, hash+"^1")
		if err != nil {
			return reject(hash, ReasonChainShape, "a root commit has nothing to start from")
		}
		if k > 0 && parent != prs[k-1].Hash {
			return reject(hash, ReasonChainShape, fmt.Sprintf("the first parent of commit %d is %s, not commit %d", k+1, parent[:12], k))
		}
		raw, err := r.git(ctx, "show", "-s", "--format=%aI%x1f%B", "--end-of-options", hash)
		if err != nil {
			return nil, nil, nil, err
		}
		dateText, msg, ok := strings.Cut(string(raw), "\x1f")
		if !ok {
			return nil, nil, nil, fmt.Errorf("unexpected git show output for %s", hash)
		}
		date, err := time.Parse(time.RFC3339, dateText)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("author date of %s: %w", hash, err)
		}
		prs[k] = chainPR{Hash: hash, Parent: parent, AuthorDate: date, Message: msg}
	}
	start, end := prs[0].Parent, prs[len(prs)-1].Hash
	n := len(prs)

	// What each pull request changed, and the test files any of them touched.
	testPaths := map[string]bool{}
	for k := range prs {
		changes, err := r.changes(ctx, prs[k].Parent, prs[k].Hash)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, c := range changes {
			if needsQuoting(c.Path) {
				return reject(prs[k].Hash, ReasonUnsupportedName, c.Path)
			}
			if _, kind := Classify(c.Path); isTestKind(kind) {
				testPaths[c.Path] = true
			} else {
				prs[k].Lines += c.Added + c.Deleted
			}
		}
	}
	all, err := r.changes(ctx, start, end)
	if err != nil {
		return nil, nil, nil, err
	}
	var goldPaths, allPaths []string
	lines, sourceLines := 0, 0
	for _, c := range all {
		if needsQuoting(c.Path) {
			return reject(end, ReasonUnsupportedName, c.Path)
		}
		allPaths = append(allPaths, c.Path)
		lines += c.Added + c.Deleted
		if _, kind := Classify(c.Path); !isTestKind(kind) {
			goldPaths = append(goldPaths, c.Path)
			sourceLines += c.Added + c.Deleted
		}
	}
	sort.Strings(allPaths)
	sort.Strings(goldPaths)
	if len(goldPaths) == 0 {
		return reject(end, ReasonEmptyGold, "")
	}

	// The hidden tests: the end's version of every test file touched. One the
	// chain deleted is removed from the start.
	var hidden []hiddenFile
	var deleted []string
	for _, p := range sortedKeysBool(testPaths) {
		lang, kind := Classify(p)
		if !r.exists(ctx, end, p) {
			if r.exists(ctx, start, p) {
				deleted = append(deleted, p)
			}
			continue
		}
		data, err := r.show(ctx, end, p)
		if err != nil {
			return nil, nil, nil, err
		}
		if opts.HiddenBlobs == nil && (bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)) {
			return reject(end, ReasonBinaryTest, p)
		}
		hidden = append(hidden, hiddenFile{Path: p, Kind: kind, Lang: lang, Data: data})
	}

	// The suite: a command per Go module, Python project and cargo package the
	// tests belong to.
	spec, setup, langs, err := chainSuite(ctx, r, end, hidden, deleted)
	if err != nil {
		return reject(end, ReasonNoTestPlan, err.Error())
	}
	if opts.Setup != nil {
		setup = opts.Setup
	}
	spec.DeadlineS = opts.DeadlineS

	license := licenseAt(ctx, r, end)
	if len(opts.AllowLicenses) > 0 && !containsFold(opts.AllowLicenses, license) {
		return reject(end, ReasonLicense, fmt.Sprintf("found %q, allowed %v", license, opts.AllowLicenses))
	}

	// The milestones.
	milestones := make([]string, n)
	for k, pr := range prs {
		text := ""
		if k < len(ch.Issues) {
			text = capText(CleanIssue(ch.Issues[k]), maxIssueBytes, "[The rest of this milestone is omitted.]")
		}
		if text == "" {
			text = capText(chainMessage(pr.Message), maxIssueBytes, "[The rest of this milestone is omitted.]")
		}
		if strings.TrimSpace(text) == "" {
			return reject(pr.Hash, ReasonEmptyMilestone, fmt.Sprintf("milestone %d", k+1))
		}
		milestones[k] = text
	}

	// The reference solution, and the state after each pull request: the start
	// with the non-test changes up to it.
	patches := make([][]byte, n+1)
	for k := 1; k <= n; k++ {
		paths := goldPaths
		if k < n {
			upTo, err := r.changes(ctx, start, prs[k-1].Hash)
			if err != nil {
				return nil, nil, nil, err
			}
			paths = nil
			for _, c := range upTo {
				if _, kind := Classify(c.Path); !isTestKind(kind) {
					paths = append(paths, c.Path)
				}
			}
		}
		if patches[k], err = nonTestDiff(ctx, r, start, prs[k-1].Hash, paths); err != nil {
			return nil, nil, nil, err
		}
	}
	gold := patches[n]
	if len(gold) == 0 {
		return reject(end, ReasonEmptyGold, "")
	}

	// Hidden files and protection.
	hiddenRefs := map[string]string{}
	hh := sha256.New()
	var protected []string
	for _, hf := range hidden {
		if opts.HiddenBlobs != nil {
			h, err := opts.HiddenBlobs.Put(hf.Data)
			if err != nil {
				return nil, nil, nil, err
			}
			hiddenRefs[hf.Path] = "blob:" + string(h)
		} else {
			hiddenRefs[hf.Path] = "text:" + string(hf.Data)
		}
		protected = append(protected, escapeGlob(hf.Path))
		hh.Write([]byte(hf.Path))
		hh.Write([]byte{0})
		hh.Write(hf.Data)
		hh.Write([]byte{0})
	}
	for _, d := range deleted {
		protected = append(protected, escapeGlob(d))
	}
	for _, l := range langs {
		protected = append(protected, protectedInfra(l)...)
		protected = append(protected, testGlobs[l]...)
	}
	// A change of the reference solution to a protected path (a CI file) is
	// discarded at verification, as an agent's would be: the self-check below
	// proves the task is solvable without it.
	protected = dedupe(protected)

	goldHash := sha256.Sum256(gold)
	source := map[string]string{"repo": repoName, "range": start + ".." + end}
	if license != "" {
		source["license"] = license
	}
	for k, v := range ch.Source {
		source[k] = v
	}
	commits := make([]string, n)
	for k, pr := range prs {
		commits[k] = pr.Hash
	}
	meta := Meta{
		Commit: end, Parent: start, AuthorDate: prs[n-1].AuthorDate.UTC().Format(time.RFC3339), Generator: "taskgen/chain",
		Lang: langs[0], Files: allPaths, Lines: lines, Commits: commits,
		GoldHash: hex.EncodeToString(goldHash[:]), HiddenHash: hex.EncodeToString(hh.Sum(nil)), Source: source,
	}
	if opts.GoldBlobs != nil {
		h, err := opts.GoldBlobs.Put(gold)
		if err != nil {
			return nil, nil, nil, err
		}
		meta.GoldBlob = string(h)
	}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return nil, nil, nil, err
	}
	t := &rl.Task{
		ID:     opts.IDPrefix + "-" + ch.ID,
		Kind:   rl.TaskSwarm,
		Repo:   rl.RepoSpec{Path: repoDir, Commit: start, License: license},
		Setup:  setup,
		Prompt: BuildChainPrompt(milestones),
		Team:   rl.Team{Mode: "swarm", Agents: clamp(n-1, 2, 6)},
		Verifier: rl.Verifier{
			Hidden: hiddenRefs, Protected: protected,
		},
		Budget: chainBudget(n, sourceLines, opts.Budget),
		Tags:   dedupe(append(append([]string{"long", "chain", "harvested"}, langs...), append([]string{opts.IDPrefix}, opts.Tags...)...)),
		Meta:   metaRaw,
	}

	// A sealed start, without the test files the chain deletes, is what the
	// suite is recorded and checked on.
	if opts.Seal != "" {
		if err := sealTask(ctx, r.g, r.gitDir, t, opts.Seal, "", SealOptions{Remove: deleted}); err != nil {
			return nil, nil, nil, err
		}
		defer func() {
			if task == nil {
				_ = os.RemoveAll(filepath.Join(opts.Seal, t.ID+".git"))
			}
		}()
	}

	// Record the suite at the start and after each pull request.
	if err := WriteSuite(t, spec, opts.HiddenBlobs); err != nil {
		return nil, nil, nil, err
	}
	progress("recording the suite at %d states, %d runs each", n+1, opts.Repeats)
	steps, err := RecordSuiteSteps(ctx, opts.Workspaces, *t, patches, opts.Repeats, opts.HiddenBlobs)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, env.ErrInfra) {
			return nil, nil, nil, err
		}
		return reject(end, ReasonSuiteUnrecorded, tail(err.Error(), 2000))
	}
	ms := make([]SuiteMilestone, n)
	step := map[string]int{}
	for k, pr := range prs {
		ms[k] = SuiteMilestone{Name: fmt.Sprintf("milestone-%d", k+1), Weight: float64(max(pr.Lines, 1))}
		step[ms[k].Name] = k + 1
	}
	sel, err := BuildStepUnits(steps, ms)
	if err != nil {
		return reject(end, ReasonNoMilestones, err.Error())
	}
	sel.Apply(&spec)
	if err := WriteSuite(t, spec, opts.HiddenBlobs); err != nil {
		return nil, nil, nil, err
	}
	if err := env.ValidateTask(*t); err != nil {
		return nil, nil, nil, fmt.Errorf("internal error: %w", err)
	}

	// The self-check: each state scores the weight of the milestones it reached.
	check = &ChainCheck{ID: ch.ID, Want: make([]float64, n+1), Got: make([]float64, n+1), Units: len(sel.Units), Guards: len(sel.Guards), Dropped: len(sel.Dropped)}
	total := 0.0
	for _, u := range sel.Units {
		total += u.Weight
	}
	for k := 0; k <= n; k++ {
		reached := 0.0
		for _, u := range sel.Units {
			if step[u.Name] <= k {
				reached += u.Weight
			}
		}
		check.Want[k] = reached / total
	}
	progress("checking the score of %d states", n+1)
	var bad []string
	for k := 0; k <= n; k++ {
		res, err := env.VerifyPatch(ctx, *t, patches[k], env.VerifyOptions{Workspaces: opts.Workspaces, HiddenBlobs: opts.HiddenBlobs})
		if err != nil {
			return nil, check, nil, err
		}
		check.Got[k] = res.Score
		label := "the start"
		if k > 0 {
			label = fmt.Sprintf("the state after pull request %d", k)
		}
		switch {
		case res.Rejected != "":
			bad = append(bad, fmt.Sprintf("%s: %s", label, res.Rejected))
		case res.TimedOut:
			bad = append(bad, label+": the scorer timed out")
		case math.Abs(res.Score-check.Want[k]) > chainTolerance:
			bad = append(bad, fmt.Sprintf("%s scores %.4f, want %.4f", label, res.Score, check.Want[k]))
		case k == n && !res.Pass:
			bad = append(bad, fmt.Sprintf("%s scores %.6f, want 1", label, res.Score))
		}
	}
	if len(bad) > 0 {
		return reject(end, ReasonSelfCheck, strings.Join(bad, "; "))
	}
	check.OK = true
	if opts.Seal != "" && opts.SealPath != "" {
		t.Repo.Path = path.Join(filepath.ToSlash(opts.SealPath), t.ID+".git")
	}
	return t, check, nil, nil
}

// testGlobs protect, per language with a suite command, every test file the
// suite runs, beyond the ones a chain touched: a guard or a milestone test in
// an untouched file must not be the agent's to edit. The globs follow what
// Classify calls tests, which the reference solution leaves out; a change of
// it they do match is discarded at verification like an agent's, and the
// self-check shows whether the task still holds.
var testGlobs = map[string][]string{
	Go:     {"*_test.go", "testdata/"},
	Python: {"test_*.py", "*_test.py", "tests/", "test/"},
	Rust:   {"tests/", "*_test.rs", "*_tests.rs", "tests.rs"},
}

// pythonProjectFiles mark the directory a Python project's tests run from.
var pythonProjectFiles = []string{"pyproject.toml", "setup.cfg", "setup.py", "pytest.ini", "tox.ini"}

// chainSuite chooses the suite of a chain from its hidden tests at rev (the
// end): GoSuiteCommand for each Go module, PythonSuiteCommand for the Python
// project (one at most) with the end's pytest configuration and, in a src
// layout, src/ first on PYTHONPATH, RustSuiteCommand
// for each cargo package. Each command first removes the deleted test files.
// It returns the record-only spec, the setup commands (go mod download and
// cargo fetch where they apply) and the languages, the most tested first.
func chainSuite(ctx context.Context, r *repo, rev string, hidden []hiddenFile, deleted []string) (SuiteSpec, []string, []string, error) {
	dirs := map[string]map[string]bool{Go: {}, Python: {}, Rust: {}}
	count := map[string]int{}
	for _, h := range hidden {
		if h.Kind != Test {
			continue
		}
		d := path.Dir(h.Path)
		var root string
		var ok bool
		switch h.Lang {
		case Go:
			if !strings.HasSuffix(h.Path, "_test.go") {
				continue
			}
			root, ok = r.nearestDirWith(ctx, rev, d, "go.mod")
		case Python:
			if path.Base(h.Path) == "conftest.py" {
				continue
			}
			root, ok = nearestDirWithAny(ctx, r, rev, d, pythonProjectFiles)
			if !ok {
				root, ok = ".", true
			}
		case Rust:
			root, ok = r.nearestDirWith(ctx, rev, d, "Cargo.toml")
		default:
			continue
		}
		if !ok {
			continue
		}
		dirs[h.Lang][root] = true
		count[h.Lang]++
	}
	var langs []string
	for l, c := range count {
		if c > 0 {
			langs = append(langs, l)
		}
	}
	if len(langs) == 0 {
		return SuiteSpec{}, nil, nil, errors.New("no Go, Python or Rust test file in a module, project or package the suite can run")
	}
	sort.Slice(langs, func(a, b int) bool {
		if count[langs[a]] != count[langs[b]] {
			return count[langs[a]] > count[langs[b]]
		}
		return langs[a] < langs[b]
	})
	if len(dirs[Python]) > 1 {
		return SuiteSpec{}, nil, nil, fmt.Errorf("tests span several Python projects (%s)", strings.Join(sortedKeysBool(dirs[Python]), ", "))
	}
	var spec SuiteSpec
	var setup []string
	cd := func(dir, cmd string) string {
		if dir == "." {
			return cmd
		}
		return "cd " + shq(dir) + " && " + cmd
	}
	rel := func(dir string) string {
		if dir == "." {
			return ""
		}
		return dir
	}
	for _, d := range sortedKeysBool(dirs[Go]) {
		spec.Commands = append(spec.Commands, GoSuiteCommand(rel(d)))
		setup = append(setup, cd(d, "go mod download"))
	}
	for _, d := range sortedKeysBool(dirs[Python]) {
		ini, err := pytestINI(func(name string) (string, bool, error) {
			p := name
			if d != "." {
				p = d + "/" + name
			}
			if !r.exists(ctx, rev, p) {
				return "", false, nil
			}
			b, err := r.show(ctx, rev, p)
			return string(b), err == nil, err
		})
		if err != nil {
			return SuiteSpec{}, nil, nil, fmt.Errorf("the pytest configuration: %w", err)
		}
		spec.PytestINI = ini
		c := PythonSuiteCommand(rel(d))
		if r.exists(ctx, rev, path.Join(d, "src")) {
			// A src layout: the tests import the working tree only with src/ first on the import path, ahead of
			// any installed copy of the project.
			c.Run = `export PYTHONPATH="src:$PYTHONPATH"; ` + c.Run
		}
		spec.Commands = append(spec.Commands, c)
	}
	for _, d := range sortedKeysBool(dirs[Rust]) {
		spec.Commands = append(spec.Commands, RustSuiteCommand(rel(d)))
		setup = append(setup, cd(d, "cargo fetch"))
	}
	if len(deleted) > 0 {
		for i, c := range spec.Commands {
			up := ""
			if c.Cwd != "" {
				up = strings.Repeat("../", strings.Count(c.Cwd, "/")+1)
			}
			args := make([]string, len(deleted))
			for j, p := range deleted {
				args[j] = shq(up + p)
			}
			spec.Commands[i].Run = "rm -f -- " + strings.Join(args, " ") + "; " + c.Run
		}
	}
	return spec, setup, langs, nil
}

// nearestDirWithAny is nearestDirWith for the first of several file names.
func nearestDirWithAny(ctx context.Context, r *repo, rev, dir string, files []string) (string, bool) {
	for {
		for _, f := range files {
			p := f
			if dir != "." {
				p = dir + "/" + f
			}
			if r.exists(ctx, rev, p) {
				return dir, true
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return "", false
		}
		dir = path.Dir(dir)
	}
}

// nonTestDiff is the diff from one commit to another of paths (none: an empty
// patch), binary-safe and without rename detection, every path meaning itself.
func nonTestDiff(ctx context.Context, r *repo, from, to string, paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return []byte{}, nil
	}
	args := append([]string{"diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv", from, to, "--"}, paths...)
	return r.gitIn(ctx, nil, []string{"GIT_LITERAL_PATHSPECS=1"}, args...)
}

// licenseAt is the SPDX identifier of the licence at rev, "" when none is
// recognised.
func licenseAt(ctx context.Context, r *repo, rev string) string {
	for _, f := range licenseFiles {
		if b, err := r.show(ctx, rev, f); err == nil {
			return DetectLicense(string(b))
		}
	}
	return ""
}

var scpRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/]`)

// repoIdentity names the repository in provenance: its origin's URL without
// credentials, query or fragment, or else the directory's base name (never a
// local path).
func repoIdentity(ctx context.Context, r *repo, abs string) string {
	if out, err := r.git(ctx, "config", "--get", "remote.origin.url"); err == nil {
		u := strings.TrimSpace(string(out))
		if p, err := url.Parse(u); err == nil && p.Host != "" && (p.Scheme == "https" || p.Scheme == "http" || p.Scheme == "ssh" || p.Scheme == "git") {
			p.User, p.RawQuery, p.Fragment = nil, "", ""
			return p.String()
		}
		if scpRepoRe.MatchString(u) {
			return u
		}
	}
	return filepath.Base(abs)
}

// mergeHeaderRe matches the first line a merge commit's message starts with.
var mergeHeaderRe = regexp.MustCompile(`^Merge (pull request|branch|remote-tracking branch|tag)\b`)

// chainMessage is a pull request's text from its commit message: cleaned
// (CleanMessage), without the "Merge pull request #12 from ..." line a merge
// commit starts with when a description follows it.
func chainMessage(msg string) string {
	msg = strings.ReplaceAll(msg, "\r\n", "\n")
	if first, rest, ok := strings.Cut(strings.TrimSpace(msg), "\n"); ok && mergeHeaderRe.MatchString(first) && strings.TrimSpace(CleanMessage(rest)) != "" {
		msg = rest
	}
	return CleanMessage(msg)
}

// BuildChainPrompt turns a chain's milestone texts, oldest first, into the task
// prompt: what the series is, each milestone under its number, and how the work
// is checked.
func BuildChainPrompt(milestones []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This task is a series of %d milestones: changes the project made one after another. Make them in order; each one builds on those before it.", len(milestones))
	for i, m := range milestones {
		fmt.Fprintf(&b, "\n\nMilestone %d:\n%s", i+1, strings.TrimSpace(m))
	}
	b.WriteString("\n\nYour work will be checked by the project's test suite, which you cannot see in full: each milestone earns its share of the score as its tests pass, and tests that pass now must keep passing. Do not edit existing test files.")
	return b.String()
}

// chainBudget scales a chain's budget with its pull requests and the lines it
// changes outside the tests: 6 hours of wall clock, 45 minutes more per pull
// request beyond the third and an hour more per 1000 lines, at most 12 hours.
// Fields set in override win.
func chainBudget(prs, lines int, override rl.Budget) rl.Budget {
	b := override
	if b.Steps == 0 {
		b.Steps = clamp(80*prs+lines/4, 300, 1500)
	}
	if b.Requests == 0 {
		b.Requests = b.Steps * 5 / 2
	}
	if b.WallS == 0 {
		b.WallS = clamp(6*3600+2700*(prs-MinChainPRs)+lines*36/10, 6*3600, 12*3600)
	}
	return b
}

// sortedKeysBool returns a set's members in lexical order.
func sortedKeysBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Summary is one line on the self-check: the score each state reached against
// the one it had to, and how many tests made it into the suite.
func (c ChainCheck) Summary() string {
	f := func(v []float64) string {
		s := make([]string, len(v))
		for i, x := range v {
			s[i] = strconv.FormatFloat(x, 'f', 3, 64)
		}
		return strings.Join(s, " ")
	}
	return fmt.Sprintf("scores %s, want %s (%d milestones with tests, %d guards, %d tests dropped)", f(c.Got), f(c.Want), c.Units, c.Guards, c.Dropped)
}
