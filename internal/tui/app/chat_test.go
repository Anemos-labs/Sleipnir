package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tui/input"
)

// The tests of the chat program: a scripted session behind a real renderer, read through the emulator.

func contains(s string, words ...string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

func TestChatDrawsItsFirstScreen(t *testing.T) {
	r := startChat(t, rigOpts{})
	s := r.screen()
	for _, want := range []string{"◆ sleipnir 0.1.0", "mock/mock-1", "/work/proj", "╭", "❯ Type a goal", "╰", "default", "ctrl+t stack", "20260102-030405-abcdef"} {
		if !strings.Contains(s, want) {
			t.Errorf("the first screen lacks %q:\n%s", want, s)
		}
	}
	if _, _, visible := r.bridge.v.Cursor(); !visible {
		t.Error("the cursor is shown: it rests in the prompt")
	}
	x, y, _ := r.bridge.v.Cursor()
	rows := r.bridge.v.Rows()
	if !strings.Contains(rows[y], "❯") || x != 4 {
		t.Errorf("the cursor is at (%d,%d), which is not the start of the prompt's text: %q", x, y, rows[y])
	}
}

func TestChatTypingSubmitAndTheGoalReachesTheSession(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.typeText("fix the build")
	if s := r.screen(); !strings.Contains(s, "❯ fix the build") {
		t.Fatalf("what is typed is in the prompt:\n%s", s)
	}
	done := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		<-done
		return TurnResult{Steps: 1, CostUSD: 0.0041, HitRatio: 0.5}
	}
	r.enter()
	s := r.until("the turn running", func(s string) bool { return strings.Contains(s, "esc to interrupt") })
	if strings.Count(s, "fix the build") != 1 {
		t.Errorf("the goal is in the scrollback once, and not in the prompt any more:\n%s", s)
	}
	if !strings.Contains(s, "Starting") && !strings.Contains(s, "…") {
		t.Errorf("the status line says that the agent works:\n%s", s)
	}
	close(done)
	s = r.shows("1 step", "$0.0041", "cache hit 50%")
	if strings.Contains(s, "esc to interrupt") {
		t.Errorf("the status line goes when the turn is over:\n%s", s)
	}
	if got := r.host.seen(); len(got) != 1 || got[0] != "fix the build" {
		t.Errorf("the session got %q", got)
	}
}

func TestChatStreamsTheAnswerAsMarkdown(t *testing.T) {
	r := startChat(t, rigOpts{cols: 60})
	gate := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.Text("main", "# Plan\n\nFirst **bold** and `code`, then a list:\n\n- one\n- two\n\n")
		r.sink.Text("main", "```go\nfunc main() {}\n```\n\nThe end")
		<-gate
		r.sink.Response("main", nil, 0)
		return TurnResult{Steps: 1}
	}
	r.submit("go")
	s := r.shows("Plan", "First bold and code", "• one", "func main() {}", "The end")
	if strings.ContainsAny(s, "*`#") {
		t.Errorf("the markers of the markdown are not on the screen:\n%s", s)
	}
	if !strings.Contains(s, "● Plan") {
		t.Errorf("the first line of the answer has the bullet:\n%s", s)
	}
	close(gate)
	r.shows("1 step")
	// what is final is in the scrollback and not drawn again: the answer is on the screen once
	s = r.screen()
	if strings.Count(s, "The end") != 1 {
		t.Errorf("the answer is printed once:\n%s", s)
	}
}

func TestChatAToolCallIsALineWithItsResult(t *testing.T) {
	r := startChat(t, rigOpts{cols: 80})
	gate := make(chan struct{})
	call := toolCall("c1", "bash", map[string]any{"command": "go test ./..."})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.ToolStart("main", call)
		<-gate
		r.sink.ToolEnd("main", call, okResult("ok  \texample.com/orders\t0.318s\n[exit code 0]", map[string]any{"exit_code": 0}), 1400*time.Millisecond)
		return TurnResult{Steps: 1}
	}
	r.submit("run the tests")
	s := r.shows("Bash go test ./...")
	if !strings.Contains(s, "Running Bash") {
		t.Errorf("the status line says which tool runs:\n%s", s)
	}
	close(gate)
	s = r.shows("✓ 1.4s")
	if !strings.Contains(s, "● Bash go test ./...  ✓ 1.4s") {
		t.Errorf("the call is ● Name command, then a mark and how long it took:\n%s", s)
	}
	if !strings.Contains(s, "⎿ ok      example.com/orders  0.318s") {
		t.Errorf("the result is under it, behind ⎿, with its tabs expanded and without the tool's own exit line:\n%s", s)
	}
	if strings.Contains(s, "exit code") {
		t.Errorf("the exit line of the tool is the mark's business:\n%s", s)
	}
}

