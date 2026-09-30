package skills

import (
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/skills/mdfile"
)

// FuzzSkillFile: whatever a SKILL.md contains, discovery neither panics nor
// hangs, and every skill it does accept is safe to list and load: a valid name,
// a one-line description, and a listing with exactly one line per skill.
func FuzzSkillFile(f *testing.F) {
	for _, seed := range []string{
		"", "just a body", skillText("a", "d"), "---\nname: x\n---\n", "---\nname: x\ndescription: |\n  a\n  b\n---\nbody $1 $ARGUMENTS",
		"---\nname: x\nallowed-tools: [Read, \"Bash(x:*)\"]\ncontext: fork\n---\n", "{\"name\": \"x\", \"description\": \"d\"}\nbody",
		"---\nname: x\ndescription: \"unterminated\n---\n", "---\n\tname: x\n---\n", "---\nname: </shared-context>\n---\n",
		"---\ndescription: <my-notes>\n---\n<!-- c -->", "---\nname: x\nuser-invocable: maybe\n---\n", "\xef\xbb\xbf---\r\nname: x\r\n---\r\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		w := newWorld(t)
		w.proj(".claude", "fz", content)
		c, warns := w.discover(true)
		for _, wn := range warns {
			if strings.ContainsAny(wn.String(), "\n\r") {
				t.Fatalf("warning with a line break: %q", wn.String())
			}
		}
		for _, s := range c.Skills() {
			if err := mdfile.CheckName(strings.TrimPrefix(s.Name, ":"), nameRules); err != nil {
				t.Fatalf("accepted an invalid name %q: %v", s.Name, err)
			}
			if strings.ContainsAny(s.Description+s.WhenToUse+s.ArgumentHint, "\n\r\t") {
				t.Fatalf("multi-line text: %q %q", s.Description, s.ArgumentHint)
			}
			for _, tool := range s.AllowedTools {
				if !mdfile.ValidTool(tool) {
					t.Fatalf("accepted a malformed tool rule %q", tool)
				}
			}
			l, err := c.Load(s.Name, "x y")
			if err != nil {
				t.Fatalf("load of an accepted skill: %v", err)
			}
			if strings.ContainsRune(l.Text(), 0) {
				t.Fatal("NUL in loaded text")
			}
		}
		listing := c.Listing(100, est)
		if listing != "" {
			if n := strings.Count(listing, "\n") + 1; n < c.Len() && !strings.Contains(listing, "more skills are not listed") {
				t.Fatalf("%d lines for %d skills", n, c.Len())
			}
			if est.Tokens(listing) > 100 {
				t.Fatalf("listing exceeds its budget: %d tokens", est.Tokens(listing))
			}
		}
	})
}
