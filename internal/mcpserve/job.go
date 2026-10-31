package mcpserve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// Job states.
const (
	StateRunning   = "running"
	StateDone      = "done"      // the run ended and reported success
	StateFailed    = "failed"    // the run ended with an error or a non-zero status
	StateCancelled = "cancelled" // the run ended after a cancel
)

const (
	maxActivity    = 400       // activity lines a job keeps (the oldest are dropped)
	maxLineRunes   = 240       // one activity line is cut to this many characters
	maxEventBytes  = 1 << 20   // a stream line longer than this is skipped, not parsed
	maxStderrBytes = 8 << 10   // the tail of the child's stderr that is kept
	maxResultText  = 128 << 10 // the final answer is cut to this many bytes
	maxMessage     = 4 << 10   // an agent's message in progress is held up to this many bytes
	killAfter      = 2 * time.Minute
)

// Activity is one line of a job's digest. Seq grows by one per line, so a
// caller can ask for what came after the last line it saw.
type Activity struct {
	Seq  int    `json:"seq"`
	Line string `json:"line"`
}

// Job is one child run.
type Job struct {
	ID        string
	Goal      string
	Dir       string
	Args      []string // the child's arguments, after the program
	StartedAt time.Time

	cmd  *exec.Cmd
	done chan struct{}

	mu        sync.Mutex
	state     string
	endedAt   time.Time
	cancelled bool
	seq       int
	activity  []Activity
	tools     int
	messages  map[string]*strings.Builder // per agent: the message being streamed
	result    map[string]any              // the stream's final "result" event
	exitErr   string
	stderr    tailBuffer
}

// start launches the child and the goroutines that read it. prepare, when not
// nil, is called just before Start and returns the function to call with
// Start's error (sched.PassKeys has this shape).
func (j *Job) start(exe string, env []string, prepare func(*exec.Cmd) func(error)) error {
	cmd := exec.Command(exe, j.Args...)
	cmd.Dir = j.Dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(j.Goal)
	cmd.Stderr = &j.stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var started func(error)
	if prepare != nil {
		started = prepare(cmd)
	}
	err = cmd.Start()
	if started != nil {
		started(err)
	}
	if err != nil {
		return err
	}
	j.cmd = cmd
	j.state = StateRunning
	j.done = make(chan struct{})
	j.messages = map[string]*strings.Builder{}
	go func() {
		j.read(stdout)
		werr := cmd.Wait()
		j.finish(werr)
		close(j.done)
	}()
	return nil
}

// read digests the child's JSON event stream until it ends.
func (j *Job) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br, maxEventBytes)
		if len(line) > 0 {
			j.event(line)
		}
		if err != nil {
			return
		}
	}
}

