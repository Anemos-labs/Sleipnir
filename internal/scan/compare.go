package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// Kinds of change between two scans, for one finding.
const (
	DeltaNew       = "new"       // the newer scan found it and the older one did not
	DeltaFixed     = "fixed"     // the older scan found it, and the newer one, finished, did not or rejected it
	DeltaPending   = "pending"   // the older scan found it, and the newer one, still running, has not confirmed it again yet
	DeltaUnchanged = "unchanged" // both scans found it
)

// DeltaKinds lists the kinds in the order a comparison gives them.
var DeltaKinds = []string{DeltaNew, DeltaFixed, DeltaPending, DeltaUnchanged}

// Delta is one finding as two scans see it.
type Delta struct {
	Kind string `json:"kind"`
	// Finding is the newer scan's finding (new, unchanged), or the older one's (fixed, pending).
	Finding Finding `json:"finding"`
	// Was is the older scan's finding, for an unchanged one.
	Was *Finding `json:"was,omitempty"`
	// Note says why: the verdict that rejected a fixed finding in the newer scan, a severity that changed.
	Note string `json:"note,omitempty"`
}

// Comparison is what changed between two scans: the findings (confirmed or unproven leads) the newer one has that the older did
// not, those it no longer has, and those both have. A finding of one is the same as a lead of the other when they are of the
// same category and their sinks hold the same code (the fingerprint SARIF carries), or failing that, when their traces share a
// step: the same code in the same file, wherever its line moved to.
type Comparison struct {
	Old       string `json:"old"`
	OldCommit string `json:"oldCommit"`
	New       string `json:"new"`
	NewCommit string `json:"newCommit"`
	// Final is set when the newer scan is done: a finding it did not confirm is then gone, not merely not found yet.
	Final  bool           `json:"final"`
	Counts map[string]int `json:"counts"`
	Deltas []Delta        `json:"deltas"`
}

// reported reports whether a finding counts as one: confirmed, or confirmed and waiting for its reproduction.
func reported(f Finding) bool { return f.Status == LeadConfirmed || f.Status == LeadUnproven }

// Compare compares the scan cur with the older scan old.
func Compare(old, cur *Run) Comparison {
	c := Comparison{Old: old.Slug, OldCommit: old.Commit, New: cur.Slug, NewCommit: cur.Commit, Final: Check(cur).Phase == PhaseDone,
		Counts: map[string]int{}, Deltas: []Delta{}}
	var was []Finding
	for _, f := range Findings(old) {
		if reported(f) {
			was = append(was, f)
		}
	}
	now := Findings(cur)
	used := make([]bool, len(now))
	// match finds the newer scan's lead that is the older finding f: by fingerprint first, then by a shared step; a lead
	// that is a finding wins over one that is not.
	match := func(f Finding) int {
		best := -1
		for pass := 0; pass < 2 && best < 0; pass++ {
			for i, n := range now {
				if used[i] || n.Category != f.Category || len(n.Trace) == 0 || len(f.Trace) == 0 {
					continue
				}
				same := fingerprint(n) == fingerprint(f)
				if pass == 1 {
					same = shareStep(n, f)
				}
				if same && (best < 0 || reported(n) && !reported(now[best])) {
					best = i
				}
			}
		}
		return best
	}
	add := func(d Delta) {
		c.Deltas = append(c.Deltas, d)
		c.Counts[d.Kind]++
	}
	for _, f := range was {
		i := match(f)
		if i < 0 {
			if c.Final {
				add(Delta{Kind: DeltaFixed, Finding: f, Note: "not found again at " + short(cur.Commit)})
			} else {
				add(Delta{Kind: DeltaPending, Finding: f, Note: "not confirmed again yet"})
			}
			continue
		}
		used[i] = true
		n := now[i]
		switch {
		case reported(n):
			w := f
			d := Delta{Kind: DeltaUnchanged, Finding: n, Was: &w}
			if n.Severity != f.Severity {
				d.Note = "severity " + f.Severity + " → " + n.Severity
			}
			add(d)
		case n.Status == LeadRejected:
			note := "rejected in " + cur.Slug
			if n.Reason != "" {
				note += " as " + n.Reason
			}
			add(Delta{Kind: DeltaFixed, Finding: f, Note: note + " (" + n.ID + ")"})
		default:
			add(Delta{Kind: DeltaPending, Finding: f, Note: "filed again as " + n.ID + ", being verified"})
		}
	}
	for i, n := range now {
		if !used[i] && reported(n) {
			add(Delta{Kind: DeltaNew, Finding: n})
		}
	}
	order := map[string]int{}
	for i, k := range DeltaKinds {
		order[k] = i
	}
	sort.SliceStable(c.Deltas, func(i, j int) bool {
		a, b := c.Deltas[i], c.Deltas[j]
		if order[a.Kind] != order[b.Kind] {
			return order[a.Kind] < order[b.Kind]
		}
		return sevRank(a.Finding.Severity) < sevRank(b.Finding.Severity)
	})
	return c
}

