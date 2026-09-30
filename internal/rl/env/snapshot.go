package env

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/rl"
)

// SkippedPath records something left out of a baseline or a diff, and why. Such
// paths are reported, never silently dropped: a policy that discovers that some
// odd construct makes its files vanish from the diff would otherwise learn to
// exploit it.
type SkippedPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system's entropy source failing is not recoverable
	}
	return hex.EncodeToString(b)
}

// ---- exporting a commit ----

type treeEntry struct {
	mode string // "100644", "100755", "120000", "160000"
	sha  string
	path string
}

// listTree returns the entries of the commit's tree in tree order.
func (m *Workspaces) listTree(ctx context.Context, gitDir, commit string) ([]treeEntry, error) {
	out, err := m.git.RunEnv(ctx, "", nil, nil, 1<<30, "--git-dir="+gitDir, "ls-tree", "-r", "-z", "--full-tree", "--end-of-options", commit)
	if err != nil {
		return nil, err
	}
	var ents []treeEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree record %q", rec)
		}
		ents = append(ents, treeEntry{mode: f[0], sha: f[2], path: string(name)})
	}
	return ents, nil
}

// exportCommit writes the commit's tree into dst without git history and
// without running any checkout machinery. It reads blobs with `cat-file` and
// writes them itself, so none of the repository's attributes, smudge filters
// (git-lfs), line-ending rules or hooks can run or alter a byte: the workspace
// holds exactly what the commit holds. Paths are validated and written through
// os.Root, so a crafted tree ("../x", ".git/hooks/...", a symlink followed by
// a path below it) cannot write outside dst or plant git control files.
//
// It returns the exported file paths (submodule entries become empty
// directories and are not listed).
func (m *Workspaces) exportCommit(ctx context.Context, gitDir, commit, dst string) ([]string, error) {
	ents, err := m.listTree(ctx, gitDir, commit)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	var blobs []treeEntry
	for _, e := range ents {
		if err := validRelPath(e.path); err != nil {
			return nil, fmt.Errorf("commit %s contains a path git would refuse to check out (%q): %w", commit[:min(12, len(commit))], e.path, err)
		}
		switch e.mode {
		case "160000": // submodule: an empty directory, as `git checkout` leaves it
			if err := ensureDirIn(root, e.path); err != nil {
				return nil, err
			}
		case "100644", "100755", "120000":
			blobs = append(blobs, e)
		default:
			return nil, fmt.Errorf("unsupported tree entry mode %s for %q", e.mode, e.path)
		}
	}

	// One long-lived cat-file: requests go in on a pipe from a goroutine while
	// this goroutine consumes the answers, so neither side can block the other.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gitDone := make(chan error, 1)
	go func() {
		err := m.git.Stream(ctx, "", nil, inR, outW, 0, "--git-dir="+gitDir, "cat-file", "--batch")
		outW.CloseWithError(err)
		gitDone <- err
	}()
	go func() {
		bw := bufio.NewWriter(inW)
		for _, e := range blobs {
			_, _ = bw.WriteString(e.sha)
			_ = bw.WriteByte('\n')
		}
		_ = bw.Flush()
		_ = inW.Close()
	}()

	br := bufio.NewReaderSize(outR, 1<<20)
	ensured := map[string]bool{}
	files := make([]string, 0, len(blobs))
	var loopErr error
	for _, e := range blobs {
		header, err := br.ReadString('\n')
		if err != nil {
			loopErr = fmt.Errorf("reading cat-file output: %w", err)
			break
		}
		f := strings.Fields(header)
		if len(f) != 3 || f[1] != "blob" {
			loopErr = fmt.Errorf("object %s for %q is not a readable blob: %q", e.sha, e.path, strings.TrimSpace(header))
			break
		}
		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			loopErr = fmt.Errorf("bad cat-file header %q", header)
			break
		}
		if dir := path.Dir(e.path); dir != "." && !ensured[dir] {
			if err := ensureDirIn(root, dir); err != nil {
				loopErr = err
				break
			}
			ensured[dir] = true
		}
		if e.mode == "120000" {
			target := make([]byte, size)
			if _, err := io.ReadFull(br, target); err != nil {
				loopErr = err
				break
			}
			if err := removeIn(root, e.path); err != nil {
				loopErr = err
				break
			}
			if err := os.Symlink(string(target), filepath.Join(dst, filepath.FromSlash(e.path))); err != nil {
				loopErr = err
				break
			}
		} else {
			mode := fs.FileMode(0o644)
			if e.mode == "100755" {
				mode = 0o755
			}
			file, err := createFileIn(root, e.path, mode)
			if err != nil {
				loopErr = err
				break
			}
			_, cerr := io.CopyN(file, br, size)
			if err := file.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				loopErr = cerr
				break
			}
		}
		if _, err := br.Discard(1); err != nil { // the newline after the content
			loopErr = err
			break
		}
		files = append(files, e.path)
	}
	if loopErr != nil {
		cancel()
		inR.CloseWithError(loopErr)
		outR.CloseWithError(loopErr)
		<-gitDone
		return nil, loopErr
	}
	_ = inR.Close()
	if err := <-gitDone; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return nil, err
	}
	return files, nil
}

