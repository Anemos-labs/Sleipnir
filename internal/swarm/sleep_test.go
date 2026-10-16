package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
)

// A sleep counts for what it waits in the command's own shell: in line or in an && chain, in GNU sleep's units; one in a loop, an
// if, a pipe or the background, or of a duration that is not a number, counts for nothing.
func TestShellSleep(t *testing.T) {
	for cmd, want := range map[string]time.Duration{
		"sleep 600": 10 * time.Minute,
		`sleep 1800; date -u +%FT%TZ; wc -l "$TMPDIR/events.jsonl"`: 30 * time.Minute,
		"go build ./... && sleep 1m 30":                             90 * time.Second,
		"sleep 2 && curl -fsS http://127.0.0.1:8080/healthz":        2 * time.Second,
		"sleep 0.5h":            30 * time.Minute,
		"timeout 900 sleep 300": 5 * time.Minute,
		"for i in $(seq 1 30); do curl -fsS localhost:8080 && break; sleep 10; done": 0,
		"if true; then sleep 600; fi": 0,
		"sleep 600 &":                 0,
		`sleep "$N"`:                  0,
		"echo sleep 600":              0,
		`sh -c "sleep 600"`:           0,
	} {
		if got := shellSleep(cmd); got != want {
			t.Errorf("%q: %s, want %s", cmd, got, want)
		}
	}
}

// A manager's sleep in the shell is a wait: refused, with the reason a wait gets, when the board needs it or nothing can change;
// allowed while a worker runs; a short sleep is never one. A manager that slept to watch its own long simulation went past every
// guard on its waits, with a task in review and twelve idle workers.
func TestAManagersShellSleepIsAWait(t *testing.T) {
	r := idleCapacityRigWith(t, time.Minute, false) // be-1 runs with no task; the others are idle
	h := &holdHooks{s: r.sw, m: r.sw.get("mgr")}
	sleep := func(cmd string) agent.ToolHookOutcome {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return h.BeforeTool(context.Background(), agent.ToolHookCall{Agent: "mgr", Role: "manager", Tool: "bash", Input: in})
	}
	task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "awaits review", Role: "backend"})
	if err := r.sw.Board.Assign("mgr", "be-2", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Finish("be-2", task.ID, StatusReview, "ready"); err != nil {
		t.Fatal(err)
	}
	if o := sleep("sleep 1800; date"); o.Veto {
		t.Fatalf("a sleep while a worker runs was refused: %s", o.Reason)
	}
	m := r.sw.get("be-1")
	m.mu.Lock()
	m.life = lifeIdle // nobody runs now, and a task waits in review
	m.mu.Unlock()
	o := sleep("sleep 1800; date")
	if !o.Veto || !strings.Contains(o.Reason, "Cannot wait") || !strings.Contains(o.Reason, task.ID) || !strings.Contains(o.Reason, "A sleep in the shell is a wait") {
		t.Fatalf("a long sleep with a task in review and nobody running: %+v", o)
	}
	if o := sleep("sleep 5 && curl -fsS localhost:8080/healthz"); o.Veto {
		t.Fatalf("a short sleep was refused: %s", o.Reason)
	}
	if o := sleep("go test ./..."); o.Veto {
		t.Fatalf("a command that does not sleep was refused: %s", o.Reason)
	}
}

// While the manager sleeps in the shell it counts as waiting for the stall sweep, as it does in wait.
func TestAManagersShellSleepCountsAsWaiting(t *testing.T) {
	r := idleCapacityRig(t, 20*time.Minute)
	r.sw.noteCoordination("mgr", "task", "")
	in, _ := json.Marshal(map[string]string{"command": "sleep 900; tail -5 sim.log"})
	if !managerSleeps("bash", in) {
		t.Fatal("a 15-minute sleep is no wait")
	}
	sink := &memberSink{s: r.sw, m: r.sw.get("mgr"), Sink: agent.NopSink{}, ev: NewEvidence()}
	sink.ToolStart("mgr", core.ToolUse("1", "bash", in))
	r.sw.sweepStalls()
	if _, ok := active(r, StallIdleCapacity); !ok {
		t.Fatal("most of the team idle while the manager sleeps in the shell was not found")
	}
}
