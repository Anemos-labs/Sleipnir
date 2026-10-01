package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

// `rl reward` rescores rollouts that are already on disk, which is how a scoring fix reaches data that was scored wrongly
// (a benchmark flagged correct, passing rollouts "hack:outside_worktree" because its workspace lived below ~/.something).
// Rescoring must therefore take the workspace from the run's own log, as scoring during the rollout does.
func TestRLRewardRescoresWithTheWorkspaceTheLogRecorded(t *testing.T) {
	const tree = "/root/.bench/work/ws/t-s0-abc/tree"
	tr := trajtest.New()
	tr.Emit("a1", events.TypeSessionStart, map[string]any{"cwd": tree, "root": tree})
	a := tr.Agent("a1", "worker", "m1")
	a.Step(trajtest.Call{Text: "fix", Tool: "edit", Input: `{"path":"` + tree + `/pkg/a.go","old_string":"a - b","new_string":"a + b"}`, Result: "edited"})
	a.Step(trajtest.Call{Text: "Fixed."})

	task := rl.Task{ID: "t", Kind: rl.TaskFix, Prompt: "fix Add", Verifier: rl.Verifier{Cmd: "go test ./..."}}
	ep, err := traj.OpenWith(tr.Events(), tr.Blobs).Episode(traj.Options{TaskID: "t", Policy: rl.PolicyRef{Model: "m1"}, Task: &task})
	if err != nil {
		t.Fatal(err)
	}
	ep.Outcome.Verifier = &rl.Verdict{Kind: "verifier", Pass: true, Score: 1}
	ep.Outcome.Claimed = "done"
	ep.Flags = []string{rl.FlagHackEscape} // what the scorer used to store

	dir := filepath.Join(t.TempDir(), "run", "t", "0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	for _, e := range tr.Events() {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		log.Write(append(b, '\n'))
	}
	blobs, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range tr.Blobs.All() {
		if _, err := blobs.Put(b); err != nil {
			t.Fatal(err)
		}
	}
	for name, v := range map[string][]byte{"events.jsonl": log.Bytes(), "episode.json": mustJSON(t, ep), "task.json": mustJSON(t, task)} {
		if err := os.WriteFile(filepath.Join(dir, name), v, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	flags := func(args ...string) (flags []string, out string) {
		t.Helper()
		out, errb, err := rlRun{t}.do(rlReward, append([]string{filepath.Dir(filepath.Dir(dir))}, args...)...)
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
		return got.Flags, out
	}
	// Scoring never removes a flag by itself (a flag may come from somewhere else), so a plain rescore keeps it ...
	if got, _ := flags(); !slices.Contains(got, rl.FlagHackEscape) {
		t.Errorf("a plain rescore must keep the stored flag, got %v", got)
	}
	// ... and --redetect-hacks forgets what the detectors stored and runs them again, now knowing where the run worked.
	if got, out := flags("--redetect-hacks"); slices.Contains(got, rl.FlagHackEscape) {
		t.Errorf("the edit is inside the workspace the log recorded, yet it is still flagged: %v\n%s", got, out)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
