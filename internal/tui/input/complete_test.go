package input

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

func TestFuzzyRanking(t *testing.T) {
	order := func(q string, texts ...string) []string {
		return rank(q, texts, func(s string) string { return s })
	}
	cases := []struct {
		name  string
		query string
		in    []string
		want  []string
	}{
		{"prefix, then word boundary, then subsequence", "rev",
			[]string{"preview", "xrevx", "git-review", "reverse", "review", "rvw", "abc"},
			[]string{"review", "reverse", "git-review", "xrevx", "preview"}},
		{"acronyms: tighter first, prefix before acronym, subsequence last", "gc",
			[]string{"logic", "git-commit", "go-cache", "gcc"},
			[]string{"gcc", "go-cache", "git-commit", "logic"}},
		{"an exact-case prefix beats a prefix that differs in case", "Re", []string{"review", "Review", "REVIEW"}, []string{"Review", "review", "REVIEW"}},
		{"shorter text first among equal prefixes", "co", []string{"compact", "cost", "co", "config"}, []string{"co", "cost", "config", "compact"}},
		{"an earlier word beats a later one", "b", []string{"x-y-b", "x-b", "a-b-c"}, []string{"x-b", "a-b-c", "x-y-b"}},
		{"camel case humps are words", "gc", []string{"gitCommit", "gopher"}, []string{"gitCommit"}},
		{"case is ignored", "CLEAR", []string{"clear", "Clear"}, []string{"clear", "Clear"}},
		{"empty query keeps the order given", "", []string{"b", "a", "c"}, []string{"b", "a", "c"}},
		{"no match", "xyz", []string{"abc", "xy"}, nil},
		{"order of the subsequence matters", "ba", []string{"ab"}, nil},
		{"unicode", "é", []string{"x-é", "Élan", "zzz"}, []string{"Élan", "x-é"}},
		{"ties keep the order given", "a", []string{"ab", "ac", "ad"}, []string{"ab", "ac", "ad"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := order(c.query, c.in...); !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Errorf("rank(%q, %q) = %q, want %q", c.query, c.in, got, c.want)
			}
		})
	}
}

func TestFuzzyTiersNeverOverlap(t *testing.T) {
	long := strings.Repeat("ab ", 1300) // 3900 runes
	prefix, _ := Fuzzy("a", long)
	boundary, _ := Fuzzy("b", long)
	sub, _ := Fuzzy("ba", long+"a") // a subsequence only
	if !(prefix > boundary && boundary > sub) {
		t.Errorf("tiers: prefix %d boundary %d subsequence %d", prefix, boundary, sub)
	}
	if _, ok := Fuzzy("a", strings.Repeat("a", 5000)); ok {
		t.Error("a text over 4096 runes is not matched (bounded work)")
	}
	if _, ok := Fuzzy("abc", "ab"); ok {
		t.Error("a query longer than the text cannot match")
	}
}

var testCommands = []Command{
	{Name: "help", Description: "show help"},
	{Name: "/clear", Description: "clear the screen"},
	{Name: "compact", Args: "[focus]", Description: "fold the context"},
	{Name: "cost", Description: "show spend"},
	{Name: "  config ", Description: "settings"},
	{Name: "", Description: "nameless"},
	{Name: "bad name", Description: "has a space"},
	{Name: "ctl\x1bname", Description: "has a control character"},
	{Name: "review", Description: "review the diff"},
}

func candTexts(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Text)
	}
	return out
}

