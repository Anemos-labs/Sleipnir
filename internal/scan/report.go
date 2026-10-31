package scan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Report files, in a scan's directory.
const (
	ReportFile   = "REPORT.md"
	FindingsFile = "findings.json"
	SARIFFile    = "findings.sarif"
	MapFile      = "map.json"
)

// WriteReport writes the scan's report files from its ledger as it is now: the Markdown report, the findings as JSON and as
// SARIF, the map, the findings as CSV, the report as a page that prints to PDF, and the reproductions (WriteProofs). It is called after every change, so the
// report can be read while the scan runs.
func WriteReport(root string, r *Run) error {
	dir := RunDir(root, r.Slug)
	f, err := json.MarshalIndent(FindingsDoc(r), "", " ")
	if err != nil {
		return err
	}
	s, err := json.MarshalIndent(SARIF(r), "", " ")
	if err != nil {
		return err
	}
	m, err := json.MarshalIndent(MapDoc(r), "", " ")
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{ReportFile: []byte(Markdown(r)), FindingsFile: append(f, '\n'), SARIFFile: append(s, '\n'), MapFile: append(m, '\n'), CSVFile: CSV(r), HTMLFile: []byte(HTML(r))} {
		if err := WriteAtomic(root, filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return WriteProofs(root, r)
}

// Finding is a lead as the report gives it. A report says what was found and how it was checked, never which agent did what:
// a verdict is given by its role, and a reproduction by whether it was independent of the filer.
type Finding struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	// CVSS is the CVSS v3.1 base vector the severity was computed from and Score its base score; both are absent for a lead
	// filed before leads carried vectors, which gave an Impact level instead.
	CVSS     string         `json:"cvss,omitempty"`
	Score    *float64       `json:"score,omitempty"`
	Impact   string         `json:"impact,omitempty"`
	Reach    string         `json:"reach"`
	Exploit  string         `json:"exploit"`
	Claim    string         `json:"claim"`
	Trace    []Step         `json:"trace"`
	Points   []string       `json:"points,omitempty"`
	Fix      string         `json:"fix,omitempty"`
	Verdicts []Ruling       `json:"verdicts,omitempty"`
	Proofs   []Reproduction `json:"proofs,omitempty"`
	Reason   string         `json:"reject_reason,omitempty"`
	// Confirms and Rejects count the verifiers' latest verdicts; Defended says a defender confirmed; Reproduced says an
	// independent reproducer showed the harm.
	Confirms   int  `json:"confirms"`
	Rejects    int  `json:"rejects"`
	Defended   bool `json:"defended,omitempty"`
	Reproduced bool `json:"reproduced,omitempty"`
	// Late says the lead was filed after every shard was hunted; Deferred why it was not verified.
	Late     bool   `json:"late,omitempty"`
	Deferred string `json:"deferred,omitempty"`
	// Anchor is where the finding shows on the code: in a review, the last step of its trace on a line the change made.
	Anchor *Cite `json:"anchor,omitempty"`
}

// Ruling is one verifier's latest verdict as a report gives it: its role and what it found, not who gave it.
type Ruling struct {
	Role    string `json:"role"`
	Confirm bool   `json:"confirm"`
	Reason  string `json:"reason,omitempty"`
	Note    string `json:"note"`
	Reach   string `json:"reach,omitempty"`
	Exploit string `json:"exploit,omitempty"`
	CVSS    string `json:"cvss,omitempty"`
	Cites   []Cite `json:"cites,omitempty"`
}

// Reproduction is one run of a reproducer as a report gives it. Independent says it was written by an agent other than the
// filer: only such a run proves a lead.
type Reproduction struct {
	Independent bool   `json:"independent"`
	Script      string `json:"script"`
	Ready       bool   `json:"ready"`
	Exercised   bool   `json:"exercised"`
	Outcome     string `json:"outcome"`
	Exit        int    `json:"exit"`
	Output      string `json:"output"`
	Network     bool   `json:"network,omitempty"`
	Seconds     int    `json:"seconds"`
	At          string `json:"at,omitempty"`
}

// FindingOf is a lead as the report gives it, with the agents' names taken out of its text (Run.redactor).
func FindingOf(r *Run, l *Lead) Finding { return findingOf(r, l, r.redactor()) }

func findingOf(r *Run, l *Lead, red func(string) string) Finding {
	f := Finding{ID: l.ID, Title: red(l.Title), Category: l.Category, Severity: l.Severity(), Status: l.Status(r.DepthInfo()), Impact: l.Impact,
		Reach: l.Reachability(), Exploit: l.Exploitability(), Claim: red(l.Claim), Points: l.Points, Fix: red(l.Fix),
		Reproduced: l.Reproduced(), Late: l.Late, Deferred: l.Deferred, Anchor: r.Anchor(l)}
	if score, ok := l.Score(); ok {
		f.CVSS, f.Score = l.Vector(), &score
	}
	f.Confirms, f.Rejects, f.Defended = l.Tally()
	for _, s := range l.Trace {
		f.Trace = append(f.Trace, Step{Cite: s.Cite, Note: red(s.Note)})
	}
	for _, v := range l.latestVerdicts() {
		f.Verdicts = append(f.Verdicts, Ruling{Role: v.Role, Confirm: v.Confirm, Reason: v.Reason, Note: red(v.Note), Reach: v.Reach, Exploit: v.Exploit, CVSS: v.CVSS, Cites: v.Cites})
	}
	for _, p := range l.Proofs {
		f.Proofs = append(f.Proofs, Reproduction{Independent: p.By != l.By, Script: red(p.Script), Ready: p.Ready, Exercised: p.Exercised, Outcome: p.Outcome,
			Exit: p.Exit, Output: red(p.Output), Network: p.Network, Seconds: p.Seconds, At: p.At})
	}
	if f.Status == LeadRejected {
		f.Reason = l.RejectReason()
	}
	return f
}

// Findings are the scan's leads as the report gives them: confirmed first, then unproven, disputed, open, deferred and
// rejected, each by severity.
func Findings(r *Run) []Finding {
	out := make([]Finding, 0, len(r.Leads))
	red := r.redactor()
	for i := range r.Leads {
		out = append(out, findingOf(r, &r.Leads[i], red))
	}
	order := map[string]int{LeadConfirmed: 0, LeadUnproven: 1, LeadDisputed: 2, LeadOpen: 3, LeadDeferred: 4, LeadRejected: 5}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Status] != order[out[j].Status] {
			return order[out[i].Status] < order[out[j].Status]
		}
		return worse(out[i], out[j])
	})
	return out
}

