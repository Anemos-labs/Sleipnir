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

// A file included twice (by two includeIf sections, and again under a comment after its path) is mounted once: the engine refuses
// two mounts on one destination.
func TestGitMountsMountAnIncludedFileOnce(t *testing.T) {
	p := t.TempDir()
	for f, body := range map[string]string{
		filepath.Join(p, ".git", "config"): "[includeIf \"gitdir:/a/\"]\n\tpath = ../shared.cfg\n[includeIf \"gitdir:/b/\"]\n\tpath = ../shared.cfg ; the same file\n" +
			"[include]\n\tpath = \"../spaced name.cfg\" # quoted\n",
		filepath.Join(p, "shared.cfg"):      "[core]\n",
		filepath.Join(p, "spaced name.cfg"): "[core]\n",
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	count := map[string]int{}
	for _, m := range gitMounts([]string{p}) {
		count[m.path]++
	}
	for path, n := range count {
		if n > 1 {
			t.Errorf("%s is mounted %d times", path, n)
		}
	}
	for _, want := range []string{filepath.Join(p, "shared.cfg"), filepath.Join(p, "spaced name.cfg")} {
		if count[want] != 1 {
			t.Errorf("%s is not mounted: %v", want, count)
		}
	}
}

func TestGitConfigValue(t *testing.T) {
	for raw, want := range map[string]string{
		`../a.cfg`:                 "../a.cfg",
		`../a.cfg ; note`:          "../a.cfg",
		`../a.cfg# note`:           "../a.cfg",
		`"../a b.cfg" # note`:      "../a b.cfg",
		`"../a;b#c.cfg"`:           "../a;b#c.cfg",
		`../a\ b.cfg`:              "../a b.cfg",
		`"../q\"uote.cfg"   `:      `../q"uote.cfg`,
		`../in"side quotes".cfg  `: "../inside quotes.cfg",
	} {
		if got := gitConfigValue(raw); got != want {
			t.Errorf("gitConfigValue(%q) = %q, want %q", raw, got, want)
		}
	}
}
