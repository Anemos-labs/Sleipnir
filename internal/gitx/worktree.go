package gitx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	Path     string
	Head     string
	Branch   string // short name; "" when detached
	Detached bool
	Bare     bool
	Locked   bool
	// Prunable is set when git considers the entry stale (its directory is gone).
	Prunable       bool
	PrunableReason string
}

// Worktrees lists the repository's worktrees, main one first.
func (r *Repo) Worktrees(ctx context.Context) ([]Worktree, error) {
	out, err := r.run(ctx, call{args: []string{"worktree", "list", "--porcelain", "-z"}})
	if err != nil {
		return nil, err
	}
	var list []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil && cur.Path != "" {
			list = append(list, *cur)
		}
		cur = nil
	}
	for _, tok := range strings.Split(out.text(), "\x00") {
		if tok == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(tok, " ")
		if key == "worktree" {
			flush()
			cur = &Worktree{Path: val}
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable, cur.PrunableReason = true, val
		}
	}
	flush()
	return list, nil
}

// WorktreeAddOptions describes a new linked worktree.
type WorktreeAddOptions struct {
	// Path is the absolute directory to create.
	Path string
	// Branch names a new branch to create and check out ("" with Detach set for a
	// detached HEAD).
	Branch string
	// Commit is what the worktree starts from (branch, tag or commit id).
	Commit string
	Detach bool
	// NoCheckout skips populating files: the caller fills the tree (sparse
	// checkout, copy-on-write population).
	NoCheckout bool
	// Force allows a path git considers registered-but-missing.
	Force bool
}

// WorktreeAdd creates a linked worktree. The worktree shares the object store and
// refs with the repository, which is what makes creating one per agent cheap.
func (r *Repo) WorktreeAdd(ctx context.Context, o WorktreeAddOptions) error {
	if !filepath.IsAbs(o.Path) {
		return newErr(KindInvalid, "worktree add", "path %q must be absolute", o.Path)
	}
	if strings.ContainsAny(o.Path, "\x00\n") {
		return newErr(KindInvalid, "worktree add", "path contains a control character")
	}
	if err := validateRev("worktree add", o.Commit); err != nil {
		return err
	}
	args := []string{"worktree", "add", "--quiet"}
	if o.NoCheckout {
		args = append(args, "--no-checkout")
	}
	if o.Force {
		args = append(args, "--force")
	}
	switch {
	case o.Branch != "" && !o.Detach:
		if err := ValidateBranchName(o.Branch); err != nil {
			return err
		}
		args = append(args, "-b", o.Branch)
	case o.Detach:
		args = append(args, "--detach")
	default:
		return newErr(KindInvalid, "worktree add", "either Branch or Detach is required")
	}
	args = append(args, "--", o.Path, o.Commit)
	_, err := r.run(ctx, call{args: args, mutating: true, timeout: 10 * r.s.timeout})
	return err
}

// WorktreeRemove deletes a linked worktree's directory and its administrative
// entry. Without force, git refuses when the tree has uncommitted changes or
// untracked files; with force it removes them.
func (r *Repo) WorktreeRemove(ctx context.Context, path string, force bool) error {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\n") {
		return newErr(KindInvalid, "worktree remove", "path %q must be absolute", path)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", path)
	_, err := r.run(ctx, call{args: args, mutating: true})
	return err
}

// ValidateBranchName checks a branch name against git's ref rules plus our own
// stricter ones (printable ASCII only, no leading '-', no control characters), so
// a name is safe to put on a command line, in a path and in a log.
func ValidateBranchName(name string) error {
	if name == "" || len(name) > 200 {
		return newErr(KindInvalid, "branch", "invalid branch name length")
	}
	if strings.HasPrefix(name, "-") {
		return newErr(KindInvalid, "branch", "branch name %q starts with '-'", name)
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") {
		return newErr(KindInvalid, "branch", "branch name %q has an empty component", name)
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") || name == "@" {
		return newErr(KindInvalid, "branch", "branch name %q contains a forbidden sequence", name)
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return newErr(KindInvalid, "branch", "branch name %q has a forbidden suffix", name)
	}
	for _, c := range []byte(name) {
		if c < 0x21 || c > 0x7e || strings.IndexByte("~^:?*[\\", c) >= 0 {
			return newErr(KindInvalid, "branch", "branch name %q contains a forbidden character", name)
		}
	}
	for _, comp := range strings.Split(name, "/") {
		if strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") {
			return newErr(KindInvalid, "branch", "branch name component %q is forbidden", comp)
		}
	}
	return nil
}

