package swarm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func ranTest(ev *Evidence, cmd, out string) {
	in, _ := json.Marshal(map[string]any{"command": cmd})
	ev.Observe(core.ToolUse("1", "bash", in), &tools.Result{Text: out + "\n[exit code 0]"}, time.Now())
}

// A worker ran `cargo test M src/walk.rs | tail -3`, a broken invocation, and its evidence read "exit status masked by the command
// line, whose output ended "test result: ok. 0 passed"": with no verifier configured nothing else checked it. Its done is held
// once, asking for a run whose status shows; a second done at the same revision goes through, so a worker is never stuck on it.
func TestADoneAfterAMaskedTestIsHeldOncePerRevision(t *testing.T) {
	s := &Swarm{}
	task := Task{ID: "T3", Rev: 1}
	ev := NewEvidence()
	ev.Begin("T3")
	ranTest(ev, "cargo test M src/walk.rs | tail -3", "test result: ok. 0 passed; 0 failed")
	got := s.maskedTestNotice(task, ev)
	if !strings.HasPrefix(got, "Not done yet: your last test `cargo test M src/walk.rs | tail -3` ran with its exit status hidden") ||
		!strings.Contains(got, "set -o pipefail") {
		t.Fatalf("notice = %q", got)
	}
	if again := s.maskedTestNotice(task, ev); again != "" {
		t.Fatalf("the second done at the same revision was held again: %q", again)
	}
	task.Rev = 2
	if s.maskedTestNotice(task, ev) == "" {
		t.Fatal("a new revision with a masked last test was not told")
	}

	task = Task{ID: "T4", Rev: 1}
	for _, tc := range []struct{ name, cmd, out string }{
		{"a test whose status shows", "cargo test", "test result: ok. 3 passed"},
		{"a masked test whose line prints its status", `cargo test | tail -3; echo "EXIT=${PIPESTATUS[0]}"`, "ok\nEXIT=0"},
	} {
		ev := NewEvidence()
		ev.Begin("T4")
		ranTest(ev, tc.cmd, tc.out)
		if got := s.maskedTestNotice(task, ev); got != "" {
			t.Errorf("%s: held with %q", tc.name, got)
		}
	}
	if got := s.maskedTestNotice(task, NewEvidence()); got != "" {
		t.Errorf("no test at all: %q", got)
	}
}
