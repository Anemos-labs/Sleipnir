package env

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/rl"
)

// sharedGoCache lets the many `go test` runs of this package share one build
// cache. Compiling the standard library from scratch for every fresh HOME would
// take ten seconds per test process; the developer's own cache is already warm
// and only ever gains entries. Production runs get a per-rollout cache instead
// (see EnvSpec), which is why this is a test-only override.
var sharedGoCache string

func TestMain(m *testing.M) {
	dir := ""
	if out, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil {
		dir = strings.TrimSpace(string(out))
	}
	cleanup := false
	if dir == "" || dir == "off" {
		d, err := os.MkdirTemp("", "envtest-gocache-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		dir, cleanup = d, true
	}
	sharedGoCache = dir
	code := m.Run()
	if cleanup {
		_ = removeAllNoFollow(dir)
	}
	os.Exit(code)
}

// gitEnv is the environment for git commands run by tests: hermetic and
// deterministic.
func gitEnv(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Test Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Test Committer", "GIT_COMMITTER_EMAIL=committer@example.com",
		"LC_ALL=C",
	}
	return append(env, extra...)
}

// fixtureRepo is a real git repository in a temp dir.
type fixtureRepo struct {
	t   testing.TB
	Dir string
	n   int
}

func newFixtureRepo(t testing.TB) *fixtureRepo {
	t.Helper()
	dir := t.TempDir()
	r := &fixtureRepo{t: t, Dir: dir}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *fixtureRepo) git(args ...string) string {
	r.t.Helper()
	return r.gitEnv(nil, args...)
}

func (r *fixtureRepo) gitEnv(extra []string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}, args...)...)
	cmd.Dir = r.Dir
	cmd.Env = gitEnv(extra...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// write creates or replaces a file (content "" with a trailing "\x00delete" is not
// supported; use remove).
func (r *fixtureRepo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit stages everything and commits with a deterministic date; it returns the
// new commit id.
func (r *fixtureRepo) commit(msg string) string {
	r.t.Helper()
	r.n++
	date := time.Date(2024, 1, r.n, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	r.git("add", "-A")
	r.gitEnv([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "--allow-empty", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// ---- a tiny Go module used as a real, runnable task ----

const goMod = "module example.com/mathx\n\ngo 1.24\n"

// buggyMath has a Max that returns the minimum.
const buggyMath = `package mathx

// Max returns the larger of a and b.
func Max(a, b int) int {
	if a > b {
		return b
	}
	return a
}

// Abs returns the absolute value of v.
func Abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
`

const fixedMath = `package mathx

// Max returns the larger of a and b.
func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Abs returns the absolute value of v.
func Abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
`

// visibleTest is the test the agent can see; it passes even with the bug.
const visibleTest = `package mathx

import "testing"

func TestAbs(t *testing.T) {
	if Abs(-3) != 3 || Abs(4) != 4 {
		t.Fatal("Abs is broken")
	}
}
`

// hiddenTest is what the verifier adds; it fails until Max is fixed.
const hiddenTest = `package mathx

import "testing"

func TestMax(t *testing.T) {
	cases := []struct{ a, b, want int }{{1, 2, 2}, {5, 3, 5}, {-1, -7, -1}, {4, 4, 4}}
	for _, c := range cases {
		if got := Max(c.a, c.b); got != c.want {
			t.Errorf("Max(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
`

// mathxRepo returns a repository whose head commit is the buggy starting state.
func mathxRepo(t testing.TB) (*fixtureRepo, string) {
	t.Helper()
	r := newFixtureRepo(t)
	r.write("go.mod", goMod)
	r.write("mathx.go", buggyMath)
	r.write("mathx_test.go", visibleTest)
	r.write(".gitignore", "*.log\nbuild/\n")
	r.write("README.md", "# mathx\n")
	return r, r.commit("initial")
}

// mathxTask is the task "fix Max" against the repository at commit.
func mathxTask(r *fixtureRepo, commit string) rl.Task {
	return rl.Task{
		ID:     "mathx-max",
		Kind:   rl.TaskFix,
		Repo:   rl.RepoSpec{Path: r.Dir, Commit: commit, License: "MIT"},
		Prompt: "Max returns the smaller number. Fix it.",
		Team:   rl.Team{Mode: "single"},
		Verifier: rl.Verifier{
			Cmd:       "go test -count=1 ./...",
			TimeoutS:  120,
			Hidden:    map[string]string{"mathx_hidden_test.go": "text:" + hiddenTest},
			Protected: []string{"*_test.go", "go.mod"},
		},
		Budget: rl.Budget{WallS: 300},
		Tags:   []string{"go", "small"},
	}
}

// newManager builds a Workspaces manager under a temp root. Network isolation is
// off unless a test asks for it: the probe is slow and irrelevant to most tests.
func newManager(t testing.TB, mut ...func(*WorkspaceOptions)) *Workspaces {
	t.Helper()
	o := WorkspaceOptions{
		Root:                filepath.Join(t.TempDir(), "root"),
		DisableNetIsolation: true,
		SetupTimeout:        2 * time.Minute,
		SetEnv:              map[string]string{"GOCACHE": sharedGoCache},
		FailureTTL:          -1,
	}
	for _, f := range mut {
		f(&o)
	}
	m, err := NewWorkspaces(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

// treeFiles lists a directory's regular files and symlinks (relative, sorted),
// ignoring .git.
func treeFiles(t testing.TB, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == ".git" && d.IsDir() {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ctxT returns a context that ends with the test.
func ctxT(t testing.TB) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// eventually polls cond until it holds or the timeout elapses.
func eventually(t testing.TB, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}
