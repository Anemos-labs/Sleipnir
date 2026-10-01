package widget

import (
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

func flatLines(ls []cell.Line) string { return widgettest.Flatten(ls) }

func TestMDStreamStableAndTail(t *testing.T) {
	var s MDStream
	th := MonoTheme()
	steps := []struct {
		add          string
		stable, tail string
	}{
		{"", "", ""},
		{"Hello", "", "Hello"},
		{" world", "", "Hello world"},
		// the next block has begun, but on a line that is not complete: it could still be taken back
		{"\n\nSecond para", "", "Hello world\n\nSecond para"},
		// now the first paragraph is final, and the blank line that follows it is the tail's first line
		{"\n\n- a\n- b", "Hello world\n\nSecond para", "\n• a\n• b"},
		// an open fence: its label and every line that is complete are stable, the line being typed is not
		{"\n\n```go\nx := 1\ny := 2", "Hello world\n\nSecond para\n\n• a\n• b\n\n│ go\n│ x := 1", "│ y := 2"},
		{" + 1", "Hello world\n\nSecond para\n\n• a\n• b\n\n│ go\n│ x := 1", "│ y := 2 + 1"},
		{"\n", "Hello world\n\nSecond para\n\n• a\n• b\n\n│ go\n│ x := 1\n│ y := 2 + 1", ""},
		{"```\n\nEnd", "Hello world\n\nSecond para\n\n• a\n• b\n\n│ go\n│ x := 1\n│ y := 2 + 1", "\nEnd"},
		{"\n", "Hello world\n\nSecond para\n\n• a\n• b\n\n│ go\n│ x := 1\n│ y := 2 + 1", "\nEnd"},
		{"\nmore", "Hello world\n\nSecond para\n\n• a\n• b\n\n│ go\n│ x := 1\n│ y := 2 + 1", "\nEnd\n\nmore"},
	}
	var text strings.Builder
	for i, st := range steps {
		s.Append(st.add)
		text.WriteString(st.add)
		stable, tail := s.Render(40, th)
		if flatLines(stable) != st.stable || flatLines(tail) != st.tail {
			t.Errorf("step %d (+%q):\n stable %q\n tail   %q\nwant\n stable %q\n tail   %q", i, st.add, flatLines(stable), flatLines(tail), st.stable, st.tail)
		}
		if want := Markdown(text.String(), 40, th); !reflect.DeepEqual(append(append([]cell.Line(nil), stable...), tail...), want) {
			t.Errorf("step %d: stable+tail is not Markdown of the text so far", i)
		}
	}
	if s.Len() == 0 {
		t.Error("Len")
	}
}

func TestMDStreamAParagraphBecomesAHeading(t *testing.T) {
	var s MDStream
	th := MonoTheme()
	s.Append("intro\n\nTitle")
	stable, tail := s.Render(30, th)
	if flatLines(stable) != "" || flatLines(tail) != "intro\n\nTitle" {
		t.Fatalf("%q %q", flatLines(stable), flatLines(tail))
	}
	s.Append("\nmore")
	stable, tail = s.Render(30, th)
	if flatLines(stable) != "intro" || flatLines(tail) != "\nTitle more" {
		t.Fatalf("a paragraph goes on: %q %q", flatLines(stable), flatLines(tail))
	}
	before := tail[len(tail)-1][0].Style
	s.Append("\n=====")
	stable2, tail2 := s.Render(30, th)
	if flatLines(stable2) != "intro" || flatLines(tail2) != "\nTitle more" {
		t.Errorf("the setext underline turns the tail into a heading and leaves the stable part alone: %q %q", flatLines(stable2), flatLines(tail2))
	}
	if after := tail2[len(tail2)-1][0].Style; before == after || !after.Has(cell.Bold) {
		t.Error("the tail changed its look: a heading is bold and underlined")
	}
	if !reflect.DeepEqual(stable, stable2) {
		t.Error("stable changed")
	}
}

func TestMDStreamARowBecomesATable(t *testing.T) {
	var s MDStream
	th := MonoTheme()
	s.Append("Results:\na | b")
	_, tail := s.Render(30, th)
	if flatLines(tail) != "Results: a | b" {
		t.Fatalf("%q", flatLines(tail))
	}
	// the row under the header is not complete: the header could still turn out to be text, so nothing is stable
	s.Append("\n--|-")
	stable, tail := s.Render(30, th)
	if flatLines(stable) != "" {
		t.Errorf("a table header whose delimiter row is not complete takes nothing out of the tail: %q | %q", flatLines(stable), flatLines(tail))
	}
	s.Append("x\n")
	stable, tail = s.Render(30, th)
	if flatLines(stable) != "" || !strings.HasPrefix(flatLines(tail), "Results: a | b") {
		t.Errorf("--|-x is no delimiter row, so it is all one paragraph: %q | %q", flatLines(stable), flatLines(tail))
	}
	var t2 MDStream
	t2.Append("Results:\na | b\n--|--\n1 | 2")
	stable, tail = t2.Render(30, th)
	if flatLines(stable) != "Results:" || flatLines(tail) != "\na │ b\n──┼──\n1 │ 2" {
		t.Errorf("a table that starts right after a paragraph line splits the paragraph: %q | %q", flatLines(stable), flatLines(tail))
	}
}

// Only a paragraph can lose the header row of a table (until the row under it is complete, the header is a line of the
// paragraph). Every other block is final as soon as the block after it has begun, and stays final when that block turns
// out to be a table: stable must never shrink. (Found by fuzzing: "# 0\n00|\n" makes the heading stable, and "|-" after it
// must not take that back.)
func TestMDStreamATableHeaderAfterAnotherBlockTakesNothingBack(t *testing.T) {
	th := MonoTheme()
	var s MDStream
	s.Append("# 0\n00|\n")
	before, _ := s.Render(30, th)
	s.Append("|-")
	after, tail := s.Render(30, th)
	if flatLines(before) != "0" || flatLines(after) != "0" || !strings.HasPrefix(flatLines(tail), "\n") {
		t.Errorf("heading, then a header row, then the start of its delimiter row: stable %q then %q, tail %q", flatLines(before), flatLines(after), flatLines(tail))
	}
	for _, first := range []string{"# 0", "---", "```\nx\n```", "    code", "> # q", "- # i", "a\n===", "~~~\n~~~", "> a\n>\n> b", "1. a\n\n2. b"} {
		for _, rest := range []string{"\n00|\n|-", "\n00|\n|-|\n", "\n00|\n|-|\n|1|\n\nend", "\n|a|b|\n|-|-|\n|1|2|\n", "\n00|\n|-x\nt"} {
			doc := first + rest
			var singles []string
			for i := 0; i < len(doc); i++ {
				singles = append(singles, doc[i:i+1])
			}
			for _, w := range []int{10, 40} {
				checkStreamProperties(t, doc, singles, w, th)
			}
		}
	}
}

// A paragraph ends before a table header row only when the block parser makes a table of it. A row that is indented by four
// (code there) or starts with a list marker that cannot interrupt a paragraph is text, however the row under it changes.
// (Found by a random search: the paragraph was final as soon as such a row began, and grew back over it when the delimiter
// row under it was typed out to a different number of cells.)
func TestMDStreamARowThatCannotBeAHeaderDoesNotEndTheParagraph(t *testing.T) {
	for _, doc := range []string{
		"00- [ ] # | a | b |```\n\t\t# |-\r|--|--|-|**",
		"x\n    a | b\n|-|-\n|-|-|-",
		"x\n\ta | b\n|-|-|-",
		"x\n2. a | b\n|-|-|-\n",
		"x\n2) a | b\n|-|-|\n|1|2|",
		"x\n-\n|-|-|",
	} {
		var singles []string
		for i := 0; i < len(doc); i++ {
			singles = append(singles, doc[i:i+1])
		}
		for _, w := range []int{10, 40} {
			checkStreamProperties(t, doc, singles, w, MonoTheme())
		}
	}
}

func TestMDStreamFreezesACodeBlockLineByLine(t *testing.T) {
	var s MDStream
	th := MonoTheme()
	s.Append("```\n")
	stable, tail := s.Render(30, th)
	if len(stable) != 0 || flatLines(tail) != "│" {
		t.Errorf("an open fence with no lines: %q %q", flatLines(stable), flatLines(tail))
	}
	printed := 0
	for i := 0; i < 50; i++ {
		s.Append("line " + strings.Repeat("x", i%10) + "\n")
		stable, tail = s.Render(30, th)
		if len(stable) != i+1 || len(tail) != 0 {
			t.Fatalf("after %d lines: %d stable and %d tail rows (want %d stable, none in the tail)", i+1, len(stable), len(tail), i+1)
		}
		if len(stable) < printed {
			t.Fatal("stable shrank")
		}
		printed = len(stable)
	}
	s.Append("partial")
	stable, tail = s.Render(30, th)
	if len(stable) != 50 || flatLines(tail) != "│ partial" {
		t.Errorf("the line being typed stays in the tail: %d stable, tail %q", len(stable), flatLines(tail))
	}
	// a language label is held back until the fence line is complete: it may still be typed
	var g MDStream
	g.Append("```g")
	stable, tail = g.Render(30, th)
	if len(stable) != 0 || flatLines(tail) != "│ g" {
		t.Errorf("an unfinished fence line is tail: %q %q", flatLines(stable), flatLines(tail))
	}
	g.Append("o\nx")
	stable, tail = g.Render(30, th)
	if flatLines(stable) != "│ go" || flatLines(tail) != "│ x" {
		t.Errorf("the label is stable once its line is complete: %q %q", flatLines(stable), flatLines(tail))
	}
	// a closing fence that is still being typed is not stable: "``" may become "```" or "```x"
	g.Append("\n``")
	stable, tail = g.Render(30, th)
	if flatLines(stable) != "│ go\n│ x" || flatLines(tail) != "│ ``" {
		t.Errorf("a partial fence line: %q %q", flatLines(stable), flatLines(tail))
	}
	g.Append("`x\n")
	stable, tail = g.Render(30, th)
	if flatLines(stable) != "│ go\n│ x\n│ ```x" || flatLines(tail) != "" {
		t.Errorf("```x does not close a fence: %q %q", flatLines(stable), flatLines(tail))
	}
}

func TestMDStreamWithAHighlighterFreezesWholeBlocksOnly(t *testing.T) {
	calls := 0
	hl := func(lang, code string) []cell.Line {
		calls++
		var out []cell.Line
		for _, l := range strings.Split(code, "\n") {
			out = append(out, cell.Text(l))
		}
		return out
	}
	s := MDStream{Options: MarkdownOptions{Highlighter: hl}}
	s.Append("```go\na\nb\n")
	stable, tail := s.Render(30, MonoTheme())
	if len(stable) != 0 || flatLines(tail) != "│ go\n│ a\n│ b" {
		t.Errorf("a highlighter may look at the whole block: nothing is stable early: %q %q", flatLines(stable), flatLines(tail))
	}
	s.Append("```\n\nafter\n")
	stable, tail = s.Render(30, MonoTheme())
	if flatLines(stable) != "│ go\n│ a\n│ b" || flatLines(tail) != "\nafter" {
		t.Errorf("%q %q", flatLines(stable), flatLines(tail))
	}
	// a finished block is rendered once, however often Render is called
	before := calls
	for i := 0; i < 20; i++ {
		s.Render(30, MonoTheme())
	}
	if calls != before {
		t.Errorf("a finished block was rendered again: %d more calls of the highlighter", calls-before)
	}
	// a new width starts over
	s.Render(31, MonoTheme())
	if calls == before {
		t.Error("a new width must render again")
	}
}

func TestMDStreamWidthAndThemeChangeStartOver(t *testing.T) {
	var s MDStream
	s.Append("# A long heading that wraps somewhere\n\nsecond\n\nthird\n")
	st1, _ := s.Render(20, MonoTheme())
	st2, tail2 := s.Render(40, MonoTheme())
	if reflect.DeepEqual(st1, st2) {
		t.Error("the stable lines at another width are laid out again")
	}
	checkLines(t, "stable", st2, 40)
	checkLines(t, "tail", tail2, 40)
	if !reflect.DeepEqual(append(append([]cell.Line(nil), st2...), tail2...), Markdown("# A long heading that wraps somewhere\n\nsecond\n\nthird\n", 40, MonoTheme())) {
		t.Error("after a resize stable+tail is still Markdown")
	}
	st3, _ := s.Render(40, DefaultTheme())
	if reflect.DeepEqual(st2, st3) {
		t.Error("a new theme restyles the stable lines")
	}
	if a, b := s.Render(0, MonoTheme()); a != nil || b != nil {
		t.Error("a width below 1 gives nothing")
	}
}

func TestMDStreamZeroValueResetAndEmptyChunks(t *testing.T) {
	var s MDStream
	if st, tl := s.Render(20, MonoTheme()); st != nil || tl != nil {
		t.Error("an empty stream renders nothing")
	}
	s.Append("")
	s.Append("")
	if st, tl := s.Render(20, MonoTheme()); st != nil || tl != nil || s.Len() != 0 {
		t.Error("empty chunks change nothing")
	}
	s.Append("# one\n\ntwo")
	s.Render(20, MonoTheme())
	s.Reset()
	if s.Len() != 0 {
		t.Error("Reset forgets the text")
	}
	s.Append("three")
	st, tl := s.Render(20, MonoTheme())
	if st != nil || flatLines(tl) != "three" {
		t.Errorf("after Reset: %q %q", flatLines(st), flatLines(tl))
	}
	// whitespace only, and blocks that render to nothing, are not content
	var w MDStream
	w.Append("a\n\n#\n\n")
	st, tl = w.Render(20, MonoTheme())
	if flatLines(st) != "a" || len(tl) != 0 {
		t.Errorf("an empty heading leaves no trace: %q %q", flatLines(st), flatLines(tl))
	}
	w.Append("b")
	st, tl = w.Render(20, MonoTheme())
	if flatLines(st) != "a" || flatLines(tl) != "\nb" {
		t.Errorf("%q %q", flatLines(st), flatLines(tl))
	}
}

// startsWith reports whether now starts with the lines of before.
func startsWith(now, before []cell.Line) bool {
	if len(now) < len(before) {
		return false
	}
	for i := range before {
		if !reflect.DeepEqual(now[i], before[i]) {
			return false
		}
	}
	return true
}

// The heart of it: for any way of cutting the text into chunks, at every step stable+tail is exactly what Markdown makes of the
// text so far, and stable only ever grows by appending.
func checkStreamProperties(t testing.TB, src string, chunks []string, width int, th Theme) {
	t.Helper()
	var s MDStream
	var prev []cell.Line
	var soFar strings.Builder
	for i, c := range chunks {
		s.Append(c)
		soFar.WriteString(c)
		stable, tail := s.Render(width, th)
		got := append(append([]cell.Line(nil), stable...), tail...)
		if want := Markdown(soFar.String(), width, th); !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Fatalf("after chunk %d of %q: stable+tail is not Markdown(text so far)\n got:\n%s\nwant:\n%s\n(text so far %q, width %d)", i, chunks, flatLines(got), flatLines(want), soFar.String(), width)
		}
		if !startsWith(stable, prev) {
			t.Fatalf("after chunk %d of %q: stable is not an extension of the stable before it\n before:\n%s\n now:\n%s", i, chunks, flatLines(prev), flatLines(stable))
		}
		if again, tailAgain := s.Render(width, th); !reflect.DeepEqual(again, stable) || !reflect.DeepEqual(tailAgain, tail) {
			t.Fatalf("rendering twice gives different results")
		}
		checkLines(t, "stream", stable, width)
		checkLines(t, "stream tail", tail, width)
		prev = stable
	}
	if want := Markdown(src, width, th); len(chunks) > 0 && !reflect.DeepEqual(append(append([]cell.Line(nil), prev...), func() []cell.Line { _, tl := s.Render(width, th); return tl }()...), want) && len(want) > 0 {
		t.Fatalf("the final state is not Markdown(src)")
	}
}

