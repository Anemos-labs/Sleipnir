package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

func TestGoalPauseInterruptsBeforeChangingGoalState(t *testing.T) {
	r := startChat(t, rigOpts{})
	started, stopped := make(chan struct{}), make(chan struct{})
	r.host.turn = func(ctx context.Context, _ string) TurnResult {
		close(started)
		<-ctx.Done()
		close(stopped)
		return TurnResult{Err: ctx.Err()}
	}
	r.host.command = func(_ context.Context, line string, out io.Writer) CommandResult {
		select {
		case <-stopped:
		default:
			t.Error("goal state changed before the turn stopped")
		}
		io.WriteString(out, "goal paused")
		return CommandResult{}
	}
	r.submit("work")
	<-started
	r.submit("/goal pause")
	r.shows("goal paused")
	if len(r.host.seen()) != 1 {
		t.Error("pause must not start another turn")
	}
}

func TestStatsPanelUpdatesAndLeavesNoTranscriptSnapshots(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 20})
	r.ctrl('t')
	r.shows("nothing has been sent yet")
	b := statetest.NewBuilder()
	r.emit(sessionLog(b, 4, 0)...)
	r.shows("4 requests")
	if !r.altScreen() {
		t.Fatal("statistics must use a live panel")
	}
	r.special(input.Esc)
	r.shows("Message Sleipnir")
	if r.altScreen() || strings.Contains(r.visible(), "4 requests") {
		t.Error("stats leaked into the chat")
	}
}

func TestStatsFromCockpitReturnsToChat(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 30, team: 4})
	r.ctrl('g')
	r.shows("ctrl+g back to the chat")
	r.ctrl('t')
	r.shows("nothing has been sent yet")
	r.special(input.Esc)
	r.shows("Message Sleipnir")
	if r.altScreen() {
		t.Fatal("closing statistics must restore the chat, not an obscured cockpit")
	}
}

func TestExitInterruptsAnActiveTurn(t *testing.T) {
	r := startChat(t, rigOpts{})
	started := make(chan struct{})
	r.host.turn = func(ctx context.Context, _ string) TurnResult {
		close(started)
		<-ctx.Done()
		return TurnResult{Err: ctx.Err()}
	}
	r.submit("work")
	<-started
	r.typeText("/exit")
	r.send(input.SpecialKey(input.Enter, 0))
	if e := r.wait(); e.end != ChatQuit || e.err != nil {
		t.Fatalf("exit: %+v", e)
	}
}
