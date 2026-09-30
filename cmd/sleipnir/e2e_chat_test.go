package main

// `sleipnir chat` on a terminal. The documentation says "Ctrl-C cancels the running turn (not
// the session)", and what Ctrl-C does is decided by the terminal and the signals the process
// registers for, so these tests run the command on a pseudo-terminal and press the key.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/ptytest"
)

// chatTerm is a chat session on a terminal, and the ways a person drives it.
type chatTerm struct {
	t    *testing.T
	w    *world
	term *ptytest.Session
}

// startChat starts `sleipnir chat args...` and waits for its first prompt.
func startChat(t *testing.T, w *world, args ...string) *chatTerm {
	t.Helper()
	c := &chatTerm{t: t, w: w, term: ptytest.Start(t, w.cmd(append([]string{"chat"}, args...)...))}
	c.prompt()
	return c
}

func (c *chatTerm) expect(s string) {
	c.t.Helper()
	if err := c.term.ExpectString(s, e2eGuard); err != nil {
		c.t.Fatal(err)
	}
}

// prompt waits for the prompt the chat shows when it is ready for a goal.
func (c *chatTerm) prompt() { c.t.Helper(); c.expect("› ") }

// untilPrompt waits for the next prompt and returns what was printed before it.
func (c *chatTerm) untilPrompt() string {
	c.t.Helper()
	m, err := c.term.ExpectRegexp(`(?s)^(.*?)\n› `, e2eGuard)
	if err != nil {
		c.t.Fatal(err)
	}
	return m[1]
}

// send types a line.
func (c *chatTerm) send(line string) {
	c.t.Helper()
	if _, err := c.term.WriteString(line + "\n"); err != nil {
		c.t.Fatal(err)
	}
}

func (c *chatTerm) ctrlC() {
	c.t.Helper()
	if err := c.term.SendCtrlC(); err != nil {
		c.t.Fatal(err)
	}
}

func (c *chatTerm) ctrlD() {
	c.t.Helper()
	if err := c.term.SendCtrlD(); err != nil {
		c.t.Fatal(err)
	}
}

