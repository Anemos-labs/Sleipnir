package taskgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// Sealed start states.
//
// A mined task names the repository it was mined from, and a workspace exports
// only the start commit's tree into a history-free repository. The source
// repository stays on disk, though, with every later commit in it (the fix of a
// task mined from history), where an agent running as the same user can read it.
// Seal writes the start state as a repository of its own: a bare repository with
// one commit, holding that commit and the trees and blobs its start tree reaches
// and no object outside them, so a task that names it carries no future and
// nothing a generator took away from the start.

// SealOptions configures Seal.
type SealOptions struct {
	// Remove lists repository-relative paths left out of the sealed tree: files a
	// generator takes away from the start, which then exist nowhere in the sealed
	// repository. A path the tree does not have is ignored.
	Remove []string
}

// sealEnv fixes who made the sealed commit and when, so its id depends on the
// tree alone.
var sealEnv = []string{
	"GIT_AUTHOR_NAME=sleipnir", "GIT_AUTHOR_EMAIL=start@sleipnir.invalid", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
	"GIT_COMMITTER_NAME=sleipnir", "GIT_COMMITTER_EMAIL=start@sleipnir.invalid", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
}

// sealMessage is the sealed commit's message.
const sealMessage = "start"

// sealBranch is the one ref of a sealed repository; HEAD points at it.
const sealBranch = "refs/heads/main"

