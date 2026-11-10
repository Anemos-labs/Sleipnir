package webtest

import (
	"encoding/json"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// TestMemoryAndPluginsPagesHaveContent: the canned Memory page has something in every card and the plugin catalogue lists the
// built-in plugins, so the two pages draw content on the fake server instead of a placeholder.
func TestMemoryAndPluginsPagesHaveContent(t *testing.T) {
	m := Memory()
	if len(m.Notes.Notes) == 0 || m.Project == nil || len(m.Project.Notes) == 0 || m.Lore == nil || len(m.Lore.Roles) == 0 ||
		len(m.Files) == 0 || len(m.Lessons.Lessons) == 0 {
		t.Fatalf("a card of the canned Memory page is empty: %+v", m)
	}
	b, err := json.Marshal(Plugins())
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Profile string `json:"profile"`
		Plugins []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Profile == "" || len(v.Plugins) == 0 {
		t.Fatalf("the canned plugin catalogue is empty: profile %q, %d plugins", v.Profile, len(v.Plugins))
	}
	for _, p := range v.Plugins {
		if p.ID == "" || p.Category == "" {
			t.Fatalf("a plugin row has no id or category: %+v", p)
		}
	}
}

// TestMemoryAndPluginsRoutes: a tab's Memory page has its project's memory, the page with no tab only the user's own, and a tab
// that does not exist answers 404 on both pages' routes.
func TestMemoryAndPluginsRoutes(t *testing.T) {
	r := StartT(t, Options{})
	var tab, user wire.MemoryView
	getJSON(t, r, "/api/sessions/shop/memory", &tab)
	if tab.Project == nil || tab.Lore == nil || tab.Root != Cwd {
		t.Errorf("the tab's memory has no project: %+v", tab)
	}
	getJSON(t, r, "/api/memory", &user)
	if user.Project != nil || user.Lore != nil || user.Root != "" || len(user.Notes.Notes) == 0 || len(user.Files) == 0 {
		t.Errorf("the memory with no tab is not the user's own only: %+v", user)
	}
	for _, f := range user.Files {
		if f.Scope != "user" {
			t.Errorf("the memory with no tab lists %s (scope %s)", f.Path, f.Scope)
		}
	}
	var cat map[string]json.RawMessage
	getJSON(t, r, "/api/sessions/shop/plugins", &cat)
	if len(cat["plugins"]) < 10 {
		t.Error("the tab's plugin catalogue is empty")
	}
	for _, path := range []string{"/api/sessions/nope/memory", "/api/sessions/nope/plugins"} {
		if code, b, _ := call(t, r, "GET", path, "", nil); code != 404 {
			t.Errorf("GET %s = %d %s, want 404", path, code, b)
		}
	}
}

func TestResearchRoute(t *testing.T) {
	r := StartT(t, Options{})
	var v ResearchView
	getJSON(t, r, "/api/sessions/shop/research", &v)
	if !v.On || len(v.Depths) == 0 || len(v.Runs) != 2 {
		t.Fatalf("the canned Research page has no runs or depths: %+v", v)
	}
	if !v.Runs[0].Active || v.Runs[0].Phase == "done" || v.Runs[1].Phase != "done" {
		t.Errorf("want one run in work and one published: %+v", v.Runs)
	}
	if code, b, _ := call(t, r, "GET", "/api/sessions/nope/research", "", nil); code != 404 {
		t.Errorf("GET research of a tab that does not exist = %d %s, want 404", code, b)
	}
}

func TestMergeRoutesAgreeWithTheCannedTeam(t *testing.T) {
	r := StartT(t, Options{})
	var trees struct {
		Worktrees []wire.Worktree `json:"worktrees"`
	}
	getJSON(t, r, "/api/sessions/shop/ws/worktrees", &trees)
	if len(trees.Worktrees) != 4 {
		t.Fatalf("want a tree per worker of the canned team, got %+v", trees.Worktrees)
	}
	var q wire.MergeQueueStatus
	getJSON(t, r, "/api/sessions/shop/ws/queue", &q)
	if !q.Healthy || len(q.Landed) != 1 || q.Landed[0].Task != "T1" || q.Waiting == nil {
		t.Errorf("the queue does not show the merged T1: %+v", q)
	}
	for _, path := range []string{"/api/sessions/nope/ws/worktrees", "/api/sessions/nope/ws/queue"} {
		if code, b, _ := call(t, r, "GET", path, "", nil); code != 404 {
			t.Errorf("GET %s = %d %s, want 404", path, code, b)
		}
	}
}

func TestScanRoutes(t *testing.T) {
	r := StartT(t, Options{})
	var v ScanView
	getJSON(t, r, "/api/sessions/shop/scan", &v)
	if !v.On || len(v.Depths) == 0 || len(v.Runs) != 1 || v.Runs[0].Slug != ScanSlug {
		t.Fatalf("the canned Deep scan page has no scan or depths: %+v", v)
	}
	run := v.Runs[0]
	if run.Shards != 4 || run.Mapped != 4 || run.Leads != 4 || run.Confirmed+run.Unproven != 2 || run.Rejected != 1 || run.Open != 1 {
		t.Errorf("the canned scan is not measured as built: %+v", run)
	}
	var d map[string]json.RawMessage
	getJSON(t, r, "/api/sessions/shop/scan/"+ScanSlug, &d)
	for _, k := range []string{"findings", "summary", "headline", "shards", "points", "edges", "counts", "phase"} {
		if len(d[k]) == 0 || string(d[k]) == "null" {
			t.Errorf("the scan's detail has no %s", k)
		}
	}
	for _, path := range []string{"/api/sessions/nope/scan", "/api/sessions/shop/scan/nope"} {
		if code, b, _ := call(t, r, "GET", path, "", nil); code != 404 {
			t.Errorf("GET %s = %d %s, want 404", path, code, b)
		}
	}
}

func TestModelsHaveSavedDefaults(t *testing.T) {
	m := Models()
	if m.Saved["default"].Ref == "" || m.SaveFile == "" {
		t.Errorf("the canned catalogue has no saved defaults, so the page says the configuration does not load: %+v", m.Saved)
	}
}

// TestWorkspaceCountsAgree checks that the Workspace's numbers are one story: a file's lines in the tree, in its diff since the
// start and in its text are the same number, and a checkpoint's counts are those of its files' diffs across it.
func TestWorkspaceCountsAgree(t *testing.T) {
	idx := WsIndex()
	for _, f := range idx.Tree {
		if f.Status == "unchanged" {
			continue
		}
		d, lines := WsDiff(f.Path, "base", "live"), len(wsLines(WsContent(f.Path, "live").Text))
		if f.Add != d.Added || f.Del != d.Removed || d.Added != lines {
			t.Errorf("%s: tree +%d -%d, diff since the start +%d -%d, %d lines of text", f.Path, f.Add, f.Del, d.Added, d.Removed, lines)
		}
	}
	prev := "base"
	for _, cp := range idx.Cps {
		add, del := 0, 0
		for _, ch := range cp.Changes {
			d := WsDiff(ch.Path, prev, cp.ID)
			if ch.Added != d.Added || ch.Removed != d.Removed {
				t.Errorf("%s in %s: the index says +%d -%d, the diff +%d -%d", ch.Path, cp.ID, ch.Added, ch.Removed, d.Added, d.Removed)
			}
			add, del = add+ch.Added, del+ch.Removed
		}
		if cp.Added != add || cp.Removed != del {
			t.Errorf("%s: +%d -%d, its files sum to +%d -%d", cp.ID, cp.Added, cp.Removed, add, del)
		}
		if !cp.Skipped {
			prev = cp.ID
		}
	}
	if d := WsDiff("web/cart/Cart.tsx", "c01", "live"); d.Added != 0 || len(d.Hunks) != 0 {
		t.Errorf("Cart.tsx did not change after c01, the diff says +%d with %d hunks", d.Added, len(d.Hunks))
	}
}

// TestPrunePlanFollowsItsRequest checks that the fake prune preview lists only the sessions older than the age and past the
// newest Keep, as the real route does, and that it refuses an age it cannot read.
func TestPrunePlanFollowsItsRequest(t *testing.T) {
	r := StartT(t, Options{})
	plan := func(body string) wire.PrunePlan {
		t.Helper()
		code, b, _ := call(t, r, "POST", "/api/recorded/prune", body, nil)
		if code != 200 {
			t.Fatalf("prune %s = %d %s", body, code, b)
		}
		var p wire.PrunePlan
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := plan(`{"olderThan":"30d","keep":20}`); len(p.List) != 0 || p.MB != 0 {
		t.Errorf("no canned recording is 30 days old, the plan lists %d (%.1f MB)", len(p.List), p.MB)
	}
	if p := plan(`{"olderThan":"36h","keep":0}`); len(p.List) != 2 {
		t.Errorf("two canned recordings are older than 36 hours, the plan lists %d", len(p.List))
	}
	if p := plan(`{"olderThan":"1h","keep":2}`); len(p.List) != 1 {
		t.Errorf("past the newest two, one recording is left to delete, the plan lists %d", len(p.List))
	}
	if code, b, _ := call(t, r, "POST", "/api/recorded/prune", `{"olderThan":"soon"}`, nil); code != 400 || errorCode(b) != "bad_age" {
		t.Errorf("an age that is not one = %d %s, want 400 bad_age", code, b)
	}
}

// TestACannedProjectIsAFolderThatExists checks that the folder check says a canned project exists, though this disk has no such path.
func TestACannedProjectIsAFolderThatExists(t *testing.T) {
	r := StartT(t, Options{})
	var l struct {
		State wire.DirState `json:"state"`
	}
	code, body, _ := call(t, r, "GET", "/api/fs/dirs?path="+Cwd, "", nil)
	if err := json.Unmarshal(body, &l); code != 200 || err != nil {
		t.Fatalf("GET the folder = %d %s (%v)", code, body, err)
	}
	if s := l.State; !s.Known || !s.Exists || !s.Dir {
		t.Errorf("the canned project folder %s: %+v", Cwd, s)
	}
}
