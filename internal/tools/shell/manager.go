package shell

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// Options tunes a Manager. The zero value is what production uses.
type Options struct {
	// PassEnv lists environment variable names (case-insensitive, path.Match
	// wildcards allowed) that are handed to commands even though they look like
	// secrets. Everything else matching the secret pattern is scrubbed.
	PassEnv []string
	// BaseEnv, when non-nil, is the environment commands start from instead of
	// the harness process's own ("K=V" entries). RL rollouts pass a private HOME
	// and TMPDIR with no credentials at all. The secret scrub still runs over it.
	BaseEnv []string
	// Tmp, when set, is the directory commands get as TMPDIR: a scratch place of the
	// session's own, so that a build or test run does not write to the shared /tmp.
	Tmp string
	// Path lists directories put in front of the PATH commands run with (the
	// programs a person located for a plugin, <state>/bin).
	Path []string
	// Wrap is an argv prefix every command runs under: the shell is started as
	// Wrap[0] Wrap[1:]... <shell> <flags> <script>. It is how a rollout is given a
	// network-less namespace (see internal/rl/env NetPrefix) without the shell
	// tools knowing anything about sandboxes.
	Wrap []string
	// Shell overrides shell detection with the path or name of a shell binary.
	Shell string
	// MaxOutputBytes kills a foreground command that writes more than this many
	// bytes (default 1 GiB): a runaway `yes` must not burn CPU until the timeout.
	MaxOutputBytes int64
	// CaptureBytes bounds the memory a foreground command's output may use
	// (default 8 MiB, split between head and tail).
	CaptureBytes int
	// JobBuffer is the rolling output buffer of each background job (default 5 MiB).
	JobBuffer int
	// MaxJobs bounds the jobs remembered by the Manager (default 64). When full,
	// the oldest finished job is forgotten; if all are running, new jobs are refused.
	MaxJobs int
	// MergeStreams gives a command's stdout and stderr one shared pipe. Output
	// then keeps the exact order in which the command wrote it, but live chunks
	// are all reported as "stdout". By default the two streams are read
	// separately (so the UI can tell them apart) and merged in arrival order,
	// which can swap writes made microseconds apart.
	MergeStreams bool
	// KillGrace is how long SIGTERM gets before SIGKILL (default 2s).
	KillGrace time.Duration
	// DrainGrace is how long a finished command waits for its output pipes to
	// close before treating leftover background processes as strays (default 250ms).
	DrainGrace time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxOutputBytes <= 0 {
		o.MaxOutputBytes = 1 << 30
	}
	if o.CaptureBytes <= 0 {
		o.CaptureBytes = 8 << 20
	}
	if o.JobBuffer <= 0 {
		o.JobBuffer = 5 << 20
	}
	if o.MaxJobs <= 0 {
		o.MaxJobs = 64
	}
	if o.KillGrace <= 0 {
		o.KillGrace = 2 * time.Second
	}
	if o.DrainGrace <= 0 {
		o.DrainGrace = 250 * time.Millisecond
	}
	return o
}

// Manager owns everything the shell tools share across agents: the background
// jobs (session-wide, any agent may read or kill any job), each agent's
// remembered working directory, and every process group still alive so that
// Shutdown can end them all. All state is behind mu; the tools themselves hold
// nothing per call.
type Manager struct {
	opts Options
	stop chan struct{} // closed by Shutdown

	shellOnce sync.Once
	sh        shellInfo
	shErr     error

	mu     sync.Mutex
	closed bool
	seq    int
	jobs   map[string]*job
	order  []string // job ids, oldest first
	cwds   map[string]cwdState
	procs  map[*proc]struct{}
	// lingering are the process groups of finished commands that still had a live member when the command ended: a server a
	// command started with "nohup … &" and its output sent to a file holds no pipe of the harness, so nothing else keeps it in
	// view. Shutdown ends them, and EndAgent the ones its agent's foreground commands left.
	lingering map[int]lingerer
	wg        sync.WaitGroup // processes being supervised
}

// NewManager returns a Manager. Options are optional; when several are given
// their PassEnv lists are concatenated and later non-zero fields win.
func NewManager(opts ...Options) *Manager {
	var o Options
	for _, x := range opts {
		o.PassEnv = append(o.PassEnv, x.PassEnv...)
		if x.BaseEnv != nil {
			o.BaseEnv = x.BaseEnv
		}
		if len(x.Wrap) > 0 {
			o.Wrap = append([]string(nil), x.Wrap...)
		}
		if x.Shell != "" {
			o.Shell = x.Shell
		}
		if len(x.Path) > 0 {
			o.Path = x.Path
		}
		if x.Tmp != "" {
			o.Tmp = x.Tmp
		}
		if x.MaxOutputBytes != 0 {
			o.MaxOutputBytes = x.MaxOutputBytes
		}
		if x.CaptureBytes != 0 {
			o.CaptureBytes = x.CaptureBytes
		}
		if x.JobBuffer != 0 {
			o.JobBuffer = x.JobBuffer
		}
		if x.MaxJobs != 0 {
			o.MaxJobs = x.MaxJobs
		}
		if x.MergeStreams {
			o.MergeStreams = true
		}
		if x.KillGrace != 0 {
			o.KillGrace = x.KillGrace
		}
		if x.DrainGrace != 0 {
			o.DrainGrace = x.DrainGrace
		}
	}
	return &Manager{
		opts:  o.withDefaults(),
		stop:  make(chan struct{}),
		jobs:  map[string]*job{},
		cwds:  map[string]cwdState{},
		procs: map[*proc]struct{}{}, lingering: map[int]lingerer{},
	}
}

