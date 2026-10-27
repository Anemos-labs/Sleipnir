package taskgen

import (
	"strings"
	"testing"
)

// A fixture cannot put files in git's own directory, under any spelling, so its configuration cannot choose the programs that
// staging the fixture runs.
func TestFixtureFilesCannotReachGitsDirectory(t *testing.T) {
	for _, p := range []string{".git/config", ".GIT/config", "sub/.git/hooks/pre-commit", ".git./config", ".git /info/attributes", "GIT~1/config", `a\.git\config`} {
		err := writeFiles(t.TempDir(), map[string][]byte{p: []byte("[filter \"x\"]\n\tclean = touch pwned\n")})
		if err == nil || !strings.Contains(err.Error(), "git's own directory") {
			t.Errorf("%q: %v", p, err)
		}
	}
	if err := writeFiles(t.TempDir(), map[string][]byte{".gitattributes": []byte("* text\n"), ".github/x.yml": nil, "a.git/x": nil}); err != nil {
		t.Fatal(err)
	}
}
