package vt

import (
	"testing"
)

// checkInvariants asserts what must hold after any input: the cursor is inside the screen, every row is exactly as wide as the
// screen, wide runes are whole, padding is where it says it is, and the scrollback is within its limit.
func checkInvariants(tb testing.TB, v *Term) {
	tb.Helper()
	x, y, _ := v.Cursor()
	if x < 0 || x >= v.cols || y < 0 || y >= v.rows {
		tb.Fatalf("cursor (%d,%d) outside the %dx%d screen", x, y, v.cols, v.rows)
	}
	if v.pending && v.x != v.cols-1 {
		tb.Fatalf("delayed wrap at column %d of %d", v.x, v.cols)
	}
	if len(v.g().rows) != v.rows {
		tb.Fatalf("%d screen rows for a %d row screen", len(v.g().rows), v.rows)
	}
	if len(v.main.rows) != v.rows {
		tb.Fatalf("%d primary rows for a %d row screen", len(v.main.rows), v.rows)
	}
	check := func(where string, rows []vrow) {
		for i, r := range rows {
			if len(r.cells) != v.cols {
				tb.Fatalf("%s row %d has %d cells, want %d", where, i, len(r.cells), v.cols)
			}
			for x, c := range r.cells {
				if c.wide && (x+1 >= len(r.cells) || !r.cells[x+1].cont) {
					tb.Fatalf("%s row %d: wide rune at %d has no right half", where, i, x)
				}
				if c.cont && (x == 0 || !r.cells[x-1].wide) {
					tb.Fatalf("%s row %d: right half at %d has no left half", where, i, x)
				}
				if c.wide && c.text == "" {
					tb.Fatalf("%s row %d: wide cell %d has no text", where, i, x)
				}
				if c.cont && c.text != "" {
					tb.Fatalf("%s row %d: right half %d has text %q", where, i, x, c.text)
				}
			}
			if r.pad && (!r.wrapped || r.cells[len(r.cells)-1] != (vcell{})) {
				tb.Fatalf("%s row %d: padding that is not a blank wrapped last cell", where, i)
			}
		}
	}
	check("screen", v.g().rows)
	check("primary", v.main.rows)
	check("scrollback", v.sb)
	if len(v.sb) > v.sbMax {
		tb.Fatalf("%d scrollback rows, limit %d", len(v.sb), v.sbMax)
	}
	if v.syncDepth < 0 {
		tb.Fatalf("sync depth %d", v.syncDepth)
	}
	_ = v.All()
	_ = v.String()
	for yy := 0; yy < v.rows; yy++ {
		for xx := 0; xx < v.cols; xx++ {
			v.Cell(xx, yy)
		}
	}
}
