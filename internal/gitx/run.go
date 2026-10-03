package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults. They are deliberately finite: a stuck git (a lock held by a dead
// process, a hook we failed to disable, a filesystem that hangs) must cost one
// timeout, never a swarm.
const (
	DefaultTimeout   = 2 * time.Minute
	DefaultMaxOutput = 32 << 20
	maxStderrBytes   = 256 << 10
	// waitDelay bounds how long we wait for output pipes to close after the
	// process group has been told to stop: a grandchild that escaped the group and
	// still holds the pipe must not hang us.
	waitDelay = 2 * time.Second
	// termGrace is how long a command may take to die after SIGTERM before the
	// whole process group is sent SIGKILL (proc_unix.go; other platforms have no
	// such step). It must stay below waitDelay, the point at which the standard
	// library gives up on the leader on its own.
	termGrace = 1500 * time.Millisecond
	// DefaultLockWait is how long a command keeps retrying while another git process
	// holds a lock file or is creating a worktree.
	DefaultLockWait = 10 * time.Second
)

// settings is the immutable configuration of a runner.
type settings struct {
	git      string
	timeout  time.Duration
	maxOut   int64
	hermetic bool
	trusted  map[string]bool
	now      func() time.Time
	lockWait time.Duration
}

// Option configures Open, Init and friends.
type Option func(*settings)

// WithTimeout sets the default per-command timeout (the context deadline still
// applies on top).
func WithTimeout(d time.Duration) Option { return func(s *settings) { s.timeout = d } }

// WithMaxOutput caps the bytes captured from a command's stdout. Exceeding it is
// an error (KindTooLarge) except where a helper truncates on purpose (Diff).
func WithMaxOutput(n int64) Option { return func(s *settings) { s.maxOut = n } }

// WithGitPath uses this git binary instead of the one found on PATH. Tests use
// it to interpose a recording shim; operators can pin a vetted build.
func WithGitPath(p string) Option { return func(s *settings) { s.git = p } }

// WithHermeticConfig ignores the system and global git configuration (needs git
// >= 2.32; older gits ignore the request). Used by tests and by callers that want
// results independent of a developer's ~/.gitconfig. The dangerous keys are
// overridden either way; this only removes ambient preferences.
func WithHermeticConfig() Option { return func(s *settings) { s.hermetic = true } }

// WithTrustedFilters names filter drivers (for example "lfs") that may run even
// though they execute programs. By default none may: a driver name is chosen by
// the repository's .gitattributes, so trusting a name trusts whatever command the
// configuration binds to it. Only name drivers that your own global
// configuration defines.
func WithTrustedFilters(names ...string) Option {
	return func(s *settings) {
		if s.trusted == nil {
			s.trusted = map[string]bool{}
		}
		for _, n := range names {
			s.trusted[n] = true
		}
	}
}

// WithClock injects the time source used for commit dates (tests).
func WithClock(now func() time.Time) Option { return func(s *settings) { s.now = now } }

// WithLockWait sets how long commands retry on lock contention.
func WithLockWait(d time.Duration) Option { return func(s *settings) { s.lockWait = d } }

func newSettings(opts []Option) (settings, error) {
	s := settings{timeout: DefaultTimeout, maxOut: DefaultMaxOutput, now: time.Now, lockWait: DefaultLockWait}
	for _, o := range opts {
		o(&s)
	}
	if s.timeout <= 0 {
		s.timeout = DefaultTimeout
	}
	if s.maxOut <= 0 {
		s.maxOut = DefaultMaxOutput
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.lockWait < 0 {
		s.lockWait = 0
	}
	if s.git == "" {
		p, err := exec.LookPath("git")
		if err != nil {
			return s, &Error{Kind: KindNoGit, Op: "lookup", ExitCode: -1, Detail: "git executable not found on PATH", Err: err}
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		s.git = p
	}
	if err := checkVersion(s.git); err != nil {
		return s, err
	}
	return s, nil
}

// hardenedConfig is applied with -c to every invocation. Command-line
// configuration outranks every file (system, global, repository, worktree), so a
// hostile .git/config cannot undo these. Each entry names what would otherwise
// run or corrupt our parsing.
func hardenedConfig() []string {
	return []string{
		// Hooks are repository- or user-controlled programs; harness commits are
		// machine work and must not trigger them (pre-commit formatters, post-merge
		// installers, reference-transaction spies). /dev/null is not a directory, so
		// no hook is ever found.
		"core.hooksPath=" + os.DevNull,
		// core.fsmonitor may be a command run on every status/diff/add.
		"core.fsmonitor=false",
		// Network is never needed. Blocking every transport also disarms ext::
		// (arbitrary command) URLs and ssh/proxy helpers.
		"protocol.ext.allow=never",
		"protocol.allow=never",
		"credential.helper=",
		"core.askPass=",
		"core.sshCommand=",
		// Programs that run when git wants to show or edit text.
		"core.editor=:",
		"sequence.editor=:",
		"core.pager=cat",
		"diff.external=",
		// Signing runs a configured program (gpg.program, ssh-keygen, a wrapper
		// script) for every commit; machine commits are unsigned by design, and log
		// must not spawn a verifier per commit.
		"commit.gpgsign=false",
		"tag.gpgsign=false",
		"log.showSignature=false",
		"merge.verifySignatures=false",
		// ... and a signing or verification that is asked for anyway (a flag, or a
		// caller of Git) must not find a program named by the repository.
		"gpg.program=",
		"gpg.openpgp.program=",
		"gpg.x509.program=",
		"gpg.ssh.program=",
		"gpg.ssh.defaultKeyCommand=",
		// Background maintenance would outlive the command and touch files while the
		// caller is deleting or rewriting them.
		"gc.auto=0",
		"gc.autoDetach=false",
		"maintenance.auto=false",
		// Output must be identical on every machine: no color, no quoting of
		// non-ASCII names, UTF-8 messages, no advice text.
		"color.ui=false",
		"color.status=false",
		"color.diff=false",
		"color.branch=false",
		"color.interactive=false",
		"core.quotePath=false",
		"i18n.logOutputEncoding=UTF-8",
		"i18n.commitEncoding=UTF-8",
		"advice.detachedHead=false",
		"advice.addEmbeddedRepo=false",
		// Recursing into submodules can run `git submodule update`, which fetches
		// and runs update commands.
		"submodule.recurse=false",
		"status.submoduleSummary=false",
		"diff.submodule=short",
		// Patches we hand back must be byte-stable and applicable whatever the
		// user's diff preferences are (prefix style, rename detection, moved-line
		// coloring, submodule log format).
		"diff.noprefix=false",
		"diff.mnemonicPrefix=false",
		"diff.renames=false",
		"diff.colorMoved=no",
		// Behaviour switches that would make the same command do different things on
		// different machines or block on input.
		"merge.autoStash=false",
		"rebase.autoStash=false",
		"rebase.autoSquash=false",
		"rebase.updateRefs=false",
	}
}

// passEnv lists the only variables inherited from the caller. Everything else is
// dropped: unknown variables are how GIT_DIR, GIT_EXTERNAL_DIFF, GIT_SSH_COMMAND,
// GIT_PROXY_COMMAND, GIT_EXEC_PATH and friends would steer git, and how provider
// keys would reach a program we failed to disarm.
var passEnv = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"TMPDIR": true, "TEMP": true, "TMP": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true,
	// Windows needs these for any process to start.
	"SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
	"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true, "PROGRAMDATA": true,
}

// baseEnv is the scrubbed environment every invocation starts from.
func (s *settings) baseEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if passEnv[strings.ToUpper(name)] {
			env = append(env, kv)
		}
	}
	env = append(env,
		"LC_ALL=C", "LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:",
		"GIT_PAGER=cat", "GIT_MERGE_AUTOEDIT=no",
		"GIT_NO_REPLACE_OBJECTS=1",
	)
	if s.hermetic {
		env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	}
	return env
}

