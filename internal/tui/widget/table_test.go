package widget

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

func tblRow(cells ...string) []cell.Line { return plainLines(cells...) }

func tblAgents() Table {
	return Table{
		Columns: []Column{
			{Title: "Agent", Weight: 1, Priority: 3},
			{Title: "Role", Priority: 1},
			{Title: "Tokens", Align: AlignRight, Priority: 2},
			{Title: "Hit", Align: AlignCenter},
		},
		Rows: [][]cell.Line{
			tblRow("manager", "lead", "12.1k", "93%"),
			tblRow("worker-auth-refactor", "writer", "812", "7%"),
			tblRow("日本語のエージェント", "reader", "1.2M", "100%"),
			tblRow("x", "y"),
		},
	}
}

func TestTableGolden(t *testing.T) {
	tb := tblAgents()
	for _, nt := range []themeCase{{"mono", MonoTheme()}, {"default", DefaultTheme()}} {
		checkGoldenLines(t, "table_"+nt.name+"_60", nt.th, tb.Render(60, nt.th))
		checkGoldenLines(t, "table_"+nt.name+"_34", nt.th, tb.Render(34, nt.th))
		checkGoldenLines(t, "table_"+nt.name+"_selected", nt.th, tb.RenderSelected(44, nt.th, 1))
	}
	zebra := tblAgents()
	zebra.Zebra = true
	zebra.Grid = true
	checkGoldenLines(t, "table_default_grid_zebra", DefaultTheme(), zebra.RenderSelected(60, DefaultTheme(), 2))
	checkGoldenLines(t, "table_mono_grid", MonoTheme(), zebra.Render(60, MonoTheme()))
}

func TestTableRender(t *testing.T) {
	th := MonoTheme()
	tb := Table{
		Columns: []Column{{Title: "Name"}, {Title: "Qty", Align: AlignRight}, {Title: "Mid", Align: AlignCenter}},
		Rows:    [][]cell.Line{tblRow("apple", "3", "x"), tblRow("kiwi", "12", "yy"), tblRow("fig", "456", "zzzz")},
	}
	want := "" +
		"Name   Qty  Mid\n" +
		"─────  ───  ────\n" +
		"apple    3   x\n" +
		"kiwi    12   yy\n" +
		"fig    456  zzzz"
	if got := widgettest.Flatten(tb.Render(40, th)); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	tb.Grid = true
	want = "" +
		"Name  │ Qty │ Mid\n" +
		"──────┼─────┼─────\n" +
		"apple │   3 │  x\n" +
		"kiwi  │  12 │  yy\n" +
		"fig   │ 456 │ zzzz"
	if got := widgettest.Flatten(tb.Render(40, th)); got != want {
		t.Errorf("grid:\n%s\nwant:\n%s", got, want)
	}
	if got := widgettest.Flatten(tb.Render(40, th.WithASCII(true))); !strings.Contains(got, "------+-----+-----") || strings.Contains(got, "│") {
		t.Errorf("an ASCII grid:\n%s", got)
	}
}

func TestTableTruncatesWithAnEllipsis(t *testing.T) {
	th := MonoTheme()
	tb := Table{Columns: []Column{{Title: "Path"}, {Title: "N", Align: AlignRight}}, Rows: [][]cell.Line{
		tblRow("internal/tui/widget/table.go", "1"), tblRow("a.go", "22"), tblRow("日本語のファイル名.go", "3"),
	}}
	got := tb.Render(16, th)
	checkLines(t, "truncate", got, 16)
	flat := widgettest.Flatten(got)
	for _, frag := range []string{"internal/tu…", "a.go", "22", "日本語のフ…"} {
		if !strings.Contains(flat, frag) {
			t.Errorf("missing %q in:\n%s", frag, flat)
		}
	}
	for _, l := range got[2:] {
		if l.Width() != 14+2 {
			t.Errorf("a table that has to shrink fills the width exactly: %q is %d cells", l.Plain(), l.Width())
		}
	}
	// the narrow column is intact, the wide one is cut
	if !strings.Contains(flat, "  1") || !strings.Contains(flat, " 22") {
		t.Errorf("short cells must not be cut first:\n%s", flat)
	}
	// ASCII ellipsis
	ascii := widgettest.Flatten(tb.Render(16, th.WithASCII(true)))
	if !strings.Contains(ascii, "internal/...") || strings.ContainsAny(ascii, "…─") {
		t.Errorf("ASCII table:\n%s", ascii)
	}
}

