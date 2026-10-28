package scan

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
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
	"entry_file", "entry_line", "confirms", "rejects", "defended", "reproduced", "reject_reason", "claim", "fix", "filed_by"}

// CSV is findings.csv: one row per lead, in the report's order, for a spreadsheet or a tracker's import. A cell that a
// spreadsheet would read as a formula is prefixed with a quote, since every cell was written by an agent reading untrusted code.
func CSV(r *Run) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(csvHeader)
	for _, f := range Findings(r) {
		l := r.Lead(f.ID)
		confirms, rejects, defended := l.Tally()
		var entry, sink Cite
		if len(f.Trace) > 0 {
			entry, sink = f.Trace[0].Cite, f.Trace[len(f.Trace)-1].Cite
		}
		row := []string{f.ID, f.Status, f.Severity, f.Category, f.Title, f.Reach, f.Exploit, f.Impact, sink.File, strconv.Itoa(sink.Line),
			entry.File, strconv.Itoa(entry.Line), strconv.Itoa(confirms), strconv.Itoa(rejects), strconv.FormatBool(defended),
			strconv.FormatBool(l.Reproduced()), f.Reason, oneLine(f.Claim), oneLine(f.Fix), f.By}
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

// HTML is report.html: the whole report as one page that prints well (to paper or to a PDF), with no script and nothing
// loaded from elsewhere. Every text in it is escaped.
func HTML(r *Run) string {
	d := r.DepthInfo()
	rep := Check(r)
	c := rep.Counts
	var b strings.Builder
	e := html.EscapeString
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	w("<title>Deep scan %s</title><style>%s</style></head><body>\n", e(r.Slug), reportCSS)
	w("<header><h1>Deep scan <span class=\"mono\">%s</span></h1><p>Commit <span class=\"mono\">%s</span>", e(r.Slug), e(short(r.Commit)))
	if r.Focus != "" {
		w(" · focus <span class=\"mono\">%s</span>", e(r.Focus))
	}
	w(" · %s depth (%s) · phase <b>%s</b></p>", e(d.Title), e(d.Summary), e(rep.Phase))
	if rep.Phase != PhaseDone {
		w("<p class=\"note\">This scan was still running when this report was made: only confirmed findings are final.</p>")
	}
	w("<table class=\"nums\"><tr><th>Files</th><th>Functions</th><th>Lines</th><th>Shards mapped</th><th>Shards hunted</th><th>Points</th><th>Edges</th><th>Leads</th><th>Confirmed</th><th>Unproven</th><th>Rejected</th></tr>")
	w("<tr><td>%d</td><td>%d</td><td>%d</td><td>%d/%d</td><td>%d/%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td></tr></table></header>\n",
		c.Files, c.Funcs, c.Lines, c.Mapped, c.Shards, c.Hunted, c.Shards, c.Points, c.Edges, c.Leads, c.Confirmed, c.Unproven, c.Rejected)
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
		w("<section><h2>%s (%d)</h2><p class=\"intro\">%s</p>\n", e(title), len(list), e(intro))
		if !full {
			w("<ul>")
			for _, f := range list {
				w("<li><b>%s</b> %s <span class=\"sev %s\">%s</span> %s", e(f.ID), e(f.Title), e(f.Severity), e(f.Severity), e(f.Category))
				if f.Reason != "" {
					w(": rejected as %s", e(f.Reason))
				}
				w("</li>")
			}
			w("</ul></section>\n")
			return
		}
		for _, f := range list {
			w("<article><h3><span class=\"sev %s\">%s</span> %s · %s</h3>", e(f.Severity), e(f.Severity), e(f.ID), e(f.Title))
			w("<p class=\"meta\">%s · reachability %s · exploitability %s · impact %s</p><p>%s</p><h4>Trace</h4><ol>", e(f.Category), e(f.Reach), e(f.Exploit), e(f.Impact), e(f.Claim))
			for _, s := range f.Trace {
				w("<li><span class=\"mono\">%s:%d</span> %s<pre>%s</pre></li>", e(s.File), s.Line, e(s.Note), e(s.Quote))
			}
			w("</ol>")
			if len(f.Verdicts) > 0 {
				w("<h4>Verdicts</h4><ul>")
				for _, v := range f.Verdicts {
					verdict := "confirms"
					if !v.Confirm {
						verdict = "rejects (" + v.Reason + ")"
					}
					w("<li><b>%s</b>, %s, %s: %s</li>", e(v.By), e(v.Role), e(verdict), e(oneLine(v.Note)))
				}
				w("</ul>")
			}
			for _, p := range f.Proofs {
				if p.Outcome != ProofReproduced {
					continue
				}
				w("<h4>Reproducer (by %s, %s)</h4><pre>%s</pre><h4>Its output</h4><pre>%s</pre>", e(p.By), e(p.Outcome), e(p.Script), e(p.Output))
				break
			}
			if f.Fix != "" {
				w("<h4>Fix</h4><p>%s</p>", e(f.Fix))
			}
			w("</article>\n")
		}
		w("</section>\n")
	}
	section("Findings", LeadConfirmed, "Confirmed by agents that did not file them"+proofNote(d)+".", true)
	section("Unproven", LeadUnproven, "Confirmed by the verifiers, but not reproduced in the sandbox: treat as likely, not shown.", true)
	section("Still being verified", LeadOpen, "Filed, not yet ruled on by enough verifiers.", false)
	section("Rejected", LeadRejected, "Leads the verifiers rejected, with the reason.", false)
	if c.Leads == 0 {
		w("<section><h2>Findings</h2><p>No lead has been filed yet.</p></section>\n")
	}
	w("<section><h2>Attack surface</h2>")
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
		w("<h3>%s (%d)</h3><ul>", e(laneTitle[lane]), len(ps))
		for _, p := range ps {
			w("<li><b>%s</b> %s: <span class=\"mono\">%s:%d</span>", e(p.ID), e(p.Title), e(p.File), p.Line)
			if p.Note != "" {
				w(": %s", e(oneLine(p.Note)))
			}
			w("</li>")
		}
		w("</ul>")
	}
	w("</section>\n<section><h2>Map</h2><ul>")
	for _, s := range r.Shards {
		if s.Mapped != "" {
			w("<li><b>%s</b> %s (%d functions): %s</li>", e(s.ID), e(shardSpan(s)), s.Funcs, e(oneLine(s.Mapped)))
		}
	}
	w("</ul></section>\n<footer>Every quote in this report was checked by the harness against the file at commit %s.</footer></body></html>\n", e(short(r.Commit)))
	return b.String()
}

