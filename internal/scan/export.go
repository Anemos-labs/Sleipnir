package scan

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Export files, in a scan's directory, beside the report.
const (
	CSVFile  = "findings.csv"
	HTMLFile = "report.html"
)

// csvHeader names the columns of findings.csv.
var csvHeader = []string{"id", "status", "severity", "category", "title", "reachability", "exploitability", "impact", "sink_file", "sink_line",
	"entry_file", "entry_line", "confirms", "rejects", "defended", "reproduced", "reject_reason", "claim", "fix", "not_verified_because"}

// CSV is findings.csv: one row per lead, in the report's order, for a spreadsheet or a tracker's import. A cell that a
// spreadsheet would read as a formula is prefixed with a quote, since every cell was written by an agent reading untrusted code.
func CSV(r *Run) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(csvHeader)
	for _, f := range Findings(r) {
		var entry, sink Cite
		if len(f.Trace) > 0 {
			entry, sink = f.Trace[0].Cite, f.Trace[len(f.Trace)-1].Cite
		}
		row := []string{f.ID, f.Status, f.Severity, f.Category, f.Title, f.Reach, f.Exploit, f.Impact, sink.File, strconv.Itoa(sink.Line),
			entry.File, strconv.Itoa(entry.Line), strconv.Itoa(f.Confirms), strconv.Itoa(f.Rejects), strconv.FormatBool(f.Defended),
			strconv.FormatBool(f.Reproduced), f.Reason, oneLine(f.Claim), oneLine(f.Fix), f.Deferred}
		for j := range row {
			row[j] = csvCell(row[j])
		}
		_ = w.Write(row)
	}
	w.Flush()
	return b.Bytes()
}

