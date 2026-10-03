package env

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// PatchFile is one file section of a git patch.
type PatchFile struct {
	Path string
	// Status is "A" (added), "M" (modified, including mode changes) or "D" (deleted).
	Status           string
	OldMode, NewMode string
	Binary           bool
	Added, Deleted   int
	// Kind is "file", "symlink" or "gitlink" (submodule or nested repository).
	// ("rename" only exists while parsing: such patches are rejected.)
	Kind string

	body []byte
}

var diffMarker = []byte("diff --git ")

// splitSections cuts a git patch into its per-file sections. Sections start
// with "diff --git " at the beginning of a line. In a patch git generated
// that string cannot occur anywhere else: hunk lines begin with a space, '+',
// '-' or '\', and the base85 lines of a binary patch cannot contain a space.
func splitSections(patch []byte) ([][]byte, error) {
	if len(patch) == 0 {
		return nil, nil
	}
	if !bytes.HasPrefix(patch, diffMarker) {
		return nil, fmt.Errorf("patch does not start with a file header")
	}
	var idx []int
	idx = append(idx, 0)
	for i := 0; ; {
		j := bytes.Index(patch[i:], append([]byte{'\n'}, diffMarker...))
		if j < 0 {
			break
		}
		i += j + 1
		idx = append(idx, i)
	}
	secs := make([][]byte, len(idx))
	for k := range idx {
		end := len(patch)
		if k+1 < len(idx) {
			end = idx[k+1]
		}
		secs[k] = patch[idx[k]:end]
	}
	return secs, nil
}

// parsePatch lists the files of a patch. Paths come from `git apply --numstat -z`,
// git's own parser and unambiguous even for names with spaces, quotes or
// newlines, and are matched one to one with the sections found by splitSections;
// any disagreement is an error rather than a guess. Renames and copies are
// refused: patches are generated with rename detection off, and a rename
// section names two paths, which would let a protected file be moved out from
// under its glob.
func (m *Workspaces) parsePatch(ctx context.Context, patch []byte) ([]PatchFile, error) {
	secs, err := splitSections(patch)
	if err != nil {
		return nil, err
	}
	if len(secs) == 0 {
		return nil, nil
	}
	env := []string{"GIT_CEILING_DIRECTORIES=" + m.root}
	out, err := m.git.RunEnv(ctx, m.tmp, env, bytes.NewReader(patch), 1<<28, "apply", "--numstat", "-z")
	if err != nil {
		return nil, err
	}
	recs := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	if len(recs) == 1 && len(recs[0]) == 0 {
		recs = nil
	}
	if len(recs) != len(secs) {
		return nil, fmt.Errorf("patch has %d file sections but git lists %d files (renames or copies are not supported)", len(secs), len(recs))
	}
	files := make([]PatchFile, len(secs))
	for i, rec := range recs {
		f := strings.SplitN(string(rec), "\t", 3)
		if len(f) != 3 || f[2] == "" {
			return nil, fmt.Errorf("unexpected numstat record %q (renames or copies are not supported)", rec)
		}
		pf := PatchFile{Path: f[2], body: secs[i]}
		if f[0] == "-" {
			pf.Binary = true
		} else {
			pf.Added, _ = strconv.Atoi(f[0])
			pf.Deleted, _ = strconv.Atoi(f[1])
		}
		parseSectionHeader(&pf)
		if pf.Kind == "rename" {
			// numstat names only the destination, so a rename would look like an
			// ordinary file and slip past the path-based protection.
			return nil, fmt.Errorf("the patch renames or copies a file to %q; regenerate it without rename detection", pf.Path)
		}
		firstLine, _, _ := bytes.Cut(secs[i], []byte{'\n'})
		if !bytes.Contains(firstLine, []byte(pf.Path)) && !bytes.Contains(firstLine, []byte(quoteHint(pf.Path))) {
			return nil, fmt.Errorf("section %d header %q does not mention %q", i+1, firstLine, pf.Path)
		}
		files[i] = pf
	}
	return files, nil
}

// quoteHint gives the most common C-style quoting of a path with characters git
// escapes in headers, for the consistency check above.
func quoteHint(p string) string {
	s := strconv.Quote(p)
	return s[1 : len(s)-1]
}