var errShutDown = errors.New("the shell manager has been shut down")

// baseEnv is the environment commands are built from. Credentials that
// harden.MoveKeys took out of the process environment are not in os.Environ; the
// ones the operator listed in PassEnv are put back, so moving keys away from the
// harness's other children does not take away what PassEnv promised these ones.
func (m *Manager) baseEnv() []string {
	if m.opts.BaseEnv != nil {
		return m.opts.BaseEnv
	}
	env := os.Environ()
	for _, name := range harden.Held() {
		if passAllowed(name, m.opts.PassEnv) {
			env = append(env, name+"="+harden.Secret(name))
		}
	}
	return env
}

// begin registers a process about to be supervised; it fails after Shutdown.
// Checking and adding under one lock is what makes Shutdown's Wait safe.
func (m *Manager) begin() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errShutDown
	}
	m.wg.Add(1)
	return nil
}

// end releases one active-operation count previously acquired from the manager.
func (m *Manager) end() { m.wg.Done() }

// track adds a running process to the manager's shutdown registry under its lock.
func (m *Manager) track(p *proc) {
	m.mu.Lock()
	m.procs[p] = struct{}{}
	m.mu.Unlock()
}

// untrack removes a process from the manager's shutdown registry under its lock. A process group that still has a live member
// once its command has ended is kept in view (lingering) for Shutdown; the groups recorded before that are gone by now are
// forgotten, so that the record holds no id a later, unrelated group may take.
func (m *Manager) untrack(p *proc) {
	lingers := probesGroup && groupAlive(p.pid) // the real check: p.alive is a test's stand-in for killTree
	m.mu.Lock()
	delete(m.procs, p)
	gone := make([]int, 0, len(m.lingering))
	for g := range m.lingering {
		gone = append(gone, g)
	}
	m.mu.Unlock()
	for i := 0; i < len(gone); {
		if groupAlive(gone[i]) {
			gone = append(gone[:i], gone[i+1:]...)
			continue
		}
		i++
	}
	m.mu.Lock()
	for _, g := range gone {
		delete(m.lingering, g)
	}
	if lingers && !m.closed {
		m.lingering[p.pid] = lingerer{agent: p.agent, job: p.job}
	}
	m.mu.Unlock()
}

// lingerer says who left a lingering process group: the agent whose command started it, and whether that command was a
// background job.
type lingerer struct {
	agent string
	job   bool
}

// EndAgent ends what agent's finished foreground commands left running: a server started with "server &" or
// "nohup server >log 2>&1 &" inside an ordinary command, whether it still holds that command's output pipes or not. The host
// calls it when the agent's run ends (a team worker's run, internal/swarm Config.RunEnded), so a port such a server holds is
// free for the next command that needs it. Background jobs (run_in_background) and their processes are session-wide and are
// left alone: bash_kill and Shutdown end them. A command of the agent's that is still running is left alone too.
func (m *Manager) EndAgent(agent string) {
	if agent == "" {
		return
	}
	m.mu.Lock()
	var strays []*proc
	for p := range m.procs {
		if p.agent == agent && !p.job && p.leaderDone() {
			strays = append(strays, p)
		}
	}
	var groups []int
	for g, l := range m.lingering {
		if l.agent == agent && !l.job {
			groups = append(groups, g)
			delete(m.lingering, g)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range strays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !p.pumpsFinished() {
				p.killTree()
			} else {
				// the command's pipes are closed but untrack has not run yet: what it left would join lingering after
				// the look above
				m.endGroup(p.pid)
			}
			p.closeReaders()
		}()
	}
	for _, g := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.endGroup(g)
		}()
	}
	wg.Wait()
}

// Shutdown kills every background job and every command still running, then
// reaps leftovers (background processes a foreground command left holding its
// pipes). It is idempotent and safe to call concurrently with tool calls,
// which fail once it has started.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	first := !m.closed
	m.closed = true
	if first {
		close(m.stop)
	}
	m.mu.Unlock()

	m.wg.Wait() // supervisors observe m.stop, kill their trees and return

	m.mu.Lock()
	stray := make([]*proc, 0, len(m.procs))
	for p := range m.procs {
		stray = append(stray, p)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range stray {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !p.leaderDone() || !p.pumpsFinished() {
				p.killTree()
			}
			p.closeReaders()
		}()
	}
	m.mu.Lock()
	groups := make([]int, 0, len(m.lingering))
	for g := range m.lingering {
		groups = append(groups, g)
	}
	m.lingering = map[int]lingerer{}
	m.mu.Unlock()
	for _, g := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.endGroup(g)
		}()
	}
	wg.Wait()
}

