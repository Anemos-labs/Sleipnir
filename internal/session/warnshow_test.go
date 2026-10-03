package session

import (
	"fmt"
	"strings"
	"testing"
)

// A person with a hundred skills of another tool saw a hundred lines of "unknown frontmatter field" before the chat began (a first run on
// Windows). Every note stays in the log; the screen gets the ones that matter, a few of them, and a count of the rest.
func TestShownWarningsAreFewAndMatter(t *testing.T) {
	var all []string
	for i := 0; i < 90; i++ {
		all = append(all, fmt.Sprintf("skills: ~/.claude/skills/s%d/SKILL.md: [s%d] unknown frontmatter field(s) ignored: homepage", i, i))
	}
	if got := shownWarnings(all); len(got) != 0 {
		t.Errorf("unknown fields alone showed %d lines: %q", len(got), got)
	}
	all = append(all, "skills: ~/.claude/skills/tdd/SKILL.md: [tdd] shadowed by the skill of the same name (skipped)")
	if got := shownWarnings(all); len(got) != 1 || !strings.Contains(got[0], "shadowed") {
		t.Errorf("one real note among them: %q", got)
	}
	var many []string
	for i := 0; i < 12; i++ {
		many = append(many, fmt.Sprintf("skills: skill %d is skipped", i))
	}
	got := shownWarnings(many)
	if len(got) != 6 || !strings.Contains(got[5], "7 more") || !strings.Contains(got[5], "session log") {
		t.Errorf("twelve real notes: %d lines, last %q", len(got), got[len(got)-1])
	}
}
