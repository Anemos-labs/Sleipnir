package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	webtool "github.com/anemos-labs/sleipnir/internal/tools/web"
)

// fakeReader serves fixed pages and records what was asked.
type fakeReader struct {
	pages   map[string]*webtool.Page
	backend bool
	reads   []string
}

func (f *fakeReader) Read(_ context.Context, _ *tools.Env, u string) (*webtool.Page, error) {
	f.reads = append(f.reads, u)
	if c, err := Canonical(u); err == nil {
		u = c
	}
	if p, ok := f.pages[u]; ok {
		return p, nil
	}
	return nil, errors.New("HTTP 404")
}

func (f *fakeReader) Search(_ context.Context, _ *tools.Env, q string, n int) ([]webtool.SearchResult, bool, error) {
	if !f.backend {
		return nil, false, nil
	}
	return []webtool.SearchResult{{Title: "STORM", URL: "https://arxiv.org/abs/2402.14207", Snippet: "Assisting in writing Wikipedia-like articles"}}, true, nil
}

func (f *fakeReader) Backend() string {
	if f.backend {
		return "fake"
	}
	return ""
}

// call runs one action as agent.
func call(t *testing.T, tool *Tool, agent string, args map[string]any) (string, bool) {
	t.Helper()
	b, _ := json.Marshal(args)
	res, err := tool.Run(context.Background(), &tools.Call{Name: ToolName, Input: b, Env: &tools.Env{Agent: agent, Perm: perm.AllowAll{}}})
	if err != nil {
		t.Fatal(err)
	}
	return res.Text, res.IsError
}

// must runs an action that must succeed.
func must(t *testing.T, tool *Tool, agent string, args map[string]any) string {
	t.Helper()
	out, isErr := call(t, tool, agent, args)
	if isErr {
		t.Fatalf("%v: %s", args["action"], out)
	}
	return out
}

func newTestTool(t *testing.T, depth, mode string) (*Tool, *fakeReader, *Run, string) {
	t.Helper()
	root := t.TempDir()
	st := NewStore(root)
	r, err := st.Create("How do agent harnesses do deep research?", depth, mode, "")
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeReader{pages: map[string]*webtool.Page{}}
	return NewTool(st, fr), fr, r, root
}

func TestCiteKeepsOnlyQuotesOnThePage(t *testing.T) {
	tool, fr, _, _ := newTestTool(t, "quick", ModeReport)
	fr.pages["https://example.org/storm"] = &webtool.Page{Title: "STORM", Text: "STORM discovers diverse perspectives by surveying articles on similar topics and uses them to control the question asking process."}
	out, isErr := call(t, tool, "sc-1", map[string]any{"action": "cite", "url": "https://example.org/storm#x",
		"quotes": []string{"discovers diverse perspectives by surveying articles on similar topics", "STORM was trained end to end with reinforcement learning"}})
	if isErr || !strings.Contains(out, "S1:") || !strings.Contains(out, "1 quotes found") || !strings.Contains(out, "is not on the page") {
		t.Fatalf("cite: %s", out)
	}
	// a quote that is on no page refuses the source
	out, isErr = call(t, tool, "sc-1", map[string]any{"action": "cite", "url": "https://example.org/storm", "quotes": []string{"an invented sentence that the page never says at all"}})
	if !isErr || !strings.Contains(out, "no quote was found") {
		t.Fatalf("invented quote: %s", out)
	}
	// the same page under another spelling is the same source
	out = must(t, tool, "sc-2", map[string]any{"action": "cite", "url": "http://www.example.org/storm/?utm_source=x",
		"quotes": []string{"uses them to control the question asking process"}})
	if !strings.Contains(out, "S1:") || !strings.Contains(out, "2 in all") {
		t.Fatalf("dedup: %s", out)
	}
}

func TestNoteNeedsKnownSources(t *testing.T) {
	tool, _, _, _ := newTestTool(t, "quick", ModeReport)
	if out, isErr := call(t, tool, "sc-1", map[string]any{"action": "note", "claim": "STORM writes Wikipedia-like articles from scratch with citations", "sources": []string{"S4"}}); !isErr {
		t.Fatalf("a finding citing no source was kept: %s", out)
	}
}

