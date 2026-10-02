// Package shell provides the command-execution tools: bash (foreground or
// background), bash_output and bash_kill.
//
// One Manager is shared by every agent of a session. It owns what must outlive
// a single tool call: background jobs, each agent's remembered working
// directory and the process groups that have to be killed at shutdown. The tool
// values themselves are stateless and safe to call from many agent goroutines.
package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// maxCommandBytes keeps a command inside what execve accepts as one argument
// (128 KiB on Linux) with room for the wrapper. Larger scripts belong in a file.
const maxCommandBytes = 100_000

// Register adds bash, bash_output and bash_kill to r. A nil Manager gets a
// fresh one, which is only sensible in tests: callers that need Shutdown must
// keep the Manager they pass.
func Register(r *tools.Registry, m *Manager) {
	if m == nil {
		m = NewManager()
	}
	r.Register(&bashTool{m: m})
	r.Register(&outputTool{m: m})
	r.Register(&killTool{m: m})
}

type bashTool struct{ m *Manager }

func (*bashTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "bash",
		Description: "Run a shell command (bash -c) and return its combined stdout and stderr followed by the exit code. " +
			"Each agent's working directory persists between calls. stdin is empty, so never run anything that waits for input. " +
			"Long output is truncated and the result names a handle for paging through the rest. " +
			"Set run_in_background for servers, watchers and long jobs: it returns a job id for bash_output and bash_kill. " +
			"Use the dedicated tools for reading, searching and editing files, and chain steps with && to save calls.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"command":{"type":"string","description":"Shell command to run"},` +
			`"timeout":{"type":"number","description":"Seconds before the command is killed; omit for the default"},` +
			`"description":{"type":"string","description":"Short summary of what the command does"},` +
			`"run_in_background":{"type":"boolean","description":"Start the command as a job and return its id immediately"}},` +
			`"required":["command"]}`),
	}
}

func (t *bashTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	if c.Env == nil {
		c.Env = &tools.Env{}
	}
	env := c.Env.Defaults()

	a, err := parseArgs(c.Input)
	if err != nil {
		return fail(env, "bash: %v", err), nil
	}
	known := []string{"command", "timeout", "description", "run_in_background"}
	command, present, err := a.str("command")
	switch {
	case err != nil:
		return fail(env, "bash: %v", err), nil
	case !present:
		return fail(env, "bash: %v", a.missing("command", known...)), nil
	case strings.TrimSpace(command) == "":
		return fail(env, "bash: the command is empty"), nil
	case len(command) > maxCommandBytes:
		return fail(env, "bash: the command is %d bytes; the limit is %d. Write it to a script file and run that instead", len(command), maxCommandBytes), nil
	case strings.IndexByte(command, 0) >= 0:
		return fail(env, "bash: the command contains a NUL byte"), nil
	}
	timeoutSec, hasTimeout, err := a.num("timeout")
	if err != nil {
		return fail(env, "bash: %v", err), nil
	}
	if hasTimeout && timeoutSec < 0 {
		return fail(env, "bash: field \"timeout\" must be positive seconds, got %v", timeoutSec), nil
	}
	description, _, err := a.str("description")
	if err != nil {
		return fail(env, "bash: %v", err), nil
	}
	background, _, err := a.boolean("run_in_background")
	if err != nil {
		return fail(env, "bash: %v", err), nil
	}

	m := t.m
	base, err := baseDir(env)
	if err != nil {
		return fail(env, "bash: %v", err), nil
	}
	sh, err := m.shell()
	if err != nil {
		return fail(env, "bash: %v", err), nil
	}

	if msg := missingLeadingCd(command, m.startDir(env, base), env.Root); msg != "" {
		return fail(env, "bash: %s", msg), nil
	}

	// The permission engine resolves relative paths in the command against the
	// directory it will actually run in (the agent's tracked cwd, which persists
	// across calls), not against the session root.
	dec := env.Perm.Check(ctx, perm.Request{
		Agent:   env.Agent,
		Role:    env.Role,
		Tool:    "bash",
		Input:   c.Input,
		Summary: summary(description, command),
		Command: command,
		Cwd:     m.startDir(env, base),
		Writes:  true,
	})
	if !dec.Allow {
		if dec.Reason != "" {
			return fail(env, "permission denied: %s", dec.Reason), nil
		}
		return fail(env, "permission denied"), nil
	}

	if err := m.begin(); err != nil {
		return fail(env, "bash: %v", err), nil
	}
	if background {
		// The job starts where the agent currently is (but its own `cd` never
		// moves the agent), and its supervisor owns the wait-group slot from here on.
		return m.startJob(env, sh, command, m.startDir(env, base), backgroundTimeout(timeoutSec, hasTimeout)), nil
	}
	defer m.end()
	return m.runForeground(ctx, env, sh, command, base, foregroundTimeout(timeoutSec, hasTimeout, env.Limits)), nil
}

// fail builds a model-visible error result through Finish, so even an
// unexpectedly long reason is bounded like every other output.
func fail(env *tools.Env, format string, args ...any) *tools.Result {
	return env.Finish(fmt.Sprintf(format, args...), true)
}

