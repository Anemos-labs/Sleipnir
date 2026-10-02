package main

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
)

func selectWith(t *testing.T, keys string, labels []string, searchable bool) (int, error) {
	t.Helper()
	rawMode = func() (func(), error) { return func() {}, nil }
	return selectRows(bufio.NewReader(strings.NewReader(keys)), io.Discard, "pick", labels, labels, searchable, 3)
}

// The menu is driven by the arrow keys; a digit jumps in a short list, typing narrows a long one, Esc or Ctrl-C backs out.
func TestSelectRowsArrowsDigitsSearchAndCancel(t *testing.T) {
	labels := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	if i, err := selectWith(t, "\x1b[B\x1b[B\r", labels, false); err != nil || i != 2 {
		t.Errorf("two downs: %d %v", i, err)
	}
	if i, err := selectWith(t, "\x1b[B\x1b[A\x1b[A\r", labels, false); err != nil || i != 0 {
		t.Errorf("up stops at the top: %d %v", i, err)
	}
	if i, err := selectWith(t, "4\r", labels, false); err != nil || i != 3 {
		t.Errorf("a digit jumps: %d %v", i, err)
	}
	if i, err := selectWith(t, "eps\r", labels, true); err != nil || i != 4 {
		t.Errorf("search: %d %v", i, err)
	}
	if i, err := selectWith(t, "a\x1b[B\r", labels, true); err != nil || i != 1 {
		t.Errorf("search then arrow (alpha, beta, gamma, delta match 'a'): %d %v", i, err)
	}
	if _, err := selectWith(t, "\x03", labels, false); err == nil {
		t.Error("ctrl-c backs out")
	}
	if _, err := selectWith(t, "zzz\r", labels, true); err == nil {
		t.Error("enter on nothing chooses nothing (the input ends: cancelled)")
	}
}

var ansiCodes = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// A row wider than the terminal is broken by it in the middle of a word, and a menu that counts its rows to draw them again counts it twice:
// at 80 columns the model menu's rows (a long id, the window, the price) did not fit. Every line of the menu, and the line that says what was
// chosen, fits.
func TestSelectRowsFitTheTerminalTheyAreOn(t *testing.T) {
	rawMode = func() (func(), error) { return func() {}, nil }
	old := termWidth
	termWidth = func(io.Writer) int { return 40 }
	t.Cleanup(func() { termWidth = old })
	labels := []string{"heimdall/audnai/penclaw-glm-5.3-abliterated    131k ctx  $0.16/M out  tools", "short"}
	var out bytes.Buffer
	i, err := selectRows(bufio.NewReader(strings.NewReader("abliterated\r")), &out, "Which model?", labels, labels, true, 3)
	if err != nil || i != 0 {
		t.Fatalf("the long row is chosen by what it holds: %d %v", i, err)
	}
	lines := strings.Split(out.String(), "\r\n")
	for _, l := range lines {
		plain := strings.TrimLeft(ansiCodes.ReplaceAllString(l, ""), "\r")
		if n := utf8.RuneCountInString(plain); n > 39 {
			t.Errorf("%d characters on a 40-column terminal: %q", n, plain)
		}
	}
	last := strings.TrimLeft(ansiCodes.ReplaceAllString(lines[len(lines)-2], ""), "\r")
	if !strings.HasPrefix(last, "Which model? heimdall/audnai/pen") || !strings.HasSuffix(last, "…") {
		t.Errorf("what was chosen is said on one line, cut where it must be: %q", last)
	}
}

func TestFitAndWrapFor(t *testing.T) {
	if fit("short", 10) != "short" || fit("short", 0) != "short" || fit("exactly", 7) != "exactly" || fit("a longer one", 6) != "a lon…" || fit("ab", 1) != "…" {
		t.Errorf("fit: %q %q %q %q", fit("short", 10), fit("exactly", 7), fit("a longer one", 6), fit("ab", 1))
	}
	if got := wrapFor(io.Discard, "one two three four five six seven"); got != "one two three four five six seven" {
		t.Errorf("not a terminal: the text as it is: %q", got)
	}
	old := termWidth
	termWidth = func(io.Writer) int { return 24 }
	t.Cleanup(func() { termWidth = old })
	if got := wrapFor(io.Discard, "one two three four five six seven"); got != "one two three four five\nsix seven" {
		t.Errorf("a terminal of 24 columns: %q", got)
	}
}

// The rows of the model menu are columns: "tools" starts in one place whatever the price in front of it takes (it did not: $0.019 is a
// character longer than $0.01), and no row is padded for a reference that is not in the list.
func TestModelTableLinesTheColumnsUp(t *testing.T) {
	row := func(ref string, out float64) modelRow {
		return modelRow{Ref: ref, Entry: gateway.Entry{Model: cost.Model{ID: ref, ContextTokens: 131072, Price: cost.Price{OutputPerM: out}}, Modality: "text->text", Supported: []string{"tools"}}}
	}
	got := modelTable([]modelRow{row("h/a/short", 0.01), row("h/a/much-longer-name", 0.019)}, map[string]bool{"h/a/short": true})
	if len(got) != 2 || strings.Index(got[0], "tools") != strings.Index(got[1], "tools") {
		t.Errorf("the tools column moves: %q", got)
	}
	if !strings.HasSuffix(got[0], "tools *") || strings.HasSuffix(got[1], " ") {
		t.Errorf("a favorite is starred and nothing trails: %q", got)
	}
	if one := modelLine(row("h/a/short", 0.01), nil); strings.Index(one, "131k") >= strings.Index(got[1], "131k") {
		t.Errorf("a row by itself is not padded for the others: %q", one)
	}
}