// kv is one configuration override delivered through GIT_CONFIG_COUNT.
type kv struct{ k, v string }

// call describes one git invocation.
type call struct {
	// dir is the working directory ("" leaves the caller's).
	dir string
	// workTree pins --work-tree; "" means no pin (bare repositories, or commands
	// that need no repository).
	workTree string
	// gitDir pins GIT_DIR: the repository is the one we opened, whatever the
	// directory's own .git says now.
	gitDir string
	// args is the subcommand and its arguments, without global options.
	args  []string
	stdin io.Reader
	// env are extra variables from the small set our own code sets
	// (GIT_INDEX_FILE, author and committer identity).
	env []string
	// okExit lists exit statuses that are results, not failures (for example 1
	// from `merge-base --is-ancestor`).
	okExit []int
	// maxOut overrides the stdout cap.
	maxOut int64
	// killOnCap stops the command when the cap is reached and reports the
	// truncated output instead of failing.
	killOnCap bool
	timeout   time.Duration
	// mutating commands are retried while a lock file is contended.
	mutating bool
	// overrides are the attribute-driver neutralizations (guard.go).
	overrides []kv
	// config are extra -c settings for this command only (merge.conflictStyle).
	config []string
}

// output is what a finished command produced.
type output struct {
	stdout    []byte
	stderr    string
	exit      int
	truncated bool
}

