package app

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

func scene(sn *state.Snapshot, v View, cols, rows int) Scene {
	mem := NewMemory()
	mem.Seed(sn)
	return Scene{Snap: sn, View: v, Agent: Newest(sn), Cols: cols, Rows: rows, Pal: widget.DefaultPalette(), Mem: mem, Frame: 1000, Mode: Mode{Final: true}}
}

// sceneOf is scene for one agent.
func sceneOf(sn *state.Snapshot, agent string, v View, cols, rows int) Scene {
	s := scene(sn, v, cols, rows)
	s.Agent = agent
	return s
}

// Every view, on every size from a corner of a screen to a wall of it, is exactly as many lines as the screen has, none wider than
// it, none holding a control character, whatever the session is: nothing, a few events of one, all of the demo, the hand-made story.
func TestEveryViewFillsTheScreenExactlyAtEverySize(t *testing.T) {
	demo := statetest.DemoEvents()
	sessions := map[string]*state.Snapshot{"empty": state.New().Snapshot(), "story": storySnapshot(t)}
	part := state.New()
	for _, e := range demo[:40] {
		part.Apply(e)
	}
	sessions["demo-40"] = part.Snapshot()
	all := state.New()
	for _, e := range demo {
		all.Apply(e)
	}
	sessions["demo"] = all.Snapshot()
	sizes := [][2]int{{1, 1}, {3, 2}, {10, 4}, {23, 5}, {24, 6}, {40, 10}, {59, 12}, {60, 8}, {60, 18}, {69, 24}, {70, 24}, {80, 24}, {95, 30}, {96, 30}, {100, 36}, {132, 43}, {200, 60}}
	for name, sn := range sessions {
		for v := View(0); v < numViews; v++ {
			for _, sz := range sizes {
				cols, rows := sz[0], sz[1]
				lines := Draw(scene(sn, v, cols, rows))
				if len(lines) != rows {
					t.Errorf("%s %s %dx%d: %d lines", name, v, cols, rows, len(lines))
					continue
				}
				for i, l := range lines {
					if w := l.Width(); w > cols {
						t.Errorf("%s %s %dx%d: line %d is %d cells wide: %q", name, v, cols, rows, i, w, l.Plain())
					}
					for _, r := range l.Plain() {
						if unicode.IsControl(r) {
							t.Errorf("%s %s %dx%d: line %d holds the control character %U", name, v, cols, rows, i, r)
						}
					}
				}
			}
		}
	}
}

