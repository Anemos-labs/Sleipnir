package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// What the chat shows besides the conversation: the size of the window, the look it is given, what the session's log adds (the prompt
// stack, the hit ratio, a compaction, a cache break) and the panels a key opens.

// sessionLog is what a session's log holds after n requests of the main agent: each reads more of the cache than the one before,
// except the one at brk (1-based), which misses. The clock it ends on is b.Now().
func sessionLog(b *statetest.Builder, n, brk int) []events.Event {
	var out []events.Event
	emit := func(e events.Event) { out = append(out, e) }
	emit(b.Emit("", events.TypeSessionStart, map[string]any{"model": "mock-1", "version": "0.1.0"}))
	secs := func(i int) []statetest.Sec {
		return []statetest.Sec{{Name: "shared", Tokens: 3200, BP: true}, {Name: "role", Tokens: 300}, {Name: "notes", Tokens: 600 + 40*i, BP: i > 1}, {Name: "spine", Tokens: 900}}
	}
	for i := 1; i <= n; i++ {
		req := fmt.Sprintf("r%d", i)
		b.Advance(8 * time.Second)
		emit(b.Request("main", req, "mock-1", "pk1", secs(i)...))
		b.Advance(2 * time.Second)
		read, in := 4000+400*i, 600
		if i == 1 {
			read, in = 0, 5000
		}
		if i == brk {
			emit(b.Emit("main", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "diverged": "notes", "req": req, "expected_read": 4000 + 400*i, "actual_read": 900, "missed": 3000 + 400*i}))
			read, in = 900, 5000
		}
		emit(b.Response("main", req, "mock-1", in, read, 0, 200, 0.0031))
	}
	return out
}

// The chat page is as clean as it can be: nothing about the cache is on it, whatever the log says. The statistics are one key away,
// and the footer says which key.
func TestChatPageCarriesNoStatisticsAndTheStatsPageHoldsThem(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 30})
	b := statetest.NewBuilder()
	log := sessionLog(b, 4, 0)
	r.at(b.Now().Add(48 * time.Second))
	r.emit(log...)
	s := r.visible()
	for _, no := range []string{"prompt ", "cached", "warm", "G1", "hit ratio", "requests", "saved"} {
		if strings.Contains(s, no) {
			t.Errorf("the chat page carries a statistic (%q):\n%s", no, s)
		}
	}
	if !strings.Contains(s, "ctrl+t stats") {
		t.Errorf("the footer names the key of the stats page:\n%s", s)
	}
	r.ctrl('t')
	s = r.shows("❯ /stats", "◆ stats", "cost", "$0.01", "4 requests", "saved ≈", "at list price", "the prompt, layer by layer")
	for _, want := range []string{"shared", "role", "notes", "spine", "of the prompts came from the provider's cache"} {
		if !strings.Contains(s, want) {
			t.Errorf("the stats page lacks %q:\n%s", want, s)
		}
	}
	// the same page, typed out
	r2 := startChat(t, rigOpts{cols: 100, rows: 30})
	r2.at(b.Now().Add(48 * time.Second))
	r2.emit(log...)
	r2.typeText("/stats")
	r2.enter()
	r2.shows("◆ stats", "saved ≈")
	if got := r2.host.commands; len(got) != 0 {
		t.Errorf("the page is drawn by the program, the host was asked %q", got)
	}
}

func TestChatStatsPageBeforeAnyPromptSaysSo(t *testing.T) {
	r := startChat(t, rigOpts{})
	r.ctrl('t')
	r.shows("nothing has been sent yet")
	if strings.Contains(r.screen(), "layer by layer") {
		t.Error("there is no stack to show")
	}
}