// cloneCommit is ModeClone: a local clone with full history, checked out
// detached at commit. It exists for tasks where history is deliberately part of
// the problem; for anything mined from history it leaks the answer.
func (m *Workspaces) cloneCommit(ctx context.Context, gitDir, commit, dst string) ([]string, error) {
	if _, err := m.git.Run(ctx, "", nil, "clone", "--quiet", "--no-checkout", "--no-hardlinks", "--template=", gitDir, dst); err != nil {
		return nil, err
	}
	if _, err := m.git.Run(ctx, dst, nil, "checkout", "--quiet", "--detach", commit); err != nil {
		return nil, err
	}
	// The clone must not be able to fetch from (or leak its way back to) the
	// source repository, which would contain the future.
	_, _ = m.git.Run(ctx, dst, nil, "remote", "remove", "origin")
	out, err := m.git.Run(ctx, dst, nil, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			files = append(files, string(p))
		}
	}
	return files, nil
}

// ---- hashing a work tree into a git tree ----

type hashOpts struct {
	GitDir       string   // scratch bare repository whose alternates point at the baseline objects
	AltObjects   string   // baseline objects directory (blobs found there are not rewritten)
	Work         string   // work tree
	Base         string   // baseline tree that seeds the index ("" for none)
	Force        []string // paths included even when ignored (tracked at the source commit)
	Write        bool     // write blobs into GitDir; false only computes ids (integrity checks)
	MaxFiles     int
	MaxFileBytes int64
}

type hashResult struct {
	Tree    string
	Skipped []SkippedPath
	Files   int
	Bytes   int64
}

const (
	defaultMaxFiles     = 500_000
	defaultMaxFileBytes = 256 << 20
)

