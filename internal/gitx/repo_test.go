package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestOpenVariants(t *testing.T) {
	dir := newRepo(t)
	real, _ := filepath.EvalSymlinks(dir)

	t.Run("root and subdirectory", func(t *testing.T) {
		r := openRepo(t, dir)
		if r.Root() != real {
			t.Fatalf("Root = %q, want %q", r.Root(), real)
		}
		sub := openRepo(t, filepath.Join(dir, "sub"))
		if sub.Root() != real {
			t.Fatalf("subdirectory Root = %q, want %q", sub.Root(), real)
		}
		fileRepo := openRepo(t, filepath.Join(dir, "a.txt"))
		if fileRepo.Root() != real {
			t.Fatalf("file path Root = %q", fileRepo.Root())
		}
		if r.IsBare() || r.IsLinked() {
			t.Fatalf("plain repo reported bare=%v linked=%v", r.IsBare(), r.IsLinked())
		}
	})

	t.Run("symlinked path resolves", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Skip("no symlinks")
		}
		if got := openRepo(t, link).Root(); got != real {
			t.Fatalf("Root via symlink = %q, want %q", got, real)
		}
	})

	t.Run("not a repository", func(t *testing.T) {
		plain := t.TempDir()
		_, err := Open(plain, WithHermeticConfig())
		if !errors.Is(err, ErrNotARepo) {
			t.Fatalf("want ErrNotARepo, got %v", err)
		}
		if IsRepo(plain, WithHermeticConfig()) {
			t.Fatal("IsRepo(plain dir) = true")
		}
		if !IsRepo(dir, WithHermeticConfig()) {
			t.Fatal("IsRepo(repo) = false")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := Open(filepath.Join(dir, "nope"), WithHermeticConfig())
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
		if _, err := Open("", WithHermeticConfig()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("empty path: want ErrInvalid, got %v", err)
		}
	})

	t.Run("bare repository", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), "bare.git")
		rawGit(t, dir, "clone", "-q", "--bare", ".", bare)
		r := openRepo(t, bare)
		if !r.IsBare() {
			t.Fatal("bare repo not detected")
		}
		if _, err := r.Status(ctxT(t)); !errors.Is(err, ErrNotARepo) {
			t.Fatalf("Status on bare: want ErrNotARepo, got %v", err)
		}
		if _, err := r.Head(ctxT(t)); err != nil {
			t.Fatalf("Head on bare: %v", err)
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		wt := filepath.Join(t.TempDir(), "wt")
		rawGit(t, dir, "worktree", "add", "-q", "-b", "linked", wt)
		r := openRepo(t, wt)
		if !r.IsLinked() {
			t.Fatal("linked worktree not detected")
		}
		if r.CommonDir() == r.GitDir() {
			t.Fatal("common dir must differ from the per-worktree git dir")
		}
		br, err := r.Branch(ctxT(t))
		if err != nil || br != "linked" {
			t.Fatalf("Branch = %q, %v", br, err)
		}
	})

	t.Run("spaces and unicode in the path", func(t *testing.T) {
		isolateHome(t)
		weird := filepath.Join(t.TempDir(), "my proj é 日本語")
		if err := os.MkdirAll(weird, 0o755); err != nil {
			t.Fatal(err)
		}
		rawGit(t, weird, "init", "-q", "-b", "main", ".")
		writeFile(t, filepath.Join(weird, "ü file.txt"), "x\n")
		rawGit(t, weird, "add", "-A")
		rawGit(t, weird, "commit", "-q", "-m", "m")
		r := openRepo(t, weird)
		st, err := r.Status(ctxT(t))
		if err != nil || !st.Clean() {
			t.Fatalf("status %+v err %v", st, err)
		}
		writeFile(t, filepath.Join(weird, "sub dir", "ñ.txt"), "y\n")
		files, err := r.ChangedPaths(ctxT(t), "HEAD", "")
		if err != nil || len(files) != 1 || files[0] != "sub dir/ñ.txt" {
			t.Fatalf("ChangedPaths = %v, %v", files, err)
		}
	})
}

