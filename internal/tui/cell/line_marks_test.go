package cell

import (
	"strings"
	"testing"
)

// A long run of combining marks stays on its base rune, cut to maxGlyphBytes, in one glyph.
func TestALongRunOfMarksIsBounded(t *testing.T) {
	g := glyphs(Line{{Text: "e" + strings.Repeat("́", 100000)}})
	if len(g) != 1 || len(g[0].text) > maxGlyphBytes || !strings.HasPrefix(g[0].text, "é") {
		t.Errorf("%d glyphs, first %d bytes", len(g), len(g[0].text))
	}
}
