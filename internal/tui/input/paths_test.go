package input

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// pathTree builds
//
//	root/README.md  root/main.go  root/.hidden  root/.git/HEAD  root/docs/  root/src/a.go  root/src/b.go  root/src/sub/deep.go
//	root/link -> src (inside)   root/flink -> README.md (inside)   root/escape -> ../outside   root/fescape -> ../outside/secret.txt
//	root/dangling -> nowhere    root/abs -> <outside>
//
// and returns root. The symbolic links are skipped, with ok false, where they cannot be made.
func pathTree(t *testing.T) (root string, links bool) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{"docs", "src/sub", ".git"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"README.md", "main.go", ".hidden", ".git/HEAD", "src/a.go", "src/b.go", "src/sub/deep.go", "my file.txt"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("secret contents\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside secret\n"), 0o644)
	os.WriteFile(filepath.Join(outside, "other.txt"), []byte("outside\n"), 0o644)
	if err := os.Symlink("src", filepath.Join(root, "link")); err != nil {
		return root, false // no symbolic links here (Windows without the privilege)
	}
	os.Symlink("README.md", filepath.Join(root, "flink"))
	os.Symlink(filepath.Join("..", "outside"), filepath.Join(root, "escape"))
	os.Symlink(filepath.Join("..", "outside", "secret.txt"), filepath.Join(root, "fescape"))
	os.Symlink("nowhere", filepath.Join(root, "dangling"))
	os.Symlink(outside, filepath.Join(root, "abs"))
	return root, true
}

func complete(c Completer, line string) (int, []string) {
	from, cands := c.Complete(line, len(line))
	return from, candTexts(cands)
}

func TestPathsListing(t *testing.T) {
	root, links := pathTree(t)
	c := Paths(root)

	// nothing typed after @: directories first, then files, hidden last
	from, got := complete(c, "@")
	want := []string{"@docs/", "@src/", "@README.md ", "@main.go ", "@.git/", "@.hidden "}
	if links {
		want = []string{"@docs/", "@link/", "@src/", "@README.md ", "@flink ", "@main.go ", "@.git/", "@.hidden "}
	}
	if from != 0 || !reflect.DeepEqual(got, want) {
		t.Errorf("root listing\n got  %q\n want %q", got, want)
	}

	// the word can be anywhere in the line; from is where the @ is
	from, got = complete(c, "look at @sr")
	if from != len("look at ") || !reflect.DeepEqual(got, []string{"@src/"}) {
		t.Errorf("@sr: from %d %q", from, got)
	}

	_, got = complete(c, "@src/")
	if !reflect.DeepEqual(got, []string{"@src/sub/", "@src/a.go ", "@src/b.go "}) {
		t.Errorf("@src/: %q", got)
	}
	_, got = complete(c, "@src/su")
	if !reflect.DeepEqual(got, []string{"@src/sub/"}) {
		t.Errorf("@src/su: %q", got)
	}
	_, got = complete(c, "@src/sub/")
	if !reflect.DeepEqual(got, []string{"@src/sub/deep.go "}) {
		t.Errorf("@src/sub/: %q", got)
	}
	_, got = complete(c, "@./src/")
	if !reflect.DeepEqual(got, []string{"@./src/sub/", "@./src/a.go ", "@./src/b.go "}) {
		t.Errorf("a leading ./ is kept as typed: %q", got)
	}
	// fuzzy, and case-insensitive
	if _, got = complete(c, "@mn"); !reflect.DeepEqual(got, []string{"@main.go "}) {
		t.Errorf("@mn: %q", got)
	}
	if _, got = complete(c, "@read"); !reflect.DeepEqual(got, []string{"@README.md "}) {
		t.Errorf("@read: %q", got)
	}
	// a file is not a directory: nothing, and (see TestPathsNeverOpensFiles) it is not opened
	if _, got = complete(c, "@README.md/"); len(got) != 0 {
		t.Errorf("a file as a directory: %q", got)
	}
	// hidden entries lead when the user types a dot
	if _, got = complete(c, "@."); !reflect.DeepEqual(got, []string{"@.git/", "@.hidden ", "@main.go ", "@README.md "}) {
		t.Errorf("@. (hidden names first, then the names that merely contain a dot): %q", got)
	}
	// names with a space are not offered: the word would end at the space
	for _, s := range got {
		if strings.Contains(s, "my file") {
			t.Error("a name with a space was offered")
		}
	}
	if _, got = complete(c, "@my"); len(got) != 0 {
		t.Errorf("@my: %q", got)
	}
	// not a path word
	for _, line := range []string{"", "src", "a@b", "@", "x"} {
		if line == "@" {
			continue
		}
		if _, got = complete(c, line); len(got) != 0 {
			t.Errorf("%q: %q", line, got)
		}
	}
	// the cursor in the middle of the line: only the text before it counts
	if from, cands := c.Complete("@src/b.go and more", len("@src/b.")); from != 0 || !reflect.DeepEqual(candTexts(cands), []string{"@src/b.go "}) {
		t.Errorf("cursor mid-line: %d %q", from, candTexts(cands))
	}
}

