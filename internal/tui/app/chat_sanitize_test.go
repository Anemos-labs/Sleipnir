package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/input"
	"github.com/reee344/sleipnir/internal/tui/state"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/widgettest"
)

// Everything the chat prints that somebody else wrote (a model, a tool, a file, a web page, another agent, the person's own paste) is
// data, and no byte of it may reach the terminal as an escape sequence. Two checks say so. The first is on the lines: every block the
// chat makes of hostile text is free of anything a terminal would act on, for every look, whether or not the renderer would have
// cleaned it again. The second is on the screen: the program is run on a terminal whose every frame is checked for what it
// contains (chat_rig_test.go: only the sequences the renderer composes, nothing else below a space), with hostile text sent through
// every door the session has.

func hostileTexts() []string {
	return append(append([]string(nil), widgettest.Hostile()...), widgettest.Unicode()...)
}

func looks() map[string]*chatLook {
	return map[string]*chatLook{
		"unicode": newChatLook(Look{Theme: widget.DefaultTheme(), Palette: widget.DefaultPalette(), Unicode: true, Anim: true}),
		"ascii":   newChatLook(Look{Theme: widget.DefaultTheme().WithASCII(true), Palette: widget.DefaultPalette(), Unicode: false, Anim: false}),
		"mono":    newChatLook(Look{Theme: widget.MonoTheme(), Palette: widget.MonoPalette(), Unicode: true, Anim: false}),
	}
}

// blocksOf is every block of the scrollback and every row of the live region that can be made of the text h, at width w.
func blocksOf(k *chatLook, h string, w int) map[string][]cell.Line {
	out := map[string][]cell.Line{}
	out["banner"] = k.bannerLines(ChatInfo{Version: h, Model: h, Cwd: h, SessionID: h, Budget: h, Resumed: h}, w)
	out["prompt"] = k.promptLines(h, w)
	for _, level := range []string{"warn", "error", "info", ""} {
		out["notice/"+level] = k.noticeLines(h, level, h, w, false)
		out["notice main/"+level] = k.noticeLines("", level, h, w, true)
	}
	in := mustJSON(map[string]any{"command": h, "path": h, "file_path": h, "pattern": h, "url": h, "content": h, "old_string": h, "new_string": h,
		"patch": "*** Begin Patch\n*** Update File: " + h + "\n" + h + "\n+" + h + "\n*** End Patch", "edits": []any{1, 2}})
	for _, name := range []string{"bash", "edit", "write", "apply_patch", "read", "grep", "web_fetch", "other", h, "mcp__" + h} {
		for _, failed := range []bool{false, true} {
			for _, worker := range []bool{false, true} {
				res := toolResult{Text: h + "\n" + h + "\n[exit code 1]", Failed: failed, IsError: failed, Outcome: h, Meta: map[string]any{"diff": h, "created": true, "lines": 3, "matches": 2, "added": 1, "removed": 2}}
				lines, exp := k.toolLines(doneTool{agent: h, call: core.Block{ToolID: "c", ToolName: name, Input: in}, res: res, took: 1400 * time.Millisecond, cwd: h, worker: worker}, w)
				key := fmt.Sprintf("tool %q failed=%v worker=%v", name, failed, worker)
				out[key] = lines
				if exp != nil {
					out[key+" expanded"] = append([]cell.Line{cell.Text(exp.title)}, textRows(exp.lines)...)
				}
			}
		}
	}
	out["fold"] = k.foldRecord(state.Compaction{Agent: h, Mode: h, Reason: h, Before: 31000, After: 2000, Moment: h, SpineAdded: 1}, h, w)
	out["anomaly"] = k.anomalyLines(state.Anomaly{Agent: h, Kind: h, Layer: h, Note: h, Expected: 9000, Actual: 3000, MissKnown: true, MissUSD: 0.5}, h, w)
	for _, tool := range []string{"bash", "edit", "write", "apply_patch", "web_fetch", "mcp-server", h} {
		req := perm.Request{Agent: h, Tool: tool, Command: h, Summary: h + " [" + h + "]", Paths: []string{h}, Cwd: h}
		call := &toolRun{name: tool, input: in}
		title, body := k.requestBody(req, call, max(w-4, 1), h)
		out["question/"+tool+" title"] = []cell.Line{cell.Text(title)}
		out["question/"+tool] = body
		opts, _ := dialogOptions(req)
		out["dialog/"+tool] = k.dialogLines(&dialogView{title: title, body: body, options: opts, armed: true}, w)
		out["dialog hint"] = []cell.Line{k.dialogHint(&dialogView{armed: false}, w), k.dialogHint(&dialogView{armed: true}, w)}
	}
	th := k.inputTheme()
	ed := input.NewEditor(input.Options{Prompt: k.g.prompt + " ", Placeholder: h, Theme: &th})
	ed.SetWidth(max(w-4, 1))
	ed.Handle(input.PasteKey(h))
	lv := liveView{cols: w, rows: 24, status: statusView{kind: statusTool, detail: h, elapsed: 3 * time.Second, tokIn: 1000, tokOut: 20, cost: 0.01, saved: 0.5},
		tools: []toolView{{agent: h, title: h, summary: h, worker: true, elapsed: 2 * time.Second}, {title: h, summary: h, asking: true}},
		fold:  &foldView{who: h, before: 31000, after: 2000, progress: 0.4},
		queue: []string{h, "(quit when this is done)"},
		mode:  h, model: h, session: h, hint: h, editorOn: true,
		ed:   ed.View(max(w-4, 1)),
		tail: k.answerLines(widget.Markdown(h, max(w-2, 1), k.Theme), 0)}
	out["live"] = k.liveLines(&lv).lines
	lv.dlg = &dialogView{title: h, body: []cell.Line{cell.Text(h)}, options: nil, armed: true}
	lv.dlg.options, _ = dialogOptions(perm.Request{Tool: "bash"})
	out["live with a question"] = k.liveLines(&lv).lines
	return out
}

