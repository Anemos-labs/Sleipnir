package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// ctrl+g is the team's cockpit, the screen of `sleipnir watch`, on the alternate screen with the prompt under it; ctrl+g again and the chat
// is back as it was.
func TestChatCtrlGOpensTheCockpitAndGivesTheChatBack(t *testing.T) {
	r := startChat(t, rigOpts{cols: 110, rows: 34, team: 8, mainAgent: "mgr"})
	if !strings.Contains(r.visible(), "ctrl+g cockpit") {
		t.Fatalf("the footer of a team names the cockpit:\n%s", r.visible())
	}
	b := statetest.NewBuilder()
	log := sessionLog(b, 2, 0)
	log = append(log, b.Spawn("be-1", "backend", "T1", "mgr"), b.Spawn("fe-1", "frontend", "T2", "mgr"))
	r.at(b.Now().Add(2 * time.Second))
	r.emit(log...)

	r.ctrl('g')
	s := r.shows("ctrl+g back to the chat", "type to message the manager", "Message Sleipnir")
	if !r.bridge.v.AltScreen() {
		t.Error("the cockpit is a page of the whole screen")
	}
	if !strings.Contains(s, "agents") {
		t.Errorf("the page has the table of the agents:\n%s", s)
	}
	r.ctrl('g')
	r.shows("ctrl+g cockpit")
	if r.bridge.v.AltScreen() {
		t.Error("the alternate screen is left with the cockpit")
	}
}

// A single agent has no cockpit: the key says so and nothing changes.
func TestChatASingleAgentHasNoCockpit(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 30})
	r.ctrl('g')
	r.shows("a single agent")
	if r.bridge.v.AltScreen() {
		t.Error("a single agent has no cockpit to show")
	}
}

// The cockpit opens by itself when a worker starts and closes by itself when the turn ends, so that the answer, which is written into the
// scrollback, is not left behind the page.
func TestChatTheCockpitOpensWhenAWorkerStartsAndClosesWhenTheTurnEnds(t *testing.T) {
	r := startChat(t, rigOpts{cols: 110, rows: 34, team: 8, mainAgent: "mgr", tickClock: true})
	started, finish := make(chan struct{}), make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		close(started)
		select {
		case <-finish:
		case <-ctx.Done():
		}
		return TurnResult{Steps: 3}
	}
	r.submit("build it")
	<-started
	if r.bridge.v.AltScreen() {
		t.Fatal("the cockpit does not open before a worker has started")
	}
	b := statetest.NewBuilder()
	log := sessionLog(b, 2, 0)
	log = append(log, b.Spawn("be-1", "backend", "T1", "mgr"), b.Request("be-1", "w1", "mock-1", "pk1"))
	r.at(b.Now().Add(time.Second))
	r.emit(log...)
	r.step(100 * time.Millisecond)
	r.shows("ctrl+g back to the chat")
	if !r.bridge.v.AltScreen() {
		t.Fatal("a worker started: the cockpit opens")
	}
	close(finish)
	r.until("the chat is back", func(string) bool { return !r.bridge.v.AltScreen() })
}

// A person who closed the cockpit during a turn does not have it opened again by the next worker.
func TestChatACockpitTheyClosedStaysClosedForTheTurn(t *testing.T) {
	r := startChat(t, rigOpts{cols: 110, rows: 34, team: 8, mainAgent: "mgr", tickClock: true})
	started := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		close(started)
		<-ctx.Done()
		return TurnResult{Err: ctx.Err()}
	}
	r.submit("build it")
	<-started
	b := statetest.NewBuilder()
	log := sessionLog(b, 2, 0)
	log = append(log, b.Spawn("be-1", "backend", "T1", "mgr"), b.Request("be-1", "w1", "mock-1", "pk1"))
	r.at(b.Now().Add(time.Second))
	r.emit(log...)
	r.step(100 * time.Millisecond)
	r.ctrl('g') // closes it
	r.until("closed", func(string) bool { return !r.bridge.v.AltScreen() })
	log2 := []events.Event{b.Spawn("fe-1", "frontend", "T2", "mgr"), b.Request("fe-1", "w2", "mock-1", "pk1")}
	r.at(b.Now().Add(2 * time.Second))
	r.emit(log2...)
	r.step(100 * time.Millisecond)
	if r.bridge.v.AltScreen() {
		t.Error("the person closed the cockpit: another worker does not open it again")
	}
}
