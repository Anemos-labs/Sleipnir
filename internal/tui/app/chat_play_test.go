package app

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tui/input"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
	"github.com/reee344/sleipnir/internal/tui/svg"
)

// What a playback of a transcript has to be: the program's own screens, in the order the transcript says, the same on every run.
// Nothing here waits for a time to pass (the clock is a number the player sets) and nothing depends on how fast the machine is.

// tape builds a transcript by hand.
type tape struct {
	t   *testing.T
	tr  *ChatTranscript
	now int64
	b   *statetest.Builder
}

func newTape(t *testing.T) *tape {
	return &tape{t: t, tr: &ChatTranscript{Epoch: statetest.Epoch}, b: statetest.NewBuilder()}
}

// at sets the time of the records that follow, in milliseconds.
func (p *tape) at(ms int64) *tape { p.now = ms; return p }

func (p *tape) put(r ChatRecord) *tape {
	r.T = p.now
	p.tr.Records = append(p.tr.Records, r)
	return p
}

func (p *tape) key(k string) *tape { return p.put(ChatRecord{Kind: RecKey, Key: k}) }

// typed types a line with 55 ms between the keys, and leaves the clock after the last of them.
func (p *tape) typed(s string) *tape {
	for _, r := range s {
		p.now += 55
		p.key(FormatKey(input.RuneKey(r, 0)))
	}
	return p
}

func (p *tape) event(e events.Event) *tape { return p.put(ChatRecord{Kind: RecEvent, Event: &e}) }

func (p *tape) text(s string) *tape { return p.put(ChatRecord{Kind: RecText, Agent: "main", Text: s}) }

func (p *tape) call(id, name string, in map[string]any) *ChatCall {
	b, _ := json.Marshal(in)
	return &ChatCall{ID: id, Name: name, Input: b}
}

func (p *tape) attach() *tape {
	return p.put(ChatRecord{Kind: RecAttach, Attach: &ChatAttachRecord{Version: "0.1.0", Model: "mock-1", Cwd: "/work/proj", Session: "20260102-030405-abcdef"}})
}

// the prompt the session sends, in the layers the stack bar draws
func (p *tape) request(req string) *tape {
	return p.event(p.b.Request("main", req, "mock-1", "pk1",
		statetest.Sec{Name: "shared", Tokens: 3200, BP: true}, statetest.Sec{Name: "role", Tokens: 300}, statetest.Sec{Name: "notes", Tokens: 600}))
}

func (p *tape) response(req string, in, read int) *tape {
	p.event(p.b.Response("main", req, "mock-1", in, read, 0, 200, 0.0031))
	return p.put(ChatRecord{Kind: RecResponse, Agent: "main", Hit: float64(read) / float64(in+read)})
}

// chatStory is the transcript most of these tests play: a goal typed and sent; an answer that streams as markdown; a command that fails; a
// file edited after the person has pressed 1; the end of the turn; a second goal, and a Ctrl-C that cancels it.
func chatStory(t *testing.T) *ChatTranscript {
	t.Helper()
	p := newTape(t)
	p.at(300).attach()
	p.at(700).typed("fix it")
	p.at(1100).key("enter").put(ChatRecord{Kind: RecTurn, Text: "fix it"})
	p.request("main.1")
	p.at(2000).text("# Plan\n\nFirst, a list:\n\n- one\n- two\n\n")
	p.at(2100).text("```go\nx := 1\n```\n\nDone.")
	p.at(2300).response("main.1", 5000, 0)
	test := p.call("c1", "bash", map[string]any{"command": "go test ./..."})
	p.at(2300).put(ChatRecord{Kind: RecToolStart, Agent: "main", Call: test})
	p.at(3600).put(ChatRecord{Kind: RecToolEnd, Agent: "main", Call: test, TookMS: 1300,
		Result: &ChatResult{Text: "--- FAIL: TestX (0.00s)\nFAIL\n[exit code 1]", Meta: map[string]any{"exit_code": float64(1)}}})
	p.request("main.2")
	p.at(4200).text("The fix is one line.")
	p.at(4300).response("main.2", 600, 4400)
	edit := p.call("c2", "edit", map[string]any{"path": "/work/proj/a.go", "old_string": "x := page * size", "new_string": "x := (page - 1) * size"})
	p.at(4300).put(ChatRecord{Kind: RecToolStart, Agent: "main", Call: edit})
	p.put(ChatRecord{Kind: RecAsk, Ask: &ChatAsk{Request: perm.Request{Agent: "main", Role: "worker", Tool: "edit", Paths: []string{"/work/proj/a.go"},
		Summary: "edit a.go [default mode: writing /work/proj/a.go needs approval]"}, Allow: true}})
	p.at(6200).key("1")
	p.at(6200).put(ChatRecord{Kind: RecToolEnd, Agent: "main", Call: edit, TookMS: 1900,
		Result: &ChatResult{Text: "Edited a.go", Meta: map[string]any{"added": float64(1), "removed": float64(1),
			"diff": "@@ -1,3 +1,3 @@\n a\n-x := page * size\n+x := (page - 1) * size\n c"}}})
	p.at(6300).put(ChatRecord{Kind: RecTurnEnd, End: &ChatTurnEnd{Steps: 3, CostUSD: 0.0123, HitRatio: 0.8}})
	p.at(8000).typed("again")
	p.at(8400).key("enter").put(ChatRecord{Kind: RecTurn, Text: "again"})
	p.request("main.3")
	p.at(9000).text("I will start by looking at")
	p.at(9200).key("ctrl+c")
	p.event(p.b.Cancel("main", "model", "canceled", 0))
	p.put(ChatRecord{Kind: RecTurnEnd, End: &ChatTurnEnd{Err: ErrTranscriptCanceled}})
	return p.tr
}

