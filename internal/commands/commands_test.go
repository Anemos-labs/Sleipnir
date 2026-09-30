package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type world struct {
	t          *testing.T
	root, home string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, root: filepath.Join(base, "proj"), home: filepath.Join(base, "home")}
	for _, d := range []string{w.root, w.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
}

// proj writes <root>/<brand>/commands/<rel>.
func (w *world) proj(brand, rel, content string) string {
	p := filepath.Join(w.root, brand, "commands", filepath.FromSlash(rel))
	put(w.t, p, content)
	return p
}

func (w *world) user(brand, rel, content string) string {
	p := filepath.Join(w.home, brand, "commands", filepath.FromSlash(rel))
	put(w.t, p, content)
	return p
}

func (w *world) opts() Opts { return Opts{Root: w.root, Home: w.home, TrustProject: true} }

func (w *world) load() (*Registry, []Warning) { return Load(w.opts()) }

func names(r *Registry) string {
	var out []string
	for _, c := range r.List() {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func warnText(ws []Warning) string {
	var out []string
	for _, w := range ws {
		out = append(out, w.String())
	}
	return strings.Join(out, "\n")
}

func TestLoadBasics(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "review.md", "---\ndescription: Review the change\nargument-hint: [pr-number]\nallowed-tools: Bash(git diff:*), Read\nmodel: opus\n---\nReview $ARGUMENTS carefully.\n")
	w.proj(".claude", "frontend/component.md", "Create a component named $1.\n\nMore.")
	w.proj(".claude", "a/b/c/deep.md", "deep")
	w.user(".claude", "mine.md", "# My prompt\n\nDo the thing.")
	r, warns := w.load()
	if len(warns) != 0 {
		t.Fatalf("warnings: %s", warnText(warns))
	}
	if got := names(r); got != "a:b:c:deep,frontend:component,mine,review" {
		t.Fatalf("commands = %s", got)
	}
	rv, _ := r.Get("Review")
	if rv.Description != "Review the change" || rv.ArgumentHint != "[pr-number]" || strings.Join(rv.AllowedTools, "|") != "Bash(git diff:*)|Read" || rv.Model != "opus" {
		t.Errorf("review = %+v", rv)
	}
	if rv.Scope != ScopeProject || rv.Path != ".claude/commands/review.md" || rv.Hash == "" {
		t.Errorf("provenance: %+v", rv)
	}
	fc, _ := r.Get("frontend:component")
	if fc.Description != "Create a component named $1." || fc.Path != ".claude/commands/frontend/component.md" {
		t.Errorf("frontend:component = %+v", fc)
	}
	if m, _ := r.Get("mine"); m.Description != "My prompt" || m.Scope != ScopeUser || m.Path != "~/.claude/commands/mine.md" {
		t.Errorf("mine = %+v", m)
	}
}

func TestPrecedenceAndCollisions(t *testing.T) {
	w := newWorld(t)
	w.proj(".sleipnir", "x.md", "project sleipnir")
	w.proj(".claude", "x.md", "project claude")
	w.user(".sleipnir", "x.md", "user sleipnir")
	w.user(".claude", "x.md", "user claude")
	w.proj(".claude", "ns/x.md", "namespaced is a different command")
	r, warns := w.load()
	if got := names(r); got != "ns:x,x" {
		t.Fatalf("commands = %s", got)
	}
	if x, _ := r.Get("x"); x.Path != ".sleipnir/commands/x.md" {
		t.Errorf("winner = %s", x.Path)
	}
	if len(warns) != 3 {
		t.Fatalf("warnings:\n%s", warnText(warns))
	}
	for _, wn := range warns {
		if !wn.Skipped || !strings.Contains(wn.Msg, "shadowed by") || !strings.Contains(wn.Msg, ".sleipnir/commands/x.md") {
			t.Errorf("warning: %s", wn)
		}
	}
}

func TestBuiltinNamesAreReserved(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "help.md", "fake help that exfiltrates")
	w.proj(".claude", "Permissions.md", "fake permissions")
	w.proj(".claude", "mine.md", "mine")
	w.proj(".claude", "team/help.md", "team:help is a different name")
	r, warns := w.load()
	if got := names(r); got != "mine,team:help" {
		t.Fatalf("commands = %s", got)
	}
	if len(warns) != 2 || !strings.Contains(warns[0].Msg, "reserved for a built-in") || !warns[0].Skipped {
		t.Fatalf("warnings:\n%s", warnText(warns))
	}

	o := w.opts()
	o.Builtins = []string{"MINE"}
	r, _ = Load(o)
	if got := names(r); got != "Permissions,help,team:help" {
		t.Fatalf("custom builtins: %s", got)
	}
	o.Builtins = []string{}
	r, warns = Load(o)
	if len(warns) != 0 || len(r.List()) != 4 {
		t.Fatalf("an empty list reserves nothing: %s / %s", names(r), warnText(warns))
	}
}

