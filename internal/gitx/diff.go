package gitx

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// FileChange is one file in a Diff.
type FileChange struct {
	Path string
	// OldPath is the source of a rename or copy.
	OldPath string
	// Status is A added, M modified, D deleted, R renamed, C copied, T type
	// change.
	Status byte
	// Added and Deleted are line counts; both are -1 for binary files.
	Added, Deleted int
	Binary         bool
}

// DiffOptions tunes Diff.
type DiffOptions struct {
	// To is the revision to compare with. Empty means the work tree exactly as a
	// commit of it would record it, untracked files included (see SnapshotTree).
	To string
	// Paths restricts the diff (literal paths, not patterns).
	Paths []string
	// Context is the number of context lines (default 3).
	Context int
	// MaxPatchBytes caps the patch text (default 4 MiB). The patch is cut at a
	// file boundary so what remains still applies; Truncated and Omitted say so.
	MaxPatchBytes int
	// MaxFiles caps the file list (default 5000).
	MaxFiles int
	// NoPatch returns only the file list and stat.
	NoPatch bool
	// Renames detects renames and copies. Off by default: without it a rename is
	// a delete plus an add, which is what scope checks and overlap detection want.
	Renames bool
}

// Diff is a comparison of two states.
type Diff struct {
	// Base and To are the commit (or tree) ids compared; To is the snapshot tree
	// when the work tree was compared.
	Base, To string
	Files    []FileChange
	// Stat is a compact, human-readable summary, one line per file.
	Stat string
	// Patch is a binary-safe, git-apply-able patch (full index lines, --binary).
	Patch string
	// Truncated is true when the patch or the file list hit its cap.
	Truncated bool
	// Omitted lists files whose patch text was cut.
	Omitted []string
}

// Empty reports whether the two states are identical.
func (d *Diff) Empty() bool { return len(d.Files) == 0 }

// Diff compares base with the work tree (or opts.To). The patch is deterministic
// (fixed prefixes, no color, no external diff or textconv programs), binary-safe
// (--binary --full-index) and size-capped.
func (r *Repo) Diff(ctx context.Context, base string, opts DiffOptions) (*Diff, error) {
	baseTree, err := r.ResolveTree(ctx, base)
	if err != nil {
		return nil, err
	}
	var to string
	if opts.To == "" {
		if to, err = r.SnapshotTree(ctx); err != nil {
			return nil, err
		}
	} else if to, err = r.ResolveTree(ctx, opts.To); err != nil {
		return nil, err
	}
	return r.DiffTrees(ctx, baseTree, to, opts)
}

// DiffTrees compares two tree-ish objects (tree or commit ids, already
// resolved).
func (r *Repo) DiffTrees(ctx context.Context, a, b string, opts DiffOptions) (*Diff, error) {
	for _, id := range []string{a, b} {
		if err := validateRev("diff", id); err != nil {
			return nil, err
		}
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 5000
	}
	maxPatch := opts.MaxPatchBytes
	if maxPatch <= 0 {
		maxPatch = 4 << 20
	}
	ctxLines := opts.Context
	if ctxLines <= 0 {
		ctxLines = 3
	}
	var paths []string
	if len(opts.Paths) > 0 {
		paths = append(paths, "--")
		for _, p := range opts.Paths {
			c, err := cleanRelPath("diff", p)
			if err != nil {
				return nil, err
			}
			paths = append(paths, c)
		}
	}
	// Every diff invocation gets the same neutralizing flags: an attribute-selected
	// external diff or textconv would otherwise run a program from the repository.
	common := []string{"--no-color", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty"}
	if opts.Renames {
		common = append(common, "-M")
	} else {
		common = append(common, "--no-renames")
	}
	bt, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	d := &Diff{Base: a, To: b}

	ns, err := bt.run(ctx, call{args: append(append([]string{"diff", "--name-status", "-z"}, common...), append([]string{"--end-of-options", a, b}, paths...)...), maxOut: 16 << 20, killOnCap: true})
	if err != nil {
		return nil, err
	}
	files, trunc := parseNameStatus(ns.stdout, maxFiles)
	d.Truncated = trunc || ns.truncated
	if len(files) > 0 {
		nstat, err := bt.run(ctx, call{args: append(append([]string{"diff", "--numstat", "-z"}, common...), append([]string{"--end-of-options", a, b}, paths...)...), maxOut: 16 << 20, killOnCap: true})
		if err != nil {
			return nil, err
		}
		applyNumstat(files, nstat.stdout)
	}
	d.Files = files
	d.Stat = renderStat(files)
	if opts.NoPatch || len(files) == 0 {
		return d, nil
	}
	args := append([]string{"diff", "--binary", "--full-index", "--src-prefix=a/", "--dst-prefix=b/", "-U" + strconv.Itoa(ctxLines)}, common...)
	args = append(args, "--end-of-options", a, b)
	args = append(args, paths...)
	// Read one byte past the cap so we can tell "exactly the cap" from "more".
	out, err := bt.run(ctx, call{args: args, maxOut: int64(maxPatch) + 1, killOnCap: true})
	if err != nil {
		return nil, err
	}
	patch := out.stdout
	if len(patch) > maxPatch {
		patch, d.Omitted = cutAtFileBoundary(patch, maxPatch, files)
		d.Truncated = true
	}
	d.Patch = string(patch)
	return d, nil
}

// parseNameStatus decodes `diff --name-status -z`.
func parseNameStatus(data []byte, max int) ([]FileChange, bool) {
	toks := strings.Split(string(data), "\x00")
	var files []FileChange
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t == "" {
			continue
		}
		fc := FileChange{Status: t[0], Added: 0, Deleted: 0}
		switch t[0] {
		case 'R', 'C':
			if i+2 >= len(toks) {
				return files, false
			}
			fc.OldPath, fc.Path = toks[i+1], toks[i+2]
			i += 2
		default:
			if i+1 >= len(toks) {
				return files, false
			}
			fc.Path = toks[i+1]
			i++
		}
		if len(files) >= max {
			return files, true
		}
		files = append(files, fc)
	}
	return files, false
}

