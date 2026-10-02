package main

// Output that a terminal of 80 columns breaks in the middle of a word: the help of a command, a table, an error. wrapBlock and what uses
// it (printHelp, printFlags, reportError, printModelsWidth, printSessions) fit what is written to the terminal it goes to, and leave what
// goes anywhere else as it was (docs/CLI.md is made from the help of a pipe). e2e_width_test.go runs the real commands on a terminal.

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// atWidth makes every writer a terminal of w columns for the length of the test.
func atWidth(t *testing.T, w int) {
	t.Helper()
	old := termWidth
	termWidth = func(io.Writer) int { return w }
	t.Cleanup(func() { termWidth = old })
}

// tooWide are the lines of text wider than n characters.
func tooWide(text string, n int) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if utf8.RuneCountInString(l) > n {
			out = append(out, l)
		}
	}
	return out
}

func TestWrapBlock(t *testing.T) {
	const w = 30
	for _, tc := range []struct{ name, in, want string }{
		{"text that fits is left as it was, blank lines and the final newline too", "one\n\n  two  words\n", "one\n\n  two  words\n"},
		{"a long line goes on flush left", "aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj kkkk", "aaaa bbbb cccc dddd eeee ffff\ngggg hhhh iiii jjjj kkkk"},
		{"an indented line goes on under its first word", "    aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii", "    aaaa bbbb cccc dddd eeee\n    ffff gggg hhhh iiii"},
		{"a table row goes on under its text, so the table keeps its columns", "  taskgen    make tasks from git history, compose swarm tasks or mutations",
			"  taskgen    make tasks from\n             git history,\n             compose swarm\n             tasks or\n             mutations"},
		{"a usage line goes on under the command", "usage: sleipnir rl rollout --tasks tasks.jsonl --model MODEL", "usage: sleipnir rl rollout\n       --tasks tasks.jsonl\n       --model MODEL"},
		{"a word wider than the width stays whole", "  /a/very/long/path/that/cannot/be/broken/anywhere/at/all/really and more", "  /a/very/long/path/that/cannot/be/broken/anywhere/at/all/really\n  and more"},
		// A paragraph broken by hand at the margin, whose last line runs a little over: flowed again, with no word left alone on a line.
		{"a hand-wrapped paragraph is flowed as a whole", "Asks which provider and for its key, and keeps the key in\n~/.sleipnir/auth.json, readable by you only.",
			"Asks which provider and for\nits key, and keeps the key in\n~/.sleipnir/auth.json,\nreadable by you only."},
		{"the short lines of a list are not one paragraph", "  one two three four five six seven eight nine ten eleven\n  a\n  b", "  one two three four five six\n  seven eight nine ten eleven\n  a\n  b"},
		{"the items of a list are not joined", "- one two three four five six seven eight\n- nine ten eleven twelve thirteen fourteen", "- one two three four five six\n  seven eight\n- nine ten eleven twelve\n  thirteen fourteen"},
	} {
		if got := wrapBlock(tc.in, w); got != tc.want {
			t.Errorf("%s:\n%q\nwant\n%q", tc.name, got, tc.want)
		}
	}
	row := "  add [--yes]    say yes to those files as they are now (shows them and asks first)" // the name of a row may have a space in it
	if got, want := wrapBlock(row, 50), "  add [--yes]    say yes to those files as they\n                 are now (shows them and asks\n                 first)"; got != want {
		t.Errorf("a table row with a space in its name:\n%q\nwant\n%q", got, want)
	}
	if got := wrapBlock("a very long line of words that would be wrapped at any real width", 10); strings.Contains(got, "\n") {
		t.Errorf("a window of 10 columns is no window: %q", got)
	}
	if lines := tooWide(wrapBlock(strings.Repeat("lorem ipsum dolor ", 30)+"\n  name   "+strings.Repeat("sit amet ", 30), 40), 40); len(lines) != 0 {
		t.Errorf("too wide: %q", lines)
	}
}

func TestPrintHelpFitsATerminalAndLeavesAFileAlone(t *testing.T) {
	text := "usage: sleipnir x [flags]\n\n  name     " + strings.Repeat("a description that is much too long for the line ", 4) + "\n"
	var file bytes.Buffer
	printHelp(&file, text)
	if file.String() != text {
		t.Errorf("help written to a file was changed:\n%s", file.String())
	}
	atWidth(t, 80)
	var term bytes.Buffer
	printHelp(&term, text)
	if lines := tooWide(term.String(), 79); len(lines) != 0 || term.String() == text {
		t.Errorf("help written to a terminal of 80 columns:\n%s", term.String())
	}
}

func TestPrintFlagsFitsTheTerminalAndKeepsThePipeAsItWas(t *testing.T) {
	build := func() (*flag.FlagSet, *bytes.Buffer) {
		var out bytes.Buffer
		fs := newFlagSet("x", flag.ContinueOnError)
		fs.SetOutput(&out)
		fs.String("allow", "", "a permission rule that needs no question in this run, repeatable: 'Bash(go test:*)', 'Edit(docs/**)'; the name tests stands for the build and test commands of most projects")
		fs.Bool("quiet", false, "print only the final answer")
		fs.Int("n", 20, "how many")
		return fs, &out
	}
	fs, pipe := build()
	fs.Usage()
	want := "Usage of x:\n  -allow string\n    \ta permission rule that needs no question in this run, repeatable: 'Bash(go test:*)', 'Edit(docs/**)'; the name tests stands for the build and test commands of most projects\n" +
		"  -n int\n    \thow many (default 20)\n  -quiet\n    \tprint only the final answer\n"
	if pipe.String() != want {
		t.Errorf("the help of a pipe changed:\n%q\nwant\n%q", pipe.String(), want)
	}
	atWidth(t, 80)
	fs, term := build()
	fs.Usage()
	if lines := tooWide(term.String(), 79); len(lines) != 0 {
		t.Errorf("flag help on a terminal of 80 columns has %d too wide:\n%s", len(lines), term.String())
	}
	for _, word := range strings.Fields(want) {
		if word != "\t" && !strings.Contains(term.String(), word) {
			t.Errorf("a word of the help is gone: %q\n%s", word, term.String())
		}
	}
	if !strings.Contains(term.String(), "  -allow string\n        a permission rule") {
		t.Errorf("a description starts under its flag, indented:\n%s", term.String())
	}
}