func TestMDStreamEqualsMarkdownForAnyChunking(t *testing.T) {
	g := widgettest.NewRand(51)
	docs := []string{mdKitchenSink, "", "\n\n", "a", "```\n", "```go\nx\ny", "- a\n  - b\n\n  c\n- d", "> q\n\nlazy", "a\n===\nb\n---\n", "| a |\n|--|\n| 1 |\n| 2 |\n\ntext"}
	for _, h := range widgettest.Hostile() {
		docs = append(docs, "# "+h+"\n\n- "+h+"\n\n```\n"+h+"\n```\n\n"+h)
	}
	n := 50
	if widgetUnderRace() {
		n = 20
	}
	for i := 0; i < n; i++ {
		docs = append(docs, g.Markdown(2+g.Intn(60)))
	}
	themes := []Theme{MonoTheme(), DefaultTheme()}
	for i, doc := range docs {
		for k := 0; k < 3; k++ {
			chunks := g.Chunks(doc, 1+g.Intn(40))
			if k == 0 && len(doc) <= 300 { // the worst case: one byte at a time
				chunks = chunks[:0]
				for j := 0; j < len(doc); j++ {
					chunks = append(chunks, doc[j:j+1])
				}
			}
			if k > 0 && len(doc) > 300 { // a long document: pieces of about ten bytes, cut anywhere
				chunks = g.Chunks(doc, len(doc)/10)
			}
			width := []int{10, 24, 40, 80}[g.Intn(4)]
			checkStreamProperties(t, doc, chunks, width, themes[(i+k)%2])
		}
	}
}