// csvCell keeps a spreadsheet from running a cell as a formula.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// HTML is report.html: the report as one page, laid out as an assessment, that reads on a screen and prints well (to paper or
// to a PDF). It has no script and loads nothing from elsewhere: the severity and status filters of its index are checkboxes the
// stylesheet reads, and every text in it is escaped.
func HTML(r *Run) string {
	d := r.DepthInfo()
	rep := Check(r)
	c := rep.Counts
	fs := Findings(r)
	sum := Summarize(fs)
	done := rep.Phase == PhaseDone
	list := bySeverity(Reportable(fs))
	var b strings.Builder
	e := html.EscapeString
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	kind := "Security assessment"
	if r.IsReview() {
		kind = "Security review of a change"
	}
	w("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	w("<title>%s · %s</title><style>%s</style></head><body>\n", e(kind), e(short(r.Commit)), reportCSS)

	// the cover: what was assessed, how, and where the work stands
	w("<header class=\"cover\"><p class=\"kicker\">%s</p><h1>%s</h1>", e(kind), e(coverTitle(r)))
	w("<dl class=\"facts\">")
	fact := func(k, v string) { w("<div><dt>%s</dt><dd>%s</dd></div>", e(k), v) }
	if r.IsReview() {
		fact("Change", fmt.Sprintf("<span class=\"mono\">%s..%s</span> · %d files", e(short(r.Base)), e(short(r.Commit)), len(r.Changes)))
	} else {
		fact("Commit", "<span class=\"mono\">"+e(short(r.Commit))+"</span>")
	}
	if r.Focus != "" {
		fact("Scope", "<span class=\"mono\">"+e(r.Focus)+"</span>")
	}
	fact("Code", e(fmt.Sprintf("%s files · %s functions · %s lines", thousands(c.Files), thousands(c.Funcs), thousands(c.Lines))))
	fact("Depth", "<b>"+e(d.Title)+"</b> · "+e(d.Summary))
	if done {
		fact("Status", "<span class=\"st st-done\">Complete</span>")
	} else {
		fact("Status", "<span class=\"st st-run\">In progress</span> · "+e(rep.Phase)+" phase; only confirmed findings are final")
	}
	fact("Reference", "<span class=\"mono\">"+e(r.Slug)+"</span>")
	w("</dl></header>\n")
	w("<nav class=\"toc\"><a href=\"#summary\">Summary</a><a href=\"#index\">Index</a><a href=\"#findings\">Findings</a><a href=\"#appendix\">Appendices</a><a href=\"#method\">Method</a></nav>\n")

	// the summary: the sentence, a tile per severity, the distribution, the categories and what happened to the leads
	w("<section id=\"summary\"><h2>Summary</h2><p class=\"headline\">%s</p>", e(sum.Headline(done)))
	w("<div class=\"tiles\">")
	for _, sev := range Severities {
		n := sum.BySeverity[sev]
		w("<div class=\"tile t-%s%s\"><b>%d</b><span>%s</span>", sev, map[bool]string{true: " zero", false: ""}[n == 0], n, e(title(sev)))
		if u := sum.Unproven[sev]; u > 0 {
			w("<small>%d not reproduced</small>", u)
		}
		w("</div>")
	}
	w("<div class=\"tile t-total\"><b>%d</b><span>Findings</span>", sum.Findings)
	if sum.Reproduced > 0 {
		w("<small>%d reproduced</small>", sum.Reproduced)
	}
	w("</div></div>")
	if sum.Findings > 0 {
		w("<div class=\"dist\" role=\"img\" aria-label=\"%s\">", e(distLabel(sum)))
		for _, sev := range Severities {
			if n := sum.BySeverity[sev]; n > 0 {
				w("<i class=\"b-%s\" style=\"flex:%d\" title=\"%d %s\"></i>", sev, n, n, sev)
			}
		}
		w("</div><div class=\"legend\">")
		for _, sev := range Severities {
			w("<span><i class=\"b-%s\"></i>%s %d%%</span>", sev, e(title(sev)), pct(sum.BySeverity[sev], sum.Findings))
		}
		w("</div>")
	}
	w("<div class=\"cols\">")
	if len(sum.Categories) > 0 {
		top := sum.Categories[0].Total
		w("<div class=\"card\"><h3>By category</h3><table class=\"bars\">")
		for _, cc := range sum.Categories {
			w("<tr><th>%s</th><td><div class=\"bar\" style=\"width:%d%%\">", e(cc.Category), max(4, cc.Total*100/max(top, 1)))
			for _, sev := range Severities {
				if n := cc.BySeverity[sev]; n > 0 {
					w("<i class=\"b-%s\" style=\"flex:%d\"></i>", sev, n)
				}
			}
			w("</div></td><td class=\"n\">%d</td></tr>", cc.Total)
		}
		w("</table></div>")
	}
	w("<div class=\"card\"><h3>Leads</h3><table class=\"bars funnel\">")
	funnel := [][2]any{{"Filed", c.Leads}, {"Confirmed by review", sum.Findings}, {"Reproduced", sum.Reproduced}, {"Disputed", sum.Disputed},
		{"Being verified", sum.Open}, {"Not verified", sum.Deferred}, {"Rejected", sum.Rejected}}
	for _, row := range funnel {
		n := row[1].(int)
		if n == 0 && row[0] != "Filed" && row[0] != "Confirmed by review" {
			continue
		}
		w("<tr><th>%s</th><td><div class=\"bar\" style=\"width:%d%%\"><i class=\"b-lead\" style=\"flex:1\"></i></div></td><td class=\"n\">%d</td></tr>", e(row[0].(string)), max(2, n*100/max(c.Leads, 1)), n)
	}
	w("</table></div></div></section>\n")

	// the index: one row per finding, filtered by severity and status
	w("<section id=\"index\"><h2>Index</h2>")
	if len(list) == 0 {
		w("<p class=\"muted\">No lead has been confirmed%s.</p>", map[bool]string{true: "", false: " yet"}[done])
	} else {
		sevN, stN := map[string]int{}, map[string]int{}
		for _, f := range list {
			sevN[f.Severity]++
			stN[f.Status]++
		}
		w("<div class=\"filters\" aria-label=\"filter the findings\"><span class=\"fl\">Severity</span>")
		for _, sev := range Severities {
			if sevN[sev] > 0 {
				w("<input type=\"checkbox\" id=\"fs-%s\" checked><label for=\"fs-%s\" class=\"chip c-%s\">%s <b>%d</b></label>", sev, sev, sev, e(title(sev)), sevN[sev])
			}
		}
		if len(stN) > 1 {
			w("<span class=\"fl\">Status</span>")
			for _, st := range []string{LeadConfirmed, LeadUnproven, LeadDisputed} {
				if stN[st] > 0 {
					w("<input type=\"checkbox\" id=\"ft-%s\" checked><label for=\"ft-%s\" class=\"chip\">%s <b>%d</b></label>", st, st, e(statusWord[st]), stN[st])
				}
			}
		}
		w("</div><div class=\"tablewrap\"><table class=\"idx\"><thead><tr><th>ID</th><th>Severity</th><th>Finding</th><th>Category</th><th>Location</th><th>Status</th></tr></thead><tbody>")
		for _, f := range list {
			w("<tr data-sev=\"%s\" data-st=\"%s\"><td class=\"mono\"><a href=\"#%s\">%s</a></td><td>%s</td><td><a href=\"#%s\">%s</a></td><td>%s</td><td class=\"mono loc\">%s</td><td>%s</td></tr>",
				e(f.Severity), e(f.Status), e(strings.ToLower(f.ID)), e(f.ID), sevPill(f.Severity), e(strings.ToLower(f.ID)), e(oneLine(f.Title)), e(f.Category), e(Location(f)), statusChip(f))
		}
		w("</tbody></table></div><p class=\"muted note-print\">Filters apply to the index and to the findings below; the printed report has every finding.</p>")
	}
	w("</section>\n")

	// the findings in full
	if len(list) > 0 {
		w("<section id=\"findings\"><h2>Findings</h2>")
		for _, f := range list {
			writeHTMLFinding(&b, f, d)
		}
		w("</section>\n")
	}

	// the leads that are not findings, the scope, the method
	w("<section id=\"appendix\"><h2>Appendices</h2>")
	rest := func(status string) []Finding {
		var out []Finding
		for _, f := range fs {
			if f.Status == status {
				out = append(out, f)
			}
		}
		return bySeverity(out)
	}
	letter := 'A'
	appendix := func(heading, intro, col string, list []Finding, note func(Finding) string) {
		if len(list) == 0 {
			return
		}
		w("<h3>%c. %s <span class=\"count\">%d</span></h3><p class=\"muted\">%s</p><div class=\"tablewrap\"><table class=\"idx plain\"><thead><tr><th>ID</th><th>Severity</th><th>Lead</th><th>Category</th><th>Location</th><th>%s</th></tr></thead><tbody>",
			letter, e(heading), len(list), e(intro), e(col))
		for _, f := range list {
			w("<tr><td class=\"mono\">%s</td><td>%s</td><td>%s</td><td>%s</td><td class=\"mono loc\">%s</td><td>%s</td></tr>", e(f.ID), sevPill(f.Severity), e(oneLine(f.Title)), e(f.Category), e(Location(f)), e(note(f)))
		}
		w("</tbody></table></div>")
		letter++
	}
	appendix("Being verified", "Filed and not yet ruled on by enough reviewers: not findings yet.", "Confirmations", rest(LeadOpen), func(f Finding) string {
		return fmt.Sprintf("%d of %d", f.Confirms, d.Verifiers)
	})
	appendix("Not verified", "Recorded after the scan reached its stopping point (its budget, or the end of the hunt), and kept unreviewed: leads to triage, not findings.", "Why", rest(LeadDeferred), func(f Finding) string { return f.Deferred })
	appendix("Rejected leads", "Leads the reviewers showed are not real, with the reason.", "Reason", rest(LeadRejected), func(f Finding) string {
		if f.Reason == "" {
			return "rejected"
		}
		return f.Reason
	})
	w("<h3>%c. Scope and coverage</h3><div class=\"cols\"><div class=\"card\"><table class=\"kv\">", letter)
	for _, kv := range [][2]string{{"Files", thousands(c.Files)}, {"Functions", thousands(c.Funcs)}, {"Lines", thousands(c.Lines)},
		{"Shards mapped", fmt.Sprintf("%d of %d", c.Mapped, c.Shards)}, {"Shards hunted", fmt.Sprintf("%d of %d", c.Hunted, c.Shards)},
		{"Relations between places", fmt.Sprintf("%s (%s inferred)", thousands(c.Edges), thousands(c.Inferred))}, {"Leads filed", thousands(c.Leads)}} {
		w("<tr><th>%s</th><td>%s</td></tr>", e(kv[0]), e(kv[1]))
	}
	w("</table></div><div class=\"card\"><h4>Attack surface</h4><table class=\"bars\">")
	topLane := 1
	for _, lane := range Lanes {
		topLane = max(topLane, c.Lanes[lane])
	}
	for _, lane := range Lanes {
		w("<tr><th>%s</th><td><div class=\"bar\" style=\"width:%d%%\"><i class=\"b-lane\" style=\"flex:1\"></i></div></td><td class=\"n\">%s</td></tr>", e(laneTitle[lane]), max(2, c.Lanes[lane]*100/topLane), thousands(c.Lanes[lane]))
	}
	w("</table></div></div>")
	if hot := hotFiles(r, 10); len(hot) > 0 {
		w("<h4>Files with the largest attack surface</h4><div class=\"tablewrap\"><table class=\"idx plain\"><thead><tr><th>File</th><th class=\"n\">Points</th><th class=\"n\">Entry points</th><th class=\"n\">Sinks</th></tr></thead><tbody>")
		for _, h := range hot {
			w("<tr><td class=\"mono loc\">%s</td><td class=\"n\">%d</td><td class=\"n\">%d</td><td class=\"n\">%d</td></tr>", e(h.File), h.Total, h.Lanes[LaneEntry], h.Lanes[LaneSink])
		}
		w("</tbody></table></div>")
	}
	if len(r.Inventory.Skipped) > 0 {
		var ks []string
		for k, n := range r.Inventory.Skipped {
			ks = append(ks, fmt.Sprintf("%d %s", n, k))
		}
		sort.Strings(ks)
		w("<p class=\"muted\">Left out of the scan: %s.</p>", e(strings.Join(ks, ", ")))
	}
	w("<p class=\"muted\">The attack surface point by point is in <span class=\"mono\">%s</span>, and every lead with its trace in <span class=\"mono\">%s</span>.</p></section>\n", MapFile, FindingsFile)
	w("<section id=\"method\"><h2>Method</h2><p>%s</p></section>\n", e(methodText(r)))
	w("<footer>Every quote in this report was checked against the file at commit <span class=\"mono\">%s</span>.</footer></body></html>\n", e(short(r.Commit)))
	return b.String()
}

// writeHTMLFinding writes one finding of report.html: its facts, the description, the trace, how it was checked, the fix and
// the reproducer that showed it.
func writeHTMLFinding(b *strings.Builder, f Finding, d Depth) {
	e := html.EscapeString
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	w("<article class=\"finding s-%s\" id=\"%s\" data-sev=\"%s\" data-st=\"%s\"><header><span class=\"mono fid\">%s</span>%s<h3>%s</h3></header>",
		e(f.Severity), e(strings.ToLower(f.ID)), e(f.Severity), e(f.Status), e(f.ID), sevPill(f.Severity), e(oneLine(f.Title)))
	w("<dl class=\"ff\"><div><dt>Status</dt><dd>%s</dd></div><div><dt>Category</dt><dd>%s</dd></div><div><dt>Location</dt><dd class=\"mono loc\">%s</dd></div>",
		statusChip(f), e(f.Category), e(Location(f)))
	w("<div><dt>Reachability</dt><dd>%s</dd></div><div><dt>Exploitability</dt><dd>%s</dd></div><div><dt>Impact</dt><dd>%s</dd></div></dl>", e(f.Reach), e(f.Exploit), e(f.Impact))
	w("<h4>Description</h4><p>%s</p>", e(strings.TrimSpace(f.Claim)))
	w("<h4>Trace</h4><ol class=\"trace\">")
	for _, s := range f.Trace {
		w("<li><div class=\"step\"><span class=\"mono loc\">%s:%d</span> %s</div><pre>%s</pre></li>", e(s.File), s.Line, e(s.Note), e(s.Quote))
	}
	w("</ol><h4>Verification</h4><p>%s</p>", e(Verification(f, d)))
	if len(f.Verdicts) > 0 {
		labels := RoleLabels(f.Verdicts)
		w("<ul class=\"rulings\">")
		for i, v := range f.Verdicts {
			verdict := "<span class=\"ok\">confirms</span>"
			if !v.Confirm {
				verdict = "<span class=\"no\">rejects: " + e(v.Reason) + "</span>"
			}
			w("<li><b>%s</b> %s · %s</li>", e(labels[i]), verdict, e(oneLine(v.Note)))
		}
		w("</ul>")
	}
	if f.Fix != "" {
		w("<h4>Remediation</h4><p>%s</p>", e(strings.TrimSpace(f.Fix)))
	}
	for _, p := range f.Proofs {
		if p.Outcome == ProofReproduced && p.Independent {
			w("<details class=\"repro\"><summary>Reproducer and its output</summary><pre>%s</pre><pre>%s</pre></details>", e(p.Script), e(p.Output))
			break
		}
	}
	w("<a class=\"back\" href=\"#index\">Back to the index</a></article>\n")
}

// statusWord is a reportable status as the index's filters name it.
var statusWord = map[string]string{LeadConfirmed: "Confirmed", LeadUnproven: "Not reproduced", LeadDisputed: "Disputed"}

// coverTitle is the report's title: what was assessed, by its name and the folder the scan covers.
func coverTitle(r *Run) string {
	name := r.Name
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "Commit " + short(r.Commit)
	}
	if r.Focus != "" {
		return name + " / " + r.Focus
	}
	return name
}

