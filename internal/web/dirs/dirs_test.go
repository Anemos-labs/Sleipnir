package dirs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tree makes folders (a name ending in "/") and files under a temporary folder and returns it.
func tree(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		p := filepath.Join(root, filepath.FromSlash(n))
		if strings.HasSuffix(n, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// entryNames are the names of the entries Complete offers for text.
func entryNames(t *testing.T, root, text string, o Options) []string {
	t.Helper()
	l, err := Complete(text, o)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range l.Entries {
		out = append(out, e.Name)
	}
	return out
}

// A path that ends in a separator lists every subfolder of that folder, files and hidden folders left out; the unfinished last
// part of a path lists the folders that start with it, ignoring case, the ones that match the case typed first.
func TestCompleteListsFoldersByPrefix(t *testing.T) {
	root := tree(t, "shop/", "Shed/", "shell-notes.txt", "docs/", ".cache/", "shop2/")
	o := Options{Base: root}
	got := func(text string) string { return strings.Join(entryNames(t, root, text, o), ",") }
	sep := string(filepath.Separator)
	if g := got(root + sep); g != "docs,Shed,shop,shop2" {
		t.Errorf("all folders: %s", g)
	}
	if g := got(filepath.Join(root, "sh")); g != "shop,shop2,Shed" {
		t.Errorf("sh: %s (the case typed first, then the others)", g)
	}
	if g := got(filepath.Join(root, "Sh")); g != "Shed,shop,shop2" {
		t.Errorf("Sh: %s", g)
	}
	if g := got(filepath.Join(root, "sho")); g != "shop,shop2" {
		t.Errorf("sho: %s", g)
	}
	if g := got(root + sep + "."); g != ".cache" {
		t.Errorf("a dot lists the hidden folders: %s", g)
	}
	if g := got(filepath.Join(root, "nope")); g != "" {
		t.Errorf("nothing starts with nope: %s", g)
	}
	l, _ := Complete(filepath.Join(root, "sho"), o)
	if l.Dir != root || l.Prefix != "sho" {
		t.Errorf("dir %q prefix %q", l.Dir, l.Prefix)
	}
	if g := got("sho"); g != "shop,shop2" {
		t.Errorf("a relative path is read from the base folder: %s", g)
	}
	if g := got(root + sep + "shop" + sep + ".."); g != "docs,Shed,shop,shop2" {
		t.Errorf("a path that ends in .. lists the folder it names: %s", g)
	}
}

// A relative path is read from the base folder, ~ is the home folder, ~name and a path with a NUL byte are refused, and nothing typed is
// an error of its own.
func TestExpandPaths(t *testing.T) {
	root := tree(t, "code/shop/")
	o := Options{Home: root, Base: filepath.Join(root, "code")}
	for text, want := range map[string]string{
		"~":              root,
		"~/code":         filepath.Join(root, "code"),
		"shop":           filepath.Join(root, "code", "shop"),
		"./shop/../shop": filepath.Join(root, "code", "shop"),
		"  ~/code/  ":    filepath.Join(root, "code"),
	} {
		got, err := Expand(text, o)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", text, got, err, want)
		}
	}
	if runtime.GOOS != "windows" { // a path with no drive is not absolute there
		if got, err := Expand("/a//b/../c", o); err != nil || got != "/a/c" {
			t.Errorf("/a//b/../c: %q, %v", got, err)
		}
	}
	for _, text := range []string{"~other/x", "a\x00b", strings.Repeat("a", maxPath+1), "\xff\xfe"} {
		if _, err := Expand(text, o); err == nil {
			t.Errorf("%q was taken", text)
		}
	}
	if _, err := Expand("  ", o); err != ErrEmpty {
		t.Errorf("blank: %v", err)
	}
	if _, err := Expand("~/x", Options{}); err == nil {
		t.Error("~ without a home folder was taken")
	}
	if _, err := Expand("x", Options{}); err == nil {
		t.Error("a relative path without a base was taken")
	}
}

// What a path names: a folder that exists (empty or not, a repository, a folder inside one), one that does not (whose parent does, or
// does not), a file, and the checks its files suggest.
func TestInspectSaysWhatThePathIs(t *testing.T) {
	root := tree(t, "empty/", "full/a.txt", "repo/.git/", "repo/sub/", "bare/HEAD", "bare/objects/", "bare/refs/", "mod/go.mod", "mod/package.json", "mod/Makefile", "file.txt", "node/package.json", "py/setup.py", "py/pyproject.toml")
	st := func(rel string) (s struct {
		Exists, Dir, ParentOK, Empty bool
		Git, GitRoot                 string
		Verify                       []string
	}) {
		d := Inspect(filepath.Join(root, filepath.FromSlash(rel)))
		s.Exists, s.Dir, s.ParentOK, s.Empty, s.Git, s.GitRoot = d.Exists, d.Dir, d.ParentOK, d.Empty, d.Git, d.GitRoot
		for _, v := range d.Verify {
			s.Verify = append(s.Verify, v.Cmd+" ("+v.Why+")")
		}
		return
	}
	if s := st("empty"); !s.Exists || !s.Dir || !s.Empty || s.Git != "none" {
		t.Errorf("empty: %+v", s)
	}
	if s := st("full"); !s.Dir || s.Empty {
		t.Errorf("full: %+v", s)
	}
	if s := st("repo"); s.Git != "repo" || s.GitRoot != "" {
		t.Errorf("repo: %+v", s)
	}
	if s := st("bare"); s.Git != "bare" {
		t.Errorf("a bare repository: %+v", s)
	}
	if s := st("repo/sub"); s.Git != "inside" || s.GitRoot != filepath.Join(root, "repo") {
		t.Errorf("repo/sub: %+v", s)
	}
	if s := st("repo/new"); s.Exists || !s.ParentOK || s.Git != "inside" {
		t.Errorf("a folder to make in a repository would be inside it: %+v", s)
	}
	if s := st("new"); s.Exists || !s.ParentOK || s.Git != "none" {
		t.Errorf("new: %+v", s)
	}
	if s := st("no/such/deep"); s.Exists || s.ParentOK {
		t.Errorf("no/such/deep: %+v", s)
	}
	if s := st("file.txt"); !s.Exists || s.Dir {
		t.Errorf("a file is not a folder: %+v", s)
	}
	if s := st("mod"); strings.Join(s.Verify, "; ") != "go test {dirs} (go.mod); npm test (package.json); make test (Makefile)" {
		t.Errorf("mod: %v", s.Verify)
	}
	if s := st("py"); strings.Join(s.Verify, "; ") != "pytest (pyproject.toml)" {
		t.Errorf("one suggestion for each command: %v", s.Verify)
	}
	if s := st("empty"); len(s.Verify) != 0 {
		t.Errorf("an empty folder suggests nothing: %v", s.Verify)
	}
}

// A link to a folder is offered (and marked); a link to a file, a broken link, and a name with a control character, a character that
// reorders text or bytes that are not UTF-8 are not.
func TestCompleteOffersOnlyFoldersWithNamesThatAreSafeToShow(t *testing.T) {
	root := tree(t, "real/", "plain.txt", "r-repo/.git/")
	mk := func(name string) {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Skipf("this file system does not take the name %q: %v", name, err)
		}
	}
	if runtime.GOOS != "windows" {
		mk("r\nnew")
		mk("r‮gnp")
		mk("r\xffbad")
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "r-link")); err != nil {
		t.Skipf("no links here: %v", err)
	}
	os.Symlink(filepath.Join(root, "plain.txt"), filepath.Join(root, "r-file"))
	os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "r-broken"))
	l, err := Complete(filepath.Join(root, "r"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range l.Entries {
		got = append(got, e.Name)
		if e.Name == "r-link" && !e.Link {
			t.Error("a link to a folder is not marked")
		}
		if e.Name == "r-repo" && !e.Repo {
			t.Error("a repository is not marked")
		}
	}
	if strings.Join(got, ",") != "r-link,r-repo,real" {
		t.Errorf("offered %q", got)
	}
}

// A listing offers at most MaxEntries and says there are more.
func TestCompleteBoundsTheListing(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxEntries+7; i++ {
		if err := os.Mkdir(filepath.Join(root, "d"+string(rune('a'+i/26))+string(rune('a'+i%26))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l, err := Complete(root+string(filepath.Separator), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != MaxEntries || !l.More {
		t.Errorf("%d entries, more %v", len(l.Entries), l.More)
	}
	if l.Entries[0].Name != "daa" {
		t.Errorf("the first is %q", l.Entries[0].Name)
	}
}

// The root of the file system is a folder to list, not a name in its parent; a folder that cannot be read lists nothing and the state
// says so.
func TestCompleteOfTheRootAndOfWhatCannotBeRead(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	l, err := Complete(root, Options{})
	if err != nil || l.Dir != root || l.Prefix != "" || !l.State.Dir {
		t.Errorf("root: %+v, %v", l, err)
	}
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions do not hide a folder here")
	}
	dir := tree(t, "locked/inside/")
	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	l, err = Complete(locked+string(filepath.Separator), Options{})
	if err != nil || len(l.Entries) != 0 || !l.State.Dir || l.State.Readable {
		t.Errorf("a folder that cannot be read: %+v, %v", l, err)
	}
}

// A path that cannot be looked at for another reason than that it is not there (a link that loops) is there as far as anyone can tell:
// it is never taken for absent, so that nothing is made where it stands.
func TestInspectDoesNotTakeWhatCannotBeLookedAtForAbsent(t *testing.T) {
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Skipf("no links here: %v", err)
	}
	st := Inspect(loop)
	if !st.Exists || !st.Inaccessible || st.Dir {
		t.Errorf("a link that loops: %+v", st)
	}
	if st := Inspect(filepath.Join(root, "absent")); st.Exists || st.Inaccessible || !st.ParentOK {
		t.Errorf("a path that is not there: %+v", st)
	}
}