// summary is the one line humans see when asked to approve the command.
func summary(description, command string) string {
	s := firstLine(description)
	if s == "" {
		s = firstLine(command)
	}
	if utf8.RuneCountInString(s) > 200 {
		s = string([]rune(s)[:200]) + "…"
	}
	return s
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// foregroundTimeout applies the default and the cap. Zero limits (a partially
// filled Env.Limits) fall back to the package defaults rather than meaning
// "no timeout".
func foregroundTimeout(sec float64, has bool, lim tools.Limits) time.Duration {
	def := tools.DefaultLimits()
	if lim.DefaultTimeout <= 0 {
		lim.DefaultTimeout = def.DefaultTimeout
	}
	if lim.MaxTimeout <= 0 {
		lim.MaxTimeout = def.MaxTimeout
	}
	d := lim.DefaultTimeout
	if has && sec > 0 {
		d = secondsToDuration(sec, lim.MaxTimeout)
	}
	return min(d, lim.MaxTimeout)
}

// timeoutAdvice says what to do about a command that needed longer. Told only "timed out", a model runs the same command
// again, or asks for a timeout the cap quietly lowers.
func timeoutAdvice(ran time.Duration, lim tools.Limits) string {
	limit := lim.MaxTimeout
	if limit <= 0 {
		limit = tools.DefaultLimits().MaxTimeout
	}
	if ran < limit {
		return "[it needs longer: pass timeout (at most " + fmtSeconds(limit) + ") or start it with run_in_background and read its output with bash_output]"
	}
	return "[it needs longer than the limit of " + fmtSeconds(limit) + ": start it with run_in_background and read its output with bash_output]"
}

// backgroundTimeout is 0 (no deadline) unless the model asked for one: a dev
// server that dies after the foreground default would be useless.
func backgroundTimeout(sec float64, has bool) time.Duration {
	if !has || sec <= 0 {
		return 0
	}
	return secondsToDuration(sec, 24*time.Hour)
}

func secondsToDuration(sec float64, ceiling time.Duration) time.Duration {
	if sec >= ceiling.Seconds() {
		return ceiling
	}
	return time.Duration(sec * float64(time.Second))
}

func fmtSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GiB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// runForeground runs the command to completion (or death) and builds the
// result. The caller holds a Manager wait-group slot for the duration.
func (m *Manager) runForeground(ctx context.Context, env *tools.Env, sh shellInfo, command, base string, timeout time.Duration) *tools.Result {
	dir := m.startDir(env, base)

	script := command
	var cwdFile string
	if sh.posix {
		if f, err := os.CreateTemp("", "sleipnir-cwd-*"); err == nil {
			cwdFile = f.Name()
			f.Close()
			defer os.Remove(cwdFile)
			script = wrapScript(command, cwdFile)
		}
	}

	sk := newFGSink(env.Out, m.opts.CaptureBytes)
	began := env.Now()
	p, err := m.startProc(procSpec{
		path:   sh.path,
		args:   append(append([]string(nil), sh.flags...), script),
		dir:    dir,
		env:    m.env(env.Agent, dir),
		sink:   sk,
		maxOut: m.opts.MaxOutputBytes,
		merge:  m.opts.MergeStreams,
	})
	if err != nil {
		return fail(env, "bash: could not start the command: %v", err)
	}

	reason := p.supervise(ctx, timeout)
	p.waitPumps(m.opts.DrainGrace)
	// Whatever is still writing now is a leftover background process; it keeps
	// running (Shutdown will end it) but must not touch this result or the UI.
	sk.detach()
	body := sk.snapshot()
	st, exited := p.status()

	var note string
	final := dir
	if cwdFile != "" && reason != killTimeout && reason != killCancel && reason != killShutdown {
		final, note = m.recordCwd(env, base, dir, cwdFile)
	}

	raw, ctrl := p.raw.Load(), p.ctrl.Load()
	var text string
	if raw >= 64 && ctrl*20 >= raw {
		text = fmt.Sprintf("[binary output suppressed: %d bytes]", raw)
	} else {
		text = strings.TrimRight(tidyOutput(body), "\n")
	}

	var marks []string
	if note != "" {
		marks = append(marks, note)
	}
	switch reason {
	case killTimeout:
		marks = append(marks, "[timed out after "+fmtSeconds(timeout)+"]", timeoutAdvice(timeout, env.Limits))
	case killCancel:
		marks = append(marks, "[cancelled]")
	case killShutdown:
		marks = append(marks, "[cancelled: the harness is shutting down]")
	case killOutput:
		marks = append(marks, "[killed: output exceeded "+humanBytes(m.opts.MaxOutputBytes)+"]")
	}
	if exited {
		marks = append(marks, fmt.Sprintf("[exit code %d]", st.code))
	} else {
		marks = append(marks, "[exit code unknown: the process did not exit after SIGKILL]")
	}
	full := strings.Join(marks, "\n")
	if text != "" {
		full = text + "\n" + full
	}

	res := env.Finish(full, reason == killCancel || reason == killShutdown)
	res.Meta = map[string]any{
		"exit_code":   st.code,
		"timed_out":   reason == killTimeout,
		"cwd":         final,
		"duration_ms": env.Now().Sub(began).Milliseconds(),
		"pid":         p.pid,
	}
	if st.signal != 0 {
		res.Meta["signal"] = st.signal
	}
	return res
}
