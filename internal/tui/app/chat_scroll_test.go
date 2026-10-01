package app

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// The blocks of the scrollback and the small functions they are made of.

func plainLines(ls []cell.Line) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Plain()
	}
	return out
}

func TestToolTitle(t *testing.T) {
	for in, want := range map[string]string{
		"bash": "Bash", "Read": "Read", "web_fetch": "Web fetch", "apply_patch": "Apply patch", "": "tool", "  ": "tool",
		"mcp__git__log": "mcp__git__log", "x-y": "x-y", "a.b": "a.b",
	} {
		if got := toolTitle(in); got != want {
			t.Errorf("toolTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolSummary(t *testing.T) {
	cwd := "/work/proj"
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"bash", `{"command":"go test ./..."}`, "go test ./..."},
		{"bash", `{"command":"sleep 5","run_in_background":true}`, "sleep 5  (in the background)"},
		{"bash", `{"command":"echo a\necho b"}`, "echo a …"},
		{"read", `{"path":"/work/proj/orders/list.go"}`, "orders/list.go"},
		{"read", `{"file_path":"/elsewhere/x.go"}`, "/elsewhere/x.go"},
		{"edit", `{"path":"/work/proj/a.go","edits":[{},{}]}`, "a.go (2 edits)"},
		{"edit", `{"path":"/work/proj/a.go","edits":[{}]}`, "a.go"},
		{"grep", `{"pattern":"TODO","path":"/work/proj/src"}`, "TODO  in src"},
		{"grep", `{"pattern":"TODO"}`, "TODO"},
		{"web_fetch", `{"url":"https://example.com/x"}`, "https://example.com/x"},
		{"web_search", `{"query":"go generics"}`, "go generics"},
		{"bash", `{}`, ""},
		{"bash", `not json`, ""},
		{"bash", ``, ""},
		{"bash", `{"command":"   "}`, ""},
		{"x", `{"unrelated":"a"}`, ""},
	} {
		if got := toolSummary(tc.name, []byte(tc.input), cwd); got != tc.want {
			t.Errorf("toolSummary(%s %s) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}

func TestKindOfAndRelPath(t *testing.T) {
	for name, want := range map[string]toolKind{"bash": kindCommand, "BASH": kindCommand, "bash_output": kindCommand, "read": kindLook, "grep": kindLook, "edit": kindEdit,
		"write": kindWrite, "apply_patch": kindPatch, "mcp__x__y": kindOther, "": kindOther} {
		if got := kindOf(name); got != want {
			t.Errorf("kindOf(%q) = %d, want %d", name, got, want)
		}
	}
	for _, tc := range []struct{ dir, path, want string }{
		{"/w", "/w/a/b.go", "a/b.go"}, {"/w", "/x/y", "/x/y"}, {"", "/w/a", "/w/a"}, {"/w/", "/w/a", "a"}, {"/w", "/w", "/w"}, {"/w", "/wx/a", "/wx/a"}, {"/w", "  /w/a  ", "a"},
	} {
		if got := relPath(tc.dir, tc.path); got != tc.want {
			t.Errorf("relPath(%q, %q) = %q, want %q", tc.dir, tc.path, got, tc.want)
		}
	}
}

func TestResultLinesLeavesOffTheExitLineOfACommandOnly(t *testing.T) {
	if got, want := resultLines("out\n[exit code 1]\n\n", kindCommand), []string{"out"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a command's result is %q, want %q", got, want)
	}
	if got, want := resultLines("out\n[exit code 1]\n\n", kindOther), []string{"out", "[exit code 1]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("another tool's result keeps its words: %q, want %q", got, want)
	}
	if got := resultLines("\n\n", kindCommand); len(got) != 0 {
		t.Errorf("nothing is nothing: %q", got)
	}
	if got, want := resultLines("a\tb\r\nc\x1b[2J", kindOther), []string{"a   b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tabs have a width and escapes are gone: %q, want %q", got, want)
	}
}

func TestLongOutputIsCollapsedAboveNineLines(t *testing.T) {
	k := goldenLook(true)
	mk := func(n int) []string {
		var ls []string
		for i := 1; i <= n; i++ {
			ls = append(ls, fmt.Sprintf("line %d", i))
		}
		return ls
	}
	call := core.Block{ToolName: "bash", Input: []byte(`{"command":"seq"}`)}
	for _, n := range []int{1, 8, 9, 10, 11, 40} {
		res := toolResult{Text: strings.Join(mk(n), "\n") + "\n[exit code 0]"}
		lines, exp := k.toolLines(doneTool{call: call, res: res, took: time.Second}, 60)
		got := plainLines(lines[1:])
		if n <= collapseAbove {
			if len(got) != n || exp != nil {
				t.Errorf("%d lines are shown whole: %q (expandable: %v)", n, got, exp != nil)
			}
			continue
		}
		want := []string{"  ⎿ line 1", "    line 2", "    line 3", fmt.Sprintf("    … +%d lines (ctrl+o to expand)", n-6), fmt.Sprintf("    line %d", n-2), fmt.Sprintf("    line %d", n-1), fmt.Sprintf("    line %d", n)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%d lines are shown as their head and tail:\n got %q\nwant %q", n, got, want)
		}
		if exp == nil || len(exp.lines) != n || exp.title != "Bash seq" {
			t.Errorf("ctrl+o can bring back all %d lines: %+v", n, exp)
		}
	}
}

func TestAFailedToolIsMarkedAndKeepsItsOutput(t *testing.T) {
	k := goldenLook(true)
	call := core.Block{ToolName: "write", Input: []byte(`{"path":"/work/proj/x.txt","content":"c"}`)}
	lines, _ := k.toolLines(doneTool{call: call, cwd: "/work/proj", res: toolResult{Text: "permission denied: x.txt", Failed: true, IsError: true, Outcome: "denied"}, took: 40 * time.Millisecond}, 70)
	got := plainLines(lines)
	if len(got) != 2 || got[0] != "● Write x.txt  ✗ 40ms · denied" || got[1] != "  ⎿ permission denied: x.txt" {
		t.Errorf("a refusal is a cross, says how it ended, and its words are under it: %q", got)
	}
}

func TestAWorkersToolIsOneLine(t *testing.T) {
	k := goldenLook(true)
	call := core.Block{ToolName: "bash", Input: []byte(`{"command":"go test ./..."}`)}
	lines, exp := k.toolLines(doneTool{agent: "be-2", worker: true, call: call, res: toolResult{Text: "a\nb\nc"}, took: time.Second}, 70)
	if got := plainLines(lines); len(got) != 1 || got[0] != "[be-2] ● Bash go test ./...  ✓ 1.0s" || exp != nil {
		t.Errorf("a worker's call is one line that says whose it is: %q", got)
	}
}

func TestPatchToUnified(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: new.txt\n+hello\n+world\n*** Update File: a.go\n@@\n-old\n+new\n*** Delete File: gone.go\n*** End Patch"
	want := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,2 @@\n+hello\n+world\n--- a/a.go\n+++ b/a.go\n@@\n-old\n+new\n--- a/gone.go\n+++ /dev/null\n"
	if got := patchToUnified(patch); got != want {
		t.Errorf("the patch as a diff:\n%q\nwant\n%q", got, want)
	}
	if got := patchToUnified("nothing the format knows"); got != "" {
		t.Errorf("a patch that is not one is no diff: %q", got)
	}
	if got := patchToUnified("*** Begin Patch\n*** Update File: a\n*** Move to: b\n*** End Patch"); !strings.Contains(got, "rename to b") {
		t.Errorf("a move is said: %q", got)
	}
}

func TestFoldCellsShrinkAndSettle(t *testing.T) {
	if got := foldCells(0, 0, 0.5, foldWidth); got != 0 {
		t.Errorf("no thread, no cells: %d", got)
	}
	if got := foldCells(100, 10, 0.5, 0); got != 0 {
		t.Errorf("no room, no cells: %d", got)
	}
	prev := foldWidth + 1
	for i := 0; i <= 20; i++ {
		p := float64(i) / 20
		c := foldCells(31200, 2400, p, foldWidth)
		if c > prev || c < 1 || c > foldWidth {
			t.Fatalf("at %.2f the thread is %d cells, after %d", p, c, prev)
		}
		prev = c
	}
	if got := foldCells(31200, 2400, 0, foldWidth); got != foldWidth {
		t.Errorf("the fold starts at the full width: %d", got)
	}
	if got := foldTokens(31200, 2400, 0); got != 31200 {
		t.Errorf("it starts at what the thread was: %d", got)
	}
	if got := foldTokens(31200, 2400, 1); got != 2400 {
		t.Errorf("it ends at what the thread is: %d", got)
	}
	if got := foldTokens(100, 500, 1); got != 100 {
		t.Errorf("a thread does not grow in a compaction: %d", got)
	}
}

func TestPromptLinesCutAVeryLongPrompt(t *testing.T) {
	k := goldenLook(true)
	var ls []string
	for i := 1; i <= 20; i++ {
		ls = append(ls, fmt.Sprintf("pasted %d", i))
	}
	got := plainLines(k.promptLines(strings.Join(ls, "\n"), 40))
	if len(got) != 13 || got[0] != "❯ pasted 1" || got[1] != "  pasted 2" || got[12] != "  … +8 lines" {
		t.Errorf("a prompt of 20 lines is 12 of them and a count: %q", got)
	}
	if got := k.promptLines("", 40); got != nil {
		t.Errorf("nothing was said: %q", got)
	}
	got = plainLines(k.promptLines("one two three four five six seven eight nine ten", 20))
	if len(got) < 3 || got[0] != "❯ one two three four" || !strings.HasPrefix(got[1], "  ") {
		t.Errorf("a long line wraps under its first word: %q", got)
	}
}

func TestNoticeLinesHangUnderTheirSign(t *testing.T) {
	k := goldenLook(true)
	got := plainLines(k.noticeLines("be-2", "warn", "a rather long warning that has to go over more than one line of the window", 30, false))
	if len(got) < 3 || !strings.HasPrefix(got[0], "  ⚠ [be-2] ") || !strings.HasPrefix(got[1], "           ") {
		t.Errorf("a notice of a worker says whose, and hangs: %q", got)
	}
	got = plainLines(k.noticeLines("main", "error", "boom", 30, true))
	if !reflect.DeepEqual(got, []string{"  ✗ boom"}) {
		t.Errorf("an error of the main agent: %q", got)
	}
	got = plainLines(k.noticeLines("", "info", "line one\nline two", 30, true))
	if !reflect.DeepEqual(got, []string{"  · line one", "    line two"}) {
		t.Errorf("a notice of two lines: %q", got)
	}
}

func TestTurnSummaryIsOneLine(t *testing.T) {
	k := goldenLook(true)
	res := TurnResult{Steps: 3, CostUSD: 0.0123, HitRatio: 0.5}
	got := k.turnSummary(1400*time.Millisecond, res, 0.31, 100).Plain()
	for _, want := range []string{"──", duration(time.Second), "3 steps", widget.USD(0.0123), "cache hit " + widget.Percent(0.5), "saved ≈ " + widget.USD(0.31)} {
		if !strings.Contains(got, want) {
			t.Errorf("the summary %q lacks %q", got, want)
		}
	}
	if got := k.turnSummary(time.Second, TurnResult{Steps: 1}, 0, 100).Plain(); strings.Contains(got, "saved") || !strings.Contains(got, "1 step") || strings.Contains(got, "1 steps") {
		t.Errorf("nothing saved, nothing said: %q", got)
	}
	if w := k.turnSummary(time.Hour, res, 1, 20).Width(); w > 20 {
		t.Errorf("the summary is cut to the window: %d cells", w)
	}
}

// What is printed of a message that streams is the same as what is printed of it whole: line i is always the line i.
func TestAnswerLinesJoinUpWhereverTheyAreCut(t *testing.T) {
	k := goldenLook(true)
	md := widget.Markdown("# Title\n\nFirst paragraph with `code`.\n\n- one\n- two\n\nLast.", 60, k.Theme)
	whole := plainLines(k.answerLines(md, 0))
	if !strings.HasPrefix(whole[0], "● Title") || !strings.HasPrefix(whole[2], "  First") {
		t.Fatalf("the bullet is on the first line and the rest hang under it: %q", whole)
	}
	for from := 0; from <= len(md)+1; from++ {
		got := plainLines(k.answerLines(md, from))
		want := whole[min(from, len(whole)):]
		if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("from %d: %q, want %q", from, got, want)
		}
	}
}

func TestASCIIGlyphsKeepTheirCells(t *testing.T) {
	l := cell.Join(cell.Text("█▓▒░▏⚠…◕▰▱▶·◆↻▁▂▃▄▅▆▇ plain"))
	got := asciiLine(l)
	if got.Width() != l.Width() {
		t.Errorf("the line is %d cells wide after, %d before", got.Width(), l.Width())
	}
	for _, r := range got.Plain() {
		if r > 0x7e {
			t.Errorf("%q is not ASCII", r)
		}
	}
	if !strings.HasSuffix(got.Plain(), " plain") {
		t.Errorf("text is left alone: %q", got.Plain())
	}
}

func TestAnAgentTagIsCutToSixteenCells(t *testing.T) {
	k := goldenLook(true)
	if got := k.agentTag("be-2"); got != "be-2" {
		t.Errorf("a short name is as it is: %q", got)
	}
	got := k.agentTag(strings.Repeat("x", 40))
	if cell.StringWidth(got) != 16 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long name is cut with a mark: %q", got)
	}
	if got := goldenLook(false).agentTag(strings.Repeat("x", 40)); !strings.HasSuffix(got, "...") || cell.StringWidth(got) != 16 {
		t.Errorf("in ASCII the mark is three dots: %q", got)
	}
}

func TestTextLinesTabsAndEscapes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a\nb\n\n", []string{"a", "b"}},
		{"\tx", []string{"    x"}},
		{"ab\tx", []string{"ab  x"}},
		{"a   \nb  ", []string{"a", "b"}},
		{"a\r\nb\rc", []string{"a", "b", "c"}},
		{"\x1b[31mred\x1b[0m", []string{"red"}},
		{"日\tx", []string{"日  x"}},
	} {
		if got := textLines(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("textLines(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := firstLine("one\ntwo", 20, "…"); got != "one …" {
		t.Errorf("firstLine: %q", got)
	}
	if got := firstLine("one two three", 6, "…"); got != "one t…" {
		t.Errorf("firstLine cut: %q", got)
	}
	if got := cutCells("日本語", 5, "…"); cell.StringWidth(got) > 5 || !strings.HasSuffix(got, "…") {
		t.Errorf("cutCells: %q", got)
	}
	if got := cutCells("abc", 0, "…"); got != "" {
		t.Errorf("cutCells to nothing: %q", got)
	}
}