func TestChatAFailedCommandIsMarkedAndSaysHowItEnded(t *testing.T) {
	r := startChat(t, rigOpts{})
	call := toolCall("c1", "bash", map[string]any{"command": "go vet ./..."})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.ToolStart("main", call)
		r.sink.ToolEnd("main", call, okResult("vet: boom\n[exit code 2]", map[string]any{"exit_code": 2}), 2*time.Second)
		return TurnResult{Steps: 1}
	}
	r.submit("vet")
	s := r.shows("✗ 2.0s")
	if !strings.Contains(s, "✗ 2.0s · exit 2") || !strings.Contains(s, "⎿ vet: boom") {
		t.Errorf("a command that exited with 2 is a cross and says so, and its output is shown:\n%s", s)
	}
}

func TestChatLongOutputIsCollapsedAndCtrlOBringsItBack(t *testing.T) {
	r := startChat(t, rigOpts{rows: 40})
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	call := toolCall("c1", "bash", map[string]any{"command": "seq 30"})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.ToolStart("main", call)
		r.sink.ToolEnd("main", call, okResult(strings.Join(lines, "\n")+"\n[exit code 0]", map[string]any{"exit_code": 0}), time.Second)
		return TurnResult{Steps: 1}
	}
	r.submit("seq")
	s := r.shows("ctrl+o to expand")
	if !strings.Contains(s, "line 1") || !strings.Contains(s, "line 30") || strings.Contains(s, "line 15") || !strings.Contains(s, "+24 lines") {
		t.Errorf("the head and the tail are shown, the middle is folded into a count:\n%s", s)
	}
	r.ctrl('o')
	s = r.shows("whole output", "line 15")
	if strings.Count(s, "line 30") != 2 {
		t.Errorf("ctrl+o writes the whole output into the scrollback:\n%s", s)
	}
	r.ctrl('o')
	if !strings.Contains(r.screen(), "nothing is collapsed") {
		t.Errorf("there is nothing more to expand:\n%s", r.screen())
	}
}

func TestChatAnEditIsADiffWithLineNumbers(t *testing.T) {
	r := startChat(t, rigOpts{cols: 90})
	call := toolCall("c1", "edit", map[string]any{"path": "/work/proj/orders/list.go", "old_string": "a", "new_string": "b"})
	diff := "@@ -58,3 +58,3 @@\n func (s *Store) List() {\n-\toffset := page * size\n+\toffset := (page - 1) * size\n \treturn nil"
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.ToolStart("main", call)
		r.sink.ToolEnd("main", call, okResult("Edited", map[string]any{"diff": diff, "added": 1, "removed": 1}), 30*time.Millisecond)
		return TurnResult{Steps: 1}
	}
	r.submit("fix it")
	s := r.shows("● Edit orders/list.go", "offset := (page - 1) * size")
	for _, want := range []string{"+1 −1", "58 58   func (s *Store) List() {", "59    -     offset := page * size", "59 +     offset := (page - 1) * size", "60 60       return nil"} {
		if !strings.Contains(s, want) {
			t.Errorf("the diff lacks %q:\n%s", want, s)
		}
	}
}

func TestChatNoticesAndRetries(t *testing.T) {
	r := startChat(t, rigOpts{})
	gate := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.Notice("main", "warn", "server (http 429): slow down; retrying in 4s (attempt 2 of 6)")
		r.sink.Notice("main", "info", "an informational notice")
		<-gate
		return TurnResult{Steps: 1}
	}
	r.submit("go")
	s := r.shows("retrying in 4s (attempt 2 of 6)")
	if !strings.Contains(s, "⚠ server (http 429)") {
		t.Errorf("a warning has the sign:\n%s", s)
	}
	if strings.Contains(s, "informational") {
		t.Errorf("information is for --verbose:\n%s", s)
	}
	close(gate)
	r.shows("1 step")
}

func TestChatVerboseShowsInformation(t *testing.T) {
	r := startChat(t, rigOpts{verbose: true})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.Notice("main", "info", "an informational notice")
		return TurnResult{Steps: 1}
	}
	r.submit("go")
	r.shows("an informational notice")
}