// The agents page is the cockpit's table, in the chat: who is in the team and what each one is doing. A single agent has no team, and
// the footer names the key only for a team.
func TestChatAgentsPageShowsTheTeamAndASingleAgentHasNone(t *testing.T) {
	solo := startChat(t, rigOpts{cols: 100, rows: 30})
	if strings.Contains(solo.visible(), "ctrl+g") {
		t.Errorf("a single agent has no agents page:\n%s", solo.visible())
	}
	solo.press(input.RuneKey('g', input.Ctrl))
	solo.shows("a single agent: /swarm 8 starts a team of eight")

	r := startChat(t, rigOpts{cols: 100, rows: 30, team: 8})
	if !strings.Contains(r.visible(), "ctrl+t stats") || !strings.Contains(r.visible(), "ctrl+g agents") {
		t.Errorf("the footer of a team names both pages:\n%s", r.visible())
	}
	r.press(input.RuneKey('g', input.Ctrl))
	r.shows("the team starts with your first goal: a manager, and the 7 workers it can spawn")
	b := statetest.NewBuilder()
	log := sessionLog(b, 2, 0)
	log = append(log, b.Spawn("w1", "backend", "T1", "main"), b.Spawn("w2", "tester", "T2", "main"))
	r.at(b.Now().Add(2 * time.Second))
	r.emit(log...)
	r.press(input.RuneKey('g', input.Ctrl))
	s := r.shows("❯ /agents", "3 agents", "tasks:", "AGENT", "backend", "tester")
	if strings.Contains(s, "a single agent") {
		t.Errorf("a team is not a single agent:\n%s", s)
	}
}

func TestChatACacheBreakIsAWarningInTheScrollback(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 30})
	b := statetest.NewBuilder()
	log := sessionLog(b, 4, 3)
	r.at(b.Now().Add(5 * time.Second))
	r.emit(log...)
	s := r.shows("⚠ cache break in notes (low_hit) · read 900 of 5.2k expected")
	if strings.Count(s, "cache break") != 1 {
		t.Errorf("a break is said once:\n%s", s)
	}
	// more of the log does not say it again
	more := statetest.NewBuilder().At(b.Now())
	r.emit(more.Request("main", "r9", "mock-1", "pk1", statetest.Sec{Name: "shared", Tokens: 3200, BP: true}), more.Response("main", "r9", "mock-1", 100, 3000, 0, 50, 0.001))
	if got := strings.Count(r.screen(), "cache break"); got != 1 {
		t.Errorf("a break that was said is not said again, %d times:\n%s", got, r.screen())
	}
}

// A break whose prompt did not change is the endpoint's: the break line says so, and the agent's own notice, which said the same
// thing in other words, is not printed under it. The first real chat showed both, one under the other, for every break.
func TestChatACacheBreakTheEndpointCausedIsSaidOnceWithItsCause(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 30})
	b := statetest.NewBuilder()
	log := sessionLog(b, 3, 0)
	b.Advance(8 * time.Second)
	log = append(log, b.Request("main", "r4", "mock-1", "pk1", statetest.Sec{Name: "shared", Tokens: 3200, BP: true}))
	log = append(log, b.Emit("main", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "diverged": "", "req": "r4", "expected_read": 6033, "actual_read": 3840, "missed": 2193}))
	log = append(log, b.Response("main", "r4", "mock-1", 3000, 3840, 0, 200, 0.0031))
	r.at(b.Now().Add(2 * time.Second))
	r.sink.Notice("main", "warn", agent.CacheMissNoticePrefix+" ~6033 tokens read from cache, got 3840 (the prompt prefix did not change: the endpoint did not serve it)")
	r.emit(log...)
	s := r.shows("⚠ cache break (low_hit) · read 3.8k of 6.0k expected")
	if strings.Contains(s, "cache miss: expected") {
		t.Errorf("the agent's notice says again what the break line says:\n%s", s)
	}
	if n := strings.Count(s, "the endpoint did not serve it"); n != 1 {
		t.Errorf("the cause is on the screen %d times, once, under the break line:\n%s", n, s)
	}
}