// hashTree turns the content of a work tree into a git tree object, without
// ever running `git add`.
//
// `git add -A` is convenient and wrong here. It aborts on inputs an agent can
// create on purpose (a nested repository without commits, a path git considers
// invalid) and it applies attribute filters and line-ending conversions, so the
// stored bytes would differ from the ones on disk. Aborting would also turn
// "make the diff fail" into a way to dodge a bad reward. Instead the file list
// comes from `git ls-files` (which knows the ignore rules) and every file is
// then hashed here, byte for byte, with per-file problems recorded as
// SkippedPath instead of errors.
func (m *Workspaces) hashTree(ctx context.Context, o hashOpts) (hashResult, error) {
	var res hashResult
	if o.MaxFiles <= 0 {
		o.MaxFiles = defaultMaxFiles
	}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = defaultMaxFileBytes
	}
	gitArgs := func(args ...string) []string { return append([]string{"--git-dir=" + o.GitDir}, args...) }
	listIdx := filepath.Join(o.GitDir, "index-list")
	env := []string{"GIT_INDEX_FILE=" + listIdx, "GIT_WORK_TREE=" + o.Work}
	if o.Base != "" {
		if _, err := m.git.RunEnv(ctx, o.Work, env, nil, 1<<20, gitArgs("read-tree", o.Base)...); err != nil {
			return res, err
		}
	} else if _, err := m.git.RunEnv(ctx, o.Work, env, nil, 1<<20, gitArgs("read-tree", "--empty")...); err != nil {
		return res, err
	}
	out, err := m.git.RunEnv(ctx, o.Work, env, nil, 1<<30, gitArgs("ls-files", "-z", "--cached", "--others", "--exclude-standard")...)
	if err != nil {
		return res, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 && !seen[string(p)] {
			seen[string(p)] = true
			paths = append(paths, string(p))
		}
	}
	for _, p := range o.Force {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	if len(paths) > o.MaxFiles {
		return res, &limitError{msg: fmt.Sprintf("the work tree has %d files (limit %d)", len(paths), o.MaxFiles)}
	}

	root, err := os.OpenRoot(o.Work)
	if err != nil {
		return res, err
	}
	defer func() { _ = root.Close() }()

	dirs := &dirChecker{root: root, memo: map[string]bool{}}
	type result struct {
		mode, sha string
		size      int64
		skip      string
		gone      bool
		err       error
	}
	results := make([]result, len(paths))
	workers := min(runtime.GOMAXPROCS(0), 8)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, p := range paths {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			r := &results[i]
			if strings.HasSuffix(p, "/") {
				r.skip = "nested repository or directory (git does not descend into it)"
				return
			}
			if err := validRelPath(p); err != nil {
				r.skip = err.Error()
				return
			}
			if !dirs.ok(path.Dir(p)) {
				r.skip = "a parent directory is a symlink or not a directory"
				return
			}
			fi, err := root.Lstat(p)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				r.gone = true
				return
			case err != nil:
				r.skip = "cannot stat: " + err.Error()
				return
			}
			switch {
			case fi.Mode()&fs.ModeSymlink != 0:
				target, err := os.Readlink(filepath.Join(o.Work, filepath.FromSlash(p)))
				if err != nil {
					r.skip = "cannot read symlink: " + err.Error()
					return
				}
				r.mode, r.size = "120000", int64(len(target))
				r.sha, r.err = writeBlob(o, int64(len(target)), strings.NewReader(target))
			case fi.Mode().IsRegular():
				if fi.Size() > o.MaxFileBytes {
					r.skip = fmt.Sprintf("file is larger than %d bytes", o.MaxFileBytes)
					return
				}
				f, err := root.Open(p)
				if err != nil {
					r.skip = "cannot open: " + err.Error()
					return
				}
				r.mode = "100644"
				if fi.Mode().Perm()&0o100 != 0 {
					r.mode = "100755"
				}
				r.size = fi.Size()
				r.sha, r.err = writeBlob(o, fi.Size(), f)
				_ = f.Close()
			default:
				r.skip = "special file (socket, FIFO or device)"
			}
			if r.err != nil {
				// A file that vanished or changed while being read is the agent's
				// doing (or a dying process's), not an infrastructure fault.
				r.skip = "unreadable: " + r.err.Error()
				r.err = nil
				r.sha = ""
			}
		}(i, p)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return res, err
	}

	var info bytes.Buffer
	for i, r := range results {
		switch {
		case r.gone:
		case r.skip != "":
			res.Skipped = append(res.Skipped, SkippedPath{Path: paths[i], Reason: r.skip})
		default:
			fmt.Fprintf(&info, "%s %s\t%s\x00", r.mode, r.sha, paths[i])
			res.Files++
			res.Bytes += r.size
		}
	}
	newIdx := filepath.Join(o.GitDir, "index-new")
	_ = os.Remove(newIdx)
	env2 := []string{"GIT_INDEX_FILE=" + newIdx}
	if _, err := m.git.RunEnv(ctx, "", env2, &info, 1<<20, gitArgs("update-index", "--add", "-z", "--index-info")...); err != nil {
		return res, err
	}
	wt := []string{"write-tree"}
	if !o.Write {
		wt = append(wt, "--missing-ok")
	}
	tree, err := m.git.RunEnv(ctx, "", env2, nil, 1<<20, gitArgs(wt...)...)
	if err != nil {
		return res, err
	}
	res.Tree = strings.TrimSpace(string(tree))
	if !isSHA(res.Tree) {
		return res, fmt.Errorf("git write-tree returned %q", res.Tree)
	}
	return res, nil
}

