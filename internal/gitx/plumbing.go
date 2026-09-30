package gitx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CommitTree creates a commit object for tree with the given parents and message,
// without touching HEAD, the index or the work tree. The commit is unreachable
// until the caller points a ref at it.
func (r *Repo) CommitTree(ctx context.Context, tree string, parents []string, msg string, author Author) (string, error) {
	if err := validateRev("commit-tree", tree); err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		if err := validateRev("commit-tree", p); err != nil {
			return "", err
		}
		args = append(args, "-p", p)
	}
	msg = strings.TrimSpace(msg)
	if msg == "" || strings.ContainsRune(msg, 0) {
		return "", newErr(KindInvalid, "commit-tree", "invalid commit message")
	}
	idEnv, err := author.env(r.s.now)
	if err != nil {
		return "", err
	}
	out, err := r.run(ctx, call{args: args, stdin: strings.NewReader(msg + "\n"), env: idEnv, mutating: true})
	if err != nil {
		return "", err
	}
	return out.trimmed(), nil
}

// CommitAllowEmpty is CommitAll that also records a commit when nothing changed
// (the first commit of a private repository whose source directory is empty).
func (r *Repo) CommitAllowEmpty(ctx context.Context, msg string, author Author) (string, error) {
	sha, err := r.CommitAll(ctx, msg, author)
	if err != nil || sha != "" {
		return sha, err
	}
	idEnv, err := author.env(r.s.now)
	if err != nil {
		return "", err
	}
	if _, err := r.run(ctx, call{
		args:  []string{"commit", "--no-verify", "--no-gpg-sign", "--allow-empty", "--cleanup=whitespace", "-F", "-"},
		stdin: strings.NewReader(strings.TrimSpace(msg) + "\n"), env: idEnv, mutating: true,
	}); err != nil {
		return "", err
	}
	return r.Head(ctx)
}

// CommitsOnlyOn counts commits reachable from tip that are reachable from none of:
// the local branches whose names do not match excludeBranchGlob (for example
// "sleipnir/s1/*"), tags, remote-tracking branches, this work tree's HEAD, and the
// extra revisions. A non-zero count means tip carries work that exists nowhere
// else, so deleting the branch that points at it would lose that work.
func (r *Repo) CommitsOnlyOn(ctx context.Context, tip, excludeBranchGlob string, extra ...string) (int, error) {
	if err := validateRev("rev-list", tip); err != nil {
		return 0, err
	}
	args := []string{"rev-list", "--count", tip, "--not"}
	if excludeBranchGlob != "" {
		if strings.ContainsAny(excludeBranchGlob, "\x00\n") || strings.HasPrefix(excludeBranchGlob, "-") {
			return 0, newErr(KindInvalid, "rev-list", "invalid glob %q", excludeBranchGlob)
		}
		args = append(args, "--exclude="+excludeBranchGlob)
	}
	args = append(args, "--branches", "--tags", "--remotes")
	if !r.bare {
		args = append(args, "HEAD")
	}
	for _, e := range extra {
		if err := validateRev("rev-list", e); err != nil {
			return 0, err
		}
		args = append(args, e)
	}
	out, err := r.run(ctx, call{args: args})
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(out.trimmed())
	if err != nil {
		return 0, fmt.Errorf("gitx: unexpected rev-list count %q", out.trimmed())
	}
	return n, nil
}

// SparseCheckoutSet restricts this work tree to the given directories (cone
// mode: everything under them plus the files at the repository root) and
// materializes the change. Git records the setting per worktree, which makes it
// enable extensions.worktreeConfig in the shared repository configuration; that is
// git's own behavior for sparse worktrees.
func (r *Repo) SparseCheckoutSet(ctx context.Context, dirs []string) error {
	if r.bare {
		return &Error{Kind: KindNotARepo, Op: "sparse-checkout", ExitCode: -1, Detail: "bare repository has no work tree"}
	}
	args := []string{"sparse-checkout", "set", "--cone", "--"}
	for _, d := range dirs {
		c, err := cleanRelPath("sparse-checkout", strings.TrimSuffix(d, "/"))
		if err != nil {
			return err
		}
		// Unlike every other path we pass, these end up as lines of a pattern file
		// (.git/info/sparse-checkout, or its per-worktree copy): a newline would add
		// a pattern of the caller's choosing, and other control characters have no
		// business in a directory name we are asked to check out.
		for _, r := range c {
			if r < 0x20 || r == 0x7f {
				return newErr(KindInvalid, "sparse-checkout", "directory name %q contains a control character", d)
			}
		}
		args = append(args, c)
	}
	_, err := r.run(ctx, call{args: args, mutating: true})
	return err
}

// RefreshIndex re-reads file metadata so later commands do not have to rehash
// files that were copied in (copy-on-write population). It compares only size and
// modification time, which is what copying preserves.
func (r *Repo) RefreshIndex(ctx context.Context) error {
	_, err := r.run(ctx, call{
		args:     []string{"update-index", "-q", "--really-refresh"},
		config:   []string{"core.checkStat=minimal", "core.trustctime=false"},
		okExit:   []int{1}, // exit 1: some entries need an update (files really differ); not an error here
		mutating: true,
	})
	return err
}
