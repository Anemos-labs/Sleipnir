package env

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Git runs the git binary with a hardened, deterministic configuration. It is
// the only way this package (and the task generators) talk to git.
//
// Why hardening matters: the repositories and workspaces git is pointed at are
// attacker-influenced. A repository's own configuration can name programs git
// will run (core.fsmonitor, core.hooksPath, filter and diff drivers), and an
// agent controls the .git directory of its workspace. Every invocation
// therefore ignores system and user configuration, overrides the dangerous keys
// on the command line (which outranks repository configuration), and starts
// from an explicit environment instead of the harness's.
type Git struct {
	bin     string
	scratch string
	owned   bool // scratch was created by NewGit, so Close removes it
	pass    []string
	base    []string

	versionOnce sync.Once
	version     string
}

// GitOptions configures NewGit.
type GitOptions struct {
	// Bin is the git executable; "" looks it up on PATH.
	Bin string
	// Scratch is a directory git may use as HOME (created if missing).
	Scratch string
	// PassEnv lists environment variables to hand to git untouched (names or
	// globs), for credentials the operator wants fetches of private repositories
	// to use: SSH_AUTH_SOCK, GIT_ASKPASS, GIT_SSH_COMMAND.
	PassEnv []string
	// Base is the environment PassEnv draws from; nil means os.Environ().
	Base []string
}

// NewGit locates git.
func NewGit(o GitOptions) (*Git, error) {
	bin := o.Bin
	if bin == "" {
		p, err := exec.LookPath("git")
		if err != nil {
			return nil, fmt.Errorf("git is required but was not found on PATH: %w", err)
		}
		bin = p
	}
	scratch, owned := o.Scratch, false
	if scratch == "" {
		d, err := os.MkdirTemp("", "sleipnir-git-")
		if err != nil {
			return nil, err
		}
		scratch, owned = d, true
	} else if err := os.MkdirAll(scratch, 0o700); err != nil {
		return nil, err
	}
	return &Git{bin: bin, scratch: scratch, owned: owned, pass: o.PassEnv, base: o.Base}, nil
}

// Close removes the scratch directory NewGit made when GitOptions.Scratch was empty;
// a directory the caller named is theirs and is left alone. It is safe to call on a nil
// Git and more than once, and git must not be run through g afterwards.
func (g *Git) Close() error {
	if g == nil || !g.owned {
		return nil
	}
	g.owned = false
	return os.RemoveAll(g.scratch)
}

// safeConfig overrides every configuration key through which git can be made to
// run a program or behave differently from run to run.
var safeConfig = []string{
	"core.hooksPath=/dev/null",
	"core.fsmonitor=false",
	"core.untrackedCache=false",
	"core.quotePath=false",
	"core.autocrlf=false",
	"core.safecrlf=false",
	"core.symlinks=true",
	"core.pager=cat",
	"core.editor=true",
	"core.askPass=",
	"core.sshCommand=ssh",
	"core.gitProxy=",
	"credential.helper=",
	"diff.external=",
	"diff.renames=false",
	"diff.noprefix=false",
	"diff.mnemonicPrefix=false",
	"color.ui=false",
	"commit.gpgSign=false",
	"tag.gpgSign=false",
	"gc.auto=0",
	"maintenance.auto=false",
	"advice.detachedHead=false",
	"protocol.ext.allow=never",
	"safe.directory=*",
	"log.showSignature=false",
	"i18n.logOutputEncoding=UTF-8",
}

// GitError is a failed git invocation.
type GitError struct {
	Args   []string
	Code   int
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	args := e.Args
	if len(args) > 8 {
		args = append(append([]string{}, args[:8]...), "...")
	}
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 600 {
		msg = "..." + msg[len(msg)-600:]
	}
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(args, " "), msg)
}

func (e *GitError) Unwrap() error { return e.Err }

// ErrOutputLimit is returned when a command's output exceeds the caller's limit.
var ErrOutputLimit = errors.New("output limit exceeded")

// Run runs git in dir (which may be "") and returns its stdout.
func (g *Git) Run(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	return g.RunEnv(ctx, dir, nil, stdin, 256<<20, args...)
}

