package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/research"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// researchSegment is the key of the shared-context segment a published kickoff run adds: research text came from the web, so it
// is marked as untrusted like the repository's own files.
const researchSegment = "research (from web sources, unverified)"

// ResearchStore is the session's research runs (nil when the research plugin is off).
func (s *Session) ResearchStore() *research.Store { return s.research }

// researchShared is the shared-context segment of the newest published kickoff run of the project, if any.
func (s *Session) researchShared() (kv.Segment, bool) {
	slug, text := research.PublishedContext(s.opts.Root)
	if text == "" {
		return kv.Segment{}, false
	}
	s.researchPinned = slug
	return kv.Segment{Key: researchSegment, Text: text, Vol: kv.VolEpoch}, true
}

// researchBuilt is told of every wiki build. When a kickoff run is published, its context page becomes part of every agent's
// shared context: one declared epoch, the same for the whole team.
func (s *Session) researchBuilt(r *research.Run) {
	text, ok := research.Context(s.opts.Root, r)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.researchPinned == r.Slug && s.researchText == text {
		s.mu.Unlock()
		return
	}
	s.researchPinned, s.researchText = r.Slug, text
	var segs []kv.Segment
	var version uint64 = 1
	if s.Shared != nil {
		version = s.Shared.Version + 1
		for _, seg := range s.Shared.Segments {
			if seg.Key != researchSegment {
				segs = append(segs, seg)
			}
		}
	}
	segs = append(segs, kv.Segment{Key: researchSegment, Text: text, Vol: kv.VolEpoch})
	l := kv.NewLayer("shared", kv.KindShared, version, segs)
	s.Shared = l
	sw, a := s.Swarm, s.Agent
	s.mu.Unlock()
	reason := "research " + r.Slug + " published"
	switch {
	case sw != nil:
		sw.SetShared(l, reason)
	case a != nil:
		a.SyncShared(l, nil, reason)
	}
	s.notice("", "research "+r.Slug+" is published: its build context is now in every agent's shared context")
}

// researchUsage is the line /research answers a wrong call with.
const researchUsage = "usage: /research [quick|standard|deep] QUESTION starts a deep research run; /research lists the runs; /research status|build|open [RUN]; /research export RUN DIR. /goal --research=DEPTH GOAL researches before building."

// ResearchCommand runs /research: it lists runs, reports or builds one, exports its wiki, or starts a run. A start answers with
// the goal the host sets (the run's objective, with its brief as the spec); every other use answers with text only.
func (s *Session) ResearchCommand(arg string) (goalText, out string) {
	if s.research == nil {
		return "", "/research needs the research plugin: /plugins enable research"
	}
	f := strings.Fields(arg)
	if len(f) == 0 || f[0] == "list" {
		return "", s.researchList()
	}
	slugArg := func() (string, error) {
		if len(f) > 1 {
			return f[1], nil
		}
		if a := s.research.Active(); a != "" {
			return a, nil
		}
		runs := research.List(s.opts.Root)
		if len(runs) == 0 {
			return "", fmt.Errorf("no research run yet: /research QUESTION starts one")
		}
		return runs[0].Slug, nil
	}
	switch f[0] {
	case "status", "build", "open":
		slug, err := slugArg()
		if err != nil {
			return "", err.Error()
		}
		r, err := s.research.Snapshot(slug)
		if err != nil {
			return "", err.Error()
		}
		switch f[0] {
		case "status":
			return "", tools.SanitizeForTerminal(research.StatusText(s.opts.Root, r))
		case "build":
			return "", s.researchBuild(slug)
		}
		index := filepath.Join(research.SiteDir(s.opts.Root, slug), "index.html")
		if _, err := os.Stat(index); err != nil {
			return "", "the wiki of " + slug + " is not built yet: /research build " + slug
		}
		return "", "the wiki of " + slug + ": " + index + " (in the web interface: the Research view)"
	case "export":
		if len(f) != 3 {
			return "", "usage: /research export RUN DIR: render the wiki into DIR inside the project"
		}
		dst, err := s.researchExport(f[1], f[2])
		if err != nil {
			return "", "export: " + err.Error()
		}
		return "", "the wiki of " + f[1] + " is in " + dst
	}
	depth := research.DefaultDepth
	question := strings.TrimSpace(arg)
	if d, ok := research.DepthByID(f[0]); ok {
		depth = d.ID
		question = strings.TrimSpace(strings.TrimPrefix(question, f[0]))
	}
	if question == "" {
		return "", researchUsage
	}
	g, err := s.StartResearch(question, depth, research.ModeReport, "")
	if err != nil {
		return "", "research not started: " + err.Error()
	}
	return g, ""
}

