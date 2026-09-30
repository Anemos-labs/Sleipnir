package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzClean: normalisation must never panic and always yields text that is safe
// to embed in a prompt block: valid UTF-8, no carriage returns, no hidden or
// control characters (tag characters, bidi controls, zero-width characters, NULs,
// escapes), trimmed of surrounding blank lines.
func FuzzClean(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "a\r\nb\rc\n", "<!-- c -->", "x <!-- c --> y", "<!--\nmulti\n-->\nkeep",
		"```\n<!-- in fence -->\n```\n<!-- out -->", "`<!-- inline -->` <!-- out -->", "<!-- unterminated",
		"\xef\xbb\xbfbom", "bad \xff utf8", "@import.md\n```\n@no.md\n```\n", "~~~\n```\n~~~\n<!-- x -->",
		"tags \U000E0041\U000E0042 end", "rlo \u202edcba\u202c", "zw<\u200b!\u200b--x-->y", "@\u200bimport.md", "esc \x1b[31m red \x00 nul \x07",
		"ls\u2028ps\u2029nel\u0085", "vs \ufe0f \U000E0100", "\xf3\xa0\x81", // a truncated tag character
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out := clean(raw)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 out of clean(%q): %q", raw, out)
		}
		if strings.ContainsRune(out, '\r') {
			t.Fatalf("carriage return survived: %q", out)
		}
		if out != strings.Trim(out, "\n") {
			t.Fatalf("not trimmed: %q", out)
		}
		if r := hiddenIn(out); r != 0 {
			t.Fatalf("hidden code point U+%04X survived clean(%q): %q", r, raw, out)
		}
		_ = findImports(out) // must not panic either
	})
}

// FuzzLoadNeverLeavesTheRoot: whatever an instruction file says (import paths with dots, slashes,
// tildes, hidden names and invisible characters, or content that tries to smuggle text), Load never
// panics, nothing planted outside the project or under a hidden name reaches the prompt, and the
// rendered text carries no hidden code point.
func FuzzLoadNeverLeavesTheRoot(f *testing.F) {
	for _, seed := range []string{
		"../outside/canary.md", "/etc/passwd", "~/notes.md", "ln.md", "dirln/canary.md", ".env.md", ".git/notes.md",
		"docs/../../outside/canary.md", "docs//..//..//outside/canary.md", "\u200b../outside/canary.md", "..\\outside\\canary.md",
		"a\u0000b.md", "~", "~/.sleipnir/x.md", "docs/ok.md", "./docs/./ok.md", ".sleipnir/ok.md", "%2e%2e/outside/canary.md",
		"\U000E0041../outside/canary.md", "ok.md\u202e", strings.Repeat("../", 50) + "outside/canary.md",
	} {
		f.Add("intro\n", seed)
	}
	f.Fuzz(func(t *testing.T, content, spec string) {
		base := t.TempDir()
		root := filepath.Join(base, "repo")
		home := filepath.Join(base, "home")
		plant := map[string]string{
			filepath.Join(base, "outside", "canary.md"): "CANARY-OUTSIDE\n",
			filepath.Join(home, "notes.md"):             "CANARY-HOME\n",
			filepath.Join(home, ".sleipnir", "x.md"):    "CANARY-USERDIR\n",
			filepath.Join(root, ".env.md"):              "CANARY-HIDDEN\n",
			filepath.Join(root, ".git", "notes.md"):     "CANARY-GIT\n",
			filepath.Join(root, "docs", "ok.md"):        "fine\n",
			filepath.Join(root, ".sleipnir", "ok.md"):   "fine too\n",
		}
		for p, c := range plant {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Skip(err)
			}
			if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
				t.Skip(err)
			}
		}
		_ = os.Symlink(filepath.Join(base, "outside", "canary.md"), filepath.Join(root, "ln.md"))
		_ = os.Symlink(filepath.Join(base, "outside"), filepath.Join(root, "dirln"))
		if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(content+"\n@"+spec+"\n"), 0o644); err != nil {
			t.Skip(err)
		}
		srcs, _ := Load(Opts{Root: root, Home: home})
		out := Render(srcs)
		for _, canary := range []string{"CANARY-OUTSIDE", "CANARY-HOME", "CANARY-USERDIR", "CANARY-HIDDEN", "CANARY-GIT"} {
			if strings.Contains(out, canary) && !strings.Contains(content, canary) {
				t.Fatalf("%s reached the prompt through @%q:\n%s", canary, spec, out)
			}
		}
		if r := hiddenIn(out); r != 0 {
			t.Fatalf("hidden code point U+%04X in the render", r)
		}
	})
}