func TestChatATurnThatFailsSaysWhy(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		return TurnResult{Err: errors.New("the provider is down")}
	}
	r.submit("go")
	s := r.shows("error: the provider is down")
	if !strings.Contains(s, "✗ error: the provider is down") {
		t.Errorf("an error has the cross:\n%s", s)
	}
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		return TurnResult{Err: errors.New("budget"), Message: "stopped: the budget of $5.00 is exhausted"}
	}
	r.submit("again")
	s = r.shows("stopped: the budget of $5.00 is exhausted")
	if strings.Count(s, "error: budget") != 0 {
		t.Errorf("a message replaces the words of the error:\n%s", s)
	}
}

// ---- Ctrl-C ----

func TestChatCtrlCCancelsTheTurnAndNothingElse(t *testing.T) {
	r := startChat(t, rigOpts{})
	started := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		if goal == "slow" {
			close(started)
			<-ctx.Done()
			return TurnResult{Err: ctx.Err()}
		}
		r.sink.Text("main", "hi there")
		r.sink.Response("main", nil, 0)
		return TurnResult{Steps: 1}
	}
	r.submit("slow")
	<-started
	r.ctrl('c')
	s := r.shows("(cancelled)")
	if strings.Contains(s, "press Ctrl-C again") {
		t.Errorf("a Ctrl-C that cancels a turn says nothing about quitting:\n%s", s)
	}
	// the session is still there: the prompt takes a goal, and is answered
	r.submit("hello")
	r.shows("hi there")
	select {
	case e := <-r.done:
		t.Fatalf("the program ended (%v): Ctrl-C must not end the session", e)
	default:
	}
}

func TestChatCtrlCAtThePromptAsksBeforeItQuits(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.ctrl('c')
	s := r.shows(QuitHint)
	if strings.Count(s, QuitHint) != 1 {
		t.Errorf("the hint is said once:\n%s", s)
	}
	// a second press, soon after, quits
	r.step(time.Second)
	r.keys <- input.RuneKey('c', input.Ctrl)
	if e := r.wait(); e.end != ChatInterrupted || e.err != nil {
		t.Errorf("the chat ended %+v, want interrupted and no error", e)
	}
}

func TestChatASecondCtrlCAfterTheWindowIsAFirstOne(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.ctrl('c')
	r.shows(QuitHint)
	r.step(2*time.Second + time.Millisecond)
	r.ctrl('c')
	s := r.until("the hint twice", func(s string) bool { return strings.Count(s, QuitHint) == 2 })
	_ = s
	select {
	case e := <-r.done:
		t.Fatalf("the program ended (%v) on a press that came after the window", e)
	default:
	}
}

func TestChatTypingBetweenTwoCtrlCsMakesTheSecondAFirst(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.ctrl('c')
	r.shows(QuitHint)
	r.typeText("x")
	r.ctrl('c') // clears what was typed, and is a first press again
	r.until("the hint twice", func(s string) bool { return strings.Count(s, QuitHint) == 2 })
	if strings.Contains(r.screen(), "❯ x") {
		t.Errorf("Ctrl-C discards what was typed:\n%s", r.screen())
	}
	r.keys <- input.RuneKey('c', input.Ctrl)
	if e := r.wait(); e.end != ChatInterrupted {
		t.Errorf("two presses with nothing between them quit: %+v", e)
	}
}

func TestChatCtrlCDropsAHalfTypedLine(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.typeText("@hel")
	r.ctrl('c')
	r.shows(QuitHint)
	r.typeText("hello")
	r.enter()
	r.until("the goal", func(s string) bool { return len(r.host.seen()) == 1 })
	if got := r.host.seen(); got[0] != "hello" {
		t.Errorf("the goal is what was typed after the Ctrl-C, got %q", got)
	}
}

func TestChatCtrlDQuitsAtOnceWhenNothingRuns(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.keys <- input.RuneKey('d', input.Ctrl)
	if e := r.wait(); e.end != ChatQuit || e.err != nil {
		t.Errorf("Ctrl-D ended the chat %+v, want quit", e)
	}
}

func TestChatCtrlDDuringATurnQuitsAfterIt(t *testing.T) {
	r := startChat(t, rigOpts{})
	gate := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		<-gate
		r.sink.Text("main", "the turn was not cut short")
		r.sink.Response("main", nil, 0)
		return TurnResult{Steps: 1}
	}
	r.submit("slow")
	r.until("the turn", func(s string) bool { return strings.Contains(s, "esc to interrupt") })
	r.ctrl('d')
	r.shows("quit when this is done")
	close(gate)
	if e := r.wait(); e.end != ChatQuit {
		t.Errorf("the chat ended %+v", e)
	}
	if s := r.screen(); !strings.Contains(s, "the turn was not cut short") {
		t.Errorf("the turn ran to its end:\n%s", s)
	}
}