func textRows(ss []string) []cell.Line {
	out := make([]cell.Line, len(ss))
	for i, s := range ss {
		out[i] = cell.Text(s)
	}
	return out
}

func TestChatLinesCarryNothingThatATerminalWouldActOn(t *testing.T) {
	for name, k := range looks() {
		for _, h := range hostileTexts() {
			for _, w := range []int{1, 7, 40, 100} {
				for what, lines := range blocksOf(k, h, w) {
					if where, found := widgettest.Control(lines); found {
						t.Fatalf("%s look, width %d, %s of %q: %s", name, w, what, h, where)
					}
					for i, l := range lines {
						// a row never overflows a window that has room for a line's lead (an agent's tag takes 16 cells at most)
						if w >= 40 && l.Width() > w && !strings.HasSuffix(what, " title") && !strings.HasPrefix(what, "question/") { // the box cuts a title and wraps a body
							t.Fatalf("%s look, width %d, %s of %q: row %d is %d cells wide: %q", name, w, what, h, i, l.Width(), l.Plain())
						}
					}
				}
			}
		}
	}
}

func FuzzChatLines(f *testing.F) {
	for i, h := range hostileTexts() {
		f.Add(h, uint8(40), uint8(i))
	}
	f.Add("", uint8(1), uint8(0))
	f.Add("\x1b]52;c;QQ==\x07\x1b[2J", uint8(5), uint8(1))
	all := []*chatLook{}
	for _, k := range looks() {
		all = append(all, k)
	}
	f.Fuzz(func(t *testing.T, h string, width uint8, which uint8) {
		if len(h) > 2000 {
			t.Skip("long inputs only make it slow")
		}
		k := all[int(which)%len(all)]
		w := 1 + int(width)%130
		for what, lines := range blocksOf(k, h, w) {
			if where, found := widgettest.Control(lines); found {
				t.Fatalf("%s of %q at width %d: %s", what, h, w, where)
			}
		}
	})
}