// FindingsDoc is findings.json.
func FindingsDoc(r *Run) map[string]any {
	rep := Check(r)
	doc := map[string]any{"scan": r.Slug, "commit": r.Commit, "depth": r.Depth, "focus": r.Focus, "phase": rep.Phase,
		"counts": rep.Counts, "findings": Findings(r)}
	if r.IsReview() {
		doc["base"], doc["changes"] = r.Base, r.Changes
	}
	return doc
}

// MapDoc is map.json: the shards with their maps, the points and the edges, in the node and edge shape code graphs use
// (graphify's), so the map can be read by the same tools. Edges are EXTRACTED when a checked quote backs them, else INFERRED.
func MapDoc(r *Run) map[string]any {
	type node struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Kind     string `json:"kind"`
		Lane     string `json:"lane,omitempty"`
		File     string `json:"source_file,omitempty"`
		Location string `json:"source_location,omitempty"`
		Shard    string `json:"shard,omitempty"`
		Summary  string `json:"summary,omitempty"`
	}
	type edge struct {
		ID         string `json:"id"`
		Source     string `json:"source"`
		Target     string `json:"target"`
		Relation   string `json:"relation"`
		Confidence string `json:"confidence"`
		Location   string `json:"source_location,omitempty"`
		Note       string `json:"note,omitempty"`
	}
	nodes := []node{}
	for _, s := range r.Shards {
		nodes = append(nodes, node{ID: s.ID, Label: shardSpan(s), Kind: "shard", Summary: s.Mapped})
	}
	for _, p := range r.Points {
		nodes = append(nodes, node{ID: p.ID, Label: p.Title, Kind: "point", Lane: p.Lane, File: p.File, Location: fmt.Sprintf("L%d", p.Line), Shard: p.Shard, Summary: p.Note})
	}
	edges := []edge{}
	for _, e := range r.Edges {
		x := edge{ID: e.ID, Source: e.From, Target: e.To, Relation: e.Relation, Confidence: "INFERRED", Note: e.Note}
		if e.Cite != nil {
			x.Confidence, x.Location = "EXTRACTED", fmt.Sprintf("%s:L%d", e.Cite.File, e.Cite.Line)
		}
		edges = append(edges, x)
	}
	return map[string]any{"scan": r.Slug, "commit": r.Commit, "nodes": nodes, "edges": edges}
}

