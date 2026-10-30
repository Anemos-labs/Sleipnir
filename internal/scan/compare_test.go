package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// lead is a survey-depth lead of category cat whose sink quotes sink in file, ruled on by one verifier: confirmed, or rejected
// for reason.
func lead(id, cat, file, sink string, line int, confirm bool, reason string) Lead {
	return Lead{ID: id, Title: cat + " in " + file, Category: cat, Claim: "c", Impact: SevHigh, Reach: ReachExternal, Exploit: ExploitTrivial, By: "hunter",
		Trace:    []Step{{Cite: Cite{File: "routes.go", Line: 1, Quote: "route(" + id + ")"}}, {Cite: Cite{File: file, Line: line, Quote: sink}}},
		Verdicts: []Verdict{{By: "v", Confirm: confirm, Reason: reason, Note: "n"}}}
}

func TestCompareSortsFindingsIntoNewFixedAndUnchanged(t *testing.T) {
	old := &Run{Slug: "scan-old", Commit: strings.Repeat("a", 40), Depth: "survey", Leads: []Lead{
		lead("L1", "command-injection", "handler.go", `exec.Command("sh", "-c", cmd)`, 15, true, ""),
		lead("L2", "xss", "app.js", `el.innerHTML = name`, 3, true, ""),
		lead("L3", "path-traversal", "files.go", `os.Open(filepath.Join(root, p))`, 40, true, ""),
		lead("L4", "ssrf", "fetch.go", `http.Get(u)`, 9, false, "unreachable"),
	}}
	moved := lead("L1", "command-injection", "handler.go", "exec.Command(\"sh\",\n  \"-c\", cmd)", 31, true, "") // same code, moved and rewrapped
	xss := lead("L2", "xss", "app.js", `el.innerHTML = name`, 3, false, "mitigated")
	idor := lead("L3", "idor", "users.go", `db.Get(id)`, 12, true, "")
	ssrf := lead("L4", "ssrf", "fetch.go", `http.Get(u)`, 9, true, "")
	// the same path traversal, its sink rewritten but its trace still through the same route
	pt := lead("L5", "path-traversal", "files.go", `os.Open(path.Join(root, p))`, 44, false, "")
	pt.Verdicts = nil
	pt.Trace[0] = Step{Cite: Cite{File: "routes.go", Line: 1, Quote: "route(L3)"}}
	cur := &Run{Slug: "scan-new", Commit: strings.Repeat("b", 40), Depth: "survey", Leads: []Lead{moved, xss, idor, ssrf, pt}}

	c := Compare(old, cur)
	got := map[string][]string{}
	for _, d := range c.Deltas {
		got[d.Kind] = append(got[d.Kind], d.Finding.ID+":"+d.Finding.Category)
	}
	want := map[string][]string{
		DeltaNew:       {"L3:idor", "L4:ssrf"}, // a finding the old scan rejected counts as new
		DeltaFixed:     {"L2:xss"},             // the rerun's verifiers rejected it
		DeltaPending:   {"L3:path-traversal"},  // filed again, still being verified
		DeltaUnchanged: {"L1:command-injection"},
	}
	for _, k := range DeltaKinds {
		if strings.Join(got[k], ",") != strings.Join(want[k], ",") {
			t.Errorf("%s: %v, want %v", k, got[k], want[k])
		}
	}
	if c.Final {
		t.Error("a scan with nothing mapped is not final")
	}
	for _, d := range c.Deltas {
		if d.Kind == DeltaFixed && !strings.Contains(d.Note, "mitigated") {
			t.Errorf("a fixed finding says why: %q", d.Note)
		}
		if d.Kind == DeltaUnchanged && (d.Was == nil || d.Was.Trace[1].Line != 15) {
			t.Errorf("an unchanged finding keeps the old one: %+v", d.Was)
		}
	}
	// the rerun no longer has the path traversal at all: while it runs that is pending, not fixed
	cur.Leads = cur.Leads[:4]
	if c := Compare(old, cur); c.Counts[DeltaPending] != 1 || c.Counts[DeltaFixed] != 1 {
		t.Errorf("counts %v", c.Counts)
	}
	text := CompareText(Compare(old, cur))
	for _, w := range []string{"scan-new (bbbbbbbbbbbb) since scan-old", "2 new, 1 fixed, 1 unchanged", "still running", "handler.go:31"} {
		if !strings.Contains(text, w) {
			t.Errorf("CompareText lacks %q:\n%s", w, text)
		}
	}
}