// exited waits for the session to end and checks its exit status.
func (c *chatTerm) exited(code int) {
	c.t.Helper()
	st, err := c.term.Wait(e2eGuard)
	if err != nil {
		c.t.Fatal(err)
	}
	if st.ExitCode() != code {
		c.t.Errorf("the chat exited with %v, want status %d.\n%s", st, code, c.term.Transcript())
	}
	if err := c.term.ExpectEOF(e2eGuard); err != nil {
		c.t.Error(err)
	}
	noCrash(c.t, c.term.Transcript())
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Ctrl-C cancels the turn that is running and nothing else: the session goes on, the next
// goal is answered, and Ctrl-D ends it with the status and the reason of a person leaving.
func TestChatCtrlCCancelsTheTurnNotTheSession(t *testing.T) {
	m := startModel(t)
	slow := m.on("@slow", say("slow turn finished").held())
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	c := startChat(t, w)

	c.send("@slow")
	slow.wait(t) // the model has the request: the turn is running
	c.ctrlC()
	c.expect("(cancelled)")
	c.prompt() // a fresh prompt: the session is still there

	slow.release() // let the model's abandoned answer go; nothing is waiting for it
	c.send("@hello")
	c.expect("hi there")
	c.prompt()

	c.ctrlD()
	c.exited(0)
	if got, ok := w.sessionEnd(); !ok || got != "exit" {
		t.Errorf("the session ended with reason %q (recorded: %v), want \"exit\"", got, ok)
	}
	if got, want := m.seen(), []string{"@slow", "@hello"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the model got the goals %q, want %q", got, want)
	}
}

// At the prompt Ctrl-C does not quit: it says how to, and a goal works afterwards. A second
// Ctrl-C, right after one, quits. Anything in between (a goal) makes the next one a first.
func TestChatCtrlCAtThePromptAsksBeforeItQuits(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	c := startChat(t, w)

	c.ctrlC()
	c.expect("Ctrl-C again") // the hint
	c.prompt()               // and a fresh prompt: the session is still there

	c.send("@hello")
	c.expect("hi there")
	c.prompt()

	c.ctrlC() // a goal ran since the last one: this is a first press again
	c.expect("Ctrl-C again")
	c.prompt()

	// The second press quits. "Right after" is a clock (two seconds), so a machine that took
	// longer than that between the two makes the second press a first one again: press after
	// each hint until the session ends. Only a session that does not end fails.
	ended := false
	for i := 0; i < 5 && !ended; i++ {
		c.ctrlC()
		err := c.term.ExpectString("Ctrl-C again", e2eGuard)
		switch {
		case errors.Is(err, ptytest.ErrEOF):
			ended = true
		case err != nil:
			t.Fatal(err)
		}
	}
	if !ended {
		t.Fatalf("five presses of Ctrl-C at the prompt did not end the session:\n%s", c.term.Transcript())
	}
	c.exited(0)
	if got, ok := w.sessionEnd(); !ok || got != "interrupted" {
		t.Errorf("the session ended with reason %q (recorded: %v), want \"interrupted\"", got, ok)
	}
}

// What was typed at the prompt and not sent is thrown away by Ctrl-C (the terminal does
// that), and the next goal is only what is typed after it.
func TestChatCtrlCDropsAHalfTypedLine(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	c := startChat(t, w)

	if _, err := c.term.WriteString("@hel"); err != nil { // no Enter
		t.Fatal(err)
	}
	c.expect("@hel")
	c.ctrlC()
	c.expect("Ctrl-C again")
	c.prompt()
	c.send("@hello")
	c.expect("hi there")
	c.prompt()
	c.ctrlD()
	c.exited(0)
	if got, want := m.seen(), []string{"@hello"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the model got the goals %q, want %q (the half-typed line must be gone)", got, want)
	}
}

// Ctrl-D is the end of input for whatever reads next. During a turn nothing reads the input
// for an answer, so it waits, like a line typed ahead, and ends the chat when the turn is over.
func TestChatCtrlDDuringATurnQuitsAfterIt(t *testing.T) {
	m := startModel(t)
	slow := m.on("@slow", say("slow turn finished").held())
	w := newWorld(t, m.url())
	c := startChat(t, w)

	c.send("@slow")
	slow.wait(t)
	c.ctrlD()
	slow.release()
	c.expect("slow turn finished") // the turn was not cut short
	c.exited(0)
	if got, ok := w.sessionEnd(); !ok || got != "exit" {
		t.Errorf("the session ended with reason %q (recorded: %v), want \"exit\"", got, ok)
	}
}

// Ctrl-D at an approval question is no answer: the action is refused, the chat goes on.
func TestChatCtrlDAtAQuestionIsARefusal(t *testing.T) {
	m := startModel(t)
	m.on("@write", writes("approved.txt"))
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	c := startChat(t, w)

	c.send("@write")
	c.expect("allow? [y]es once")
	c.ctrlD()
	if out := c.untilPrompt(); !strings.Contains(out, "✗ write approved.txt") {
		t.Errorf("the write should have been refused:\n%s", out)
	}
	c.send("@hello") // still here
	c.expect("hi there")
	c.prompt()
	c.ctrlD()
	c.exited(0)
	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("the write was done")
	}
	if res := m.toolResults(); len(res) != 1 || !strings.Contains(res[0], "no answer") {
		t.Errorf("the model was told %q, want a refusal that says there was no answer", res)
	}
}

// SIGTERM still ends the process, at the prompt and in the middle of a turn, whatever Ctrl-C
// does: the turn is cancelled and the chat leaves, as a session that was interrupted.
func TestChatSIGTERMEndsTheProcess(t *testing.T) {
	t.Run("at the prompt", func(t *testing.T) {
		m := startModel(t)
		w := newWorld(t, m.url())
		c := startChat(t, w)
		if err := c.term.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		c.exited(0)
	})
	t.Run("in a turn", func(t *testing.T) {
		m := startModel(t)
		slow := m.on("@slow", say("slow turn finished").held())
		w := newWorld(t, m.url())
		c := startChat(t, w)
		c.send("@slow")
		slow.wait(t)
		if err := c.term.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		c.expect("(cancelled)")
		c.exited(0)
		if got, ok := w.sessionEnd(); !ok || got != "interrupted" {
			t.Errorf("the session ended with reason %q (recorded: %v), want \"interrupted\"", got, ok)
		}
	})
}

