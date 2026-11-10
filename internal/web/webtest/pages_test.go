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
