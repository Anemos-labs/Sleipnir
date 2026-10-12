package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/input"
)

// /queue over the lines that wait: listed in order, one dropped by its number, all cleared; what it does not take is answered with
// its usage.
func TestQueueCommand(t *testing.T) {
	texts := []string{"first", "second\nmore", "/budget 3"}
	out, drop := QueueCommand("/queue", texts)
	for _, want := range []string{"queued (3)", "1. first", "2. second …", "3. /budget 3"} {
		if !strings.Contains(out, want) || drop != nil {
			t.Errorf("list lacks %q: %s (%v)", want, out, drop)
		}
	}
	if out, drop := QueueCommand("/queue drop 2", texts); out != "dropped 2: second …" || len(drop) != 1 || drop[0] != 1 {
		t.Errorf("drop: %q %v", out, drop)
	}
	if out, drop := QueueCommand("/queue drop 9", texts); !strings.Contains(out, "from 1 to 3") || drop != nil {
		t.Errorf("drop out of range: %q %v", out, drop)
	}
	if out, drop := QueueCommand("/queue clear", texts); out != "cleared the queue (3 lines)" || len(drop) != 3 {
		t.Errorf("clear: %q %v", out, drop)
	}
	if out, _ := QueueCommand("/queue", nil); out != "nothing is queued" {
		t.Errorf("empty: %q", out)
	}
	if out, _ := QueueCommand("/queue frobnicate", texts); out != QueueUsage {
		t.Errorf("usage: %q", out)
	}
	if !IsQueueCommand("/queue drop 1") || IsQueueCommand("/queued") {
		t.Error("IsQueueCommand")
	}
}

// While a turn runs: Up on an empty prompt takes the last line typed ahead back into the prompt (Enter queues it again), /queue answers
// at once and drops, and Alt+S sends what the prompt holds to the turn as a steer instead of queueing it.
func TestChatQueueEditDropAndSteer(t *testing.T) {
	r := startChat(t, rigOpts{})
	release := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		if goal == "slow" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return TurnResult{Steps: 1}
	}
	var steered []string
	r.host.command = func(ctx context.Context, line string, out io.Writer) CommandResult {
		if strings.HasPrefix(line, "/steer ") {
			steered = append(steered, strings.TrimPrefix(line, "/steer "))
			fmt.Fprintln(out, "steering sent")
		}
		return CommandResult{}
	}
	r.submit("slow")
	r.until("the turn to start", func(string) bool { return len(r.host.seen()) == 1 })
	r.submit("second")
	r.submit("third")
	r.shows("queued: “second” (+1 more)", "edits")

	// Up on the empty prompt: the last line typed ahead comes back to be edited
	r.special(input.Up)
	s := r.shows("taken back from the queue")
	if strings.Contains(s, "(+1 more)") {
		t.Errorf("the line taken back is still queued:\n%s", s)
	}
	r.typeText(" again")
	r.enter()
	r.shows("queued: “second” (+1 more)")

	// /queue lists at once, beside the turn, and drops by number
	r.submit("/queue")
	r.shows("queued (2)", "2. third again")
	r.submit("/queue drop 1")
	r.shows("dropped 1: second", "queued: “third again”")

	// Alt+S: the prompt goes to the turn as a steer, not into the queue
	r.typeText("use the other file")
	r.press(input.RuneKey('s', input.Alt))
	r.shows("steering sent")
	if strings.Join(steered, "|") != "use the other file" {
		t.Errorf("steered %q", steered)
	}

	close(release)
	r.until("the queue ran", func(string) bool { return len(r.host.seen()) == 2 })
	if got := r.host.seen(); strings.Join(got, ",") != "slow,third again" {
		t.Errorf("the session was given %q", got)
	}
}
