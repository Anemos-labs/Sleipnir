package widget

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

func dialogBody() []cell.Line {
	return []cell.Line{cell.Text("go test -race -count=1 ./internal/tui/widget/..."), cell.Text("in ~/code/orders-api")}
}

func TestDialogGolden(t *testing.T) {
	for _, nt := range []themeCase{{"mono", MonoTheme()}, {"default", DefaultTheme()}} {
		for sel := 0; sel < 3; sel++ {
			got := Dialog("Bash command", dialogBody(), PermissionOptions("go test"), sel, 60, nt.th)
			checkGoldenLines(t, fmt.Sprintf("dialog_%s_sel%d", nt.name, sel+1), nt.th, got)
		}
	}
}

func TestDialogWithADiffBodyGolden(t *testing.T) {
	th := MonoTheme()
	inner := BoxInnerWidth(70, BoxHardWrap())
	body := Diff("orders/list.go", "a\nb\nc\nd\n", "a\nB\nc\nd\ne\n", inner, th, DiffOptions{})
	got := Dialog("Edit orders/list.go", body, PermissionOptions("edits to orders/"), 0, 70, th)
	checkLines(t, "dialog with diff", got, 70)
	checkGoldenLines(t, "dialog_diff_mono", th, got)
}

// Without colour, the dialog must still say which option is selected, in the text.
func TestDialogMonoShowsTheSelectionInPlainText(t *testing.T) {
	th := MonoTheme()
	opts := PermissionOptions("go test")
	for sel := range opts {
		plain := widgettest.Flatten(Dialog("Bash command", dialogBody(), opts, sel, 60, th))
		var marked []string
		for _, ln := range strings.Split(plain, "\n") {
			if strings.Contains(ln, "❯") {
				marked = append(marked, ln)
			}
		}
		if len(marked) != 1 || !strings.Contains(marked[0], fmt.Sprintf("❯ %d. ", sel+1)) {
			t.Fatalf("selected %d: marker lines %q in\n%s", sel+1, marked, plain)
		}
		for i := range opts {
			if i != sel && !strings.Contains(plain, fmt.Sprintf("  %d. ", i+1)) {
				t.Errorf("selected %d: option %d is not listed unmarked:\n%s", sel+1, i+1, plain)
			}
		}
	}
	// and in attributes: the selected row is reverse video and bold, the others are not
	got := Dialog("T", nil, opts, 1, 60, th)
	for _, l := range got {
		isSel := strings.Contains(l.Plain(), "❯")
		for _, sp := range l {
			if strings.TrimSpace(sp.Text) == "" || strings.ContainsAny(sp.Text, "╭╮╰╯│─") {
				continue
			}
			if isSel != (sp.Style.Has(cell.Reverse) && sp.Style.Has(cell.Bold)) {
				t.Errorf("span %q of line %q: reverse+bold = %v, selected = %v", sp.Text, l.Plain(), !isSel, isSel)
			}
		}
	}
}

func TestDialogColourThemeMarksTheSelectedRowToo(t *testing.T) {
	th := DefaultTheme()
	got := Dialog("T", nil, PermissionOptions(""), 2, 60, th)
	var row cell.Line
	for _, l := range got {
		if strings.Contains(l.Plain(), "❯") {
			row = l
		}
	}
	if row == nil {
		t.Fatal("no selected row")
	}
	banded := 0
	for _, sp := range row {
		if sp.Style.BG == th.SelectedRow.BG {
			banded++
		}
	}
	if banded < 2 {
		t.Errorf("the selected row has the SelectedRow band: %+v", row)
	}
	// the border is the warning colour
	if got[0][0].Style.FG != th.Warn.FG {
		t.Errorf("border: %+v", got[0][0].Style)
	}
}

func TestDialogSelectionOutOfRange(t *testing.T) {
	for _, sel := range []int{-1, 3, 99, -99} {
		got := widgettest.Flatten(Dialog("T", nil, PermissionOptions(""), sel, 60, MonoTheme()))
		if strings.Contains(got, "❯") {
			t.Errorf("selected %d marks something:\n%s", sel, got)
		}
		if !strings.Contains(got, "1. Yes") || !strings.Contains(got, "3. No") {
			t.Errorf("the options must be listed:\n%s", got)
		}
	}
}