func TestViewNamesParse(t *testing.T) {
	for in, want := range map[string]View{"": ViewCockpit, "cockpit": ViewCockpit, "o": ViewCockpit, "overview": ViewCockpit, "cache": ViewCache, "c": ViewCache,
		"Mail": ViewMail, " m ": ViewMail, "board": ViewBoard, "b": ViewBoard} {
		got, err := ParseView(in)
		if err != nil || got != want {
			t.Errorf("ParseView(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseView("graph"); err == nil || !strings.Contains(err.Error(), "cockpit, cache, mail, board") {
		t.Errorf("an unknown view names the ones there are: %v", err)
	}
	for v := View(0); v < numViews; v++ {
		if back, err := ParseView(v.String()); err != nil || back != v {
			t.Errorf("%v does not parse back: %v %v", v, back, err)
		}
	}
}

// The cache view of the story says what happened to the manager's cache in the words and glyphs a person looks for.
func TestTheCacheViewTellsTheStoryOfAnAgent(t *testing.T) {
	sn := storySnapshot(t)
	text := plainText(Draw(sceneOf(sn, "mgr", ViewCache, 110, 44)))
	for _, want := range []string{
		"cache · mgr", "Build the shop", // whose cache, in which session
		"mgr manager", "9 requests", // the headline
		"G0", "G1", "G2", "G3", "G4", "G5", // the layers of the prompt
		"▏", "cached", "paid", "breakpoint", // the legend
		"warm",                                                   // the clock
		"hit ratio per request", "⚠ cache break", "◆ compaction", // the history and its marks
		"its prefix", "3 riders", "mgr w1 w2", // who shares the prefix
		"compactions", "31.2k", "2.4k", "cold", // the fold, and the moment it was made at
		"cache anomalies", "drift", "notes", // the break and the layer that diverged
		"every agent's cache", "› mgr", "w1", "w2", // the comparison, with the agent marked
		"the session", "saved", // the totals
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the cache view lacks %q:\n%s", want, text)
		}
	}
	other := plainText(Draw(sceneOf(sn, "w2", ViewCache, 110, 44)))
	if !strings.Contains(other, "cache · w2") || !strings.Contains(other, "› w2") {
		t.Errorf("the view follows the agent it is asked about:\n%s", other)
	}
	if strings.Contains(other, "compactions\n") && strings.Contains(other, "31.2k") {
		t.Errorf("w2 never compacted its thread:\n%s", other)
	}
}

func TestTheMailAndTheBoardShowTheirSessions(t *testing.T) {
	sn := storySnapshot(t)
	mail := plainText(Draw(scene(sn, ViewMail, 100, 30)))
	for _, want := range []string{"mail · Build the shop", "2 sent", "w1 ➜ w2", "schema is ready", "w2 ➜ mgr", "need the price", "who writes to whom", "×1"} {
		if !strings.Contains(mail, want) {
			t.Errorf("the mail view lacks %q:\n%s", want, mail)
		}
	}
	board := plainText(Draw(scene(sn, ViewBoard, 100, 30)))
	for _, want := range []string{"board ·", "3 tasks", "1 todo", "1 running", "1 merged", "task board", "T1", "T2", "T3", "the catalogue api", "w1", "merge queue"} {
		if !strings.Contains(board, want) {
			t.Errorf("the board view lacks %q:\n%s", want, board)
		}
	}
}

// The animations of the cache view are functions of the frame and of when things arrived: the same scene at the same frame is the
// same screen, a new answer sweeps a light along the prompt, a new compaction folds, a new anomaly flashes, and with NoAnim none of
// it moves.
func TestTheCacheViewAnimatesWhatArrives(t *testing.T) {
	sn := storySnapshot(t)
	draw := func(frame int, born func(*Memory), noAnim bool) string {
		mem := NewMemory()
		mem.Seed(sn)
		if born != nil {
			born(mem)
		}
		s := Scene{Snap: sn, View: ViewCache, Agent: "mgr", Frame: frame, Cols: 110, Rows: 44, Pal: widget.DefaultPalette(), Mem: mem, NoAnim: noAnim}
		return plainText(Draw(s))
	}
	settled := draw(500, nil, false)
	if settled != draw(500, nil, false) {
		t.Fatal("the same scene must draw the same screen")
	}
	a := pick(sn, "mgr")
	arrives := func(seqs ...uint64) func(*Memory) {
		return func(m *Memory) {
			for _, s := range seqs {
				m.Born[s] = 500 // born at the frame the screen is drawn at
			}
		}
	}
	sweeping := draw(500, arrives(a.Stack.RespSeq), false)
	if sweeping == settled {
		t.Error("a response that has just arrived must be shown sweeping the prompt")
	}
	if !strings.ContainsAny(sweeping, "▶▷▹") {
		t.Errorf("the light of the sweep is not on the prompt:\n%s", sweeping)
	}
	late := draw(500+sweepFrames, arrives(a.Stack.RespSeq), false)
	if late != settled {
		t.Error("the sweep is over after sweepFrames: the screen is the settled one")
	}
	folding := draw(500, arrives(a.Compacts[len(a.Compacts)-1].Seq), false)
	if folding == settled {
		t.Error("a compaction that has just landed must be shown folding")
	}
	if draw(500, arrives(a.Stack.RespSeq, a.Compacts[0].Seq), true) != settled {
		t.Error("with the animation off nothing moves")
	}
}

// Text that came from the session is data: an agent, a task, a message and a goal made of escape sequences and controls reach
// the screen as text and never as anything a terminal would act on.
func TestHostileTextNeverReachesTheScreenAsControl(t *testing.T) {
	evil := "\x1b[2J\x1b]52;c;ZXZpbA==\x07evil\x00\u202e\u0085\r\n"
	b := statetest.NewBuilder()
	b.Emit("", events.TypeSessionStart, startPayload())
	b.Emit("a"+evil, events.TypeUserInput, map[string]any{"text": "goal " + evil, "origin": "user"})
	b.Spawn("w"+evil, "role"+evil, "task"+evil, "")
	b.Emit("w"+evil, events.TypeBoardOp, taskOp("create", "T"+evil, "doing", "w"+evil, 1, "title"+evil))
	b.Emit("w", events.TypeMailSend, map[string]any{"id": "m", "from": "w" + evil, "to": "x" + evil, "kind": "info", "text": "text " + evil})
	b.Request("w"+evil, "r1", "m"+evil, "pk", statetest.Sec{Name: "shared" + evil, Tokens: 100})
	b.Response("w"+evil, "r1", "m"+evil, 100, 0, 0, 10, 0.01)
	b.Emit("w"+evil, events.TypeCacheAnomaly, map[string]any{"kind": "drift" + evil, "diverged": "notes" + evil})
	st := state.New()
	for _, e := range b.Events() {
		st.Apply(e)
	}
	sn := st.Snapshot()
	for v := View(0); v < numViews; v++ {
		for _, l := range Draw(scene(sn, v, 100, 30)) {
			for _, sp := range l {
				for _, r := range sp.Text {
					if unicode.IsControl(r) || r == '\u202e' {
						t.Fatalf("%s: the screen holds %U in %q", v, r, sp.Text)
					}
				}
			}
		}
	}
}

func TestADrawingOfNoSessionIsAWaitingScreen(t *testing.T) {
	for _, sn := range []*state.Snapshot{nil, state.New().Snapshot()} {
		text := plainText(Draw(Scene{Snap: sn, Cols: 80, Rows: 10, Pal: widget.DefaultPalette()}))
		if !strings.Contains(text, "waiting for the first event") {
			t.Errorf("an empty session says it is waiting:\n%s", text)
		}
	}
	text := plainText(Draw(Scene{Snap: state.New().Snapshot(), Cols: 80, Rows: 10, Pal: widget.DefaultPalette(), Mode: Mode{Replay: true}}))
	if !strings.Contains(text, "holds no events") {
		t.Errorf("an empty recording says so:\n%s", text)
	}
}

func TestTheTitleBarSaysWhatTheScreenIsDoing(t *testing.T) {
	sn := storySnapshot(t)
	for mode, want := range map[string]struct {
		m    Mode
		text string
	}{
		"live":    {Mode{}, "● live"},
		"paused":  {Mode{Paused: true}, "⏸ paused"},
		"replay":  {Mode{Replay: true, Speed: 4, At: 12 * time.Second, Total: 90 * time.Second}, "▶ replay 4× 12s/1m30s"},
		"replay1": {Mode{Replay: true, Speed: 1}, "▶ replay"},
		"final":   {Mode{Final: true}, "■ stopped"},
	} {
		for _, v := range []View{ViewCockpit, ViewCache} {
			s := scene(sn, v, 120, 40)
			s.Mode = want.m
			if first, _, _ := strings.Cut(plainText(Draw(s)), "\n"); !strings.Contains(first, want.text) {
				t.Errorf("%s %s: the title bar lacks %q: %q", mode, v, want.text, first)
			}
		}
	}
}

// Memory remembers when things arrived and how big G0 must be, and forgets what has left the snapshot.
func TestMemoryRemembersArrivalsAndForgetsTheOld(t *testing.T) {
	sn := storySnapshot(t)
	m := NewMemory()
	m.Observe(sn, 10)
	a := pick(sn, "mgr")
	if age := m.Born.Age(a.Stack.RespSeq, 13); age != 3 {
		t.Errorf("a response seen at frame 10 is 3 frames old at frame 13, got %d", age)
	}
	m.Observe(sn, 20)
	if age := m.Born.Age(a.Stack.RespSeq, 20); age != 10 {
		t.Errorf("seeing it again does not make it new: age %d", age)
	}
	if age := m.Born.Age(999999, 20); age != Settled {
		t.Errorf("what was never seen is settled, got %d", age)
	}
	m.Seed(sn)
	if m.G0 <= 0 {
		t.Errorf("G0 is estimated from the smallest unsectioned prompt: %d", m.G0)
	}
	seeded := NewMemory()
	seeded.Seed(sn)
	if age := seeded.Born.Age(a.Stack.RespSeq, 0); age != Settled {
		t.Errorf("what was there before anyone looked has arrived long ago: %d", age)
	}
	var nilMem *Memory
	nilMem.Observe(sn, 1)
	nilMem.Seed(sn)
	if nilMem.born() != nil || nilMem.g0est() != 0 {
		t.Error("a nil Memory is a screen with no history")
	}

	// the map stays small: what is not in the snapshot any more and is old is forgotten
	big := Born{}
	for i := uint64(1); i <= bornMax+100; i++ {
		big[1_000_000+i] = 0
	}
	big.Observe(sn, bornKeep+10)
	if len(big) > bornMax {
		t.Errorf("Born holds %d entries after pruning, the bound is %d", len(big), bornMax)
	}
	if _, ok := big[a.Stack.RespSeq]; !ok {
		t.Error("what the snapshot still holds must be kept")
	}
}

func TestParagraphBreaksBetweenWordsAndNeverLoops(t *testing.T) {
	text := "one two three four five six seven eight nine ten"
	for w := 1; w <= 30; w++ {
		lines := paragraph(cell.Style{}, text, w)
		joined := strings.Join(strings.Fields(plainText(lines)), "")
		if want := strings.Join(strings.Fields(text), ""); w >= 5 && joined != want {
			t.Errorf("w=%d lost text: %q", w, joined)
		}
		for _, l := range lines {
			if l.Width() > w {
				t.Errorf("w=%d: line %q is %d wide", w, l.Plain(), l.Width())
			}
		}
	}
	if got := paragraph(cell.Style{}, "世界世界", 1); len(got) > 0 {
		t.Errorf("a character wider than the line cannot be shown: %v", got)
	}
	if got := paragraph(cell.Style{}, "\xff\xfe bad \x1b[31m bytes", 6); len(got) == 0 {
		t.Error("invalid bytes must not make the text vanish")
	}
	if got := paragraph(cell.Style{}, "x", 0); got != nil {
		t.Errorf("no width, no lines: %v", got)
	}
}

func TestFitCutsWithAnEllipsis(t *testing.T) {
	l := cell.Join(cell.Styled(cell.Style{Attr: cell.Bold}, "hello "), cell.Text("wide world"))
	for w := 0; w < 20; w++ {
		got := fit(l, w)
		if got.Width() > w {
			t.Errorf("fit(%d) is %d wide: %q", w, got.Width(), got.Plain())
		}
	}
	if got := fit(l, 9).Plain(); got != "hello wi…" {
		t.Errorf("fit(9) = %q", got)
	}
	if got := fit(l, 50).Plain(); got != l.Plain() {
		t.Errorf("a line that fits is left alone: %q", got)
	}
	if got := fmt.Sprint(fit(cell.Text("世界世界"), 5).Plain()); got != "世界…" {
		t.Errorf("wide characters are cut whole: %q", got)
	}
}

// Golden screens: the views of the recorded demo at the two sizes a person most likely has. A change of a widget or a view shows
// up here as a diff to read (go test ./internal/tui/app -update).
func TestGoldenViews(t *testing.T) {
	sn := demoSnapshot(t)
	for _, v := range []View{ViewCache, ViewMail, ViewBoard} {
		for _, sz := range [][2]int{{100, 36}, {80, 24}} {
			s := scene(sn, v, sz[0], sz[1])
			golden(t, fmt.Sprintf("view-%s-%d.txt", v, sz[0]), plainText(Draw(s)))
		}
	}
	st := storySnapshot(t)
	for _, v := range []View{ViewCache, ViewMail, ViewBoard} {
		golden(t, fmt.Sprintf("story-%s-110.txt", v), plainText(Draw(sceneOf(st, "mgr", v, 110, 44))))
	}
}
