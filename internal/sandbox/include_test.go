package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// A file .git/config includes from inside the project is guarded read-only like the configuration itself; one outside the
// project is not mounted at all.
func TestGitMountsGuardProjectIncludes(t *testing.T) {
	p := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.cfg")
	for f, body := range map[string]string{
		filepath.Join(p, ".git", "config"):      "[include]\n\tpath = ../tools/git.cfg\n[includeIf \"gitdir:/x/\"]\n\tpath = " + outside + "\n",
		filepath.Join(p, "tools", "git.cfg"):    "[include]\n\tpath = nested.cfg\n",
		filepath.Join(p, "tools", "nested.cfg"): "[core]\n\tsshCommand = ssh\n",
		outside:                                 "[core]\n",
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ro := map[string]bool{}
	for _, m := range gitMounts([]string{p}) {
		if m.readOnly {
			ro[m.path] = true
		}
	}
	for _, want := range []string{filepath.Join(p, "tools", "git.cfg"), filepath.Join(p, "tools", "nested.cfg")} {
		if !ro[want] {
			t.Errorf("%s is not guarded: %v", want, ro)
		}
	}
	if ro[outside] {
		t.Errorf("a file outside the project was mounted")
	}
}
