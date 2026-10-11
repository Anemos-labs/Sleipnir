package taskgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// startEnv fixes who made a rebuild task's start commit and when, so that its
// id depends on its tree alone.
var startEnv = []string{
	"GIT_AUTHOR_NAME=sleipnir", "GIT_AUTHOR_EMAIL=start@sleipnir.invalid", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
	"GIT_COMMITTER_NAME=sleipnir", "GIT_COMMITTER_EMAIL=start@sleipnir.invalid", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
}

// startBranch is the one ref of a start repository; HEAD points at it.
const startBranch = "refs/heads/main"

// writeStart writes a task's start state to dest as a bare repository holding
// exactly one commit, and returns that commit and the reference solution.
//
// The start's tree is rev's tree (in the repository whose git directory is
// gitDir) with edits applied: a path mapped to nil is removed, any other path is
// written with that content, keeping the mode a replaced file had (0644 for a
// new one). The commit has no parent and a fixed author, committer, date and
// message, so the same tree always gives the same commit id. The repository
// holds only the objects that tree reaches, in one pack, and refs/heads/main
// (HEAD) naming the commit: nothing else of the source repository, its history
// and the files the edits replaced included, is in it, and it needs nothing from
// the source to be read.
//
// The reference solution is the diff, binary-safe and without rename detection,
// from the start's tree to rev's tree of the paths gold (each path meaning
// itself); with no paths it is empty.
//
// dest is replaced when it exists. The repository is built next to it and
// renamed into place (installDir), so a failed call leaves no partial
// repository at dest and keeps the one that was there.
func writeStart(ctx context.Context, g *env.Git, gitDir, rev, dest string, edits map[string][]byte, gold []string) (string, []byte, error) {
	if g == nil || dest == "" {
		return "", nil, errors.New("writeStart: no git or no destination")
	}
	src := func(args ...string) (string, error) {
		out, err := g.Run(ctx, "", nil, append([]string{"--git-dir=" + gitDir}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	tree, err := src("rev-parse", "--verify", "--end-of-options", rev+"^{tree}")
	if err != nil {
		return "", nil, fmt.Errorf("start: the tree of %s: %w", rev, err)
	}
	objects, err := src("rev-parse", "--git-path", "objects")
	if err != nil {
		return "", nil, fmt.Errorf("start: the object directory of %s: %w", gitDir, err)
	}
	if !filepath.IsAbs(objects) {
		if objects, err = filepath.Abs(objects); err != nil {
			return "", nil, err
		}
	}
	format, err := src("rev-parse", "--show-object-format")
	if err != nil || format == "" {
		format = "sha1"
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".tmp-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmp) // a no-op once renamed to dest
	initArgs := []string{"init", "--quiet", "--bare", "--template="}
	if format != "sha1" {
		initArgs = append(initArgs, "--object-format="+format)
	}
	if _, err := g.Run(ctx, "", nil, append(initArgs, tmp)...); err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}
	// The source's objects are borrowed through the environment only, so the
	// start repository never records where they came from.
	borrow := "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + objects
	dst := func(extra []string, stdin []byte, maxOut int64, args ...string) ([]byte, error) {
		if stdin == nil {
			return g.RunEnv(ctx, "", extra, nil, maxOut, append([]string{"--git-dir=" + tmp}, args...)...)
		}
		return g.RunEnv(ctx, "", extra, bytes.NewReader(stdin), maxOut, append([]string{"--git-dir=" + tmp}, args...)...)
	}
	index := []string{borrow, "GIT_INDEX_FILE=" + filepath.Join(tmp, "start.index")}
	if _, err := dst(index, nil, 1<<20, "read-tree", tree); err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}

	// The modes of the files the edits replace.
	modes := map[string]string{}
	if len(edits) > 0 {
		ls, err := dst([]string{borrow}, nil, 1<<30, "ls-tree", "-r", "-z", "--full-tree", tree)
		if err != nil {
			return "", nil, fmt.Errorf("start: %w", err)
		}
		for _, rec := range bytes.Split(ls, []byte{0}) {
			meta, p, ok := strings.Cut(string(rec), "\t")
			if f := strings.Fields(meta); ok && len(f) == 3 {
				if _, edited := edits[p]; edited {
					modes[p] = f[0]
				}
			}
		}
	}
	// The written files' blobs, hashed in one run from files on disk.
	paths := make([]string, 0, len(edits))
	for p := range edits {
		if p == "" || strings.ContainsAny(p, "\x00\n") {
			return "", nil, fmt.Errorf("start: cannot write %q", p)
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	blobDir := filepath.Join(tmp, "start.blobs")
	if err := os.Mkdir(blobDir, 0o700); err != nil {
		return "", nil, err
	}
	var list bytes.Buffer
	var written []string
	for _, p := range paths {
		if edits[p] == nil {
			continue
		}
		f := filepath.Join(blobDir, strconv.Itoa(len(written)))
		if err := os.WriteFile(f, edits[p], 0o600); err != nil {
			return "", nil, err
		}
		list.WriteString(f + "\n")
		written = append(written, p)
	}
	var shas []string
	if len(written) > 0 {
		out, err := dst(nil, list.Bytes(), 1<<24, "hash-object", "-w", "--no-filters", "--stdin-paths")
		if err != nil {
			return "", nil, fmt.Errorf("start: %w", err)
		}
		shas = strings.Fields(string(out))
		if len(shas) != len(written) {
			return "", nil, fmt.Errorf("start: git hash-object gave %d ids for %d files", len(shas), len(written))
		}
	}
	if err := os.RemoveAll(blobDir); err != nil {
		return "", nil, err
	}
	// Mode 0 removes a path from the index; one that is not there stays absent.
	var info bytes.Buffer
	zero := strings.Repeat("0", len(tree))
	i := 0
	for _, p := range paths {
		if edits[p] == nil {
			fmt.Fprintf(&info, "0 %s\t%s\x00", zero, p)
			continue
		}
		mode := modes[p]
		if mode != "100644" && mode != "100755" {
			mode = "100644"
		}
		fmt.Fprintf(&info, "%s %s\t%s\x00", mode, shas[i], p)
		i++
	}
	if info.Len() > 0 {
		if _, err := dst(index, info.Bytes(), 1<<20, "update-index", "-z", "--index-info"); err != nil {
			return "", nil, fmt.Errorf("start: editing the tree: %w", err)
		}
	}
	out, err := dst(index, nil, 1<<20, "write-tree")
	if err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}
	startTree := strings.TrimSpace(string(out))
	if err := os.Remove(filepath.Join(tmp, "start.index")); err != nil {
		return "", nil, err
	}

	// The reference solution, while the source's objects are still at hand.
	patch := []byte{}
	if len(gold) > 0 {
		args := append([]string{"diff-tree", "-r", "-p", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv",
			"--no-color", "--src-prefix=a/", "--dst-prefix=b/", startTree, tree, "--"}, gold...)
		if patch, err = dst([]string{borrow, "GIT_LITERAL_PATHSPECS=1"}, nil, 1<<30, args...); err != nil {
			return "", nil, fmt.Errorf("start: the reference solution: %w", err)
		}
	}

	// The objects the tree reaches, listed into a file and packed from it.
	listFile := filepath.Join(tmp, "start.objects")
	lf, err := os.Create(listFile)
	if err != nil {
		return "", nil, err
	}
	err = g.Stream(ctx, "", []string{borrow}, nil, lf, 0, "--git-dir="+tmp, "rev-list", "--objects", "--end-of-options", startTree)
	if cerr := lf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", nil, fmt.Errorf("start: listing the start tree's objects: %w", err)
	}
	lr, err := os.Open(listFile)
	if err != nil {
		return "", nil, err
	}
	_, err = g.RunEnv(ctx, "", []string{borrow}, lr, 1<<20, "--git-dir="+tmp, "pack-objects", "-q", filepath.Join(tmp, "objects", "pack", "pack"))
	_ = lr.Close()
	if err != nil {
		return "", nil, fmt.Errorf("start: packing the start tree: %w", err)
	}
	if err := os.Remove(listFile); err != nil {
		return "", nil, err
	}
	// The loose blobs and trees written above are in the pack now.
	if _, err := dst(nil, nil, 1<<20, "prune-packed", "-q"); err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}
	out, err = dst(startEnv, nil, 1<<20, "commit-tree", "-m", "start", startTree)
	if err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}
	commit := strings.TrimSpace(string(out))
	if _, err := dst(nil, nil, 1<<20, "update-ref", startBranch, commit); err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}
	if _, err := dst(nil, nil, 1<<20, "symbolic-ref", "HEAD", startBranch); err != nil {
		return "", nil, fmt.Errorf("start: %w", err)
	}
	// Without the source's objects every object the commit reaches must be there.
	if _, err := dst(nil, nil, 1<<20, "fsck", "--connectivity-only", "--no-dangling", "--no-progress"); err != nil {
		return "", nil, fmt.Errorf("start: the start repository is incomplete: %w", err)
	}
	if err := installDir(tmp, abs); err != nil {
		return "", nil, err
	}
	return commit, patch, nil
}