// A tool that needs approval in the default mode asks on the terminal, and only what the
// person types after the question is the answer.
func TestChatApproval(t *testing.T) {
	const ask = "allow? [y]es once / [a]lways this session / [n]o: "
	for _, tc := range []struct {
		name, answer string
		allowed      bool
	}{
		{"yes", "y", true},
		{"yes, spelled out", "yes", true},
		{"always", "a", true},
		{"no", "n", false},
		{"anything else refuses", "maybe", false},
		{"an empty line refuses", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := startModel(t)
			m.on("@write", writes("approved.txt"))
			w := newWorld(t, m.url())
			c := startChat(t, w)

			c.send("@write")
			c.expect("wants to: write approved.txt")
			c.expect(ask)
			c.send(tc.answer)
			out := c.untilPrompt()

			mark := "✗"
			if tc.allowed {
				mark = "✓"
			}
			if !strings.Contains(out, mark+" write approved.txt") {
				t.Errorf("the turn should show %q for the write:\n%s", mark+" write approved.txt", out)
			}
			if got := exists(filepath.Join(w.project, "approved.txt")); got != tc.allowed {
				t.Errorf("approved.txt exists: %v, want %v", got, tc.allowed)
			}
			c.ctrlD()
			c.exited(0)
			res := m.toolResults()
			if len(res) != 1 || strings.HasPrefix(res[0], "Created approved.txt") != tc.allowed {
				t.Errorf("the model was told %q, want the write to be reported as %v", res, map[bool]string{true: "done", false: "refused"}[tc.allowed])
			}
		})
	}
}

// "Always this session" is an exact rule: the same request is not asked again, another one is.
func TestChatApprovalAlwaysRemembersTheExactRequest(t *testing.T) {
	m := startModel(t)
	m.on("@touch-a", runs("touch a.marker"))
	m.on("@touch-a-again", runs("touch a.marker"))
	m.on("@touch-b", runs("touch b.marker"))
	w := newWorld(t, m.url())
	c := startChat(t, w)

	c.send("@touch-a")
	c.expect("allow? [y]es once")
	c.send("a")
	c.untilPrompt()

	c.send("@touch-a-again") // the same command: no question, the turn runs to its end
	if out := c.untilPrompt(); strings.Contains(out, "allow?") {
		t.Errorf("the same command was asked about again:\n%s", out)
	}

	c.send("@touch-b") // another command: a question
	c.expect("allow? [y]es once")
	c.send("n")
	c.untilPrompt()

	c.ctrlD()
	c.exited(0)
	for name, want := range map[string]bool{"a.marker": true, "b.marker": false} {
		if got := exists(filepath.Join(w.project, name)); got != want {
			t.Errorf("%s exists: %v, want %v", name, got, want)
		}
	}
}

// Ctrl-C while the question is on the screen cancels the turn and its question. The line
// typed next is a goal: it is not the answer to the question that was cancelled, and nothing
// that is still reading the terminal takes it away.
func TestChatCtrlCDuringAnApprovalThenAGoal(t *testing.T) {
	m := startModel(t)
	m.on("@write", writes("approved.txt"))
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	c := startChat(t, w)

	c.send("@write")
	c.expect("allow? [y]es once")
	c.ctrlC()
	c.expect("(cancelled)")
	c.prompt()

	c.send("@hello")
	c.expect("hi there") // the goal was not swallowed
	c.prompt()

	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("the write was done, though its question was cancelled and the next line was a goal")
	}
	c.ctrlD()
	c.exited(0)
	if got, want := m.seen(), []string{"@write", "@hello"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the model got the goals %q, want %q", got, want)
	}
	if res := m.toolResults(); len(res) != 0 {
		t.Errorf("a cancelled question produced tool results for the model: %q", res)
	}
}

// A line typed while a turn runs is not the answer to a question that comes later: the
// question waits for a line typed after it was shown, and the line typed before is kept for
// the next prompt, where it is a goal.
func TestChatALineTypedAheadIsNotTheAnswer(t *testing.T) {
	m := startModel(t)
	held := m.on("@hold-write", writes("approved.txt").held())
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	c := startChat(t, w)

	c.send("@hold-write")
	held.wait(t)     // the turn is running, and will ask when the model answers
	c.send("@hello") // typed ahead
	c.expect("@hello")
	// The terminal has the line. Wait for the chat to have read it too (a chat that reads the
	// terminal while a turn runs does, at once; one that does not never will, so a wait that
	// runs out is not a failure here: what follows is).
	if err := c.term.WaitInputRead(5 * time.Second); err != nil && !errors.Is(err, ptytest.ErrTimeout) {
		t.Fatal(err)
	}
	held.release() // the model asks for approval

	c.expect("allow? [y]es once")
	c.send("y") // typed after the question: the answer
	out := c.untilPrompt()
	if !strings.Contains(out, "✓ write approved.txt") {
		t.Fatalf("the write should have been approved by the line typed after the question, not refused on the line typed before it:\n%s", out)
	}
	c.expect("hi there") // and the line typed ahead ran as a goal, once the turn was over
	c.prompt()
	c.ctrlD()
	c.exited(0)

	if !exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("approved.txt does not exist")
	}
	if got, want := m.seen(), []string{"@hold-write", "@hello"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the model got the goals %q, want %q", got, want)
	}
}

