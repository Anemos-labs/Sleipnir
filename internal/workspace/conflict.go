package workspace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/gitx"
)

// Caps on what a Conflict carries. A conflict is shown to a model: enough to act
// on, never enough to flood its context.
const (
	maxConflictFiles     = 20
	maxHunksPerFile      = 5
	maxConflictHunks     = 40
	maxSnippetLines      = 30
	maxSnippetBytes      = 2000
	maxConflictBlobBytes = 2 << 20
)

// Conflict describes a textual conflict between a submitted tree and what has
// already been integrated. It is data for the agent that must resolve it; it is
// also an error, for callers that prefer errors.As.
type Conflict struct {
	Agent string
	Task  string
	// Tip is the integration tip the submission was merged onto (the "ours" side),
	// Theirs the submitted commit, MergeBase their common ancestor.
	Tip       string
	Theirs    string
	MergeBase string
	// Files are the conflicted paths, sorted.
	Files []string
	// Details has one entry per file in Files.
	Details []FileConflict
	// Hunks are the conflicting regions, ours/base/theirs, capped.
	Hunks []Hunk
	// Suggest is what to do next, in words a model can follow.
	Suggest string
	// Truncated is true when files or hunks beyond the caps were left out.
	Truncated bool
}

func (c *Conflict) Error() string { return "workspace: merge conflict: " + c.Suggest }

// FileConflict is the shape of the conflict in one file.
type FileConflict struct {
	Path string
	// Kind is "content", "add/add", "deleted-by-us", "deleted-by-them",
	// "both-deleted", "added-by-us", "added-by-them" or "binary".
	Kind      string
	Binary    bool
	HunkCount int
	// With names the agents whose already-landed changes touch this file.
	With []string
	// Note explains anything unusual (a custom merge driver was disabled, a file
	// was too large to show).
	Note string
}

// Hunk is one conflicting region with the three versions of it. Snippets are
// capped in lines and bytes; Truncated says so.
type Hunk struct {
	File string
	// Line is the 1-based line of the region's start in the merged text with
	// conflict markers (the text git leaves in the work tree).
	Line      int
	Ours      string
	Base      string
	Theirs    string
	Truncated bool
}

type conflictOpts struct {
	agent, task string
	tip, theirs string
	mergeBase   string
	// landedBy names the agents whose landed changes touch a path.
	landedBy func(path string) []string
}

// buildConflict inspects a repository that is stopped in a conflicted merge or
// rebase and describes the conflict. It reads the index stages and rebuilds the
// marker text from the three blob versions with `git merge-file` instead of
// trusting the files in the work tree, so the answer does not depend on what a
// driver, an attribute or the agent left there, and works the same for a merge and
// a rebase.
func buildConflict(ctx context.Context, r *gitx.Repo, o conflictOpts) (*Conflict, error) {
	un, err := r.Unmerged(ctx)
	if err != nil {
		return nil, err
	}
	c := &Conflict{Agent: o.agent, Task: o.task, Tip: o.tip, Theirs: o.theirs, MergeBase: o.mergeBase}
	for i, u := range un {
		if i >= maxConflictFiles {
			c.Truncated = true
			break
		}
		fc := FileConflict{Path: u.Path, Kind: u.Kind()}
		if o.landedBy != nil {
			fc.With = o.landedBy(u.Path)
		}
		switch {
		case u.Ours.ID != "" && u.Theirs.ID != "" && (fc.Kind == "content" || fc.Kind == "add/add"):
			hunks, binary, note := contentHunks(ctx, r, u, &c.Truncated)
			fc.Binary, fc.Note = binary, note
			if binary {
				fc.Kind = "binary"
			}
			for _, h := range hunks {
				fc.HunkCount++
				if fc.HunkCount <= maxHunksPerFile && len(c.Hunks) < maxConflictHunks {
					c.Hunks = append(c.Hunks, h)
				} else {
					c.Truncated = true
				}
			}
		case fc.Kind == "deleted-by-us":
			fc.Note = "the integrated code deleted this file; the submission modified it"
		case fc.Kind == "deleted-by-them":
			fc.Note = "the submission deleted this file; the integrated code modified it"
		}
		c.Files = append(c.Files, u.Path)
		c.Details = append(c.Details, fc)
	}
	sort.Strings(c.Files)
	sort.SliceStable(c.Details, func(i, j int) bool { return c.Details[i].Path < c.Details[j].Path })
	c.Suggest = suggestText(c)
	return c, nil
}

