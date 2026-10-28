package session

import (
	"context"
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
const scanUsage = "usage: /scan [survey|standard|deep] [DIR] starts a deep scan of the repository (or of DIR in it) at HEAD; /scan lists the scans; /scan status|report [SCAN]"

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
	if depth == "" {
		depth = scan.DefaultDepth
	}
	if _, ok := scan.DepthByID(depth); !ok {
		return "", fmt.Errorf("unknown depth %q (%s)", depth, strings.Join(scan.DepthIDs(), ", "))
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
	head, err := repo.Head(ctx)
	if err != nil {
		return "", fmt.Errorf("the repository has no commit to scan: %w", err)
	}
	inv, err := scan.TakeInventory(ctx, repo, head, focus)
	if err != nil {
		return "", err
	}
	r, err := s.scan.store.Create(head, depth, focus, inv)
	if err != nil {
		return "", err
	}
	if err := scan.WriteBrief(s.opts.Root, r); err != nil {
		return "", err
	}
	if s.box == nil && r.DepthInfo().ProveFrom != "" {
		s.notice("", fmt.Sprintf("scan %s: this session has no sandbox, so no lead can be reproduced and findings from %s up stay unproven; start the session with --sandbox docker to prove them", r.Slug, r.DepthInfo().ProveFrom))
	}
	return scan.Objective(r, s.scanBriefSpec(r)), nil
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
