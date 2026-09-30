package gitx

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestDiffWorktreeIncludesEverything(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)

	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\nadded line\n") // modified
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {   // deleted
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "new dir", "ünï.txt"), "fresh\n")           // untracked, odd name
	if err := os.Chmod(filepath.Join(dir, "sub", "c.txt"), 0o755); err != nil { // mode change
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored/\n")
	writeFile(t, filepath.Join(dir, "ignored", "junk.txt"), "junk\n") // ignored: must not appear

	d, err := r.Diff(ctx, "HEAD", DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]byte{}
	for _, f := range d.Files {
		got[f.Path] = f.Status
	}
	want := map[string]byte{"a.txt": 'M', "b.txt": 'D', "new dir/ünï.txt": 'A', "sub/c.txt": 'M', ".gitignore": 'A'}
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for p, s := range want {
		if got[p] != s {
			t.Fatalf("%s: status %c, want %c (all: %v)", p, got[p], s, got)
		}
	}
	if !strings.Contains(d.Patch, "diff --git a/a.txt b/a.txt") || !strings.Contains(d.Patch, "+added line") {
		t.Fatalf("patch missing content:\n%s", d.Patch)
	}
	if !strings.Contains(d.Patch, "old mode 100644") || !strings.Contains(d.Patch, "new mode 100755") {
		t.Fatalf("mode change missing:\n%s", d.Patch)
	}
	if strings.Contains(d.Patch, "junk.txt") || strings.Contains(d.Stat, "junk") {
		t.Fatal("ignored file leaked into the diff")
	}
	if !strings.Contains(d.Stat, "new dir/ünï.txt") || !strings.Contains(d.Stat, "5 file(s) changed") {
		t.Fatalf("stat:\n%s", d.Stat)
	}
	if strings.Contains(d.Patch, "\x1b[") {
		t.Fatal("color escapes in patch")
	}
	// The real index must be untouched by a diff: nothing staged.
	st, _ := r.Status(ctx)
	if len(st.Staged) != 0 {
		t.Fatalf("Diff staged something: %+v", st.Staged)
	}
	// ChangedPaths is the sorted path list.
	paths, err := r.ChangedPaths(ctx, "HEAD", "")
	if err != nil || !sort.StringsAreSorted(paths) || len(paths) != 5 {
		t.Fatalf("ChangedPaths = %v, %v", paths, err)
	}
	// And an unchanged tree diffs empty.
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "all")
	d2, err := r.Diff(ctx, "HEAD", DiffOptions{})
	if err != nil || !d2.Empty() || d2.Patch != "" {
		t.Fatalf("clean diff: %+v, %v", d2, err)
	}
}

func TestDiffPatchAppliesToAFreshCheckout(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)

	bin := make([]byte, 8192)
	for i := range bin {
		bin[i] = byte(i*13 + i/7)
	}
	if err := os.WriteFile(filepath.Join(dir, "img.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "add binary")
	base, _ := r.Head(ctx)

	// Edit everything kind of thing: text edit, binary edit, rename, delete, new file with no trailing newline,
	// CRLF file, symlink, exec bit.
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\nmore\n")
	bin2 := append([]byte(nil), bin...)
	bin2[100] ^= 0xff
	bin2 = append(bin2, 0, 1, 2, 3)
	if err := os.WriteFile(filepath.Join(dir, "img.bin"), bin2, 0o644); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "mv", "b.txt", "sub/b-renamed.txt")
	if err := os.Remove(filepath.Join(dir, "sub", "c.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "no-newline.txt"), "no newline at end")
	writeFile(t, filepath.Join(dir, "crlf.txt"), "one\r\ntwo\r\n")
	if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
		t.Skip("no symlinks")
	}
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\necho hi\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := snapshotFiles(t, dir)

	for _, renames := range []bool{false, true} {
		d, err := r.Diff(ctx, base, DiffOptions{Renames: renames})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(d.Patch, "GIT binary patch") {
			t.Fatalf("binary change not carried as a binary patch:\n%.400s", d.Patch)
		}
		// Apply it to a pristine checkout of the base and compare every file.
		fresh := filepath.Join(t.TempDir(), "fresh")
		rawGit(t, dir, "worktree", "add", "-q", "--detach", fresh, base)
		fr := openRepo(t, fresh)
		if err := fr.Apply(ctx, d.Patch, true); err != nil {
			t.Fatalf("renames=%v: --check failed: %v", renames, err)
		}
		if ok, _ := fr.IsClean(ctx); !ok {
			t.Fatal("Apply(check) modified the tree")
		}
		if err := fr.Apply(ctx, d.Patch, false); err != nil {
			t.Fatalf("renames=%v: apply failed: %v", renames, err)
		}
		got := snapshotFiles(t, fresh)
		if len(got) != len(want) {
			t.Fatalf("renames=%v: files after apply: %v\nwant %v", renames, keys(got), keys(want))
		}
		for name, content := range want {
			if got[name] != content {
				t.Fatalf("renames=%v: %s differs after apply", renames, name)
			}
		}
		if renames && !strings.Contains(d.Patch, "rename from b.txt") {
			t.Fatalf("rename not detected:\n%s", d.Patch)
		}
		if !renames && strings.Contains(d.Patch, "rename from") {
			t.Fatal("rename detected although it was not requested")
		}
	}

	// A patch that does not apply is a typed conflict and changes nothing.
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\nmore\n")
	d, _ := r.Diff(ctx, base, DiffOptions{})
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "applied already")
	if err := r.Apply(ctx, d.Patch, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-applying: want ErrConflict, got %v", err)
	}
}

func snapshotFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Name() == ".git" {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			return nil
		}
		info, _ := os.Lstat(p)
		if info.Mode()&os.ModeSymlink != 0 {
			tgt, _ := os.Readlink(p)
			out[rel] = "link->" + tgt
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = info.Mode().String() + "|" + string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func keys(m map[string]string) []string {
	var k []string
	for name := range m {
		k = append(k, name)
	}
	sort.Strings(k)
	return k
}

func TestDiffRenamesAndStatuses(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	rawGit(t, dir, "mv", "b.txt", "moved.txt")
	d, err := r.Diff(ctx, "HEAD", DiffOptions{Renames: true})
	if err != nil || len(d.Files) != 1 || d.Files[0].Status != 'R' || d.Files[0].OldPath != "b.txt" || d.Files[0].Path != "moved.txt" {
		t.Fatalf("rename: %+v, %v", d.Files, err)
	}
	// Without rename detection a rename is delete + add, and ChangedPaths lists both ends.
	paths, err := r.ChangedPaths(ctx, "HEAD", "")
	if err != nil || strings.Join(paths, ",") != "b.txt,moved.txt" {
		t.Fatalf("ChangedPaths = %v, %v", paths, err)
	}
	d2, _ := r.Diff(ctx, "HEAD", DiffOptions{})
	if len(d2.Files) != 2 {
		t.Fatalf("no-renames files: %+v", d2.Files)
	}
}

func TestDiffBinaryStatAndLineCounts(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), bytes.Repeat([]byte{0, 1, 2, 3}, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\nx\ny\nz\n")
	d, err := r.Diff(ctx, "HEAD", DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]FileChange{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if !byPath["blob.bin"].Binary || byPath["blob.bin"].Added != -1 {
		t.Fatalf("binary flag: %+v", byPath["blob.bin"])
	}
	if a := byPath["a.txt"]; a.Binary || a.Added != 3 || a.Deleted != 0 {
		t.Fatalf("line counts: %+v", a)
	}
	if !strings.Contains(d.Stat, "blob.bin | binary") || !strings.Contains(d.Stat, "+3 -0") {
		t.Fatalf("stat:\n%s", d.Stat)
	}
	np, err := r.Diff(ctx, "HEAD", DiffOptions{NoPatch: true})
	if err != nil || np.Patch != "" || len(np.Files) != 2 {
		t.Fatalf("NoPatch: %+v, %v", np, err)
	}
}

func TestDiffCapsTruncateAtFileBoundaries(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	for i := 0; i < 12; i++ {
		writeFile(t, filepath.Join(dir, "gen", "f"+string(rune('a'+i))+".txt"), strings.Repeat("line of text that is reasonably long\n", 40))
	}
	full, err := r.Diff(ctx, "HEAD", DiffOptions{})
	if err != nil || full.Truncated || len(full.Files) != 12 {
		t.Fatalf("full diff: %+v, %v", full, err)
	}
	capN := len(full.Patch) / 3
	d, err := r.Diff(ctx, "HEAD", DiffOptions{MaxPatchBytes: capN})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Truncated || len(d.Patch) > capN || len(d.Omitted) == 0 || len(d.Files) != 12 {
		t.Fatalf("cap %d: truncated=%v len=%d omitted=%d files=%d", capN, d.Truncated, len(d.Patch), len(d.Omitted), len(d.Files))
	}
	if !strings.HasPrefix(d.Patch, "diff --git ") || !strings.HasSuffix(d.Patch, "\n") {
		t.Fatalf("patch does not end at a file boundary:\n%.80s ... %q", d.Patch, d.Patch[max(0, len(d.Patch)-40):])
	}
	// What remains must still be a valid patch: it applies to the base.
	fresh := filepath.Join(t.TempDir(), "fresh")
	rawGit(t, dir, "worktree", "add", "-q", "--detach", fresh, "HEAD")
	if err := openRepo(t, fresh).Apply(ctx, d.Patch, true); err != nil {
		t.Fatalf("truncated patch no longer applies: %v", err)
	}
	kept := strings.Count(d.Patch, "\ndiff --git ") + 1
	if kept+len(d.Omitted) != 12 {
		t.Fatalf("kept %d + omitted %d != 12", kept, len(d.Omitted))
	}
	// A cap smaller than the first file keeps nothing rather than half a file.
	tiny, err := r.Diff(ctx, "HEAD", DiffOptions{MaxPatchBytes: 50})
	if err != nil || tiny.Patch != "" || !tiny.Truncated || len(tiny.Omitted) != 12 {
		t.Fatalf("tiny cap: patch=%q truncated=%v omitted=%d err=%v", tiny.Patch, tiny.Truncated, len(tiny.Omitted), err)
	}
	// MaxFiles bounds the file list.
	lim, err := r.Diff(ctx, "HEAD", DiffOptions{MaxFiles: 5, NoPatch: true})
	if err != nil || len(lim.Files) != 5 || !lim.Truncated {
		t.Fatalf("MaxFiles: %d files truncated=%v err=%v", len(lim.Files), lim.Truncated, err)
	}
}

func TestDiffTargetsAndPaths(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	base, _ := r.Head(ctx)
	writeFile(t, filepath.Join(dir, "a.txt"), "second\n")
	writeFile(t, filepath.Join(dir, "sub", "d.txt"), "d\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "second")
	// commit-to-commit
	d, err := r.Diff(ctx, base, DiffOptions{To: "HEAD"})
	if err != nil || len(d.Files) != 2 {
		t.Fatalf("commit diff: %+v, %v", d, err)
	}
	// path filter is literal: a glob character is just a character
	only, err := r.Diff(ctx, base, DiffOptions{To: "HEAD", Paths: []string{"sub/d.txt"}})
	if err != nil || len(only.Files) != 1 || only.Files[0].Path != "sub/d.txt" {
		t.Fatalf("path filter: %+v, %v", only, err)
	}
	star, err := r.Diff(ctx, base, DiffOptions{To: "HEAD", Paths: []string{"*.txt"}})
	if err != nil || len(star.Files) != 0 {
		t.Fatalf("pathspec was interpreted as a glob: %+v, %v", star, err)
	}
	if _, err := r.Diff(ctx, "nope", DiffOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown base: %v", err)
	}
	if _, err := r.Diff(ctx, base, DiffOptions{Paths: []string{"/etc/passwd"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("absolute path: %v", err)
	}
}

func TestApplyRejectsHostilePatches(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	victim := filepath.Join(t.TempDir(), "victim.txt")
	writeFile(t, victim, "keep me\n")
	mk := func(path string) string {
		return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+pwned\n"
	}
	for _, p := range []string{"../escape.txt", "sub/../../escape.txt", ".git/hooks/pre-commit", ".git/config"} {
		err := r.Apply(ctx, mk(p), false)
		if err == nil {
			t.Fatalf("patch writing %q was applied", p)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); err == nil {
		t.Fatal("escape.txt written outside the work tree")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", "pre-commit")); err == nil {
		t.Fatal("a hook was planted through a patch")
	}
	// Writing through a symlink that points outside the tree.
	if err := os.Symlink(filepath.Dir(victim), filepath.Join(dir, "out")); err != nil {
		t.Skip("no symlinks")
	}
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "symlink")
	if err := r.Apply(ctx, mk("out/planted.txt"), false); err == nil {
		t.Fatal("patch was applied through a symlink")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(victim), "planted.txt")); err == nil {
		t.Fatal("file planted outside through symlink")
	}
	if err := r.Apply(ctx, "", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty patch: %v", err)
	}
	if err := r.Apply(ctx, "garbage that is not a patch\n", false); err == nil {
		t.Fatal("garbage accepted")
	}
}

// File names are attacker- (or model-) controlled data. Names that look like
// options, pathspec magic, globs, or that contain control characters, quotes,
// backslashes and newlines must survive every parse and every round trip.
func TestPathsThatLookLikeOptionsPathspecsAndControlCharacters(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	names := []string{"-rf", "--force", "-n", "a\nb.txt", "tab\there.txt", `"quoted".txt`, ":(top)x.txt", ":!excluded.txt",
		"*.txt", "[ab].txt", "sp ace.txt", "é ü.txt", `back\slash.txt`, "trailing space ", "#hash.txt", "!bang.txt", "@{at}.txt",
		".hidden", "sub dir/ nested -x.txt", "-dir/-file", "日本語/ファイル.txt"}
	base, _ := r.Head(ctx)
	for i, n := range names {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(n)), "content of "+strconv.Itoa(i)+"\n")
	}

	// the parses agree with what was written
	st, err := r.StatusWith(ctx, StatusOptions{Untracked: "all"})
	if err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), st.Untracked...)
	sort.Strings(got)
	want := append([]string(nil), names...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Status untracked:\n got %q\nwant %q", got, want)
	}
	paths, err := r.ChangedPaths(ctx, base, "")
	if err != nil || strings.Join(paths, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("ChangedPaths:\n got %q\nwant %q (err %v)", paths, want, err)
	}
	d, err := r.Diff(ctx, base, DiffOptions{Renames: true})
	if err != nil || len(d.Files) != len(names) {
		t.Fatalf("Diff: %d files, %v", len(d.Files), err)
	}
	for _, f := range d.Files {
		if f.Added != 1 || f.Binary {
			t.Errorf("%q: %+v", f.Path, f)
		}
	}
	// the patch survives a round trip through git apply into a fresh checkout
	fresh := filepath.Join(t.TempDir(), "fresh")
	rawGit(t, dir, "worktree", "add", "-q", "--detach", fresh, base)
	fr := openRepo(t, fresh)
	if err := fr.Apply(ctx, d.Patch, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for i, n := range names {
		if b, err := os.ReadFile(filepath.Join(fresh, filepath.FromSlash(n))); err != nil || string(b) != "content of "+strconv.Itoa(i)+"\n" {
			t.Errorf("%q after apply: %q, %v", n, b, err)
		}
	}
	// committing records every name exactly
	sha, err := r.CommitAll(ctx, "weird names", Author{})
	if err != nil || sha == "" {
		t.Fatal(err)
	}
	tree := rawGit(t, dir, "ls-tree", "-r", "-z", "--name-only", sha)
	inTree := map[string]bool{}
	for _, n := range strings.Split(tree, "\x00") {
		inTree[n] = true
	}
	for _, n := range names {
		if !inTree[n] {
			t.Errorf("%q missing from the commit", n)
		}
		if b, err := r.Show(ctx, sha, n); err != nil || !strings.HasPrefix(string(b), "content of ") {
			t.Errorf("Show(%q) = %q, %v", n, b, err)
		}
	}
	// paths are literal: a glob in a path filter names exactly that file
	only, err := r.Log(ctx, LogOptions{Paths: []string{"*.txt"}})
	if err != nil || len(only) != 1 {
		t.Fatalf("Log on the literal path *.txt: %d commits, %v", len(only), err)
	}
	dd, err := r.Diff(ctx, base, DiffOptions{To: sha, Paths: []string{"*.txt"}})
	if err != nil || len(dd.Files) != 1 || dd.Files[0].Path != "*.txt" {
		t.Fatalf("Diff on the literal path *.txt: %+v, %v", dd, err)
	}
	// rename one weird name to another: both ends are reported
	if err := os.Rename(filepath.Join(dir, "a\nb.txt"), filepath.Join(dir, "--renamed\n.txt")); err != nil {
		t.Fatal(err)
	}
	paths, err = r.ChangedPaths(ctx, sha, "")
	if err != nil || strings.Join(paths, "|") != "--renamed\n.txt|a\nb.txt" {
		t.Fatalf("rename ends: %q, %v", paths, err)
	}
}