// limitError reports that an agent-produced work tree exceeds a resource limit.
// It is deliberately not an InfraError: the agent caused it.
type limitError struct{ msg string }

func (e *limitError) Error() string { return e.msg }

// dirChecker memoises whether a directory path consists solely of real
// directories below the root: a path that crosses a symlink (say a tracked
// directory the agent replaced by a link to elsewhere) must not be read.
type dirChecker struct {
	root *os.Root
	mu   sync.Mutex
	memo map[string]bool
}

func (d *dirChecker) ok(dir string) bool {
	if dir == "." || dir == "" {
		return true
	}
	d.mu.Lock()
	v, done := d.memo[dir]
	d.mu.Unlock()
	if done {
		return v
	}
	v = d.ok(path.Dir(dir))
	if v {
		fi, err := d.root.Lstat(dir)
		v = err == nil && fi.IsDir()
	}
	d.mu.Lock()
	d.memo[dir] = v
	d.mu.Unlock()
	return v
}

// writeBlob hashes size bytes from r as a git blob and, when o.Write is set,
// stores it as a loose object in o.GitDir unless the baseline already has it.
// Reading anything other than exactly size bytes means the file changed
// underneath us and is an error.
func writeBlob(o hashOpts, size int64, r io.Reader) (string, error) {
	h := sha1.New()
	header := fmt.Sprintf("blob %d\x00", size)
	if !o.Write {
		_, _ = io.WriteString(h, header)
		n, err := io.Copy(h, io.LimitReader(r, size+1))
		if err != nil {
			return "", err
		}
		if n != size {
			return "", fmt.Errorf("file changed size while being read (%d, expected %d)", n, size)
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	objDir := filepath.Join(o.GitDir, "objects")
	tmp, err := os.CreateTemp(objDir, "tmp_obj_")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	zw, _ := zlib.NewWriterLevel(tmp, zlib.BestSpeed)
	mw := io.MultiWriter(h, zw)
	_, _ = io.WriteString(mw, header)
	n, err := io.Copy(mw, io.LimitReader(r, size+1))
	if err == nil && n != size {
		err = fmt.Errorf("file changed size while being read (%d, expected %d)", n, size)
	}
	if err == nil {
		err = zw.Close()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	sha := hex.EncodeToString(h.Sum(nil))
	rel := filepath.Join(sha[:2], sha[2:])
	for _, dir := range []string{o.AltObjects, objDir} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			return sha, nil
		}
	}
	dest := filepath.Join(objDir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	_ = os.Chmod(tmpName, 0o444)
	if err := os.Rename(tmpName, dest); err != nil {
		return "", err
	}
	return sha, nil
}

// ErrDiffTooLarge is returned when an agent's diff exceeds the size limit.
var ErrDiffTooLarge = &limitError{msg: "the diff is larger than the size limit"}

// diffTrees renders the difference between two trees as a binary-safe patch
// without rename detection: every path stands alone, which is what lets the
// verifier drop protected paths by removing whole file sections.
func (m *Workspaces) diffTrees(ctx context.Context, gitDir, base, tree string, max int64) ([]byte, error) {
	if base == tree {
		return nil, nil
	}
	out, err := m.git.RunEnv(ctx, "", nil, nil, max, "--git-dir="+gitDir, "diff-tree", "-r", "-p", "--binary", "--full-index",
		"--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", base, tree)
	if errors.Is(err, ErrOutputLimit) {
		return nil, ErrDiffTooLarge
	}
	return out, err
}

// ---- snapshots ----

// snapshot is a cached, immutable starting state for one (repo, commit, setup):
// the tree with setup applied, the tool home setup populated, and an object
// store holding the baseline tree that diffs are computed against.
type snapshot struct {
	key  string
	dir  string
	tree string
	home string
	base string // bare repository holding the baseline objects
	meta snapMeta
}

type snapMeta struct {
	Key      string        `json:"key"`
	Source   string        `json:"source"`
	Commit   string        `json:"commit,omitempty"`
	Mode     string        `json:"mode"`
	Subdir   string        `json:"subdir,omitempty"`
	BaseTree string        `json:"base_tree"`
	Setup    []string      `json:"setup,omitempty"`
	Created  time.Time     `json:"created"`
	SetupMs  int64         `json:"setup_ms"`
	Files    int           `json:"files"`
	Skipped  []SkippedPath `json:"skipped,omitempty"`
	Git      string        `json:"git,omitempty"`
}

func (s *snapshot) altObjects() string { return filepath.Join(s.base, "objects") }

const gitConfigFile = `[user]
	name = sleipnir
	email = sleipnir@localhost
[core]
	autocrlf = false
[init]
	defaultBranch = main
[safe]
	directory = *
[advice]
	detachedHead = false
`

// SetupError is a failed setup command.
type SetupError struct {
	Cmd      string
	ExitCode int
	TimedOut bool
	Output   string
}

func (e *SetupError) Error() string {
	why := fmt.Sprintf("exit status %d", e.ExitCode)
	if e.TimedOut {
		why = "timed out"
	}
	out := strings.TrimSpace(e.Output)
	if len(out) > 1500 {
		out = "..." + out[len(out)-1500:]
	}
	return fmt.Sprintf("setup command %q failed (%s):\n%s", e.Cmd, why, out)
}

// buildSnapshot creates a snapshot in a temporary directory and moves it into
// place atomically. It runs on the manager's own context, not a caller's, so
// one cancelled rollout does not fail the setup others are waiting for.
func (m *Workspaces) buildSnapshot(ctx context.Context, task rl.Task, src *repoSource, key string) (*snapshot, error) {
	final := filepath.Join(m.root, "snaps", key)
	tmpDir, err := os.MkdirTemp(filepath.Join(m.root, "snaps"), ".tmp-")
	if err != nil {
		return nil, Infra("snapshot", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = removeAllNoFollow(tmpDir)
		}
	}()
	s := &snapshot{
		key: key, dir: tmpDir,
		tree: filepath.Join(tmpDir, "tree"), home: filepath.Join(tmpDir, "home"), base: filepath.Join(tmpDir, "base.git"),
	}

	// 1. the files
	var files []string
	switch {
	case src.kind == "git" && m.opts.Mode == ModeClone:
		files, err = m.cloneCommit(ctx, src.gitDir, src.commit, s.tree)
	case src.kind == "git":
		files, err = m.exportCommit(ctx, src.gitDir, src.commit, s.tree)
	default:
		err = copyTreeGo(ctx, src.dir, s.tree)
		if err == nil {
			if _, serr := os.Lstat(filepath.Join(s.tree, ".git")); serr == nil {
				// A plain directory that carries a stray .git: it is not a repository
				// we trust and would confuse the agent's own git.
				err = removeAllNoFollow(filepath.Join(s.tree, ".git"))
			}
		}
	}
	if err != nil {
		return nil, Infra("materialize "+src.ident, err)
	}
	if task.Repo.Subdir != "" {
		if fi, err := os.Stat(filepath.Join(s.tree, filepath.FromSlash(task.Repo.Subdir))); err != nil || !fi.IsDir() {
			return nil, Infra("materialize "+src.ident, fmt.Errorf("repo.subdir %q does not exist at that commit", task.Repo.Subdir))
		}
	}

	// 2. the home the setup commands (and later the rollouts) start from
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		return nil, Infra("snapshot", err)
	}
	if err := os.WriteFile(filepath.Join(s.home, ".gitconfig"), []byte(gitConfigFile), 0o644); err != nil {
		return nil, Infra("snapshot", err)
	}

	// 3. setup
	started := time.Now()
	if len(task.Setup) > 0 {
		if err := m.runSetup(ctx, task, s, key); err != nil {
			return nil, Infra("setup", err)
		}
	}
	setupMs := time.Since(started).Milliseconds()

	// 4. the baseline: what the tree holds now, which is what diffs are against
	if err := m.git.initBare(ctx, s.base); err != nil {
		return nil, Infra("snapshot", err)
	}
	hr, err := m.hashTree(ctx, hashOpts{
		GitDir: s.base, Work: s.tree, Force: files, Write: true,
		MaxFiles: m.opts.MaxFiles, MaxFileBytes: m.opts.MaxFileBytes,
	})
	if err != nil {
		return nil, Infra("baseline", err)
	}
	_ = os.Remove(filepath.Join(s.base, "index-list"))
	_ = os.Remove(filepath.Join(s.base, "index-new"))

	// 5. the repository the agent sees
	if m.opts.Mode != ModeClone || src.kind != "git" {
		if err := m.makeAgentRepo(ctx, s, hr.Tree); err != nil {
			return nil, Infra("agent repo", err)
		}
	}

	s.meta = snapMeta{
		Key: key, Source: src.ident, Commit: src.commit, Mode: m.opts.Mode, Subdir: task.Repo.Subdir,
		BaseTree: hr.Tree, Setup: task.Setup, Created: time.Now().UTC(), SetupMs: setupMs,
		Files: hr.Files, Skipped: hr.Skipped, Git: m.git.Version(ctx),
	}
	raw, _ := json.MarshalIndent(s.meta, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "snapshot.json"), raw, 0o644); err != nil {
		return nil, Infra("snapshot", err)
	}
	if err := os.Rename(tmpDir, final); err != nil {
		if _, serr := os.Stat(filepath.Join(final, "snapshot.json")); serr == nil {
			// Another process finished the same snapshot first; use theirs.
			return m.loadSnapshot(key)
		}
		return nil, Infra("snapshot", err)
	}
	ok = true
	s.dir = final
	s.tree, s.home, s.base = filepath.Join(final, "tree"), filepath.Join(final, "home"), filepath.Join(final, "base.git")
	// The agent repository's alternates file names the baseline objects by
	// absolute path, and the build happened under a temporary name.
	if m.opts.Mode != ModeClone || src.kind != "git" {
		alt := filepath.Join(s.tree, ".git", "objects", "info", "alternates")
		if err := os.WriteFile(alt, []byte(s.altObjects()+"\n"), 0o644); err != nil {
			return nil, Infra("snapshot", err)
		}
	}
	return s, nil
}

