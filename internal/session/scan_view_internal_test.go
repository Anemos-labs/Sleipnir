package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/scan"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// The Deep scan view's calls read the scans of the project: the list with its numbers, one scan in full, and its report in
// each export format. A lead still being verified is neither written up nor fixed, and a name that is not a scan is refused.
func TestScanViewReadsTheProjectsScans(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package server\n\nimport \"os/exec\"\n\nfunc Ping(host string) error {\n\tif host == \"\" {\n\t\treturn nil\n\t}\n\treturn exec.Command(\"sh\", \"-c\", \"ping \"+host).Run()\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "server", "ping.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	s := &Session{opts: Options{Root: dir, Cwd: dir}}
	s.newScanTool()
	repo, err := s.scanRepo()
	if err != nil {
		t.Fatal(err)
	}
	r, err := scan.Start(context.Background(), s.scan.store, repo, "standard", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.scan.store.Update(r.Slug, func(r *scan.Run) error {
		r.Leads = append(r.Leads, scan.Lead{ID: "L1", Title: "Command injection in Ping", Category: "command-injection", Impact: "critical", Reach: "external", Exploit: "trivial", By: "hunter"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runs := s.ScanRuns()
	if len(runs) != 1 || runs[0].Slug != r.Slug || runs[0].Files != 1 || runs[0].Leads != 1 || runs[0].Team != 8 || !runs[0].Active {
		t.Fatalf("runs: %+v", runs)
	}
	d, err := s.ScanDetail(r.Slug)
	if err != nil || d["phase"] != scan.PhaseMap || len(d["findings"].([]scan.Finding)) != 1 {
		t.Fatalf("detail: %v %+v", err, d)
	}
	for format, want := range map[string]string{"json": `"findings"`, "csv": "id,status,severity", "html": "<!doctype html>", "md": "# Security assessment", "sarif": `"2.1.0"`} {
		data, slug, err := s.ScanExport(r.Slug, format)
		if err != nil || slug != r.Slug || !strings.Contains(string(data), want) {
			t.Errorf("export %s: %v %q", format, err, data)
		}
	}
	if _, _, err := s.ScanIssue(r.Slug, "L1"); err == nil || !strings.Contains(err.Error(), "still being verified") {
		t.Errorf("an open lead was written up: %v", err)
	}
	// a session started on the scan's goal (from the page or a shell) works on that scan
	s.scan.store = scan.NewStore(dir)
	s.noteObjective(&goal.State{Objective: scan.Objective(r, "")})
	if got := s.scan.store.Active(); got != r.Slug {
		t.Errorf("the goal's scan is not the session's: %q", got)
	}
	if _, err := s.ScanDetail("../../etc"); err == nil {
		t.Error("a path was read as a scan")
	}
}

// The view shows the session's own team on the scan it runs, and not once that scan is done although it stays selected.
func TestScanViewShowsTheSessionsTeamWhileTheScanRuns(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	s := &Session{opts: Options{Root: dir, Cwd: dir}}
	s.newScanTool()
	repo, err := s.scanRepo()
	if err != nil {
		t.Fatal(err)
	}
	r, err := scan.Start(context.Background(), s.scan.store, repo, "standard", "")
	if err != nil {
		t.Fatal(err)
	}
	s.Swarm = swarm.New(swarm.Config{MaxWorkers: 24}, swarm.Deps{}, nil)
	workers := func() (int, any) {
		d, err := s.ScanDetail(r.Slug)
		if err != nil {
			t.Fatal(err)
		}
		return s.ScanRuns()[0].Workers, d["workers"]
	}
	if run, det := workers(); run != 24 || det != 24 {
		t.Fatalf("a running scan shows a team of %d / %v, want the session's 24", run, det)
	}
	if _, err := s.scan.store.Update(r.Slug, func(r *scan.Run) error {
		for i := range r.Shards {
			r.Shards[i].Mapped, r.Shards[i].Hunted = "read", "checked"
		}
		r.Reported = &scan.Mark{Version: r.Version + 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.ScanDetail(r.Slug); d["phase"] != scan.PhaseDone {
		t.Fatalf("the scan is not done: %v", d["phase"])
	}
	if run, det := workers(); run != 0 || det != 0 {
		t.Fatalf("a done scan shows the session's team: %d / %v", run, det)
	}
}

// A goal that drives a scan is judged from the scan's ledger: it goes on with the ledger's problems while there are any, the
// harness writes the report when only the report is missing, and the goal is met, every milestone with it, once the ledger is done.
func TestAScanGoalIsJudgedFromItsLedger(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	s := &Session{opts: Options{Root: dir, Cwd: dir}}
	s.newScanTool()
	repo, err := s.scanRepo()
	if err != nil {
		t.Fatal(err)
	}
	r, err := scan.Start(context.Background(), s.scan.store, repo, "standard", "")
	if err != nil {
		t.Fatal(err)
	}
	g := &goal.State{Objective: scan.Objective(r, ""), Milestones: []goal.Milestone{{ID: "M1", Text: "Map"}, {ID: "M2", Text: "Report"}}}
	if _, ok := s.scanJudgment(&goal.State{Objective: "build a todo app"}); ok {
		t.Fatal("a goal that is not a scan was judged from a ledger")
	}
	v, ok := s.scanJudgment(g)
	if !ok || v.Kind != goal.Continue || !strings.Contains(v.Reason, "map phase") || len(v.Left) == 0 || !strings.Contains(v.Left[0], "S1 is not mapped") {
		t.Fatalf("an unmapped scan: %v %+v", ok, v)
	}
	if _, err := s.scan.store.Update(r.Slug, func(r *scan.Run) error {
		for i := range r.Shards {
			r.Shards[i].Mapped, r.Shards[i].Hunted = "read", "checked"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v, ok = s.scanJudgment(g)
	if !ok || v.Kind != goal.Done || g.MetCount() != 2 || !strings.Contains(v.Reason, "ledger is done") {
		t.Fatalf("a scan with only its report missing: %+v, %d milestones met", v, g.MetCount())
	}
	if _, err := os.Stat(filepath.Join(scan.RunDir(dir, r.Slug), scan.ReportFile)); err != nil {
		t.Errorf("the report was not written: %v", err)
	}
}