func sevPill(sev string) string {
	if !oneOf(sev, Severities) {
		sev = SevLow
	}
	return "<span class=\"sev c-" + sev + "\">" + sev + "</span>"
}

func statusChip(f Finding) string {
	cls := map[string]string{LeadConfirmed: "st-done", LeadUnproven: "st-run", LeadDisputed: "st-run", LeadRejected: "st-off", LeadDeferred: "st-off", LeadOpen: "st-run"}[f.Status]
	return "<span class=\"st " + cls + "\">" + html.EscapeString(StatusLabel(f)) + "</span>"
}

func pct(n, of int) int {
	if of == 0 {
		return 0
	}
	return (n*100 + of/2) / of
}

// distLabel says the severity distribution in words, for a reader that does not see the bar.
func distLabel(s Summary) string {
	var parts []string
	for _, sev := range Severities {
		parts = append(parts, fmt.Sprintf("%d %s", s.BySeverity[sev], sev))
	}
	return "findings by severity: " + strings.Join(parts, ", ")
}

// reportCSS is report.html's style: the product's dark palette on a screen, a light one on paper, each finding kept on one
// page where it fits. The index's filters are checkboxes read with :has(), so the page needs no script.
const reportCSS = `:root{color-scheme:dark;--bg:#07090f;--panel:#0b0e17;--panel2:#0f1424;--line:#1f2437;--line2:#2d3558;--fg:#c0caf5;--fg2:#a1abd4;--dim:#7d87b2;--bright:#e6eaff;--well:#05070c;--accent:#bb9af7;--link:#7dcfff;
--critical:#f7768e;--high:#ff9e64;--medium:#e0af68;--low:#7aa2f7;--ok:#9ece6a;--lead:#565f89;--lane:#73daca;
--f-lab:'Barlow Condensed','Nimbus Sans Narrow','Liberation Sans Narrow','Helvetica Neue',Arial,sans-serif;--f-ui:'Barlow Semi Condensed','Helvetica Neue',system-ui,-apple-system,'Segoe UI',sans-serif;--f-num:'JetBrains Mono','IBM Plex Mono',ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
*{box-sizing:border-box}html{background:var(--bg)}body{margin:0 auto;max-width:1100px;padding:28px 20px 48px;font:400 14px/1.55 var(--f-ui);color:var(--fg);background:var(--bg)}
a{color:var(--link);text-decoration:none}a:hover{text-decoration:underline}.mono,pre{font-family:var(--f-num)}.muted{color:var(--dim);font-size:12.5px}
h1{font:600 30px/1.15 var(--f-ui);color:var(--bright);margin:4px 0 18px;overflow-wrap:anywhere}
h2{font:600 13px/1 var(--f-lab);letter-spacing:.12em;text-transform:uppercase;color:var(--fg2);margin:40px 0 14px;padding-bottom:8px;border-bottom:1px solid var(--line2)}
h3{font:600 16px/1.3 var(--f-ui);color:var(--bright);margin:22px 0 8px}h4{font:600 11px/1 var(--f-lab);letter-spacing:.1em;text-transform:uppercase;color:var(--dim);margin:18px 0 6px}
.kicker{margin:0;font:600 12px/1 var(--f-lab);letter-spacing:.16em;text-transform:uppercase;color:var(--accent)}
.cover{padding:26px 26px 20px;border:1px solid var(--line2);border-radius:4px;background:linear-gradient(180deg,var(--panel2),var(--panel))}
.facts{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px 22px;margin:0}.facts div{min-width:0}
dt{font:600 10.5px/1 var(--f-lab);letter-spacing:.1em;text-transform:uppercase;color:var(--dim);margin-bottom:4px}dd{margin:0;color:var(--fg);overflow-wrap:anywhere}
.toc{display:flex;flex-wrap:wrap;gap:4px 18px;margin:14px 2px 0;font:600 12px var(--f-lab);letter-spacing:.08em;text-transform:uppercase}
.headline{font-size:16px;line-height:1.55;color:var(--bright);max-width:860px;margin:0 0 16px}
.tiles{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));gap:10px}
.tile{display:flex;flex-direction:column;gap:3px;padding:12px 14px;border:1px solid var(--line);border-top:3px solid var(--sc,var(--line2));border-radius:3px;background:var(--panel)}
.tile b{font:600 28px/1 var(--f-num);color:var(--sc,var(--bright))}.tile span{font:600 11px/1 var(--f-lab);letter-spacing:.1em;text-transform:uppercase;color:var(--fg2)}.tile small{font-size:11.5px;color:var(--dim)}
.tile.zero b{color:var(--dim)}.t-critical{--sc:var(--critical)}.t-high{--sc:var(--high)}.t-medium{--sc:var(--medium)}.t-low{--sc:var(--low)}.t-total{--sc:var(--bright)}
.dist{display:flex;height:12px;margin:16px 0 6px;border-radius:2px;overflow:hidden;gap:2px}.dist i,.bar i{display:block;min-width:3px}
.b-critical{background:var(--critical)}.b-high{background:var(--high)}.b-medium{background:var(--medium)}.b-low{background:var(--low)}.b-lead{background:var(--lead)}.b-lane{background:var(--lane)}
.legend{display:flex;flex-wrap:wrap;gap:4px 16px;font-size:12px;color:var(--fg2)}.legend i{display:inline-block;width:9px;height:9px;border-radius:1px;margin-right:6px;vertical-align:0}
.cols{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:14px;margin-top:18px}.card{border:1px solid var(--line);border-radius:3px;padding:12px 14px;background:var(--panel);min-width:0}.card h3,.card h4{margin:0 0 10px}
.card h3{font:600 11px/1 var(--f-lab);letter-spacing:.1em;text-transform:uppercase;color:var(--dim)}
table{border-collapse:collapse;width:100%}.bars th{font-weight:500;text-align:left;padding:3px 10px 3px 0;white-space:nowrap;width:1%;color:var(--fg2);font-size:12.5px}.bars td{padding:3px 0}.bars td:nth-child(2){padding-right:4px}
.bar{display:flex;height:10px;gap:1px;border-radius:1px;overflow:hidden}.n{text-align:right;font:500 12px var(--f-num);color:var(--fg);width:1%;padding-left:10px;white-space:nowrap}
.kv th{text-align:left;font-weight:500;color:var(--fg2);padding:4px 10px 4px 0;font-size:12.5px}.kv td{text-align:right;font:500 12.5px var(--f-num)}.kv tr+tr{border-top:1px solid var(--line)}
.filters{display:flex;flex-wrap:wrap;align-items:center;gap:6px;margin:0 0 10px}.filters input{position:absolute;opacity:0;pointer-events:none}.fl{font:600 10.5px var(--f-lab);letter-spacing:.1em;text-transform:uppercase;color:var(--dim);margin:0 2px 0 8px}.fl:first-child{margin-left:0}
.chip{display:inline-flex;gap:6px;align-items:center;padding:3px 9px;border:1px solid var(--line2);border-radius:2px;font:600 11px var(--f-lab);letter-spacing:.06em;text-transform:uppercase;color:var(--fg2);cursor:pointer;user-select:none}
.chip b{font:500 11px var(--f-num);color:var(--dim)}input:checked+.chip{background:var(--panel2);color:var(--bright);border-color:currentColor}input:checked+.chip.c-critical{color:var(--critical)}input:checked+.chip.c-high{color:var(--high)}input:checked+.chip.c-medium{color:var(--medium)}input:checked+.chip.c-low{color:var(--low)}
input:not(:checked)+.chip{opacity:.55;text-decoration:line-through}input:focus-visible+.chip{outline:2px solid var(--link);outline-offset:1px}
.tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:3px}.idx{font-size:13px}.idx th{position:sticky;top:0;text-align:left;font:600 10.5px var(--f-lab);letter-spacing:.1em;text-transform:uppercase;color:var(--dim);background:var(--panel2);padding:8px 10px;white-space:nowrap}
.idx td{padding:7px 10px;border-top:1px solid var(--line);vertical-align:top}.idx tbody tr:hover{background:var(--panel)}.idx td a{color:var(--bright)}.idx td.mono a{color:var(--link)}.loc{font-size:11.5px;color:var(--fg2);overflow-wrap:anywhere;word-break:break-word}
.sev{display:inline-block;padding:1px 6px;border:1px solid currentColor;border-radius:2px;font:600 10px/1.5 var(--f-lab);letter-spacing:.08em;text-transform:uppercase;white-space:nowrap}.c-critical{color:var(--critical)}.c-high{color:var(--high)}.c-medium{color:var(--medium)}.c-low{color:var(--low)}
.st{font-size:12px;white-space:nowrap;color:var(--fg2)}.st::before{content:'';display:inline-block;width:7px;height:7px;border-radius:50%;margin-right:6px;background:var(--dim);vertical-align:1px}.st-done::before{background:var(--ok)}.st-run::before{background:var(--medium)}.st-off::before{background:var(--lead)}
.finding{margin:0 0 18px;padding:16px 18px 12px;border:1px solid var(--line);border-left:3px solid var(--sc,var(--line2));border-radius:3px;background:var(--panel);scroll-margin-top:12px}
.finding.s-critical{--sc:var(--critical)}.finding.s-high{--sc:var(--high)}.finding.s-medium{--sc:var(--medium)}.finding.s-low{--sc:var(--low)}
.finding header{display:flex;flex-wrap:wrap;align-items:center;gap:8px}.finding header h3{margin:0;flex:1 1 320px}.fid{font-size:12px;color:var(--dim)}
.ff{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px 18px;margin:14px 0 4px;padding:10px 0;border-top:1px solid var(--line);border-bottom:1px solid var(--line)}
.finding p{margin:0;max-width:900px}.trace{margin:0;padding-left:20px}.trace li{margin:0 0 8px}.step{font-size:13px}
pre{margin:4px 0 0;padding:8px 10px;background:var(--well);border:1px solid var(--line);border-radius:2px;white-space:pre-wrap;word-break:break-word;font-size:12px;line-height:1.5;color:var(--fg)}
.rulings{margin:8px 0 0;padding-left:18px;font-size:13px}.rulings li{margin:2px 0}.ok{color:var(--ok)}.no{color:var(--critical)}
.repro{margin-top:12px}.repro summary{cursor:pointer;font:600 11px var(--f-lab);letter-spacing:.1em;text-transform:uppercase;color:var(--fg2)}
.back{display:inline-block;margin-top:12px;font-size:12px}.count{font:500 13px var(--f-num);color:var(--dim)}
footer{margin-top:40px;padding-top:12px;border-top:1px solid var(--line);color:var(--dim);font-size:12px}
@media screen{body:has(#fs-critical:not(:checked)) [data-sev=critical],body:has(#fs-high:not(:checked)) [data-sev=high],body:has(#fs-medium:not(:checked)) [data-sev=medium],body:has(#fs-low:not(:checked)) [data-sev=low],
body:has(#ft-confirmed:not(:checked)) [data-st=confirmed],body:has(#ft-unproven:not(:checked)) [data-st=unproven],body:has(#ft-disputed:not(:checked)) [data-st=disputed]{display:none}}
@media (max-width:720px){body{padding:16px 14px 36px}h1{font-size:24px}.tiles{grid-template-columns:repeat(2,minmax(0,1fr))}.tile.t-total{grid-column:1/-1}.cover{padding:18px}.idx th:nth-child(4),.idx td:nth-child(4){display:none}}
@media print{:root{color-scheme:light;--bg:#fff;--panel:#fff;--panel2:#f4f5f8;--line:#d9dce4;--line2:#c4c8d4;--fg:#1d2230;--fg2:#3b4254;--dim:#646b7e;--bright:#0d1220;--well:#f6f7f9;--accent:#5b3fc4;--link:#1d4ed8;--critical:#b42318;--high:#c2410c;--medium:#a16207;--low:#1d4ed8;--ok:#15803d;--lead:#9aa1b2;--lane:#0f766e}
body{max-width:none;padding:0;font-size:11.5pt}.toc,.filters,.back,.note-print{display:none}.cover{background:none}.finding{break-inside:avoid-page}h2{break-after:avoid}.tablewrap{overflow:visible;border:0}.idx th{position:static}
.repro[open] summary~*,.repro pre{display:block}}`