func TestTableWeightsAndBounds(t *testing.T) {
	th := MonoTheme()
	tb := Table{Columns: []Column{{Title: "A", Weight: 1}, {Title: "B", Weight: 3}, {Title: "C", Weight: 0}}, Rows: [][]cell.Line{tblRow("a", "b", "c")}}
	got := tb.Render(30, th)
	if got[0].Width() != 30 {
		t.Errorf("columns with a weight fill the width: %d", got[0].Width())
	}
	rule := got[1].Plain()
	cols := strings.Fields(rule)
	n := utf8.RuneCountInString
	if len(cols) != 3 || n(cols[2]) != 1 || n(cols[1]) < 2*n(cols[0]) {
		t.Errorf("weights 1:3 and a fixed last column: %q", rule)
	}
	capped := Table{Columns: []Column{{Title: "A", Weight: 1, Max: 5}, {Title: "B", Weight: 1}}, Rows: [][]cell.Line{tblRow("a", "b")}}
	if w := strings.Fields(capped.Render(40, th)[1].Plain())[0]; utf8.RuneCountInString(w) != 5 {
		t.Errorf("Max caps the growth: %d", utf8.RuneCountInString(w))
	}
	floor := Table{Columns: []Column{{Title: "A", Min: 8}, {Title: "B"}}, Rows: [][]cell.Line{tblRow("a", "b")}}
	if w := strings.Fields(floor.Render(40, th)[1].Plain())[0]; utf8.RuneCountInString(w) != 8 {
		t.Errorf("Min is a floor even for short content: %d", utf8.RuneCountInString(w))
	}
}

func TestTableDropsLowestPriorityColumnsFirst(t *testing.T) {
	th := MonoTheme()
	tb := tblAgents()
	headers := func(w int) []string {
		got := tb.Render(w, th)
		return strings.Fields(got[0].Plain())
	}
	if h := headers(80); !reflect.DeepEqual(h, []string{"Agent", "Role", "Tokens", "Hit"}) {
		t.Errorf("wide: %v", h)
	}
	// Hit has the lowest priority (0), then Role (1), then Tokens (2), and Agent (3) stays to the end
	var states []string
	for w := 80; w >= 1; w-- {
		h := headers(w)
		if len(h) == 0 {
			t.Fatalf("width %d: the last column is never dropped", w)
		}
		if state := strings.Join(h, ","); len(states) == 0 || states[len(states)-1] != state {
			states = append(states, state)
		}
	}
	want := []string{"Agent,Role,Tokens,Hit", "Agent,Role,Tokens", "Agent,Tokens", "Agent"}
	if len(states) < 4 || !reflect.DeepEqual(states[:4], want) {
		t.Fatalf("columns were dropped in this order: %v, want %v first", states, want)
	}
	for _, st := range states[4:] { // what is left is the one column, cut shorter and shorter
		if !strings.HasPrefix("Agent", strings.TrimSuffix(st, "…")) {
			t.Errorf("a single column: %q", st)
		}
	}
	if h := headers(45); !reflect.DeepEqual(h, []string{"Agent", "Role", "Tokens"}) && !reflect.DeepEqual(h, []string{"Agent", "Role", "Tokens", "Hit"}) {
		t.Errorf("width 45: %v", h)
	}
}

func TestTableDropsTheRightmostOfEqualPriority(t *testing.T) {
	tb := Table{Columns: []Column{{Title: "AAAAAAAAAA", Min: 10}, {Title: "BBBBBBBBBB", Min: 10}, {Title: "CCCCCCCCCC", Min: 10}}, Rows: [][]cell.Line{tblRow("1", "2", "3")}}
	got := strings.Fields(tb.Render(24, MonoTheme())[0].Plain())
	if !reflect.DeepEqual(got, []string{"AAAAAAAAAA", "BBBBBBBBBB"}) {
		t.Errorf("%v", got)
	}
}