// RunEnv is Run with extra environment entries ("K=V", they win) and an output
// limit; exceeding the limit kills git and returns ErrOutputLimit.
func (g *Git) RunEnv(ctx context.Context, dir string, extraEnv []string, stdin io.Reader, maxOut int64, args ...string) ([]byte, error) {
	var out bytes.Buffer
	if err := g.Stream(ctx, dir, extraEnv, stdin, &out, maxOut, args...); err != nil {
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// Stream is RunEnv writing stdout to w instead of buffering it.
func (g *Git) Stream(ctx context.Context, dir string, extraEnv []string, stdin io.Reader, w io.Writer, maxOut int64, args ...string) error {
	full := make([]string, 0, 2*len(safeConfig)+len(args))
	for _, c := range safeConfig {
		full = append(full, "-c", c)
	}
	full = append(full, args...)
	cmd := exec.Command(g.bin, full...)
	cmd.Dir = dir
	cmd.Env = g.environ(extraEnv)
	cmd.Stdin = stdin
	lw := &limitWriter{w: w, max: maxOut}
	var stderr tailBuffer
	stderr.max = 8 << 10
	cmd.Stdout = lw
	cmd.Stderr = &stderr
	configureProc(cmd)
	if err := cmd.Start(); err != nil {
		return &GitError{Args: args, Code: -1, Err: err}
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-ctx.Done():
		killGroup(pid)
		<-done
		return ctx.Err()
	}
	// git may leave helpers behind (ssh, credential programs): they die with it.
	killGroup(pid)
	if lw.exceeded {
		return ErrOutputLimit
	}
	if werr != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			code = ee.ExitCode()
		}
		return &GitError{Args: args, Code: code, Stderr: stderr.String(), Err: werr}
	}
	return nil
}

// environ builds git's environment from scratch.
func (g *Git) environ(extra []string) []string {
	env := map[string]string{
		"HOME":                g.scratch,
		"XDG_CONFIG_HOME":     filepath.Join(g.scratch, "xdg"),
		"PATH":                sysPath(),
		"LC_ALL":              "C",
		"LANG":                "C",
		"TZ":                  "UTC",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_OPTIONAL_LOCKS":  "0",
		"GIT_PAGER":           "cat",
		"GIT_ALLOW_PROTOCOL":  "file:https:http:ssh:git",
		"GIT_ATTR_NOSYSTEM":   "1",
		"GIT_AUTHOR_NAME":     "sleipnir", "GIT_AUTHOR_EMAIL": "sleipnir@localhost",
		"GIT_COMMITTER_NAME": "sleipnir", "GIT_COMMITTER_EMAIL": "sleipnir@localhost",
	}
	base := g.base
	if base == nil && len(g.pass) > 0 {
		base = os.Environ()
	}
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, pat := range g.pass {
			if m, _ := filepath.Match(pat, k); m {
				env[k] = v
			}
		}
	}
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	list := make([]string, 0, len(env))
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	return list
}

// sysPath is the fixed PATH git runs with: it needs to find its own helpers and
// sh, nothing else.
func sysPath() string {
	var dirs []string
	for _, d := range []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin", "/opt/homebrew/bin"} {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// Version returns "git version x.y.z" (or "" if it cannot be determined).
func (g *Git) Version(ctx context.Context) string {
	g.versionOnce.Do(func() {
		out, err := g.Run(ctx, "", nil, "--version")
		if err == nil {
			g.version = strings.TrimSpace(string(out))
		}
	})
	return g.version
}

// Bin returns the git executable in use.
func (g *Git) Bin() string { return g.bin }

// limitWriter forwards up to max bytes and then refuses more, which makes
// os/exec's copy goroutine fail; the caller notices exceeded and kills git.
type limitWriter struct {
	w        io.Writer
	max      int64
	n        int64
	exceeded bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.max > 0 && l.n+int64(len(p)) > l.max {
		l.exceeded = true
		return 0, ErrOutputLimit
	}
	l.n += int64(len(p))
	return l.w.Write(p)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.ToValidUTF8(string(t.buf), "?") }

// ---- helpers over Git ----

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// isSHA reports whether s is a full object id.
func isSHA(s string) bool { return shaRe.MatchString(s) }

// ResolveCommit resolves rev to a full commit id in the repository at gitDir
// (a .git directory or a bare repository).
func (g *Git) ResolveCommit(ctx context.Context, gitDir, rev string) (string, error) {
	out, err := g.Run(ctx, "", nil, "--git-dir="+gitDir, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if !isSHA(sha) {
		return "", fmt.Errorf("git rev-parse returned %q", sha)
	}
	return sha, nil
}

// GitDirOf returns the git directory of a repository rooted at dir (a work
// tree with .git, or a bare repository) and whether dir is such a root. A
// subdirectory of a repository is not a root: treating it as one would export
// the whole enclosing repository.
func (g *Git) GitDirOf(ctx context.Context, dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	ceil := "GIT_CEILING_DIRECTORIES=" + filepath.Dir(real)
	out, err := g.RunEnv(ctx, real, []string{ceil}, nil, 1<<16, "rev-parse", "--absolute-git-dir", "--is-bare-repository", "--show-toplevel")
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if err != nil && len(lines) < 2 {
		return "", false
	}
	if len(lines) < 2 {
		return "", false
	}
	gitDir, bare := lines[0], lines[1]
	if bare == "true" {
		return gitDir, true
	}
	if len(lines) >= 3 {
		top, err := filepath.EvalSymlinks(lines[2])
		if err == nil && top == real {
			return gitDir, true
		}
	}
	return "", false
}

// init sets up a bare repository used only as an object store. --template= keeps
// the operator's template directory (and its hooks) out of it.
func (g *Git) initBare(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_, err := g.Run(ctx, "", nil, "init", "--quiet", "--bare", "--template=", dir)
	return err
}