// Without a terminal nothing can be asked, and the chat reads its goals line by line until
// the input ends.
func TestChatFromAPipe(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		goals       []string
		replies     []string
	}{
		{"one goal", "@hello\n", []string{"@hello"}, []string{"hi there"}},
		{"two goals", "@hello\n@hello\n", []string{"@hello", "@hello"}, []string{"hi there", "hi there"}},
		{"no newline at the end", "@hello", []string{"@hello"}, []string{"hi there"}},
		{"CRLF line ends", "@hello\r\n", []string{"@hello"}, []string{"hi there"}},
		{"blank lines are skipped", "\n\n@hello\n\n", []string{"@hello"}, []string{"hi there"}},
		{"a backslash continues the line", "@two\\\nlines\n", []string{"@two\nlines"}, []string{"one goal of two lines"}},
		{"a slash command does not reach the model", "/help\n@hello\n", []string{"@hello"}, []string{"hi there"}},
		{"empty input", "", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := startModel(t)
			m.on("@hello", say("hi there"))
			m.on("@two\nlines", say("one goal of two lines"))
			w := newWorld(t, m.url())
			r := w.run(tc.input, "chat")
			if r.code != 0 {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
			}
			if got := m.seen(); !reflect.DeepEqual(got, tc.goals) {
				t.Errorf("the model got the goals %q, want %q", got, tc.goals)
			}
			for _, want := range tc.replies {
				if !strings.Contains(r.stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
				}
			}
			if !strings.Contains(r.stderr, "› ") {
				t.Errorf("stderr has no prompt:\n%s", r.stderr)
			}
		})
	}
}

// With no one to ask, an action that needs approval is refused, and the model is told so.
func TestChatFromAPipeRefusesWhatNeedsApproval(t *testing.T) {
	m := startModel(t)
	m.on("@write", writes("approved.txt"))
	w := newWorld(t, m.url())
	r := w.run("@write\n", "chat")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if strings.Contains(r.stderr, "allow?") {
		t.Errorf("a question was asked with no one to answer it:\n%s", r.stderr)
	}
	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("the write was done")
	}
	if res := m.toolResults(); len(res) != 1 || !strings.Contains(res[0], "approval required") {
		t.Errorf("the model was told %q, want an \"approval required\" refusal", res)
	}
}

// `run` reads its answers from the terminal directly.
func TestRunAsksOnTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		answer  string
		allowed bool
	}{{"y", true}, {"n", false}} {
		t.Run(tc.answer, func(t *testing.T) {
			m := startModel(t)
			m.on("@write", writes("approved.txt"))
			w := newWorld(t, m.url())
			term := ptytest.Start(t, w.cmd("run", "@write"))
			c := &chatTerm{t: t, w: w, term: term}
			c.expect("allow? [y]es once / [a]lways this session / [n]o: ")
			c.send(tc.answer)
			c.expect("2 steps") // the footer: the tool call, then the answer
			c.exited(0)
			if got := exists(filepath.Join(w.project, "approved.txt")); got != tc.allowed {
				t.Errorf("approved.txt exists: %v, want %v", got, tc.allowed)
			}
		})
	}
}

// `run` is not a chat: Ctrl-C ends the run, with the status of a run that failed.
func TestRunCtrlCEndsTheRun(t *testing.T) {
	m := startModel(t)
	slow := m.on("@slow", say("slow turn finished").held())
	w := newWorld(t, m.url())
	term := ptytest.Start(t, w.cmd("run", "@slow"))
	c := &chatTerm{t: t, w: w, term: term}
	slow.wait(t)
	c.ctrlC()
	c.expect("sleipnir: ") // the error that ended it, however it is worded
	c.exited(1)
	if got, ok := w.sessionEnd(); !ok || got != "interrupted" {
		t.Errorf("the session ended with reason %q (recorded: %v), want \"interrupted\"", got, ok)
	}
}