// endGroup ends a lingering process group (Manager.lingering) as killTree ends a command's: SIGTERM, then SIGKILL for what is
// still alive after the grace period, then a bounded wait for the group to be gone. A group with no live member is left alone.
func (m *Manager) endGroup(pgid int) {
	if !groupAlive(pgid) {
		return
	}
	termGroup(pgid)
	grace := time.NewTimer(m.opts.KillGrace)
	defer grace.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for groupAlive(pgid) {
		select {
		case <-grace.C:
			killGroup(pgid)
			deadline := time.Now().Add(5 * time.Second)
			for groupAlive(pgid) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			return
		case <-tick.C:
		}
	}
}

// shellInfo says how to run a script with the detected shell.
type shellInfo struct {
	path  string
	flags []string // placed before the script
	posix bool     // understands the EXIT-trap wrapper used for cwd tracking
}

// shell detects the configured shell once and returns the cached result or detection error.
func (m *Manager) shell() (shellInfo, error) {
	m.shellOnce.Do(func() { m.sh, m.shErr = detectShell(m.opts.Shell) })
	return m.sh, m.shErr
}

// RuntimeContext describes the host and the shell cached for command execution.
// It resolves the executable without running it and omits machine-specific paths.
// The result is stable for the lifetime of the manager and shared by all agents.
func (m *Manager) RuntimeContext() string {
	prefix := "Operating system: " + runtime.GOOS + ".\nCommand shell: "
	sh, err := m.shell()
	if err != nil {
		return prefix + "unavailable. Shell tool calls will fail; file tools remain available."
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(sh.path)), ".exe")
	var dialect, guidance string
	switch name {
	case "bash", "sh", "dash", "ksh", "zsh":
		dialect = name
		guidance = "Use this shell's syntax."
	case "pwsh":
		dialect = "PowerShell (pwsh)"
		guidance = "Use PowerShell syntax. Unix utilities such as grep may be unavailable."
	case "powershell":
		dialect = "Windows PowerShell (powershell)"
		guidance = "Use PowerShell syntax. The && and || operators are unavailable; run dependent commands separately and check their exit codes. Unix utilities such as grep may be unavailable."
	case "cmd":
		dialect = "cmd.exe"
		guidance = "Use cmd syntax. Unix utilities such as grep may be unavailable."
	default:
		dialect = "custom shell (-c)"
		guidance = "Check its supported syntax before using shell-specific features."
	}
	text := prefix + dialect + ".\nThe bash tool runs commands directly in this shell; send commands without an extra shell wrapper. " + guidance
	if m.opts.Tmp != "" {
		// The directory itself is not named: its path holds the session's id, and this text is shared by every session.
		text += "\n" + ScratchGuidance
	}
	return text
}

// ScratchGuidance is the runtime context's sentence about scratch files, in a session whose commands get a TMPDIR of their own
// (Options.Tmp). The permission engine allows writes there and asks before a write elsewhere outside the workspace, naming the directory.
const ScratchGuidance = "Scratch files go in $TMPDIR (the TMPDIR environment variable: a directory private to this session, writable by every agent; the file tools take $TMPDIR/name too); /tmp is outside the workspace and asks."

func detectShell(override string) (shellInfo, error) {
	candidates := []string{"bash", "sh"}
	if runtime.GOOS == "windows" {
		candidates = []string{"pwsh", "powershell", "cmd"}
	}
	if override != "" {
		candidates = []string{override}
	}
	for _, c := range candidates {
		path, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
		switch name {
		case "pwsh", "powershell":
			return shellInfo{path: path, flags: []string{"-NoProfile", "-NonInteractive", "-Command"}}, nil
		case "cmd":
			return shellInfo{path: path, flags: []string{"/C"}}, nil
		default:
			return shellInfo{path: path, flags: []string{"-c"}, posix: true}, nil
		}
	}
	return shellInfo{}, errors.New("no usable shell found (looked for " + strings.Join(candidates, ", ") + ")")
}

// env is the environment of a command of agent in dir.
func (m *Manager) env(agent, dir string) []string {
	env := commandEnv(m.baseEnv(), agent, dir, m.opts.PassEnv)
	if len(m.opts.Path) > 0 {
		env = withPath(env, m.opts.Path)
	}
	if m.opts.Tmp == "" {
		return env
	}
	out := env[:0:0]
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name != "TMPDIR" {
			out = append(out, kv)
		}
	}
	return append(out, "TMPDIR="+m.opts.Tmp)
}

// withPath puts dirs in front of env's PATH (whatever its case, for Windows' Path).
func withPath(env, dirs []string) []string {
	front := strings.Join(dirs, string(os.PathListSeparator))
	out := make([]string, 0, len(env)+1)
	done := false
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		if !done && strings.EqualFold(name, "PATH") {
			if val != "" {
				val = front + string(os.PathListSeparator) + val
			} else {
				val = front
			}
			kv, done = name+"="+val, true
		}
		out = append(out, kv)
	}
	if !done {
		out = append(out, "PATH="+front)
	}
	return out
}