// reportCSS is report.html's style: readable on a screen, and on paper with each finding kept on one page where it fits.
const reportCSS = `body{font:14px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;color:#1b1f24;max-width:960px;margin:24px auto;padding:0 16px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:18px;border-bottom:1px solid #d0d7de;padding-bottom:4px;margin-top:28px}h3{font-size:15px;margin:18px 0 4px}h4{font-size:12px;text-transform:uppercase;letter-spacing:.06em;color:#57606a;margin:12px 0 4px}
.mono,pre{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}pre{background:#f6f8fa;border:1px solid #d0d7de;border-radius:4px;padding:6px 8px;white-space:pre-wrap;word-break:break-word;font-size:12px;margin:4px 0}
.nums{border-collapse:collapse;margin:10px 0;font-size:12px}.nums th,.nums td{border:1px solid #d0d7de;padding:4px 8px;text-align:center}.nums th{background:#f6f8fa;font-weight:600}
.sev{display:inline-block;padding:0 6px;border:1px solid currentColor;border-radius:3px;font-size:11px;font-weight:700;text-transform:uppercase}.critical{color:#b42318}.high{color:#c4320a}.medium{color:#9a6700}.low{color:#57606a}
.meta,.intro{color:#57606a;font-size:12.5px}.note{border-left:3px solid #bf8700;padding:4px 10px;background:#fff8c5}article{border-top:1px solid #eaeef2;padding-top:6px}
footer{margin-top:32px;color:#57606a;font-size:12px}
@media print{body{margin:0;max-width:none}article{break-inside:avoid-page}h2{break-after:avoid}pre{max-height:none}}`
