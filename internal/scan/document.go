package scan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The report's document: what every rendering of a scan's report (REPORT.md, report.html, the Deep scan view) says, in the
// same words and the same order. A report reads as an assessment of the code: a summary first, the findings by severity with an
// index, each finding with what it is, where, how it was checked and how to fix it, then what was not verified, and the scope.
// It names no agent: a verdict is given by its role, and the work by what it covered.

// Summary is a scan's findings counted for the first page of its report.
type Summary struct {
	// Findings are the confirmed and unproven leads; BySeverity counts them, and Unproven the unproven ones among them.
	Findings   int            `json:"findings"`
	BySeverity map[string]int `json:"bySeverity"`
	Unproven   map[string]int `json:"unproven"`
	Reproduced int            `json:"reproduced"`
	// Categories are the findings' categories, the most frequent first.
	Categories []CategoryCount `json:"categories"`
	Disputed   int             `json:"disputed"`
	Open       int             `json:"open"`
	Deferred   int             `json:"deferred"`
	Rejected   int             `json:"rejected"`
}

// CategoryCount is how many findings of a category the scan has, by severity.
type CategoryCount struct {
	Category   string         `json:"category"`
	Total      int            `json:"total"`
	BySeverity map[string]int `json:"bySeverity"`
}

// Summarize counts the findings fs.
func Summarize(fs []Finding) Summary {
	s := Summary{BySeverity: map[string]int{}, Unproven: map[string]int{}}
	cats := map[string]*CategoryCount{}
	for _, f := range fs {
		switch f.Status {
		case LeadConfirmed, LeadUnproven:
			s.Findings++
			s.BySeverity[f.Severity]++
			if f.Status == LeadUnproven {
				s.Unproven[f.Severity]++
			}
			if f.Reproduced {
				s.Reproduced++
			}
			c := cats[f.Category]
			if c == nil {
				c = &CategoryCount{Category: f.Category, BySeverity: map[string]int{}}
				cats[f.Category] = c
			}
			c.Total++
			c.BySeverity[f.Severity]++
		case LeadDisputed:
			s.Disputed++
		case LeadOpen:
			s.Open++
		case LeadDeferred:
			s.Deferred++
		case LeadRejected:
			s.Rejected++
		}
	}
	for _, c := range cats {
		s.Categories = append(s.Categories, *c)
	}
	sort.Slice(s.Categories, func(i, j int) bool {
		a, b := s.Categories[i], s.Categories[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		for _, sev := range Severities {
			if a.BySeverity[sev] != b.BySeverity[sev] {
				return a.BySeverity[sev] > b.BySeverity[sev]
			}
		}
		return a.Category < b.Category
	})
	return s
}

// Headline is the summary in a sentence: how many findings, how severe, and what else the scan left.
func (s Summary) Headline(done bool) string {
	var b strings.Builder
	if s.Findings == 0 {
		b.WriteString("The scan has no confirmed finding")
		if !done {
			b.WriteString(" yet")
		}
	} else {
		var parts []string
		for _, sev := range Severities {
			if n := s.BySeverity[sev]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, sev))
			}
		}
		fmt.Fprintf(&b, "The scan confirmed %d %s: %s", s.Findings, plural(s.Findings, "finding", "findings"), joinAnd(parts))
		if n := sumMap(s.Unproven); n > 0 {
			fmt.Fprintf(&b, ". %d of them %s confirmed by review but not reproduced in the sandbox", n, plural(n, "was", "were"))
		}
	}
	b.WriteString(".")
	var rest []string
	if s.Disputed > 0 {
		rest = append(rest, fmt.Sprintf("%d %s disputed by the verifiers", s.Disputed, plural(s.Disputed, "lead is", "leads are")))
	}
	if s.Open > 0 {
		rest = append(rest, fmt.Sprintf("%d %s still being verified", s.Open, plural(s.Open, "lead is", "leads are")))
	}
	if s.Deferred > 0 {
		rest = append(rest, fmt.Sprintf("%d %s recorded without verification", s.Deferred, plural(s.Deferred, "lead was", "leads were")))
	}
	if s.Rejected > 0 {
		rest = append(rest, fmt.Sprintf("%d %s rejected on review", s.Rejected, plural(s.Rejected, "lead was", "leads were")))
	}
	if len(rest) > 0 {
		r := joinAnd(rest)
		b.WriteString(" " + strings.ToUpper(r[:1]) + r[1:] + ".")
	}
	return b.String()
}

// StatusLabel is a finding's status in a report's words.
func StatusLabel(f Finding) string {
	switch f.Status {
	case LeadConfirmed:
		if f.Reproduced {
			return "Confirmed, reproduced"
		}
		return "Confirmed"
	case LeadUnproven:
		return "Not reproduced"
	case LeadDisputed:
		return "Disputed"
	case LeadOpen:
		return "Being verified"
	case LeadDeferred:
		return "Not verified"
	case LeadRejected:
		if f.Reason != "" {
			return "Rejected: " + f.Reason
		}
		return "Rejected"
	}
	return f.Status
}

// Location is where a finding does harm: in a review its anchor on the change, else the last step of its trace.
func Location(f Finding) string {
	if f.Anchor != nil {
		return fmt.Sprintf("%s:%d", f.Anchor.File, f.Anchor.Line)
	}
	if len(f.Trace) == 0 {
		return ""
	}
	s := f.Trace[len(f.Trace)-1]
	return fmt.Sprintf("%s:%d", s.File, s.Line)
}

