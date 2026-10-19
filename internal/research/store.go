package research

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Run is one research run as run.json holds it. Its slices keep the order things were added in, and ids are never reused, so
// the file and every listing made from it are the same bytes for the same history.
type Run struct {
	Slug     string `json:"slug"`
	Question string `json:"question"`
	Depth    string `json:"depth"`
	Mode     string `json:"mode"`
	// Project is a kickoff run's project description (the goal the research prepares).
	Project string `json:"project,omitempty"`
	Created string `json:"created,omitempty"`

	Plan     *Plan     `json:"plan,omitempty"`
	Sources  []Source  `json:"sources"`
	Findings []Finding `json:"findings"`
	Snippets []Snippet `json:"snippets"`
	Gaps     []Gap     `json:"gaps"`
	// Reviews are the gap reviews that closed gather rounds, in order; len(Reviews)+1 is the current round.
	Reviews []RoundReview `json:"reviews"`
	Pages   []PageMeta    `json:"pages"`
	Queries []Query       `json:"queries"`
	// Built is what the last build saw: the ledger's version then, and its problems.
	Built *BuildMark `json:"built,omitempty"`
	// Version counts the ledger's changes.
	Version int `json:"version"`
}

// Plan is the run's plan: the perspectives the question is seen from, the tracks the researchers work, and the wiki's pages.
type Plan struct {
	Scope        string        `json:"scope"`
	Perspectives []Perspective `json:"perspectives"`
	Tracks       []Track       `json:"tracks"`
	Pages        []PlanPage    `json:"pages"`
	By           string        `json:"by,omitempty"`
}

// Perspective is a point of view the question is researched from, and what it asks.
type Perspective struct {
	Name     string `json:"name"`
	Concerns string `json:"concerns"`
}

// Track is a research track: one researcher's share of the questions, with the kinds of sources to look in.
type Track struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Questions []string `json:"questions"`
	Look      string   `json:"look,omitempty"`
}

// PlanPage is a planned wiki page.
type PlanPage struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Category string   `json:"category,omitempty"`
	Tracks   []string `json:"tracks,omitempty"`
}

// Source is a page an agent cited, with the passages the harness found on it.
type Source struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Site  string `json:"site"`
	// Kind is what the source is: one of SourceKinds.
	Kind      string   `json:"kind"`
	Published string   `json:"published,omitempty"`
	Quotes    []Quote  `json:"quotes"`
	Tracks    []string `json:"tracks,omitempty"`
	By        []string `json:"by,omitempty"`
}

// Quote is a passage found on its source's page.
type Quote struct {
	Text string `json:"text"`
	By   string `json:"by,omitempty"`
}

// SourceKinds are the kinds a source can be.
var SourceKinds = []string{"docs", "spec", "paper", "repo", "code", "reference", "news", "blog", "forum", "talk", "data", "other"}

// Finding is one claim the research established, with the sources that support it.
type Finding struct {
	ID         string   `json:"id"`
	Claim      string   `json:"claim"`
	Sources    []string `json:"sources"`
	Track      string   `json:"track,omitempty"`
	Confidence string   `json:"confidence"`
	// Contradicts names findings this one disagrees with.
	Contradicts []string `json:"contradicts,omitempty"`
	By          string   `json:"by,omitempty"`
}

// Snippet is code from a mature project, found on its source's page, with what it shows and the license it is under.
type Snippet struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Project  string `json:"project"`
	Language string `json:"language"`
	License  string `json:"license"`
	Shows    string `json:"shows"`
	Code     string `json:"code"`
	By       string `json:"by,omitempty"`
}

