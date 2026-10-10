package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Repo is a handle on one repository work tree (or a bare repository). It is
// immutable after Open and safe for concurrent use; every method runs git
// through the hardened runner (run.go).
type Repo struct {
	s         settings
	root      string // canonical work tree root; the git directory for a bare repository
	gitDir    string // absolute; per-worktree for a linked worktree
	commonDir string // absolute; shared by all worktrees of the repository
	bare      bool
}

// Open returns a handle for the repository containing path. path may be any
// directory (or file) inside the work tree.
//
// The work tree is taken to be the directory that holds .git, found by walking
// up from path, and every later command is pinned to it with --work-tree. That is
// what makes a hostile `core.worktree = /home/victim` in .git/config harmless: git
// would otherwise honor it and run `add -A`, `reset --hard` or `clean` over that
// directory.
func Open(path string, opts ...Option) (*Repo, error) {
	s, err := newSettings(opts)
	if err != nil {
		return nil, err
	}
	return open(context.Background(), s, path)
}

func open(ctx context.Context, s settings, path string) (*Repo, error) {
	if path == "" {
		return nil, newErr(KindInvalid, "open", "empty path")
	}
	if strings.ContainsRune(path, 0) {
		return nil, newErr(KindInvalid, "open", "path contains a NUL byte")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, &Error{Kind: KindInvalid, Op: "open", ExitCode: -1, Err: err}
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &Error{Kind: KindNotFound, Op: "open", ExitCode: -1, Detail: "no such directory: " + path, Err: err}
		}
		return nil, &Error{Kind: KindOther, Op: "open", ExitCode: -1, Err: err}
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, &Error{Kind: KindNotFound, Op: "open", ExitCode: -1, Err: err}
	}
	start := real
	if !fi.IsDir() {
		start = filepath.Dir(real)
	}

	if root, ok := findWorkTreeRoot(start); ok {
		info, err := s.revParse(ctx, root, root)
		if err != nil {
			return nil, err
		}
		if info.bare {
			// core.bare=true in a directory that has .git: honor git's view; there
			// is no work tree to pin.
			return &Repo{s: s, root: info.gitDir, gitDir: info.gitDir, commonDir: info.commonDir, bare: true}, nil
		}
		return &Repo{s: s, root: root, gitDir: info.gitDir, commonDir: info.commonDir}, nil
	}
	// No .git on the way up: a bare repository is the one remaining shape.
	info, err := s.revParse(ctx, start, "")
	if err != nil {
		return nil, err
	}
	if !info.bare {
		return nil, &Error{Kind: KindNotARepo, Op: "open", ExitCode: -1, Detail: path + " is not inside a git work tree"}
	}
	return &Repo{s: s, root: info.gitDir, gitDir: info.gitDir, commonDir: info.commonDir, bare: true}, nil
}

// IsRepo reports whether path is inside a git work tree (or is a bare
// repository), without keeping a handle. It never runs anything the repository
// controls: it is the same hardened Open.
func IsRepo(path string, opts ...Option) bool {
	_, err := Open(path, opts...)
	return err == nil
}