func TestSearchRunsOnce(t *testing.T) {
	tool, fr, _, _ := newTestTool(t, "quick", ModeReport)
	fr.backend = true
	out := must(t, tool, "sc-1", map[string]any{"action": "search", "query": "storm wikipedia generation"})
	if !strings.Contains(out, "arxiv.org/abs/2402.14207") || strings.Contains(out, "gather bar") {
		t.Fatalf("search: %s", out)
	}
	if out, isErr := call(t, tool, "sc-2", map[string]any{"action": "search", "query": "STORM  Wikipedia generation"}); !isErr || !strings.Contains(out, "already made") {
		t.Fatalf("repeat: %s", out)
	}
}

func TestReviewerCannotReviewOwnPage(t *testing.T) {
	tool, fr, _, _ := newTestTool(t, "quick", ModeReport)
	fr.pages["https://example.org/a"] = &webtool.Page{Text: "Agents use about four times more tokens than chat interactions, and multi-agent systems about fifteen times more."}
	must(t, tool, "sc-1", map[string]any{"action": "cite", "url": "https://example.org/a", "quotes": []string{"multi-agent systems about fifteen times more"}})
	must(t, tool, "dc-1", map[string]any{"action": "write_page", "page": "cost", "title": "Cost", "body": strings.Repeat("Multi-agent research uses about fifteen times the tokens of a chat [S1]. ", 8)})
	if out, isErr := call(t, tool, "dc-1", map[string]any{"action": "review_page", "page": "cost", "ok": true}); !isErr || !strings.Contains(out, "you wrote page") {
		t.Fatalf("self-review: %s", out)
	}
	must(t, tool, "rv-1", map[string]any{"action": "review_page", "page": "cost", "ok": true})
}