func TestChatEscInterruptsTheTurnAndIsTheEditorsWhenThereIsText(t *testing.T) {
	r := startChat(t, rigOpts{})
	started := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		close(started)
		<-ctx.Done()
		return TurnResult{Err: ctx.Err()}
	}
	r.submit("slow")
	<-started
	r.typeText("ab")
	r.special(input.Esc) // with text in the prompt the first Esc arms the clear, as it does at any prompt
	if s := r.screen(); strings.Contains(s, "(cancelled)") || !strings.Contains(s, "press Esc again to clear") {
		t.Fatalf("Esc with text in the prompt does not interrupt:\n%s", s)
	}
	r.ctrl('u')
	r.special(input.Esc)
	r.shows("(cancelled)")
}

func TestChatASlowStartIsCancelledByCtrlCAndEsc(t *testing.T) {
	for name, key := range map[string]input.Key{"ctrl-c": input.RuneKey('c', input.Ctrl), "esc": input.SpecialKey(input.Esc, 0)} {
		t.Run(name, func(t *testing.T) {
			r := startChat(t, rigOpts{noAttach: true})
			if s := r.screen(); !strings.Contains(s, "Starting") {
				t.Fatalf("the status line says the session is being made:\n%s", s)
			}
			r.press(key)
			r.mu.Lock()
			cancelled := r.cancelled
			r.mu.Unlock()
			if !cancelled {
				t.Error("the start was not cancelled")
			}
			r.attach <- ChatAttach{Err: context.Canceled}
			if e := r.wait(); e.end != ChatFailed || !errors.Is(e.err, context.Canceled) {
				t.Errorf("the chat ended %+v, want a failure that is the cancellation", e)
			}
		})
	}
}

func TestChatTypingWhileTheSessionStartsIsKept(t *testing.T) {
	r := startChat(t, rigOpts{noAttach: true})
	r.submit("typed early")
	r.shows("queued", "typed early")
	r.attachSession()
	r.until("the goal", func(string) bool { return len(r.host.seen()) == 1 })
	if got := r.host.seen(); got[0] != "typed early" {
		t.Errorf("got %q", got)
	}
}

func TestChatASessionThatCannotBeMadeEndsTheProgramWithWhy(t *testing.T) {
	r := startChat(t, rigOpts{noAttach: true})
	r.attach <- ChatAttach{Err: errors.New("no provider is configured")}
	e := r.wait()
	if e.end != ChatFailed || e.err == nil || !strings.Contains(e.err.Error(), "no provider is configured") {
		t.Errorf("the chat ended %+v", e)
	}
}

func TestChatTheProcessBeingToldToStopCancelsTheTurnAndEnds(t *testing.T) {
	r := startChat(t, rigOpts{})
	started := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		close(started)
		<-ctx.Done()
		return TurnResult{Err: ctx.Err()}
	}
	r.submit("slow")
	<-started
	r.cancel() // SIGTERM: the context of the process
	e := r.wait()
	if e.end != ChatInterrupted || e.err != nil {
		t.Errorf("the chat ended %+v, want interrupted", e)
	}
	if s := r.screen(); !strings.Contains(s, "(cancelled)") {
		t.Errorf("the turn says it was cancelled before the chat goes:\n%s", s)
	}
}

// ---- typing ahead ----

func TestChatLinesTypedAheadRunInOrderWhenTheTurnEnds(t *testing.T) {
	r := startChat(t, rigOpts{})
	gate := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		if goal == "first" {
			<-gate
		}
		return TurnResult{Steps: 1}
	}
	r.submit("first")
	r.until("the turn", func(s string) bool { return strings.Contains(s, "esc to interrupt") })
	r.submit("second")
	r.submit("third")
	s := r.shows("queued: “second” (+1 more)")
	if got := r.host.seen(); len(got) != 1 {
		t.Fatalf("a line typed ahead is not sent while the turn runs: %q\n%s", got, s)
	}
	close(gate)
	r.until("all three", func(string) bool { return len(r.host.seen()) == 3 })
	if got := r.host.seen(); strings.Join(got, ",") != "first,second,third" {
		t.Errorf("the goals ran in the order they were typed: %q", got)
	}
	s = r.until("everything has run", func(s string) bool { return !strings.Contains(s, "esc to interrupt") && !strings.Contains(s, "queued") })
	if strings.Count(s, "── ") != 3 {
		t.Errorf("each turn leaves its summary:\n%s", s)
	}
}

