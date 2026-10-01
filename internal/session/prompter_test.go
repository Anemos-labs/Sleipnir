package session

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/perm"
)

// lockedBuffer is what a prompter prints to, read by a test while the prompter runs.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// promptGuard is how long a test waits for something that must happen: a hang guard.
const promptGuard = 20 * time.Second

// waitForQuestions waits until the prompter has printed n questions.
func waitForQuestions(t *testing.T, out *lockedBuffer, n int) {
	t.Helper()
	deadline := time.Now().Add(promptGuard)
	for strings.Count(out.String(), "allow? [y]es once") < n {
		if time.Now().After(deadline) {
			t.Fatalf("the prompter did not print %d question(s) in %v; it printed %q", n, promptGuard, out.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// decisionWithin returns what a prompter decided, or fails the test if it decides nothing
// within the hang guard.
func decisionWithin(t *testing.T, c <-chan perm.Decision, what string) perm.Decision {
	t.Helper()
	select {
	case d := <-c:
		return d
	case <-time.After(promptGuard):
		t.Fatalf("no decision within %v: %s", promptGuard, what)
		return perm.Decision{}
	}
}

func toolRequest() perm.Request {
	return perm.Request{Agent: "worker-2", Tool: "write", Summary: "write notes.txt [default mode: writing notes.txt needs approval]"}
}

func serverRequest() perm.Request {
	return perm.Request{Tool: "mcp-server", Summary: `start the project's MCP server "files" (npx files-server)`}
}

func trustRequest() perm.Request {
	return perm.Request{Tool: perm.ToolProjectTrust, Summary: "use this project's own instructions and settings?\nAGENTS.md: instructions, 1.3 KB\n.claude/skills/ (3 files): skills, 4.1 KB\n\nThese are added to every prompt. [changed since you trusted it: AGENTS.md changed]"}
}

// A question is answered by the line the person types: y once, a for the session, anything
// else (an empty line too) refuses. What `run` and `mcp test` use.
func TestTerminalPrompterAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    perm.Request
		answer string
		want   perm.Decision
	}{
		{"y", toolRequest(), "y\n", perm.Decision{Allow: true, Reason: "allowed by user"}},
		{"yes", toolRequest(), "yes\n", perm.Decision{Allow: true, Reason: "allowed by user"}},
		{"upper case and spaces", toolRequest(), "  YeS \n", perm.Decision{Allow: true, Reason: "allowed by user"}},
		{"CRLF", toolRequest(), "y\r\n", perm.Decision{Allow: true, Reason: "allowed by user"}},
		{"a", toolRequest(), "a\n", perm.Decision{Allow: true, Reason: "allowed by user for the session", Remember: perm.ScopeSession}},
		{"always", toolRequest(), "always\n", perm.Decision{Allow: true, Reason: "allowed by user for the session", Remember: perm.ScopeSession}},
		{"n", toolRequest(), "n\n", perm.Decision{Reason: "denied by user"}},
		{"no", toolRequest(), "no\n", perm.Decision{Reason: "denied by user"}},
		{"an empty line", toolRequest(), "\n", perm.Decision{Reason: "denied by user"}},
		{"something else", toolRequest(), "sure, why not\n", perm.Decision{Reason: "denied by user"}},
		{"p is not an answer to a tool", toolRequest(), "p\n", perm.Decision{Reason: "denied by user"}},
		{"a server: y", serverRequest(), "y\n", perm.Decision{Allow: true, Reason: "allowed by user"}},
		{"a server: p remembers the entry", serverRequest(), "p\n", perm.Decision{Allow: true, Reason: "approved by user for this project", Remember: perm.ScopeProject}},
		{"a server: project", serverRequest(), "project\n", perm.Decision{Allow: true, Reason: "approved by user for this project", Remember: perm.ScopeProject}},
		{"a server: a does not remember", serverRequest(), "a\n", perm.Decision{Reason: "denied by user"}},
		{"a server: n", serverRequest(), "n\n", perm.Decision{Reason: "denied by user"}},
		// the question of the files that come with the project: remembered until they change, and only there
		{"files: y", trustRequest(), "y\n", perm.Decision{Allow: true, Reason: "allowed by user"}},
		{"files: r remembers them until they change", trustRequest(), "r\n", perm.Decision{Allow: true, Reason: "trusted by user until the files change", Remember: perm.ScopeProject}},
		{"files: remember", trustRequest(), "remember\n", perm.Decision{Allow: true, Reason: "trusted by user until the files change", Remember: perm.ScopeProject}},
		{"files: a is not an answer (nothing here is for ever)", trustRequest(), "a\n", perm.Decision{Reason: "denied by user"}},
		{"files: p is the answer of a server, not of this", trustRequest(), "p\n", perm.Decision{Reason: "denied by user"}},
		{"files: n", trustRequest(), "n\n", perm.Decision{Reason: "denied by user"}},
		{"a tool: r is not an answer", toolRequest(), "r\n", perm.Decision{Reason: "denied by user"}},
		{"a server: r is not an answer", serverRequest(), "r\n", perm.Decision{Reason: "denied by user"}},
		// The input ended: nobody answered. A last line with no newline is not taken for an answer.
		{"the input ended", toolRequest(), "", perm.Decision{Reason: "no answer"}},
		{"the input ended after y with no newline", toolRequest(), "y", perm.Decision{Reason: "no answer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out lockedBuffer
			got := TerminalPrompter(strings.NewReader(tc.answer), &out)(context.Background(), tc.req)
			if got != tc.want {
				t.Errorf("decision %+v, want %+v", got, tc.want)
			}
		})
	}
}

// What the person is shown: who asks and what for, and how to answer.
func TestTerminalPrompterShowsTheQuestion(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  perm.Request
		want string
	}{
		{"a tool", toolRequest(), "\nworker-2 wants to: write notes.txt [default mode: writing notes.txt needs approval]\n  allow? [y]es once / [a]lways this session / [n]o: "},
		{"an unnamed agent", perm.Request{Tool: "bash", Summary: "run ls"}, "\nagent wants to: run ls\n  allow? [y]es once / [a]lways this session / [n]o: "},
		{"a server", serverRequest(), "\nSleipnir wants to start the project's MCP server \"files\" (npx files-server)\n  start it? [y]es this time / [p]roject: remember this exact entry / [n]o: "},
		{"the project's own files", trustRequest(), "\nSleipnir asks: use this project's own instructions and settings?\n  AGENTS.md: instructions, 1.3 KB\n  .claude/skills/ (3 files): skills, 4.1 KB\n\n  These are added to every prompt. [changed since you trusted it: AGENTS.md changed]\n  use them? [y]es this time / [r]emember until they change / [n]o: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out lockedBuffer
			TerminalPrompter(strings.NewReader("n\n"), &out)(context.Background(), tc.req)
			if out.String() != tc.want {
				t.Errorf("printed %q, want %q", out.String(), tc.want)
			}
		})
	}
}