// Verification says how a finding was checked, in a sentence: by how many independent verifiers, a defender among them, and
// whether a reproducer showed it.
func Verification(f Finding, d Depth) string {
	var b strings.Builder
	switch {
	case f.Confirms == 0 && f.Rejects == 0:
		b.WriteString("Not yet reviewed")
	case f.Rejects == 0:
		fmt.Fprintf(&b, "Confirmed by %d independent %s", f.Confirms, plural(f.Confirms, "reviewer", "reviewers"))
	default:
		fmt.Fprintf(&b, "%d independent %s confirmed it and %d rejected it", f.Confirms, plural(f.Confirms, "reviewer", "reviewers"), f.Rejects)
	}
	if f.Defended {
		b.WriteString(", one of them a defender who looked for the reason it is not real")
	}
	b.WriteString(".")
	switch {
	case f.Reproduced:
		b.WriteString(" An independent reproducer showed the harm in the sandbox.")
	case len(f.Proofs) > 0:
		fmt.Fprintf(&b, " %d %s did not show the harm in the sandbox.", len(f.Proofs), plural(len(f.Proofs), "reproduction", "reproductions"))
	case f.Status == LeadUnproven:
		b.WriteString(" No reproduction was run.")
	case d.ProveFrom != "" && d.needsProof(f.Severity) && f.Status != LeadConfirmed:
		fmt.Fprintf(&b, " At %s depth a finding of this severity is also reproduced.", d.ID)
	}
	return b.String()
}

// RoleLabels names a finding's verdicts by role, in order: "Defender", "Reviewer 1", "Reviewer 2".
func RoleLabels(vs []Ruling) []string {
	out := make([]string, len(vs))
	n := 0
	for i, v := range vs {
		if v.Role == RoleDefender {
			out[i] = "Defender"
			continue
		}
		n++
		out[i] = fmt.Sprintf("Reviewer %d", n)
	}
	return out
}

// Reportable are the findings a report lists in full: the confirmed, unproven and disputed leads.
func Reportable(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Status == LeadConfirmed || f.Status == LeadUnproven || f.Status == LeadDisputed {
			out = append(out, f)
		}
	}
	return out
}

// bySeverity sorts findings worst first: by severity, then by CVSS score, then by id in the order they were filed.
func bySeverity(fs []Finding) []Finding {
	out := append([]Finding(nil), fs...)
	sort.SliceStable(out, func(i, j int) bool { return worse(out[i], out[j]) })
	return out
}

// leadNum is the number of a lead id ("L12" is 12).
func leadNum(id string) int {
	n := 0
	for _, c := range strings.TrimLeft(id, "Ll") {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// hotFiles are the files with the most attack-surface points, at most n, with their counts by lane.
func hotFiles(r *Run, n int) []fileSurface {
	m := map[string]*fileSurface{}
	for _, p := range r.Points {
		f := m[p.File]
		if f == nil {
			f = &fileSurface{File: p.File, Lanes: map[string]int{}}
			m[p.File] = f
		}
		f.Total++
		f.Lanes[p.Lane]++
	}
	var out []fileSurface
	for _, f := range m {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].File < out[j].File
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// fileSurface is one file's attack surface: its points by lane.
type fileSurface struct {
	File  string
	Total int
	Lanes map[string]int
}

func sumMap(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// joinAnd joins parts as a list in a sentence: "a", "a and b", "a, b and c".
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// title is a word with its first letter upper case.
func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// thousands writes n with thin separators: 12,345.
func thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// teamIDRE matches the ids a team's agents go by (mgr, be-1, rv-12) wherever an agent wrote one into a note.
var teamIDRE = regexp.MustCompile(`\b(?:mgr|[a-z]{2,4}-\d{1,3})\b`)

// redactor is what keeps agent names out of a report's text: it replaces every name the ledger records an agent by, and every
// word in the form of a team's agent id, with "a reviewer". A report says what was checked, not which agent did it. Code
// (quotes) is never rewritten, and a recorded name that is an ordinary word (several letters and no digit, like "main") is left
// alone, since it would also be a word of the text.
func (r *Run) redactor() func(string) string {
	names := map[string]bool{}
	add := func(n string) {
		if n == "" {
			return
		}
		if strings.ContainsAny(n, "0123456789") || len(n) <= 3 {
			names[n] = true
		}
	}
	for _, s := range r.Shards {
		add(s.Claimed)
		add(s.MappedBy)
		add(s.HuntedBy)
	}
	for _, p := range r.Points {
		add(p.By)
	}
	for _, e := range r.Edges {
		add(e.By)
	}
	for _, l := range r.Leads {
		add(l.By)
		for _, v := range l.Verdicts {
			add(v.By)
		}
		for _, p := range l.Proofs {
			add(p.By)
		}
	}
	var alts []string
	for n := range names {
		alts = append(alts, regexp.QuoteMeta(n))
	}
	sort.Slice(alts, func(i, j int) bool {
		return len(alts[i]) > len(alts[j]) || len(alts[i]) == len(alts[j]) && alts[i] < alts[j]
	})
	var own *regexp.Regexp
	if len(alts) > 0 {
		own = regexp.MustCompile(`(?:^|\b)(?:` + strings.Join(alts, "|") + `)(?:\b|$)`)
	}
	return func(s string) string {
		if own != nil {
			s = own.ReplaceAllString(s, "a reviewer")
		}
		return teamIDRE.ReplaceAllString(s, "a reviewer")
	}
}
