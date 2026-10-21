package research

import (
	"fmt"
	"sort"
	"strings"
)

// Phases of a run, in order.
const (
	PhasePlan    = "plan"
	PhaseGather  = "gather"
	PhaseWrite   = "write"
	PhaseVerify  = "verify"
	PhasePublish = "publish"
	PhaseDone    = "done"
)

// Phases lists the phases in order.
var Phases = []string{PhasePlan, PhaseGather, PhaseWrite, PhaseVerify, PhasePublish, PhaseDone}

// Problem is one thing a run's wiki or ledger lacks. Phase is the phase it belongs to; Page names a page when it is one page's.
type Problem struct {
	Phase string `json:"phase"`
	Page  string `json:"page,omitempty"`
	Text  string `json:"text"`
}

// PageReport is what checking one written page found.
type PageReport struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Words    int      `json:"words"`
	Cites    int      `json:"cites"`
	Links    int      `json:"links"`
	Inbound  int      `json:"inbound"`
	Uncited  int      `json:"uncited"`
	Prose    int      `json:"prose"`
	Passed   int      `json:"passed"`
	Unknown  []string `json:"unknown,omitempty"`
	Reviewed bool     `json:"reviewed"`
}

// Report is a run's state measured against its depth: the phase it is in, what blocks each phase, and the numbers.
type Report struct {
	Phase    string       `json:"phase"`
	Problems []Problem    `json:"problems"`
	Warnings []Problem    `json:"warnings"`
	Pages    []PageReport `json:"pages"`
	Counts   Counts       `json:"counts"`
}

// Counts are a run's numbers next to the bars of its depth.
type Counts struct {
	Sources     int `json:"sources"`
	Domains     int `json:"domains"`
	Findings    int `json:"findings"`
	Snippets    int `json:"snippets"`
	Projects    int `json:"projects"`
	Quotes      int `json:"quotes"`
	OpenGaps    int `json:"open_gaps"`
	Round       int `json:"round"`
	Pages       int `json:"pages"`
	Planned     int `json:"planned"`
	Reviewed    int `json:"reviewed"`
	Queries     int `json:"queries"`
	Words       int `json:"words"`
	Contradicts int `json:"contradicts"`
}

