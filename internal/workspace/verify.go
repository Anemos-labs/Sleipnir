package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// VerifyRequest is one run of the verification command.
type VerifyRequest struct {
	// Dir is the integration tree the command runs in.
	Dir string
	// Cmd is the shell command line (the harness's configured VerifyCmd).
	Cmd         string
	Agent, Task string
	// Timeout bounds the run; the whole process group is killed when it expires.
	Timeout time.Duration
	// MaxOutput caps the captured output (head and tail are kept, the middle is
	// elided).
	MaxOutput int
	// Env is extra environment, "KEY=VALUE".
	Env []string
}

// VerifyResult is what the verifier did.
type VerifyResult struct {
	Cmd      string
	ExitCode int
	// Output is the combined stdout and stderr, elided in the middle when long:
	// the start (what ran) and the end (why it failed) are what matter.
	Output    string
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
	// Err is set when the command could not be run at all.
	Err error
}

// OK reports whether the verifier ran and succeeded.
func (v VerifyResult) OK() bool { return v.Err == nil && !v.TimedOut && v.ExitCode == 0 }

// Summary is a one-line description for logs and boards.
func (v VerifyResult) Summary() string {
	switch {
	case v.Err != nil:
		return fmt.Sprintf("could not run %q: %v", v.Cmd, v.Err)
	case v.TimedOut:
		return fmt.Sprintf("%q timed out", v.Cmd)
	case v.ExitCode != 0:
		return fmt.Sprintf("%q failed (exit %d)", v.Cmd, v.ExitCode)
	}
	return fmt.Sprintf("%q passed", v.Cmd)
}

// VerifyFunc runs a verification command. The default is RunShell; the session
// layer can inject the shell tool's runner instead.
type VerifyFunc func(ctx context.Context, req VerifyRequest) VerifyResult

// RunShell runs req.Cmd with the platform shell in req.Dir. The command is
// repository-defined and runs test code, so it is contained the way every tool
// command is: bounded in time (process group killed), bounded in output,
// credentials scrubbed from its environment, GIT_* removed (an inherited GIT_DIR
// would point the tests' own git calls at the wrong repository), no terminal.
func RunShell(ctx context.Context, req VerifyRequest) VerifyResult {
	res := VerifyResult{Cmd: req.Cmd, ExitCode: -1}
	if strings.TrimSpace(req.Cmd) == "" {
		res.Err = errors.New("empty verification command")
		return res
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	maxOut := req.MaxOutput
	if maxOut <= 0 {
		maxOut = 64 << 10
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(tctx, "cmd", "/C", req.Cmd)
	} else {
		cmd = exec.CommandContext(tctx, "sh", "-c", req.Cmd)
	}
	cmd.Dir = req.Dir
	cmd.Env = verifyEnv(os.Environ(), req)
	cmd.Stdin = nil
	buf := newTailBuffer(maxOut)
	cmd.Stdout, cmd.Stderr = buf, buf // one writer: interleaving is preserved
	configureCmd(cmd)
	cmd.WaitDelay = 3 * time.Second

	start := time.Now()
	err := cmd.Run()
	res.Duration = time.Since(start)
	if cmd.Process != nil {
		killGroup(cmd.Process.Pid) // reap background children the command left behind
	}
	res.Output, res.Truncated = buf.String()
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case err == nil:
	case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil:
		// The command finished but something it started kept our pipes open; its own
		// exit status stands.
	case ctx.Err() != nil:
		res.Err = ctx.Err()
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.ExitCode = -1
	default:
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			res.Err = err // could not be started
		}
	}
	return res
}

var secretEnvName = regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password|passwd|credential|private[_-]?key|(^|[_-])(key|pass|passphrase|pat|auth|authorization)$|^ssh_auth_sock$)`)

// verifyEnv builds the verifier's environment from base.
func verifyEnv(base []string, req VerifyRequest) []string {
	out := make([]string, 0, len(base)+8)
	for _, kv := range base {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		up := strings.ToUpper(name)
		if strings.HasPrefix(up, "GIT_") || secretEnvName.MatchString(name) ||
			up == "PWD" || up == "TERM" || up == "NO_COLOR" || strings.HasPrefix(up, "SLEIPNIR_") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"TERM=dumb", "NO_COLOR=1", "GIT_TERMINAL_PROMPT=0", "PAGER=cat", "GIT_PAGER=cat",
		"PWD="+req.Dir, "SLEIPNIR_WORKSPACE="+req.Dir, "SLEIPNIR_AGENT="+req.Agent, "SLEIPNIR_TASK="+req.Task)
	return append(out, req.Env...)
}

// tailBuffer keeps the first head bytes and the last tail bytes written, and
// counts what fell between.
type tailBuffer struct {
	mu      sync.Mutex
	headMax int
	tailMax int
	head    []byte
	tail    []byte
	total   int64
}

func newTailBuffer(max int) *tailBuffer {
	head := max / 8
	if head > 8<<10 {
		head = 8 << 10
	}
	return &tailBuffer{headMax: head, tailMax: max - head}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	if room := b.headMax - len(b.head); room > 0 {
		take := room
		if take > len(p) {
			take = len(p)
		}
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		// Trim in bulk so appends stay amortized O(1).
		if len(b.tail) > 2*b.tailMax {
			b.tail = append([]byte(nil), b.tail[len(b.tail)-b.tailMax:]...)
		}
	}
	return n, nil
}

func (b *tailBuffer) String() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tail := b.tail
	if len(tail) > b.tailMax {
		tail = tail[len(tail)-b.tailMax:]
	}
	omitted := b.total - int64(len(b.head)) - int64(len(tail))
	var out bytes.Buffer
	out.Write(b.head)
	if omitted > 0 {
		fmt.Fprintf(&out, "\n[... %d bytes omitted ...]\n", omitted)
	}
	out.Write(tail)
	return strings.ToValidUTF8(out.String(), "�"), omitted > 0
}