// StartResearch creates a run, writes its brief and returns the goal that drives it. A kickoff run researches for the project
// projectGoal describes; its goal is the research milestones followed by projectGoal.
func (s *Session) StartResearch(question, depth, mode, projectGoal string) (string, error) {
	if s.research == nil {
		return "", fmt.Errorf("the research plugin is off: /plugins enable research")
	}
	if !s.pluginOn(plugin.Goal) {
		return "", fmt.Errorf("a research run is driven as a goal, and the goal plugin is off: /plugins enable goal")
	}
	r, err := s.research.Create(question, depth, mode, projectGoal)
	if err != nil {
		return "", err
	}
	if err := research.WriteBrief(s.opts.Root, r); err != nil {
		return "", err
	}
	return research.Objective(r, projectGoal, s.briefSpec(r)), nil
}

// briefSpec is the run's brief as its goal names it: relative to the session's directory when the brief is below it, else, and
// always when the team works in trees of its own, by its absolute path in the checkout. A tree is made from the commits and the brief is written after it, so a relative path
// would lead every agent to a file its tree does not have.
func (s *Session) briefSpec(r *research.Run) string {
	spec := filepath.ToSlash(filepath.Join(".sleipnir", "research", r.Slug, "BRIEF.md"))
	abs := filepath.Join(s.opts.Root, filepath.FromSlash(spec))
	if s.iso != nil {
		return filepath.ToSlash(abs)
	}
	if rel, err := filepath.Rel(s.opts.Cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(abs) // a session in a folder the brief is not below: a path relative to it would lead nowhere
}

// ResearchGoal turns a goal into one that researches first: "--research=DEPTH GOAL" (or "--research DEPTH GOAL") starts a kickoff
// run for GOAL and returns its goal. A goal without the flag is returned as it is.
func (s *Session) ResearchGoal(text string) (string, error) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "--research") {
		return text, nil
	}
	rest := strings.TrimPrefix(t, "--research")
	var depth string
	switch {
	case strings.HasPrefix(rest, "="):
		depth, rest, _ = strings.Cut(rest[1:], " ")
	case strings.HasPrefix(rest, " "):
		f := strings.Fields(rest)
		if len(f) > 0 {
			if _, ok := research.DepthByID(f[0]); ok {
				depth = f[0]
				rest = strings.TrimPrefix(strings.TrimSpace(rest), f[0])
			}
		}
	default:
		return text, nil
	}
	if depth == "" {
		depth = research.DefaultDepth
	}
	if _, ok := research.DepthByID(depth); !ok {
		return "", fmt.Errorf("--research: unknown depth %q (quick, standard or deep)", depth)
	}
	project := strings.TrimSpace(rest)
	if project == "" {
		return "", fmt.Errorf("--research needs the goal to research for")
	}
	return s.StartResearch(firstLineOf(project, 300), depth, research.ModeKickoff, project)
}

// firstLineOf is the first line of text, at most n runes: the question a kickoff run is filed under.
func firstLineOf(text string, n int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	r := []rune(strings.TrimSpace(line))
	if len(r) > n {
		return string(r[:n])
	}
	return string(r)
}

// researchList lists the project's runs with their phase and numbers.
func (s *Session) researchList() string {
	runs := research.List(s.opts.Root)
	if len(runs) == 0 {
		return "no research runs in this project. " + researchUsage
	}
	var b strings.Builder
	for _, r := range runs {
		rep := research.Check(s.opts.Root, r)
		c := rep.Counts
		fmt.Fprintf(&b, "%-40s %-8s %-8s %-7s %3d sources %3d findings %2d pages  %s\n", r.Slug, r.Depth, r.Mode, rep.Phase, c.Sources, c.Findings, c.Pages,
			tools.SanitizeForTerminal(clipRunes(r.Question, 80)))
	}
	b.WriteString("/research status RUN shows one; /research open RUN its wiki")
	return b.String()
}

// researchBuild builds a run's wiki from the host (/research build).
func (s *Session) researchBuild(slug string) string {
	r, res, err := s.research.Publish(slug, research.BuildOptions{Fonts: s.researchFonts})
	if err != nil {
		return "build: " + err.Error()
	}
	s.researchBuilt(r)
	msg := fmt.Sprintf("the wiki of %s: %s (%d pages)", slug, filepath.Join(res.Dir, "index.html"), res.Pages)
	if n := res.Report.Blocking(); n > 0 {
		msg += fmt.Sprintf("; %d problems remain (/research status %s)", n, slug)
	}
	return msg
}

