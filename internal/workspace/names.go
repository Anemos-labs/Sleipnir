package workspace

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// Sentinel errors. They are wrapped with detail, so use errors.Is.
var (
	// ErrBadName: an agent id, prefix or branch name failed validation.
	ErrBadName = errors.New("workspace: invalid name")
	// ErrExists: a tree (or branch) with that name already exists.
	ErrExists = errors.New("workspace: already exists")
	// ErrOutsideDir: a path is not strictly inside Manager.Dir, or reaches there
	// through a symbolic link.
	ErrOutsideDir = errors.New("workspace: path is outside the workspace directory")
	// ErrForeign: the tree or branch was not created by this manager (no marker, or
	// a marker for another prefix); it is never touched.
	ErrForeign = errors.New("workspace: not created by this manager")
	// ErrDirty: the tree has uncommitted changes and force was not given.
	ErrDirty = errors.New("workspace: tree has uncommitted changes")
	// ErrUnmerged: the tree's branch has commits that exist nowhere else and force
	// was not given.
	ErrUnmerged = errors.New("workspace: branch has commits that are not merged anywhere else")
	// ErrRemoved: the tree was removed.
	ErrRemoved = errors.New("workspace: tree was removed")
	// ErrBroken: the merge queue could not restore its integration tree and refuses
	// further work until repaired.
	ErrBroken = errors.New("workspace: merge queue is broken")
	// ErrClosed: the queue or manager was closed.
	ErrClosed = errors.New("workspace: closed")
	// ErrNoCommits: the repository has no commit to base trees on.
	ErrNoCommits = errors.New("workspace: repository has no commits")
	// ErrNotFastForward: the user's branch cannot be fast-forwarded to the
	// integration tip.
	ErrNotFastForward = errors.New("workspace: not a fast-forward")
	// ErrTooLarge: a file or a patch exceeds its limit.
	ErrTooLarge = errors.New("workspace: too large")
	// ErrUnsupported: the operation does not apply to this kind of workspace.
	ErrUnsupported = errors.New("workspace: unsupported")
	// ErrNestedRepo: a directory in the tree is a git repository of its own, which
	// cannot be recorded (git would store a bare pointer to a commit that exists
	// nowhere the project can get it).
	ErrNestedRepo = errors.New("workspace: nested git repository")
)

var (
	agentIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	// component of a branch prefix
	prefixPartRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
)

// reservedNames are device names Windows refuses as file names in any case.
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateAgentID reports whether id is safe to use as a directory name, a branch
// component and a log token: ASCII letters, digits, '.', '_' and '-', starting
// with a letter or digit, at most 63 bytes, no "..", no ".lock" suffix. Names that
// start with '_' are reserved for the manager's own trees.
func ValidateAgentID(id string) error {
	if err := validateName(id); err != nil {
		return fmt.Errorf("%w: agent id %q: %s", ErrBadName, printable(id), err)
	}
	return nil
}

func validateName(s string) error {
	switch {
	case !agentIDRe.MatchString(s):
		return errors.New("must match [A-Za-z0-9][A-Za-z0-9._-]{0,62}")
	case strings.Contains(s, ".."):
		return errors.New("must not contain \"..\"")
	case strings.HasSuffix(s, ".") || strings.HasSuffix(strings.ToLower(s), ".lock"):
		return errors.New("must not end in '.' or '.lock'")
	}
	stem := strings.ToLower(s)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if reservedNames[stem] {
		return errors.New("is a reserved device name")
	}
	return nil
}

// ValidatePrefix reports whether p is usable as the branch namespace: one or more
// '/'-separated components, each a valid name, for example "sleipnir" or
// "sleipnir/s-4f2a".
func ValidatePrefix(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty branch prefix", ErrBadName)
	}
	if len(p) > 120 {
		return fmt.Errorf("%w: branch prefix is too long", ErrBadName)
	}
	for _, part := range strings.Split(p, "/") {
		if !prefixPartRe.MatchString(part) {
			return fmt.Errorf("%w: branch prefix %q has an invalid component %q", ErrBadName, printable(p), printable(part))
		}
		if strings.Contains(part, "..") || strings.HasSuffix(part, ".") || strings.HasSuffix(strings.ToLower(part), ".lock") {
			return fmt.Errorf("%w: branch prefix component %q is not allowed", ErrBadName, part)
		}
	}
	// git's own rules are the final word.
	if err := gitx.ValidateBranchName(p + "/x"); err != nil {
		return fmt.Errorf("%w: %v", ErrBadName, err)
	}
	return nil
}

// printable makes a name safe to embed in an error message.
func printable(s string) string {
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// Reserved internal names. Agent ids cannot start with '_', so these can never
// collide with an agent's tree or branch.
const (
	integrationName = "_integration"
	shadowName      = "_shadow"
	baseBranchName  = "_base"
)
