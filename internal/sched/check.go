package sched

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/hooks"
)

// CheckTimeout bounds a job's change check: a check is meant to read one small value (a commit, a count, a date), and one that
// hangs must not hold the daemon.
const CheckTimeout = time.Minute

// maxCheckOutput is how much of a check's output is compared; the rest is read and ignored.
const maxCheckOutput = 1 << 20

// Exit texts of a job whose change check stopped the run.
const (
	// ExitNoChange is the LastExit of a due job whose check printed what it printed before the last successful run: the run was
	// skipped and no model was called.
	ExitNoChange = "no change"
	// checkFailedPrefix starts the LastExit of a job whose check failed; the run was skipped.
	checkFailedPrefix = "check failed: "
)

// runCheck is RunCheck; tests replace it.
var runCheck = RunCheck

// RunCheck runs the change check of j (its Check command, by the shell, in its directory, stdin closed, bounded by CheckTimeout) and
// returns a digest of the first megabyte it printed on standard output. The check gets the environment a hook gets (hooks.ScrubEnv:
// no provider key, held or not, and nothing that looks like a credential), and every process it started is ended when it returns. A check that exits non-zero, times out or
// cannot start is an error that names the step and the last line of its standard error.
func RunCheck(ctx context.Context, j Job) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	cmd := checkCommand(cctx, j.Check)
	cmd.Dir = j.Dir
	cmd.Env = hooks.ScrubEnv(os.Environ(), harden.Held())
	var errb bytes.Buffer
	h := sha256.New()
	cmd.Stdout = &limitedWriter{w: h, n: maxCheckOutput}
	cmd.Stderr = &limitedWriter{w: &errb, n: 4 << 10}
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	endGroup(cmd) // a background process the check left behind must not pile up, one per slot
	switch {
	case err == nil:
		return hex.EncodeToString(h.Sum(nil)), nil
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("timed out after %s", CheckTimeout)
	case ctx.Err() != nil:
		return "", errors.New("interrupted")
	}
	msg := err.Error()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg = fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	if line := lastLine(errb.String()); line != "" {
		msg += ": " + line
	}
	return "", errors.New(msg)
}

// lastLine is the last non-blank line of s, trimmed and at most 120 characters.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(l); len(r) > 120 {
		l = string(r[:119]) + "…"
	}
	return l
}

// limitedWriter passes on the first n bytes written to it and drops the rest, never failing the writer: a check that prints more
// than is compared still runs to its end.
type limitedWriter struct {
	w io.Writer
	n int
}

// Write passes on what fits in the remaining allowance and reports the whole of p as written.
func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// gate decides, for a due job with a change check, whether its run starts: run is true when the check printed something other than
// what it printed before the last successful run (sum is the new digest, to be kept once the run succeeds); otherwise exit says why
// the run was skipped. A job without a check always runs.
func gate(ctx context.Context, j Job) (run bool, sum, exit string) {
	if strings.TrimSpace(j.Check) == "" {
		return true, "", ""
	}
	sum, err := runCheck(ctx, j)
	if err != nil {
		return false, "", checkFailedPrefix + err.Error()
	}
	if sum == j.CheckSum {
		return false, sum, ExitNoChange
	}
	return true, sum, ""
}
