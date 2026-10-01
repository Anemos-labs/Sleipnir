package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/input"
	"github.com/reee344/sleipnir/internal/tui/state"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/widgettest"
)

// The live region of the chat as a golden file, at the three widths the design is checked at, in plain text; and one check of the
// styles, because text alone cannot say that a cached layer is bright and a paid one dim. Each file is a pure function of a
// liveView, so a changed cell fails one named test; -update rewrites them (go test ./internal/tui/app -run ChatLive -update), and
// the diff of testdata/ is what to read.

// chatSession is a session of the main agent: seven requests, a cache break at the fifth, a compaction before the sixth. now is a
// moment 48 seconds after the last request, when the cache has 4 minutes 12 seconds left.
func chatSession(t testing.TB) (sn *state.Snapshot, mem *Memory) {
	t.Helper()
	b := statetest.NewBuilder()
	st := state.New()
	apply := func(e events.Event) { st.Apply(e) }
	apply(b.Emit("", events.TypeSessionStart, map[string]any{"model": "mock-1", "version": "0.1.0"}))
	secs := func(i int) []statetest.Sec {
		return []statetest.Sec{{Name: "shared", Tokens: 3200, BP: true}, {Name: "role", Tokens: 300}, {Name: "notes", Tokens: 600 + 40*i, BP: i > 1}, {Name: "spine", Tokens: 900}}
	}
	type step struct{ in, read, out int }
	steps := []step{{7600, 0, 180}, {1100, 6900, 220}, {400, 8800, 140}, {500, 9700, 310}, {6100, 3200, 250}, {800, 2600, 90}, {300, 3700, 400}}
	for i, s := range steps {
		req := fmt.Sprintf("r%d", i+1)
		b.Advance(8 * time.Second)
		if i == 5 {
			apply(b.Emit("main", events.TypeCompactPlan, map[string]any{"decision": "start", "warm": false}))
			apply(b.Emit("main", events.TypeCompactCommit, map[string]any{"reason": "the thread outgrew its budget", "removed_turns": 14, "removed_tokens": 29000,
				"retained_tokens": 2200, "snap_tokens": 31200, "spine_added": 180}))
		}
		apply(b.Request("main", req, "mock-1", "pk1", secs(i)...))
		b.Advance(2 * time.Second)
		if i == 4 {
			apply(b.Emit("main", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "diverged": "notes", "req": req, "expected_read": 9700, "actual_read": 3200, "missed": 6500}))
		}
		apply(b.Response("main", req, "mock-1", s.in, s.read, 0, s.out, 0.0031))
	}
	now := b.Now().Add(48 * time.Second)
	sn = st.SnapshotAt(now)
	mem = NewMemory()
	mem.Seed(sn)
	return sn, mem
}

// chatEditor is an editor with text typed into it, laid out for the inner width of the box.
func chatEditor(t testing.TB, k *chatLook, cols int, text string, cmds []input.Command) input.View {
	t.Helper()
	th := k.inputTheme()
	ed := input.NewEditor(input.Options{Prompt: k.g.prompt + " ", Placeholder: "Type a goal, / for commands, @ for files", Theme: &th,
		Completer: input.SlashCommands(cmds)})
	inner := widget.BoxInnerWidth(cols)
	if inner < 10 {
		inner = cols
	}
	ed.SetWidth(inner)
	for _, r := range text {
		ed.Handle(input.RuneKey(r, 0))
	}
	return ed.View(inner)
}

var goldenCommands = []input.Command{{Name: "help", Description: "this text"}, {Name: "cost", Description: "tokens, cost and cache hit ratio so far"},
	{Name: "compact", Args: "[focus]", Description: "fold the older thread now"}, {Name: "exit", Description: "quit"}}

