package research

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// assetsFS holds the wiki's stylesheet and script.
//
//go:embed assets
var assetsFS embed.FS

// BuildOptions tune a build.
type BuildOptions struct {
	// Fonts are copied into the wiki's assets/fonts (the web page's: Barlow and JetBrains Mono, with their licenses), with its
	// fonts.css. Without them the wiki falls back to the system's fonts.
	Fonts fs.FS
}

// BuildResult is what a build wrote.
type BuildResult struct {
	Dir     string
	Pages   int
	Indexed int
	Report  Report
}

// Rendered is a run's wiki rendered in memory: every file by its path inside the site ("index.html", "assets/wiki.css").
type Rendered struct {
	Files   map[string][]byte
	Pages   int
	Indexed int
	Report  Report
}

// Render renders a run's wiki in memory. Everything in it comes from the run's ledger and its pages' Markdown through the
// renderer, which escapes what it does not understand, and from the embedded stylesheet, script and fonts: nothing is copied from
// the disk as it is, so a site served from it holds only what the renderer made.
func Render(root string, r *Run, opts BuildOptions) (*Rendered, error) {
	w := &wiki{root: root, r: r, files: map[string][]byte{}, fonts: opts.Fonts}
	if err := w.write(); err != nil {
		return nil, err
	}
	return &Rendered{Files: w.files, Pages: len(w.pages), Indexed: w.indexed, Report: w.report}, nil
}

// Build renders a run's wiki into its site directory, replacing what was there. It renders whatever the run has, so a wiki can be
// looked at while the run goes on; the report says what is missing.
func Build(root string, r *Run, opts BuildOptions) (*BuildResult, error) {
	site, err := Render(root, r, opts)
	if err != nil {
		return nil, err
	}
	dir := SiteDir(root, r.Slug)
	tmp := dir + ".new"
	_ = os.RemoveAll(tmp)
	names := make([]string, 0, len(site.Files))
	for name := range site.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
		if err := os.WriteFile(p, site.Files[name], 0o644); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
	}
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.Rename(old, dir)
		return nil, err
	}
	_ = os.RemoveAll(old)
	return &BuildResult{Dir: dir, Pages: site.Pages, Indexed: site.Indexed, Report: site.Report}, nil
}