// sarifLevel is the SARIF level of a severity.
func sarifLevel(sev string) string {
	switch sev {
	case SevCritical, SevHigh:
		return "error"
	case SevMedium:
		return "warning"
	}
	return "note"
}

// securitySeverity is the score GitHub code scanning sorts security results by, for a finding without a CVSS score of its own
// or one whose severity was capped below its score's band.
var securitySeverity = map[string]string{SevCritical: "9.5", SevHigh: "8.0", SevMedium: "5.5", SevLow: "2.0"}

// SARIF is findings.sarif (SARIF 2.1.0): the confirmed and unproven findings, located at their trace's last step (the sink),
// or in a review at the last step on a changed line, with the whole trace as a code flow.
func SARIF(r *Run) map[string]any {
	rules := []any{}
	seen := map[string]bool{}
	results := []any{}
	loc := func(c Cite, msg string) map[string]any {
		return map[string]any{"physicalLocation": map[string]any{"artifactLocation": map[string]any{"uri": c.File},
			"region": map[string]any{"startLine": max(c.Line, 1), "endLine": max(c.Line, 1) + max(len(normLines(c.Quote))-1, 0)}},
			"message": map[string]any{"text": msg}}
	}
	for _, f := range Findings(r) {
		if (f.Status != LeadConfirmed && f.Status != LeadUnproven) || len(f.Trace) == 0 {
			continue
		}
		rule := "sleipnir/" + f.Category
		if !seen[rule] {
			seen[rule] = true
			rules = append(rules, map[string]any{"id": rule, "name": f.Category, "shortDescription": map[string]any{"text": f.Category},
				"properties": map[string]any{"tags": []string{"security", f.Category}}})
		}
		var flow []any
		for _, s := range f.Trace {
			flow = append(flow, map[string]any{"location": loc(s.Cite, s.Note)})
		}
		at := f.Trace[len(f.Trace)-1]
		for _, s := range f.Trace {
			if f.Anchor != nil && s.Cite == *f.Anchor {
				at = s
			}
		}
		results = append(results, map[string]any{
			"ruleId": rule, "level": sarifLevel(f.Severity),
			"message":             map[string]any{"text": f.Title + ": " + f.Claim},
			"locations":           []any{loc(at.Cite, at.Note)},
			"codeFlows":           []any{map[string]any{"threadFlows": []any{map[string]any{"locations": flow}}}},
			"partialFingerprints": map[string]any{"sleipnirScan/v1": fingerprint(f)},
			"properties":          sarifProperties(f),
		})
	}
	return map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json", "version": "2.1.0",
		"runs": []any{map[string]any{
			"tool":                     map[string]any{"driver": map[string]any{"name": "sleipnir-scan", "informationUri": "https://github.com/anemos-labs/sleipnir", "rules": rules}},
			"versionControlProvenance": []any{map[string]any{"revisionId": r.Commit}},
			"results":                  results,
		}},
	}
}

// fingerprint identifies a finding across scans: its category and where its sink is, by the code there rather than the line.
func fingerprint(f Finding) string {
	sink := f.Trace[len(f.Trace)-1]
	return f.Category + ":" + sink.File + ":" + strings.Join(normLines(sink.Quote), " ")
}