// Check measures a run: its ledger and its written pages (read from root) against its depth.
func Check(root string, r *Run) Report {
	d := r.DepthInfo()
	var rep Report
	add := func(phase, page, format string, args ...any) {
		rep.Problems = append(rep.Problems, Problem{Phase: phase, Page: page, Text: fmt.Sprintf(format, args...)})
	}
	warn := func(phase, page, format string, args ...any) {
		rep.Warnings = append(rep.Warnings, Problem{Phase: phase, Page: page, Text: fmt.Sprintf(format, args...)})
	}
	c := &rep.Counts
	c.Sources, c.Domains, c.Findings, c.Snippets = len(r.Sources), r.Domains(), len(r.Findings), len(r.Snippets)
	c.Round, c.Pages, c.Queries = r.Round(), len(r.Pages), len(r.Queries)
	for _, s := range r.Sources {
		c.Quotes += len(s.Quotes)
	}
	for _, g := range r.Gaps {
		if g.Open {
			c.OpenGaps++
		}
	}
	for _, f := range r.Findings {
		if len(f.Contradicts) > 0 {
			c.Contradicts++
		}
	}
	projects := map[string]bool{}
	for _, sn := range r.Snippets {
		projects[strings.ToLower(sn.Project)] = true
	}
	c.Projects = len(projects)

	// plan
	if r.Plan == nil {
		add(PhasePlan, "", "no plan yet: record the perspectives, %d research tracks with their questions, and the wiki's pages (action plan)", d.Tracks)
	} else {
		p := r.Plan
		c.Planned = len(p.Pages)
		if len(p.Perspectives) < d.Perspectives {
			add(PhasePlan, "", "the plan names %d perspectives; %s research needs %d", len(p.Perspectives), d.ID, d.Perspectives)
		}
		if len(p.Tracks) < d.Tracks {
			add(PhasePlan, "", "the plan has %d research tracks; %s research runs %d in parallel", len(p.Tracks), d.ID, d.Tracks)
		}
		qs := 0
		for _, t := range p.Tracks {
			qs += len(t.Questions)
		}
		if qs < d.Questions {
			add(PhasePlan, "", "the tracks ask %d questions in all; %s research asks at least %d", qs, d.ID, d.Questions)
		}
		body := 0
		hasIndex := false
		for _, pg := range p.Pages {
			if pg.ID == "index" {
				hasIndex = true
			} else {
				body++
			}
		}
		if !hasIndex {
			add(PhasePlan, "", "the plan has no page index (the overview every other page is reached from)")
		}
		if body < d.MinPages {
			add(PhasePlan, "", "the plan has %d pages besides the overview; %s research publishes at least %d", body, d.ID, d.MinPages)
		}
		if body > d.MaxPages {
			warn(PhasePlan, "", "the plan has %d pages besides the overview; %s research aims at %d at most", body, d.ID, d.MaxPages)
		}
		if r.Mode == ModeKickoff && !planHas(p, "context") {
			add(PhasePlan, "", "a kickoff plan needs a page context: what the build should reuse, choose and avoid, for the team that builds the project")
		}
	}

	// gather
	if c.Sources < d.MinSources {
		add(PhaseGather, "", "%d verified sources; %s research needs %d", c.Sources, d.ID, d.MinSources)
	}
	if c.Domains < d.MinDomains {
		add(PhaseGather, "", "the sources come from %d sites; %s research needs %d (look past the first site that answers)", c.Domains, d.ID, d.MinDomains)
	}
	if c.Findings < d.MinFindings {
		add(PhaseGather, "", "%d findings; %s research needs %d", c.Findings, d.ID, d.MinFindings)
	}
	if r.Plan != nil {
		perSrc, perFind := max(2, d.MinSources/max(1, len(r.Plan.Tracks))/2), max(3, d.MinFindings/max(1, len(r.Plan.Tracks))/2)
		for _, t := range r.Plan.Tracks {
			ns, nf := 0, 0
			for _, s := range r.Sources {
				if contains(s.Tracks, t.ID) {
					ns++
				}
			}
			for _, f := range r.Findings {
				if f.Track == t.ID {
					nf++
				}
			}
			if ns < perSrc || nf < perFind {
				add(PhaseGather, "", "track %s (%s) has %d sources and %d findings; each track needs %d and %d", t.ID, t.Title, ns, nf, perSrc, perFind)
			}
		}
	}
	if need := d.Rounds - 1; len(r.Reviews) < need {
		add(PhaseGather, "", "round %d of %d: %d of %d gap reviews done (a reviewer reads the findings, records what is missing or contested, and the next round researches it: action review_round)",
			r.Round(), d.Rounds, len(r.Reviews), need)
	}
	if c.OpenGaps > 0 {
		var ids []string
		for _, g := range r.Gaps {
			if g.Open {
				ids = append(ids, g.ID)
			}
		}
		add(PhaseGather, "", "%d open %s (%s): answer each with findings, or close it as unanswerable with the reason (action gap)", c.OpenGaps, plural(c.OpenGaps, "gap", "gaps"), strings.Join(clipList(ids, 8), ", "))
	}
	if r.Mode == ModeKickoff {
		if c.Snippets < d.MinSnippets {
			add(PhaseGather, "", "%d code %s from mature projects; a %s kickoff needs %d (action snippet)", c.Snippets, plural(c.Snippets, "snippet", "snippets"), d.ID, d.MinSnippets)
		}
		if c.Projects < d.MinProjects {
			add(PhaseGather, "", "snippets come from %d %s; a %s kickoff studies %d", c.Projects, plural(c.Projects, "project", "projects"), d.ID, d.MinProjects)
		}
	}
	if d.ID == "deep" && c.Contradicts == 0 && c.Findings >= d.MinFindings/2 {
		warn(PhaseGather, "", "no finding records a contradiction: deep research maps where sources disagree (note with contradicts)")
	}

	// write and verify: every written page, checked as the builder renders it
	pages := map[string]bool{}
	for _, pg := range r.Pages {
		pages[pg.ID] = true
	}
	inbound := map[string]int{}
	type pageScan struct {
		meta PageMeta
		refs mdRefs
	}
	var scans []pageScan
	for _, pg := range r.Pages {
		_, refs := renderMarkdown(ReadPage(root, r.Slug, pg.ID), checkEnv(r))
		for _, l := range refs.Links {
			if l != pg.ID {
				inbound[l]++
			}
		}
		scans = append(scans, pageScan{pg, refs})
	}
	if r.Plan != nil {
		for _, pp := range r.Plan.Pages {
			if !pages[pp.ID] {
				add(PhaseWrite, pp.ID, "planned page %s (%s) is not written", pp.ID, pp.Title)
			}
		}
	}
	for _, sc := range scans {
		pg, refs := sc.meta, sc.refs
		pr := PageReport{ID: pg.ID, Title: pg.Title, Words: refs.Words, Cites: len(refs.Cites), Links: len(refs.Links), Inbound: inbound[pg.ID],
			Uncited: refs.Uncited, Prose: refs.Prose, Unknown: refs.Unknown}
		c.Words += refs.Words
		if len(refs.Unknown) > 0 {
			add(PhaseWrite, pg.ID, "page %s refers to what does not exist: %s", pg.ID, strings.Join(clipList(refs.Unknown, 8), ", "))
		}
		minCites := d.MinCites
		if pg.ID == "index" {
			minCites = max(2, d.MinCites/2)
		}
		if len(refs.Cites) < minCites {
			add(PhaseWrite, pg.ID, "page %s cites %d sources; each page cites at least %d", pg.ID, len(refs.Cites), minCites)
		}
		if refs.Prose > 0 && refs.Uncited*3 > refs.Prose {
			add(PhaseWrite, pg.ID, "page %s: %d of %d paragraphs cite no source; a paragraph that states facts cites them", pg.ID, refs.Uncited, refs.Prose)
		} else if refs.Uncited > 0 {
			warn(PhaseWrite, pg.ID, "page %s: %d of %d paragraphs cite no source", pg.ID, refs.Uncited, refs.Prose)
		}
		if pg.ID != "index" && inbound[pg.ID] == 0 {
			add(PhaseWrite, pg.ID, "no page links to %s: link it from the overview or a related page with [[%s]]", pg.ID, pg.ID)
		}
		if len(refs.Links) < 2 && len(r.Pages) > 2 {
			warn(PhaseWrite, pg.ID, "page %s links to %d other pages; related pages link to each other", pg.ID, len(refs.Links))
		}
		if pg.ID != "index" && refs.Words < 150 {
			add(PhaseWrite, pg.ID, "page %s has %d words; a page of %s research has %s", pg.ID, refs.Words, d.ID, d.Words)
		}
		// reviews of the current revision by agents that did not write it
		passed, failed := map[string]bool{}, []PageReview{}
		for _, ck := range pg.Checks {
			if ck.Rev != pg.Rev || contains(pg.Writers, ck.By) {
				continue
			}
			if ck.OK {
				passed[ck.By] = true
			} else {
				failed = append(failed, ck)
			}
		}
		pr.Passed = len(passed)
		pr.Reviewed = len(passed) > 0 && len(passed) >= d.Reviewers && len(failed) == 0 // a page nobody reviewed is not fact-checked
		if pr.Reviewed {
			c.Reviewed++
		}
		for _, f := range failed {
			add(PhaseVerify, pg.ID, "%s found issues in page %s (revision %d): %s", f.By, pg.ID, f.Rev, strings.Join(clipList(f.Issues, 4), "; "))
		}
		if len(failed) == 0 && len(passed) < d.Reviewers { // never with 0 reviewers: no fact-check round
			add(PhaseVerify, pg.ID, "page %s revision %d is fact-checked by %d of %d reviewers (each claim against the quotes of the sources it cites; the writer cannot review their own page)",
				pg.ID, pg.Rev, len(passed), d.Reviewers)
		}
		rep.Pages = append(rep.Pages, pr)
	}
	sort.SliceStable(rep.Pages, func(i, j int) bool { return rep.Pages[i].ID < rep.Pages[j].ID })

	// publish
	switch {
	case r.Built == nil:
		add(PhasePublish, "", "the wiki has not been built (action build)")
	case r.Built.Version != r.Version:
		add(PhasePublish, "", "the wiki was built before the latest changes (version %d, now %d): build it again", r.Built.Version, r.Version)
	}

	rep.Phase = PhaseDone
	for _, ph := range Phases {
		if ph == PhaseDone {
			break
		}
		if rep.hasPhase(ph) {
			rep.Phase = ph
			break
		}
	}
	return rep
}

