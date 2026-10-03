package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

func TestRLTasksCheckCompositeReferenceSolutions(t *testing.T) {
	fixtures, out := t.TempDir(), t.TempDir()
	writeCLIFixture(t, fixtures, "pair")
	// Both files must be fixed. A partial solution must fail admission.
	for name, body := range map[string]string{
		"start/value": "wrong\n", "solution/value": "right\n",
		"hidden/check.sh": ". ./lib.sh\n[ \"$(add 2 3)\" = 5 ] && [ \"$(cat value)\" = right ]\n",
	} {
		if err := os.WriteFile(filepath.Join(fixtures, "pair", filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := rlRun{t}
	file := filepath.Join(out, "tasks.jsonl")
	r.must(rlTaskgen, "fixture", "--dir", fixtures, "-o", file)
	tasks, err := env.LoadTasks(file)
	if err != nil {
		t.Fatal(err)
	}
	store, err := events.NewDirBlobs(filepath.Join(out, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		GoldBlob string `json:"gold_blob"`
	}
	if err := json.Unmarshal(tasks[0].Meta, &original); err != nil {
		t.Fatal(err)
	}
	patch, err := store.Get(core.Hash(original.GoldBlob))
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.Index(patch, []byte("\ndiff --git "))
	if cut < 0 {
		t.Fatalf("reference solution must contain two file patches: %s", patch)
	}
	var hashes []string
	for _, part := range [][]byte{patch[:cut+1], patch[cut+1:]} {
		h, err := store.Put(part)
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, string(h))
	}
	meta := func(hashes []string) json.RawMessage {
		b, err := json.Marshal(map[string]any{"gold_blobs": hashes})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	tasks[0].Kind, tasks[0].Team = rl.TaskSwarm, rl.Team{Mode: "swarm", Agents: 2}
	t.Chdir(out)
	for _, tc := range []struct {
		name   string
		meta   json.RawMessage
		ok     bool
		skip   bool
		reason string
	}{
		{"complete", meta(hashes), true, false, ""},
		{"incomplete", meta(hashes[:1]), false, false, "reference solution"},
		{"missing blob", meta([]string{hashes[0], strings.Repeat("a", 64)}), false, false, "reference solution"},
		{"empty hash", meta([]string{hashes[0], ""}), false, false, "reference solution"},
		{"invalid metadata", json.RawMessage(`{"gold_blobs":42}`), false, false, "metadata"},
		{"unrecorded", nil, false, true, "no reference solution recorded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := tasks[0]
			task.Meta = tc.meta
			if err := env.WriteTasks(file, []rl.Task{task}); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(t.TempDir(), "report.json")
			stdout, stderr, err := (rlRun{t}).do(rlTasks, "check", file, "--work-dir", filepath.Join(t.TempDir(), "work"),
				"--no-net-isolation", "--verify-repeats", "2", "--report", report)
			if (err == nil) != (tc.ok || tc.skip) {
				t.Fatalf("err=%v\nstdout: %s\nstderr: %s", err, stdout, stderr)
			}
			var verdicts []struct {
				ID      string
				OK      bool
				Skipped bool
				Reason  string
			}
			b, err := os.ReadFile(report)
			if err != nil || json.Unmarshal(b, &verdicts) != nil || len(verdicts) != 1 {
				t.Fatalf("report: %v %s", err, b)
			}
			v := verdicts[0]
			if v.ID != task.ID || v.OK != tc.ok || v.Skipped != tc.skip || !strings.Contains(v.Reason, tc.reason) {
				t.Fatalf("verdict: %+v", v)
			}
		})
	}
}
