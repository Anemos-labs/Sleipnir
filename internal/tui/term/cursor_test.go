package term

import "testing"

func TestParseCursorReport(t *testing.T) {
	for _, c := range []struct {
		in   string
		row  int
		want bool
	}{
		{"\x1b[12;1R", 11, true},
		{"\x1b[1;40R", 0, true},
		{"ab\x1b[5;7R", 4, true}, // a key typed before the answer
		{"\x1b[5;7", 0, false},   // not finished
		{"\x1b[;7R", 0, false},
		{"\x1b[0;0R", 0, false},
		{"hello", 0, false},
	} {
		row, ok := parseCursorReport([]byte(c.in))
		if ok != c.want || (ok && row != c.row) {
			t.Errorf("parseCursorReport(%q) = %d, %v; want %d, %v", c.in, row, ok, c.row, c.want)
		}
	}
}
