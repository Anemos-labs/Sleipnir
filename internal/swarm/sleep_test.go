package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// A sleep counts for what it waits in the command's own shell: in line or in an && chain, in GNU sleep's units; one in a loop, an
// if, a pipe or the background, or of a duration that is not a number, counts for nothing.
func TestShellSleep(t *testing.T) {
	for cmd, want := range map[string]time.Duration{
		"sleep 600": 10 * time.Minute,
		`sleep 1800; date -u +%FT%TZ; wc -l "$TMPDIR/events.jsonl"`: 30 * time.Minute,
		"go build ./... && sleep 1m 30":                             90 * time.Second,
		"sleep 2 && curl -fsS http://127.0.0.1:8080/healthz":        2 * time.Second,
		"sleep 0.5h":                            30 * time.Minute,
		"sleep 1000001":                         1000001 * time.Second, // eleven days and more: counted, no longer discarded
		"sleep 2000d":                           longestSleep,
		"sleep inf":                             longestSleep,
		"sleep 1e400":                           longestSleep,
		"sleep 59.9":                            59900 * time.Millisecond,
		"sleep nan":                             0,
		"sleep -5":                              0,
		"sleep nan 60":                          0, // GNU sleep fails at once: an invalid operand makes the whole command none
		"sleep":                                 0,
		"sleep 30; sleep 40":                    70 * time.Second,
		"sleep " + strings.Repeat("365d ", 300): longestSleep, // saturated, no overflow
		strings.Repeat("sleep 365d; ", 300):     longestSleep,
		"timeout 900 sleep 300":                 5 * time.Minute,
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

// While the manager sleeps in the shell it counts as waiting for the stall sweep, as it does in wait. It is judged on the input the
// call runs with: a user's hook that rewrites a short command into a long sleep makes it a wait, and the tool-start notice (which
// comes before the hooks and sees the command as written) does not decide it.
func TestAManagersShellSleepCountsAsWaiting(t *testing.T) {
	r := idleCapacityRig(t, 20*time.Minute)
	in, _ := json.Marshal(map[string]string{"command": "date"})
	slept, _ := json.Marshal(map[string]string{"command": "sleep 900; tail -5 sim.log"})
	h := &holdHooks{s: r.sw, m: r.sw.get("mgr"), inner: rewriteHooks{to: slept}}
	sink := &memberSink{s: r.sw, m: r.sw.get("mgr"), Sink: agent.NopSink{}, ev: NewEvidence()}
	sink.ToolStart("mgr", core.ToolUse("1", "bash", in)) // as written: no sleep
	if o := h.BeforeTool(context.Background(), agent.ToolHookCall{Agent: "mgr", Role: "manager", Tool: "bash", Input: in}); o.Veto {
		t.Fatalf("a sleep while a worker runs was refused: %s", o.Reason)
	}
	r.sw.sweepStalls()
	if _, ok := active(r, StallIdleCapacity); !ok {
		t.Fatal("most of the team idle while the manager sleeps in the shell was not found")
	}
}

// rewriteHooks stands for a user's hooks that replace every tool call's input.
type rewriteHooks struct{ to json.RawMessage }

func (h rewriteHooks) BeforeTool(context.Context, agent.ToolHookCall) agent.ToolHookOutcome {
	return agent.ToolHookOutcome{UpdatedInput: h.to}
}

func (rewriteHooks) AfterTool(context.Context, agent.ToolHookCall, *tools.Result) agent.ToolHookOutcome {
	return agent.ToolHookOutcome{}
}

func (rewriteHooks) BeforeStop(context.Context, string, string, string, bool) agent.StopOutcome {
	return agent.StopOutcome{}
}
