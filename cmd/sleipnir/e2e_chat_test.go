package main

// `sleipnir chat` on a terminal: the program of docs/UX.md. What it draws, which keys it takes and what Ctrl-C does are decided by
// the terminal (raw mode, the size of the window, the signals), so these tests run the real command on a pseudo-terminal and look at
// the screen through a terminal emulator (internal/tui/vt) that is given what the program writes. A person sees the screen, so the
// tests wait for what the screen shows and press keys the way a keyboard sends them.
//
// The line chat (--plain, a pipe, TERM=dumb) has its own tests in e2e_chat_plain_test.go.
//
// Two things to know about the terminal these tests have. Its environment has no locale, so the program draws with ASCII (ok, x, *,
// +--+) unless a test gives it one; and a question takes answers only when the keyboard has been quiet for a while after it appeared
// (what stops a line that is being typed from answering it), which the screen says by naming the keys, so a test waits for that.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/ptytest"
	"github.com/anemos-labs/sleipnir/internal/tui/vt"
)

const (
	uiRows = 30
	uiCols = 100
)

// ui is a chat program on a terminal, and the ways a person drives it.
type ui struct {
	t          *testing.T
	w          *world
	s          *ptytest.Session
	cols, rows int
	scr        *vt.Term
	fed        int // how much of the transcript the emulator has been given
}

// launch starts `sleipnir chat args...` on a terminal of rows by cols.
func launch(t *testing.T, w *world, rows, cols int, args ...string) *ui {
	t.Helper()
	u := &ui{t: t, w: w, cols: cols, rows: rows, scr: vt.New(cols, rows)}
	argv := append([]string{"chat"}, args...)
	if !slices.Contains(args, "--swarm") { // on a terminal the default is a team; these tests are about one agent
		argv = append(argv, "--swarm", "0")
	}
	u.s = ptytest.Start(t, w.cmd(argv...), ptytest.Size(uint16(rows), uint16(cols)))
	return u
}

// startUI starts the chat on a terminal of 100 columns and 30 rows and waits until it is ready for a goal.
func startUI(t *testing.T, w *world, args ...string) *ui {
	t.Helper()
	u := launch(t, w, uiRows, uiCols, args...)
	u.ready()
	return u
}

// sync gives the emulator what the program has written since it last looked.
func (u *ui) sync() {
	raw := u.s.Transcript()
	if len(raw) > u.fed {
		_, _ = u.scr.WriteString(raw[u.fed:])
		u.fed = len(raw)
	}
}

// visible is the screen, and all is everything the terminal has shown, the lines that scrolled off it included.
func (u *ui) visible() string { u.sync(); return strings.Join(u.scr.Rows(), "\n") }
func (u *ui) all() string     { u.sync(); return strings.Join(u.scr.All(), "\n") }

