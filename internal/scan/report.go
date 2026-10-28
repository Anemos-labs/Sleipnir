package scan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
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
// SARIF, the map, the findings as CSV and the report as a page that prints to PDF. It is called after every change, so the
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
		if err := WriteAtomic(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}

// Finding is a lead as the report gives it.
type Finding struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Category string    `json:"category"`
	Severity string    `json:"severity"`
	Status   string    `json:"status"`
	Impact   string    `json:"impact"`
	Reach    string    `json:"reach"`
	Exploit  string    `json:"exploit"`
	Claim    string    `json:"claim"`
	Trace    []Step    `json:"trace"`
	Points   []string  `json:"points,omitempty"`
	Fix      string    `json:"fix,omitempty"`
	By       string    `json:"by"`
	Verdicts []Verdict `json:"verdicts,omitempty"`
	Proofs   []Proof   `json:"proofs,omitempty"`
	Reason   string    `json:"reject_reason,omitempty"`
	// Anchor is where the finding shows on the code: in a review, the last step of its trace on a line the change made.
	Anchor *Cite `json:"anchor,omitempty"`
}

// FindingOf is a lead as the report gives it.
func FindingOf(r *Run, l *Lead) Finding {
	f := Finding{ID: l.ID, Title: l.Title, Category: l.Category, Severity: l.Severity(), Status: l.Status(r.DepthInfo()), Impact: l.Impact,
		Reach: l.Reachability(), Exploit: l.Exploitability(), Claim: l.Claim, Trace: l.Trace, Points: l.Points, Fix: l.Fix, By: l.By,
		Verdicts: l.Verdicts, Proofs: l.Proofs, Anchor: r.Anchor(l)}
	if f.Status == LeadRejected {
		f.Reason = l.RejectReason()
	}
	return f
}

// Findings are the scan's leads as the report gives them: confirmed first, then unproven, open and rejected, each by severity.
func Findings(r *Run) []Finding {
	out := make([]Finding, 0, len(r.Leads))
	for i := range r.Leads {
		out = append(out, FindingOf(r, &r.Leads[i]))
	}
	order := map[string]int{LeadConfirmed: 0, LeadUnproven: 1, LeadOpen: 2, LeadRejected: 3}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Status] != order[out[j].Status] {
			return order[out[i].Status] < order[out[j].Status]
		}
		return sevRank(out[i].Severity) < sevRank(out[j].Severity)
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

// securitySeverity is the score GitHub code scanning sorts security results by.
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
			"properties": map[string]any{"security-severity": securitySeverity[f.Severity], "status": f.Status, "reachability": f.Reach,
				"exploitability": f.Exploit, "reproduced": f.Status == LeadConfirmed && anyReproduced(f.Proofs, f.By), "lead": f.ID},
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

func anyReproduced(ps []Proof, filer string) bool {
	for _, p := range ps {
		if p.Outcome == ProofReproduced && p.By != filer {
			return true
		}
	}
	return false
}