// TestFullRun drives a quick run from plan to a published wiki and checks the site it builds.
func TestFullRun(t *testing.T) {
	tool, fr, r, root := newTestTool(t, "quick", ModeReport)
	d := r.DepthInfo()
	must(t, tool, "mgr", map[string]any{"action": "plan", "scope": "multi-agent deep research systems",
		"perspectives": []map[string]string{{"name": "builder", "concerns": "how to build one"}, {"name": "skeptic", "concerns": "where they fail"}, {"name": "user", "concerns": "what they cost"}},
		"tracks": []map[string]any{
			{"title": "Architectures", "questions": []string{"q1", "q2", "q3"}},
			{"title": "Citations", "questions": []string{"q4", "q5", "q6"}},
			{"title": "Costs", "questions": []string{"q7", "q8", "q9"}}},
		"pages": []map[string]any{{"id": "index", "title": "Deep research", "summary": "How harnesses research"},
			{"id": "architectures", "title": "Architectures", "summary": "a", "category": "Design"},
			{"id": "citations", "title": "Citations", "summary": "b", "category": "Design"},
			{"id": "failures", "title": "Failure modes", "summary": "d", "category": "Practice"}}})
	// sources across sites and tracks
	for i := 0; i < max(d.MinSources, 15); i++ { // the pages below cite S1 to S15
		u := "https://site" + string(rune('a'+i%10)) + ".example/p" + string(rune('a'+i))
		text := "Finding number " + string(rune('a'+i)) + " says research agents run searches in parallel and compress what they read into findings."
		fr.pages[u] = &webtool.Page{Title: "Page " + string(rune('A'+i)), Text: text}
		track := []string{"T1", "T2", "T3"}[i%3]
		must(t, tool, "sc-"+track, map[string]any{"action": "cite", "url": u, "track": track, "quotes": []string{text}})
	}
	for i := 0; i < d.MinFindings; i++ {
		track := []string{"T1", "T2", "T3"}[i%3]
		src := []string{"S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8", "S9", "S10", "S11", "S12", "S13", "S14", "S15"}[i%15]
		must(t, tool, "sc-"+track, map[string]any{"action": "note", "track": track, "sources": []string{src},
			"claim": "Research agents number " + string(rune('a'+i)) + " compress pages into findings before a writer uses them", "confidence": "high"})
	}
	out := must(t, tool, "mgr", map[string]any{"action": "status"})
	if !strings.Contains(out, "Phase: write") {
		t.Fatalf("after gathering: %s", out)
	}
	fr.backend = true // a search past the bars says that new sources no longer move the run on
	if out := must(t, tool, "sc-T1", map[string]any{"action": "search", "query": "one more angle on research agents"}); !strings.Contains(out, "Every gather bar of this run is met") {
		t.Fatalf("search after gathering: %s", out)
	}
	cites := func(ids ...string) string {
		var b strings.Builder
		for _, id := range ids {
			for k := 0; k < 3; k++ {
				b.WriteString("Parallel researchers compress what they read into dense findings with their sources, which keeps the writer's context small and the evidence traceable [" + id + "]. ")
			}
			b.WriteString("\n\n")
		}
		return b.String()
	}
	pages := map[string]string{
		"index":         "Deep research harnesses plan, fan out and verify. See [[architectures]], [[citations]] and [[failures|failure modes]].\n\n" + cites("S1", "S2", "S3"),
		"architectures": "## Orchestrator and workers\n\n" + cites("S1", "S2", "S3") + "\n\n## Outlines\n\n" + cites("S4") + "\n\n## Tokens\n\n" + cites("S9", "S10", "S11", "S12") + "\n\n| Tier | Agents |\n|---|---:|\n| quick | 3 |\n| deep | 10 |\n\nSee [[citations]] and [[failures]].",
		"citations":     "## Verified quotes\n\n" + cites("S5", "S6", "S7", "S8") + "\n\nSee [[architectures]] and [[failures]]. A finding: [F2].",
		"failures":      "## Hallucinated citations\n\n" + cites("S13", "S14", "S15", "S1") + "\n\n```go\nfmt.Println(\"<script>alert(1)</script>\")\n```\n\nSee [[citations]] and [[architectures]].",
	}
	for id, body := range pages {
		must(t, tool, "dc-1", map[string]any{"action": "write_page", "page": id, "body": body})
	}
	for id := range pages {
		if id != "failures" { // quick research has no fact-check round: a page nobody reviewed is published as written
			must(t, tool, "rv-1", map[string]any{"action": "review_page", "page": id, "ok": true})
		}
	}
	out = must(t, tool, "mgr", map[string]any{"action": "build"})
	if !strings.Contains(out, "No problems") {
		t.Fatalf("build: %s\n%s", out, must(t, tool, "mgr", map[string]any{"action": "check"}))
	}
	out = must(t, tool, "mgr", map[string]any{"action": "status"})
	if !strings.Contains(out, "Phase: done") {
		t.Fatalf("after build: %s", out)
	}
	if !strings.Contains(out, "fact-checked 3 ") { // failures was not reviewed: it is published, but not counted as fact-checked
		t.Errorf("status counts a page nobody reviewed as fact-checked: %s", out)
	}
	site := SiteDir(root, r.Slug)
	for _, f := range []string{"index.html", "architectures.html", "sources.html", "evidence.html", "method.html", "gaps.html", "assets/wiki.css", "assets/wiki.js", "assets/search-index.js"} {
		if _, err := os.Stat(filepath.Join(site, f)); err != nil {
			t.Errorf("site lacks %s", f)
		}
	}
	page, _ := os.ReadFile(filepath.Join(site, "failures.html"))
	if strings.Contains(string(page), "<script>alert") {
		t.Fatal("a page's code was not escaped")
	}
	if !strings.Contains(string(page), `href="citations.html"`) || !strings.Contains(string(page), `id="ref-S13"`) {
		t.Fatal("wiki links or references missing")
	}
	idx, _ := os.ReadFile(filepath.Join(site, "assets/search-index.js"))
	if strings.Contains(string(idx), "</script") {
		t.Fatal("the search index can close a script element")
	}
	// a change after the build makes the build stale
	must(t, tool, "sc-T1", map[string]any{"action": "note", "track": "T1", "sources": []string{"S1"}, "claim": "One more finding arrives after the wiki was published here"})
	if out := must(t, tool, "mgr", map[string]any{"action": "status"}); !strings.Contains(out, "Phase: publish") {
		t.Fatalf("stale build: %s", out)
	}
}

