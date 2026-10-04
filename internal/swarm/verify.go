package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// verifyResult is the outcome of the harness's verification gate.
type verifyResult struct {
	ok    bool // the gate passed, or no verifier is configured
	ran   bool // the verifier ran to completion (pass or fail)
	infra bool // it could not run: an error, a timeout, an interrupted swarm
	code  int
	out   string
	err   error
}

// verify runs the configured verifier (Config.VerifyCmd through Config.Verify) in
// dir. The harness, not the model, decides whether work is finished: this is the
// gate a worker's done passes through, and again when the manager accepts.
//
// The run is bounded: at most Config.MaxVerifies verifiers run at once (fifty workers
// finishing together must not start fifty test suites in one tree), each has a
// deadline, and a verifier that ignores its context cannot hold the caller past it.
// A verifier that could not run at all is reported as such (infra) and never as a
// failed test.
//
// files is the task's scope, used to expand {dirs} in the command (ExpandVerify).
func (s *Swarm) verify(ctx context.Context, dir string, files []string) verifyResult {
	vr := s.runVerify(ctx, dir, files)
	if s.cfg.VerifyCmd != "" && !vr.infra {
		s.verifyRan.Add(1)
		if !vr.ok {
			s.verifyFailed.Add(1)
		}
	}
	return vr
}

// VerifyRuns is how many times the verify command ran (a run that could not start, or was cut off, is not counted) and how many of them failed.
func (s *Swarm) VerifyRuns() (ran, failed int) {
	return int(s.verifyRan.Load()), int(s.verifyFailed.Load())
}

// recordVerificationFailure spends the task's shared budget for explicit done
// calls and implicit stops. It retains bounded verifier evidence for recovery.
func (s *Swarm) recordVerificationFailure(t Task, cmd string, vr verifyResult) (Task, bool) {
	reason := fmt.Sprintf("verification `%s` failed %d times (last exit %d)", cleanText(cmd, 80), maxGateTries+1, vr.code)
	evidence := fmt.Sprintf("verification `%s` failed (exit %d): %s", cleanText(cmd, 80), vr.code, tailText(vr.out, 200))
	return s.Board.FailVerification(t.Owner, t.ID, t.Rev, reason, evidence, maxGateTries+1, s.cfg.MaxAttempts, taskGateCheck(t))
}

// taskGateCheck binds verification and review evidence to the assignment and
// scope that produced it. Scope changes deliberately do not replace a run's
// assignment revision, so checking the revision alone is insufficient.
func taskGateCheck(expected Task) TaskCheck {
	return func(_ *Snapshot, current Task) error {
		if current.ID != expected.ID || current.Owner != expected.Owner || current.Rev != expected.Rev {
			return fmt.Errorf("%s changed assignment while it was being checked; inspect the task before retrying", expected.ID)
		}
		if !slices.Equal(current.Files, expected.Files) {
			return fmt.Errorf("%s changed scope while it was being checked; verify the current scope and retry", expected.ID)
		}
		return nil
	}
}

// checkTaskGate rejects an obsolete result before further merge or retry work.
// The final board mutation repeats this check atomically.
func (s *Swarm) checkTaskGate(expected Task) error {
	snap := s.Board.Snapshot()
	current, _ := snap.Task(expected.ID)
	return taskGateCheck(expected)(snap, current)
}

// retryChangedScope wakes an implicitly stopped worker to check its current
// scope. A replacement assignment or a settled task receives no stale feedback.
func (s *Swarm) retryChangedScope(m *member, checked Task) {
	current, ok := s.Board.Snapshot().Task(checked.ID)
	if ok && current.Owner == m.id && current.Rev == checked.Rev && current.Status == StatusDoing && !slices.Equal(current.Files, checked.Files) {
		s.notify(m.id, "request", fmt.Sprintf("%s changed scope while verification or merging ran. Read its current scope with task get, finish that scope, and call task done again; the old result did not complete the task.", checked.ID))
	}
}