// runSetup executes the task's setup commands, with network access, inside the
// half-built snapshot. Their result is what every rollout then starts from.
func (m *Workspaces) runSetup(ctx context.Context, task rl.Task, s *snapshot, key string) error {
	tmpDir := filepath.Join(s.dir, "setup-tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	defer func() { _ = removeAllNoFollow(tmpDir) }()
	// Unique to this build: another process that shares the work directory may be building the same snapshot, and
	// sweeping a marker kills every process that carries it, so a marker both share lets the one that finishes first
	// kill the other's setup.
	marker := "setup-" + key + "-" + randHex(6)
	env := BuildEnv(EnvSpec{
		Home: s.home, Tmp: tmpDir, Network: true, Marker: marker, Base: m.baseEnv,
		Deny: []string{m.root}, PassEnv: m.opts.PassEnv, Set: m.opts.SetEnv,
	})
	dir := s.tree
	if task.Repo.Subdir != "" {
		dir = filepath.Join(s.tree, filepath.FromSlash(task.Repo.Subdir))
	}
	pol := ExecPolicy{Network: true, Marker: marker, MaxOutput: m.opts.MaxOutput, Limits: m.opts.Limits, Grace: m.opts.KillGrace}
	var log bytes.Buffer
	defer func() { _ = os.WriteFile(filepath.Join(s.dir, "setup.log"), log.Bytes(), 0o644) }()
	defer sweepMarker(marker)
	for _, cmd := range task.Setup {
		res, err := m.sandbox.Exec(WithExecPolicy(ctx, pol), dir, env, cmd, m.opts.SetupTimeout)
		fmt.Fprintf(&log, "$ %s\n%s\n", cmd, res.Output)
		if err != nil {
			return fmt.Errorf("setup command %q could not run: %w", cmd, err)
		}
		if res.TimedOut || res.ExitCode != 0 || res.Canceled {
			if res.Canceled && ctx.Err() != nil {
				return ctx.Err()
			}
			return &SetupError{Cmd: cmd, ExitCode: res.ExitCode, TimedOut: res.TimedOut, Output: res.Output}
		}
	}
	return nil
}

// makeAgentRepo gives the tree a repository of its own with a single commit
// holding the baseline, so the agent can use git normally (status, diff,
// commit, stash) and `git diff` shows exactly the agent's change. The history
// is one commit deep on purpose: nothing after (or before) the starting state
// can be found in it.
func (m *Workspaces) makeAgentRepo(ctx context.Context, s *snapshot, baseTree string) error {
	gd := filepath.Join(s.tree, ".git")
	if _, err := m.git.Run(ctx, "", nil, "init", "--quiet", "--template=", s.tree); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(gd, "objects", "info"), 0o755); err != nil {
		return err
	}
	// The baseline objects live in the snapshot's object store; alternates make
	// them readable without copying them into every workspace.
	if err := os.WriteFile(filepath.Join(gd, "objects", "info", "alternates"), []byte(s.altObjects()+"\n"), 0o644); err != nil {
		return err
	}
	date := "2000-01-01T00:00:00+0000"
	env := []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	out, err := m.git.RunEnv(ctx, "", env, nil, 1<<16, "--git-dir="+gd, "commit-tree", baseTree, "-m", "sleipnir: starting state")
	if err != nil {
		return err
	}
	commit := strings.TrimSpace(string(out))
	for _, args := range [][]string{
		{"update-ref", "refs/heads/main", commit},
		{"symbolic-ref", "HEAD", "refs/heads/main"},
		{"config", "user.name", "sleipnir"},
		{"config", "user.email", "sleipnir@localhost"},
		{"config", "core.autocrlf", "false"},
		// Stat data that survives copying the tree: without these the first `git
		// status` in every fresh workspace would re-hash the whole repository.
		{"config", "core.trustctime", "false"},
		{"config", "core.checkstat", "minimal"},
	} {
		if _, err := m.git.Run(ctx, "", nil, append([]string{"--git-dir=" + gd}, args...)...); err != nil {
			return err
		}
	}
	wt := []string{"GIT_WORK_TREE=" + s.tree}
	if _, err := m.git.RunEnv(ctx, s.tree, wt, nil, 1<<20, "--git-dir="+gd, "read-tree", "HEAD"); err != nil {
		return err
	}
	_, _ = m.git.RunEnv(ctx, s.tree, wt, nil, 1<<20, "--git-dir="+gd, "update-index", "-q", "--refresh")
	return nil
}

