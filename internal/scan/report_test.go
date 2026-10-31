package scan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mockAgents are the names a team's agents go by: none of them may reach a report.
var mockAgents = []string{"be-1", "be-2", "fe-1", "fe-2", "dv-1", "rv-1", "rv-2", "ts-1", "sc-1", "mgr"}

// bigRun is a finished deep scan of a large codebase: 120 shards, about 1700 points of attack surface, and 160 leads in every
// status, ruled on and reproduced by a team whose agents have the usual short names.
func bigRun() *Run {
	r := &Run{Slug: "scan-20261031-0400-abcdef1", Name: "orchard", Commit: strings.Repeat("abcdef12", 5), Depth: "deep", Created: "2026-10-31T04:00:00Z", Version: 9000}
	dirs := []string{"api", "auth", "billing", "storage", "web/src", "worker", "cli", "internal/db", "internal/queue", "plugins"}
	cats := []string{"injection", "access-control", "idor", "path-traversal", "ssrf", "xss", "auth", "secrets", "dos", "info-leak", "crypto", "race"}
	for i := 0; i < 120; i++ {
		dir := dirs[i%len(dirs)]
		files := []string{fmt.Sprintf("%s/mod%02d.go", dir, i), fmt.Sprintf("%s/mod%02d_util.go", dir, i)}
		for _, f := range files {
			r.Inventory.Files = append(r.Inventory.Files, File{Path: f, Lang: "go", Lines: 380 + i*3, Funcs: 14, Complexity: 60})
		}
		r.Shards = append(r.Shards, Shard{ID: fmt.Sprintf("S%d", i+1), Files: files, Funcs: 28, Lines: 760, Complexity: 120,
			Mapped: "Handlers and their storage calls.", MappedBy: mockAgents[i%4], Hunted: "Followed every entry point to its sinks.", HuntedBy: mockAgents[(i+1)%4]})
	}
	for i := 0; i < 1700; i++ {
		f := r.Inventory.Files[(i*7)%len(r.Inventory.Files)]
		r.Points = append(r.Points, Point{ID: fmt.Sprintf("P%d", i+1), Lane: Lanes[i%len(Lanes)], Title: fmt.Sprintf("point %d", i+1),
			Cite: Cite{File: f.Path, Line: 10 + i%300, Quote: "x"}, Shard: r.ShardOf(f.Path), By: mockAgents[i%len(mockAgents)]})
	}
	for i := 0; i < 600; i++ {
		e := Edge{ID: fmt.Sprintf("E%d", i+1), From: fmt.Sprintf("P%d", i+1), To: fmt.Sprintf("P%d", i+2), Relation: "calls", By: mockAgents[i%3]}
		if i%3 > 0 {
			e.Cite = &Cite{File: "api/mod00.go", Line: 1, Quote: "x"}
		}
		r.Edges = append(r.Edges, e)
	}
	impacts := []string{SevCritical, SevHigh, SevHigh, SevMedium, SevMedium, SevMedium, SevLow, SevLow}
	for i := 0; i < 160; i++ {
		f := r.Inventory.Files[(i*5)%len(r.Inventory.Files)]
		filer := mockAgents[i%len(mockAgents)]
		other := func(k int) string { return mockAgents[(i+k)%len(mockAgents)] }
		l := Lead{ID: fmt.Sprintf("L%d", i+1), Title: fmt.Sprintf("%s through the %s handler (%d)", title(cats[i%len(cats)]), strings.Split(f.Path, "/")[0], i+1),
			Category: cats[i%len(cats)], Impact: impacts[i%len(impacts)], Reach: ReachExternal, Exploit: ExploitModerate, By: filer,
			Claim: "An unauthenticated caller sends a crafted request and the handler passes the value on unchecked, so the caller reads or changes data that belongs to another tenant.",
			Fix:   "Validate the value against the caller's tenant before using it, and reject anything else.",
			Trace: []Step{{Cite: Cite{File: "api/routes.go", Line: 12, Quote: `mux.HandleFunc("/v1/items", Items)`}, Note: "public route"},
				{Cite: Cite{File: f.Path, Line: 40 + i, Quote: `id := r.URL.Query().Get("id")`}, Note: "the id comes from the query"},
				{Cite: Cite{File: f.Path, Line: 52 + i, Quote: `db.Query("SELECT * FROM items WHERE id = " + id)`}, Note: "concatenated into the query"}}}
		if i%9 == 4 {
			l.Exploit = ExploitDifficult
		}
		yes := func(k int, role string) Verdict {
			return Verdict{By: other(k), Role: role, Confirm: true, Note: "Read every step at the commit; " + other(k) + " found no guard on the way."}
		}
		no := func(k int) Verdict {
			return Verdict{By: other(k), Role: RoleVerifier, Reason: "mitigated", Note: "The middleware set by " + filer + " checks the tenant first."}
		}
		switch {
		case i < 110: // confirmed, the most severe reproduced
			l.Verdicts = []Verdict{yes(1, RoleVerifier), yes(2, RoleVerifier), yes(3, RoleDefender)}
			l.Proofs = []Proof{{By: other(4), Outcome: ProofReproduced, Ready: true, Exercised: true, Script: "#!/bin/bash\ngo run ./cmd/repro # by " + other(4), Output: "SLEIPNIR-REPRODUCED"}}
			if i%5 == 0 {
				l.Proofs = nil
			}
		case i < 120: // disputed
			l.Verdicts = []Verdict{yes(1, RoleVerifier), yes(2, RoleVerifier), no(3), no(4), yes(5, RoleVerifier)}
		case i < 128: // still open
			l.Verdicts = []Verdict{yes(1, RoleVerifier)}
		case i < 140: // deferred
			l.Late, l.Deferred = true, "every shard is hunted and the 11 late leads the scan verifies are filed"
		default:
			l.Verdicts = []Verdict{no(1), no(2), no(3)}
		}
		r.Leads = append(r.Leads, l)
	}
	r.Reported = &Mark{Version: r.Version}
	return r
}

