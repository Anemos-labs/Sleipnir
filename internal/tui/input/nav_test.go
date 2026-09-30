package input

import (
	"math/rand"
	"testing"
)

func rowRig(t *testing.T, width int, text string) *rig {
	t.Helper()
	r := newRig(t, Options{})
	r.ed.SetWidth(width)
	r.ed.SetText(text)
	return r
}

func TestUpAndDownMoveByDisplayRowWhenTheWidthIsKnown(t *testing.T) {
	// width 10: the text area is 8 cells: rows "aaa bbb" / "ccc ddd" / "eee fff" / "ggg"
	r := rowRig(t, 10, "aaa bbb ccc ddd eee fff ggg")
	if got := r.ed.View(10).Plain(); len(got) != 4 {
		t.Fatalf("the layout this test assumes: %q", got)
	}
	steps := []struct{ key, want string }{
		{kUp, "aaa bbb ccc ddd eee| fff ggg"},
		{kUp, "aaa bbb ccc| ddd eee fff ggg"},
		{kUp, "aaa| bbb ccc ddd eee fff ggg"},
		{kDown, "aaa bbb ccc| ddd eee fff ggg"},
		{kDown, "aaa bbb ccc ddd eee| fff ggg"},
		{kDown, "aaa bbb ccc ddd eee fff ggg|"},
	}
	for _, s := range steps {
		r.send(s.key)
		if got := r.state(); got != s.want {
			t.Fatalf("after %q: %q want %q", s.key, got, s.want)
		}
	}
}

func TestUpAndDownByLineWithoutAWidth(t *testing.T) {
	r := newRig(t, Options{})
	r.ed.SetText("aaa bbb ccc ddd eee fff ggg")
	r.send(kUp) // one buffer line: straight to the history (which is empty)
	if got := r.state(); got != "aaa bbb ccc ddd eee fff ggg|" {
		t.Errorf("%q", got)
	}
	r.ed.SetWidth(10)
	r.send(kUp)
	if got := r.state(); got != "aaa bbb ccc ddd eee| fff ggg" {
		t.Errorf("with a width: %q", got)
	}
	r.ed.SetWidth(0)
	r.send(kUp)
	if got := r.state(); got != "aaa bbb ccc ddd eee| fff ggg" {
		t.Errorf("width 0 is unknown again: %q", got)
	}
}

func TestRowMovementReachesTheHistoryOnlyFromTheFirstAndLastRow(t *testing.T) {
	h := NewHistory()
	h.Add("older")
	r := newRig(t, Options{History: h})
	r.ed.SetWidth(10)
	r.ed.SetText("aaa bbb ccc ddd")
	r.send(kUp) // rows "aaa bbb" / "ccc ddd": the cursor was at column 7 of the second; the first row ends at 7: on its last rune
	if got := r.state(); got != "aaa bb|b ccc ddd" {
		t.Fatalf("Up to the first row: %q", got)
	}
	r.send(kUp)
	if got := r.state(); got != "older|" {
		t.Fatalf("from the first row Up walks the history: %q", got)
	}
	r.send(kDown)
	if got := r.state(); got != "aaa bb|b ccc ddd" {
		t.Fatalf("Down returns to the draft, cursor where it was when the walk began: %q", got)
	}
}

func TestRowMovementKeepsThePreferredColumn(t *testing.T) {
	// rows: "abcdef" / "ab" / "abcdef" at width 10 (the lines are separate)
	r := rowRig(t, 10, "abcdef\nab\nabcdef")
	r.send(kUp)
	if got := r.state(); got != "abcdef\nab|\nabcdef" {
		t.Fatalf("%q", got)
	}
	r.send(kUp)
	if got := r.state(); got != "abcdef|\nab\nabcdef" {
		t.Fatalf("the column 6 is remembered across the short row: %q", got)
	}
	r.send(kDown, kDown)
	if got := r.state(); got != "abcdef\nab\nabcdef|" {
		t.Fatalf("%q", got)
	}
	// a cursor after a row that is exactly full stands on a row of its own
	r = rowRig(t, 10, "abcdefgh")
	if v := r.ed.View(10); v.CursorRow != 1 || v.CursorCol != 2 {
		t.Fatalf("%d,%d %q", v.CursorRow, v.CursorCol, v.Plain())
	}
	r.send(kUp)
	if got := r.state(); got != "|abcdefgh" {
		t.Errorf("Up from that row: %q", got)
	}
}

func TestRowMovementOverWideRunesAndTabs(t *testing.T) {
	// text area 6 cells: rows "中中中" / "中中"
	r := rowRig(t, 8, "中中中中中")
	r.send(kUp)
	if got := r.state(); got != "中中|中中中" {
		t.Errorf("%q", got)
	}
	r.send(kDown)
	if got := r.state(); got != "中中中中中|" {
		t.Errorf("the end of the last row: %q", got)
	}
	r.send(kHome, kDown, kRight, kUp) // a column inside a wide rune stays before it
	if got := r.state(); got != "|中中中中中" && got != "中|中中中中" {
		t.Errorf("%q", got)
	}
	r = rowRig(t, 20, "a\tb\nxxxxxx")
	r.send(kUp, kEnd, kDown) // from after the b (display column 5) down to the column 5 of "xxxxxx"
	if got := r.state(); got != "a\tb\nxxxxx|x" {
		t.Errorf("%q", got)
	}
}

func TestRowMovementWithAChip(t *testing.T) {
	r := newRig(t, Options{})
	r.ed.SetWidth(30)
	r.send("ab ", paste(lines(5)), " cd", kUp)
	if r.ed.Empty() {
		t.Fatal("unexpected clear")
	}
	// the chip label is 25 cells: "ab [pasted text #1 +5 lines] cd" is 31 wide and wraps at 28
	r.send(kDown, kUp, kDown)
	r.check()
}

func TestRowMovementNeverLeavesTheBufferInAnOddState(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for n := 0; n < 200; n++ {
		r := newRig(t, Options{})
		r.ed.SetText(randomBuffer(rng) + randomBuffer(rng))
		r.ed.SetWidth(4 + rng.Intn(30))
		start := r.ed.Text()
		for k := 0; k < 30; k++ {
			switch rng.Intn(4) {
			case 0:
				r.send(kUp)
			case 1:
				r.send(kDown)
			case 2:
				r.send(kLeft)
			default:
				r.send(kRight)
			}
		}
		if r.ed.Text() != start {
			t.Fatalf("moving changed the text: %q -> %q", start, r.ed.Text())
		}
		// Up as far as it goes stays inside the text, and the cursor sits on the first row
		for k := 0; k < 100; k++ {
			r.ed.moveRow(-1)
		}
		if v := r.ed.View(r.ed.width); v.CursorRow != 0 {
			t.Fatalf("after Up x100 the cursor is on row %d of %q", v.CursorRow, v.Plain())
		}
		r.check()
	}
}