func TestPathsNeverLeaveTheRoot(t *testing.T) {
	root, links := pathTree(t)
	c := Paths(root)
	for _, line := range []string{"@../", "@../outside/", "@..", "@src/../../", "@/", "@/etc/", "@" + filepath.ToSlash(filepath.Dir(root)) + "/", "@src\\..\\", "@~/"} {
		if _, got := complete(c, line); len(got) != 0 {
			t.Errorf("%q escaped the root: %q", line, got)
		}
	}
	// ".." that stays inside is fine
	if _, got := complete(c, "@src/../ma"); !reflect.DeepEqual(got, []string{"@src/../main.go "}) {
		t.Errorf("an inner ..: %q", got)
	}
	if !links {
		t.Skip("symbolic links cannot be created here")
	}
	_, got := complete(c, "@")
	for _, bad := range []string{"escape", "fescape", "dangling", "abs"} {
		for _, g := range got {
			if strings.HasPrefix(g, "@"+bad) {
				t.Errorf("%s is a symbolic link that does not stay inside the root, yet it is offered: %q", bad, got)
			}
		}
	}
	for _, line := range []string{"@escape/", "@escape/o", "@abs/", "@fescape/"} {
		if _, got := complete(c, line); len(got) != 0 {
			t.Errorf("%q followed a link out of the root: %q", line, got)
		}
	}
	// a link that stays inside is followed, and is offered as what it is
	if _, got := complete(c, "@link/"); !reflect.DeepEqual(got, []string{"@link/sub/", "@link/a.go ", "@link/b.go "}) {
		t.Errorf("@link/: %q", got)
	}
	if _, got := complete(c, "@fl"); !reflect.DeepEqual(got, []string{"@flink "}) {
		t.Errorf("a link to a file is a file: %q", got)
	}
}

