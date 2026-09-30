package taskgen

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl/env"
)

// writeFixture lays out a small fixture whose verifier is a shell script: no toolchain, so the tests are fast and hermetic.
func writeFixture(t *testing.T, root, id string, mutate func(spec map[string]any)) string {
	t.Helper()
	dir := filepath.Join(root, id)
	spec := map[string]any{
		"id": id, "kind": "fix", "lang": "sh", "difficulty": "easy",
		"prompt": "add() in lib.sh must print the sum of its two arguments.", "verify": "sh check.sh", "timeout_s": 30,
		"protected": []string{"check.sh"}, "team": map[string]any{"mode": "single"}, "tags": []string{"demo"},
		"budget": map[string]any{"steps": 12, "requests": 30, "wall_s": 120},
	}
	if mutate != nil {
		mutate(spec)
	}
	raw, _ := json.MarshalIndent(spec, "", "  ")
	files := map[string]string{
		"task.json":        string(raw),
		"start/lib.sh":     "add() { echo $(($1 - $2)); }\n",
		"start/README.md":  "# demo\n",
		"hidden/check.sh":  ". ./lib.sh\n[ \"$(add 2 3)\" = 5 ] && [ \"$(add 10 4)\" = 14 ]\n",
		"solution/lib.sh":  "add() { echo $(($1 + $2)); }\n",
		"solution/NOTE.md": "fixed\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func fixtureBlobs(t *testing.T) events.Blobs {
	t.Helper()
	b, err := events.NewDirBlobs(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFromFixtureBuildsAPortableTask(t *testing.T) {
	root := t.TempDir()
	dir := writeFixture(t, root, "demo-add", nil)
	blobs := fixtureBlobs(t)
	repos := filepath.Join(t.TempDir(), "repos")
	task, err := FromFixture(context.Background(), dir, FixtureOptions{RepoRoot: repos, RepoPathPrefix: "fixture-repos", HiddenBlobs: blobs, GoldBlobs: blobs})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "demo-add" || task.Kind != "fix" || task.Repo.Path != "fixture-repos/demo-add" || task.Verifier.Cmd != "sh check.sh" ||
		task.Verifier.TimeoutS != 30 || task.Budget.Steps != 12 || task.Team.Mode != "single" {
		t.Fatalf("task: %+v", task)
	}
	for _, want := range []string{"fixture", "sh", "fix", "easy", "demo"} {
		found := false
		for _, tg := range task.Tags {
			found = found || tg == want
		}
		if !found {
			t.Errorf("tags %v lack %q", task.Tags, want)
		}
	}
	// The hidden test is in the blob store and is protected; its text is not in the task.
	ref := task.Verifier.Hidden["check.sh"]
	if !strings.HasPrefix(ref, "blob:") {
		t.Fatalf("hidden check.sh = %q", ref)
	}
	got, err := blobs.Get(core.Hash(strings.TrimPrefix(ref, "blob:")))
	if err != nil || !strings.Contains(string(got), `add 10 4`) {
		t.Fatalf("hidden blob: %q %v", got, err)
	}
	raw, _ := json.Marshal(task)
	if strings.Contains(string(raw), "add 10 4") {
		t.Error("the hidden test's content is in the task")
	}
	prot := strings.Join(task.Verifier.Protected, " ")
	if !strings.Contains(prot, "check.sh") {
		t.Errorf("protected: %v", task.Verifier.Protected)
	}
	// The reference solution is a patch of the changed files only, recorded in meta.
	var meta Meta
	if err := json.Unmarshal(task.Meta, &meta); err != nil || meta.GoldBlob == "" || meta.Generator != "taskgen/fixture" {
		t.Fatalf("meta: %+v %v", meta, err)
	}
	patch, err := blobs.Get(core.Hash(meta.GoldBlob))
	if err != nil || !strings.Contains(string(patch), "+add() { echo $(($1 + $2)); }") || !strings.Contains(string(patch), "NOTE.md") {
		t.Fatalf("gold patch: %q %v", patch, err)
	}
	// The repository is there, with one commit: the task's.
	out, err := exec.Command("git", "-C", filepath.Join(repos, "demo-add"), "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(out)) != task.Repo.Commit {
		t.Fatalf("the fixture's repository is at %q, the task says %s (%v)", out, task.Repo.Commit, err)
	}
	if out, _ := exec.Command("git", "-C", filepath.Join(repos, "demo-add"), "rev-list", "--count", "HEAD").Output(); strings.TrimSpace(string(out)) != "1" {
		t.Errorf("commits: %s", out)
	}
}

// The commit depends on the fixture alone: the same fixture anywhere has the same task, which is what lets a suite be locked by a hash.
func TestFromFixtureIsDeterministic(t *testing.T) {
	dir := writeFixture(t, t.TempDir(), "demo-add", nil)
	var commits []string
	var tasks []string
	for i := 0; i < 2; i++ {
		blobs := fixtureBlobs(t)
		task, err := FromFixture(context.Background(), dir, FixtureOptions{RepoRoot: t.TempDir(), RepoPathPrefix: "fixture-repos", HiddenBlobs: blobs, GoldBlobs: blobs})
		if err != nil {
			t.Fatal(err)
		}
		commits = append(commits, task.Repo.Commit)
		raw, _ := json.Marshal(task)
		tasks = append(tasks, string(raw))
	}
	if commits[0] != commits[1] || tasks[0] != tasks[1] {
		t.Fatalf("two builds of one fixture differ:\n%s\n%s", tasks[0], tasks[1])
	}
}

func TestFromFixtureRefusesWhatIsNotAFixture(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(spec map[string]any)
		after  func(t *testing.T, dir string)
		want   string
	}{
		{name: "no verifier", mutate: func(s map[string]any) { s["verify"] = "" }, want: "verify is required"},
		{name: "unknown kind", mutate: func(s map[string]any) { s["kind"] = "chore" }, want: "kind"},
		{name: "unknown difficulty", mutate: func(s map[string]any) { s["difficulty"] = "impossible" }, want: "difficulty"},
		{name: "a misspelt key", mutate: func(s map[string]any) { s["verfy"] = "x" }, want: "unknown field"},
		{name: "an id that is not the directory", mutate: func(s map[string]any) { s["id"] = "other" }, want: "differs from the directory"},
		{name: "an id with a slash", mutate: func(s map[string]any) { s["id"] = "a/b" }, want: "letters, digits"},
		{name: "a team of nobody", mutate: func(s map[string]any) { s["team"] = map[string]any{"mode": "crowd"} }, want: "team.mode"},
		{name: "a solution that changes nothing", after: func(t *testing.T, dir string) {
			b, _ := os.ReadFile(filepath.Join(dir, "start", "lib.sh"))
			_ = os.WriteFile(filepath.Join(dir, "solution", "lib.sh"), b, 0o644)
			_ = os.Remove(filepath.Join(dir, "solution", "NOTE.md"))
		}, want: "the solution is the start"},
		{name: "a symlink in the start", after: func(t *testing.T, dir string) {
			if err := os.Symlink("/etc/passwd", filepath.Join(dir, "start", "link")); err != nil {
				t.Skip(err)
			}
		}, want: "not a regular file"},
		{name: "no hidden files", after: func(t *testing.T, dir string) {
			_ = os.RemoveAll(filepath.Join(dir, "hidden"))
			_ = os.Mkdir(filepath.Join(dir, "hidden"), 0o755)
		}, want: "must each hold files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := writeFixture(t, root, "demo-add", tc.mutate)
			if tc.after != nil {
				tc.after(t, dir)
			}
			_, err := FromFixture(context.Background(), dir, FixtureOptions{RepoRoot: t.TempDir(), HiddenBlobs: fixtureBlobs(t)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// The task is sound in the environment rollouts use: it fails at the start and passes with the reference solution.
func TestAFixtureTaskIsSoundInTheRolloutEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the verifier")
	}
	dir := writeFixture(t, t.TempDir(), "demo-add", nil)
	blobs := fixtureBlobs(t)
	task, err := FromFixture(context.Background(), dir, FixtureOptions{RepoRoot: filepath.Join(t.TempDir(), "repos"), HiddenBlobs: blobs, GoldBlobs: blobs})
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	_ = json.Unmarshal(task.Meta, &meta)
	gold, err := blobs.Get(core.Hash(meta.GoldBlob))
	if err != nil {
		t.Fatal(err)
	}
	ws := newManager(t)
	if _, err := env.CheckTask(context.Background(), *task, gold, env.VerifyOptions{Workspaces: ws, HiddenBlobs: blobs}); err != nil {
		t.Fatalf("the fixture task is unsound: %v", err)
	}
}