// wait waits until ok is true of the screen and returns what it was given; a screen that never satisfies it fails the test with
// what it showed. The wait is a hang guard (e2eGuard), not a time anything must be done in.
func (u *ui) wait(what string, ok func(visible, all string) bool) (visible, all string) {
	u.t.Helper()
	deadline := time.Now().Add(e2eGuard)
	for {
		visible, all = u.visible(), u.all()
		if ok(visible, all) {
			return visible, all
		}
		if time.Now().After(deadline) {
			u.t.Fatalf("the screen never showed %s; it shows:\n%s\n--- all of it, scrollback first:\n%s\n--- transcript (%d bytes)", what, visible, all, len(u.s.Transcript()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expect waits until everything shown so far has all of words.
func (u *ui) expect(words ...string) string {
	u.t.Helper()
	_, all := u.wait(strings.Join(words, " and "), func(_, all string) bool { return containsAll(all, words) })
	return all
}

// expectVisible waits until the screen itself (not the scrollback) has all of words.
func (u *ui) expectVisible(words ...string) string {
	u.t.Helper()
	vis, _ := u.wait(strings.Join(words, " and "), func(vis, _ string) bool { return containsAll(vis, words) })
	return vis
}

func containsAll(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// ready waits until the program waits for a goal: the prompt is empty and nothing is running.
func (u *ui) ready() string {
	u.t.Helper()
	vis, _ := u.wait("a prompt that waits for a goal", func(vis, _ string) bool {
		return strings.Contains(vis, "Message Sleipnir") && !strings.Contains(vis, "esc to interrupt")
	})
	return vis
}

// busy waits until something runs.
func (u *ui) busy() string {
	u.t.Helper()
	return u.expectVisible("esc to interrupt")
}

// question waits until the question with this title is on the screen and takes answers, which the screen says by naming the keys.
func (u *ui) question(title string) string {
	u.t.Helper()
	return u.expectVisible(title, "esc says no")
}

// summaryLine is the record a turn leaves, which starts a line (the rule is -- or ── by the look).
var summaryLine = regexp.MustCompile(`(?m)^(--|──) `)

// turns is how many turns have been summed up in the scrollback.
func (u *ui) turns() int { return len(summaryLine.FindAllString(u.all(), -1)) }

// turnsDone waits until n turns have ended.
func (u *ui) turnsDone(n int) string {
	u.t.Helper()
	_, all := u.wait(fmt.Sprintf("%d turns done", n), func(_, all string) bool { return len(summaryLine.FindAllString(all, -1)) >= n })
	return all
}

// statusGlyphs are the pictures the program has drawn in front of a status line ("* Thinking...") so far, as written: a spinner that
// turns is several, one that stands still is one.
var statusWord = regexp.MustCompile(`([^\s\x1b\w]) [A-Z][a-z]+(?:\.\.\.|…)`)

func (u *ui) statusGlyphs() map[string]bool {
	seen := map[string]bool{}
	for _, m := range statusWord.FindAllStringSubmatch(u.s.Transcript(), -1) {
		seen[m[1]] = true
	}
	return seen
}

func (u *ui) typeText(s string) {
	u.t.Helper()
	if _, err := u.s.WriteString(s); err != nil {
		u.t.Fatal(err)
	}
}

// Keys as a terminal sends them.
const (
	keyEnter = "\r"
	keyEsc   = "\x1b"
	keyUp    = "\x1b[A"
	keyDown  = "\x1b[B"
	keyTab   = "\t"
	keyBTab  = "\x1b[Z"
	keyCtrlU = "\x15"
)

func (u *ui) enter() { u.t.Helper(); u.typeText(keyEnter) }

// send types a goal and presses enter.
func (u *ui) send(line string) { u.t.Helper(); u.typeText(line); u.enter() }

// paste types text as a terminal that has bracketed paste sends a paste.
func (u *ui) paste(text string) { u.t.Helper(); u.typeText("\x1b[200~" + text + "\x1b[201~") }

func (u *ui) ctrlC() {
	u.t.Helper()
	if err := u.s.SendCtrlC(); err != nil {
		u.t.Fatal(err)
	}
}

func (u *ui) ctrlD() {
	u.t.Helper()
	if err := u.s.SendCtrlD(); err != nil {
		u.t.Fatal(err)
	}
}

func (u *ui) signal(sig syscall.Signal) {
	u.t.Helper()
	if err := u.s.Signal(sig); err != nil {
		u.t.Fatal(err)
	}
}

// resize changes the size of the window: the terminal is the new size first, then the program is told (SIGWINCH), as it happens.
func (u *ui) resize(rows, cols int) {
	u.t.Helper()
	u.sync()
	u.scr.Resize(cols, rows)
	u.cols, u.rows = cols, rows
	if err := u.s.Resize(uint16(rows), uint16(cols)); err != nil {
		u.t.Fatal(err)
	}
}

// ended reports whether the program has exited.
func (u *ui) ended() bool {
	_, err := u.s.Wait(time.Millisecond)
	return err == nil
}

// quitWithCtrlC presses Ctrl-C at the prompt until the program ends. One press only says how to quit; a second, soon after and with
// nothing typed in between, quits. "Soon" is a clock (two seconds), so a machine that took longer than that between two presses
// makes the second a first one again, and the test presses again after each reminder. Only a program that never ends fails.
func (u *ui) quitWithCtrlC() {
	u.t.Helper()
	for range 5 {
		before := strings.Count(u.all(), "Ctrl-C again")
		u.ctrlC()
		deadline := time.Now().Add(e2eGuard)
		for strings.Count(u.all(), "Ctrl-C again") == before {
			if u.ended() {
				return
			}
			if time.Now().After(deadline) {
				u.t.Fatalf("a press of Ctrl-C at the prompt did nothing:\n%s", u.visible())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	u.t.Fatalf("five presses of Ctrl-C at the prompt did not end the chat:\n%s", u.visible())
}

// exited waits for the program to end, checks its exit status, and that it left the terminal as it found it.
func (u *ui) exited(code int) {
	u.t.Helper()
	st, err := u.s.Wait(e2eGuard)
	if err != nil {
		u.t.Fatal(err)
	}
	if err := u.s.ExpectEOF(e2eGuard); err != nil {
		u.t.Error(err)
	}
	if st.ExitCode() != code {
		u.t.Errorf("the chat exited with %v, want status %d.\n%s", st, code, u.all())
	}
	noCrash(u.t, u.s.Transcript())
	u.sync()
	if u.scr.AltScreen() {
		u.t.Error("the alternate screen was left on")
	}
	x, _, shown := u.scr.Cursor()
	if !shown {
		u.t.Error("the cursor was left hidden")
	}
	if x != 0 {
		u.t.Errorf("the cursor was left at column %d: the shell's prompt would start in the middle of a line", x)
	}
	if u.scr.BracketedPaste() {
		u.t.Error("bracketed paste was left on: the shell would get every paste wrapped in markers")
	}
	if len(u.scr.Rejected) > 0 {
		u.t.Errorf("the program wrote what it must never write: %q", u.scr.Rejected)
	}
	if u.scr.SyncDepth() != 0 {
		u.t.Error("a frame was left open")
	}
}

func (u *ui) endReason(want string) {
	u.t.Helper()
	if got, ok := u.w.sessionEnd(); !ok || got != want {
		u.t.Errorf("the session ended with reason %q (recorded: %v), want %q", got, ok, want)
	}
}

func (u *ui) goals(m *scriptedModel, want ...string) {
	u.t.Helper()
	if got := m.seen(); !reflect.DeepEqual(got, want) {
		u.t.Errorf("the model got the goals %q, want %q", got, want)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// slowServer is a project whose tool server asks, while the session is made, whether it may start: the session waits for the answer,
// which is how a test holds the start.
func slowServer(t *testing.T, w *world) {
	t.Helper()
	writeFile(t, filepath.Join(w.project, ".mcp.json"), `{"mcpServers":{"slow":{"command":"sleep","args":["60"]}}}`)
}

// ---- what it is ----

// On a terminal the chat is a program: it draws on the screen it was started on (not the alternate one, so that what it prints stays
// in the scrollback), shows the cursor in the prompt, takes keys in raw mode, and puts the terminal back.
func TestE2EChatIsAProgramOnATerminal(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	vis := u.visible()
	for _, want := range []string{"sleipnir", "mock-1", "Message Sleipnir, / for commands, @ for files", "+---", "| > Message Sleipnir", "default"} {
		if !strings.Contains(vis, want) {
			t.Errorf("the first screen lacks %q:\n%s", want, vis)
		}
	}
	if u.scr.AltScreen() {
		t.Error("the chat must not use the alternate screen: what it prints is the terminal's scrollback")
	}
	if !u.scr.BracketedPaste() {
		t.Error("pastes are told apart from typing")
	}
	x, y, shown := u.scr.Cursor()
	rows := u.scr.Rows()
	if !shown || !strings.Contains(rows[y], "| > ") || x != 4 {
		t.Errorf("the cursor is at (%d,%d) %v on %q: it rests in the prompt", x, y, shown, rows[y])
	}

	u.send("@hello")
	all := u.expect("hi there")
	if !strings.Contains(all, "> @hello") {
		t.Errorf("what was sent is in the scrollback:\n%s", all)
	}
	all = u.turnsDone(1)
	if !strings.Contains(all, "1 step") || strings.Contains(all, "cache hit") || strings.Contains(all, "saved") {
		t.Errorf("the turn leaves a record, without statistics (they are on the stats page):\n%s", all)
	}
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.endReason("exit")
	u.goals(m, "@hello")
}

// With a locale that has UTF-8 the chat draws with its own glyphs.
func TestE2EChatDrawsWithGlyphsWhereTheLocaleHasThem(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	w.extra = []string{"LANG=C.UTF-8"}
	u := startUI(t, w)
	vis := u.visible()
	for _, want := range []string{"◆ sleipnir", "╭", "╰", "❯ Message Sleipnir"} {
		if !strings.Contains(vis, want) {
			t.Errorf("the first screen lacks %q:\n%s", want, vis)
		}
	}
	u.send("@hello")
	all := u.turnsDone(1)
	for _, want := range []string{"❯ @hello", "● hi there", "── "} {
		if !strings.Contains(all, want) {
			t.Errorf("the turn lacks %q:\n%s", want, all)
		}
	}
	u.ctrlD()
	u.exited(0)
}

// NO_COLOR is for colour, not for the program: it is drawn, and no colour is written. Nor is anything animated.
func TestE2EChatWithNoColorIsDrawnWithoutColour(t *testing.T) {
	m := startModel(t)
	slow := m.on("@slow", say("slow turn finished").held())
	w := newWorld(t, m.url())
	w.extra = []string{"NO_COLOR=1", "LANG=C.UTF-8"}
	u := startUI(t, w)
	u.send("@slow")
	slow.wait(t)
	u.busy()
	slow.release()
	u.turnsDone(1)
	raw := u.s.Transcript()
	if strings.Contains(raw, "\x1b[38") || strings.Contains(raw, "\x1b[48") {
		t.Error("a colour was written")
	}
	for _, sgr := range regexp.MustCompile("\x1b\\[([0-9;]*)m").FindAllStringSubmatch(raw, -1) {
		for _, p := range strings.Split(sgr[1], ";") {
			switch p {
			case "", "0", "1", "2", "3", "4", "7", "22", "23", "24", "27":
			default:
				t.Fatalf("SGR %q is not an attribute but a colour or something else", sgr[0])
			}
		}
	}
	if g := u.statusGlyphs(); len(g) != 1 || !g["●"] {
		t.Errorf("with NO_COLOR nothing moves, and the status line has the one dot: %v", g)
	}
	u.ctrlD()
	u.exited(0)
}

// --no-anim, SLEIPNIR_ANIM=0 and REDUCE_MOTION=1 each stand the spinner still; without them it turns.
func TestE2EChatSpinnerTurnsOnlyWhereMotionIsAllowed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		env   []string
		turns bool
	}{
		{"by default", nil, nil, true},
		{"--no-anim", []string{"--no-anim"}, nil, false},
		{"SLEIPNIR_ANIM=0", nil, []string{"SLEIPNIR_ANIM=0"}, false},
		{"REDUCE_MOTION=1", nil, []string{"REDUCE_MOTION=1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := startModel(t)
			slow := m.on("@slow", say("slow turn finished").held())
			w := newWorld(t, m.url())
			w.extra = tc.env
			u := startUI(t, w, tc.args...)
			u.send("@slow")
			slow.wait(t)
			if tc.turns {
				// the spinner of the ASCII look is | / - \ and the program draws a frame every so often: wait for two of them
				u.wait("a spinner that turns", func(string, string) bool { return len(u.statusGlyphs()) >= 2 })
				for g := range u.statusGlyphs() {
					if !strings.Contains("|/-\\", g) {
						t.Errorf("the spinner shows %q: %v", g, u.statusGlyphs())
					}
				}
			} else {
				u.busy()
				// the status line of the turn, and of the start before it, has had one picture in front of it, all the time
				if g := u.statusGlyphs(); len(g) != 1 || !g["*"] {
					t.Errorf("the status line has %v in front of it, want the dot that stands still", g)
				}
			}
			slow.release()
			u.turnsDone(1)
			u.ctrlD()
			u.exited(0)
		})
	}
}

// ---- Ctrl-C ----

// Ctrl-C cancels the turn that is running and nothing else: the session goes on, the next goal is answered, and Ctrl-D ends it with
// the status and the reason of a person leaving.
func TestE2EChatCtrlCCancelsTheTurnNotTheSession(t *testing.T) {
	m := startModel(t)
	slow := m.on("@slow", say("slow turn finished").held())
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@slow")
	slow.wait(t) // the model has the request: the turn is running
	u.busy()
	u.ctrlC()
	u.expect("(cancelled)")
	u.ready() // a fresh prompt: the session is still there

	slow.release() // let the model's abandoned answer go; nothing is waiting for it
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	if strings.Contains(u.all(), "slow turn finished") {
		t.Error("the answer of the cancelled turn was shown")
	}

	u.ctrlD()
	u.exited(0)
	u.endReason("exit")
	u.goals(m, "@slow", "@hello")
}

// A chat with a manager (--swarm) is the same: Ctrl-C cancels the manager's turn, not the session.
func TestE2EChatSwarmCtrlCCancelsTheTurnNotTheSession(t *testing.T) {
	m := startModel(t)
	slow := m.on("@slow", say("slow turn finished").held())
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w, "--swarm", "1")

	u.send("@slow")
	slow.wait(t)
	u.busy()
	u.ctrlC()
	u.expect("(cancelled)")
	u.ready()
	slow.release()
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	u.ctrlD()
	u.exited(0)
}

// A session that ended by two Ctrl-C at the prompt is a session that ended cleanly: it can be continued, with the turns it had.
func TestE2EChatResumesASessionThatCtrlCEnded(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	m.on("@again", say("and again"))
	w := newWorld(t, m.url())

	u := startUI(t, w)
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	u.quitWithCtrlC()
	u.exited(0)

	u = startUI(t, w, "--continue")
	u.expect("resumed: 2 turns restored")
	u.send("@again")
	u.expect("and again")
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.goals(m, "@hello", "@again")
}

// At the prompt Ctrl-C does not quit: it says how to, and a goal works afterwards. A second Ctrl-C, right after one, quits. Anything
// in between (a goal) makes the next one a first.
func TestE2EChatCtrlCAtThePromptAsksBeforeItQuits(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.ctrlC()
	u.expect("Ctrl-C again") // the hint
	u.ready()                // and a fresh prompt: the chat is still there

	u.send("@hello")
	u.expect("hi there")
	u.ready()

	u.ctrlC() // a goal ran since the last one: this is a first press again
	u.wait("a second hint", func(_, all string) bool { return strings.Count(all, "Ctrl-C again") == 2 })
	u.ready()

	u.quitWithCtrlC()
	u.exited(0)
	u.endReason("interrupted")
}

// What was typed at the prompt and not sent is thrown away by Ctrl-C, and the next goal is only what is typed after it.
func TestE2EChatCtrlCDropsAHalfTypedLine(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.typeText("@hel") // no Enter
	u.expectVisible("| > @hel")
	u.ctrlC()
	u.expect("Ctrl-C again")
	u.ready() // the prompt is empty again
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.goals(m, "@hello") // the half-typed line must be gone
}

// Ctrl-D is the end of input. During a turn it waits, like a line typed ahead, and ends the chat when the turn is over.
func TestE2EChatCtrlDDuringATurnQuitsAfterIt(t *testing.T) {
	m := startModel(t)
	slow := m.on("@slow", say("slow turn finished").held())
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@slow")
	slow.wait(t)
	u.ctrlD()
	u.expectVisible("quit when this is done") // the chat has it, and says so
	slow.release()
	u.expect("slow turn finished") // the turn was not cut short
	u.exited(0)
	u.endReason("exit")
}

// Ctrl-D at an approval question is no answer: the action is refused, the chat goes on.
func TestE2EChatCtrlDAtAQuestionIsARefusal(t *testing.T) {
	m := startModel(t)
	m.on("@write", writes("approved.txt"))
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@write")
	u.question("Write a file")
	u.ctrlD()
	u.turnsDone(1)
	u.ready()
	if all := u.all(); !regexp.MustCompile(`\* Write approved\.txt\s+x `).MatchString(all) {
		t.Errorf("the write should have been refused, and the line says so:\n%s", all)
	}
	u.send("@hello") // still here
	u.expect("hi there")
	u.ready()
	u.ctrlD()
	u.exited(0)
	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("the write was done")
	}
	if res := m.toolResults(); len(res) != 1 || !strings.Contains(res[0], "no answer") {
		t.Errorf("the model was told %q, want a refusal that says there was no answer", res)
	}
}

// SIGTERM still ends the process, at the prompt, in the middle of a turn and at a question, whatever Ctrl-C does: the turn is
// cancelled and the chat leaves as a session that was interrupted, with the terminal as it found it.
func TestE2EChatSIGTERMEndsTheProcess(t *testing.T) {
	t.Run("at the prompt", func(t *testing.T) {
		m := startModel(t)
		w := newWorld(t, m.url())
		u := startUI(t, w)
		u.signal(syscall.SIGTERM)
		u.exited(0)
	})
	t.Run("in a turn", func(t *testing.T) {
		m := startModel(t)
		slow := m.on("@slow", say("slow turn finished").held())
		w := newWorld(t, m.url())
		u := startUI(t, w)
		u.send("@slow")
		slow.wait(t)
		u.busy()
		u.signal(syscall.SIGTERM)
		u.expect("(cancelled)")
		u.exited(0)
		u.endReason("interrupted")
	})
	t.Run("at a question", func(t *testing.T) {
		m := startModel(t)
		m.on("@write", writes("approved.txt"))
		w := newWorld(t, m.url())
		u := startUI(t, w)
		u.send("@write")
		u.question("Write a file")
		u.signal(syscall.SIGTERM)
		u.exited(0)
		if exists(filepath.Join(w.project, "approved.txt")) {
			t.Error("a chat that was told to stop approved a write")
		}
		u.endReason("interrupted")
	})
}

// ---- the start ----

// A tool server of the project asks, while the session is made, whether it may start: the question is on the screen like any other,
// and the session is made when it is answered.
func TestE2EChatAToolServerOfTheProjectAsksWhileTheSessionStarts(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	slowServer(t, w)
	u := launch(t, w, uiRows, uiCols, "--trust-project")
	vis := u.question("Start a tool server")
	for _, want := range []string{"start the MCP server \"slow\"", "1. Yes, start it this time", "2. Yes, and remember this exact entry for this project", "3. No"} {
		if !strings.Contains(vis, want) {
			t.Errorf("the question lacks %q:\n%s", want, vis)
		}
	}
	u.typeText("3")
	u.ready()
	u.expect("did not start")
	u.send("@hello")
	u.expect("hi there")
	u.ctrlD()
	u.exited(0)
}

// Ctrl-C while the session is being made ends the chat with the status a shell gives a process that Ctrl-C stopped, and says why.
func TestE2EChatCtrlCWhileTheSessionStartsIsAnInterruption(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())
	slowServer(t, w)
	u := launch(t, w, uiRows, uiCols, "--trust-project")
	u.question("Start a tool server")
	u.ctrlC()
	u.exited(130)
	if !strings.Contains(u.all(), "sleipnir: interrupted") {
		t.Errorf("a chat that was interrupted says so:\n%s", u.all())
	}
}

// SIGTERM while the session is being made ends the chat with 143, and the terminal is as it was.
func TestE2EChatSIGTERMWhileTheSessionStarts(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())
	slowServer(t, w)
	u := launch(t, w, uiRows, uiCols, "--trust-project")
	u.question("Start a tool server")
	u.signal(syscall.SIGTERM)
	u.exited(143)
	if !strings.Contains(u.all(), "sleipnir: interrupted") {
		t.Errorf("a chat that was told to stop says so:\n%s", u.all())
	}
}

// A session that cannot be made is an error with its reason, printed once the terminal is back, and status 1.
func TestE2EChatASessionThatCannotBeMadeEndsWithWhy(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())
	u := launch(t, w, uiRows, uiCols, "--cwd", "/nonexistent/dir")
	u.exited(1)
	all := u.all()
	if !strings.Contains(all, "sleipnir: working directory /nonexistent/dir does not exist") || strings.Contains(all, "sleipnir: interrupted") {
		t.Errorf("the chat says why it could not start:\n%s", all)
	}
}

// ---- approvals ----

// A tool that needs approval in the default mode asks on the screen, and the numbers, the arrows with enter, and esc answer it.
func TestE2EChatApproval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keys    string
		allowed bool
	}{
		{"1", "1", true},
		{"2", "2", true},
		{"3", "3", false},
		{"enter takes the choice, which starts at yes", keyEnter, true},
		{"down and enter", keyDown + keyEnter, true},
		{"up wraps to no", keyUp + keyEnter, false},
		{"tab and enter", keyTab + keyEnter, true},
		{"esc says no", keyEsc, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := startModel(t)
			m.on("@write", writes("approved.txt"))
			w := newWorld(t, m.url())
			u := startUI(t, w)

			u.send("@write")
			vis := u.question("Write a file")
			for _, want := range []string{"approved.txt", "1. Yes", "2. Yes, and don't ask again for edits in this project this session", "3. No, and tell Sleipnir what to do instead"} {
				if !strings.Contains(vis, want) {
					t.Errorf("the question lacks %q:\n%s", want, vis)
				}
			}
			u.typeText(tc.keys)
			u.turnsDone(1)
			u.ready()
			mark := "x"
			if tc.allowed {
				mark = "ok"
			}
			if all := u.all(); !regexp.MustCompile(`\* Write approved\.txt\s+` + mark + ` `).MatchString(all) {
				t.Errorf("the turn should show %q for the write:\n%s", mark, all)
			}
			if got := exists(filepath.Join(w.project, "approved.txt")); got != tc.allowed {
				t.Errorf("approved.txt exists: %v, want %v", got, tc.allowed)
			}
			u.ctrlD()
			u.exited(0)
			res := m.toolResults()
			if len(res) != 1 || strings.HasPrefix(res[0], "Created approved.txt") != tc.allowed {
				t.Errorf("the model was told %q, want the write to be reported as %v", res, map[bool]string{true: "done", false: "refused"}[tc.allowed])
			}
		})
	}
}

// Letters never answer a question: they are what a sentence is made of, and go to the prompt (which is under the question, dim, so
// that it can be seen that they do).
func TestE2EChatLettersDoNotAnswerAQuestion(t *testing.T) {
	m := startModel(t)
	m.on("@write", writes("approved.txt"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@write")
	u.question("Write a file")
	u.typeText("yanYAN")
	u.expectVisible("| > yanYAN", "Write a file", "1. Yes")
	u.typeText(keyCtrlU) // the line is gone, and the question has waited for a quiet keyboard again
	u.wait("an empty prompt under a question that takes answers", func(vis, _ string) bool {
		return strings.Contains(vis, "Message Sleipnir") && strings.Contains(vis, "esc says no") && strings.Contains(vis, "Write a file")
	})
	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Fatal("a letter approved the write")
	}
	u.typeText("3")
	u.turnsDone(1)
	u.ready()
	u.ctrlD()
	u.exited(0)
	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("the write was done")
	}
}

// "Always this session" is an exact rule: the same request is not asked again, another one is.
func TestE2EChatApprovalAlwaysRemembersTheExactRequest(t *testing.T) {
	m := startModel(t)
	m.on("@touch-a", runs("touch a.marker"))
	m.on("@touch-a-again", runs("touch a.marker"))
	m.on("@touch-b", runs("touch b.marker"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@touch-a")
	u.question("Run a command")
	u.typeText("2")
	u.turnsDone(1)
	u.ready()
	if !strings.Contains(u.s.Transcript(), "Run a command") {
		t.Fatal("the transcript does not hold the title of the question: this test would prove nothing")
	}

	mark := len(u.s.Transcript())
	u.send("@touch-a-again") // the same command: no question, the turn runs to its end
	u.turnsDone(2)
	u.ready()
	if strings.Contains(u.s.Transcript()[mark:], "Run a command") {
		t.Error("the same command was asked about again")
	}

	u.send("@touch-b") // another command: a question
	u.question("Run a command")
	u.typeText("3")
	u.turnsDone(3)
	u.ready()
	u.ctrlD()
	u.exited(0)
	for name, want := range map[string]bool{"a.marker": true, "b.marker": false} {
		if got := exists(filepath.Join(w.project, name)); got != want {
			t.Errorf("%s exists: %v, want %v", name, got, want)
		}
	}
}

// Ctrl-C while the question is on the screen cancels the turn and its question. The line typed next is a goal: it is not the answer
// to the question that was cancelled.
func TestE2EChatCtrlCDuringAnApprovalThenAGoal(t *testing.T) {
	m := startModel(t)
	m.on("@write", writes("approved.txt"))
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@write")
	u.question("Write a file")
	u.ctrlC()
	u.expect("(cancelled)")
	u.ready()

	u.send("@hello")
	u.expect("hi there") // the goal was not swallowed
	u.ready()

	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("the write was done, though its question was cancelled and the next line was a goal")
	}
	u.ctrlD()
	u.exited(0)
	u.goals(m, "@write", "@hello")
	if res := m.toolResults(); len(res) != 0 {
		t.Errorf("a cancelled question produced tool results for the model: %q", res)
	}
}

// A line typed while a turn runs is not the answer to a question that comes later: it waits, shown, and is a goal when the turn is
// over. The question is answered by the key that is pressed for it.
func TestE2EChatALineTypedAheadIsNotTheAnswer(t *testing.T) {
	m := startModel(t)
	held := m.on("@hold-write", writes("approved.txt").held())
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@hold-write")
	held.wait(t) // the turn is running, and will ask when the model answers
	u.send("@hello")
	u.expectVisible(`>> queued: "@hello"`) // typed ahead, and the chat has it
	held.release()                         // the model asks for approval

	u.question("Write a file")
	u.expectVisible(`>> queued: "@hello"`) // the line is still waiting: it was not the answer
	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Fatal("a line typed ahead approved the write")
	}
	u.typeText("1") // the answer
	u.expect("hi there")
	u.ready()
	all := u.all()
	if !regexp.MustCompile(`\* Write approved\.txt\s+ok `).MatchString(all) {
		t.Errorf("the write should have been approved by the key pressed for the question:\n%s", all)
	}
	if strings.Index(all, "* Write approved.txt") > strings.Index(all, "hi there") {
		t.Errorf("the line typed ahead ran as a goal once the turn was over, not before:\n%s", all)
	}
	u.ctrlD()
	u.exited(0)
	if !exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("approved.txt does not exist")
	}
	u.goals(m, "@hold-write", "@hello")
}

