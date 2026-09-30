package perm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchSegs(t *testing.T) {
	for _, tc := range []struct {
		pat  string
		path string
		want bool
	}{
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false}, // matchSegs is a full match; prefix matching is pathGlob.matches
		{"a/*", "a/b", true},
		{"a/*", "a/b/c", false},
		{"a/**", "a", true},
		{"a/**", "a/b/c/d", true},
		{"**/c", "c", true},
		{"**/c", "a/b/c", true},
		{"**/c", "a/b/d", false},
		{"a/**/c", "a/c", true},
		{"a/**/c", "a/x/y/c", true},
		{"a/**/c", "a/x/y/d", false},
		{"**", "", true},
		{"**", "x/y", true},
		{"**/**/x", "x", true},
		{"a/**/b/**/c", "a/1/b/2/3/c", true},
		{"a/**/b/**/c", "a/1/c", false},
		{"*.go", "main.go", true},
		{"*.go", "main.go.bak", false},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"[ab].go", "b.go", true},
		{"[!ab].go", "c.go", true},
		{"[!ab].go", "a.go", false},
		{`\*.go`, "*.go", true},
		{`\*.go`, "a.go", false},
	} {
		got := matchSegs(splitSegs(tc.pat), splitSegs(tc.path))
		if got != tc.want {
			t.Errorf("matchSegs(%q, %q) = %v, want %v", tc.pat, tc.path, got, tc.want)
		}
	}
}