func TestPathsOnAMissingRoot(t *testing.T) {
	c := Paths(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, got := complete(c, "@"); len(got) != 0 {
		t.Errorf("%q", got)
	}
	if _, got := complete(Paths(""), "@"); len(got) != 0 {
		t.Errorf("%q", got)
	}
}

// spyFS records how it is used: which names are opened, what is read, and how many directory entries are handed out.
type spyFS struct {
	t       *testing.T
	entries int // entries of the big directory
	served  int // entries returned by ReadDir
	opened  []string
}

func (s *spyFS) Open(name string) (fs.File, error) {
	s.opened = append(s.opened, name)
	switch {
	case name == ".":
		return &spyDir{s: s}, nil
	case name == "file.txt":
		return &spyFile{s: s}, nil
	}
	return nil, fs.ErrNotExist
}

func (s *spyFS) Stat(name string) (fs.FileInfo, error) {
	switch name {
	case ".":
		return spyInfo{name: ".", dir: true}, nil
	case "file.txt":
		return spyInfo{name: "file.txt"}, nil
	}
	return nil, fs.ErrNotExist
}

type spyInfo struct {
	name string
	dir  bool
}

func (i spyInfo) Name() string { return i.name }
func (i spyInfo) Size() int64  { return 0 }
func (i spyInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i spyInfo) ModTime() time.Time { return time.Time{} }
func (i spyInfo) IsDir() bool        { return i.dir }
func (i spyInfo) Sys() any           { return nil }

type spyFile struct{ s *spyFS }

func (f *spyFile) Stat() (fs.FileInfo, error) { return spyInfo{name: "file.txt"}, nil }
func (f *spyFile) Read([]byte) (int, error) {
	f.s.t.Error("the completer read file contents")
	return 0, io.EOF
}
func (f *spyFile) Close() error { return nil }

type spyDir struct {
	s    *spyFS
	next int
}

func (d *spyDir) Stat() (fs.FileInfo, error) { return spyInfo{name: ".", dir: true}, nil }
func (d *spyDir) Read([]byte) (int, error)   { return 0, io.EOF }
func (d *spyDir) Close() error               { return nil }
func (d *spyDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		d.s.t.Error("ReadDir(-1) would read the whole directory")
		n = d.s.entries
	}
	var out []fs.DirEntry
	for len(out) < n && d.next < d.s.entries {
		out = append(out, fs.FileInfoToDirEntry(spyInfo{name: "f" + itoa(d.next)}))
		d.next++
	}
	d.s.served += len(out)
	if len(out) == 0 {
		return nil, io.EOF
	}
	return out, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestPathsWorkPerKeystrokeIsBounded(t *testing.T) {
	spy := &spyFS{t: t, entries: 1_000_000}
	c := PathsFS(spy)
	_, cands := c.Complete("@", 1)
	if spy.served > PathScanLimit {
		t.Errorf("%d directory entries read for one keystroke, limit %d", spy.served, PathScanLimit)
	}
	if len(cands) > maxPathCands || len(cands) == 0 {
		t.Errorf("%d candidates", len(cands))
	}
	spy.served = 0
	c.Complete("@f99", 4)
	if spy.served > PathScanLimit {
		t.Errorf("filtering: %d entries read", spy.served)
	}
}

func TestPathsNeverOpensFiles(t *testing.T) {
	spy := &spyFS{t: t, entries: 3}
	c := PathsFS(spy)
	if _, cands := c.Complete("@file.txt/", 10); len(cands) != 0 {
		t.Errorf("%v", cands)
	}
	for _, o := range spy.opened {
		if o == "file.txt" {
			t.Error("a regular file was opened, even just to see what it is")
		}
	}
}

func TestPathsFSSkipsSymbolicLinks(t *testing.T) {
	fsys := fstest.MapFS{
		"real.txt":      {Data: []byte("x")},
		"dir/inner.txt": {Data: []byte("x")},
		"link":          {Mode: fs.ModeSymlink, Data: []byte("dir")},
	}
	_, got := complete(PathsFS(fsys), "@")
	if !reflect.DeepEqual(got, []string{"@dir/", "@real.txt "}) {
		t.Errorf("a plain fs.FS cannot vouch for a link: %q", got)
	}
	if _, got = complete(PathsFS(fsys), "@dir/"); !reflect.DeepEqual(got, []string{"@dir/inner.txt "}) {
		t.Errorf("%q", got)
	}
}

func TestPathsSkipsUnsafeNames(t *testing.T) {
	fsys := fstest.MapFS{
		"ok.txt":            {},
		"esc\x1b[31mred.go": {},
		"bell\x07.go":       {},
		"tab\tname":         {},
		"new\nline":         {},
		"bad\xffutf8":       {},
		"white space":       {},
	}
	if _, got := complete(PathsFS(fsys), "@"); !reflect.DeepEqual(got, []string{"@ok.txt "}) {
		t.Errorf("only the safe name is offered: %q", got)
	}
}

func TestPathsThroughTheEditor(t *testing.T) {
	root, _ := pathTree(t)
	r := newRig(t, Options{Completer: Paths(root)})
	r.send("read @sr")
	m := r.ed.Completion()
	if !m.Open || !reflect.DeepEqual(candTexts(m.Candidates), []string{"@src/"}) {
		t.Fatalf("typing @sr opens the menu: %+v", m)
	}
	r.send(kTab)
	if r.state() != "read @src/|" || !r.ed.Completion().Open {
		t.Fatalf("a directory stays open: %q %+v", r.state(), r.ed.Completion())
	}
	r.send("b.")
	if got := candTexts(r.ed.Completion().Candidates); !reflect.DeepEqual(got, []string{"@src/b.go "}) {
		t.Errorf("typing narrows it: %q", got)
	}
	r.send(kTab)
	if r.state() != "read @src/b.go |" || r.ed.Completion().Open {
		t.Errorf("a file ends the word: %q", r.state())
	}
	r.send("now", kEnter)
	if got := r.events[len(r.events)-2]; got != (Submit{Text: "read @src/b.go now"}) {
		t.Errorf("submit: %v", got)
	}
}