// screen is what a frame shows, as text.
func screen(f svg.Frame) string {
	var rows []string
	for _, row := range f.Rows {
		var b strings.Builder
		for _, c := range row {
			if c.Text == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(c.Text)
			}
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(rows, "\n")
}

func screens(frames []svg.Frame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = screen(f)
	}
	return out
}

// firstShowing is the index of the first screen that has all of the words, and fails the test (with the last screen) if none has.
func firstShowing(t *testing.T, scr []string, words ...string) int {
	t.Helper()
	for i, s := range scr {
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(s, w)
		}
		if ok {
			return i
		}
	}
	t.Fatalf("no screen of %d shows %q; the last one:\n%s", len(scr), words, scr[len(scr)-1])
	return -1
}

func play(t *testing.T, tr *ChatTranscript, o ChatPlayOptions) []svg.Frame {
	t.Helper()
	frames, err := PlayChat(tr, o)
	if err != nil {
		t.Fatal(err)
	}
	return frames
}

func TestPlayChatShowsTheSessionAsTheTranscriptTellsIt(t *testing.T) {
	scr := screens(play(t, chatStory(t), ChatPlayOptions{Cols: 100, Rows: 36, FPS: 15}))

	start := scr[0]
	if !strings.HasPrefix(start, "$ sleipnir chat\n") || !strings.Contains(start, "Starting") {
		t.Errorf("the first screen is the shell's line and the program starting:\n%s", start)
	}
	// in the order the transcript has them
	steps := []struct {
		what  string
		words []string
	}{
		{"the banner", []string{"◆ sleipnir 0.1.0", "mock-1", "/work/proj"}},
		{"the goal being typed", []string{"❯ fix"}},
		{"the goal typed", []string{"│ ❯ fix it"}},
		{"the goal sent", []string{"❯ fix it", "esc to interrupt"}},
		{"the answer's heading", []string{"● Plan"}},
		{"the list", []string{"• one", "• two"}},
		{"the code", []string{"x := 1"}},
		{"the failing command", []string{"● Bash go test ./...", "✗ 1.3s · exit 1", "⎿ --- FAIL: TestX"}},
		{"the question", []string{"Edit a file", "1 2 3 answers", "waiting for your answer"}},
		{"the edit as a diff", []string{"● Edit a.go", "1.9s", "+1 −1", "(page - 1) * size"}},
		{"the end of the turn", []string{"── 5.0s", "3 steps", "$0.01", "cache hit 80%"}},
		{"the second goal sent", []string{"❯ again", "esc to interrupt"}},
		{"the words that were cut", []string{"I will start by looking at"}},
		{"the cancel", []string{"(cancelled)"}},
	}
	at := -1
	for _, s := range steps {
		i := firstShowing(t, scr, s.words...)
		if i < at {
			t.Errorf("%s is on screen %d, before the step before it (%d)", s.what, i, at)
		}
		at = max(at, i)
	}
	if last := scr[len(scr)-1]; strings.Contains(last, "esc to interrupt") || !strings.Contains(last, "(cancelled)") {
		t.Errorf("when the turn is over the status line is gone and the prompt waits:\n%s", last)
	}
	// the stack bar and the clock are drawn from the events, the question hides them, and the spinner turns
	if i := firstShowing(t, scr, "prompt 5.0k", "% cached", "hit ratio per request"); i < 0 {
		t.Error("no stack")
	}
	glyphs := map[string]bool{}
	for _, s := range scr {
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "esc to interrupt") && len(line) > 0 {
				glyphs[string([]rune(line)[:1])] = true
			}
		}
	}
	if len(glyphs) < 4 {
		t.Errorf("the spinner turned through %d glyphs: %v", len(glyphs), glyphs)
	}
	for i, s := range scr {
		if strings.Contains(s, "Edit a file") && strings.Contains(s, "hit ratio per request") {
			t.Errorf("screen %d: a question has the live region, the stack bar is not drawn beside it", i)
		}
	}
}