func compactionLog(b *statetest.Builder) []events.Event {
	b.Advance(time.Second)
	return []events.Event{
		b.Emit("main", events.TypeCompactPlan, map[string]any{"decision": "start", "warm": false}),
		b.Emit("main", events.TypeCompactCommit, map[string]any{"reason": "the thread outgrew its budget", "removed_turns": 14, "removed_tokens": 29000,
			"retained_tokens": 2200, "snap_tokens": 31200, "spine_added": 180}),
	}
}

// A compaction is a fold that plays in the live region for a while, and then it is one line in the scrollback.
func TestChatACompactionFoldsAndLeavesOneLine(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 30, tickClock: true})
	b := statetest.NewBuilder()
	log := sessionLog(b, 2, 0)
	r.at(b.Now().Add(time.Second))
	r.step(0) // the program has no clock but the ticks: this is the time it is
	r.emit(log...)
	r.emit(compactionLog(b)...)
	s := r.shows("◆ compacting")
	if strings.Contains(s, "◆ compacted") {
		t.Errorf("the record comes when the fold is over:\n%s", s)
	}
	for range foldFrames {
		r.step(66 * time.Millisecond)
	}
	s = r.shows("◆ compacted")
	if strings.Contains(r.visible(), "◆ compacting") {
		t.Errorf("the fold is gone from the live region:\n%s", s)
	}
	if !strings.Contains(s, "31.2k") || !strings.Contains(s, "2.4k") || strings.Count(s, "◆ compacted") != 1 {
		t.Errorf("the record says what the thread was and is, once:\n%s", s)
	}
}

// Without animation there is no fold to watch: the record is written at once.
func TestChatACompactionWithoutAnimationIsTheRecordAtOnce(t *testing.T) {
	look := defaultLook()
	look.Anim = false
	r := startChat(t, rigOpts{cols: 100, rows: 30, look: look})
	b := statetest.NewBuilder()
	r.at(b.Now().Add(time.Hour))
	r.emit(sessionLog(b, 2, 0)...)
	r.emit(compactionLog(b)...)
	s := r.shows("◆ compacted")
	if strings.Contains(s, "◆ compacting") {
		t.Errorf("nothing folds without animation:\n%s", s)
	}
}

func TestChatTheSpinnerTurnsOnlyWithAnimation(t *testing.T) {
	spin := func(look Look) map[string]bool {
		r := startChat(t, rigOpts{cols: 80, rows: 24, look: look, tickClock: true})
		started := make(chan struct{})
		r.host.turn = func(ctx context.Context, goal string) TurnResult {
			close(started)
			<-ctx.Done()
			return TurnResult{Err: ctx.Err()}
		}
		r.submit("go")
		<-started
		seen := map[string]bool{}
		glyph := regexp.MustCompile(`(?m)^(\S) [A-Z][a-z]+…`)
		for range 12 {
			r.step(70 * time.Millisecond)
			if m := glyph.FindStringSubmatch(r.visible()); m != nil {
				seen[m[1]] = true
			}
		}
		return seen
	}
	moving := spin(defaultLook())
	if len(moving) < 3 {
		t.Errorf("the spinner turns through its frames: %v", moving)
	}
	still := defaultLook()
	still.Anim = false
	if got := spin(still); len(got) != 1 || !got["●"] {
		t.Errorf("the spinner stands still, as a dot: %v", got)
	}
}