// Markdown is REPORT.md.
func Markdown(r *Run) string {
	d := r.DepthInfo()
	rep := Check(r)
	c := rep.Counts
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	if r.IsReview() {
		w("# Review %s\n\n", r.Slug)
		w("Change `%s`..`%s` (%d files)", short(r.Base), short(r.Commit), len(r.Changes))
	} else {
		w("# Deep scan %s\n\n", r.Slug)
		w("Commit `%s`", short(r.Commit))
	}
	if r.Focus != "" {
		w(" · focus `%s`", r.Focus)
	}
	w(" · %s depth (%s) · phase **%s**\n\n", d.Title, d.Summary, rep.Phase)
	if rep.Phase != PhaseDone {
		w("> This scan is still running: the report is rewritten after every change, and only confirmed findings are final.\n\n")
	}
	w("| Scanned | Mapped | Hunted | Points | Edges | Leads | Confirmed | Unproven | Rejected |\n|---|---|---|---|---|---|---|---|---|\n")
	w("| %d files, %d functions, %d lines | %d/%d shards | %d/%d shards | %d | %d (%d inferred) | %d | %d | %d | %d |\n\n",
		c.Files, c.Funcs, c.Lines, c.Mapped, c.Shards, c.Hunted, c.Shards, c.Points, c.Edges, c.Inferred, c.Leads, c.Confirmed, c.Unproven, c.Rejected)
	fs := Findings(r)
	section := func(title, status, intro string, full bool) {
		var list []Finding
		for _, f := range fs {
			if f.Status == status {
				list = append(list, f)
			}
		}
		if len(list) == 0 {
			return
		}
		w("## %s (%d)\n\n%s\n\n", title, len(list), intro)
		for _, f := range list {
			if !full {
				line := fmt.Sprintf("- **%s** %s (%s, %s)", f.ID, f.Title, f.Severity, f.Category)
				if f.Reason != "" {
					line += ": rejected as " + f.Reason
				}
				w("%s\n", line)
				continue
			}
			writeFinding(&b, f)
		}
		w("\n")
	}
	section("Findings", LeadConfirmed, "Confirmed by agents that did not file them"+proofNote(d)+".", true)
	section("Unproven", LeadUnproven, "Confirmed by the verifiers, but not reproduced in the sandbox: treat as likely, not shown.", true)
	section("Still being verified", LeadOpen, "Filed, not yet ruled on by enough verifiers.", false)
	section("Rejected", LeadRejected, "Leads the verifiers rejected, with the reason.", false)
	if c.Leads == 0 {
		w("## Findings\n\nNo lead has been filed yet.\n\n")
	}
	w("## Attack surface\n\n")
	if len(r.Points) == 0 {
		w("No point is recorded yet.\n\n")
	}
	for _, lane := range Lanes {
		var ps []Point
		for _, p := range r.Points {
			if p.Lane == lane {
				ps = append(ps, p)
			}
		}
		if len(ps) == 0 {
			continue
		}
		w("### %s (%d)\n\n", laneTitle[lane], len(ps))
		for _, p := range ps {
			w("- **%s** %s: `%s:%d`", p.ID, p.Title, p.File, p.Line)
			if p.Note != "" {
				w(": %s", oneLine(p.Note))
			}
			w("\n")
		}
		w("\n")
	}
	w("## Map\n\n")
	for _, s := range r.Shards {
		if s.Mapped == "" {
			continue
		}
		w("- **%s** %s (%d functions): %s\n", s.ID, shardSpan(s), s.Funcs, oneLine(s.Mapped))
	}
	w("\n## Method\n\nThe harness listed every tracked file at the commit, left out vendored, generated and binary files, counted the functions and their branches, ")
	w("and split the code into %d shards. Agents mapped each shard, recorded the attack surface and the relations between places, and hunted each shard for holes. ", len(r.Shards))
	w("Every quote in this report was checked by the harness against the file at the commit. A lead becomes a finding when %d %s that did not file it confirm it", d.Verifiers, plural(d.Verifiers, "agent", "agents"))
	if d.Defender {
		w(", one of them a defender who looked for the reason it is not real")
	}
	w("%s. Severity is computed from impact, reachability and exploitability: one level lower when only an insider reaches the code, one lower when exploiting it is difficult, low at most when nothing reaches it or no practical exploit was shown.\n", proofNote(d))
	if len(r.Inventory.Skipped) > 0 {
		var ks []string
		for k, n := range r.Inventory.Skipped {
			ks = append(ks, fmt.Sprintf("%d %s", n, k))
		}
		sort.Strings(ks)
		w("\nLeft out: %s.\n", strings.Join(ks, ", "))
	}
	return b.String()
}

var laneTitle = map[string]string{LaneEntry: "Entry points", LaneBoundary: "Trust boundaries", LaneAccess: "Access control", LaneSink: "Sinks", LaneConfig: "Security configuration"}

func proofNote(d Depth) string {
	if d.ProveFrom == "" {
		return ""
	}
	return fmt.Sprintf(", and, from %s up, reproduced by a script run in the sandbox", d.ProveFrom)
}

func writeFinding(b *strings.Builder, f Finding) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	w("### %s · %s · %s\n\n", f.ID, strings.ToUpper(f.Severity), f.Title)
	w("%s · reachability %s · exploitability %s · impact %s\n\n", f.Category, f.Reach, f.Exploit, f.Impact)
	w("%s\n\n", f.Claim)
	w("**Trace**\n\n")
	for i, s := range f.Trace {
		w("%d. `%s:%d` %s\n\n", i+1, s.File, s.Line, s.Note)
		w("   ```\n")
		for _, l := range strings.Split(s.Quote, "\n") {
			w("   %s\n", l)
		}
		w("   ```\n\n")
	}
	for _, v := range f.Verdicts {
		verdict := "confirms"
		if !v.Confirm {
			verdict = "rejects (" + v.Reason + ")"
		}
		w("- %s, %s, %s: %s\n", v.By, v.Role, verdict, oneLine(v.Note))
	}
	for _, p := range f.Proofs {
		w("- Reproduction by %s: **%s** (exit %d, app ready %v, target exercised %v, %ds)\n", p.By, p.Outcome, p.Exit, p.Ready, p.Exercised, p.Seconds)
	}
	if f.Fix != "" {
		w("\n**Fix**: %s\n", f.Fix)
	}
	w("\n")
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