// shareStep reports whether two findings' traces hold the same code in the same file at some step.
func shareStep(a, b Finding) bool {
	for _, x := range a.Trace {
		qx := strings.Join(normLines(x.Quote), " ")
		if qx == "" {
			continue
		}
		for _, y := range b.Trace {
			if x.File == y.File && qx == strings.Join(normLines(y.Quote), " ") {
				return true
			}
		}
	}
	return false
}

// Baseline is the scan cur is compared with when none is named: the scan it reruns, else the newest scan of the same code
// (the same focus, a whole scan rather than a review) created before it; nil when there is none. A review has a baseline only
// when it is a rerun, since another review covers another change.
func Baseline(root string, cur *Run) *Run {
	if cur.Previous != "" {
		if r, err := Load(root, cur.Previous); err == nil {
			return r
		}
	}
	if cur.IsReview() {
		return nil
	}
	for _, r := range List(root) {
		if r.Slug == cur.Slug || r.IsReview() || r.Focus != cur.Focus {
			continue
		}
		if r.Created < cur.Created || r.Created == cur.Created && r.Slug < cur.Slug {
			return r
		}
	}
	return nil
}

// Rerun creates a new scan of what prev scanned, at the repository's HEAD now: the same depth and focus, or for a review, the
// change from the same base. The new scan names prev as its previous one; its brief lists prev's findings for the team to check
// again, and Compare tells what changed.
func Rerun(ctx context.Context, st *Store, repo *gitx.Repo, prev *Run) (*Run, error) {
	if prev.IsReview() {
		return startReview(ctx, st, repo, prev.Depth, prev.Base, prev.Slug)
	}
	return start(ctx, st, repo, prev.Depth, prev.Focus, prev.Slug)
}

// pullTimeout bounds Pull.
const pullTimeout = 10 * time.Minute

// Pull brings the repository at dir up to date before a rerun: it fast-forwards the checked-out branch from its upstream, with
// cloneConfig (no hook runs, https and ssh only) and without submodules. A branch that has diverged, or that tracks nothing, is
// refused and left as it is.
func Pull(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	var args []string
	for _, c := range cloneConfig {
		args = append(args, "-c", c)
	}
	args = append(args, "pull", "--ff-only", "--no-recurse-submodules", "--no-rebase")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	msg := strings.TrimSpace(out.String())
	if len(msg) > 600 {
		msg = msg[len(msg)-600:]
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git pull took longer than %s", pullTimeout)
		}
		return "", fmt.Errorf("git pull --ff-only: %v: %s", err, msg)
	}
	return msg, nil
}

// errNoBaseline is what a comparison without a scan to compare with answers.
func errNoBaseline(slug string) error {
	return fmt.Errorf("%s has no earlier scan of the same code to compare with: name one, or rerun it first", slug)
}

// CompareWith compares the scan slug with the scan since, or with its baseline when since is empty.
func CompareWith(root, slug, since string) (Comparison, error) {
	cur, err := Load(root, slug)
	if err != nil {
		return Comparison{}, err
	}
	var old *Run
	if since != "" {
		if old, err = Load(root, since); err != nil {
			return Comparison{}, err
		}
		if old.Slug == cur.Slug {
			return Comparison{}, errors.New("a scan compared with itself has no changes")
		}
	} else if old = Baseline(root, cur); old == nil {
		return Comparison{}, errNoBaseline(cur.Slug)
	}
	return Compare(old, cur), nil
}

// CompareText is a comparison as text: a line of counts, then one line per finding by kind.
func CompareText(c Comparison) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s) since %s (%s): %d new, %d fixed, %d unchanged", c.New, short(c.NewCommit), c.Old, short(c.OldCommit),
		c.Counts[DeltaNew], c.Counts[DeltaFixed], c.Counts[DeltaUnchanged])
	if n := c.Counts[DeltaPending]; n > 0 {
		fmt.Fprintf(&b, ", %d not confirmed again yet", n)
	}
	if !c.Final {
		b.WriteString(" (the newer scan is still running: what it has not found yet is not counted as fixed)")
	}
	b.WriteString("\n")
	if len(c.Deltas) == 0 {
		b.WriteString("neither scan has a finding\n")
	}
	for _, d := range c.Deltas {
		f := d.Finding
		sink := ""
		if len(f.Trace) > 0 {
			s := f.Trace[len(f.Trace)-1]
			sink = fmt.Sprintf(" %s:%d", oneLine(s.File), s.Line)
		}
		fmt.Fprintf(&b, "%-9s %-8s %-4s %s [%s]%s", d.Kind, f.Severity, f.ID, oneLine(f.Title), f.Category, sink)
		if d.Note != "" {
			fmt.Fprintf(&b, " · %s", oneLine(d.Note))
		}
		b.WriteString("\n")
	}
	return b.String()
}