// applyNumstat fills line counts from `diff --numstat -z`.
func applyNumstat(files []FileChange, data []byte) {
	byPath := make(map[string]int, len(files))
	for i, f := range files {
		byPath[f.Path] = i
	}
	toks := strings.Split(string(data), "\x00")
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t == "" {
			continue
		}
		parts := strings.SplitN(t, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[2]
		if path == "" { // rename/copy: the next two tokens are old and new
			if i+2 >= len(toks) {
				return
			}
			path = toks[i+2]
			i += 2
		}
		idx, ok := byPath[path]
		if !ok {
			continue
		}
		if parts[0] == "-" || parts[1] == "-" {
			files[idx].Binary, files[idx].Added, files[idx].Deleted = true, -1, -1
			continue
		}
		files[idx].Added, _ = strconv.Atoi(parts[0])
		files[idx].Deleted, _ = strconv.Atoi(parts[1])
	}
}

// renderStat formats a compact per-file summary.
func renderStat(files []FileChange) string {
	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	added, deleted, bin := 0, 0, 0
	for _, f := range files {
		name := f.Path
		if f.OldPath != "" {
			name = f.OldPath + " => " + f.Path
		}
		if f.Binary {
			bin++
			fmt.Fprintf(&sb, " %c %s | binary\n", f.Status, name)
			continue
		}
		added += f.Added
		deleted += f.Deleted
		fmt.Fprintf(&sb, " %c %s | +%d -%d\n", f.Status, name, f.Added, f.Deleted)
	}
	fmt.Fprintf(&sb, " %d file(s) changed, %d insertion(s), %d deletion(s)", len(files), added, deleted)
	if bin > 0 {
		fmt.Fprintf(&sb, ", %d binary", bin)
	}
	sb.WriteString("\n")
	return sb.String()
}

var fileBoundary = []byte("\ndiff --git ")

// cutAtFileBoundary shortens patch to at most max bytes without splitting a
// file's section, and names the files that were dropped. If even the first file
// does not fit, nothing is kept: half a file is not an applicable patch. The
// patch and the file list come from the same diff and so have the same order:
// keeping n sections means the first n files were kept.
func cutAtFileBoundary(patch []byte, max int, files []FileChange) ([]byte, []string) {
	head := patch[:max]
	cut := bytes.LastIndex(head, fileBoundary)
	var kept []byte
	if cut >= 0 {
		kept = patch[:cut+1]
	}
	// A section header is the only line that starts with "diff --git " (content
	// lines carry a ' ', '+' or '-' prefix), so counting them counts sections.
	n := bytes.Count(kept, fileBoundary)
	if bytes.HasPrefix(kept, fileBoundary[1:]) {
		n++
	}
	if n > len(files) {
		n = len(files)
	}
	var omitted []string
	for _, f := range files[n:] {
		omitted = append(omitted, f.Path)
	}
	return kept, omitted
}

// ChangedPaths lists the paths that differ between base and the work tree (or
// opts.To), sorted, renames reported as delete plus add.
func (r *Repo) ChangedPaths(ctx context.Context, base, to string) ([]string, error) {
	d, err := r.Diff(ctx, base, DiffOptions{To: to, NoPatch: true, MaxFiles: 1 << 20})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range d.Files {
		for _, p := range []string{f.OldPath, f.Path} {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