func TestChatFollowsTheSizeOfTheWindow(t *testing.T) {
	// Where a terminal re-wraps the rows it had (most do) or cuts them (xterm), the program draws the live region again at the new
	// width, with nothing of the old one left on the screen. (A window that gets so much narrower and shorter that the old region no
	// longer fits it leaves a ghost in the scrollback, where nothing can erase it: the sizes here are a person's, not a stress test.)
	for _, reflow := range []bool{true, false} {
		t.Run(fmt.Sprintf("reflow=%v", reflow), func(t *testing.T) {
			r := startChat(t, rigOpts{cols: 80, rows: 24})
			r.bridge.v.SetReflow(reflow)
			r.host.turn = func(ctx context.Context, goal string) TurnResult {
				r.sink.Text("main", "The answer is long enough that it has to wrap when the window gets narrower, and it is printed once, before the window changes.\n")
				r.sink.Response("main", nil, 0)
				return TurnResult{Steps: 1}
			}
			r.submit("go")
			r.shows("1 step")
			box := func(w int) string { return "╭" + strings.Repeat("─", w-2) + "╮" }
			for _, size := range [][2]int{{60, 24}, {100, 30}, {70, 20}, {80, 24}} {
				cols, rows := size[0], size[1]
				r.resize(cols, rows)
				vis := r.visible()
				if n := strings.Count(vis, box(cols)); n != 1 {
					t.Errorf("at %d columns the input's box is one row of %d cells; the screen:\n%s", cols, cols, vis)
				}
				if n := strings.Count(vis, "╭"); n != 1 {
					t.Errorf("at %d columns there are %d boxes on the screen (a ghost of the one before):\n%s", cols, n, vis)
				}
				for i, row := range strings.Split(vis, "\n") {
					if w := cell.StringWidth(row); w > cols {
						t.Errorf("row %d is %d cells wide on a screen of %d: %q", i, w, cols, row)
					}
				}
				if n := strings.Count(vis, "❯ Type a goal"); n != 1 {
					t.Errorf("at %d columns the prompt is on the screen %d times:\n%s", cols, n, vis)
				}
			}
			// what is typed while the window changes is not lost, and the cursor is where the text is
			r.typeText("hello")
			r.resize(60, 20)
			x, y, shown := r.bridge.v.Cursor()
			rows := r.bridge.v.Rows()
			if !shown || !strings.Contains(rows[y], "❯ hello") || x != 4+len("hello") {
				t.Errorf("the cursor after the resize is at (%d,%d), row %q", x, y, rows[y])
			}
		})
	}
}

// NO_COLOR: the terminal is drawn on, and nothing is coloured. Attributes (bold, dim, reverse) say what colour would have.
func TestChatInMonochromeWritesNoColour(t *testing.T) {
	look := Look{Theme: widget.MonoTheme(), Palette: widget.MonoPalette(), Unicode: true, Anim: false}
	r := startChat(t, rigOpts{look: look, cols: 100, rows: 30})
	gate := make(chan struct{})
	call := toolCall("c1", "edit", map[string]any{"path": "/work/proj/a.go", "old_string": "a", "new_string": "b"})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.Text("main", "# Title\n\nSome **bold** and `code`.\n\n```go\nfunc main() {}\n```\n")
		r.sink.ToolStart("main", call)
		<-gate
		r.sink.ToolEnd("main", call, okResult("Edited", map[string]any{"diff": "@@ -1,1 +1,1 @@\n-a\n+b", "added": 1, "removed": 1}), time.Second)
		r.sink.Notice("main", "warn", "a warning")
		return TurnResult{Steps: 1, CostUSD: 0.01}
	}
	r.submit("go")
	r.shows("Edit a.go")
	close(gate)
	r.shows("1 step")
	b := statetest.NewBuilder()
	r.at(b.Now().Add(time.Minute))
	r.emit(sessionLog(b, 3, 2)...)
	raw := string(r.written())
	if strings.Contains(raw, "\x1b[38") || strings.Contains(raw, "\x1b[48") {
		t.Errorf("a colour was written")
	}
	for _, m := range regexp.MustCompile("\x1b\\[([0-9;]*)m").FindAllStringSubmatch(raw, -1) {
		for _, p := range strings.Split(m[1], ";") {
			switch p {
			case "", "0", "1", "2", "3", "4", "7", "22", "23", "24", "27":
			default:
				t.Fatalf("SGR %q is not an attribute but a colour or something else: %q", p, m[0])
			}
		}
	}
	if !strings.Contains(raw, "\x1b[1m") || !strings.Contains(raw, "\x1b[2m") {
		t.Error("bold and dim carry the emphasis the colours do not")
	}
}

