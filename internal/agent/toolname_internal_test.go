package agent

import (
	"strings"
	"testing"
)

func TestRepairToolName(t *testing.T) {
	have := map[string]bool{"read": true, "grep": true, "bash": true}
	has := func(n string) bool { return have[n] }
	cases := []struct{ in, want, why string }{
		{"read", "read", "already a tool"},
		{"read<|channel|>commentary", "read", "gpt-oss harmony channel marker glued on by a gateway"},
		{"grep<|channel|>commentary json", "grep", "the marker and what follows it"},
		{"functions.read", "read", "a namespace"},
		{"functions.bash<|channel|>commentary", "bash", "both"},
		{"  read  ", "read", "white space"},
		{"read.", "read", "a stray full stop"},
		{"`read`", "read", "backquotes"},
		{"<|channel|>read", "<|channel|>read", "a marker in front leaves nothing to repair: not guessed"},
		{"readfile", "readfile", "a different name is not repaired, it is suggested (see unknownToolMessage)"},
		{"reed", "reed", "a typo is not repaired"},
		{"write<|channel|>commentary", "write<|channel|>commentary", "a repair that is not a tool stays as it was"},
		{"", "", "empty"},
	}
	for _, c := range cases {
		if got := repairToolName(has, c.in); got != c.want {
			t.Errorf("repairToolName(%q) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}
}

func TestUnknownToolMessageListsTheToolsAndSuggests(t *testing.T) {
	names := []string{"bash", "edit", "glob", "grep", "ls", "read", "write"}
	cases := []struct{ in, suggest string }{
		{"search", "grep"}, {"read_file", "read"}, {"open_file", "read"}, {"run_command", "bash"}, {"list_files", "ls"}, {"Read", "read"},
		{"greп", "grep"},             // a look-alike letter is one edit away     // nothing is close enough to be a guess
		{"rd", ""},                   // two edits from a short name is too far
		{"xyz", ""}, {"rea", "read"}, // nor this
		{"reed", "read"}, // a typo
		{"str_replace_editor", "edit"},
	}
	for _, c := range cases {
		msg := unknownToolMessage(names, c.in)
		if !strings.Contains(msg, `unknown tool "`+c.in+`"`) || !strings.Contains(msg, "bash, edit, glob, grep, ls, read, write") {
			t.Errorf("%q: the message must name the tool and list the tools: %s", c.in, msg)
		}
		if c.suggest != "" && !strings.Contains(msg, `Did you mean "`+c.suggest+`"?`) {
			t.Errorf("%q: want a suggestion of %q: %s", c.in, c.suggest, msg)
		}
		if c.suggest == "" && strings.Contains(msg, "Did you mean") {
			t.Errorf("%q: want no suggestion: %s", c.in, msg)
		}
	}
	// A suggestion must exist among the tools: an alias of a tool this agent does not have is not offered.
	if msg := unknownToolMessage([]string{"bash"}, "search"); strings.Contains(msg, "Did you mean") {
		t.Errorf("suggested a tool that is not there: %s", msg)
	}
}

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{{"", "", 0}, {"a", "", 1}, {"kitten", "sitting", 3}, {"read", "read", 0}, {"read", "reed", 1}, {"ecoh", "echo", 2}, {"über", "uber", 1}} {
		if got := editDistance(c.a, c.b); got != c.d {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.d)
		}
	}
}
