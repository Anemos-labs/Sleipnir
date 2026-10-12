package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/env/taskgen"
)

// `rl tasks update-scorer FILE -o FILE` updates the tasks file in place, and a write that fails leaves it whole: the
// output is written next to its place and renamed there, never truncated first.
func TestRLTasksUpdateScorerInPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tasks")
	blobs, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	task := rl.Task{ID: "suite-1", Kind: rl.TaskFeature, Prompt: "build it", Repo: rl.RepoSpec{Path: "/r", Commit: "abc"}}
	if err := taskgen.WriteSuite(&task, taskgen.SuiteSpec{Commands: []taskgen.SuiteCommand{taskgen.GoSuiteCommand("")}}, blobs); err != nil {
		t.Fatal(err)
	}
	current := task.Verifier.Hidden[".sleipnir/suite/score.py"]
	stale, err := blobs.Put([]byte("print('an older scorer')\n"))
	if err != nil {
		t.Fatal(err)
	}
	task.Verifier.Hidden[".sleipnir/suite/score.py"] = "blob:" + string(stale)
	file := filepath.Join(dir, "tasks.jsonl")
	if err := env.WriteTasks(file, []rl.Task{task}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	if os.Geteuid() != 0 { // root writes into a read-only directory anyway
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		_, _, err := rlRun{t}.do(rlTasks, "update-scorer", file, "-o", file)
		if cerr := os.Chmod(dir, 0o755); cerr != nil {
			t.Fatal(cerr)
		}
		if err == nil {
			t.Fatal("writing into a read-only directory succeeded")
		}
		if now, rerr := os.ReadFile(file); rerr != nil || !bytes.Equal(now, before) {
			t.Fatalf("a failed in-place update damaged the tasks file: %v", rerr)
		}
	}

	if _, errb, err := (rlRun{t}).do(rlTasks, "update-scorer", file, "-o", file); err != nil || !strings.Contains(errb, "1 updated") {
		t.Fatalf("update-scorer: %v %s", err, errb)
	}
	tasks, err := env.LoadTasks(file)
	if err != nil || len(tasks) != 1 || tasks[0].Verifier.Hidden[".sleipnir/suite/score.py"] != current {
		t.Fatalf("the tasks file after the update: %+v %v", tasks, err)
	}
}