func TestSlashCommandsCompleter(t *testing.T) {
	c := SlashCommands(testCommands)
	from, cands := c.Complete("/", 1)
	if from != 0 || !reflect.DeepEqual(candTexts(cands), []string{"/help ", "/clear ", "/compact ", "/cost ", "/config ", "/review "}) {
		t.Errorf("all commands in the order given, bad names dropped: %d %q", from, candTexts(cands))
	}
	if cands[2].Display != "/compact" || cands[2].Detail != "[focus]  fold the context" || cands[0].Detail != "show help" {
		t.Errorf("display and detail: %+v %+v", cands[2], cands[0])
	}
	_, cands = c.Complete("/co", 3)
	if got := candTexts(cands); !reflect.DeepEqual(got, []string{"/cost ", "/config ", "/compact "}) {
		t.Errorf("/co: %q", got)
	}
	_, cands = c.Complete("/cmp", 4)
	if got := candTexts(cands); !reflect.DeepEqual(got, []string{"/compact "}) {
		t.Errorf("/cmp (subsequence): %q", got)
	}
	_, cands = c.Complete("/RE", 3)
	if got := candTexts(cands); !reflect.DeepEqual(got, []string{"/review "}) {
		t.Errorf("case: %q", got)
	}
	for _, line := range []string{"", "help", " /he", "/he lp", "/help arg", "a/b"} {
		if _, cands := c.Complete(line, len(line)); len(cands) != 0 {
			t.Errorf("%q should not complete: %q", line, candTexts(cands))
		}
	}
	// only the text before the cursor counts
	if _, cands := c.Complete("/cost and more", 3); !reflect.DeepEqual(candTexts(cands), []string{"/cost ", "/config ", "/compact "}) {
		t.Errorf("cursor in the middle: %q", candTexts(cands))
	}
	if _, cands := c.Complete("/x", 9); len(cands) != 0 {
		t.Error("a cursor past the end is ignored")
	}
	if _, cands := SlashCommands(nil).Complete("/", 1); len(cands) != 0 {
		t.Error("no commands, no candidates")
	}
}

// menuRig is a rig with the slash commands installed.
func menuRig(t *testing.T) *rig {
	return newRig(t, Options{Completer: SlashCommands(testCommands)})
}

func TestCompletionOpensOnSlashAndFiltersAsYouType(t *testing.T) {
	r := menuRig(t)
	evs := r.send("/")
	if !reflect.DeepEqual(evs, []Event{CompletionOpened{}, Changed{}}) {
		t.Errorf("typing / opens the menu: %v", evs)
	}
	m := r.ed.Completion()
	if !m.Open || len(m.Candidates) != 6 || m.Selected != 0 || m.From != 0 {
		t.Fatalf("%+v", m)
	}
	r.send("co")
	if got := candTexts(r.ed.Completion().Candidates); !reflect.DeepEqual(got, []string{"/cost ", "/config ", "/compact "}) {
		t.Errorf("filtered: %q", got)
	}
	evs = r.send("mp")
	if got := candTexts(r.ed.Completion().Candidates); !reflect.DeepEqual(got, []string{"/compact "}) {
		t.Errorf("filtered again: %q", got)
	}
	if !reflect.DeepEqual(withoutChanged(evs), []Event(nil)) {
		t.Errorf("a menu that stays open says nothing: %v", evs)
	}
	evs = r.send("zzz") // nothing matches: the menu goes away
	if r.ed.Completion().Open || !reflect.DeepEqual(withoutChanged(evs), []Event{CompletionClosed{}}) {
		t.Errorf("no match closes it: %v", evs)
	}
}

func TestCompletionAcceptWithTabAndEnter(t *testing.T) {
	for _, key := range []string{kTab, kEnter} {
		r := menuRig(t)
		r.send("/he")
		evs := r.send(key)
		if r.state() != "/help |" {
			t.Errorf("%q: %q", key, r.state())
		}
		if !reflect.DeepEqual(withoutChanged(evs), []Event{CompletionClosed{}}) {
			t.Errorf("%q: events %v (Enter accepts, it does not submit)", key, evs)
		}
		if r.ed.Completion().Open {
			t.Error("the menu is closed after accepting")
		}
		r.send("arg")
		if r.state() != "/help arg|" {
			t.Errorf("typing on: %q", r.state())
		}
	}
}

