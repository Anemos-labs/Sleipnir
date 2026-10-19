// Package research is Sleipnir's deep research: a team researches a question on the web, every source it cites is checked
// against the page it came from, and the result is published as a wiki (internal/research/site) with search, cross-links
// and a page per source.
//
// A run lives in the project, under .sleipnir/research/<slug>/: run.json holds the plan, the sources with their verified
// quotes, the findings, the code snippets, the gaps and the page reviews; pages/ holds the wiki's pages as Markdown; site/ is
// the built wiki. Nothing in a run is written by an agent directly: the research tool (Tool) is the only writer, so read-only
// roles can research, workers in separate worktrees share one ledger, and every quote in it was found on its page by the
// harness, not taken on a model's word.
//
// A run is driven as a standing goal (internal/goal): Brief writes the run's protocol as the goal's spec, with milestones
// the judge confirms on evidence, and the tool's status action says, from the ledger alone, which phase the run is in and
// what is missing. Depths (quick, standard, deep) scale the team and the bars: research tracks in parallel, gather
// rounds with a gap review between them, sources and domains, pages, and how pages are fact-checked.
//
// A run can also open a coding project (ModeKickoff): it researches prior art (mature projects, their architecture,
// libraries, licenses and code worth learning from) and its wiki and context page become the build's starting point.
package research
