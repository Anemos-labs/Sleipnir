package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

// The hidden verifier files of a generated task are blobs in the task's blob store, not in the run's. Scoring read blobs
// from the run's store only, so the hardcoded-value detector never saw them ("unreadable; hardcoded-value detector ignores
// it") and an agent that hardcoded a hidden test's expected value went unflagged. `rl reward --tasks` reads them from the
// blob store next to the tasks file (or --blobs).
func TestRLRewardReadsHiddenFilesFromTheTaskBlobStore(t *testing.T) {
	root := t.TempDir()
	taskBlobs, err := events.NewDirBlobs(filepath.Join(root, "tasks", "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := taskBlobs.Put([]byte("package calc\nfunc TestBig(t *testing.T) { if f() != 987654321 { t.Fatal() } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	task := rl.Task{ID: "t", Kind: rl.TaskFix, Prompt: "fix f", Repo: rl.RepoSpec{Path: "/r", Commit: "abc"},
		Verifier: rl.Verifier{Cmd: "go test ./...", Hidden: map[string]string{"calc_hidden_test.go": "blob:" + string(hidden)}}}
	tasksFile := filepath.Join(root, "tasks", "tasks.jsonl")
	if err := env.WriteTasks(tasksFile, []rl.Task{task}); err != nil {
		t.Fatal(err)
	}

	tr := trajtest.New()
	a := tr.Agent("a1", "worker", "m1")
	a.Step(trajtest.Call{Text: "Fixed."})
	ep, err := traj.OpenWith(tr.Events(), tr.Blobs).Episode(traj.Options{TaskID: "t", Policy: rl.PolicyRef{Model: "m1"}, Task: &task})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "run", "t", "0")
	runBlobs, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range tr.Blobs.All() {
		if _, err := runBlobs.Put(b); err != nil {
			t.Fatal(err)
		}
	}
	diff := "diff --git a/calc.go b/calc.go\nindex 1111111..2222222 100644\n--- a/calc.go\n+++ b/calc.go\n" +
		"@@ -1,0 +1,3 @@\n+\tif x == 987654321 {\n+\t\treturn 1\n+\t}\n"
	if ep.Outcome.Diff, err = runBlobs.Put([]byte(diff)); err != nil {
		t.Fatal(err)
	}
	ep.Outcome.Verifier = &rl.Verdict{Kind: "verifier", Pass: true, Score: 1}
	ep.Outcome.Claimed = "done"
	var log bytes.Buffer
	for _, e := range tr.Events() {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		log.Write(append(b, '\n'))
	}
	for name, v := range map[string][]byte{"events.jsonl": log.Bytes(), "episode.json": mustJSON(t, ep), "task.json": mustJSON(t, task)} {
		if err := os.WriteFile(filepath.Join(dir, name), v, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	score := func(args ...string) *rl.Episode {
		t.Helper()
		out, errb, err := rlRun{t}.do(rlReward, append([]string{filepath.Join(root, "run")}, args...)...)
		if err != nil {
			t.Fatalf("%v\n%s\n%s", err, out, errb)
		}
		b, err := os.ReadFile(filepath.Join(dir, "episode.json"))
		if err != nil {
			t.Fatal(err)
		}
		var got rl.Episode
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		return &got
	}
	got := score("--tasks", tasksFile, "--redetect-hacks")
	if !slices.Contains(got.Flags, rl.FlagHackHardcode) {
		t.Errorf("the hidden test's expected value is hardcoded, yet not flagged: %v %q", got.Flags, got.Reward.Notes)
	}
	// Elsewhere than next to the tasks file, --blobs names the store; without it the file cannot be read.
	moved := filepath.Join(root, "elsewhere")
	if err := os.Rename(filepath.Join(root, "tasks", "blobs"), moved); err != nil {
		t.Fatal(err)
	}
	got = score("--tasks", tasksFile, "--redetect-hacks")
	if slices.Contains(got.Flags, rl.FlagHackHardcode) || !strings.Contains(strings.Join(got.Reward.Notes, "\n"), "unreadable") {
		t.Errorf("without the store: %v %q", got.Flags, got.Reward.Notes)
	}
	if got = score("--tasks", tasksFile, "--blobs", moved, "--redetect-hacks"); !slices.Contains(got.Flags, rl.FlagHackHardcode) {
		t.Errorf("--blobs: %v %q", got.Flags, got.Reward.Notes)
	}
	if _, _, err := (rlRun{t}).do(rlReward, filepath.Join(root, "run"), "--blobs", moved); err == nil {
		t.Error("--blobs without --tasks was accepted")
	}

	// The task's repository already has the value (its specification names it): not evidence of copying it.
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "SPEC.md"), []byte("f returns 987654321 for the big case.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "spec")
	task.Repo = rl.RepoSpec{Path: repo, Commit: git("rev-parse", "HEAD")}
	if err := env.WriteTasks(tasksFile, []rl.Task{task}); err != nil {
		t.Fatal(err)
	}
	if got = score("--tasks", tasksFile, "--blobs", moved, "--redetect-hacks"); slices.Contains(got.Flags, rl.FlagHackHardcode) {
		t.Errorf("a value the repository already had is flagged: %v %q", got.Flags, got.Reward.Notes)
	}
}