// Random documents made of the pieces that give markdown its structure (markers, pipes, fences, tabs, blank lines, line ends):
// that is where "stable never changes" is hardest to keep, because a short text of such pieces is a table, a list or a code
// block in one state and something else one byte later. Every document is fed a byte at a time. (A search of this kind found
// the two cases of TestMDStreamATableHeaderAfterAnotherBlockTakesNothingBack and ...ARowThatCannotBeAHeader....)
func TestMDStreamStructuralDocuments(t *testing.T) {
	pieces := []string{"\n", "\n", "\n", "#", "# ", " ", "  ", "    ", "\t", "-", "- ", "|", "|-", "-|", "|-|", "---", "===", "=", ">", "> ", "`", "```",
		"~~~", "a", "b", "00", "1", "1.", "1. ", "2. ", ".", "*", "* ", ":", ":-", "-:", "\\", "[", "]", "(", ")", "[a](b)", "**", "_", "~~", "<", "&",
		"\r\n", "\r", "a|b", "a | b", "| a | b |", "|--|--|", "- [ ] ", "- [x] ", "\n\n"}
	g := widgettest.NewRand(73)
	n := 4000
	if widgetUnderRace() {
		n = 800
	}
	themes := []Theme{MonoTheme(), DefaultTheme()}
	for i := 0; i < n; i++ {
		var sb strings.Builder
		for j, k := 0, 2+g.Intn(14); j < k; j++ {
			sb.WriteString(pieces[g.Intn(len(pieces))])
		}
		doc := sb.String()
		singles := make([]string, len(doc))
		for j := 0; j < len(doc); j++ {
			singles[j] = doc[j : j+1]
		}
		checkStreamProperties(t, doc, singles, []int{6, 10, 24, 40}[g.Intn(4)], themes[i%2])
	}
}

