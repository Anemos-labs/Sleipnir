package gitx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrBinary is returned by MergeFile for content it cannot merge as text.
var ErrBinary = errors.New("gitx: binary content")

// MergeOptions describes a merge into the work tree's HEAD.
type MergeOptions struct {
	// Ref is what to merge (commit id, branch).
	Ref     string
	Message string
	// NoFF always creates a merge commit; FFOnly refuses anything but a
	// fast-forward.
	NoFF   bool
	FFOnly bool
	Author Author
}

// MergeResult reports what a merge did.
type MergeResult struct {
	// UpToDate: Ref was already merged; nothing changed.
	UpToDate bool
	// Conflicted: the merge stopped with conflicts left in the work tree and
	// index (MERGE_HEAD set). The caller inspects Unmerged and then either fixes
	// them or calls MergeAbort.
	Conflicted bool
	// Head is HEAD after the merge (the merge commit, the fast-forward target, or
	// unchanged when up to date or conflicted).
	Head string
	// Output is git's message text (CONFLICT lines included), capped.
	Output string
}

// conflictStyleDiff3 makes conflict markers carry the merge base too, which is
// what lets us show "ours / base / theirs" and lets an agent see what both sides
// changed relative to.
const conflictStyleDiff3 = "merge.conflictStyle=diff3"

// Merge merges opts.Ref into HEAD without an editor, hooks or signatures. A
// textual conflict is a result (Conflicted), not an error.
func (r *Repo) Merge(ctx context.Context, opts MergeOptions) (*MergeResult, error) {
	if r.bare {
		return nil, &Error{Kind: KindNotARepo, Op: "merge", ExitCode: -1, Detail: "bare repository has no work tree"}
	}
	if err := validateRev("merge", opts.Ref); err != nil {
		return nil, err
	}
	idEnv, err := opts.Author.env(r.s.now)
	if err != nil {
		return nil, err
	}
	msg := strings.TrimSpace(opts.Message)
	if msg == "" {
		msg = "Merge " + opts.Ref
	}
	if strings.ContainsRune(msg, 0) {
		return nil, newErr(KindInvalid, "merge", "message contains a NUL byte")
	}
	args := []string{"merge", "--no-edit", "--no-verify", "--no-autostash", "--no-stat", "--no-log", "--no-squash", "--no-gpg-sign", "-m", msg}
	switch {
	case opts.FFOnly:
		args = append(args, "--ff-only")
	case opts.NoFF:
		args = append(args, "--no-ff")
	default:
		args = append(args, "--ff")
	}
	args = append(args, "--end-of-options", opts.Ref)
	b, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	out, err := b.run(ctx, call{args: args, env: idEnv, config: []string{conflictStyleDiff3}, okExit: []int{1}, mutating: true, timeout: 5 * r.s.timeout})
	if err != nil {
		return nil, err
	}
	res := &MergeResult{Output: clip(out.text()+out.stderr, 16<<10)}
	if out.exit == 1 {
		un, uerr := r.Unmerged(ctx)
		if uerr != nil {
			return nil, uerr
		}
		if len(un) > 0 || r.InProgress() == "merge" {
			res.Conflicted = true
			res.Head, _ = r.Head(ctx)
			return res, nil
		}
		// Exit 1 without conflicts: git refused (e.g. --ff-only cannot fast-forward).
		return nil, &Error{Kind: classify(out.stderr + "\n" + out.text()), Op: "merge", ExitCode: 1, Stderr: out.stderr, Detail: firstLines(out.text(), 2)}
	}
	res.UpToDate = strings.Contains(out.text(), "Already up to date")
	res.Head, err = r.Head(ctx)
	return res, err
}

// MergeAbort abandons a merge in progress and restores HEAD, index and work tree.
func (r *Repo) MergeAbort(ctx context.Context) error {
	_, err := r.run(ctx, call{args: []string{"merge", "--abort"}, mutating: true})
	return err
}

// RebaseOptions describes replaying commits onto a new base.
type RebaseOptions struct {
	// Onto is the commit to replay onto.
	Onto string
	// Upstream is the exclusive lower bound: commits in (Upstream, Branch] are
	// replayed.
	Upstream string
	// Branch is the tip to replay. Pass a commit id, not a branch name: rebasing a
	// commit id leaves HEAD detached at the result and moves no branch, so the
	// caller decides which ref advances.
	Branch string
	Author Author
}