func TestPathGlobCompilation(t *testing.T) {
	f := newFixture(t)
	rs := newResolver(f.home, f.root, nil)
	type c struct {
		pat    string
		action Action
		path   string
		want   bool
	}
	R, H, O := f.root, f.home, f.outside
	cases := []c{
		// anchoring: relative to the workspace
		{"src/**", Allow, R + "/src/a.go", true},
		{"src/**", Allow, R + "/src", true},
		{"src/**", Allow, R + "/other/src/a.go", false},
		{"./src/**", Allow, R + "/src/a.go", true},
		{"src", Deny, R + "/src/a.go", true}, // no slash: floating, so it matches any component named src
		{"docs/*.md", Allow, R + "/docs/README.md", true},
		{"docs/*.md", Allow, R + "/docs/deep/README.md", false},
		{"docs/**/*.md", Allow, R + "/docs/deep/README.md", true},
		{"docs/**/*.md", Allow, R + "/docs/README.md", true},
		// anchoring: home and absolute
		{"~/.ssh/**", Deny, H + "/.ssh/id_rsa", true},
		{"~/.ssh", Deny, H + "/.ssh/id_rsa", true},
		{"~/.ssh", Deny, H + "/.sshx", false},
		{"~", Deny, H + "/anything/at/all", true},
		{"~/*.txt", Deny, H + "/notes.txt", true},
		{"~/*.txt", Deny, H + "/sub/notes.txt", false},
		{O + "/**", Allow, O + "/secret.txt", true},
		{O + "/secret.txt", Allow, O + "/secret.txt", true},
		{O + "/secret.txt", Allow, O + "/secret.txt.bak", false},
		{"/etc/**", Deny, "/etc/passwd", true},
		{"/etc", Deny, "/etc/ssh/sshd_config", true},
		{"/", Deny, "/anything", true},
		// floating patterns: deny/ask everywhere, allow only inside the workspace
		{"*.pem", Deny, O + "/cert.pem", true},
		{"*.pem", Deny, R + "/a/b/c.pem", true},
		{"*.pem", Allow, O + "/cert.pem", false},
		{"*.pem", Allow, R + "/a/b/c.pem", true},
		{"secrets", Deny, R + "/secrets/key.txt", true},
		{"secrets", Ask, "/elsewhere/secrets/key.txt", true},
		{"secrets", Allow, "/elsewhere/secrets/key.txt", false},
		{".env", Deny, R + "/sub/.env", true},
		// trailing slash means "this directory": same as the bare name
		{"secrets/", Deny, R + "/secrets/key.txt", true},
		{"src/", Allow, R + "/src/a.go", true},
		// dotdot in patterns is cleaned lexically
		{"src/../docs/**", Allow, R + "/docs/README.md", true},
		{"src/../docs/**", Allow, R + "/src/a.go", false},
		// escapes and classes
		{`docs/\[x\].md`, Allow, R + "/docs/[x].md", true},
		{`docs/\[x\].md`, Allow, R + "/docs/x.md", false},
		{"src/[ab].go", Allow, R + "/src/a.go", true},
		{"src/[ab].go", Allow, R + "/src/c.go", false},
	}
	for _, tc := range cases {
		gs, err := compilePathGlobs(tc.pat, tc.action, rs)
		if err != nil {
			t.Errorf("compile %q: %v", tc.pat, err)
			continue
		}
		got := false
		for _, g := range gs {
			got = got || g.matches(tc.path)
		}
		if got != tc.want {
			t.Errorf("%s(%q) on %q = %v, want %v", tc.action, tc.pat, tc.path, got, tc.want)
		}
	}
	for _, bad := range []string{"!x", "src/[", "a/[b", `a/\`} {
		if _, err := compilePathGlobs(bad, Allow, rs); err == nil {
			t.Errorf("compile %q: expected an error", bad)
		}
	}
}

func TestRealPath(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(base, "a", "b"), 0o755))
	must(os.WriteFile(filepath.Join(base, "a", "b", "file"), []byte("x"), 0o644))
	must(os.Symlink(filepath.Join(base, "a", "b"), filepath.Join(base, "abs-link")))
	must(os.Symlink("a/b", filepath.Join(base, "rel-link")))
	must(os.Symlink("../a", filepath.Join(base, "a", "up")))
	must(os.Symlink(filepath.Join(base, "missing", "target"), filepath.Join(base, "dangling")))
	must(os.Symlink("loop2", filepath.Join(base, "loop1")))
	must(os.Symlink("loop1", filepath.Join(base, "loop2")))
	must(os.Symlink("abs-link", filepath.Join(base, "chain")))

	for _, tc := range []struct {
		in, want string
	}{
		{filepath.Join(base, "a", "b", "file"), filepath.Join(base, "a", "b", "file")},
		{filepath.Join(base, "abs-link", "file"), filepath.Join(base, "a", "b", "file")},
		{filepath.Join(base, "rel-link", "file"), filepath.Join(base, "a", "b", "file")},
		{filepath.Join(base, "chain", "file"), filepath.Join(base, "a", "b", "file")},
		{filepath.Join(base, "a", "up", "b"), filepath.Join(base, "a", "b")},
		{filepath.Join(base, "abs-link", "new-file"), filepath.Join(base, "a", "b", "new-file")},
		{filepath.Join(base, "abs-link", "x", "y", "z"), filepath.Join(base, "a", "b", "x", "y", "z")},
		{filepath.Join(base, "dangling"), filepath.Join(base, "missing", "target")},
		{filepath.Join(base, "dangling", "child"), filepath.Join(base, "missing", "target", "child")},
		{base + "/a/../a/b", filepath.Join(base, "a", "b")},
		// ".." after a link is applied to what the link points to (a/b), not to
		// the directory that holds the link; filepath.Join would clean it away.
		{base + "/abs-link/../b", filepath.Join(base, "a", "b")},
		{base + "/rel-link/../b", filepath.Join(base, "a", "b")},
		{base + "/abs-link/../../a/b/file", filepath.Join(base, "a", "b", "file")},
		{base + "/dangling/../x", filepath.Join(base, "missing", "x")},
		{"/", "/"},
		{"relative/path", "relative/path"},
	} {
		if got := realPath(tc.in); got != tc.want {
			t.Errorf("realPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// A loop terminates and returns something sane.
	if got := realPath(filepath.Join(base, "loop1", "x")); got == "" {
		t.Error("loop: empty result")
	}
}

func TestExpandGlob(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.go", "b.go", "c.txt", ".hidden", ".hidden2", "d/x.go", "d/.dot", "e/f/g.go", ".dd/z"} {
		full := filepath.Join(base, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rel := func(ms []string) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			r, _ := filepath.Rel(base, m)
			out[i] = r
		}
		return out
	}
	for _, tc := range []struct {
		pat  string
		want []string
	}{
		{"*.go", []string{"a.go", "b.go"}},
		{"*", []string{"a.go", "b.go", "c.txt", "d", "e"}}, // no dotfiles, like the shell
		{".*", []string{".dd", ".hidden", ".hidden2"}},
		{".h*", []string{".hidden", ".hidden2"}},
		{"?.go", []string{"a.go", "b.go"}},
		{"[ab].go", []string{"a.go", "b.go"}},
		{"[!a].go", []string{"b.go"}},
		{"d/*", []string{"d/x.go"}},
		{"d/.*", []string{"d/.dot"}},
		{"*/x.go", []string{"d/x.go"}},
		{"*/*/g.go", []string{"e/f/g.go"}},
		{".d*/z", []string{".dd/z"}},
		{"nomatch*", nil},
		{"d/nomatch/*", nil},
	} {
		budget := 1000
		ms, ok := expandGlob(filepath.Join(base, tc.pat), &budget)
		if !ok {
			t.Errorf("%s: budget exhausted", tc.pat)
			continue
		}
		got := rel(ms)
		if len(got) != len(tc.want) {
			t.Errorf("expandGlob(%q) = %q, want %q", tc.pat, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("expandGlob(%q) = %q, want %q", tc.pat, got, tc.want)
				break
			}
		}
	}
	budget := 3
	if _, ok := expandGlob(filepath.Join(base, "*"), &budget); ok {
		t.Error("a tiny budget must be reported as exhausted")
	}
}

func TestProtect(t *testing.T) {
	f := newFixture(t)
	rs := newResolver(f.home, f.root, nil)
	acc := func(p string, write, tree, content bool) access {
		return access{raw: p, lex: p, real: realPath(p), read: !write, write: write, tree: tree, content: content}
	}
	for _, tc := range []struct {
		name string
		a    access
		want tier
	}{
		{"ssh private key read", acc(f.home+"/.ssh/id_rsa", false, false, false), tierHard},
		{"ssh private key write", acc(f.home+"/.ssh/id_rsa", true, false, false), tierHard},
		{"ssh public key read", acc(f.home+"/.ssh/id_rsa.pub", false, false, false), tierGuarded},
		{"ssh public key write", acc(f.home+"/.ssh/id_rsa.pub", true, false, false), tierHard},
		{"ssh known_hosts read", acc(f.home+"/.ssh/known_hosts", false, false, false), tierGuarded},
		{"ssh dir listing", acc(f.home+"/.ssh", false, false, false), tierGuarded},
		{"ssh dir recursive read", acc(f.home+"/.ssh", false, true, true), tierHard},
		{"ancestor recursive names only", acc(f.home, false, true, false), tierNone},
		{"ancestor recursive content", acc(f.home, false, true, true), tierHard},
		{"ancestor delete", acc(f.home, true, true, false), tierHard},
		{"aws", acc(f.home+"/.aws/credentials", false, false, false), tierHard},
		{"gnupg", acc(f.home+"/.gnupg/x", false, false, false), tierHard},
		{"gcloud", acc(f.home+"/.config/gcloud/x", false, false, false), tierHard},
		{"other config is fine", acc(f.home+"/.config/other/x", false, false, false), tierNone},
		{"npmrc", acc(f.home+"/.npmrc", false, false, false), tierGuarded},
		{"dotenv", acc(f.root+"/.env", false, false, false), tierGuarded},
		{"dotenv.local", acc(f.root+"/a/.env.local", false, false, false), tierGuarded},
		{"dotenv upper", acc(f.root+"/.ENV", false, false, false), tierGuarded},
		{"envrc", acc(f.root+"/.envrc", false, false, false), tierNone},
		{"env dir file", acc(f.root+"/env/x", false, false, false), tierNone},
		{"git write", acc(f.root+"/.git/config", true, false, false), tierHard},
		{"git read", acc(f.root+"/.git/config", false, false, false), tierNone},
		{"github write", acc(f.root+"/.github/x", true, false, false), tierNone},
		{"gitignore write", acc(f.root+"/.gitignore", true, false, false), tierNone},
		{"etc write", acc("/etc/hosts", true, false, false), tierHard},
		{"etc read", acc("/etc/hosts", false, false, false), tierNone},
		{"etc shadow read", acc("/etc/shadow", false, false, false), tierHard},
		{"usr write", acc("/usr/local/x", true, false, false), tierHard},
		{"root delete", acc("/", true, true, false), tierHard},
		{"dev null write", acc("/dev/null", true, false, false), tierNone},
		{"dev sda write", acc("/dev/sda", true, false, false), tierHard},
		{"dev shm write", acc("/dev/shm/x", true, false, false), tierNone},
		{"proc environ", acc("/proc/self/environ", false, false, false), tierHard},
		{"tmp write is fine", acc("/tmp/x", true, false, false), tierNone},
		{"workspace write is fine", acc(f.root+"/src/a.go", true, false, false), tierNone},
	} {
		if got := rs.protect(tc.a).tier; got != tc.want {
			t.Errorf("%s: tier %d, want %d (%s)", tc.name, got, tc.want, rs.protect(tc.a).why)
		}
	}

	// A workspace under a system directory is still a workspace, but a root of
	// "/" does not switch the system-directory rule off.
	rs2 := newResolver(f.home, "/usr/src/proj", nil)
	w := access{raw: "x", lex: "/usr/src/proj/x", real: "/usr/src/proj/x", write: true}
	if got := rs2.protect(w).tier; got != tierNone {
		t.Errorf("write inside a workspace under /usr/src: tier %d", got)
	}
	w2 := access{raw: "x", lex: "/usr/bin/x", real: "/usr/bin/x", write: true}
	if got := rs2.protect(w2).tier; got != tierHard {
		t.Errorf("write to /usr/bin with that workspace: tier %d", got)
	}
	rs3 := newResolver(f.home, "/", nil)
	if got := rs3.protect(w2).tier; got != tierHard {
		t.Errorf("a workspace of / must not open /usr/bin: tier %d", got)
	}
}
