package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/term"
)

// The golden screens (testdata/*.screen) pin what a user sees: the emulator's screen after a scripted sequence, as plain text with
// the cursor. A changed cell fails one named test; -update rewrites them (go test ./internal/tui/render -run Golden -update),
// after which the diff of testdata/ is what to read.

var (
	violet = cell.Style{}.Fg(cell.Hex("#bb9af7")).With(cell.Bold)
	green  = cell.Style{}.Fg(cell.Hex("#9ece6a"))
	red    = cell.Style{}.Fg(cell.Hex("#f7768e"))
	dim    = cell.Style{}.With(cell.Dim)
	bar    = cell.Style{}.Bg(cell.Hex("#33467c"))
)

// chatHistory prints a small chat session: a banner, what was typed, the assistant's answer with a line that needs wrapping, and a
// tool line with its result.
func chatHistory(h *harness) {
	h.r.Print(cell.Styled(violet, "sleipnir 0.1"), cell.Styled(dim, "a coding-agent harness for swarms"), txt(""))
	h.r.Print(cell.Join(cell.Styled(green, "> "), txt("why does the cache miss after the compaction?")), txt(""))
	h.r.Print(txt("The compaction rewrites the thread, which is a declared rebase: every byte after the first changed one is written again at the cache-write price, once, for each agent that shares the prefix."), txt(""))
	h.r.Print(cell.Join(cell.Styled(green, "● "), cell.Styled(cell.Style{}.With(cell.Bold), "Bash "), txt("go test ./internal/kv  "), cell.Styled(green, "✓ 1.4s")))
	h.r.Print(cell.Join(cell.Styled(dim, "  ⎿  "), txt("ok  \tgithub.com/reee344/sleipnir/internal/kv\t1.402s")))
}

// chatLive lays the live region out for a terminal of the given width, as a widget would: a status line, a stack bar, a blank
// row, the input box and a footer, each cut to fit. The input is on row 3, and col is where the cursor rests in it.
func chatLive(width int, status string) (lines []cell.Line, col int) {
	fit := func(l cell.Line) cell.Line { return l.Truncate(width, "…") }
	input := cell.Join(cell.Styled(green, "> "), txt("and what does it cost?"))
	footer := cell.Join(cell.Styled(dim, "? for shortcuts"), cell.Spaces(max(width-15-12, 1), cell.Style{}), cell.Styled(dim, "default mode"))
	return []cell.Line{
		fit(cell.Join(cell.Styled(violet, "⠹ "), txt(status), cell.Styled(dim, "  esc to interrupt"))),
		fit(cell.Join(cell.Styled(cell.Style{}.Bg(cell.Hex("#6366f1")), "  G0  "), cell.Styled(cell.Style{}.Bg(cell.Hex("#7aa2f7")), " G1 "), cell.Styled(cell.Style{}.Bg(cell.Hex("#9ece6a")), " G2 "), cell.Styled(bar, "  ▏hit 93%  "))),
		nil,
		fit(input),
		fit(footer),
	}, 2 + len("and what does it cost?")
}

func chat(h *harness, status string) {
	chatHistory(h)
	cols, _ := h.v.Size()
	live, col := chatLive(cols, status)
	h.r.SetLive(live)
	h.r.SetCursor(3, col)
	h.flush()
}

func TestGoldenChatInline(t *testing.T) {
	h := newHarness(t, termCaps(56, 16))
	chat(h, "Thinking 12s  ↑1.2k ↓340  $0.01")
	golden(t, "chat_inline", snapshot(h.v))
}

// The terminal gets narrower: the program lays its region out again for the new width and the renderer erases the old one (which
// the terminal has re-wrapped) and draws the new one. The history above has been re-wrapped by the terminal, mid-word: it is
// output the program has already handed over.
func TestGoldenChatInlineNarrow(t *testing.T) {
	h := newHarness(t, termCaps(56, 16))
	chat(h, "Thinking 12s  ↑1.2k ↓340  $0.01")
	h.resize(34, 16)
	live, col := chatLive(34, "Thinking 13s  ↑1.2k ↓340  $0.01")
	h.r.SetLive(live)
	h.r.SetCursor(3, col)
	h.flush()
	golden(t, "chat_inline_narrow", snapshot(h.v))
}

func TestGoldenChatInlineWider(t *testing.T) {
	h := newHarness(t, termCaps(60, 14))
	chat(h, "Thinking 12s")
	h.resize(90, 14)
	live, col := chatLive(90, "Thinking 12s  ↑1.2k ↓340  $0.012  saved ≈ $0.08")
	h.r.SetLive(live)
	h.r.SetCursor(3, col)
	h.flush()
	golden(t, "chat_inline_wider", snapshot(h.v))
}

