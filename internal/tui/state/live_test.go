package state

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/demo"
	"github.com/reee344/sleipnir/internal/provider/mock"
)

// The live tests fold the log of a real session: the repository's own scripted team runs through the real harness (tools,
// permission engine, board, mail router, governor, merge queue, cache accounting) against the mock endpoint, and what it wrote
// is what the State has to make sense of. Nothing of it needs a network, an API key or a model, and nothing of it is timed; but it
// does start goroutines, a local server and, for the isolated session, git, so each session is recorded once per test process
// (whichever test needs it first) and skipped with -short.

// recording is one session's log, made on first use.
type recording struct {
	once sync.Once
	log  []byte
	err  error
}

// get returns the recorded log (a copy: callers edit it), recording it with run the first time. A session that cannot be
// recorded fails every test that needs it, not only the first.
func (r *recording) get(t testing.TB, name string, run func() ([]byte, error)) []byte {
	t.Helper()
	if testing.Short() {
		t.Skipf("records the %s session (a real harness against the mock endpoint): skipped with -short", name)
	}
	r.once.Do(func() { r.log, r.err = run() })
	if r.err != nil {
		t.Fatalf("recording the %s session: %v", name, r.err)
	}
	return bytes.Clone(r.log)
}

// recordingGuard is how long a recorded session may take before the test gives up on it: a guard against a hang, minutes, never a
// timing (a loaded machine running the race detector takes many times what a quiet one does).
const recordingGuard = 3 * time.Minute

// tempRoot makes the directory a recording works in and the function that removes it again. A recording is not tied to one test
// (the first to need it makes it for all), so it is not t.TempDir(). To look at a session's files, set STATE_LIVE_KEEP to a
// directory: each recording then works in a directory made under it and leaves it there.
func tempRoot(prefix string) (string, func(), error) {
	keep := os.Getenv("STATE_LIVE_KEEP")
	dir, err := os.MkdirTemp(keep, prefix)
	if err != nil {
		return "", nil, err
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real // the harness names files by the path it resolves (macOS: /var is /private/var), and the tests compare what it wrote
	}
	if keep != "" {
		return dir, func() {}, nil
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

var demoRecording recording

// demoLog is the log of the repository's demo with eight topics: a manager, eight scouts, four writers and a reviewer.
func demoLog(t testing.TB) []byte {
	t.Helper()
	return demoRecording.get(t, "demo", func() ([]byte, error) {
		root, cleanup, err := tempRoot("state-demo-")
		if err != nil {
			return nil, err
		}
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), recordingGuard)
		defer cancel()
		rep, err := demo.Run(ctx, demo.Options{Topics: 8, Dir: root})
		if err != nil {
			return nil, err
		}
		return os.ReadFile(filepath.Join(rep.Dir, "events.jsonl"))
	})
}

// The scripts of the live sessions find out who is asking the way the demo's does: from the prompt the agent sent, as a model
// would read it.
var (
	reWho  = regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	reTask = regexp.MustCompile(`task (T\d+)`)
)

// whoIs is the agent that sent the request, its role and the task it was last given.
func whoIs(c *mock.Call) (id, role, tid string) {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role != "user" {
			continue
		}
		body := c.Messages[i].Content
		if m := reWho.FindStringSubmatch(body); m != nil && id == "" {
			id, role = m[1], m[2]
		}
		if m := reTask.FindStringSubmatch(body); m != nil && tid == "" {
			tid = m[1]
		}
	}
	return
}

// call is a tool call of the scripted model.
func call(id, name string, args any) mock.ToolCall {
	b, _ := json.Marshal(args)
	return mock.ToolCall{ID: id, Name: name, Args: string(b)}
}

// assistantTurns is how many turns the model has taken in the thread it was sent.
func assistantTurns(c *mock.Call) int {
	n := 0
	for _, m := range c.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// toolResults are the results the model is answering: the tool messages after its last message.
func toolResults(c *mock.Call) string {
	var out []string
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.Role == "assistant" {
			break
		}
		if m.Role == "tool" {
			out = append([]string{m.Content}, out...)
		}
	}
	return strings.Join(out, "\n")
}

// toolCalled reports whether the thread holds a call of the tool whose arguments contain sub.
func toolCalled(c *mock.Call, name, sub string) bool {
	for _, m := range c.Messages {
		for _, tc := range m.ToolCalls {
			if tc.Name == name && strings.Contains(tc.Args, sub) {
				return true
			}
		}
	}
	return false
}

// jumpClock is the mock endpoint's clock: the real one, plus whatever the script added. Moving it forward ages every entry of the
// endpoint's cache at once, which is how a session gets a cache that went cold without the harness having waited for it.
type jumpClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *jumpClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *jumpClock) jump(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}
