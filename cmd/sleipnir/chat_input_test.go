package main

// What a chat session does with its one input: the prompt reads the goals from it and, when a
// tool needs approval, the approval reads the answer from it. These tests are the rules of that
// sharing, without a terminal:
//
//   - typed ahead: a line typed while the turn runs is kept for the prompt and is never the
//     answer to a question that comes later;
//   - cancelled reads: a read that is cancelled (Ctrl-C ended the turn that was asking) takes
//     nothing, so the next line goes to whoever reads next;
//   - approval and goal: a question that is asked while the prompt is waiting takes the first
//     line typed after it, and the prompt the one after that.
//
// chatInput is the input as cmdChat builds it (newChatInput is the one place that knows how),
// fed by a pipe the test types into.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// probeReader is the input as the program sees it, with a record of what reads it: whether
// a Read is in progress, and how many have returned data.
type probeReader struct {
	r io.Reader

	mu        sync.Mutex
	inRead    bool
	delivered int
}

func (p *probeReader) Read(b []byte) (int, error) {
	p.mu.Lock()
	p.inRead = true
	p.mu.Unlock()
	n, err := p.r.Read(b)
	p.mu.Lock()
	p.inRead = false
	if n > 0 {
		p.delivered++
	}
	p.mu.Unlock()
	return n, err
}

func (p *probeReader) state() (inRead bool, delivered int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inRead, p.delivered
}

// lockedBuf is what the approval prompt prints to, read by a test while the prompt runs.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// inputGuard is how long a test waits for something that has to happen. A wait that can only be
// satisfied by a design (settle) is shorter: its running out is an answer, not a hang.
const (
	inputGuard = 10 * time.Second
	settle     = 2 * time.Second
)

// chatInput is one chat session's input, and the person typing into it.
type chatInput struct {
	t     *testing.T
	pw    *io.PipeWriter
	probe *probeReader
	out   lockedBuf

	goal   func(ctx context.Context) (string, error) // the prompt reads one logical line
	ask    perm.Prompter                             // the approval asks
	asking func() bool                               // an approval is waiting for its answer, as far as the input can tell

	typed int // lines typed so far
}

func (c *chatInput) waitUntil(what string, cond func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(inputGuard)
	for !cond() {
		if time.Now().After(deadline) {
			c.t.Fatalf("%s did not happen within %v", what, inputGuard)
		}
		time.Sleep(time.Millisecond)
	}
}

// typeLine types a line. It then waits, for a little, until whatever owns the input has read the
// line and come back for more. An input that is read only when someone asks never comes back,
// and the wait runs out: the test goes on, and what it asserts afterwards is what matters.
func (c *chatInput) typeLine(line string) {
	c.t.Helper()
	c.typed++
	wrote := make(chan struct{})
	go func() {
		c.pw.Write([]byte(line + "\n"))
		close(wrote)
	}()
	deadline := time.After(settle)
	select {
	case <-wrote:
	case <-deadline:
		return
	}
	// The line was taken by a Read that has returned; the owner is back when another is waiting.
	for {
		if in, n := c.probe.state(); in && n >= c.typed {
			return
		}
		select {
		case <-deadline:
			return
		case <-time.After(time.Millisecond):
		}
	}
}

// read is a read that has started, and will deliver its line or error on done.
type goalRead struct {
	line string
	err  error
	done chan struct{}
}

// startGoal has the prompt read a line, in the background.
func (c *chatInput) startGoal(ctx context.Context) *goalRead {
	r := &goalRead{done: make(chan struct{})}
	go func() {
		r.line, r.err = c.goal(ctx)
		close(r.done)
	}()
	return r
}

func (r *goalRead) wait(t *testing.T, what string) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(inputGuard):
		t.Fatalf("%s: no result within %v", what, inputGuard)
	}
}

// question is an approval that has been asked, and will deliver its decision.
type question struct {
	d    perm.Decision
	done chan struct{}
}

// startQuestion has the approval ask, in the background; it returns when the question has been
// shown and the input knows it is waiting for the answer.
func (c *chatInput) startQuestion(ctx context.Context) *question {
	c.t.Helper()
	shown := strings.Count(c.out.String(), "allow? [y]es once")
	q := &question{done: make(chan struct{})}
	go func() {
		q.d = c.ask(ctx, perm.Request{Agent: "main", Tool: "write", Summary: "write notes.txt"})
		close(q.done)
	}()
	c.waitUntil("the question being shown", func() bool { return strings.Count(c.out.String(), "allow? [y]es once") > shown })
	c.waitUntil("the input waiting for the answer", func() bool {
		select {
		case <-q.done: // decided already: by a line that was not typed after the question
			return true
		default:
			return c.asking()
		}
	})
	return q
}

func (q *question) wait(t *testing.T, what string) perm.Decision {
	t.Helper()
	select {
	case <-q.done:
		return q.d
	case <-time.After(inputGuard):
		t.Fatalf("%s: no decision within %v", what, inputGuard)
		return perm.Decision{}
	}
}

// waitReading waits until something is blocked reading the input.
func (c *chatInput) waitReading() {
	c.t.Helper()
	c.waitUntil("a read of the input", func() bool { in, _ := c.probe.state(); return in })
}

// ---- how cmdChat builds its input: the one part of this file that follows the code ----

func newChatInput(t *testing.T) *chatInput {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	c := &chatInput{t: t, pw: pw, probe: &probeReader{r: pr}}
	lines := newStdinLines(c.probe, true)
	c.ask = session.LinePrompter(lines.Answer, &c.out)
	c.goal = func(ctx context.Context) (string, error) { return readInput(ctx, lines, nil) }
	c.asking = lines.asking
	return c
}