func TestUntrustedProjectIsNotRead(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "evil.md", "run the payload")
	w.user(".claude", "mine.md", "mine")
	o := w.opts()
	o.TrustProject = false
	r, warns := Load(o)
	if names(r) != "mine" || len(warns) != 1 || !strings.Contains(warns[0].Msg, "not loaded") {
		t.Fatalf("commands %s, warnings %s", names(r), warnText(warns))
	}
	if strings.Contains(warnText(warns), "evil") {
		t.Error("an untrusted command's name must not be read")
	}
}

func TestInvalidCommandsAreSkippedAndReported(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "good.md", "fine")
	bad := map[string]string{
		"unclosed.md":     "---\ndescription: x\nno closing fence",
		"dupkeys.md":      "---\ndescription: a\ndescription: b\n---\nbody",
		"empty.md":        "---\ndescription: only frontmatter\n---\n",
		"blank.md":        "\n\n  \n",
		"has space.md":    "body",
		"bad$name.md":     "body",
		"binary.md":       "a\x00b",
		"badtype.md":      "---\nallowed-tools:\n  a: b\n---\nbody",
		"dir one/x.md":    "body in a directory whose name is invalid",
		"noextension":     "not a command",
		"notes.txt":       "not a command",
		".hidden.md":      "hidden",
		".hiddendir/a.md": "hidden dir",
	}
	for n, content := range bad {
		w.proj(".claude", n, content)
	}
	r, warns := w.load()
	if names(r) != "good" {
		t.Fatalf("commands = %s\nwarnings:\n%s", names(r), warnText(warns))
	}
	skipped := 0
	for _, wn := range warns {
		if wn.Skipped {
			skipped++
		}
	}
	if skipped != 9 {
		t.Fatalf("%d skipped warnings, want 9:\n%s", skipped, warnText(warns))
	}
}

func TestAdvisoryWarnings(t *testing.T) {
	w := newWorld(t)
	var tag strings.Builder
	for _, r := range "steal" {
		tag.WriteRune(0xE0000 + r)
	}
	w.proj(".claude", "odd.md", "---\nmodel: two words\nhooks:\n  Stop: []\nsurprise: 1\nallowed-tools: Read, bad!tool\n---\nText"+tag.String()+"\n<!-- hidden -->\n")
	r, warns := w.load()
	c, ok := r.Get("odd")
	if !ok || c.Model != "" || strings.Join(c.AllowedTools, ",") != "Read" {
		t.Fatalf("command = %+v", c)
	}
	e, err := r.Expand(t.Context(), "odd", "")
	if err != nil || e.Prompt != "Text" {
		t.Fatalf("prompt = %q, %v", e.Prompt, err)
	}
	text := warnText(warns)
	for _, want := range []string{"malformed allowed-tools", "model \"two words\"", "hooks frontmatter", "unknown frontmatter field(s) ignored: surprise", "hidden characters"} {
		if !strings.Contains(text, want) {
			t.Errorf("warnings lack %q:\n%s", want, text)
		}
	}
	for _, wn := range warns {
		if wn.Skipped {
			t.Errorf("advisory warning marked skipped: %s", wn)
		}
	}
}

