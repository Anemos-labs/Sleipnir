package acpserve

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/mcpserve"
)

// Turn states.
const (
	turnRunning   = "running"
	turnDone      = "done"
	turnFailed    = "failed"
	turnCancelled = "cancelled"
)

const (
	maxChunkRunes    = 8000 // one streamed text chunk
	maxToolOutput    = 4096 // tool result text shown to the ACP client
	maxStderrTail    = 8 << 10
	maxRunEventBytes = 1 << 20 // one line of `sleipnir run --json`
	killAfter        = 2 * time.Minute
)

// runEvent is the part of a `sleipnir run --json` event the ACP adapter reads.
type runEvent struct {
	Type    string          `json:"type"`
	Agent   string          `json:"agent"`
	ID      string          `json:"id"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Failed  bool            `json:"failed"`
	Output  string          `json:"output"`
	Level   string          `json:"level"`
	Message string          `json:"message"`
	Stop    string          `json:"stop"`
	Usage   struct {
		InputTokens     int `json:"input_tokens"`
		CacheReadTokens int `json:"cache_read_tokens"`
		OutputTokens    int `json:"output_tokens"`
		CacheWrite5m    int `json:"cache_write_5m_tokens"`
		CacheWrite1h    int `json:"cache_write_1h_tokens"`
	} `json:"usage"`
	CostUSD float64 `json:"cost_usd"`
	Error   string  `json:"error"`
	Session string  `json:"session"`
}

// Job is one ACP prompt turn: a child `sleipnir run --json` and the ACP
// notifications translated from its event stream.
type Job struct {
	sess     *Session
	srv      *Server
	goal     string
	args     []string
	dir      string
	cmd      *exec.Cmd
	done     chan struct{}
	doneOnce sync.Once

	mu         sync.Mutex
	cancelled  bool
	state      string
	stopReason string
	gen        map[string]int
	tools      map[string]string
	streamed   map[string]bool
	result     map[string]any
	lastUsed   int
	exitErr    string
	stderr     tailBuffer
}

// start launches the run and its reader goroutine. A cancellation that
// arrives before the launch settles the turn without starting a child.
func (j *Job) start() error {
	j.mu.Lock()
	cancelled := j.cancelled
	j.mu.Unlock()
	if cancelled {
		j.finish(nil)
		j.closeDone()
		return nil
	}
	cmd := exec.Command(j.srv.cfg.Exe, j.args...)
	cmd.Dir = j.dir
	cmd.Env = j.srv.cfg.Env
	if cmd.Env == nil {
		cmd.Env = mcpserve.Environ()
	}
	cmd.Stdin = strings.NewReader(j.goal)
	cmd.Stderr = &j.stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		j.finish(err)
		j.closeDone()
		return err
	}
	var started func(error)
	if j.srv.cfg.Prepare != nil {
		started = j.srv.cfg.Prepare(cmd)
	}
	if err := cmd.Start(); err != nil {
		if started != nil {
			started(err)
		}
		j.finish(err)
		j.closeDone()
		return err
	}
	if started != nil {
		started(nil)
	}
	j.mu.Lock()
	j.cmd = cmd
	j.state = turnRunning
	cancelled = j.cancelled
	j.mu.Unlock()
	if cancelled {
		j.terminate(cmd)
	}
	go func() {
		j.read(stdout)
		werr := cmd.Wait()
		j.finish(werr)
		j.closeDone()
	}()
	return nil
}

// closeDone publishes completion exactly once, including for launch failures.
func (j *Job) closeDone() { j.doneOnce.Do(func() { close(j.done) }) }

// cancel asks the run to end as Ctrl-C ends `sleipnir run`: SIGTERM first, on
// systems without signals a kill, and a kill after the grace period. It is
// idempotent and is safe before the child has started.
func (j *Job) cancel() {
	j.mu.Lock()
	if j.cancelled || j.state != turnRunning {
		j.mu.Unlock()
		return
	}
	j.cancelled = true
	cmd := j.cmd
	j.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		j.terminate(cmd)
	}
}

// terminate signals a started child and kills it if it is still there after
// killAfter, leaving an isolated team's verified work time to land.
func (j *Job) terminate(cmd *exec.Cmd) {
	p := cmd.Process
	if err := p.Signal(syscall.SIGTERM); err != nil {
		_ = p.Kill()
	}
	go func() {
		t := time.NewTimer(killAfter)
		defer t.Stop()
		select {
		case <-j.done:
		case <-t.C:
			_ = p.Kill()
		}
	}()
}

// read digests the run's JSON event stream until it ends.
func (j *Job) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := mcpserve.ReadLine(br, maxRunEventBytes)
		if len(line) > 0 {
			j.event(line)
		}
		if err != nil {
			return
		}
	}
}

// event turns one run event into one or more ACP updates.
func (j *Job) event(line []byte) {
	var ev runEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	id := ev.ID
	if id == "" {
		id = "auto"
	}
	switch ev.Type {
	case "text":
		if ev.Text == "" {
			return
		}
		mid := j.messageID(ev.Agent, false)
		j.markStreamed(ev.Agent)
		j.update(map[string]any{
			"sessionUpdate": "agent_message_chunk", "messageId": mid,
			"content": textBlock(j.runeClip(ev.Text, maxChunkRunes)),
			"_meta":   map[string]any{"sleipnir/agent": agentName(ev.Agent)},
		})
	case "thinking":
		if ev.Text == "" {
			return
		}
		mid := j.messageID(ev.Agent, true)
		j.update(map[string]any{
			"sessionUpdate": "agent_thought_chunk", "messageId": mid,
			"content": textBlock(j.runeClip(ev.Text, maxChunkRunes)),
		})
	case "reset":
		j.mu.Lock()
		j.gen[agentName(ev.Agent)]++
		j.mu.Unlock()
	case "response":
		j.mu.Lock()
		j.gen[agentName(ev.Agent)]++
		j.mu.Unlock()
		j.usage(ev)
	case "tool_start":
		kind := toolKind(ev.Name)
		title := ev.Name
		if gist := inputGist(ev.Input); gist != "" {
			title = ev.Name + " " + gist
		}
		tid := j.toolID(ev.Agent, id)
		update := map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": tid,
			"title": j.runeClip(title, 200), "kind": kind, "status": "pending",
			"_meta": map[string]any{"sleipnir/agent": agentName(ev.Agent)},
		}
		if len(ev.Input) > 0 {
			update["rawInput"] = ev.Input
		}
		j.update(update)
	case "tool_end":
		tid := j.toolID(ev.Agent, id)
		update := map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": tid,
			"status": "completed",
		}
		if ev.Failed {
			update["status"] = "failed"
		}
		if out := strings.TrimSpace(ev.Output); out != "" {
			update["content"] = []map[string]any{{"type": "content", "content": textBlock(j.runeClip(out, maxToolOutput))}}
		}
		j.update(update)
	case "notice":
		if ev.Level == "debug" || ev.Message == "" {
			return
		}
		j.notice(ev)
	case "result":
		var m map[string]any
		if json.Unmarshal(line, &m) == nil {
			j.mu.Lock()
			j.result = m
			j.mu.Unlock()
			if sid, ok := m["session"].(string); ok && sid != "" {
				j.sess.mu.Lock()
				j.sess.ResumeID = sid
				j.sess.mu.Unlock()
			}
		}
		if text, _ := m["text"].(string); text != "" && !j.wasStreamed(ev.Agent) {
			j.update(map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"messageId":     j.messageID(ev.Agent, false),
				"content":       textBlock(j.runeClip(text, maxChunkRunes)),
			})
		}
		j.cost(m)
	}
}

// update sends one session update to the client and stores it for replay.
func (j *Job) update(u map[string]any) {
	j.sess.addReplay(u)
	j.srv.notify(j.sess.ID, u)
}

// notice sends a notice when the client supports it, or an agent message when
// it does not: a notice is not conversation history.
func (j *Job) notice(ev runEvent) {
	j.srv.mu.Lock()
	supported := j.srv.caps.Notices
	j.srv.mu.Unlock()
	if supported {
		severity := "info"
		switch ev.Level {
		case "warn", "warning":
			severity = "warning"
		case "error":
			severity = "error"
		}
		j.update(map[string]any{
			"sessionUpdate": "notice", "severity": severity,
			"title":       "Sleipnir (" + agentName(ev.Agent) + ")",
			"description": j.runeClip(ev.Message, 2000),
		})
		return
	}
	j.update(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"messageId":     j.messageID(ev.Agent, false),
		"content":       textBlock("[" + ev.Level + "] " + j.runeClip(ev.Message, 2000)),
	})
}

// usage reports the context in use after a model response.
func (j *Job) usage(ev runEvent) {
	u := ev.Usage
	used := u.InputTokens + u.CacheReadTokens + u.OutputTokens + u.CacheWrite5m + u.CacheWrite1h
	j.mu.Lock()
	j.lastUsed = used
	j.mu.Unlock()
	size := defaultContextSize
	if model := j.sess.value("model"); model != "" {
		if n := j.srv.contexts[model]; n > 0 {
			size = n
		}
	}
	j.update(map[string]any{
		"sessionUpdate": "usage_update", "used": used, "size": size,
	})
}

// cost reports the run's total cost when the client can show it.
func (j *Job) cost(result map[string]any) {
	amount, ok := result["cost_usd"].(float64)
	if !ok || amount <= 0 {
		return
	}
	j.mu.Lock()
	used := j.lastUsed
	j.mu.Unlock()
	j.update(map[string]any{
		"sessionUpdate": "usage_update",
		"used":          used, "size": defaultContextSize,
		"cost": map[string]any{"amount": amount, "currency": "USD"},
	})
}

// finish settles the turn's state and stop reason and saves the session.
func (j *Job) finish(err error) {
	j.mu.Lock()
	j.state = turnCancelled
	j.stopReason = "cancelled"
	resErr := ""
	if j.result != nil {
		resErr, _ = j.result["error"].(string)
	}
	switch {
	case j.cancelled:
	case err != nil:
		j.state, j.stopReason = turnFailed, j.stopReasonFor("")
		j.exitErr = err.Error()
	case resErr != "":
		j.state, j.stopReason = turnFailed, j.stopReasonFor(resErr)
	case j.result == nil:
		j.state, j.stopReason = turnFailed, "refusal"
	default:
		j.state, j.stopReason = turnDone, j.stopReasonFor(j.stopOf(j.result))
	}
	stop, failed := j.stopReason, j.state == turnFailed
	j.mu.Unlock()
	if err != nil && j.exitError() == "" {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			j.mu.Lock()
			j.exitErr = fmt.Sprintf("exit status %d", ee.ExitCode())
			j.mu.Unlock()
		}
	}
	if stop != "cancelled" && (failed || j.exitError() != "") {
		var detail string
		if resErr := j.resultError(); resErr != "" {
			detail = resErr
		} else if stderr := strings.TrimSpace(j.stderr.String()); stderr != "" {
			detail = stderr
		} else {
			detail = j.exitError()
		}
		if detail != "" {
			j.update(map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"messageId":     j.messageID("main", false),
				"content":       textBlock("[error] " + j.runeClip(detail, 2000)),
			})
		}
	}
	j.sess.save()
	j.sess.clearActive(j)
}

// stopOf returns the run's own stop reason.
func (j *Job) stopOf(result map[string]any) string {
	stop, _ := result["stop"].(string)
	return stop
}

// stopReasonFor maps a Sleipnir stop reason onto an ACP one.
func (j *Job) stopReasonFor(stop string) string {
	switch stop {
	case "max_tokens":
		return "max_tokens"
	case "refusal":
		return "refusal"
	default:
		return "end_turn"
	}
}

// stopReasonValue returns the ACP stop reason settled by finish.
func (j *Job) stopReasonValue() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stopReason
}

// resultError and exitError read the fields set by finish.
func (j *Job) resultError() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.result == nil {
		return ""
	}
	s, _ := j.result["error"].(string)
	return s
}

func (j *Job) exitError() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.exitErr
}

// messageID returns the id of the current message of an agent, making a new
// one when a response resets or ends.
func (j *Job) messageID(agent string, thought bool) string {
	agent = agentName(agent)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.gen == nil {
		j.gen = map[string]int{}
	}
	key := agent
	if thought {
		key += ":thought"
	}
	return fmt.Sprintf("msg_%s_%s_%d", j.sess.ID, key, j.gen[key])
}

// toolID returns a stable ACP id for a run's tool call.
func (j *Job) toolID(agent, id string) string {
	agent = agentName(agent)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.tools == nil {
		j.tools = map[string]string{}
	}
	key := agent + ":" + id
	if tid := j.tools[key]; tid != "" {
		return tid
	}
	tid := fmt.Sprintf("tc_%s_%s_%s", j.sess.ID, agent, sanitizeID(id))
	j.tools[key] = tid
	return tid
}

// markStreamed and wasStreamed remember whether a turn sent text chunks.
func (j *Job) markStreamed(agent string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.streamed == nil {
		j.streamed = map[string]bool{}
	}
	j.streamed[agentName(agent)] = true
}

func (j *Job) wasStreamed(agent string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.streamed[agentName(agent)]
}

// runArgs builds the child's arguments from the session's current options.
func (j *Job) runArgs(sess *Session, goal string, resume string) ([]string, string, error) {
	workers, _ := strconv.Atoi(sess.value("swarm"))
	args, dir, err := mcpserve.Args(j.srv.mcpCfg, mcpserve.StartArgs{
		Goal: goal, Cwd: sess.Cwd, Model: sess.value("model"),
		Mode: sess.value("mode"), Workers: workers, Resume: resume,
	})
	if err != nil {
		return nil, "", err
	}
	extra := []string{}
	if effort := sess.value("effort"); effort != "" && effort != "medium" {
		extra = append(extra, "--effort", effort)
	}
	if sess.MCPFile != "" {
		extra = append(extra, "--mcp-config", sess.MCPFile)
	}
	return insertBeforeDash(args, extra), dir, nil
}

// insertBeforeDash puts flags before the trailing "-" that marks goal on stdin.
func insertBeforeDash(args, extra []string) []string {
	if len(extra) == 0 {
		return args
	}
	if n := len(args); n > 0 && args[n-1] == "-" {
		out := make([]string, 0, len(args)+len(extra))
		out = append(out, args[:n-1]...)
		out = append(out, extra...)
		return append(out, "-")
	}
	return append(args, extra...)
}

// runeClip cuts s to n runes, keeping valid UTF-8.
func (j *Job) runeClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		if !utf8.ValidString(s) {
			return strings.ToValidUTF8(s, "")
		}
		return s
	}
	return string(r[:n])
}

// textBlock is one ACP text content block.
func textBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// agentName is a stable, short label for a run's agent.
func agentName(agent string) string {
	if agent == "" {
		return "main"
	}
	return agent
}

// sanitizeID keeps a run's tool id safe inside an ACP id.
func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		s = "tool"
	}
	return s
}

// toolKind maps a Sleipnir tool name to an ACP presentation category.
func toolKind(name string) string {
	name = strings.ToLower(name)
	switch {
	case strings.Contains(name, "write"), strings.Contains(name, "edit"), strings.Contains(name, "patch"):
		return "edit"
	case strings.Contains(name, "delete"):
		return "delete"
	case strings.Contains(name, "move"), strings.Contains(name, "rename"):
		return "move"
	case strings.Contains(name, "read"), strings.Contains(name, "cat"):
		return "read"
	case strings.Contains(name, "grep"), strings.Contains(name, "find"), strings.Contains(name, "search"), strings.Contains(name, "scan"):
		return "search"
	case strings.Contains(name, "shell"), strings.Contains(name, "bash"), strings.Contains(name, "exec"), strings.Contains(name, "run"):
		return "execute"
	case strings.Contains(name, "fetch"), strings.Contains(name, "web"):
		return "fetch"
	case strings.Contains(name, "think"), strings.Contains(name, "plan"):
		return "think"
	default:
		return "other"
	}
}

// inputGist picks a short human-readable field of a tool's input.
func inputGist(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, key := range []string{"command", "path", "file_path", "pattern", "query", "url", "title", "to", "name"} {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// tailBuffer keeps only the last maxStderrBytes of a child's stderr.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > maxStderrTail {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-maxStderrTail:]...)
	}
	return len(p), nil
}

// String returns the bytes as valid UTF-8.
func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.buf), "")
}
