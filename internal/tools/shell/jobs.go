package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tools"
)

// EventJob is the event type emitted when a background job starts and when it
// ends.
const EventJob = "tool.job"

// JobEvent is the payload of EventJob events. Exit is nil while the job runs.
type JobEvent struct {
	ID       string `json:"id"`
	Agent    string `json:"agent"`
	Command  string `json:"command"`
	Status   string `json:"status"` // "started", "exited" or "killed"
	Exit     *int   `json:"exit,omitempty"`
	Duration int64  `json:"duration_ms,omitempty"`
}

type jobState int

const (
	jobRunning jobState = iota
	jobExited
	jobKilled
)

// job is a background command. Jobs are session-wide: any agent may read or
// kill any job, so nothing here is keyed by the starting agent except the
// per-reader cursors.
type job struct {
	id      string
	agent   string
	command string
	started time.Time
	p       *proc
	buf     *rolling
	emit    events.Emitter
	now     func() time.Time

	finished chan struct{} // closed once the final state is recorded and announced

	mu      sync.Mutex
	state   jobState
	reason  killReason
	exit    int
	cursors map[string]int64 // agent -> offset already read
}

func (j *job) snapshot() (jobState, killReason, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state, j.reason, j.exit
}

func (j *job) statusText() string {
	state, reason, exit := j.snapshot()
	switch state {
	case jobRunning:
		return "running"
	case jobKilled:
		if reason == killTimeout {
			return "killed (timed out)"
		}
		return "killed"
	}
	return fmt.Sprintf("exited %d", exit)
}

func (j *job) cursor(agent string) int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cursors[agent]
}

func (j *job) setCursor(agent string, off int64) {
	j.mu.Lock()
	j.cursors[agent] = off
	j.mu.Unlock()
}

func (j *job) emitEvent(status string, exit *int, dur time.Duration) {
	if j.emit == nil {
		return
	}
	cmd := j.command
	if len(cmd) > 512 { // the full command is already in the tool.call event
		cmd = strings.ToValidUTF8(cmd[:512], "") + "…"
	}
	_, _ = j.emit.Emit(j.agent, EventJob, JobEvent{
		ID: j.id, Agent: j.agent, Command: cmd, Status: status, Exit: exit, Duration: dur.Milliseconds(),
	})
}

// startJob launches command as a background job. It consumes the caller's
// Manager wait-group slot: released here on failure, by the supervisor on
// success.
func (m *Manager) startJob(env *tools.Env, sh shellInfo, command, dir string, timeout time.Duration) *tools.Result {
	m.mu.Lock()
	full := len(m.jobs) >= m.opts.MaxJobs && !m.hasFinishedLocked()
	m.mu.Unlock()
	if full {
		m.end()
		return fail(env, "bash: %d background jobs are already running; stop some with bash_kill first", m.opts.MaxJobs)
	}

	buf := newRolling(m.opts.JobBuffer)
	p, err := m.startProc(procSpec{
		path: sh.path,
		args: append(append([]string(nil), sh.flags...), command),
		dir:  dir,
		env:  commandEnv(os.Environ(), env.Agent, m.opts.PassEnv),
		sink: buf,
	})
	if err != nil {
		m.end()
		return fail(env, "bash: could not start the command: %v", err)
	}

	j := &job{
		agent:    env.Agent,
		command:  command,
		started:  env.Now(),
		p:        p,
		buf:      buf,
		emit:     env.Emit,
		now:      env.Now,
		finished: make(chan struct{}),
		cursors:  map[string]int64{},
	}
	m.mu.Lock()
	for len(m.jobs) >= m.opts.MaxJobs && m.evictOldestFinishedLocked() {
	}
	m.seq++
	j.id = fmt.Sprintf("job_%d", m.seq)
	m.jobs[j.id] = j
	m.order = append(m.order, j.id)
	m.mu.Unlock()

	j.emitEvent("started", nil, 0)
	go m.superviseJob(j, timeout)

	res := env.Finish("job "+j.id+" started", false)
	res.Meta = map[string]any{"job": j.id, "pid": p.pid}
	return res
}

// superviseJob waits for the job to end (killing it on deadline, kill request
// or shutdown), records the outcome and announces it.
func (m *Manager) superviseJob(j *job, timeout time.Duration) {
	defer m.end()
	reason := j.p.supervise(context.Background(), timeout)
	j.p.waitPumps(m.opts.DrainGrace)
	st, _ := j.p.status()

	j.mu.Lock()
	j.exit = st.code
	j.reason = reason
	if reason != killNone {
		j.state = jobKilled
	} else {
		j.state = jobExited
	}
	status := "exited"
	if j.state == jobKilled {
		status = "killed"
	}
	j.mu.Unlock()

	code := st.code
	j.emitEvent(status, &code, j.now().Sub(j.started))
	close(j.finished)
}

func (m *Manager) job(id string) *job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

// unknownJob explains a bad id, listing the known ones so the model can retry
// without another round trip.
func (m *Manager) unknownJob(env *tools.Env, id string) *tools.Result {
	m.mu.Lock()
	ids := append([]string(nil), m.order...)
	m.mu.Unlock()
	sort.Slice(ids, func(i, k int) bool { return jobNum(ids[i]) < jobNum(ids[k]) })
	if len(ids) == 0 {
		return fail(env, "no job %q: no background jobs have been started", clip(id, 40))
	}
	if len(ids) > 10 {
		ids = ids[len(ids)-10:]
	}
	return fail(env, "no job %q; known jobs: %s", clip(id, 40), strings.Join(ids, ", "))
}

