package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/scan"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// scanState is the session's deep scans: the store, the repository they read, and a reader of each scanned commit.
type scanState struct {
	store *scan.Store
	mu    sync.Mutex
	repo  *gitx.Repo
	code  map[string]scan.Code
}

// ScanStore is the session's deep scans (nil when the scan plugin is off).
func (s *Session) ScanStore() *scan.Store {
	if s.scan == nil {
		return nil
	}
	return s.scan.store
}

// newScanTool makes the scan tool: its store rewrites the report after every change, and its prover runs reproducers in the
// session's sandbox when there is one. Without a sandbox the tool is the same (the tool list never depends on the session)
// and refuses to prove.
func (s *Session) newScanTool() *scan.Tool {
	if s.scan == nil {
		st := scan.NewStore(s.opts.Root)
		root := s.opts.Root
		st.OnChange = func(r *scan.Run) {
			if err := scan.WriteReport(root, r); err != nil {
				s.notice("", "scan report: "+err.Error())
			}
		}
		s.scan = &scanState{store: st, code: map[string]scan.Code{}}
	}
	t := scan.NewTool(s.scan.store, s.scanCode)
	if s.box != nil {
		if repo, err := s.scanRepo(); err == nil {
			t.Prover = &scan.Prover{Repo: repo, Run: s.runInSandbox, Network: s.box.Spec.Network}
		}
	}
	return t
}

// scanRepo is the project's repository, opened once.
func (s *Session) scanRepo() (*gitx.Repo, error) {
	s.scan.mu.Lock()
	defer s.scan.mu.Unlock()
	if s.scan.repo != nil {
		return s.scan.repo, nil
	}
	repo, err := gitx.Open(s.opts.Root, gitx.WithMaxOutput(256<<20), gitx.WithTimeout(5*time.Minute))
	if err != nil {
		return nil, fmt.Errorf("a deep scan reads a git repository, and %s is not one: %w", s.opts.Root, err)
	}
	s.scan.repo = repo
	return repo, nil
}

// scanCode reads the files of a scanned commit.
func (s *Session) scanCode(commit string) scan.Code {
	repo, err := s.scanRepo()
	if err != nil {
		return failCode{err}
	}
	s.scan.mu.Lock()
	defer s.scan.mu.Unlock()
	if c, ok := s.scan.code[commit]; ok {
		return c
	}
	c := scan.CodeAt(repo, commit)
	s.scan.code[commit] = c
	return c
}

type failCode struct{ err error }

func (f failCode) Read(context.Context, string) ([]byte, error) { return nil, f.err }

// runInSandbox runs argv from dir in the session's container, as the shell tool runs its commands: through the wrapper, with
// only the formatting variables crossing over.
func (s *Session) runInSandbox(ctx context.Context, dir string, argv []string, timeout time.Duration) ([]byte, int, error) {
	wrap := s.opts.ShellWrap
	if s.box == nil || len(wrap) == 0 {
		return nil, 0, errors.New("this session has no sandbox")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := append(append([]string(nil), wrap[1:]...), argv...)
	cmd := exec.CommandContext(ctx, wrap[0], args...)
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, -1, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, ee.ExitCode(), nil
	}
	return out, 0, err
}

// scanUsage is the line /scan answers a wrong call with.
const scanUsage = "usage: /scan [survey|standard|deep] [DIR] starts a deep scan of the repository (or of DIR in it) at HEAD; /scan lists the scans; /scan status|report [SCAN]; /scan rerun [SCAN] scans the same code again at HEAD; /scan compare [SCAN] [SINCE] says what is new, fixed and unchanged"