// Ref is a branch with the commit it points at.
type Ref struct {
	Name string // short name, without refs/heads/
	SHA  string
}

// Branches lists local branches whose names start with prefix (which must end in
// '/' or be empty), sorted by name.
func (r *Repo) Branches(ctx context.Context, prefix string) ([]Ref, error) {
	if prefix != "" {
		if err := ValidateBranchName(strings.TrimSuffix(prefix, "/")); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
	}
	out, err := r.run(ctx, call{args: []string{"for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads/" + prefix}})
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(out.text(), "\n") {
		name, sha, ok := strings.Cut(line, "\x00")
		if !ok {
			continue
		}
		refs = append(refs, Ref{Name: strings.TrimPrefix(name, "refs/heads/"), SHA: sha})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

// BranchSHA returns the commit a local branch points at, or ErrNotFound.
func (r *Repo) BranchSHA(ctx context.Context, name string) (string, error) {
	if err := ValidateBranchName(name); err != nil {
		return "", err
	}
	out, err := r.run(ctx, call{args: []string{"rev-parse", "--verify", "--quiet", "refs/heads/" + name + "^{commit}"}, okExit: []int{1}})
	if err != nil {
		return "", err
	}
	if out.exit != 0 {
		return "", notFound("rev-parse", fmt.Sprintf("no branch %q", name))
	}
	return out.trimmed(), nil
}

// CreateBranch creates a branch at sha. It fails if the branch exists.
func (r *Repo) CreateBranch(ctx context.Context, name, sha string) error {
	if err := ValidateBranchName(name); err != nil {
		return err
	}
	if err := validateRev("branch", sha); err != nil {
		return err
	}
	_, err := r.run(ctx, call{args: []string{"branch", "--no-track", "--", name, sha}, mutating: true})
	return err
}

// DeleteBranch deletes a local branch. git refuses to delete a branch that is
// checked out in any worktree, which is the safety we want.
func (r *Repo) DeleteBranch(ctx context.Context, name string) error {
	if err := ValidateBranchName(name); err != nil {
		return err
	}
	_, err := r.run(ctx, call{args: []string{"branch", "-D", "--", name}, mutating: true})
	return err
}

// UpdateBranch moves a branch to sha only if it currently points at old (an empty
// old means "must not exist yet"). The compare-and-swap is what lets the merge
// queue advance the integration branch without trusting that nobody else did.
func (r *Repo) UpdateBranch(ctx context.Context, name, sha, old, reason string) error {
	if err := ValidateBranchName(name); err != nil {
		return err
	}
	for _, s := range []string{sha, old} {
		if s != "" {
			if err := validateRev("update-ref", s); err != nil {
				return err
			}
		}
	}
	zero := strings.Repeat("0", len(sha))
	if old == "" {
		old = zero
	}
	args := []string{"update-ref"}
	if reason != "" {
		args = append(args, "-m", strings.ReplaceAll(reason, "\n", " "))
	}
	args = append(args, "refs/heads/"+name, sha, old)
	_, err := r.run(ctx, call{args: args, mutating: true})
	return err
}

// Init creates a repository at dir (which is created if needed) and returns a
// handle. Templates are disabled (no sample hooks, no template config) and the
// initial branch is "main". Used for the private repository behind copy-mode
// workspaces.
func Init(ctx context.Context, dir string, bare bool, opts ...Option) (*Repo, error) {
	s, err := newSettings(opts)
	if err != nil {
		return nil, err
	}
	return initRepo(ctx, s, dir, bare)
}

func initRepo(ctx context.Context, s settings, dir string, bare bool) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, &Error{Kind: KindInvalid, Op: "init", ExitCode: -1, Err: err}
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, &Error{Kind: KindOther, Op: "init", ExitCode: -1, Err: err}
	}
	args := []string{"init", "--quiet", "--initial-branch=main", "--template="}
	if bare {
		args = append(args, "--bare")
	}
	args = append(args, "--", abs)
	if _, err := s.exec(ctx, call{dir: abs, args: args, mutating: true}); err != nil {
		return nil, err
	}
	return open(ctx, s, abs)
}

// Init creates a new repository at dir with this handle's settings (timeouts,
// hermetic configuration, trusted filters); see the package-level Init.
func (r *Repo) Init(ctx context.Context, dir string, bare bool) (*Repo, error) {
	return initRepo(ctx, r.s, dir, bare)
}
