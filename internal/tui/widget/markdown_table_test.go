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
	if n := (len(tb.rows) + 1) * len(tb.align); n > mdMaxTableCells {
		t.Errorf("%d cells kept", n)
	}
	if last := tb.rows[len(tb.rows)-1]; last[0] != "…" {
		t.Errorf("the cut is not shown: %q", last[0])
	}
}

// A header wider than any table is drawn as text: its columns are not laid out at all.
func TestMarkdownTableHeaderIsBounded(t *testing.T) {
	lines := []string{"|" + strings.Repeat("h|", mdMaxTableCols+1), "|" + strings.Repeat("-|", mdMaxTableCols+1)}
	if _, _, ok := mdTableAt(lines, 0); ok {
		t.Error("a header wider than the column bound was taken for a table")
	}
}