func TestCompletionNavigation(t *testing.T) {
	r := menuRig(t)
	r.send("/")
	sel := func() int { return r.ed.Completion().Selected }
	r.send(kDown, kDown)
	if sel() != 2 {
		t.Errorf("down: %d", sel())
	}
	r.send(kUp, kUp, kUp) // wraps around
	if sel() != 5 {
		t.Errorf("up wraps: %d", sel())
	}
	r.send(kDown)
	if sel() != 0 {
		t.Errorf("down wraps: %d", sel())
	}
	r.send(ctrl('n'), ctrl('n'), ctrl('p'))
	if sel() != 1 {
		t.Errorf("ctrl+n ctrl+p: %d", sel())
	}
	r.send(kShiftTab)
	if sel() != 0 {
		t.Errorf("shift+tab: %d", sel())
	}
	r.send(kPgDn)
	if sel() != 5 {
		t.Errorf("page down stops at the end: %d", sel())
	}
	r.send(kPgUp)
	if sel() != 0 {
		t.Errorf("page up stops at the start: %d", sel())
	}
	r.send(kDown, kDown, kTab) // /compact
	if r.state() != "/compact |" {
		t.Errorf("accept the selected one: %q", r.state())
	}
	// the selection stays on the same candidate when the list is refiltered
	r = menuRig(t)
	r.send("/", kDown, kDown, kDown) // /cost
	r.send("o")                      // "/o": /cost, /config... the same candidate stays selected if it is still there
	if m := r.ed.Completion(); m.Open && m.Candidates[m.Selected].Text != "/cost " {
		t.Errorf("selection follows the candidate: %+v", m)
	}
}

func TestCompletionEscCloses(t *testing.T) {
	r := menuRig(t)
	r.send("/he")
	evs := withoutChanged(r.send(kEsc))
	if !reflect.DeepEqual(evs, []Event{CompletionClosed{}}) || r.state() != "/he|" || r.ed.EscArmed() {
		t.Errorf("Esc closes the menu and nothing else: %v %q armed=%v", evs, r.state(), r.ed.EscArmed())
	}
	// typing on in the same word does not open it again
	r.send("l")
	if r.ed.Completion().Open {
		t.Error("a menu the user closed stays closed for that word")
	}
	// a new word does
	r.send(" /c")
	if r.ed.Completion().Open {
		t.Error("/ is only a command at the start of a line")
	}
	r.send(kAltEnter, "/c")
	if !r.ed.Completion().Open {
		t.Error("a slash at the start of a line opens it")
	}
	// Esc with no menu goes back to its usual job: arm the clear
	r.send(kEsc, kEsc)
	r.send(kEsc)
	if !r.ed.EscArmed() && !r.ed.Empty() {
		t.Error("Esc with no menu arms the clear")
	}
}

func TestCompletionDismissedWordReopensAfterLeavingIt(t *testing.T) {
	r := menuRig(t)
	r.send("/he", kEsc)
	r.send(kBS, kBS, kBS) // the word is gone
	r.send("/")
	if !r.ed.Completion().Open {
		t.Error("a fresh / opens the menu again")
	}
}

func TestCompletionFollowsTheCursorAndEdits(t *testing.T) {
	r := menuRig(t)
	r.send("/co")
	r.send(kBS)
	if m := r.ed.Completion(); !m.Open || len(m.Candidates) != 4 {
		t.Errorf("backspace inside the word keeps the menu, refiltered to /c: %+v", m)
	}
	r.send(kBS)
	if m := r.ed.Completion(); !m.Open || len(m.Candidates) != 6 {
		t.Errorf("down to the bare slash: all commands: %+v", m)
	}
	r.send(kBS) // the slash itself
	if r.ed.Completion().Open {
		t.Error("deleting the slash closes it")
	}

	r.ed.Reset()
	r.send("/c", kLeft)
	if !r.ed.Completion().Open {
		t.Error("the cursor is still in the word")
	}
	r.send(kLeft) // now before the slash
	if r.ed.Completion().Open {
		t.Error("the cursor left the word")
	}

	r.ed.Reset()
	r.send("/help x", kLeft, kLeft)
	if r.ed.Completion().Open {
		t.Error("a cursor after a space is not in the command word")
	}
	r.send(kLeft, kLeft)
	if r.ed.Completion().Open {
		t.Error("moving does not open a menu by itself, only typing does")
	}
}