func TestTableSelectionIsInTheText(t *testing.T) {
	th := MonoTheme()
	tb := tblAgents()
	for sel := 0; sel < len(tb.Rows); sel++ {
		plain := strings.Split(widgettest.Flatten(tb.RenderSelected(60, th, sel)), "\n")
		marked := 0
		for i, ln := range plain {
			if strings.HasPrefix(ln, "❯ ") {
				marked++
				if i != sel+2 {
					t.Errorf("selected %d: the marker is on line %d", sel, i)
				}
			} else if !strings.HasPrefix(ln, "  ") {
				t.Errorf("selected %d: every other row keeps the gutter: %q", sel, ln)
			}
		}
		if marked != 1 {
			t.Errorf("selected %d: %d marked rows", sel, marked)
		}
	}
	for _, sel := range []int{-1, 4, 99} {
		got := widgettest.Flatten(tb.RenderSelected(60, th, sel))
		if strings.Contains(got, "❯") || strings.HasPrefix(got, "  ") {
			t.Errorf("selected %d marks something or leaves a gutter:\n%s", sel, got)
		}
	}
	// and in attributes: reverse video in mono
	got := tb.RenderSelected(60, th, 0)
	for _, sp := range got[2] {
		if !sp.Style.Has(cell.Reverse) {
			t.Errorf("the selected row is reverse video in mono: %+v", sp)
		}
	}
	if got[3][0].Style.Has(cell.Reverse) {
		t.Error("only the selected row")
	}
	ascii := widgettest.Flatten(tb.RenderSelected(60, th.WithASCII(true), 0))
	if !strings.Contains(ascii, "> manager") {
		t.Errorf("ASCII marker:\n%s", ascii)
	}
}

func TestTableHeaderAndRuleInMono(t *testing.T) {
	got := widgettest.Flatten(tblAgents().Render(60, MonoTheme()))
	lines := strings.Split(got, "\n")
	if !strings.HasPrefix(lines[0], "Agent") || !strings.HasPrefix(lines[1], "─") || strings.Trim(lines[1], "─ ") != "" {
		t.Errorf("a header and a rule, in plain text:\n%s", got)
	}
	head := tblAgents().Render(60, MonoTheme())[0]
	for _, sp := range head {
		if strings.TrimSpace(sp.Text) != "" && !sp.Style.Has(cell.Bold) {
			t.Errorf("header cells are bold: %+v", sp)
		}
	}
}

func TestTableStyles(t *testing.T) {
	th := DefaultTheme()
	tb := tblAgents()
	tb.Zebra = true
	tb.Rows[0][0] = cell.Styled(th.Good, "manager")
	got := tb.RenderSelected(60, th, 2)
	if got[2][0].Style.BG != th.Zebra.BG && got[2][0].Style.BG.Kind != cell.KindDefault {
		t.Errorf("row 0 is even: %+v", got[2][0].Style)
	}
	found := false
	for _, sp := range got[2] {
		if strings.Contains(sp.Text, "manager") && sp.Style.FG == th.Good.FG {
			found = true
		}
	}
	if !found {
		t.Error("a cell keeps its own style")
	}
	if got[3][len(got[3])-1].Style.BG != th.Zebra.BG {
		t.Errorf("odd rows have the zebra style: %+v", got[3][len(got[3])-1].Style)
	}
	if got[4][len(got[4])-1].Style.BG != th.SelectedRow.BG {
		t.Errorf("the selected row has the selection style: %+v", got[4][len(got[4])-1].Style)
	}
	if got[0][0].Style.FG != (cell.Color{}) && got[0][0].Style.Attr&cell.Bold == 0 {
		t.Error("header style")
	}
}

