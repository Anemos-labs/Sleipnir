package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// ExecResult is what one command produced.
type ExecResult struct {
	// ExitCode is the exit status, or 128+N when the command died from signal N
	// (the shell convention), or -1 when it never ran to completion.
	ExitCode int
	Signal   int // the signal that ended it, or 0
	// Stdout, Stderr and Output (both streams interleaved in arrival order) each
	// keep the first and last part of what was written; the middle is replaced by
	// a marker. Tails matter most: test runners print their verdict last.
	Stdout, Stderr, Output string
	TimedOut               bool // ended because timeout elapsed
	Canceled               bool // ended because the context was cancelled
	OutputKilled           bool // ended because it wrote more than ExecPolicy.KillOutput
	Truncated              bool // some output was dropped
	Duration               time.Duration
	// Warnings records degradations the caller should surface, such as network
	// isolation being unavailable.
	Warnings []string
}

// Exec runs one shell command line to completion in dir with exactly the
// environment env (it must not fall back to the caller's own) and kills it,
// together with everything it started, when timeout elapses or ctx ends.
//
// It returns an error only when the command could not be run at all; a
// non-zero exit, a timeout or a kill is a normal ExecResult. Per-call policy
// (network, limits, output caps) travels in ctx, see WithExecPolicy: the
// signature is deliberately small so that a container runner can implement it
// in a dozen lines.
type Exec func(ctx context.Context, dir string, env []string, cmd string, timeout time.Duration) (ExecResult, error)

// Sandbox is the hook through which workspace setup and the verifier run
// commands. The default is LocalSandbox: process-group isolation on the host,
// which is NOT a security boundary against a hostile agent (see the package
// documentation). Deployments that need one implement Sandbox with a container
// or VM runner: mount dir (and the HOME and TMPDIR named in env) into a
// container without network unless ExecPolicyFrom(ctx).Network, run cmd with
// `sh -c`, and enforce timeout.
type Sandbox interface {
	Exec(ctx context.Context, dir string, env []string, cmd string, timeout time.Duration) (ExecResult, error)
}

// Exec lets any Exec function be used as a Sandbox.
func (f Exec) Exec(ctx context.Context, dir string, env []string, cmd string, timeout time.Duration) (ExecResult, error) {
	return f(ctx, dir, env, cmd, timeout)
}

// Limits are per-command resource limits (setrlimit). Zero means "default" for
// FileSize and "unset" for the rest; a negative FileSize removes the default.
type Limits struct {
	FileSize     int64         // largest file a command may create, bytes (default 1 GiB)
	OpenFiles    int           // RLIMIT_NOFILE
	Procs        int           // RLIMIT_NPROC: counts per user, not per command; leave 0 unless the user is dedicated
	CPU          time.Duration // RLIMIT_CPU
	AddressSpace int64         // RLIMIT_AS, bytes; breaks runtimes that reserve address space (Go, Java)
	Core         bool          // allow core dumps (off by default: they can be gigabytes)
}

// DefaultFileSizeLimit is applied when Limits.FileSize is zero.
const DefaultFileSizeLimit int64 = 1 << 30

// ExecPolicy travels with a context to whichever Sandbox runs a command.
type ExecPolicy struct {
	// Network allows network access. When false a sandbox should deny it (the
	// local one uses a network namespace where the host allows).
	Network bool
	// MaxOutput bounds the bytes kept per stream (head plus tail); default 1 MiB.
	MaxOutput int64
	// KillOutput kills the command once it has written this many raw bytes in
	// total; default 256 MiB. A runaway printer is stopped long before a
	// timeout would, and its output never has to fit in memory.
	KillOutput int64
	// Grace is the delay between SIGTERM and SIGKILL; default 2 s.
	Grace time.Duration
	// Limits are the resource limits.
	Limits Limits
	// Marker identifies this run's processes: it is exported to them as
	// SLEIPNIR_ENV_RUN so stragglers that escaped the process group can be found
	// and killed (Linux only).
	Marker string
}

const (
	defaultMaxOutput  = 1 << 20
	defaultKillOutput = 256 << 20
	defaultGrace      = 2 * time.Second
)

func (p ExecPolicy) withDefaults() ExecPolicy {
	if p.MaxOutput <= 0 {
		p.MaxOutput = defaultMaxOutput
	}
	if p.KillOutput <= 0 {
		p.KillOutput = defaultKillOutput
	}
	if p.Grace <= 0 {
		p.Grace = defaultGrace
	}
	return p
}

type policyKey struct{}

// WithExecPolicy attaches a policy to ctx for the Sandbox that runs the command.
func WithExecPolicy(ctx context.Context, p ExecPolicy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}

// ExecPolicyFrom returns the policy attached to ctx and whether there was one.
func ExecPolicyFrom(ctx context.Context) (ExecPolicy, bool) {
	p, ok := ctx.Value(policyKey{}).(ExecPolicy)
	return p, ok
}

// MarkerEnv is the environment variable that carries ExecPolicy.Marker.
const MarkerEnv = "SLEIPNIR_ENV_RUN"