func TestReportErrorFitsTheTerminal(t *testing.T) {
	long := errors.New("init: /home/someone/projects/a/quite/deeply/nested/place/.sleipnir/config.json already exists; edit it, or use `sleipnir config` to inspect it")
	var pipe bytes.Buffer
	reportError(&pipe, long)
	if pipe.String() != "sleipnir: "+long.Error()+"\n" {
		t.Errorf("an error written to a file was changed: %q", pipe.String())
	}
	atWidth(t, 80)
	var term bytes.Buffer
	reportError(&term, long)
	if lines := tooWide(term.String(), 79); len(lines) != 0 || strings.Count(term.String(), "\n") < 2 {
		t.Errorf("an error on a terminal of 80 columns:\n%s", term.String())
	}
	// A message of several lines keeps them.
	term.Reset()
	reportError(&term, errors.New("two problems:\n  one\n  two"))
	if term.String() != "sleipnir: two problems:\n  one\n  two\n" {
		t.Errorf("a short multi-line error was changed: %q", term.String())
	}
}

func TestModelsTableFitsTheTerminal(t *testing.T) {
	rows := []modelRow{
		row("heimdall/audnai/penclaw-glm-5.3-abliterated", 131_000, 0.16, "tools", "reasoning"),
		row("heimdall/qwen/qwen3.8-flash-next", 1_000_000, 0.035, "tools"),
		row("openrouter/some-lab/a-model-with-a-really-quite-long-name-indeed", 256_000, 2.5),
	}
	fav := map[string]bool{"heimdall/qwen/qwen3.8-flash-next": true}
	table := func(rows []modelRow, width int) string {
		var b bytes.Buffer
		if err := printModelsWidth(&b, rows, modelFilter{}, fav, width); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	header := func(s string) string { return strings.Join(strings.Fields(strings.SplitN(s, "\n", 2)[0]), " ") }
	normal := rows[:2] // the longest reference is 43 characters: the whole table is 99 wide
	for _, c := range []struct {
		width int
		want  string
	}{
		{0, "MODEL CONTEXT $/M IN $/M CACHED $/M OUT TOOLS REASONING"}, // not a terminal: every column
		{110, "MODEL CONTEXT $/M IN $/M CACHED $/M OUT TOOLS REASONING"},
		{95, "MODEL CONTEXT $/M IN $/M CACHED $/M OUT TOOLS"},
		{85, "MODEL CONTEXT $/M IN $/M OUT TOOLS"},
		{70, "MODEL CONTEXT $/M OUT TOOLS"},
	} {
		got := table(normal, c.width)
		if g := header(got); g != c.want {
			t.Errorf("at %d columns the columns are %q, want %q", c.width, g, c.want)
		}
		if c.width > 0 {
			if lines := tooWide(got, c.width-1); len(lines) != 0 {
				t.Errorf("at %d columns:\n%s", c.width, got)
			}
		}
	}
	got := table(rows, 50) // a reference gives way only when the columns that tell less are gone
	if lines := tooWide(got, 49); len(lines) != 0 || !strings.Contains(got, "…") || !strings.Contains(got, "* heimdall/qwen/qwen3.8…") {
		t.Errorf("at 50 columns:\n%s", got)
	}
	if first := strings.Fields(strings.Split(got, "\n")[1])[0]; first != "*" {
		t.Errorf("a favorite comes first: %q", first)
	}
}

func TestSessionsListFitsTheTerminal(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "20261002-023227-7ba0c7")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"seq":1,"type":"session.start","data":{"model":"heimdall/deepseek/deepseek-v4.1-flash"}}` + "\n" +
		`{"seq":2,"type":"user.input","data":{"text":"Fix the failing test in the slugify package, and add one for the case of a title with accents in it, then run everything"}}` + "\n"
	if err := os.WriteFile(filepath.Join(d, "events.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	list := func() (out, note string) {
		var o, n bytes.Buffer
		if err := printSessions(&o, &n, dir, 5); err != nil {
			t.Fatal(err)
		}
		return o.String(), n.String()
	}
	pipe, _ := list()
	if !strings.Contains(pipe, "heimdall/deepseek/deepseek-v4.1-flash") || !strings.Contains(pipe, "Fix the failing test") {
		t.Errorf("a listing that goes to a pipe has the model and the prompt: %q", pipe)
	}
	atWidth(t, 80)
	out, note := list()
	if lines := tooWide(out, 79); len(lines) != 0 || !strings.Contains(out, "Fix the failing test") || !strings.Contains(out, "…") || strings.Contains(out, "heimdall/") {
		t.Errorf("a listing on a terminal of 80 columns:\n%s", out)
	}
	if note != "" && len(tooWide(note, 79)) != 0 {
		t.Errorf("the note under the listing:\n%s", note)
	}
}

func TestOneLineCLICutsWholeCharacters(t *testing.T) {
	if got := oneLineCLI("ça va très bien aujourd'hui", 6); got != "ça va …" || !utf8.ValidString(got) {
		t.Errorf("got %q", got)
	}
}
