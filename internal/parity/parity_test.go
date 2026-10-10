package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// problems of one comparison, joined, for a test to look for a phrase in.
func problems(d Differences, terminal, web []string) string {
	return strings.Join(d.Check("dim", terminal, web), "\n")
}

// Two interfaces that have the same items have nothing to report, whatever the order.
func TestEqualInterfacesHaveNoProblem(t *testing.T) {
	if got := problems(Differences{}, []string{"a", "b"}, []string{"b", "a"}); got != "" {
		t.Errorf("equal lists reported %q", got)
	}
}

// An item that only one interface has is a failure that names the item, the side that has it and the file to edit.
func TestAnItemOnOneSideOnlyFails(t *testing.T) {
	got := problems(Differences{}, []string{"a", "t"}, []string{"a", "w"})
	for _, want := range []string{`"t" exists in the terminal interface and not in the web interface`, `"w" exists in the web interface and not in the terminal interface`, "contract/dim.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Listing an item with a reason is how a difference is meant; a gap is allowed the same way.
func TestListedDifferencesPass(t *testing.T) {
	d := Differences{
		TerminalOnly: []Entry{{"t", "the program draws it on the terminal itself"}},
		WebOnly:      []Entry{{"w", "a browser has no equivalent command"}},
		Gaps:         []Entry{{"g", "the page has to learn to show it"}},
	}
	if got := problems(d, []string{"a", "t", "g"}, []string{"a", "w"}); got != "" {
		t.Errorf("listed differences reported %q", got)
	}
	if lines := d.GapLines("dim"); len(lines) != 1 || !strings.Contains(lines[0], `gap "g"`) {
		t.Errorf("gap lines %v", lines)
	}
}

// An exception that no longer applies is removed on purpose: a difference that was closed, or that never existed, fails.
func TestStaleEntriesFail(t *testing.T) {
	d := Differences{
		TerminalOnly: []Entry{{"closed", "it was only in the terminal once"}, {"typo", "this item does not exist anywhere"}},
		Gaps:         []Entry{{"both", "this gap has been closed since"}},
	}
	got := problems(d, []string{"closed", "both"}, []string{"closed", "both"})
	for _, want := range []string{`"closed" under terminal_only is stale`, `"typo" under terminal_only is stale`, `"both" under gaps is stale`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A reason of a word, an empty item and an item listed twice are refused.
func TestEntriesNeedAnItemAndAReason(t *testing.T) {
	d := Differences{
		TerminalOnly: []Entry{{"t", "no"}, {"", "an entry without an item"}},
		Gaps:         []Entry{{"t", "listed under two headings as well"}},
	}
	got := problems(d, []string{"t"}, nil)
	for _, want := range []string{"needs a reason", "has no item", `is listed under terminal_only and under gaps`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// The problems come in the same order every time, so a failure reads the same on every machine.
func TestProblemsAreInOrder(t *testing.T) {
	a := problems(Differences{}, []string{"z", "m", "a"}, nil)
	if i, j := strings.Index(a, `"a"`), strings.Index(a, `"z"`); i < 0 || j < 0 || i > j {
		t.Errorf("not in order:\n%s", a)
	}
}

// A contract file that names a field this package does not know is refused, so a misspelt key cannot silently mean nothing, and a
// missing file is an error.
func TestLoadRefusesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(path, []byte(`{"terminal_onyl":[{"item":"a","reason":"a misspelt heading"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var d Differences
	if err := loadFile(path, &d); err == nil || !strings.Contains(err.Error(), "terminal_onyl") {
		t.Errorf("a misspelt key loaded: %v", err)
	}
	if err := Load("does-not-exist", &d); err == nil {
		t.Error("a missing file loaded")
	}
}