// Publish builds a run's wiki (slug "" is the active run) and records the build in its ledger, so that the run's phase moves on
// once the build matches what the ledger holds.
func (s *Store) Publish(slug string, opts BuildOptions) (*Run, *BuildResult, error) {
	var res *BuildResult
	r, err := s.Update(slug, func(r *Run) error {
		var err error
		res, err = Build(s.root, r, opts)
		if err != nil {
			return err
		}
		r.Built = &BuildMark{Version: r.Version + 1, Problems: res.Report.Blocking(), Warnings: len(res.Report.Warnings), Pages: res.Pages}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return r, res, nil
}

// wiki is one build.
type wiki struct {
	root    string
	r       *Run
	files   map[string][]byte
	fonts   fs.FS
	pages   []*builtPage
	byID    map[string]*builtPage
	report  Report
	indexed int
	citedOn map[string][]string // source -> pages
}

// builtPage is a page rendered, before it is laid out.
type builtPage struct {
	meta  PageMeta
	html  string
	refs  mdRefs
	text  string
	back  []string
	order int
}

// write renders every file of the wiki into w.files.
func (w *wiki) write() error {
	r := w.r
	w.report = Check(w.root, r)
	w.byID = map[string]*builtPage{}
	order := map[string]int{}
	if r.Plan != nil {
		for i, p := range r.Plan.Pages {
			order[p.ID] = i
		}
	}
	for _, pm := range r.Pages {
		bp := &builtPage{meta: pm, order: len(order) + len(w.pages)}
		if o, ok := order[pm.ID]; ok {
			bp.order = o
		}
		w.pages = append(w.pages, bp)
		w.byID[pm.ID] = bp
	}
	sort.SliceStable(w.pages, func(i, j int) bool {
		if (w.pages[i].meta.ID == "index") != (w.pages[j].meta.ID == "index") {
			return w.pages[i].meta.ID == "index"
		}
		return w.pages[i].order < w.pages[j].order
	})
	env := w.env()
	w.citedOn = map[string][]string{}
	for _, bp := range w.pages {
		src := ReadPage(w.root, r.Slug, bp.meta.ID)
		bp.html, bp.refs = renderMarkdown(src, env)
		bp.text = plainText(src)
		for _, s := range bp.refs.Cites {
			w.citedOn[s] = append(w.citedOn[s], bp.meta.ID)
		}
	}
	for _, bp := range w.pages {
		for _, l := range bp.refs.Links {
			if t := w.byID[l]; t != nil && l != bp.meta.ID {
				t.back = addUnique(t.back, bp.meta.ID)
			}
		}
	}
	if err := w.assets(); err != nil {
		return err
	}
	if w.byID["index"] == nil {
		if err := w.file("index.html", w.layout("index", r.Question, w.placeholderIndex(), "")); err != nil {
			return err
		}
	}
	for _, bp := range w.pages {
		if err := w.file(bp.meta.ID+".html", w.layout(bp.meta.ID, bp.meta.Title, w.article(bp), w.toc(bp))); err != nil {
			return err
		}
	}
	for _, sp := range []struct {
		id, title string
		body      func() string
		show      bool
	}{
		{"sources", "Sources", w.sourcesPage, true},
		{"evidence", "Evidence", w.evidencePage, true},
		{"method", "Method", w.methodPage, true},
		{"gaps", "Open questions", w.gapsPage, true},
		{"snippets", "Code snippets", w.snippetsPage, len(r.Snippets) > 0 || r.Mode == ModeKickoff},
	} {
		if !sp.show {
			continue
		}
		if err := w.file(sp.id+".html", w.layout(sp.id, sp.title, sp.body(), "")); err != nil {
			return err
		}
	}
	return w.searchIndex()
}

// env resolves a page's references for the wiki.
func (w *wiki) env() mdEnv {
	return mdEnv{
		Page: func(id string) (string, bool) {
			if bp := w.byID[id]; bp != nil {
				return bp.meta.Title, true
			}
			return "", false
		},
		Source:  func(id string) bool { return w.r.SourceByID(id) != nil },
		Finding: func(id string) bool { return w.r.FindingByID(id) != nil },
		Snippet: func(id string) (string, bool) {
			sn := w.r.SnippetByID(id)
			if sn == nil {
				return "", false
			}
			return w.snippetBlock(sn), true
		},
	}
}

// file adds one file to the wiki.
func (w *wiki) file(name, body string) error {
	w.files[name] = []byte(body)
	return nil
}

// assets copies the stylesheet, the script and the fonts.
func (w *wiki) assets() error {
	for _, name := range []string{"wiki.css", "wiki.js"} {
		b, err := assetsFS.ReadFile("assets/" + name)
		if err != nil {
			return err
		}
		if err := w.file("assets/"+name, string(b)); err != nil {
			return err
		}
	}
	if w.fonts == nil {
		return w.file("assets/fonts/fonts.css", "/* the fonts were not bundled: the wiki uses the system's */\n")
	}
	return fs.WalkDir(w.fonts, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := path.Ext(p)
		if ext != ".woff2" && ext != ".css" && ext != ".txt" {
			return nil
		}
		b, err := fs.ReadFile(w.fonts, p)
		if err != nil {
			return err
		}
		return w.file("assets/fonts/"+path.Base(p), string(b))
	})
}

// esc escapes text for HTML.
func esc(s string) string { return html.EscapeString(s) }

// layout wraps a page's content in the wiki's frame: the header with search, the sidebar, the article and its contents.
func (w *wiki) layout(id, title, content, toc string) string {
	r := w.r
	d := r.DepthInfo()
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\" data-theme=\"dark\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<meta name=\"generator\" content=\"Sleipnir deep research\">\n")
	fmt.Fprintf(&b, "<title>%s · %s</title>\n", esc(title), esc(wikiName(r)))
	b.WriteString("<link rel=\"stylesheet\" href=\"assets/fonts/fonts.css\">\n<link rel=\"stylesheet\" href=\"assets/wiki.css\">\n")
	b.WriteString("<script src=\"assets/search-index.js\" defer></script>\n<script src=\"assets/wiki.js\" defer></script>\n</head>\n")
	fmt.Fprintf(&b, "<body data-page=\"%s\">\n", esc(id))
	b.WriteString("<a class=\"skip\" href=\"#main\">Skip to the article</a>\n")
	b.WriteString("<header class=\"top\">\n<button class=\"menu\" type=\"button\" aria-label=\"Pages\" aria-expanded=\"false\" data-menu>☰</button>\n")
	fmt.Fprintf(&b, "<a class=\"brand\" href=\"index.html\"><span class=\"mark\" aria-hidden=\"true\">◆</span><span class=\"name\">%s</span></a>\n", esc(wikiName(r)))
	b.WriteString("<button class=\"search\" type=\"button\" data-search aria-label=\"Search the wiki\"><span>Search the wiki</span><kbd>/</kbd></button>\n")
	b.WriteString("<button class=\"theme\" type=\"button\" data-theme-toggle aria-label=\"Light or dark\"><svg viewBox=\"0 0 16 16\" width=\"16\" height=\"16\" aria-hidden=\"true\"><circle cx=\"8\" cy=\"8\" r=\"6.25\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.5\"/><path d=\"M8 1.75a6.25 6.25 0 0 1 0 12.5z\" fill=\"currentColor\"/></svg></button>\n</header>\n")
	b.WriteString("<div class=\"frame\">\n<nav class=\"side\" aria-label=\"Pages\">\n")
	fmt.Fprintf(&b, "<div class=\"side-q\"><span class=\"lab\">Question</span><p>%s</p><span class=\"chip depth-%s\">%s</span>", esc(r.Question), esc(d.ID), esc(d.Title))
	if r.Mode == ModeKickoff {
		b.WriteString("<span class=\"chip kick\">Project kickoff</span>")
	}
	b.WriteString("</div>\n")
	b.WriteString(w.nav(id))
	b.WriteString("</nav>\n<main id=\"main\">\n")
	b.WriteString(content)
	b.WriteString("</main>\n")
	if toc != "" {
		b.WriteString("<aside class=\"toc\" aria-label=\"On this page\">" + toc + "</aside>\n")
	}
	b.WriteString("</div>\n<footer class=\"foot\">")
	fmt.Fprintf(&b, "Researched by a Sleipnir team · %s depth · %d sources from %d sites · every quote found on its page by the harness · <a href=\"method.html\">how this was made</a>",
		esc(d.Title), len(r.Sources), r.Domains())
	b.WriteString("</footer>\n<div class=\"pop\" role=\"tooltip\" hidden></div>\n</body>\n</html>\n")
	return b.String()
}