func TestCompletionAtFilesWordAnywhere(t *testing.T) {
	c := CompleterFunc(func(line string, cursor int) (int, []Candidate) {
		i := strings.LastIndex(line[:cursor], "@")
		return i, []Candidate{{Text: "@file.go "}, {Text: "@fine.txt "}}
	})
	r := newRig(t, Options{Completer: c})
	r.send("see @f")
	if !r.ed.Completion().Open || r.ed.Completion().From != 4 {
		t.Errorf("an @ word opens the menu anywhere: %+v", r.ed.Completion())
	}
	r.send(kTab)
	if r.state() != "see @file.go |" {
		t.Errorf("%q", r.state())
	}
	r.ed.Reset()
	r.send("mail me at a@b")
	if r.ed.Completion().Open {
		t.Error("an @ inside a word (an email address) is not a path")
	}
}

func TestTabCompletion(t *testing.T) {
	var calls []string
	c := CompleterFunc(func(line string, cursor int) (int, []Candidate) {
		calls = append(calls, fmt.Sprintf("%q@%d", line, cursor))
		w := strings.LastIndex(line[:cursor], " ") + 1
		switch line[w:cursor] {
		case "on":
			return w, []Candidate{{Text: "only "}}
		case "t":
			return w, []Candidate{{Text: "two "}, {Text: "three "}, {Text: "ten "}}
		}
		return 0, nil
	})
	r := newRig(t, Options{Completer: c})
	r.send("say on", kTab)
	if r.state() != "say only |" || r.ed.Completion().Open {
		t.Errorf("one candidate is applied at once: %q", r.state())
	}
	r.send("t")
	if r.ed.Completion().Open {
		t.Error("an ordinary word does not open a menu by typing")
	}
	evs := r.send(kTab)
	if !r.ed.Completion().Open || len(r.ed.Completion().Candidates) != 3 || !reflect.DeepEqual(withoutChanged(evs), []Event{CompletionOpened{}}) {
		t.Errorf("Tab opens a menu for several candidates: %+v %v", r.ed.Completion(), evs)
	}
	r.send(kTab, kTab)
	if m := r.ed.Completion(); !m.Open || m.Selected != 2 {
		t.Errorf("Tab on a menu that Tab opened cycles: %+v", m)
	}
	r.send(kTab)
	if m := r.ed.Completion(); m.Selected != 0 {
		t.Errorf("and wraps: %+v", m)
	}
	r.send(kTab, kEnter)
	if r.state() != "say only three |" {
		t.Errorf("Enter accepts: %q", r.state())
	}
	before := r.ed.Text()
	r.send("zz", kTab)
	if r.ed.Text() != before+"zz" || r.ed.Completion().Open {
		t.Errorf("no candidates: Tab does nothing: %q", r.state())
	}
	if len(calls) == 0 || calls[0] != `"say on"@6` {
		t.Errorf("the completer sees the line and the byte offset of the cursor: %q", calls)
	}
}

func TestCompleterSeesTheCurrentLineAndByteOffsets(t *testing.T) {
	var gotLine string
	var gotCur int
	c := CompleterFunc(func(line string, cursor int) (int, []Candidate) {
		gotLine, gotCur = line, cursor
		return cursor, nil
	})
	r := newRig(t, Options{Completer: c})
	r.send("first line", kAltEnter, "é中 @", kTab)
	if gotLine != "é中 @" || gotCur != len("é中 @") {
		t.Errorf("line %q cursor %d (bytes)", gotLine, gotCur)
	}
	r.send(kLeft, kLeft, kTab)
	if gotLine != "é中 @" || gotCur != len("é中") {
		t.Errorf("mid-line: %q %d", gotLine, gotCur)
	}
	r.ed.Reset()
	r.send(paste(strings.Repeat("a long line\n", 10)), "x", kTab)
	if gotLine != "\U0000fffcx" {
		t.Errorf("a chip is one U+FFFC for the completer: %q", gotLine)
	}
}

func TestCompleterMisbehaviourMakesNoMenu(t *testing.T) {
	for name, f := range map[string]func(string, int) (int, []Candidate){
		"from past the cursor": func(l string, c int) (int, []Candidate) { return c + 1, []Candidate{{Text: "x"}} },
		"negative from":        func(l string, c int) (int, []Candidate) { return -1, []Candidate{{Text: "x"}} },
		"from inside a rune":   func(l string, c int) (int, []Candidate) { return 1, []Candidate{{Text: "x"}} },
		"no candidates":        func(l string, c int) (int, []Candidate) { return 0, nil },
		"only empty texts":     func(l string, c int) (int, []Candidate) { return 0, []Candidate{{Text: ""}, {Text: "\x1b"}} },
	} {
		r := newRig(t, Options{Completer: CompleterFunc(f)})
		r.send("é", kTab)
		if r.ed.Completion().Open {
			t.Errorf("%s: a menu opened", name)
		}
		if r.ed.Text() != "é" {
			t.Errorf("%s: the text changed: %q", name, r.ed.Text())
		}
	}
}