func TestTheReportNamesNoAgent(t *testing.T) {
	r := bigRun()
	fj, _ := json.Marshal(FindingsDoc(r))
	sj, _ := json.Marshal(SARIF(r))
	re := regexp.MustCompile(`\b(be|fe|dv|rv|ts|sc)-\d\b|\bmgr\b`)
	for name, text := range map[string]string{ReportFile: Markdown(r), HTMLFile: HTML(r), CSVFile: string(CSV(r)), FindingsFile: string(fj), SARIFFile: string(sj)} {
		if m := re.FindString(text); m != "" {
			t.Errorf("%s names the agent %q", name, m)
		}
	}
}

func TestTheReportIsADocument(t *testing.T) {
	r := bigRun()
	md := Markdown(r)
	for _, want := range []string{"# Security assessment", "## Summary", "| Critical | ", "### By category", "## Index", "## Findings",
		"[L1](#l1)", "**Verification.** Confirmed by 3 independent reviewers, one of them a defender", "**Remediation.**",
		"## Appendix A. Being verified (8)", "## Appendix B. Not verified (12)", "## Appendix C. Rejected leads (20)", "## Appendix D. Scope and coverage", "| Entry points | 340 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("REPORT.md lacks %q", want)
		}
	}
	if strings.Contains(md, "point 17") {
		t.Error("REPORT.md lists the attack surface point by point")
	}
	// the index runs worst first, every reportable finding once
	idx := md[strings.Index(md, "## Index"):strings.Index(md, "## Findings")]
	if n := strings.Count(idx, "\n| [L"); n != 120 {
		t.Errorf("the index has %d rows, want the 110 findings and 10 disputed leads", n)
	}
	if strings.Index(idx, "| Critical |") > strings.Index(idx, "| Low |") {
		t.Error("the index is not ordered by severity")
	}
	page := HTML(r)
	for _, want := range []string{`<section id="summary">`, `class="tiles"`, `class="dist"`, `id="fs-critical"`, `id="ft-disputed"`, `data-sev="critical" data-st="confirmed"`,
		`<article class="finding s-critical" id="l1"`, "Reviewer 2", "Defender", "Back to the index"} {
		if !strings.Contains(page, want) {
			t.Errorf("report.html lacks %q", want)
		}
	}
	if strings.Contains(page, "<script") {
		t.Error("report.html has a script")
	}
	if dir := os.Getenv("SLEIPNIR_SCAN_SAMPLE"); dir != "" { // a sample report to look at: SLEIPNIR_SCAN_SAMPLE=DIR go test -run TestTheReportIsADocument
		_ = os.WriteFile(filepath.Join(dir, HTMLFile), []byte(page), 0o644)
		_ = os.WriteFile(filepath.Join(dir, ReportFile), []byte(md), 0o644)
		b, _ := json.MarshalIndent(r, "", " ")
		_ = os.WriteFile(filepath.Join(dir, "scan.json"), b, 0o644)
	}
}