// hasPhase reports whether a problem belongs to phase.
func (rep Report) hasPhase(phase string) bool {
	for _, p := range rep.Problems {
		if p.Phase == phase {
			return true
		}
	}
	return false
}

// Blocking is the number of problems that keep the wiki from being published: all but those of the publish phase itself.
func (rep Report) Blocking() int {
	n := 0
	for _, p := range rep.Problems {
		if p.Phase != PhasePublish {
			n++
		}
	}
	return n
}

// checkEnv resolves a page's references against the ledger, for checking.
func checkEnv(r *Run) mdEnv {
	pages := map[string]string{}
	for _, p := range r.Pages {
		pages[p.ID] = p.Title
	}
	return mdEnv{
		Page:    func(id string) (string, bool) { t, ok := pages[id]; return t, ok },
		Source:  func(id string) bool { return r.SourceByID(id) != nil },
		Finding: func(id string) bool { return r.FindingByID(id) != nil },
		Snippet: func(id string) (string, bool) { return "", r.SnippetByID(id) != nil },
	}
}

// planHas reports whether the plan has a page.
func planHas(p *Plan, id string) bool {
	for _, pg := range p.Pages {
		if pg.ID == id {
			return true
		}
	}
	return false
}

// contains reports whether list holds v.
func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// clipList keeps the first n items of list, saying how many more there are.
func clipList(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return append(append([]string(nil), list[:n]...), fmt.Sprintf("%d more", len(list)-n))
}
