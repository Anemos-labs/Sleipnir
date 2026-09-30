package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// shim writes an executable wrapper around the real git. body runs before the
// real git is exec'd (or instead of it); it may use $D, the shim's own directory,
// to leave evidence, because the scrubbed environment carries no test variables.
func shim(t *testing.T, body string) (path, dir string) {
	t.Helper()
	skipWithoutUnix(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir = t.TempDir()
	path = filepath.Join(dir, "git-shim")
	script := "#!/bin/sh\n" +
		"D=$(dirname \"$0\")\n" +
		"REAL='" + realGit + "'\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'git version 2.43.0'; exit 0; fi\n" +
		body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func globFiles(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunnerScrubsEnvironmentAndHardensEveryInvocation(t *testing.T) {
	dir := newRepo(t)
	gitShim, out := shim(t, `env > "$D/env.$$"; printf '%s\n' "$@" > "$D/args.$$"; exec "$REAL" "$@"`)

	// Everything that could steer git or leak a credential, in the caller's environment.
	for k, v := range map[string]string{
		"GIT_DIR": "/nonexistent", "GIT_WORK_TREE": "/nonexistent", "GIT_INDEX_FILE": "/nonexistent",
		"GIT_EXTERNAL_DIFF": "/bin/false", "GIT_SSH_COMMAND": "evil", "GIT_PROXY_COMMAND": "evil",
		"GIT_EXEC_PATH": "/nonexistent", "GIT_ASKPASS": "/evil", "GIT_CONFIG_COUNT": "1",
		"GIT_CONFIG_KEY_0": "core.fsmonitor", "GIT_CONFIG_VALUE_0": "/evil", "GIT_CONFIG_PARAMETERS": "'core.hooksPath=/evil'",
		"GIT_PAGER": "evil", "GIT_EDITOR": "evil", "GIT_TRACE": "/tmp/should-not-exist",
		"GITHUB_TOKEN": "ghp_secret", "OPENAI_API_KEY": "sk-secret", "AWS_SECRET_ACCESS_KEY": "s", "SSH_AUTH_SOCK": "/evil",
		"SSH_ASKPASS": "/evil", "EDITOR": "evil", "VISUAL": "evil", "PAGER": "evil",
		"LANG": "de_DE.UTF-8", "LC_ALL": "de_DE.UTF-8", "LANGUAGE": "de",
	} {
		t.Setenv(k, v)
	}
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "changed\n")
	if _, err := r.Status(ctx); err != nil {
		t.Fatalf("Status under a hostile environment: %v", err)
	}
	if _, err := r.Diff(ctx, "HEAD", DiffOptions{}); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if _, err := r.CommitAll(ctx, "c", Author{}); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	envs := globFiles(t, filepath.Join(out, "env.*"))
	argsFiles := globFiles(t, filepath.Join(out, "args.*"))
	if len(envs) < 5 || len(envs) != len(argsFiles) {
		t.Fatalf("recorded %d env files and %d arg files", len(envs), len(argsFiles))
	}
	forbidden := []string{"GIT_DIR=/nonexistent", "GIT_WORK_TREE=", "GIT_EXTERNAL_DIFF=", "GIT_SSH_COMMAND=", "GIT_PROXY_COMMAND=", "GIT_EXEC_PATH=",
		"GIT_ASKPASS=/evil", "GIT_CONFIG_PARAMETERS=", "GIT_PAGER=evil", "GIT_EDITOR=evil", "GIT_TRACE=", "GITHUB_TOKEN", "OPENAI_API_KEY",
		"AWS_SECRET", "SSH_AUTH_SOCK", "SSH_ASKPASS", "EDITOR=evil", "VISUAL", "PAGER=evil", "de_DE", "LANGUAGE=de", "GIT_CONFIG_KEY_0=core.fsmonitor"}
	required := []string{"LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:", "GIT_NO_REPLACE_OBJECTS=1", "HOME="}
	for _, f := range envs {
		env := readFile(t, f)
		// Repo-bound commands carry our own GIT_DIR (the repository we opened), never
		// the caller's; the discovery commands of Open run before we know it.
		argsText := readFile(t, strings.Replace(f, "env.", "args.", 1))
		if strings.Contains(argsText, "--work-tree=") && !strings.Contains(argsText, "--is-bare-repository") && !strings.Contains(env, "GIT_DIR="+filepath.Join(r.Root(), ".git")+"\n") {
			t.Errorf("%s: GIT_DIR is not pinned to the opened repository:\n%s", filepath.Base(f), env)
		}
		for _, bad := range forbidden {
			if strings.Contains(env, bad) {
				t.Errorf("%s: environment leaks %q", filepath.Base(f), bad)
			}
		}
		for _, want := range required {
			if !strings.Contains(env, want) {
				t.Errorf("%s: environment lacks %q", filepath.Base(f), want)
			}
		}
	}
	// Every invocation carries the hardening on its command line, before the subcommand.
	wantArgs := []string{"--no-pager", "--no-replace-objects", "--literal-pathspecs", "core.hooksPath=" + os.DevNull, "core.fsmonitor=false",
		"protocol.ext.allow=never", "protocol.allow=never", "credential.helper=", "commit.gpgsign=false", "core.askPass=", "diff.external=", "gc.auto=0"}
	for _, f := range argsFiles {
		args := strings.Split(strings.TrimRight(readFile(t, f), "\n"), "\n")
		joined := "\n" + strings.Join(args, "\n") + "\n"
		for _, want := range wantArgs {
			if !strings.Contains(joined, "\n"+want+"\n") {
				t.Errorf("%s: missing %q in %v", filepath.Base(f), want, args)
			}
		}
		if !strings.Contains(joined, "\n--work-tree="+r.Root()+"\n") {
			t.Errorf("%s: work tree is not pinned: %v", filepath.Base(f), args)
		}
	}
}

func TestRunnerSessionAndStdin(t *testing.T) {
	dir := newRepo(t)
	gitShim, out := shim(t, `
pgid=$(ps -o pgid= -p $$ | tr -d ' ')
tty=$(ps -o tty= -p $$ | tr -d ' ')
n=$(wc -c 2>/dev/null | tr -d ' ')
echo "pid=$$ pgid=$pgid tty=$tty stdin=$n" > "$D/proc.$$"
exec "$REAL" "$@"`)
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Head(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	files := globFiles(t, filepath.Join(out, "proc.*"))
	if len(files) == 0 {
		t.Fatal("shim did not run")
	}
	for _, f := range files {
		line := strings.TrimSpace(readFile(t, f))
		if !strings.Contains(line, "stdin=0") {
			t.Errorf("git inherited a readable stdin: %s", line)
		}
		// A process that is the leader of its own group and has no controlling terminal is what
		// Setsid makes of it (ps has no portable "session id" column: macOS lacks it).
		var pid, pgid, tty string
		for _, fld := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(fld, "pid="); ok {
				pid = v
			}
			if v, ok := strings.CutPrefix(fld, "pgid="); ok {
				pgid = v
			}
			if v, ok := strings.CutPrefix(fld, "tty="); ok {
				tty = v
			}
		}
		if pid == "" || pid != pgid || strings.Trim(tty, "?") != "" {
			t.Errorf("git is not the leader of its own session (no controlling terminal, killable as a group): %s", line)
		}
	}
}

func TestRunnerTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	dir := newRepo(t)
	// The shim starts a grandchild that holds our pipes, then hangs.
	gitShim, out := shim(t, `
if [ "$1" = "--no-pager" ] && echo "$@" | grep -q ' status '; then
  sleep 60 &
  echo $! > "$D/grandchild.pid"
  sleep 60
  exit 0
fi
exec "$REAL" "$@"`)
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig(), WithTimeout(400*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = r.Status(ctxT(t))
	took := time.Since(start)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ErrTimeout wrapping DeadlineExceeded, got %v", err)
	}
	if took > 6*time.Second {
		t.Fatalf("timeout took %s: the runner waited for the grandchild", took)
	}
	pidText := strings.TrimSpace(readFile(t, filepath.Join(out, "grandchild.pid")))
	pid, _ := strconv.Atoi(pidText)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if !processRunning(pid) {
			break // gone (a zombie counts as gone: dead, just not reaped by its killed parent yet)
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunnerCancellation(t *testing.T) {
	dir := newRepo(t)
	gitShim, _ := shim(t, `case " $* " in *" status "*) sleep 30; exit 0;; esac; exec "$REAL" "$@"`)
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err = r.Status(ctx)
	if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("want ErrCanceled, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancellation was not prompt")
	}
	// A caller deadline that fires first is reported as a timeout.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if _, err := r.Status(ctx2); !errors.Is(err, ErrTimeout) {
		t.Fatalf("caller deadline: want ErrTimeout, got %v", err)
	}
}

// fileAppears polls for path (a shim's "I am ready" signal, so that a signal is
// never sent before its trap is installed).
func fileAppears(path string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cancelWhenReady cancels once the shim has said it is ready and reports when it
// did, so that a test measures how long stopping took rather than how long the
// shim took to start. It never blocks forever: a shim that fails to start is
// reported and the call is cancelled anyway.
func cancelWhenReady(t *testing.T, out string, cancel context.CancelFunc) <-chan time.Time {
	at := make(chan time.Time, 1)
	go func() {
		if !fileAppears(filepath.Join(out, "ready"), 10*time.Second) {
			t.Errorf("the shim never signalled readiness")
		}
		at <- time.Now()
		cancel()
	}()
	return at
}

func waitGone(t *testing.T, what string, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for processRunning(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("%s (pid %d) is still running", what, pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A command that is stopped gets SIGTERM first: that is what makes git delete the
// lock files it holds. (SIGKILL leaves index.lock or a ref lock behind, and every
// later write to that repository then fails.)
func TestRunnerStopsCommandsGentlyFirstSoTheyCanRemoveTheirLocks(t *testing.T) {
	dir := newRepo(t)
	gitShim, out := shim(t, `
case " $* " in *" status "*)
  : > "$D/held.lock"
  trap 'echo term > "$D/got-term"; rm -f "$D/held.lock"; exit 143' TERM
  sleep 30 &
  echo ready > "$D/ready"
  wait
  exit 0;;
esac
exec "$REAL" "$@"`)
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelledAt := cancelWhenReady(t, out, cancel)
	_, err = r.Status(ctx)
	<-cancelledAt
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("want ErrCanceled, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(out, "got-term")); serr != nil {
		t.Fatal("the command was not asked to stop with SIGTERM: it never got to clean up")
	}
	if _, serr := os.Stat(filepath.Join(out, "held.lock")); serr == nil {
		t.Fatal("the lock file the command held is still there")
	}
}

// A command that ignores SIGTERM is killed, with everything else in its group,
// once the grace period is over.
func TestRunnerKillsWhatIgnoresSIGTERMAfterTheGracePeriod(t *testing.T) {
	dir := newRepo(t)
	gitShim, out := shim(t, `
case " $* " in *" status "*)
  trap '' TERM
  echo $$ > "$D/leader.pid"
  sleep 60 &
  echo $! > "$D/child.pid"
  echo ready > "$D/ready"
  wait
  exit 0;;
esac
exec "$REAL" "$@"`)
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelledAt := cancelWhenReady(t, out, cancel)
	_, err = r.Status(ctx)
	took := time.Since(<-cancelledAt)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("want ErrCanceled, got %v", err)
	}
	if took < termGrace-100*time.Millisecond {
		t.Fatalf("stopped after %s: the command was killed without the grace period", took)
	}
	if took > 8*time.Second {
		t.Fatalf("took %s to stop a command that ignores SIGTERM", took)
	}
	for _, f := range []string{"leader.pid", "child.pid"} {
		pid, _ := strconv.Atoi(strings.TrimSpace(readFile(t, filepath.Join(out, f))))
		waitGone(t, f, pid, 3*time.Second)
	}
}

// A stray that ignores SIGTERM and no longer holds our pipes would not delay the
// call at all, so nothing but the final sweep can end it.
func TestRunnerSweepsStraysThatSurviveTheStop(t *testing.T) {
	dir := newRepo(t)
	gitShim, out := shim(t, `
case " $* " in *" status "*)
  ( trap '' TERM; exec sleep 60 ) >/dev/null 2>&1 </dev/null &
  echo $! > "$D/stray.pid"
  echo ready > "$D/ready"
  sleep 60
  exit 0;;
esac
exec "$REAL" "$@"`)
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelWhenReady(t, out, cancel)
	if _, err = r.Status(ctx); !errors.Is(err, ErrCanceled) {
		t.Fatalf("want ErrCanceled, got %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(readFile(t, filepath.Join(out, "stray.pid"))))
	waitGone(t, "the stray", pid, 3*time.Second)
}

func TestRunnerOutputCap(t *testing.T) {
	dir := newRepo(t)
	// 10 MB on stdout for status and cat-file, from a process that would block if we stopped reading.
	gitShim, _ := shim(t, `
case " $* " in
  *" status "*|*" cat-file "*) head -c 10000000 /dev/zero | tr '\0' 'x'; exit 0;;
esac
exec "$REAL" "$@"`)
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig(), WithMaxOutput(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	// A caller that treats overflow as truncation (Status) gets what fits.
	st, err := r.Status(ctx)
	if err != nil || !st.Truncated {
		t.Fatalf("Status: truncated=%v err=%v", st != nil && st.Truncated, err)
	}
	// Anything else fails loudly instead of returning a silently partial answer.
	if _, err := r.Show(ctx, "HEAD", "a.txt"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Show over the cap: want ErrTooLarge, got %v", err)
	}
}

func TestRunnerLockContention(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir, WithLockWait(5*time.Second))
	ctx := ctxT(t)
	lock := filepath.Join(dir, ".git", "index.lock")
	writeFile(t, lock, "")
	writeFile(t, filepath.Join(dir, "a.txt"), "changed under lock\n")
	go func() {
		time.Sleep(400 * time.Millisecond)
		os.Remove(lock)
	}()
	start := time.Now()
	sha, err := r.CommitAll(ctx, "after the lock cleared", Author{})
	if err != nil || sha == "" {
		t.Fatalf("CommitAll should have retried past the lock: %q, %v", sha, err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("did not actually wait for the lock")
	}
	// A lock that never clears is reported as such, after the configured wait.
	stuck := openRepo(t, dir, WithLockWait(300*time.Millisecond))
	writeFile(t, lock, "")
	writeFile(t, filepath.Join(dir, "a.txt"), "changed again\n")
	start = time.Now()
	_, err = stuck.CommitAll(ctx, "stuck", Author{})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("gave up too slowly: %s", d)
	}
	os.Remove(lock)
	// Reads never wait on locks (GIT_OPTIONAL_LOCKS=0): Status works while one is held.
	writeFile(t, lock, "")
	defer os.Remove(lock)
	if _, err := r.Status(ctx); err != nil {
		t.Fatalf("Status while an index lock is held: %v", err)
	}
}

func TestVersionGate(t *testing.T) {
	skipWithoutUnix(t)
	old := filepath.Join(t.TempDir(), "old-git")
	if err := os.WriteFile(old, []byte("#!/bin/sh\necho 'git version 2.20.1'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.TempDir(), WithGitPath(old)); !errors.Is(err, ErrNoGit) {
		t.Fatalf("old git: want ErrNoGit, got %v", err)
	}
	junk := filepath.Join(t.TempDir(), "junk-git")
	if err := os.WriteFile(junk, []byte("#!/bin/sh\necho 'hello'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.TempDir(), WithGitPath(junk)); !errors.Is(err, ErrNoGit) {
		t.Fatalf("unparsable version: want ErrNoGit, got %v", err)
	}
	if _, err := Open(t.TempDir(), WithGitPath(filepath.Join(t.TempDir(), "missing"))); !errors.Is(err, ErrNoGit) {
		t.Fatalf("missing binary: want ErrNoGit, got %v", err)
	}
	apple := filepath.Join(t.TempDir(), "apple-git")
	if err := os.WriteFile(apple, []byte("#!/bin/sh\necho 'git version 2.39.3 (Apple Git-146)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkVersion(apple); err != nil {
		t.Fatalf("vendor-suffixed version rejected: %v", err)
	}
}

func TestErrorFormatting(t *testing.T) {
	e := &Error{Kind: KindConflict, Op: "merge", Stderr: "line one\n\nline two\nline three\nline four", ExitCode: 1}
	msg := e.Error()
	if !strings.Contains(msg, "merge") || !strings.Contains(msg, "conflict") || strings.Contains(msg, "line four") {
		t.Fatalf("message = %q", msg)
	}
	if !errors.Is(e, ErrConflict) || errors.Is(e, ErrDirty) {
		t.Fatal("sentinel matching is wrong")
	}
	if KindOf(e) != KindConflict || KindOf(errors.New("x")) != KindOther || KindOf(nil) != KindOther {
		t.Fatal("KindOf")
	}
	wrapped := errors.Join(errors.New("ctx"), e)
	if !errors.Is(wrapped, ErrConflict) {
		t.Fatal("errors.Is through Join")
	}
	for stderr, want := range map[string]Kind{
		"fatal: not a git repository (or any of the parent directories): .git":                 KindNotARepo,
		"fatal: detected dubious ownership in repository at '/x'":                              KindUnsafe,
		"fatal: Unable to create '/x/.git/index.lock': File exists.":                           KindLocked,
		"error: Your local changes to the following files would be overwritten by merge:\n\tx": KindDirty,
		"CONFLICT (content): Merge conflict in a.txt":                                          KindConflict,
		"fatal: ambiguous argument 'zzz': unknown revision or path not in the working tree.":   KindNotFound,
		"fatal: this operation must be run in a work tree":                                     KindNotARepo,
		"fatal: failed to read /x/.git/worktrees/w-01/commondir: Success":                      KindLocked,
		"fatal: failed to read .git/worktrees/w-01/commondir: Undefined error: 0":              KindLocked,
		"fatal: failed to read /x/notes.txt: Success":                                          KindOther,
		"error: could not lock config file .git/config: File exists":                           KindLocked,
		"error: could not lock config file .git/config: Permission denied":                     KindOther,
		"something unexpected": KindOther,
	} {
		if got := classify(stderr); got != want {
			t.Errorf("classify(%q) = %v, want %v", stderr, got, want)
		}
	}
}

// A retry after lock contention must send the same standard input again: the
// commit message travels on stdin, and a retry with an empty stdin would create a
// commit with no message (or fail in a confusing way).
func TestLockRetryReplaysStandardInput(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir, WithLockWait(10*time.Second))
	ctx := ctxT(t)
	// the branch's ref lock: `add` is unaffected, but the commit that follows it
	// fails at the last step, after it has read the message
	lock := filepath.Join(dir, ".git", "refs", "heads", "main.lock")
	writeFile(t, lock, "")
	writeFile(t, filepath.Join(dir, "a.txt"), "changed\n")
	go func() {
		time.Sleep(700 * time.Millisecond)
		os.Remove(lock)
	}()
	start := time.Now()
	sha, err := r.CommitAll(ctx, "message that must survive the retry\n\nbody", Author{Name: "agent"})
	if err != nil || sha == "" {
		t.Fatalf("CommitAll: %q, %v", sha, err)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Fatal("did not actually hit and wait out the ref lock")
	}
	c, err := r.CommitInfo(ctx, sha)
	if err != nil || c.Subject != "message that must survive the retry" || c.Body != "body" {
		t.Fatalf("commit after a retry: %+v, %v", c, err)
	}
}