// ---- LocalSandbox ----

// LocalSandbox runs commands as child processes of the harness, each in its own
// session and process group so that the whole tree can be killed together.
// Optional hardening is applied when the host offers it: a fresh network
// namespace (Linux, unshare) and setrlimit limits (prlimit). Neither is a
// substitute for a container.
type LocalSandbox struct {
	shell    string
	prlimit  string
	netIso   NetIsolation
	defaults ExecPolicy
	warnings []string
}

// LocalSandboxOptions configures NewLocalSandbox.
type LocalSandboxOptions struct {
	// Shell runs command lines as `Shell -c cmd`; default /bin/sh (POSIX, so a
	// task's commands mean the same on every host).
	Shell string
	// Defaults is the policy used when the context carries none.
	Defaults ExecPolicy
	// DisableNetIsolation and DisableRlimits skip the corresponding probes.
	DisableNetIsolation bool
	DisableRlimits      bool
	// LookPath and Probe let tests substitute the host; nil means exec.LookPath
	// and running the command with a short timeout.
	LookPath func(string) (string, error)
	Probe    func(ctx context.Context, name string, args ...string) error
}

// NewLocalSandbox probes the host once and returns a ready sandbox.
func NewLocalSandbox(o LocalSandboxOptions) (*LocalSandbox, error) {
	look := o.LookPath
	if look == nil {
		look = exec.LookPath
	}
	probe := o.Probe
	if probe == nil {
		probe = runProbe
	}
	s := &LocalSandbox{defaults: o.Defaults}
	shell := o.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	if _, err := os.Stat(shell); err != nil {
		p, lerr := look("sh")
		if lerr != nil {
			return nil, fmt.Errorf("no usable shell (%s): %w", shell, err)
		}
		shell = p
	}
	s.shell = shell
	if !o.DisableRlimits {
		if p, err := look("prlimit"); err == nil {
			s.prlimit = p
		}
	}
	if !o.DisableNetIsolation {
		s.netIso = detectNetIsolation(look, probe)
		if !s.netIso.Available {
			s.warnings = append(s.warnings, "network isolation unavailable, commands of tasks with network=false run with host networking: "+s.netIso.Reason)
		}
	}
	return s, nil
}

// Warnings lists the degradations found at construction (each is also added to
// the ExecResult of commands that were affected).
func (s *LocalSandbox) Warnings() []string { return append([]string(nil), s.warnings...) }

// NetIsolation reports the network isolation capability.
func (s *LocalSandbox) NetIsolation() NetIsolation { return s.netIso }