// contentHunks merges the three versions of a file and parses the conflicts.
func contentHunks(ctx context.Context, r *gitx.Repo, u gitx.Unmerged, truncated *bool) (hunks []Hunk, binary bool, note string) {
	read := func(s gitx.UnmergedStage) ([]byte, error) {
		if s.ID == "" {
			return nil, nil
		}
		return r.Blob(ctx, s.ID, maxConflictBlobBytes+1)
	}
	ours, err1 := read(u.Ours)
	base, err2 := read(u.Base)
	theirs, err3 := read(u.Theirs)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, false, "could not read the file versions: " + firstLine(err.Error())
	}
	if len(ours) > maxConflictBlobBytes || len(base) > maxConflictBlobBytes || len(theirs) > maxConflictBlobBytes {
		*truncated = true
		return nil, false, "too large to show the conflicting regions; compare the two versions directly"
	}
	merged, n, err := gitx.MergeFile(ctx, ours, base, theirs, "ours", "base", "theirs")
	if errors.Is(err, gitx.ErrBinary) {
		return nil, true, "binary file: keep one side (or produce a new version) and resubmit"
	}
	if err != nil {
		return nil, false, "could not compute the conflicting regions: " + firstLine(err.Error())
	}
	if n == 0 {
		// Git stopped, yet a plain text merge is clean: a custom merge driver named in
		// the repository is disabled (drivers run programs, which we never do).
		return nil, false, "a custom merge driver applies to this file and is disabled; merge the two versions by hand"
	}
	return parseHunks(u.Path, merged), false, ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// parseHunks extracts the conflict regions from diff3-style marker text:
//
//	<<<<<<< ours / ours lines / ||||||| base / base lines / ======= / theirs / >>>>>>> theirs
func parseHunks(file string, text []byte) []Hunk {
	var hunks []Hunk
	var cur *Hunk
	var ours, base, theirs []string
	section := 0 // 0 outside, 1 ours, 2 base, 3 theirs
	for i, line := range strings.Split(string(text), "\n") {
		l := strings.TrimSuffix(line, "\r")
		switch {
		case section == 0 && strings.HasPrefix(l, "<<<<<<< "):
			cur = &Hunk{File: file, Line: i + 1}
			ours, base, theirs = nil, nil, nil
			section = 1
		case section == 1 && strings.HasPrefix(l, "||||||| "):
			section = 2
		case (section == 1 || section == 2) && l == "=======":
			section = 3
		case section == 3 && strings.HasPrefix(l, ">>>>>>> "):
			var t1, t2, t3 bool
			cur.Ours, t1 = clipSnippet(ours)
			cur.Base, t2 = clipSnippet(base)
			cur.Theirs, t3 = clipSnippet(theirs)
			cur.Truncated = t1 || t2 || t3
			hunks = append(hunks, *cur)
			cur, section = nil, 0
		case section == 1:
			ours = append(ours, line)
		case section == 2:
			base = append(base, line)
		case section == 3:
			theirs = append(theirs, line)
		}
	}
	return hunks
}

// clipSnippet joins lines, cutting at the line and byte caps.
func clipSnippet(lines []string) (string, bool) {
	cut := false
	if len(lines) > maxSnippetLines {
		lines, cut = lines[:maxSnippetLines], true
	}
	s := strings.Join(lines, "\n")
	if len(s) > maxSnippetBytes {
		s = s[:maxSnippetBytes]
		// do not end in the middle of a UTF-8 sequence
		for len(s) > 0 && s[len(s)-1]&0xC0 == 0x80 {
			s = s[:len(s)-1]
		}
		if len(s) > 0 && s[len(s)-1] >= 0xC0 {
			s = s[:len(s)-1]
		}
		cut = true
	}
	return s, cut
}

// suggestText writes the next step. It is deterministic (sorted, no times) so it
// is safe to place in a prompt.
func suggestText(c *Conflict) string {
	var sb strings.Builder
	name := c.Agent
	if name == "" {
		name = "the submission"
	}
	fmt.Fprintf(&sb, "%s conflicts with changes that already landed in %d file(s): ", name, len(c.Files))
	var parts []string
	authors := map[string]bool{}
	for _, d := range c.Details {
		p := d.Path + " (" + d.Kind
		if len(d.With) > 0 {
			p += "; landed by " + strings.Join(d.With, ", ")
			for _, w := range d.With {
				authors[w] = true
			}
		}
		parts = append(parts, p+")")
	}
	sb.WriteString(strings.Join(parts, ", "))
	if c.Truncated {
		sb.WriteString(", ...")
	}
	sb.WriteString(". ")
	tip := c.Tip
	if len(tip) > 8 {
		tip = tip[:8]
	}
	if tip != "" {
		fmt.Fprintf(&sb, "Update your tree from the integration tip %s, ", tip)
	} else {
		sb.WriteString("Update your tree from the integration tip, ")
	}
	sb.WriteString("resolve the conflict markers keeping both intents, run the checks, and resubmit. ")
	if len(authors) > 0 {
		var who []string
		for a := range authors {
			who = append(who, a)
		}
		sort.Strings(who)
		fmt.Fprintf(&sb, "If the intents cannot both hold, ask %s or the manager before overwriting their work.", strings.Join(who, ", "))
	} else {
		sb.WriteString("If the intents cannot both hold, ask the manager to split or sequence the tasks.")
	}
	return sb.String()
}