// text converts captured standard output to a string without trimming it.
func (o *output) text() string { return string(o.stdout) }

// trimmed returns captured standard output with leading and trailing whitespace removed.
func (o *output) trimmed() string { return strings.TrimSpace(string(o.stdout)) }

// capWriter keeps at most max bytes but always reports the full write, so the
// child never blocks on a full pipe (or dies of SIGPIPE) just because we stopped
// keeping its output.
type capWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	max    int64
	over   bool
	onOver func()
}

// Write retains output within a positive byte cap, invokes the overflow callback once under lock,
// and reports excess bytes consumed.
func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.max > 0 && int64(w.buf.Len())+int64(len(p)) > w.max {
		if room := w.max - int64(w.buf.Len()); room > 0 {
			w.buf.Write(p[:room])
		}
		if !w.over {
			w.over = true
			if w.onOver != nil {
				w.onOver()
			}
		}
		return len(p), nil
	}
	return w.buf.Write(p)
}

// bytes returns the retained output slice under the writer lock; it shares storage and should be
// read only after writes stop.
func (w *capWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Bytes()
}

// overflowed reports under the writer lock whether output exceeded the capture limit.
func (w *capWriter) overflowed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.over
}

// argv assembles the full command line: global options, hardening, subcommand.
func (s *settings) argv(c call) []string {
	args := []string{
		"--no-pager",
		"--no-optional-locks",
		// Replacement refs let a repository silently substitute objects: results
		// must describe the real history.
		"--no-replace-objects",
		// Pathspecs we pass are file names, never patterns.
		"--literal-pathspecs",
	}
	if c.workTree != "" {
		args = append(args, "--work-tree="+c.workTree)
	}
	for _, cfg := range hardenedConfig() {
		args = append(args, "-c", cfg)
	}
	for _, cfg := range c.config {
		args = append(args, "-c", cfg)
	}
	return append(args, c.args...)
}

