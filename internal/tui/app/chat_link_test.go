package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The link between a session and the program does nothing but forward, and never leaves an agent waiting on a program that is gone.

func recv(t *testing.T, l *ChatLink) chatMsg {
	t.Helper()
	select {
	case m := <-l.msgs:
		return m
	case <-time.After(time.Minute):
		t.Fatal("nothing was forwarded (a hang guard)")
		return chatMsg{}
	}
}

func TestTheSinkForwardsInOrderWhatItIsGiven(t *testing.T) {
	l := NewChatLink()
	s := l.Sink()
	call := toolCall("c1", "bash", map[string]any{"command": "ls"})
	res := &tools.Result{Text: "a\nb", Meta: map[string]any{"lines": 2}}
	s.Text("main", "hello")
	s.Thinking("main", "never shown")
	s.ToolStart("be-2", call)
	s.ToolEnd("be-2", call, res, 1500*time.Millisecond)
	s.Reset("main")
	s.Response("main", nil, 0.75)
	s.Notice("be-2", "warn", "slow down")
	m := recv(t, l)
	if m.kind != mText || m.agent != "main" || m.text != "hello" {
		t.Errorf("text: %+v", m)
	}
	if m = recv(t, l); m.kind != mToolStart || m.agent != "be-2" || m.call.ToolName != "bash" {
		t.Errorf("a call begins: %+v", m)
	}
	if m = recv(t, l); m.kind != mToolEnd || m.res.Text != "a\nb" || m.took != 1500*time.Millisecond || m.res.Meta["lines"] != 2 {
		t.Errorf("a call ends: %+v", m)
	}
	if m = recv(t, l); m.kind != mReset || m.agent != "main" {
		t.Errorf("a retry: %+v", m)
	}
	if m = recv(t, l); m.kind != mResponse || m.hit != 0.75 {
		t.Errorf("a response: %+v", m)
	}
	if m = recv(t, l); m.kind != mNotice || m.level != "warn" || m.text != "slow down" || m.agent != "be-2" {
		t.Errorf("a notice: %+v", m)
	}
	select {
	case m := <-l.msgs:
		t.Errorf("the model's reasoning is not forwarded: %+v", m)
	default:
	}
}

// What the agent does with its result after the tool ended must not be seen by the program, nor race with it.
func TestAResultIsCopiedWhenTheToolEnds(t *testing.T) {
	l := NewChatLink()
	res := &tools.Result{Text: "kept", Meta: map[string]any{"diff": "d"}}
	l.Sink().ToolEnd("main", toolCall("c", "edit", nil), res, 0)
	res.Meta["diff"] = "changed afterwards"
	res.Meta["new"] = 1
	res.Text = "changed afterwards"
	m := recv(t, l)
	if m.res.Text != "kept" || m.res.Meta["diff"] != "d" || len(m.res.Meta) != 1 {
		t.Errorf("the program got %+v", m.res)
	}
	if got := snapshotResult(nil); got.Text != "" || got.Meta != nil {
		t.Errorf("no result is an empty one: %+v", got)
	}
	failed := snapshotResult(&tools.Result{Text: "x", IsError: true})
	if !failed.Failed || !failed.IsError {
		t.Errorf("an error is a failure: %+v", failed)
	}
}

func TestAQuestionIsAnsweredThroughTheLink(t *testing.T) {
	l := NewChatLink()
	ask := l.Prompter()
	got := make(chan perm.Decision, 1)
	go func() { got <- ask(context.Background(), perm.Request{Agent: "main", Tool: "bash", Command: "ls"}) }()
	m := recv(t, l)
	if m.kind != mQuestion || m.q.req.Command != "ls" {
		t.Fatalf("the question is forwarded: %+v", m)
	}
	m.q.ans <- perm.Decision{Allow: true, Reason: "allowed by user"}
	select {
	case d := <-got:
		if !d.Allow || d.Reason != "allowed by user" {
			t.Errorf("the decision: %+v", d)
		}
	case <-time.After(time.Minute):
		t.Fatal("the answer did not come back (a hang guard)")
	}
}

// A turn that is cancelled takes no answer: the question is refused, and the program is told so that it takes it off the screen.
func TestAQuestionOfACancelledTurnIsRefusedAndTheProgramIsTold(t *testing.T) {
	l := NewChatLink()
	ask := l.Prompter()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan perm.Decision, 1)
	go func() { got <- ask(ctx, perm.Request{Tool: "bash"}) }()
	q := recv(t, l)
	if q.kind != mQuestion {
		t.Fatalf("%+v", q)
	}
	cancel()
	select {
	case d := <-got:
		if d.Allow || d.Reason != "no answer" {
			t.Errorf("a refusal that says there was no answer: %+v", d)
		}
	case <-time.After(time.Minute):
		t.Fatal("the cancelled question was not refused (a hang guard)")
	}
	if gone := recv(t, l); gone.kind != mQuestionGone || gone.q != q.q {
		t.Errorf("the program is told which question went: %+v", gone)
	}
}

// An agent that outlives the program never waits on it: a closed link drops what it is given and refuses at once.
func TestAClosedLinkDropsAndRefuses(t *testing.T) {
	l := NewChatLink()
	l.Close()
	l.Close() // more than once is fine
	select {
	case <-l.Done():
	default:
		t.Fatal("the link says it is over")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s := l.Sink()
		for range 2 * linkBuffer { // more than it could ever hold
			s.Text("main", "x")
		}
		if d := l.Prompter()(context.Background(), perm.Request{Tool: "bash"}); d.Allow {
			t.Errorf("a question on a closed link is refused: %+v", d)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("an agent waited on a program that is gone (a hang guard)")
	}
}

// A sender that waits for room is let go when the link ends.
func TestClosingTheLinkLetsAWaitingAgentGo(t *testing.T) {
	l := NewChatLink()
	s := l.Sink()
	for range linkBuffer {
		s.Text("main", "x") // the buffer is full
	}
	done := make(chan struct{})
	go func() { s.Text("main", "one more"); close(done) }()
	l.Close()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("a sender waited on a link that was closed (a hang guard)")
	}
}

// Many agents at once: nothing is lost, and the race detector has nothing to say.
func TestTheSinkIsSafeForManyAgentsAtOnce(t *testing.T) {
	l := NewChatLink()
	s := l.Sink()
	const agents, each = 8, 200
	var wg sync.WaitGroup
	for a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := string(rune('a' + a))
			for range each {
				s.Text(name, "x")
				s.Notice(name, "info", "n")
			}
		}()
	}
	wg.Wait()
	counts := map[string]int{}
	for range agents * each * 2 {
		counts[recv(t, l).agent]++
	}
	for a := range agents {
		if got := counts[string(rune('a'+a))]; got != each*2 {
			t.Errorf("agent %d: %d messages arrived, want %d", a, got, each*2)
		}
	}
}
