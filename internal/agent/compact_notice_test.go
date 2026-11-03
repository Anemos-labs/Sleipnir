package agent

import "testing"

func TestCompactNotice(t *testing.T) {
	for _, c := range []struct {
		turns, masked, before, after int
		want                         string
	}{
		{13, 0, 21000, 9000, "compacted 13 turns (21k→9.0k tokens)"},
		{0, 7, 51000, 39000, "shortened 7 old tool results (51k→39k tokens)"},
		{1, 1, 6600, 3800, "compacted 1 turn, shortened 1 old tool result (6.6k→3.8k tokens)"},
		{0, 0, 1000, 1000, "compacted 0 turns (1.0k→1.0k tokens)"},
	} {
		if got := compactNotice(c.turns, c.masked, c.before, c.after); got != c.want {
			t.Errorf("compactNotice(%d, %d) = %q, want %q", c.turns, c.masked, got, c.want)
		}
	}
}