// Render serves only what the renderer makes: a file planted in the site directory is not part of it, and a page's raw HTML is
// escaped.
func TestRenderHoldsOnlyRenderedFiles(t *testing.T) {
	tool, fr, r, root := newTestTool(t, "quick", ModeReport)
	fr.pages["https://example.org/a"] = &webtool.Page{Text: "Agents use about four times more tokens than chat interactions, and multi-agent systems about fifteen times more."}
	must(t, tool, "sc-1", map[string]any{"action": "cite", "url": "https://example.org/a", "quotes": []string{"multi-agent systems about fifteen times more"}})
	must(t, tool, "dc-1", map[string]any{"action": "write_page", "page": "index", "title": "Cost", "body": strings.Repeat("Multi-agent research costs more tokens <script>alert(1)</script> [S1]. ", 8)})
	planted := filepath.Join(SiteDir(root, r.Slug), "assets", "evil.js")
	if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(planted, []byte("alert(1)"), 0o644)
	st := tool.store
	snap, err := st.Snapshot(r.Slug)
	if err != nil {
		t.Fatal(err)
	}
	site, err := Render(root, snap, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := site.Files["assets/evil.js"]; ok {
		t.Fatal("a file from the disk is part of the render")
	}
	for _, f := range []string{"index.html", "assets/wiki.js", "assets/wiki.css", "assets/search-index.js", "sources.html"} {
		if _, ok := site.Files[f]; !ok {
			t.Errorf("the render lacks %s", f)
		}
	}
	if strings.Contains(string(site.Files["index.html"]), "<script>alert") {
		t.Fatal("a page's HTML was not escaped")
	}
}

// A resumed session's store has no active run: a call that names none still reaches the project's only unfinished run, and
// names the runs to choose from when there are several.
func TestAResumedStoreFindsTheOnlyUnfinishedRun(t *testing.T) {
	_, fr, r, root := newTestTool(t, "quick", ModeKickoff)
	resumed := NewTool(NewStore(root), fr)
	if out := must(t, resumed, "dc-1", map[string]any{"action": "status"}); !strings.Contains(out, r.Slug) {
		t.Fatalf("status after a resume: %s", out)
	}
	if _, err := NewStore(root).Create("Another question entirely", "quick", ModeReport, ""); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, NewTool(NewStore(root), fr), "dc-1", map[string]any{"action": "status"})
	if !isErr || !strings.Contains(out, "2 are unfinished") || !strings.Contains(out, r.Slug) {
		t.Fatalf("two unfinished runs: %s", out)
	}
}

// run is never a reason to refuse: a call that leaves it out goes to the session's run, and one that names a run that does not exist
// (the question, the project, a page) goes there too, with a note to leave it out, instead of a refusal the agent retries.
func TestACallThatMisnamesTheRunStillLands(t *testing.T) {
	tool, fr, r, root := newTestTool(t, "quick", ModeReport)
	u := "https://example.org/a"
	fr.pages[u] = &webtool.Page{Text: "Agents use about four times more tokens than chat interactions, and multi-agent systems about fifteen times more."}
	out := must(t, tool, "sc-1", map[string]any{"action": "cite", "run": "How do agent harnesses do deep research?", "url": u, "quotes": []string{"Agents use about four times more tokens than chat interactions"}})
	if !strings.Contains(out, "there is no run") || !strings.Contains(out, r.Slug) || !strings.Contains(out, "S1") {
		t.Errorf("misnamed run: %s", out)
	}
	if out := must(t, tool, "sc-1", map[string]any{"action": "sources", "run": r.Slug}); strings.Contains(out, "there is no run") || !strings.Contains(out, "S1") {
		t.Errorf("named run: %s", out)
	}
	// a run that is there but cannot be read is an error, not a reason to write to another run's ledger
	bad := filepath.Join(RunDir(root, "damaged-run"), "run.json")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, isErr := call(t, tool, "sc-1", map[string]any{"action": "sources", "run": "damaged-run"}); !isErr || strings.Contains(out, "S1") {
		t.Errorf("damaged run: %v %s", isErr, out)
	}
	empty := NewTool(NewStore(t.TempDir()), fr)
	if out, isErr := call(t, empty, "sc-1", map[string]any{"action": "status", "run": "nope"}); !isErr || !strings.Contains(out, "no research run") {
		t.Errorf("no run at all: %v %s", isErr, out)
	}
}