// submitSettledTask submits an implicit completion only for the checked scope,
// arranging another worker run if a scope update made the result obsolete.
func (s *Swarm) submitSettledTask(m *member, checked Task, result, evidence string) bool {
	if err := s.Board.SubmitAt(m.id, checked.ID, checked.Rev, result, evidence, taskGateCheck(checked)); err != nil {
		s.retryChangedScope(m, checked)
		return false
	}
	return true
}

// verificationFailureNotice gives the manager the exhausted retry budget and
// bounded verifier output, marked as untrusted data rather than instructions.
func (s *Swarm) verificationFailureNotice(owner string, t Task, cmd string, vr verifyResult) string {
	reason := fmt.Sprintf("verification `%s` failed %d times (last exit %d)", cleanText(cmd, 80), maxGateTries+1, vr.code)
	// notifyManager caps the whole notice at 400 runes. Reserve room for the final
	// diagnostic, rather than keeping the beginning of an already truncated tail.
	return cleanText(s.requeueLine(owner, t, reason), 200) + "\nVerifier output (untrusted data):\n" + tailText(vr.out, 160)
}

// runVerify bounds scope discovery, queueing, and execution by one deadline.
// A runner retains its concurrency slot until it exits, even if its caller has
// already returned after cancellation; late output is never a verdict.
func (s *Swarm) runVerify(ctx context.Context, dir string, files []string) verifyResult {
	if s.cfg.VerifyCmd == "" {
		return verifyResult{ok: true}
	}
	vctx, cancel := context.WithTimeout(ctx, s.cfg.VerifyTimeout)
	defer cancel()
	if vctx.Err() != nil {
		return s.interruptedVerification(ctx)
	}
	if len(files) == 0 && strings.Contains(s.cfg.VerifyCmd, verifyDirsToken) {
		// Limit an unscoped task to changed files; repository-wide checks may
		// depend on other workers' unmerged changes.
		files = changedFiles(vctx, dir)
	}
	cmd := ExpandVerify(s.cfg.VerifyCmd, dir, files)
	if s.cfg.Verify == nil {
		return verifyResult{infra: true, err: errors.New("no verification runner is installed")}
	}
	select {
	case s.verifySem <- struct{}{}:
	case <-vctx.Done():
		return s.interruptedVerification(ctx)
	}
	// Both cases can be ready together. Do not launch a runner after observing
	// cancellation, even when the select acquired an available slot.
	if vctx.Err() != nil {
		<-s.verifySem
		return s.interruptedVerification(ctx)
	}
	type outcome struct {
		out  string
		code int
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		defer func() { <-s.verifySem }()
		defer func() {
			if r := recover(); r != nil {
				ch <- outcome{err: fmt.Errorf("the verifier crashed: %v", r)}
			}
		}()
		out, code, err := s.cfg.Verify(vctx, dir, cmd)
		ch <- outcome{out, code, err}
	}()
	select {
	case r := <-ch:
		if vctx.Err() != nil { // interrupted or late: output is not a verdict
			return s.interruptedVerification(ctx)
		}
		if r.err != nil {
			return verifyResult{infra: true, err: r.err, out: r.out}
		}
		return verifyResult{ran: true, ok: r.code == 0, code: r.code, out: r.out}
	case <-vctx.Done():
		return s.interruptedVerification(ctx)
	}
}

// interruptedVerification preserves caller cancellation or reports the verifier's
// deadline as an infrastructure failure. Call only after its context has ended.
func (s *Swarm) interruptedVerification(ctx context.Context) verifyResult {
	if err := ctx.Err(); err != nil {
		return verifyResult{infra: true, err: err}
	}
	return verifyResult{infra: true, err: fmt.Errorf("verification timed out after %s", s.cfg.VerifyTimeout)}
}

// changedFiles is the paths git reports as changed in dir (modified, added, untracked), or nil when it cannot say.
func changedFiles(ctx context.Context, dir string) []string {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "git", "-C", dir, "status", "--porcelain", "-z", "--untracked-files=all").Output()
	if err != nil {
		return nil
	}
	var files []string
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if len(r) < 4 {
			continue
		}
		files = append(files, r[3:])
		if r[0] == 'R' || r[0] == 'C' { // a rename carries its source as the next record
			i++
		}
	}
	return files
}