// loadSnapshot reads a finished snapshot from disk.
func (m *Workspaces) loadSnapshot(key string) (*snapshot, error) {
	dir := filepath.Join(m.root, "snaps", key)
	raw, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return nil, err
	}
	var meta snapMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", filepath.Join(dir, "snapshot.json"), err)
	}
	return &snapshot{
		key: key, dir: dir, meta: meta,
		tree: filepath.Join(dir, "tree"), home: filepath.Join(dir, "home"), base: filepath.Join(dir, "base.git"),
	}, nil
}

// errSnapshotTampered means a cached snapshot no longer matches its baseline.
var errSnapshotTampered = errors.New("cached snapshot was modified after it was built")

// checkSnapshot re-hashes the snapshot's tree and compares it with the
// baseline recorded when it was built. The cache is shared by every rollout of
// a task, so a run that edits it (an agent that wandered up out of its
// workspace, or a leftover process) would silently change the starting state
// and the verifier's checkout of everything that follows. Ignored files are not
// covered.
func (m *Workspaces) checkSnapshot(ctx context.Context, s *snapshot) error {
	if m.opts.SkipSnapshotCheck {
		return nil
	}
	scratch, err := os.MkdirTemp(m.tmp, "chk-")
	if err != nil {
		return err
	}
	defer func() { _ = removeAllNoFollow(scratch) }()
	gd := filepath.Join(scratch, "git")
	if err := m.git.initBare(ctx, gd); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gd, "objects", "info", "alternates"), []byte(s.altObjects()+"\n"), 0o644); err != nil {
		return err
	}
	hr, err := m.hashTree(ctx, hashOpts{
		GitDir: gd, AltObjects: s.altObjects(), Work: s.tree, Base: s.meta.BaseTree, Write: false,
		MaxFiles: m.opts.MaxFiles, MaxFileBytes: m.opts.MaxFileBytes,
		Force: nil,
	})
	if err != nil {
		return err
	}
	if hr.Tree != s.meta.BaseTree {
		return fmt.Errorf("%w (%s != %s)", errSnapshotTampered, hr.Tree[:12], s.meta.BaseTree[:12])
	}
	return nil
}