func runProbe(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// argv assembles the command line for cmd: [unshare ...] [prlimit ...] sh -c cmd.
// It is separate from Exec so the wrapping can be tested without running anything.
func (s *LocalSandbox) argv(cmd string, pol ExecPolicy) (argv []string, warnings []string) {
	inner := []string{s.shell, "-c", cmd}
	switch {
	case s.prlimit != "":
		wrap := append([]string{s.prlimit}, rlimitArgs(pol.Limits)...)
		inner = append(append(wrap, "--"), inner...)
	default:
		// No prlimit binary: core dumps are the one limit the shell can set
		// portably (ulimit -f is 512- or 1024-byte units depending on the shell).
		if !pol.Limits.Core {
			inner = append([]string{s.shell, "-c", `ulimit -c 0 2>/dev/null; exec "$@"`, "sleipnir-limits"}, inner...)
		}
	}
	if pol.Network {
		return inner, nil
	}
	if s.netIso.Available {
		return s.netIso.wrap(s.shell, inner), nil
	}
	if s.netIso.Reason != "" {
		warnings = append(warnings, "network isolation unavailable: "+s.netIso.Reason)
	}
	return inner, warnings
}

func rlimitArgs(l Limits) []string {
	var a []string
	if !l.Core {
		a = append(a, "--core=0")
	}
	fs := l.FileSize
	if fs == 0 {
		fs = DefaultFileSizeLimit
	}
	if fs > 0 {
		a = append(a, fmt.Sprintf("--fsize=%d", fs))
	}
	if l.OpenFiles > 0 {
		a = append(a, fmt.Sprintf("--nofile=%d", l.OpenFiles))
	}
	if l.Procs > 0 {
		a = append(a, fmt.Sprintf("--nproc=%d", l.Procs))
	}
	if l.CPU > 0 {
		a = append(a, fmt.Sprintf("--cpu=%d", int64((l.CPU+time.Second-1)/time.Second)))
	}
	if l.AddressSpace > 0 {
		a = append(a, fmt.Sprintf("--as=%d", l.AddressSpace))
	}
	return a
}

// Exec implements Sandbox.
func (s *LocalSandbox) Exec(ctx context.Context, dir string, env []string, cmd string, timeout time.Duration) (ExecResult, error) {
	pol, ok := ExecPolicyFrom(ctx)
	if !ok {
		pol = s.defaults
	}
	pol = pol.withDefaults()
	argv, warns := s.argv(cmd, pol)
	res := ExecResult{ExitCode: -1, Warnings: warns}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if strings.ContainsRune(cmd, 0) {
		return res, errors.New("command contains a NUL byte")
	}

	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = dir
	// A nil Env would inherit the harness's environment, secrets included.
	c.Env = append([]string{}, env...)
	configureProc(c)

	outR, outW, err := os.Pipe()
	if err != nil {
		return res, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		return res, err
	}
	// Plain *os.File pipes rather than exec's own copying goroutines: Wait then
	// returns as soon as the leader exits, even if a background grandchild still
	// holds the write end open.
	c.Stdout, c.Stderr = outW, errW
	start := time.Now()
	if err := c.Start(); err != nil {
		_ = outR.Close()
		_ = outW.Close()
		_ = errR.Close()
		_ = errW.Close()
		return res, fmt.Errorf("start %s: %w", argv[0], err)
	}
	_ = outW.Close()
	_ = errW.Close()
	pid := c.Process.Pid

	outBuf, errBuf, allBuf := newCapBuf(pol.MaxOutput), newCapBuf(pol.MaxOutput), newCapBuf(pol.MaxOutput)
	var total atomic.Int64
	overflow := make(chan struct{})
	var overflowOnce sync.Once
	var pumps sync.WaitGroup
	pump := func(r *os.File, own *capBuf) {
		defer pumps.Done()
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				_, _ = own.Write(buf[:n])
				_, _ = allBuf.Write(buf[:n])
				if total.Add(int64(n)) > pol.KillOutput {
					overflowOnce.Do(func() { close(overflow) })
				}
			}
			if err != nil {
				return
			}
		}
	}
	pumps.Add(2)
	go pump(outR, outBuf)
	go pump(errR, errBuf)

	waitCh := make(chan error, 1)
	go func() { waitCh <- c.Wait() }()

	var timeoutC <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timeoutC = t.C
	}
	select {
	case <-waitCh:
	case <-timeoutC:
		res.TimedOut = true
		terminateGroup(pid, waitCh, pol.Grace)
	case <-ctx.Done():
		res.Canceled = true
		terminateGroup(pid, waitCh, pol.Grace)
	case <-overflow:
		res.OutputKilled = true
		terminateGroup(pid, waitCh, pol.Grace)
	}
	// The leader is gone. Whatever it left running dies with it: a verifier
	// must not leave a daemon behind, and neither may setup.
	killGroup(pid)

	// EOF on the pipes arrives once every writer is dead; bound the wait for a
	// straggler that escaped the group and closed nothing.
	done := make(chan struct{})
	go func() { pumps.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = outR.Close()
		_ = errR.Close()
		<-done
	}
	_ = outR.Close()
	_ = errR.Close()

	res.Duration = time.Since(start)
	res.ExitCode, res.Signal = exitInfo(c.ProcessState)
	res.Stdout, res.Stderr, res.Output = outBuf.String(), errBuf.String(), allBuf.String()
	res.Truncated = outBuf.truncated() || errBuf.truncated() || allBuf.truncated()
	if pol.Marker != "" && (res.TimedOut || res.Canceled || res.OutputKilled) {
		sweepMarker(pol.Marker)
	}
	return res, nil
}

// terminateGroup asks the group to stop, then kills it, and waits for the
// leader to be reaped.
func terminateGroup(pid int, waitCh <-chan error, grace time.Duration) {
	signalGroup(pid, sigTerm)
	select {
	case <-waitCh:
		return
	case <-time.After(grace):
	}
	killGroup(pid)
	select {
	case <-waitCh:
	case <-time.After(10 * time.Second):
		// An uninterruptible process (a hung NFS mount): give up rather than hang
		// the whole run. The kernel will reap it when it can.
	}
}

// ---- capped output ----

// capBuf keeps the first max/2 and the last max/2 bytes written to it and
// counts what fell in between. It never holds more than max bytes, however
// much the process prints.
type capBuf struct {
	mu      sync.Mutex
	half    int
	head    []byte
	tail    []byte
	total   int64
	dropped int64
}

func newCapBuf(max int64) *capBuf {
	h := int(max / 2)
	if h < 1 {
		h = 1
	}
	return &capBuf{half: h}
}

func (b *capBuf) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(n)
	if room := b.half - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if len(p) >= b.half {
		b.dropped += int64(len(b.tail)) + int64(len(p)-b.half)
		b.tail = append(b.tail[:0], p[len(p)-b.half:]...)
		return n, nil
	}
	b.tail = append(b.tail, p...)
	if over := len(b.tail) - b.half; over > 0 {
		b.dropped += int64(over)
		copy(b.tail, b.tail[over:])
		b.tail = b.tail[:b.half]
	}
	return n, nil
}

func (b *capBuf) truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped > 0
}

func (b *capBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var sb strings.Builder
	sb.Write(b.head)
	if b.dropped > 0 {
		fmt.Fprintf(&sb, "\n[... %d bytes of output dropped ...]\n", b.dropped)
	}
	sb.Write(b.tail)
	return strings.ToValidUTF8(sb.String(), string(utf8.RuneError))
}