// researchExport renders a run's wiki from its ledger as it is now and writes it to dir, a directory inside the project (created,
// and emptied of an earlier export).
func (s *Session) researchExport(slug, dir string) (string, error) {
	dst := dir
	if !filepath.IsAbs(dst) {
		dst = filepath.Join(s.opts.Cwd, dst)
	}
	dst = filepath.Clean(dst)
	rel, err := filepath.Rel(s.opts.Root, dst)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is not a directory inside the project", dir)
	}
	r, err := s.research.Snapshot(slug)
	if err != nil {
		return "", err
	}
	site, err := research.Render(s.opts.Root, r, research.BuildOptions{Fonts: s.researchFonts})
	if err != nil {
		return "", err
	}
	return dst, research.Export(s.opts.Root, rel, site.Files)
}

// ResearchRuns lists the project's research runs, newest first, with where each stands (the Research view's list).
func (s *Session) ResearchRuns() []wire.ResearchRun {
	out := []wire.ResearchRun{}
	if s.research == nil {
		return out
	}
	active := s.research.Active()
	for _, r := range research.List(s.opts.Root) {
		rep := research.Check(s.opts.Root, r)
		c := rep.Counts
		out = append(out, wire.ResearchRun{Slug: r.Slug, Question: r.Question, Depth: r.Depth, Mode: r.Mode, Phase: rep.Phase,
			Created: r.Created, Built: r.Built != nil && r.Built.Version == r.Version, Problems: rep.Blocking(),
			Sources: c.Sources, Domains: c.Domains, Findings: c.Findings, Snippets: c.Snippets, Pages: c.Pages, Planned: c.Planned,
			OpenGaps: c.OpenGaps, Active: r.Slug == active})
		if d, ok := research.DepthByID(r.Depth); ok && d.Reviewers == 0 {
			out[len(out)-1].NoFactCheck = true
		}
	}
	return out
}

// ResearchDepths are the depths a run can have, for the forms that start one.
func ResearchDepths() []wire.ResearchDepth {
	out := make([]wire.ResearchDepth, 0, len(research.Depths))
	for _, d := range research.Depths {
		out = append(out, wire.ResearchDepth{ID: d.ID, Title: d.Title, About: d.Summary, Tracks: d.Tracks, Sources: d.MinSources,
			Pages: fmt.Sprintf("%d–%d", d.MinPages+1, d.MaxPages+1)})
	}
	return out
}

// ResearchStart starts a research run from the web page: the goal it returns is set as /research sets it.
func (s *Session) ResearchStart(question, depth string) (string, error) {
	if s.research == nil {
		return "", fmt.Errorf("the research plugin is off: /plugins enable research")
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("the research needs a question")
	}
	if depth == "" {
		depth = research.DefaultDepth
	}
	if _, ok := research.DepthByID(depth); !ok {
		return "", fmt.Errorf("unknown depth %q (quick, standard or deep)", depth)
	}
	return s.StartResearch(question, depth, research.ModeReport, "")
}

// ResearchExport renders a run's wiki into dir, a directory inside the project, and says where it went.
func (s *Session) ResearchExport(slug, dir string) (string, error) {
	if s.research == nil {
		return "", fmt.Errorf("the research plugin is off: /plugins enable research")
	}
	if !research.ValidSlug(slug) {
		return "", fmt.Errorf("no research run %q", slug)
	}
	return s.researchExport(slug, dir)
}

// ResearchSite is a run's wiki rendered from its ledger, for the web page to serve. A render is kept until the run changes.
func (s *Session) ResearchSite(slug string) (*research.Rendered, error) {
	if s.research == nil {
		return nil, fmt.Errorf("the research plugin is off")
	}
	r, err := s.research.Snapshot(slug)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if c := s.researchSite; c != nil && c.slug == slug && c.version == r.Version {
		s.mu.Unlock()
		return c.site, nil
	}
	s.mu.Unlock()
	site, err := research.Render(s.opts.Root, r, research.BuildOptions{Fonts: s.researchFonts})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.researchSite = &researchRender{slug: slug, version: r.Version, site: site}
	s.mu.Unlock()
	return site, nil
}

// researchRender is the newest wiki the web page was served, by run and version.
type researchRender struct {
	slug    string
	version int
	site    *research.Rendered
}
