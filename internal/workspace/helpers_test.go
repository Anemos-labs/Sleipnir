package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/gitx"
)

// Fixtures use the real git binary under a hermetic environment; the code under
// test opens them through gitx with hermetic configuration too.

var fixtureClock atomic.Int64

func fixtureEnv(home string) []string {
	n := fixtureClock.Add(1)
	date := time.Unix(1_700_000_000+n*60, 0).UTC().Format("2006-01-02T15:04:05") + " +0000"
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date,
	}
}

func rawGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = fixtureEnv(t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

func rawGitMayFail(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = fixtureEnv(os.TempDir())
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func isolateHome(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func hasMarkers(b []byte) bool {
	for _, l := range bytes.Split(b, []byte("\n")) {
		l = bytes.TrimRight(l, "\r")
		if bytes.HasPrefix(l, []byte("<<<<<<< ")) || bytes.HasPrefix(l, []byte(">>>>>>> ")) {
			return true
		}
	}
	return false
}

func tctx(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// newRepo creates a repository with a small project committed on main:
//
//	README.md, go.mod, cmd/app/main.go, internal/core/core.go, internal/util/util.go, docs/guide.md
func newRepo(t testing.TB) string {
	t.Helper()
	isolateHome(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(dir, "project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "init", "-q", "-b", "main", ".")
	files := map[string]string{
		"README.md":                 "# project\n",
		"go.mod":                    "module example.com/project\n\ngo 1.24\n",
		"cmd/app/main.go":           "package main\n\nfunc main() {}\n",
		"internal/core/core.go":     "package core\n\nfunc Name() string { return \"core\" }\n\nfunc Version() int { return 1 }\n\nfunc Extra() int { return 0 }\n",
		"internal/util/util.go":     "package util\n\nfunc Add(a, b int) int { return a + b }\n",
		"docs/guide.md":             "# guide\n\nline one\nline two\nline three\nline four\nline five\n",
		"scripts/ok.sh":             "#!/bin/sh\nexit 0\n",
		"data/table with space.csv": "a,b\n1,2\n",
	}
	for name, content := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), content)
	}
	if err := os.Chmod(filepath.Join(dir, "scripts", "ok.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-q", "-m", "initial project")
	return dir
}

func openRepo(t testing.TB, dir string, opts ...gitx.Option) *gitx.Repo {
	t.Helper()
	r, err := gitx.Open(dir, append([]gitx.Option{gitx.WithHermeticConfig()}, opts...)...)
	if err != nil {
		t.Fatalf("gitx.Open(%s): %v", dir, err)
	}
	return r
}

// testClock returns strictly increasing times so commits made by the code under
// test never share a timestamp.
func testClock() func() time.Time {
	var n atomic.Int64
	return func() time.Time { return time.Unix(1_800_000_000+n.Add(1), 0).UTC() }
}

// newManager makes a Manager over dir with its trees in a sibling temp directory.
func newManager(t testing.TB, repo *gitx.Repo) *Manager {
	t.Helper()
	m := &Manager{Repo: repo, Dir: filepath.Join(t.TempDir(), "trees"), Prefix: "sleipnir/s1", Clock: testClock()}
	return m
}

// events collects workspace events.
type eventLog struct {
	mu sync.Mutex
	ev []Event
}

func (l *eventLog) fn() EventFunc {
	return func(e Event) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.ev = append(l.ev, e)
	}
}

func (l *eventLog) types() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.ev {
		out = append(out, e.Type)
	}
	return out
}

func (l *eventLog) count(typ string) int {
	n := 0
	for _, t := range l.types() {
		if t == typ {
			n++
		}
	}
	return n
}

func (l *eventLog) first(typ string) (Event, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.ev {
		if e.Type == typ {
			return e, true
		}
	}
	return Event{}, false
}

// edit writes content to rel inside a tree.
func edit(t testing.TB, tree *Tree, rel, content string) {
	t.Helper()
	writeFile(t, filepath.Join(tree.Path, filepath.FromSlash(rel)), content)
}

func mustCreate(t testing.TB, m *Manager, agent string, opts ...CreateOptions) *Tree {
	t.Helper()
	var o CreateOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	tree, err := m.Create(tctx(t), agent, o)
	if err != nil {
		t.Fatalf("Create(%s): %v", agent, err)
	}
	return tree
}

func mustQueue(t testing.TB, m *Manager, o QueueOptions) *Queue {
	t.Helper()
	q, err := NewQueue(tctx(t), m, o)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

func mustSubmit(t testing.TB, q *Queue, s Submission) *Result {
	t.Helper()
	res, err := q.Submit(tctx(t), s)
	if err != nil {
		t.Fatalf("Submit(%s): %v", s.Agent, err)
	}
	return res
}

func osRename(from, to string) error { return os.Rename(from, to) }