// Rebase replays commits (Upstream, Branch] onto Onto. A conflict is a result
// (Conflicted); the caller aborts with RebaseAbort and, because git returns to the
// *branch tip* rather than to where the caller started, resets HEAD itself.
func (r *Repo) Rebase(ctx context.Context, o RebaseOptions) (*MergeResult, error) {
	if r.bare {
		return nil, &Error{Kind: KindNotARepo, Op: "rebase", ExitCode: -1, Detail: "bare repository has no work tree"}
	}
	for _, rev := range []string{o.Onto, o.Upstream, o.Branch} {
		if err := validateRev("rebase", rev); err != nil {
			return nil, err
		}
	}
	idEnv, err := o.Author.env(r.s.now)
	if err != nil {
		return nil, err
	}
	b, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	args := []string{"rebase", "--no-autosquash", "--no-autostash", "--no-verify", "--no-rebase-merges", "--empty=drop", "--no-gpg-sign",
		"--onto", o.Onto, o.Upstream, o.Branch}
	out, err := b.run(ctx, call{args: args, env: idEnv, config: []string{conflictStyleDiff3}, okExit: []int{1}, mutating: true, timeout: 5 * r.s.timeout})
	if err != nil {
		return nil, err
	}
	res := &MergeResult{Output: clip(out.text()+out.stderr, 16<<10)}
	if out.exit == 1 {
		if un, _ := r.Unmerged(ctx); len(un) > 0 || r.InProgress() == "rebase" {
			res.Conflicted = true
			return res, nil
		}
		return nil, &Error{Kind: classify(out.stderr + "\n" + out.text()), Op: "rebase", ExitCode: 1, Stderr: out.stderr}
	}
	res.Head, err = r.Head(ctx)
	return res, err
}

// RebaseAbort abandons a rebase in progress.
func (r *Repo) RebaseAbort(ctx context.Context) error {
	_, err := r.run(ctx, call{args: []string{"rebase", "--abort"}, mutating: true})
	return err
}

// Abort abandons whatever multi-step operation is in progress, leaving HEAD where
// git decides; callers that need a specific commit follow it with ResetHard.
func (r *Repo) Abort(ctx context.Context) error {
	switch r.InProgress() {
	case "merge":
		return r.MergeAbort(ctx)
	case "rebase":
		return r.RebaseAbort(ctx)
	case "cherry-pick":
		_, err := r.run(ctx, call{args: []string{"cherry-pick", "--abort"}, mutating: true})
		return err
	case "revert":
		_, err := r.run(ctx, call{args: []string{"revert", "--abort"}, mutating: true})
		return err
	}
	return nil
}

// Blob reads a blob by object id, capped at max bytes (0 means the runner
// default).
func (r *Repo) Blob(ctx context.Context, id string, max int64) ([]byte, error) {
	if err := validateRev("cat-file", id); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, call{args: []string{"cat-file", "blob", id}, maxOut: max, killOnCap: max > 0})
	if err != nil {
		return nil, err
	}
	return out.stdout, nil
}

// MergeFile three-way merges file contents the way git does for text, without a
// repository and without consulting merge drivers, and returns the merged text
// with diff3-style conflict markers and the number of conflicts. Binary content
// yields ErrBinary.
func MergeFile(ctx context.Context, ours, base, theirs []byte, oursLabel, baseLabel, theirsLabel string, opts ...Option) (merged []byte, conflicts int, err error) {
	s, err := newSettings(opts)
	if err != nil {
		return nil, 0, err
	}
	for _, b := range [][]byte{ours, base, theirs} {
		if bytes.IndexByte(b, 0) >= 0 {
			return nil, 0, ErrBinary
		}
	}
	dir, err := os.MkdirTemp("", "sleipnir-merge-*")
	if err != nil {
		return nil, 0, &Error{Kind: KindOther, Op: "merge-file", ExitCode: -1, Err: err}
	}
	defer os.RemoveAll(dir)
	files := make([]string, 3)
	for i, content := range [][]byte{ours, base, theirs} {
		files[i] = filepath.Join(dir, [...]string{"ours", "base", "theirs"}[i])
		if err := os.WriteFile(files[i], content, 0o600); err != nil {
			return nil, 0, &Error{Kind: KindOther, Op: "merge-file", ExitCode: -1, Err: err}
		}
	}
	clean := func(s string) string { return strings.NewReplacer("\n", " ", "\x00", "").Replace(s) }
	ok := make([]int, 0, 128)
	for i := 1; i <= 127; i++ { // exit status is the number of conflicts (capped at 127)
		ok = append(ok, i)
	}
	out, err := s.exec(ctx, call{
		dir: dir,
		args: []string{"merge-file", "-p", "--diff3", "-L", clean(oursLabel), "-L", clean(baseLabel), "-L", clean(theirsLabel),
			files[0], files[1], files[2]},
		okExit: ok,
	})
	if err != nil {
		if strings.Contains(err.Error(), "binary") {
			return nil, 0, ErrBinary
		}
		return nil, 0, err
	}
	return out.stdout, out.exit, nil
}

// clip limits diagnostic text to n bytes and appends a truncation notice; n must be nonnegative.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[... output truncated ...]"
}