// ScanCommand runs /scan: it lists scans, reports on one, writes its report, or starts one. A start answers with the goal the
// host sets; every other use answers with text only.
func (s *Session) ScanCommand(ctx context.Context, arg string) (goalText, out string) {
	if s.scan == nil {
		return "", "/scan needs the scan plugin: /plugins enable scan, then restart the session"
	}
	f := strings.Fields(arg)
	if len(f) == 0 || f[0] == "list" {
		return "", s.scanList()
	}
	switch f[0] {
	case "status", "report":
		slug := ""
		if len(f) > 1 {
			slug = f[1]
		}
		r, err := s.scan.store.Snapshot(slug)
		if err != nil {
			return "", err.Error()
		}
		if f[0] == "status" {
			return "", tools.SanitizeForTerminal(scan.StatusText(r))
		}
		if err := scan.WriteReport(s.opts.Root, r); err != nil {
			return "", "report: " + err.Error()
		}
		return "", "the report of " + r.Slug + ": " + filepath.Join(scan.RunDir(s.opts.Root, r.Slug), scan.ReportFile)
	case "compare":
		if len(f) > 3 {
			return "", scanUsage
		}
		slug, since := "", ""
		if len(f) > 1 {
			slug = f[1]
		}
		if len(f) > 2 {
			since = f[2]
		}
		c, err := s.ScanCompare(slug, since)
		if err != nil {
			return "", err.Error()
		}
		return "", tools.SanitizeForTerminal(strings.TrimRight(scan.CompareText(c), "\n"))
	case "rerun":
		if len(f) > 2 {
			return "", scanUsage
		}
		slug := ""
		if len(f) > 1 {
			slug = f[1]
		}
		g, err := s.RerunScan(ctx, slug, false)
		if err != nil {
			return "", "scan not started: " + err.Error()
		}
		return g, ""
	}
	depth := scan.DefaultDepth
	if _, ok := scan.DepthByID(f[0]); ok {
		depth, f = f[0], f[1:]
	}
	if len(f) > 1 {
		return "", scanUsage
	}
	focus := ""
	if len(f) == 1 {
		focus = f[0]
	}
	g, err := s.StartScan(ctx, depth, focus)
	if err != nil {
		return "", "scan not started: " + err.Error()
	}
	return g, ""
}

// StartScan takes the inventory of the repository at HEAD (below focus, a directory of it, when set), creates the scan, writes
// its brief and first report, and returns the goal that drives it.
func (s *Session) StartScan(ctx context.Context, depth, focus string) (string, error) {
	if s.scan == nil {
		return "", errors.New("the scan plugin is off: /plugins enable scan")
	}
	if !s.pluginOn(plugin.Goal) {
		return "", errors.New("a scan is driven as a goal, and the goal plugin is off: /plugins enable goal")
	}
	if focus != "" {
		abs := focus
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(s.opts.Cwd, abs)
		}
		rel, err := filepath.Rel(s.opts.Root, filepath.Clean(abs))
		if err != nil || !filepath.IsLocal(rel) {
			return "", fmt.Errorf("%s is not a directory of the project", focus)
		}
		focus = filepath.ToSlash(rel)
	}
	repo, err := s.scanRepo()
	if err != nil {
		return "", err
	}
	r, err := scan.Start(ctx, s.scan.store, repo, depth, focus)
	if err != nil {
		return "", err
	}
	if s.box == nil && r.DepthInfo().ProveFrom != "" {
		s.notice("", fmt.Sprintf("scan %s: this session has no sandbox, so no lead can be reproduced and findings from %s up stay unproven; start the session with --sandbox docker to prove them", r.Slug, r.DepthInfo().ProveFrom))
	}
	return scan.Objective(r, s.scanBriefSpec(r)), nil
}

// RerunScan creates a new scan of what the scan slug (the newest one when empty) scanned, at HEAD now, and returns the goal that
// drives it. With pull, the checked-out branch is first fast-forwarded from its upstream (scan.Pull), so that a repository cloned
// for a scan is scanned as it is now.
func (s *Session) RerunScan(ctx context.Context, slug string, pull bool) (string, error) {
	if s.scan == nil {
		return "", errors.New("the scan plugin is off: /plugins enable scan")
	}
	if !s.pluginOn(plugin.Goal) {
		return "", errors.New("a scan is driven as a goal, and the goal plugin is off: /plugins enable goal")
	}
	prev, err := s.scanOrNewest(slug)
	if err != nil {
		return "", err
	}
	if pull {
		if _, err := scan.Pull(ctx, s.opts.Root); err != nil {
			return "", fmt.Errorf("the repository was not updated, so the scan was not rerun: %w", err)
		}
	}
	repo, err := s.scanRepo()
	if err != nil {
		return "", err
	}
	r, err := scan.Rerun(ctx, s.scan.store, repo, prev)
	if err != nil {
		return "", err
	}
	if r.Commit == prev.Commit {
		s.notice("", fmt.Sprintf("scan %s reruns %s at the same commit %s: the comparison shows what this team finds that the last one did not", r.Slug, prev.Slug, r.Commit[:min(len(r.Commit), 12)]))
	}
	return scan.Objective(r, s.scanBriefSpec(r)), nil
}

// scanOrNewest is the scan slug names, or the project's newest scan when slug is empty.
func (s *Session) scanOrNewest(slug string) (*scan.Run, error) {
	if slug != "" {
		return s.scanRun(slug)
	}
	runs := scan.List(s.opts.Root)
	if len(runs) == 0 {
		return nil, errors.New("no scans in this project: /scan starts one")
	}
	return runs[0], nil
}