// findWorkTreeRoot walks up from dir to the first directory holding a .git entry
// (directory, or the file worktrees and submodules use).
func findWorkTreeRoot(dir string) (string, bool) {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

type revInfo struct {
	gitDir, commonDir string
	bare              bool
}

// revParse asks git where its directories are. workTree pins the work tree
// ("" for bare probing).
func (s *settings) revParse(ctx context.Context, dir, workTree string) (revInfo, error) {
	out, err := s.exec(ctx, call{
		dir: dir, workTree: workTree,
		args: []string{"rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--is-bare-repository"},
	})
	if err != nil {
		return revInfo{}, err
	}
	lines := strings.Split(strings.TrimRight(out.text(), "\n"), "\n")
	if len(lines) < 3 {
		return revInfo{}, &Error{Kind: KindOther, Op: "rev-parse", ExitCode: out.exit, Detail: "unexpected output: " + firstLines(out.text(), 3)}
	}
	canon := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return revInfo{gitDir: canon(lines[0]), commonDir: canon(lines[1]), bare: strings.TrimSpace(lines[2]) == "true"}, nil
}

// Root is the canonical work tree root (for a bare repository, its git
// directory).
func (r *Repo) Root() string { return r.root }

// GitDir is the repository's git directory; for a linked worktree it is the
// private administrative directory under the main repository's worktrees/.
func (r *Repo) GitDir() string { return r.gitDir }

// CommonDir is the git directory shared by every worktree of the repository
// (objects, refs, config live there).
func (r *Repo) CommonDir() string { return r.commonDir }

// IsBare reports whether the repository has no work tree.
func (r *Repo) IsBare() bool { return r.bare }

// IsLinked reports whether the handle is a linked worktree (not the main one).
func (r *Repo) IsLinked() bool { return r.gitDir != r.commonDir }

// Reopen returns a handle for another directory with the same settings (an agent
// worktree, the integration tree).
func (r *Repo) Reopen(ctx context.Context, path string) (*Repo, error) {
	return open(ctx, r.s, path)
}

// batch is a set of commands that share one scan of the configuration's drivers:
// one high-level operation (snapshot, commit, merge) runs several git commands,
// and scanning before each would triple the process count for nothing.
type batch struct {
	r  *Repo
	ov []kv
}

// begin scans the configuration once for the driver neutralizations.
func (r *Repo) begin(ctx context.Context) (*batch, error) {
	ov, err := r.s.driverOverrides(ctx, r.runDir(), r.pin(), r.gitDir)
	if err != nil {
		return nil, err
	}
	return &batch{r: r, ov: ov}, nil
}

// runDir returns the repository root used as the working directory for Git commands.
func (r *Repo) runDir() string { return r.root }

// pin returns the worktree root for non-bare repositories and no pinned directory for bare
// repositories.
func (r *Repo) pin() string {
	if r.bare {
		return ""
	}
	return r.root
}

// run executes c inside the batch.
func (b *batch) run(ctx context.Context, c call) (*output, error) {
	if c.dir == "" {
		c.dir = b.r.runDir()
	}
	if c.workTree == "" {
		c.workTree = b.r.pin()
	}
	if c.gitDir == "" {
		c.gitDir = b.r.gitDir
	}
	c.overrides = append(append([]kv(nil), b.ov...), c.overrides...)
	return b.r.s.exec(ctx, c)
}

// pureCommands only read or write objects and refs; they never consult
// attributes, so they skip the driver scan.
var pureCommands = map[string]bool{
	"rev-parse": true, "rev-list": true, "cat-file": true, "merge-base": true,
	"for-each-ref": true, "show-ref": true, "update-ref": true, "symbolic-ref": true,
	"ls-tree": true, "commit-tree": true, "write-tree": true, "mktree": true,
	"check-ref-format": true, "merge-file": true, "count-objects": true, "diff-tree": true,
	"ls-files": true, "config": true,
}

// run executes a single command, scanning drivers first unless the command is
// object-only.
func (r *Repo) run(ctx context.Context, c call) (*output, error) {
	if c.dir == "" {
		c.dir = r.runDir()
	}
	if c.workTree == "" {
		c.workTree = r.pin()
	}
	if c.gitDir == "" {
		c.gitDir = r.gitDir
	}
	if len(c.args) > 0 && !pureCommands[c.args[0]] && c.overrides == nil {
		ov, err := r.s.driverOverrides(ctx, c.dir, c.workTree, c.gitDir)
		if err != nil {
			return nil, err
		}
		c.overrides = ov
	}
	return r.s.exec(ctx, c)
}

// allowedSubcommands is what Git accepts. Network commands (fetch, pull, push,
// clone, remote, submodule), config writes, gc, daemons and commands that write
// files of their own choosing into the current directory (format-patch) are absent
// on purpose.
var allowedSubcommands = map[string]bool{
	"add": true, "apply": true, "blame": true, "branch": true, "cat-file": true, "checkout": true, "cherry-pick": true,
	"clean": true, "commit": true, "commit-tree": true, "diff": true, "diff-files": true, "diff-index": true,
	"diff-tree": true, "for-each-ref": true, "log": true, "ls-files": true,
	"ls-tree": true, "merge": true, "merge-base": true, "merge-file": true, "merge-tree": true,
	"mktree": true, "mv": true, "read-tree": true, "rebase": true, "reset": true, "restore": true,
	"rev-list": true, "rev-parse": true, "revert": true, "rm": true, "show": true, "show-ref": true,
	"sparse-checkout": true, "status": true, "switch": true, "symbolic-ref": true, "update-index": true,
	"update-ref": true, "worktree": true, "write-tree": true,
}

// deniedOptions are, per subcommand, the long options that make git run a program
// named by the caller's argument, write to a path of the caller's choosing, or ask
// for signatures (which run the program a repository configures; the configuration
// is blanked, but the command line is the caller's word). The configuration cannot
// disarm an option, so Git refuses them. git accepts any unambiguous abbreviation
// of a long option, so an argument is refused when it is a prefix of a denied
// option (--exe, --out, ...); legitimate options that merely start alike
// (--oneline, --index, --verify) are not prefixes of anything on the list of their
// subcommand and pass.
var deniedOptions = map[string][]string{
	"rebase":      {"--exec", "--gpg-sign"},
	"merge":       {"--gpg-sign", "--verify-signatures"},
	"commit":      {"--gpg-sign"},
	"cherry-pick": {"--gpg-sign"},
	"revert":      {"--gpg-sign"},
	"apply":       {"--unsafe-paths"},
	"read-tree":   {"--index-output"},
	"diff":        diffLike,
	"diff-files":  diffLike,
	"diff-index":  diffLike,
	"diff-tree":   diffLike,
	"log":         diffLike,
	"show":        diffLike,
	"rev-list":    diffLike,
	"cat-file":    {"--textconv", "--filters"},
	// blame reads the file named by --contents, --ignore-revs-file and -S: paths of the
	// caller's choosing (BlameContents passes content on stdin itself).
	"blame": {"--contents", "--ignore-revs-file", "--textconv", "--output", "--ext-diff"},
}

var diffLike = []string{"--output", "--ext-diff", "--textconv", "--show-signature", "--open-files-in-pager"}

// checkGitArgs applies the option rules of Git.
func checkGitArgs(args []string) error {
	sub := args[0]
	denied := deniedOptions[sub]
	for _, a := range args[1:] {
		if a == "--" {
			return nil // what follows is paths
		}
		if strings.HasPrefix(a, "--") {
			name, _, _ := strings.Cut(a, "=")
			if len(name) <= 2 {
				continue
			}
			for _, d := range denied {
				if strings.HasPrefix(d, name) {
					return newErr(KindInvalid, sub, "option %q is not allowed", name)
				}
			}
			continue
		}
		// Short options: -x runs a command for every commit of a rebase, and -S signs
		// (runs a program) for the subcommands that create commits. They may be bundled
		// (-ix, -aS) or carry an attached value (-Skeyid).
		if len(a) > 1 && a[0] == '-' {
			if sub == "blame" && strings.ContainsRune(a, 'S') {
				return newErr(KindInvalid, sub, "option %q is not allowed", a)
			}
			switch sub {
			case "rebase":
				if strings.ContainsRune(a, 'x') {
					return newErr(KindInvalid, sub, "option %q is not allowed", a)
				}
				fallthrough
			case "commit", "merge", "cherry-pick", "revert":
				if strings.ContainsRune(a, 'S') {
					return newErr(KindInvalid, sub, "option %q is not allowed", a)
				}
			}
		}
	}
	return nil
}

// Result is the outcome of Git.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
}

// Git runs an allow-listed git subcommand through the hardened runner and
// returns its output. It is the escape hatch for callers that need a command
// gitx has no typed helper for; it cannot bypass the hardening, and it refuses
// options before the subcommand (-c, --exec-path, --git-dir ...), network
// commands, config writes and the options that run programs or write to paths of
// the caller's choosing (deniedOptions).
func (r *Repo) Git(ctx context.Context, args ...string) (*Result, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || !allowedSubcommands[args[0]] {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		return nil, newErr(KindInvalid, "git", "subcommand %q is not allowed", name)
	}
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return nil, newErr(KindInvalid, args[0], "argument contains a NUL byte")
		}
	}
	if err := checkGitArgs(args); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, call{args: args, mutating: true})
	res := &Result{}
	if out != nil {
		res = &Result{Stdout: out.text(), Stderr: out.stderr, ExitCode: out.exit, Truncated: out.truncated}
	}
	return res, err
}
