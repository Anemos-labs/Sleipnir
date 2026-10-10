package wsvc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// What the Workspace reads of a session's event log: which task each agent was on when
// (agent.spawn, agent.assign), and the runs of the verification gate (verify.run in a
// shared tree, merge.verify_failed of an isolated team's queue). The log is read
// incrementally: each refresh reads only what was appended since the last one.

const (
	// maxLogLine bounds one line of the log that is parsed; longer lines are skipped.
	maxLogLine = 4 << 20
	// maxAssigns and maxRuns bound what is kept per agent and per task.
	maxAssigns = 2000
	maxRuns    = 100
)

// assign says that an agent took a task at a time.
type assign struct {
	at   time.Time
	task string
}

// gateRun is one run of a task's verification gate as the log records it.
type gateRun struct {
	at       time.Time
	agent    string
	cmd      string
	exit     int
	ok       bool
	timedOut bool
	infra    bool
	ms       int64
	output   string // a blob hash (verify.run) or the text (merge.verify_failed)
	inline   bool   // output is the text itself
	bytes    int
	gate     string
}

// logCache is the incremental reading of one session's log.
type logCache struct {
	mu      sync.Mutex
	path    string
	off     int64
	assigns map[string][]assign
	runs    map[string][]gateRun
}

// logOf returns the cache of a session directory's log.
func (s *service) logOf(dir string) *logCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.logs[dir]
	if c == nil {
		c = &logCache{path: filepath.Join(dir, "events.jsonl"), assigns: map[string][]assign{}, runs: map[string][]gateRun{}}
		s.logs[dir] = c
	}
	return c
}

// taskIDRE finds the task id at the start of a queue submission's task ("T3: title").
var taskPrefixRE = regexp.MustCompile(`^(T\d+)\b`)

// taskID is the board id of a task as an event names it.
func taskID(s string) string {
	if m := taskPrefixRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// refresh reads what was appended to the log since the last refresh.
func (c *logCache) refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.Open(c.path)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() < c.off {
		c.off, c.assigns, c.runs = 0, map[string][]assign{}, map[string][]gateRun{} // replaced: read it again
	}
	if _, err := f.Seek(c.off, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, n, err := readLine(r)
		if err != nil {
			return // a torn last line is read again next time
		}
		c.off += int64(n)
		if line == nil {
			continue // longer than maxLogLine: skipped
		}
		c.apply(bytes.TrimSpace(line))
	}
}

// readLine reads one complete line and reports how many bytes it took. A line longer
// than maxLogLine is consumed and returned as nil.
func readLine(r *bufio.Reader) ([]byte, int, error) {
	var out []byte
	n := 0
	for {
		part, err := r.ReadSlice('\n')
		n += len(part)
		if out != nil || n == len(part) {
			if n <= maxLogLine {
				out = append(out, part...)
			} else {
				out = nil
			}
		}
		switch {
		case err == nil:
			if n > maxLogLine {
				return nil, n, nil
			}
			return out, n, nil
		case err == bufio.ErrBufferFull:
			continue
		default:
			return nil, n, err
		}
	}
}

// apply folds one log event into the cache.
func (c *logCache) apply(line []byte) {
	var ev events.Event
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case events.TypeAgentSpawn, "agent.assign":
		var d struct {
			ID   string `json:"id"`
			Task string `json:"task"`
		}
		if json.Unmarshal(ev.Data, &d) != nil || d.ID == "" || d.Task == "" {
			return
		}
		l := append(c.assigns[d.ID], assign{at: ev.TS, task: d.Task})
		if len(l) > maxAssigns {
			l = l[len(l)-maxAssigns:]
		}
		c.assigns[d.ID] = l
	case swarm.EventVerifyRun:
		var d struct {
			Task     string `json:"task"`
			Agent    string `json:"agent"`
			Cmd      string `json:"cmd"`
			Exit     int    `json:"exit_code"`
			OK       bool   `json:"ok"`
			Ms       int64  `json:"ms"`
			Infra    bool   `json:"infra"`
			TimedOut bool   `json:"timed_out"`
			Output   string `json:"output"`
			Bytes    int    `json:"bytes"`
		}
		if json.Unmarshal(ev.Data, &d) != nil || d.Task == "" {
			return
		}
		c.addRun(d.Task, gateRun{at: ev.TS, agent: d.Agent, cmd: d.Cmd, exit: d.Exit, ok: d.OK, timedOut: d.TimedOut, infra: d.Infra,
			ms: d.Ms, output: d.Output, bytes: d.Bytes, gate: "done"})
	case workspace.EventVerifyFailed:
		var d struct {
			Task     string `json:"task"`
			Cmd      string `json:"cmd"`
			Exit     int    `json:"exit_code"`
			TimedOut bool   `json:"timed_out"`
			Output   string `json:"output"`
		}
		if json.Unmarshal(ev.Data, &d) != nil || d.Task == "" {
			return
		}
		c.addRun(taskID(d.Task), gateRun{at: ev.TS, agent: ev.Agent, cmd: d.Cmd, exit: d.Exit, timedOut: d.TimedOut, output: d.Output,
			inline: true, bytes: len(d.Output), gate: "merge"})
	}
}

// addRun keeps a gate run of a task.
func (c *logCache) addRun(task string, g gateRun) {
	l := append(c.runs[task], g)
	if len(l) > maxRuns {
		l = l[len(l)-maxRuns:]
	}
	c.runs[task] = l
}

// taskAt is the task an agent was on at a time: its latest assignment before then.
func (c *logCache) taskAt(agent string, at time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	task := ""
	for _, a := range c.assigns[agent] {
		if !at.IsZero() && a.at.After(at) {
			break
		}
		task = a.task
	}
	return task
}

// runsOf lists the gate runs of a task recorded in the log, oldest first.
func (c *logCache) runsOf(task string) []gateRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]gateRun(nil), c.runs[task]...)
}