// The same text through every door of the session, on the terminal whose frames are all checked.
func TestChatHostileTextNeverReachesTheTerminalAsAnEscape(t *testing.T) {
	for _, h := range hostileTexts() {
		t.Run("", func(t *testing.T) {
			r := startChat(t, rigOpts{cols: 90, rows: 30, verbose: true})
			r.press(input.PasteKey(h)) // the person's own paste
			r.ctrl('u')
			call := toolCall("c1", h, map[string]any{"command": h, "path": h, "pattern": h})
			edit := toolCall("c2", "edit", map[string]any{"path": h, "old_string": h, "new_string": h})
			bash := toolCall("c3", "bash", map[string]any{"command": h})
			asked := make(chan struct{})
			ans := make(chan perm.Decision, 1)
			r.host.turn = func(ctx context.Context, goal string) TurnResult {
				r.sink.Text("main", h+"\n\n# "+h+"\n\n- "+h+"\n\n```\n"+h+"\n```\n\n| "+h+" | b |\n|---|---|\n| "+h+" | "+h+" |\n\n["+h+"]("+h+")\n")
				r.sink.Notice(h, "warn", h)
				r.sink.Notice("main", "info", h)
				r.sink.ToolStart(h, call)
				r.sink.ToolStart("main", edit)
				r.sink.ToolStart("main", bash)
				r.sink.ToolEnd(h, call, okResult(h+"\n"+h, map[string]any{"lines": 3}), time.Second)
				r.sink.ToolEnd("main", edit, okResult(h, map[string]any{"diff": "@@ -1,2 +1,2 @@\n-" + h + "\n+" + h + "\n " + h, "added": 1, "removed": 1}), time.Second)
				r.sink.ToolEnd("main", bash, okResult(h+"\n"+strings.Repeat(h+"\n", 12)+"[exit code 3]", map[string]any{"exit_code": 3}), time.Second)
				go func() {
					ans <- r.prompt(ctx, perm.Request{Agent: h, Tool: "bash", Command: h, Summary: h + " [" + h + "]", Paths: []string{h}, Cwd: h})
				}()
				close(asked)
				<-ctx.Done()
				return TurnResult{Err: ctx.Err()}
			}
			r.submit("go")
			<-asked
			r.shows("1. Yes")
			r.ctrl('o')
			b := statetest.NewBuilder()
			r.at(b.Now().Add(time.Minute))
			evs := []events.Event{b.Emit("", events.TypeSessionStart, map[string]any{"model": h, "version": h})}
			evs = append(evs, b.Request(h, "r1", h, "pk", statetest.Sec{Name: h, Tokens: 100, BP: true}), b.Response(h, "r1", h, 100, 0, 0, 10, 0.01),
				b.Emit(h, events.TypeCacheAnomaly, map[string]any{"kind": h, "diverged": h, "req": "r1", "expected_read": 10, "actual_read": 1, "missed": 9, "note": h}),
				b.Emit(h, events.TypeCompactCommit, map[string]any{"reason": h, "removed_turns": 1, "removed_tokens": 100, "retained_tokens": 10, "snap_tokens": 110}))
			r.emit(evs...)
			r.ctrl('t')
			r.typeText("/")
			r.ctrl('c') // cancels the turn and the question
			r.shows("(cancelled)")
			r.host.turn = nil
			r.press(input.PasteKey("/marker " + h)) // a command with the text in it: what it prints comes back through the command's own writer
			r.enter()
			r.shows("marker")
			if len(r.bridge.v.Rejected) != 0 {
				t.Fatalf("the terminal was told %q", r.bridge.v.Rejected)
			}
			if r.bridge.v.AltScreen() {
				t.Fatal("the alternate screen was entered")
			}
		})
	}
}
