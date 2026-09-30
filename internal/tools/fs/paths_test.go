package fs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAbsClean(t *testing.T) {
	tests := []struct {
		cwd, in, want string
		err           string
	}{
		{"/w", "a/b", "/w/a/b", ""},
		{"/w", "./a", "/w/a", ""},
		{"/w", "a/../b", "/w/b", ""},
		{"/w", "../x", "/x", ""},
		{"/w", "../../../../x", "/x", ""},
		{"/w", "/abs/p", "/abs/p", ""},
		{"/w", "/abs//p/./q/../r", "/abs/p/r", ""},
		{"/w", "a//b///c", "/w/a/b/c", ""},
		{"/w", "a/", "/w/a", ""},
		{"/w", ".", "/w", ""},
		{"/w", "..", "/", ""},
		{"/w", "日本語/🎉", "/w/日本語/🎉", ""},
		{"/w", "sp ace/x y", "/w/sp ace/x y", ""},
		{"/w", "", "", "empty"},
		{"/w", "a\x00b", "", "NUL"},
		{"/w", strings.Repeat("a/", 20000), "", "too long"},
	}
	for _, tc := range tests {
		got, err := absClean(tc.cwd, tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("absClean(%q) err = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("absClean(%q, %q) = %q, %v; want %q", tc.cwd, tc.in, got, err, tc.want)
		}
	}
	// A relative cwd is made absolute; an empty cwd means the process directory.
	wd, _ := os.Getwd()
	if got, _ := absClean("", "x"); got != filepath.Join(wd, "x") {
		t.Errorf("empty cwd: %q", got)
	}
	if got, _ := absClean("rel", "x"); got != filepath.Join(wd, "rel", "x") {
		t.Errorf("relative cwd: %q", got)
	}
}

func TestRealPath(t *testing.T) {
	dir := realTemp(t)
	must := func(err error) {
		if err != nil {
			t.Skip("symlinks unsupported")
		}
	}
	tree(t, dir, map[string]string{"a/b/file.txt": "x", "other/o.txt": "y"})
	must(os.Symlink("a/b", filepath.Join(dir, "shortcut")))                  // relative dir link
	must(os.Symlink(filepath.Join(dir, "other"), filepath.Join(dir, "abs"))) // absolute dir link
	must(os.Symlink("../other", filepath.Join(dir, "a", "up")))              // link that goes up
	must(os.Symlink("shortcut", filepath.Join(dir, "chain1")))               // link to a link
	must(os.Symlink("chain1", filepath.Join(dir, "chain2")))
	must(os.Symlink("missing/target.txt", filepath.Join(dir, "dangling")))
	must(os.Symlink("l2", filepath.Join(dir, "l1")))
	must(os.Symlink("l1", filepath.Join(dir, "l2")))
	must(os.Symlink("file.txt", filepath.Join(dir, "a", "b", "flink")))

	tests := []struct {
		name string
		in   string
		want string
		err  string
	}{
		{"plain", "a/b/file.txt", "a/b/file.txt", ""},
		{"relative link", "shortcut/file.txt", "a/b/file.txt", ""},
		{"absolute link", "abs/o.txt", "other/o.txt", ""},
		{"link going up", "a/up/o.txt", "other/o.txt", ""},
		{"chain", "chain2/file.txt", "a/b/file.txt", ""},
		{"file link", "a/b/flink", "a/b/file.txt", ""},
		{"missing tail is appended", "a/new/dir/file.txt", "a/new/dir/file.txt", ""},
		{"missing tail under a link", "shortcut/new.txt", "a/b/new.txt", ""},
		{"dangling link resolves to its target", "dangling", "missing/target.txt", ""},
		{"through a dangling link", "dangling/x", "missing/target.txt/x", ""},
		{"loop", "l1", "", "symbolic links"},
		{"loop with tail", "l1/x", "", "symbolic links"},
		{"file used as directory", "a/b/file.txt/child", "", "not a directory"},
		{"root itself", ".", ".", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := filepath.Join(dir, tc.in)
			got, err := realPath(in)
			if tc.err != "" {
				if err == nil || !strings.Contains(osReason(err), tc.err) {
					t.Fatalf("realPath(%q) = %q, %v; want error %q", tc.in, got, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("realPath(%q): %v", tc.in, err)
			}
			want := filepath.Join(dir, tc.want)
			if got != want {
				t.Errorf("realPath(%q) = %q, want %q", tc.in, got, want)
			}
		})
	}
}

func TestRealPathLongSymlinkChainIsBounded(t *testing.T) {
	dir := realTemp(t)
	prev := "target"
	writeFile(t, filepath.Join(dir, "target"), "x")
	for i := 0; i < 300; i++ {
		name := "link" + string(rune('a'+i%26)) + strings.Repeat("x", i/26)
		if err := os.Symlink(prev, filepath.Join(dir, name)); err != nil {
			t.Skip("symlinks unsupported")
		}
		prev = name
	}
	start := time.Now()
	_, err := realPath(filepath.Join(dir, prev))
	if err == nil || !strings.Contains(osReason(err), "symbolic links") {
		t.Errorf("a 300-link chain should be refused, got %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("took too long")
	}
}

func TestResolveKeepsRootSpellingAndReportsEscapes(t *testing.T) {
	real := realTemp(t)
	other := realTemp(t)
	tree(t, real, map[string]string{"src/a.go": "", "d/": ""})
	tree(t, other, map[string]string{"secret.txt": ""})
	linkDir := realTemp(t)
	root := filepath.Join(linkDir, "proj")
	if err := os.Symlink(real, root); err != nil {
		t.Skip("symlinks unsupported")
	}
	os.Symlink(other, filepath.Join(real, "escape"))
	os.Symlink("src", filepath.Join(real, "inner"))

	env := testEnv(t)
	env.Cwd, env.Root = root, root
	k := begin(nil, nil, "x")
	k.env = env
	tests := []struct {
		in   string
		want string
	}{
		{"src/a.go", filepath.Join(root, "src/a.go")},
		{filepath.Join(root, "src/a.go"), filepath.Join(root, "src/a.go")},
		{filepath.Join(real, "src/a.go"), filepath.Join(root, "src/a.go")}, // the real spelling maps onto Root's
		{"inner/a.go", filepath.Join(root, "src/a.go")},                    // in-project alias resolves to its target
		{"src/new/file.go", filepath.Join(root, "src/new/file.go")},
		{"escape/secret.txt", filepath.Join(other, "secret.txt")}, // out of the project: the real location
		{filepath.Join(other, "secret.txt"), filepath.Join(other, "secret.txt")},
		{"../elsewhere", filepath.Join(linkDir, "elsewhere")},
		{".", root},
	}
	for _, tc := range tests {
		got, err := k.resolve(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("resolve(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestDisplayPaths(t *testing.T) {
	env := testEnv(t)
	k := begin(nil, nil, "x")
	k.env = env
	tests := []struct{ in, want string }{
		{filepath.Join(env.Cwd, "a/b.go"), "a/b.go"},
		{env.Cwd, "."},
		{"/etc/passwd", "/etc/passwd"},
		{filepath.Join(filepath.Dir(env.Cwd), "sibling"), filepath.Join(filepath.Dir(env.Cwd), "sibling")},
	}
	for _, tc := range tests {
		if got := k.display(tc.in); got != tc.want {
			t.Errorf("display(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPathLocksExcludeSamePath(t *testing.T) {
	var inside, maxInside, total int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := fileLocks.acquire("/x/same.txt")
			n := atomic.AddInt32(&inside, 1)
			if n > atomic.LoadInt32(&maxInside) {
				atomic.StoreInt32(&maxInside, n)
			}
			total++ // deliberately unsynchronised: the lock must make it safe under -race
			time.Sleep(50 * time.Microsecond)
			atomic.AddInt32(&inside, -1)
			unlock()
		}()
	}
	wg.Wait()
	if maxInside != 1 || total != 64 {
		t.Errorf("maxInside=%d total=%d", maxInside, total)
	}
}

func TestPathLocksAllowDifferentPathsInParallel(t *testing.T) {
	u1 := fileLocks.acquire("/x/one.txt")
	done := make(chan struct{})
	go func() {
		u2 := fileLocks.acquire("/x/two.txt")
		u2()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("locks on different paths must not block each other")
	}
	u1()
}

func TestPathLocksMultiPathIsDeadlockFree(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var paths []string
			if i%2 == 0 {
				paths = []string{"/p/a", "/p/b", "/p/c", "/p/a"}
			} else {
				paths = []string{"/p/c", "/p/b", "/p/a"}
			}
			for n := 0; n < 50; n++ {
				unlock := fileLocks.acquire(paths...)
				unlock()
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock")
	}
}

func TestPathLocksAreReleasedFromTheTable(t *testing.T) {
	unlock := fileLocks.acquire("/leak/a", "/leak/b")
	fileLocks.mu.Lock()
	n := len(fileLocks.m)
	fileLocks.mu.Unlock()
	if n < 2 {
		t.Errorf("held locks should be in the table, got %d", n)
	}
	unlock()
	fileLocks.mu.Lock()
	_, a := fileLocks.m["/leak/a"]
	_, b := fileLocks.m["/leak/b"]
	fileLocks.mu.Unlock()
	if a || b {
		t.Errorf("idle locks must not accumulate")
	}
}

func TestAtomicWriteKeepsSpecialModeBits(t *testing.T) {
	dir := realTemp(t)
	p := filepath.Join(dir, "f")
	writeFile(t, p, "old")
	os.Chmod(p, 0o755|os.ModeSetgid)
	fi, _ := os.Stat(p)
	if err := atomicWrite(p, []byte("new"), fi); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if after.Mode()&os.ModeSetgid == 0 || after.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", after.Mode())
	}
}

func TestWriteInPlaceFallback(t *testing.T) {
	dir := realTemp(t)
	p := filepath.Join(dir, "f")
	writeFile(t, p, "a much longer original content")
	if err := writeInPlace(p, []byte("short")); err != nil {
		t.Fatal(err)
	}
	if readFileT(t, p) != "short" {
		t.Errorf("in-place write must truncate: %q", readFileT(t, p))
	}
	if err := writeInPlace(filepath.Join(dir, "missing"), []byte("x")); err == nil {
		t.Errorf("in-place write must not create files")
	}
}

func TestAtomicWriteDoesNotFallBackToTruncationOnFailure(t *testing.T) {
	// If the atomic route fails for a reason other than permissions (here: the
	// target became a directory), the original must survive rather than be
	// truncated by a fallback.
	dir := realTemp(t)
	p := filepath.Join(dir, "target")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(p, "keep"), "important")
	fi, _ := os.Stat(p)
	if err := atomicWrite(p, []byte("x"), fi); err == nil {
		t.Errorf("renaming a file over a non-empty directory must fail")
	}
	if readFileT(t, filepath.Join(p, "keep")) != "important" {
		t.Errorf("data lost")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}
