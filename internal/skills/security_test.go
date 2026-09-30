package skills

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// secretFile is a stand-in for ~/.ssh/id_ed25519: a file outside the project
// that no skill may ever read.
func secretFile(t *testing.T, w *world) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(w.root), "outside", "secret.txt")
	put(t, p, "---\nname: leaked\ndescription: SECRET-CANARY\n---\nSECRET-CANARY body\n")
	return p
}

func TestSymlinkedSKILLmdCannotReadOutsideTheSkillDirectory(t *testing.T) {
	w := newWorld(t)
	secret := secretFile(t, w)
	for _, scope := range []string{"project", "user"} {
		dir := filepath.Join(w.root, ".claude", "skills", "thief")
		if scope == "user" {
			dir = filepath.Join(w.home, ".claude", "skills", "thief")
		}
		link(t, secret, filepath.Join(dir, "SKILL.md"))
	}
	c, warns := w.discover(true)
	if c.Len() != 0 {
		t.Fatalf("a SKILL.md symlinked to a file outside its directory was loaded: %s", names(c))
	}
	if len(warns) != 2 {
		t.Fatalf("warnings: %s", warnText(warns))
	}
	for _, wn := range warns {
		if !wn.Skipped || !strings.Contains(wn.Msg, "outside the skill directory") {
			t.Errorf("warning: %s", wn)
		}
		if strings.Contains(wn.String(), "SECRET-CANARY") {
			t.Errorf("warning leaks file content: %s", wn)
		}
	}
}

func TestProjectSkillDirectorySymlinkedOutsideTheProject(t *testing.T) {
	w := newWorld(t)
	elsewhere := filepath.Join(filepath.Dir(w.root), "elsewhere")
	put(t, filepath.Join(elsewhere, "SKILL.md"), skillText("stolen", "from elsewhere"))
	link(t, elsewhere, filepath.Join(w.root, ".claude", "skills", "stolen"))
	c, warns := w.discover(true)
	if c.Len() != 0 || len(warns) != 1 || !warns[0].Skipped || !strings.Contains(warns[0].Msg, "outside") {
		t.Fatalf("skills %s, warnings %s", names(c), warnText(warns))
	}
}

func TestProjectSkillDirectorySymlinkedInsideTheProject(t *testing.T) {
	w := newWorld(t)
	put(t, filepath.Join(w.root, "shared", "tools", "SKILL.md"), skillText("tools", "shared in repo"))
	link(t, filepath.Join(w.root, "shared", "tools"), filepath.Join(w.root, ".claude", "skills", "tools"))
	c, warns := w.discover(true)
	if got := names(c); got != "tools" || len(warns) != 0 {
		t.Fatalf("skills %s, warnings %s", got, warnText(warns))
	}
}

// The user's own dotfiles setup: ~/.claude/skills/foo -> ~/dotfiles/foo.
func TestUserSkillDirectorySymlinkIsHonoured(t *testing.T) {
	w := newWorld(t)
	dot := filepath.Join(filepath.Dir(w.home), "dotfiles", "foo")
	put(t, filepath.Join(dot, "SKILL.md"), skillText("foo", "from dotfiles"))
	put(t, filepath.Join(dot, "refs", "a.md"), "reference a")
	link(t, dot, filepath.Join(w.home, ".claude", "skills", "foo"))
	c, warns := w.discover(false)
	if got := names(c); got != "foo" || len(warns) != 0 {
		t.Fatalf("skills %s, warnings %s", got, warnText(warns))
	}
	f, err := c.ReadFile("foo", "refs/a.md")
	if err != nil || f.Text != "reference a" {
		t.Fatalf("supporting file of a symlinked user skill: %+v %v", f, err)
	}
}

// .claude itself may be a symlink to somewhere else entirely.
func TestSymlinkedBrandDirectoryPointingOutsideTheProject(t *testing.T) {
	w := newWorld(t)
	elsewhere := filepath.Join(filepath.Dir(w.root), "elsewhere")
	put(t, filepath.Join(elsewhere, "skills", "stolen", "SKILL.md"), skillText("stolen", "from elsewhere"))
	link(t, elsewhere, filepath.Join(w.root, ".claude"))
	c, warns := w.discover(true)
	if c.Len() != 0 || len(warns) != 1 || !warns[0].Skipped || !strings.Contains(warns[0].Msg, "outside the project") {
		t.Fatalf("skills %s, warnings %s", names(c), warnText(warns))
	}
}