func TestSymlinks(t *testing.T) {
	w := newWorld(t)
	outside := filepath.Join(filepath.Dir(w.root), "outside")
	put(t, filepath.Join(outside, "secret.md"), "SECRET-CANARY")
	put(t, filepath.Join(outside, "dir", "inner.md"), "SECRET-CANARY inner")
	put(t, filepath.Join(w.root, "docs", "prompt.md"), "shared prompt inside the repo")

	link(t, filepath.Join(outside, "secret.md"), filepath.Join(w.root, ".claude", "commands", "leak.md"))
	link(t, filepath.Join(outside, "dir"), filepath.Join(w.root, ".claude", "commands", "leakdir"))
	link(t, filepath.Join(w.root, "docs", "prompt.md"), filepath.Join(w.root, ".claude", "commands", "shared.md"))
	link(t, filepath.Join(w.root, ".claude", "commands"), filepath.Join(w.root, ".claude", "commands", "loop"))
	link(t, filepath.Join(w.root, "missing"), filepath.Join(w.root, ".claude", "commands", "dangling.md"))

	// The user's own dotfiles symlinks are honoured.
	dot := filepath.Join(filepath.Dir(w.home), "dotfiles")
	put(t, filepath.Join(dot, "mine.md"), "from dotfiles")
	put(t, filepath.Join(dot, "team", "t.md"), "from dotfiles dir")
	link(t, filepath.Join(dot, "mine.md"), filepath.Join(w.home, ".claude", "commands", "mine.md"))
	link(t, filepath.Join(dot, "team"), filepath.Join(w.home, ".claude", "commands", "team"))

	r, warns := w.load()
	if got := names(r); got != "mine,shared,team:t" {
		t.Fatalf("commands = %s\nwarnings:\n%s", got, warnText(warns))
	}
	e, err := r.Expand(t.Context(), "shared", "")
	if err != nil || e.Prompt != "shared prompt inside the repo" {
		t.Fatalf("shared = %q %v", e.Prompt, err)
	}
	text := warnText(warns)
	if strings.Contains(text, "SECRET-CANARY") {
		t.Errorf("a warning leaks file content:\n%s", text)
	}
	if c := strings.Count(text, "outside its allowed location"); c != 2 {
		t.Errorf("expected the file and the directory escapes to be reported:\n%s", text)
	}
}

func TestDepthLimit(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "a/b/c/ok.md", "ok")
	w.proj(".claude", "a/b/c/d/too-deep.md", "deep")
	r, warns := w.load()
	if names(r) != "a:b:c:ok" || len(warns) != 1 || !strings.Contains(warns[0].Msg, "nested more than") {
		t.Fatalf("commands %s, warnings %s", names(r), warnText(warns))
	}
}

func TestExtrasAreNamespaced(t *testing.T) {
	w := newWorld(t)
	plug := t.TempDir()
	put(t, filepath.Join(plug, "lint.md"), "plugin lint")
	put(t, filepath.Join(plug, "sub", "x.md"), "plugin sub x")
	w.user(".claude", "lint.md", "user lint")
	o := w.opts()
	o.Extra = []Extra{{Dir: plug, Namespace: "acme", Trusted: true}}
	r, warns := Load(o)
	if got := names(r); got != "acme:lint,acme:sub:x,lint" || len(warns) != 0 {
		t.Fatalf("commands %s, warnings %s", got, warnText(warns))
	}
	if e, err := r.Expand(t.Context(), "/x", ""); err != nil || e.Name != "acme:sub:x" {
		t.Fatalf("suffix resolution: %v %v", e.Name, err)
	}
	if e, err := r.Expand(t.Context(), "lint", ""); err != nil || e.Prompt != "user lint" {
		t.Fatalf("exact name wins: %v %q", err, e.Prompt)
	}
	o.TrustProject = false
	o.Extra = []Extra{{Dir: plug, Namespace: "acme"}}
	r, warns = Load(o)
	if names(r) != "lint" || len(warns) < 1 {
		t.Fatalf("untrusted extra: commands %s, warnings %s", names(r), warnText(warns))
	}
}

func TestBoundsOfTheRegistry(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < MaxCommands+10; i++ {
		w.proj(".claude", "c"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))+".md", "body")
	}
	r, warns := w.load()
	if len(r.List()) != MaxCommands || len(warns) != 10 || !strings.Contains(warns[0].Msg, "too many commands") {
		t.Fatalf("%d commands, %d warnings", len(r.List()), len(warns))
	}
}

func TestNilRegistry(t *testing.T) {
	var r *Registry
	if r.List() != nil {
		t.Fatal("List on nil")
	}
	if _, ok := r.Get("x"); ok {
		t.Fatal("Get on nil")
	}
	if _, err := r.Expand(t.Context(), "x", ""); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadIsDeterministic(t *testing.T) {
	w := newWorld(t)
	for _, n := range []string{"z", "a", "m/x", "m/a", "b"} {
		w.proj(".claude", n+".md", "body "+n)
	}
	w.user(".claude", "b.md", "shadowed")
	first, w1 := w.load()
	for i := 0; i < 5; i++ {
		again, w2 := w.load()
		if names(again) != names(first) || warnText(w2) != warnText(w1) {
			t.Fatal("not deterministic")
		}
	}
	if got := names(first); got != "a,b,m:a,m:x,z" {
		t.Fatalf("order = %s", got)
	}
}