func TestAScanStopsTakingLeads(t *testing.T) {
	r := &Run{Depth: "standard"}
	for i := 0; i < 4; i++ {
		r.Shards = append(r.Shards, Shard{ID: fmt.Sprintf("S%d", i+1)})
	}
	if b := r.LeadBudget(); b != minLeadBudget {
		t.Fatalf("budget of a small scan: %d", b)
	}
	add := func(late bool, deferred, status string) {
		l := Lead{ID: fmt.Sprintf("L%d", len(r.Leads)+1), Late: late, Deferred: deferred}
		if status == LeadRejected {
			l.Verdicts = []Verdict{{By: "a", Reason: "wrong"}, {By: "b", Reason: "wrong"}}
		}
		r.Leads = append(r.Leads, l)
	}
	for i := 0; i < 30; i++ {
		add(false, "", LeadOpen)
	}
	// while the hunt runs, the budget holds back all but a critical lead
	if late, why := r.intake(SevHigh); late || !strings.Contains(why, "budget of 24") {
		t.Fatalf("over the budget: %v %q", late, why)
	}
	if _, why := r.intake(SevCritical); why != "" {
		t.Fatalf("a critical lead was deferred: %q", why)
	}
	// once every shard is hunted, a few late leads are verified, and then none
	r.Leads = r.Leads[:20]
	for i := range r.Shards {
		r.Shards[i].Hunted = "done"
	}
	if late, why := r.intake(SevLow); !late || why != "" {
		t.Fatalf("the first late lead: %v %q", late, why)
	}
	add(true, "", LeadOpen)
	add(true, "", LeadOpen)
	add(true, "", LeadOpen)
	if _, why := r.intake(SevMedium); !strings.Contains(why, "3 late leads") {
		t.Fatalf("the late allowance is spent: %q", why)
	}
	// late leads that are all rejected close the intake too
	r.Leads = r.Leads[:20]
	r.Shards = append(r.Shards, make([]Shard, 36)...) // a larger scan: a budget of 240 and an allowance of 3 for 20 leads
	for i := range r.Shards {
		r.Shards[i].Hunted = "done"
	}
	for i := 0; i < 3; i++ {
		add(true, "", LeadRejected)
	}
	in := r.Intake()
	if !in.Closed || in.Late != 3 {
		t.Fatalf("intake after three rejected late leads: %+v", in)
	}
	if _, why := r.intake(SevHigh); why == "" {
		t.Fatal("a late lead was taken after the late leads stopped holding up")
	}
	add(false, "the budget is spent", LeadOpen)
	if r.Intake().Deferred != 1 || r.Leads[len(r.Leads)-1].Status(r.DepthInfo()) != LeadDeferred {
		t.Fatal("a deferred lead is not counted as deferred")
	}
}

func TestADisputedLeadIsSettled(t *testing.T) {
	deep, _ := DepthByID("deep")
	l := Lead{By: "f", Impact: SevMedium, Reach: ReachExternal, Exploit: ExploitTrivial}
	for i, v := range []Verdict{{By: "a", Confirm: true}, {By: "b", Confirm: true}, {By: "c", Reason: "wrong"}, {By: "d", Reason: "wrong"}} {
		l.Verdicts = append(l.Verdicts, v)
		if st := l.Status(deep); st != LeadOpen {
			t.Fatalf("after %d verdicts: %s", i+1, st)
		}
	}
	l.Verdicts = append(l.Verdicts, Verdict{By: "e", Confirm: true})
	if st := l.Status(deep); st != LeadDisputed {
		t.Fatalf("five verdicts without a defender's: %s", st)
	}
	r := &Run{Depth: "deep", Leads: []Lead{l}}
	rep := Check(r)
	if rep.Counts.Disputed != 1 {
		t.Fatalf("counts: %+v", rep.Counts)
	}
	for _, p := range rep.Problems {
		if p.Phase == PhaseVerify {
			t.Fatalf("a disputed lead blocks the scan: %+v", p)
		}
	}
}

func TestASettledLeadTakesNoVerdict(t *testing.T) {
	tl, _ := harness(t, "survey")
	mustOK(t, tl, "hunter", map[string]any{"action": "lead", "title": "Command injection in /ping", "category": "command-injection",
		"claim": "Anyone can run shell commands on the server by sending host=x;id to GET /ping.", "trace": pingTrace,
		"cvss": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", "reach": "external", "exploit": "trivial"})
	mustOK(t, tl, "v1", map[string]any{"action": "verdict", "id": "L1", "confirm": true, "note": "routes.go registers /ping with no middleware; host flows into sh -c"})
	mustFail(t, tl, "v2", map[string]any{"action": "verdict", "id": "L1", "confirm": false, "reason": "wrong", "note": "I would rather argue about this one a bit more"}, "settled")
}