// Where only ASCII is trusted nothing else is written: not a bullet, not a box corner, not an arrow.
func TestChatInASCIIWritesNothingElse(t *testing.T) {
	look := Look{Theme: widget.DefaultTheme().WithASCII(true), Palette: widget.DefaultPalette(), Unicode: false, Anim: true}
	r := startChat(t, rigOpts{look: look, cols: 100, rows: 30, tickClock: true})
	call := toolCall("c1", "bash", map[string]any{"command": "go test ./..."})
	edit := toolCall("c2", "edit", map[string]any{"path": "/work/proj/a.go", "old_string": "a", "new_string": "b"})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		r.sink.Text("main", "# Title\n\n- one\n- two\n\n> quote\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {}\n```\n")
		r.sink.ToolStart("main", call)
		r.sink.ToolEnd("main", call, okResult("ok  \texample.com/orders\t0.3s\n[exit code 0]", map[string]any{"exit_code": 0}), 1400*time.Millisecond)
		r.sink.ToolStart("main", edit)
		r.sink.ToolEnd("main", edit, okResult("Edited", map[string]any{"diff": "@@ -1,1 +1,1 @@\n-a\n+b", "added": 1, "removed": 1}), 30*time.Millisecond)
		r.sink.Notice("main", "warn", "a warning")
		return TurnResult{Steps: 2, CostUSD: 0.01, HitRatio: 0.5}
	}
	r.submit("go")
	r.shows("2 steps")
	b := statetest.NewBuilder()
	r.at(b.Now().Add(time.Minute))
	r.emit(append(sessionLog(b, 3, 2), compactionLog(b)...)...)
	for range foldFrames {
		r.step(66 * time.Millisecond)
	}
	r.shows("<> compacted")
	r.ctrl('t')
	r.typeText("/")
	for i, c := range r.written() {
		if c >= 0x80 {
			t.Fatalf("byte %d of what was written is %#x, which is not ASCII; around it: %q", i, c, r.written()[max(i-40, 0):min(i+20, len(r.written()))])
		}
	}
}

// The window is short and the answer is long: what is final goes into the scrollback in order, once, and the live region keeps to its
// share of the terminal.
func TestChatALongAnswerInAShortWindowIsPrintedOnceAndInOrder(t *testing.T) {
	r := startChat(t, rigOpts{cols: 60, rows: 10})
	gate := make(chan struct{})
	r.host.turn = func(ctx context.Context, goal string) TurnResult {
		for i := 1; i <= 40; i++ {
			r.sink.Text("main", fmt.Sprintf("paragraph %d says something of some length so that it takes a line or two of the window.\n\n", i))
		}
		<-gate
		r.sink.Response("main", nil, 0)
		return TurnResult{Steps: 1}
	}
	r.submit("go")
	r.shows("paragraph 40")
	if rows := r.bridge.v.Rows(); len(rows) != 10 {
		t.Fatalf("the screen has %d rows", len(rows))
	}
	for i := 1; i <= 40; i++ {
		if n := strings.Count(r.screen(), fmt.Sprintf("paragraph %d says", i)); n != 1 {
			t.Fatalf("paragraph %d is on the screen %d times:\n%s", i, n, r.screen())
		}
	}
	close(gate)
	s := r.shows("1 step")
	last := -1
	for i := 1; i <= 40; i++ {
		at := strings.Index(s, fmt.Sprintf("paragraph %d says", i))
		if at < last {
			t.Fatalf("paragraph %d is out of order", i)
		}
		last = at
	}
}

