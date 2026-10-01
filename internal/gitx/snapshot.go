package gitx

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SnapshotTree returns the id of a tree object holding the work tree exactly as
// `git add -A && git commit` would record it: tracked modifications, deletions,
// mode changes and untracked, non-ignored files.
//
// It works on a private copy of the index, so the real index (which the agent or
// the user may be using at that very moment) is neither locked nor rewritten, and
// no ref moves. The only trace is loose objects for content git had not seen,
// which gc reclaims. Because the snapshot is what a commit would contain, Diff,
// Changed and Commit always agree with each other.
func (r *Repo) SnapshotTree(ctx context.Context) (string, error) {
	if r.bare {
		return "", &Error{Kind: KindNotARepo, Op: "snapshot", ExitCode: -1, Detail: "bare repository has no work tree"}
	}
	b, err := r.begin(ctx)
	if err != nil {
		return "", err
	}
	return b.snapshotTree(ctx)
}

func (b *batch) snapshotTree(ctx context.Context) (string, error) {
	r := b.r
	real, err := b.gitPath(ctx, "index")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "sleipnir-index-*")
	if err != nil {
		// A sandbox without a writable temp dir: fall back to the git directory,
		// which we are about to write to anyway.
		if tmp, err = os.CreateTemp(r.gitDir, "sleipnir-index-*"); err != nil {
			return "", &Error{Kind: KindOther, Op: "snapshot", ExitCode: -1, Detail: "cannot create a temporary index", Err: err}
		}
	}
	name := tmp.Name()
	defer os.Remove(name)
	at, err := copyIndex(tmp, real)
	if err != nil {
		tmp.Close()
		return "", &Error{Kind: KindOther, Op: "snapshot", ExitCode: -1, Detail: "cannot copy the index", Err: err}
	}
	if err := tmp.Close(); err != nil {
		return "", &Error{Kind: KindOther, Op: "snapshot", ExitCode: -1, Err: err}
	}
	// The copy has the time of the real index, not the time it was copied: see copyIndex.
	if !at.IsZero() {
		if err := os.Chtimes(name, at, at); err != nil {
			return "", &Error{Kind: KindOther, Op: "snapshot", ExitCode: -1, Detail: "cannot give the copy of the index the time of the index", Err: err}
		}
	}
	env := []string{"GIT_INDEX_FILE=" + name}
	if _, err := b.run(ctx, call{args: []string{"add", "-A"}, env: env, timeout: 5 * r.s.timeout}); err != nil {
		return "", err
	}
	out, err := b.run(ctx, call{args: []string{"write-tree"}, env: env})
	if err != nil {
		return "", err
	}
	return out.trimmed(), nil
}

// copyIndex seeds dst with the contents of the index at src, keeping its stat
// cache so `add -A` only rehashes files that really changed, and returns the time
// of the index file (the zero time when there is none). A missing index (a fresh
// --no-checkout worktree) leaves dst empty; git then treats it as absent.
//
// The caller gives dst that time. git takes a file whose size, inode and times
// match its entry for unchanged, except for an entry that is "racily clean": its
// time is not before the time of the index file, so a rewrite that kept the size
// and fell in the same second could have left no trace in the stat data, and git
// reads the content. A copy that has the time of the copying says no entry is
// racy, and a rewrite of that kind, made after the index was written, is missed by
// the snapshot.
func copyIndex(dst *os.File, src string) (time.Time, error) {
	f, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, os.Remove(dst.Name()) // let git create it: an empty file is an invalid index
		}
		return time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}
	_, err = io.Copy(dst, f)
	return fi.ModTime(), err
}

// gitPath resolves a path inside this repository's git directory, honoring
// per-worktree files (index, HEAD) and the shared common directory.
func (b *batch) gitPath(ctx context.Context, name string) (string, error) {
	out, err := b.r.run(ctx, call{args: []string{"rev-parse", "--path-format=absolute", "--git-path", name}})
	if err != nil {
		return "", err
	}
	p := out.trimmed()
	if p == "" {
		return "", newErr(KindOther, "rev-parse", "empty git path for %s", name)
	}
	return filepath.Clean(p), nil
}

// GitPath returns the absolute location of a file inside the git directory
// (for example "index", "MERGE_HEAD"), resolving per-worktree versus shared
// files the way git does.
func (r *Repo) GitPath(ctx context.Context, name string) (string, error) {
	if strings.ContainsAny(name, "\x00\n") || strings.HasPrefix(name, "-") {
		return "", newErr(KindInvalid, "rev-parse", "invalid git path %q", name)
	}
	b := &batch{r: r}
	return b.gitPath(ctx, name)
}