// Gap is something the research has not answered yet; it is closed by findings that answer it, or as unanswerable with a reason.
type Gap struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	Track      string   `json:"track,omitempty"`
	Round      int      `json:"round"`
	Open       bool     `json:"open"`
	Findings   []string `json:"findings,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
}

// RoundReview closes a gather round: who reviewed it and what they found missing.
type RoundReview struct {
	Round   int      `json:"round"`
	By      string   `json:"by"`
	Summary string   `json:"summary"`
	Gaps    []string `json:"gaps"`
}

// PageMeta is a written page as the ledger knows it; the text is in pages/<id>.md.
type PageMeta struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Category string `json:"category,omitempty"`
	// Rev counts the page's writes; Writers are the agents that wrote it, the last one last.
	Rev     int          `json:"rev"`
	Writers []string     `json:"writers"`
	Checks  []PageReview `json:"checks,omitempty"`
}

// PageReview is a fact-check of one revision of a page.
type PageReview struct {
	Rev    int      `json:"rev"`
	By     string   `json:"by"`
	OK     bool     `json:"ok"`
	Issues []string `json:"issues,omitempty"`
}

// Query is a search the run made, so that the same search is not made twice.
type Query struct {
	Text  string `json:"text"`
	In    string `json:"in"`
	By    string `json:"by,omitempty"`
	Count int    `json:"count"`
}

// BuildMark is what the last build of the wiki saw.
type BuildMark struct {
	Version  int `json:"version"`
	Problems int `json:"problems"`
	Warnings int `json:"warnings"`
	Pages    int `json:"pages"`
}

// Dir is where the runs of a project live.
func Dir(root string) string { return filepath.Join(root, ".sleipnir", "research") }

// RunDir is the directory of one run.
func RunDir(root, slug string) string { return filepath.Join(Dir(root), slug) }

// SiteDir is where a run's wiki is built.
func SiteDir(root, slug string) string { return filepath.Join(RunDir(root, slug), "site") }

// BriefPath is the run's brief: the spec of the goal that drives it, relative to the project root.
func BriefPath(slug string) string {
	return filepath.ToSlash(filepath.Join(".sleipnir", "research", slug, "BRIEF.md"))
}

// Store is the runs of one project, loaded on demand and written back after every change. It is safe for concurrent use; one
// session holds one Store, so every agent of a team writes the same ledger.
type Store struct {
	root string
	mu   sync.Mutex
	runs map[string]*Run
	// active is the run a call that names none means: the newest one created or opened by this store.
	active string
	now    func() time.Time
}

// NewStore returns the store of the runs under root.
func NewStore(root string) *Store {
	return &Store{root: root, runs: map[string]*Run{}, now: time.Now}
}

// Root is the project root the store keeps its runs under.
func (s *Store) Root() string { return s.root }

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidSlug reports whether slug can name a run.
func ValidSlug(slug string) bool { return slugRE.MatchString(slug) }

// Slugify makes a run slug out of a question: its first words, lower-case, joined by dashes.
func Slugify(text string) string {
	var words []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			words = append(words, b.String())
			b.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	stop := map[string]bool{"a": true, "an": true, "the": true, "of": true, "to": true, "in": true, "for": true, "and": true, "on": true,
		"is": true, "are": true, "what": true, "how": true, "do": true, "does": true, "with": true, "about": true, "me": true, "i": true}
	var kept []string
	for _, w := range words {
		if !stop[w] {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		kept = words
	}
	slug := ""
	for _, w := range kept {
		next := w
		if slug != "" {
			next = slug + "-" + w
		}
		if len(next) > 48 {
			break
		}
		slug = next
		if strings.Count(slug, "-") >= 5 {
			break
		}
	}
	if slug == "" {
		slug = "research"
	}
	return slug
}

// Create starts a run: a new slug (the question's, numbered when taken) and an empty ledger. depth must be one of Depths and mode
// ModeReport or ModeKickoff.
func (s *Store) Create(question, depth, mode, project string) (*Run, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, errors.New("a research run needs a question")
	}
	d, ok := DepthByID(depth)
	if !ok {
		return nil, errDepth(depth)
	}
	if mode == "" {
		mode = ModeReport
	}
	if mode != ModeReport && mode != ModeKickoff {
		return nil, fmt.Errorf("unknown mode %q: report or kickoff", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := Slugify(question)
	slug := base
	for n := 2; ; n++ {
		if _, err := os.Stat(RunDir(s.root, slug)); errors.Is(err, os.ErrNotExist) {
			break
		}
		slug = fmt.Sprintf("%s-%d", base, n)
	}
	r := &Run{Slug: slug, Question: question, Depth: d.ID, Mode: mode, Project: strings.TrimSpace(project),
		Created: s.now().UTC().Format(time.RFC3339)}
	if err := os.MkdirAll(filepath.Join(RunDir(s.root, slug), "pages"), 0o755); err != nil {
		return nil, err
	}
	if err := s.saveLocked(r); err != nil {
		return nil, err
	}
	s.runs[slug] = r
	s.active = slug
	return cloneRun(r), nil
}

// Open loads a run and makes it the active one. It returns a copy, like Create and Update: the store keeps its own.
func (s *Store) Open(slug string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadLocked(slug)
	if err != nil {
		return nil, err
	}
	s.active = slug
	return cloneRun(r), nil
}

// Snapshot is a copy of a run as it is now; unlike Open it leaves the active run as it was.
func (s *Store) Snapshot(slug string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadLocked(slug)
	if err != nil {
		return nil, err
	}
	return cloneRun(r), nil
}

// Active is the slug calls that name no run mean, or "".
func (s *Store) Active() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// loadLocked returns the run, reading it from disk the first time (s.mu held).
func (s *Store) loadLocked(slug string) (*Run, error) {
	if !ValidSlug(slug) {
		return nil, fmt.Errorf("no research run %q", slug)
	}
	if r, ok := s.runs[slug]; ok {
		return r, nil
	}
	r, err := Load(s.root, slug)
	if err != nil {
		return nil, err
	}
	s.runs[slug] = r
	return r, nil
}

// Load reads a run from disk, without a store.
func Load(root, slug string) (*Run, error) {
	if !ValidSlug(slug) {
		return nil, fmt.Errorf("no research run %q", slug)
	}
	b, err := os.ReadFile(filepath.Join(RunDir(root, slug), "run.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no research run %q", slug)
	}
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("research run %s: %w", slug, err)
	}
	if r.Slug != slug {
		return nil, fmt.Errorf("research run %s: run.json names %q", slug, r.Slug)
	}
	return &r, nil
}

// List reads every run of a project, newest first (by creation, then slug). Runs that cannot be read are left out.
func List(root string) []*Run {
	ents, err := os.ReadDir(Dir(root))
	if err != nil {
		return nil
	}
	var out []*Run
	for _, e := range ents {
		if !e.IsDir() || !ValidSlug(e.Name()) {
			continue
		}
		if r, err := Load(root, e.Name()); err == nil {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// Update runs fn on the run under the store's lock and writes the run back when fn succeeds. slug "" means the active run.
func (s *Store) Update(slug string, fn func(r *Run) error) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slug == "" {
		slug = s.active
	}
	if slug == "" {
		return nil, errors.New("no research run is open: name one with run, or start one with /research")
	}
	r, err := s.loadLocked(slug)
	if err != nil {
		return nil, err
	}
	before, _ := json.Marshal(r)
	if err := fn(r); err != nil {
		// fn may have changed r before it failed: put the run back as it was
		var back Run
		_ = json.Unmarshal(before, &back)
		*r = back
		return nil, err
	}
	r.Version++
	if err := s.saveLocked(r); err != nil {
		// nothing was written: the cached run must not keep a change the caller was told failed
		var back Run
		_ = json.Unmarshal(before, &back)
		*r = back
		return nil, err
	}
	return cloneRun(r), nil
}

// View runs fn on the run under the store's lock, changing nothing. slug "" means the active run.
func (s *Store) View(slug string, fn func(r *Run) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slug == "" {
		slug = s.active
	}
	if slug == "" {
		return errors.New("no research run is open: name one with run, or start one with /research")
	}
	r, err := s.loadLocked(slug)
	if err != nil {
		return err
	}
	return fn(r)
}

// saveLocked writes run.json atomically (s.mu held).
func (s *Store) saveLocked(r *Run) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(RunDir(s.root, r.Slug), "run.json"), append(b, '\n'))
}

// writeAtomic writes data to path through a temporary file in the same directory.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// PagePath is the Markdown file of a page.
func PagePath(root, slug, page string) string {
	return filepath.Join(RunDir(root, slug), "pages", page+".md")
}

// ReadPage returns the Markdown of a written page, or "" when it has none.
func ReadPage(root, slug, page string) string {
	if !ValidPageID(page) {
		return ""
	}
	b, err := os.ReadFile(PagePath(root, slug, page))
	if err != nil {
		return ""
	}
	return string(b)
}

var pageIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidPageID reports whether id can name a page: lower-case letters, digits and dashes.
func ValidPageID(id string) bool { return pageIDRE.MatchString(id) }

// Domains counts the distinct sites of the sources.
func (r *Run) Domains() int {
	seen := map[string]bool{}
	for _, s := range r.Sources {
		seen[s.Site] = true
	}
	return len(seen)
}

// SourceByID returns the source with that id.
func (r *Run) SourceByID(id string) *Source {
	for i := range r.Sources {
		if r.Sources[i].ID == id {
			return &r.Sources[i]
		}
	}
	return nil
}

// FindingByID returns the finding with that id.
func (r *Run) FindingByID(id string) *Finding {
	for i := range r.Findings {
		if r.Findings[i].ID == id {
			return &r.Findings[i]
		}
	}
	return nil
}

// SnippetByID returns the snippet with that id.
func (r *Run) SnippetByID(id string) *Snippet {
	for i := range r.Snippets {
		if r.Snippets[i].ID == id {
			return &r.Snippets[i]
		}
	}
	return nil
}

// PageByID returns the written page with that id.
func (r *Run) PageByID(id string) *PageMeta {
	for i := range r.Pages {
		if r.Pages[i].ID == id {
			return &r.Pages[i]
		}
	}
	return nil
}

// TrackByID returns the planned track with that id.
func (r *Run) TrackByID(id string) *Track {
	if r.Plan == nil {
		return nil
	}
	for i := range r.Plan.Tracks {
		if r.Plan.Tracks[i].ID == id {
			return &r.Plan.Tracks[i]
		}
	}
	return nil
}

// Round is the gather round in progress, from 1.
func (r *Run) Round() int { return len(r.Reviews) + 1 }

// DepthInfo is the run's depth.
func (r *Run) DepthInfo() Depth {
	d, ok := DepthByID(r.Depth)
	if !ok {
		d, _ = DepthByID(DefaultDepth)
	}
	return d
}

// addUnique appends v to list unless it is there.
func addUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
