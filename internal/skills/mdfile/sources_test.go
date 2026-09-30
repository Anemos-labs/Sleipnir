package mdfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func labels(srcs []Source) []string {
	out := make([]string, len(srcs))
	for i, s := range srcs {
		out[i] = string(s.Scope) + ":" + s.Label
	}
	return out
}

func TestSourcesPrecedenceAndTrust(t *testing.T) {
	root, home := realTemp(t), realTemp(t)
	for _, d := range []string{".sleipnir/skills", ".claude/skills"} {
		mkdir(t, filepath.Join(root, d))
		mkdir(t, filepath.Join(home, d))
	}
	extra := realTemp(t)

	trusted, warns := Sources("skills", Layout{Root: root, Home: home, TrustProject: true, Extra: []Extra{{Dir: extra, Namespace: "plug", Trusted: true}}})
	want := []string{"project:.sleipnir/skills", "project:.claude/skills", "user:~/.sleipnir/skills", "user:~/.claude/skills", "extra:plug"}
	if got := labels(trusted); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	if trusted[0].Contain != root || trusted[4].Contain != extra || trusted[2].Contain != "" {
		t.Errorf("containment: %q %q %q", trusted[0].Contain, trusted[4].Contain, trusted[2].Contain)
	}

	untrusted, warns := Sources("skills", Layout{Root: root, Home: home, Extra: []Extra{{Dir: extra, Namespace: "plug", Trusted: true}, {Dir: extra, Namespace: "other"}}})
	if got := labels(untrusted); strings.Join(got, ",") != "user:~/.sleipnir/skills,user:~/.claude/skills,extra:plug" {
		t.Fatalf("untrusted sources = %v", got)
	}
	// The two repository directories and the untrusted plugin were left alone, and the user is told.
	if len(warns) != 3 {
		t.Fatalf("warnings = %v", warns)
	}
	for _, w := range warns {
		if w.Skipped || !strings.Contains(w.Msg, "not loaded") {
			t.Errorf("unexpected warning %v", w)
		}
		if strings.Contains(w.Path, root) {
			t.Errorf("warning leaks an absolute path: %v", w)
		}
	}
}

func TestSourcesMissingDirectoriesAreSilent(t *testing.T) {
	srcs, warns := Sources("commands", Layout{Root: realTemp(t), Home: realTemp(t), TrustProject: true})
	if len(srcs) != 0 || len(warns) != 0 {
		t.Fatalf("%v %v", srcs, warns)
	}
	srcs, warns = Sources("commands", Layout{TrustProject: true, Home: realTemp(t)})
	if len(srcs) != 0 || len(warns) != 0 {
		t.Fatalf("no root: %v %v", srcs, warns)
	}
}

func TestSourcesRefuseProjectDirectoryOutsideTheRepository(t *testing.T) {
	root, elsewhere := realTemp(t), realTemp(t)
	mkdir(t, filepath.Join(elsewhere, "skills"))
	symlink(t, elsewhere, filepath.Join(root, ".claude"))
	srcs, warns := Sources("skills", Layout{Root: root, Home: realTemp(t), TrustProject: true})
	if len(srcs) != 0 {
		t.Fatalf("a symlinked .claude pointing out of the repository was accepted: %v", labels(srcs))
	}
	if len(warns) != 1 || !warns[0].Skipped || !strings.Contains(warns[0].Msg, "outside the project") {
		t.Fatalf("warnings = %v", warns)
	}
}

// The user's own symlinks (a dotfiles checkout) are honoured: only they can make them.
func TestSourcesFollowUserSymlinks(t *testing.T) {
	home, dotfiles := realTemp(t), realTemp(t)
	mkdir(t, filepath.Join(dotfiles, "claude-skills"))
	symlink(t, filepath.Join(dotfiles, "claude-skills"), filepath.Join(home, ".claude", "skills"))
	srcs, _ := Sources("skills", Layout{Home: home})
	if len(srcs) != 1 || srcs[0].Real != filepath.Join(dotfiles, "claude-skills") || srcs[0].Scope != ScopeUser {
		t.Fatalf("sources = %+v", srcs)
	}
}

func TestSourcesExtras(t *testing.T) {
	a, b := realTemp(t), realTemp(t)
	srcs, warns := Sources("agents", Layout{Home: realTemp(t), Extra: []Extra{
		{Dir: a, Namespace: "bad ns!", Trusted: true},
		{Dir: filepath.Join(a, "missing"), Trusted: true},
		{Dir: "", Trusted: true},
		{Dir: b, Trusted: true}, // no namespace: labelled by directory name
	}})
	if len(srcs) != 1 || srcs[0].Namespace != "" || srcs[0].Label != filepath.Base(b) {
		t.Fatalf("sources = %+v", srcs)
	}
	if len(warns) != 1 || !warns[0].Skipped || !strings.Contains(warns[0].Msg, "invalid namespace") {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestWarningString(t *testing.T) {
	for w, want := range map[Warning]string{
		Warnf("a/b", "n", "careful"):    "a/b: [n] careful",
		Skipf("a/b", "", "broken"):      "a/b: broken (skipped)",
		Warnf("", "", "just a message"): "just a message",
		Skipf("", "name", "no path"):    "[name] no path (skipped)",
	} {
		if got := w.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", w, got, want)
		}
	}
}
