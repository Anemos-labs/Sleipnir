package gitx

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a failure so callers can react without parsing stderr.
type Kind int

const (
	// KindOther is an unclassified git failure; Error.Stderr has the detail.
	KindOther Kind = iota
	// KindNotARepo: the path is not inside a git repository (or the operation
	// needs a work tree and the repository is bare).
	KindNotARepo
	// KindConflict: overlapping changes prevented a merge, rebase or patch.
	KindConflict
	// KindDirty: the operation was refused because uncommitted changes would be
	// lost or would block it.
	KindDirty
	// KindNotFound: a ref, object or path does not exist.
	KindNotFound
	// KindTimeout: the command outlived its deadline and was killed.
	KindTimeout
	// KindCanceled: the caller's context was canceled and the command killed.
	KindCanceled
	// KindLocked: another git process held a lock file for longer than we were
	// willing to wait.
	KindLocked
	// KindUnsafe: a safety check refused the repository (foreign owner,
	// redirected work tree, absurd configuration).
	KindUnsafe
	// KindTooLarge: captured output exceeded its cap.
	KindTooLarge
	// KindNoGit: no usable git binary (missing, or older than MinGitVersion).
	KindNoGit
	// KindInvalid: the caller passed an argument we refuse to hand to git.
	KindInvalid
)

func (k Kind) String() string {
	switch k {
	case KindNotARepo:
		return "not a git repository"
	case KindConflict:
		return "conflict"
	case KindDirty:
		return "dirty working tree"
	case KindNotFound:
		return "not found"
	case KindTimeout:
		return "timeout"
	case KindCanceled:
		return "canceled"
	case KindLocked:
		return "locked"
	case KindUnsafe:
		return "unsafe repository"
	case KindTooLarge:
		return "output too large"
	case KindNoGit:
		return "git unavailable"
	case KindInvalid:
		return "invalid argument"
	}
	return "git error"
}

// Sentinels for errors.Is: errors.Is(err, gitx.ErrConflict) matches any *Error
// of that kind.
var (
	ErrNotARepo = &Error{Kind: KindNotARepo}
	ErrConflict = &Error{Kind: KindConflict}
	ErrDirty    = &Error{Kind: KindDirty}
	ErrNotFound = &Error{Kind: KindNotFound}
	ErrTimeout  = &Error{Kind: KindTimeout}
	ErrCanceled = &Error{Kind: KindCanceled}
	ErrLocked   = &Error{Kind: KindLocked}
	ErrUnsafe   = &Error{Kind: KindUnsafe}
	ErrTooLarge = &Error{Kind: KindTooLarge}
	ErrNoGit    = &Error{Kind: KindNoGit}
	ErrInvalid  = &Error{Kind: KindInvalid}
)

// Error is the one error type gitx returns for git-level failures.
type Error struct {
	Kind Kind
	// Op is the git subcommand ("status", "worktree add") or the helper that
	// failed.
	Op string
	// ExitCode is git's exit status, or -1 when it did not exit normally.
	ExitCode int
	// Stderr is git's (capped) standard error, trimmed.
	Stderr string
	// Detail is a short human explanation added by gitx when stderr alone is not
	// enough.
	Detail string
	// Err is the underlying cause (context.DeadlineExceeded, an I/O error).
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("gitx")
	if e.Op != "" {
		b.WriteString(": ")
		b.WriteString(e.Op)
	}
	b.WriteString(": ")
	b.WriteString(e.Kind.String())
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if s := firstLines(e.Stderr, 3); s != "" {
		b.WriteString(": ")
		b.WriteString(s)
	}
	if e.Err != nil && e.Detail == "" && e.Stderr == "" {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap exposes the cause, so errors.Is(err, context.DeadlineExceeded) works
// for timeouts.
func (e *Error) Unwrap() error { return e.Err }

// Is makes the sentinel comparison kind-based.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return t.Kind == e.Kind && t.Op == "" && t.Stderr == "" && t.Detail == "" && t.Err == nil && t.ExitCode == 0
}

// KindOf returns the kind of a gitx error (KindOther for anything else, and for
// nil).
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindOther
}

func newErr(kind Kind, op, format string, args ...any) *Error {
	return &Error{Kind: kind, Op: op, ExitCode: -1, Detail: fmt.Sprintf(format, args...)}
}

// firstLines returns up to n non-empty lines of s joined by "; ".
func firstLines(s string, n int) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
		if len(out) == n {
			break
		}
	}
	return strings.Join(out, "; ")
}

// classify maps git's failure (exit status plus stderr text) to a Kind. The text
// is stable because every invocation runs with LC_ALL=C.
func classify(stderr string) Kind {
	l := strings.ToLower(stderr)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(l, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("not a git repository"):
		return KindNotARepo
	case has("detected dubious ownership", "unsafe repository"):
		return KindUnsafe
	case has("unable to create", "cannot lock ref", "another git process seems to be running", "could not lock config file", "unable to lock"):
		if has(".lock", "another git process", "lock") {
			return KindLocked
		}
	}
	switch {
	case has("this operation must be run in a work tree", "must be run in a work tree"):
		return KindNotARepo
	case has("would be overwritten", "please commit your changes or stash them",
		"you have unstaged changes", "your index contains uncommitted changes",
		"contains modified or untracked files", "is dirty", "local changes"):
		return KindDirty
	case has("automatic merge failed", "could not apply", "patch does not apply", "patch failed",
		"conflict (", "merge conflict", "fix conflicts", "unmerged files", "needs merge",
		"does not match index", "already exists in working directory"):
		return KindConflict
	case has("unknown revision", "bad revision", "not a valid object name", "needed a single revision",
		"bad object", "invalid reference", "not a valid ref", "no such ref", "does not exist",
		"did not match any", "exists on disk, but not in", "path '", "unknown commit", "couldn't find remote ref",
		"not found", "no such file or directory"):
		return KindNotFound
	}
	return KindOther
}
