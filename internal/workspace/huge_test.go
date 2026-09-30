package workspace

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newHugeRepo builds a repository with dirs*perDir small files (via fast-import, so
// building it is not the slow part of the test).
func newHugeRepo(t testing.TB, dirs, perDir int) string {
	t.Helper()
	isolateHome(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	dir := filepath.Join(root, "huge")
	must(t, os.MkdirAll(dir, 0o755))
	rawGit(t, dir, "init", "-q", "-b", "main", ".")
	var stream bytes.Buffer
	mark := 0
	var paths []string
	emit := func(path, content string) {
		mark++
		fmt.Fprintf(&stream, "blob\nmark :%d\ndata %d\n%s\n", mark, len(content), content)
		paths = append(paths, fmt.Sprintf("M 100644 :%d %s\n", mark, path))
	}
	emit("README.md", "# huge\n")
	emit("go.mod", "module example.com/huge\n\ngo 1.24\n")
	for d := 0; d < dirs; d++ {
		for f := 0; f < perDir; f++ {
			emit(fmt.Sprintf("pkg%04d/file%02d.go", d, f), fmt.Sprintf("package pkg%04d\n\nconst V%02d = %d\n", d, f, d*perDir+f))
		}
	}
	msg := "huge project"
	fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter Fixture <fixture@example.com> 1700000000 +0000\ndata %d\n%s\n", len(msg), msg)
	for _, p := range paths {
		stream.WriteString(p)
	}
	stream.WriteString("\n")
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = dir
	cmd.Env = fixtureEnv(t.TempDir())
	cmd.Stdin = &stream
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
	rawGit(t, dir, "checkout", "-q", "-f", "main")
	return dir
}

// hugeSize is the default size of the large-tree tests, chosen so that they take
// seconds, not minutes, in a normal run (the race detector and a busy machine
// multiply the time spent copying files). SLEIPNIR_HUGE=1 runs the full sizes.
func hugeSize(normal, full int) int {
	if os.Getenv("SLEIPNIR_HUGE") != "" {
		return full
	}
	return normal
}

func countFiles(t testing.TB, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() != ".git" {
			n++
		}
		return nil
	})
	must(t, err)
	return n
}