// A question that is taller than the window is cut to fit it, and its options are always where they can be read.
func TestChatAQuestionIsCutToTheHeightOfAShortWindow(t *testing.T) {
	for _, rows := range []int{6, 8, 12, 20} {
		r := startChat(t, rigOpts{cols: 70, rows: rows})
		ans := r.ask(bashRequest(strings.Repeat("echo a long command line that wraps; ", 30)))
		s := r.shows("1. Yes")
		if got := len(strings.Split(r.visible(), "\n")); got != rows {
			t.Errorf("the screen has %d rows, want %d", got, rows)
		}
		if !strings.Contains(s, "3. No") {
			t.Errorf("with %d rows the options of the question are all on the screen:\n%s", rows, r.visible())
		}
		r.press(input.RuneKey('3', 0))
		decision(t, ans)
	}
}

// A question is on the screen while the window changes size: it is drawn again for the new window, with its options, and still waits
// for its answer.
func TestChatAQuestionSurvivesAResize(t *testing.T) {
	// (Narrowing a window re-wraps what the terminal had drawn, so a tall region at the full width takes twice its rows for a moment; the
	// program erases that and draws again, which is only possible while the window is tall enough for the doubled region.)
	r := startChat(t, rigOpts{cols: 100, rows: 50})
	ans := r.ask(bashRequest("go test -race -count=1 ./internal/orders/... ./internal/billing/... ./internal/shipping/..."))
	r.shows("1. Yes")
	for _, size := range [][2]int{{70, 50}, {120, 50}, {45, 50}} {
		cols, rows := size[0], size[1]
		r.resize(cols, rows)
		vis := r.visible()
		for _, want := range []string{"Run a command", "1. Yes", "2. Yes, and don't", "3. No"} {
			if !strings.Contains(vis, want) {
				t.Errorf("at %d columns the question lacks %q:\n%s", cols, want, vis)
			}
		}
		for i, row := range strings.Split(vis, "\n") {
			if w := cell.StringWidth(row); w > cols {
				t.Errorf("at %d columns row %d is %d cells wide: %q", cols, i, w, row)
			}
		}
		if n := strings.Count(vis, "Run a command"); n != 1 {
			t.Errorf("at %d columns the question is on the screen %d times:\n%s", cols, n, vis)
		}
	}
	select {
	case d := <-ans:
		t.Fatalf("a resize answered the question: %+v", d)
	default:
	}
	r.press(input.RuneKey('1', 0))
	if d := decision(t, ans); !d.Allow {
		t.Errorf("the question is still the person's to answer: %+v", d)
	}
}

// An endpoint whose cache misses all the time would put a break line under every answer. Three are said, then one line says that the
// rest are not, and the next ones are left to /cost and the inspector.
func TestChatTheEndpointsOwnCacheBreaksAreSaidThreeTimes(t *testing.T) {
	r := startChat(t, rigOpts{cols: 100, rows: 40})
	b := statetest.NewBuilder()
	log := sessionLog(b, 1, 0)
	for i := 2; i <= 7; i++ {
		req := fmt.Sprintf("r%d", i)
		b.Advance(8 * time.Second)
		log = append(log, b.Request("main", req, "mock-1", "pk1", statetest.Sec{Name: "shared", Tokens: 3200, BP: true}))
		log = append(log, b.Emit("main", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "diverged": "", "req": req, "expected_read": 6033, "actual_read": 100, "missed": 5000}))
		log = append(log, b.Response("main", req, "mock-1", 3000, 100, 0, 200, 0.0031))
	}
	r.at(b.Now().Add(2 * time.Second))
	r.emit(log...)
	s := r.shows("further misses are not said here")
	if n := strings.Count(s, "⚠ cache break"); n != maxEndpointBreaksShown {
		t.Errorf("%d break lines, want %d:\n%s", n, maxEndpointBreaksShown, s)
	}
	if n := strings.Count(s, "further misses are not said here"); n != 1 {
		t.Errorf("the note is said %d times, once", n)
	}
}