func jobNum(id string) int {
	var n int
	fmt.Sscanf(id, "job_%d", &n)
	return n
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

func (m *Manager) hasFinishedLocked() bool {
	for _, j := range m.jobs {
		if st, _, _ := j.snapshot(); st != jobRunning {
			return true
		}
	}
	return false
}

// evictOldestFinishedLocked forgets the oldest job that is no longer running.
func (m *Manager) evictOldestFinishedLocked() bool {
	for i, id := range m.order {
		j := m.jobs[id]
		if j == nil {
			continue
		}
		if st, _, _ := j.snapshot(); st != jobRunning && j.p.pumpsFinished() {
			delete(m.jobs, id)
			m.order = append(m.order[:i:i], m.order[i+1:]...)
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- bash_output

type outputTool struct{ m *Manager }

func (*outputTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "bash_output",
		Description: "Read output from a background bash job. Returns what the job has printed since the byte offset `since` " +
			"(or since your last read when omitted), its status (running, exited N, killed) and the next offset.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","description":"Job id returned by bash, e.g. job_1"},` +
			`"since":{"type":"integer","description":"Byte offset to read from; omit to continue from your last read"}},` +
			`"required":["id"]}`),
		ReadOnly: true,
	}
}

func (t *outputTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	if c.Env == nil {
		c.Env = &tools.Env{}
	}
	env := c.Env.Defaults()
	a, err := parseArgs(c.Input)
	if err != nil {
		return fail(env, "bash_output: %v", err), nil
	}
	id, present, err := a.str("id")
	switch {
	case err != nil:
		return fail(env, "bash_output: %v", err), nil
	case !present || strings.TrimSpace(id) == "":
		return fail(env, "bash_output: %v", a.missing("id", "id", "since")), nil
	}
	since, hasSince, err := a.num("since")
	if err != nil {
		return fail(env, "bash_output: %v", err), nil
	}
	if hasSince && since < 0 {
		return fail(env, "bash_output: field \"since\" must not be negative"), nil
	}

	j := t.m.job(strings.TrimSpace(id))
	if j == nil {
		return t.m.unknownJob(env, strings.TrimSpace(id)), nil
	}
	from := j.cursor(env.Agent)
	if hasSince {
		from = int64(math.Min(math.Floor(since), 9e18))
	}
	data, dropped, next := j.buf.read(from)
	j.setCursor(env.Agent, next)

	var sb strings.Builder
	if dropped > 0 {
		fmt.Fprintf(&sb, "[%d bytes of earlier output were dropped from the job's buffer]\n", dropped)
	}
	if text := strings.TrimRight(tidyOutput(string(data)), "\n"); text != "" {
		sb.WriteString(text)
		sb.WriteByte('\n')
	} else {
		sb.WriteString("(no new output)\n")
	}
	fmt.Fprintf(&sb, "[%s %s; next offset %d]", j.id, j.statusText(), next)

	res := env.Finish(sb.String(), false)
	state, _, exit := j.snapshot()
	res.Meta = map[string]any{"job": j.id, "running": state == jobRunning, "next": next}
	if state != jobRunning {
		res.Meta["exit_code"] = exit
	}
	return res, nil
}

// ------------------------------------------------------------------ bash_kill

type killTool struct{ m *Manager }

func (*killTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "bash_kill",
		Description: "Stop a background bash job and all of its child processes (SIGTERM, then SIGKILL after 2 seconds). " +
			"Safe to call on a job that has already ended.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","description":"Job id returned by bash, e.g. job_1"}},"required":["id"]}`),
	}
}

func (t *killTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	if c.Env == nil {
		c.Env = &tools.Env{}
	}
	env := c.Env.Defaults()
	a, err := parseArgs(c.Input)
	if err != nil {
		return fail(env, "bash_kill: %v", err), nil
	}
	id, present, err := a.str("id")
	switch {
	case err != nil:
		return fail(env, "bash_kill: %v", err), nil
	case !present || strings.TrimSpace(id) == "":
		return fail(env, "bash_kill: %v", a.missing("id", "id")), nil
	}
	m := t.m
	j := m.job(strings.TrimSpace(id))
	if j == nil {
		return m.unknownJob(env, strings.TrimSpace(id)), nil
	}

	state, _, exit := j.snapshot()
	if state != jobRunning {
		verb := "exited"
		if state == jobKilled {
			verb = "killed"
		}
		text := fmt.Sprintf("job %s already %s (exit code %d)", j.id, verb, exit)
		if !j.p.pumpsFinished() {
			// The leader is gone but something it started still holds the
			// output pipes: that is what "kill the job" means now.
			j.p.killTree()
			text += "; stopped leftover processes it had started"
		}
		res := env.Finish(text, false)
		res.Meta = map[string]any{"job": j.id, "exit_code": exit}
		return res, nil
	}

	j.p.requestKill(killRequested)
	wait := time.NewTimer(m.opts.KillGrace + 8*time.Second)
	defer wait.Stop()
	select {
	case <-j.finished:
	case <-wait.C:
	case <-ctx.Done():
	}
	state, _, exit = j.snapshot()
	if state == jobRunning {
		return fail(env, "bash_kill: sent SIGTERM and SIGKILL to %s but it has not exited yet", j.id), nil
	}
	res := env.Finish(fmt.Sprintf("job %s killed (exit code %d)", j.id, exit), false)
	res.Meta = map[string]any{"job": j.id, "exit_code": exit}
	return res, nil
}
