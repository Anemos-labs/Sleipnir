package widget

import (
	"strings"
	"testing"
)

// A table's rows are all as wide as its header, so a wide header over many short rows is cut at a budget of cells, and the
// table still ends where it ends.
func TestMarkdownTableCellsAreBounded(t *testing.T) {
	lines := []string{"|" + strings.Repeat("h|", 2000), "|" + strings.Repeat("-|", 2000)}
	for range 5000 {
		lines = append(lines, "|")
	}
	lines = append(lines, "", "after")
	tb, end, ok := mdTableAt(lines, 0)
	if !ok || end != len(lines)-2 {
		t.Fatalf("table ends at %d, ok %v", end, ok)
	}
	if n := len(tb.rows) * len(tb.align); n > mdMaxTableCells {
		t.Errorf("%d cells kept", n)
	}
	if last := tb.rows[len(tb.rows)-1]; last[0] != "…" {
		t.Errorf("the cut is not shown: %q", last[0])
	}
}
