package research

import (
	"fmt"
	"strings"
)

// Depth is how much research a run does: its team, its rounds and the bars its wiki is held to.
type Depth struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Tracks are research tracks worked in parallel, one researcher each; Rounds are gather rounds, with a gap review
	// between two rounds; Perspectives are the points of view the plan names (STORM's perspective-guided questions).
	Tracks       int `json:"tracks"`
	Rounds       int `json:"rounds"`
	Perspectives int `json:"perspectives"`
	Questions    int `json:"questions"`
	// MinSources and MinDomains are the verified sources and the distinct sites among them; MinFindings the findings.
	MinSources  int `json:"min_sources"`
	MinDomains  int `json:"min_domains"`
	MinFindings int `json:"min_findings"`
	// MinPages and MaxPages bound the planned pages besides the overview; MinCites is the distinct sources each page cites.
	MinPages int `json:"min_pages"`
	MaxPages int `json:"max_pages"`
	MinCites int `json:"min_cites"`
	// Reviewers is how many agents, none of them the page's writer, must pass each page.
	Reviewers int `json:"reviewers"`
	// MinSnippets and MinProjects apply to a kickoff run: code snippets from mature projects, and projects studied.
	MinSnippets int `json:"min_snippets"`
	MinProjects int `json:"min_projects"`
	// Words is the length a page aims at.
	Words string `json:"words"`
}

// Depths are the built-in depths, lightest first.
var Depths = []Depth{
	{ID: "quick", Title: "Quick", Summary: "three researchers in one round, a compact wiki: for a focused question",
		Tracks: 3, Rounds: 1, Perspectives: 3, Questions: 9, MinSources: 15, MinDomains: 8, MinFindings: 24,
		MinPages: 4, MaxPages: 7, MinCites: 4, Reviewers: 1, MinSnippets: 4, MinProjects: 3, Words: "400 to 900"},
	{ID: "standard", Title: "Standard", Summary: "six researchers over two rounds with a gap review, every page fact-checked",
		Tracks: 6, Rounds: 2, Perspectives: 5, Questions: 20, MinSources: 45, MinDomains: 18, MinFindings: 70,
		MinPages: 9, MaxPages: 15, MinCites: 6, Reviewers: 1, MinSnippets: 10, MinProjects: 6, Words: "700 to 1,500"},
	{ID: "deep", Title: "Deep", Summary: "ten researchers over three rounds, two independent fact-checks per page, contradictions mapped",
		Tracks: 10, Rounds: 3, Perspectives: 7, Questions: 36, MinSources: 110, MinDomains: 40, MinFindings: 170,
		MinPages: 18, MaxPages: 32, MinCites: 8, Reviewers: 2, MinSnippets: 20, MinProjects: 10, Words: "1,000 to 2,500"},
}

// DefaultDepth is the depth of a run that names none.
const DefaultDepth = "standard"

// DepthByID returns the depth with that id (case and surrounding space ignored).
func DepthByID(id string) (Depth, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, d := range Depths {
		if d.ID == id {
			return d, true
		}
	}
	return Depth{}, false
}

// DepthIDs lists the depth ids, lightest first.
func DepthIDs() []string {
	out := make([]string, len(Depths))
	for i, d := range Depths {
		out[i] = d.ID
	}
	return out
}

// Modes of a run.
const (
	// ModeReport researches a question and publishes the wiki as the result.
	ModeReport = "report"
	// ModeKickoff researches prior art for a coding project; its wiki and context page feed the build that follows.
	ModeKickoff = "kickoff"
)

// errDepth is the error for a depth that is not one of Depths.
func errDepth(id string) error {
	return fmt.Errorf("unknown depth %q: one of %s", id, strings.Join(DepthIDs(), ", "))
}