// The same transcript is the same pictures, however the machine schedules the program and the player: four playbacks at once give the
// same bytes, and the bytes of the SVG are the same as a fifth one's.
func TestPlayChatIsTheSameEveryTime(t *testing.T) {
	tr := chatStory(t)
	o := ChatPlayOptions{Cols: 100, Rows: 36, FPS: 15}
	var wg sync.WaitGroup
	docs := make([]string, 4)
	for i := range docs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frames, err := PlayChat(tr, o)
			if err != nil {
				docs[i] = err.Error()
				return
			}
			docs[i] = svg.Animated(frames, svg.Theme{}, svg.Options{Hold: 4 * time.Second})
		}()
	}
	wg.Wait()
	for i := 1; i < len(docs); i++ {
		if docs[i] != docs[0] {
			t.Fatalf("playback %d is not playback 0 (%d and %d bytes; %.80s)", i, len(docs[i]), len(docs[0]), docs[i])
		}
	}
	frames := play(t, tr, o)
	if again := svg.Animated(frames, svg.Theme{}, svg.Options{Hold: 4 * time.Second}); again != docs[0] {
		t.Error("a later playback is not the first")
	}
	if !strings.Contains(docs[0], "@keyframes") || strings.Contains(docs[0], "<script") {
		t.Error("the picture is an animation of CSS and no script")
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].At <= frames[i-1].At {
			t.Fatalf("frame %d is at %v, not after frame %d (%v)", i, frames[i].At, i-1, frames[i-1].At)
		}
	}
	if frames[0].At != 0 {
		t.Errorf("the picture starts at %v", frames[0].At)
	}
}

