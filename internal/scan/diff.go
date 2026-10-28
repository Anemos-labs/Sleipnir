package scan

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// Change is what a reviewed change did to one file: the line ranges, at the scanned commit, that it added or modified.
type Change struct {
	File  string   `json:"file"`
	Lines [][2]int `json:"lines,omitempty"`
}

// IsReview reports whether the scan reviews a change (from Base to Commit) rather than the whole repository.
func (r *Run) IsReview() bool { return r.Base != "" }

// Changed reports whether lines from..to of file (at the scanned commit) overlap what the reviewed change added or modified.
func (r *Run) Changed(file string, from, to int) bool {
	for _, c := range r.Changes {
		if c.File != file {
			continue
		}
		for _, l := range c.Lines {
			if from <= l[1] && to >= l[0] {
				return true
			}
		}
	}
	return false
}

// touches reports whether a cite lies, by its first line or any line of its quote, on a line the reviewed change made.
func (r *Run) touches(c Cite) bool {
	return r.Changed(c.File, c.Line, c.Line+strings.Count(strings.TrimRight(c.Quote, "\n"), "\n"))
}

// Anchor is where a finding is shown on the change: the last step of its trace on a changed line (the sink, when the change
// made it), or, in a scan of the whole repository, the sink.
func (r *Run) Anchor(l *Lead) *Cite {
	if len(l.Trace) == 0 {
		return nil
	}
	if !r.IsReview() {
		c := l.Trace[len(l.Trace)-1].Cite
		return &c
	}
	for i := len(l.Trace) - 1; i >= 0; i-- {
		if r.touches(l.Trace[i].Cite) {
			c := l.Trace[i].Cite
			return &c
		}
	}
	return nil
}

// Diff lists the files that the change from base to head adds or modifies, with the line ranges at head it made. Deleted files
// and pure renames have no lines at head and are left out.
func Diff(ctx context.Context, repo *gitx.Repo, base, head string) ([]Change, error) {
	res, err := repo.Git(ctx, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--unified=0", "-M", "--diff-filter=AMR",
		"--end-of-options", base, head, "--")
	if err != nil {
		return nil, err
	}
	return parseDiff(res.Stdout), nil
}

// parseDiff reads a unified diff with no context lines: each file's "+++ b/path" header, then its hunks' "+start,count" ranges.
// A "+++ " line counts as a header only between a file's "diff --git" line and its first hunk, so an added line that begins
// with "++ " is not taken for one.
func parseDiff(out string) []Change {
	var cs []Change
	var cur *Change
	header := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			cur, header = nil, true
		case header && strings.HasPrefix(l, "+++ "):
			// git ends an unquoted path that has a space with a tab
			p := strings.TrimSuffix(strings.TrimPrefix(l, "+++ "), "\t")
			cur = nil
			if p == "/dev/null" {
				continue
			}
			p = unquotePath(p)
			cs = append(cs, Change{File: strings.TrimPrefix(p, "b/")})
			cur = &cs[len(cs)-1]
		case strings.HasPrefix(l, "@@ ") && cur != nil:
			header = false
			f := strings.Fields(l)
			if len(f) < 3 || !strings.HasPrefix(f[2], "+") {
				continue
			}
			start, count, ok := strings.Cut(f[2][1:], ",")
			s, err := strconv.Atoi(start)
			if err != nil {
				continue
			}
			n := 1
			if ok {
				if n, err = strconv.Atoi(count); err != nil {
					continue
				}
			}
			if n > 0 {
				cur.Lines = append(cur.Lines, [2]int{s, s + n - 1})
			}
		}
	}
	out2 := cs[:0]
	for _, c := range cs {
		if len(c.Lines) > 0 {
			out2 = append(out2, c)
		}
	}
	return out2
}

// unquotePath reads a path git wrote in C quotes (one with special characters), as it is otherwise.
func unquotePath(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// StartReview creates a scan of the change from base to HEAD: the files the change made, mapped and hunted with what they
// reach, and leads only where a step of the trace is on a line the change wrote. base is a commit, or a ref such as the pull
// request's base branch: the review starts from where HEAD left it (their merge base), so a base that moved on since does not
// show its own commits as the change's.
func StartReview(ctx context.Context, st *Store, repo *gitx.Repo, depth, base string) (*Run, error) {
	if depth == "" {
		depth = DefaultDepth
	}
	if _, ok := DepthByID(depth); !ok {
		return nil, errDepth(depth)
	}
	head, err := repo.Head(ctx)
	if err != nil {
		return nil, fmt.Errorf("the repository has no commit to review: %w", err)
	}
	if strings.HasPrefix(base, "-") {
		return nil, fmt.Errorf("%q is not a commit", base)
	}
	from, err := repo.MergeBase(ctx, base, head)
	if err != nil {
		return nil, fmt.Errorf("no common history between %s and HEAD: %w", base, err)
	}
	if from == head {
		return nil, fmt.Errorf("HEAD has nothing that %s does not: there is no change to review", base)
	}
	changes, err := Diff(ctx, repo, from, head)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, c := range changes {
		paths = append(paths, c.File)
	}
	inv, err := inventory(ctx, repo, head, paths)
	if err != nil {
		return nil, err
	}
	if len(inv.Files) == 0 {
		return nil, errors.New("the change adds or modifies no source files: there is nothing to review")
	}
	kept := map[string]bool{}
	for _, f := range inv.Files {
		kept[f.Path] = true
	}
	review := changes[:0]
	for _, c := range changes {
		if kept[c.File] {
			review = append(review, c)
		}
	}
	r, err := st.create(head, depth, "", inv, from, review)
	if err != nil {
		return nil, err
	}
	if err := WriteBrief(st.Root(), r); err != nil {
		return nil, err
	}
	if err := WriteReport(st.Root(), r); err != nil {
		return nil, errors.Join(errors.New("the review was created but its first report was not written"), err)
	}
	return r, nil
}