// Markdown is REPORT.md: the report as a document, a summary first and the findings by severity with an index, each with
// its facts, its trace, how it was checked and the fix; then the leads that are not findings, the scope and the method.
func Markdown(r *Run) string {
	d := r.DepthInfo()
	rep := Check(r)
	c := rep.Counts
	fs := Findings(r)
	sum := Summarize(fs)
	done := rep.Phase == PhaseDone
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	cell := func(s string) string { return strings.ReplaceAll(oneLine(s), "|", "\\|") }
	if r.IsReview() {
		w("# Security review of a change: %s\n\n", coverTitle(r))
	} else {
		w("# Security assessment: %s\n\n", coverTitle(r))
	}
	w("| | |\n|---|---|\n")
	if r.IsReview() {
		w("| Change | `%s`..`%s` (%d files) |\n", short(r.Base), short(r.Commit), len(r.Changes))
	} else {
		w("| Commit | `%s` |\n", short(r.Commit))
	}
	if r.Focus != "" {
		w("| Scope | `%s` |\n", r.Focus)
	}
	w("| Code | %s files, %s functions, %s lines |\n", thousands(c.Files), thousands(c.Funcs), thousands(c.Lines))
	w("| Depth | %s: %s |\n", d.Title, d.Summary)
	if done {
		w("| Status | Complete |\n")
	} else {
		w("| Status | In progress (%s): only confirmed findings are final |\n", rep.Phase)
	}
	w("| Reference | `%s` |\n\n", r.Slug)

	w("## Summary\n\n%s\n\n", sum.Headline(done))
	w("| Severity | Findings | Not reproduced |\n|---|--:|--:|\n")
	for _, sev := range Severities {
		w("| %s | %d | %d |\n", title(sev), sum.BySeverity[sev], sum.Unproven[sev])
	}
	w("| **Total** | **%d** | **%d** |\n\n", sum.Findings, sumMap(sum.Unproven))
	if len(sum.Categories) > 0 {
		w("### By category\n\n| Category | Critical | High | Medium | Low | Total |\n|---|--:|--:|--:|--:|--:|\n")
		for _, cc := range sum.Categories {
			w("| %s | %d | %d | %d | %d | %d |\n", cc.Category, cc.BySeverity[SevCritical], cc.BySeverity[SevHigh], cc.BySeverity[SevMedium], cc.BySeverity[SevLow], cc.Total)
		}
		w("\n")
	}

	list := bySeverity(Reportable(fs))
	if len(list) == 0 {
		w("## Findings\n\nNo lead has been confirmed")
		if !done {
			w(" yet")
		}
		w(".\n\n")
	} else {
		w("## Index\n\n| ID | Severity | Finding | Category | Location | Status |\n|---|---|---|---|---|---|\n")
		for _, f := range list {
			w("| [%s](#%s) | %s | %s | %s | `%s` | %s |\n", f.ID, strings.ToLower(f.ID), SeverityLabel(f), cell(f.Title), f.Category, cell(Location(f)), StatusLabel(f))
		}
		w("\n## Findings\n\n")
		for _, f := range list {
			writeFinding(&b, f, d)
		}
	}

	rest := func(status string) []Finding {
		var out []Finding
		for _, f := range fs {
			if f.Status == status {
				out = append(out, f)
			}
		}
		return bySeverity(out)
	}
	appendix := 'A'
	table := func(heading, intro string, list []Finding, why func(Finding) string) {
		if len(list) == 0 {
			return
		}
		w("## Appendix %c. %s (%d)\n\n%s\n\n| ID | Severity | Lead | Category | Location | %s |\n|---|---|---|---|---|---|\n", appendix, heading, len(list), intro, "Note")
		for _, f := range list {
			w("| %s | %s | %s | %s | `%s` | %s |\n", f.ID, SeverityLabel(f), cell(f.Title), f.Category, cell(Location(f)), cell(why(f)))
		}
		w("\n")
		appendix++
	}
	table("Being verified", "Filed and not yet ruled on by enough reviewers: not findings yet.", rest(LeadOpen), func(f Finding) string {
		return fmt.Sprintf("%d of %d confirmations", f.Confirms, d.Verifiers)
	})
	table("Not verified", "Recorded after the scan reached its stopping point (its budget, or the end of the hunt), and kept unreviewed: leads to triage, not findings.", rest(LeadDeferred), func(f Finding) string { return f.Deferred })
	table("Rejected leads", "Leads the reviewers showed are not real, with the reason.", rest(LeadRejected), func(f Finding) string {
		if f.Reason == "" {
			return "rejected"
		}
		return f.Reason
	})

	w("## Appendix %c. Scope and coverage\n\n", appendix)
	w("| Measure | Count |\n|---|--:|\n| Files | %s |\n| Functions | %s |\n| Lines | %s |\n| Shards mapped | %d of %d |\n| Shards hunted | %d of %d |\n",
		thousands(c.Files), thousands(c.Funcs), thousands(c.Lines), c.Mapped, c.Shards, c.Hunted, c.Shards)
	for _, lane := range Lanes {
		w("| %s | %s |\n", laneTitle[lane], thousands(c.Lanes[lane]))
	}
	w("| Relations between places | %s (%s inferred) |\n| Leads filed | %s |\n\n", thousands(c.Edges), thousands(c.Inferred), thousands(c.Leads))
	if hot := hotFiles(r, 10); len(hot) > 0 {
		w("Files with the largest attack surface:\n\n| File | Points | Entry | Sinks |\n|---|--:|--:|--:|\n")
		for _, h := range hot {
			w("| `%s` | %d | %d | %d |\n", cell(h.File), h.Total, h.Lanes[LaneEntry], h.Lanes[LaneSink])
		}
		w("\n")
	}
	if len(r.Inventory.Skipped) > 0 {
		var ks []string
		for k, n := range r.Inventory.Skipped {
			ks = append(ks, fmt.Sprintf("%d %s", n, k))
		}
		sort.Strings(ks)
		w("Left out of the scan: %s.\n\n", strings.Join(ks, ", "))
	}
	w("The attack surface point by point and the relations between places are in `%s`; every lead, with its trace, in `%s`.\n\n", MapFile, FindingsFile)
	w("## Method\n\n%s\n", methodText(r))
	return b.String()
}

