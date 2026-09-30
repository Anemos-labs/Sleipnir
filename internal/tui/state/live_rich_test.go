package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/session"
)

// The rich session is the team of the demo made to do what the demo never does, so that the State meets, in a log the harness
// really wrote, the events the demo does not produce:
//
//	mail             a writer sends a note to the manager and one to a scout
//	compaction       a scout reads a large file again and again in a small context window
//	stuck            a scout repeats one failing call until the repetition guard stops it (a nudge first, then the end of its run)
//	rate limit       the endpoint refuses one request with a 429, which the agent retries and the governor notes
//	cache anomaly    the endpoint's clock jumps ten minutes, so that its router sends the session to another engine, whose cache is
//	                 cold, when the harness still believes the warm one is serving it
//	leases           a writer takes the file its task names
//
// What the script does is fixed; in what order the agents do it is not (they run in parallel), so the tests that fold this log
// count what the log says and compare, and never assume an order.

var richRecording recording

// richLog is the log of the rich session.
func richLog(t testing.TB) []byte {
	t.Helper()
	return richRecording.get(t, "rich", recordRich)
}

// richPrices are the prices the rich session's model is billed at: not the demo's, and with a real write premium.
var richPrices = cost.Price{InputPerM: 1.00, OutputPerM: 4.00, CacheReadPerM: 0.10, CacheWrite5mPerM: 1.25, CacheWrite1hPerM: 2.00}

const richGoal = "Survey docs/big.md, probe the flaky tool and write the notes; then have the notes accepted."

// richWindow is the context window of the rich session's agents: small, so that a scout that keeps reading a large file
// outgrows it and the harness compacts its thread, and large enough for a compaction to bring the thread back under it.
const richWindow = 20000