// A plan that leaves out the overview gets it: a quick run whose manager trimmed its pages to the bounds and dropped index went
// back from gathering to planning.
func TestAPlanWithoutTheOverviewGetsIt(t *testing.T) {
	tool, _, r, root := newTestTool(t, "quick", ModeReport)
	out := must(t, tool, "mgr", map[string]any{"action": "plan", "scope": "s",
		"perspectives": []map[string]string{{"name": "a", "concerns": "x"}, {"name": "b", "concerns": "y"}},
		"tracks":       []map[string]any{{"title": "One", "questions": []string{"q1", "q2"}}, {"title": "Two", "questions": []string{"q3", "q4"}}, {"title": "Three", "questions": []string{"q5", "q6"}}},
		"pages":        []map[string]any{{"id": "stores", "title": "Stores", "summary": "s"}}})
	if !strings.Contains(out, "index was added") {
		t.Errorf("plan: %s", out)
	}
	run, err := Load(root, r.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Plan.Pages) != 2 || run.Plan.Pages[0].ID != "index" || run.Plan.Pages[0].Title != r.Question {
		t.Errorf("pages: %+v", run.Plan.Pages)
	}
	if strings.Contains(must(t, tool, "mgr", map[string]any{"action": "status"}), "Phase: plan") {
		t.Error("the run is still in the plan phase")
	}
}

// A runs directory that cannot be looked in fails Create at once: no other name would fare better.
func TestCreateFailsWhereTheRunsCannotBeListed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".sleipnir"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root).Create("A question", "quick", ModeReport, ""); err == nil {
		t.Fatal("Create under a file succeeded")
	}
}

// Create tries 10,000 names for a question and then says none is free, instead of looking forever.
func TestCreateStopsWhenEveryNameIsTaken(t *testing.T) {
	root := t.TempDir()
	base := Slugify("A question")
	for n := 1; n <= 10000; n++ {
		slug := base
		if n > 1 {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		if err := os.MkdirAll(RunDir(root, slug), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := NewStore(root).Create("A question", "quick", ModeReport, "")
	if err == nil || !strings.Contains(err.Error(), "no free name") {
		t.Fatalf("Create with every name taken: %v; want a refusal that no name is free", err)
	}
}

// A Deny rule for the research tool refuses a change to the ledger, and leaves reading it alone.
func TestDenyRuleRefusesLedgerChanges(t *testing.T) {
	tool, _, r, root := newTestTool(t, "quick", "")
	for _, mode := range []perm.Mode{perm.ModeDefault, perm.ModeBypass} {
		e, err := perm.NewEngine(perm.Config{Root: root, Home: t.TempDir(), Mode: mode, Deny: []string{ToolName}})
		if err != nil {
			t.Fatal(err)
		}
		run := func(args map[string]any) (*tools.Result, error) {
			b, _ := json.Marshal(args)
			return tool.Run(context.Background(), &tools.Call{Name: ToolName, Input: b, Env: &tools.Env{Agent: "a", Perm: e}})
		}
		for _, args := range []map[string]any{
			{"action": "note", "run": r.Slug, "text": "x", "sources": []string{"s1"}},
			{"action": "search", "run": r.Slug, "query": "q"}, // a search logs its query in the ledger
		} {
			res, err := run(args)
			if err != nil || !res.IsError || !strings.Contains(res.Text, "permission denied") {
				t.Fatalf("%s: a %s under a research Deny rule: %v %+v", mode, args["action"], err, res)
			}
		}
		if got, err := Load(root, r.Slug); err != nil || len(got.Queries) != 0 {
			t.Fatalf("%s: a refused search was logged: %v %+v", mode, err, got)
		}
		if res, err := run(map[string]any{"action": "status", "run": r.Slug}); err != nil || res.IsError {
			t.Fatalf("%s: status under a research Deny rule: %v %+v", mode, err, res)
		}
	}
	e, err := perm.NewEngine(perm.Config{Root: root, Home: t.TempDir(), Mode: perm.ModeDefault})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"action": "plan", "run": r.Slug, "scope": "s",
		"perspectives": []map[string]string{{"name": "a", "concerns": "x"}, {"name": "b", "concerns": "y"}},
		"tracks":       []map[string]any{{"title": "One", "questions": []string{"q1", "q2"}}, {"title": "Two", "questions": []string{"q3", "q4"}}, {"title": "Three", "questions": []string{"q5", "q6"}}},
		"pages":        []map[string]any{{"id": "stores", "title": "Stores", "summary": "s"}}})
	if res, err := tool.Run(context.Background(), &tools.Call{Name: ToolName, Input: b, Env: &tools.Env{Agent: "a", Perm: e}}); err != nil || res.IsError {
		t.Fatalf("a plan without a rule against research: %v %+v", err, res)
	}
	if got, err := Load(root, r.Slug); err != nil || got.Plan == nil {
		t.Fatalf("the allowed plan was not saved: %v %+v", err, got)
	}
}
