package perm

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitIn runs git in dir with a configuration of its own, failing the test on an error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// The gitlinks of an index are read whatever git wrote it with: index versions 2 to 4, extended flags, SHA-1 or SHA-256 object
// IDs, and a split index whose shared part holds them.
func TestIndexGitlinksReadsEachIndexFormat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	cases := []struct {
		name string
		init []string
		prep [][]string
		ok   bool
	}{
		{"v2", []string{"init", "-q"}, nil, true},
		{"v3 extended flags", []string{"init", "-q"}, [][]string{{"add", "-N", "new.txt"}}, true},
		{"v4", []string{"init", "-q"}, [][]string{{"update-index", "--index-version", "4"}}, true},
		{"sha256", []string{"init", "-q", "--object-format=sha256"}, nil, true},
		{"sha256 v4", []string{"init", "-q", "--object-format=sha256"}, [][]string{{"update-index", "--index-version", "4"}}, true},
		{"split", []string{"init", "-q"}, [][]string{{"update-index", "--split-index"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitIn(t, dir, tc.init...)
			if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitIn(t, dir, "add", "a.txt")
			head := gitIn(t, dir, "hash-object", "-w", "a.txt") // any object ID of the repository's width
			for _, p := range []string{"lib", "vendor/deep/x", "vendor/deep/y"} {
				gitIn(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+","+p)
			}
			for _, a := range tc.prep {
				gitIn(t, dir, a...)
			}
			links, ok := indexGitlinks(filepath.Join(dir, ".git", "index"))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (links %q)", ok, tc.ok, links)
			}
			if ok && !slices.Equal(links, []string{"lib", "vendor/deep/x", "vendor/deep/y"}) {
				t.Errorf("links = %q", links)
			}
		})
	}
	if links, ok := indexGitlinks(filepath.Join(t.TempDir(), "index")); !ok || links != nil {
		t.Errorf("no index: %q %v", links, ok)
	}
	bad := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(bad, []byte("DIRC\x00\x00\x00\x02\x00\x00\x00\x09garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := indexGitlinks(bad); ok {
		t.Error("a truncated index was read through")
	}
}

// A gitlink .gitmodules does not name is judged as naming a program even when it names others, and a linked worktree's own index
// is the one read.
func TestGitReadsAskForAGitlinkGitmodulesDoesNotName(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repo, "a.txt"), "a")
	gitIn(t, repo, "add", "a.txt")
	gitIn(t, repo, "commit", "-qm", "a")
	head := gitIn(t, repo, "rev-parse", "HEAD")
	write(filepath.Join(repo, ".gitmodules"), "[submodule \"lib\"]\n\tpath = lib\n")
	gitIn(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",lib")
	if why := gitHelperConfig(repo); why != "" {
		t.Errorf("a named gitlink: %q", why)
	}
	gitIn(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",other")
	if why := gitHelperConfig(repo); !strings.Contains(why, "does not name") {
		t.Errorf("an unnamed gitlink beside a named one: %q", why)
	}
	gitIn(t, repo, "update-index", "--force-remove", "other")
	gitIn(t, repo, "add", ".gitmodules")
	gitIn(t, repo, "commit", "-qm", "lib")
	// a linked worktree: its own index holds the unnamed gitlink, the main one does not
	wt := filepath.Join(base, "wt")
	gitIn(t, repo, "worktree", "add", "-q", wt)
	if why := gitHelperConfig(wt); why != "" {
		t.Errorf("a linked worktree with a named gitlink: %q", why)
	}
	gitIn(t, wt, "update-index", "--add", "--cacheinfo", "160000,"+head+",other")
	if why := gitHelperConfig(wt); !strings.Contains(why, "does not name") {
		t.Errorf("an unnamed gitlink in a linked worktree's index: %q", why)
	}
	gitIn(t, wt, "update-index", "--force-remove", "other")
	// a submodule repository kept in the linked worktree's own modules/
	own := gitIn(t, wt, "rev-parse", "--absolute-git-dir")
	mod := filepath.Join(own, "modules", "lib")
	write(filepath.Join(mod, "HEAD"), "ref: refs/heads/main\n")
	write(filepath.Join(mod, "config"), "[core]\n\tfsmonitor = ./hook.sh\n")
	if why := gitHelperConfig(wt); why == "" {
		t.Error("fsmonitor in a linked worktree's submodule repository was not seen")
	}
}

// The bounds of the submodule check fail closed: a .gitmodules that cannot be read, an index too large to read, and more submodule
// repositories than are checked.
func TestSubmoduleCheckFailsClosedPastItsBounds(t *testing.T) {
	mk := func(t *testing.T) string {
		t.Helper()
		repo := t.TempDir()
		for _, n := range []string{"HEAD", "config"} {
			p := filepath.Join(repo, ".git", n)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("ref: refs/heads/main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core]\n\tbare = false\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return repo
	}
	t.Run("plain", func(t *testing.T) {
		if why := gitHelperConfig(mk(t)); why != "" {
			t.Errorf("a repository with no submodules: %q", why)
		}
	})
	t.Run("unreadable .gitmodules", func(t *testing.T) {
		repo := mk(t)
		if err := os.Mkdir(filepath.Join(repo, ".gitmodules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if why := gitHelperConfig(repo); !strings.Contains(why, ".gitmodules") {
			t.Errorf("a .gitmodules that is a directory: %q", why)
		}
	})
	t.Run("oversized index", func(t *testing.T) {
		repo := mk(t)
		f, err := os.Create(filepath.Join(repo, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(64<<20 + 1); err != nil {
			t.Fatal(err)
		}
		f.Close()
		if why := gitHelperConfig(repo); !strings.Contains(why, "does not name") {
			t.Errorf("an index past the bound: %q", why)
		}
	})
	t.Run("too many submodules", func(t *testing.T) {
		repo := mk(t)
		for i := 0; i <= maxSubmoduleConfigs; i++ {
			m := filepath.Join(repo, ".git", "modules", "m"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26)))
			if err := os.MkdirAll(m, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, n := range []string{"HEAD", "config"} {
				if err := os.WriteFile(filepath.Join(m, n), []byte("\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		if why := gitHelperConfig(repo); !strings.Contains(why, "more submodules") {
			t.Errorf("%d submodule repositories: %q", maxSubmoduleConfigs+1, why)
		}
	})
}