func TestRefs(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	head, err := r.Head(ctx)
	if err != nil || len(head) != 40 {
		t.Fatalf("Head = %q, %v", head, err)
	}
	if br, _ := r.Branch(ctx); br != "main" {
		t.Fatalf("Branch = %q", br)
	}
	sha, err := r.ResolveRef(ctx, "main")
	if err != nil || sha != head {
		t.Fatalf("ResolveRef(main) = %q, %v", sha, err)
	}
	if _, err := r.ResolveRef(ctx, "no-such-branch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ref: want ErrNotFound, got %v", err)
	}
	for _, bad := range []string{"", "-x", "--output=/tmp/x", "a\nb", "a\x00b"} {
		if _, err := r.ResolveRef(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ResolveRef(%q): want ErrInvalid, got %v", bad, err)
		}
	}

	// Second commit, then merge-base / ancestry.
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha2\n")
	rawGit(t, dir, "commit", "-aqm", "second")
	second, _ := r.Head(ctx)
	mb, err := r.MergeBase(ctx, head, second)
	if err != nil || mb != head {
		t.Fatalf("MergeBase = %q, %v", mb, err)
	}
	if ok, err := r.IsAncestor(ctx, head, second); err != nil || !ok {
		t.Fatalf("IsAncestor(head, second) = %v, %v", ok, err)
	}
	if ok, err := r.IsAncestor(ctx, second, head); err != nil || ok {
		t.Fatalf("IsAncestor(second, head) = %v, %v", ok, err)
	}
	// Unrelated history: no merge base.
	rawGit(t, dir, "checkout", "-q", "--orphan", "other")
	rawGit(t, dir, "rm", "-rqf", ".")
	writeFile(t, filepath.Join(dir, "z.txt"), "z\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "orphan")
	if _, err := r.MergeBase(ctx, "main", "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unrelated histories: want ErrNotFound, got %v", err)
	}
	// Detached HEAD has no branch name.
	rawGit(t, dir, "checkout", "-q", "--detach", "main")
	if br, err := r.Branch(ctx); err != nil || br != "" {
		t.Fatalf("detached Branch = %q, %v", br, err)
	}
	st, err := r.Status(ctx)
	if err != nil || !st.Detached || st.Branch != "" {
		t.Fatalf("detached status %+v, %v", st, err)
	}
}

func TestHeadOnEmptyRepository(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "-q", "-b", "main", ".")
	r := openRepo(t, dir)
	if _, err := r.Head(ctxT(t)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unborn HEAD: want ErrNotFound, got %v", err)
	}
	writeFile(t, filepath.Join(dir, "f"), "x\n")
	st, err := r.Status(ctxT(t))
	if err != nil || st.Head != "" || len(st.Untracked) != 1 {
		t.Fatalf("status of unborn repo: %+v %v", st, err)
	}
	// The very first commit works and is reported.
	sha, err := r.CommitAll(ctxT(t), "first", Author{})
	if err != nil || len(sha) != 40 {
		t.Fatalf("CommitAll on unborn repo = %q, %v", sha, err)
	}
}

