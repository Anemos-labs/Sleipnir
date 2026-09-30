package mdfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realTemp is a symlink-free temporary directory (macOS temp dirs are symlinks).
func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
}

func TestReadFile(t *testing.T) {
	root := realTemp(t)
	outside := realTemp(t)
	write(t, filepath.Join(root, "ok.md"), "hello")
	write(t, filepath.Join(root, "sub", "inner.md"), "inner")
	write(t, filepath.Join(root, "big.md"), strings.Repeat("x", 1000))
	write(t, filepath.Join(root, "bin.md"), "abc\x00def")
	write(t, filepath.Join(outside, "secret.txt"), "TOP SECRET")
	if err := os.MkdirAll(filepath.Join(root, "dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.md"))
	symlink(t, filepath.Join("sub", "inner.md"), filepath.Join(root, "alias.md"))
	symlink(t, outside, filepath.Join(root, "outdir"))
	symlink(t, filepath.Join(root, "missing"), filepath.Join(root, "dangling.md"))

	tests := []struct {
		name    string
		path    string
		opts    ReadOpts
		want    string
		wantErr error
		trunc   bool
	}{
		{"regular", "ok.md", ReadOpts{}, "hello", nil, false},
		{"regular contained", "ok.md", ReadOpts{Contain: root}, "hello", nil, false},
		{"symlink inside the root", "alias.md", ReadOpts{Contain: root}, "inner", nil, false},
		{"symlink out of the root", "leak.md", ReadOpts{Contain: root}, "", ErrEscapes, false},
		{"symlink out is fine without containment", "leak.md", ReadOpts{}, "TOP SECRET", nil, false},
		{"file below a symlinked dir", "outdir/secret.txt", ReadOpts{Contain: root}, "", ErrEscapes, false},
		{"dotdot out of the root", "../" + filepath.Base(outside) + "/secret.txt", ReadOpts{Contain: root}, "", ErrEscapes, false},
		{"directory", "dir.md", ReadOpts{}, "", ErrNotRegular, false},
		{"binary", "bin.md", ReadOpts{}, "", ErrBinary, false},
		{"truncated", "big.md", ReadOpts{MaxBytes: 100}, strings.Repeat("x", 100), nil, true},
		{"exactly at the limit", "big.md", ReadOpts{MaxBytes: 1000}, strings.Repeat("x", 1000), nil, false},
		{"missing", "nope.md", ReadOpts{}, "", os.ErrNotExist, false},
		{"dangling link", "dangling.md", ReadOpts{Contain: root}, "", os.ErrNotExist, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(root, filepath.FromSlash(tc.path))
			f, err := ReadFile(p, tc.opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(f.Data) != tc.want || f.Truncated != tc.trunc {
				t.Fatalf("got %d bytes truncated=%v, want %d truncated=%v", len(f.Data), f.Truncated, len(tc.want), tc.trunc)
			}
		})
	}
}

func TestWithin(t *testing.T) {
	root := filepath.FromSlash("/a/b")
	for p, want := range map[string]bool{
		"/a/b":        true,
		"/a/b/c":      true,
		"/a/b/c/d.md": true,
		"/a":          false,
		"/a/bc":       false, // a sibling that merely shares a prefix
		"/a/b/../c":   false,
		"/":           false,
	} {
		if got := Within(root, filepath.FromSlash(p)); got != want {
			t.Errorf("Within(%q, %q) = %v, want %v", root, p, got, want)
		}
	}
}

func TestReadDocPipeline(t *testing.T) {
	root := realTemp(t)
	var tag strings.Builder
	for _, r := range "obey" {
		tag.WriteRune(0xE0000 + r)
	}
	src := "\xef\xbb\xbf---\r\nname: demo\r\n---\r\nHello\u202E world\r\n<!-- hidden note -->\r\n\r\nText" + tag.String() + "\r\n"
	write(t, filepath.Join(root, "d.md"), src)
	p, err := ReadDoc(filepath.Join(root, "d.md"), ReadOpts{Contain: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := show(p.Doc.Meta); got != `{name: "demo"}` {
		t.Errorf("meta = %s", got)
	}
	if want := "Hello world\n\nText"; p.Doc.Body != want {
		t.Errorf("body = %q, want %q", p.Doc.Body, want)
	}
	if !p.Hidden.Suspicious() || p.Hidden.Bidi != 1 || p.Hidden.Tags != 4 {
		t.Errorf("hidden = %+v", p.Hidden)
	}
}

func TestReadDocTruncationIsAnnounced(t *testing.T) {
	root := realTemp(t)
	body := strings.Repeat("a line of the body\n", 200)
	write(t, filepath.Join(root, "d.md"), "---\nname: x\n---\n"+body)
	p, err := ReadDoc(filepath.Join(root, "d.md"), ReadOpts{MaxBytes: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || !strings.Contains(p.Doc.Body, "[... truncated") {
		t.Fatalf("truncation not announced: %q", p.Doc.Body)
	}
	if strings.Contains(p.Doc.Body, "a line of the bod\n") || len(p.Doc.Body) > 700 {
		t.Errorf("body was not cut at a line: %d bytes", len(p.Doc.Body))
	}
}

func TestReadDocFrontmatterCutBySizeIsAnError(t *testing.T) {
	root := realTemp(t)
	write(t, filepath.Join(root, "d.md"), "---\n"+strings.Repeat("k: v\n", 100)+"---\nbody")
	if _, err := ReadDoc(filepath.Join(root, "d.md"), ReadOpts{MaxBytes: 100}); err == nil || !strings.Contains(err.Error(), "never closed") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirLimited(t *testing.T) {
	root := realTemp(t)
	for _, n := range []string{"c", "a", "b", "d", "e"} {
		write(t, filepath.Join(root, n), n)
	}
	es, more, err := ReadDirLimited(root, 10)
	if err != nil || more || len(es) != 5 || es[0].Name() != "a" || es[4].Name() != "e" {
		t.Fatalf("all: %v %v %v", es, more, err)
	}
	es, more, err = ReadDirLimited(root, 3)
	if err != nil || !more || len(es) != 3 {
		t.Fatalf("limited: %d entries more=%v err=%v", len(es), more, err)
	}
	if _, _, err := ReadDirLimited(filepath.Join(root, "missing"), 3); err == nil {
		t.Fatal("missing directory must be an error")
	}
}