func TestCandidatesAreCleaned(t *testing.T) {
	c := CompleterFunc(func(line string, cursor int) (int, []Candidate) {
		return 0, []Candidate{
			{Text: "/\x1b]52;c;ZXZpbA==\x07evil", Display: "\x1b[31mred\x1b[0m", Detail: "line one\nline two\t!"},
			{Text: "/ok", Display: "", Detail: ""},
		}
	})
	r := newRig(t, Options{Completer: c})
	r.send("/")
	cs := r.ed.Completion().Candidates
	if len(cs) != 2 || cs[0].Text != "/evil" || cs[0].Display != "red" || cs[0].Detail != "line one line two !" || cs[1].Display != "/ok" {
		t.Fatalf("%+v", cs)
	}
	for _, l := range r.ed.View(40).Plain() {
		if strings.ContainsAny(l, "\x1b\x07") {
			t.Errorf("escape in the view: %q", l)
		}
	}
}

func TestCandidatesAreCapped(t *testing.T) {
	c := CompleterFunc(func(line string, cursor int) (int, []Candidate) {
		var cs []Candidate
		for i := 0; i < 5000; i++ {
			cs = append(cs, Candidate{Text: fmt.Sprint("/c", i)})
		}
		return 0, cs
	})
	r := newRig(t, Options{Completer: c})
	r.send("/")
	if n := len(r.ed.Completion().Candidates); n != maxCands {
		t.Errorf("%d candidates kept", n)
	}
}

func TestDirectoryCandidateKeepsTheMenuOpen(t *testing.T) {
	tree := map[string][]string{"": {"@src/", "@README.md "}, "src/": {"@src/a.go ", "@src/sub/"}, "src/sub/": {"@src/sub/deep.go "}}
	c := CompleterFunc(func(line string, cursor int) (int, []Candidate) {
		ws := strings.LastIndex(line[:cursor], "@")
		w := line[ws:cursor]
		dir := ""
		if i := strings.LastIndex(w, "/"); i >= 0 {
			dir = w[1 : i+1]
		}
		var cs []Candidate
		for _, t := range tree[dir] {
			if strings.HasPrefix(t, w) {
				cs = append(cs, Candidate{Text: t})
			}
		}
		return ws, cs
	})
	r := newRig(t, Options{Completer: c})
	r.send("@s")
	evs := withoutChanged(r.send(kTab))
	if r.state() != "@src/|" || !r.ed.Completion().Open {
		t.Fatalf("a directory stays open: %q %+v", r.state(), r.ed.Completion())
	}
	if !reflect.DeepEqual(candTexts(r.ed.Completion().Candidates), []string{"@src/a.go ", "@src/sub/"}) {
		t.Errorf("the menu shows what is inside: %q", candTexts(r.ed.Completion().Candidates))
	}
	if !reflect.DeepEqual(evs, []Event{CompletionClosed{}, CompletionOpened{}}) {
		t.Errorf("events: %v", evs)
	}
	r.send(kDown, kEnter)
	if r.state() != "@src/sub/|" || !r.ed.Completion().Open {
		t.Fatalf("and one level deeper: %q", r.state())
	}
	r.send(kTab)
	if r.state() != "@src/sub/deep.go |" || r.ed.Completion().Open {
		t.Errorf("a file ends it: %q", r.state())
	}
}