// ScanCompare compares the scan slug (the newest one when empty) with the scan since, or with its baseline (the scan it reruns,
// else the newest earlier scan of the same code) when since is empty.
func (s *Session) ScanCompare(slug, since string) (scan.Comparison, error) {
	if s.scan == nil {
		return scan.Comparison{}, errors.New("the scan plugin is off: /plugins enable scan")
	}
	cur, err := s.scanOrNewest(slug)
	if err != nil {
		return scan.Comparison{}, err
	}
	if since != "" && !scan.ValidSlug(since) {
		return scan.Comparison{}, fmt.Errorf("%q is not a scan", since)
	}
	return scan.CompareWith(s.opts.Root, cur.Slug, since)
}

// scanBriefSpec is the scan's brief as its goal names it (see briefSpec).
func (s *Session) scanBriefSpec(r *scan.Run) string {
	abs := filepath.Join(s.opts.Root, filepath.FromSlash(scan.BriefPath(r.Slug)))
	if s.iso != nil {
		return filepath.ToSlash(abs)
	}
	if rel, err := filepath.Rel(s.opts.Cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(abs)
}

// scanList lists the project's scans with their phase and numbers.
func (s *Session) scanList() string {
	runs := scan.List(s.opts.Root)
	if len(runs) == 0 {
		return "no scans in this project. " + scanUsage
	}
	var b strings.Builder
	for _, r := range runs {
		rep := scan.Check(r)
		c := rep.Counts
		fmt.Fprintf(&b, "%-36s %-8s %-7s %5d files %4d/%-4d mapped %4d/%-4d hunted %3d leads %3d confirmed\n", r.Slug, r.Depth, rep.Phase, c.Files,
			c.Mapped, c.Shards, c.Hunted, c.Shards, c.Leads, c.Confirmed)
	}
	b.WriteString("/scan status SCAN shows one; the report is .sleipnir/scan/SCAN/REPORT.md")
	return b.String()
}

// ScanRuns are the project's scans with their numbers, for the Deep scan view.
func (s *Session) ScanRuns() []wire.ScanRun {
	out := []wire.ScanRun{}
	if s.scan == nil {
		return out
	}
	active := s.scan.store.Active()
	for _, r := range scan.List(s.opts.Root) {
		rep := scan.Check(r)
		c := rep.Counts
		workers := 0
		if r.Slug == active {
			workers = s.scanWorkers()
		}
		out = append(out, wire.ScanRun{Slug: r.Slug, Commit: r.Commit, Depth: r.Depth, Focus: r.Focus, Phase: rep.Phase, Created: r.Created,
			Team: r.Team(), Workers: workers, Problems: rep.Blocking(), Files: c.Files, Funcs: c.Funcs, Lines: c.Lines, Shards: c.Shards, Mapped: c.Mapped,
			Hunted: c.Hunted, Points: c.Points, Edges: c.Edges, Leads: c.Leads, Open: c.Open, Confirmed: c.Confirmed, Unproven: c.Unproven,
			Rejected: c.Rejected, BySeverity: c.BySeverity, Active: r.Slug == active, Previous: r.Previous, Review: r.IsReview()})
	}
	return out
}

// scanWorkers is how many workers this session's team runs (its --swarm size, or its team's slots), 0 for a single agent.
func (s *Session) scanWorkers() int {
	if s.Swarm == nil {
		return 0
	}
	return s.Swarm.MaxWorkers()
}

// ScanDepths are the depths a scan can have, for the forms that start one.
func ScanDepths() []wire.ScanDepth {
	out := make([]wire.ScanDepth, 0, len(scan.Depths))
	for _, d := range scan.Depths {
		out = append(out, wire.ScanDepth{ID: d.ID, Title: d.Title, About: d.Summary, ShardFuncs: d.ShardFuncs, ShardLines: d.ShardLines,
			Verifiers: d.Verifiers, Defender: d.Defender, ProveFrom: d.ProveFrom})
	}
	return out
}

// maxScanProblems is the most of a scan's open problems ScanDetail lists.
const maxScanProblems = 40

// scanRun is a scan of the project by name, for the view's calls.
func (s *Session) scanRun(slug string) (*scan.Run, error) {
	if s.scan == nil {
		return nil, errors.New("the scan plugin is off: /plugins enable scan")
	}
	if !scan.ValidSlug(slug) {
		return nil, fmt.Errorf("%q is not a scan", slug)
	}
	return s.scan.store.Snapshot(slug)
}

// ScanDetail is one scan as the Deep scan view shows it: what it still needs, its findings with their traces, verdicts and
// reproductions, its shards with their maps, the attack surface and the edges.
func (s *Session) ScanDetail(slug string) (map[string]any, error) {
	r, err := s.scanRun(slug)
	if err != nil {
		return nil, err
	}
	rep := scan.Check(r)
	problems := rep.Problems
	if len(problems) > maxScanProblems {
		problems = problems[:maxScanProblems]
	}
	type shard struct {
		ID         string `json:"id"`
		Files      int    `json:"files"`
		First      string `json:"first"`
		Funcs      int    `json:"funcs"`
		Lines      int    `json:"lines"`
		Complexity int    `json:"complexity"`
		Claimed    string `json:"claimed,omitempty"`
		Map        string `json:"map,omitempty"`
		MappedBy   string `json:"mappedBy,omitempty"`
		Hunt       string `json:"hunt,omitempty"`
		HuntedBy   string `json:"huntedBy,omitempty"`
	}
	shards := make([]shard, 0, len(r.Shards))
	for _, sh := range r.Shards {
		x := shard{ID: sh.ID, Files: len(sh.Files), Funcs: sh.Funcs, Lines: sh.Lines, Complexity: sh.Complexity, Claimed: sh.Claimed,
			Map: sh.Mapped, MappedBy: sh.MappedBy, Hunt: sh.Hunted, HuntedBy: sh.HuntedBy}
		if len(sh.Files) > 0 {
			x.First = sh.Files[0]
		}
		shards = append(shards, x)
	}
	d := r.DepthInfo()
	workers := 0
	if r.Slug == s.scan.store.Active() {
		workers = s.scanWorkers()
	}
	return map[string]any{"slug": r.Slug, "commit": r.Commit, "depth": r.Depth, "focus": r.Focus, "created": r.Created, "team": r.Team(), "workers": workers,
		"verifiers": d.Verifiers, "defender": d.Defender, "proveFrom": d.ProveFrom, "sandbox": s.box != nil,
		"phase": rep.Phase, "counts": rep.Counts, "problems": problems, "moreProblems": len(rep.Problems) - len(problems),
		"findings": scan.Findings(r), "shards": shards, "points": r.Points, "edges": scan.MapDoc(r)["edges"],
		"reportDir": scan.RunDir(s.opts.Root, r.Slug), "previous": r.Previous, "base": r.Base}, nil
}

// ScanIssue writes a finding up as an issue for the project's tracker; the person reads and edits it before it is opened.
func (s *Session) ScanIssue(slug, lead string) (title, body string, err error) {
	r, err := s.scanRun(slug)
	if err != nil {
		return "", "", err
	}
	return scan.Issue(r, r.Lead(lead))
}

// maxIssueBody is the longest issue body OpenScanIssue sends (GitHub takes 65536 characters).
const maxIssueBody = 60000

// OpenScanIssue opens the issue the person approved on the project's GitHub repository, with gh on this machine (its own login
// and the project's remote), and returns its address.
func (s *Session) OpenScanIssue(ctx context.Context, title, body string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" || strings.TrimSpace(body) == "" {
		return "", errors.New("an issue needs a title and a body")
	}
	if len(title) > 256 || len(body) > maxIssueBody {
		return "", errors.New("the issue is too long for GitHub")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return "", errors.New("opening an issue needs the GitHub CLI (gh) on this machine, logged in: copy the issue instead")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "create", "--title", title, "--body-file", "-")
	cmd.Dir = s.opts.Root
	cmd.Stdin = strings.NewReader(body)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if len(text) > 400 {
			text = text[:400]
		}
		return "", fmt.Errorf("gh issue create: %s", tools.SanitizeForTerminal(text))
	}
	lines := strings.Split(text, "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// ScanFix is the goal that has the team fix a finding.
func (s *Session) ScanFix(slug, lead string) (string, error) {
	if !s.pluginOn(plugin.Goal) {
		return "", errors.New("a fix is driven as a goal, and the goal plugin is off: /plugins enable goal")
	}
	r, err := s.scanRun(slug)
	if err != nil {
		return "", err
	}
	return scan.FixObjective(r, r.Lead(lead))
}

// ScanExport renders a scan's report in one format from its ledger: json (findings.json), csv, html (the page that prints to
// PDF), md (REPORT.md) or sarif.
func (s *Session) ScanExport(slug, format string) (data []byte, name string, err error) {
	r, err := s.scanRun(slug)
	if err != nil {
		return nil, "", err
	}
	switch format {
	case "json":
		data, err = json.MarshalIndent(scan.FindingsDoc(r), "", " ")
	case "sarif":
		data, err = json.MarshalIndent(scan.SARIF(r), "", " ")
	case "csv":
		data = scan.CSV(r)
	case "html":
		data = []byte(scan.HTML(r))
	case "md":
		data = []byte(scan.Markdown(r))
	default:
		return nil, "", fmt.Errorf("unknown format %q", format)
	}
	return data, r.Slug, err
}
