package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
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

// The question about a build or test command has a fourth answer, which allows the builds and tests of every worker for the session: a team of
// eight asks for go test, go build and gofmt one after the other, and one yes should cover them. Every other question keeps its three.
func TestAQuestionAboutATestCommandOffersToAllowBuildsAndTests(t *testing.T) {
	plain := perm.Request{Tool: "Bash", Command: "curl https://example.com"}
	if opts, _ := dialogOptions(plain); len(opts) != 3 {
		t.Errorf("an ordinary question has %d answers", len(opts))
	}
	test := perm.Request{Tool: "Bash", Command: "go test ./...", Remembers: `"go test" commands`, OffersTests: true}
	opts, _ := dialogOptions(test)
	if len(opts) != 4 || !strings.Contains(opts[2].Label, "builds and tests") {
		t.Fatalf("a question about go test has %d answers: %+v", len(opts), opts)
	}
	three, ok := AnswerFor(test, input.RuneKey('3', 0))
	if !ok || !three.Allow || three.Preset != perm.PresetTests || three.Remember != perm.ScopeSession {
		t.Errorf("the third answer: %+v %v", three, ok)
	}
	if four, ok := AnswerFor(test, input.RuneKey('4', 0)); !ok || four.Allow {
		t.Errorf("the fourth answer is no: %+v %v", four, ok)
	}
	if _, ok := AnswerFor(plain, input.RuneKey('4', 0)); ok {
		t.Error("an ordinary question has no fourth answer")
	}
}