// wikiName is the wiki's name: the overview's title, else the question's first words.
func wikiName(r *Run) string {
	if pm := r.PageByID("index"); pm != nil && pm.Title != "" {
		return pm.Title
	}
	return clipText(r.Question, 60)
}

// nav is the sidebar: the pages by category, then the pages the builder makes.
func (w *wiki) nav(current string) string {
	var cats []string
	by := map[string][]*builtPage{}
	for _, bp := range w.pages {
		if bp.meta.ID == "index" {
			continue
		}
		c := firstNonEmpty(bp.meta.Category, "Articles")
		if _, ok := by[c]; !ok {
			cats = append(cats, c)
		}
		by[c] = append(by[c], bp)
	}
	var b strings.Builder
	link := func(id, title string) {
		cur := ""
		if id == current {
			cur = ` aria-current="page"`
		}
		fmt.Fprintf(&b, "<li><a href=\"%s.html\"%s>%s</a></li>\n", esc(id), cur, esc(title))
	}
	b.WriteString("<ul class=\"nav\">\n")
	link("index", "Overview")
	b.WriteString("</ul>\n")
	for _, c := range cats {
		fmt.Fprintf(&b, "<h2 class=\"lab\">%s</h2>\n<ul class=\"nav\">\n", esc(c))
		for _, bp := range by[c] {
			link(bp.meta.ID, bp.meta.Title)
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("<h2 class=\"lab\">Research</h2>\n<ul class=\"nav tools\">\n")
	link("sources", fmt.Sprintf("Sources (%d)", len(w.r.Sources)))
	link("evidence", fmt.Sprintf("Evidence (%d)", len(w.r.Findings)))
	if len(w.r.Snippets) > 0 || w.r.Mode == ModeKickoff {
		link("snippets", fmt.Sprintf("Code snippets (%d)", len(w.r.Snippets)))
	}
	open := 0
	for _, g := range w.r.Gaps {
		if g.Open {
			open++
		}
	}
	link("gaps", fmt.Sprintf("Open questions (%d)", open))
	link("method", "Method")
	b.WriteString("</ul>\n")
	return b.String()
}

// toc is a page's table of contents.
func (w *wiki) toc(bp *builtPage) string {
	if len(bp.refs.Headings) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<h2 class=\"lab\">On this page</h2>\n<ol>\n")
	for _, h := range bp.refs.Headings {
		fmt.Fprintf(&b, "<li class=\"l%d\"><a href=\"#%s\">%s</a></li>\n", h.Level, esc(h.ID), esc(h.Text))
	}
	b.WriteString("<li class=\"l2\"><a href=\"#references\">References</a></li>\n</ol>\n")
	return b.String()
}

// article lays out a written page: title, summary, meta, body, references and backlinks.
func (w *wiki) article(bp *builtPage) string {
	r := w.r
	var b strings.Builder
	b.WriteString("<article class=\"page\">\n")
	if bp.meta.ID == "index" {
		b.WriteString(w.hero())
	} else {
		cat := firstNonEmpty(bp.meta.Category, "Articles")
		fmt.Fprintf(&b, "<p class=\"crumb\"><a href=\"index.html\">Overview</a> › %s</p>\n", esc(cat))
		fmt.Fprintf(&b, "<h1>%s</h1>\n", esc(bp.meta.Title))
		if bp.meta.Summary != "" {
			fmt.Fprintf(&b, "<p class=\"lede\">%s</p>\n", esc(bp.meta.Summary))
		}
	}
	reviewed := 0
	for _, ck := range bp.meta.Checks {
		if ck.Rev == bp.meta.Rev && ck.OK && !contains(bp.meta.Writers, ck.By) {
			reviewed++
		}
	}
	state := "not yet fact-checked"
	if reviewed > 0 {
		state = fmt.Sprintf("fact-checked by %d %s", reviewed, plural(reviewed, "reviewer", "reviewers"))
	}
	fmt.Fprintf(&b, "<p class=\"meta\"><span>%d sources</span><span>%d words</span><span class=\"%s\">%s</span><span>revision %d</span></p>\n",
		len(bp.refs.Cites), bp.refs.Words, map[bool]string{true: "ok", false: "warn"}[reviewed > 0], state, bp.meta.Rev)
	b.WriteString("<div class=\"prose\">\n" + bp.html + "</div>\n")
	if bp.meta.ID == "index" {
		b.WriteString(w.pageCards())
	}
	b.WriteString("<section class=\"refs\" id=\"references\">\n<h2>References</h2>\n")
	if len(bp.refs.Cites) == 0 {
		b.WriteString("<p class=\"dim\">This page cites no source.</p>\n")
	} else {
		b.WriteString("<ol>\n")
		for _, sid := range bp.refs.Cites {
			s := r.SourceByID(sid)
			fmt.Fprintf(&b, "<li id=\"ref-%s\"><a class=\"ext\" href=\"%s\" rel=\"noopener noreferrer\" target=\"_blank\">%s</a> <span class=\"site\">%s</span> <span class=\"kind\">%s</span> <a class=\"sid\" href=\"sources.html#%s\">%s</a>",
				esc(sid), esc(s.URL), esc(s.Title), esc(s.Site), esc(s.Kind), esc(sid), esc(sid))
			if len(s.Quotes) > 0 {
				fmt.Fprintf(&b, "<blockquote>%s</blockquote>", esc(s.Quotes[0].Text))
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ol>\n")
	}
	b.WriteString("</section>\n")
	if len(bp.back) > 0 {
		b.WriteString("<section class=\"back\">\n<h2>Linked from</h2>\n<ul>\n")
		for _, id := range bp.back {
			if t := w.byID[id]; t != nil {
				fmt.Fprintf(&b, "<li><a href=\"%s.html\">%s</a></li>\n", esc(id), esc(t.meta.Title))
			}
		}
		b.WriteString("</ul>\n</section>\n")
	}
	b.WriteString("</article>\n")
	return b.String()
}

// plural picks the word for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// hero is the head of the overview: the question and the run's numbers.
func (w *wiki) hero() string {
	r := w.r
	c := w.report.Counts
	var b strings.Builder
	fmt.Fprintf(&b, "<header class=\"hero\">\n<span class=\"lab\">%s research</span>\n<h1>%s</h1>\n", esc(r.DepthInfo().Title), esc(wikiName(r)))
	fmt.Fprintf(&b, "<p class=\"question\">%s</p>\n", esc(r.Question))
	if pm := r.PageByID("index"); pm != nil && pm.Summary != "" {
		fmt.Fprintf(&b, "<p class=\"lede\">%s</p>\n", esc(pm.Summary))
	}
	b.WriteString("<dl class=\"stats\">\n")
	stat := func(n int, label, href string) {
		fmt.Fprintf(&b, "<div><dt><a href=\"%s\">%s</a></dt><dd>%s</dd></div>\n", href, strconv.Itoa(n), esc(label))
	}
	stat(c.Sources, "sources", "sources.html")
	stat(c.Domains, "sites", "sources.html")
	stat(c.Findings, "findings", "evidence.html")
	stat(c.Quotes, "verified quotes", "sources.html")
	stat(len(w.pages), "pages", "#pages")
	if len(r.Snippets) > 0 {
		stat(len(r.Snippets), "snippets", "snippets.html")
	}
	b.WriteString("</dl>\n</header>\n")
	return b.String()
}

// pageCards lists every page as a card, by category, on the overview.
func (w *wiki) pageCards() string {
	var b strings.Builder
	b.WriteString("<section class=\"cards\" id=\"pages\">\n<h2>Pages</h2>\n<div class=\"grid\">\n")
	for _, bp := range w.pages {
		if bp.meta.ID == "index" {
			continue
		}
		fmt.Fprintf(&b, "<a class=\"card\" href=\"%s.html\"><span class=\"lab\">%s</span><strong>%s</strong><span>%s</span><span class=\"n\">%d sources</span></a>\n",
			esc(bp.meta.ID), esc(firstNonEmpty(bp.meta.Category, "Articles")), esc(bp.meta.Title), esc(bp.meta.Summary), len(bp.refs.Cites))
	}
	b.WriteString("</div>\n</section>\n")
	return b.String()
}

// placeholderIndex is the overview of a run that has not written one yet.
func (w *wiki) placeholderIndex() string {
	var b strings.Builder
	b.WriteString("<article class=\"page\">\n" + w.hero())
	fmt.Fprintf(&b, "<aside class=\"callout note\"><span class=\"callout-kind\">in progress</span><p>The overview is not written yet. The run is in its %s phase.</p></aside>\n", esc(w.report.Phase))
	b.WriteString(w.pageCards())
	b.WriteString("</article>\n")
	return b.String()
}

// snippetBlock renders a snippet where a page shows it.
func (w *wiki) snippetBlock(sn *Snippet) string {
	s := w.r.SourceByID(sn.Source)
	u, site := "", ""
	if s != nil {
		u, site = s.URL, s.Site
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<figure class=\"code snippet\" id=\"%s\"><figcaption><span class=\"proj\">%s</span><span class=\"lic\">%s</span>", esc(sn.ID), esc(sn.Project), esc(sn.License))
	if sn.Language != "" {
		fmt.Fprintf(&b, "<span class=\"lang\">%s</span>", esc(sn.Language))
	}
	if u != "" {
		fmt.Fprintf(&b, "<a class=\"ext\" href=\"%s\" rel=\"noopener noreferrer\" target=\"_blank\">%s</a>", esc(u), esc(site))
	}
	fmt.Fprintf(&b, "<a class=\"sid\" href=\"snippets.html#%s\">%s</a></figcaption>", esc(sn.ID), esc(sn.ID))
	fmt.Fprintf(&b, "<pre><code class=\"lang-%s\">%s</code></pre><p class=\"shows\">%s</p></figure>\n", esc(sn.Language), esc(sn.Code), esc(sn.Shows))
	return b.String()
}

// pageTitle is a written page's title, or its id.
func (w *wiki) pageTitle(id string) string {
	if bp := w.byID[id]; bp != nil {
		return bp.meta.Title
	}
	return id
}

// sourcesPage lists every source by kind, with its quotes, the pages that cite it and the findings it supports.
func (w *wiki) sourcesPage() string {
	r := w.r
	var b strings.Builder
	b.WriteString("<article class=\"page\">\n<h1>Sources</h1>\n")
	fmt.Fprintf(&b, "<p class=\"lede\">%d sources from %d sites. Each was cited by a researcher with passages copied from it, and the harness kept a passage only after finding it on the page.</p>\n",
		len(r.Sources), r.Domains())
	kinds := map[string][]Source{}
	for _, s := range r.Sources {
		kinds[s.Kind] = append(kinds[s.Kind], s)
	}
	b.WriteString("<p class=\"filters\">")
	for _, k := range SourceKinds {
		if n := len(kinds[k]); n > 0 {
			fmt.Fprintf(&b, "<a class=\"chip\" href=\"#kind-%s\">%s <b>%d</b></a>", k, k, n)
		}
	}
	b.WriteString("</p>\n")
	supports := map[string][]string{}
	for _, f := range r.Findings {
		for _, s := range f.Sources {
			supports[s] = append(supports[s], f.ID)
		}
	}
	for _, k := range SourceKinds {
		list := kinds[k]
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "<h2 id=\"kind-%s\">%s</h2>\n<ol class=\"sources\">\n", k, esc(strings.ToUpper(k[:1])+k[1:]))
		for _, s := range list {
			fmt.Fprintf(&b, "<li id=\"%s\"><div class=\"src-head\"><span class=\"sid\">%s</span><a class=\"ext\" href=\"%s\" rel=\"noopener noreferrer\" target=\"_blank\">%s</a><span class=\"site\">%s</span>",
				esc(s.ID), esc(s.ID), esc(s.URL), esc(s.Title), esc(s.Site))
			if s.Published != "" {
				fmt.Fprintf(&b, "<span class=\"kind\">%s</span>", esc(s.Published))
			}
			b.WriteString("</div>\n")
			for _, q := range s.Quotes {
				fmt.Fprintf(&b, "<blockquote>%s</blockquote>\n", esc(q.Text))
			}
			var meta []string
			if on := w.citedOn[s.ID]; len(on) > 0 {
				var ls []string
				for _, p := range on {
					ls = append(ls, fmt.Sprintf("<a href=\"%s.html#ref-%s\">%s</a>", esc(p), esc(s.ID), esc(w.pageTitle(p))))
				}
				meta = append(meta, "Cited on "+strings.Join(ls, ", "))
			}
			if fs := supports[s.ID]; len(fs) > 0 {
				var ls []string
				for _, f := range fs {
					ls = append(ls, fmt.Sprintf("<a href=\"evidence.html#%s\">%s</a>", esc(f), esc(f)))
				}
				meta = append(meta, "Supports "+strings.Join(ls, ", "))
			}
			if len(meta) > 0 {
				b.WriteString("<p class=\"src-meta\">" + strings.Join(meta, " · ") + "</p>\n")
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ol>\n")
	}
	b.WriteString("</article>\n")
	return b.String()
}

// evidencePage lists every finding by track, with its confidence, its sources and what it contradicts.
func (w *wiki) evidencePage() string {
	r := w.r
	var b strings.Builder
	b.WriteString("<article class=\"page\">\n<h1>Evidence</h1>\n")
	fmt.Fprintf(&b, "<p class=\"lede\">The %d findings the pages are written from, each with the sources that support it. Where findings disagree, both are kept and marked.</p>\n", len(r.Findings))
	groups := []struct{ id, title string }{}
	if r.Plan != nil {
		for _, t := range r.Plan.Tracks {
			groups = append(groups, struct{ id, title string }{t.ID, t.Title})
		}
	}
	groups = append(groups, struct{ id, title string }{"", "Other findings"})
	for _, g := range groups {
		var list []Finding
		for _, f := range r.Findings {
			if f.Track == g.id || g.id == "" && r.TrackByID(f.Track) == nil {
				list = append(list, f)
			}
		}
		if len(list) == 0 {
			continue
		}
		anchor := "other"
		if g.id != "" {
			anchor = strings.ToLower(g.id)
		}
		label := g.title
		if g.id != "" {
			label = g.id + " · " + g.title
		}
		fmt.Fprintf(&b, "<h2 id=\"track-%s\">%s</h2>\n<ul class=\"findings\">\n", esc(anchor), esc(label))
		for _, f := range list {
			fmt.Fprintf(&b, "<li id=\"%s\"><span class=\"fid\">%s</span><span class=\"conf %s\">%s</span><p>%s</p><p class=\"src-meta\">",
				esc(f.ID), esc(f.ID), esc(f.Confidence), esc(f.Confidence), esc(f.Claim))
			var ls []string
			for _, s := range f.Sources {
				if src := r.SourceByID(s); src != nil {
					ls = append(ls, fmt.Sprintf("<a href=\"sources.html#%s\" data-src=\"%s\">%s</a> %s", esc(s), esc(s), esc(s), esc(src.Site)))
				}
			}
			b.WriteString(strings.Join(ls, " · "))
			if len(f.Contradicts) > 0 {
				var cs []string
				for _, c := range f.Contradicts {
					cs = append(cs, fmt.Sprintf("<a href=\"#%s\">%s</a>", esc(c), esc(c)))
				}
				b.WriteString(" <span class=\"contra\">disagrees with " + strings.Join(cs, ", ") + "</span>")
			}
			b.WriteString("</p></li>\n")
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("</article>\n")
	return b.String()
}

// gapsPage lists what the research left open, and how the closed gaps were answered.
func (w *wiki) gapsPage() string {
	r := w.r
	var b strings.Builder
	b.WriteString("<article class=\"page\">\n<h1>Open questions</h1>\n")
	b.WriteString("<p class=\"lede\">What each gap review found missing or contested, and how it was answered. A question the sources could not settle stays here with the reason.</p>\n")
	if len(r.Gaps) == 0 {
		b.WriteString("<p class=\"dim\">No gaps were recorded.</p>\n")
	}
	b.WriteString("<ul class=\"findings\">\n")
	for _, g := range r.Gaps {
		state := "open"
		if !g.Open {
			state = "answered"
			if len(g.Findings) == 0 {
				state = "unanswerable"
			}
		}
		fmt.Fprintf(&b, "<li id=\"%s\"><span class=\"fid\">%s</span><span class=\"conf %s\">%s</span><p>%s</p><p class=\"src-meta\">round %d",
			esc(g.ID), esc(g.ID), state, state, esc(g.Text), g.Round)
		for _, f := range g.Findings {
			fmt.Fprintf(&b, " · <a href=\"evidence.html#%s\">%s</a>", esc(f), esc(f))
		}
		if g.Resolution != "" {
			b.WriteString(" · " + esc(g.Resolution))
		}
		b.WriteString("</p></li>\n")
	}
	b.WriteString("</ul>\n</article>\n")
	return b.String()
}

// snippetsPage shows every snippet.
func (w *wiki) snippetsPage() string {
	r := w.r
	var b strings.Builder
	b.WriteString("<article class=\"page\">\n<h1>Code snippets</h1>\n")
	b.WriteString("<p class=\"lede\">Code from mature projects worth learning from, each found on its page by the harness. Mind each project's license before reusing any of it.</p>\n")
	if len(r.Snippets) == 0 {
		b.WriteString("<p class=\"dim\">No snippet was recorded.</p>\n")
	}
	for i := range r.Snippets {
		b.WriteString(w.snippetBlock(&r.Snippets[i]))
	}
	b.WriteString("</article>\n")
	return b.String()
}

// methodPage says how the wiki was made: the depth, the plan, the rounds, the reviews, the searches and the agents.
func (w *wiki) methodPage() string {
	r := w.r
	d := r.DepthInfo()
	c := w.report.Counts
	var b strings.Builder
	b.WriteString("<article class=\"page\">\n<h1>Method</h1>\n")
	fmt.Fprintf(&b, "<p class=\"lede\">How this wiki was researched: %s.</p>\n", esc(d.Summary))
	b.WriteString("<div class=\"table\"><table>\n<thead><tr><th>Measure</th><th style=\"text-align:right\">This run</th><th style=\"text-align:right\">Bar</th></tr></thead>\n<tbody>\n")
	row := func(label string, got, bar int) {
		cls := "ok"
		if got < bar {
			cls = "warn"
		}
		fmt.Fprintf(&b, "<tr><td>%s</td><td class=\"%s\" style=\"text-align:right\">%d</td><td style=\"text-align:right\">%d</td></tr>\n", esc(label), cls, got, bar)
	}
	tracks, persp := 0, 0
	if r.Plan != nil {
		tracks, persp = len(r.Plan.Tracks), len(r.Plan.Perspectives)
	}
	row("Perspectives", persp, d.Perspectives)
	row("Research tracks", tracks, d.Tracks)
	row("Gather rounds", min(len(r.Reviews)+1, d.Rounds), d.Rounds)
	row("Verified sources", c.Sources, d.MinSources)
	row("Distinct sites", c.Domains, d.MinDomains)
	row("Findings", c.Findings, d.MinFindings)
	row("Pages besides the overview", max(0, c.Pages-boolInt(r.PageByID("index") != nil)), d.MinPages)
	row("Pages fact-checked", c.Reviewed, c.Pages)
	if r.Mode == ModeKickoff {
		row("Code snippets", c.Snippets, d.MinSnippets)
		row("Projects studied", c.Projects, d.MinProjects)
	}
	b.WriteString("</tbody></table></div>\n")
	if r.Plan != nil {
		if r.Plan.Scope != "" {
			fmt.Fprintf(&b, "<h2 id=\"scope\">Scope</h2>\n<p>%s</p>\n", esc(r.Plan.Scope))
		}
		if len(r.Plan.Perspectives) > 0 {
			b.WriteString("<h2 id=\"perspectives\">Perspectives</h2>\n<ul>\n")
			for _, p := range r.Plan.Perspectives {
				fmt.Fprintf(&b, "<li><strong>%s</strong>: %s</li>\n", esc(p.Name), esc(p.Concerns))
			}
			b.WriteString("</ul>\n")
		}
		b.WriteString("<h2 id=\"tracks\">Research tracks</h2>\n")
		for _, t := range r.Plan.Tracks {
			fmt.Fprintf(&b, "<h3 id=\"%s\">%s · %s</h3>\n<ul>\n", esc(strings.ToLower(t.ID)), esc(t.ID), esc(t.Title))
			for _, q := range t.Questions {
				fmt.Fprintf(&b, "<li>%s</li>\n", esc(q))
			}
			b.WriteString("</ul>\n")
			if t.Look != "" {
				fmt.Fprintf(&b, "<p class=\"dim\">Looked in: %s</p>\n", esc(t.Look))
			}
		}
	}
	if len(r.Reviews) > 0 {
		b.WriteString("<h2 id=\"rounds\">Gap reviews</h2>\n<ol>\n")
		for _, rv := range r.Reviews {
			fmt.Fprintf(&b, "<li><strong>Round %d</strong>, reviewed by %s: %s", rv.Round, esc(rv.By), esc(rv.Summary))
			if len(rv.Gaps) > 0 {
				b.WriteString(" Gaps: ")
				var ls []string
				for _, g := range rv.Gaps {
					ls = append(ls, fmt.Sprintf("<a href=\"gaps.html#%s\">%s</a>", esc(g), esc(g)))
				}
				b.WriteString(strings.Join(ls, ", "))
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ol>\n")
	}
	b.WriteString("<h2 id=\"fact-checks\">Fact-checks</h2>\n<ul>\n")
	for _, bp := range w.pages {
		var who []string
		for _, ck := range bp.meta.Checks {
			v := "passed"
			if !ck.OK {
				v = "found " + strconv.Itoa(len(ck.Issues)) + " issues"
			}
			who = append(who, fmt.Sprintf("%s %s revision %d", esc(ck.By), v, ck.Rev))
		}
		if len(who) == 0 {
			who = []string{"not reviewed"}
		}
		fmt.Fprintf(&b, "<li><a href=\"%s.html\">%s</a> (written by %s): %s</li>\n", esc(bp.meta.ID), esc(bp.meta.Title), esc(strings.Join(bp.meta.Writers, ", ")), strings.Join(who, "; "))
	}
	b.WriteString("</ul>\n")
	agents := map[string]int{}
	for _, s := range r.Sources {
		for _, a := range s.By {
			agents[a]++
		}
	}
	if len(agents) > 0 {
		var names []string
		for a := range agents {
			names = append(names, a)
		}
		sort.Strings(names)
		b.WriteString("<h2 id=\"agents\">Researchers</h2>\n<p>")
		var ls []string
		for _, a := range names {
			ls = append(ls, fmt.Sprintf("%s (%d sources)", esc(a), agents[a]))
		}
		b.WriteString(strings.Join(ls, " · ") + "</p>\n")
	}
	if len(r.Queries) > 0 {
		fmt.Fprintf(&b, "<h2 id=\"searches\">Searches</h2>\n<p class=\"dim\">%d searches, each made once.</p>\n<ol class=\"queries\">\n", len(r.Queries))
		for _, q := range r.Queries {
			fmt.Fprintf(&b, "<li><code>%s</code> <span class=\"site\">%s · %d results · %s</span></li>\n", esc(q.Text), esc(q.In), q.Count, esc(q.By))
		}
		b.WriteString("</ol>\n")
	}
	if n := len(w.report.Problems); n > 0 {
		fmt.Fprintf(&b, "<h2 id=\"open\">What the checks still report</h2>\n<p class=\"dim\">The run was in its %s phase when this was built.</p>\n<ul>\n", esc(w.report.Phase))
		for i, p := range w.report.Problems {
			if i == 40 {
				fmt.Fprintf(&b, "<li>and %d more</li>\n", n-40)
				break
			}
			fmt.Fprintf(&b, "<li>%s</li>\n", esc(p.Text))
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("</article>\n")
	return b.String()
}

// boolInt is 1 for true.
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// searchIndex writes assets/search-index.js: every page, source and finding, and the sources' first quotes for the citation
// pop-ups. It is a script rather than JSON so the wiki searches when it is opened from disk.
func (w *wiki) searchIndex() error {
	type entry struct {
		T string `json:"t"`
		U string `json:"u"`
		K string `json:"k"`
		S string `json:"s,omitempty"`
		X string `json:"x,omitempty"`
	}
	type src struct {
		T string `json:"t"`
		U string `json:"u"`
		H string `json:"h"`
		K string `json:"k"`
		Q string `json:"q,omitempty"`
	}
	var es []entry
	for _, bp := range w.pages {
		es = append(es, entry{T: bp.meta.Title, U: bp.meta.ID + ".html", K: firstNonEmpty(bp.meta.Category, "Page"), S: bp.meta.Summary, X: clipText(bp.text, 6000)})
		for _, h := range bp.refs.Headings {
			es = append(es, entry{T: h.Text, U: bp.meta.ID + ".html#" + h.ID, K: "Section of " + bp.meta.Title})
		}
	}
	srcs := map[string]src{}
	for _, s := range w.r.Sources {
		q := ""
		var all []string
		for i, qt := range s.Quotes {
			if i == 0 {
				q = qt.Text
			}
			all = append(all, qt.Text)
		}
		es = append(es, entry{T: s.Title, U: "sources.html#" + s.ID, K: "Source · " + s.Site, X: clipText(strings.Join(all, " "), 1500)})
		srcs[s.ID] = src{T: s.Title, U: s.URL, H: s.Site, K: s.Kind, Q: q}
	}
	fids := map[string]string{}
	for _, f := range w.r.Findings {
		es = append(es, entry{T: clipText(f.Claim, 140), U: "evidence.html#" + f.ID, K: "Finding " + f.ID, X: f.Claim})
		fids[f.ID] = f.Claim
	}
	for _, sn := range w.r.Snippets {
		es = append(es, entry{T: sn.Project + ": " + clipText(sn.Shows, 100), U: "snippets.html#" + sn.ID, K: "Snippet · " + sn.License, X: sn.Shows})
	}
	w.indexed = len(es)
	data, err := json.Marshal(map[string]any{"entries": es, "sources": srcs, "findings": fids})
	if err != nil {
		return err
	}
	// "</" never appears in a script's JSON: a page title holding "</script>" stays text
	js := strings.ReplaceAll(string(data), "</", "<\\/")
	return w.file("assets/search-index.js", "window.WIKI="+js+";\n")
}