// methodText is how the scan worked and what makes a lead a finding, in a paragraph.
func methodText(r *Run) string {
	d := r.DepthInfo()
	var b strings.Builder
	fmt.Fprintf(&b, "Every file tracked at the commit was listed, vendored, generated and binary files left out, and the code split into %d shards of at most %d functions or %d lines. ", len(r.Shards), d.ShardFuncs, d.ShardLines)
	b.WriteString("Each shard was read in full and mapped: its entry points, trust boundaries, access checks, sinks and security configuration, and how its parts connect. Each was then hunted by following input from where it enters to where it can do harm. ")
	b.WriteString("Every quote in this report was checked against the file at the commit. ")
	fmt.Fprintf(&b, "A lead becomes a finding when %d %s that did not file it confirm it", d.Verifiers, plural(d.Verifiers, "reviewer", "reviewers"))
	if d.Defender {
		b.WriteString(", one of them a defender who looks for the reason it is not real")
	}
	b.WriteString(proofNote(d) + ". ")
	b.WriteString("Severity is computed from a CVSS v3.1 base vector the reviewers agree on: the score comes from the specification's formula and the severity from its rating band (critical from 9.0, high from 7.0, medium from 4.0), and it is low at most when nothing reaches the code or no practical exploit was shown. ")
	for i := range r.Leads {
		if _, ok := r.Leads[i].Score(); !ok {
			b.WriteString("A lead recorded without a vector keeps the earlier rule: its impact, one level lower when only an insider reaches the code and one lower when exploiting it is difficult. ")
			break
		}
	}
	fmt.Fprintf(&b, "The scan verifies at most %d leads; after every shard was hunted, only a few more, and a lead below critical filed past that point is recorded as not verified.", r.LeadBudget())
	return b.String()
}

var laneTitle = map[string]string{LaneEntry: "Entry points", LaneBoundary: "Trust boundaries", LaneAccess: "Access control", LaneSink: "Sinks", LaneConfig: "Security configuration"}

func proofNote(d Depth) string {
	if d.ProveFrom == "" {
		return ""
	}
	return fmt.Sprintf(", and, from %s up, reproduced by a script run in the sandbox", d.ProveFrom)
}

