package widget_test

// Readers of a drawn cockpit, for the tests of what it shows: they pick the agent table, the bands, the horse and the fan out of the
// plain text of the lines, the way a person looks at the screen. They know the layout of the frame (docs/design/ux/swarm.png).

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

var (
	showAgentRow = regexp.MustCompile(`^│ ([mw]\d+) +[a-z]+ +\S+ `)
	showMoreRow  = regexp.MustCompile(`^│ … and (\d+) more`)
	showFeedRow  = regexp.MustCompile(`^│ \d\d:\d\d `)
	showRiderRow = regexp.MustCompile(`^([mw]\d+|\+\d+)$`)
)

// showBoxChars are the characters of the cockpit's frame: a list never has them.
const showBoxChars = "╭╮╰╯│├┤┬┴┼─"

// showTable lists what the agent table of a boxed cockpit shows: the ids of the agents in the order of the rows, and how many the
// "… and N more" row says are left out (0 when there is no such row).
func showTable(ls []cell.Line) (ids []string, more int) {
	for _, l := range ls {
		s := l.Plain()
		if m := showAgentRow.FindStringSubmatch(s); m != nil {
			ids = append(ids, m[1])
		} else if m := showMoreRow.FindStringSubmatch(s); m != nil {
			more, _ = strconv.Atoi(m[1])
		}
	}
	return ids, more
}

// showLayout is which bands a boxed cockpit has and how big the flexible ones are.
type showLayout struct {
	top, mid, panels bool
	horse, feed      int // rows of the band with the horse, lines of the live feed
}

func showLayoutOf(ls []cell.Line) showLayout {
	var b showLayout
	for i, l := range ls {
		s := l.Plain()
		switch {
		case strings.HasPrefix(s, "├─ agents"):
			b.top, b.horse = true, i-1
		case strings.HasPrefix(s, "├─ swarm gantt"), strings.HasPrefix(s, "├─ task board"):
			b.mid = true
		case strings.HasPrefix(s, "├─ merge queue"):
			b.panels = true
		case showFeedRow.MatchString(s):
			b.feed++
		}
	}
	return b
}

// showRules joins the rules of the box that carry a title (├─ name ──): they name the panels the cockpit has.
func showRules(ls []cell.Line) string {
	var out []string
	for _, l := range ls {
		if s := l.Plain(); strings.HasPrefix(s, "├") {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// showTopRows are the rows between the title bar and the first rule, as runes.
func showTopRows(ls []cell.Line) [][]rune {
	var out [][]rune
	for i := 1; i < len(ls); i++ {
		s := []rune(ls[i].Plain())
		if len(s) == 0 || s[0] == '├' || s[0] == '╰' {
			break
		}
		out = append(out, s)
	}
	return out
}

// showSplitTop cuts a row of the top band at the line between the horse and the fan: what is left of it (without the border) and
// what is right of it (without the border). A row with no such line is all horse.
func showSplitTop(row []rune) (horse, fan string) {
	n := 0
	for x, r := range row {
		if r == '│' {
			if n++; n == 2 {
				return string(row[1:x]), string(row[x+1 : len(row)-1])
			}
		}
	}
	return string(row), ""
}

// showHorse is the horse band of a cockpit, row by row.
func showHorse(ls []cell.Line) []string {
	var out []string
	for _, row := range showTopRows(ls) {
		h, _ := showSplitTop(row)
		out = append(out, h)
	}
	return out
}

// showRiders reads the row of the fan that names the riders: the ones it names and the "+N" that counts the others (0 without).
func showRiders(ls []cell.Line) (named []string, more int) {
	for _, row := range showTopRows(ls) {
		_, fan := showSplitTop(row)
		f := strings.Fields(strings.Trim(fan, "│ "))
		ok := len(f) > 1
		for _, w := range f {
			ok = ok && showRiderRow.MatchString(w)
		}
		if !ok {
			continue
		}
		for _, w := range f {
			if strings.HasPrefix(w, "+") {
				more, _ = strconv.Atoi(w[1:])
			} else {
				named = append(named, w)
			}
		}
		return named, more
	}
	return nil, 0
}

// showLine is the first row of the picture that holds s ("" when none does).
func showLine(ls []cell.Line, s string) string {
	for _, l := range ls {
		if p := l.Plain(); strings.Contains(p, s) {
			return p
		}
	}
	return ""
}

func showNoBox(t *testing.T, what string, ls []cell.Line) {
	t.Helper()
	for i, l := range ls {
		if strings.ContainsAny(l.Plain(), showBoxChars) {
			t.Errorf("%s: line %d has a frame character in a list: %q", what, i, l.Plain())
			return
		}
	}
}