func recordRich() ([]byte, error) {
	root, cleanup, err := tempRoot("state-rich-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	ws := filepath.Join(root, "work")
	if err := writeRichWorkspace(ws); err != nil {
		return nil, err
	}
	defer func(old time.Duration) { agent.RetryBase = old }(agent.RetryBase)
	agent.RetryBase = time.Millisecond // the retry after the refused request is not something to wait for

	clock := &jumpClock{}
	sc := &richScript{clock: clock, turn: map[string]int{}}
	// Two engines, and the session pinned to the one that served it first for a minute past its last request: when the clock has
	// jumped past that, the session lands on the other engine, whose cache is empty.
	srv := mock.New(mock.Config{Engines: 2, PinTTL: time.Minute, Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}, Now: clock.Now}, sc.respond)
	ts := srv.Start()
	defer ts.Close()
	prof := openaichat.DefaultProfile("rich", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "rich", BaseURL: ts.URL, Profile: &prof,
		Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	model := cost.Model{ID: "rich-model", ContextTokens: 1_000_000, MaxOutput: 8192, Cache: cost.OpenAICacheModel(), Price: richPrices}

	ctx, cancel := context.WithTimeout(context.Background(), recordingGuard)
	defer cancel()
	sdir := filepath.Join(root, "session")
	s, err := session.New(ctx, session.Options{
		Cwd: ws, Root: ws, Home: filepath.Join(root, "home"), Dir: sdir,
		Provider: client, ModelInfo: &model, Model: model.ID,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: true, NoMCP: true,
		Swarm: true, MaxAgents: 8, ContextWindow: richWindow,
	})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if _, err := s.Run(ctx, richGoal); err != nil {
		return nil, fmt.Errorf("the rich session: %w", err)
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(sdir, "events.jsonl"))
}

func writeRichWorkspace(ws string) error {
	var big strings.Builder
	for i := 1; i <= 900; i++ {
		fmt.Fprintf(&big, "Line %04d: area %d of the system takes its inputs, keeps its promises and is misused in detail %d.\n", i, i%17, i*7)
	}
	files := map[string]string{
		"README.md":    "# Notes\n\nSummaries go to summaries/.\n",
		"docs/big.md":  big.String(),
		"docs/tiny.md": "# Tiny\n\nNothing to see.\n",
	}
	for name, body := range files {
		p := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(ws, "summaries"), 0o755)
}

// richScript plays the model of every agent of the rich session. An agent's step is how many requests the script has answered for
// it, not how long its thread is: compaction folds a thread, so its length is no clock.
type richScript struct {
	clock *jumpClock

	mu       sync.Mutex
	turn     map[string]int // requests answered, by agent
	refused  bool           // the 429 has been sent
	jumped   bool           // the clock has been moved
	accepted bool           // the manager has given its verdicts
}

// The request numbers at which the endpoint misbehaves: the sixth it sees is refused once, and before the sixteenth its clock is
// moved on. Both happen well inside the session, and neither depends on which agent asks.
const (
	richRefuseAt = 6
	richJumpAt   = 16
)

func (r *richScript) respond(c *mock.Call) mock.Reply {
	r.mu.Lock()
	switch {
	case c.N >= richRefuseAt && !r.refused:
		r.refused = true
		r.mu.Unlock()
		return mock.Reply{Fault: &mock.Fault{Status: 429, Message: "rate limit reached: slow down"}}
	case c.N >= richJumpAt && !r.jumped:
		r.jumped = true
		r.clock.jump(10 * time.Minute)
	}
	id, role, tid := whoIs(c)
	if strings.Contains(c.LastUser(), "<compactor-task>") {
		r.mu.Unlock()
		return mock.Reply{Text: "{}"}
	}
	k := r.turn[id]
	r.turn[id]++
	r.mu.Unlock()

	switch role {
	case "manager":
		return r.manager(c, k)
	case "scout":
		if tid == "T2" {
			return r.flaky(k)
		}
		return r.reader(k, tid)
	case "docs":
		return r.writer(k, id, tid)
	}
	return mock.Reply{Text: "nothing to do"}
}

func (r *richScript) manager(c *mock.Call, k int) mock.Reply {
	if k == 0 {
		return mock.Reply{Text: "Three tasks: survey, probe, write.", ToolCalls: []mock.ToolCall{
			call("t1", "task", map[string]any{"action": "create", "title": "Survey docs/big.md", "role": "scout"}),
			call("t2", "task", map[string]any{"action": "create", "title": "Probe the flaky tool", "role": "scout"}),
			call("t3", "task", map[string]any{"action": "create", "title": "Write the notes", "role": "docs", "files": []string{"summaries/notes.md"}}),
			call("s1", "spawn", map[string]any{"role": "scout", "task": "T1"}),
			call("s2", "spawn", map[string]any{"role": "scout", "task": "T2"}),
			call("s3", "spawn", map[string]any{"role": "docs", "task": "T3"}),
			call("w", "wait", map[string]any{"until": []string{"T1", "T3"}, "timeout_sec": 120}),
		}}
	}
	settled := strings.Contains(toolResults(c), "all awaited tasks settled")
	r.mu.Lock()
	done := r.accepted
	if settled && !done {
		r.accepted = true
	}
	r.mu.Unlock()
	switch {
	case settled && !done:
		// The probe (T2) was put back on the board by the harness when its worker was stopped, and nobody takes it up again: the
		// manager says it is finished all the same, and the swarm holds it to that, as it does a batch run's manager.
		return mock.Reply{Text: "Accepting what is finished.", ToolCalls: []mock.ToolCall{
			call("a1", "task", map[string]any{"action": "accept", "id": "T1"}),
			call("a3", "task", map[string]any{"action": "accept", "id": "T3"}),
		}}
	case !done && k < 20:
		// Mail arrived, or the wait ran out, before everything settled: sleep again.
		return mock.Reply{Text: "waiting", ToolCalls: []mock.ToolCall{
			call(fmt.Sprintf("w%d", k), "wait", map[string]any{"until": []string{"T1", "T3"}, "timeout_sec": 120})}}
	}
	return mock.Reply{Text: "The survey and the notes are in; the probe is written off."}
}

// reader reads the large file from ever further down, until its thread has outgrown the window more than once, then reports.
func (r *richScript) reader(k int, tid string) mock.Reply {
	switch {
	case k < 7:
		return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{
			call(fmt.Sprintf("r%d", k), "read", map[string]any{"path": "docs/big.md", "offset": 1 + 100*k, "limit": 220})}}
	case k == 7:
		return mock.Reply{Text: "reporting", ToolCalls: []mock.ToolCall{
			call("d", "task", map[string]any{"action": "done", "id": tid, "text": "read the file from top to bottom"})}}
	}
	return mock.Reply{Text: "done"}
}

// flaky makes the same failing call four times in a turn, which the repetition guard answers with a nudge, and four times again,
// which ends the run.
func (r *richScript) flaky(k int) mock.Reply {
	if k > 1 {
		return mock.Reply{Text: "I cannot get past this."}
	}
	var calls []mock.ToolCall
	for i := 0; i < 4; i++ {
		calls = append(calls, call(fmt.Sprintf("x%d-%d", k, i), "read", map[string]any{"path": "docs/missing.md"}))
	}
	return mock.Reply{Text: "trying again", ToolCalls: calls}
}

// writer tells the manager and a scout what it is doing, writes the file its task names and reports.
func (r *richScript) writer(k int, id, tid string) mock.Reply {
	switch k {
	case 0:
		return mock.Reply{Text: "starting", ToolCalls: []mock.ToolCall{
			call("m1", "mail", map[string]any{"to": "manager", "text": id + " has started the notes", "kind": "info"}),
			call("m2", "mail", map[string]any{"to": "sc-1", "text": "which part of big.md matters most?", "kind": "request"}),
			call("rd", "read", map[string]any{"path": "docs/tiny.md"}),
		}}
	case 1:
		return mock.Reply{Text: "writing", ToolCalls: []mock.ToolCall{
			call("w", "write", map[string]any{"path": "summaries/notes.md", "content": "# Notes\n\n- written by " + id + "\n"}),
		}}
	case 2:
		return mock.Reply{Text: "reporting", ToolCalls: []mock.ToolCall{
			call("d", "task", map[string]any{"action": "done", "id": tid, "text": "wrote the notes"})}}
	}
	return mock.Reply{Text: "done"}
}