// parseSectionHeader reads the extended header lines (modes, binary markers).
func parseSectionHeader(pf *PatchFile) {
	pf.Status = "M"
	pf.Kind = "file"
	for _, line := range strings.Split(string(pf.body), "\n")[1:] {
		switch {
		case strings.HasPrefix(line, "new file mode "):
			pf.Status, pf.NewMode = "A", strings.TrimSpace(strings.TrimPrefix(line, "new file mode "))
		case strings.HasPrefix(line, "deleted file mode "):
			pf.Status, pf.OldMode = "D", strings.TrimSpace(strings.TrimPrefix(line, "deleted file mode "))
		case strings.HasPrefix(line, "old mode "):
			pf.OldMode = strings.TrimSpace(strings.TrimPrefix(line, "old mode "))
		case strings.HasPrefix(line, "new mode "):
			pf.NewMode = strings.TrimSpace(strings.TrimPrefix(line, "new mode "))
		case strings.HasPrefix(line, "index "):
			if f := strings.Fields(line); len(f) == 3 {
				if pf.OldMode == "" {
					pf.OldMode = f[2]
				}
				if pf.NewMode == "" {
					pf.NewMode = f[2]
				}
			}
		case strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "),
			strings.HasPrefix(line, "copy from "), strings.HasPrefix(line, "copy to "),
			strings.HasPrefix(line, "similarity index "), strings.HasPrefix(line, "dissimilarity index "):
			pf.Kind = "rename"
		case strings.HasPrefix(line, "Binary files "), line == "GIT binary patch":
			pf.Binary = true
			goto done
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "@@ "):
			goto done
		}
	}
done:
	for _, mode := range []string{pf.OldMode, pf.NewMode} {
		switch mode {
		case "160000":
			pf.Kind = "gitlink"
		case "120000":
			if pf.Kind != "gitlink" {
				pf.Kind = "symlink"
			}
		}
	}
}

// filterResult is what survives protection.
type filterResult struct {
	Patch     []byte      // the sections to apply
	Applied   []PatchFile // the files they touch
	Protected []string    // protected or hidden paths the patch touched (sorted, unique)
	Skipped   []SkippedPath
}

// foldPath normalises a path for case-insensitive comparison.
func foldPath(p string) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(p)))
}

// filterPatch removes every file section that touches a protected path or a
// hidden file and reports what it removed. Hidden paths are protected
// implicitly: the verifier is about to write its own content there, and an
// agent that had already put something at that path was either guessing or
// trying to pre-empt it. Sections with unsafe paths and submodule entries are
// dropped too (and reported), so nothing the agent names can reach outside the
// checkout or git's control files.
func filterPatch(files []PatchFile, protected *Matcher, hidden map[string]bool) filterResult {
	var res filterResult
	var buf bytes.Buffer
	seen := map[string]bool{}
	for _, f := range files {
		if err := validRelPath(f.Path); err != nil {
			res.Skipped = append(res.Skipped, SkippedPath{Path: f.Path, Reason: "unsafe path: " + err.Error()})
			continue
		}
		if protected.Match(f.Path) || hidden[foldPath(f.Path)] {
			if !seen[f.Path] {
				seen[f.Path] = true
				res.Protected = append(res.Protected, f.Path)
			}
			continue
		}
		if f.Kind == "gitlink" {
			res.Skipped = append(res.Skipped, SkippedPath{Path: f.Path, Reason: "submodule or nested repository entries are not applied"})
			continue
		}
		buf.Write(f.body)
		if !bytes.HasSuffix(f.body, []byte("\n")) {
			buf.WriteByte('\n')
		}
		res.Applied = append(res.Applied, f)
	}
	sort.Strings(res.Protected)
	res.Patch = buf.Bytes()
	return res
}

// errPatchRejected means the agent's diff could not be applied to the pristine
// checkout it was computed against. That can only be caused by the content of
// the diff (for example names that collide on a case-insensitive filesystem),
// so it is the agent's failure, not an infrastructure error.
type errPatchRejected struct{ msg string }

// Error returns the reason a patch was rejected.
func (e *errPatchRejected) Error() string { return e.msg }

// applyPatch applies a filtered patch inside dir. `git apply` never touches
// paths outside the directory or beyond a symlink, and runs against an empty
// scratch repository so that no configuration or attributes of the checkout can
// influence it.
func (m *Workspaces) applyPatch(ctx context.Context, dir string, patch []byte) error {
	if len(patch) == 0 {
		return nil
	}
	gd, err := m.emptyGitDir(ctx)
	if err != nil {
		return Infra("apply", err)
	}
	_, err = m.git.RunEnv(ctx, dir, nil, bytes.NewReader(patch), 1<<20, "--git-dir="+gd, "apply", "--whitespace=nowarn", "-")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &errPatchRejected{msg: "the diff does not apply to a clean checkout: " + err.Error()}
	}
	return nil
}

// emptyGitDir returns a shared empty bare repository used as GIT_DIR for
// commands that need none.
func (m *Workspaces) emptyGitDir(ctx context.Context) (string, error) {
	m.emptyOnce.Do(func() {
		m.emptyDir = filepath.Join(m.root, "git-home", "empty.git")
		m.emptyErr = m.git.initBare(context.WithoutCancel(ctx), m.emptyDir)
	})
	return m.emptyDir, m.emptyErr
}