// A question takes the number of an option and never a letter: the transcript that has the person press y is one whose question was
// not answered, and the player says so instead of drawing a session that did not happen.
func TestPlayChatAQuestionIsAnsweredByANumberAndNeverByALetter(t *testing.T) {
	letter := chatStory(t)
	for i := range letter.Records {
		if letter.Records[i].Kind == RecKey && letter.Records[i].Key == "1" {
			letter.Records[i].Key = "y"
		}
	}
	_, err := PlayChat(letter, ChatPlayOptions{Guard: 10 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "question") {
		t.Errorf("a question answered by y was played: %v", err)
	}

	no := chatStory(t)
	for i := range no.Records {
		if no.Records[i].Kind == RecAsk {
			no.Records[i].Ask.Allow = false // the transcript says the key 1 said no
		}
	}
	_, err = PlayChat(no, ChatPlayOptions{Guard: 10 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "answered the question with allow=true") {
		t.Errorf("the key 1 said yes, and the transcript says no: %v", err)
	}

	two := chatStory(t)
	for i := range two.Records {
		if two.Records[i].Kind == RecKey && two.Records[i].Key == "1" {
			two.Records[i].Key = "2" // yes, and do not ask again: the session keeps the answer
		}
		if two.Records[i].Kind == RecAsk {
			two.Records[i].Ask.Remember = perm.ScopeSession
		}
	}
	if _, err := PlayChat(two, ChatPlayOptions{Guard: 10 * time.Second}); err != nil {
		t.Errorf("the key 2 is the second answer: %v", err)
	}
}

func TestPlayChatACancelledTurnIsOneThatTheKeysCancelled(t *testing.T) {
	missing := chatStory(t)
	for i := range missing.Records {
		if missing.Records[i].Kind == RecKey && missing.Records[i].Key == "ctrl+c" {
			missing.Records[i].Key = "x" // typed, not pressed
		}
	}
	_, err := PlayChat(missing, ChatPlayOptions{Guard: 10 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "did not cancel it") {
		t.Errorf("a turn that no key cancelled was played as cancelled: %v", err)
	}
}

func TestPlayChatSaysWhereTheTranscriptAndTheProgramDisagree(t *testing.T) {
	wrongGoal := chatStory(t)
	for i := range wrongGoal.Records {
		if wrongGoal.Records[i].Kind == RecTurn {
			wrongGoal.Records[i].Text = "something else"
			break
		}
	}
	if _, err := PlayChat(wrongGoal, ChatPlayOptions{Guard: 10 * time.Second}); err == nil || !strings.Contains(err.Error(), `sent the goal "fix it"`) {
		t.Errorf("a goal that is not the one that was typed: %v", err)
	}

	noEnter := newTape(t)
	noEnter.at(100).attach().at(200).typed("fix it").at(900).put(ChatRecord{Kind: RecTurn, Text: "fix it"})
	if _, err := PlayChat(noEnter.tr, ChatPlayOptions{Guard: 500 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "sent no goal") {
		t.Errorf("a goal that the person never sent: %v", err)
	}

	early := newTape(t)
	early.at(100).put(ChatRecord{Kind: RecTurnEnd, End: &ChatTurnEnd{}})
	if _, err := PlayChat(early.tr, ChatPlayOptions{Guard: 10 * time.Second}); err == nil {
		t.Error("a turn ended before the session was made")
	}
}

func TestPlayChatFromAndLengthChooseTheStretchOfTheSession(t *testing.T) {
	tr := chatStory(t)
	all := play(t, tr, ChatPlayOptions{FPS: 5})
	part := play(t, tr, ChatPlayOptions{FPS: 5, From: 2400 * time.Millisecond, Length: 1500 * time.Millisecond})
	if len(part) < 7 || len(part) > 9 || len(part) >= len(all) {
		t.Errorf("a second and a half at 5 frames a second is %d frames, the whole is %d", len(part), len(all))
	}
	if part[0].At != 0 {
		t.Errorf("a stretch starts at its own beginning, not at %v", part[0].At)
	}
	// what happened before the stretch is on the screen already, and nothing after it is
	first, last := screen(part[0]), screen(part[len(part)-1])
	if !strings.Contains(first, "◆ sleipnir") || !strings.Contains(first, "❯ fix it") {
		t.Errorf("the state at the start of the stretch:\n%s", first)
	}
	if strings.Contains(last, "Edit a file") || strings.Contains(last, "(cancelled)") {
		t.Errorf("the end of the stretch is at 3.9 s, before the question:\n%s", last)
	}
	noShell := screen(play(t, tr, ChatPlayOptions{FPS: 5, Shell: "-"})[0])
	if strings.Contains(noShell, "$ sleipnir chat") {
		t.Errorf("no shell line was asked for:\n%s", noShell)
	}
	custom := screen(play(t, tr, ChatPlayOptions{FPS: 5, Shell: "$ sleipnir chat --model x"})[0])
	if !strings.HasPrefix(custom, "$ sleipnir chat --model x\n") {
		t.Errorf("the shell line that was asked for:\n%s", custom)
	}
}

// Whatever the rate, the frames are in order in time, and a rate that the program's own does not divide has frames that are not evenly
// apart: each has the time it is at.
func TestPlayChatFramesFollowTheRate(t *testing.T) {
	tr := chatStory(t)
	for _, fps := range []int{1, 3, 5, 10, 15, 60} {
		frames := play(t, tr, ChatPlayOptions{FPS: fps, Length: 3 * time.Second})
		want := min(fps, UIFPS) * 3
		if len(frames) < want || len(frames) > want+1 {
			t.Errorf("%d frames a second for 3 seconds: %d frames, want %d", fps, len(frames), want)
		}
		for i := 1; i < len(frames); i++ {
			if frames[i].At <= frames[i-1].At {
				t.Fatalf("%d fps: frame %d is at %v after %v", fps, i, frames[i].At, frames[i-1].At)
			}
		}
	}
}

// The program is run with the animation on, and what a transcript makes of a frame with it off is a still: the spinner stands.
func TestPlayChatWithNoAnimationNothingTurns(t *testing.T) {
	scr := screens(play(t, chatStory(t), ChatPlayOptions{FPS: 15, NoAnim: true}))
	glyphs := map[string]bool{}
	for _, s := range scr {
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "esc to interrupt") {
				glyphs[string([]rune(line)[:1])] = true
			}
		}
	}
	for g := range glyphs {
		if g != "●" && g != "?" { // the mark that stands in for the spinner, and the one of a question
			t.Errorf("the spinner stands still, and a status line begins with %q", g)
		}
	}
}