// ---- the rules ----

// Lines come out as they were typed: one per call, CR LF and a last line with no newline
// included, a backslash continuing the line, and the end of the input is an error once the
// lines are done.
func TestChatInputLines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typed string
		want  []string
	}{
		{"lines", "one\ntwo\n", []string{"one", "two"}},
		{"CRLF", "one\r\ntwo\r\n", []string{"one", "two"}},
		{"no newline at the end", "one\ntwo", []string{"one", "two"}},
		{"empty lines are lines", "\n\none\n", []string{"", "", "one"}},
		{"a backslash continues the line", "one \\\ntwo\nthree\n", []string{"one \ntwo", "three"}},
		{"unicode and long lines", "héllo › wörld\n" + strings.Repeat("x", 70000) + "\n", []string{"héllo › wörld", strings.Repeat("x", 70000)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newChatInput(t)
			go func() {
				in.pw.Write([]byte(tc.typed))
				in.pw.Close()
			}()
			for i, want := range tc.want {
				r := in.startGoal(context.Background())
				r.wait(t, "line "+want[:min(len(want), 10)])
				if r.err != nil || r.line != want {
					t.Fatalf("line %d: %q, %v; want %q", i, r.line, r.err, want)
				}
			}
			for range 2 { // and it stays ended
				r := in.startGoal(context.Background())
				r.wait(t, "the end of the input")
				if !errors.Is(r.err, io.EOF) {
					t.Fatalf("after the last line: %q, %v; want the end of the input", r.line, r.err)
				}
			}
		})
	}
}

// A line typed while a turn runs is not the answer to a question that comes after it: the
// question takes a line typed after it was shown, and the one typed ahead stays for the prompt.
func TestALineTypedAheadIsNotAnApproval(t *testing.T) {
	in := newChatInput(t)
	in.typeLine("typed before the question") // nobody is asking yet

	q := in.startQuestion(context.Background())
	in.typeLine("y") // typed after it was shown
	if d := q.wait(t, "the answer"); !d.Allow || d.Reason != "allowed by user" {
		t.Fatalf("the question was decided %+v: it took the line that was typed before it, not the one after", d)
	}

	r := in.startGoal(context.Background())
	r.wait(t, "the line typed ahead")
	if r.err != nil || r.line != "typed before the question" {
		t.Fatalf("the prompt got %q, %v; the line typed ahead should be waiting for it", r.line, r.err)
	}
}

// Several lines typed ahead stay in order for the prompt, and the question takes none of them.
func TestLinesTypedAheadStayInOrder(t *testing.T) {
	in := newChatInput(t)
	for _, l := range []string{"first", "second", "third"} {
		in.typeLine(l)
	}
	q := in.startQuestion(context.Background())
	in.typeLine("n")
	if d := q.wait(t, "the answer"); d.Allow || d.Reason != "denied by user" {
		t.Fatalf("the question was decided %+v", d)
	}
	for _, want := range []string{"first", "second", "third"} {
		r := in.startGoal(context.Background())
		r.wait(t, want)
		if r.line != want {
			t.Fatalf("the prompt got %q, want %q", r.line, want)
		}
	}
}

// A read that is cancelled has taken nothing: the next line goes to the next read, whichever
// kind either is. (The turn's Ctrl-C cancels a question; a swarm's workers can ask while the
// prompt waits.)
func TestCancelledReadsTakeNothing(t *testing.T) {
	for _, first := range []string{"goal", "question"} {
		for _, second := range []string{"goal", "question"} {
			t.Run(first+" cancelled, then a "+second, func(t *testing.T) {
				in := newChatInput(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var wait func() error
				switch first {
				case "goal":
					r := in.startGoal(ctx)
					in.waitReading()
					cancel()
					wait = func() error { r.wait(t, "the cancelled prompt"); return r.err }
				case "question":
					q := in.startQuestion(ctx)
					cancel()
					wait = func() error {
						if d := q.wait(t, "the cancelled question"); d.Allow {
							t.Errorf("a cancelled question was allowed: %+v", d)
						}
						return ctx.Err()
					}
				}
				if err := wait(); !errors.Is(err, context.Canceled) {
					t.Fatalf("the cancelled read ended with %v", err)
				}

				switch second {
				case "goal":
					r := in.startGoal(context.Background())
					in.waitReading()
					in.typeLine("the next goal")
					r.wait(t, "the line typed after the cancellation: it was taken by the read that was cancelled")
					if r.err != nil || r.line != "the next goal" {
						t.Fatalf("the prompt got %q, %v", r.line, r.err)
					}
				case "question":
					q := in.startQuestion(context.Background())
					in.typeLine("y")
					if d := q.wait(t, "the line typed after the cancellation: it was taken by the read that was cancelled"); !d.Allow {
						t.Fatalf("the question was decided %+v", d)
					}
				}
			})
		}
	}
}

// A question that is asked while the prompt is waiting takes the first line typed after it;
// the prompt gets the next one.
func TestAnApprovalAndThePromptShareTheInput(t *testing.T) {
	in := newChatInput(t)
	goal := in.startGoal(context.Background())
	in.waitReading()
	q := in.startQuestion(context.Background())

	in.typeLine("y")
	if d := q.wait(t, "the answer"); !d.Allow {
		t.Fatalf("the question was decided %+v: the prompt took its answer", d)
	}
	in.typeLine("the next goal")
	goal.wait(t, "the goal typed after the answer")
	if goal.err != nil || goal.line != "the next goal" {
		t.Fatalf("the prompt got %q, %v; want the line typed after the answer", goal.line, goal.err)
	}
}