// writeFinding writes one finding of REPORT.md: its facts, the description, the trace, how it was checked, and the fix.
func writeFinding(b *strings.Builder, f Finding, d Depth) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	w("<a id=\"%s\"></a>\n### %s · %s\n\n", strings.ToLower(f.ID), f.ID, oneLine(f.Title))
	w("| Severity | Status | Category | Location |\n|---|---|---|---|\n| **%s** | %s | %s | `%s` |\n\n", SeverityLabel(f), StatusLabel(f), f.Category, Location(f))
	w("Reachability %s · exploitability %s · %s\n\n", f.Reach, f.Exploit, scoreMarkdown(f))
	w("**Description.** %s\n\n", strings.TrimSpace(f.Claim))
	w("**Trace**, from where the input comes in to where it does harm:\n\n")
	for i, s := range f.Trace {
		w("%d. `%s:%d` %s\n\n", i+1, s.File, s.Line, oneLine(s.Note))
		writeFence(b, "   ", s.Quote)
	}
	w("**Verification.** %s\n\n", Verification(f, d))
	labels := RoleLabels(f.Verdicts)
	for i, v := range f.Verdicts {
		verdict := "confirms"
		if !v.Confirm {
			verdict = "rejects (" + v.Reason + ")"
		}
		w("- %s %s: %s\n", labels[i], verdict, oneLine(v.Note))
	}
	if len(f.Verdicts) > 0 {
		w("\n")
	}
	if f.Fix != "" {
		w("**Remediation.** %s\n\n", strings.TrimSpace(f.Fix))
	}
	for i, p := range f.Proofs {
		if p.Outcome == ProofReproduced && p.Independent {
			d := ProofDir(f.ID, proofNumber(&f, i))
			w("**Proof.** [%s](%s/%s)\n\n", d, d, ProofReadme)
			w("<details><summary>Reproducer and its output</summary>\n\n")
			writeFence(b, "", p.Script)
			writeFence(b, "", p.Output)
			w("</details>\n\n")
			break
		}
	}
	w("[Back to the index](#index)\n\n---\n\n")
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// sarifProperties are a finding's SARIF result properties: its CVSS score as GitHub's security-severity, unless a cap put the
// severity below the score's band (then the band's representative score, so code scanning does not rank it above its severity).
func sarifProperties(f Finding) map[string]any {
	sev := securitySeverity[f.Severity]
	p := map[string]any{"status": f.Status, "reachability": f.Reach, "exploitability": f.Exploit,
		"reproduced": f.Status == LeadConfirmed && f.Reproduced, "lead": f.ID}
	if f.Score != nil {
		p["cvss"], p["cvssScore"] = f.CVSS, *f.Score
		if CVSSBand(*f.Score) == f.Severity {
			sev = strconv.FormatFloat(*f.Score, 'f', 1, 64)
		}
	}
	p["security-severity"] = sev
	return p
}

// ScoreLabel is a finding's score in plain text: "CVSS 9.8 (vector)", or the impact a finding without a vector gave.
func ScoreLabel(f Finding) string {
	if f.Score == nil {
		return "impact " + f.Impact
	}
	s := fmt.Sprintf("CVSS %.1f (%s)", *f.Score, f.CVSS)
	if CVSSBand(*f.Score) != f.Severity {
		s += ", capped at " + f.Severity + ": " + capReason(f)
	}
	return s
}

// scoreMarkdown is ScoreLabel for REPORT.md, with the vector linked to FIRST's calculator.
func scoreMarkdown(f Finding) string {
	if f.Score == nil {
		return "impact " + f.Impact
	}
	s := fmt.Sprintf("CVSS **%.1f** [`%s`](%s)", *f.Score, f.CVSS, CVSSCalculator(f.CVSS))
	if CVSSBand(*f.Score) != f.Severity {
		s += ", capped at " + f.Severity + ": " + capReason(f)
	}
	return s
}

// capReason says why a finding's severity is below its score's band.
func capReason(f Finding) string {
	if f.Reach == ReachUnreachable {
		return "no path reaches the code"
	}
	return "no practical exploit was shown"
}

// worse orders findings worst first: by severity, then by CVSS score (a scored finding before an unscored one), then in the
// order they were filed.
func worse(a, b Finding) bool {
	if x, y := sevRank(a.Severity), sevRank(b.Severity); x != y {
		return x < y
	}
	if (a.Score != nil) != (b.Score != nil) {
		return a.Score != nil
	}
	if a.Score != nil && *a.Score != *b.Score {
		return *a.Score > *b.Score
	}
	return leadNum(a.ID) < leadNum(b.ID)
}

// SeverityLabel is a finding's severity with its CVSS score when it has one: "Critical 9.8", or "Low (capped, CVSS 9.8)" when
// the severity was capped below the score's band.
func SeverityLabel(f Finding) string {
	switch {
	case f.Score == nil:
		return title(f.Severity)
	case CVSSBand(*f.Score) != f.Severity:
		return fmt.Sprintf("%s (capped, CVSS %.1f)", title(f.Severity), *f.Score)
	}
	return fmt.Sprintf("%s %.1f", title(f.Severity), *f.Score)
}