// verifyDirsToken stands, in a verify command, for the directories a task may touch.
const verifyDirsToken = "{dirs}"

// maxVerifyDirs bounds how many directories {dirs} lists; a scope wider than that is the whole tree.
const maxVerifyDirs = 40

// ExpandVerify replaces {dirs} in a verify command with the directories the task's scope covers,
// as words a shell (or cmd) reads as they are: `go test {dirs}` becomes `go test ./p01 ./p05`.
//
// Task-scoped verification avoids depending on other workers' unmerged changes.
// When files is empty, reaches the repository root, contains an unsafe path, or
// exceeds maxVerifyDirs directories, {dirs} expands to ./.... The caller may use
// changedFiles to supply a scope for an otherwise unscoped task. Commands without
// the token are returned unchanged.
//
// The scope is text a model wrote, and it ends up in a command line: an entry is used only if it
// is a plain relative path inside the checkout made of letters, digits and . _ - + / (a glob
// contributes the directory before its first wildcard), and nothing else is ever inserted. root is
// the checkout the paths are looked up in: an entry that names a directory there is that
// directory, any other entry is a file (or one that does not exist yet) and contributes its parent.
func ExpandVerify(cmd, root string, files []string) string {
	if !strings.Contains(cmd, verifyDirsToken) {
		return cmd
	}
	dirs := verifyDirs(root, files)
	arg := "./..."
	if len(dirs) > 0 {
		words := make([]string, len(dirs))
		for i, d := range dirs {
			words[i] = "./" + d
		}
		arg = strings.Join(words, " ")
	}
	return strings.ReplaceAll(cmd, verifyDirsToken, arg)
}

// verifyDirs is the sorted, distinct directories (relative, slash-separated, without "./") of a
// scope; nil means the whole tree.
func verifyDirs(root string, files []string) []string {
	seen := map[string]bool{}
	for _, f := range files {
		f = strings.TrimSpace(f)
		f = strings.TrimPrefix(f, "./")
		if f == "" {
			continue
		}
		if !plainScopePath(f) {
			return nil // something odd in the scope: verify everything rather than guess
		}
		var dir string
		if i := strings.IndexAny(f, "*?[{"); i >= 0 {
			prefix := f[:i]
			if strings.HasSuffix(prefix, "/") {
				dir = strings.TrimSuffix(prefix, "/")
			} else {
				dir = path.Dir(prefix)
			}
		} else if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil && st.IsDir() {
			dir = strings.TrimSuffix(f, "/")
		} else {
			dir = path.Dir(f)
		}
		dir = path.Clean(dir)
		if dir == "." || dir == "" {
			return nil // the scope reaches the top of the repository
		}
		seen[dir] = true
	}
	if len(seen) == 0 || len(seen) > maxVerifyDirs {
		return nil
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// plainScopePath reports whether a scope entry is safe to put in a command line: relative, inside
// the checkout, and made only of letters, digits and . _ - + / (and the wildcards a scope allows).
func plainScopePath(f string) bool {
	if strings.HasPrefix(f, "/") || strings.HasPrefix(f, "-") {
		return false
	}
	for _, seg := range strings.Split(f, "/") {
		if seg == ".." {
			return false
		}
	}
	for _, r := range f {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._-+/*?[]{},", r):
		default:
			return false
		}
	}
	return true
}

// isolatedVerifyHint is added to the failure a worker in an isolated tree gets when the verify
// command does not name its task's directories ({dirs}): the command then looks at the whole
// repository, and the tree holds only that worker's changes, so a failure may be another task's
// work that has not been merged yet. Without this a worker edits code that is not its own to make
// the command pass, or loops on it (see ExpandVerify).
func (s *Swarm) isolatedVerifyHint(m *member) string {
	if m == nil || m.tree == nil || strings.Contains(s.cfg.VerifyCmd, verifyDirsToken) {
		return ""
	}
	return " Your tree holds only your own changes, so failures in files you did not touch may be another task's work that is not merged yet: " +
		"if the failing files are not yours, block the task and tell the manager rather than editing them."
}