func TestStatusParsing(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)

	// staged add + rename, unstaged modify, untracked, both-staged-and-modified
	writeFile(t, filepath.Join(dir, "new file.txt"), "n\n")
	rawGit(t, dir, "add", "new file.txt")
	rawGit(t, dir, "mv", "b.txt", "renamed b.txt")
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha changed\n")
	writeFile(t, filepath.Join(dir, "sub", "c.txt"), "charlie staged\n")
	rawGit(t, dir, "add", "sub/c.txt")
	writeFile(t, filepath.Join(dir, "sub", "c.txt"), "charlie staged and modified\n")
	writeFile(t, filepath.Join(dir, "u", "deep", "x.txt"), "u\n")

	st, err := r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Clean() {
		t.Fatal("dirty tree reported clean")
	}
	staged := map[string]byte{}
	for _, c := range st.Staged {
		staged[c.Path] = c.Code
	}
	if staged["new file.txt"] != 'A' || staged["renamed b.txt"] != 'R' || staged["sub/c.txt"] != 'M' {
		t.Fatalf("staged = %+v", st.Staged)
	}
	for _, c := range st.Staged {
		if c.Code == 'R' && c.OrigPath != "b.txt" {
			t.Fatalf("rename origin = %q", c.OrigPath)
		}
	}
	unstaged := map[string]byte{}
	for _, c := range st.Unstaged {
		unstaged[c.Path] = c.Code
	}
	if unstaged["a.txt"] != 'M' || unstaged["sub/c.txt"] != 'M' {
		t.Fatalf("unstaged = %+v", st.Unstaged)
	}
	if len(st.Untracked) != 1 || st.Untracked[0] != "u/" {
		t.Fatalf("untracked (normal) = %v", st.Untracked)
	}
	all, err := r.StatusWith(ctx, StatusOptions{Untracked: "all"})
	if err != nil || len(all.Untracked) != 1 || all.Untracked[0] != "u/deep/x.txt" {
		t.Fatalf("untracked (all) = %v, %v", all.Untracked, err)
	}
	none, err := r.StatusWith(ctx, StatusOptions{Untracked: "no"})
	if err != nil || len(none.Untracked) != 0 {
		t.Fatalf("untracked (no) = %v, %v", none.Untracked, err)
	}
	if _, err := r.StatusWith(ctx, StatusOptions{Untracked: "bogus"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad mode: %v", err)
	}
	if ok, _ := r.IsClean(ctx); ok {
		t.Fatal("IsClean true on a dirty tree")
	}
}

func TestStatusConflictAndUpstream(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)

	// upstream tracking: ahead 1, behind 1
	remote := filepath.Join(t.TempDir(), "remote.git")
	rawGit(t, dir, "clone", "-q", "--bare", ".", remote)
	rawGit(t, dir, "remote", "add", "origin", remote)
	rawGit(t, dir, "fetch", "-q", "origin")
	rawGit(t, dir, "branch", "-q", "--set-upstream-to=origin/main", "main")
	other := filepath.Join(t.TempDir(), "other")
	rawGit(t, t.TempDir(), "clone", "-q", remote, other)
	writeFile(t, filepath.Join(other, "remote.txt"), "r\n")
	rawGit(t, other, "add", "-A")
	rawGit(t, other, "commit", "-qm", "remote change")
	rawGit(t, other, "push", "-q", "origin", "main")
	rawGit(t, dir, "fetch", "-q", "origin")
	writeFile(t, filepath.Join(dir, "local.txt"), "l\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "local change")
	st, err := r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.HasUpstream || st.Upstream != "origin/main" || st.Ahead != 1 || st.Behind != 1 {
		t.Fatalf("upstream state: %+v", st)
	}

	// a real merge conflict
	rawGit(t, dir, "checkout", "-q", "-b", "x")
	writeFile(t, filepath.Join(dir, "a.txt"), "from x\n")
	rawGit(t, dir, "commit", "-aqm", "x")
	rawGit(t, dir, "checkout", "-q", "main")
	writeFile(t, filepath.Join(dir, "a.txt"), "from main\n")
	rawGit(t, dir, "commit", "-aqm", "m")
	cmd := rawGitMayFail(dir, "merge", "x")
	_ = cmd
	st, err = r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Conflicted) != 1 || st.Conflicted[0].Path != "a.txt" || st.Conflicted[0].XY != "UU" {
		t.Fatalf("conflicted = %+v", st.Conflicted)
	}
	un, err := r.Unmerged(ctx)
	if err != nil || len(un) != 1 || un[0].Kind() != "content" {
		t.Fatalf("Unmerged = %+v, %v", un, err)
	}
	if r.InProgress() != "merge" {
		t.Fatalf("InProgress = %q", r.InProgress())
	}
	// Committing with markers still in the file must be refused.
	if _, err := r.CommitAll(ctx, "oops", Author{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("commit with markers: want ErrConflict, got %v", err)
	}
	// After resolving, the merge commit is created with two parents.
	writeFile(t, filepath.Join(dir, "a.txt"), "resolved\n")
	sha, err := r.CommitAll(ctx, "merge x", Author{Name: "agent-1", Email: "a1@example.com"})
	if err != nil || sha == "" {
		t.Fatalf("resolving commit: %q, %v", sha, err)
	}
	c, err := r.CommitInfo(ctx, sha)
	if err != nil || len(c.Parents) != 2 || c.Author.Name != "agent-1" || c.Committer.Email != "a1@example.com" {
		t.Fatalf("merge commit %+v, %v", c, err)
	}
	if r.InProgress() != "" {
		t.Fatalf("merge still in progress: %q", r.InProgress())
	}
}