func TestChatALineTypedAheadSurvivesCtrlC(t *testing.T) {
	r := startChat(t, rigOpts{})
	started := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		if goal == "slow" {
			close(started)
			<-ctx.Done()
			return TurnResult{Err: ctx.Err()}
		}
		return TurnResult{Steps: 1}
	}
	r.submit("slow")
	<-started
	r.submit("hello")
	r.ctrl('c')
	r.until("both goals", func(string) bool { return len(r.host.seen()) == 2 })
	if got := r.host.seen(); strings.Join(got, ",") != "slow,hello" {
		t.Errorf("the line typed ahead runs after the cancelled turn: %q", got)
	}
}

// ---- a question ----

// ask starts a turn whose agent puts a question to the prompter, as an agent does, and returns where the decision will be. Every other
// goal is a turn that ends at once.
func (r *chatRig) ask(req perm.Request) <-chan perm.Decision {
	out := make(chan perm.Decision, 1)
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		if goal == "ask" {
			out <- r.prompt(ctx, req)
		}
		return TurnResult{Steps: 1}
	}
	r.submit("ask")
	return out
}

func decision(t *testing.T, ch <-chan perm.Decision) perm.Decision {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(time.Minute):
		t.Fatal("the question was not answered (a hang guard)")
		return perm.Decision{}
	}
}

func bashRequest(cmd string) perm.Request {
	return perm.Request{Agent: "main", Tool: "bash", Command: cmd, Summary: "run a command [not on the allow list]"}
}

func TestChatAQuestionIsADialogAndEachKeyAnswersIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keys  []input.Key
		allow bool
		scope perm.Scope
		why   string
	}{
		{"1", []input.Key{input.RuneKey('1', 0)}, true, perm.ScopeOnce, "allowed by user"},
		{"2", []input.Key{input.RuneKey('2', 0)}, true, perm.ScopeSession, "allowed by user for the session"},
		{"3", []input.Key{input.RuneKey('3', 0)}, false, perm.ScopeOnce, "denied by user"},
		{"enter takes the choice, which starts at yes", []input.Key{input.SpecialKey(input.Enter, 0)}, true, perm.ScopeOnce, "allowed by user"},
		{"down and enter", []input.Key{input.SpecialKey(input.Down, 0), input.SpecialKey(input.Enter, 0)}, true, perm.ScopeSession, "allowed by user for the session"},
		{"up wraps to no", []input.Key{input.SpecialKey(input.Up, 0), input.SpecialKey(input.Enter, 0)}, false, perm.ScopeOnce, "denied by user"},
		{"tab and enter", []input.Key{input.SpecialKey(input.Tab, 0), input.SpecialKey(input.Enter, 0)}, true, perm.ScopeSession, "allowed by user for the session"},
		{"esc says no", []input.Key{input.SpecialKey(input.Esc, 0)}, false, perm.ScopeOnce, "denied by user"},
		{"ctrl-d is no answer", []input.Key{input.RuneKey('d', input.Ctrl)}, false, perm.ScopeOnce, "no answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := startChat(t, rigOpts{})
			ans := r.ask(bashRequest("go test ./..."))
			s := r.shows("Run a command", "$ go test ./...", "1. Yes", "2. Yes, and don't ask again for this command this session", "3. No, and tell Sleipnir what to do instead", "not on the allow list")
			if !strings.Contains(s, "❯ 1. Yes") {
				t.Errorf("the choice starts on yes:\n%s", s)
			}
			for _, k := range tc.keys {
				r.press(k)
			}
			d := decision(t, ans)
			if d.Allow != tc.allow || d.Remember != tc.scope || d.Reason != tc.why {
				t.Errorf("the decision is %+v, want allow %v, scope %q, reason %q", d, tc.allow, tc.scope, tc.why)
			}
			s = r.until("the dialog gone", func(s string) bool { return !strings.Contains(s, "1. Yes") })
			if strings.Contains(s, "Sleipnir what to do") {
				t.Errorf("the dialog is off the screen once answered:\n%s", s)
			}
		})
	}
}

func TestChatLettersNeverAnswerAQuestion(t *testing.T) {
	r := startChat(t, rigOpts{})
	ans := r.ask(bashRequest("rm x"))
	r.shows("1. Yes")
	for _, c := range "yanYAN" {
		r.press(input.RuneKey(c, 0))
	}
	select {
	case d := <-ans:
		t.Fatalf("a letter answered the question: %+v", d)
	default:
	}
	if s := r.screen(); !strings.Contains(s, "yanYAN") {
		t.Errorf("the letters went to the prompt:\n%s", s)
	}
}