func TestDialogASCII(t *testing.T) {
	th := MonoTheme().WithASCII(true)
	got := widgettest.Flatten(Dialog("Run", plainLines("ls"), PermissionOptions(""), 1, 40, th))
	if strings.ContainsFunc(got, func(r rune) bool { return r > 127 }) {
		t.Errorf("non-ASCII in an ASCII dialog:\n%s", got)
	}
	if !strings.Contains(got, "> 2. Yes, and don't ask again") {
		t.Errorf("the selection marker:\n%s", got)
	}
}

func TestDialogOptionsWrapWithAHangingIndent(t *testing.T) {
	opts := []DialogOption{{Label: "Yes"}, {Label: "Yes, and don't ask again for commands that start with go test"}, {Label: "No", Hint: "(esc)"}}
	got := widgettest.Flatten(Dialog("T", nil, opts, 1, 34, MonoTheme()))
	want := "" +
		"╭─ T ────────────────────────────╮\n" +
		"│   1. Yes                       │\n" +
		"│ ❯ 2. Yes, and don't ask again  │\n" +
		"│      for commands that start   │\n" +
		"│      with go test              │\n" +
		"│   3. No (esc)                  │\n" +
		"╰────────────────────────────────╯"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A command shown for approval must be shown exactly: every character, every space.
func TestDialogShowsTheCommandExactly(t *testing.T) {
	cmds := []string{
		"echo  'two  spaces'   && rm -rf /tmp/x\t# tab",
		strings.Repeat("a", 200),
		"git commit -m \"" + strings.Repeat("word ", 40) + "\"",
		"日本語の コマンド 🐎 with e\U00000301 and 中文",
	}
	for _, cmd := range cmds {
		for _, w := range []int{20, 37, 80} {
			got := Dialog("Bash", []cell.Line{cell.Text(cmd)}, nil, 0, w, MonoTheme())
			var sb strings.Builder
			for _, l := range got[1 : len(got)-1] {
				row := l.Plain()
				row = strings.TrimPrefix(row, "│ ")
				row = strings.TrimSuffix(strings.TrimRight(row, " "), "│")
				row = strings.TrimPrefix(row, "↳")
				// a trailing space of the command can be cut off by the trim: compare what was not blank
				sb.WriteString(row)
			}
			want := strings.ReplaceAll(expandTabStops(cmd, 4), "\t", " ")
			if strings.TrimRight(strings.ReplaceAll(sb.String(), " ", ""), " ") != strings.ReplaceAll(want, " ", "") {
				t.Errorf("w=%d: the command changed:\n got: %q\nwant: %q", w, sb.String(), want)
			}
			if w == 80 && strings.Contains(cmd, "  ") && !strings.Contains(widgettest.Flatten(got), "echo  'two  spaces'   && rm -rf /tmp/x") {
				t.Errorf("double spaces were not kept:\n%s", widgettest.Flatten(got))
			}
		}
	}
}

func TestDialogNoBodyNoOptions(t *testing.T) {
	got := widgettest.Flatten(Dialog("Just a title", nil, nil, 0, 24, MonoTheme()))
	if got != "╭─ Just a title ───────╮\n╰──────────────────────╯" {
		t.Errorf("%s", got)
	}
	if Dialog("x", nil, nil, 0, 0, MonoTheme()) != nil {
		t.Error("width 0 gives nil")
	}
}

func TestDialogKeys(t *testing.T) {
	opts := PermissionOptions("x")
	cases := []struct {
		key  string
		want int
		ok   bool
	}{
		{"1", 0, true}, {"2", 1, true}, {"3", 2, true}, {"4", 0, false}, {"0", 0, false}, {"9", 0, false},
		{"y", 0, true}, {"Y", 0, true}, {"a", 1, true}, {"A", 1, true}, {"n", 2, true}, {"N", 2, true},
		{"esc", 2, true}, {"ESC", 2, true}, {"escape", 2, true}, {"Escape", 2, true},
		{"", 0, false}, {" ", 0, false}, {"enter", 0, false}, {"q", 0, false}, {"yy", 0, false}, {"10", 0, false}, {"ctrl+c", 0, false},
		{" y ", 0, true},
	}
	for _, c := range cases {
		got, ok := DialogKeys(c.key, opts)
		if got != c.want || ok != c.ok {
			t.Errorf("DialogKeys(%q) = %d, %v; want %d, %v", c.key, got, ok, c.want, c.ok)
		}
	}
	if i, ok := DialogKeys("1", nil); ok || i != 0 {
		t.Error("no options: nothing is chosen")
	}
	// explicit keys come first, and the first option to claim a key wins
	custom := []DialogOption{{Label: "a", Keys: []string{"x"}}, {Label: "b", Keys: []string{"x", "2"}}, {Label: "c", Keys: []string{"return", "space"}}}
	if i, ok := DialogKeys("x", custom); i != 0 || !ok {
		t.Error("the first wins")
	}
	if i, ok := DialogKeys("2", custom); i != 1 || !ok {
		t.Error("an explicit digit key")
	}
	if i, ok := DialogKeys("enter", custom); i != 2 || !ok {
		t.Error("return is enter")
	}
	if i, ok := DialogKeys(" ", custom); i != 2 || !ok {
		t.Error("the space key")
	}
	if i, ok := DialogKeys("1", custom); i != 0 || !ok {
		t.Error("the number of an option works when no key list claims it")
	}
}

func TestPermissionOptions(t *testing.T) {
	o := PermissionOptions("go test")
	if len(o) != 3 || o[0].Label != "Yes" || o[1].Label != "Yes, and don't ask again for go test" ||
		o[2].Label != "No, and tell Sleipnir what to do instead" || o[2].Hint != "(esc)" {
		t.Errorf("%+v", o)
	}
	if got := PermissionOptions("")[1].Label; got != "Yes, and don't ask again" {
		t.Errorf("no scope: %q", got)
	}
	if got := PermissionOptions("a\x1b[31mb\nc")[1].Label; got != "Yes, and don't ask again for ab c" {
		t.Errorf("a scope is cleaned: %q", got)
	}
}

func TestDialogHostileText(t *testing.T) {
	for _, h := range widgettest.Hostile() {
		opts := []DialogOption{{Label: h, Hint: h, Keys: []string{h}}, {Label: "ok"}}
		for _, th := range []Theme{DefaultTheme(), MonoTheme()} {
			for _, w := range []int{10, 30, 80} {
				got := Dialog(h, []cell.Line{cell.Text(h)}, opts, 0, w, th)
				checkLines(t, "hostile dialog", got, w)
			}
		}
	}
}

func TestDialogPropertyRandomAtEveryWidth(t *testing.T) {
	g := widgettest.NewRand(21)
	for i := 0; i < 40; i++ {
		var opts []DialogOption
		for j := 0; j < 1+g.Intn(4); j++ {
			opts = append(opts, DialogOption{Label: g.Text(1 + g.Intn(10)), Hint: g.Text(g.Intn(2))})
		}
		body := plainLines(g.Text(g.Intn(15)), g.Text(g.Intn(3)))
		for _, nt := range allTestThemes() {
			for w := 10; w <= 120; w += 7 {
				sel := g.Intn(len(opts) + 2)
				got := Dialog(g.Text(g.Intn(4)), body, opts, sel, w, nt.th)
				checkLines(t, "random dialog/"+nt.name, got, w)
			}
		}
	}
}

func TestDialogIsDeterministic(t *testing.T) {
	a := Dialog("T", dialogBody(), PermissionOptions("x"), 1, 50, DefaultTheme())
	b := Dialog("T", dialogBody(), PermissionOptions("x"), 1, 50, DefaultTheme())
	if !reflect.DeepEqual(a, b) {
		t.Error("not deterministic")
	}
}
