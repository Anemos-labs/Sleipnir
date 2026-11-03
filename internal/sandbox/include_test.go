package sandbox

import (
	"os"
	"path/filepath"
	"strings"
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

// A long run of blanks inside a value is kept as written, in time linear in its length.
func TestGitConfigValueKeepsLongBlankRuns(t *testing.T) {
	raw := "a" + strings.Repeat(" ", 1<<20) + "b   "
	if got := gitConfigValue(raw); got != raw[:len(raw)-3] {
		t.Errorf("gitConfigValue kept %d bytes, want %d", len(got), len(raw)-3)
	}
}

// The helper is handed the docker command the box started with, so it never searches a PATH the project may have a directory on.
func TestWrapNamesTheDockerCommandTheBoxStartedWith(t *testing.T) {
	docker := filepath.Join(t.TempDir(), "docker") // absolute on every platform
	b := &Box{Name: "sb", docker: docker}
	if got := strings.Join(b.Wrap("/x/sleipnir"), " "); got != "/x/sleipnir "+ExecCommand+" sb "+docker+" --" {
		t.Errorf("Wrap = %q", got)
	}
}

// A repository with an include whose directory in the project is a link to a directory outside it is not mounted: the engine
// would mount the file the link names, a host file, into the container, and without it the container could put its own
// directory in the link's place for the host's git to read.
func TestARepositoryWithAnIncludeBehindALinkedDirectoryIsNotMounted(t *testing.T) {
	p := t.TempDir()
	private := t.TempDir()
	if err := os.WriteFile(filepath.Join(private, "id"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(p, "lnk")); err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(filepath.Join(p, ".git", "config"), []byte("[include]\n\tpath = ../lnk/id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mountPaths([]string{p}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a repository with an include behind a linked directory was mounted: %v", err)
	}
}

// The docker command the helper runs on the host is never one inside a directory the sandbox mounts writable.
func TestTheDockerCommandMustBeOutsideTheMounts(t *testing.T) {
	proj, other := t.TempDir(), t.TempDir()
	for _, d := range []string{proj, other} {
		if err := os.WriteFile(filepath.Join(d, "docker"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := dockerOutside(filepath.Join(proj, "docker"), []string{proj}); err == nil {
		t.Error("a docker inside the project was taken")
	}
	if err := dockerOutside(filepath.Join(other, "docker"), []string{proj}); err != nil {
		t.Errorf("a docker outside the project was refused: %v", err)
	}
	if err := os.Symlink(filepath.Join(proj, "docker"), filepath.Join(other, "dlink")); err != nil {
		t.Skip(err)
	}
	if err := dockerOutside(filepath.Join(other, "dlink"), []string{proj}); err == nil {
		t.Error("a link to a docker inside the project was taken")
	}
}
