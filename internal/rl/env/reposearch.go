package env

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// maxSearchedFile bounds the files a search of a plain directory reads (RepoContains).
const maxSearchedFile = 8 << 20

// RepoContains reports whether text occurs, ignoring case, in a file of the task's starting state: `git grep` at the
// task's commit of a local repository (repo.path, resolved against base when it is relative, the working directory
// when base is empty), or a search of the files of a plain directory. It is what the rewards' hardcoded-value
// detector asks of a literal taken from a hidden verifier file: one the repository already had before the change is
// not evidence that the agent copied it. A task whose repository is not on this machine (repo.url) cannot be
// searched, and is an error; so is a checkout whose git metadata git cannot read, and a file of a plain directory
// that cannot be read (an incomplete search is not a miss).
func RepoContains(ctx context.Context, g *Git, task rl.Task, base, text string) (bool, error) {
	if text == "" {
		return true, nil
	}
	p := task.Repo.Path
	if p == "" {
		return false, errors.New("the task's repository is not on this machine")
	}
	if !filepath.IsAbs(p) {
		if base == "" {
			base, _ = os.Getwd()
		}
		p = filepath.Join(base, p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("%s is not a directory", p)
	}
	if gd, ok := g.GitDirOf(ctx, p); ok {
		rev := task.Repo.Commit
		if rev == "" {
			rev = "HEAD"
		}
		_, err := g.Run(ctx, "", nil, "--git-dir="+gd, "grep", "-q", "-i", "-F", "-I", "-e", text, rev, "--")
		var ge *GitError
		switch {
		case err == nil:
			return true, nil
		case errors.As(err, &ge) && ge.Code == 1:
			return false, nil // no match
		default:
			return false, err
		}
	}
	// A checkout git cannot read is not a plain directory: its files may hold what came after the task's commit.
	if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
		return false, fmt.Errorf("%s has git metadata that git cannot read", p)
	}
	want := bytes.ToLower([]byte(text))
	found := false
	err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err // an incomplete search is not a miss
		}
		if !info.Mode().IsRegular() || info.Size() > maxSearchedFile {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(bytes.ToLower(b), want) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

// RepoContains is env.RepoContains with the workspaces' git and repository base.
func (m *Workspaces) RepoContains(ctx context.Context, task rl.Task, text string) (bool, error) {
	return RepoContains(ctx, m.git, task, m.opts.RepoBase, strings.TrimSpace(text))
}
