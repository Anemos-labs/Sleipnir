package session_test

// Session-level tests of how a swarm session treats its manager: a batch run holds it
// to its board, an interactive one does not and wakes it, and the interactive swarm
// outlives the context of a single turn (the chat loop cancels that when a turn ends).

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/swarm"
)

var (
	sessWho  = regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	sessTask = regexp.MustCompile(`task (T\d+)`)
)

// whoIs reads which agent a request is for, and its task, from the messages.
func whoIs(c *mock.Call) (id, role, tid string) {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role != "user" {
			continue
		}
		if m := sessWho.FindStringSubmatch(c.Messages[i].Content); m != nil && id == "" {
			id, role = m[1], m[2]
		}
		if m := sessTask.FindStringSubmatch(c.Messages[i].Content); m != nil && tid == "" {
			tid = m[1]
		}
	}
	return
}

func sawText(c *mock.Call, sub string) bool {
	for _, m := range c.Messages {
		if strings.Contains(m.Content, sub) {
			return true
		}
	}
	return false
}

// notices is a sink that keeps what the swarm told the person.
type notices struct {
	agent.NopSink
	mu   sync.Mutex
	logs []string
}

func (n *notices) Notice(a, level, msg string) {
	n.mu.Lock()
	n.logs = append(n.logs, a+" "+level+": "+msg)
	n.mu.Unlock()
}

func (n *notices) has(sub string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, l := range n.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func within(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A batch run (Interactive false) holds the manager: it answers while its worker is
// still running, is told what is unfinished, and only completes after it has waited
// for and accepted the worker's task.
func TestBatchSwarmSessionHoldsTheManagerToItsBoard(t *testing.T) {
	repo := newRepo(t)
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	vetoText := ""
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		id, role, tid := whoIs(c)
		n := assistantTurns(c)
		if role == "manager" {
			if sawText(c, "[stop hook] Not finished") {
				mu.Lock()
				vetoText = "seen"
				mu.Unlock()
				once.Do(func() { close(release) })
			}
			switch n {
			case 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("m1", "task", map[string]any{"action": "create", "title": "Add notes.txt", "role": "backend", "files": []string{"notes.txt"}}),
					call("m2", "spawn", map[string]any{"role": "backend", "task": "T1"}),
				}}
			case 1:
				return mock.Reply{Text: "the worker is on it"} // answers before the worker is done
			case 2:
				return mock.Reply{Text: "waiting", ToolCalls: []mock.ToolCall{call("m3", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 20})}}
			case 3:
				return mock.Reply{Text: "accepting", ToolCalls: []mock.ToolCall{call("m4", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "all done: notes.txt added"}
		}
		switch n {
		case 0:
			select {
			case <-release:
			case <-time.After(15 * time.Second):
			}
			return mock.Reply{Text: "on it", ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "notes.txt", "content": "written by " + id + "\n"})}}
		case 1:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("w2", "task", map[string]any{"action": "done", "id": tid, "text": "added notes.txt"})}}
		}
		return mock.Reply{Text: "summary from " + id}
	})
	o := opts(t, repo, client, model)
	o.Swarm = true // Interactive stays false: a batch run
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Run(context.Background(), "add a notes.txt file")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "notes.txt added") || strings.Contains(res.Text, "[harness]") {
		t.Fatalf("manager result %q", res.Text)
	}
	mu.Lock()
	seen := vetoText
	mu.Unlock()
	if seen == "" {
		t.Fatal("the manager was never told it had unfinished work")
	}
	if b, err := os.ReadFile(filepath.Join(repo, "notes.txt")); err != nil || !strings.HasPrefix(string(b), "written by be-1") {
		t.Fatalf("the worker's file must exist by the time the run returns: %v %q", err, b)
	}
	if tk, _ := s.Swarm.Board.Snapshot().Task("T1"); tk.Status != swarm.StatusDone {
		t.Fatalf("T1 = %s at the end of the run", tk.Status)
	}
}

// An interactive session's manager is not held, its swarm survives the end of the
// turn's context (the chat loop cancels it when a turn ends), and the worker's
// completion wakes the idle manager, visibly.
func TestInteractiveSwarmSessionWakesTheManagerAfterTheTurnEnded(t *testing.T) {
	repo := newRepo(t)
	release := make(chan struct{})
	var mu sync.Mutex
	managerCalls, sawNote := 0, false
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		id, role, tid := whoIs(c)
		n := assistantTurns(c)
		if role == "manager" {
			mu.Lock()
			managerCalls++
			if sawText(c, "While you were idle") {
				sawNote = true
			}
			mu.Unlock()
			switch {
			case n == 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("m1", "task", map[string]any{"action": "create", "title": "Add notes.txt", "role": "backend", "files": []string{"notes.txt"}}),
					call("m2", "spawn", map[string]any{"role": "backend", "task": "T1"}),
				}}
			case n == 1:
				return mock.Reply{Text: "the worker is on it"} // the turn ends here
			case sawText(c, "While you were idle") && n == 2:
				return mock.Reply{Text: "accepting", ToolCalls: []mock.ToolCall{call("m3", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "T1 was accepted: notes.txt is there"}
		}
		switch n {
		case 0:
			select {
			case <-release:
			case <-time.After(20 * time.Second):
			}
			return mock.Reply{Text: "on it", ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "notes.txt", "content": "written by " + id + "\n"})}}
		case 1:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("w2", "task", map[string]any{"action": "done", "id": tid, "text": "added notes.txt"})}}
		}
		return mock.Reply{Text: "summary from " + id}
	})
	sink := &notices{}
	o := opts(t, repo, client, model)
	o.Swarm, o.Interactive = true, true
	o.Sink = sink
	o.NewSink = func(string) agent.Sink { return sink }
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	turn, endTurn := context.WithCancel(context.Background())
	res, err := s.Run(turn, "add a notes.txt file")
	endTurn() // what runTurn does when a turn returns
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the worker is on it" {
		t.Fatalf("the manager was held or misled: %q", res.Text)
	}
	mu.Lock()
	callsAfterTurn := managerCalls
	mu.Unlock()
	if callsAfterTurn != 2 {
		t.Fatalf("the manager made %d requests in its turn, want 2", callsAfterTurn)
	}
	// The worker is still running: ending the turn's context did not stop it.
	if tk, _ := s.Swarm.Board.Snapshot().Task("T1"); tk.Status != swarm.StatusDoing {
		t.Fatalf("T1 = %s after the turn's context ended, want doing: the swarm must outlive a turn", tk.Status)
	}
	close(release)
	within(t, 20*time.Second, "the manager to accept T1 after being woken", func() bool {
		tk, _ := s.Swarm.Board.Snapshot().Task("T1")
		return tk.Status == swarm.StatusDone
	})
	mu.Lock()
	woke := sawNote
	mu.Unlock()
	if !woke {
		t.Fatal("the manager never received the wake note")
	}
	if !sink.has("mgr wake: While you were idle: T1 is in review (be-1)") {
		t.Fatalf("the person was not shown the wake: %v", sink.logs)
	}
	if n := s.Swarm.Wakes(); n != 1 {
		t.Fatalf("%d automatic runs, want 1", n)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "notes.txt")); err != nil || !strings.HasPrefix(string(b), "written by be-1") {
		t.Fatalf("the worker's file: %v %q", err, b)
	}
}