// The numbers are the only characters a question takes, and only those of its options: another digit is a character of the line.
func TestChatADigitThatNumbersNoOptionIsTyping(t *testing.T) {
	r := startChat(t, rigOpts{})
	ans := r.ask(bashRequest("rm x"))
	r.shows("1. Yes")
	for _, c := range "4907" {
		r.press(input.RuneKey(c, 0))
	}
	select {
	case d := <-ans:
		t.Fatalf("a digit that is no option answered the question: %+v", d)
	default:
	}
	if s := r.screen(); !strings.Contains(s, "4907") {
		t.Errorf("the digits went to the prompt:\n%s", s)
	}
}

func TestChatAQuestionOfAToolServerHasItsOwnAnswers(t *testing.T) {
	r := startChat(t, rigOpts{})
	ans := r.ask(perm.Request{Agent: "", Tool: "mcp-server", Summary: "start the project's tool server \"git\": npx -y mcp-git"})
	s := r.shows("Start a tool server", "1. Yes, start it this time", "2. Yes, and remember this exact entry for this project", "3. No")
	if !strings.Contains(s, "npx -y mcp-git") {
		t.Errorf("the entry is shown:\n%s", s)
	}
	r.press(input.RuneKey('2', 0))
	if d := decision(t, ans); !d.Allow || d.Remember != perm.ScopeProject {
		t.Errorf("decision %+v", d)
	}
}

func TestChatCtrlCDuringAQuestionCancelsTheTurnAndTheQuestionTakesNoInput(t *testing.T) {
	r := startChat(t, rigOpts{})
	var ans <-chan perm.Decision
	asked := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		if goal == "write" {
			out := make(chan perm.Decision, 1)
			ans = out
			go func() { out <- r.prompt(ctx, bashRequest("touch x")) }()
			close(asked)
			<-ctx.Done()
			return TurnResult{Err: ctx.Err()}
		}
		return TurnResult{Steps: 1}
	}
	r.submit("write")
	<-asked
	r.shows("1. Yes")
	r.ctrl('c')
	r.shows("(cancelled)")
	if d := decision(t, ans); d.Allow {
		t.Errorf("a cancelled question is not an approval: %+v", d)
	}
	// the next keys go to the prompt; a digit is text, and starts a goal
	r.typeText("1")
	if s := r.screen(); !strings.Contains(s, "❯ 1") || strings.Contains(s, "1. Yes") {
		t.Errorf("a key after the question was cancelled is typing:\n%s", s)
	}
}

// What was typed while the agent worked must never answer a question, and neither may the keys of a sentence that is half typed
// when the question appears. The question waits for the keyboard to go quiet.
func TestChatTypeAheadNeverAnswersAQuestion(t *testing.T) {
	r := startChat(t, rigOpts{answerAfter: 400 * time.Millisecond})
	ans := r.ask(bashRequest("rm -rf build"))
	r.shows("1. Yes", "your typing goes to the prompt until you pause")
	// keys arrive at once, in the instant the question appears: they are the prompt's
	for _, c := range "see 2 and 1" {
		r.press(input.RuneKey(c, 0))
	}
	r.press(input.SpecialKey(input.Enter, 0)) // sends the line typed, and does not approve
	select {
	case d := <-ans:
		t.Fatalf("the keys typed ahead answered the question: %+v", d)
	default:
	}
	if got := r.host.seen(); len(got) != 1 {
		t.Fatalf("a line typed while a question waits is not sent: %q", got)
	}
	s := r.screen()
	if !contains(s, "queued: “see 2 and 1”") {
		t.Errorf("the line typed while the question waited is queued, not lost:\n%s", s)
	}
	// the keyboard goes quiet for the pause: now a digit answers
	r.step(300 * time.Millisecond)
	r.press(input.RuneKey('1', 0))
	select {
	case d := <-ans:
		t.Fatalf("a key 300 ms after the last keystroke answered: %+v", d)
	default:
	}
	r.step(500 * time.Millisecond)
	r.shows("1 2 3")
	r.press(input.RuneKey('3', 0))
	if d := decision(t, ans); d.Allow {
		t.Errorf("the answer was 3: %+v", d)
	}
}