// Consecutive questions read consecutive lines, from a reader that is already buffered too.
func TestTerminalPrompterReadsOneLinePerQuestion(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   func() io.Reader
	}{
		{"a plain reader", func() io.Reader { return strings.NewReader("y\nn\na\n") }},
		{"a buffered reader", func() io.Reader { return bufio.NewReader(strings.NewReader("y\nn\na\n")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out lockedBuffer
			ask := TerminalPrompter(tc.in(), &out)
			var got []string
			for range 3 {
				d := ask(context.Background(), toolRequest())
				got = append(got, d.Reason)
			}
			want := []string{"allowed by user", "denied by user", "allowed by user for the session"}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("decisions %q, want %q", got, want)
			}
		})
	}
}

// Questions from several agents are asked one at a time: the second is not shown until the
// first is answered.
func TestTerminalPrompterAsksOneQuestionAtATime(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	var out lockedBuffer
	ask := TerminalPrompter(pr, &out)
	first, second := make(chan perm.Decision, 1), make(chan perm.Decision, 1)

	go func() { first <- ask(context.Background(), toolRequest()) }()
	waitForQuestions(t, &out, 1)
	go func() { second <- ask(context.Background(), toolRequest()) }()
	// The second asker has to wait for its turn: write the first answer, which returns when
	// it has been read, and only then can the second question appear.
	if _, err := pw.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	if d := decisionWithin(t, first, "the first question"); !d.Allow {
		t.Errorf("the first question: %+v", d)
	}
	waitForQuestions(t, &out, 2)
	if _, err := pw.Write([]byte("n\n")); err != nil {
		t.Fatal(err)
	}
	if d := decisionWithin(t, second, "the second question"); d.Allow {
		t.Errorf("the second question: %+v", d)
	}
	if n := strings.Count(out.String(), "allow?"); n != 2 {
		t.Errorf("%d questions were shown, want 2:\n%s", n, out.String())
	}
}

