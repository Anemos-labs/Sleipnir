package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

// A patch names the files it changes in its own diff: the question must not put the path above it as well (it was said twice).
func TestTheQuestionForAPatchNamesTheFileOnce(t *testing.T) {
	k := goldenLook(true)
	patch := "*** Begin Patch\n*** Update File: a/a.go\n@@\n-func Reverse(s string) string { panic(\"todo\") }\n+func Reverse(s string) string { return s }\n*** End Patch"
	call := &toolRun{name: "apply_patch", input: mustJSON(map[string]any{"patch": patch})}
	req := perm.Request{Agent: "be-1", Tool: "apply_patch", Paths: []string{"/work/a/a.go"}, Summary: "a/a.go [default mode: writing /work/a/a.go needs approval]"}
	_, body := k.requestBody(req, call, 90, "/work", "")
	n := 0
	for _, l := range strings.Split(widgettest.Flatten(body), "\n") {
		if strings.HasPrefix(l, "a/a.go") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d lines of the question begin with the file, want 1:\n%s", n, widgettest.Flatten(body))
	}
}

// A request that goes unanswered for long is called what it is: the status line said "Reasoning..." for four minutes of an endpoint that
// had not answered, as if the model were thinking hard.
func TestTheStatusLineSaysWhenItIsWaitingForTheModel(t *testing.T) {
	k := goldenLook(true)
	line := func(waiting time.Duration) string {
		v := &liveView{cols: 100, rows: 24, status: statusView{kind: statusThinking, seed: 1, elapsed: 3 * time.Minute, waiting: waiting}}
		return widgettest.Flatten([]cell.Line{k.statusLine(v, 100)})
	}
	if got := line(80 * time.Second); !strings.Contains(got, "Waiting for the model (1m20s)") {
		t.Errorf("a request unanswered for 80 s: %q", got)
	}
	if got := line(0); strings.Contains(got, "Waiting for the model") {
		t.Errorf("a request in its first seconds: %q", got)
	}
}

// The status line in the real chat, not only drawn from a made-up status: a request that has gone unanswered for 80 s makes the live
// status say so (the line was written and tested alone, and nothing in the chat ever set it).
func TestTheChatSaysItIsWaitingForTheModelWhenARequestIsUnanswered(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 24})
	done := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		<-done
		return TurnResult{Steps: 1}
	}
	defer close(done)
	r.typeText("fix it")
	r.enter()
	r.until("the turn running", func(s string) bool { return strings.Contains(s, "esc to interrupt") })
	b := statetest.NewBuilder()
	r.emit(b.Request("main", "r1", "mock-1", "pk1", statetest.Sec{Name: "shared", Tokens: 3200, BP: true}))
	r.at(b.Now())
	r.step(80 * time.Second)
	r.until("the wait named", func(s string) bool { return strings.Contains(s, "Waiting for the model (1m20s)") })
}