func TestMenuView(t *testing.T) {
	var cands []Candidate
	for i := 0; i < 20; i++ {
		cands = append(cands, Candidate{Text: fmt.Sprintf("/cmd%02d ", i), Display: fmt.Sprintf("/cmd%02d", i), Detail: fmt.Sprintf("does thing %d", i)})
	}
	c := CompleterFunc(func(line string, cursor int) (int, []Candidate) { return 0, cands })
	r := newRig(t, Options{Completer: c})
	r.send("/")
	v := r.ed.View(40)
	if v.InputRows != 1 || len(v.Lines) != 1+menuRows {
		t.Fatalf("rows: input %d total %d", v.InputRows, len(v.Lines))
	}
	plain := v.Plain()
	if !strings.HasPrefix(plain[1], "▸ /cmd00  ") || !strings.Contains(plain[1], "does thing 0") {
		t.Errorf("selected row: %q", plain[1])
	}
	if !strings.HasPrefix(plain[2], "  /cmd01") {
		t.Errorf("other rows: %q", plain[2])
	}
	if !strings.HasSuffix(plain[menuRows], " ↓") || strings.Contains(plain[1], "↑") {
		t.Errorf("more below, none above: %q / %q", plain[1], plain[menuRows])
	}
	for i, l := range v.Lines[1:] {
		if l.Width() != 40 {
			t.Errorf("row %d is %d cells, want the full width: %q", i, l.Width(), l.Plain())
		}
	}
	// the selected row is the highlighted one
	if v.Lines[1][0].Style != r.ed.th.Selected || v.Lines[2][0].Style != r.ed.th.Menu {
		t.Error("styles of the selected and other rows")
	}
	if v.CursorRow != 0 || v.CursorCol != 3 {
		t.Errorf("the cursor stays in the input: %d,%d", v.CursorRow, v.CursorCol)
	}
	// scrolling: the selected row is always visible
	for i := 0; i < 12; i++ {
		r.send(kDown)
		m := r.ed.Completion()
		v = r.ed.View(40)
		sel := fmt.Sprintf("▸ /cmd%02d", m.Selected)
		found := false
		for _, l := range v.Plain()[1:] {
			found = found || strings.HasPrefix(l, sel)
		}
		if !found || len(v.Lines) != 1+menuRows {
			t.Fatalf("after %d downs the selected row %q is not visible in %q", i+1, sel, v.Plain())
		}
	}
	plain = v.Plain()
	if !strings.HasSuffix(plain[1], " ↑") || !strings.HasSuffix(plain[menuRows], " ↓") {
		t.Errorf("more on both sides: %q", plain)
	}
	// the last page has no down arrow
	r.send(kPgDn, kPgDn)
	plain = r.ed.View(40).Plain()
	if strings.HasSuffix(plain[menuRows], "↓") || !strings.HasPrefix(plain[menuRows], "▸ /cmd19") {
		t.Errorf("last page: %q", plain)
	}
	// narrow: rows never exceed the width
	for w := 4; w <= 30; w++ {
		for i, l := range r.ed.View(w).Lines {
			if l.Width() > w {
				t.Fatalf("width %d: row %d is %d cells: %q", w, i, l.Width(), l.Plain())
			}
		}
	}
}

func TestMenuWithFewCandidates(t *testing.T) {
	r := menuRig(t)
	r.send("/cmp")
	v := r.ed.View(60)
	if len(v.Lines) != 2 || !strings.HasPrefix(v.Lines[1].Plain(), "▸ /compact") {
		t.Errorf("%q", v.Plain())
	}
	if strings.ContainsAny(v.Lines[1].Plain(), "↑↓") {
		t.Error("no scroll arrows when everything is visible")
	}
	th := DefaultTheme()
	th.Marker = ""
	r2 := newRig(t, Options{Completer: SlashCommands(testCommands), Theme: &th})
	r2.send("/cmp")
	if got := r2.ed.View(60).Plain()[1]; !strings.HasPrefix(got, "> /compact") {
		t.Errorf("an empty marker falls back to '>': %q", got)
	}
	_ = cell.Text
}

func TestCtrlCWithMenuOpen(t *testing.T) {
	r := menuRig(t)
	r.send("/he")
	evs := withoutChanged(r.send(ctrl('c')))
	if !reflect.DeepEqual(evs, []Event{CompletionClosed{}, Interrupt{Cleared: true}}) || !r.ed.Empty() {
		t.Errorf("%v %q", evs, r.state())
	}
}
