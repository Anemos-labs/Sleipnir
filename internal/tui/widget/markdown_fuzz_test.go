package widget

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

// Fuzz tests: `go test -fuzz FuzzMarkdown ./internal/tui/widget` (and FuzzMDStream) search for input that breaks the promises.
// Without -fuzz they run the seeds below as ordinary tests. A crasher is saved under testdata/fuzz: once it is fixed, keep the
// file there or add the input to the seeds below (the crashers found so far are seeds).

func markdownSeeds() []string {
	seeds := append([]string(nil), widgettest.Hostile()...)
	seeds = append(seeds, widgettest.Unicode()...)
	seeds = append(seeds, mdKitchenSink,
		"", "\n", "# ", "#\n=\n", "```", "```\n", "~~~\nx", "    x\n", "> ", ">>>> x", "- ", "- - - -", "1. 2. 3.", "* * *", "---", "===",
		"| a |\n|---|\n| b |", "|", "||", "|:-:|", "a|b\n-|-\n1|2", "**", "__", "~~", "*a _b* c_", "`", "``x`", "[", "]", "[a](", "[a](b", "![a](b",
		"[a](<b>)", "[a](b \"c\")", "<", "<a@b>", "<http://x>", "&", "&#", "&#x;", "&#27;", "&amp", "\\", "\\\n", "a  \nb", "http://", "https://a.b)",
		"\t\t\t\t\t0", "# 0\n00|\n|-", "- # i\n00|\n|-|\n|1|", "- [ ]", "- [x] a\n  - [ ] b", "1) a\n2) b", "> - a\n>\n> ```\n> x", "- a\n\n  b\n\nc", "\t- a\n\t\t- b", "a\n---\nb\n===", "|a|\n|-|\n"+
			"|b|\n|c|\n\n```\nx\n```", "\x1b]52;c;QQ==\x07", "\x1b[", "a\x1b", "\u009b31m", "\xff\xfe", "日本語\n\n中文", "e\U00000301\U00000301\U00000301",
	)
	return seeds
}

func FuzzMarkdown(f *testing.F) {
	for _, s := range markdownSeeds() {
		f.Add(s, uint8(40), uint8(0))
		f.Add(s, uint8(10), uint8(2))
	}
	themes := allTestThemes()
	f.Fuzz(func(t *testing.T, src string, width, theme uint8) {
		w := 1 + int(width)%130
		nt := themes[int(theme)%len(themes)]
		lines := Markdown(src, w, nt.th)
		checkLines(t, "fuzz/"+nt.name, lines, w)
		// The bound is there to catch a blow-up, not to be tight: a tab is one byte and up to four cells, and at width 1 a row
		// holds one cell, so a line of tabs in a code block is legitimately four rows a byte.
		if len(lines) > 5*len(src)+8 {
			t.Fatalf("%d lines for %d bytes of input", len(lines), len(src))
		}
		total := 0
		for _, l := range lines {
			for _, sp := range l {
				total += len(sp.Text)
			}
		}
		if total > (len(src)+8)*(4*w+64) {
			t.Fatalf("%d bytes of output for %d bytes of input at width %d", total, len(src), w)
		}
	})
}

func FuzzMDStream(f *testing.F) {
	for i, s := range markdownSeeds() {
		f.Add(s, int64(i), uint8(30))
	}
	f.Fuzz(func(t *testing.T, src string, seed int64, width uint8) {
		if len(src) > 2000 {
			t.Skip("long inputs only make the quadratic check slow")
		}
		w := 1 + int(width)%130
		chunks := widgettest.NewRand(seed).Chunks(src, 1+int(uint64(seed)%48))
		checkStreamProperties(t, src, chunks, w, allTestThemes()[int(uint64(seed)%3)].th)
	})
}