// liveFixtures are the moments of a chat the golden files hold.
func liveFixtures(t testing.TB, k *chatLook, cols, rows int) map[string]liveView {
	sn, mem := chatSession(t)
	base := liveView{cols: cols, rows: rows, snap: sn, mem: mem, mode: "accept-edits", model: "mock/mock-1", session: "20260102-030405-abcdef", editorOn: true}

	idle := base
	idle.ed = chatEditor(t, k, cols, "now add a test for page 0 and a negative size", goldenCommands)

	busy := base
	busy.ed = chatEditor(t, k, cols, "", goldenCommands)
	busy.status = statusView{kind: statusThinking, seed: 1, elapsed: 14 * time.Second, tokIn: 48300, tokOut: 2100, cost: 0.0021, saved: 0.31}
	md := widget.Markdown("Fixed. `List` computed the offset from a zero-based page; pages are one-based, so page 1 skipped the first `size` rows.", max(cols-2, 1), k.Theme)
	busy.tail = k.answerLines(md, 0)
	busy.tools = []toolView{{title: "Bash", summary: "go test ./orders/... -run TestList -count=1", elapsed: 3 * time.Second}}
	busy.queue = []string{"and run the race detector"}

	ask := base
	ask.ed = chatEditor(t, k, cols, "", goldenCommands)
	ask.status = statusView{kind: statusAsking, elapsed: 20 * time.Second}
	req := perm.Request{Agent: "main", Tool: "bash", Command: "go test -race ./...", Summary: "run a command [not on the allow list]"}
	title, body := k.requestBody(req, nil, widget.BoxInnerWidth(cols, widget.BoxHardWrap()), "/work/proj")
	opts, _ := dialogOptions(req)
	ask.dlg = &dialogView{title: title, body: body, options: opts, armed: true}

	askEdit := ask
	call := &toolRun{name: "edit", input: mustJSON(map[string]any{"path": "/work/proj/orders/list.go", "old_string": "offset := page * size", "new_string": "offset := (page - 1) * size"})}
	req = perm.Request{Agent: "main", Tool: "edit", Paths: []string{"/work/proj/orders/list.go"}, Summary: "edit orders/list.go [outside the allow list]"}
	title, body = k.requestBody(req, call, widget.BoxInnerWidth(cols, widget.BoxHardWrap()), "/work/proj")
	opts, _ = dialogOptions(req)
	askEdit.dlg = &dialogView{title: title, body: body, options: opts, sel: 1, armed: false}

	fold := busy
	fold.tail, fold.tools, fold.queue = nil, nil, nil
	fold.status = statusView{kind: statusCompacting, elapsed: 31 * time.Second, tokIn: 61200, tokOut: 900}
	fold.fold = &foldView{before: 31200, after: 2400, progress: 0.5}

	menu := base
	menu.ed = chatEditor(t, k, cols, "/co", goldenCommands)

	starting := liveView{cols: cols, rows: rows, status: statusView{kind: statusStarting}, ed: chatEditor(t, k, cols, "", nil), mode: "default"}

	return map[string]liveView{"idle": idle, "busy": busy, "ask": ask, "askedit": askEdit, "fold": fold, "menu": menu, "starting": starting}
}

// dump is a live region as the golden file holds it: the rows, and where the cursor is.
func dump(out liveOut, cols int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d columns, %d rows, cursor at row %d column %d\n", cols, len(out.lines), out.curRow, out.curCol)
	b.WriteString(widgettest.Flatten(out.lines))
	return b.String()
}

func goldenLook(unicode bool) *chatLook {
	return newChatLook(Look{Theme: widget.MonoTheme().WithASCII(!unicode), Palette: widget.MonoPalette(), Unicode: unicode, Anim: false})
}

func TestChatLiveRegionGoldens(t *testing.T) {
	k := goldenLook(true)
	for _, cols := range []int{60, 80, 120} {
		for name, v := range liveFixtures(t, k, cols, 30) {
			t.Run(fmt.Sprintf("%s-%d", name, cols), func(t *testing.T) {
				out := k.liveLines(&v)
				for i, l := range out.lines {
					if w := l.Width(); w > cols {
						t.Errorf("row %d is %d cells wide on a screen of %d: %q", i, w, cols, l.Plain())
					}
				}
				if where, found := widgettest.Control(out.lines); found {
					t.Errorf("a control character in the live region: %s", where)
				}
				widgettest.Golden(t, *update, fmt.Sprintf("testdata/chat-live-%s-%d.txt", name, cols), dump(out, cols))
			})
		}
	}
}