func TestMDStreamSurvivesChunksThatSplitRunesAndEscapes(t *testing.T) {
	doc := "# 日本語 e\U00000301 🐎\n\ntext \x1b[31mred\x1b[0m \x1b]52;c;QQ==\x07 done\n\n- 中文\n\n```\n\x1b]0;t\nnext\x07\n```\n"
	for cut1 := 0; cut1 <= len(doc); cut1 += 1 {
		checkStreamProperties(t, doc, []string{doc[:cut1], doc[cut1:]}, 30, MonoTheme())
	}
}

// Text that is still arriving can never reach back over a newline: a terminator for an escape string that was already
// followed by lines does not remove them.
func TestMDStreamAnEscapeStringAcrossLinesNeverUnprintsAStableLine(t *testing.T) {
	doc := "intro\n\n\x1b]0;title\n\nmiddle\n\nlast\x07 tail\n\nend"
	for _, size := range []int{1, 2, 3, 5, 8} {
		var chunks []string
		for i := 0; i < len(doc); i += size {
			chunks = append(chunks, doc[i:min(i+size, len(doc))])
		}
		checkStreamProperties(t, doc, chunks, 30, MonoTheme())
	}
}

func TestMDStreamIsDeterministic(t *testing.T) {
	doc := mdKitchenSink
	run := func() ([]cell.Line, []cell.Line) {
		var s MDStream
		for i := 0; i < len(doc); i += 7 {
			s.Append(doc[i:min(i+7, len(doc))])
			s.Render(50, DefaultTheme())
		}
		return s.Render(50, DefaultTheme())
	}
	a1, a2 := run()
	b1, b2 := run()
	if !reflect.DeepEqual(a1, b1) || !reflect.DeepEqual(a2, b2) {
		t.Error("not deterministic")
	}
}

func TestMDStreamBigDocumentsStayLinearPerRender(t *testing.T) {
	// appending to a long finished document and rendering: the finished blocks are not rendered again
	requireLinear(t, "render of a stream of n blocks", 150, func(n int) {
		var s MDStream
		s.Append(strings.Repeat("a paragraph of some words\n\n- item\n- item\n\n```\ncode\n```\n\n", n))
		s.Render(80, DefaultTheme())
		for i := 0; i < 20; i++ { // 20 more renders while a last block grows
			s.Append("tail ")
			s.Render(80, DefaultTheme())
		}
	})
}