// readLine returns the next line without its newline. A line longer than max
// is consumed and returned empty.
func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	over := false
	for {
		frag, err := br.ReadSlice('\n')
		if !over {
			if len(buf)+len(frag) > max {
				over, buf = true, nil
			} else {
				buf = append(buf, frag...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if over {
			return nil, err
		}
		return bytes.TrimRight(buf, "\r\n"), err
	}
}

// streamEvent is the part of a `sleipnir run --json` event the digest reads.
type streamEvent struct {
	Type     string          `json:"type"`
	Agent    string          `json:"agent"`
	Text     string          `json:"text"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Failed   bool            `json:"failed"`
	ExitCode *int            `json:"exit_code"`
	Output   string          `json:"output"`
	Level    string          `json:"level"`
	Message  string          `json:"message"`
}

// event folds one line of the stream into the digest.
func (j *Job) event(line []byte) {
	var ev streamEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	who := ev.Agent
	if who == "" {
		who = "agent"
	}
	switch ev.Type {
	case "text":
		b := j.messages[who]
		if b == nil {
			b = &strings.Builder{}
			j.messages[who] = b
		}
		if b.Len() < maxMessage {
			b.WriteString(ev.Text)
		}
	case "reset":
		delete(j.messages, who)
	case "response":
		j.flushMessage(who)
	case "tool_start":
		j.flushMessage(who)
		j.tools++
		j.add(fmt.Sprintf("%s: %s %s", who, ev.Name, inputGist(ev.Input)))
	case "tool_end":
		if ev.Failed {
			note := "failed"
			if ev.ExitCode != nil {
				note = fmt.Sprintf("exit %d", *ev.ExitCode)
			}
			j.add(fmt.Sprintf("%s: %s %s: %s", who, ev.Name, note, firstLine(ev.Output)))
		}
	case "notice":
		if ev.Level != "debug" {
			j.add(fmt.Sprintf("%s [%s]: %s", who, ev.Level, ev.Message))
		}
	case "result":
		var m map[string]any
		if json.Unmarshal(line, &m) == nil {
			delete(m, "type")
			if t, ok := m["text"].(string); ok && len(t) > maxResultText {
				m["text"] = clip(t, maxResultText) + "\n[cut]"
			}
			j.result = m
		}
	}
}

// flushMessage turns an agent's streamed message into one activity line.
func (j *Job) flushMessage(who string) {
	b := j.messages[who]
	if b == nil {
		return
	}
	delete(j.messages, who)
	if t := strings.TrimSpace(b.String()); t != "" {
		j.add(who + " says: " + t)
	}
}

// add appends an activity line, cut to one line of maxLineRunes.
func (j *Job) add(line string) {
	line = strings.Join(strings.Fields(line), " ")
	if r := []rune(line); len(r) > maxLineRunes {
		line = string(r[:maxLineRunes]) + "…"
	}
	j.seq++
	j.activity = append(j.activity, Activity{Seq: j.seq, Line: line})
	if len(j.activity) > maxActivity {
		j.activity = append(j.activity[:0:0], j.activity[len(j.activity)-maxActivity:]...)
	}
}

// finish records how the child ended.
func (j *Job) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for who := range j.messages {
		j.flushMessage(who)
	}
	j.endedAt = time.Now()
	resErr, _ := j.result["error"].(string)
	switch {
	case j.cancelled:
		j.state = StateCancelled
	case err != nil:
		j.state = StateFailed
		j.exitErr = err.Error()
	case resErr != "" || j.result == nil:
		j.state = StateFailed
	default:
		j.state = StateDone
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			j.exitErr = fmt.Sprintf("exit status %d", ee.ExitCode())
		}
	}
}

// cancel asks the child to end as Ctrl-C would (SIGTERM; on a system without
// signals, the process is killed), and kills it if it is still there after
// killAfter. It reports whether the job was running.
func (j *Job) cancel() bool {
	j.mu.Lock()
	if j.state != StateRunning || j.cancelled {
		running := j.state == StateRunning
		j.mu.Unlock()
		return running
	}
	j.cancelled = true
	j.mu.Unlock()
	p := j.cmd.Process
	if err := p.Signal(syscall.SIGTERM); err != nil {
		_ = p.Kill()
	}
	go func() {
		select {
		case <-j.done:
		case <-time.After(killAfter):
			_ = p.Kill()
		}
	}()
	return true
}

// Status is what sleipnir_status reports about a job.
type Status struct {
	Job        string         `json:"job"`
	State      string         `json:"state"`
	Goal       string         `json:"goal"`
	Dir        string         `json:"dir"`
	ElapsedSec int64          `json:"elapsed_s"`
	ToolCalls  int            `json:"tool_calls"`
	LastSeq    int            `json:"last_seq"`
	Activity   []Activity     `json:"activity,omitempty"`
	Omitted    int            `json:"omitted,omitempty"` // lines after the caller's mark that were not returned
	Result     map[string]any `json:"result,omitempty"`
	Exit       string         `json:"exit,omitempty"`
	Stderr     string         `json:"stderr,omitempty"`
}

// status reports the job with at most limit activity lines after seq after.
func (j *Job) status(after, limit int) Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	end := time.Now()
	if !j.endedAt.IsZero() {
		end = j.endedAt
	}
	st := Status{
		Job: j.ID, State: j.state, Goal: clip(j.Goal, 400), Dir: j.Dir,
		ElapsedSec: int64(end.Sub(j.StartedAt).Seconds()), ToolCalls: j.tools, LastSeq: j.seq,
	}
	var lines []Activity
	for _, a := range j.activity {
		if a.Seq > after {
			lines = append(lines, a)
		}
	}
	if len(lines) > limit {
		st.Omitted = len(lines) - limit
		lines = lines[len(lines)-limit:]
	}
	st.Activity = lines
	if j.state != StateRunning {
		st.Result = j.result
		st.Exit = j.exitErr
		if j.state != StateDone {
			st.Stderr = j.stderr.String()
		}
	}
	return st
}

// running reports whether the child has not ended.
func (j *Job) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == StateRunning
}

// inputGist is a short view of a tool call's input: the value of its most
// telling field, or the input itself.
func inputGist(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "path", "file_path", "pattern", "query", "url", "title", "to", "name", "action"} {
		if v, ok := m[k].(string); ok && v != "" {
			return firstLine(v)
		}
	}
	return firstLine(string(raw))
}

// firstLine is the first non-empty line of s.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// clip cuts s to at most n bytes on a UTF-8 boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tailBuffer keeps the last maxStderrBytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

// Write keeps the tail of what it is given; it never fails.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - maxStderrBytes; over > 0 {
		t.buf = append(t.buf[:0:0], t.buf[over:]...)
	}
	return len(p), nil
}

// String returns the kept tail, as valid UTF-8.
func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.buf), "")
}

// environ is the child's environment: the server's own.
func environ() []string { return os.Environ() }