func TestHugeRepositoryTrees(t *testing.T) {
	if testing.Short() {
		t.Skip("large repository")
	}
	skipWithoutUnix(t)
	dirs, perDir := hugeSize(200, 400), 50 // 10,002 files (20,002 in the full size)
	dir := newHugeRepo(t, dirs, perDir)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	m.MaxFileBytes = -1
	total := dirs*perDir + 2

	start := time.Now()
	full := mustCreate(t, m, "full")
	fullTook := time.Since(start)
	if got := countFiles(t, full.Path); got != total {
		t.Fatalf("full tree has %d files, want %d", got, total)
	}
	start = time.Now()
	sp := mustCreate(t, m, "sparse", CreateOptions{Sparse: []string{"pkg0003", "pkg0004"}})
	sparseTook := time.Since(start)
	if got := countFiles(t, sp.Path); got != 2*perDir+2 {
		t.Fatalf("sparse tree has %d files, want %d", got, 2*perDir+2)
	}
	t.Logf("create: full tree of %d files %s, sparse tree of %d files %s", total, fullTook.Round(time.Millisecond), 2*perDir+2, sparseTook.Round(time.Millisecond))
	if fullTook > 2*time.Minute {
		t.Fatalf("creating a tree of %d files took %s", total, fullTook)
	}

	// many sparse trees at once (the swarm start-up case)
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	trees := make([]*Tree, n)
	start = time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			trees[i], errs[i] = m.Create(tctx(t), fmt.Sprintf("w-%02d", i), CreateOptions{Sparse: []string{fmt.Sprintf("pkg%04d", 10+i)}})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("parallel create %d: %v", i, err)
		}
	}
	t.Logf("%d sparse trees created in parallel in %s", n, time.Since(start).Round(time.Millisecond))

	// Work in the full tree: a handful of changes among the many files
	start = time.Now()
	dEdit, dAdd, dDel := fmt.Sprintf("pkg%04d", dirs/4), fmt.Sprintf("pkg%04d", dirs/2), fmt.Sprintf("pkg%04d", 3*dirs/4)
	edit(t, full, dEdit+"/file07.go", "package "+dEdit+"\n\nconst V07 = -1\n")
	edit(t, full, dAdd+"/new.go", "package "+dAdd+"\n")
	must(t, os.Remove(filepath.Join(full.Path, dDel, "file01.go")))
	ch, err := full.Changed(tctx(t))
	if want := dEdit + "/file07.go," + dAdd + "/new.go," + dDel + "/file01.go"; err != nil || strings.Join(ch, ",") != want {
		t.Fatalf("Changed = %v, %v (want %s)", ch, err, want)
	}
	patch, err := full.Diff(tctx(t))
	if err != nil || !strings.Contains(patch, "V07 = -1") {
		t.Fatalf("Diff: %v", err)
	}
	t.Logf("Changed+Diff over %d files: %s", total, time.Since(start).Round(time.Millisecond))
	if dirty, err := full.Dirty(tctx(t)); err != nil || !dirty {
		t.Fatalf("Dirty: %v %v", dirty, err)
	}

	// integrate the full tree and a sparse one; the integration tree holds everything
	edit(t, sp, "pkg0003/file00.go", "package pkg0003\n\nconst V00 = -3\n")
	q1 := mustQueue(t, m, QueueOptions{VerifyCmd: fmt.Sprintf("test -f pkg%04d/file%02d.go", dirs-1, perDir-1)})
	start = time.Now()
	r1 := mustSubmit(t, q1, Submission{Tree: full, Task: "full"})
	r2 := mustSubmit(t, q1, Submission{Tree: sp, Task: "sparse"})
	if !r1.Merged() || !r2.Merged() {
		t.Fatalf("results: %+v / %+v", r1, r2)
	}
	t.Logf("two submissions (merge+verify+status) in a %d-file repository: %s", total, time.Since(start).Round(time.Millisecond))
	if got := countFiles(t, q1.tree.Path); got != total {
		t.Fatalf("integration tree has %d files, want %d (+1 -1 +1... = %d)", got, total, total)
	}
	st, _ := q1.tree.repo.Status(tctx(t))
	if !st.Clean() {
		t.Fatalf("integration tree dirty: %+v", st)
	}
	for _, tr := range trees {
		if err := tr.Remove(tctx(t), false); err != nil {
			t.Fatalf("Remove %s: %v", tr.Agent, err)
		}
	}
}

func TestHugeDirectoryInCopyMode(t *testing.T) {
	if testing.Short() {
		t.Skip("large directory")
	}
	skipWithoutUnix(t)
	isolateHome(t)
	src := filepath.Join(t.TempDir(), "big plain dir")
	dirs, perDir := hugeSize(100, 200), 50 // 5,000 files (10,000 in the full size)
	for d := 0; d < dirs; d++ {
		for f := 0; f < perDir; f++ {
			writeFile(t, filepath.Join(src, fmt.Sprintf("d%03d", d), fmt.Sprintf("f%02d.txt", f)), fmt.Sprintf("dir %d file %d\n", d, f))
		}
	}
	m := &Manager{Source: src, Dir: filepath.Join(t.TempDir(), "trees"), Prefix: "sleipnir/s1", Clock: testClock(), MaxFileBytes: -1}
	start := time.Now()
	a := mustCreate(t, m, "a") // snapshot + first tree
	first := time.Since(start)
	start = time.Now()
	b := mustCreate(t, m, "b")
	second := time.Since(start)
	t.Logf("copy mode, %d files: snapshot+first tree %s, second tree %s", dirs*perDir, first.Round(time.Millisecond), second.Round(time.Millisecond))
	for _, tr := range []*Tree{a, b} {
		if got := countFiles(t, tr.Path); got != dirs*perDir {
			t.Fatalf("%s has %d files", tr.Agent, got)
		}
		start = time.Now()
		ch, err := tr.Changed(tctx(t))
		if err != nil || len(ch) != 0 {
			t.Fatalf("Changed on a fresh copy: %v, %v", ch, err)
		}
		t.Logf("%s: Changed on %d files: %s", tr.Agent, dirs*perDir, time.Since(start).Round(time.Millisecond))
	}
	edit(t, a, "d005/f05.txt", "edited\n")
	if ch, _ := a.Changed(tctx(t)); len(ch) != 1 || ch[0] != "d005/f05.txt" {
		t.Fatalf("Changed = %v", ch)
	}
}