// A question that is cancelled (Ctrl-C ends the turn that asked it) has not consumed
// anything: the line the person types next belongs to whoever reads next. It used to be
// taken by a read the cancelled question left running, and lost.
func TestTerminalPrompterCancelledQuestionDoesNotSwallowTheNextLine(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	var out lockedBuffer
	ask := TerminalPrompter(pr, &out)

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan perm.Decision, 1)
	go func() { first <- ask(ctx, toolRequest()) }()
	waitForQuestions(t, &out, 1)
	cancel()
	if d := decisionWithin(t, first, "the cancelled question"); d.Allow {
		t.Fatalf("a cancelled question was allowed: %+v", d)
	}

	// The next question, and the person's answer to it.
	second := make(chan perm.Decision, 1)
	go func() { second <- ask(context.Background(), toolRequest()) }()
	waitForQuestions(t, &out, 2)
	go pw.Write([]byte("y\n"))
	d := decisionWithin(t, second, "the answer to the second question never arrived: a read left behind by the cancelled one took the line")
	if !d.Allow || d.Reason != "allowed by user" {
		t.Errorf("the second question: %+v", d)
	}
}

// The answer function decides when the question is shown: it has to be ready for the answer
// before the question is visible, because a person (or a script that waits for the question and
// answers at once) types the moment it is. So LinePrompter shows nothing itself; it hands the
// answer function show, and show prints the question once however often it is called.
func TestLinePrompterLetsTheAnswerFunctionShowTheQuestion(t *testing.T) {
	var out lockedBuffer
	var before, after string
	ask := LinePrompter(func(ctx context.Context, show func()) (string, error) {
		before = out.String()
		show()
		show()
		after = out.String()
		return "y", nil
	}, &out)
	if d := ask(context.Background(), toolRequest()); !d.Allow {
		t.Fatalf("%+v", d)
	}
	if before != "" {
		t.Errorf("the question was on the screen before the answer function asked for it: %q", before)
	}
	want := "\nworker-2 wants to: write notes.txt [default mode: writing notes.txt needs approval]\n  allow? [y]es once / [a]lways this session / [n]o: "
	if after != want {
		t.Errorf("after show the screen had %q, want %q (once)", after, want)
	}
}

// An answer function that cannot take an answer shows no question.
func TestLinePrompterShowsNothingWhenThereIsNoOneToAnswer(t *testing.T) {
	var out lockedBuffer
	ask := LinePrompter(func(context.Context, func()) (string, error) { return "", io.EOF }, &out)
	if d := ask(context.Background(), toolRequest()); d.Allow || d.Reason != "no answer" {
		t.Fatalf("%+v", d)
	}
	if out.String() != "" {
		t.Errorf("a question was shown that no one could answer: %q", out.String())
	}
}

// Whatever the answer function fails with, the question is refused: no answer is not consent.
func TestLinePrompterRefusesWithoutAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"the input ended", io.EOF},
		{"the turn was cancelled", context.Canceled},
		{"another error", io.ErrClosedPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out lockedBuffer
			ask := LinePrompter(func(context.Context, func()) (string, error) { return "y", tc.err }, &out) // even with "y" in hand
			if d := ask(context.Background(), toolRequest()); d.Allow || d.Reason != "no answer" {
				t.Fatalf("%+v", d)
			}
		})
	}
}

// The context of the question reaches the answer function: it is how a cancelled turn stops its
// question.
func TestLinePrompterPassesTheContextOn(t *testing.T) {
	type key struct{}
	var got any
	var out lockedBuffer
	ask := LinePrompter(func(ctx context.Context, _ func()) (string, error) { got = ctx.Value(key{}); return "n", nil }, &out)
	ask(context.WithValue(context.Background(), key{}, "the turn"), toolRequest())
	if got != "the turn" {
		t.Fatalf("the answer function got another context: %v", got)
	}
}

// One question at a time, whichever way the answers are read.
func TestLinePrompterAsksOneQuestionAtATime(t *testing.T) {
	var out lockedBuffer
	release := make(chan struct{})
	var inAnswer sync.WaitGroup
	inAnswer.Add(1)
	var once sync.Once
	ask := LinePrompter(func(ctx context.Context, show func()) (string, error) {
		show()
		once.Do(inAnswer.Done)
		<-release
		return "y", nil
	}, &out)
	first, second := make(chan perm.Decision, 1), make(chan perm.Decision, 1)
	go func() { first <- ask(context.Background(), toolRequest()) }()
	inAnswer.Wait() // the first is on the screen and waiting
	go func() { second <- ask(context.Background(), toolRequest()) }()
	// The second asker has to wait its turn; let the first finish, and the second follows.
	close(release)
	if d := decisionWithin(t, first, "the first question"); !d.Allow {
		t.Errorf("first: %+v", d)
	}
	if d := decisionWithin(t, second, "the second question"); !d.Allow {
		t.Errorf("second: %+v", d)
	}
	if n := strings.Count(out.String(), "allow?"); n != 2 {
		t.Errorf("%d questions were shown, want 2:\n%s", n, out.String())
	}
}