func TestCommitAll(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)

	if sha, err := r.CommitAll(ctx, "nothing", Author{}); err != nil || sha != "" {
		t.Fatalf("clean tree: sha=%q err=%v (want empty, nil)", sha, err)
	}
	if _, err := r.CommitAll(ctx, "  \n", Author{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank message: %v", err)
	}
	if _, err := r.CommitAll(ctx, "x", Author{Name: "bad<name>"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad identity: %v", err)
	}

	writeFile(t, filepath.Join(dir, "a.txt"), "changed\n")
	writeFile(t, filepath.Join(dir, "new.txt"), "n\n")
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	// .gitignore'd files must stay out.
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(dir, "build.log"), "noise\n")
	msg := "subject line\n\nbody with # not a comment\n"
	sha, err := r.CommitAll(ctx, msg, Author{Name: "Ada Lovelace", Email: "ada@example.org"})
	if err != nil || len(sha) != 40 {
		t.Fatalf("CommitAll = %q, %v", sha, err)
	}
	c, err := r.CommitInfo(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "subject line" || !strings.Contains(c.Body, "# not a comment") {
		t.Fatalf("message mangled: %+v", c)
	}
	if c.Author.Name != "Ada Lovelace" || c.Author.Email != "ada@example.org" || c.Committer.Name != "Ada Lovelace" {
		t.Fatalf("identity: %+v / %+v", c.Author, c.Committer)
	}
	tree := rawGit(t, dir, "ls-tree", "-r", "--name-only", sha)
	if strings.Contains(tree, "build.log") || strings.Contains(tree, "b.txt") || !strings.Contains(tree, "new.txt") {
		t.Fatalf("committed tree:\n%s", tree)
	}
	if ok, _ := r.IsClean(ctx); !ok {
		t.Fatal("tree not clean after CommitAll")
	}
}

func TestLogAndShow(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha 2\n")
	// A message full of things that break naive --format parsing.
	nasty := "subject\x1fwith unit sep\n\nbody line 1\n\x1e record sep\ntree deadbeef\n"
	rawGit(t, dir, "commit", "-aqm", nasty)
	writeFile(t, filepath.Join(dir, "sub", "c.txt"), "charlie 2\n")
	rawGit(t, dir, "commit", "-aqm", "third")

	all, err := r.Log(ctx, LogOptions{})
	if err != nil || len(all) != 3 {
		t.Fatalf("Log = %d commits, %v", len(all), err)
	}
	if all[0].Subject != "third" || all[2].Subject != "initial" {
		t.Fatalf("order: %q ... %q", all[0].Subject, all[2].Subject)
	}
	if !strings.Contains(all[1].Subject, "with unit sep") || !strings.Contains(all[1].Body, "tree deadbeef") {
		t.Fatalf("nasty message not preserved: %+v", all[1])
	}
	if all[1].Tree == "deadbeef" || len(all[1].Tree) != 40 {
		t.Fatalf("tree id parsed from the message: %q", all[1].Tree)
	}
	if len(all[0].Parents) != 1 || all[0].Parents[0] != all[1].SHA {
		t.Fatalf("parents: %+v", all[0].Parents)
	}
	if all[0].Author.When.IsZero() || all[0].Author.Email != "fixture@example.com" {
		t.Fatalf("author: %+v", all[0].Author)
	}
	one, _ := r.Log(ctx, LogOptions{Max: 1})
	if len(one) != 1 || one[0].SHA != all[0].SHA {
		t.Fatalf("Max=1: %+v", one)
	}
	byPath, err := r.Log(ctx, LogOptions{Paths: []string{"sub/c.txt"}})
	if err != nil || len(byPath) != 2 {
		t.Fatalf("path log = %d, %v", len(byPath), err)
	}
	rng, err := r.Log(ctx, LogOptions{Range: all[2].SHA + ".." + all[0].SHA})
	if err != nil || len(rng) != 2 {
		t.Fatalf("range log = %d, %v", len(rng), err)
	}
	if _, err := r.Log(ctx, LogOptions{Rev: "--all"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("option-looking rev: %v", err)
	}
	if _, err := r.Log(ctx, LogOptions{Paths: []string{"../escape"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("escaping path: %v", err)
	}

	// Show: file content, binary content byte for byte, missing path, commit text.
	got, err := r.Show(ctx, all[2].SHA, "a.txt")
	if err != nil || string(got) != "alpha\n" {
		t.Fatalf("Show a.txt@initial = %q, %v", got, err)
	}
	bin := make([]byte, 4096)
	for i := range bin {
		bin[i] = byte(i * 7)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "bin")
	back, err := r.Show(ctx, "HEAD", "blob.bin")
	if err != nil || string(back) != string(bin) {
		t.Fatalf("binary round trip failed (%d bytes back, err %v)", len(back), err)
	}
	if _, err := r.Show(ctx, "HEAD", "no/such/file"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing path: %v", err)
	}
	if _, err := r.Show(ctx, "HEAD", "sub"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("directory as file: %v", err)
	}
	if _, err := r.Show(ctx, "HEAD", "../../etc/passwd"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("traversal: %v", err)
	}
	text, err := r.Show(ctx, "HEAD", "")
	if err != nil || !strings.Contains(string(text), "bin") || !strings.Contains(string(text), "blob.bin") {
		t.Fatalf("Show commit = %q, %v", text, err)
	}
}

func TestConcurrentReads(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "changed\n")
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Status(ctx); err != nil {
				errs <- err
			}
			if d, err := r.Diff(ctx, "HEAD", DiffOptions{}); err != nil || len(d.Files) != 1 {
				errs <- errors.Join(err, errors.New("diff did not see exactly one change"))
			}
			if _, err := r.Head(ctx); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestValidateBranchName(t *testing.T) {
	ok := []string{"main", "sleipnir/s1/be-1", "feature/x.y", "a_b-c", "v1.0.0"}
	bad := []string{"", "-x", "--force", "a..b", "a b", "a\tb", "a\x00b", "a/", "/a", "a//b", "a.lock", "a/b.lock", ".hidden",
		"a/.b", "a@{b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "é", "trailing.", "@", strings.Repeat("x", 300)}
	for _, n := range ok {
		if err := ValidateBranchName(n); err != nil {
			t.Errorf("ValidateBranchName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateBranchName(n); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateBranchName(%q) = %v, want ErrInvalid", n, err)
		}
	}
	// Every accepted name must also be accepted by git itself.
	dir := newRepo(t)
	for _, n := range ok {
		rawGit(t, dir, "check-ref-format", "refs/heads/"+n)
	}
}

func TestGitEscapeHatch(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	res, err := r.Git(ctx, "rev-parse", "HEAD")
	if err != nil || len(strings.TrimSpace(res.Stdout)) != 40 {
		t.Fatalf("Git rev-parse: %+v %v", res, err)
	}
	for _, args := range [][]string{
		{}, {"-c", "core.hooksPath=/x", "status"}, {"--exec-path=/x", "status"}, {"fetch", "origin"}, {"push"}, {"clone", "x"},
		{"config", "core.fsmonitor", "evil"}, {"gc"}, {"submodule", "update"}, {"daemon"}, {"remote", "add", "x", "y"},
		{"status", "a\x00b"},
	} {
		if _, err := r.Git(ctx, args...); !errors.Is(err, ErrInvalid) {
			t.Errorf("Git(%q) = %v, want ErrInvalid", args, err)
		}
	}
}

// An agent owns the files of its worktree, .git included. Rewriting that file (or
// replacing it with a directory) must not redirect what the harness does with its
// handle: the git directory was fixed when the handle was opened.
func TestHandleIgnoresARewrittenDotGitFile(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	mainRepo := openRepo(t, dir)
	wt := filepath.Join(t.TempDir(), "agent-tree")
	rawGit(t, dir, "worktree", "add", "-q", "-b", "agent", wt)
	r := openRepo(t, wt)
	ctx := ctxT(t)
	mainHead, _ := mainRepo.Head(ctx)
	mainStatus, _ := mainRepo.Status(ctx)
	markers := t.TempDir()

	type step struct {
		name     string
		redirect func()
	}
	rmDotGit := func() {
		if err := os.RemoveAll(filepath.Join(wt, ".git")); err != nil {
			t.Fatal(err)
		}
	}
	redirects := []step{
		{"points at the main repository", func() {
			rmDotGit()
			writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(dir, ".git")+"\n")
		}},
		{"deleted", rmDotGit},
		{"replaced by a hostile repository", func() {
			rmDotGit()
			hostile := filepath.Join(wt, ".git")
			if err := os.MkdirAll(filepath.Join(hostile, "hooks"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(hostile, "HEAD"), "ref: refs/heads/evil\n")
			writeFile(t, filepath.Join(hostile, "config"), "[core]\n\trepositoryformatversion = 0\n\thooksPath = "+filepath.Join(hostile, "hooks")+"\n\tfsmonitor = \"touch "+filepath.Join(markers, "fsmonitor")+"; echo\"\n")
			writeFile(t, filepath.Join(hostile, "hooks", "pre-commit"), "#!/bin/sh\ntouch "+filepath.Join(markers, "pre-commit")+"\n")
			if err := os.Chmod(filepath.Join(hostile, "hooks", "pre-commit"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for i, st := range redirects {
		name := st.name
		i++
		st.redirect()
		writeFile(t, filepath.Join(wt, "agent-file-"+strconv.Itoa(i)+".txt"), "by the agent\n")
		sha, err := r.CommitAll(ctx, "agent work "+name, Author{Name: "agent"})
		if err != nil || sha == "" {
			t.Fatalf("%s: CommitAll = %q, %v", name, sha, err)
		}
		if br, _ := r.Branch(ctx); br != "agent" {
			t.Fatalf("%s: the commit went to branch %q", name, br)
		}
		if head, _ := mainRepo.Head(ctx); head != mainHead {
			t.Fatalf("%s: the main repository's HEAD moved", name)
		}
		if st, _ := mainRepo.Status(ctx); len(st.Staged) != len(mainStatus.Staged) || len(st.Untracked) != len(mainStatus.Untracked) {
			t.Fatalf("%s: the main repository's index/work tree changed: %+v", name, st)
		}
		if _, err := os.Stat(filepath.Join(dir, "agent-file-"+strconv.Itoa(i)+".txt")); err == nil {
			t.Fatalf("%s: a file landed in the user's checkout", name)
		}
	}
	if ents, _ := os.ReadDir(markers); len(ents) != 0 {
		t.Fatalf("hostile replacement .git ran code: %v", ents)
	}
}