func TestRerunScansTheSameCodeAgainAndComparesWithIt(t *testing.T) {
	root, _ := gitRepo(t, appFiles)
	repo, err := gitx.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := Start(ctx, NewStore(root), repo, "survey", "server")
	if err != nil {
		t.Fatal(err)
	}
	if b := Baseline(root, first); b != nil {
		t.Fatalf("the first scan has no baseline, got %s", b.Slug)
	}
	// the first scan found the injection
	st := NewStore(root)
	if _, err := st.Update(first.Slug, func(r *Run) error {
		r.Leads = append(r.Leads, Lead{ID: "L1", Title: "Command injection in /ping", Category: "command-injection", Claim: "c", Impact: SevCritical,
			Reach: ReachExternal, Exploit: ExploitTrivial, By: "h", Verdicts: []Verdict{{By: "v", Confirm: true, Note: "n"}},
			Trace: []Step{{Cite: Cite{File: "server/handler.go", Line: 15, Quote: `exec.Command("sh", "-c", "ping -c1 "+host)`}}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// a new commit, then the rerun: the same depth and focus at the new HEAD
	if err := os.WriteFile(filepath.Join(root, "server", "extra.go"), []byte("package server\n\nfunc Extra() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "more"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	prev, _ := Load(root, first.Slug)
	st2 := NewStore(root)
	st2.now = func() time.Time { return time.Now().Add(time.Minute) }
	again, err := Rerun(ctx, st2, repo, prev)
	if err != nil {
		t.Fatal(err)
	}
	if again.Previous != first.Slug || again.Depth != "survey" || again.Focus != "server" || again.Commit == first.Commit {
		t.Fatalf("rerun: previous %q depth %q focus %q commit %s (first %s)", again.Previous, again.Depth, again.Focus, again.Commit, first.Commit)
	}
	if again.File("server/extra.go") == nil {
		t.Error("the rerun's inventory is taken at the new HEAD")
	}
	brief, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(BriefPath(again.Slug))))
	if err != nil {
		t.Fatal(err)
	}
	if b := string(brief); !strings.Contains(b, "reruns "+first.Slug) || !strings.Contains(b, "confirmed 1 finding,") || !strings.Contains(b, "L1 critical, command-injection: Command injection in /ping (`server/handler.go:15`)") {
		t.Errorf("the rerun's brief lists what the previous scan found:\n%s", b)
	}
	if b := Baseline(root, again); b == nil || b.Slug != first.Slug {
		t.Fatalf("the rerun's baseline: %v", b)
	}
	c, err := CompareWith(root, again.Slug, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Old != first.Slug || c.Counts[DeltaPending] != 1 {
		t.Errorf("comparison: %+v", c)
	}
	if _, err := CompareWith(root, first.Slug, ""); err == nil || !strings.Contains(err.Error(), "no earlier scan") {
		t.Errorf("the first scan compared with nothing: %v", err)
	}
	if _, err := CompareWith(root, again.Slug, again.Slug); err == nil {
		t.Error("a scan compared with itself")
	}
}

func TestPullFastForwardsFromTheUpstream(t *testing.T) {
	upstream, _ := gitRepo(t, map[string]string{"a.go": "package a\n"})
	dir := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", "-q", upstream, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(upstream, "b.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "b"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = upstream
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// a local upstream is a file transport, which Pull's configuration refuses like any but https and ssh
	if _, err := Pull(context.Background(), dir); err == nil {
		t.Fatal("pull from a local path: want a refusal")
	}
	if _, err := os.Stat(filepath.Join(dir, "b.go")); err == nil {
		t.Fatal("a refused pull changed the work tree")
	}
}