func TestSupportingFilesAndPathTraversal(t *testing.T) {
	w := newWorld(t)
	secret := secretFile(t, w)
	dir := w.proj(".claude", "kit", skillText("kit", "has files"))
	put(t, filepath.Join(dir, "scripts", "run.sh"), "#!/bin/sh\necho hi\n")
	put(t, filepath.Join(dir, "references", "api.md"), "api notes")
	put(t, filepath.Join(dir, ".hidden", "x"), "hidden")
	put(t, filepath.Join(w.root, "top-secret.txt"), "TOP")
	link(t, secret, filepath.Join(dir, "leak.txt"))
	link(t, "../../../top-secret.txt", filepath.Join(dir, "leak2.txt")) // inside the project, outside the skill
	link(t, filepath.Join(dir, "references", "api.md"), filepath.Join(dir, "alias.md"))
	link(t, filepath.Dir(secret), filepath.Join(dir, "outdir"))
	put(t, filepath.Join(dir, "bin.dat"), "a\x00b")

	c, _ := w.discover(true)
	l, err := c.Load("kit", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Files, ","); got != "alias.md,bin.dat,references/api.md,scripts/run.sh" {
		t.Fatalf("files = %s (symlinks out of the tree and dot files must not be listed)", got)
	}

	ok := map[string]string{"references/api.md": "api notes", "scripts/../references/api.md": "api notes", "./references/api.md": "api notes", "alias.md": "api notes"}
	for rel, want := range ok {
		f, err := c.ReadFile("kit", rel)
		if err != nil || f.Text != want {
			t.Errorf("ReadFile(%q) = %+v, %v", rel, f, err)
		}
	}
	refuse := map[string]string{
		"../top-secret.txt":                  "inside the skill directory",
		"../../top-secret.txt":               "inside the skill directory",
		"references/../../../top-secret.txt": "inside the skill directory",
		"..":                                 "inside the skill directory",
		".":                                  "inside the skill directory",
		"/etc/passwd":                        "relative",
		"~/notes":                            "relative",
		"C:/Windows/win.ini":                 "relative",
		"..\\top-secret.txt":                 "forward slashes",
		"a\x00b":                             "NUL",
		"":                                   "empty",
		"leak.txt":                           "outside the skill directory",
		"leak2.txt":                          "outside the skill directory",
		"outdir/secret.txt":                  "outside the skill directory",
		"missing.md":                         "has no file",
		"references":                         "not a regular file",
		"bin.dat":                            "binary",
	}
	for rel, want := range refuse {
		f, err := c.ReadFile("kit", rel)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ReadFile(%q) = %+v, %v; want an error mentioning %q", rel, f, err, want)
		}
		if strings.Contains(f.Text, "SECRET-CANARY") || strings.Contains(f.Text, "TOP") {
			t.Errorf("ReadFile(%q) leaked content", rel)
		}
	}
}

func TestSupportFileIsCleanedAndBounded(t *testing.T) {
	w := newWorld(t)
	dir := w.proj(".claude", "kit", skillText("kit", "d"))
	put(t, filepath.Join(dir, "notes.txt"), "line\u202E one\r\nline two\x1b[31m\n")
	put(t, filepath.Join(dir, "big.txt"), strings.Repeat("x", maxSupportFileLen+10))
	c, _ := w.discover(true)
	f, err := c.ReadFile("kit", "notes.txt")
	if err != nil || f.Text != "line one\nline two[31m\n" {
		t.Errorf("cleaned = %q, %v", f.Text, err)
	}
	f, err = c.ReadFile("kit", "big.txt")
	if err != nil || !f.Truncated || len(f.Text) != maxSupportFileLen {
		t.Errorf("big file: truncated=%v len=%d err=%v", f.Truncated, len(f.Text), err)
	}
}

func TestReadFileRespectsModelInvocationPolicy(t *testing.T) {
	w := newWorld(t)
	dir := w.proj(".claude", "private", "---\nname: private\ndescription: d\ndisable-model-invocation: true\n---\nx")
	put(t, filepath.Join(dir, "a.md"), "secret steps")
	c, _ := w.discover(true)
	if _, err := c.ReadFile("private", "a.md"); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.ReadFile("nope", "a.md"); err == nil {
		t.Fatal("unknown skill must be an error")
	}
}

// A FIFO named SKILL.md must not hang discovery.
func TestFIFOSkillDoesNotHangDiscovery(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(w.root, ".claude", "skills", "pipe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "SKILL.md"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	w.proj(".claude", "fine", skillText("fine", "d"))
	done := make(chan struct{})
	var c *Catalog
	var warns []Warning
	go func() {
		c, warns = w.discover(true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("discovery blocked on a FIFO")
	}
	if names(c) != "fine" || len(warns) != 1 || !warns[0].Skipped || !strings.Contains(warns[0].Msg, "not a regular file") {
		t.Fatalf("skills %s, warnings %s", names(c), warnText(warns))
	}
}

func TestHostileFrontmatterDoesNotHurt(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "bomb", "---\nname: bomb\n"+strings.Repeat("k: v\n", 20000)+"---\nx")
	w.proj(".claude", "deep", "---\nname: deep\nx: "+strings.Repeat("[", 5000)+"\n---\nx")
	w.proj(".claude", "utf8", "---\nname: utf8\ndescription: \xff\xfe bytes\n---\n\xff body")
	w.proj(".claude", "binary", "---\nname: binary\n---\n\x00\x01\x02")
	start := time.Now()
	c, warns := w.discover(true)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("discovery took %v", time.Since(start))
	}
	if got := names(c); got != "utf8" {
		t.Fatalf("skills = %s\nwarnings:\n%s", got, warnText(warns))
	}
	if s, _ := c.Get("utf8"); !strings.Contains(s.Description, "\uFFFD") {
		t.Errorf("invalid UTF-8 should be replaced, got %q", s.Description)
	}
}