// Seal writes the tree of rev, in the repository whose git directory is gitDir,
// to dest as a bare repository holding exactly one commit, and returns that
// commit's id. The commit has no parent, a fixed author, committer, date and
// message, so sealing the same tree twice gives the same id. Its tree is rev's
// tree (the same tree id) unless opts.Remove takes paths out of it. The
// repository holds only the objects that tree reaches, in one pack, and
// refs/heads/main (HEAD) naming the commit: no other commit, tree or blob of
// the source survives in it, and it needs nothing from the source to be read.
//
// dest is replaced when it exists. It is written next to its final place and
// renamed there, so a failed Seal leaves no partial repository at dest.
func Seal(ctx context.Context, g *env.Git, gitDir, rev, dest string, opts SealOptions) (string, error) {
	if g == nil {
		return "", errors.New("Seal: no git")
	}
	if dest == "" {
		return "", errors.New("Seal: no destination")
	}
	src := func(args ...string) (string, error) {
		out, err := g.Run(ctx, "", nil, append([]string{"--git-dir=" + gitDir}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	tree, err := src("rev-parse", "--verify", "--end-of-options", rev+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("seal: the tree of %s: %w", rev, err)
	}
	objects, err := src("rev-parse", "--git-path", "objects")
	if err != nil {
		return "", fmt.Errorf("seal: the object directory of %s: %w", gitDir, err)
	}
	if !filepath.IsAbs(objects) {
		if objects, err = filepath.Abs(objects); err != nil {
			return "", err
		}
	}
	format, err := src("rev-parse", "--show-object-format")
	if err != nil || format == "" {
		format = "sha1" // a git too old to say supports nothing else
	}

	abs, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) // a no-op once renamed to dest
	initArgs := []string{"init", "--quiet", "--bare", "--template="}
	if format != "sha1" {
		initArgs = append(initArgs, "--object-format="+format)
	}
	if _, err := g.Run(ctx, "", nil, append(initArgs, tmp)...); err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	// The source's objects are borrowed through the environment only, so the
	// sealed repository never records where they came from.
	borrow := []string{"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + objects}
	dst := func(extra []string, stdin []byte, args ...string) (string, error) {
		var in *bytes.Reader
		if stdin != nil {
			in = bytes.NewReader(stdin)
		}
		var out []byte
		var err error
		if in == nil {
			out, err = g.RunEnv(ctx, "", extra, nil, 1<<20, append([]string{"--git-dir=" + tmp}, args...)...)
		} else {
			out, err = g.RunEnv(ctx, "", extra, in, 1<<20, append([]string{"--git-dir=" + tmp}, args...)...)
		}
		return strings.TrimSpace(string(out)), err
	}

	if len(opts.Remove) > 0 {
		index := []string{borrow[0], "GIT_INDEX_FILE=" + filepath.Join(tmp, "seal.index")}
		if _, err := dst(index, nil, "read-tree", tree); err != nil {
			return "", fmt.Errorf("seal: %w", err)
		}
		// Mode 0 removes a path from the index; one that is not there stays absent.
		var info bytes.Buffer
		zero := strings.Repeat("0", len(tree))
		for _, p := range opts.Remove {
			if p == "" || strings.ContainsAny(p, "\x00\n") {
				return "", fmt.Errorf("seal: cannot remove %q", p)
			}
			fmt.Fprintf(&info, "0 %s\t%s\x00", zero, p)
		}
		if _, err := dst(index, info.Bytes(), "update-index", "-z", "--index-info"); err != nil {
			return "", fmt.Errorf("seal: removing paths: %w", err)
		}
		if tree, err = dst(index, nil, "write-tree"); err != nil {
			return "", fmt.Errorf("seal: %w", err)
		}
		if err := os.Remove(filepath.Join(tmp, "seal.index")); err != nil {
			return "", err
		}
	}

	// The objects the tree reaches, listed into a file and packed from it: a large
	// tree never sits in memory.
	list := filepath.Join(tmp, "seal.objects")
	lf, err := os.Create(list)
	if err != nil {
		return "", err
	}
	err = g.Stream(ctx, "", borrow, nil, lf, 0, "--git-dir="+tmp, "rev-list", "--objects", "--end-of-options", tree)
	if cerr := lf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("seal: listing the start tree's objects: %w", err)
	}
	lr, err := os.Open(list)
	if err != nil {
		return "", err
	}
	_, err = g.RunEnv(ctx, "", borrow, lr, 1<<20, "--git-dir="+tmp, "pack-objects", "-q", filepath.Join(tmp, "objects", "pack", "pack"))
	_ = lr.Close()
	if err != nil {
		return "", fmt.Errorf("seal: packing the start tree: %w", err)
	}
	if err := os.Remove(list); err != nil {
		return "", err
	}
	// Trees that write-tree made are in the pack now; their loose copies go.
	if _, err := dst(nil, nil, "prune-packed", "-q"); err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	commit, err := dst(sealEnv, nil, "commit-tree", "-m", sealMessage, tree)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	if _, err := dst(nil, nil, "update-ref", sealBranch, commit); err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	if _, err := dst(nil, nil, "symbolic-ref", "HEAD", sealBranch); err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	// Without the source's objects every object the commit reaches must be there.
	if _, err := dst(nil, nil, "fsck", "--connectivity-only", "--no-dangling", "--no-progress"); err != nil {
		return "", fmt.Errorf("seal: the sealed repository is incomplete: %w", err)
	}
	if err := installDir(tmp, abs); err != nil {
		return "", err
	}
	return commit, nil
}

// installDir puts the directory tmp in place at dest. A dest that exists is moved aside first and put back when the
// new one cannot be installed, so a failure never leaves dest missing; once tmp is in place the old one is removed.
func installDir(tmp, dest string) error {
	if _, err := os.Lstat(dest); err != nil {
		return os.Rename(tmp, dest)
	}
	aside, err := os.MkdirTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".old-")
	if err != nil {
		return err
	}
	old := filepath.Join(aside, "old")
	if err := os.Rename(dest, old); err != nil {
		_ = os.Remove(aside)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		if rerr := os.Rename(old, dest); rerr != nil {
			return fmt.Errorf("%w; and the earlier %s could not be put back (it is in %s): %v", err, dest, aside, rerr)
		}
		_ = os.Remove(aside)
		return err
	}
	return os.RemoveAll(aside)
}

// sealTask seals t's start state (t.Repo.Commit of the repository at gitDir) as
// <dir>/<t.ID>.git and points t at it: Repo.Path becomes <as>/<t.ID>.git (as is
// dir when empty) and Repo.Commit the sealed commit.
func sealTask(ctx context.Context, g *env.Git, gitDir string, t *rl.Task, dir, as string, opts SealOptions) error {
	name := t.ID + ".git"
	commit, err := Seal(ctx, g, gitDir, t.Repo.Commit, filepath.Join(dir, name), opts)
	if err != nil {
		return err
	}
	if as == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		t.Repo.Path = filepath.Join(abs, name)
	} else {
		t.Repo.Path = path.Join(filepath.ToSlash(as), name)
	}
	t.Repo.Commit = commit
	return nil
}