// A line that is already typed when Ctrl-C cancels the turn is not thrown away with it: it runs next.
func TestE2EChatALineTypedAheadSurvivesCtrlC(t *testing.T) {
	m := startModel(t)
	slow := m.on("@slow", say("slow turn finished").held())
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@slow")
	slow.wait(t)
	u.send("@hello") // typed ahead
	u.expectVisible(`>> queued: "@hello"`)
	u.ctrlC()
	u.expect("(cancelled)")
	u.expect("hi there") // the line typed ahead ran, without anything being typed after the Ctrl-C
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.goals(m, "@slow", "@hello")
}

// ---- the prompt ----

// A paste of many lines is a chip in the prompt, and what is sent is the whole of it.
func TestE2EChatAPasteIsAChipAndIsSentWhole(t *testing.T) {
	lines := []string{"@paste"}
	for i := 1; i <= 11; i++ {
		lines = append(lines, fmt.Sprintf("pasted line %d", i))
	}
	text := strings.Join(lines, "\n")
	m := startModel(t)
	m.on(text, say("pasted ok"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.paste(text)
	vis := u.expectVisible("[pasted text #1 +12 lines]")
	if strings.Contains(vis, "pasted line 7") {
		t.Errorf("the paste is a chip, not twelve lines in the prompt:\n%s", vis)
	}
	u.enter()
	u.expect("pasted ok")
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.goals(m, text)
}

// The history is kept between sessions, and Up brings the last goal back.
func TestE2EChatHistoryIsKeptBetweenSessions(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	u.ctrlD()
	u.exited(0)
	if data, err := os.ReadFile(filepath.Join(w.state, "history.jsonl")); err != nil || !strings.Contains(string(data), `"@hello"`) {
		t.Errorf("the history is under the state directory: %q %v", data, err)
	}

	u = startUI(t, w)
	u.typeText(keyUp)
	u.expectVisible("| > @hello")
	u.typeText(keyCtrlU)
	u.ready()
	u.ctrlD()
	u.exited(0)
}

// "/" opens the palette of commands, a command runs, /exit quits.
func TestE2EChatSlashCommands(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.typeText("/co")
	vis := u.expectVisible("/cost", "tokens, cost and cache hit ratio so far")
	if strings.Contains(vis, "quit (also") { // what /exit says of itself
		t.Errorf("typing filters the palette:\n%s", vis)
	}
	u.typeText(keyCtrlU)
	u.ready()

	u.send("/cost")
	u.expect("cache-write")
	u.send("/mode plan")
	u.expect("mode: plan")
	u.send("/help")
	all := u.expect("/rewind [id]       list checkpoints", "/exit              quit (Ctrl-D, or Ctrl-C twice at the prompt)")
	if !strings.Contains(all, "/cost              tokens, cost and cache hit ratio so far") {
		t.Errorf("the help is the line chat's:\n%s", all)
	}
	u.send("/nothing")
	u.expect("unknown command /nothing; try /help")

	u.send("/exit")
	u.exited(0)
	if got := m.seen(); len(got) != 0 {
		t.Errorf("a command is not a goal: %q", got)
	}
}

// Shift+Tab steps through the permission modes, and the footer says which is in force.
func TestE2EChatShiftTabCyclesThePermissionMode(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())
	u := startUI(t, w)
	u.expectVisible("default")
	u.typeText(keyBTab)
	u.expectVisible("mode: accept-edits")
	u.typeText(keyBTab)
	u.expectVisible("mode: plan")
	u.ctrlD()
	u.exited(0)
}

// ---- the window ----

// The window changes size and the screen follows: the live region is drawn again for the new width, once, with the box that holds
// the prompt exactly as wide as the window.
func TestE2EChatFollowsTheSizeOfTheWindow(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)
	u.send("@hello")
	u.expect("hi there")
	u.ready()

	box := func(cols int) string { return "+" + strings.Repeat("-", cols-2) + "+" }
	for _, size := range [][2]int{{24, 70}, {30, 120}, {24, 80}} {
		rows, cols := size[0], size[1]
		u.resize(rows, cols)
		vis, _ := u.wait(fmt.Sprintf("a frame of %d columns", cols), func(vis, _ string) bool {
			// the top and the bottom of the box are two rows of the width of the window, and nothing of an older frame is left
			return strings.Count(vis, box(cols)) == 2 && strings.Count(vis, "+--") == 2 && strings.Count(vis, "| > Message Sleipnir") == 1
		})
		for i, row := range strings.Split(vis, "\n") {
			if n := len([]rune(row)); n > cols {
				t.Errorf("row %d is %d cells wide on a window of %d: %q", i, n, cols, row)
			}
		}
	}
	// what is typed after the resize goes where the cursor is
	u.typeText("abc")
	u.expectVisible("| > abc")
	u.sync()
	x, y, _ := u.scr.Cursor()
	if row := u.scr.Rows()[y]; x != 7 || !strings.Contains(row, "| > abc") {
		t.Errorf("the cursor is at (%d,%d) on %q", x, y, row)
	}
	u.typeText(keyCtrlU)
	u.ready()
	u.ctrlD()
	u.exited(0)
}