func TestChatEnterSendsWhatIsTypedAndDoesNotApprove(t *testing.T) {
	r := startChat(t, rigOpts{})
	ans := r.ask(bashRequest("rm x"))
	r.shows("1. Yes")
	r.typeText("later")
	r.enter()
	select {
	case d := <-ans:
		t.Fatalf("enter answered the question although something was typed: %+v", d)
	default:
	}
	if s := r.screen(); !strings.Contains(s, "queued: “later”") {
		t.Errorf("the line typed ahead is queued:\n%s", s)
	}
	r.enter() // the prompt is empty now: enter takes the choice
	if d := decision(t, ans); !d.Allow {
		t.Errorf("enter on an empty prompt takes the choice, which is yes: %+v", d)
	}
}

// Questions wait their turn, and the one behind is not answered by the key that answered the one in front (pressed twice, or pressed
// again as the next one appeared): it takes keys only after the keyboard has been quiet, like a question that has just come.
func TestChatTheNextQuestionIsNotAnsweredByTheKeyThatAnsweredTheOneBefore(t *testing.T) {
	r := startChat(t, rigOpts{answerAfter: 400 * time.Millisecond})
	first, second := make(chan perm.Decision, 1), make(chan perm.Decision, 1)
	secondNow := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		go func() { first <- r.prompt(ctx, bashRequest("echo one")) }()
		<-secondNow
		go func() { second <- r.prompt(ctx, bashRequest("echo two")) }()
		<-ctx.Done()
		return TurnResult{Err: ctx.Err()}
	}
	r.submit("go")
	r.shows("echo one", "your typing goes to the prompt until you pause")
	r.step(500 * time.Millisecond)
	r.shows("1 2 3")
	close(secondNow)
	r.shows("1 more waiting")      // the program has the second question, which waits behind the first
	r.step(500 * time.Millisecond) // and it has waited as long as a question needs to be armed
	r.press(input.RuneKey('1', 0)) // answers the first
	if d := decision(t, first); !d.Allow {
		t.Fatalf("the first question was answered 1: %+v", d)
	}
	r.shows("echo two")
	r.press(input.RuneKey('1', 0)) // pressed again at once: it is not for a question that has just appeared
	select {
	case d := <-second:
		t.Fatalf("the key that answered the first question answered the second: %+v", d)
	default:
	}
	if s := r.screen(); !strings.Contains(s, "your typing goes to the prompt until you pause") {
		t.Errorf("the second question says why it does not take keys yet:\n%s", s)
	}
	r.ctrl('u') // what was typed is not an answer, and is not a goal either
	r.step(500 * time.Millisecond)
	r.shows("1 2 3")
	r.press(input.RuneKey('3', 0))
	if d := decision(t, second); d.Allow {
		t.Errorf("the second question was answered 3: %+v", d)
	}
}

func TestChatAQuestionFromAnAgentOfASwarmSaysWhoAsks(t *testing.T) {
	r := startChat(t, rigOpts{})
	ans := r.ask(perm.Request{Agent: "be-2", Tool: "edit", Paths: []string{"/work/proj/x.go"}, Summary: "edit x.go"})
	r.shows("Edit a file", "be-2", "x.go")
	r.press(input.RuneKey('3', 0))
	decision(t, ans)
}

func TestChatALongRequestIsWrittenWholeIntoTheScrollback(t *testing.T) {
	r := startChat(t, rigOpts{rows: 24})
	var cmd []string
	for i := 1; i <= 40; i++ {
		cmd = append(cmd, fmt.Sprintf("echo line%d", i))
	}
	ans := r.ask(bashRequest(strings.Join(cmd, "\n")))
	s := r.shows("1. Yes", "needs your answer")
	if !strings.Contains(s, "more: the whole of it is in the scrollback above") {
		t.Errorf("the dialog says that it was cut:\n%s", s)
	}
	if strings.Count(s, "echo line20") != 1 || !strings.Contains(s, "echo line1") || !strings.Contains(s, "echo line40") {
		t.Errorf("both ends are in the dialog and the whole request is in the scrollback:\n%s", s)
	}
	r.press(input.RuneKey('3', 0))
	decision(t, ans)
}

// ---- the prompt ----

func TestChatAPasteOfManyLinesIsAChipAndIsSentWhole(t *testing.T) {
	r := startChat(t, rigOpts{})
	var lines []string
	for i := 1; i <= 50; i++ {
		lines = append(lines, fmt.Sprintf("pasted line %d", i))
	}
	text := strings.Join(lines, "\n")
	r.press(input.PasteKey(text))
	s := r.shows("[pasted text #1 +50 lines]")
	if strings.Contains(s, "pasted line 7") {
		t.Errorf("the paste is a chip, not 50 lines in the prompt:\n%s", s)
	}
	r.enter()
	r.until("the goal", func(string) bool { return len(r.host.seen()) == 1 })
	if got := r.host.seen()[0]; got != text {
		t.Errorf("the session got %d bytes, want the whole paste (%d)", len(got), len(text))
	}
}

func TestChatHistoryRecallsAndIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "history.jsonl")
	hist, err := input.OpenHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	r := startChat(t, rigOpts{history: hist})
	r.submit("first goal")
	r.until("the goal", func(string) bool { return len(r.host.seen()) == 1 })
	r.shows("cache hit")
	r.special(input.Up)
	if s := r.screen(); strings.Count(s, "first goal") != 2 {
		t.Errorf("Up brings back the last prompt into the input:\n%s", s)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"first goal"`) {
		t.Errorf("the history is on disk: %q %v", data, err)
	}
	// a later session has it
	hist2, _ := input.OpenHistory(path)
	if hist2.Len() != 1 || hist2.At(0) != "first goal" {
		t.Errorf("the next session reads %v", hist2.Entries())
	}
}

func TestChatSlashOpensThePaletteAndACommandRuns(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.typeText("/")
	s := r.shows("/help", "/cost", "/compact", "fold the older thread now")
	if !strings.Contains(s, "▸ /help") {
		t.Errorf("the first command is selected:\n%s", s)
	}
	r.typeText("co")
	s = r.screen()
	if strings.Contains(s, "/exit   ") || !strings.Contains(s, "▸ /cost") {
		t.Errorf("typing filters the palette:\n%s", s)
	}
	r.enter() // accepts the selection
	r.enter() // sends it
	r.shows("ran /cost")
	if got := r.host.commands; len(got) != 1 || got[0] != "/cost" {
		t.Errorf("the command reached the session: %q", got)
	}
	if got := r.host.seen(); len(got) != 0 {
		t.Errorf("a command is not a goal: %q", got)
	}
}

// A command that is typed out is sent by enter, not completed by it.
func TestChatEnterSendsACommandThatWasTypedInFull(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.typeText("/cost")
	r.shows("▸ /cost")
	r.enter()
	r.shows("ran /cost")
}

func TestChatASlashCommandCanQuitOrSendAPrompt(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.host.command = func(ctx context.Context, line string, out io.Writer) CommandResult {
		switch line {
		case "/fix 12":
			return CommandResult{Send: "Fix issue 12 and add a regression test."}
		case "/exit":
			return CommandResult{Quit: true}
		}
		fmt.Fprintln(out, "unknown command", line)
		return CommandResult{}
	}
	r.submit("/fix 12")
	r.until("the expanded prompt sent", func(string) bool { return len(r.host.seen()) == 1 })
	if got := r.host.seen()[0]; got != "Fix issue 12 and add a regression test." {
		t.Errorf("the turn got %q", got)
	}
	s := r.shows("/fix 12")
	if strings.Contains(s, "Fix issue 12") {
		t.Errorf("the scrollback shows what was typed, not the expansion:\n%s", s)
	}
	r.submit("/nope")
	r.shows("unknown command /nope")
	r.typeText("/exit")
	r.keys <- input.SpecialKey(input.Enter, 0) // the command ends the chat: nothing is taken after it
	if e := r.wait(); e.end != ChatQuit {
		t.Errorf("/exit ended the chat %+v", e)
	}
}

func TestChatACommandIsCancelledByCtrlC(t *testing.T) {
	r := startChat(t, rigOpts{})
	started := make(chan struct{})
	r.host.command = func(ctx context.Context, line string, out io.Writer) CommandResult {
		close(started)
		<-ctx.Done()
		fmt.Fprintln(out, "compact: context canceled")
		return CommandResult{}
	}
	r.submit("/compact")
	<-started
	s := r.shows("Running /compact")
	_ = s
	r.ctrl('c')
	r.shows("compact: context canceled")
	if strings.Contains(r.screen(), QuitHint) {
		t.Error("the Ctrl-C that cancels a command says nothing about quitting")
	}
}

func TestChatShiftTabCyclesThePermissionMode(t *testing.T) {
	r := startChat(t, rigOpts{})
	shift := input.SpecialKey(input.Tab, input.Shift)
	for _, want := range []string{"accept-edits", "plan", "default"} {
		r.press(shift)
		r.shows("mode: " + want)
		if got := r.host.Mode(); got != want {
			t.Errorf("the mode is %q, want %q", got, want)
		}
	}
	r.host.SetMode("bypass")
	r.press(shift)
	if got := r.host.Mode(); got != "default" {
		t.Errorf("shift+tab never steps into bypass, and leaves it for default: %q", got)
	}
}