// exec runs one git command under the hardening described in the package doc.
func (s *settings) exec(ctx context.Context, c call) (*output, error) {
	if len(c.args) == 0 {
		return nil, newErr(KindInvalid, "", "empty git command")
	}
	op := opName(c.args)
	deadline := time.Now().Add(s.lockWait)
	backoff := 25 * time.Millisecond
	for {
		out, err := s.execOnce(ctx, c, op)
		if err == nil || KindOf(err) != KindLocked || !time.Now().Before(deadline) {
			return out, err
		}
		// A retry must send the same input again. Readers we can rewind (every stdin
		// we pass is a strings or bytes reader) are; anything else cannot be replayed,
		// so the lock error is returned rather than a command that got no input.
		if c.stdin != nil {
			sk, ok := c.stdin.(io.Seeker)
			if !ok {
				return out, err
			}
			if _, serr := sk.Seek(0, io.SeekStart); serr != nil {
				return out, err
			}
		}
		// Another git process holds a lock (index.lock, a ref lock, config.lock) or is
		// creating a worktree (see classify). git fails before changing anything in
		// those cases, so retrying is safe. So is retrying a read: it does nothing the
		// second time that it did not do the first, and a read is never locked out by
		// anything but the second case.
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(backoff):
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

// opName extracts a readable Git operation, including supported subcommands; args must be
// nonempty.
func opName(args []string) string {
	if len(args) >= 2 && (args[0] == "worktree" || args[0] == "stash" || args[0] == "sparse-checkout") && !strings.HasPrefix(args[1], "-") {
		return args[0] + " " + args[1]
	}
	return args[0]
}

func (s *settings) execOnce(ctx context.Context, c call, op string) (*output, error) {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = s.timeout
	}
	maxOut := c.maxOut
	if maxOut <= 0 {
		maxOut = s.maxOut
	}
	tctx, cancelT := context.WithTimeout(ctx, timeout)
	defer cancelT()
	// A second, cancelable layer lets the output cap stop the command without
	// being mistaken for a timeout.
	kctx, cancelK := context.WithCancel(tctx)
	defer cancelK()

	cmd := exec.CommandContext(kctx, s.git, s.argv(c)...)
	cmd.Dir = c.dir
	env := s.baseEnv()
	env = append(env, c.env...)
	if len(c.overrides) > 0 {
		env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(c.overrides)))
		for i, o := range c.overrides {
			env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, o.k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, o.v))
		}
	}
	if c.gitDir != "" {
		env = append(env, "GIT_DIR="+c.gitDir)
	}
	cmd.Env = env
	cmd.Stdin = c.stdin // nil means /dev/null: git can never wait for a prompt
	stdout := &capWriter{max: maxOut}
	if c.killOnCap {
		stdout.onOver = cancelK
	}
	stderr := &capWriter{max: maxStderrBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	finish := configureProc(cmd)
	cmd.WaitDelay = waitDelay

	runErr := cmd.Run()
	finish()
	out := &output{stdout: stdout.bytes(), stderr: strings.TrimSpace(string(stderr.bytes())), exit: 0}
	if cmd.ProcessState != nil {
		out.exit = cmd.ProcessState.ExitCode()
	} else {
		out.exit = -1
	}
	out.truncated = stdout.overflowed()

	if errors.Is(runErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		// git succeeded but something it started (a daemonized helper, a hook we
		// failed to disarm) still held our pipes past WaitDelay. The output we have
		// is complete; make sure the stray does not outlive us.
		killGroup(cmd.Process.Pid)
		runErr = nil
	}
	if runErr == nil {
		if out.truncated && !c.killOnCap {
			return out, &Error{Kind: KindTooLarge, Op: op, ExitCode: out.exit, Detail: fmt.Sprintf("output exceeded %d bytes", maxOut)}
		}
		return out, nil
	}
	if out.truncated && c.killOnCap {
		// We stopped it on purpose; what we kept is the answer.
		return out, nil
	}
	for _, ok := range c.okExit {
		if out.exit == ok && ok >= 0 {
			return out, nil
		}
	}
	// Timeout and cancellation first: the exit status of a killed process says
	// nothing about git.
	if err := ctx.Err(); err != nil {
		kind := KindCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			kind = KindTimeout
		}
		return out, &Error{Kind: kind, Op: op, ExitCode: out.exit, Stderr: out.stderr, Err: err}
	}
	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		return out, &Error{Kind: KindTimeout, Op: op, ExitCode: out.exit, Stderr: out.stderr,
			Detail: fmt.Sprintf("killed after %s", timeout), Err: context.DeadlineExceeded}
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return out, &Error{Kind: classify(out.stderr + "\n" + string(out.stdout[:min(len(out.stdout), 4096)])), Op: op, ExitCode: out.exit, Stderr: out.stderr, Err: runErr}
	}
	// The process never started (bad working directory, exec failure) or its
	// pipes failed.
	kind := KindOther
	if errors.Is(runErr, os.ErrNotExist) {
		kind = KindNotFound
	}
	return out, &Error{Kind: kind, Op: op, ExitCode: -1, Stderr: out.stderr, Err: runErr}
}