// A live region whose lines are wider than the terminal is wrapped by the renderer, which counts the rows it made.
func TestGoldenOverwideLiveRegionIsWrapped(t *testing.T) {
	h := newHarness(t, termCaps(30, 12))
	h.r.Print(txt("history"))
	h.r.SetLive([]cell.Line{
		cell.Join(cell.Styled(violet, "⠹ "), txt("Thinking 12s  ↑1.2k ↓340  $0.012  saved ≈ $0.08"), cell.Styled(dim, "  esc to interrupt")),
		txt("    an indented line that wraps and keeps its indentation"),
		cell.Join(cell.Styled(green, "> "), txt("input")),
	})
	h.r.SetCursor(99, 7) // the last row, wherever the wrapping put it
	h.flush()
	golden(t, "overwide_live", snapshot(h.v))
}

func TestGoldenChatCloseErasesTheRegion(t *testing.T) {
	h := newHarness(t, termCaps(56, 16))
	chat(h, "done")
	h.r.Close()
	golden(t, "chat_close_erase", snapshot(h.v))
}

func TestGoldenChatCloseKeepsTheRegion(t *testing.T) {
	h := newHarness(t, termCaps(56, 16), KeepLive())
	chat(h, "done")
	h.r.Close()
	golden(t, "chat_close_keeplive", snapshot(h.v))
}

func TestGoldenWideRunesMarksAndFullRows(t *testing.T) {
	h := newHarness(t, termCaps(20, 14))
	h.r.Print(
		txt("中文字符测试 ok"),
		txt("こんにちは世界 こんにちは世界"),
		txt("e\u0301a\u030ao\u0308 combining"),
		txt("exactly twenty cells"),
		txt("a\U0001f40eb \U0001f600 emoji row"),
		txt("한국어 텍스트가 한 줄보다 깁니다"),
	)
	h.r.SetLive([]cell.Line{
		txt("中中中中中中中中中中"),
		txt("e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301"),
		txt("0123456789012345678中"),
	})
	h.r.SetCursor(2, 4)
	h.flush()
	golden(t, "wide_runes", snapshot(h.v))
}

func TestGoldenHostileTextIsShownAsTextOnly(t *testing.T) {
	h := newHarness(t, termCaps(50, 14))
	h.r.Print(txt("sentinel: this line must survive"))
	for _, c := range hostile {
		switch {
		case strings.Contains(c.name, "unterminated"), strings.Contains(c.name, "invalid"), strings.Contains(c.name, "combining"), strings.Contains(c.name, "stray"), c.name == "only an escape", strings.Contains(c.name, "does not start"):
			continue // their meaning is at the edge of the line, which the framing below would hide
		}
		h.r.Print(txt(fmt.Sprintf("%-28.28s|%s|", c.name, c.in)))
	}
	h.r.SetLive(txts("live region: hostile text in here \x1b[2J\x1b]52;c;QQ==\x07 too"))
	h.flush()
	golden(t, "hostile", snapshot(h.v))
}

func TestGoldenFullScreenDashboard(t *testing.T) {
	sh := newScreenHarness(t, termCaps(44, 12))
	if err := sh.s.Enter(); err != nil {
		t.Fatal(err)
	}
	title := cell.Style{}.With(cell.Reverse | cell.Bold)
	lines := []cell.Line{
		cell.Styled(title, "sleipnir watch                     3 agents").Pad(44, title),
		txt(""),
		cell.Join(cell.Styled(dim, "AGENT   ROLE      TOOL        TOK   HIT")),
		cell.Join(cell.Styled(green, "●"), txt(" be-1    backend   edit      12.4k  93%")),
		cell.Join(cell.Styled(green, "●"), txt(" fe-1    frontend  bash       8.1k  91%")),
		cell.Join(cell.Styled(red, "⚠"), txt(" rv-1    reviewer  stuck      2.0k   0%")),
		txt(""),
		cell.Join(cell.Styled(dim, "MERGE QUEUE  "), txt("T3 verify  T4 rebase")),
		txt("中文 and wide cells 日本語"),
	}
	if err := sh.s.DrawLines(lines); err != nil {
		t.Fatal(err)
	}
	golden(t, "screen_dashboard", snapshot(sh.v))

	// A frame later: one row changes, everything else stays.
	lines[3] = cell.Join(cell.Styled(green, "●"), txt(" be-1    backend   bash      12.9k  94%"))
	sh.s.DrawLines(lines)
	golden(t, "screen_dashboard_next", snapshot(sh.v))

	sh.s.Leave()
	golden(t, "screen_dashboard_left", snapshot(sh.v))
}

func TestGoldenColourDepthDoesNotChangeTheText(t *testing.T) {
	// The same chat at every colour depth is the same screen: colour is never what carries the text.
	var shots []string
	for _, d := range []term.ColorDepth{term.ColorTrueColor, term.ColorANSI256, term.ColorANSI16} {
		c := termCaps(56, 16)
		c.Color = d
		h := newHarness(t, c)
		chat(h, "Thinking")
		shots = append(shots, snapshot(h.v))
	}
	if shots[0] != shots[1] || shots[0] != shots[2] {
		t.Error("the text and the cursor must not depend on the colour depth")
	}
}
