package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What is approved is what runs: a member the preview does not read (another case of a name, or a second copy of it) is refused
// instead of taking effect unseen.
func TestArgumentsInTwoSpellingsAreRefused(t *testing.T) {
	env := testEnv(t)
	for _, in := range []string{
		`{"path":"a.txt","content":"shown","Content":"written"}`,
		`{"path":"a.txt","CONTENT":"written"}`,
		`{"path":"a.txt","content":"one","content":"two"}`,
	} {
		res := run(t, Write{}, env, in)
		if !res.IsError || !strings.Contains(res.Text, "spell") && !strings.Contains(res.Text, "same name") {
			t.Errorf("%s: %s", in, res.Text)
		}
		if _, err := os.Stat(filepath.Join(env.Root, "a.txt")); err == nil {
			t.Fatalf("%s: the file was written", in)
		}
	}
	res := run(t, Edit{}, env, `{"path":"a.txt","edits":[{"old_string":"x","new_string":"y","New_String":"z"}]}`)
	if !res.IsError || !strings.Contains(res.Text, "same name") {
		t.Errorf("a nested member in two spellings: %s", res.Text)
	}
	if res := run(t, Write{}, env, `{"path":"a.txt","content":"ok","extra":1}`); res.IsError {
		t.Errorf("an unknown member is still tolerated: %s", res.Text)
	}
}