func TestChatLiveRegionInASCII(t *testing.T) {
	k := goldenLook(false)
	for name, v := range liveFixtures(t, k, 80, 30) {
		t.Run(name, func(t *testing.T) {
			out := k.liveLines(&v)
			for i, l := range out.lines {
				for _, r := range l.Plain() {
					if r > 0x7e {
						t.Errorf("row %d has %q, which is not ASCII: %q", i, r, l.Plain())
						break
					}
				}
			}
			widgettest.Golden(t, *update, fmt.Sprintf("testdata/chat-live-%s-80-ascii.txt", name), dump(out, 80))
		})
	}
}

// Whatever the terminal's size, the live region is never taller than the terminal and never wider than it, and the cursor is in it.
func TestChatLiveRegionFitsEveryTerminal(t *testing.T) {
	k := goldenLook(true)
	for _, size := range [][2]int{{1, 1}, {4, 2}, {12, 5}, {20, 8}, {40, 10}, {59, 12}, {60, 15}, {80, 24}, {100, 40}, {200, 60}} {
		cols, rows := size[0], size[1]
		for name, v := range liveFixtures(t, k, cols, rows) {
			out := k.liveLines(&v)
			if len(out.lines) > max(rows, liveLimit(rows)+out.boxHeight+4) {
				t.Errorf("%s at %dx%d: %d rows", name, cols, rows, len(out.lines))
			}
			for i, l := range out.lines {
				if w := l.Width(); w > cols {
					t.Errorf("%s at %dx%d: row %d is %d cells wide: %q", name, cols, rows, i, w, l.Plain())
				}
			}
			if out.curRow >= len(out.lines) {
				t.Errorf("%s at %dx%d: the cursor is on row %d of %d", name, cols, rows, out.curRow, len(out.lines))
			}
		}
	}
}

// What the colours say: a layer that came from the cache is bright and one that was paid for is dim, a cache break is red, the
// status line has the harness's violet, and what is saved is green.
func TestChatLiveRegionStyled(t *testing.T) {
	k := newChatLook(Look{Theme: widget.DefaultTheme(), Palette: widget.DefaultPalette(), Unicode: true, Anim: false})
	fx := liveFixtures(t, k, 100, 30)
	pal := widget.DefaultPalette()
	names := map[cell.Color]string{pal.Good: "good", pal.Bad: "bad", pal.Warn: "warn", pal.Info: "info", pal.Accent: "accent", pal.Dim: "dim", pal.Faint: "faint"}
	for i, c := range pal.LayerColors {
		names[c] = fmt.Sprintf("G%d", i)
	}
	out := k.liveLines(ptr(fx["busy"]))
	got := widgettest.FlattenStyledWith(out.lines, names)
	widgettest.Golden(t, *update, "testdata/chat-live-busy-100-styled.txt", got)

	// the properties the golden file shows, stated
	var bar cell.Line
	for _, l := range out.lines {
		if strings.HasPrefix(l.Plain(), "prompt ") {
			bar = l
		}
	}
	if bar == nil {
		t.Fatal("no stack bar in the live region")
	}
	var bright, dim bool
	for _, sp := range bar {
		if strings.Contains(sp.Text, "█") && sp.Style.FG != (cell.Color{}) && !sp.Style.Has(cell.Dim) {
			bright = true
		}
		if strings.Contains(sp.Text, "░") && sp.Style.Has(cell.Dim) {
			dim = true
		}
	}
	if !bright || !dim {
		t.Errorf("the bar has a bright cached part (%v) and a dim paid one (%v):\n%s", bright, dim, widgettest.FlattenStyledWith([]cell.Line{bar}, names))
	}
	if !anyStyled(out.lines, func(sp cell.Span) bool { return strings.Contains(sp.Text, "saved") && sp.Style.FG == pal.Good }) {
		t.Errorf("what the cache saved is green:\n%s", got)
	}
	if !anyStyled(out.lines, func(sp cell.Span) bool {
		return strings.Contains(sp.Text, "Thinking") || strings.Contains(sp.Text, "…") && sp.Style.FG == pal.Accent
	}) {
		t.Errorf("the status line is violet:\n%s", got)
	}
}

func ptr[T any](v T) *T { return &v }
