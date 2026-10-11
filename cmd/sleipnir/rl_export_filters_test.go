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
	"github.com/anemos-labs/sleipnir/internal/rl/export"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

const exportDSModel = "deepseek/deepseek-v4.1-flash"

// writeExportRollout records a solo deepseek rollout of task "shop" in dir the way
// rl rollout lays one out (events.jsonl, blobs/, episode.json, task.json), as a run
// made before episodes carried their task's tags: the tags are in task.json only.
func writeExportRollout(t *testing.T, dir string, sample int, tags []string, calls ...trajtest.Call) {
	t.Helper()
	tr := trajtest.New()
	a := tr.Agent("solo", "worker", exportDSModel)
	a.User("work on the shop")
	for _, c := range calls {
		a.Step(c)
	}
	tr.Outcome("verifier", true, 1)
	ep, err := traj.OpenWith(tr.Events(), tr.Blobs).Episode(traj.Options{TaskID: "shop", Sample: sample, Policy: rl.PolicyRef{Model: exportDSModel}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ep.Env.Tags) != 0 {
		t.Fatalf("the fixture must look like an older run, without tags: %v", ep.Env.Tags)
	}
	ep.Reward = rl.Reward{Total: 1}
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
	task := rl.Task{ID: "shop", Tags: tags}
	for name, v := range map[string][]byte{"events.jsonl": log.Bytes(), "episode.json": mustJSON(t, ep), "task.json": mustJSON(t, task)} {
		if err := os.WriteFile(filepath.Join(dir, name), v, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRLExportDropRulesThroughTheCLI drives the export filters from the command
// line over a directory of runs laid out like a campaign: runs/<model>/<job>/<task>/<sample>.
func TestRLExportDropRulesThroughTheCLI(t *testing.T) {
	base := t.TempDir()
	model := filepath.Join(base, "runs", "deepseek_deepseek-v4.1-flash")
	fe := filepath.Join(model, "frontend_correct")
	be := filepath.Join(model, "backend")
	// frontend_correct/shop/0: a style sheet, then Go code; task.json tags it ui.
	writeExportRollout(t, filepath.Join(fe, "shop", "0"), 0, []string{"ui"},
		trajtest.Call{Text: "theme", Tool: "write", Input: `{"path":"web/site.css","content":"body{}"}`, Result: "ok"},
		trajtest.Call{Text: "handler", Tool: "edit", Input: `{"path":"shop/h.go","old_string":"a","new_string":"b"}`, Result: "ok"},
		trajtest.Call{Text: "done"})
	// backend/shop/0 and backend/shop/1: Go code only, the second one long.
	writeExportRollout(t, filepath.Join(be, "shop", "0"), 0, []string{"go"},
		trajtest.Call{Text: "fix", Tool: "edit", Input: `{"path":"api/a.go","old_string":"a","new_string":"b"}`, Result: "ok"},
		trajtest.Call{Text: "done"})
	writeExportRollout(t, filepath.Join(be, "shop", "1"), 1, []string{"go"},
		trajtest.Call{Text: "fix", Tool: "edit", Input: `{"path":"api/b.go","old_string":"a","new_string":"b"}`, Result: "ok", Usage: &core.Usage{InputTokens: 5000, OutputTokens: 100}},
		trajtest.Call{Text: "done", Usage: &core.Usage{InputTokens: 5200, OutputTokens: 10}})

	r := rlRun{t}
	exp := func(args ...string) (records int, st export.Stats, out string) {
		t.Helper()
		dest := filepath.Join(t.TempDir(), "out.jsonl")
		stats := filepath.Join(t.TempDir(), "stats.json")
		out = r.must(rlExport, append([]string{model, "--format", "steps", "--keep-flat", "-o", dest, "--stats", stats}, args...)...)
		b, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		records = strings.Count(string(b), "\n")
		sb, err := os.ReadFile(stats)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(sb, &st); err != nil {
			t.Fatal(err)
		}
		return records, st, out
	}

	if n, _, _ := exp(); n != 7 {
		t.Fatalf("without filters: %d records, want the 7 steps", n)
	}

	// ds-frontend: the ui tag comes from the rollout's task.json and takes the whole
	// frontend rollout; the backend rollouts stay.
	n, st, out := exp("--drop", "ds-frontend")
	if n != 4 || st.Drops["step:rule:ds-frontend"] != 3 {
		t.Errorf("--drop ds-frontend: %d records, drops %v\n%s", n, st.Drops, out)
	}
	if !strings.Contains(out, "step:rule:ds-frontend=3") {
		t.Errorf("the drop is not reported:\n%s", out)
	}

	// --tasks names the task's tags (go): only the style sheet write is front-end work.
	tasks := filepath.Join(base, "tasks.jsonl")
	if err := env.WriteTasks(tasks, []rl.Task{{ID: "shop", Kind: rl.TaskFix, Repo: rl.RepoSpec{Path: base, Commit: "abc123"}, Prompt: "p", Verifier: rl.Verifier{Cmd: "true"}, Tags: []string{"go"}}}); err != nil {
		t.Fatal(err)
	}
	if n, st, out := exp("--drop", "ds-frontend", "--tasks", tasks); n != 6 || st.Drops["step:rule:ds-frontend"] != 1 {
		t.Errorf("--tasks: %d records, drops %v\n%s", n, st.Drops, out)
	}

	// A custom rule, as the command line spells it.
	if n, st, _ := exp("--drop", "name=css;edits-ui;models=*deepseek*"); n != 6 || st.Drops["step:rule:css"] != 1 {
		t.Errorf("custom rule: %d records, drops %v", n, st.Drops)
	}

	// --exclude, in every accepted spelling, drops the whole rollout.
	for _, entry := range []string{
		"deepseek_deepseek-v4.1-flash/backend/shop/1", // from the parent of the path given
		"backend/shop/1",                // from the path given; also <run dir>/<episode id>
		filepath.Join(be, "shop", "1"),  // the sample directory itself
		"./backend/shop/1/episode.json", // tidied
	} {
		list := filepath.Join(t.TempDir(), "leaked.txt")
		if err := os.WriteFile(list, []byte("# leaked rollouts\n\n"+entry+"\nno/such/rollout/9\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		n, st, out := exp("--exclude", list)
		if n != 5 || st.Drops["episode:excluded"] != 1 {
			t.Errorf("--exclude %q: %d records, drops %v\n%s", entry, n, st.Drops, out)
		}
		if !strings.Contains(out, "1 --exclude entries match no rollout here, e.g. no/such/rollout/9") {
			t.Errorf("--exclude %q: the entry that matched nothing is not reported:\n%s", entry, out)
		}
	}
	// A bare episode id names the same task/sample in every job: from the directory
	// of runs it is not a rollout name and excludes nothing.
	list := filepath.Join(t.TempDir(), "ids.txt")
	if err := os.WriteFile(list, []byte("shop/0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := exp("--exclude", list); n != 7 {
		t.Errorf("a bare episode id excluded something: %d records", n)
	}

	// --role-teacher: deepseek is the policy, so its worker steps are trainable, and
	// still dropped when the worker may only learn from another model.
	if n, st, _ := exp("--role-teacher", "worker=claude-*,gpt-*"); n != 0 || st.Drops["step:role_teacher"] != 7 {
		t.Errorf("--role-teacher: %d records, drops %v", n, st.Drops)
	}
	if n, _, _ := exp("--role-teacher", "worker=*deepseek*"); n != 7 {
		t.Errorf("--role-teacher naming the model: %d records", n)
	}

	// --max-sample-tokens drops the long steps and says so per role.
	n, st, out = exp("--max-sample-tokens", "4000")
	if n != 5 || st.Drops["step:sample_too_long"] != 2 || st.DroppedByRole["worker"]["sample_too_long"] != 2 || st.CandidatesByRole["worker"] != 7 {
		t.Errorf("--max-sample-tokens: %d records, drops %v, by role %v, candidates %v", n, st.Drops, st.DroppedByRole, st.CandidatesByRole)
	}
	if !strings.Contains(out, "dropped for length, by role: worker=2/7 (28.6%)") {
		t.Errorf("length drops are not reported by role:\n%s", out)
	}

	// --max-drop-rate fails the export when too large a share went, and leaves no
	// output file behind; the statistics are still written.
	dest := filepath.Join(t.TempDir(), "out.jsonl")
	stats := filepath.Join(t.TempDir(), "stats.json")
	stdout, stderr, err := r.do(rlExport, model, "--format", "steps", "--keep-flat", "-o", dest, "--stats", stats, "--max-sample-tokens", "4000", "--max-drop-rate", "0.2")
	if err == nil || !strings.Contains(err.Error(), "worker: 2 of 7 steps (28.6%)") || !strings.Contains(err.Error(), "was not written") {
		t.Fatalf("--max-drop-rate 0.2: err %v\n%s%s", err, stdout, stderr)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Errorf("a failed export left %s behind: %v", dest, serr)
	}
	if _, serr := os.Stat(stats); serr != nil {
		t.Errorf("the statistics of a failed export are not written: %v", serr)
	}
	if _, _, err := r.do(rlExport, model, "--format", "steps", "--keep-flat", "-o", dest, "--max-sample-tokens", "4000", "--max-drop-rate", "0.3"); err != nil {
		t.Errorf("--max-drop-rate 0.3 allows 28.6%%: %v", err)
	}

	// Misuse is refused.
	for _, args := range [][]string{
		{"--drop", "frontend"},
		{"--drop", "name=all"},
		{"--role-teacher", "worker"},
		{"--max-drop-rate", "-0.1"},
		{"--exclude", filepath.Join(base, "missing.txt")},
	} {
		if _, _, err := r.do(rlExport, append([]string{model, "--format", "steps"}, args...)...); err == nil {
			t.Errorf("rl export %v was accepted", args)
		}
	}
}
