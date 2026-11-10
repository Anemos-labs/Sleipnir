package webtest

import (
	"encoding/json"
	"testing"
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