func TestTableOddShapes(t *testing.T) {
	th := MonoTheme()
	if (Table{}).Render(40, th) != nil || (Table{Columns: []Column{{Title: "a"}}}).Render(0, th) != nil || (Table{Columns: []Column{{Title: "a"}}}).Render(-3, th) != nil {
		t.Error("no columns or no width give nil")
	}
	noHead := Table{Columns: []Column{{}, {}}, Rows: [][]cell.Line{tblRow("a", "b")}}
	if got := widgettest.Flatten(noHead.Render(20, th)); got != "a  b" {
		t.Errorf("no titles, no header row and no rule: %q", got)
	}
	ragged := Table{Columns: []Column{{Title: "A"}, {Title: "B"}}, Rows: [][]cell.Line{tblRow("1"), tblRow("1", "2", "3"), nil}}
	if got := widgettest.Flatten(ragged.Render(20, th)); got != "A  B\n─  ─\n1\n1  2\n" {
		t.Errorf("short rows are padded, extra cells ignored: %q", got)
	}
	onlyHead := Table{Columns: []Column{{Title: "A"}, {Title: "B"}}}
	if got := widgettest.Flatten(onlyHead.Render(20, th)); got != "A  B\n─  ─" {
		t.Errorf("no rows: %q", got)
	}
	styled := Table{Columns: []Column{{Head: cell.Styled(cell.Style{FG: cell.ANSI(2)}, "Green")}}, Rows: [][]cell.Line{tblRow("x")}}
	if h := styled.Render(20, th)[0]; h[0].Style.FG != cell.ANSI(2) || !h[0].Style.Has(cell.Bold) {
		t.Errorf("a styled Head keeps its colour and gets the header attributes: %+v", h[0].Style)
	}
}

func TestLayoutColumns(t *testing.T) {
	cols := func(mm ...[3]int) []Column {
		out := make([]Column, len(mm))
		for i, m := range mm {
			out[i] = Column{Min: m[0], Max: m[1], Weight: m[2]}
		}
		return out
	}
	sum := func(w []int, gap int) int {
		s := gap * (len(w) - 1)
		for _, x := range w {
			s += x
		}
		return s
	}
	cases := []struct {
		name    string
		cols    []Column
		natural []int
		avail   int
		gap     int
		keep    []int
		widths  []int
	}{
		{"fits", cols([3]int{}, [3]int{}), []int{5, 7}, 40, 2, []int{0, 1}, []int{5, 7}},
		{"exactly", cols([3]int{}, [3]int{}), []int{5, 7}, 14, 2, []int{0, 1}, []int{5, 7}},
		{"weights share the rest", cols([3]int{0, 0, 1}, [3]int{0, 0, 3}), []int{5, 5}, 28, 2, []int{0, 1}, []int{9, 17}},
		{"a weight respects Max and the rest goes to the others", cols([3]int{0, 8, 1}, [3]int{0, 0, 1}), []int{5, 5}, 30, 2, []int{0, 1}, []int{8, 20}},
		{"the widest are cut first", cols([3]int{1, 0, 0}, [3]int{1, 0, 0}, [3]int{1, 0, 0}), []int{30, 5, 30}, 44, 2, []int{0, 1, 2}, []int{18, 5, 17}},
		{"min is honoured", cols([3]int{20, 0, 0}, [3]int{1, 0, 0}), []int{30, 30}, 30, 2, []int{0, 1}, []int{20, 8}},
		{"max clamps the content", cols([3]int{0, 6, 0}), []int{30}, 40, 2, []int{0}, []int{6}},
		{"one column narrower than its min", cols([3]int{10, 0, 0}), []int{30}, 4, 2, []int{0}, []int{4}},
	}
	for _, c := range cases {
		keep, widths := tableLayout(c.cols, c.natural, c.avail, c.gap)
		if !reflect.DeepEqual(keep, c.keep) || !reflect.DeepEqual(widths, c.widths) {
			t.Errorf("%s: kept %v widths %v, want %v %v", c.name, keep, widths, c.keep, c.widths)
		}
		if len(widths) > 1 && sum(widths, c.gap) > c.avail {
			t.Errorf("%s: %v overflows %d", c.name, widths, c.avail)
		}
	}
}