// ---- what is untrusted ----

// What a model says and what a tool prints is text, never a command to the terminal: an escape sequence in it does not reach the
// terminal as one.
func TestE2EChatNothingTheModelOrAToolWritesIsAnEscapeSequence(t *testing.T) {
	m := startModel(t)
	m.on("@hostile", say("before \x1b]52;c;SGVsbG8=\x07 \x1b[2J\x1b[H\x1b]0;title\x07 \x1bPq#0\x1b\\ after"))
	m.on("@print", runs(`printf 'one \033]52;c;QUFB\007 \033[31mtwo\033[0m \033[2J three\n'`))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("@hostile")
	u.expect("before", "after")
	u.turnsDone(1)
	u.ready()
	u.send("@print")
	// a command that only prints may or may not ask: if it does, it is approved
	u.wait("a question or the end of the turn", func(vis, _ string) bool {
		if strings.Contains(vis, "Run a command") && strings.Contains(vis, "esc says no") {
			u.typeText("1")
		}
		return u.turns() >= 2
	})
	u.expect("one", "two", "three")
	u.ready()
	raw := u.s.Transcript()
	// the program writes colours, cursor moves and a few modes (ESC [), and nothing else: no operating system command (the clipboard,
	// the title, a link), no device control string, no bell
	for _, bad := range []string{"\x1b]", "\x1bP", "\x1b_", "\x1b^", "\x07", "\u009b", "\u009d"} {
		if strings.Contains(raw, bad) {
			t.Errorf("what the terminal was sent holds %q", bad)
		}
	}
	u.sync()
	if len(u.scr.Rejected) > 0 {
		t.Errorf("the terminal was told %q", u.scr.Rejected)
	}
	u.ctrlD()
	u.exited(0)
}

// ---- other terminals ----

// A terminal that cannot be drawn on (TERM=dumb) gets the line chat, byte for byte: no escape sequence, a prompt of its own.
func TestE2EChatOnADumbTerminalIsTheLineChat(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	w.extra = []string{"TERM=dumb"}
	c := &chatTerm{t: t, w: w, term: ptytest.Start(t, w.cmd("chat"))}
	c.prompt()
	c.send("@hello")
	c.expect("hi there")
	c.prompt()
	c.ctrlD()
	c.exited(0)
	if strings.Contains(c.term.Transcript(), "\x1b") {
		t.Errorf("a dumb terminal was sent an escape sequence: %q", c.term.Transcript())
	}
}

// --plain is the line chat on a terminal that could be drawn on.
func TestE2EChatPlainOnATerminalIsTheLineChat(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	c := startPlainChat(t, w)
	c.send("@hello")
	c.expect("hi there")
	c.prompt()
	c.ctrlD()
	c.exited(0)
	if strings.Contains(c.term.Transcript(), "\x1b[?25") || strings.Contains(c.term.Transcript(), "\x1b[?2004") {
		t.Errorf("the line chat does not take the screen: %q", c.term.Transcript())
	}
}