func TestLayoutColumnsPropertyAndAbsurdValues(t *testing.T) {
	g := widgettest.NewRand(31)
	for i := 0; i < 3000; i++ {
		n := 1 + g.Intn(6)
		cols := make([]Column, n)
		natural := make([]int, n)
		for j := range cols {
			cols[j] = Column{Min: g.Intn(6) - 1, Max: g.Intn(12) - 2, Weight: g.Intn(4) - 1, Priority: g.Intn(3)}
			natural[j] = g.Intn(40)
		}
		gap := 2 + g.Intn(2)
		avail := g.Intn(100) - 5
		keep, widths := tableLayout(cols, natural, avail, gap)
		if len(keep) == 0 || len(keep) != len(widths) || len(keep) > n {
			t.Fatalf("keep %v widths %v", keep, widths)
		}
		for k := 1; k < len(keep); k++ {
			if keep[k] <= keep[k-1] {
				t.Fatalf("columns must stay in order: %v", keep)
			}
		}
		total := gap * (len(widths) - 1)
		for k, w := range widths {
			if w < 1 {
				t.Fatalf("a column of width %d: %v", w, widths)
			}
			if c := cols[keep[k]]; c.Max > 0 && w > max(c.Max, max(1, c.Min)) {
				t.Fatalf("column %d is %d wide but its Max is %d", keep[k], w, c.Max)
			}
			total += w
		}
		if len(widths) > 1 && total > avail {
			t.Fatalf("avail %d but %v (gap %d) needs %d", avail, widths, gap, total)
		}
		k2, w2 := tableLayout(cols, natural, avail, gap)
		if !reflect.DeepEqual(keep, k2) || !reflect.DeepEqual(widths, w2) {
			t.Fatal("not deterministic")
		}
	}
	absurd := []Column{{Min: math.MaxInt, Max: math.MaxInt, Weight: math.MaxInt}, {Min: math.MinInt, Max: math.MinInt, Weight: math.MinInt}, {Weight: math.MaxInt}}
	keep, widths := tableLayout(absurd, []int{math.MaxInt32, 3, 7}, 80, 2)
	if len(keep) == 0 || len(widths) != len(keep) {
		t.Fatalf("absurd values: %v %v", keep, widths)
	}
	for _, w := range widths {
		if w < 1 || w > 1<<21 {
			t.Fatalf("absurd values gave a width of %d", w)
		}
	}
}

func TestTableProperty(t *testing.T) {
	g := widgettest.NewRand(41)
	for i := 0; i < 80; i++ {
		n := 1 + g.Intn(5)
		cols := make([]Column, n)
		for j := range cols {
			cols[j] = Column{Title: g.Text(g.Intn(3)), Min: g.Intn(4), Max: g.Intn(3) * 10, Weight: g.Intn(3), Align: Align(g.Intn(3)), Priority: g.Intn(3)}
		}
		var rows [][]cell.Line
		for r := 0; r < g.Intn(8); r++ {
			var cells []cell.Line
			for c := 0; c < g.Intn(n+2); c++ {
				cells = append(cells, cell.Text(g.Text(g.Intn(6))))
			}
			rows = append(rows, cells)
		}
		tb := Table{Columns: cols, Rows: rows, Zebra: g.Intn(2) == 0, Grid: g.Intn(2) == 0}
		for _, nt := range allTestThemes() {
			for w := 1; w <= 120; w += 1 + g.Intn(9) {
				got := tb.RenderSelected(w, nt.th, g.Intn(len(rows)+2)-1)
				checkLines(t, "random table/"+nt.name, got, w)
			}
		}
	}
}

func TestTableHostileAndUnicodeCells(t *testing.T) {
	var cells []cell.Line
	for _, s := range append(widgettest.Hostile(), widgettest.Unicode()...) {
		cells = append(cells, cell.Text(s), cell.Line{{Text: s, Style: cell.Style{Attr: cell.Bold}}})
	}
	tb := Table{Columns: []Column{{Title: "a\x1b[31m"}, {Title: "日本語"}, {Head: cell.Text("x\x1b]52;c;QQ==\x07y")}}}
	for i := 0; i+2 < len(cells); i += 3 {
		tb.Rows = append(tb.Rows, cells[i:i+3])
	}
	for _, nt := range allTestThemes() {
		for w := 1; w <= 100; w += 3 {
			checkLines(t, "hostile table/"+nt.name, tb.RenderSelected(w, nt.th, 2), w)
		}
	}
}

func TestTableIsDeterministicAndDoesNotModifyInput(t *testing.T) {
	tb := tblAgents()
	tb.Rows[0][0] = cell.Text("a\tb")
	saved := tblAgents()
	saved.Rows[0][0] = cell.Text("a\tb")
	a := tb.RenderSelected(40, DefaultTheme(), 1)
	b := tb.RenderSelected(40, DefaultTheme(), 1)
	if !reflect.DeepEqual(a, b) {
		t.Error("not deterministic")
	}
	if !reflect.DeepEqual(tb, saved) {
		t.Error("the table was modified")
	}
}
